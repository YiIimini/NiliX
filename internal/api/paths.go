package api

// 自包含部署路径解析:换电脑把整个 NiliX 目录拷走即用(ComfyUI/技能/数据目录都收在 exe 目录下)。
// 解析链:settings.json 显式值(设置弹窗「目录与部署」) → exe 目录自包含子目录(存在时) → 旧硬编码(兼容老安装)。
// InitPaths 必须在任何路径使用前调用(main 在 Chdir 到 exe 目录后立即执行)。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 解析后的运行路径(包内/导出供 main 与前端设置使用)
var (
	ManjuRootDir   string // 漫剧项目目录
	NovelRootDir   string // 小说库根目录(fs 白名单)
	ComfyRootDir   string // ComfyUI 安装目录(含 main.py / .venv)
	ComfySharedDir string // ComfyUI 共享目录(models/input/output)
	NovelSkillDir  string // 小说续作技能目录(词库/写作规范/qa 脚本)
)

// 旧硬编码路径(老安装的默认位置;新安装优先自包含目录)
const (
	legacyManjuRoot   = `C:\Mi\Ai\WorkBench\manju`
	legacyNovelRoot   = `C:\Mi\Ai\WorkBench\novel`
	legacyComfyRoot   = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Installs\ComfyUI (1)\ComfyUI`
	legacyComfyShared = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared`
	legacyNovelSkill  = `C:\Users\Administrator\.agents\skills\shuangwen-novel`
)

// InitPaths 解析全部运行路径。exeDir 为可执行文件所在目录(自包含根)。
func InitPaths(exeDir string, manjuRoot, novelRoot, comfyRoot, comfyShared, novelSkill string) {
	ManjuRootDir = pickPath(manjuRoot, filepath.Join(exeDir, "manju"), legacyManjuRoot)
	NovelRootDir = pickPath(novelRoot, filepath.Join(exeDir, "novel"), legacyNovelRoot)
	ComfyRootDir = pickPath(comfyRoot, filepath.Join(exeDir, "comfyui", "ComfyUI"), legacyComfyRoot)
	ComfySharedDir = pickPath(comfyShared, filepath.Join(exeDir, "comfyui", "shared"), legacyComfyShared)
	NovelSkillDir = pickPath(novelSkill, filepath.Join(exeDir, "skills", "shuangwen-novel"), legacyNovelSkill)
}

// pickPath 显式配置优先;否则自包含目录存在时用之;否则回退旧路径。
func pickPath(setting, selfContained, legacy string) string {
	if s := strings.TrimSpace(setting); s != "" {
		return s
	}
	if selfContained != "" {
		if st, err := os.Stat(selfContained); err == nil && st.IsDir() {
			return selfContained
		}
	}
	return legacy
}

// manjuPathsGet 返回路径配置:显式值 + 生效值 + ComfyUI/技能就绪状态(设置弹窗「目录与部署」区)。
func manjuPathsGet(w http.ResponseWriter, r *http.Request) {
	eff := map[string]any{
		"manju_root":   ManjuRootDir,
		"novel_root":   NovelRootDir,
		"comfy_root":   ComfyRootDir,
		"comfy_shared": ComfySharedDir,
		"novel_skill":  NovelSkillDir,
	}
	cfgd := map[string]any{}
	if manjuSettingsStore != nil {
		if cfg, err := manjuSettingsStore.Load(); err == nil {
			cfgd = map[string]any{
				"manju_root":   cfg.Paths.ManjuRoot,
				"novel_root":   cfg.Paths.NovelRoot,
				"comfy_root":   cfg.Paths.ComfyRoot,
				"comfy_shared": cfg.Paths.ComfyShared,
				"novel_skill":  cfg.Paths.NovelSkill,
				"comfy_input":  cfg.Paths.ComfyInput,
				"comfy_output": cfg.Paths.ComfyOutput,
			}
			// ComfyUI 成品输出目录生效值:显式配置优先,缺省跟随共享目录 output(与 main 启动解析一致)
			if s := strings.TrimSpace(cfg.Paths.ComfyOutput); s != "" {
				eff["comfy_output"] = s
			} else {
				eff["comfy_output"] = filepath.Join(ComfySharedDir, "output")
			}
		}
	}
	res := map[string]any{
		"effective":  eff,
		"configured": cfgd,
		"comfy": map[string]any{
			"exists": dirExists(ComfyRootDir),
			"venv":   fileExists(filepath.Join(ComfyRootDir, ".venv", "Scripts", "python.exe")),
		},
		"skill": map[string]any{
			"exists": dirExists(NovelSkillDir),
			"isGit":  dirExists(filepath.Join(NovelSkillDir, ".git")),
		},
	}
	writeJSON(w, http.StatusOK, res)
}

// manjuPathsPost 保存路径配置到 settings.json paths 节并立即重新解析生效
// (ComfyUI 启动参数同步;已在运行的 ComfyUI 需重启才用新目录)。
func manjuPathsPost(w http.ResponseWriter, r *http.Request) {
	if manjuSettingsStore == nil {
		http.Error(w, `{"error":"设置存储未就绪"}`, http.StatusInternalServerError)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg, err := manjuSettingsStore.Load()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读取设置失败: " + err.Error()})
		return
	}
	cfg.Paths.ManjuRoot = strings.TrimSpace(str(body["manju_root"]))
	cfg.Paths.NovelRoot = strings.TrimSpace(str(body["novel_root"]))
	cfg.Paths.ComfyRoot = strings.TrimSpace(str(body["comfy_root"]))
	cfg.Paths.ComfyShared = strings.TrimSpace(str(body["comfy_shared"]))
	cfg.Paths.NovelSkill = strings.TrimSpace(str(body["novel_skill"]))
	// ComfyUI 成品输出目录:显式指定保存;留空=跟随共享目录 output
	if v, present := body["comfy_output"]; present {
		cfg.Paths.ComfyOutput = strings.TrimSpace(str(v))
	}
	if err := manjuSettingsStore.Save(cfg); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "保存失败: " + err.Error()})
		return
	}
	// 立即重新解析生效;ComfyUI 输入/输出目录缺省跟随共享目录
	exeDir, _ := filepath.Abs(".")
	InitPaths(exeDir, cfg.Paths.ManjuRoot, cfg.Paths.NovelRoot, cfg.Paths.ComfyRoot, cfg.Paths.ComfyShared, cfg.Paths.NovelSkill)
	comfyIn := cfg.Paths.ComfyInput
	if strings.TrimSpace(comfyIn) == "" {
		comfyIn = filepath.Join(ComfySharedDir, "input")
	}
	comfyOut := cfg.Paths.ComfyOutput
	if strings.TrimSpace(comfyOut) == "" {
		comfyOut = filepath.Join(ComfySharedDir, "output")
	}
	SetComfyParams(comfyParams.url, comfyIn, comfyOut)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
