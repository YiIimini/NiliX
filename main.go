package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

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
// runMainWindow 打开主窗口:由独立 wails 进程(NiliX-Main.exe)提供——
// frameless 自定义标题栏 + WebView2 集成(wails 后端成熟),替代旧的 --mainwin
// go-webview2 子进程。NiliX-Main.exe 缺失时回退系统浏览器。
func runMainWindow(url string) {
	exe, err := os.Executable()
	if err != nil {
		openBrowser(url)
		return
	}
	mainExe := filepath.Join(filepath.Dir(exe), "NiliX-Main.exe")
	if _, err := os.Stat(mainExe); err != nil {
		openBrowser(url)
		return
	}
	log.Printf("主窗口(wails 进程)打开: %s", url)
	cmd := exec.Command(mainExe)
	// 不能 HideWindow:首个窗口若被创建为隐藏,WebView2 环境初始化会卡死(历史教训);
	// exe 为 windowsgui 无控制台,正常显示即可
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
	// 就绪重试:控制器创建前 SetBackgroundColor 是空操作(GetController 为 nil),
	// 首帧前反复应用直到生效,WebView2 默认白底来不及显示(与灵动岛 SetTransparent 同套路)
	go func() {
		for i := 0; i < 120 && !w.BackgroundOK(); i++ {
			w.SetBackgroundColor(0x0b, 0x12, 0x1f)
			time.Sleep(50 * time.Millisecond)
		}
	}()
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

	// 看门狗守护进程模式(--watchdog <主进程PID> [原参数...]):
	// 监控主进程,异常退出(崩溃/被强杀,无 graceful_exit 标记)自动重启;用户主动退出不重启。
	// 必须在单实例/主窗口逻辑之前拦截,否则会与主进程抢互斥锁。
	if len(os.Args) > 1 && os.Args[1] == "--watchdog" {
		runWatchdog()
		return
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
	// 审计 M13:主进程启动即清理旧看门狗标记——上一轮看门狗自身被杀/系统重启残留的
	// graceful_exit 会在本进程崩溃时让新看门狗误判"用户主动退出"而不重启
	_ = os.Remove(filepath.Join("logs", "graceful_exit"))

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
	// 自包含部署路径解析:settings 显式值 → exe 目录自包含子目录(存在) → 旧硬编码。
	// 必须在任何 api 路径使用前调用(ComfyUI 启动/manju 项目/技能目录/fs 白名单)。
	exeDir, _ := filepath.Abs(".")
	api.InitPaths(exeDir, cfg.Paths.ManjuRoot, cfg.Paths.NovelRoot, cfg.Paths.ComfyRoot, cfg.Paths.ComfyShared, cfg.Paths.NovelSkill)
	// ComfyUI 输入/输出目录:显式配置优先,缺省跟随共享目录(随自包含迁移)
	comfyIn := cfg.Paths.ComfyInput
	if strings.TrimSpace(comfyIn) == "" {
		comfyIn = filepath.Join(api.ComfySharedDir, "input")
	}
	comfyOut := cfg.Paths.ComfyOutput
	if strings.TrimSpace(comfyOut) == "" {
		comfyOut = filepath.Join(api.ComfySharedDir, "output")
	}
	// ComfyUI 启动参数单一数据源:settings.json → HUD 卡片 / Comfy 页面 / 实际启动命令共用。
	api.SetComfyParams(cfg.Render.ComfyURL, comfyIn, comfyOut)
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
	// 安全:会话令牌(随机 32 hex)注入所有写请求鉴权;fs 根目录白名单(知识库/漫剧/小说/Comfy 目录)
	tok := make([]byte, 16)
	if _, rerr := rand.Read(tok); rerr == nil {
		api.SetSessionToken(hex.EncodeToString(tok))
	}
	api.SetFSRoots(*kbRoot, api.NovelRootDir, comfyIn, comfyOut)
	srv := api.NewServer(store, cfg, []byte(indexHTML), renderMgr, sysmonCol, kbStore, *kbRoot, kbSub, islandSub, outDir)
	addr := "127.0.0.1:" + *port
	url := "http://" + addr

	// HTTP 服务放后台 goroutine，托盘图标阻塞主流程。
	go func() {
		log.Printf("NiliX 已启动，控制台: %s", url)
		// 审计 M14:服务超时配置(防 slowloris 挂死连接/超大头占内存);
		// WriteTimeout 不设——LLM/渲染为长任务,写超时反而误杀
		srv := &http.Server{
			Addr:              addr,
			Handler:           srv.Routes(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       5 * time.Minute,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		if err := srv.ListenAndServe(); err != nil {
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

	// ComfyUI 自动拉起:应用启动后判断,未运行则自动启动(用户无需手动;
	// 渲染/资产/编码前另有 ensureComfyReady 兜底)。在线则跳过,零打扰。
	go func() {
		time.Sleep(1200 * time.Millisecond) // 等服务与主窗口就绪
		if api.ComfyOnline() {
			return
		}
		log.Println("ComfyUI 未运行,自动启动…")
		if err := api.ComfyStart(); err != nil {
			log.Printf("ComfyUI 自动启动失败: %v(可到灵动岛/ComfyUI 页手动启动)", err)
		}
	}()

	// 崩溃恢复续跑已禁用(用户要求取消自动续跑):渲染中异常退出后不再自动恢复,
	// 用户可在工作台手动点「续跑」按需恢复(幂等跳过已完成,检查点收回未收产物)。
	// go func() {
	// 	time.Sleep(3 * time.Second) // 等服务/ComfyUI 拉起就绪
	// 	api.AutoRecoverRendering()
	// }()

	// 看门狗守护已禁用:崩溃自动重启会导致"启动即反复拉起进程 → 窗口反复闪黑"。
	// 保留单实例锁(防止多开);进程崩溃后由用户手动重新启动,不再无限自愈。
	// go func() {
	// 	time.Sleep(2 * time.Second)
	// 	exe, err := os.Executable()
	// 	if err != nil {
	// 		return
	// 	}
	// 	args := append([]string{"--watchdog", strconv.Itoa(os.Getpid())}, os.Args[1:]...)
	// 	cmd := exec.Command(exe, args...)
	// 	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// 	_ = cmd.Start()
	// }()

	// 灵动岛悬浮胶囊:由独立 wails v3 进程(NiliX-Capsule.exe)提供——go-webview2 直连
	// WebView2 的透明/无边框渲染有黑底/画刷 bug(官方 Issue #888/#5492),wails 后端
	// 成熟处理。NiliX 只负责拉起胶囊进程(独立生命周期,退出不影响主进程)。
	go func() {
		time.Sleep(800 * time.Millisecond) // 等服务与主窗口就绪
		startCapsule()
	}()

	systray.Run(onReady(url), onExit)
}

// startCapsule 拉起独立灵动岛胶囊进程(NiliX-Capsule.exe,与主 exe 同目录)。
// 单实例:胶囊控制端口 8788 已监听(进程在跑)则不重复启动。
func startCapsule() {
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:8788", 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return // 胶囊已在运行
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	capExe := filepath.Join(filepath.Dir(exe), "NiliX-Capsule.exe")
	if _, err := os.Stat(capExe); err != nil {
		log.Printf("灵动岛胶囊程序缺失(跳过): %s", capExe)
		return
	}
	cmd := exec.Command(capExe)
	// 胶囊是 GUI 应用(windowsgui,无 console),不能用 HideWindow——STARTF_USESHOWWINDOW
	// 会把胶囊的主窗口也隐藏(实测窗口 vis=false)。
	if err := cmd.Start(); err != nil {
		log.Printf("灵动岛胶囊启动失败: %v", err)
	}
}

func onReady(url string) func() {
	return func() {
		systray.SetIcon(iconICO)
		systray.SetTitle("NiliX")
		systray.SetTooltip("NiliX")
		// 托盘菜单(美化):分组 + 图标 + ComfyUI 子菜单 + 胶囊开关
		mApp := systray.AddMenuItem("NiliX 工作台", "打开 NiliX 工作台")
		mApp.SetIcon(iconICO)
		systray.AddSeparator()

		mComfy := systray.AddMenuItem("ComfyUI", "ComfyUI 控制")
		// 状态灯图标:红=已停止 黄=启动中 绿=运行中(有任务) 蓝=闲置中(在线空闲)。
		// 定时轮询刷新(3s),ComfyUI 状态变化即时反映在菜单图标。
		mComfy.SetIcon(dotIcon(235, 70, 60))
		go func() {
			for {
				mComfy.SetIcon(comfyStatusLight())
				time.Sleep(3 * time.Second)
			}
		}()
		mComfyOpen := mComfy.AddSubMenuItem("打开面板", "打开 ComfyUI 面板")
		mComfyStart := mComfy.AddSubMenuItem("启动 ComfyUI", "启动 ComfyUI 服务")
		mComfyStop := mComfy.AddSubMenuItem("停止 ComfyUI", "停止 ComfyUI 服务")
		systray.AddSeparator()

		// 灵动岛胶囊显示/隐藏(胶囊为独立 wails 进程)
		mCapsule := systray.AddMenuItemCheckbox("灵动岛胶囊", "显示/隐藏灵动岛悬浮胶囊", true)
		mAuto := systray.AddMenuItemCheckbox("开机自启", "开机自动启动 NiliX", autostart.Enabled())
		systray.AddSeparator()

		mQuit := systray.AddMenuItem("结束应用", "关闭窗口与服务并退出")
		go func() {
			for {
				select {
				case <-mApp.ClickedCh:
					showMainWindow(url)
				case <-mComfyOpen.ClickedCh:
					openBrowser(api.ComfyURL())
				case <-mComfyStart.ClickedCh:
					if err := api.ComfyStart(); err != nil {
						log.Printf("托盘启动 ComfyUI 失败: %v", err)
					}
				case <-mComfyStop.ClickedCh:
					api.ComfyStop()
				case <-mCapsule.ClickedCh:
					if mCapsule.Checked() {
						hideCapsule()
						mCapsule.Uncheck()
					} else {
						startCapsule()
						mCapsule.Check()
					}
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
					log.Println("退出触发: 托盘「结束应用」") // 诊断:莫名退出时定位触发源
					systray.Quit()
				}
			}
		}()
	}
}

// hideCapsule 隐藏灵动岛胶囊:调用胶囊进程控制端口 8788 /close(退出胶囊进程)。
// 再次显示由托盘勾选触发 startCapsule 重新拉起。
func hideCapsule() {
	req, err := http.NewRequest("GET", "http://127.0.0.1:8788/close", nil)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("胶囊关闭请求失败(可能未运行): %v", err)
		return
	}
	_ = resp.Body.Close()
}

// dotIcon 生成 16x16 实心圆点状态灯 PNG(托盘菜单项图标)
func dotIcon(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			dx, dy := float64(x)-7.5, float64(y)-7.5
			if dx*dx+dy*dy <= 7*7 {
				img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// comfyStatusLight 托盘 ComfyUI 状态灯:
// 红=已停止(离线无进程) 黄=启动中(离线但端口有进程) 绿=运行中(在线且队列有任务) 蓝=闲置中(在线空闲)
func comfyStatusLight() []byte {
	if api.ComfyOnline() {
		if api.ComfyBusy() {
			return dotIcon(70, 200, 100) // 绿
		}
		return dotIcon(80, 150, 240) // 蓝
	}
	if api.ComfyPortPID() > 0 {
		return dotIcon(255, 190, 30) // 黄
	}
	return dotIcon(235, 70, 60) // 红
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
	// 正常退出标记:看门狗守护进程据此判断——存在该标记=用户主动退出,不重启;
	// 崩溃/被强杀不会走 onExit,无标记 → 看门狗自动重启
	_ = os.MkdirAll("logs", 0755)
	_ = os.WriteFile(filepath.Join("logs", "graceful_exit"), []byte(time.Now().Format(time.RFC3339)), 0644)
	closeMainWindow() // 全退(灵动岛 X / 托盘结束应用):一并关闭管理主窗口
	log.Println("NiliX 已退出")
}

// runWatchdog 看门狗守护进程(NiliX.exe --watchdog <主进程PID> [原参数...]):
// 监控主进程句柄,退出后检查 graceful_exit 标记——有(用户主动退出)则不重启并清标记;
// 无(崩溃/被强杀/渲染 panic 未兜住)则记录并自动重启 NiliX(原参数)。
func runWatchdog() {
	pid, err := strconv.Atoi(os.Args[2])
	if err != nil || pid <= 0 {
		return
	}
	marker := filepath.Join("logs", "graceful_exit")
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// 主进程已不在(可能启动即崩):异常则重启
		if _, serr := os.Stat(marker); os.IsNotExist(serr) {
			restartNiliX()
		} else {
			_ = os.Remove(marker)
		}
		return
	}
	_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
	_ = windows.CloseHandle(h)
	if _, err := os.Stat(marker); err == nil {
		_ = os.Remove(marker) // 用户主动退出,不重启
		return
	}
	// 异常退出:记录并自动重启
	f, _ := os.OpenFile(filepath.Join("logs", "watchdog.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if f != nil {
		_, _ = f.WriteString("[" + time.Now().Format("2006-01-02 15:04:05") + "] 主进程异常退出(PID " + strconv.Itoa(pid) + "),自动重启\n")
		_ = f.Close()
	}
	restartNiliX()
}

// restartNiliX 看门狗重启主进程(os.Args[1:3] = --watchdog <pid>,原参数从 3 开始)
func restartNiliX() {
	args := os.Args[3:]
	cmd := exec.Command(os.Args[0], args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		log.Printf("看门狗重启失败: %v", err)
	}
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
