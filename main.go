package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"

	"nilix/internal/api"
	"nilix/internal/autostart"
	"nilix/internal/backend"
	"nilix/internal/config"
	"nilix/internal/island"
	"nilix/internal/kb_work"
	"nilix/internal/render"
	"nilix/internal/sysmon"
	"nilix/internal/watchdog"
)

//go:embed web/index.html
var indexHTML string

//go:embed icon.ico
var iconICO []byte

//go:embed web/kb
var kbFS embed.FS

//go:embed web/island
var islandFS embed.FS

// ---- 桌面主窗口:双击 exe 即在应用窗口内管理 ----
// 用 Edge App 模式(msedge --app=URL)开独立应用窗口:无边栏地址栏、独立任务栏项,
// 观感等同桌面应用。灵动岛保持进程内 WebView2(WebView2 同进程只允许一个 environment,
// 第二个会创建卡死——实测结论,故主窗口走 Edge App 进程,互不冲突)。
// 关闭窗口 = 驻留托盘(渲染/续写任务不中断),托盘菜单或再次双击可唤起;托盘「退出」才真正退出。

const mainWinTitle = "NiliX" // 管理主窗口标题(Edge App 窗口=页面 title,恒为 NiliX;灵动岛为 NiliX HUD 区分)

var (
	user32Lazy           = syscall.NewLazyDLL("user32.dll")
	procWinShow          = user32Lazy.NewProc("ShowWindow")
	procWinSetForeground = user32Lazy.NewProc("SetForegroundWindow")
	procWinFind          = user32Lazy.NewProc("FindWindowW")
	procGetSysMetrics    = user32Lazy.NewProc("GetSystemMetrics")
	procSetWindowPos     = user32Lazy.NewProc("SetWindowPos")
	procWinClose         = user32Lazy.NewProc("PostMessageW")
)

// msedgePath 定位 Edge 浏览器(系统自带;WebView2 运行时本就依赖同一 Edge)
func msedgePath() string {
	for _, p := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		filepath.Join(os.Getenv("LocalAppData"), `Microsoft\Edge\Application\msedge.exe`),
	} {
		if fileOK(p) {
			return p
		}
	}
	return ""
}

func fileOK(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// calcMainWinSize 按屏幕工作区计算 16:9 窗口尺寸与居中坐标(宽=工作区85%≤1600;高受限反向缩宽)
func calcMainWinSize() (x, y, w, h int) {
	sw, _, _ := procGetSysMetrics.Call(16) // SM_CXFULLSCREEN(工作区宽)
	sh, _, _ := procGetSysMetrics.Call(17) // SM_CYFULLSCREEN(工作区高)
	if sw == 0 || sh == 0 {
		sw, sh = 1920, 1040
	}
	w = int(float64(sw) * 0.85)
	if w > 1600 {
		w = 1600
	}
	h = w * 9 / 16
	if maxH := int(float64(sh) * 0.88); h > maxH {
		h = maxH
		w = h * 16 / 9
	}
	return (int(sw) - w) / 2, (int(sh) - h) / 2, w, h
}

// fitMainWindow 找到主窗口后强制 16:9 尺寸+居中(Edge App 的 --window-size 在复用已有
// Edge 进程时不生效,窗口按上次记忆尺寸打开——由本进程 SetWindowPos 兜底校正)
func fitMainWindow() {
	h := findMainWindow()
	if h == 0 {
		return
	}
	x, y, w, hgt := calcMainWinSize()
	_, _, _ = procSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(hgt), 0x0004) // SWP_NOZORDER
}

// runMainWindow 用 Edge App 模式打开管理窗口(独立应用窗口,开后校正 16:9)
func runMainWindow(url string) {
	edge := msedgePath()
	if edge == "" {
		log.Printf("主窗口: 未找到 Edge,回退系统浏览器")
		openBrowser(url)
		return
	}
	log.Printf("主窗口(Edge App)打开: %s", url)
	x, y, w, h := calcMainWinSize()
	cmd := exec.Command(edge, "--app="+url,
		fmt.Sprintf("--window-size=%d,%d", w, h),
		fmt.Sprintf("--window-position=%d,%d", x, y))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		log.Printf("主窗口启动失败: %v(回退系统浏览器)", err)
		openBrowser(url)
		return
	}
	// 窗口起来后兜底校正 16:9+居中(等 Edge 完成窗口初始化)
	go func() {
		time.Sleep(1800 * time.Millisecond)
		fitMainWindow()
	}()
}

