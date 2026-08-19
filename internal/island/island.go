// Package island 实现灵动岛悬浮胶囊（WebView2）：顶部居中胶囊，悬停展开完整监测面板。
// 从 sysmon-widget 的悬浮窗实现适配精简而来。
package island

import (
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
)

// ---- Win32(user32.dll / gdi32.dll / kernel32.dll) ----
var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procSetDpiAwareness  = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForWindow  = user32.NewProc("GetDpiForWindow")

	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	procSetWindowRgn       = user32.NewProc("SetWindowRgn")

	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	procLoadImage       = user32.NewProc("LoadImageW")
	procSendMessage     = user32.NewProc("SendMessageW")
)

const (
	imageIcon = 1
	lrDefault = 0
	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
)

// setWindowIcon 从 exe 内嵌资源加载图标并设置窗口。
func setWindowIcon(hwnd uintptr) {
	hmod, _, _ := procGetModuleHandle.Call(0)
	hBig, _, _ := procLoadImage.Call(hmod, 1, imageIcon, 0, 0, lrDefault)
	hSmall, _, _ := procLoadImage.Call(hmod, 1, imageIcon, 16, 16, lrDefault)
	if hBig != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, hBig)
	}
	if hSmall != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, hSmall)
	}
}

// EnablePerMonitorDPI 高 DPI 感知：避免窗口被系统缩放。必须在任何窗口创建前调用。
func EnablePerMonitorDPI() {
	procSetDpiAwareness.Call(^uintptr(3))
}

const (
	gwlStyle   = -16
	gwlExStyle = -20

	wsCaption      = 0x00C00000
	wsThickFrame   = 0x00040000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsMaximizeBox  = 0x00010000
	wsBorder       = 0x00800000
	wsExToolWindow = 0x00000080

	smXVirtual  = 76
	smCXVirtual = 78
)

const hwndTopmost = ^uintptr(0)

// 灵动岛双形态：收起胶囊 / 展开面板。
const (
	miniW, miniH = 300, 44
	fullW, fullH = 380, 380
	miniR        = 22
	fullR        = 0
	islandTop    = 0
)

type rect struct {
	Left, Top, Right, Bottom int32
}

var (
	islandExpanded bool
	islandCurW     = miniW
	islandCurH     = miniH
	islandCurR     = miniR
)

func getWindowLong(hwnd uintptr, index int32) uintptr {
	r, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(index))
	return r
}

func setWindowLong(hwnd uintptr, index int32, value uintptr) {
	_, _, _ = procSetWindowLongPtr.Call(hwnd, uintptr(index), value)
}

func setWindowPos(hwnd uintptr, insertAfter uintptr, x, y, cx, cy int32, flags uintptr) {
	_, _, _ = procSetWindowPos.Call(hwnd, insertAfter, uintptr(x), uintptr(y), uintptr(cx), uintptr(cy), flags)
}

func applyWindowStyle(hwnd uintptr) {
	style := getWindowLong(hwnd, gwlStyle)
	style &^= wsCaption | wsThickFrame | wsSysMenu | wsMinimizeBox | wsMaximizeBox | wsBorder
	setWindowLong(hwnd, gwlStyle, style)

	ex := getWindowLong(hwnd, gwlExStyle)
	ex |= wsExToolWindow
	setWindowLong(hwnd, gwlExStyle, ex)

	repositionIsland(hwnd, miniW, miniH)
	applyIslandShape(hwnd, miniR)
	setWindowIcon(hwnd)
}

func repositionIsland(hwnd uintptr, w, h int) {
	vx, _, _ := procGetSystemMetrics.Call(smXVirtual)
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtual)
	x := int32(vx) + (int32(vw)-int32(w))/2
	setWindowPos(hwnd, hwndTopmost, x, islandTop, int32(w), int32(h), 0)
	islandCurW, islandCurH = w, h
}

func applyIslandShape(hwnd uintptr, radius int) {
	rc := new(rect)
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(rc)))
	w := int(rc.Right - rc.Left)
	h := int(rc.Bottom - rc.Top)
	if w <= 0 || h <= 0 {
		return
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	scale := float64(uintptr(dpi)) / 96.0
	r := int(float64(radius)*scale + 0.5)
	hrgn, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(w), uintptr(h), uintptr(r*2), uintptr(r*2))
	if hrgn != 0 {
		procSetWindowRgn.Call(hwnd, hrgn, 1)
	}
	islandCurR = radius
}

