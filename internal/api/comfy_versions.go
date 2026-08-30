package api

// ComfyUI 版本/模型/插件/工作流 检测展示(2026-08-29 用户需求,仅检测不更新):
//   GET  /api/comfy/versions       — ComfyUI 版本 + 模型清单(按类分组) + 插件 git 状态 + 工作流清单
//   POST /api/comfy/plugins/check  — 逐插件 git fetch 远程,统计落后提交数(检测;不执行更新)
// 入口:#/comfy 嵌套页顶部「🛠 版本管理」按钮 → 弹窗展示。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// comfyModelDirs 模型目录清单(ComfyUI 标准 models/ 子目录 → 中文分组名)
var comfyModelDirs = []struct{ Dir, Label string }{
	{"diffusion_models", "UNet 扩散模型"},
	{"checkpoints", "Checkpoint 模型"},
	{"loras", "LoRA 加速/风格"},
	{"vae", "VAE 解码器"},
	{"clip", "CLIP 文本编码"},
	{"clip_vision", "CLIP Vision"},
	{"text_encoders", "文本编码器"},
	{"controlnet", "ControlNet"},
	{"upscale_models", "放大模型"},
	{"style_models", "风格模型"},
	{"embeddings", "Embeddings"},
}

var comfyVersionsMu sync.RWMutex // versions 快照 + 插件 behind 记录(读多写少)

// comfyPluginVer 插件版本对比记录(仅检测,不写盘)
type comfyPluginVer struct {
	Behind int    // 落后提交数(0=当前与最新一致)
	Head   string // 最新版本(origin/HEAD 短哈希;检查后才有)
}

// comfyPluginVers 插件名 → 版本对比结果
var comfyPluginVers = map[string]comfyPluginVer{}

// comfyLatestVer ComfyUI 官方最新版本(GitHub releases;打开弹窗时自动拉取,带缓存)
var comfyLatestVer string
var comfyLatestVerAt time.Time

// comfyLatestVerCached 取 ComfyUI 最新版本(10 分钟缓存;失败留空不阻塞)
func comfyLatestVerCached() string {
	comfyVersionsMu.RLock()
	v, at := comfyLatestVer, comfyLatestVerAt
	comfyVersionsMu.RUnlock()
	if v != "" && time.Since(at) < 10*time.Minute {
		return v
	}
	nv := comfyGitHubLatestVer()
	comfyVersionsMu.Lock()
	if nv != "" {
		comfyLatestVer = nv
		comfyLatestVerAt = time.Now()
	} else if v != "" {
		comfyLatestVerAt = time.Now() // 拉取失败:沿用旧值,避免每次弹窗都打 GitHub
	}
	comfyVersionsMu.Unlock()
	if nv != "" {
		return nv
	}
	return v
}

// comfyVersionSnapshot 组装版本信息(扫描本地目录,不做网络操作)
func comfyVersionSnapshot() map[string]any {
	info := map[string]any{
		"comfy":     map[string]any{"online": false},
		"models":    map[string][]any{},
		"plugins":   []any{},
		"workflows": []any{},
	}
	// ComfyUI 版本(在线时)+ 官方最新版本(自动拉取,10min 缓存;失败留空)
	if cc := newComfyClient(comfyParams().url); cc != nil {
		if v, err := cc.online(); err == nil {
			info["comfy"] = map[string]any{"online": true, "version": v}
		}
	}
	info["comfy_latest"] = comfyLatestVerCached()
	// 模型清单
	models := map[string][]any{}
	root := filepath.Join(ComfySharedDir, "models")
	for _, d := range comfyModelDirs {
		dir := filepath.Join(root, d.Dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var items []any
		for _, e := range entries {
			if e.IsDir() || !comfyModelExt(e.Name()) {
				continue
			}
			fi, _ := e.Info()
			items = append(items, map[string]any{
				"name":    e.Name(),
				"size_mb": float64(fi.Size()) / 1024 / 1024,
				"mtime":   fi.ModTime().Format("01-02 15:04"),
			})
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].(map[string]any)["size_mb"].(float64) > items[j].(map[string]any)["size_mb"].(float64)
		})
		models[d.Label] = items
	}
	info["models"] = models
	// 插件 git 状态(本地,无网络)
	var plugins []any
	cnDir := filepath.Join(ComfyRootDir, "custom_nodes")
	if entries, err := os.ReadDir(cnDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "__pycache__" {
				continue
			}
			gd := filepath.Join(cnDir, e.Name())
			item := map[string]any{"name": e.Name(), "git": false}
			if st, err := os.Stat(filepath.Join(gd, ".git")); err == nil && st.IsDir() {
				item["git"] = true
				item["remote"] = comfyGitRun(gd, "remote", "get-url", "origin")
				item["commit"] = comfyGitRun(gd, "rev-parse", "--short", "HEAD")
				item["dirty"] = comfyGitRun(gd, "status", "--porcelain") != ""
				// 最新版本与落后数:直接读本地 origin/HEAD 引用(上次 fetch 的远程快照,
				// 纯本地操作秒回,不联网)——打开弹窗即有对比;「检查更新」只是 fetch 刷新它。
				head := comfyGitRun(gd, "rev-parse", "--short", "origin/HEAD")
				behind := 0
				if b := comfyGitRun(gd, "rev-list", "--count", "HEAD..origin/HEAD"); b != "" {
					_, _ = fmt.Sscanf(b, "%d", &behind)
				}
				item["behind"] = behind
				item["latest"] = head
				item["check_time"] = ""
				if head != "" {
					item["check_time"] = "本地引用"
				}
				comfyVersionsMu.RLock()
				ver := comfyPluginVers[e.Name()]
				comfyVersionsMu.RUnlock()
				if ver.Head != "" { // 曾执行过「检查更新」:用 fetch 后的结果覆盖
					item["behind"] = ver.Behind
					item["latest"] = ver.Head
					item["check_time"] = "已检查"
				}
			}
			plugins = append(plugins, item)
		}
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].(map[string]any)["name"].(string) < plugins[j].(map[string]any)["name"].(string)
	})
	info["plugins"] = plugins
	// 工作流清单
	var wfs []any
	seen := map[string]bool{}
	for _, dir := range []string{
		filepath.Join(ComfyRootDir, "user", "default", "workflows"),
		filepath.Join(ComfyRootDir, "workflow_templates"),
	} {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") || seen[e.Name()] {
					continue
				}
				seen[e.Name()] = true
				fi, _ := e.Info()
				wfs = append(wfs, map[string]any{
					"name":  e.Name(),
					"dir":   filepath.Base(dir),
					"size":  fi.Size(),
					"mtime": fi.ModTime().Format("01-02 15:04"),
				})
			}
		}
	}
	sort.Slice(wfs, func(i, j int) bool {
		return wfs[i].(map[string]any)["name"].(string) < wfs[j].(map[string]any)["name"].(string)
	})
	info["workflows"] = wfs
	// PDD 加速 LoRA 状态(2026-08-29 整合)
	pdd := map[string]any{"available": false}
	for _, item := range models["LoRA 加速/风格"] {
		if m, ok := item.(map[string]any); ok && strings.Contains(strings.ToLower(str(m["name"])), "pdd_acc") {
			pdd["available"] = true
			pdd["file"] = m["name"]
			break
		}
	}
	info["pdd"] = pdd
	return info
}

