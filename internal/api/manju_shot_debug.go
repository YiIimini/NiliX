package api

// 镜头级调试 API(2026-09-01 二合一阶段一):
//   GET  /api/manju/shot/workflow?config=&episode=&shot=N → 该镜工作流节点图+参考图+覆盖
//   POST /api/manju/shot/override {config, episode, shot, override} → 保存/清空覆盖
// 纯推导不执行(读 plan/资产/配置/覆盖),供前端镜头调试弹窗可视化与参数编辑。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// manjuShotWorkflowNode 工作流节点(可视化展示)
type manjuShotWorkflowNode struct {
	Name   string         `json:"name"`   // 展示名(如 UNETLoader / PDDAccApply)
	Class  string         `json:"class"`  // class_type
	Params map[string]any `json:"params"` // 关键参数(当前生效值)
}

// manjuShotWorkflowResp 单镜工作流响应
type manjuShotWorkflowResp struct {
	ShotID   int                      `json:"shot_id"`
	Scene    string                   `json:"scene"`
	Duration int                      `json:"duration"`
	Nodes    []manjuShotWorkflowNode  `json:"nodes"`             // 节点链(渲染顺序)
	Refs     []manjuShotWorkflowRef   `json:"refs"`              // 参考图清单
	Override manjuShotOverride        `json:"override"`          // 当前覆盖(空=默认)
	HasAudio bool                     `json:"has_audio"`         // 有台词/配音(音色参考)
	Engines  []string                 `json:"engines"`           // 可选引擎列表(turbo lora 文件名)
	Globals  map[string]any           `json:"globals"`           // 全局渲染参数(当前值)
	Cache    string                   `json:"cache_name"`        // 条件缓存名(当前生效)
	Chained  bool                     `json:"chained"`           // 是否接缝镜
	EngineMap map[string]string       `json:"engine_map"`        // 引擎名→lora 文件名(前端选择器用)
	Prompt   string                   `json:"prompt"`            // 最终 h3_prompt(最终化后,2026-09-02 二期:调试查看/复制)
}

type manjuShotWorkflowRef struct {
	Kind string `json:"kind"` // character/scene/audio
	Name string `json:"name"` // 角色名/场景名
	File string `json:"file"` // 相对 assets 的路径(空=不存在)
	View string `json:"view,omitempty"`
}

// manjuShotWorkflowHandler GET 单镜工作流
func manjuShotWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	shotN, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("shot")))
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	plan, _, err := ctx.loadPlan()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "方案未生成: " + err.Error()})
		return
	}
	shots, _ := planShots(plan)
	var target *manjuShot
	for i := range shots {
		if shots[i].ID == shotN {
			target = &shots[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("镜头 %d 不存在", shotN)})
		return
	}
	resp := manjuShotWorkflowResp{
		ShotID:   target.ID,
		Scene:    target.Scene,
		Duration: target.Duration,
		Override: ctx.shotOverrideFor(shotN),
		HasAudio: strings.TrimSpace(target.Dialogue) != "" || strings.TrimSpace(target.Narration) != "",
		Chained:  shotIsChained(shots, shotN),
		Globals: map[string]any{
			"steps":          ctx.steps,
			"seed_policy":    ctx.seedPolicy,
			"seed":           ctx.seed,
			"sampler":        str(ctx.R["sampler"]),
			"turbo_lora":     str(ctx.R["turbo_lora"]),
			"turbo_lora_r2v": str(ctx.R["turbo_lora_r2v"]),
			"neg_prompt":     ctx.negPrompt(),
			"fps":            ctx.fps,
			"res":            fmt.Sprintf("%dx%d", ctx.R["width"], ctx.R["height"]),
		},
	}
	// 引擎列表(前端选择器):全局 pdd/turbo lora 文件名 + 禁用项
	engines := []string{}
	engineMap := map[string]string{}
	seen := map[string]bool{}
	for _, k := range []string{"turbo_lora", "turbo_lora_r2v"} {
		if n := str(ctx.R[k]); n != "" && !seen[n] {
			seen[n] = true
			engines = append(engines, n)
			engineMap[n] = n
		}
	}
	if str(ctx.R["pdd_acc"]) != "" || str(ctx.R["turbo_lora"]) != "" {
		// pdd 走 pdd_acc 文件名自动判定,并入引擎列表
		if pdd := str(ctx.R["pdd_acc"]); pdd != "" && !seen[pdd] {
			engines = append(engines, pdd)
			engineMap[pdd] = pdd
		}
	}
	sort.Strings(engines)
	resp.Engines = engines
	resp.EngineMap = engineMap
	// 条件缓存名(当前生效参数推导)
	ov := resp.Override
	steps := ctx.steps
	if ov.Steps != nil && *ov.Steps > 0 {
		steps = *ov.Steps
	}
	resp.Cache = ctx.shotCacheNameAt(*target, ctx.w, ctx.h)
	_ = steps
	// 参考图清单
	resp.Refs = ctx.shotWorkflowRefs(*target, plan)
	// 节点链(展示:按 h3RenderWorkflow 结构推导)
	resp.Nodes = ctx.shotWorkflowNodes(*target, plan, resp)
	// 最终 h3_prompt(最终化后,与渲染输入一致;2026-09-02 二期:提示词排查/复制)
	resp.Prompt = ctx.finalizeShotPrompt(target.H3Prompt, *target, nil)
	writeJSON(w, http.StatusOK, resp)
}