func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// islandAnimMu 动画互斥:连续悬停展开/收起时,后一个动画等前一个结束再启动,
// 避免多个 goroutine 并发互写 islandCurW/H/R,窗口停在中间尺寸
var islandAnimMu sync.Mutex

func islandAnimate(hwnd uintptr, toW, toH, toR, steps int) {
	go func() {
		islandAnimMu.Lock()
		defer islandAnimMu.Unlock()
		fromW, fromH, fromR := islandCurW, islandCurH, islandCurR
		for i := 1; i <= steps; i++ {
			e := easeInOutCubic(float64(i) / float64(steps))
			w := fromW + int(float64(toW-fromW)*e+0.5)
			h := fromH + int(float64(toH-fromH)*e+0.5)
			r := fromR + int(float64(toR-fromR)*e+0.5)
			repositionIsland(hwnd, w, h)
			applyIslandShape(hwnd, r)
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func setIsland(wv webview.WebView, expanded bool, w, h int) {
	if expanded == islandExpanded {
		return
	}
	islandExpanded = expanded
	hwnd := uintptr(wv.Window())
	if expanded {
		if w <= 0 {
			w = fullW
		}
		if h <= 0 {
			h = fullH
		}
		islandAnimate(hwnd, w, h, fullR, 24)
	} else {
		islandAnimate(hwnd, miniW, miniH, miniR, 24)
	}
}

// Actions 灵动岛 HUD 服务按钮的动作集合，由调用方（main）注入，避免 island 包反向依赖业务包。
type Actions struct {
	StartComfy func() error
	StopComfy  func() error
	OpenComfy  func()
	OpenKB     func()
	StartZCode func() error
	StopZCode  func() error
	StopBot    func() error
	RestartBot func() error
}

// Run 启动灵动岛悬浮胶囊（阻塞）。onClose 在用户点击关闭时回调（用于退出服务）。
func Run(islandURL string, onClose func(), a Actions) error {
	// WebView2 的消息循环(GetMessage)是线程相关的，必须锁 OS 线程，
	// 否则窗口创建与消息泵可能被调度到不同线程导致窗口卡死无响应。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("NiliX HUD") // 与管理主窗口(NiliX)区分,供窗口枚举识别
	w.SetSize(miniW, miniH, webview.HintNone)
	w.SetTransparent() // WebView2 背景透明，消除胶囊圆角外的白色块

	_ = w.Bind("setIsland", func(expanded bool, wd, ht int) error {
		setIsland(w, expanded, wd, ht)
		return nil
	})
	_ = w.Bind("closeWin", func() error {
		if onClose != nil {
			onClose()
		}
		w.Terminate()
		return nil
	})
	// 服务按钮：ComfyUI 启停/访问 + KB 访问。返回 error 的绑定会在 JS 侧形成 Promise。
	_ = w.Bind("startComfy", func() error {
		if a.StartComfy == nil {
			return nil
		}
		return a.StartComfy()
	})
	_ = w.Bind("stopComfy", func() error {
		if a.StopComfy == nil {
			return nil
		}
		return a.StopComfy()
	})
	_ = w.Bind("openComfy", func() {
		if a.OpenComfy != nil {
			a.OpenComfy()
		}
	})
	_ = w.Bind("openKB", func() {
		if a.OpenKB != nil {
			a.OpenKB()
		}
	})
	_ = w.Bind("startZCode", func() error {
		if a.StartZCode == nil {
			return nil
		}
		return a.StartZCode()
	})
	_ = w.Bind("stopZCode", func() error {
		if a.StopZCode == nil {
			return nil
		}
		return a.StopZCode()
	})
	_ = w.Bind("stopBot", func() error {
		if a.StopBot == nil {
			return nil
		}
		return a.StopBot()
	})
	_ = w.Bind("restartBot", func() error {
		if a.RestartBot == nil {
			return nil
		}
		return a.RestartBot()
	})

	w.Navigate(islandURL)
	// 白窗修复:不能创建即隐藏——WebView2 在隐藏父窗口下创建环境,完成回调会收到
	// nil 环境指针导致进程崩溃(实测 panic)。改为窗口立即显示 + SetTransparent
	// 就绪重试:控制器一创建(约首帧前)就应用透明背景,白底来不及显示。
	go func() {
		for i := 0; i < 120 && !w.TransparentOK(); i++ {
			w.SetTransparent()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	w.Dispatch(func() {
		applyWindowStyle(uintptr(w.Window()))
	})
	w.Run()
	return nil
}
