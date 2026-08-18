package main

import (
	"embed"
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
			watchdog.Alert("小说转视频服务", "服务已在运行，请勿重复启动。")
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

	// 灵动岛悬浮胶囊（WebView2）
	go func() {
		time.Sleep(500 * time.Millisecond)
		actions := island.Actions{
			StartComfy: api.ComfyStart,
			StopComfy:  api.ComfyStop,
			OpenComfy:  func() { openBrowser("http://127.0.0.1:8190") },
			OpenKB:     func() { openBrowser(url + "#/overview") },
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
		mHome := systray.AddMenuItem("打开主页", "打开 NiliX 关系图谱主页")
		mNovel := systray.AddMenuItem("小说管理", "打开小说管理")
		mManju := systray.AddMenuItem("漫剧管理", "打开漫剧管理页")
		mComfy := systray.AddMenuItem("ComfyUI", "打开 ComfyUI 页面")
		systray.AddSeparator()
		mAuto := systray.AddMenuItemCheckbox("开机自启", "开机自动启动 NiliX", autostart.Enabled())
		mQuit := systray.AddMenuItem("退出", "停止服务并退出托盘")
		go func() {
			for {
				select {
				case <-mHome.ClickedCh:
					openBrowser(url)
				case <-mNovel.ClickedCh:
					openBrowser(url + "#/novel")
				case <-mManju.ClickedCh:
					openBrowser(url + "#/manju")
				case <-mComfy.ClickedCh:
					openBrowser(url + "#/comfy")
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

func onExit() {
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