// shotWorkflowRefs 该镜参考图清单(存在者)
func (ctx *manjuCtx) shotWorkflowRefs(s manjuShot, plan map[string]any) []manjuShotWorkflowRef {
	out := []manjuShotWorkflowRef{}
	chars, _ := plan["characters"].([]any)
	charID := map[string]bool{}
	for _, c := range chars {
		if m, ok := c.(map[string]any); ok {
			charID[str(m["id"])] = true
		}
	}
	for _, cid := range s.Characters {
		if !charID[cid] {
			continue
		}
		for _, v := range []string{"front", "full", "side", "detail", "q", "form2"} {
			p := filepath.Join(ctx.assetsDir, "characters", cid+"_"+v+".png")
			if fileExists(p) {
				out = append(out, manjuShotWorkflowRef{Kind: "character", Name: cid, View: v, File: "characters/" + cid + "_" + v + ".png"})
			}
		}
		if p := filepath.Join(ctx.assetsDir, "characters", cid+".png"); fileExists(p) {
			out = append(out, manjuShotWorkflowRef{Kind: "character", Name: cid, View: "main", File: "characters/" + cid + ".png"})
		}
	}
	if s.Scene != "" {
		if p := filepath.Join(ctx.assetsDir, "scenes", s.Scene+".png"); fileExists(p) {
			out = append(out, manjuShotWorkflowRef{Kind: "scene", Name: s.Scene, File: "scenes/" + s.Scene + ".png"})
		}
	}
	return out
}

// shotWorkflowNodes 节点链(推导展示)
func (ctx *manjuCtx) shotWorkflowNodes(s manjuShot, plan map[string]any, resp manjuShotWorkflowResp) []manjuShotWorkflowNode {
	nodes := []manjuShotWorkflowNode{}
	hasChar := len(s.Characters) > 0
	unet := str(ctx.R["unet_ref2va"])
	if !hasChar {
		unet = str(ctx.R["unet_fl2va"])
	}
	nodes = append(nodes, manjuShotWorkflowNode{Name: "UNETLoader", Class: "UNETLoader",
		Params: map[string]any{"unet_name": unet, "weight_dtype": "default"}})
	// 引擎分支
	loraName := str(ctx.R["turbo_lora"])
	if hasChar {
		if r2v := str(ctx.R["turbo_lora_r2v"]); r2v != "" {
			loraName = r2v
		}
	}
	if resp.Override.TurboLora != "" {
		if resp.Override.TurboLora == "-" {
			loraName = ""
		} else {
			loraName = resp.Override.TurboLora
		}
	}
	spec := turboLoRASpecOf(loraName)
	if spec.PDD && loraName != "" {
		nodes = append(nodes, manjuShotWorkflowNode{Name: "MiniMaxH3PDDAccApply", Class: "MiniMaxH3PDDAccApply",
			Params: map[string]any{"lora": loraName, "steps": spec.Steps}})
	} else if loraName != "" {
		nodes = append(nodes, manjuShotWorkflowNode{Name: "LoraLoaderModelOnly", Class: "LoraLoaderModelOnly",
			Params: map[string]any{"lora": loraName, "strength": 1.0}})
	}
	sage := mergeOverrideBool(resp.Override.Sage, str(ctx.R["sage_attention"]) == "true" || str(ctx.R["sage_attn"]) == "true")
	if sage {
		nodes = append(nodes, manjuShotWorkflowNode{Name: "PatchSageAttentionKJ", Class: "PatchSageAttentionKJ", Params: map[string]any{"enabled": true}})
	}
	if !spec.PDD {
		nodes = append(nodes, manjuShotWorkflowNode{Name: "MiniMaxH3SigmaShift", Class: "MiniMaxH3SigmaShift",
			Params: map[string]any{"shift": spec.VideoShift}})
	}
	steps := ctx.steps
	if resp.Override.Steps != nil && *resp.Override.Steps > 0 {
		steps = *resp.Override.Steps
	}
	nodes = append(nodes,
		manjuShotWorkflowNode{Name: "VAELoader", Class: "VAELoader", Params: map[string]any{"vae_name": str(ctx.R["vae_video"])}},
		manjuShotWorkflowNode{Name: "VAELoader(Audio)", Class: "VAELoader", Params: map[string]any{"vae_name": str(ctx.R["vae_audio"])}},
		manjuShotWorkflowNode{Name: "MiniMaxH3CondLoad", Class: "MiniMaxH3CondLoad", Params: map[string]any{"cache": resp.Cache}},
		manjuShotWorkflowNode{Name: "EmptyMiniMaxH3LatentAV", Class: "EmptyMiniMaxH3LatentAV", Params: map[string]any{"length": h3Length(s.Duration, ctx.fps)}},
	)
	if resp.Chained {
		nodes = append(nodes, manjuShotWorkflowNode{Name: "MiniMaxH3MotionContext", Class: "MiniMaxH3MotionContext", Params: map[string]any{"seam": "chained"}})
	}
	sampler := spec.Sampler
	if resp.Override.Sampler != "" {
		sampler = resp.Override.Sampler
	}
	nodes = append(nodes,
		manjuShotWorkflowNode{Name: "BasicGuider", Class: "BasicGuider", Params: map[string]any{"cfg": 1.0}},
		manjuShotWorkflowNode{Name: "RandomNoise", Class: "RandomNoise", Params: map[string]any{"seed": ctx.seedFor(s.ID, 0)}},
		manjuShotWorkflowNode{Name: "KSamplerSelect", Class: "KSamplerSelect", Params: map[string]any{"sampler": sampler}},
		manjuShotWorkflowNode{Name: "BasicScheduler", Class: "BasicScheduler", Params: map[string]any{"scheduler": spec.Scheduler, "steps": steps, "denoise": 1.0}},
		manjuShotWorkflowNode{Name: "SamplerCustomAdvanced", Class: "SamplerCustomAdvanced", Params: map[string]any{"steps": steps}},
		manjuShotWorkflowNode{Name: "VAEDecode", Class: "VAEDecode", Params: map[string]any{}},
		manjuShotWorkflowNode{Name: "VAEDecodeAudio", Class: "VAEDecodeAudio", Params: map[string]any{}},
		manjuShotWorkflowNode{Name: "CreateVideo", Class: "CreateVideo", Params: map[string]any{"length": h3Length(s.Duration, ctx.fps)}},
		manjuShotWorkflowNode{Name: "SaveVideo", Class: "SaveVideo", Params: map[string]any{"prefix": "manju"}},
	)
	return nodes
}