var (
	procEnumWindows = user32Lazy.NewProc("EnumWindows")
	procGetTextW    = user32Lazy.NewProc("GetWindowTextW")
)

// findMainWindow 枚举顶层窗口按标题精确匹配管理主窗口(title 恒为 "NiliX";
// 灵动岛窗口标题为 "NiliX HUD" 已区分;不用 FindWindowW 因其对动态 title 前后缀不可靠)。
func findMainWindow() uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(h, l uintptr) uintptr {
		buf := make([]uint16, 128)
		n, _, _ := procGetTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 128)
		title := syscall.UTF16ToString(buf[:n])
		if title == mainWinTitle {
			found = h
			return 0 // 停止枚举
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	return found
}

// showMainWindow 唤起主窗口:已存在(含最小化)则恢复前置;没有则新开。
// 跨进程枚举窗口,兼容"再次双击 exe 唤起已运行实例的窗口"。
func showMainWindow(url string) {
	if h := findMainWindow(); h != 0 {
		procWinShow.Call(h, 9) // SW_RESTORE(最小化时恢复)
		procWinSetForeground.Call(h)
		return
	}
	runMainWindow(url)
}

func main() {
	// 切到可执行文件所在目录：开机自启(注册表 Run key)启动时工作目录可能是 System32，
	// 会导致相对路径(settings.json / logs / clips)读写到错误位置，进而配置丢失/日志落空。
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			_ = os.Chdir(dir)
		}
	}

	port := flag.String("port", "8787", "监听端口")
	cfgPath := flag.String("config", "settings.json", "设置文件路径")
	kbRoot := flag.String("kb", `C:\Mi\Ai\WorkBench\zhishiku`, "知识库根目录")
	flag.Parse()

	// 高 DPI 感知：必须在任何窗口（托盘/胶囊）创建前设置，否则窗口尺寸与圆角裁剪错乱。
	island.EnablePerMonitorDPI()

	// 以 windowsgui 方式运行时无控制台，日志写文件。
	if f, err := setupLogFile("logs/server.log"); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	// 单实例看门狗：防止重复启动。
	guard, err := watchdog.SingleInstance("NiliX")
	if err != nil {
		if errors.Is(err, watchdog.ErrAlreadyRunning) {
			watchdog.Alert("小说转视频服务", "NiliX 已在运行,已为你唤起管理窗口。")
			// Edge App 窗口是独立进程,不随本进程退出——已开则前置,已关则直接重开
			showMainWindow("http://127.0.0.1:8787")
		} else {
			log.Printf("单实例检查失败: %v", err)
		}
		return
	}
	defer guard.Release()

	store := config.NewStore(*cfgPath)
	cfg, err := store.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	// 首次加载时把明文 key 迁移为加密存储（幂等：已加密的跳过）。
	if err := store.Save(cfg); err != nil {
		log.Printf("迁移加密配置失败(忽略): %v", err)
	}
	// ComfyUI 启动参数单一数据源:settings.json → HUD 卡片 / Comfy 页面 / 实际启动命令共用。
	api.SetComfyParams(cfg.Render.ComfyURL, cfg.Paths.ComfyInput, cfg.Paths.ComfyOutput)
	// 智能体全局默认(settings.json agent 节 → 全项目共用)与全局设置读写入口。
	api.SetGlobalAgentCfg(cfg)
	api.SetManjuSettingsStore(store)

	// 渲染任务管理器（ComfyUI 客户端 + 本地产物目录）。
	outDir := "clips"
	renderMgr := render.NewManager(backend.NewComfyUIClient(cfg.Render.ComfyURL))
	sysmonCol := sysmon.NewCollector()
	kbStore := kb_work.NewStore(*kbRoot)
	kbSub, _ := fs.Sub(kbFS, "web/kb")
	islandSub, _ := fs.Sub(islandFS, "web/island")
	srv := api.NewServer(store, cfg, []byte(indexHTML), renderMgr, sysmonCol, kbStore, *kbRoot, kbSub, islandSub, outDir)
	addr := "127.0.0.1:" + *port
	url := "http://" + addr

	// HTTP 服务放后台 goroutine，托盘图标阻塞主流程。
	go func() {
		log.Printf("NiliX 已启动，控制台: %s", url)
		if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
			log.Printf("HTTP 服务退出: %v", err)
			systray.Quit()
		}
	}()

	// 桌面主窗口:启动即打开,双击 exe 直接在窗口内管理(关窗驻留托盘)
	go func() {
		time.Sleep(400 * time.Millisecond) // 等 HTTP listen 就绪
		showMainWindow(url)
	}()

	// 灵动岛悬浮胶囊（WebView2）
	go func() {
		time.Sleep(500 * time.Millisecond)
		actions := island.Actions{
			StartComfy: api.ComfyStart,
			StopComfy:  api.ComfyStop,
			OpenComfy:  func() { openBrowser(api.ComfyURL()) },
			OpenKB:     func() { showMainWindow(url) },
			StartZCode: startZCode,
			StopZCode:  stopZCode,
			StopBot:    stopBot,
			RestartBot: stopBot, // 同 stop：kill 后 ZCode 约 5s 自动重建接管
		}
		if err := island.Run(url+"/island/", func() { systray.Quit() }, actions); err != nil {
			log.Printf("灵动岛启动失败: %v", err)
		}
	}()

	systray.Run(onReady(url), onExit)
}

