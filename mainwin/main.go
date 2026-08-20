package main

// NiliX 主窗口(wails v3 实现)。
// 重构:go-webview2(--mainwin 子进程) → wails WebViewWindow。
//   - Frameless(去系统标题栏):前端自定义标题栏(app-region: drag 拖拽,
//     依赖 wails WindowsWindow.NonClientRegionSupport → WebView2 原生非客户区)
//   - Solid 不透明背景(主窗口无透明需求,渲染稳定)
//   - 加载 NiliX 管理页 http://127.0.0.1:8787
//   - 最小化/关闭经本地 HTTP 8799 控制(外部 URL 页面无 wails runtime)
//   - 窗口尺寸/位置记忆(同旧 mainwin.json 语义,存 exe 目录)

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	defW = 1400
	defH = 900
)

func main() {
	app := application.New(application.Options{
		Name:        "NiliX",
		Description: "NiliX 小说转视频工作台",
	})

	w, h, x, y := loadWinState()

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "NiliX",
		Width:            w,
		Height:           h,
		X:                x,
		Y:                y,
		Frameless:        true, // 去系统标题栏,前端自定义
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGB(10, 15, 30),
		URL:              "http://127.0.0.1:8787",
		Windows: application.WindowsWindow{
			// 前端标题栏用 app-region: drag 拖拽窗口(WebView2 原生非客户区支持)
			NonClientRegionSupport: true,
		},
	})

	// 本地控制端口(页面 8787 加载,无 wails runtime):最小化/关闭/最大化
	mux := http.NewServeMux()
	mux.HandleFunc("GET /minimize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		go win.Minimise()
	})
	mux.HandleFunc("GET /maximize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		go win.ToggleMaximise()
	})
	mux.HandleFunc("GET /close", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		go win.Close()
	})
	go func() {
		if err := http.ListenAndServe("127.0.0.1:8799", mux); err != nil {
			log.Printf("mainwin 控制端口退出: %v", err)
		}
	}()

	// 窗口移动/缩放落盘记忆(与旧 mainwin.json 语义一致,存 exe 目录)
	statePath := filepath.Join(exeDir(), "mainwin.json")
	save := func() {
		w2, h2 := win.Size()
		px, py := win.Position()
		b, _ := json.Marshal(map[string]int{"w": w2, "h": h2, "x": px, "y": py})
		_ = os.WriteFile(statePath, b, 0644)
	}
	win.OnWindowEvent(events.Common.WindowDidMove, func(_ *application.WindowEvent) { save() })
	win.OnWindowEvent(events.Common.WindowDidResize, func(_ *application.WindowEvent) { save() })

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// loadWinState 读取记忆的窗口尺寸/位置(缺省居中 defW×defH)
func loadWinState() (w, h, x, y int) {
	w, h = defW, defH
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
