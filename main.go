package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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

// iconPNG 从 icon.ico 提取的 PNG 字节(wails 托盘/菜单位图只认 PNG,
// 直接传 ICO 给 CreateSmallHIconFromImage 会失败——见 icotest 验证)。
var iconPNG = func() []byte {
	b, _ := icoToPNG(iconICO, 32)
	return b
}()

// iconPNGMenu 托盘菜单项图标:Windows 菜单位图(SetMenuItemBitmaps)不合成 alpha,
// 透明 PNG 的透明区在菜单里显示为黑底。给图标垫不透明深色圆角背景,
// 菜单里显示为整洁的深色小方块图标(16x16,接近系统菜单图标尺寸)。
var iconPNGMenu = func() []byte {
	b, _ := icoToPNGMenu(iconICO, 16)
	return b
}()

// icoToPNG 从 ICO 文件提取指定尺寸(含最近似)的图标图像,输出 PNG 字节。
// 支持 BMP(ICONIMAGE)与 PNG 压缩两种内嵌格式。wails 的 CreateSmallHIconFromImage
// 把完整 ICO 容器传给 CreateIconFromResourceEx(该 API 要单图像资源位),加载失败;
// SetMenuIcons 走 pngToImage 只认 PNG——故统一转 PNG。
func icoToPNG(ico []byte, targetSize int) ([]byte, error) {
	if len(ico) < 6 || ico[0] != 0 || ico[1] != 0 || ico[2] != 1 || ico[3] != 0 {
		return nil, errors.New("not an ICO file")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 {
		return nil, errors.New("empty ICO")
	}
	type entry struct{ off, size, w, h int }
	var ents []entry
	best := -1
	bestDelta := int(^uint(0) >> 1)
	for i := 0; i < count; i++ {
		base := 6 + i*16
		if base+16 > len(ico) {
			break
		}
		w := int(ico[base])
		if w == 0 {
			w = 256
		}
		h := int(ico[base+1])
		if h == 0 {
			h = 256
		}
		size := int(binary.LittleEndian.Uint32(ico[base+8 : base+12]))
		off := int(binary.LittleEndian.Uint32(ico[base+12 : base+16]))
		ents = append(ents, entry{off: off, size: size, w: w, h: h})
		d := w - targetSize
		if d < 0 {
			d = -d
		}
		if d < bestDelta {
			bestDelta = d
			best = i
		}
	}
	if best < 0 || best >= len(ents) {
		return nil, errors.New("no icon entry")
	}
	e := ents[best]
	if e.off+e.size > len(ico) {
		return nil, errors.New("icon data out of range")
	}
	data := ico[e.off : e.off+e.size]
	if len(data) > 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return data, nil // PNG 压缩内嵌:直接返回
	}
	if len(data) < 40 {
		return nil, errors.New("icon image too small")
	}
	biSize := binary.LittleEndian.Uint32(data[0:4])
	if int(biSize) < 40 || int(biSize) > len(data) {
		return nil, fmt.Errorf("bad BITMAPINFOHEADER size %d", biSize)
	}
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	bpp := int(binary.LittleEndian.Uint16(data[14:16]))
	pxHeader := int(biSize)
	if height%2 != 0 {
		return nil, fmt.Errorf("odd height %d", height)
	}
	realH := height / 2
	if realH <= 0 {
		return nil, fmt.Errorf("bad height %d", height)
	}
	rowSize := ((width*bpp + 31) / 32) * 4
	xorSize := rowSize * realH
	if pxHeader+xorSize > len(data) {
		return nil, errors.New("xor data out of range")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, realH))
	for y := 0; y < realH; y++ {
		srcRow := data[pxHeader+(realH-1-y)*rowSize : pxHeader+(realH-y)*rowSize]
		for x := 0; x < width; x++ {
			bitOff := x * bpp / 8
			if bitOff+4 > len(srcRow) {
				continue
			}
			img.SetRGBA(x, y, color.RGBA{R: srcRow[bitOff+2], G: srcRow[bitOff+1], B: srcRow[bitOff], A: srcRow[bitOff+3]})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// icoToPNGMenu 生成托盘菜单项图标(16x16,完全不透明):
// Windows 菜单位图(SetMenuItemBitmaps)用 DrawState 绘制,不合成 alpha——
// 透明 PNG 的透明区在菜单里显示为黑底。给图标垫深色圆角背景,
// 输出不透明 16x16 位图,菜单里显示为整洁小方块(接近系统菜单图标)。
func icoToPNGMenu(ico []byte, targetSize int) ([]byte, error) {
	inner, err := icoToPNG(ico, targetSize)
	if err != nil {
		return nil, err
	}
	src, err := png.Decode(bytes.NewReader(inner))
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	// 输出画布 targetSize x targetSize,深色圆角底
	out := image.NewRGBA(image.Rect(0, 0, targetSize, targetSize))
	bg := color.RGBA{R: 24, G: 28, B: 38, A: 255} // 深空蓝灰(与应用深色主题一致)
	radius := float64(targetSize) * 0.28
	for y := 0; y < targetSize; y++ {
		for x := 0; x < targetSize; x++ {
			// 圆角遮罩
			dx := float64(x) + 0.5
			dy := float64(y) + 0.5
			corn := false
			if dx < radius && dy < radius {
				corn = (dx-radius)*(dx-radius)+(dy-radius)*(dy-radius) > radius*radius
			} else if dx > float64(targetSize)-radius && dy < radius {
				corn = (dx-(float64(targetSize)-radius))*(dx-(float64(targetSize)-radius))+(dy-radius)*(dy-radius) > radius*radius
			} else if dx < radius && dy > float64(targetSize)-radius {
				corn = (dx-radius)*(dx-radius)+(dy-(float64(targetSize)-radius))*(dy-(float64(targetSize)-radius)) > radius*radius
			} else if dx > float64(targetSize)-radius && dy > float64(targetSize)-radius {
				corn = (dx-(float64(targetSize)-radius))*(dx-(float64(targetSize)-radius))+(dy-(float64(targetSize)-radius))*(dy-(float64(targetSize)-radius)) > radius*radius
			}
			if corn {
				out.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 0})
				continue
			}
			out.SetRGBA(x, y, bg)
		}
	}
	// 缩放原图标到画布中央 70%(留边距),覆盖深色底
	sw := sb.Dx()
	sh := sb.Dy()
	drawW := targetSize * 70 / 100
	drawH := targetSize * 70 / 100
	ox := (targetSize - drawW) / 2
	oy := (targetSize - drawH) / 2
	for y := 0; y < drawH; y++ {
		srcY := y * sh / drawH
		for x := 0; x < drawW; x++ {
			srcX := x * sw / drawW
			sc := src.At(sb.Min.X+srcX, sb.Min.Y+srcY)
			sr, sg, sbb, sa := sc.RGBA()
			if sa == 0 {
				continue // 透明保留底色
			}
			// 简单 alpha 混合到深色底
			a := float64(sa) / 65535.0
			r := float64(sr)/65535.0*a + float64(bg.R)/255.0*(1-a)
			g := float64(sg)/65535.0*a + float64(bg.G)/255.0*(1-a)
			b := float64(sbb)/65535.0*a + float64(bg.B)/255.0*(1-a)
			out.SetRGBA(ox+x, oy+y, color.RGBA{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- 桌面主窗�?双击 exe 即在应用窗口内管�?----
// �?Edge App 模式(msedge --app=URL)开独立应用窗口:无边栏地址栏、独立任务栏�?
// 观感等同桌面应用。灵动岛保持进程�?WebView2(WebView2 同进程只允许一�?environment,
// 第二个会创建卡死——实测结�?故主窗口�?Edge App 进程,互不冲突)�?
// 关闭窗口 = 驻留托盘(渲染/续写任务不中�?,托盘菜单或再次双击可唤起;托盘「退出」才真正退出�?

const mainWinTitle = "NiliX" // 管理主窗口标题(Edge App 窗口=页面 title,恒为 NiliX;灵动岛为 NiliX HUD 区分)

// ---- 主窗口尺寸记�?用户调整后持久化,下次启动直接加载;独立 json,避免动加�?settings) ----
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

// msedgePath 定位 Edge 浏览�?系统自带;WebView2 运行时本就依赖同一 Edge)
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

// calcMainWinSize 窗口尺寸:有记忆用记忆(用户调过的尺�?,无记忆按 16:9 默认;
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

// defaultMainWinSize 无记忆时�?16:9 默认(�?工作�?5%�?600;高受限反向缩�?
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

// saveMainWindowSize 读取当前窗口尺寸并持久化(用户�?Edge 标题栏自行调整后记忆;
// �?saveWinLoop �?3s 检测一�?尺寸变化即落�?
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

// saveWinLoop 常驻轮询:记忆窗口尺寸(用户拖动/缩放�?3s 内落�?�?
// 启动先等 10s(fitMainWindow 校正完成后再开�?避免�?Edge 启动时的旧尺寸覆盖进记忆)�?
func saveWinLoop() {
	time.Sleep(10 * time.Second)
	for {
		time.Sleep(3 * time.Second)
		saveMainWindowSize()
	}
}

// setMainWinIcon 加载内嵌 icon.ico 并设置窗口图�?左上角 + Alt-Tab + 任务栏小图标)
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

// runMainWindowWebView 子进�?--mainwin)入口:创建 WebView2 管理窗口并运行�?
// 阻塞至窗口关�?用户�?X)�?子进程退出�?
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
	// 创建即指定尺�?居中:窗口第一帧就是正确尺�?不做任何后续校正,杜绝"先小后大/闪退观感"
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
	// 就绪重试:控制器创建前 SetBackgroundColor 是空操作(GetController �?nil),
	// 首帧前反复应用直到生�?WebView2 默认白底来不及显�?与灵动岛 SetTransparent 同套�?
	go func() {
		for i := 0; i < 120 && !w.BackgroundOK(); i++ {
			w.SetBackgroundColor(0x0b, 0x12, 0x1f)
			time.Sleep(50 * time.Millisecond)
		}
	}()
	w.Navigate("http://127.0.0.1:8787")
	hw := uintptr(w.Window())
	setMainWinIcon(hw) // 窗口图标(NiliX icon.ico):左上角 + Alt-Tab
	// 记忆轮询:用户调整窗口大小后落�?子进程持有窗口句�?
	go saveWinLoop()
	w.Run()
}

var (
	procEnumWindows = user32Lazy.NewProc("EnumWindows")
	procGetTextW    = user32Lazy.NewProc("GetWindowTextW")
)

// findMainWindow 枚举顶层窗口按标题精确匹配管理主窗口(title 恒为 "NiliX";
// 灵动岛窗口标题为 "NiliX HUD" 已区�?不用 FindWindowW 因其对动�?title 前后缀不可�?�?
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

func main() {
	// 切到可执行文件所在目录：开机自�?注册�?Run key)启动时工作目录可能是 System32�?
	// 会导致相对路�?settings.json / logs / clips)读写到错误位置，进而配置丢�?日志落空�?
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			_ = os.Chdir(dir)
		}
	}

	// 看门狗守护进程模�?--watchdog <主进程PID> [原参�?..]):
	// 监控主进�?异常退�?崩溃/被强杀,�?graceful_exit 标记)自动重启;用户主动退出不重启�?
	// 必须在单实例/主窗口逻辑之前拦截,否则会与主进程抢互斥锁�?
	if len(os.Args) > 1 && os.Args[1] == "--watchdog" {
		runWatchdog()
		return
	}

	// 主窗口子进程模式:独立进程�?WebView2 管理窗口(任务栏图�?NiliX,环境不与灵动岛冲�?
	if len(os.Args) > 1 && os.Args[1] == "--mainwin" {
		runMainWindowWebView()
		return
	}

	port := flag.String("port", "8787", "监听端口")
	cfgPath := flag.String("config", "settings.json", "设置文件路径")
	kbRoot := flag.String("kb", `C:\Mi\Ai\WorkBench\zhishiku`, "知识库根目录")
	flag.Parse()
	// �?DPI 感知：必须在任何窗口（托�?胶囊）创建前设置，否则窗口尺寸与圆角裁剪错乱�?
	island.EnablePerMonitorDPI()

	// 以 windowsgui 方式运行时无控制台，日志写文件。
	// 日志固定写到 exe 目录 logs/server.log(相对 cwd 可能因开机自启/快捷方式 cwd 不同而写偏,
	// 用户找不到日志;用 exe 绝对路径保证可寻址)。
	logPath := filepath.Join(exeDir(), "logs", "server.log")
	if f, err := setupLogFile(logPath); err == nil {
		log.SetOutput(f)
		defer f.Close()
		log.Printf("NiliX 日志已开启: %s", logPath)
	}
	// 审计 M13:主进程启动即清理旧看门狗标记——上一轮看门狗自身被杀/系统重启残留�?
	// graceful_exit 会在本进程崩溃时让新看门狗误�?用户主动退�?而不重启
	_ = os.Remove(filepath.Join("logs", "graceful_exit"))

	// 单实例看门狗：防止重复启动。
	guard, err := watchdog.SingleInstance("NiliX")
	if err != nil {
		if errors.Is(err, watchdog.ErrAlreadyRunning) {
			// 第二个实例在此进程内 gApp 尚未创建(nil),直接 openMainWindow 会 panic。
			// 改为 HTTP 唤起第一个实例的 8799 /open(同进程 gApp 有效,窗口重建/置前)。
			// 唤起成功则静默退出(不弹阻塞式 MessageBox 打断用户);失败才提示。
			client := &http.Client{Timeout: 2 * time.Second}
			resp, e2 := client.Get("http://127.0.0.1:8799/open")
			if e2 == nil {
				resp.Body.Close()
			} else {
				watchdog.Alert("小说转视频服务", "NiliX 已在运行,但唤起管理窗口失败,请直接使用托盘图标。")
			}
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
	// 首次加载时把明文 key 迁移为加密存储（幂等：已加密的跳过）�?
	if err := store.Save(cfg); err != nil {
		log.Printf("迁移加密配置失败(忽略): %v", err)
	}
	// 自包含部署路径解�?settings 显式�?�?exe 目录自包含子目录(存在) �?旧硬编码�?
	// 必须在任�?api 路径使用前调�?ComfyUI 启动/manju 项目/技能目�?fs 白名�?�?
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
	// ComfyUI 启动参数单一数据�?settings.json �?HUD 卡片 / Comfy 页面 / 实际启动命令共用�?
	api.SetComfyParams(cfg.Render.ComfyURL, comfyIn, comfyOut)
	// 智能体全局默认(settings.json agent �?�?全项目共�?与全局设置读写入口�?
	api.SetGlobalAgentCfg(cfg)
	api.SetManjuSettingsStore(store)

	// 渲染任务管理器（ComfyUI 客户�?+ 本地产物目录）�?
	outDir := "clips"
	renderMgr := render.NewManager(backend.NewComfyUIClient(cfg.Render.ComfyURL))
	sysmonCol := sysmon.NewCollector()
	kbStore := kb_work.NewStore(*kbRoot)
	kbSub, _ := fs.Sub(kbFS, "web/kb")
	islandSub, _ := fs.Sub(islandFS, "web/island")
	// 安全:会话令牌(随机 32 hex)注入所有写请求鉴权;fs 根目录白名单(知识�?漫剧/小说/Comfy 目录)
	tok := make([]byte, 16)
	if _, rerr := rand.Read(tok); rerr == nil {
		api.SetSessionToken(hex.EncodeToString(tok))
	}
	api.SetFSRoots(*kbRoot, api.NovelRootDir, comfyIn, comfyOut)
	srv := api.NewServer(store, cfg, []byte(indexHTML), renderMgr, sysmonCol, kbStore, *kbRoot, kbSub, islandSub, outDir)
	addr := "127.0.0.1:" + *port
	url := "http://" + addr

	// HTTP 服务放后�?goroutine，托盘图标阻塞主流程�?
	go func() {
		log.Printf("NiliX 已启动，控制台: %s", url)
		// 审计 M14:服务超时配置(�?slowloris 挂死连接/超大头占内存);
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
			if gApp != nil {
				gApp.Quit()
			}
		}
	}()

	// ========== wails 应用(单进�?主窗�?+ 灵动岛胶�?+ 托盘,一�?exe) ==========
	// 原架�?NiliX.exe + NiliX-Capsule.exe + NiliX-Main.exe 三个独立程序不合�?
	// 合并为单一 NiliX.exe——wails 多窗�?主窗口加载管理页 / 胶囊加载 /island/ 透明悬浮)�?
	gApp = application.New(application.Options{
		Name:        "NiliX",
		Description: "NiliX 小说转视频工作台",
		// 运行日志:捕获 wails/WebView2 运行时错误写入 server.log(用户可见报错)
		ErrorHandler: func(err error) {
			if err != nil {
				log.Printf("[运行时错误] %v", err)
			}
		},
	})
	// 应用级图标(icon.ico):统一 exe 任务栏/Alt-Tab/窗口图标(rsrc syso 提供 exe 资源,
	// 此调用补 wails 应用图标,托盘 SetIcon 另设)
	if len(iconICO) > 0 {
		gApp.SetIcon(iconICO)
	}

	// 主窗�?frameless 自定义标题栏,控制按钮/拖拽�?8799 同进程直接调窗口 API)
	createMainWindow(gApp, url)

	// 灵动岛胶囊窗�?透明悬浮 + 隐藏任务�?展开/收起�?8788 同进程动�?SetSize)
	setCapsuleWin(createCapsuleWindow(gApp, url))

	// 控制端口(同进程直接调窗口,进程常驻 = 端口常驻,按钮可靠)
	startControlServers(gApp, url)

	// 托盘(状态灯/子菜�?开�?
	buildTray(gApp, url)

	// ComfyUI 自动拉起:应用启动后判�?未运行则自动启动(用户无需手动;
	// 渲染/资产/编码前另�?ensureComfyReady 兜底)。在线则跳过,零打扰�?
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

	if err := gApp.Run(); err != nil {
		log.Printf("应用退出: %v", err)
	}
	onExit()
}

// gApp 全局应用引用(HTTP 服务异常退出时调用 Quit)
var gApp *application.App

// 虚拟屏幕尺寸(胶囊/主窗口定位用,�?island 包同�?
var (
	procGetSystemMetrics = syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics")
	smXVirtual           = 76
	smYVirtual           = 77
	smCXVirtual          = 78
)

// openMainWindow 打开主窗�?已关闭则重建,已存在则显示置前
func openMainWindow(url string) {
	if mainWinClosed.Load() || getMainWin() == nil {
		createMainWindow(gApp, url)
	} else {
		getMainWin().Show()
		getMainWin().Focus()
	}
}

// mainWinRef 当前主窗�?关闭后重建用)
// 审计 F9:窗口引用在控制端口 handler(HTTP goroutine)/托盘回调/动画 goroutine 间共享,
// 裸指针读写是数据竞争(go build -race 可检出)——读写统一走 winRefMu 保护的 getter/setter
var (
	winRefMu    sync.RWMutex
	mainWinRef  *application.WebviewWindow
	capsuleWinRef *application.WebviewWindow
)

func getMainWin() *application.WebviewWindow {
	winRefMu.RLock()
	defer winRefMu.RUnlock()
	return mainWinRef
}

func setMainWin(w *application.WebviewWindow) {
	winRefMu.Lock()
	mainWinRef = w
	winRefMu.Unlock()
}

func getCapsuleWin() *application.WebviewWindow {
	winRefMu.RLock()
	defer winRefMu.RUnlock()
	return capsuleWinRef
}

func setCapsuleWin(w *application.WebviewWindow) {
	winRefMu.Lock()
	capsuleWinRef = w
	winRefMu.Unlock()
}

// mainWinClosed 主窗口是否已关闭(托盘「工作台」重�?
var mainWinClosed atomic.Bool

// createMainWindow 创建主窗�?frameless,加载管理�?8787,记忆尺寸/位置)
func createMainWindow(app *application.App, url string) *application.WebviewWindow {
	mw, mh, mx, my := loadMainWinState()
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "NiliX",
		Width:            mw,
		Height:           mh,
		X:                mx,
		Y:                my,
		Frameless:        true, // 去系统标题栏,前端自定义(按钮/拖拽走 8799)
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGB(10, 15, 30),
		URL:              url + "/?_=" + strconv.FormatInt(time.Now().UnixNano(), 36),
	})
	mainWinClosed.Store(false)
	setMainWin(win)
	// 记忆窗口尺寸/位置(与旧 mainwin.json 语义一�?
	statePath := filepath.Join(exeDir(), "mainwin.json")
	saveWin := func() {
		w2, h2 := win.Size()
		px, py := win.Position()
		b, _ := json.Marshal(map[string]int{"w": w2, "h": h2, "x": px, "y": py})
		_ = os.WriteFile(statePath, b, 0644)
	}
	win.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { saveWin() })
	win.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { saveWin() })
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		// 退出/关闭前兜底保存最后一次尺寸位置(拖动后直接关窗的场景,
		// WM_EXITSIZEMOVE 若未触发,保证下次启动仍用用户最后调整的尺寸)
		saveWin()
		mainWinClosed.Store(true)
	})
	return win
}