func onReady(url string) func() {
	return func() {
		systray.SetIcon(iconICO)
		systray.SetTitle("NiliX")
		systray.SetTooltip("NiliX")
		mApp := systray.AddMenuItem("NiliX", "打开 NiliX 工作台")
		mApp.SetIcon(iconICO) // 菜单项带应用图标
		mAuto := systray.AddMenuItemCheckbox("开机自启", "开机自动启动 NiliX", autostart.Enabled())
		mQuit := systray.AddMenuItem("结束应用", "关闭窗口与服务并退出")
		go func() {
			for {
				select {
				case <-mApp.ClickedCh:
					showMainWindow(url)
				case <-mAuto.ClickedCh:
					if mAuto.Checked() {
						if autostart.Disable() == nil {
							mAuto.Uncheck()
						}
					} else {
						if exe, err := os.Executable(); err == nil && autostart.Enable(exe) == nil {
							mAuto.Check()
						}
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}
}

// closeMainWindow 关闭管理主窗口(Edge App 独立进程,需发消息让其退出)
func closeMainWindow() {
	if h := findMainWindow(); h != 0 {
		_, _, _ = procWinClose.Call(h, 0x0010, 0, 0) // WM_CLOSE=0x10
	}
}

func onExit() {
	closeMainWindow() // 全退(灵动岛 X / 托盘结束应用):一并关闭管理主窗口
	log.Println("NiliX 已退出")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	} else {
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// startZCode 启动 ZCode 桌面端（已在运行则跳过）。
func startZCode() error {
	exe := `C:\Mi\Ai\ZCode\ZCode.exe`
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	if sysmon.ZCodeRunning() {
		return nil
	}
	return exec.Command(exe).Start()
}

// stopZCode 停止 ZCode 桌面端（结束全部 ZCode.exe 进程树）。
func stopZCode() error {
	if !sysmon.ZCodeRunning() {
		return errors.New("ZCode 未运行")
	}
	cmd := exec.Command("taskkill", "/IM", "ZCode.exe", "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// stopBot 停止 bot 运行时（kill 后 ZCode 约 5s 自动重建接管）。
func stopBot() error {
	pid := sysmon.BotPID()
	if pid == 0 {
		return errors.New("BOT 未运行")
	}
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func setupLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}
