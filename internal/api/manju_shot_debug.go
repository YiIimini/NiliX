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

// manjuShotWorkflowNode 工作流节点(可视化展示;2026-09-03 三期:带端口与连线,画布拖拽连线渲染)
type manjuShotWorkflowNode struct {
	ID     string         `json:"id"`               // 连线锚点(如 unet/samp)
	Name   string         `json:"name"`             // 展示名(如 UNETLoader / PDDAccApply)
	Class  string         `json:"class"`            // class_type
	In     int            `json:"in,omitempty"`     // 输入端口数(画布左侧)
	Out    int            `json:"out,omitempty"`    // 输出端口数(画布右侧)
	Params map[string]any `json:"params"`           // 关键参数(当前生效值)
	X      float64        `json:"x"`                // 自动布局坐标(前端拖动后存 localStorage 覆盖)
	Y      float64        `json:"y"`
}

// manjuShotWorkflowLink 节点连线(from 输出端口 → to 输入端口,与真实提交的 workflow 连线一致)
type manjuShotWorkflowLink struct {
	From     string `json:"from"`
	FromPort int    `json:"from_port"`
	To       string `json:"to"`
	ToPort   int    `json:"to_port"`
}

// manjuShotWorkflowResp 单镜工作流响应
type manjuShotWorkflowResp struct {
	ShotID   int                      `json:"shot_id"`
	Scene    string                   `json:"scene"`
	Duration int                      `json:"duration"`
	Nodes    []manjuShotWorkflowNode  `json:"nodes"`             // 节点链(渲染顺序)
	Links    []manjuShotWorkflowLink  `json:"links"`             // 连线(数据流,2026-09-03 三期)
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
	resp.Cache = ctx.shotCacheNameAt(*target, ctx.w, ctx.h)
	// 参考图清单
	resp.Refs = ctx.shotWorkflowRefs(*target, plan)
	// 节点图(展示:按 h3RenderWorkflow 结构推导;三期含端口/连线/布局)
	resp.Nodes, resp.Links = ctx.shotWorkflowNodes(*target, plan, resp)
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

// shotWorkflowNodes 节点图(推导展示;2026-09-03 三期:与 h3RenderWorkflow 真实连线一致,
// 含端口数/连线/自动布局坐标,供前端画布拖拽+SVG 贝塞尔连线)
func (ctx *manjuCtx) shotWorkflowNodes(s manjuShot, plan map[string]any, resp manjuShotWorkflowResp) ([]manjuShotWorkflowNode, []manjuShotWorkflowLink) {
	nodes := []manjuShotWorkflowNode{}
	links := []manjuShotWorkflowLink{}
	add := func(id, name, class string, in, out int, params map[string]any) string {
		nodes = append(nodes, manjuShotWorkflowNode{ID: id, Name: name, Class: class, In: in, Out: out, Params: params})
		return id
	}
	link := func(from string, fromPort int, to string, toPort int) {
		links = append(links, manjuShotWorkflowLink{From: from, FromPort: fromPort, To: to, ToPort: toPort})
	}

	hasChar := len(s.Characters) > 0
	unet := str(ctx.R["unet_ref2va"])
	if !hasChar {
		unet = str(ctx.R["unet_fl2va"])
	}
	model := add("unet", "UNETLoader", "UNETLoader", 0, 1,
		map[string]any{"unet_name": unet, "weight_dtype": "default"})
	// 引擎分支(与 h3RenderWorkflow 同判定:PDD 专用节点双输出,普通 distill LoRA 走 ModelOnly)
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
	isPDD := spec.PDD && loraName != ""
	if isPDD {
		pdd := add("pdd", "MiniMaxH3PDDAccApply", "MiniMaxH3PDDAccApply", 1, 2,
			map[string]any{"lora": loraName, "steps": spec.Steps})
		link(model, 0, pdd, 0)
		model = pdd
	} else if loraName != "" {
		lo := add("lora", "LoraLoaderModelOnly", "LoraLoaderModelOnly", 1, 1,
			map[string]any{"lora": loraName, "strength": spec.Strength})
		link(model, 0, lo, 0)
		model = lo
	}
	sage := mergeOverrideBool(resp.Override.Sage, str(ctx.R["sage_attention"]) == "true" || str(ctx.R["sage_attn"]) == "true")
	if sage {
		sg := add("sage", "PatchSageAttentionKJ", "PatchSageAttentionKJ", 1, 1, map[string]any{"enabled": true})
		link(model, 0, sg, 0)
		model = sg
	}
	if !spec.PDD {
		sh := add("shift", "MiniMaxH3SigmaShift", "MiniMaxH3SigmaShift", 1, 1,
			map[string]any{"shift": spec.VideoShift})
		link(model, 0, sh, 0)
		model = sh
	}
	steps := ctx.steps
	if resp.Override.Steps != nil && *resp.Override.Steps > 0 {
		steps = *resp.Override.Steps
	}
	vae := add("vae", "VAELoader", "VAELoader", 0, 1, map[string]any{"vae_name": str(ctx.R["vae_video"])})
	avae := add("avae", "VAELoader(Audio)", "VAELoader", 0, 1, map[string]any{"vae_name": str(ctx.R["vae_audio"])})
	cond := add("cond", "MiniMaxH3CondLoad", "MiniMaxH3CondLoad", 0, 1, map[string]any{"cache": resp.Cache})
	latent := add("latent", "EmptyMiniMaxH3LatentAV", "EmptyMiniMaxH3LatentAV", 0, 1,
		map[string]any{"length": h3Length(s.Duration, ctx.fps)})
	if resp.Chained {
		latload := add("latload", "MotionContextLoadLatent", "MiniMaxH3MotionContextLoadLatent", 0, 1,
			map[string]any{"clip_index": s.ID - 1})
		// 入序与真实节点一致:conditioning/vae/latent/context_latent;出 [0]=conditioning [1]=trim_frames
		mc := add("mc", "MiniMaxH3MotionContext", "MiniMaxH3MotionContext", 4, 2,
			map[string]any{"context_length": "22"})
		link(cond, 0, mc, 0)
		link(vae, 0, mc, 1)
		link(latent, 0, mc, 2)
		link(latload, 0, mc, 3)
		cond = "mc" // mc[0] 接管 conditioning
	}
	guider := add("guider", "BasicGuider", "BasicGuider", 2, 1, map[string]any{"cfg": 1.0})
	link(model, 0, guider, 0)
	link(cond, 0, guider, 1)
	noise := add("noise", "RandomNoise", "RandomNoise", 0, 1, map[string]any{"seed": ctx.seedFor(s.ID, 0)})
	sampler := spec.Sampler
	if resp.Override.Sampler != "" {
		sampler = resp.Override.Sampler
	}
	ksel := add("ksel", "KSamplerSelect", "KSamplerSelect", 0, 1, map[string]any{"sampler": sampler})
	if !isPDD {
		// PDD 模式 sigmas 来自 Apply 节点 [1](内置训练 shift 采样网格),不建 BasicScheduler
		sched := add("sched", "BasicScheduler", "BasicScheduler", 1, 1,
			map[string]any{"scheduler": spec.Scheduler, "steps": steps, "denoise": 1.0})
		link(model, 0, sched, 0)
	}
	// 入序与真实节点一致:noise/guider/sampler/sigmas/latent_image(latent_image 恒接 EmptyLatent,
	// 与 h3RenderWorkflow 一致——MotionContext 只接管 conditioning/trim_frames)
	samp := add("samp", "SamplerCustomAdvanced", "SamplerCustomAdvanced", 5, 1, map[string]any{"steps": steps})
	link(noise, 0, samp, 0)
	link(guider, 0, samp, 1)
	link(ksel, 0, samp, 2)
	if spec.PDD && loraName != "" {
		link("pdd", 1, samp, 3) // PDD: sigmas = Apply 节点 [1] 输出
	} else {
		link("sched", 0, samp, 3)
	}
	link(latent, 0, samp, 4)
	vdec := add("vdec", "VAEDecode", "VAEDecode", 2, 1, map[string]any{})
	adec := add("adec", "VAEDecodeAudio", "VAEDecodeAudio", 2, 1, map[string]any{})
	link(samp, 0, vdec, 0)
	link(vae, 0, vdec, 1)
	link(samp, 0, adec, 0)
	link(avae, 0, adec, 1)
	imgSrc, audSrc := vdec, adec
	if resp.Chained {
		// MotionContextTrim 入序:images/trim_frames/audio;出 [0]=images [1]=audio
		trim := add("trim", "MotionContextTrim", "MiniMaxH3MotionContextTrim", 3, 2, map[string]any{"match_tail": true})
		link(vdec, 0, trim, 0)
		link("mc", 1, trim, 1)
		link(adec, 0, trim, 2)
		imgSrc, audSrc = trim, trim
	}
	video := add("video", "CreateVideo", "CreateVideo", 2, 1, map[string]any{"length": h3Length(s.Duration, ctx.fps)})
	link(imgSrc, 0, video, 0)
	if resp.Chained {
		link(audSrc, 1, video, 1)
	} else {
		link(audSrc, 0, video, 1)
	}
	save := add("save", "SaveVideo", "SaveVideo", 1, 0, map[string]any{"prefix": "manju"})
	link(video, 0, save, 0)
	savelat := add("savelat", "SaveLatent(续接)", "MiniMaxH3MotionContextSaveLatent", 1, 0,
		map[string]any{"clip_index": s.ID})
	link(samp, 0, savelat, 0)
	layoutWorkflowGraph(nodes, links)
	return nodes, links
}

// layoutWorkflowGraph 自动布局:拓扑分层(迭代求深度),X=层,Y=层内序;前端拖动后本地覆盖
func layoutWorkflowGraph(nodes []manjuShotWorkflowNode, links []manjuShotWorkflowLink) {
	depth := map[string]int{}
	for _, n := range nodes {
		depth[n.ID] = 0
	}
	for changed := true; changed; {
		changed = false
		for _, l := range links {
			if depth[l.To] < depth[l.From]+1 {
				depth[l.To] = depth[l.From] + 1
				changed = true
			}
		}
	}
	const gapX, gapY = 300, 150
	colCount := map[int]int{}
	for i := range nodes {
		d := depth[nodes[i].ID]
		nodes[i].X = float64(d * gapX)
		nodes[i].Y = float64(colCount[d] * gapY)
		colCount[d]++
	}
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