// shotIsChained 该镜是否接缝镜(非首镜且接缝 latent 存在/配置开启)
func shotIsChained(shots []manjuShot, shotID int) bool {
	if len(shots) < 2 {
		return false
	}
	// 与 renderShotTo 的 chained 判定同源:非首镜且上一镜已渲染(接缝 latent 存在)
	for i := range shots {
		if shots[i].ID == shotID {
			return i > 0
		}
	}
	return false
}

// manjuShotOverrideHandler POST 保存/清空覆盖
func manjuShotOverrideHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config   string            `json:"config"`
		Episode  string            `json:"episode"`
		Shot     int               `json:"shot"`
		Override manjuShotOverride `json:"override"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, err := newManjuCtx(body.Config, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if body.Shot <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "shot 必须 > 0"})
		return
	}
	if err := ctx.setShotOverride(body.Shot, body.Override); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shot": body.Shot})
}

// manjuShotOverridesHandler GET 全部覆盖(前端列表显示哪些镜有自定义)
func manjuShotOverridesHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ctx.loadShotOverrides())
}

// manjuShotVideoHandler 镜头视频流(2026-09-02 二期:镜头卡片/调试面板内嵌播放)
// GET /api/manju/shot/video?config=&episode=&shot=N
func manjuShotVideoHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	episode := strings.TrimSpace(r.URL.Query().Get("episode"))
	shotN, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("shot")))
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if episode == "" {
		episode = ctx.episode
	}
	p := filepath.Join(ctx.clipsDir, episode, fmt.Sprintf("%02d.mp4", shotN))
	if !fileExists(p) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "镜头视频不存在"})
		return
	}
	// 视频流(支持 Range 拖动,浏览器内嵌播放)
	f, err := os.Open(p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, filepath.Base(p), st.ModTime(), f)
}

// manjuShotDebugStatic 前端静态引用辅助(调试面板 assets 图 URL)
func manjuShotDebugStatic(w http.ResponseWriter, r *http.Request) {
	// GET /api/manju/shot/asset?config=&file=characters/xxx_front.png
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	if file == "" || strings.Contains(file, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "file 非法"})
		return
	}
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p := filepath.Join(ctx.assetsDir, filepath.FromSlash(file))
	b, err := os.ReadFile(p)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "文件不存在"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

var _ = json.Marshal // keep import
