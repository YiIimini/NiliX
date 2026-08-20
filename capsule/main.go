package main

// NiliX 灵动岛悬浮胶囊(wails v3 实现)。
// 解决 go-webview2 直连 WebView2 的透明/无边框渲染 bug(黑底/画刷色):wails 的 Windows
// 后端成熟处理 WebView2 透明(A:0) + WS_EX_LAYERED + DirectComposition + WM_NC* 消息。
//
// 页面加载 NiliX 主服务 8787 的 /island/(外部 URL,无 wails runtime 注入),因此:
//   - 窗口尺寸(展开/收起)通过本进程的 HTTP 端口 127.0.0.1:8788 控制
//     (页面 JS fallback:setIsland → GET /size?w=&h=)
//   - 关闭走 GET /close
//   - 数据/按钮走 8787 的 HTTP API(fetch 同源,无需绑定)

import (
	"log"
	"net/http"
	"strconv"
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	smXVirtual           = 76
	smCXVirtual          = 78
)

const (
	miniW = 300
	// 窗口总高 = 胶囊 44 + 底部 40px 透明投影空间:胶囊的 box-shadow(8px 下移+30px 模糊)
	// 若超出窗口会被 WebView2 渲染边界硬裁,胶囊下方出现"直角边"投影截断。
	miniH   = 84
	expandW = 380
	expandH = 420
)

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
		},
	})

	// 本地控制端口:页面(8787 加载)通过 GET 请求控制胶囊窗口尺寸/关闭(跨源,响应带 CORS 头)
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
		vw, _, _ := procGetSystemMetrics.Call(uintptr(smCXVirtual))
		x := (int(int32(vw)) - wid) / 2
		win.SetPosition(x, 0)
		win.SetSize(wid, hgt)
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /close", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		go win.Close()
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
