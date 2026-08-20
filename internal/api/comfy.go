package api

import (
	"errors"
	"io"
	"net/http"
	neturl "net/url"
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
	comfyLogPath    = `logs/comfy.log` // 相对 exe 目录(启动时已 Chdir;随自包含目录迁移)
	comfyDesktopLog = `C:\Mi\Ai\Comfy Desktop\logs\_comfyui_server.log`
)

var (
	ansiRe         = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	harnessTitleRe = regexp.MustCompile(`(?is)<title[^>]*>\s*([^<]*?)\s*</title>`)
)

// comfyParams 启动参数单一数据源:启动时由 main 用 settings.json 注入,
// HUD 卡片 / Comfy 页面 / 实际启动命令共用同一份,保证同步。
var comfyParams = struct {
	url, in, out string
}{url: comfyURL, in: "", out: ""}

// SetComfyParams 由 main 在加载 settings.json 后调用,统一注入 ComfyUI 启动参数。
func SetComfyParams(url, inputDir, outputDir string) {
	if url != "" {
		comfyParams.url = url
	}
	if inputDir != "" {
		comfyParams.in = inputDir
	}
	if outputDir != "" {
		comfyParams.out = outputDir
	}
}

// ComfyURL 当前生效的 ComfyUI 地址(托盘/通知等零散入口共用,改设置即同步)
func ComfyURL() string { return comfyParams.url }

// ServiceStatus 服务状态（含进程、日志与启动参数信息）。
type ServiceStatus struct {
	Online  bool   `json:"online"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Pid     int    `json:"pid"`
	LogPath string `json:"logPath"`
	LogTail string `json:"logTail"`
	Err     string `json:"err"`
	// Startup 启动参数(与 HUD/Comfy 页面展示共用,保证同步)
	Startup StartupInfo `json:"startup"`
}

// StartupInfo 当前生效的 ComfyUI 启动参数。
type StartupInfo struct {
	URL     string `json:"url"`
	Root    string `json:"root"`
	Shared  string `json:"shared"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	Python  string `json:"python"`
	Port    string `json:"port"`
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
	resp, err := client.Get(comfyParams.url + "/")
	if err != nil {
		return ServiceStatus{Online: false, Err: "offline", LogPath: comfyLogPath, LogTail: comfyLogTail(), Startup: currentStartup()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Online: false, Err: "http " + http.StatusText(resp.StatusCode), LogPath: comfyLogPath, LogTail: comfyLogTail(), Startup: currentStartup()}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	title := ""
	if m := harnessTitleRe.FindSubmatch(body); len(m) > 1 {
		title = strings.TrimSpace(string(m[1]))
	}
	return ServiceStatus{
		Online: true, Title: title, URL: comfyParams.url, Pid: findPortPID(currentPort()),
		LogPath: comfyLogPath, LogTail: comfyLogTail(), Startup: currentStartup(),
	}
}

// currentPort 从生效的 comfy_url 解析端口(默认 8190)。
func currentPort() string {
	u, err := neturl.Parse(comfyParams.url)
	if err != nil || u.Port() == "" {
		return "8190"
	}
	return u.Port()
}

// currentStartup 汇总当前生效的启动参数(供 HUD / Comfy 页面展示,单一数据源)。
func currentStartup() StartupInfo {
	in, out := comfyParams.in, comfyParams.out
	if in == "" {
		in = filepath.Join(ComfySharedDir, "input")
	}
	if out == "" {
		out = filepath.Join(ComfySharedDir, "output")
	}
	return StartupInfo{
		URL: comfyParams.url, Root: ComfyRootDir, Shared: ComfySharedDir,
		Input: in, Output: out, Python: filepath.Join(ComfyRootDir, ".venv", "Scripts", "python.exe"),
		Port: currentPort(),
	}
}

func startComfy() error {
	py := filepath.Join(ComfyRootDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(py); err != nil {
		return err
	}
	in, out := comfyParams.in, comfyParams.out
	if in == "" {
		in = filepath.Join(ComfySharedDir, "input")
	}
	if out == "" {
		out = filepath.Join(ComfySharedDir, "output")
	}
	args := []string{
		filepath.Join(ComfyRootDir, "main.py"),
		"--listen", "127.0.0.1",
		"--port", currentPort(),
		"--disable-auto-launch",
		"--output-directory", out,
		"--input-directory", in,
	}
	cmd := exec.Command(py, args...)
	cmd.Dir = ComfyRootDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	// 中文 Windows 默认 ANSI 编码(GBK):重定向 stdout 后 Python 打印 emoji 会抛
	// UnicodeEncodeError 直接崩溃(实测 ComfyUI 启动秒死、日志全空的根因)。强制 UTF-8。
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
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
	// 用当前生效端口找进程(与启动一致;写死 8190 会导致改端口后停不掉真进程)
	pid := findPortPID(currentPort())
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

// ComfyOnline 检查 ComfyUI 是否在线(应用启动自动拉起/渲染前兜底用;地址=生效配置端口)
func ComfyOnline() bool {
	c := newComfyClient(comfyParams.url)
	_, err := c.online()
	return err == nil
}

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
