package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---- ComfyUI 内嵌服务（从 kb-workbench 适配） ----

const (
	comfyURL        = "http://127.0.0.1:8190"
	comfyRoot       = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Installs\ComfyUI (1)\ComfyUI`
	comfyShared     = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared`
	comfyLogPath    = `C:\Mi\Ai\WorkBench\comfy-server.log`
	comfyDesktopLog = `C:\Mi\Ai\Comfy Desktop\logs\_comfyui_server.log`
)

var (
	ansiRe         = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	harnessTitleRe = regexp.MustCompile(`(?is)<title[^>]*>\s*([^<]*?)\s*</title>`)
)

// ServiceStatus 服务状态（含进程与日志信息）。
type ServiceStatus struct {
	Online  bool   `json:"online"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Pid     int    `json:"pid"`
	LogPath string `json:"logPath"`
	LogTail string `json:"logTail"`
	Err     string `json:"err"`
}

func findPortPID(port string) int {
	cmd := exec.Command("netstat", "-ano")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && strings.HasSuffix(f[1], ":"+port) && f[3] == "LISTENING" {
			if pid, err := strconv.Atoi(f[4]); err == nil && pid > 0 {
				return pid
			}
		}
	}
	return 0
}

func tailFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		if st.Size() > 4096 {
			_, _ = f.Seek(-4096, io.SeekEnd)
		}
		b, _ := io.ReadAll(f)
		return string(b)
	}
	return ""
}

func probeComfy() ServiceStatus {
	client := http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(comfyURL + "/")
	if err != nil {
		return ServiceStatus{Online: false, Err: "offline", LogPath: comfyLogPath, LogTail: comfyLogTail()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Online: false, Err: "http " + http.StatusText(resp.StatusCode), LogPath: comfyLogPath, LogTail: comfyLogTail()}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	title := ""
	if m := harnessTitleRe.FindSubmatch(body); len(m) > 1 {
		title = strings.TrimSpace(string(m[1]))
	}
	return ServiceStatus{
		Online: true, Title: title, URL: comfyURL, Pid: findPortPID("8190"),
		LogPath: comfyLogPath, LogTail: comfyLogTail(),
	}
}

func startComfy() error {
	py := filepath.Join(comfyRoot, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(py); err != nil {
		return err
	}
	args := []string{
		filepath.Join(comfyRoot, "main.py"),
		"--listen", "127.0.0.1",
		"--port", "8190",
		"--disable-auto-launch",
		"--output-directory", filepath.Join(comfyShared, "output"),
		"--input-directory", filepath.Join(comfyShared, "input"),
	}
	cmd := exec.Command(py, args...)
	cmd.Dir = comfyRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	f, err := os.OpenFile(comfyLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	return cmd.Start()
}

func stopComfy() error {
	pid := findPortPID("8190")
	if pid == 0 {
		return errors.New("comfy not running")
	}
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func comfyLogTail() string {
	txt := tailFile(comfyLogPath)
	if strings.TrimSpace(txt) == "" {
		txt = tailFile(comfyDesktopLog)
	}
	return ansiRe.ReplaceAllString(txt, "")
}

// ComfyStart / ComfyStop 供灵动岛服务按钮调用（包级导出，避免 island 包依赖私有逻辑）。
func ComfyStart() error { return startComfy() }
func ComfyStop() error  { return stopComfy() }

func (s *Server) handleComfy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, probeComfy())
}

func (s *Server) handleComfyStart(w http.ResponseWriter, r *http.Request) {
	if st := probeComfy(); st.Online {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already": true})
		return
	}
	if err := startComfy(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": comfyLogPath})
}

func (s *Server) handleComfyStop(w http.ResponseWriter, r *http.Request) {
	if err := stopComfy(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