// createCapsuleWindow 创建灵动岛胶囊窗口(透明 + 隐藏任务栏 + 顶部居中贴边 300x44)
func createCapsuleWindow(app *application.App, url string) *application.WebviewWindow {
	vx, _, _ := procGetSystemMetrics.Call(uintptr(smXVirtual))
	vy, _, _ := procGetSystemMetrics.Call(uintptr(smYVirtual))
	vw, _, _ := procGetSystemMetrics.Call(uintptr(smCXVirtual))
	capX := int(int32(vx)) + (int(int32(vw))-300)/2
	capY := int(int32(vy)) // 顶部贴边(0px)。WindowXY 模式下直接用 X/Y,不居中;
	// 且 X≠0 不会触发 wails 的 X==0&&Y==0 → CW_USEDEFAULT(屏幕正中)分支。
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "NiliX HUD",
		Width:            300,
		Height:           44,
		X:                capX,
		Y:                capY,
		InitialPosition:  application.WindowXY, // 关键:默认 WindowCentered 会先居中再跳顶
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		URL:              url + "/island/",
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
			HiddenOnTaskbar:                   true, // 悬浮窗不占任务栏
		},
	})
	// 兜底:应用启动后(窗口 impl 已建)再强制定位一次,防止任何初始位置偏差。
	// 300ms 足够 impl 创建(此前 1.5s 造成"先中心后跳顶"的视觉闪烁)。
	go func() {
		time.Sleep(300 * time.Millisecond)
		win.SetPosition(capX, capY)
		win.SetAlwaysOnTop(true)
	}()
	return win
}

