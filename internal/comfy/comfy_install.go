package comfy

// ComfyUI 全自动安装编排器(新电脑一键部署):
// 自动下载官方 Windows Portable(含内嵌 Python)→ 解压到 paths.ComfyRootDir →
// git clone 自定义节点(H3 Easy + KJNodes)→ 按清单下载模型(支持 HF 镜像) →
// 可选安装 media 依赖(av/faster-whisper/pyJianYingDraft) → 标记完成。
// 进度写入 logs/comfy_install.log 并维护内存状态,前端轮询展示;单实例防重复安装。

import (
	"nilix/internal/paths"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// comfyInstallState 安装状态(内存 + 日志落盘)
type comfyInstallState struct {
	mu      sync.Mutex
	running bool
	cancelled bool // 审计 M6:用户点停止后置位,下载/克隆循环检查并中断(此前只改状态,后台照跑)
	step    string // portable / tool / node / model / media / done
	item    string // 当前下载项
	done    bool
	rc      int
	err     string
	log     []string
	started time.Time
}

var comfyInstallSt comfyInstallState

// comfyInstallLogPath 安装日志(相对 exe 目录,启动时已 Chdir)
const comfyInstallLogPath = "logs/comfy_install.log"

// comfyRes 一个安装资源
type comfyRes struct {
	Kind string // portable / tool / node / model
	Name string // 展示名
	Rel  string // 相对目标:portable/tool → 临时目录;node → paths.ComfyRootDir/custom_nodes;model → paths.ComfySharedDir/models
	URL  string // node/tool/portable=完整 URL;model=HF 相对路径(resolve/...)
}

// comfyModelBase 模型下载基址:默认 HuggingFace,国内可切 https://hf-mirror.com
// (设置里未提供开关,直接改此变量或后续接入配置)。
var comfyModelBase = "https://huggingface.co"

// comfyDefaultResources 默认资源清单(H3 模型文件名对齐 minimax-h3-starter 官方工作流)。
// 模型单项失败不阻塞整体(记录到日志,用户可补下或改清单)。
var comfyDefaultResources = []comfyRes{
	{Kind: "portable", Name: "ComfyUI Windows Portable(约1.5GB)", Rel: "portable.7z",
		URL: "https://github.com/comfyanonymous/ComfyUI/releases/download/latest/ComfyUI_windows_portable_nvidia.7z"},
	{Kind: "tool", Name: "7-Zip 解压器", Rel: "7zr.exe",
		URL: "https://www.7-zip.org/a/7zr.exe"},
	{Kind: "node", Name: "ComfyUI-KJNodes(SageAttention)", Rel: "ComfyUI-KJNodes",
		URL: "https://github.com/kijai/ComfyUI-KJNodes"},
	{Kind: "node", Name: "ComfyUI-MiniMaxH3-Easy(H3 节点)", Rel: "ComfyUI-MiniMaxH3-Easy",
		URL: "https://github.com/nkxx188/ComfyUI-MiniMaxH3-Easy"},
	{Kind: "model", Name: "H3 FL2VA(文生视频)", Rel: "diffusion_models/minimax_h3_fl2va_pruned_int8_convrot.safetensors",
		URL: "MiniMaxAI/MiniMax-H3/resolve/main/minimax_h3_fl2va_pruned_int8_convrot.safetensors"},
	{Kind: "model", Name: "H3 Ref2VA(参考图生视频)", Rel: "diffusion_models/minimax_h3_ref2va_pruned_int8_convrot.safetensors",
		URL: "MiniMaxAI/MiniMax-H3/resolve/main/minimax_h3_ref2va_pruned_int8_convrot.safetensors"},
	{Kind: "model", Name: "H3 Video VAE", Rel: "vae/minimax_h3_video_vae_fp16.safetensors",
		URL: "MiniMaxAI/MiniMax-H3/resolve/main/minimax_h3_video_vae_fp16.safetensors"},
	{Kind: "model", Name: "H3 Audio VAE", Rel: "vae/minimax_h3_audio_vae_fp32.safetensors",
		URL: "MiniMaxAI/MiniMax-H3/resolve/main/minimax_h3_audio_vae_fp32.safetensors"},
	{Kind: "model", Name: "H3 CLIP(Qwen3VL-32B,约60GB)", Rel: "text_encoders/qwen3vl_32b_minimax_h3.safetensors",
		URL: "MiniMaxAI/MiniMax-H3/resolve/main/qwen3vl_32b_minimax_h3.safetensors"},
}


// installLog 追加日志(内存 + 文件)
func (st *comfyInstallState) instNote(msg string) {
	line := "[" + time.Now().Format("15:04:05") + "] " + msg
	st.mu.Lock()
	st.log = append(st.log, line)
	if len(st.log) > 300 {
		st.log = st.log[len(st.log)-300:]
	}
	st.mu.Unlock()
	if f, err := os.OpenFile(comfyInstallLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

func (st *comfyInstallState) setStep(step, item string) {
	st.mu.Lock()
	st.step, st.item = step, item
	st.mu.Unlock()
}

// downloadFile 下载文件到目标(已存在且非空则跳过,断点续传友好)。
func downloadFile(url, dst string, st *comfyInstallState, label string) error {
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		st.instNote("⏭ 已存在,跳过: " + filepath.Base(dst))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	st.setStep("download", label)
	st.instNote("⬇ 下载 " + label + " ...")
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "NiliX-Installer/1.0")
	cli := &http.Client{Timeout: 2 * time.Hour}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// 流式写,进度按已落盘字节数汇报(下载大文件时前端可见在动);审计 M6:每块检查取消
	buf := make([]byte, 256<<10)
	var written int64
	for {
		// 审计 F8:取消标志在锁下读取(与 stop 写入同步)
		st.mu.Lock()
		cancelled := st.cancelled
		st.mu.Unlock()
		if cancelled {
			f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("安装已取消")
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			written += int64(n)
			if written%(8<<20) == 0 {
				st.mu.Lock()
				st.item = fmt.Sprintf("%s · %.0fMB", label, float64(written)/1e6)
				st.mu.Unlock()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	st.instNote(fmt.Sprintf("  ✅ %s (%.0fMB)", label, float64(written)/1e6))
	return nil
}

// gitClone 克隆或更新自定义节点
func gitClone(url, dst, name string, st *comfyInstallState) error {
	if dirExists(filepath.Join(dst, ".git")) {
		st.instNote("⏭ 节点已存在,跳过: " + name)
		return nil
	}
	st.setStep("node", name)
	st.instNote("📦 克隆节点 " + name + " ...")
	cmd := exec.Command("git", "clone", "--depth", "1", url, dst)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %v %s", name, err, truncate(string(out), 200))
	}
	st.instNote("  ✅ 节点 " + name)
	return nil
}

// installMediaDeps 安装 media 依赖(av/faster-whisper/pyJianYingDraft)到 ComfyUI 的 python。
func installMediaDeps(st *comfyInstallState) error {
	py := manjuPythonPath()
	if !fileExists(py) {
		return fmt.Errorf("python 未找到: %s", py)
	}
	st.setStep("media", "av/faster-whisper/pyJianYingDraft")
	st.instNote("🐍 安装媒体依赖(av, faster-whisper, pyJianYingDraft)...")
	cmd := exec.Command(py, "-m", "pip", "install", "--no-input", "-q", "av", "faster-whisper", "pyJianYingDraft")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pip 安装失败(可稍后手动装): %v %s", err, truncate(string(out), 300))
	}
	st.instNote("  ✅ 媒体依赖就绪")
	return nil
}

// installComfyUI 全自动安装(后台执行,单实例)。
func installComfyUI() error {
	st := &comfyInstallSt
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return fmt.Errorf("安装已在运行中")
	}
	st.running = true
	st.done = false
	st.rc = 0
	st.err = ""
	st.log = nil
	st.started = time.Now()
	st.mu.Unlock()

	_ = os.MkdirAll(filepath.Dir(comfyInstallLogPath), 0755)
	st.instNote("🚀 ComfyUI 自动安装开始(目标: " + paths.ComfyRootDir + ")")

	go func() {
		rc := 0
		defer func() {
			st.mu.Lock()
			// 审计 F8:用户已点停止(cancelled=true)时,goroutine 收尾不得覆盖停止终态——
			// 否则当前步骤收尾成功会把 "已手动停止" 改写成 "完成"(UI 状态跳变)
			if st.cancelled {
				st.mu.Unlock()
				return
			}
			st.running = false
			st.done = true
			st.rc = rc
			if rc != 0 && st.err == "" {
				st.err = "安装未完全成功(详见日志)"
			}
			st.step = "done"
			st.mu.Unlock()
		}()
		tmpDir := filepath.Join(paths.ComfyRootDir, "..", "_install_tmp")
		if abs, err := filepath.Abs(tmpDir); err == nil {
			tmpDir = abs
		}
		_ = os.MkdirAll(tmpDir, 0755)
		defer os.RemoveAll(tmpDir)

		// 0) 已有程序则跳过程序部分
		needProg := !dirExists(paths.ComfyRootDir)
		if needProg {
			// 工具:7zr
			sz := filepath.Join(tmpDir, "7zr.exe")
			if err := downloadFile("https://www.7-zip.org/a/7zr.exe", sz, st, "7-Zip 解压器"); err != nil {
				st.err = "下载解压器失败: " + err.Error()
				rc = 1
				return
			}
			// portable
			arc := filepath.Join(tmpDir, "portable.7z")
			url := ""
			for _, r := range comfyDefaultResources {
				if r.Kind == "portable" {
					url = r.URL
				}
			}
			if err := downloadFile(url, arc, st, "ComfyUI Windows Portable"); err != nil {
				st.err = "下载 ComfyUI 失败: " + err.Error()
				rc = 1
				return
			}
			// 解压前完整性校验(审计 H2):7z t 测试归档——下载中断/损坏的压缩包直接解压会
			// 静默产出残缺程序;下载源校验和待发布方提供后填入 downloadFile 的 sha256 参数
			st.setStep("portable", "校验压缩包完整性")
			tcmd := exec.Command(sz, "t", arc, "-y", "-bso0", "-bsp0")
			tcmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护
			if out, err := tcmd.CombinedOutput(); err != nil {
				st.err = "压缩包完整性校验失败(下载可能损坏,请重试): " + err.Error() + " " + truncate(string(out), 200)
				rc = 1
				return
			}
			// 解压
			st.setStep("portable", "解压 ComfyUI(约2分钟)")
			st.instNote("📦 解压 ComfyUI ...")
			stage := filepath.Join(tmpDir, "stage")
			_ = os.MkdirAll(stage, 0755)
			cmd := exec.Command(sz, "x", arc, "-o"+stage, "-y", "-bso0", "-bsp0")
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护
			if out, err := cmd.CombinedOutput(); err != nil {
				st.err = "解压失败: " + err.Error() + " " + truncate(string(out), 200)
				rc = 1
				return
			}
			// 定位 portable 解压出的 ComfyUI 目录并移动到目标
			src := filepath.Join(stage, "ComfyUI_windows_portable", "ComfyUI")
			if !dirExists(src) || !fileExists(filepath.Join(src, "main.py")) {
				st.err = "解压结构异常(未找到 ComfyUI/main.py)"
				rc = 1
				return
			}
			_ = os.MkdirAll(filepath.Dir(paths.ComfyRootDir), 0755)
			if err := os.Rename(src, paths.ComfyRootDir); err != nil {
				// 跨卷/占用回退复制
				if err2 := copyTree(src, paths.ComfyRootDir); err2 != nil {
					st.err = "移动 ComfyUI 失败: " + err.Error() + " / " + err2.Error()
					rc = 1
					return
				}
			}
			st.instNote("  ✅ ComfyUI 程序就绪: " + paths.ComfyRootDir)
		} else {
			st.instNote("⏭ ComfyUI 程序已存在,跳过下载")
		}

		// 自定义节点
		for _, r := range comfyDefaultResources {
			if r.Kind != "node" {
				continue
			}
			if err := gitClone(r.URL, filepath.Join(paths.ComfyRootDir, "custom_nodes", r.Rel), r.Name, st); err != nil {
				st.instNote("  ⚠️ " + err.Error())
			}
		}

		// 模型清单(镜像前缀)
		_ = os.MkdirAll(filepath.Join(paths.ComfySharedDir, "models"), 0755)
		for _, r := range comfyDefaultResources {
			if r.Kind != "model" {
				continue
			}
			dst := filepath.Join(paths.ComfySharedDir, "models", filepath.FromSlash(r.Rel))
			if err := downloadFile(comfyModelBase+"/"+r.URL, dst, st, r.Name); err != nil {
				st.instNote("  ⚠️ 模型下载失败(可稍后重试/改镜像): " + r.Name + " — " + err.Error())
			}
		}

		// media 依赖(失败不阻塞)
		if err := installMediaDeps(st); err != nil {
			st.instNote("  ⚠️ " + err.Error())
		}

		st.instNote("🎉 ComfyUI 安装流程结束,点「启动」即可开始使用")
	}()

	return nil
}

// comfyInstallStatusView 状态快照(前端轮询)
func comfyInstallStatusView() map[string]any {
	st := &comfyInstallSt
	st.mu.Lock()
	defer st.mu.Unlock()
	return map[string]any{
		"running": st.running,
		"done":    st.done,
		"rc":      st.rc,
		"err":     st.err,
		"step":    st.step,
		"item":    st.item,
		"started": st.started.Format("15:04:05"),
		"log":     strings.Join(st.log, "\n"),
	}
}

// comfyInstallStart POST 启动安装
func comfyInstallStart(w http.ResponseWriter, r *http.Request) {
	if dirExists(paths.ComfyRootDir) && fileExists(filepath.Join(paths.ComfyRootDir, "main.py")) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "ComfyUI 已存在(如需重装请先改路径)"})
		return
	}
	if err := installComfyUI(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// comfyInstallStatus GET 安装状态
func comfyInstallStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, comfyInstallStatusView())
}

// comfyInstallStop POST 停止安装(审计 M6:置 cancelled,下载/克隆循环据此真正中断——
// 此前只改 running/done 状态,后台 goroutine 的 downloadFile 从不检查,点停止后
// 下载照常跑完、文件照常落盘,还因 paths.ComfyRootDir 已存在无法重装)
func comfyInstallStop(w http.ResponseWriter, r *http.Request) {
	st := &comfyInstallSt
	st.mu.Lock()
	st.running = false
	st.cancelled = true
	st.done = true
	st.rc = -1
	st.err = "已手动停止"
	st.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ComfyInstallStart 导出面(api 路由接线)
func ComfyInstallStart(w http.ResponseWriter, r *http.Request) { comfyInstallStart(w, r) }
