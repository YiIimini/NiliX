package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
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
	webview "github.com/jchv/go-webview2"

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

// ---- 主窗口尺寸记忆(用户调整后持久化,下次启动直接加载;独立 json,避免动加密 settings) ----
const mainWinMemFile = "mainwin.json"

type mainWinMem struct {
	W, H int
	Max  bool
	Set  bool // 是否已记忆过
}

func loadMainWinMem() mainWinMem {
	var m mainWinMem
	b, err := os.ReadFile(mainWinMemFile)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	if m.W < 400 || m.H < 300 {
		m.Set = false
	}
	return m
}

func saveMainWinMem(m mainWinMem) {
	b, _ := json.Marshal(m)
	_ = os.WriteFile(mainWinMemFile, b, 0644)
}

var (
	user32Lazy           = syscall.NewLazyDLL("user32.dll")
	procWinShow          = user32Lazy.NewProc("ShowWindow")
	procWinSetForeground = user32Lazy.NewProc("SetForegroundWindow")
	procWinFind          = user32Lazy.NewProc("FindWindowW")
	procGetSysMetrics    = user32Lazy.NewProc("GetSystemMetrics")
	procSetWindowPos     = user32Lazy.NewProc("SetWindowPos")
	procWinClose         = user32Lazy.NewProc("PostMessageW")
	procGetWindowRect    = user32Lazy.NewProc("GetWindowRect")
	procLoadImageW       = user32Lazy.NewProc("LoadImageW")
	procSendMessageW     = user32Lazy.NewProc("SendMessageW")
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

// calcMainWinSize 窗口尺寸:有记忆用记忆(用户调过的尺寸),无记忆按 16:9 默认;
// 记忆尺寸超出当前屏幕时收进工作区,居中放置
func calcMainWinSize() (x, y, w, h int) {
	if m := loadMainWinMem(); m.Set {
		w, h = m.W, m.H
	} else {
		w, h = defaultMainWinSize()
	}
	sw, _, _ := procGetSysMetrics.Call(16)
	sh, _, _ := procGetSysMetrics.Call(17)
	if sw == 0 || sh == 0 {
		sw, sh = 1920, 1040
	}
	if w > int(sw)-20 {
		w = int(sw) - 20
	}
	if h > int(sh)-20 {
		h = int(sh) - 20
	}
	return (int(sw) - w) / 2, (int(sh) - h) / 2, w, h
}

// defaultMainWinSize 无记忆时的 16:9 默认(宽=工作区85%≤1600;高受限反向缩宽)
func defaultMainWinSize() (int, int) {
	sw, _, _ := procGetSysMetrics.Call(16)
	sh, _, _ := procGetSysMetrics.Call(17)
	if sw == 0 || sh == 0 {
		sw, sh = 1920, 1040
	}
	w := int(float64(sw) * 0.85)
	if w > 1600 {
		w = 1600
	}
	h := w * 9 / 16
	if maxH := int(float64(sh) * 0.88); h > maxH {
		h = maxH
		w = h * 16 / 9
	}
	return w, h
}

// winSizeMatches 判断窗口当前尺寸是否等于期望(±8px 容差)
func winSizeMatches(h uintptr, w, hgt int) bool {
	var r w32RECT
	procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	curW, curH := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if curW == 0 || curH == 0 {
		return false
	}
	return absInt(curW-w) <= 8 && absInt(curH-hgt) <= 8
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// w32RECT GetWindowRect 输出
type w32RECT struct{ Left, Top, Right, Bottom int32 }

// saveMainWindowSize 读取当前窗口尺寸并持久化(用户在 Edge 标题栏自行调整后记忆;
// 由 saveWinLoop 每 3s 检测一次,尺寸变化即落盘)
func saveMainWindowSize() {
	h := findMainWindow()
	if h == 0 {
		return
	}
	var r w32RECT
	procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	w, hgt := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w < 400 || hgt < 300 {
		return
	}
	if m := loadMainWinMem(); m.Set && m.W == w && m.H == hgt {
		return
	}
	saveMainWinMem(mainWinMem{W: w, H: hgt, Set: true})
}

// saveWinLoop 常驻轮询:记忆窗口尺寸(用户拖动/缩放后 3s 内落盘)。
// 启动先等 10s(fitMainWindow 校正完成后再开始,避免把 Edge 启动时的旧尺寸覆盖进记忆)。
func saveWinLoop() {
	time.Sleep(10 * time.Second)
	for {
		time.Sleep(3 * time.Second)
		saveMainWindowSize()
	}
}

// runMainWindow 用 Edge App 模式打开管理窗口(独立应用窗口)
// runMainWindow 以独立子进程(NiliX.exe --mainwin)打开管理窗口:
// - 子进程内 WebView2 环境与灵动岛(主进程)互不冲突(WebView2 限制的是同进程多环境)
// - 任务栏图标天然是 NiliX.exe 自己的图标(不再显示 Edge 图标)
// - 窗口由我们创建,尺寸/标题/位置全部可控,无 Edge 记忆/复用问题
// - 窗口关闭 → 子进程退出;主进程(托盘/灵动岛/后台)不受影响
func runMainWindow(url string) {
	exe, err := os.Executable()
	if err != nil {
		openBrowser(url)
		return
	}
	log.Printf("主窗口(子进程 WebView2)打开: %s", url)
	cmd := exec.Command(exe, "--mainwin")
	// 不能 HideWindow:子进程首个窗口(WebView2 主窗口)若被创建为隐藏,
	// WebView2 环境初始化会卡死(隐藏父窗口的历史教训);exe 为 windowsgui 无控制台,正常显示即可
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		log.Printf("主窗口启动失败: %v(回退系统浏览器)", err)
		openBrowser(url)
	}
}

// setMainWinIcon 加载内嵌 icon.ico 并设置窗口图标(左上角 + Alt-Tab + 任务栏小图标)
func setMainWinIcon(hwnd uintptr) {
	if hwnd == 0 || len(iconICO) == 0 {
		return
	}
	tmp := filepath.Join(os.TempDir(), "nilix_icon.ico")
	if err := os.WriteFile(tmp, iconICO, 0644); err != nil {
		return
	}
	defer os.Remove(tmp)
	p, err := syscall.UTF16PtrFromString(tmp)
	if err != nil {
		return
	}
	const lrLoadFromFile = 0x00000010
	big, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), 1 /*IMAGE_ICON*/, 32, 32, lrLoadFromFile)
	small, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), 1, 16, 16, lrLoadFromFile)
	const wmSetIcon = 0x0080
	if big != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, 1 /*ICON_BIG*/, big)
	}
	if small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, 0 /*ICON_SMALL*/, small)
	}
}

