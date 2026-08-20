package api

// ZCode / BOT 控制(胶囊服务按钮 HTTP 化)。
// 原版胶囊用 Go 绑定(island.Actions.StartZCode 等);wails 胶囊加载外部 URL 无绑定,
// 经 HTTP 端点调用——重构不能阉割功能,这里恢复 ZCode 启停与 Bot 停止。

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"nilix/internal/sysmon"
)

// ZCodeStart 启动 ZCode 桌面端(已在运行则跳过)。
func ZCodeStart() error {
	exe := `C:\Mi\Ai\ZCode\ZCode.exe`
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	if sysmon.ZCodeRunning() {
		return nil
	}
	return exec.Command(exe).Start()
}

// ZCodeStop 停止 ZCode 桌面端(结束全部 ZCode.exe 进程树)。
func ZCodeStop() error {
	if !sysmon.ZCodeRunning() {
		return errors.New("ZCode 未运行")
	}
	cmd := exec.Command("taskkill", "/IM", "ZCode.exe", "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// BotStop 停止 bot 运行时(kill 后 ZCode 约 5s 自动重建接管)。
func BotStop() error {
	pid := sysmon.BotPID()
	if pid == 0 {
		return errors.New("BOT 未运行")
	}
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// registerZcodeRoutes 胶囊服务按钮的 HTTP 端点(与原版 isl island.Actions 一一对应)
func (s *Server) registerZcodeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/zcode/start", func(w http.ResponseWriter, r *http.Request) {
		if err := ZCodeStart(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/zcode/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := ZCodeStop(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/bot/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := BotStop(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	// restart 同 stop(与原版 RestartBot 绑定 stopBot 一致:kill 后 ZCode 自动重建接管)
	mux.HandleFunc("POST /api/bot/restart", func(w http.ResponseWriter, r *http.Request) {
		if err := BotStop(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}
