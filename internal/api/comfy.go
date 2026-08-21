package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
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
// 审计 F7:用 atomic.Pointer 快照替代裸 struct 字段——SetComfyParams(写)与
// probeComfy/currentStartup/newManjuCtx(读)跨 goroutine 并发,裸读写是数据竞争
// (go build -race 可检出)
type comfyParamsT struct{ url, in, out string }

var comfyParamsVal atomic.Pointer[comfyParamsT]

// comfyParams 读取当前生效参数快照
func comfyParams() comfyParamsT {
	if p := comfyParamsVal.Load(); p != nil {
		return *p
	}
	return comfyParamsT{url: comfyURL}
}

// SetComfyParams 由 main 在加载 settings.json 后调用,统一注入 ComfyUI 启动参数。
func SetComfyParams(url, inputDir, outputDir string) {
	cur := comfyParams()
	if url != "" {
		cur.url = url
	}
	if inputDir != "" {
		cur.in = inputDir
	}
	if outputDir != "" {
		cur.out = outputDir
	}
	comfyParamsVal.Store(&cur)
}

// ComfyURL 当前生效的 ComfyUI 地址(托盘/通知等零散入口共用,改设置即同步)
func ComfyURL() string { return comfyParams().url }

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
	cp := comfyParams()
	client := http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(cp.url + "/")
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
		Online: true, Title: title, URL: cp.url, Pid: findPortPID(currentPort()),
		LogPath: comfyLogPath, LogTail: comfyLogTail(), Startup: currentStartup(),
	}
}

// currentPort 从生效的 comfy_url 解析端口(默认 8190)。
func currentPort() string {
	u, err := neturl.Parse(comfyParams().url)
	if err != nil || u.Port() == "" {
		return "8190"
	}
	return u.Port()
}

// currentStartup 汇总当前生效的启动参数(供 HUD / Comfy 页面展示,单一数据源)。
func currentStartup() StartupInfo {
	cp := comfyParams()
	in, out := cp.in, cp.out
	if in == "" {
		in = filepath.Join(ComfySharedDir, "input")
	}
	if out == "" {
		out = filepath.Join(ComfySharedDir, "output")
	}
	return StartupInfo{
		URL: cp.url, Root: ComfyRootDir, Shared: ComfySharedDir,
		Input: in, Output: out, Python: filepath.Join(ComfyRootDir, ".venv", "Scripts", "python.exe"),
		Port: currentPort(),
	}
}

func startComfy() error {
	py := filepath.Join(ComfyRootDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(py); err != nil {
		return err
	}
	cp := comfyParams()
	in, out := cp.in, cp.out
	if in == "" {
		in = filepath.Join(ComfySharedDir, "input")
	}
	if out == "" {
		out = filepath.Join(ComfySharedDir, "output")
	}
	// 端口自动避让(审计升级):Windows 的 IP Helper(iphlpsvc)等服务可能动态占用 8190,
	// ComfyUI 绑定失败即退出 → NiliX 反复自动拉起 → 多实例抢端口 → 黑窗闪退循环。
	// 1) 若 8190 已被 ComfyUI 自身占用(本服务或外部启动) → 直接复用,不起新实例;
	// 2) 若 8190 被非 ComfyUI 进程占用 → 探测 8190-8209 找空闲端口,
	//    期间若发现某端口已有可用 ComfyUI(HTTP 可达) → 直接复用它,避免重复实例;
	// 3) 都无 → 用空闲端口启动,同步更新生效参数(ComfyURL/探测/停止全链路跟随)。
	port := currentPort()
	if pid := findPortPID(port); pid > 0 && comfyProcPID.Load() != int32(pid) {
		oldPort := port
		foundExisting := false
		for try := 8190; try <= 8209; try++ {
			u := "http://127.0.0.1:" + strconv.Itoa(try)
			if probeComfyURL(u) {
				// 已有 ComfyUI 在跑:直接复用,不重复启动
				SetComfyParams(u, in, out)
				cp = comfyParams()
				log.Printf("端口 %s 被占用(PID %d),复用已运行的 ComfyUI %s", oldPort, pid, u)
				foundExisting = true
				break
			}
			if findPortPID(strconv.Itoa(try)) == 0 {
				port = strconv.Itoa(try)
				break
			}
		}
		if !foundExisting && port != oldPort {
			newURL := "http://127.0.0.1:" + port
			SetComfyParams(newURL, in, out)
			cp = comfyParams()
			log.Printf("端口 %s 被占用(PID %d,可能是系统服务),ComfyUI 改用 %s", oldPort, pid, port)
		}
	}
	args := []string{
		filepath.Join(ComfyRootDir, "main.py"),
		"--listen", "0.0.0.0", // 局域网设备可经 http://<本机IP>:8190 访问
		"--port", port,
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
	if err := cmd.Start(); err != nil {
		return err
	}
	// 审计 H4:记录本服务启动的 PID,停止时优先按 PID 杀(此前按端口 taskkill 会误杀
	// 占用同端口的无关进程;也防"改端口后停不掉自己的实例")
	comfyProcPID.Store(int32(cmd.Process.Pid))
	return nil
}

// comfyProcPID 本服务启动的 ComfyUI 进程 PID(0=非本服务启动/未启动)
// 审计 F7:启动/停止/探测跨 goroutine 并发,裸 int 读写是数据竞争

// probeComfyURL 探测某地址是否已有可用的 ComfyUI(端口避让时复用,防重复实例)
func probeComfyURL(url string) bool {
	client := http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(url + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
var comfyProcPID atomic.Int32

func stopComfy() error {
	// 优先杀本服务启动的进程(审计 H4:防误杀同端口第三方进程)
	if pid := comfyProcPID.Load(); pid > 0 {
		comfyProcPID.Store(0)
		cmd := exec.Command("taskkill", "/PID", strconv.FormatInt(int64(pid), 10), "/T", "/F")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	// 兜底:按当前生效端口找(可能用户手动重启过 ComfyUI,进程非本服务启动)
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
	c := newComfyClient(comfyParams().url)
	_, err := c.online()
	return err == nil
}

// ComfyPortPID 生效端口上监听的进程 PID(0=无进程)。托盘状态灯判"启动中"用:
// 离线但端口有进程 = 正在启动(黄);离线且无进程 = 已停止(红)。
func ComfyPortPID() int {
	return findPortPID(currentPort())
}

// ComfyBusy ComfyUI 队列是否有任务(running/pending 任一非空)。
// 托盘状态灯判"运行中(绿) vs 闲置(蓝)"用。
func ComfyBusy() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(comfyParams().url + "/queue")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var q struct {
		QueueRunning []map[string]any `json:"queue_running"`
		QueuePending []map[string]any `json:"queue_pending"`
	}
	if json.Unmarshal(body, &q) != nil {
		return false
	}
	return len(q.QueueRunning) > 0 || len(q.QueuePending) > 0
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
		log.Printf("[comfy] 启动失败: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": comfyLogPath})
}

func (s *Server) handleComfyStop(w http.ResponseWriter, r *http.Request) {
	if err := stopComfy(); err != nil {
		log.Printf("[comfy] 停止失败: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