// loadMainWinState 读取记忆的主窗口尺寸/位置(缺省 1400x900)
func loadMainWinState() (w, h, x, y int) {
	w, h = 1400, 900
	x, y = 80, 40
	if b, err := os.ReadFile(filepath.Join(exeDir(), "mainwin.json")); err == nil {
		var st struct {
			W, H, X, Y int
		}
		if json.Unmarshal(b, &st) == nil && st.W > 400 && st.H > 300 {
			w, h, x, y = st.W, st.H, st.X, st.Y
		}
	}
	return
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// validLocalHost 判断 Host/Origin 主机部分是否为本机地址(兼容带/不带端口)
// 控制端口 CSRF 防线(F10):与 api.validLocalHost 同口径,main 包自用一份
func validLocalHost(hostPort string) bool {
	h := hostPort
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		h = host
	}
	switch strings.ToLower(strings.Trim(h, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// startControlServers 同进程控制端�?
// 8799=主窗�?最小化/最大化/关闭/移动),8788=胶囊(展开/收起动画/关闭)�?
// 进程常驻(wails app.Run),端口不随子进程退出而消失——按钮可靠�?
func startControlServers(app *application.App, url string) {
	// 审计 F10:控制端口有副作用(GET /close 可退出整个应用)且无 token——必须防
	// CSRF/DNS rebinding:仅接受本机 Host + 本机 Origin(外部网页 fetch 127.0.0.1
	// 会带 Origin: http://evil.com,直接拒绝;本地页面 Origin 为 127.0.0.1 放行)
	controlGuard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !validLocalHost(r.Host) {
				http.Error(w, "非法 Host", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := neturl.Parse(origin)
				if err != nil || !validLocalHost(u.Host) {
					http.Error(w, "非法来源", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	// 8799:主窗口控�?页面按钮/拖拽,同进程直接调窗口 API)
	mux := http.NewServeMux()
	// CORS 预检兜底:页面若带自定义头(如 X-NiliX-Token)跨端口 fetch 会先发 OPTIONS,
	// 必须响应允许,否则请求被浏览器拦截(历史 bug:胶囊展开失效的真根因之一)。
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-NiliX-Token, Content-Type")
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /open", func(w http.ResponseWriter, r *http.Request) {
		// 单实例唤起:第二个实例经 HTTP 请求本进程(同进程 gApp 有效)打开/重建主窗口
		w.Header().Set("Access-Control-Allow-Origin", "*")
		openMainWindow(url)
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /nx", func(w http.ResponseWriter, r *http.Request) {
		// N_X 监测:主应用窗口是否打开(HUD 工作台按钮状态;同进程判断 mainWinRef
		// 或枚举标题 NiliX,托盘可重建)。右侧附文本:版本/端口/PID。
		w.Header().Set("Access-Control-Allow-Origin", "*")
		open := getMainWin() != nil && !mainWinClosed.Load()
		if !open && findMainWindow() != 0 {
			open = true // 兜底:窗口枚举确认(旧引用失效场景)
		}
		w.Header().Set("Content-Type", "application/json")
		// 端口从 url 提取(如 http://127.0.0.1:8787 → 8787)
		nxPort := ""
		if u, perr := neturl.Parse(url); perr == nil && u.Port() != "" {
			nxPort = u.Port()
		}
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"window_open":%t,"port":"%s","pid":%d}`,
			open, nxPort, os.Getpid())))
	})
	mux.HandleFunc("GET /minimize", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[ctl8799] /minimize from %s", r.RemoteAddr)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		if win := getMainWin(); win != nil {
			go win.Minimise()
		}
	})
	mux.HandleFunc("GET /maximize", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[ctl8799] /maximize from %s", r.RemoteAddr)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		if win := getMainWin(); win != nil {
			go win.ToggleMaximise()
		}
	})
	mux.HandleFunc("GET /close", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[ctl8799] /close from %s", r.RemoteAddr)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		if win := getMainWin(); win != nil {
			go win.Close() // 关闭主窗口,应用(托盘+胶囊)继续,托盘可重建
		}
	})
	mux.HandleFunc("GET /move", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		q := r.URL.Query()
		dx, _ := strconv.Atoi(q.Get("dx"))
		dy, _ := strconv.Atoi(q.Get("dy"))
		if (dx != 0 || dy != 0) && getMainWin() != nil {
			cx, cy := getMainWin().Position()
			go getMainWin().SetPosition(cx+dx, cy+dy)
		}
		w.WriteHeader(200)
	})
	// 前端边缘拖拽缩放(frameless + 外部 URL 无 wails runtime,JS 边缘检测 →
	// HTTP 调 SetSize 缩放)。w=新宽,h=新高(绝对像素,含最小/最大约束)。
	mux.HandleFunc("GET /resize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		q := r.URL.Query()
		nw, _ := strconv.Atoi(q.Get("w"))
		nh, _ := strconv.Atoi(q.Get("h"))
		if nw < 900 {
			nw = 900
		}
		if nh < 600 {
			nh = 600
		}
		if win := getMainWin(); win != nil {
			go win.SetSize(nw, nh)
		}
		w.WriteHeader(200)
	})
	go func() {
		if err := http.ListenAndServe("127.0.0.1:8799", controlGuard(mux)); err != nil {
			log.Printf("主窗口控制端口退出: %v", err)
		}
	}()

	// 8788:胶囊控制(展开/收起动画 SetSize + 关闭)
	mux2 := http.NewServeMux()
	mux2.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-NiliX-Token, Content-Type")
		w.WriteHeader(204)
	})
	mux2.HandleFunc("GET /size", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		q := r.URL.Query()
		wid, hgt := 300, 44
		if v, err := strconv.Atoi(q.Get("w")); err == nil && v >= 200 && v <= 800 {
			wid = v
		}
		if v, err := strconv.Atoi(q.Get("h")); err == nil && v >= 40 && v <= 900 {
			hgt = v
		}
		animateCapsule(wid, hgt)
		w.WriteHeader(200)
	})
	mux2.HandleFunc("GET /close", func(w http.ResponseWriter, r *http.Request) {
		// 灵动岛 ✕ = 整个应用退出(原版语义,防误触:页面已做两段式确认)。
		// 只关胶囊窗口会让用户以为按钮失效——必须 app.Quit() 触发 onExit 全退。
		log.Printf("[ctl8788] /close(胶囊✕退出整个应用) from %s", r.RemoteAddr)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		go func() {
			if gApp != nil {
				gApp.Quit()
			}
		}()
	})
	go func() {
		if err := http.ListenAndServe("127.0.0.1:8788", controlGuard(mux2)); err != nil {
			log.Printf("胶囊控制端口退出: %v", err)
		}
	}()
}

func capsuleWinClose() {
	if win := getCapsuleWin(); win != nil {
		win.Close() // 关闭胶囊窗口(托盘开关可重建)
	}
}

// capsuleAnimGen 胶囊动画代数(新请求取消旧动画,防打�?
var capsuleAnimGen int32

// animateCapsule 胶囊展开/收起动画:分段 SetSize + 居中
func animateCapsule(toW, toH int) {
	win := getCapsuleWin()
	if win == nil {
		return
	}
	vw, _, _ := procGetSystemMetrics.Call(uintptr(smCXVirtual))
	sw, sh := win.Size()
	if sw <= 0 {
		sw = 300
	}
	if sh <= 0 {
		sh = 44
	}
	gen := atomic.AddInt32(&capsuleAnimGen, 1)
	go func() {
		steps := 12
		if sw == toW && sh == toH {
			steps = 1
		}
		for i := 1; i <= steps; i++ {
			if atomic.LoadInt32(&capsuleAnimGen) != gen {
				return // 被新请求取代(快速 展开/收起 切换)
			}
			w := sw + (toW-sw)*i/steps
			h := sh + (toH-sh)*i/steps
			x := (int(int32(vw)) - w) / 2
			win.SetPosition(x, 0)
			win.SetSize(w, h)
			time.Sleep(12 * time.Millisecond)
		}
		// 兜底:动画结束后强制最终尺寸(防累加误差/被打断残留中间值,
		// 导致胶囊以展开高度展示但内容已隐藏=大黑框)
		if atomic.LoadInt32(&capsuleAnimGen) == gen {
			x := (int(int32(vw)) - toW) / 2
			win.SetPosition(x, 0)
			win.SetSize(toW, toH)
		}
	}()
}

// buildTray 构建 wails SystemTray 托盘菜单(wails MenuItem 原生位图支持状态灯)�?
// 结构:工作�?/ ─ / ComfyUI 状�?一个条�?状态灯位图+动态文�?控制子菜�? / ─ /
//       灵动岛胶�?/ 开机自�?/ ─ / 结束应用
func buildTray(app *application.App, url string) {
	tray := app.SystemTray.New()
	// 托盘图标:必须传 PNG(wails CreateSmallHIconFromImage 传 ICO 容器会失败——
	// CreateIconFromResourceEx 要单图像资源位)。iconPNG 是 icon.ico 提取的 32px PNG。
	if len(iconPNG) > 0 {
		tray.SetIcon(iconPNG)
	} else {
		tray.SetIcon(iconICO)
	}
	menu := application.NewMenu()

	mWork := menu.Add("NiliX 工作台")
	// 菜单项图标:SetBitmap 走 pngToImage + SetMenuItemBitmaps,Windows 菜单不合成
	// alpha(透明区=黑底)——用 iconPNGMenu(16x16 深色圆角底,完全不透明)避免黑底。
	if len(iconPNGMenu) > 0 {
		mWork.SetBitmap(iconPNGMenu)
	} else if len(iconPNG) > 0 {
		mWork.SetBitmap(iconPNG)
	}
	mWork.OnClick(func(*application.Context) {
		openMainWindow(url)
	})
	menu.AddSeparator()

	// ComfyUI:单个条目(父项=状态灯位图动态动�?无状态文字——有灯就不需要文�?�?
	// �?已停�?�?启动�?�?运行�?有任�? �?闲置�?在线空闲),3s 探测+400ms 动画帧�?
	mComfySub := menu.AddSubmenu("ComfyUI")
	mComfy := menu.FindByLabel("ComfyUI")
	mComfy.SetBitmap(dotIcon(235, 70, 60))
	mComfySub.Add("打开面板").OnClick(func(*application.Context) {
		openBrowser(api.ComfyURL())
	})
	mComfySub.Add("启动 ComfyUI").OnClick(func(*application.Context) {
		if err := api.ComfyStart(); err != nil {
			log.Printf("托盘启动 ComfyUI 失败: %v", err)
		}
	})
	mComfySub.Add("停止 ComfyUI").OnClick(func(*application.Context) {
		api.ComfyStop()
	})
	menu.AddSeparator()

	// 灵动岛胶囊显�?隐藏(同进程窗口管�?关闭/重建)
	mCapsule := menu.AddCheckbox("灵动岛胶囊", true)
	mCapsule.OnClick(func(*application.Context) {
		if mCapsule.Checked() {
			capsuleWinClose()
			mCapsule.SetChecked(false)
		} else {
			setCapsuleWin(createCapsuleWindow(app, url))
			mCapsule.SetChecked(true)
		}
	})
	mAuto := menu.AddCheckbox("开机自启", autostart.Enabled())
	mAuto.OnClick(func(*application.Context) {
		if mAuto.Checked() {
			if autostart.Disable() == nil {
				mAuto.SetChecked(false)
			}
		} else {
			if exe, err := os.Executable(); err == nil && autostart.Enable(exe) == nil {
				mAuto.SetChecked(true)
			}
		}
	})
	menu.AddSeparator()
	menu.Add("结束应用").OnClick(func(*application.Context) {
		log.Println("退出触发: 托盘「结束应用」")
		app.Quit() // app.Run 返回后 main 统一走 onExit
	})

	tray.SetMenu(menu)
	tray.SetTooltip("NiliX")

	// 状态灯动态动�?3s 探测状�?避免频繁 HTTP),500ms 切动画帧�?
	// �?启动�?=旋转加载�?�?�?运行/闲置)=呼吸脉冲;�?已停�?=静态�?
	go func() {
		st := comfyProbeState()
		frame := 0
		stateTicker := time.NewTicker(3 * time.Second)
		animTicker := time.NewTicker(400 * time.Millisecond)
		defer stateTicker.Stop()
		defer animTicker.Stop()
		for {
			select {
			case <-stateTicker.C:
				st = comfyProbeState()
				tray.SetTooltip("NiliX · ComfyUI " + st.label)
			case <-animTicker.C:
				frame++
				switch st.mode {
				case "spin":
					mComfy.SetBitmap(dotIconAnim(st.r, st.g, st.b, frame%8, 8, "spin"))
				case "pulse":
					mComfy.SetBitmap(dotIconAnim(st.r, st.g, st.b, frame%4, 4, "pulse"))
				default:
					mComfy.SetBitmap(dotIcon(st.r, st.g, st.b))
				}
				// 状态灯图标已表达状�?菜单项文字保持「ComfyUI」不带状态描�?
			}
		}
	}()
}

// comfyState 托盘 ComfyUI 状态灯状�?色�?+ 动画模式(spin 转圈/pulse 脉冲/static 静�? + 文字
type comfyState struct {
	r, g, b uint8
	mode    string
	label   string
}

// comfyProbeState 探测 ComfyUI 状�?
// �?已停�?离线无进�? �?启动�?离线但端口有进程,转圈动画)
// �?运行�?在线且队列有任务,脉冲) �?闲置�?在线空闲,脉冲)
func comfyProbeState() comfyState {
	if api.ComfyOnline() {
		if api.ComfyBusy() {
			return comfyState{70, 200, 100, "pulse", "运行中"}
		}
		return comfyState{80, 150, 240, "pulse", "闲置中"}
	}
	if api.ComfyPortPID() > 0 {
		return comfyState{255, 190, 30, "spin", "启动中"}
	}
	return comfyState{235, 70, 60, "static", "已停止"}
}

// dotIconAnim 动态状态灯动画�?
//   - spin:圆环 + 大缺口旋�?加载�?,缺口 137° 随帧转动,环加粗更醒目
//   - pulse:圆点 + 强外发光光晕,光晕随帧正弦呼吸(明暗差大,动态明�?
// dotIcon 美化的状态球:玻璃质感(径向渐变+左上高光+外发�?亮描�?�?
// 已停�?�?等静态态用�?动画帧走 dotIconAnim/dotIconBeauty�?
func dotIcon(r, g, b uint8) []byte {
	return dotIconBeauty(r, g, b, 90)
}

// dotIconBeauty 现代玻璃质感状态球(16x16,托盘菜单位图):
//   - 球体:径向渐变(光源左上)+ 底部暗部衬底 + 顶部弧形高光(玻璃折射)
//   - 双层外发光:近层强、远层柔(呼吸动画时明暗变化更灵动)
//   - 亮描边:球缘提亮,仿玻璃边缘反光
//   相比旧版:高光更集中(真实球体)、暗部更分明、光晕双层过渡——观感更现代。
func dotIconBeauty(r, g, b uint8, halo uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	cr := float64(r)
	cg := float64(g)
	cb := float64(b)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			dx, dy := float64(x)-7.5, float64(y)-7.5
			dist := math.Sqrt(dx*dx + dy*dy)
			// 外层柔光晕(远)
			if dist > 8 && dist <= 10.5 && halo > 18 {
				f := (10.5 - dist) / 2.5
				a := uint8(float64(halo) * f * 0.22)
				if a > 0 {
					img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: a})
				}
			}
			// 内层强光晕(近,呼吸主体)
			if dist > 6.5 && dist <= 8.2 && halo > 18 {
				f := (8.2 - dist) / 1.7
				a := uint8(float64(halo) * f * 0.5)
				if a > 0 {
					img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: a})
				}
			}
			// 球体:径向渐变 + 顶部高光 + 底部暗部(玻璃立体感)
			if dist <= 6.5 {
				// 基础明度:中心亮、边缘暗
				base := 1 - dist/6.5*0.55
				// 顶部高光(光源在左上方 2.5,2.5)
				hld := math.Hypot(float64(x)-2.5, float64(y)-2.5)
				hl := math.Max(0, 1-hld/7.5)
				hl = hl * hl * 1.4 // 高光更集中
				// 底部暗部(右下角)
				sh := math.Max(0, (dx+dy)/13.0+0.25)
				br := 90 + 160*base + 90*hl - 90*sh
				if br < 40 {
					br = 40
				}
				if br > 255 {
					br = 255
				}
				// 玻璃折射:亮部偏白,暗部偏状态色
				wb := 0.25 + 0.45*hl // 白色混合度(高光处更白)
				img.Set(x, y, color.RGBA{
					R: u8min(cr*(br/255.0)*(1-wb) + 255*wb),
					G: u8min(cg*(br/255.0)*(1-wb) + 255*wb),
					B: u8min(cb*(br/255.0)*(1-wb) + 255*wb),
					A: 255,
				})
			}
			// 亮描边(玻璃边缘反光,上缘更亮)
			if dist > 6.5 && dist <= 7.1 {
				edgeBright := 0.75 + 0.35*math.Max(0, -dy/2.5)
				img.Set(x, y, color.RGBA{
					R: u8min(cr*edgeBright + 60),
					G: u8min(cg*edgeBright + 60),
					B: u8min(cb*edgeBright + 60),
					A: 255,
				})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// u8min float→uint8 且截断到 255
func u8min(v float64) uint8 {
	if v > 255 {
		return 255
	}
	if v < 0 {
		return 0
	}
	return uint8(v)
}

// dotIconAnim 动态状态球动画�?
//   - spin(黄·启动中):玻璃质感圆环 + 137° 缺口旋转(加载�?
//   - pulse(绿·运行中/蓝·闲�?:玻璃�?+ 外发光正弦呼�?明暗差大)
func dotIconAnim(r, g, b uint8, frame, total int, mode string) []byte {
	if mode == "pulse" {
		p := float64(frame) / float64(total)
		halo := uint8(45 + 130*math.Sin(p*math.Pi))
		return dotIconBeauty(r, g, b, halo)
	}
	// spin:玻璃�?+ 缺口旋转
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	gapCenter := float64(frame) / float64(total) * 2 * math.Pi
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			dx, dy := float64(x)-7.5, float64(y)-7.5
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist < 4.0 || dist > 7.0 {
				continue
			}
			ang := math.Atan2(dy, dx)
			diff := math.Mod(ang-gapCenter+math.Pi*2, math.Pi*2)
			if diff <= 1.2 || diff >= math.Pi*2-1.2 {
				continue // 缺口
			}
			// 环渐�?玻璃�?内外�?中段�?
			edge := 1 - math.Abs(dist-5.5)/1.5
			img.Set(x, y, color.RGBA{
				R: u8min(float64(r)*(0.6+0.55*edge) + 70),
				G: u8min(float64(g)*(0.6+0.55*edge) + 70),
				B: u8min(float64(b)*(0.6+0.55*edge) + 70),
				A: 255,
			})
		}
	}
	// 环外发光
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			dx, dy := float64(x)-7.5, float64(y)-7.5
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > 7.0 && dist <= 10 {
				f := 1 - dist/10.5
				a := uint8(70 * f)
				if a > 0 {
					img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: a})
				}
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// comfyStatusLight 托盘 ComfyUI 状态灯(图标 + 文字):
// �?已停�?离线无进�? �?启动�?离线但端口有进程) �?运行�?在线且队列有任务) �?闲置�?在线空闲)
// 已由 comfyProbeState 取代(带动画模�?�?

// closeMainWindow 关闭管理主窗�?子进�?WebView2)。用 FindWindowW 精确匹配标题
// (主窗�?title 恒为 "NiliX",灵动岛为 "NiliX HUD")——不�?EnumWindows 枚举:
// �?systray 退出回调里枚举会跨进程 GetWindowTextW,偶发挂起导致"�?X 卡住"(托盘图标已删、进程不退)�?
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
	// 正常退出标�?看门狗守护进程据此判断——存在该标记=用户主动退出,不重启
	// 崩溃/被强杀不会�?onExit,无标�?�?看门狗自动重�?
	_ = os.MkdirAll("logs", 0755)
	_ = os.WriteFile(filepath.Join("logs", "graceful_exit"), []byte(time.Now().Format(time.RFC3339)), 0644)
	closeMainWindow() // 全退(灵动岛 X / 托盘结束应用):一并关闭管理主窗口
	log.Println("NiliX 已退出")
}

// runWatchdog 看门狗守护进�?NiliX.exe --watchdog <主进程PID> [原参�?..]):
// 监控主进程句�?退出后检�?graceful_exit 标记——有(用户主动退�?则不重启并清标记;
// �?崩溃/被强杀/渲染 panic 未兜�?则记录并自动重启 NiliX(原参�?�?
func runWatchdog() {
	pid, err := strconv.Atoi(os.Args[2])
	if err != nil || pid <= 0 {
		return
	}
	marker := filepath.Join("logs", "graceful_exit")
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// 主进程已不在(可能启动即崩):异常则重�?
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
	// 异常退�?记录并自动重�?
	f, _ := os.OpenFile(filepath.Join("logs", "watchdog.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if f != nil {
		_, _ = f.WriteString("[" + time.Now().Format("2006-01-02 15:04:05") + "] 主进程异常退出(PID " + strconv.Itoa(pid) + "),自动重启\n")
		_ = f.Close()
	}
	restartNiliX()
}

// restartNiliX 看门狗重启主进程(os.Args[1:3] = --watchdog <pid>,原参数从 3 开�?
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

// startZCode 启动 ZCode 桌面端（已在运行则跳过）�?
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

// stopZCode 停止 ZCode 桌面端（结束全部 ZCode.exe 进程树）�?
func stopZCode() error {
	if !sysmon.ZCodeRunning() {
		return errors.New("ZCode 未运行")
	}
	cmd := exec.Command("taskkill", "/IM", "ZCode.exe", "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// stopBot 停止 bot 运行时（kill �?ZCode �?5s 自动重建接管）�?
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

