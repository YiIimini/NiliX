package api

// DeepSeek Harness 服务监控(探测/启动/重启),与 ComfyUI 同一模式。
// Harness = AI 智能体工作台(DSH Web,默认 127.0.0.1:3080)。
// 启动命令与工作目录来自本机安装(restart-dsh.bat 同源):
//   node <DSH>/node_modules/@deepseek-ai/dsh/lib/bin.js web --no-open  (cwd: <DSH>)

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Harness 服务常量(端口/安装目录/启动脚本路径)
const (
	harnessURL     = "http://127.0.0.1:3080"
	harnessNodeExe = `C:\Mi\Ai\nodejs\node.exe`
	harnessRoot    = `C:\Mi\Ai\DeepSeekHarness`
	harnessLogPath = `logs/harness.log` // 相对 exe 目录(启动时已 Chdir)
)

// harnessProcPID 本服务启动的 Harness 进程 PID(0=非本服务启动/未启动)
var harnessProcPID int

// HarnessStatus Harness 服务状态(与 sysmon 探测/灵动岛展示共用)
type HarnessStatus struct {
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
	Err     string `json:"err,omitempty"`
	URL     string `json:"url"`
	LogPath string `json:"logPath,omitempty"`
	LogTail string `json:"logTail,omitempty"`
	Pid     int    `json:"pid,omitempty"`
	// Startup 启动信息(前端展示/重启用)
	Startup struct {
		URL    string `json:"url"`
		Node   string `json:"node"`
		Root   string `json:"root"`
		Port   string `json:"port"`
		Binary string `json:"binary"`
	} `json:"startup"`
}

// probeHarness 探测 Harness 是否在线(HTTP 可达即可;同时抓标题与日志尾部)
func probeHarness() HarnessStatus {
	st := HarnessStatus{URL: harnessURL}
	st.Startup.URL = harnessURL
	st.Startup.Node = harnessNodeExe
	st.Startup.Root = harnessRoot
	st.Startup.Port = "3080"
	st.Startup.Binary = filepath.Join(harnessRoot, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(harnessURL + "/")
	if err != nil {
		st.Err = "offline"
		st.LogPath = harnessLogPath
		st.LogTail = harnessLogTail()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Err = "http " + http.StatusText(resp.StatusCode)
		return st
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	st.Online = true
	if m := harnessTitleRe.FindSubmatch(body); len(m) > 1 {
		st.Version = strings.TrimSpace(string(m[1]))
	}
	st.Pid = findPortPID("3080")
	st.LogPath = harnessLogPath
	st.LogTail = harnessLogTail()
	return st
}

func harnessLogTail() string {
	return ansiRe.ReplaceAllString(tailFile(harnessLogPath), "")
}

// startHarness 启动 Harness 服务(静默后台,日志落 harness.log)
func startHarness() error {
	if _, err := os.Stat(harnessNodeExe); err != nil {
		return errors.New("未找到 Node.js(" + harnessNodeExe + ")")
	}
	bin := filepath.Join(harnessRoot, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if _, err := os.Stat(bin); err != nil {
		return errors.New("未找到 DSH 启动脚本(" + bin + ")")
	}
	args := []string{bin, "web", "--no-open"}
	cmd := exec.Command(harnessNodeExe, args...)
	cmd.Dir = harnessRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	f, err := os.OpenFile(harnessLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return err
	}
	harnessProcPID = cmd.Process.Pid
	return nil
}

// stopHarness 停止 Harness 服务(优先按本服务 PID;兜底按端口)
func stopHarness() error {
	if pid := harnessProcPID; pid > 0 {
		harnessProcPID = 0
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	pid := findPortPID("3080")
	if pid == 0 {
		return errors.New("harness not running")
	}
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// HarnessOnline Harness 是否在线(其他模块快速探测用)
func HarnessOnline() bool {
	client := http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(harnessURL + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// HarnessStart / HarnessRestart 供灵动岛服务按钮调用(包级导出)
func HarnessStart() error {
	if HarnessOnline() {
		return nil
	}
	return startHarness()
}

// HarnessRestart 重启:先停(尽力)再启
func HarnessRestart() error {
	_ = stopHarness()
	time.Sleep(500 * time.Millisecond)
	return startHarness()
}

func (s *Server) handleHarness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, probeHarness())
}

func (s *Server) handleHarnessStart(w http.ResponseWriter, r *http.Request) {
	if probeHarness().Online {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already": true})
		return
	}
	if err := startHarness(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": harnessLogPath})
}

func (s *Server) handleHarnessRestart(w http.ResponseWriter, r *http.Request) {
	if err := HarnessRestart(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": harnessLogPath})
}
