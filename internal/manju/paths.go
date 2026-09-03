package manju

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"nilix/internal/comfy"
	"strings"

	"nilix/internal/paths"
)

func manjuPathsGet(w http.ResponseWriter, r *http.Request) {
	eff := map[string]any{
		"manju_root":   paths.ManjuRootDir,
		"novel_root":   paths.NovelRootDir,
		"comfy_root":   paths.ComfyRootDir,
		"comfy_shared": paths.ComfySharedDir,
		"novel_skill":  paths.NovelSkillDir,
		"asset_lib":    paths.AssetLibDir,
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
				eff["comfy_output"] = filepath.Join(paths.ComfySharedDir, "output")
			}
		}
	}
	res := map[string]any{
		"effective":  eff,
		"configured": cfgd,
		"comfy": map[string]any{
			"exists": dirExists(paths.ComfyRootDir),
			"venv":   fileExists(filepath.Join(paths.ComfyRootDir, ".venv", "Scripts", "python.exe")),
		},
		"skill": map[string]any{
			"exists": dirExists(paths.NovelSkillDir),
			"isGit":  dirExists(filepath.Join(paths.NovelSkillDir, ".git")),
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
	paths.InitPaths(exeDir, cfg.Paths.ManjuRoot, cfg.Paths.NovelRoot, cfg.Paths.ComfyRoot, cfg.Paths.ComfyShared, cfg.Paths.NovelSkill)
	comfyIn := cfg.Paths.ComfyInput
	if strings.TrimSpace(comfyIn) == "" {
		comfyIn = filepath.Join(paths.ComfySharedDir, "input")
	}
	comfyOut := cfg.Paths.ComfyOutput
	if strings.TrimSpace(comfyOut) == "" {
		comfyOut = filepath.Join(paths.ComfySharedDir, "output")
	}
	comfy.SetParams(comfy.ComfyURL(), comfyIn, comfyOut)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