// comfyGitRun 在 dir 执行 git,返回 stdout 去尾空白;失败返回空串。
// 必须隐藏窗口(2026-08-29 用户反馈「点版本管理弹窗一直闪黑终端」):
// git.exe 是控制台程序,不设 HideWindow 每次调用都会闪现黑窗口——
// 版本快照对每个插件跑 3 次 git(remote/rev-parse/status),弹窗一开闪一片。
func comfyGitRun(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// comfyModelExt 模型文件后缀
func comfyModelExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".safetensors", ".ckpt", ".pt", ".pth", ".gguf", ".bin", ".onnx":
		return true
	}
	return false
}

// handleComfyVersions GET /api/comfy/versions(快照内部对共享 map 用 RLock,此处不持锁防死锁)
func handleComfyVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, comfyVersionSnapshot())
}

// handleComfyPluginsCheck POST /api/comfy/plugins/check:逐插件 git fetch 统计落后
// 提交(网络操作 10s+,仅检测不更新;结果缓存供 versions 展示)
func handleComfyPluginsCheck(w http.ResponseWriter, r *http.Request) {
	cnDir := filepath.Join(ComfyRootDir, "custom_nodes")
	entries, err := os.ReadDir(cnDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "custom_nodes 不可读: " + err.Error()})
		return
	}
	results := map[string]any{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "__pycache__" {
			continue
		}
		gd := filepath.Join(cnDir, e.Name())
		if st, err := os.Stat(filepath.Join(gd, ".git")); err != nil || !st.IsDir() {
			results[e.Name()] = map[string]any{"git": false}
			continue
		}
		// git fetch 超时保护(20s);fetch 失败保留旧状态。HideWindow 防黑窗口闪现(同上)
		fetchCmd := exec.Command("git", "-C", gd, "fetch", "origin", "--quiet")
		fetchCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		fetchCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		done := make(chan error, 1)
		go func() { done <- fetchCmd.Run() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = fetchCmd.Process.Kill()
			results[e.Name()] = map[string]any{"git": true, "error": "fetch 超时"}
			continue
		}
		behind := 0
		head := ""
		if b := comfyGitRun(gd, "rev-list", "--count", "HEAD..origin/HEAD"); b != "" {
			_, _ = fmt.Sscanf(b, "%d", &behind)
		}
		head = comfyGitRun(gd, "rev-parse", "--short", "origin/HEAD") // 最新版本
		cur := comfyGitRun(gd, "rev-parse", "--short", "HEAD")         // 当前版本
		comfyVersionsMu.Lock()
		comfyPluginVers[e.Name()] = comfyPluginVer{Behind: behind, Head: head}
		comfyVersionsMu.Unlock()
		results[e.Name()] = map[string]any{"git": true, "behind": behind, "latest": head, "current": cur}
	}
	// ComfyUI 官方最新版本(GitHub releases latest tag;失败/超时留空,不阻塞插件检查)
	if comfyLatestVer == "" {
		comfyLatestVer = comfyGitHubLatestVer()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plugins": results, "comfy_latest": comfyLatestVer})
}

// comfyGitHubLatestVer 查 ComfyUI 官方仓库最新 release tag(10s 超时;失败返回空串)
func comfyGitHubLatestVer() string {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/comfyanonymous/ComfyUI/releases/latest", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "NiliX-Version-Manager")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&rel) != nil {
		return ""
	}
	// 归一化:GitHub tag 常带 v 前缀(v0.34.0),本地版本无前缀(0.34.0)——比较前统一剥离,
	// 否则同版本被判为不同(2026-08-29 用户反馈「0.34.0 与 v0.34.0 不匹配」)
	return strings.TrimPrefix(rel.TagName, "v")
}