// runMainWindowWebView 子进程(--mainwin)入口:创建 WebView2 管理窗口并运行。
// 阻塞至窗口关闭(用户点 X)→ 子进程退出。
func runMainWindowWebView() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("主窗口异常退出: %v", r)
		}
	}()
	island.EnablePerMonitorDPI() // 子进程同样高 DPI 感知:GetSystemMetrics/SetWindowPos 用物理像素
	_, _, ww, wh := calcMainWinSize()
	// 创建即指定尺寸+居中:窗口第一帧就是正确尺寸,不做任何后续校正,杜绝"先小后大/闪退观感"
	w := webview.NewWithOptions(webview.WebViewOptions{
		WindowOptions: webview.WindowOptions{
			Title:  mainWinTitle,
			Width:  uint(ww),
			Height: uint(wh),
			Center: true,
		},
	})
	if w == nil {
		log.Printf("主窗口创建失败")
		return
	}
	defer w.Destroy()
	w.SetBackgroundColor(0x0b, 0x12, 0x1f) // 站点深色底色:加载期不白闪
	w.Navigate("http://127.0.0.1:8787")
	hw := uintptr(w.Window())
	setMainWinIcon(hw) // 窗口图标(NiliX icon.ico):左上角 + Alt-Tab
	// 记忆轮询:用户调整窗口大小后落盘(子进程持有窗口句柄)
	go saveWinLoop()
	w.Run()
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

	// 主窗口子进程模式:独立进程跑 WebView2 管理窗口(任务栏图标=NiliX,环境不与灵动岛冲突)
	if len(os.Args) > 1 && os.Args[1] == "--mainwin" {
		runMainWindowWebView()
		return
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
		go saveWinLoop() // 记忆用户调整的窗口尺寸
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

// closeMainWindow 关闭管理主窗口(子进程 WebView2)。用 FindWindowW 精确匹配标题
// (主窗口 title 恒为 "NiliX",灵动岛为 "NiliX HUD")——不用 EnumWindows 枚举:
// 在 systray 退出回调里枚举会跨进程 GetWindowTextW,偶发挂起导致"点 X 卡住"(托盘图标已删、进程不退)。
func closeMainWindow() {
	t, err := syscall.UTF16PtrFromString(mainWinTitle)
	if err != nil {
		return
	}
	if h, _, _ := procWinFind.Call(0, uintptr(unsafe.Pointer(t))); h != 0 {
		_, _, _ = procWinClose.Call(h, 0x0010, 0, 0) // WM_CLOSE=0x10
	}
}

func onExit() {
	log.Println("onExit: 开始退出")
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
