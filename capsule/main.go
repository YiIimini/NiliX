package main

// NiliX 灵动岛悬浮胶囊(wails v3 实现)。
// 解决 go-webview2 直连 WebView2 的透明/无边框渲染 bug(黑底/画刷色):wails 的 Windows
// 后端成熟处理 WebView2 透明(A:0) + WS_EX_LAYERED + DirectComposition + WM_NC* 消息。
//
// 页面加载 NiliX 主服务 8787 的 /island/(外部 URL,无 wails runtime 注入),因此:
//   - 窗口尺寸(展开/收起)通过本进程 HTTP 端口 127.0.0.1:8788 控制,带分段动画
//     (页面 JS fallback:setIsland → GET /size?w=&h=)
//   - 退出走 GET /close → app.Quit(确保进程真退出)
//   - 数据/按钮走 8787 的 HTTP API(fetch 同源,无需绑定)

import (
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	smXVirtual           = 76
	smCXVirtual          = 78
)

const (
	miniW    = 300
	miniH    = 44
	animStep = 12 // 动画步数
	animMS   = 12 // 每步间隔毫秒
)

// animGen 动画代数:新的展开/收起请求递增,旧动画检测到代数变化立即中止(防动画打架)
var animGen int32

func main() {
	app := application.New(application.Options{
		Name:        "NiliX-Capsule",
		Description: "NiliX LingDong Island Capsule",
	})

	vx, _, _ := procGetSystemMetrics.Call(uintptr(smXVirtual))
	vw, _, _ := procGetSystemMetrics.Call(uintptr(smCXVirtual))
	posX := int(int32(vx)) + (int(int32(vw))-miniW)/2

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "NiliX HUD",
		Width:            miniW,
		Height:           miniH,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		// 透明背景:页面 CSS 圆角 + body transparent → 圆角外透桌面
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		URL:              "http://127.0.0.1:8787/island/",
		InitialPosition:  application.WindowXY,
		X:                posX,
		Y:                0,
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
			// 胶囊是悬浮窗,不占任务栏(HiddenOnTaskbar)——避免与主应用窗口在任务栏
			// 显示两个 NiliX 图标
			HiddenOnTaskbar: true,
		},
	})

	// 本地控制端口(页面 8787 加载,跨源响应带 CORS 头)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /size", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		q := r.URL.Query()
		wid, hgt := miniW, miniH
		if v, err := strconv.Atoi(q.Get("w")); err == nil && v >= 200 && v <= 800 {
			wid = v
		}
		if v, err := strconv.Atoi(q.Get("h")); err == nil && v >= 40 && v <= 900 {
			hgt = v
		}
		animateWindow(win, wid, hgt)
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /close", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		// 必须 app.Quit() 而非 win.Close():确保胶囊进程真退出(win.Close 可能只关窗口)
		go app.Quit()
	})
	go func() {
		if err := http.ListenAndServe("127.0.0.1:8788", mux); err != nil {
			log.Printf("capsule 控制端口退出: %v", err)
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// animateWindow 展开/收起动画:从当前尺寸分段 SetSize/SetPosition 过渡到目标,
// 窗口居中(宽度变化 → x 同步)。新请求递增代数,旧动画中止。
func animateWindow(win *application.WebviewWindow, toW, toH int) {
	vw, _, _ := procGetSystemMetrics.Call(uintptr(smCXVirtual))
	sw, sh := win.Size()
	if sw <= 0 {
		sw = miniW
	}
	if sh <= 0 {
		sh = miniH
	}
	gen := atomic.AddInt32(&animGen, 1)
	go func() {
		steps := animStep
		if sw == toW && sh == toH {
			steps = 1
		}
		for i := 1; i <= steps; i++ {
			if atomic.LoadInt32(&animGen) != gen {
				return // 被新的展开/收起请求取代
			}
			w := sw + (toW-sw)*i/steps
			h := sh + (toH-sh)*i/steps
			x := (int(int32(vw)) - w) / 2
			win.SetPosition(x, 0)
			win.SetSize(w, h)
			time.Sleep(animMS * time.Millisecond)
		}
	}()
}
