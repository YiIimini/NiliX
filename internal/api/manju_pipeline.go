package api

// 漫剧管线 Go 实现:方案(DeepSeek LLM 直出)→ 资产(SDXL/Z-Image)→ 预编码(Qwen3-VL 条件缓存)
// → 渲染(H3 + Turbo LoRA + MotionContext 接缝)→ 质检 + 合成(venv PyAV 辅助)。
// 与原 Python 管线的日志格式契约保持一致(━━━ 阶段 X ━━━ / [i/n] 镜头),前端进度/流程图无需改动。

import (
	"bufio"
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"nilix/internal/agent"
)

//go:embed scripts/manju_media.py
var manjuMediaPy string

// face_detection_yunet_2023mar.onnx 官方 YuNet 人脸检测模型(2026-08-26 正脸裁切用):
// opencv-python 5.x 移除 CascadeClassifier 且 pip 包不带模型数据,内嵌随 exe 释放到
// 媒体脚本同目录,py 侧 FaceDetectorYN 加载。
//
//go:embed scripts/face_detection_yunet_2023mar.onnx
var manjuHaarFaceXML string

// ---- 项目上下文 ----

type manjuCtx struct {
	configPath   string
	cfg          map[string]any
	style        string
	R            map[string]any
	P            map[string]any
	project      string // 项目名(目录名)
	episode      string
	chapters     string
	only         string
	novel        string // 实际使用的小说文件(前端覆盖优先)
	scriptMode   bool   // 视频脚本直出模式(输入为 H3 官方格式的分镜脚本 md/json,而非小说正文)
	agentMode    bool   // AI 一条龙(2026-09-01 用户规则):全走 LLM+Agent 全流程,跳过脚本检测强制 LLM 直出
	auto         bool   // 全本自动分集模式:该集章节由引擎按内容量切分(方案复用校验用)
	llm          *manjuLLM
	comfy        *comfyClient
	comfyOutput  string
	comfyInput   string
	sharedModels string
	loraRepair   string // 渲染入口 turbo LoRA 归一化提示(配置引用的 LoRA 文件缺失自动回退时填充,启动日志展示)
	assetsDir    string
	analysisDir  string
	clipsDir     string // <workdir>/clips
	workdir      string
	steps        int // 采样步数(turbo_lora 存在则用 turbo_steps)
	w, h         int
	fps          int
	seed         int
	minSec       int
	maxSec       int
	charsPerSec  float64 // 中文语音字速预算(台词+旁白总字数÷字速 ≤ 时长;默认 4,2026-08-26 可配)
	seedPolicy   string  // seed 重试策略:fixed(默认全剧固定)/increment(重试 seed+N)/random(重试换随机)
	resTier      string  // 分辨率档位(空/custom=手动宽高;draft/standard/fhd 见 manjuResTiers)
	draftJudge   bool    // 智能模式草稿预审:审片返工轮用缩放分辨率草稿,全部通过后全分辨率定稿重渲
	draftScale   float64 // 草稿缩放(0.2-0.95,默认 0.5;0.5 ≈ 1/4 像素量)
	forceAttempt int     // 定点返工等外部路径传入的重试序号(seed 策略用它换 seed;0=首渲)
	sageChecked  bool    // SageAttn 节点探测已完成(每 run 一次,避免逐镜 HTTP 探测)
	sageOK       bool    // PatchSageAttentionKJ 节点存在
	sageNodeName string  // 实际存在的 SageAttn 节点名(Pathch/Patch 拼写兼容;审计 3.3 从 R 移出)
	pddChecked   bool    // PDD Acc 节点探测已完成(2026-08-29,每 run 一次)
	pddOK        bool    // MiniMaxH3PDDAccApply 节点存在(缺失时 PDD LoRA 回退普通模式)
	qcRerender   map[int]int // 质检自愈重渲轮数(镜头号 → 已重渲次数;换 seed 重渲,上限后提示逃生门)
	visionOnce   sync.Once
	vision       *agent.VisionClient // 每 run 共享(粘性降级状态跨镜头保留)
	charInfo     map[string]map[string]any // 方案角色 id → 角色对象(懒加载,自动音色匹配用)
	voiceLibDone map[string]bool           // 库音色自动补齐去重(每 run 一次,防重复生成)
	speakerReg   map[string]string         // 全局说话人注册表(角色→全局 (Sx),2026-08-30 ver14,ensurePlanAndPrompts 构建)
	voiceAssign  map[string]string         // 角色→最终音色 key(2026-08-30 ver15 同档位差异化变体分配,懒构建)
}

// sageAttnGuard 检查 SageAttention 节点可用性:ComfyUI 未装对应节点时
// 硬提交会 400 missing_node_type 失败——可选加速项不阻塞渲染,自动降级关闭并提示。
// 每 run 只探测一次(逐镜探测太慢);装好节点后 config 里开关仍是开的,下次 run 自动恢复。
// 注意 KJNodes 上游把类名拼错为 PathchSageAttentionKJ(非 Patch),两个名字都探测取实际存在者。
// 审计 3.3:探测结果写入 ctx 字段而非 ctx.R——预编码 goroutine 与渲染主 goroutine 并发,
// 各自通过 applySageToR 把结果注入自己的 R 副本,消除"主流程写 R 时 goroutine 读 R"的
// 并发 map 读写(此前靠调用顺序侥幸规避)
// sageEnabled SageAttention 开关,缺省开启(存量项目 config 缺字段时按 true 处理,
// 让加速件对新旧项目一致生效;节点缺失时 sageAttnGuard 会自动降级关闭不阻塞渲染)。
func sageEnabled(R map[string]any) bool {
	if v, ok := R["sage_attention"].(bool); ok {
		return v
	}
	return true
}

func (ctx *manjuCtx) sageAttnGuard(lg *manjuLogger) {
	if !sageEnabled(ctx.R) || ctx.sageChecked {
		return
	}
	ctx.sageChecked = true
	for _, n := range []string{"PathchSageAttentionKJ", "PatchSageAttentionKJ"} {
		if ctx.comfy.hasNode(n) {
			ctx.sageOK = true
			ctx.sageNodeName = n
			return
		}
	}
	ctx.sageOK = false
	ctx.sageNodeName = ""
	lg.logf("  ⚠️ ComfyUI 缺少 PatchSageAttentionKJ/PathchSageAttentionKJ 节点(未装 ComfyUI-KJNodes),SageAttn 已自动关闭继续渲染;装好节点或关闭「渲染参数→SageAttn」后恢复")
}

// applySageToR 把 sageAttnGuard 探测结果写入给定 R(调用方自己的副本/私有 map)
func (ctx *manjuCtx) applySageToR(R map[string]any) {
	if !ctx.sageChecked {
		return
	}
	if ctx.sageOK && ctx.sageNodeName != "" {		R["sage_node_name"] = ctx.sageNodeName
	} else {
		R["sage_attention"] = false
	}
}

// pddGuard PDD Acc 节点探测(2026-08-29):MiniMaxH3PDDAccApply 存在性。
// 探测一次缓存;渲染提交前由 renderShotTo 调用并把结果注入 R 副本。
func (ctx *manjuCtx) pddGuard(lg *manjuLogger) {
	if ctx.pddChecked {
		return
	}
	ctx.pddChecked = true
	ctx.pddOK = ctx.comfy.hasNode("MiniMaxH3PDDAccApply")
	if !ctx.pddOK && strings.Contains(strings.ToLower(str(ctx.R["turbo_lora"])), "pdd_acc") {
		lg.logf("  ⚠️ 检测到 PDD Acc LoRA 但 ComfyUI 缺少 MiniMaxH3PDDAccApply 节点(未装 ComfyUI-MiniMax-H3-PDD-Acc 或未重启),已回退普通 LoRA 模式;装好节点后恢复")
	}
}

// applyPddToR 把 PDD 节点探测结果写入 R 副本(h3RenderWorkflow 读取)
func (ctx *manjuCtx) applyPddToR(R map[string]any) {
	if !ctx.pddChecked {
		return
	}
	R["_pdd_ok"] = ctx.pddOK
}

// ensureComfyReady 渲染/资产/编码等需 Comfy 的阶段前,确保 ComfyUI 在线:
// 不在线自动拉起并轮询等待就绪(最长 120s),不再让续跑/一键渲染直接报"连接被拒绝"。
// 在线则立即返回,零开销。
func (ctx *manjuCtx) ensureComfyReady(lg *manjuLogger) error {
	if _, err := ctx.comfy.online(); err == nil {
		return nil
	}
	lg.logf("  ⚠️ ComfyUI 未运行,自动启动中(首次加载模型约 10-60s,请稍候)…")
	if err := startComfy(); err != nil {
		return fmt.Errorf("ComfyUI 自动启动失败(检查「目录与部署」ComfyUI 安装目录): %w", err)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)
		if _, err := ctx.comfy.online(); err == nil {
			lg.logf("  ✅ ComfyUI 已就绪")
			return nil
		}
	}
	return fmt.Errorf("ComfyUI 启动后 120 秒内未就绪——请到灵动岛/ComfyUI 页查看启动日志,或手动启动后重试")
}

// freeComfyModels 释放 ComfyUI 已加载的模型显存(POST /free 卸载全部驻留模型)。
// 场景:assets 阶段加载的 ZImage(7.6G)+Lumina2(11.7G) 常驻显存,而 H3 预编码需要加载
// Qwen3-VL 32B(14.6G)——RTX 5090 24G 装不下两者,编码任务提交后 ComfyUI 加载阻塞
// (显存不足),NiliX 侧 wait 挂起、GPU 无动静(本 BUG 根因)。编码前释放腾出显存。
func (ctx *manjuCtx) freeComfyModels(lg *manjuLogger) {
	body := bytes.NewBufferString(`{"unload_models": true, "free_memory": true}`)
	req, err := http.NewRequest("POST", ctx.comfy.base+"/free", body)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ctx.comfy.client.Do(req)
	if err != nil {
		if lg != nil {
			lg.logf("  ⚠️ 释放 ComfyUI 显存失败(忽略,继续): " + truncate(err.Error(), 80))
		}
		return
	}
	defer resp.Body.Close()
	// 释放后稍等模型卸载完成(大模型卸载需数秒)
	time.Sleep(2 * time.Second)
	if lg != nil {
		lg.logf("  🧹 已释放 ComfyUI 模型显存(编码前腾出 Qwen3-VL 空间)")
	}
}

func manjuToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// normalizeEpisode 集数(纯数字)→ 集号(EPxx):1→EP01,12→EP12;非纯数字原样(兼容旧 EP01 输入)。
// 集数 0 = 自动按章节数分集,由 manjuRun 特殊处理,不在此转 EP00。
func normalizeEpisode(ep string) string {
	ep = strings.TrimSpace(ep)
	if n, err := strconv.Atoi(ep); err == nil && n > 0 {
		return fmt.Sprintf("EP%02d", n)
	}
	return ep
}

func newManjuCtx(configPath, episode, chapters, only, novel string) (*manjuCtx, error) {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return nil, err
	}
	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
	}
	// 用户规则(2026-08):普通执行管线/一条龙 渲染配置参数一律用用户自己配置的
	// style/neg_prompt(config.json),不自动解析小说总集覆盖;仅「AI 一条龙」(
	// manjuAgentStyleAnalyze)才解析小说提示词/负面提示词并分析给出最优配置。
	// 因此这里不再调用 manjuInjectPromptMaster 自动写回 config。
	epEff := orDefault(episode, str(R["episode"]))
	// ComfyUI 输入/输出目录:优先用「生效启动参数」(comfyParams.in/out,与 ComfyUI 实际
	// 启动命令同步)——项目 config 里的 comfy_input/comfy_output 可能是旧路径(创建项目时
	// 写入的硬编码 Desktop 共享目录),而 ComfyUI 现在按自包含目录启动,两处不一致时
	// 任务"完成"但从错误目录读产物,报「open ... output\xxx.png: not found」。
	// comfyParams 为空(未注入)时回退 config;config 也空则回退共享目录 output/input。
	comfyOut := comfyParams().out
	if comfyOut == "" {
		comfyOut = str(P["comfy_output"])
	}
	if comfyOut == "" {
		comfyOut = filepath.Join(ComfySharedDir, "output")
	}
	comfyIn := comfyParams().in
	if comfyIn == "" {
		comfyIn = str(P["comfy_input"])
	}
	if comfyIn == "" {
		comfyIn = filepath.Join(ComfySharedDir, "input")
	}
	ctx := &manjuCtx{
		configPath:   configPath,
		cfg:          cfg,
		style:        str(cfg["style"]),
		R:            R,
		P:            P,
		project:      filepath.Base(filepath.Dir(configPath)),
		episode:      normalizeEpisode(epEff),
		chapters:     orDefault(chapters, str(R["chapters"])),
		only:         only,
		novel:        str(P["novel"]),
		comfyOutput:  comfyOut,
		comfyInput:   comfyIn,
		sharedModels: filepath.Join(ComfySharedDir, "models"),
		workdir:      str(P["workdir"]),
		qcRerender:   map[int]int{},
	}
	// 视频脚本直出模式:config paths.script 指向 H3 官方格式分镜脚本 md 时启用——
	// ctx.novel 切换到脚本文件(复用指纹/复用校验/章节范围标记),方案由 manjuScriptSystem 直出。
	// 章节范围固定 "script"(脚本无章节概念),保证复用校验稳定;脚本文件变化(指纹)强制重生成。
	// 多集脚本(批量导入后 script/EP01.md..EPxx.md):按集号选对应文件——渲染 EP02 就读
	// script/EP02.md,不再固定读 paths.script 指向的单文件(2026-08-24 用户要求)。
	if sp := strings.TrimSpace(str(P["script"])); sp != "" && fileExists(sp) {
		ctx.scriptMode = true
		if strings.EqualFold(filepath.Base(filepath.Dir(sp)), "script") {
			if epFile := filepath.Join(filepath.Dir(sp), ctx.episode+".md"); fileExists(epFile) {
				sp = epFile
			}
		}
		ctx.novel = sp
		ctx.chapters = "script"
	}
	if ctx.episode == "" {
		ctx.episode = "EP01"
	}
	// 章节范围语义归一(与 manjuRun 同一套规则;抽卡方案/智能体分析/诊断等旁路入口
	// 不再各自漏归一化——"0" 曾被当字面章节号报"章节不存在: 0"):
	// 空/0 = 全书 1-N;集数纯数字 N>0 = 第 N 集即第 N 章(chapters=N-N)
	// 脚本模式跳过归一化(chapters 已固定 "script")
	if !ctx.scriptMode && (ctx.chapters == "" || ctx.chapters == "0") {
		if n := manjuChapterTotal(configPath); n > 0 {
			ctx.chapters = fmt.Sprintf("1-%d", n)
		} else {
			ctx.chapters = "1-3"
		}
	}
	if epNum, err := strconv.Atoi(strings.TrimSpace(epEff)); err == nil && epNum > 0 && !ctx.scriptMode {
		ctx.chapters = fmt.Sprintf("%d-%d", epNum, epNum)
	}
	if novel != "" && !ctx.scriptMode {
		if fileExists(novel) {
			ctx.novel = novel
		}
	}
	ctx.assetsDir = filepath.Join(ctx.workdir, "assets")
	ctx.analysisDir = filepath.Join(ctx.workdir, "analysis")
	ctx.clipsDir = filepath.Join(ctx.workdir, "clips")
	// fs 白名单:项目 config paths 里的绝对路径动态注册(小说/工作目录/Comfy 目录等,
	// 前端经 /api/fs/* 预览产物/封面才不会被白名单拦截)
	// 审查 F1 收紧:novel/novel_dir 若不在小说库根内不注册(防历史/恶意 config 指向任意
	// 目录后,经 GET 免 token 入口 newManjuCtx 动态扩白名单 → GET /api/fs/read 任意文件读)
	for _, k := range []string{"novel", "novel_dir", "workdir", "analysis", "assets", "clips", "comfy_input", "comfy_output"} {
		if v := strings.TrimSpace(str(P[k])); v != "" && filepath.IsAbs(v) {
			if k == "novel" || k == "novel_dir" {
				if _, gerr := manjuGuardNovel(v); gerr != nil {
					continue // 越界小说路径不扩白名单
				}
			}
			addFSRoot(v)
		}
	}
	ctx.llm = manjuLLMFromCfg(cfg)
	ctx.llm.onUsage = func(model string, u agent.Usage) { manjuStatsAdd(ctx.project, model, u) }
	// 停止感知:LLM 长请求(方案生成/提示词生成/审片判分)在用户点「停止」后立即放弃,
	// 不再等 300s 超时——"停止无反应"的最后一个残留点
	ctx.llm.SetStopped(func() bool {
		manjuState.mu.Lock()
		defer manjuState.mu.Unlock()
		return manjuState.stopped
	})
	ctx.comfy = newComfyClient(str(R["comfy_url"]))
	// 接缝 latent 命名空间(审计 S6):h3_context/<项目>_<集>/clip_NNNNN——此前 latent 只按镜头号
	// 落 output/h3_context/ 全局共享,项目 B 定点重渲会接续项目 A 的画面;草稿/定稿分辨率也混用
	ctx.R["_latent_ns"] = reNonWord.ReplaceAllString(ctx.project, "_") + "_" + reNonWord.ReplaceAllString(ctx.episode, "_")
	// turbo LoRA 存在性归一化(必须早于 ctx.steps 计算:回退后按实际生效的 LoRA 取步数)
	ctx.normalizeTurboLora()
	if n, ok := manjuToInt(R["width"]); ok && n > 0 {
		ctx.w = n
	} else {
		ctx.w = 768
	}
	if n, ok := manjuToInt(R["height"]); ok && n > 0 {
		ctx.h = n
	} else {
		ctx.h = 1344
	}
	ctx.fps = 24
	if n, ok := manjuToInt(R["fps"]); ok && n > 0 {
		ctx.fps = n
	}
	ctx.seed = 1688
	if n, ok := manjuToInt(R["seed"]); ok {
		ctx.seed = n
	}
	ctx.minSec, ctx.maxSec = 4, 12
	if n, ok := manjuToInt(R["min_shot_seconds"]); ok && n > 0 {
		ctx.minSec = n
	}
	if n, ok := manjuToInt(R["max_shot_seconds"]); ok && n > 0 {
		ctx.maxSec = n
	}
	// 中文语音字速预算(2026-08-26):台词+旁白总字数÷字速 ≤ 镜头时长;默认 4 字/秒(保守,
	// 爽文技能契约上限 5)。超预算在 validatePlan 报问题(脚本模式自动补偿时长)
	ctx.charsPerSec = 4.0
	if f, ok := manjuToFloat(R["chars_per_sec"]); ok && f >= 2 && f <= 8 {
		ctx.charsPerSec = f
	}
	ctx.steps = 20
	if n, ok := manjuToInt(R["steps"]); ok && n > 0 {
		ctx.steps = n
	}
	if str(R["turbo_lora"]) != "" {
		// Turbo 步数:用户显式配置优先,缺省按 LoRA 类型参数表(旧系 8 步,Kijai 4 步版 4 步)
		if n, ok := manjuToInt(R["turbo_steps"]); ok && n > 0 {
			ctx.steps = n
		} else {
			ctx.steps = turboLoRASpecOf(str(R["turbo_lora"])).Steps
		}
	}
	// seed 重试策略(fixed 默认;非法值回退 fixed)
	ctx.seedPolicy = "fixed"
	if s := str(R["seed_policy"]); s == "increment" || s == "random" {
		ctx.seedPolicy = s
	}
	// 分辨率档位:非 custom 时覆盖手动宽高(等比缩放对齐 32)
	ctx.resTier = str(R["res_tier"])
	if t := ctx.resTier; t != "" && t != "custom" {
		if tw, th, ok := manjuResTierDims(t, ctx.w, ctx.h); ok {
			ctx.w, ctx.h = tw, th
		}
	}
	// 手动/custom 宽高也强制 32 倍数(H3 VAE 32× 下采样网格;审计 S10——
	// 此前未对齐直接进 EmptyMiniMaxH3LatentAV,非法尺寸 ComfyUI 400 或产出破损)
	ctx.w, ctx.h = manjuAlign32(ctx.w), manjuAlign32(ctx.h)
	ctx.draftScale = 0.5
	if v, ok := manjuToFloat(R["draft_scale"]); ok && v >= 0.2 && v <= 0.95 {
		ctx.draftScale = v
	}
	ctx.draftJudge, _ = R["draft_judge"].(bool)
	return ctx, nil
}

// normalizeTurboLora 渲染入口 turbo LoRA 文件存在性归一化(2026-08-26):
// 配置引用的 LoRA 文件不在磁盘时,自动回退到 loras 目录里第一个同族文件并写回项目 config
// ——「minimax_h3_turbo_4step_ema.safetensors(未找到)」类报错的根治:该旧默认名(4step EMA)
// 随 Turbo LoRA 部署升级停发,新项目默认已对齐新版(见 manjuDefaultConfig),存量项目经此自愈;
// 回退同时重算 ctx.steps 依赖的 R 值,体检/自检/渲染全链路不再报缺失。
// 找不到同族文件时保持原值不动(不静默清配置,渲染时 ComfyUI 仍会报具体错误)。
func (ctx *manjuCtx) normalizeTurboLora() {
	loraDir := filepath.Join(ctx.sharedModels, "loras")
	entries, err := os.ReadDir(loraDir)
	if err != nil {
		return // 目录不存在(测试环境/未部署):不动
	}
	var hasLora []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".safetensors") {
			hasLora = append(hasLora, e.Name())
		}
	}
	if len(hasLora) == 0 {
		return
	}
	firstOf := func(pat string) string {
		for _, n := range hasLora {
			if strings.Contains(strings.ToLower(n), pat) {
				return n
			}
		}
		return ""
	}
	fix := func(key, pat string) string {
		cur := strings.TrimSpace(str(ctx.R[key]))
		if cur == "" || fileExists(filepath.Join(loraDir, cur)) {
			return ""
		}
		if fb := firstOf(pat); fb != "" && fb != cur {
			ctx.R[key] = fb
			return key + " " + cur + " → " + fb
		}
		return ""
	}
	var msgs []string
	if m := fix("turbo_lora", "fl2v"); m != "" {
		msgs = append(msgs, m)
	}
	if m := fix("turbo_lora_r2v", "ref2v"); m != "" {
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return
	}
	ctx.loraRepair = "🔄 Turbo LoRA 自动修复: " + strings.Join(msgs, "; ") +
		"(配置引用的 LoRA 文件不存在,已回退磁盘实际存在的同族 LoRA 并写回项目 config)"
	if err := writeManjuConfig(ctx.configPath, ctx.cfg); err != nil {
		ctx.loraRepair += "(config 写回失败,仅本次运行生效: " + truncate(err.Error(), 60) + ")"
	}
}

// latentNS 接缝 latent 命名空间(审计 S6):<项目>_<集>,防跨项目/跨方案/草稿定稿分辨率串接
func (ctx *manjuCtx) latentNS() string {
	ns := strings.TrimSpace(str(ctx.R["_latent_ns"]))
	if ns == "" {
		return "default"
	}
	return ns
}

// seedFor 镜头渲染 seed:每镜独立基线 = 配置 seed + 镜头号(2026-08-25 用户反馈:
// 镜头 4/7 渲染出相同视频——同 h3_prompt 时条件缓存共用,同 seed 必出同画;
// 加镜头号后即使两镜提示词完全相同,噪声不同也不会逐帧雷同)。
// 重试策略:fixed=恒定基线(重渲 attempt>0 时 seed+attempt,否则同 seed 同画面质检死循环);
// increment=第 N 次重试 seed+N;random=重试换新随机(首渲仍用基线)。
func (ctx *manjuCtx) seedFor(shotID, attempt int) int {
	base := ctx.seed + shotID
	switch ctx.seedPolicy {
	case "increment":
		return base + attempt
	case "random":
		if attempt > 0 {
			return randSeed()
		}
	default:
		// fixed:重渲(attempt>0)也换 seed——否则同 seed 同画面,质检重渲/定点返工/终审重渲
		// 永远产出相同结果,质检不过的死循环无法打破
		if attempt > 0 {
			return base + attempt
		}
	}
	return base
}

// ---- 2026-08-25 用户规则:渲染提示词最终化(违规词同义/谐音替换 + 多余人脸硬约束) ----

// manjuRenderWordReplace 渲染提示词违规词 → 同义/谐音替换表(用户规则:检测到违规词应替换为
// 相同词意或谐音词,避免 H3/云端内容审核拒绝或渲染异常——分镜 2/6 未正常渲染的根因排查项)。
// 覆盖技能违规词库的[慎用]暴力血腥/迷信邪教高频词 + [硬禁]常见词(理论上小说 QA 已拦截,兜底)。
var manjuRenderWordReplace = map[string]string{
	// [慎用]暴力血腥 → 同义柔和(仙侠化间接表达)
	"碎尸": "残躯", "分尸": "遗体分离", "肢解": "身躯碎裂", "剖腹": "腹部重创",
	"挖眼": "双目重创", "剥皮": "皮开肉绽", "凌迟": "酷刑加身", "酷刑": "严刑",
	"虐杀": "残害", "烹人": "炼化躯体", "食人": "吞噬血肉", "吃人肉": "吞噬血肉",
	"喝人血": "噬血", "活埋": "困于地底", "五马分尸": "众力撕裂", "千刀万剐": "百刃加身",
	"割喉": "颈间重创", "砍头": "身首异处", "斩首示众": "枭首示众",
	"灭门惨案": "灭门之祸", "屠城": "血洗城池", "大屠杀": "血腥屠戮",
	"血肉模糊": "伤痕累累", "开膛破肚": "腹部重创", "尸横遍野": "尸骸遍地",
	"血流成河": "血水漫地", "满门抄斩": "满门获罪",
	// [慎用]迷信邪教
	"邪教": "旁门左道", "洗脑": "蛊惑心智", "活人祭祀": "活祭", "人祭": "活祭",
	"血祭": "血契", "童男童女": "稚童", "生剖": "生取", "巫蛊": "蛊术", "降头": "咒术",
	"养小鬼": "豢养邪物", "扎小人": "咒人偶", "请神上身": "借神之力",
	// [硬禁]常见词(不应出现,兜底替换)
	"强奸": "强迫侵犯", "轮奸": "多人施暴", "迷奸": "迷药加害", "卖淫": "堕落风尘",
	"嫖娼": "寻花问柳", "自慰": "自我纾解", "手淫": "自我纾解", "色情": "靡靡",
	"淫秽": "不堪入目", "裸照": "不雅之影", "艳照": "不雅之影", "海洛因": "毒物",
	"冰毒": "毒物", "大麻": "迷幻草", "摇头丸": "迷幻丹", "可卡因": "毒物",
	"鸦片": "迷烟", "吗啡": "镇痛禁药", "吸毒": "误食毒物", "贩毒": "贩卖禁物",
	"制毒": "炼制禁物", "毒品交易": "禁物交易", "幼女": "幼童", "拐卖儿童": "拐骗孩童",
	"童工": "年幼苦工", "恋童": "畸恋幼者", "儿童色情": "秽图", "猥亵儿童": "冒犯幼童",
	"买卖儿童": "贩售孩童", "反党": "悖逆", "反政府": "逆乱", "反华": "悖逆",
	"辱华": "轻慢上邦", "颠覆国家": "动摇社稷",
}

// manjuSanitizeRenderWords 检测并替换文本中的违规词,返回替换后的文本与替换记录(词→次数)。
// 只在渲染提交前执行,不改写方案/正文(台词逐字约束针对 LLM 生成环节,渲染输入可安全化)。
func manjuSanitizeRenderWords(s string) (string, map[string]int) {
	repl := map[string]int{}
	out := s
	for w, safe := range manjuRenderWordReplace {
		if strings.Contains(out, w) {
			repl[w] = strings.Count(out, w)
			out = strings.ReplaceAll(out, w, safe)
		}
	}
	return out, repl
}

// manjuFrameGuard H3 多余人脸硬约束(2026-08-25 用户反馈:镜头 05 出现多余不相干人脸;
// 2026-08-27 二修:分镜 4 出现重复人物——无名群演(牢卒)未定义 Subject 时模型自由发挥,
// 从参考图复制了白发管事的形象):
// 有角色镜追加「画面中只允许出现本镜角色,绝对没有其他人/多余脸/路人/人群」的英文硬约束——
// 正面列名 + 排除句双管齐下(H3 对否定句敏感,与写作规则 11/19/33 同口径)。
// 二修追加「每人独立形象/禁止复制同人/禁止复用他人形象」——自由发挥的群演不复用参考图人物脸。
const manjuFrameGuard = "FRAME DISCIPLINE: this shot contains ONLY the characters listed in subject_definitions; every face in the frame belongs to these characters alone - absolutely no other people, no extra faces, no bystanders, no passers-by, no crowd, no background figures with visible faces; every person in the frame is a distinct individual with their own unique appearance - never show the same character twice, never duplicate a face, never reuse another character's look for an extra person"

// manjuConsistencyGuard 身份一致性纪律(2026-09-01 知识库「五步导演法」整合):
// 图生视频翻车第一因=五官漂移/发型变/服装变色/配饰消失——官方/社区一致推荐提示词
// 主动声明一致性约束(「保持参考图中人物脸部特征/发型/服装/配饰/身体比例/位置/场景
// 布局/光线方向一致」+「appearance and costume remain unchanged throughout the shot」)。
// 注入条件:提示词引用 <Picture N>(有人物参考图);无参考图镜不注入(文字描述自行成型)。
const manjuConsistencyGuard = "IDENTITY CONSISTENCY: the characters' facial features, hairstyles, outfit styles and colors, accessories, body proportions, positions in the frame, and the scene layout and lighting direction must remain unchanged throughout this shot, matching the reference pictures exactly - no facial drift, no hair or costume changes, no lost accessories"

// manjuMotionGuard 运动纪律(2026-08-27 用户反馈"像PPT/运镜不电影级"):H3 长镜存在
// 运动衰减——动作早早 settle 后画面趋静止,叠加段尾固有冻结,成片观感即幻灯片。这里在
// 提示词尾部强注入持续运动要求(镜头运动/人物动作/环境动态三选一持续到最后帧),
// 配合 QC 段尾冻结自动截尾双管齐下:提示词尽量让运动持续,残余冻结尾巴程序裁掉。
// (官方 MotionContext README 同结论:"a held framing with nothing happening renders
// as a literal freeze"——hold 必须有事做:a breath, a weight shift, an eyeline change)
const manjuMotionGuard = "MOTION DISCIPLINE: maintain continuous visible motion through every second of this clip until the final frame - camera movement (push/dolly/pan/handheld drift), character action, or environmental motion (light flicker, floating particles, moving fabric and hair) must never fully stop; the clip must not settle into a static freeze frame before the end"

// manjuChainGuard 链式衔接纪律(2026-08-27 官方 MotionContext README 核验转化,
// 对治"镜头间不连贯"与"多出不相干人脸"):
// ①气闸(airlock):接缝镜开头先保持上一镜收尾构图约1秒再发展新内容,官方实测这种
//   衔接"measure tighter than an ordinary frame-to-frame cut";
// ②矛盾=并集:提示词的人物安排若与 pinned 开头帧矛盾,模型不会二选一而是全部渲染
//   (union)——这正是多余人脸的深层机理,FRAME DISCIPLINE 只能防"凭空多人",
//   防不了"构图矛盾加人";
// ③静止 hold 要有事做(与 MOTION DISCIPLINE 呼应)。
// 措辞条件化("if/otherwise")兼容独立返工重渲(fresh 无 pinned 头时自然落空),
// 因此对所有镜头恒定注入——指纹与链式状态解耦,不会因 latent 文件有无漂移。
const manjuChainGuard = "CHAIN DISCIPLINE: if this clip opens on pinned continuation frames from the previous clip, hold that exact closing composition for about one second with no new subjects and no dialogue, then develop into this clip's own content; the arrangement of people at the clip opening must match the pinned frames - a contradicting arrangement renders as a union and puts extra people and faces into the frame; otherwise open directly with this clip's own establishing framing. During any held beat keep small visible motion alive (a breath, a weight shift, an eyeline change, fabric or hair movement)"

// manjuExecutionGuard 执行纪律(2026-08-29 用户反馈「视频内容与小说差距大/看不懂」):
// ASR 实证台词逐字念出、提交 prompt 与脚本一致,但画面不执行脚本动作——该回头的镜
// 人物一直走、该咧嘴的没咧嘴、旁白时段画面静止,且会脑补脚本外同行角色(Shot4 实锤:
// 脚本只有阿凯独行,画面出现另一持剑角色)。纯文本纪律对 H3 的「静止偏好」约束有限,
// 这里是正向指令:动作必须完整演出且幅度可感知;说话者说话时必须有可见反应;
// 画外音必须引发画面主体反应;画面只允许出现 subject_definitions 列出的主体。
// 2026-08-29 二修:删除「lip movements must be performed」与「说话者 mouth movement」
// 措辞——H3 会把「说话」动作默认派给画面主体,画外音/旁白镜里主角全程动嘴
// (用户实测镜4「都是主角一个人在动嘴说」),唇动约束改由 LIP DISCIPLINE 独立承载。
const manjuExecutionGuard = "EXECUTION DISCIPLINE: act out every scripted action in detailed_description visibly and completely - stomps, head turns, grins, shrugs and gestures must be performed with clearly perceivable amplitude, never reduced to a static standing pose; the on-screen character who is visibly delivering a line must show a matching expression change at the moment of the line; an off-screen voice must provoke a visible response from the on-screen character it addresses (head turn, halt, glance, expression) while keeping that character's lips closed; the frame contains ONLY the subjects listed in subject_definitions - never add a companion, passer-by or extra person that the description does not explicitly mention"

// manjuLipGuard 唇动纪律(2026-08-29 用户反馈「镜头4 都是主角一个人在动嘴说」):
// 画外音(off-screen voiceover)/旁白/内心独白镜,画面角色嘴唇必须完全闭合——
// H3 默认把台词「表演」给画面主体,画外喊话时主角张嘴对口型,音画错乱。
// 显式禁止:画外音期间任何人不得动嘴(可以转头/停步/表情反应,嘴必须闭)。
const manjuLipGuard = "LIP DISCIPLINE: speech is performed ONLY by the on-screen character who is visibly speaking the line with their own voice; when a line comes from an off-screen voice, a narrator, or a character's inner monologue, EVERY on-screen character's lips remain completely closed for the whole line - they may turn their head, halt, glance, frown or react with body language, but never open their mouth, never mouth the words, never move their lips in speech"

// ---- 2026-08-25 角色板(角色资料卡)资产:即梦「角色版控制一致性」方法落地 ----
// 知识库「创作管理/AI漫剧/制作链路/角色板控制一致性教程_即梦角色版.md」:
// 用角色板(多视图+细节特写+服饰分层+表情神态+配色HEX+人设文字整合图)替代三视图控一致性,
// 一张角色板即可做整部剧。角色板图只作角色资产/展示/素材,不参与 H3 渲染参考
// (渲染参考仍是正脸特写+全身视图,避免网格图干扰)。

// manjuBoardLayout 角色板布局兜底指令(LLM 未直出 board_prompt 时拼在 image_prompt 后)
const manjuBoardLayout = ", character reference board (role info sheet): a clean vertical grid sheet combining: 1) three full-body views side by side (front / side profile / back); 2) close-up detail panels (face, hairstyle, costume embroidery, accessories); 3) layered costume display (outer robe / inner garment / belt, woven patterns); 4) 4-6 expression headshots; 5) a 5-color HEX palette swatch row; 6) one line of Chinese character bio text. light plain background, neat grid layout, all panels showing the same character with identical face and costume"

// manjuBeastBoardLayout 兽类角色板布局(2026-08-26 用户实测「灵宠 · 吞吞_board」出人形):
// 兽形三视图+兽体细节,显式禁人形——LLM 的 board_prompt 模板与人类布局常量都是人形措辞,
// 兽类角色照抄必出人物形象。
const manjuBeastBoardLayout = ", creature reference board (beast info sheet): a clean vertical grid sheet combining: 1) three full-body views of the same beast creature side by side (front / side profile / back); 2) close-up detail panels (creature head and face, fur or scale texture, markings, paws or claws, tail); 3) 4-6 expression headshots of the creature; 4) a 5-color HEX palette swatch row; 5) one line of Chinese creature bio text. light plain background, neat grid layout, all panels showing the same beast creature with identical fur/scale colors and markings, NOT a human, NOT a humanoid, no human body, no human face, no human clothes"

// manjuViewGenderAnchor 视图性别锚(2026-08-26 用户实测「墨姨_side」女性被画成有胡须的男人):
// Z-Image 高 denoise 重绘下阴性约束(no beard)权重弱,必须正向强化性别;gender 空不加
func manjuViewGenderAnchor(m map[string]any) string {
	if manjuIsBeast(m) || manjuIsItem(m) {
		return ""
	}
	if manjuIsFemale(m) {
		return ", a woman, clearly feminine facial features and physique"
	}
	if str(m["gender"]) == "男" {
		return ", a man, clearly masculine facial features"
	}
	return ""
}

// manjuColorAnchor 配色板锚(角色卡 color_palette → 英文 HEX 色板注入,统一全部角色图配色)
func manjuColorAnchor(m map[string]any) string {
	cp := str(m["color_palette"])
	if cp == "" {
		return ""
	}
	return ", color palette (strictly use these HEX colors for the whole character design): " + cp
}

// manjuBoardPromptFor 角色板提示词:优先 LLM 直出 board_prompt;兜底 image_prompt+角色板布局+配色。
// 兽类(2026-08-26):LLM board_prompt 模板是人形措辞,兽类一律忽略,直接用兽形板布局。
func (ctx *manjuCtx) manjuBoardPromptFor(char string, m map[string]any) string {
	// 兽类与物品都忽略 LLM board_prompt(模板是人形措辞,2026-08-29 审计补物品豁免)
	if !manjuIsBeast(m) && !manjuIsItem(m) {
		if p := str(m["board_prompt"]); p != "" {
			return p
		}
		if vs, ok := m["views"].(map[string]any); ok {
			if p := str(vs["board"]); p != "" {
				return p
			}
		}
	}
	base := str(m["image_prompt"])
	if base == "" {
		base = "portrait of " + char
	}
	if manjuIsItem(m) {
		base = manjuItemStrip(base)
	}
	layout := manjuBoardLayout
	if manjuIsBeast(m) {
		layout = manjuBeastBoardLayout
	} else if manjuIsItem(m) {
		layout = manjuItemBoardLayout
	}
	return base + layout + manjuColorAnchor(m)
}

// manjuAudioGuard 台词纪律(2026-08-27 用户反馈:03 镜对话重复)。实测(ASR 实证)H3 会把
// 六段式描述里的"说话动作"间接引语(如 "She lifts her gaze and asks one flat question")
// 也合成成语音——与 <d> 标记台词并行 = 同义台词念两遍/凭空加戏。对白与旁白的官方载体
// 都是 <d> 标签(旁白=off-screen voiceover 句式同样包 <d>,见 manju_script_parse.go
// 机械质检自动补写),故此处只认 <d>:描述文字一律不发声。
const manjuAudioGuard = "AUDIO DISCIPLINE: every spoken line in this clip comes ONLY from the text inside <d> tags; never voice, paraphrase, repeat, translate or invent any other dialogue; narration and description sentences outside <d> tags must never be spoken aloud; if this clip opens on pinned continuation frames from the previous clip, keep that opening beat completely silent before the first marked line"

// manjuNoRefGuard 无参考图人物镜纪律(2026-08-27 用户反馈:04/05 镜周管事渲染两次)。
// 镜头有人物(subject_definitions)但登场角色为空(群演/无卡次要角色,如牢头/牢卒)——
// 提示词里的 <Picture N> 引用悬空(未挂任何参考图),模型转而从链式上一镜复制形象,
// 把已登场角色(白发周管事)的长相套到其他主体身上 = 同一人物入画多次。
const manjuNoRefGuard = "REFERENCE NOTE: no reference picture is attached to this clip; ignore any <Picture N> mention, render each subject strictly as described in subject_definitions, with each person clearly distinct from the others (different age, build, hairstyle and clothing as described); never copy one subject's appearance onto another subject or onto any background figure"

// manjuFinalizePromptPure 渲染输入最终化·纯函数版(2026-08-27 审计修复:指纹对称)。
// 旧版 guard 只在 renderShotTo 的值拷贝里注入,manifestMark 记的是"含 guard"指纹,
// 而下次运行的 stale 检查用原始 prompt 算指纹——有角色镜恒 stale 反复重渲。现在指纹
// 计算与渲染共用本函数,两侧永远一致(幂等:已含关键短语不重复追加)。
// reDialogueTagNoise 已废止(2026-08-30):官方 base-en §4.4/ref-en §5.4 要求 <d> 内
// 必须带语言标签,正确形态是 <d>[Chinese]原文</d>;旧剥除把语言标签整个去掉,模型只能
// 猜配音语言。规范化职责移交 manju_prompt_align.go alignDialogueLangTags——
// [中文]/[chinese] 统一改写为官方英文写法 [Chinese],裸中文开标签自动补标,
// 指纹走同一条对齐链,改词即缓存失效。

// manjuFinalizePromptPure 渲染提示词最终化纯函数(指纹/编码/渲染三处一致)。
// picSlots:本镜实际提交的参考图槽位数(人物视图数+场景图 0/1)——2026-08-28 Picture
// 引用对齐:h3(脚本直出)按叙述写 <Picture N>,与渲染端实际提交顺序(人物图前+场景图
// 后)不保证一致;超界引用(道具句 Picture N>槽位数)会错位指到别的图,必须剥除。
// 无人物图镜(picSlots<=1 且 hasChars=false)的人物主体句 Picture 引用同样剥除——
// 唯一槽位是场景图,人物句引用它=拿场景图当人脸参考(EP01 镜3 实锤:城市夜景图被当
// 陈默长相,<Picture 1> is Chen Mo 完全错位)。
func manjuFinalizePromptPure(hp string, hasChars bool, picSlots int) string {
	if hp == "" {
		return hp
	}
	if out, _ := manjuSanitizeRenderWords(hp); out != "" {
		hp = out
	}
	hp = manjuStripDanglingPictureRefs(hp, hasChars, picSlots)
	// 人物纪律覆盖面(2026-08-27 修复):frameGuard 旧条件是 hasChars(登场角色非空),
	// 群像无卡镜(subject_definitions 定义了牢头/牢卒等群演,但 characters 为空)整段漏掉
	// ——无参考图+无纪律,链上白发形象被复制给每个主体 = 周管事入画多次。有主体即约束。
	// 判定用带冒号的段首标记:纪律文本会引用 "subject_definitions" 一词(EXECUTION/
	// FRAME guard),无冒号判定会被自身注入的纪律文本二次触发,破坏幂等(2026-08-29)。
	hasSubjects := strings.Contains(hp, "subject_definitions:")
	if (hasChars || hasSubjects) && !strings.Contains(hp, "no extra faces") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuFrameGuard
	}
	// 无人物参考图的人物镜:声明按文字各自成型、禁止形象互抄。有场景图时(picSlots>=1)
	// 明示唯一挂图是环境参考而非人脸参考(EP01 镜3:城市夜景图被 <Picture 1> is Chen Mo
	// 错位引用,H3 拿场景图当人物长相)。幂等锚=REFERENCE NOTE 开头串,两种变体共用。
	if !hasChars && hasSubjects && !strings.Contains(hp, "REFERENCE NOTE:") {
		guard := manjuNoRefGuard
		if picSlots >= 1 {
			guard = "REFERENCE NOTE: the only attached picture is a scene/environment reference, NOT a person; ignore any <Picture N> mention on human subjects and render every person strictly as described in subject_definitions, with each person clearly distinct from the others (different age, build, hairstyle and clothing as described); never copy any face, hairstyle or clothing from the attached scene picture onto any person"
		}
		hp = strings.TrimRight(hp, " \n") + "\n" + guard
	}
	if !strings.Contains(hp, "AUDIO DISCIPLINE") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuAudioGuard
	}
	if !strings.Contains(hp, "MOTION DISCIPLINE") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuMotionGuard
	}
	if !strings.Contains(hp, "EXECUTION DISCIPLINE") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuExecutionGuard
	}
	if !strings.Contains(hp, "LIP DISCIPLINE") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuLipGuard
	}
	if !strings.Contains(hp, "CHAIN DISCIPLINE") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuChainGuard
	}
	// 2026-09-01 身份一致性纪律(知识库五步导演法):有人物参考图(<Picture N> 引用)
	// 才注入——无参考图镜注入反而约束文字自由成型;幂等锚=IDENTITY CONSISTENCY
	if strings.Contains(hp, "<Picture ") && !strings.Contains(hp, "IDENTITY CONSISTENCY") {
		hp = strings.TrimRight(hp, " \n") + "\n" + manjuConsistencyGuard
	}
	return hp
}

// finalizeShotPrompt 渲染输入最终化(2026-08-25 用户规则):违规词替换 + 多余人脸硬约束
// + 运动纪律(2026-08-27)。renderShotTo 入口调用一次——缓存指纹(shotCondFingerprintAt)
// 与预编码(ensureEncodedAt)都用同一份最终化文本,保证指纹/编码/渲染三处一致。
func (ctx *manjuCtx) finalizeShotPrompt(hp string, s manjuShot, lg *manjuLogger) string {
	return ctx.finalizeShotPromptSlots(hp, s, ctx.shotPicSlots(s), lg)
}

// finalizeShotPromptSlots 带显式槽位参数的最终化变体:renderShotTo/指纹走实测槽位
// (shotPicSlots 探测资产文件);plan 汇点(ensurePlanAndPrompts,资产未生成)走预期槽位
// (manjuExpectPicSlots)——2026-08-29 绿萝实锤:汇点在定妆照生成之前按实测 0 槽剥光
// 全部 <Picture N> 引用并回写固化进 plan,渲染时资产已在也救不回,EP01 25 镜人物参考
// 图整集失效(人物长相/服装与角色卡无关的根因)。
func (ctx *manjuCtx) finalizeShotPromptSlots(hp string, s manjuShot, picSlots int, lg *manjuLogger) string {
	if hp == "" {
		return hp
	}
	before := hp
	// 音色绑定兜底(2026-08-29):脚本直出路径的 h3_prompt 是脚本原文逐字保留,不含
	// <Audio N> 定义——ref_audios 挂载同序但 prompt 无引用,H3 音色跟随不生效
	// (音色锁定前提是 prompt 有 <Audio> 引用,见 ensureVoiceBindings 注释)。
	// LLM 生成路径在 genShotPromptRaw 已注入,这里只补脚本直出/落盘读回的缺失镜。
	// 登场角色绑定之后接续注入画外说话者(路人/群众喊话)差异化音色——用户反馈
	// 「路人配音和主角配音都是主角在说话」:画外音无 <Audio> 引用时 H3 用默认/主角
	// 音色念所有画外音;按声线描述(性别/年龄/语气)分配独立音色并挂 ref_audios。
	vbs := ctx.voiceBindingsFor(s)
	hp = ensureVoiceBindings(hp, vbs, ctx.refContractFor(s))
	innerCid, innerKey := ctx.innerVoiceFor(s) // 内心戏角色音色(2026-08-30 ver14,问题⑥)
	obs := ctx.manjuOffscreenBindings(hp, innerCid, innerKey)
	// 画外编号从补写后最大 <Audio N> 接续(2026-08-30 五问整改:ensureVoiceBindings
	// 逐角色补写后编号可能超过 len(vbs),再按 len(vbs) 起步会与补写定义编号冲突)
	hp = injectOffscreenVoiceBindings(hp, obs, maxAudioNum(hp))
	// 画外音唇动任务句注入 summary(2026-08-29 二修):LIP DISCIPLINE 在 prompt 队尾,
	// H3 对尾部约束注意力弱(实测镜4 画外音时主角仍对口型)。summary 是模型的任务定义
	// 段,服从度最高——存在 off-screen voiceover 时在 summary 追加「画外音期间所有
	// 画面角色嘴唇闭合」的任务句。幂等(已含锚词跳过)。
	// 先移除旧任务句(位置错误自愈,2026-08-29):早期版本把 TASK 插到 prompt 末尾
	// (Windows \r\n 换行导致 "\n\n" 定位失败),幂等锚「已含 OFF-SCREEN LINES」会跳过
	// 重新注入——必须「先删后插」,存量 plan 才能自愈到正确位置。
	reTask := regexp.MustCompile(`(?m)^\s*OFF-SCREEN LINES TASK:[^\r\n]*\r?\n?`)
	hp = reTask.ReplaceAllString(hp, "")
	if len(obs) > 0 && !strings.Contains(hp, "OFF-SCREEN LINES") {
		// 插到 detailed_description: 段标题之前(任务句紧邻画面描述,H3 注意力最强;
		// summary 段定位曾被注入的混合换行 \n\r\n 破坏,2026-08-29 弃用)。
		di := strings.Index(hp, "detailed_description:")
		if di < 0 {
			di = len(hp)
		}
		task := "OFF-SCREEN LINES TASK: all spoken lines marked as off-screen voiceover are delivered by unseen speakers NOT present in the frame; during every off-screen line, every on-screen character's lips remain completely closed - they listen and react with head turns, glances and expressions only, never mouthing the words\n\n"
		hp = hp[:di] + task + hp[di:]
	}
	// 契约对齐(2026-08-30 H3 官方源码核验:Picture/Audio 编号=挂载顺序、Sx 全局
	// 稳定、<d>[Chinese] 标签、时码 clip-local)在 guard 注入之前执行——注入的
	// Audio 定义行同样被规范化;与指纹侧(shotCondFingerprintAt→finalizeAlignedPrompt)
	// 走同一函数链,指纹与渲染输入恒一致。2026-08-30 ver14:统一走
	// finalizeAlignedPrompt(全局说话人注册表 + Audio 定义行音色指纹短语)。
	hp = ctx.finalizeAlignedPrompt(hp, s, picSlots)
	// ①违规词替换(同义/谐音)——日志提示
	if _, repl := manjuSanitizeRenderWords(before); len(repl) > 0 {
		var parts []string
		for w, n := range repl {
			parts = append(parts, fmt.Sprintf("%s×%d→%s", w, n, manjuRenderWordReplace[w]))
		}
		sort.Strings(parts)
		lg.logf(fmt.Sprintf("  ⚠️ 镜头 %d 检测到违规词,已同义/谐音替换: %s", s.ID, strings.Join(parts, "、")))
	}
	return hp
}

// finalizeAlignedPrompt 渲染/指纹统一汇点(2026-08-30 ver14):契约对齐(带全局说话人
// 注册表,跨镜 Sx 稳定)+ 纪律注入 + Audio 定义行音色指纹短语注入。指纹侧与渲染侧
// 必须走同一函数链(否则对齐改词不触发重渲或恒 stale);speakerReg 为空时退化为
// 既有 manjuFinalizeAligned 行为。
func (ctx *manjuCtx) finalizeAlignedPrompt(hp string, s manjuShot, picSlots int) string {
	c := ctx.refContractFor(s)
	out := manjuFinalizeAlignedReg(hp, c, s.Duration, s.Dialogue, len(s.Characters) > 0, picSlots, ctx.speakerReg)
	// 2026-08-30 五问整改(问题②乱对嘴型):画外说话句机械 off-screen 标注——
	// 对齐层把画面角色写成 <Subject N> (Sx) says,裸 (Sx) says: 即画外说话者,
	// 未标 off-screen 时 H3 会把台词安给画面角色动嘴。纯函数,指纹/渲染共用。
	out = markOffscreenSays(out)
	// 2026-08-30 五问整改(问题④运镜垃圾):分镜运镜列三要素机械注入——
	// LLM 软规则可忽略,纪律句是渲染前硬兜底;「固定」镜与 MOTION DISCIPLINE
	// 不冲突(static camera + 画面动作/环境动效持续,官方三选一)。
	out = injectCameraDiscipline(out, s.Camera)
	// 2026-08-30 五问整改(问题③站位不清):站位纪律硬注入——存量脚本六段式
	// 站位稀疏(EP01 14 镜 8 镜零位置词)且「脚本即权威」无人补,纪律句强制
	// 每个登场角色带屏幕位置+朝向,新渲染即生效。
	out = injectPositionDiscipline(out)
	return ctx.injectAudioTimbrePhrases(out, c)
}

// manjuCameraPhrase 分镜运镜列 → 英文三要素短语(2026-08-30 五问整改,问题④):
// ①括号内英文直取(「缓推（Push In, small, slow）」→ Push In, small, slow);
// ②「固定」→ static locked-off camera;③中文词走映射表兜底;④解析不出返回空
// (不注入,信任既有提示词文本)。
func manjuCameraPhrase(camera string) string {
	cm := strings.TrimSpace(camera)
	if cm == "" {
		return ""
	}
	// ①括号内英文三要素(rune 级索引,全角括号 3 字节不能按字节切)
	if rs := []rune(cm); len(rs) > 0 {
		for k, ch := range rs {
			if ch != '（' && ch != '(' {
				continue
			}
			rest := rs[k+1:]
			for m, c2 := range rest {
				if c2 != '）' && c2 != ')' {
					continue
				}
				en := strings.TrimSpace(string(rest[:m]))
				if len([]rune(en)) >= 3 && !strings.ContainsAny(en, "《<>") {
					return en
				}
				break
			}
			break
		}
	}
	// ②固定机位
	if strings.Contains(cm, "固定") {
		return "static locked-off camera"
	}
	// ③中文映射表(注意顺序:复合词在前,单字在后)
	table := []struct{ zh, en string }{
		{"低机位", "low-angle shot"},
		{"贴地", "ground-level shot"},
		{"过肩", "over-the-shoulder shot"},
		{"俯拍", "high-angle shot"},
		{"仰拍", "low-angle shot"},
		{"缓推", "push in with small amplitude at slow speed"},
		{"急推", "push in with large amplitude at fast speed"},
		{"缓拉", "pull back with small amplitude at slow speed"},
		{"急拉", "pull back with large amplitude at fast speed"},
		{"横移", "lateral truck with medium amplitude"},
		{"跟移", "tracking shot following the subject"},
		{"甩镜", "whip pan"},
		{"环绕", "arc move around the subject"},
		{"环摇", "arc move around the subject"},
		{"慢升", "slow crane rise"},
		{"快升", "fast crane rise"},
		{"缓摇", "slow pan"},
		{"推", "push in"},
		{"拉", "pull back"},
		{"摇", "pan"},
		{"移", "lateral truck"},
		{"升", "crane rise"},
		{"降", "crane drop"},
	}
	for _, t := range table {
		if strings.Contains(cm, t.zh) {
			return t.en
		}
	}
	return ""
}

// injectCameraDiscipline 运镜必达纪律注入(2026-08-30 五问整改,问题④):队尾追加
// CAMERA DISCIPLINE(幂等锚),机械保证运镜列三要素进入渲染输入。
func injectCameraDiscipline(hp, camera string) string {
	if strings.Contains(hp, "CAMERA DISCIPLINE") {
		return hp
	}
	ph := manjuCameraPhrase(camera)
	if ph == "" {
		return hp
	}
	guard := "CAMERA DISCIPLINE: this shot's camera performs " + ph
	if strings.Contains(ph, "static") {
		guard += "; the camera stays locked but on-screen character action or environmental motion (light flicker, moving fabric and hair, drifting particles) must keep every second of the frame alive"
	} else {
		guard += "; keep that camera movement visible from the first frame to the last frame - never settle into a static locked-off frame"
	}
	return strings.TrimRight(hp, " \n") + "\n" + guard
}

// injectPositionDiscipline 站位纪律注入(2026-08-30 五问整改,问题③):队尾追加
// POSITION DISCIPLINE(幂等锚)——存量脚本 detailed_description 站位稀疏且
// 「脚本即权威」逐字保留无人补,纪律句强制每个登场角色带屏幕位置+朝向。
func injectPositionDiscipline(hp string) string {
	if strings.Contains(hp, "POSITION DISCIPLINE") {
		return hp
	}
	guard := "POSITION DISCIPLINE: every on-screen character must appear at a specific screen position - left/center/right third of the frame combined with foreground/midground/background depth - with a clear facing direction (facing camera, facing left, facing right, or turned away); no character may float without a position, and once the relative arrangement of characters is set it must not flip within the shot"
	return strings.TrimRight(hp, " \n") + "\n" + guard
}

// reAudioDefPhrase 规范化后的 Audio 定义行(alignAudioDefs 输出格式,稳定可匹配;
// 注入音色短语后再跑不命中=幂等)
var reAudioDefPhrase = regexp.MustCompile(`(?m)^<Audio (\d+)> is the voice-timbre reference for <Subject (\d+)> \(S(\d+)\), containing a spoken voiceover\.?\r?$`)

// injectAudioTimbrePhrases Audio 定义行注入角色音色指纹短语(2026-08-30 ver14,问题②
// 音色年龄/一致性根治的执行层一环):H3 官方 base-en §4.4 要求说话者首次出现给足
// 身份信息(年龄/性别/音高/音色/语速),模型按身份短语分配声线——此前 <Audio N> 行
// 只写 "containing a spoken voiceover" 无任何年龄/音色信息,角色卡 age 字段(24/74 岁)
// 从不进入渲染提示词。短语由 autoVoiceFor 音色档位编译(单一事实源)。
func (ctx *manjuCtx) injectAudioTimbrePhrases(hp string, c manjuRefContract) string {
	if !strings.Contains(hp, "containing a spoken voiceover") || len(c.Chars) == 0 {
		return hp
	}
	return reAudioDefPhrase.ReplaceAllStringFunc(hp, func(m string) string {
		sm := reAudioDefPhrase.FindStringSubmatch(m)
		no := mustAtoi(sm[2])
		if no < 1 || no > len(c.Chars) {
			return m
		}
		phr := ctx.voiceTimbrePhrase(c.Chars[no-1].ID)
		if phr == "" {
			return m
		}
		return fmt.Sprintf("<Audio %s> is the voice-timbre reference for <Subject %s> (S%s), with %s, containing a spoken voiceover.",
			sm[1], sm[2], sm[3], phr)
	})
}

// manjuVoicePhraseFor 音色库 key → H3 官方身份短语(官方示例 "the middle-aged baker
// with a calm, slightly raspy voice (S1)"):年龄/性别/音高/音色/语速一体,模型按此
// 分配声线。key 与 autoVoiceFor 输出同源,单一事实源。
func manjuVoicePhraseFor(key string) string {
	phrases := map[string]string{
		"beast_cute":    "a cute, playful creature voice, bright and bubbly",
		"child_boy":     "a little boy's voice, high and clear with childlike energy",
		"child_girl":    "a little girl's voice, high and bright with childlike energy",
		"boy_teen":      "a teenage boy's voice, bright and youthful",
		"girl_lively":   "a young girl's voice, lively and clear",
		"male_sun":      "a young man's voice, clear and steady",
		"female_warm":   "a young woman's voice, warm and gentle",
		"male_mag":      "a middle-aged man's voice, calm and deep",
		"female_mature": "a mature woman's voice, smooth and composed",
		"male_elder":    "an old man's voice, low and weathered",
		"female_elder":  "an elderly woman's voice, warm and crackly",
		"male_deep":     "a man's voice, low and magnetic",
		"female_deep":   "a woman's voice, cold and sharp",
		// 方言/区域(2026-08-30 ver15):口音描述注入 Audio 定义行——H3 按描述
		// 带口音生成(edge-tts 无四川/河南/广西/湖南方言声源,参考音频只锁音色基底)
		"cn_dongbei":  "a woman speaking Mandarin with a cheerful Northeastern accent",
		"cn_shaanxi":  "a woman speaking Mandarin with a bright Shaanxi accent",
		"cn_sichuan":  "a man speaking Mandarin with a lively Sichuan accent",
		"cn_henan":    "a man speaking Mandarin with an earthy Henan accent",
		"cn_guangxi":  "a man speaking Mandarin with a soft Guangxi accent",
		"cn_hunan":    "a man speaking Mandarin with a spirited Hunan accent",
		"hk_male":     "a man speaking Cantonese with a Hong Kong accent",
		"hk_female":   "a woman speaking Cantonese with a Hong Kong accent",
		"tw_male":     "a man speaking Mandarin with a gentle Taiwanese accent",
		"tw_female":   "a woman speaking Mandarin with a gentle Taiwanese accent",
		"male_narrator":   "a calm neutral storytelling voice",
		"female_narrator": "a calm neutral storytelling voice",
	}
	return phrases[key]
}

// voiceTimbrePhrase 角色音色指纹短语(2026-08-30 ver14):按 autoVoiceFor 档位编译,
// 注入 <Audio N> 定义行——H3 的年龄感/音色来自短语而非随机。
func (ctx *manjuCtx) voiceTimbrePhrase(cid string) string {
	key := ctx.autoVoiceFor(cid)
	if key == "" {
		return ""
	}
	return manjuVoicePhraseFor(key)
}

// buildSpeakerRegistry 全局说话人注册表(2026-08-30 ver14,H3 官方契约
// "A speaker keeps the same ID across shots"):按全集首次发声顺序为每个有音色绑定
// (VoiceRoster)的角色分配全局 (Sx),跨镜稳定——修复此前 alignSpeakerIDs 逐镜重排
// (S1..Sk)导致的同角色声线逐镜漂移。确定性来源=dialogue 说话人序(与 alignAudioDefs
// 同源);无音色绑定角色不注册,逐镜镜内序兜底(无权威绑定可依)。
func (ctx *manjuCtx) buildSpeakerRegistry(shots []manjuShot) map[string]string {
	reg := map[string]string{}
	order := 0
	for _, s := range shots {
		c := ctx.refContractFor(s)
		roster := map[string]bool{}
		for _, cid := range c.VoiceRoster {
			roster[cid] = true
		}
		names := map[string]bool{}
		for cid := range dialogueSpeakerIDs(s.Dialogue, c) {
			names[cid] = true
		}
		// 2026-08-30 五问整改(问题②兜底):内心戏「内心·角色名」说话人也注册全局
		// Sx——内心说话者此前不在注册表,画外内心句的 (Sx) 跨镜漂移(旁白不注册,
		// 独立叙述音色,镜内序即可)
		for _, line := range strings.Split(s.Narration, "\n") {
			if i := strings.Index(line, "内心·"); i >= 0 {
				rest := line[i+len("内心·"):]
				if j := strings.IndexAny(rest, "：:"); j > 0 {
					if cid := charIDMatch(rest[:j], c); cid != "" {
						names[cid] = true
					}
				}
			}
		}
		for cid := range names {
			if !roster[cid] {
				continue
			}
			if _, ok := reg[cid]; !ok {
				order++
				reg[cid] = "S" + strconv.Itoa(order)
			}
		}
	}
	return reg
}

// shotPicSlots 该镜实际提交给 H3 的参考图槽位数(= charRefNames 数 + 场景图 0/1,
// 不含 FL2VA 尾帧):与 charRefNames/sceneRefName 的提交顺序同源,指纹与渲染共用。
func (ctx *manjuCtx) shotPicSlots(s manjuShot) int {
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	slots := 0
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		slots += len(ctx.charViewRels(cid, i, n))
	}
	if s.Scene != "" && fileExists(filepath.Join(ctx.assetsDir, "scenes", s.Scene+".png")) {
		slots++
	}
	return slots
}

// manjuExpectPicSlots 计划期预期参考图槽位(纯函数,不探测文件)。ensurePlanAndPrompts
// 汇点在 assets 阶段之前 finalize,shotPicSlots 探测文件恒 0——若按 0 剥 <Picture N>,
// 引用被剥后回写固化,渲染时人物参考图失效(2026-08-29 绿萝 EP01 25 镜实锤)。渲染管线
// 保证 characters 非空角色的定妆照必然生成,预期槽位=charViewRels 的 picks 规则上限;
// 场景槽=s.Scene 非空(场景图必生成)。预期≥实测恒成立(front 缺失回退主图必有,full/
// detail/场景图缺失只会让实测更小),plan 汇点永不引入「再剥」震荡;实测与预期不一致
// (资产生成失败)时 renderShotTo 用实测重 finalize,指纹自然变化触发重渲,不产错片。
func manjuExpectPicSlots(s manjuShot) int {
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	slots := 0
	for i := 0; i < n; i++ {
		switch {
		case n <= 1:
			slots += 4 // front/full/detail/side(2026-09-01 加 side 视图)
		case n == 2:
			slots += 3 // front/full/side
		default: // 3 角色:主角 front+full+side,其余 front+side
			if i == 0 {
				slots += 3
			} else {
				slots += 2
			}
		}
	}
	if s.Scene != "" {
		slots++
	}
	return slots
}

// rePicRef <Picture N> 引用(含可选的 in/and/as 前缀连接词由上下文处理,这里只抓标签)
var rePicRef = regexp.MustCompile(`<Picture\s*(\d+)\s*>`)

// manjuStripDanglingPictureRefs 剥除错位的 <Picture N> 引用(2026-08-28 EP01 人物不一致
// 根治层):h3 的 Picture 编号是脚本作者按叙述假设的,渲染端实际提交顺序=人物视图图+
// 场景图——两张清单不保证一致,错位引用会让 H3 拿错图当参考:
//   ①超界引用(N>picSlots):道具/多视图句错位指到别人的图 → 剥除标签(句子保留纯文字描述);
//   ②无人物图镜(hasChars=false)的人物主体句:唯一槽位是场景图,人物句引用它=拿场景图
//     当人脸(EP01 镜3 <Picture 1> is Chen Mo 错位)→ 人物句的 Picture 引用全剥。
func manjuStripDanglingPictureRefs(hp string, hasChars bool, picSlots int) string {
	if picSlots < 0 {
		picSlots = 0
	}
	if !strings.Contains(hp, "<Picture") {
		return hp
	}
	// 场景环境词:主体句含这些词=环境/场景句(可引用场景槽),否则视为人物/道具句
	envWords := []string{"environment", "scene", "office", "room", "floor", "landscape",
		"sky", "street", "background", "corridor", "hallway", "building", "city", "interior"}
	isEnvLine := func(line string) bool {
		low := strings.ToLower(line)
		for _, w := range envWords {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	for _, line := range strings.Split(hp, "\n") {
		if !strings.Contains(line, "<Picture") {
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		stripWhole := !hasChars && !isEnvLine(line) && strings.Contains(line, "<Subject")
		newLine := line
		for _, m := range rePicRef.FindAllStringSubmatch(line, -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if n > picSlots || stripWhole {
				newLine = strings.Replace(newLine, m[0], "", 1)
			}
		}
		if newLine != line {
			// 剥标签后残留的连接词清理(" in " 悬空 / 双空格),保持英文句子通顺
			newLine = regexp.MustCompile(`\s+(in|and|as)\s+(\s*,|[,;])`).ReplaceAllString(newLine, ",")
			newLine = regexp.MustCompile(`\bin\s{2,}`).ReplaceAllString(newLine, " ")
			newLine = regexp.MustCompile(`\s{2,}`).ReplaceAllString(newLine, " ")
			newLine = regexp.MustCompile(`\s+([,.;])`).ReplaceAllString(newLine, "$1")
		}
		b.WriteString(newLine)
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(out) != len(hp) {
		// 保留原文结尾换行语义(末尾空行损失无碍,内容完整即可)
		return out
	}
	return hp
}

// draftDims 草稿预审分辨率:定稿画幅 × draftScale 对齐 32(判分与分辨率弱相关,
// 0.5 缩放的像素量约为定稿 1/4,审片返工轮 GPU 时间等比下降)
func (ctx *manjuCtx) draftDims() (int, int) {
	sc := ctx.draftScale
	if sc <= 0 || sc >= 1 {
		sc = 0.5
	}
	return manjuAlign32(int(float64(ctx.w)*sc + 0.5)), manjuAlign32(int(float64(ctx.h)*sc + 0.5))
}

// draftDir 草稿预审产物目录(clips/<ep>/_draft,与定稿同集隔离;合成/集清单不读,
// 定稿轮完成后整目录清除)
func (ctx *manjuCtx) draftDir() string {
	return filepath.Join(ctx.clipsDir, ctx.episode, "_draft")
}

// ---- 运行日志(写入内存状态 + run.log,检测阶段切换通知) ----

type manjuLogger struct {
	state     *manjuTask
	file      *os.File
	mu        sync.Mutex
	lastStage string
	proj, ep  string
}

func newManjuLogger(state *manjuTask, file *os.File, proj, ep string) *manjuLogger {
	return &manjuLogger{state: state, file: file, proj: proj, ep: ep}
}

func (l *manjuLogger) logf(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.state
	// 每行带时间戳(前端时间轴展示 + run.log 排障;阶段/镜头进度正则均为子串匹配,不受前缀影响)
	line = "[" + time.Now().Format("15:04:05") + "] " + line
	state.mu.Lock()
	state.log += "\n" + line
	if len(state.log) > 300000 {
		state.log = state.log[len(state.log)-200000:]
	}
	state.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line + "\n")
	}
	if m := reManjuStage.FindStringSubmatch(line); m != nil {
		st := m[1]
		if st != l.lastStage {
			l.lastStage = st
			name := manjuStageName[st]
			if name == "" {
				name = st
			}
			manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · 进入阶段: %s", l.proj, l.ep, name))
		}
	}
}

func (l *manjuLogger) logStage(key string) {
	// 阶段横幅带集号(2026-08-26 用户要求:运行日志分清楚集数——多集/全本自动分集时
	// 逐集跑管线,不带集号则阶段混在一起无法分辨当前属于哪集)。reManjuStage 前缀匹配
	// 阶段名仍有效(解析到空格即停),前端 renderLog 分组/横幅正则已同步兼容 ` · 集号`。
	label := "阶段 " + key
	if l.ep != "" {
		label += " · " + l.ep
	}
	l.logf("━━━ " + label + " ━━━")
}

func (l *manjuLogger) stopped() bool {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.stopped
}

// ---- 运行主循环(替代原 manjuWorker 的 Python 子进程) ----

// syncLixiRenderPlan 立项.render 变更同步(2026-08-26):立项.json 此前只在建项目时合并一次,
// 创作期重新规划(改风格/字速/时长区间等)不会同步进已有项目。方案:启动管线前对比立项.json
// 指纹(大小+mtime)与 config.render._lixi_fp,变化则重新合并——applyNovelRenderPlan 幂等且
// 「config 非零显式值优先」,用户手改的配置不会被覆盖。返回提示消息(空=无需同步)。
func syncLixiRenderPlan(configPath string) string {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return ""
	}
	P, _ := cfg["paths"].(map[string]any)
	nv := strings.TrimSpace(str(P["novel"]))
	if nv == "" {
		return ""
	}
	novelDir := nv
	if st, serr := os.Stat(nv); serr == nil && !st.IsDir() {
		novelDir = filepath.Dir(nv)
	}
	st, err := os.Stat(filepath.Join(novelDir, "立项.json"))
	if err != nil {
		return "" // 无立项文件(非爽文技能项目)静默跳过
	}
	fp := fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano())
	R, _ := cfg["render"].(map[string]any)
	if R != nil && str(R["_lixi_fp"]) == fp {
		return ""
	}
	applyNovelRenderPlan(cfg, novelDir)
	if R, _ = cfg["render"].(map[string]any); R != nil {
		R["_lixi_fp"] = fp
	}
	if werr := writeManjuConfig(configPath, cfg); werr != nil {
		return ""
	}
	return "📎 检测到 立项.json 更新,渲染规划已重新同步(显式配置不被覆盖)"
}

// manjuPipelineRun 执行一个或多个阶段,返回退出码(0 成功)
func manjuPipelineRun(ctx *manjuCtx, phase string, lg *manjuLogger) int {
	stages := []string{"plan", "assets", "encode", "render", "qc", "assemble"}
	if phase != "all" {
		stages = []string{phase}
	}
	if ctx.loraRepair != "" {
		lg.logf(ctx.loraRepair)
	}
	for _, st := range stages {
		if lg.stopped() {
			lg.logf("⏹ 任务已被手动停止。已完成产物保留,可直接再点同按钮续跑。")
			return 0
		}
		manjuSetStage(st) // 实时阶段推进:运行状态/气泡显示当前步骤(此前启动后恒为 all)
		lg.logStage(st)
		var err error
		switch st {
		case "plan":
			err = stagePlan(ctx, lg)
		case "assets":
			err = stageAssets(ctx, lg)
		case "encode":
			err = stageEncode(ctx, lg)
		case "render":
			err = stageRender(ctx, lg)
		case "qc":
			err = stageQC(ctx, lg)
		case "assemble":
			// 旁白/画外音后期配音(2026-08-23:H3 本地对画面外音/旁白不生成音轨,edge-tts 兜底):
			// render.voiceover=true 且 plan 存在时,合成前先给有 narration/画外音的镜补 TTS 配音
			if ok, _ := ctx.R["voiceover"].(bool); ok {
				if voErr := ctx.runVoiceover(lg); voErr != nil {
					lg.logf("⚠️ 旁白/画外音配音跳过: " + voErr.Error())
				}
			}
			err = stageAssemble(ctx, lg)
			if err == nil {
				// 成片终检(与 agent 模式一致,报告性质不阻断):时长/黑屏/静音/音轨兜底
				agentAssembleCheck(ctx, lg)
			}
		}
		if err != nil {
			lg.logf("❌ 阶段 " + st + " 失败: " + err.Error())
			return 1
		}
	}
	// 2026-08-26 升级:渲染完成空闲 N 分钟自动释放 ComfyUI 模型显存(render.idle_free_minutes,0=关)
	ctx.scheduleIdleFree(lg)
	return 0
}

// scheduleIdleFree 渲染完成后空闲 N 分钟且队列无任务时,自动 POST /free 释放模型显存。
// 默认 10 分钟(留足人工查看/续跑时间),render.idle_free_minutes=0 关闭。
func (ctx *manjuCtx) scheduleIdleFree(lg *manjuLogger) {
	mins := 10
	if v, ok := manjuToFloat(ctx.R["idle_free_minutes"]); ok && v >= 0 {
		mins = int(v)
	}
	if mins <= 0 {
		return
	}
	lg.logf(fmt.Sprintf("  ⏰ 渲染完成,空闲 %d 分钟后自动释放 ComfyUI 显存(/free)", mins))
	go func() {
		time.Sleep(time.Duration(mins) * time.Minute)
		busy, err := ctx.comfy.queueBusy()
		if err != nil {
			return
		}
		if !busy {
			ctx.freeComfyModels(nil)
			lg.logf("  🧹 空闲自动释放 ComfyUI 模型显存完成")
		}
	}()
}

// manjuSetStage 推进实时阶段(内存状态;前端运行状态/气泡据此显示当前步骤)
func manjuSetStage(st string) {
	manjuState.mu.Lock()
	manjuState.stage = st
	manjuState.mu.Unlock()
}

// manjuFinish 收尾:更新内存/磁盘状态(项目目录 run_state.json) + 结束通知
func manjuFinish(rc int) {
	state := manjuState
	state.mu.Lock()
	if state.stopped {
		state.log += "\n\n[" + time.Now().Format("15:04:05") + "] ⏹ 任务已被手动停止。已完成镜头保留,可直接再点同按钮续跑。"
		rc = 0
	}
	state.rc = &rc
	state.done = true
	state.running = false
	state.cmd = nil
	proj, ep := state.project, state.episode
	// 任务结束自动写诊断快照到固定目录(本地服务无需导出 zip,反馈时直接提供该文件)
	manjuWriteDiagnoseSnapshot(proj, ep)
	logTail := state.log
	if len(logTail) > 30000 {
		logTail = logTail[len(logTail)-30000:]
	}
	elapsedSec := state.baseElapsed + int(time.Since(state.started).Seconds())
	state.elapsed = elapsedSec // 冻结总耗时(含续跑累加基数),供 status 接口在结束后展示
	info := parseManjuProgress(state.log, false)
	stopped := state.stopped
	state.mu.Unlock()
	writeManjuDiskState(proj, &manjuDiskState{
		Running: false, Stage: "", Done: true, RC: &rc, Stopped: stopped, StartedAt: 0, PID: 0,
		Episode: ep, CurrentStage: info.CurrentStage, StageIdx: info.StageIdx,
		ShotCur: info.ShotCur, ShotTotal: info.ShotTotal,
		ElapsedSec: elapsedSec, LogTail: logTail, UpdatedAt: time.Now().Unix(),
	})
	if stopped {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ⏹ 已手动停止", proj, ep))
	} else if rc == 0 {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ✅ 全部完成", proj, ep))
	} else {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ❌ 失败(退出码 %d)", proj, ep, rc))
	}
}

// ---- 章节 ----

// parseChapterSet 解析 "1-3" / "1,2,3" / "1-999"(全本=全部)
func parseChapterSet(s string, all []int) map[int]bool {
	out := map[int]bool{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			a, _ := strconv.Atoi(strings.TrimSpace(part[:i]))
			b, _ := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if a == 0 {
				a = 1
			}
			if b > 900 { // 全本
				for _, n := range all {
					out[n] = true
				}
				continue
			}
			if b < a {
				a, b = b, a
			}
			for n := a; n <= b; n++ {
				out[n] = true
			}
		} else if n, err := strconv.Atoi(part); err == nil {
			out[n] = true
		}
	}
	return out
}

// extractChapters 从小说文本抽取指定章节内容(^# 第N章 标题切分,与旧管线同款)
func extractChapters(novelText string, nums map[int]bool) []string {
	matches := reManjuChapter.FindAllStringSubmatchIndex(novelText, -1)
	var out []string
	for i, m := range matches {
		n, _ := strconv.Atoi(novelText[m[2]:m[3]])
		if !nums[n] {
			continue
		}
		end := len(novelText)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		out = append(out, novelText[m[0]:end])
	}
	return out
}

func allChapterNums(novelText string) []int {
	ms := reManjuChapter.FindAllStringSubmatch(novelText, -1)
	out := []int{}
	for _, m := range ms {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (ctx *manjuCtx) chapterText() (string, error) {
	if ctx.novel == "" {
		return "", fmt.Errorf("未配置输入源: 请在「视频脚本直出」卡片粘贴脚本,或在渲染配置中选择小说文件")
	}
	data, err := os.ReadFile(ctx.novel)
	if err != nil {
		return "", fmt.Errorf("读小说失败: %w", err)
	}
	text := string(data)
	all := allChapterNums(text)
	if len(all) == 0 {
		// 无 # 第N章 结构(如 duanju 短剧全案):整篇作为单一素材直出方案
		return text, nil
	}
	nums := parseChapterSet(ctx.chapters, all)
	if len(nums) == 0 {
		return "", fmt.Errorf("章节范围无效: %s（该文件共 %d 章）", ctx.chapters, len(all))
	}
	chs := extractChapters(text, nums)
	if len(chs) == 0 {
		return "", fmt.Errorf("章节不存在: %s（该文件共 %d 章）", ctx.chapters, len(all))
	}
	return strings.Join(chs, "\n\n"), nil
}

// ---- 全本自动分段分集 ----

// chapterRangeFullBook 章节范围是否带"全本"标记(任一端点 >900,与 parseChapterSet 同语义)
func chapterRangeFullBook(s string) bool {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "-"); i > 0 {
			if b, err := strconv.Atoi(strings.TrimSpace(part[i+1:])); err == nil && b > 900 {
				return true
			}
		}
	}
	return false
}

// manjuEpSeg 自动分集的一段:集号 + 该集章节范围
type manjuEpSeg struct {
	Episode  string
	Chapters string
}

// manjuEpCharBudget 每集方案字数预算:低于方案生成的 20000 字截断,留余量(≈2-3 章)
const manjuEpCharBudget = 18000

// reChapterFile 分章文件名:第001章_标题.md
var reChapterFile = regexp.MustCompile(`第\s*(\d+)\s*章`)

// chapterNumFromName 从分章文件名解析章节号
func chapterNumFromName(name string) (int, bool) {
	m := reChapterFile.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// findChapterDir 在小说目录找 正文 分章子目录(01_正文/正文 等)
func findChapterDir(novelDir string) string {
	if novelDir == "" {
		return ""
	}
	entries, err := os.ReadDir(novelDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), "正文") {
			return filepath.Join(novelDir, e.Name())
		}
	}
	return ""
}

// reVolDir 卷目录名:卷一_逐出家门 / 卷12_xxx(卷号取 卷 后到下划线前)
var reVolDir = regexp.MustCompile(`^卷([一二三四五六七八九十百\d]+)[_\s]`)

// cnNumToInt 中文数字转整数(支持 一~十/百 及组合:十二=12、二十五=25)
func cnNumToInt(s string) int {
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10, '百': 100}
	total, cur := 0, 0
	for _, r := range s {
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		} else if r == '百' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 100
			cur = 0
		} else if d, ok := digits[r]; ok {
			cur = d
		}
	}
	return total + cur
}

// novelRootDir 小说项目根目录:优先 config 的 novel_dir;缺省时小说文件位于 全本/正文 子目录则向上取一级。
// 2026-08-30 存量自愈:novel_dir 被历史链路写成布局子目录(…/全本,其下无 素材/)时上提书根
// ——否则素材卡/分镜脚本查找落空,「资产 0 角色/0 场景直接编码」复发。
func (ctx *manjuCtx) novelRootDir() string {
	if d := strings.TrimSpace(str(ctx.P["novel_dir"])); d != "" {
		base := strings.ToLower(filepath.Base(d))
		if (base == "全本" || strings.Contains(base, "正文") || base == "素材") &&
			!dirExists(filepath.Join(d, "素材")) {
			return filepath.Dir(d)
		}
		return d
	}
	d := filepath.Dir(ctx.novel)
	base := strings.ToLower(filepath.Base(d))
	if base == "全本" || strings.Contains(base, "正文") {
		return filepath.Dir(d)
	}
	return d
}

// autoStoryboardForEpisode 小说解析模式自动检测分镜脚本(2026-08-24 用户反馈"导入小说目录不会自行解析"):
// 在 novel_dir(小说根目录)的 素材/分镜脚本/ 里找当前集对应脚本(EP01→第001章,EP12→第012章),
// 找到返回脚本路径并切脚本直出(程序化解析零 LLM,避免逐镜提示词截断);未找到返回空串。
func (ctx *manjuCtx) autoStoryboardForEpisode(lg *manjuLogger) string {
	// 2026-08-26 修复:脚本模式下 ctx.novel=workdir/script/EPxx.md,novelRootDir() 会误指
	// workdir/script(其下无 素材/分镜脚本)→ 永远找不到 novel 源。先用 config paths 显式
	// 指向的小说目录兜底(novel_dir 优先,novel 为全本文件时上溯书根),再回退 novelRootDir()。
	root := ""
	if d := strings.TrimSpace(str(ctx.P["novel_dir"])); d != "" && dirExists(filepath.Join(d, "素材")) {
		root = d
	} else if nv := strings.TrimSpace(str(ctx.P["novel"])); nv != "" {
		if st, serr := os.Stat(nv); serr == nil && st.IsDir() {
			if dirExists(filepath.Join(nv, "素材")) {
				root = nv
			}
		} else if dirExists(filepath.Join(filepath.Dir(nv), "素材")) {
			d := filepath.Dir(nv)
			if strings.EqualFold(filepath.Base(d), "全本") {
				d = filepath.Dir(d)
			}
			if dirExists(filepath.Join(d, "素材")) {
				root = d
			}
		}
	}
	if root == "" {
		root = ctx.novelRootDir()
	}
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, "素材", "分镜脚本")
	if !dirExists(dir) {
		dir = filepath.Join(root, "素材") // 兼容:素材/ 下直接放分镜脚本
		if !dirExists(dir) {
			return ""
		}
	}
	ep := ctx.episode
	if ep == "" {
		ep = str(ctx.R["episode"])
	}
	n := 0
	if a, err := strconv.Atoi(strings.TrimPrefix(ep, "EP")); err == nil && a > 0 {
		n = a
	}
	if n == 0 {
		if a, err := strconv.Atoi(ep); err == nil && a > 0 {
			n = a
		}
	}
	if n == 0 {
		return ""
	}
	chap := fmt.Sprintf("%03d", n)
	// 2026-08-31 JSON 分镜脚本优先(.json 为新格式,md 兼容旧项目)
	matches, _ := filepath.Glob(filepath.Join(dir, "第"+chap+"章*.json"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "第"+chap+"章*.md"))
	}
	if len(matches) == 0 {
		// 2026-08-26 兜底:部分分镜脚本文件名无前导零(第1章_xxx.md)——补两位/一位数字匹配
		for _, w := range []int{2, 1} {
			if n < 10 || w == 2 {
				matches, _ = filepath.Glob(filepath.Join(dir, fmt.Sprintf("第%0*d章*.json", w, n)))
			}
			if len(matches) == 0 {
				matches, _ = filepath.Glob(filepath.Join(dir, fmt.Sprintf("第%0*d章*.md", w, n)))
			}
			if len(matches) > 0 {
				break
			}
		}
	}
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "EP"+fmt.Sprintf("%02d", n)+".json"))
	}
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "EP"+fmt.Sprintf("%02d", n)+".md"))
	}
	if len(matches) > 0 {
		return pickStoryboardMatch(matches)
	}
	return ""
}

// pickStoryboardMatch 同章号存在多个分镜脚本候选时择优:排除备份标记(_old/旧/bak/backup/副本),
// 余下取 mtime 最新。旧版取字典序首个,备份文件(第001章…_old.md)排在正片前时会把备份当正片渲染。
func pickStoryboardMatch(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	best, bestMt := "", int64(-1)
	for _, m := range matches {
		low := strings.ToLower(filepath.Base(m))
		if strings.Contains(low, "_old") || strings.Contains(low, "old.") ||
			strings.Contains(low, "bak") || strings.Contains(low, "backup") ||
			strings.Contains(low, "旧") || strings.Contains(low, "副本") {
			continue
		}
		mt := int64(0)
		if st, err := os.Stat(m); err == nil {
			mt = st.ModTime().Unix()
		}
		if mt > bestMt {
			best, bestMt = m, mt
		}
	}
	if best == "" {
		best = matches[0] // 全部是备份文件时兜底取首个
	}
	return best
}

// syncScriptFromNovel 把 novel 源分镜脚本同步进 workdir 工作副本(ensurePlan 入口调用)。
// 脚本模式渲染实际读 workdir/script/EPxx.md 副本:源文件更新而副本不换,指纹/方案/镜头全按旧
// 剧情复用——这正是「分镜改了、视频还是旧剧情」的主链路。内容一致时零开销;不同则覆盖副本,
// 由副本 mtime 变化触发既有指纹失配 → 「脚本已更换」→ 清产物重生成。
func (ctx *manjuCtx) syncScriptFromNovel(lg *manjuLogger) {
	if !ctx.scriptMode || ctx.novel == "" {
		return
	}
	sp := ctx.autoStoryboardForEpisode(lg)
	if sp == "" || strings.EqualFold(sp, ctx.novel) {
		return
	}
	src, err := readTextFileUTF8(sp)
	if err != nil || len(src) == 0 {
		return
	}
	if dst, derr := readTextFileUTF8(ctx.novel); derr == nil && bytes.Equal(src, dst) {
		return
	}
	if werr := os.WriteFile(ctx.novel, src, 0o644); werr == nil {
		lg.logf("🔄 已同步小说源分镜(" + filepath.Base(sp) + ") → 工作副本;方案与旧镜头将按「脚本已更换」过期重生成")
	}
}

// volumeEpisodes 按卷分集:正文/<卷X_标题>/ 卷目录下每卷一个集(卷内章节文件定章节范围,卷序定集号)。
// 返回 (集列表, 是否检测到卷结构);未检测到卷结构时回退字数打包。
func (ctx *manjuCtx) volumeEpisodes() ([]manjuEpSeg, bool) {
	dir := findChapterDir(ctx.novelRootDir())
	if dir == "" {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	type vol struct {
		no  int
		chs []int
	}
	var vols []vol
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := reVolDir.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		no := cnNumToInt(m[1])
		if no <= 0 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				no = n
			}
		}
		if no <= 0 {
			continue
		}
		var chs []int
		files, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
				continue
			}
			if n, ok := chapterNumFromName(f.Name()); ok {
				chs = append(chs, n)
			}
		}
		if len(chs) == 0 {
			continue
		}
		sort.Ints(chs)
		vols = append(vols, vol{no: no, chs: chs})
	}
	if len(vols) == 0 {
		return nil, false
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].no < vols[j].no })
	out := make([]manjuEpSeg, 0, len(vols))
	for i, v := range vols {
		a, b := v.chs[0], v.chs[len(v.chs)-1]
		r := strconv.Itoa(a)
		if b != a {
			r += "-" + strconv.Itoa(b)
		}
		out = append(out, manjuEpSeg{Episode: fmt.Sprintf("EP%02d", i+1), Chapters: r})
	}
	return out, true
}

// chapterEntries 收集小说全部章节(章节号+字数)。优先全本文本按 # 第N章 切分
// (与方案生成同源,字数统计最准);全本无章节结构时回退 01_正文 分章文件。
func (ctx *manjuCtx) chapterEntries() ([]struct{ n, chars int }, error) {
	// 2026-08-24 用户反馈:导入全本分镜脚本只渲染一集——脚本直出模式的"章节结构"是 script/ 目录下的
	// EPxx.md 文件数(import-all 导入几章就是几集),不是小说正文(脚本模式 ctx.novel 是单集脚本文件)。
	// 这里优先按 script/ 目录的 EPxx.md 数量生成分集依据,EP01→第1集…EP12→第12集。
	if ctx.scriptMode && ctx.workdir != "" {
		scriptDir := filepath.Join(ctx.workdir, "script")
		entries, err := os.ReadDir(scriptDir)
		if err == nil {
			var out []struct{ n, chars int }
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					continue
				}
				// EP01.md → n=1; 兼容 第001章_xxx.md → n=1
				n, ok := episodeNumFromName(e.Name())
				if !ok {
					continue
				}
				if b, rerr := os.ReadFile(filepath.Join(scriptDir, e.Name())); rerr == nil {
					out = append(out, struct{ n, chars int }{n: n, chars: len([]rune(string(b)))})
				}
			}
			if len(out) >= 1 {
				sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
				return out, nil
			}
		}
	}
	out := []struct{ n, chars int }{}
	data, err := os.ReadFile(ctx.novel)
	if err != nil {
		return nil, err
	}
	text := string(data)
	ms := reManjuChapter.FindAllStringSubmatchIndex(text, -1)
	for i, m := range ms {
		n, _ := strconv.Atoi(text[m[2]:m[3]])
		end := len(text)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		out = append(out, struct{ n, chars int }{n: n, chars: len([]rune(text[m[0]:end]))})
	}
	if len(out) >= 2 {
		return out, nil
	}
	out = out[:0]
	if dir := findChapterDir(ctx.novelRootDir()); dir != "" {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					continue
				}
				n, ok := chapterNumFromName(e.Name())
				if !ok {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
					out = append(out, struct{ n, chars int }{n: n, chars: len([]rune(string(b)))})
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	}
	return out, nil
}

// episodeNumFromName 从脚本文件名解析集号:EP01.md→1, 第001章_xxx.md→1, 01.md→1
func episodeNumFromName(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	// EP01 / EP1
	if strings.HasPrefix(strings.ToUpper(base), "EP") {
		if n, err := strconv.Atoi(base[2:]); err == nil && n > 0 {
			return n, true
		}
	}
	// 第001章_xxx
	if i := strings.Index(base, "第"); i >= 0 {
		rest := base[i+1:]
		j := strings.Index(rest, "章")
		if j > 0 {
			if n, err := strconv.Atoi(rest[:j]); err == nil && n > 0 {
				return n, true
			}
		}
	}
	// 纯数字 01.md
	if n, err := strconv.Atoi(base); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// manjuChapterTotal 小说总章数(章节 0 默认值解析:从 config 读小说文件统计 # 第N章)。
// 2026-08-24 脚本直出模式:paths.novel 为空,改为统计 script/ 目录 EPxx.md 数量(=分集数)。
func manjuChapterTotal(configPath string) int {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return 0
	}
	P, _ := cfg["paths"].(map[string]any)
	novel := str(P["novel"])
	if novel == "" || !fileExists(novel) {
		// 脚本直出:统计 script/ 目录分镜脚本数
		if workdir := str(P["workdir"]); workdir != "" {
			entries, rerr := os.ReadDir(filepath.Join(workdir, "script"))
			if rerr == nil {
				n := 0
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
						n++
					}
				}
				if n > 0 {
					return n
				}
			}
		}
		return 0
	}
	data, err := os.ReadFile(novel)
	if err != nil {
		return 0
	}
	return len(allChapterNums(string(data)))
}

// manjuChapterEpisodes 集数=0 的自动模式:按章节数计算——每章一集(第 N 章 = 第 N 集)。
// chapters 范围:章节 0 已解析为 1-总章数(全部);具体范围(如 1-10)则只生成范围内每章一集。
// 与 manjuAutoEpisodes(按卷/字数打包)并存:集数 0 优先每章一集,保底回退自动打包。
func manjuChapterEpisodes(ctx *manjuCtx, chapters string) []manjuEpSeg {
	chs, err := ctx.chapterEntries()
	if err != nil || len(chs) == 0 {
		return nil
	}
	// 范围过滤:解析 a-b(非全本)时只保留章节号在 [a,b] 的条目
	if lo, hi, ok := parseChapterRange(chapters); ok {
		var filtered []struct{ n, chars int }
		for _, c := range chs {
			if c.n >= lo && c.n <= hi {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) > 0 {
			chs = filtered
		}
	}
	out := make([]manjuEpSeg, 0, len(chs))
	for i, c := range chs {
		out = append(out, manjuEpSeg{Episode: fmt.Sprintf("EP%02d", i+1), Chapters: fmt.Sprintf("%d-%d", c.n, c.n)})
	}
	return out
}

// parseChapterRange 解析 "a-b" 章节范围(返回 lo,hi,ok);"1-999"/全本或空返回 !ok
func parseChapterRange(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	i := strings.Index(s, "-")
	if i <= 0 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
	b, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil || a <= 0 || b < a {
		return 0, 0, false
	}
	if b > 900 { // 全本:不过滤
		return 0, 0, false
	}
	return a, b, true
}

// manjuAutoEpisodes 全本时自动分段分集:
// 优先按卷划分(正文/<卷X_标题>/ 每卷一集,如吞天废子 8 卷 → EP01..EP08);
// 无卷结构时按内容量贪心打包(章节顺序,每集累计字数 ≤ 预算,至少 1 章)。
// 两种规则都是确定性分段,同一小说每次结果一致,可安全续跑。
func manjuAutoEpisodes(ctx *manjuCtx, chapters string) ([]manjuEpSeg, error) {
	if !chapterRangeFullBook(chapters) {
		return nil, nil
	}
	if segs, ok := ctx.volumeEpisodes(); ok {
		return segs, nil
	}
	chs, err := ctx.chapterEntries()
	if err != nil {
		return nil, err
	}
	if len(chs) == 0 {
		return nil, nil // 无章节结构:整篇一集
	}
	segFrom := func(idx int, seg []struct{ n, chars int }) manjuEpSeg {
		a, b := seg[0].n, seg[len(seg)-1].n
		r := strconv.Itoa(a)
		if b != a {
			r += "-" + strconv.Itoa(b)
		}
		return manjuEpSeg{Episode: fmt.Sprintf("EP%02d", idx+1), Chapters: r}
	}
	var out []manjuEpSeg
	curStart, curChars := 0, 0
	for i, c := range chs {
		if i > curStart && curChars+c.chars > manjuEpCharBudget {
			out = append(out, segFrom(len(out), chs[curStart:i]))
			curStart, curChars = i, 0
		}
		curChars += c.chars
	}
	if curStart < len(chs) {
		out = append(out, segFrom(len(out), chs[curStart:]))
	}
	return out, nil
}

// ---- 方案(LLM 直出人物/场景/分镜 + 逐镜 H3 提示词) ----

type manjuShot struct {
	ID         int
	Scene      string
	Characters []string
	ShotSize   string
	Camera     string
	Action     string
	Dialogue   string
	Narration  string
	Duration   int
	Style      string   // 镜级渲染风格(2026-08-23 多风格并用:缺省继承全局 style;按镜差异化如 real+magical/ink 回忆)
	Light      string   // 分镜表光影列(2026-08-30 ver14 保留,LLM 逐镜重写路径注入 detailed_description)
	Sound      string   // 分镜表音效列(同上,注入 overall_soundscape)
	H3Prompt   string
	TakeTail   bool        // 多切点长镜的内镜:不独立渲染,由组头一次生成覆盖
	TakeGroup  []manjuShot // 多切点长镜组头携带整组(含自身;单镜为空)
}

// manjuSanitizePlanIDs 规范化方案内角色/场景 id,并同步改写镜头引用(shot.scene 与
// shot.characters)。id 直接用作落盘文件名(characters/<id>.png、scenes/<id>.png),
// LLM 直出或素材卡带来的 id 可能含 Windows 非法字符(如 王鹏飞"王胖" 的英文双引号),
// 落盘时直接报「文件名语法不正确」。在 loadPlan 读盘后与 writePlan 落盘前统一清洗,
// 存量方案与新方案走同一套名字;幂等可重复调用,清洗后撞名的 id 追加下划线保唯一。
// 2026-08-28 伪场景过滤:素材全局段(通用负向词/统一风格前缀/质量后缀等)被 LLM 当
// 场景输出(实测 scenes[0]=通用负向词,白烧一张 GPU)——黑名单同 reManjuGlobalSection
// 词源;裸「通用」不进黑名单(防误伤"通用仓库"类真场景名)。
func manjuSanitizePlanIDs(plan map[string]any) {
	if plan == nil {
		return
	}
	renames := map[string]string{}
	used := map[string]bool{}
	assign := func(raw string) string {
		if safe, ok := renames[raw]; ok {
			return safe
		}
		safe := sanitizeFileName(raw)
		if safe == "" {
			safe = "_"
		}
		for used[safe] {
			safe += "_"
		}
		used[safe] = true
		renames[raw] = safe
		return safe
	}
	// 伪场景过滤:素材全局配置段不是场景(id 命中黑名单的整条删除,并清镜头引用)。
	// 2026-08-28 补「封面」且扫 description:EP01 实锤——场景卡「城市夜景大远景」id 干净,
	// desc 写「封面备用·开篇/终章」,封面备用卡混进正片场景池,经别名匹配+最高频兜底
	// 传染全片(办公室戏全挂城市大远景参考图)。封面/备用卡不属于任何正片镜头,整条删。
	dropScene := func(id string, desc string) bool { return manjuSceneDropped(id, desc) }
	var keptScenes []any
	dropped := map[string]bool{}
	for _, x := range anyArr(plan["scenes"]) {
		if m, ok := x.(map[string]any); ok {
			if id := str(m["id"]); id != "" {
				if dropScene(id, str(m["description"])) {
					dropped[id] = true
					continue
				}
				m["id"] = assign(id)
			}
		}
		keptScenes = append(keptScenes, x)
	}
	plan["scenes"] = keptScenes
	for _, key := range []string{"characters"} {
		for _, x := range anyArr(plan[key]) {
			if m, ok := x.(map[string]any); ok {
				if raw := str(m["id"]); raw != "" {
					m["id"] = assign(raw)
				}
			}
		}
	}
	for _, x := range anyArr(plan["shots"]) {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		if sid := str(m["scene"]); sid != "" {
			if dropped[sid] {
				// 引用被删伪场景/封面卡的镜头:置空(渲染端空 scene 不挂场景图,h3 文本
				// 自述场景),绝不残留指向已删卡的引用继续挂错图
				m["scene"] = ""
			} else if s2, moved := renames[sid]; moved {
				m["scene"] = s2
			}
		}
		if carr, ok2 := m["characters"].([]any); ok2 {
			for i, c := range carr {
				if cs, ok3 := c.(string); ok3 {
					if c2, moved := renames[cs]; moved {
						carr[i] = c2
					}
				}
			}
		}
	}
}

// loadPlan 读取 analysis/<ep>_direct_plan.json,规范化镜头字段
func (ctx *manjuCtx) loadPlan() (map[string]any, []manjuShot, error) {
	p := manjuFindPlanDir(ctx.analysisDir, ctx.episode)
	if p == "" {
		return nil, nil, fmt.Errorf("方案不存在: %s_direct_plan.json", ctx.episode)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	var plan map[string]any
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, nil, fmt.Errorf("方案解析失败: %w", err)
	}
	manjuSanitizePlanIDs(plan)
	shots, err := planShots(plan)
	if err != nil {
		return nil, nil, err
	}
	return plan, shots, nil
}

func planShots(plan map[string]any) ([]manjuShot, error) {
	var out []manjuShot
	arr, _ := plan["shots"].([]any)
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		s := manjuShot{
			Scene:     str(m["scene"]),
			ShotSize:  str(m["shot_size"]),
			Camera:    str(m["camera"]),
			Action:    str(m["action"]),
			Dialogue:  str(m["dialogue"]),
			Narration: str(m["narration"]),
			Style:     str(m["style"]),
			Light:     str(m["light"]), // 2026-08-30 ver14:分镜表光影/音效列保留(LLM 重写路径注入)
			Sound:     str(m["sound"]),
			H3Prompt:  str(m["h3_prompt"]),
		}
		s.ID, _ = manjuToInt(m["shot_id"])
		s.Duration = 5
		if n, ok := manjuToInt(m["duration"]); ok && n > 0 {
			s.Duration = n
		}
		if arr2, ok := m["characters"].([]any); ok {
			for _, c := range arr2 {
				if cs := str(c); cs != "" {
					s.Characters = append(s.Characters, cs)
				}
			}
		}
		out = append(out, s)
	}
	// 2026-08-25 用户要求:分镜头必须按顺序渲染——方案 shots 数组若乱序(LLM 直出/修复回写偶发
	// 打乱 shot_id 顺序),渲染/接缝序号/进度/agent 返工会全部跟着乱;统一按 shot_id 升序固定顺序。
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// manjuConciseSuffix 方案输出超长被截断时的精简约束(仅重试时追加,不改变正常生成)
const manjuConciseSuffix = `

【输出体积硬约束(前次输出被截断,本次必须精简)】:
- characters 不超过 4 个、scenes 不超过 4 个、shots 不超过 16 个
- appearance/costume/image_prompt 每项不超过 40 字;views 的 front/full/side/detail 每项不超过 45 个英文词,scene 的 description 不超过 30 字
- 所有描述压缩到"可渲染"即可,禁止铺陈展开;整个 JSON 输出控制在 8000 tokens 以内`

// ensurePlan 保证方案存在(有则复用,无则 LLM 直出),同时写 _characters.json(抽卡用)
// 复用校验:方案记录了章节范围(plan.chapters)且与本次请求一致才复用;
// 旧方案(无分段标记)在普通模式复用(兼容不重渲),在全本自动分集模式视为过期重新生成;
// 章节范围变化导致重新生成时,清空该集旧镜头/缓存,避免按旧内容复用。
// 生成失败(输出被截断/非 JSON)时追加精简约束重试一次。
func (ctx *manjuCtx) ensurePlan(lg *manjuLogger) (map[string]any, error) {
	// 2026-08-26 修复(「渲染视频与小说不同步」主链路):脚本模式下 ctx.novel 指向 workdir
	// 副本,指纹只盯副本——直接改 novel 源分镜后副本不变,旧方案与旧镜头全部被复用,
	// 视频永远停留在旧剧情。生成/复用方案前先把 novel 源脚本同步进副本:内容不同则覆盖,
	// 副本 mtime 变化使指纹失配,走下方既有「脚本已更换」链路清产物重生成。
	ctx.syncScriptFromNovel(lg)
	plan, _, err := ctx.loadPlan()
	if err == nil {
		planC := str(plan["chapters"])
		// 2026-08-24 实测修复:脚本直出方案(chapters="script")在自动恢复/续跑时被误判过期——
		// 恢复按小说分集重建上下文(ctx.chapters="1-1"),"script" != "1-1" → 判定「章节范围已变」
		// → 清空已完成镜头+缓存重新生成(脚本没变却重复烧 GPU,实测 EP01 6 镜被误清)。
		// 方案为脚本直出时,先尝试找回同集分镜脚本:找回且脚本指纹一致 → 切回脚本模式复用;
		// 找回但指纹已变 → 脚本真换了,走「脚本已更换」重新生成;找回不到 → 按原章节范围判定。
		if planC == "script" && !ctx.scriptMode && !ctx.agentMode {
			if sp := ctx.autoStoryboardForEpisode(lg); sp != "" {
				ctx.scriptMode = true
				ctx.novel = sp
				ctx.chapters = "script"
			}
		}
		legacy := planC == ""                               // 旧方案无分段标记
		reuse := planC == ctx.chapters                      // 范围一致
		reuse = reuse || (legacy && !ctx.auto)              // 旧方案普通模式兼容复用
		reuse = reuse || chapterRangeFullBook(ctx.chapters) // 全本请求:沿用该集既有方案
		// 小说内容指纹:改过正文必须重新生成方案,否则渲染的还是旧剧情
		// (「改了小说但视频对不上」的头号原因);旧方案无指纹记录则不强制,兼容老项目
		fp := ctx.novelFingerprint()
		planFp := str(plan["novel_fp"])
		if reuse && fp != "" && planFp != "" && planFp != fp {
			// 2026-08-24 措辞区分:脚本模式(全本分集每集脚本不同)是"脚本已更换"属正常;小说模式才是"正文已修改"
			if ctx.scriptMode {
				lg.logf("ℹ️ 脚本已更换(" + filepath.Base(ctx.novel) + "),该集方案重新生成")
			} else {
				lg.logf("⚠️ 小说正文已修改(" + planFp + " → " + fp + ")，方案过期，重新生成并清空该集旧产物")
			}
			reuse = false
			ctx.clearEpisodeArtifacts(lg)
		}
		if reuse {
			// 2026-08-26 修复(用户实测:16 分镜脚本只渲染 8 个,普通一条龙):旧版解析器失败时
			// 静默回退 LLM 直出,LLM 拆镜数不受脚本约束(旧版无拆镜密度强制,8 镜常见),plan
			// 落盘后 chapters="script"+脚本指纹一致 → 永久复用,升级解析器也不自愈。
			// plan 记解析器代数:版本落后 → 强制重新程序化解析替换(成功才替换,失败沿用旧方案);
			// 旧镜头产物与新方案提示词指纹不同,渲染阶段 manifest 判 stale 自动删旧重渲。
			if planC == "script" && ctx.scriptMode {
				if v, hasVer := manjuToInt(plan["script_parse_ver"]); !hasVer || v < manjuScriptParseVer {
					if p, perr := ctx.scriptParsePlan(lg); perr == nil {
						newShots, _ := planShots(p)
						oldN := len(anyArr(plan["shots"]))
						if len(newShots) != oldN {
							lg.logf(fmt.Sprintf("🔄 脚本解析器已升级:重新解析替换旧方案(旧 %d 镜 → 脚本 %d 镜;镜数不一致=旧版 LLM 兜底/解析缺镜的痕迹,已按脚本纠正)", oldN, len(newShots)))
						} else {
							lg.logf("🔄 脚本解析器已升级:重新解析刷新方案(六段式逐字保留)")
						}
						plan = p
						if werr := ctx.writePlan(plan); werr != nil {
							return nil, werr
						}
					} else {
						lg.logf("  ⚠️ 解析器升级后重新解析失败(" + perr.Error() + "),沿用现有方案")
					}
				}
			}
			ctx.writeCharactersJSON(plan)
			lg.logf("♻️  复用方案: " + filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"))
			ctx.recordPlanFingerprint(plan, lg) // 反同质化:复用也回填指纹(老方案无 directing 则记空五维)
			return plan, nil
		}
		if legacy {
			lg.logf("⚠️ 集 " + ctx.episode + " 旧方案无分集标记,按自动分集重新生成并清空该集旧产物")
		} else {
			lg.logf("⚠️ 集 " + ctx.episode + " 方案章节范围已变(" + planC + " → " + ctx.chapters + "),重新生成并清空该集旧产物")
		}
		ctx.clearEpisodeArtifacts(lg)
	}
	chapterText, cerr := ctx.chapterText()
	if cerr != nil {
		return nil, cerr
	}
	runes := len([]rune(chapterText))
	if ctx.scriptMode {
		lg.logf("🎬 视频脚本直出模式: " + filepath.Base(ctx.novel) + "（" + strconv.Itoa(runes) + " 字脚本）")
	} else {
		lg.logf("📖 章节 " + ctx.chapters + "（" + strconv.Itoa(runes) + " 字）")
	}
	if runes > 20000 {
		// 超长静默截断会让超出部分的剧情根本没进方案,视频自然对不上——必须明示。
		// 脚本直出:全文程序化解析(分镜表+六段式逐字),不受 LLM 20000 字上限约束;
		// 仅当程序化解析失败回退 LLM 直出时才有真实截断风险(回退点另行告警)。
		if ctx.scriptMode {
			lg.logf("  ℹ️ 脚本 " + strconv.Itoa(runes) + " 字:直出模式全文程序化解析(不受 20000 字 LLM 上限约束;若解析失败回退 LLM 才有截断风险)")
		} else {
			lg.logf("  ⚠️ 内容 " + strconv.Itoa(runes) + " 字超出 20000 字上限,超出部分可能未被方案覆盖(建议缩小章节范围或分集)")
		}
	}
	// 2026-09-01 日志口径修正:脚本直出(技能侧分镜 json)零 LLM——程序化解析直接采用
	// 脚本内嵌六段式,此前统一打印「🤖 大模型直出」误导(用户以为走了 LLM 浪费时间);
	// 脚本直出明示零 LLM,仅小说解析/LLM 直出才用 🤖 前缀
	if ctx.scriptMode {
		lg.logf("🎬 脚本直出(零 LLM):程序化解析分镜脚本,逐镜 H3 提示词直接采用" + manjuModeTag(ctx.scriptMode) + "...")
	} else {
		lg.logf("🤖 大模型直出 人物/场景/分镜" + manjuModeTag(ctx.scriptMode) + "...")
	}
	// 2026-08-25 防污染:style 净化丢弃词 + knowledge 模板缺失——明示用户,防静默(此前配置串里的
	// "玄幻修仙/Q版呆萌可爱小角色(…)/反派磕碜"等非美术风格词被原样拼进提示词,角色/场景/视频与小说不符)
	if dropped := manjuStyleLastDropped; len(dropped) > 0 {
		lg.logf("  ⚠️ 风格词已净化 " + strconv.Itoa(len(dropped)) + " 项: " + strings.Join(dropped, " / ") + "(非美术风格/括号指令/跨书残留词,已从 image_prompt 与 H3 提示词剔除)")
	}
	if kb := manjuKBLastMissing; len(kb) > 0 {
		lg.logf("  ⚠️ 知识库模板缺失 " + strconv.Itoa(len(kb)) + " 个: " + strings.Join(kb, " / ") + "(已跳过;请在 渲染配置→知识模板 中修正路径)")
	}
	// 2026-08-24 用户反馈:脚本直出(爽文技能阶段6分镜脚本)不要再走 LLM 全量重出——
	// 脚本本身已含电影级分镜(分镜表 8 字段 + 每镜完整六段式 H3 提示词:站位/运镜/光影/音效
	// 逐字写死),LLM 重出既超长截断(此前 plan 失败),又丢站位/运镜(渲染视频人物站位怪)。
	// 优先程序化解析:直接采用脚本内嵌六段式,素材抽角色/场景卡;解析失败才回退 LLM 直出。
	// 2026-08-24 再修复:小说解析模式(用户导入小说目录)也应自动检测 素材/分镜脚本/ 里的
	// 对应集分镜脚本——有则自动切脚本直出(程序化解析零 LLM,且避免逐镜提示词截断),
	// 否则用户"导入小说目录"却走 LLM 直出分镜+逐镜 H3,镜头多时提示词超长截断(实测镜头 10)。
	// 2026-09-01 用户规则分流(2026-09-01 再收紧):AI 一条龙(agentMode)=全走 LLM+Agent,不检测脚本;
	// 普通一条龙/小说导入/全本自动分集=检测对应 json 分镜脚本,有则脚本直出;
	// 无则【不再 LLM 直出】——分镜产出统一交给技能侧(爽文小说技能),提示用户改用 AI 一条龙。
	if !ctx.scriptMode && !ctx.agentMode {
		if sp := ctx.autoStoryboardForEpisode(lg); sp != "" {
			lg.logf("  📽 自动检测到分镜脚本「" + filepath.Base(sp) + "」,切换脚本直出(程序化解析,六段式逐字保留)")
			ctx.scriptMode = true
			ctx.novel = sp
			ctx.chapters = "script"
		} else {
			return nil, fmt.Errorf("未检测到分镜脚本(素材/分镜脚本/*.json):普通模式仅支持脚本直出,分镜产出统一由技能侧完成。请①先用爽文小说技能生成该集分镜脚本(细中细+特效锚定等规则已内置),或②改用「AI 一条龙」走 LLM+Agent 全流程直出")
		}
	}
	if ctx.scriptMode {
		if p, perr := ctx.scriptParsePlan(lg); perr == nil {
			plan = p
			lg.logf("  ✅ 脚本程序化解析直出方案(六段式逐字保留,站位/运镜/光影/音效零丢失)")
			// 2026-08-25 H3 修复:脚本直出此前跳过 validatePlan——台词-时长失衡(9 列时长错位时
			// 长台词被压进 5s)零警告直进渲染,观众感知"台词丢失/内容不完整"。现在脚本模式也跑
			// 硬校验,发现台词超长/角色卡缺失/重复提示词等显式告警(不阻断,脚本仍为权威)。
			if sh, serr := planShots(plan); serr == nil && len(sh) > 0 {
				if probs := ctx.validatePlan(plan, sh); len(probs) > 0 {
					for _, pr := range probs {
						lg.logf("  ⚠️ 脚本直出校验: " + pr)
					}
					lg.logf("  ℹ️ 脚本直出校验共 " + strconv.Itoa(len(probs)) + " 项提示(不阻断渲染;台词超长镜建议检查对应脚本时长)")
				}
			}
			// 解析方案直接落盘并返回(不跑 LLM 校验修复段——脚本即权威,站位/运镜以脚本为准;
			// 角色卡缺失由后续角色管理/素材抽卡补,不阻断渲染)
			if err := ctx.writePlan(plan); err != nil {
				return nil, err
			}
			ctx.writeCharactersJSON(plan)
			chars := len(anyArr(plan["characters"]))
			scenes := len(anyArr(plan["scenes"]))
			shots, _ := planShots(plan)
			lg.logf(fmt.Sprintf("  ✅ %d 角色 / %d 场景 / %d 镜头", chars, scenes, len(shots)))
			ctx.recordPlanFingerprint(plan, lg)
			return plan, nil
		} else {
			// 2026-08-26 可发现性:回退 LLM 后拆镜数不受脚本约束(旧版曾把 16 镜脚本渲成 8 镜),
			// 分镜表行数能数出来时明示落差,用户可当场发现并检查脚本格式,而不是渲完才发现缺镜
			// (2026-09-01 注:回退仅限手动粘贴/旧 md 脚本;技能侧 json 分镜为标准格式,解析必成功)
			if n := len(reScriptTableRow.FindAllString(chapterText, -1)); n > 0 {
				lg.logf(fmt.Sprintf("  ⚠️ 脚本程序化解析失败(%s),回退 LLM 直出——脚本分镜表约 %d 行,LLM 拆镜数不受脚本约束,成片镜数可能对不上,请检查脚本格式(分镜表 8/9 列 + ### Shot N 六段式)后重新生成方案", perr.Error(), n))
			} else {
				lg.logf("  ⚠️ 脚本程序化解析失败(" + perr.Error() + "),回退 LLM 直出")
			}
		}
	}
	sys := manjuDirectSystem(ctx.cfg, ctx.style)
	if ctx.scriptMode {
		// 视频脚本直出:输入即镜头级脚本,直接映射为方案;不走小说素材注入
		sys = manjuScriptSystem(ctx.cfg, ctx.style)
	}
	// 小说素材完整注入(通用目录约定:素材/人物生成提示词.md、素材/场景*.md、素材/其它、设定集/*.md、封面/封面提示词.md):
	// 方案生成时全部参考——角色/场景 image_prompt 贴合素材,世界观/创作规范贴合设定集,避免「素材白准备」
	// 用户规则(2026-08):不注入总集的 风格/负面(StylePrompt/NegPrompt)——渲染风格/负面
	// 一律用用户配置(config.style / render.neg_prompt);仅 AI 一条龙分析时读总集并写回配置。
	if !ctx.scriptMode {
		if assets := scanNovelAssets(ctx.novelRootDir()); len(assets.Files) > 0 {
			lg.logf("📎 已利用小说素材: " + strings.Join(assets.Files, "、"))
			if assets.Setting != "" {
				sys += "\n\n【小说设定集·世界观/大纲/创作规范(角色设定/场景设定/剧情线/文风必须贴合,禁止与设定冲突;未知细节以本章原文为准)】\n" + assets.Setting
			}
			if assets.CharPrompt != "" {
				sys += "\n\n【小说素材·人物生成提示词(角色 image_prompt 必须贴合此文件的人物描述——外观/服装/气质/记忆点以其为准,再结合章节原文细节;不要照抄整段,提炼为可渲染英文)】\n" + assets.CharPrompt
			}
			if assets.ScenePrompt != "" {
				sys += "\n\n【小说素材·场景提示词(场景 image_prompt 必须贴合此文件的场景描述,再结合本章原文)】\n" + assets.ScenePrompt
			}
			if assets.ExtraPrompt != "" {
				sys += "\n\n【小说素材·其它提示词(道具/氛围/H3 母版等,如有相关镜头尽量贴合)】\n" + assets.ExtraPrompt
			}
			if assets.CoverPrompt != "" {
				sys += "\n\n【封面提示词参考(全剧美术基调与封面一致)】\n" + assets.CoverPrompt
			}
		}
	} else if assets := scanNovelAssets(ctx.workdir); len(assets.Files) > 0 {
		// 视频脚本直出(2026-08-24 用户要求:定妆照也要贴合人物提示词文档):
		// 批量导入时源目录 素材/(人物生成提示词.md/场景提示词.md/渲染提示词总集.md)
		// 已复制到 workdir/素材/,这里同样注入——角色/场景 image_prompt 以文档描述为准
		lg.logf("📎 已利用脚本素材: " + strings.Join(assets.Files, "、"))
		if assets.CharPrompt != "" {
			sys += "\n\n【脚本素材·人物生成提示词(角色 image_prompt 必须贴合此文件的人物描述——外观/服装/气质/记忆点以其为准,再结合脚本原文;不要照抄整段,提炼为可渲染英文)】\n" + assets.CharPrompt
		}
		if assets.ScenePrompt != "" {
			sys += "\n\n【脚本素材·场景提示词(场景 image_prompt 必须贴合此文件的场景描述,再结合脚本原文)】\n" + assets.ScenePrompt
		}
		if assets.ExtraPrompt != "" {
			sys += "\n\n【脚本素材·其它提示词(道具/氛围/H3 母版等,如有相关镜头尽量贴合)】\n" + assets.ExtraPrompt
		}
	}
	// 反同质化(整合 ai-film-skills directing 指纹机制):同小说历史摘要注入,
	// 要求新方案五维与近 3 集拉开距离、高潮/首尾手法不与全部历史重复(历史为空则无注入)
	if hist := ctx.manjuFingerprintHistorySummary(nil); hist != "" {
		lg.logf("  🧬 反同质化:注入 " + filepath.Base(ctx.novel) + " 历史指纹摘要(近 3 集)")
		sys += "\n\n【防同质化·历史冲突警告(整合 ai-film-skills 指纹查重,必须与历史拉开距离)】\n" + hist
	}
	// 生成并校验:输出被截断/非 JSON/无镜头都视为无效,追加精简约束重试一次
	plan, err = ctx.llm.chatJSON(sys, truncate(chapterText, 20000), 0.4)
	invalid := err != nil || len(anyArr(plan["shots"])) == 0
	if invalid {
		lg.logf("  ⚠️ 方案生成无效(输出过长/非 JSON/无镜头)，追加精简约束重试一次...")
		plan, err = ctx.llm.chatJSON(sys+manjuConciseSuffix, truncate(chapterText, 20000), 0.4)
		if err != nil || len(anyArr(plan["shots"])) == 0 {
			if err == nil {
				err = fmt.Errorf("方案生成后仍无镜头")
			}
			return nil, fmt.Errorf("方案生成失败(重试后): %w", err)
		}
		lg.logf("  ✅ 精简重试成功")
	}
	if err := ctx.writePlan(plan); err != nil {
		return nil, err
	}
	ctx.writeCharactersJSON(plan)
	chars := len(anyArr(plan["characters"]))
	scenes := len(anyArr(plan["scenes"]))
	shots, _ := planShots(plan)
	lg.logf(fmt.Sprintf("  ✅ %d 角色 / %d 场景 / %d 镜头", chars, scenes, len(shots)))
	// 审计升级 P0:方案硬校验(角色卡完整性/时长-台词量/说话人纪律),不达标带意见修复重试一次——
	// 此前只有 prompt 软约束,LLM 偶尔违规直接流到渲染烧 GPU
	if probs := ctx.validatePlan(plan, shots); len(probs) > 0 {
		lg.logf("  ⚠️ 方案硬校验未过(" + strconv.Itoa(len(probs)) + " 项),带意见修复重试...")
		for _, p := range probs {
			lg.logf("    - " + p)
		}
		fix := "【方案校验未过,逐条修正后重新输出完整方案】\n" + strings.Join(probs, "\n")
		plan2, err2 := ctx.llm.chatJSON(sys+manjuConciseSuffix+"\n\n"+fix, truncate(chapterText, 20000), 0.4)
		if err2 == nil {
			shots2, _ := planShots(plan2)
			if len(anyArr(plan2["shots"])) > 0 && len(ctx.validatePlan(plan2, shots2)) == 0 {
				plan = plan2
				shots = shots2
				if err := ctx.writePlan(plan); err == nil {
					ctx.writeCharactersJSON(plan)
				}
				lg.logf("  ✅ 方案修复通过(" + strconv.Itoa(len(shots)) + " 镜)")
			} else {
				lg.logf("  ⚠️ 修复后仍不达标,沿用原方案继续(渲染/质检兜底)")
			}
		} else {
			lg.logf("  ⚠️ 方案修复重试失败,沿用原方案继续")
		}
	}
	ctx.recordPlanFingerprint(plan, lg) // 反同质化闭环:方案定稿即写回指纹(漏了下次查重就失效)
	return plan, nil
}

// validatePlan 方案运行时硬校验(审计升级 P0):返回问题清单(空=通过)
func (ctx *manjuCtx) validatePlan(plan map[string]any, shots []manjuShot) []string {
	var problems []string
	charNames := map[string]bool{}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok {
			if id := str(m["id"]); id != "" {
				charNames[id] = true
			}
		}
	}
	// 时长域(2026-08-26 激活 min/max_shot_seconds):LLM 直出模式用用户配置的区间
	// (立项.render 规划真正生效);脚本直出模式脚本为权威,仅按 API 硬域 4-15 校验。
	lo, hi := ctx.minSec, ctx.maxSec
	if ctx.scriptMode {
		lo, hi = 4, 15
	}
	for _, s := range shots {
		for _, ch := range s.Characters {
			if ch != "" && !charNames[ch] {
				problems = append(problems, fmt.Sprintf("镜头 %d 登场角色「%s」缺少角色卡(Ref2VA 将无参考图)", s.ID, ch))
			}
		}
		// H3 官方硬规则:每镜 ≤3 个主要角色——超过 3 个时参考图只送前 3(shotRefViews 截断),
		// 第 4+ 角色无参考图必脸崩,必须在方案层拆镜
		if n := len(s.Characters); n > 3 {
			problems = append(problems, fmt.Sprintf("镜头 %d 登场角色 %d 个超 3(H3 参考图上限,多余角色无参考图必脸崩,须拆镜)", s.ID, n))
		}
		if s.Duration < lo || s.Duration > hi {
			problems = append(problems, fmt.Sprintf("镜头 %d 时长 %d 超出 %d-%d 秒", s.ID, s.Duration, lo, hi))
		}
		// 语音预算(2026-08-26 升级):台词+旁白总字数 ÷ 字速 ≤ 时长——此前只算 dialogue、
		// 旁白(narration)完全无预算,旁白超预算的镜 H3 念一半就切(「画面有字无配音」诱因)
		speech := manjuSpeechChars(s.Dialogue, s.Narration)
		if speech > 0 && ctx.charsPerSec > 0 {
			need := float64(speech) / ctx.charsPerSec
			if need > float64(s.Duration) {
				problems = append(problems, fmt.Sprintf("镜头 %d 台词+旁白 %d 字约需 %.1fs 但时长仅 %.1fs(%.1f 字/秒,可能截断)", s.ID, speech, need, float64(s.Duration), ctx.charsPerSec))
			}
		}
		if s.Dialogue != "" {
			for _, line := range strings.Split(s.Dialogue, "\n") {
				if i := strings.Index(line, ":"); i > 0 {
					speaker := strings.TrimSpace(line[:i])
					content := strings.TrimSpace(line[i+1:])
					// 画外群杂(画外·前缀)豁免:不入画的角色也能说话(群众议论承载叙述,
					// 2026-08-27 用户规则:旁白禁止复述画面,背景信息优先群众议论化)
					if !charNames[speaker] && !manjuIsOffScreenSpeaker(speaker) {
						problems = append(problems, fmt.Sprintf("镜头 %d 台词说话人「%s」不在登场角色内", s.ID, speaker))
					}
					// H3 官方经验值:每句对白 ≤20 字(超长句 H3 语音节奏崩,须拆成多句)
					if rc := len([]rune(stripSpeechPunct(content))); rc > 20 {
						problems = append(problems, fmt.Sprintf("镜头 %d 对白 %d 字超 20 字/句(「%s…」,建议拆成多句对话)", s.ID, rc, firstN(content, 10)))
					}
				}
			}
		}
	}
	// 反同质化(整合 ai-film-skills fingerprint.md):vars 近 3 集撞 ≥3 维 / signature 全历史撞 ≥2 项
	// → 判问题进既有「带意见修复重试」闭环(修复不了沿用原方案,不阻断)
	problems = append(problems, ctx.planFingerprintProblems(plan)...)
	// 2026-08-25 用户反馈:镜头 4/7 渲染出相同视频——同 h3_prompt 时条件缓存共用 + 同 seed 必出同画。
	// 检测同集内提示词完全相同的镜头对(脚本直出模式逐字相同最常见);已按镜头号分配独立 seed
	// 避免逐帧雷同,但内容重复仍需作者检查(是否方案重复/该拆镜)。
	seenP := map[string]int{}
	for _, s := range shots {
		if s.H3Prompt == "" || s.TakeTail {
			continue
		}
		if prev, ok := seenP[s.H3Prompt]; ok {
			problems = append(problems, fmt.Sprintf("镜头 %d 与镜头 %d 的 H3 提示词完全相同(内容重复;已按镜头号分配独立 seed 避免逐帧雷同,仍建议检查方案是否重复)", s.ID, prev))
		} else {
			seenP[s.H3Prompt] = s.ID
		}
	}
	return problems
}

// manjuSpeechChars 统计镜头语音总字数:dialogue 每行去「说话人:」前缀,narration 去
// 「旁白:」/「内心·角色名:」前缀;去标点后计数(标点停顿不占语音时长,一字一音节)。
// 2026-08-26:narration 纳入预算(此前旁白零校验,超预算镜 H3 念一半就切)。
func manjuSpeechChars(dialogue, narration string) int {
	stripped := 0
	for _, line := range strings.Split(dialogue, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexAny(line, ":："); i >= 0 && i < 16 {
			line = strings.TrimSpace(line[i+1:])
		}
		stripped += len([]rune(stripSpeechPunct(line)))
	}
	if narr := strings.TrimSpace(narration); narr != "" {
		if i := strings.IndexAny(narr, ":："); i >= 0 && i < 16 {
			narr = strings.TrimSpace(narr[i+1:])
		}
		stripped += len([]rune(stripSpeechPunct(narr)))
	}
	return stripped
}

// stripSpeechPunct 去掉标点与空白,只留有效发音字符(语音字数统计用)
func stripSpeechPunct(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// novelFingerprint 小说正文指纹(大小+mtime):方案复用校验的依据,
// 正文变化后旧方案视为过期,强制重新生成,保证视频内容与小说同步。
// novelFingerprint 方案内容指纹 = 正文 + 小说素材(素材/人物生成提示词.md、场景*.md、设定集/、封面/封面提示词.md)。
// 素材文件是方案角色/场景 image_prompt 的注入源:改了素材必须重新生成方案,
// 否则「素材白准备」——旧方案继续复用(如 8 角色素材文件 + 4 角色旧方案)。
func (ctx *manjuCtx) novelFingerprint() string {
	parts := []string{}
	if ctx.novel != "" {
		if st, err := os.Stat(ctx.novel); err == nil {
			parts = append(parts, fmt.Sprintf("%s|%d|%d", ctx.novel, st.Size(), st.ModTime().UnixNano()))
		}
	}
	// 素材目录指纹:人物生成提示词/场景提示词/设定集/封面提示词,全部计入
	root := ctx.novelRootDir()
	if root != "" {
		var walk func(dir string)
		walk = func(dir string) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				if e.IsDir() {
					walk(p)
					continue
				}
				// 2026-09-01:场景/人物卡已 JSON 化,指纹须同时纳入 .md 与 .json 素材,
				// 否则补卡/改卡不失效旧方案(场景池仍用旧卡)
				if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") &&
					!strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
					continue
				}
				// 只纳入与方案相关的素材(排除正文/全本章节文件——正文由 ctx.novel 指纹覆盖)
				low := strings.ToLower(e.Name())
				rel := strings.ToLower(strings.TrimPrefix(p, root))
				if strings.Contains(low, "人物") || strings.Contains(low, "角色") ||
					strings.Contains(low, "场景") || strings.Contains(low, "封面") ||
					strings.Contains(low, "总集") || // 渲染提示词总集:改风格/负面词必失效旧方案(LLM 模式注入源)
					strings.Contains(rel, "设定集") {
					if st, err := os.Stat(p); err == nil {
						parts = append(parts, fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().UnixNano()))
					}
				}
			}
		}
		walk(root)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts) // 稳定顺序(目录遍历顺序不定,排序保证指纹一致)
	return strings.Join(parts, ";")
}

func anyArr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// manjuClearProject 重跑模式:清空项目全部渲染产物(方案/镜头/成片/条件缓存),资产(定妆照/场景图)保留。
// 缓存按项目名前缀匹配,不误删其他项目。
func manjuClearProject(ctx *manjuCtx, lg *manjuLogger) {
	// 方案与逐镜提示词(全部集)
	if entries, err := os.ReadDir(ctx.analysisDir); err == nil {
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Remove(filepath.Join(ctx.analysisDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空方案文件 " + strconv.Itoa(n) + " 个")
		}
	}
	// 镜头与成片
	removed := 0
	if entries, err := os.ReadDir(ctx.clipsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				epDir := filepath.Join(ctx.clipsDir, e.Name())
				if fs, err := os.ReadDir(epDir); err == nil {
					for _, f := range fs {
						if !f.IsDir() {
							_ = os.Remove(filepath.Join(epDir, f.Name()))
							removed++
						}
					}
				}
			} else {
				_ = os.Remove(filepath.Join(ctx.clipsDir, e.Name()))
				removed++
			}
		}
	}
	if entries, err := os.ReadDir(ctx.workdir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), "_成片.mp4") {
				_ = os.Remove(filepath.Join(ctx.workdir, e.Name()))
				removed++
			}
		}
	}
	if removed > 0 {
		lg.logf("  🧹 已清空镜头/成片 " + strconv.Itoa(removed) + " 个")
	}
	// 条件缓存(项目前缀)
	prefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_"
	condDir := filepath.Join(ctx.sharedModels, "conditioning")
	if entries, err := os.ReadDir(condDir); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				_ = os.Remove(filepath.Join(condDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空条件缓存 " + strconv.Itoa(n) + " 个")
		}
	}
}

// clearEpisodeArtifacts 清空该集旧产物:镜头 mp4 + 条件缓存(缓存名带项目_集号前缀,不误删其他项目)。
// 方案章节范围变化后旧镜头/缓存与新方案内容不符,必须清除让渲染按新方案重做。
func (ctx *manjuCtx) clearEpisodeArtifacts(lg *manjuLogger) {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if entries, err := os.ReadDir(clipsEp); err == nil {
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Remove(filepath.Join(clipsEp, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空该集旧镜头 " + strconv.Itoa(n) + " 个: " + clipsEp)
		}
	}
	// 审计 P4:缓存名是 <项目>_v2_c<指纹>(不含集号),旧的"项目_集号_a指纹_s"前缀
	// 永不匹配任何缓存名——"已清空该集旧条件缓存"从不生效,旧 .pt 只增不减。
	// 方案章节范围变化意味着本项目条件指纹整体失效,按项目前缀清理(等价 manjuClearProject
	// 的缓存部分;缓存名以项目为前缀,不误删其他项目)
	prefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_"
	condDir := filepath.Join(ctx.sharedModels, "conditioning")
	if entries, err := os.ReadDir(condDir); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				_ = os.Remove(filepath.Join(condDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空该项目旧条件缓存 " + strconv.Itoa(n) + " 个(将自动重新编码)")
		}
	}
	// 接缝 latent 随集清理(审计 S6):删本集命名空间目录,防旧方案 latent 残留串接
	latentDir := filepath.Join(ctx.comfyOutput, "h3_context", ctx.latentNS())
	if err := os.RemoveAll(latentDir); err == nil {
		if _, serr := os.Stat(latentDir); os.IsNotExist(serr) {
			lg.logf("  🧹 已清空接缝 latent: " + latentDir)
		}
	}
}

func (ctx *manjuCtx) writePlan(plan map[string]any) error {
	manjuSanitizePlanIDs(plan) // id 即文件名:落盘前清洗,防 Windows 非法字符(如英文双引号)
	if err := os.MkdirAll(ctx.analysisDir, 0755); err != nil {
		return err
	}
	// 记录方案章节范围(复用校验依据);旧方案复用时不覆盖已有标记,保持其来源
	if str(plan["chapters"]) == "" {
		plan["chapters"] = ctx.chapters
	}
	if str(plan["episode"]) == "" {
		plan["episode"] = ctx.episode
	}
	// 记录小说正文指纹(复用校验依据:改过正文 → 方案过期强制重生成)
	if str(plan["novel_fp"]) == "" {
		if fp := ctx.novelFingerprint(); fp != "" {
			plan["novel_fp"] = fp
		}
	}
	return atomicWriteJSON(filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"), plan)
}

// writeCharactersJSON 抽卡/主页角色列表(无 shots)
func (ctx *manjuCtx) writeCharactersJSON(plan map[string]any) {
	manjuSanitizePlanIDs(plan) // 与 writePlan 同规:抽卡/角色管理拿到的 id 已是可落盘名
	_ = os.MkdirAll(ctx.analysisDir, 0755)
	chars, _ := plan["characters"].([]any)
	scenes, _ := plan["scenes"].([]any)
	out := map[string]any{"characters": chars, "scenes": scenes}
	_ = atomicWriteJSON(filepath.Join(ctx.analysisDir, ctx.episode+"_characters.json"), out)
}

// genShotPrompts 逐镜补全 h3_prompt(缺失才生成,进度落盘 _shots_prompts.json)
func (ctx *manjuCtx) genShotPrompts(plan map[string]any, shots []manjuShot, lg *manjuLogger) error {
	promptsPath := filepath.Join(ctx.analysisDir, ctx.episode+"_shots_prompts.json")
	prompts := map[string]string{}
	if b, err := os.ReadFile(promptsPath); err == nil {
		var pm map[string]any
		if json.Unmarshal(b, &pm) == nil {
			for k, v := range pm {
				prompts[k] = str(v)
			}
		}
	}
	need := false
	missing := []int{}
	for _, s := range shots {
		if s.TakeTail {
			continue // 长镜内镜不独立生成提示词(由组头多切点提示词覆盖)
		}
		if s.H3Prompt == "" && prompts[strconv.Itoa(s.ID)] == "" {
			need = true
			missing = append(missing, s.ID)
		}
	}
	if !need {
		return nil
	}
	// 2026-09-01 收紧(用户规则:分镜产出统一交给技能侧,不再 LLM 直出):
	// 脚本直出模式 h3 缺失=脚本不完整,报错提示修正脚本,不用 LLM 补(补的 h3 丢
	// 技能侧站位/运镜/特效锚定,且镜数漂移);LLM 直出/agentMode 才允许逐镜生成。
	if ctx.scriptMode && !ctx.agentMode {
		return fmt.Errorf("脚本直出模式检出 %d 镜缺 h3_prompt(镜 %v)——技能侧脚本为唯一权威,请修正脚本(每镜六段式完整)后重跑;或改用 AI 一条龙", len(missing), missing[:min(len(missing), 6)])
	}
	lg.logf("🤖 逐镜直出完整 H3 提示词（六段式/三段式）...")
	chars, _ := plan["characters"].([]any)
	scenes, _ := plan["scenes"].([]any)
	charMap := map[string]map[string]any{}
	for _, c := range chars {
		if m, ok := c.(map[string]any); ok {
			charMap[str(m["id"])] = m
		}
	}
	sceneMap := map[string]map[string]any{}
	for _, s := range scenes {
		if m, ok := s.(map[string]any); ok {
			sceneMap[str(m["id"])] = m
		}
	}
	// 逐镜提示词生成彼此无依赖(纯文本 LLM,无 429 风暴):并发上限 4 的 worker pool,
	// 15 镜 × 10-30s 串行 → 并发后墙钟时间约 1/4;提示词按镜 ID 落 map,顺序无关。
	// prev_shot(上一镜收尾)来自分镜数据而非 LLM 输出,并发安全。
	var todo []manjuShot
	prevOf := map[int]*manjuShot{}
	last := (*manjuShot)(nil)
	for i := range shots {
		if shots[i].TakeTail {
			continue
		}
		prevOf[shots[i].ID] = last
		last = &shots[i]
		if shots[i].H3Prompt == "" && prompts[strconv.Itoa(shots[i].ID)] == "" {
			todo = append(todo, shots[i])
		}
	}
	if len(todo) > 0 {
		lg.logf(fmt.Sprintf("🤖 逐镜直出完整 H3 提示词（六段式/三段式,并发 %d）...", 4))
		shotObjs, _ := plan["shots"].([]any)
		objOf := map[int]map[string]any{}
		for _, x := range shotObjs {
			if m, ok := x.(map[string]any); ok {
				if n, ok := manjuToInt(m["shot_id"]); ok {
					objOf[n] = m
				}
			}
		}
		var mu sync.Mutex
		var firstErr error
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, s := range todo {
			wg.Add(1)
			safeGo("genprompt", lg, func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				hp, err := ctx.genShotPrompt(s, charMap, sceneMap, prevOf[s.ID])
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("镜头 %d 提示词失败: %w", s.ID, err)
					}
					return
				}
				prompts[strconv.Itoa(s.ID)] = hp
				if m := objOf[s.ID]; m != nil {
					m["h3_prompt"] = hp
				}
				lg.logf(fmt.Sprintf("    ✅ 镜头 %d 提示词就绪（%d 字）", s.ID, len([]rune(hp))))
			})
		}
		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
		// 审计升级 P0:提示词结构校验——六段/三段字段齐全、<d> 台词、<Picture N> 参考标签;
		// 不达标串行修复重试一次(坏提示词进条件缓存会污染 .pt 且难排查)
		var repair []manjuShot
		for _, s := range todo {
			hp := prompts[strconv.Itoa(s.ID)]
			if hp == "" {
				continue
			}
			if probs := ctx.validateShotPrompt(s, hp); len(probs) > 0 {
				repair = append(repair, s)
				lg.logf(fmt.Sprintf("  ⚠️ 镜头 %d 提示词结构校验未过(%d 项),修复重试", s.ID, len(probs)))
			}
		}
		if len(repair) > 0 {
			for _, s := range repair {
				fix := "【提示词结构校验未过,逐条修正后重新输出】\n" + strings.Join(ctx.validateShotPrompt(s, prompts[strconv.Itoa(s.ID)]), "\n")
				hp2, err2 := ctx.genShotPromptWithFix(s, charMap, sceneMap, fix, prevOf[s.ID])
				if err2 == nil && len(ctx.validateShotPrompt(s, hp2)) == 0 {
					prompts[strconv.Itoa(s.ID)] = hp2
					if m := objOf[s.ID]; m != nil {
						m["h3_prompt"] = hp2
					}
					lg.logf(fmt.Sprintf("  ✅ 镜头 %d 提示词修复通过(%d 字)", s.ID, len([]rune(hp2))))
				} else {
					lg.logf(fmt.Sprintf("  ⚠️ 镜头 %d 提示词修复仍不达标,沿用原稿(渲染时注意检查)", s.ID))
				}
			}
		}
	}
	if err := os.MkdirAll(ctx.analysisDir, 0755); err != nil {
		return err
	}
	// 官方 Context IR 扩写(2026-08-26 升级):对空镜三段式提示词做 MiniMax 官方增强,
	// 输出格式与空镜一致,直接替换;失败降级用本地提示词,不阻塞管线。
	if irExpandEnabled(ctx.R) {
		// 从 plan 构造 shots 索引供 IR 读取时长/回写 h3_prompt
		shotObjs := map[int]map[string]any{}
		for _, x := range anyArr(plan["shots"]) {
			if m, ok := x.(map[string]any); ok {
				if n, ok := manjuToInt(m["shot_id"]); ok {
					shotObjs[n] = m
				}
			}
		}
		if err := ctx.expandShotsWithIR(prompts, shotObjs, lg); err != nil {
			lg.logf("  ⚠️ Context IR 扩写失败(已保留本地提示词继续): " + err.Error())
		}
	}
	pm := map[string]any{}
	for k, v := range prompts {
		pm[k] = v
	}
	_ = atomicWriteJSON(promptsPath, pm)
	return ctx.writePlan(plan)
}

// validateShotPrompt 逐镜 h3_prompt 结构校验(审计升级 P0):返回问题清单(空=通过)
func (ctx *manjuCtx) validateShotPrompt(s manjuShot, hp string) []string {
	var problems []string
	hasChar := len(s.Characters) > 0
	if hasChar {
		for _, sec := range []string{"subject_definitions", "summary", "retention_analysis", "detailed_description", "overall_soundscape", "non_diegetic_music"} {
			if !strings.Contains(hp, sec) {
				problems = append(problems, "Ref2VA 缺少六段式字段 "+sec)
			}
		}
	} else {
		for _, sec := range []string{"integrated_multimodal_description", "overall_soundscape", "non_diegetic_music"} {
			if !strings.Contains(hp, sec) {
				problems = append(problems, "FL2VA 缺少三段式字段 "+sec)
			}
		}
	}
	if s.Dialogue != "" && !strings.Contains(hp, "<d>") {
		problems = append(problems, "有台词但提示词无 <d> 原生对白标记")
	}
	if hasChar && !strings.Contains(hp, "<Picture") {
		problems = append(problems, "有角色但提示词无 <Picture N> 参考标签")
	}
	if s.Dialogue != "" {
		for _, line := range strings.Split(s.Dialogue, "\n") {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, ":"); i >= 0 {
				line = strings.TrimSpace(line[i+1:])
			}
			if line == "" {
				continue
			}
			if len([]rune(line)) > 2 && !strings.Contains(hp, line) {
				problems = append(problems, "台词「"+truncate(line, 12)+"」未出现在提示词 <d> 中")
			}
			break // 抽查首句防整体遗漏
		}
	}
	// 契约校验(2026-08-30 H3 官方源码核验转化):Picture 槽位/说话者编号/时码/语言
	// 标签四项与官方输入流机制的硬契约——LLM 直出违反时携带问题清单重写(源头自修),
	// 渲染端 manjuAlignShotPrompt 机器兜底,两层各司其职。
	problems = append(problems, validatePromptContract(hp, s, manjuExpectPicSlots(s))...)
	return problems
}

// genShotPromptWithFix 带修复意见重新生成单镜提示词(校验未过修复重试用);
// prev 为上一镜收尾(链式衔接输入,2026-08-30 ver14,可为 nil)。
func (ctx *manjuCtx) genShotPromptWithFix(s manjuShot, charMap, sceneMap map[string]map[string]any, fix string, prev *manjuShot) (string, error) {
	return ctx.genShotPromptRaw(s, charMap, sceneMap, fix, prev)
}

func (ctx *manjuCtx) genShotPrompt(s manjuShot, charMap, sceneMap map[string]map[string]any, prev *manjuShot) (string, error) {
	return ctx.genShotPromptRaw(s, charMap, sceneMap, "", prev)
}

// genShotPromptRaw 生成单镜 H3 提示词;fix 非空时追加修复意见(校验未过修复重试用)
func (ctx *manjuCtx) genShotPromptRaw(s manjuShot, charMap, sceneMap map[string]map[string]any, fix string, prev *manjuShot) (string, error) {
	hasChar := len(s.Characters) > 0
	// 镜级风格(2026-08-23 多风格并用):shots[].style 优先,缺省继承全局 ctx.style
	shotStyle := strings.TrimSpace(s.Style)
	if shotStyle == "" {
		shotStyle = ctx.style
	}
	sys := manjuShotPromptSystem(hasChar, shotStyle)
	// 用户规则(2026-08):普通执行管线/一条龙不解析小说总集——风格/负面一律用
	// 用户配置(config.style / render.neg_prompt)。仅「AI 一条龙」在
	// manjuAgentStyleAnalyze 分析时读总集并写回配置,此后再由本处使用配置值。
	shotObj := map[string]any{
		"shot_id": s.ID, "shot_size": s.ShotSize, "camera": s.Camera, "action": s.Action,
		"dialogue": s.Dialogue, "narration": s.Narration, "duration": s.Duration,
		"scene": s.Scene, "characters": s.Characters,
		// 2026-08-30 ver14:分镜表光影/音效列保留——LLM 逐镜重写时必须把脚本列信息
		// 翻译进 detailed_description(光线)与 overall_soundscape(音效),防止内容丢列
		"light": s.Light, "sound": s.Sound,
	}
	// 上一镜收尾注入(2026-08-30 ver14,问题③多镜连贯):H3 MotionContext 接缝把上一镜
	// 尾帧/尾音钉入本镜头部——提示词必须承接其收尾构图(矛盾会被渲染成 union=多出人脸),
	// 规则 35 依据 prev_shot 写官方延续句式/气闸/微动作。prev 来自分镜数据,并发安全。
	if prev != nil {
		shotObj["prev_shot"] = map[string]any{
			"shot_id": prev.ID, "shot_size": prev.ShotSize, "camera": prev.Camera,
			"action": prev.Action, "dialogue": prev.Dialogue, "narration": prev.Narration,
			"characters": prev.Characters,
		}
	}
	chars := map[string]any{}
	for _, cid := range s.Characters {
		if m, ok := charMap[cid]; ok {
			chars[cid] = m
		}
	}
	data := map[string]any{
		"shot": shotObj, "characters": chars, "scene": sceneMap[s.Scene],
		"negative_prompt": ctx.negPrompt(),
		"known_issues":    topAgentIssues(ctx.project, 3),
		"ref_available":   ctx.shotRefViews(s),
	}
	// 配音音色绑定(2026-08-26 用户需求):该镜绑定音色角色的 <Audio N> 编号映射,
	// LLM 据此在 subject_definitions 写音色定义、对白处引用(见写作规范第 34 条)
	var vbs []voiceBinding
	if hasChar {
		vbs = ctx.voiceBindingsFor(s)
	}
	if len(vbs) > 0 {
		var vbAny []map[string]any
		for _, b := range vbs {
			vbAny = append(vbAny, map[string]any{"char_id": b.CharID, "audio": b.Audio})
		}
		data["voice_bindings"] = vbAny
	}
	// 多切点长镜:附加规范 + 组内各镜字段与切点时间(take_shots 供 LLM 直引,不必自算)
	if len(s.TakeGroup) > 1 {
		sys += manjuMultiCutAddon
		var group []any
		cum := 0.0
		for _, g := range s.TakeGroup {
			gm := map[string]any{
				"shot_id": g.ID, "shot_size": g.ShotSize, "camera": g.Camera, "action": g.Action,
				"dialogue": g.Dialogue, "narration": g.Narration, "duration": g.Duration,
				"scene": g.Scene, "characters": g.Characters, "cut_at": manjuTimecode(cum),
			}
			group = append(group, gm)
			cum += float64(g.Duration)
		}
		data["take_shots"] = group
	}
	ctxData, _ := json.Marshal(data)
	if fix != "" {
		ctxData = []byte(string(ctxData) + "\n\n" + fix)
	}
	out, err := ctx.llm.chatJSON(sys, string(ctxData), 0.3)
	if err != nil {
		// 2026-08-24 修复:单镜提示词输出超长截断(finish_reason=length)时,
		// 追加精简约束重试一次(此前直接失败,plan 阶段 17 镜里镜头 10 截断即卡死整集)
		// 2026-08-25 加强:旧重试仍是「完整 system + 一句精简要求」,LLM 被完整六段式模板
		// 带偏继续输出长文(实测镜头 11 二次截断)。改为「精简 system + 精简 data」重试——
		// 不加载完整 Ref2VA/FL2VA 模板,只要求核心字段精简输出,从源头压短。
		if err == errLLMTruncated {
			compactSys := "你是 MiniMax H3 视频生成模型的提示词专家。基于镜头信息输出【精简】H3 提示词,严格 JSON {\"h3_prompt\":\"提示词全文\"}。h3_prompt 结构:①开头 风格一句+[Shot N]+景别运镜;②画面 detailed_description 100-150 英文词(构图/主体动作/运镜/光影,台词中文逐字);③音效 1 句;④配乐 1 句;⑤结尾亮度句 subject clearly visible and well-lit, not nearly black。整个 h3_prompt 严格 ≤ 900 tokens,宁可精简,禁止铺陈。台词/旁白原文逐字保留。"
			compactData := map[string]any{
				"shot":            shotObj,
				"negative_prompt": ctx.negPrompt(),
				"ref_available":   ctx.shotRefViews(s),
			}
			cd, _ := json.Marshal(compactData)
			if out2, err2 := ctx.llm.chatJSON(compactSys, string(cd), 0.3); err2 == nil {
				if hp2 := str(out2["h3_prompt"]); hp2 != "" {
					return ensureVoiceBindings(hp2, vbs, ctx.refContractFor(s)), nil
				}
			}
		}
		return "", err
	}
	hp := str(out["h3_prompt"])
	if hp == "" {
		return "", fmt.Errorf("LLM 未返回 h3_prompt")
	}
	return ensureVoiceBindings(hp, vbs, ctx.refContractFor(s)), nil
}

// manjuTimecode 秒 → MM:SS.mmm(H3 官方多切点时间戳格式)
func manjuTimecode(sec float64) string {
	total := int(sec * 1000)
	ms := total % 1000
	ss := (total / 1000) % 60
	mm := total / 60000
	return fmt.Sprintf("%02d:%02d.%03d", mm, ss, ms)
}

// ---- 资产(定妆照 SDXL + 场景图 Z-Image,已存在复用) ----

// negPrompt 负面提示词(角色/场景图生成):内置安全底线恒在(2026-08-27 三修:
// 用户配置 render.neg_prompt 曾**整体替换**内置——自定义只剩画质词时,禁日漫/禁真人/
// 服装禁裸全线裸奔,精卫 Q 版袒胸实锤)。改为**追加**语义:内置在前(高权重位),
// 用户词接后,只增不减。用户规则(2026-08):普通管线不解析小说总集负面——
// 总集负面仅在「AI 一条龙」分析时读入并写回配置。
func (ctx *manjuCtx) negPrompt() string {
	if s := strings.TrimSpace(str(ctx.R["neg_prompt"])); s != "" {
		return manjuNegPrompt + ", " + s
	}
	return manjuNegPrompt
}

// characterCkpt 角色定妆照 checkpoint(按性别)。
// 2026-08-24 用户规则:SDXL 全面禁用(观感差),定妆照统一 Z-Image/Krea-2——本函数生产管线
// 已不再调用(portraitWF 不落 SDXL 分支),仅保留定义供兼容/测试引用。
// 2026-08-26:SDXL/animagine 模型文件已清理,不再有兜底 checkpoint,禁日漫硬防线命中返回空。
func (ctx *manjuCtx) characterCkpt(char map[string]any) string {
	cm, _ := ctx.R["char_models"].(map[string]any)
	g := str(char["gender"])
	ckpt := ""
	if g == "男" {
		ckpt = str(cm["男"])
	}
	if g == "女" {
		ckpt = str(cm["女"])
	}
	if ckpt == "" {
		ckpt = str(ctx.R["animagine_ckpt"])
	}
	// 禁日漫硬防线:识别日漫系 checkpoint 名(animagine/anything-v3/counterfeit/meinamix 等),
	// 命中即拒绝(返回空),杜绝日漫脸定妆照流入渲染
	if manjuIsAnimeCheckpoint(ckpt) {
		return ""
	}
	return ckpt
}

// manjuIsAnimeCheckpoint 判定 checkpoint 是否为日漫系模型(禁日漫硬规则用)。
// 命中关键词即认为会输出日本动漫脸,定妆照拒绝使用。
func manjuIsAnimeCheckpoint(name string) bool {
	low := strings.ToLower(name)
	for _, kw := range []string{"animagine", "anything", "counterfeit", "meinamix", "nijijourney", "anime-xl", "aam_xl", "toonyou"} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// manjuPortraitW/H 定妆照固定尺寸(SDXL 原生最佳 1024×1024):
// 定妆照必须与项目画幅/分辨率档位完全解耦——同一角色在横屏/竖屏/不同档位项目里
// 若按各自 ctx.w×ctx.h 生成,构图比例与细节密度漂移,H3 参考图内容随之变化导致角色不一致。
// 固定尺寸后同角色跨项目同 prompt 同尺寸 → 形象唯一稳定。
const manjuPortraitW, manjuPortraitH = 1024, 1024

// manjuViewW/H full/side/Q 版视图画幅(2026-08-27 用户反馈 full 不到脚):
// 方形 1024 里 7 头身全身脸部占比过小,模型放大人物裁掉下半身;竖幅 2:3 是全身立绘
// 经典比例,模型有「竖构图=全身立绘」先验。主图/face/detail 仍用方形(形象稳定锚)。
const manjuViewW, manjuViewH = 832, 1248

// manjuQStrength Q 版 img2img denoise 强度:换装成 chibi 需要高 denoise 彻底重绘构图/比例
// (0.85 以下容易残留主图的正常 7 头身)。2026-08-26 用户实测「Q版一点也不呆萌」:
// 0.88 重绘不足,主图写实构图残留压制 Q 萌比例——提到 0.93 让 prompt 主导大头小身,
// 身份靠 身份锚+面容锚 双锁兜底。
const manjuQStrength = 0.93

// manjuViewGen 视图生成逻辑代数(2026-08-26):写入 asset_map.json views_gen,落后则删除全部
// 旧视图重出。2=Q版两头身(幼态化修正)/视图性别锚(防女性长胡须)/兽形角色板/身份前缀清理;
// 3=角色板改为基于主图 img2img(板与主图同一人)+板生成顺序提前;
// 4=Q版呆萌强化(denoise 0.93 + 大头呆萌特征词);
// 6=删角色板(2026-08-27 用户裁决:板零消费——渲染参考只用 front/full/detail/主图,前端过滤不显示,
//   纯成本且网格重绘易画风漂移)+full/side/detail 引擎切 Krea-2 img2img(与主图同引擎统一画风,
//   Z-Image 大重绘按照片先验出图导致视图动漫化/与 3D 主图割裂);
// 5=视图清洗与分工修正(2026-08-26 用户三条反馈:①角色板动漫化 ②full/side/detail 全成正面照
//   ③Q版应基于全身照)——视图 prompt 剥 Front-facing portrait 前缀(主定妆特写构图污染视图)、
//   full/side 重绘强度提高(full 0.88 真全身/side 0.90 真 90 度侧面)、Q版 initImage 改用
//   full 全身照、角色板 3D 档措辞 3D 化+denoise 降 0.86(过度重绘丢主图形象→动漫设定卡先验)。
// 7=下半身着装锚(2026-08-27 用户反馈:小男孩全身图下半身裸露没穿裤子)——full/side 视图锚
//   补完整下装硬约束+未成年人着装护栏(manjuMinorGuard)+负面词补腰部以下裸露词组,
//   存量 full/side 视图必须全部重出。
// 8=兽类视图锚分流(2026-08-28 用户反馈:灵宠小貔 full/Q 渲染出人形)——兽类用兽形锚+
//   manjuBeastStrip 剥 image_prompt 人互动子句,人形着装/未成年人护栏不参与。
// 10=兽形判定词表修复(2026-08-28 猫·二两:9 的兽形后缀/剥人锚对"漏判人形"的猫
//   根本没生效——常见动物词补齐后兽形分支真正接管,全部视图按兽形重出)。
const manjuViewGen = 10

// manjuQGen Q 版生成逻辑代数(独立于 views_gen,只清 _q.png):2=发色锁(HAIR LOCK
// 显式点名发色/毛色,高重绘下双色挑染不再被平均成单色;2026-08-27 用户反馈叶澜黑白发
// Q 版变纯黑);3=着装锁(OUTFIT LOCK+禁敞开外套露胸+BJD 素体措辞修正,2026-08-27
// 用户反馈男性 Q 版袒胸露乳);4=着装锁强化(袍服/儒袍/官袍敞胸全覆盖:宽袍交领闭合/
// 覆盖锁骨与胸口/服装子句剥敞开感词,2026-08-27 用户反馈柳含烟·魏鹤年·魏琮 Q 版
// 宽袍敞开露胸,旧 "zipped and buttoned" 措辞对无拉链的古风袍服无效);
// 9=兽类剥人+性别/老态修正(2026-08-28 用户反馈废根噬天三症状:①灵宠 Q 版出人形
//   =image_prompt 人互动子句(cultivator's shoulder)未剥 ②顾清寒男 Q 版被画成女
//   =阴柔美男词+chibi 幼态先验,masculine 单词压不住 ③80岁玄机老人 Q 版年轻化
//   =无胡须词老者被 no beard 一刀切剃须+幼态);
// 10=fullRef 快照时机修复(2026-08-28 用户实锤「Q版都没按全身照生成」:fullRef 循环前
//   快照,全新项目 _full.png 尚未生成 → Q 恒「基于主图」方形脸图重绘=畸形+身份漂移,
//   修复后 Q 分支生成时重新解析);
// 11=Q 版画风与项目风格档解耦(2026-08-28 用户反馈「Q版变成动漫形象」:style=real 落
//   "chibi illustration" 插画分支+标志特征角色 denoise 1.0 文本唯一画风源 → 2D 赛璐璐
//   动漫;统一 3D 手办潮玩锚+基底前置+禁 2D 平面形态);
// 12=Q 版提示词紧凑化(2026-08-28 用户反馈「同逻辑有的Q版有的手办有的动漫」:cfg=1.0 蒸馏
//   无负面通道,提示词随角色卡厚薄 2000~4300 漂移→遵循度抽奖;统一紧凑骨架压回 ~2000
//   定案量级,着装锁换 manjuQOutfitCompact,删重复特征锁/长覆盖行/长幼态行);
// 13=Q 版底图改回正面主图+画布方形同幅(2026-08-28 用户裁决:全身照底图多轮不理想——
//   7 头身立绘构图牵引+init 脸部像素小身份信号弱;主图大脸身份信号强,1024 方形同幅
//   消除晨间「方图拉竖幅」形变根因);
// 14=两段式生成·形态→身份(2026-08-28 终局:单次 img2img 死结=低重绘保构图(主图=半身
//   写实残留)高重绘丢身份;①形态段纯文生图短提示词锁两头身3D手办+特征进构成,
//   ②身份段以形态段产物为底 0.5 低重绘注入发色/服装/性别/面容);
// 20=提示词污染审计(2026-08-28 用户「全面审计,别老出问题」:①armor 材质词条件化
//   manjuHasArmor——林小满Q版左肩凭空金属护甲;②eyeCls 精确化 manjuEyeClsFor——老纪
//   挂脖老花镜被 "glasses/visor" 混写画成赛博护目镜,挂脖镜保持挂脖;③特征锁全列举
//   (glasses/visors/scars)改中性 signature features;④无甲基底去 fur 兽毛词);
// 21=兽形判定修复(2026-08-28 猫·二两Q版生成人:动物本体词补齐前纯英文动物卡
//   漏判人形,Q版走了人形手办模板;判兽形后兽形分支(毛绒小兽+NOT a human/NOT
//   wearing human clothes)真正接管);
// 22=兽形毛色显式锁 manjuFurAnchor(2026-08-28 猫·二两:主图橘白 Q版黑灰狸花——
//   0.93 高重绘身份靠文本,删 img 拼接后色词零出现、HAIR LOCK 只抓到无色名的
//   dusty fur,毛色按猫类默认先验随机;色名显式点名与人形 HAIR LOCK 同级)。
// manjuQGen Q 版生成逻辑代数(独立于 views_gen,只清 _q.png):2=发色锁(HAIR LOCK
// 显式点名发色);…;23=年龄分档锚(2026-08-30 用户实锤:老年人生成的全是年轻 Q 版
// ——chibi 基底是年轻萌模板+男性分支硬编码 young man's face;manjuAgeBand/
// manjuQAgeAnchor 按角色卡机械分档注入老年皱纹/中年成熟/少年儿童特征,存量必须重出)
const manjuQGen = 23

// manjuPortraitGen 主图代数(2026-08-27 六修):单人/纯白背景/服装严格锚上线时 bump,
// stageAssets 检测到落后即清全部旧主图重出(视图/Q版联动)。
// 3=兽形判定修复(2026-08-28 猫·二两案:manjuBeastBody 补常见动物词(中文+英文词
//   边界 manjuAnimalEnRe)——species 缺失的纯英文动物卡(an orange stray cat)此前
//   漏判人形,主图无兽类锚/full 出人/Q版人形手办;另 facecrop YuNet 阈值 0.5→0.4);
// 4=face裁切窗口定版(2026-08-28 标尺实测:单人白底方形主图头顶20%/下巴70%,旧窗
//   口 8%-52% 切口鼻、8%-70% 贴下巴线切嘴;定版 8%-88% 对齐检测命中分支比例,
//   主图重出联动 face/视图/Q版 全链重出重裁)。
const manjuPortraitGen = 5

// portraitWF 定妆照工作流按风格分流:含写实元素用 Z-Image(真人级),其余用 SDXL checkpoint。
// 尺寸固定为标准 1024×1024(与项目画幅无关);正脸参考(ensureFaceCrop)再从该图按视频比例裁切。
// initImage 非空 → img2img(主图作 latent 起点保留身份,视图 full/side/detail 用,
// 防生成不相干新角色)。initStrength:denoise 强度——视图换视角需要更高(0.8)才不像主图正面,
// 0.6 对 turbo 模型重绘量太小(四视图全变正面,用户反馈)。
// manjuPortraitPrompt 角色定妆照 prompt 统一包装(2026-08-24 用户规则升级:写实拟动漫,禁日漫+禁真人)。
// 所有角色图(主图/视图/Q版/抽卡)强制附加拟动漫锚——这是最后防线,不依赖 LLM 直出或素材自觉:
// 任何来源的 image_prompt 只要含真人写实措辞(photorealistic/real human/realistic photo)都替换为
// 半写实拟动漫,并附加 manjuPortraitAnchor(东方/中式面孔,非日漫,非真人,防侵权)。
// appearance 为角色卡独特面容特征(发型/眼睛/痣/疤/气质),注入"独特面容锚"防止不同角色撞脸
// (2026-08-24 用户反馈:不同角色生成相同脸)。视图生成也传角色 appearance 保持同一人。
func manjuPortraitPrompt(prompt, appearance string) string {
	p := prompt
	// 防真人:真人写实措辞 → 半写实拟动漫(残留 photorealistic 会把模型拉向真人脸=侵权)
	for _, re := range []struct{ old, neu string }{
		{"photorealistic", "semi-realistic stylized"},
		{"realistic photo", "stylized illustration"},
		{"real human", "stylized character"},
		{"realistic photograph", "stylized illustration"},
		{"realistic human", "stylized character"},
		{"photograph", "painterly illustration"},
		{"live-action", "cinematic stylized"},
	} {
		p = strings.ReplaceAll(p, re.old, re.neu)
		p = strings.ReplaceAll(p, strings.ToUpper(re.old[:1])+re.old[1:], re.neu)
	}
	// 拟动漫锚强制附加(禁日漫/禁真人/东方面孔)
	if !strings.Contains(p, "not a Japanese anime") {
		p = p + ", " + manjuPortraitAnchor
	}
	// 独特面容锚:角色卡 appearance 逐字引用(防不同角色撞脸、视图串脸)
	// 2026-08-24 用户反馈:不同角色生成相同的脸——必须带每个角色的独有面容特征。
	if appearance != "" && !strings.Contains(p, appearance) {
		p = p + ", distinct unique face with: " + appearance
	}
	return p
}

// ---- 2026-08-25 用户规则:角色种族/性别/年龄/胡须画像(Q版/视图/主图/抽卡共用) ----

// manjuBeastAnchor 妖兽/灵宠等非人形种族的拟动漫锚(替代人类角色的 manjuPortraitAnchor,
// 防止兽类角色被「East Asian/Chinese character」锚拉成人脸)。
const manjuBeastAnchor = "semi-realistic stylized illustration of a fantastical beast creature, subtly stylized painterly art, not a photorealistic photo, not a Japanese anime/manga style, avoid resembling any real animal breed or any real person"

// manjuBeastBody 兽形身体强特征词(检测 species 缺失的旧方案/脚本素材角色):
// 只收「兽形身体」级强特征,不收动物名(狐/龙 等会误伤 虎牙/龙傲天 这类人形角色)。
var manjuBeastBody = []string{
	"兽形", "兽类", "兽身", "兽体", "四足", "毛茸茸", "通体雪白", "通体漆黑", "九尾",
	"兽瞳", "兽耳", "獠牙", "利爪", "兽爪", "鳞甲", "鳞片", "蛇身", "鹿角", "龙鳞",
	"凤羽", "翅膀", "尾巴", "鬃毛", "妖兽", "灵宠", "神兽", "灵兽", "魔兽", "凶兽",
	"异兽", "精怪", "妖物", "坐骑",
	// 2026-08-28(猫·二两 full/Q版画出人):常见动物中文本体词——species 缺失的卡面
	// 一个中文古风词都命中不了会被当人形。人形否决(manjuHumanFigureRe)在前,
	// 狐耳娘等"26岁女子+狐耳"不会误判。
	"橘猫", "狸花", "奶牛猫", "狸猫", "家猫", "野猫", "小猫", "猫咪", "小狗", "家犬",
	"中华田园犬", "柴犬", "柯基", "边牧", "金毛犬", "布偶猫", "英短", "鹦鹉", "仓鼠",
}

// manjuAnimalEnRe 常见动物英文本体词(词边界匹配,2026-08-28 猫·二两判定修复):
// an orange and white chubby stray cat 类英文卡面;词边界防 category/catch/
// dogma/fishery 等子串误伤。
// 2026-09-01 扩充(小月案实锤:image_prompt「tiny palm-sized creature with soft fur」,
// species 缺失+creature/fur 不在词表 → 判成人形 → 视图按人画,正面却按兽画):
// 通用兽类词 creature/fur/paws/whiskers/fluffy 等补入,species 缺失的兽卡不再漏判
var manjuAnimalEnRe = regexp.MustCompile(`(?i)\b(cat|kitten|kitty|dog|puppy|panda|fox|wolf|tiger|lion|bear|rabbit|bunny|hamster|parrot|bird|fish|snake|horse|deer|squirrel|otter|ferret|hedgehog|turtle|frog|dragon|phoenix|creature|fur|paws|whiskers|fluffy|beast|animal|palm-sized|tail)\b`)

// manjuIsMinorCast 群演轻量卡判定(2026-08-27 群演分级):脚本直出时有台词但无角色卡
// 的说话人自动建卡(minor:true)——资产阶段只出 1 张定妆照+正脸(跳过视图/Q版/双形态)。
func manjuIsMinorCast(m map[string]any) bool {
	if m == nil {
		return false
	}
	b, _ := m["minor"].(bool)
	return b
}

// manjuIsItem 角色是否物品类(有意识/可说话的器物与植物:神器/法宝/武器/道具/灵器/
// 灵植/花精/树精等,2026-08-29 用户硬性规则:物品必须按物品渲染,禁止人形/人脸/人衣;
// Q 版为物品萌化不受影响)。判定:①species 权威含物品词;②role/id 含 神器/法宝/器物/
// 武器/兵器/道具/法器/灵器/秘宝/剑灵/灵植/花精 等。
func manjuIsItem(m map[string]any) bool {
	if m == nil {
		return false
	}
	if s := str(m["species"]); s != "" {
		// 2026-08-29 词表对齐(审计 V4):scriptRoleSpecies 会返回 藤精/花精/树精 等
		// 植物精 species——species 层词表不含 X精 类时误判兽形管线;器灵类同理补齐
		for _, k := range []string{"物品", "神器", "法宝", "器物", "法器", "武器", "兵器", "道具", "灵器", "秘宝", "圣物", "容器", "饰品", "灵植", "植物", "盆栽", "藤蔓", "灵草", "仙草", "树精", "花精", "草精", "藤精", "竹精", "菇精", "剑灵", "塔灵", "灯灵", "镜灵", "器灵", "壶灵", "书灵"} {
			if strings.Contains(s, k) {
				return true
			}
		}
	}
	for _, f := range []string{"role", "id"} {
		t := str(m[f])
		if t == "" {
			continue
		}
		for _, k := range []string{"神器", "法宝", "器物", "法器", "武器", "兵器", "道具", "灵器", "秘宝", "圣物", "剑灵", "塔灵", "灯灵", "镜灵", "器灵", "树精", "花精", "草精", "藤精", "灵植", "盆栽"} {
			if strings.Contains(t, k) {
				return true
			}
		}
	}
	// ③ 兜底(2026-08-29 阿碧实锤:LLM 生成角色卡漏 species 时,image_prompt 植物本体
	// 措辞本身可判定——pothos/wisteria/藤蔓/盆栽 等专属植物词 + 无人形主体词 → 植物类物品)
	if ip := str(m["image_prompt"]); ip != "" && !manjuHumanFigureRe.MatchString(ip) {
		low := strings.ToLower(ip)
		for _, k := range []string{"pothos", "wisteria", "vine plant", "potted plant", "plant itself", "植物本体", "藤蔓", "盆栽"} {
			if strings.Contains(low, k) {
				return true
			}
		}
	}
	return false
}

// manjuItemAnchor 物品本体锚(2026-08-29 用户硬性规则:物品按物品渲染):
// 主体=物品本身(材质/形制/纹路/光泽),显式禁人形/人脸/四肢/人衣。
const manjuItemAnchor = ", the item itself (its material, shape, engravings, glow and craftsmanship), NOT a person, NOT a humanoid, no human body, no human face, no limbs, no human clothes"

// manjuItemBoardLayout 物品角色板布局(三视图=多角度器物图 + 细节特写,禁人形)
const manjuItemBoardLayout = ", item reference board (object info sheet): a clean vertical grid sheet combining: 1) multiple views of the same item side by side (front / side profile / top-down); 2) close-up detail panels (material texture, engravings, ornaments, glow); 3) a 5-color HEX palette swatch row; 4) one line of Chinese item bio text. light plain background, neat grid layout, all panels showing the same item with identical shape, material and markings, NOT a person, NOT a humanoid, no human body, no human face"

// manjuIsBeast 角色是否非人形兽类/生灵:①species 字段权威(非空且非人 → 非人形);
// ②role/id 含 灵宠/宠物/坐骑/妖兽/神兽;③appearance/image_prompt 命中兽形身体强特征兜底。
// 物品类(manjuIsItem)不判兽形——走物品本体渲染链(2026-08-29 用户硬性规则)。
func manjuIsBeast(m map[string]any) bool {
	if m == nil || manjuIsItem(m) {
		return false
	}
	if s := str(m["species"]); s != "" {
		if s == "人" || s == "人类" || s == "人族" || strings.Contains(s, "人形") || strings.Contains(s, "拟人") {
			return false
		}
		return true
	}
	for _, f := range []string{"role", "id"} {
		t := str(m[f])
		if t != "" && (strings.Contains(t, "灵宠") || strings.Contains(t, "宠物") || strings.Contains(t, "坐骑") ||
			strings.Contains(t, "妖兽") || strings.Contains(t, "神兽")) {
			return true
		}
	}
	// 2026-08-27 修复(九尾 Q 版渲染成正常比例动漫人,非 Q 版兽形也非 Q 版人形):
	// ③兜底加人形否决——image_prompt 是形象权威,含人形主体措辞(N-year-old/woman/
	// girl…)的人形妖怪(九尾狐精人设=26 岁美妆博主)不得因「记忆点含尾巴/九尾」误判
	// 兽形:兽形分支的 "chibi beast + NOT a human" 与人形 init 图/人形 image_prompt
	// 完全矛盾,Krea-2 强指令跟随下折中出正常比例动漫人。前两层(species 权威/身份词)
	// 不受否决——真兽形角色照判。
	if ip := str(m["image_prompt"]); ip != "" && manjuHumanFigureRe.MatchString(ip) {
		return false
	}
	hay := str(m["appearance"]) + " " + str(m["image_prompt"])
	for _, k := range manjuBeastBody {
		if strings.Contains(hay, k) {
			return true
		}
	}
	// 2026-08-28 常见动物英文本体词(词边界):纯英文卡面的猫狗等不再漏判为人形
	if manjuAnimalEnRe.MatchString(hay) {
		return true
	}
	return false
}

// manjuHumanFigureRe image_prompt 人形主体措辞(兽形判定的③兜底否决条件):
// 人物形象提示词惯例必含 年龄/性别主体词;兽形提示词(beast/creature/furry)不含。
var manjuHumanFigureRe = regexp.MustCompile(`(?i)\d+\s*[- ]year[- ]s?\s*old|\b(woman|girl|boy|lady|gentleman)\b|\bman\b`)

// manjuIsFemale 女性角色(gender 唯一权威,2026-08-24 用户要求)
func manjuIsFemale(m map[string]any) bool {
	return m != nil && str(m["gender"]) == "女"
}

// manjuYoungAge 年轻男性年龄词(无胡须;胡须仅限成年/老年男性,2026-08-25 用户规则)
var manjuYoungAge = []string{"少年", "青少年", "青年", "年轻", "男孩", "小男孩", "孩童", "儿童", "幼年", "孩子", "学生", "童子", "娃娃", "小少", "十来岁", "十几岁", "未成年"}

// manjuIsYoungMale 年轻男性判定:gender=男 + age 含年轻词或数字年龄 < 20
func manjuIsYoungMale(m map[string]any) bool {
	if m == nil || str(m["gender"]) != "男" {
		return false
	}
	age := str(m["age"])
	for _, k := range manjuYoungAge {
		if strings.Contains(age, k) {
			return true
		}
	}
	if re := regexp.MustCompile(`(\d+)`); re.MatchString(age) {
		n, _ := strconv.Atoi(re.FindStringSubmatch(age)[1])
		if n < 20 {
			return true
		}
	}
	return false
}

// manjuBeardWords 胡须特征词(英文小写匹配)
var manjuBeardWords = []string{"胡须", "胡子", "络腮", "山羊胡", "白须", "长须", "美髯", "髯", "beard", "mustache", "moustache"}

// manjuHasBeard 角色是否有胡须(仅对人类角色有意义;appearance/image_prompt/costume 三源)
func manjuHasBeard(m map[string]any) bool {
	if m == nil {
		return false
	}
	hay := strings.ToLower(str(m["appearance"]) + " " + str(m["image_prompt"]) + " " + str(m["costume"]))
	for _, k := range manjuBeardWords {
		if strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

// manjuBeardEnforce 胡须纪律强制(2026-08-25 用户规则):
// 女性一律无胡须;年轻男性无胡须;成年/老年男性已写明胡须则保留;其余默认无胡须。兽类跳过。
func manjuBeardEnforce(m map[string]any) string {
	if m == nil || manjuIsBeast(m) {
		return ""
	}
	if manjuHasBeard(m) && !manjuIsFemale(m) && !manjuIsYoungMale(m) {
		return ", keeping the character's beard and mustache"
	}
	// 老者(2026-08-28 用户反馈「老的Q版形象那么年轻」):无胡须词的老年男被一刀切
	// no beard 强制剃须——chibi 幼态先验+无胡须=年轻化。老者改为跟随主图胡须+显式
	// 老态特征锚(皱纹/苍老皮肤/明确老年人),压制 chibi 幼态化。
	if manjuIsOldMale(m) {
		return ", facial hair exactly as shown in the reference portrait, elderly look with a wrinkled aged face, sagging aged skin, clearly an old elderly man, never a young face"
	}
	return ", no beard, no mustache, no facial hair"
}

// manjuIsOldMale 老年男性判定(age 老年词 或 数字年龄 ≥50 岁;仅男性)
func manjuIsOldMale(m map[string]any) bool {
	if m == nil || manjuIsFemale(m) {
		return false
	}
	if str(m["gender"]) != "男" {
		return false
	}
	age := str(m["age"]) + " " + str(m["appearance"]) + " " + str(m["image_prompt"])
	for _, kw := range []string{"老年", "老者", "花甲", "暮年", "古稀"} {
		if strings.Contains(age, kw) {
			return true
		}
	}
	for _, mm := range regexp.MustCompile(`(\d+)\s*(?:years?[- ]old|岁)`).FindAllStringSubmatch(age, -1) {
		if n, err := strconv.Atoi(mm[1]); err == nil && n >= 50 {
			return true
		}
	}
	return false
}

// manjuSanitizeAppearance 胡须净化:女性/年轻男性的面容锚剥离胡须词(防 LLM 误给胡须被锚强制画出来)
func manjuSanitizeAppearance(m map[string]any, appearance string) string {
	if !manjuIsFemale(m) && !manjuIsYoungMale(m) {
		return appearance
	}
	for _, k := range []string{"络腮胡", "山羊胡", "白胡须", "胡须", "胡子", "白须", "长须", "美髯", "髯", "beard", "mustache", "moustache"} {
		appearance = strings.ReplaceAll(appearance, k, "")
	}
	appearance = strings.TrimSpace(strings.Trim(appearance, "，,;； "))
	return appearance
}

// manjuStripFullBody 剔除 image_prompt 里的全身立绘/比例指令(与 Q 版 chibi 小身体冲突,
// 如「full body, head to toe」「7 头身」「禁止大头小身」——Q 版恰恰是大头小身)
func manjuStripFullBody(s string) string {
	for _, k := range []string{
		"full body, head to toe", "full body", "head to toe",
		"natural 7-head-tall proportions", "natural proportions",
		"禁止大头小身/半身/头像/portrait", "禁止大头小身", "禁止半身", "禁止头像",
		"正常比例", "7 头身", "自然 7 头身",
	} {
		s = strings.ReplaceAll(s, k, "")
	}
	s = regexp.MustCompile(`,\s*,+`).ReplaceAllString(s, ",")
	return strings.Trim(s, " ,")
}

// manjuPortraitPromptFor 人形/兽形角色卡级拟动漫包装(2026-08-25;
// 2026-08-29 架构重构:物品角色已在 portraitPromptFor 分流到独立物品管线
// manju_char_species.go,不再进入本函数——物品与人形措辞彻底解耦,互不污染):
// 按种族选锚(人=东方人像锚;妖兽/灵宠=兽类锚,防兽被画成人脸)、
// 面容锚取角色卡 appearance 并做胡须净化、末尾强加胡须纪律。
// 人形/兽形角色图(主图/视图/Q版/抽卡)统一走它——最后防线,不依赖 LLM 自觉。
func manjuPortraitPromptFor(prompt string, m map[string]any) string {
	beast := manjuIsBeast(m)
	ap := manjuSanitizeAppearance(m, str(m["appearance"]))
	var p string
	if beast {
		p = manjuPortraitPrompt(prompt, "")
		// 人类锚 → 兽类锚(替换残留的「East Asian/Chinese character」措辞)
		p = strings.ReplaceAll(p, manjuPortraitAnchor, manjuBeastAnchor)
		p = strings.ReplaceAll(p, "an East Asian/Chinese character", "a fantastical beast creature")
		p = strings.ReplaceAll(p, "East Asian/Chinese character", "fantastical beast creature")
		if !strings.Contains(p, "fantastical beast creature") && !strings.Contains(p, "beast creature") {
			p = p + ", " + manjuBeastAnchor
		}
		if ap != "" && !strings.Contains(p, ap) {
			p = p + ", distinct creature features: " + ap
		}
	} else {
		p = manjuPortraitPrompt(prompt, ap)
	}
	return p + manjuBeardEnforce(m)
}

// manjuPortraitSoloBgAnchor 定妆统一锚(2026-08-27 用户四反馈:小男孩定妆画出四个大人/
// 客人多出外套/背景要纯白):单人 + 纯白背景 + 服装严格按描述。所有角色图(主图/视图/
// Q版/群演卡)经 portraitPromptFor 统一附加。
const manjuPortraitSoloBgAnchor = "solo portrait, only this one single character in the frame, absolutely no other people, no additional figures or faces in the background, plain pure white background, clean white studio backdrop, no scenery, no environment, no background objects, wearing exactly the outfit described above, no additional outerwear, coat or jacket that is not described"

// manjuBgStrip 剥 image_prompt 里的场景背景词段(2026-08-27:素材卡常带 office background/
// evening alley background 等场景词,定妆照被画出环境;白底由 SoloBgAnchor 正向提供)。
var manjuBgStrip = regexp.MustCompile(`(?i),?\s*[a-z0-9\- ]{0,40}\s+background\b`)

// portraitPromptFor 定妆/视图/Q版提示词统一分档入口(2026-08-26 用户四问题修复):
// ①Q 版(prompt 含 chibi):不附加任何人像写实/插画锚——旧链路把插画风锚拼进 chibi prompt,
//   chibi 词被稀释渲染成写实人物形象;改附 chibi 专用锚并显式禁写实(3D 风格=3D chibi 手办)。
// ②次世代3D/BJD 风格(manjuStyleIs3D):不做 photorealistic→stylized 替换、不附插画风锚,
//   改附 manju3DPortraitAnchor——写实/3D 被干成动漫的根因即旧锚"stylized illustration/painterly"。
// ③主定妆与正面视图(frontFace=true):强制正面人脸锚(用户规则:人物角色提示词必须正面人脸)。
// ④非 3D 风格:维持原拟漫链路(manjuPortraitPromptFor)+正面人脸锚。
// 2026-08-27 六修:入口统一剥背景词段+单人白底服装严格锚;Q 版 chibi 分支补防日漫正向锚
// (老龟 Q 版出日漫脸——chibi 分支提前 return 没吃到拟漫锚的漏洞)。
func (ctx *manjuCtx) portraitPromptFor(prompt string, m map[string]any, frontFace bool) string {
	prompt = manjuBannedItemStrip(prompt)
	p := manjuBgStrip.ReplaceAllString(prompt, "")
	ap := manjuSanitizeAppearance(m, str(m["appearance"]))
	if strings.Contains(p, "chibi") {
		// Q 版:chibi 专用锚 + 显式禁写实人物(防写实形象混入 Q 版)。
		// 2026-08-27 措辞修正:裸 "BJD doll chibi aesthetic" 会唤起 BJD 素体(无衣服
		// 娃体)先验,男性潮玩先验更是敞开外套露胸——一律强调 fully dressed 完整着装。
		// 2026-08-28 画风定案(用户反馈「Q版变成动漫形象」):Q 版=3D 手办/潮玩收藏品
		// 形态,**不随项目风格档分流**——旧 else 分支(style=real 等)拼 "chibi
		// illustration style" 是动漫邀请词,标志特征角色 denoise 1.0(文本唯一画风源)
		// 时必出 2D 赛璐璐动漫;统一 3D 手办锚(与历史定案「呆萌手办感」一致)。
		if !strings.Contains(p, "NOT a realistic human") {
			// 2026-08-28 盔甲条件化(林小满Q版护甲实例,与 manjuQPrompt 基底同规):
			// armor 材质词只给真有盔甲的角色,无甲角色纯织物材质。
			mat := "realistic surface detail on fabric"
			if manjuHasArmor(m) {
				mat = "realistic surface detail on armor and fabric"
			}
			p = p + ", 3D rendered chibi collectible figure in complete outfit, cinematic movie-grade CGI rendering, dramatic studio lighting with soft rim light, physically based materials, " + mat + ", big glossy eyes"
			p = p + ", NOT a photograph of a real person, not real human proportions — but movie-grade cinematic realism in materials, lighting and surface detail"
		}
		// 防日漫(2026-08-27 老龟 Q 版日漫脸;2026-08-28 强化:不止脸,整个 2D 平面
		// 插画形态都要禁——赛璐璐上色/描边/平面感是 denoise 1.0 下的默认去向)
		p = p + ", NOT a Japanese anime style, no japanese-style face, no japanese anime eyes, NOT a 2D flat anime illustration, not cel-shaded, no flat colors, no outlines around the figure, Chinese semi-realistic CG character style, a physical 3D collectible toy figure not a drawing"
		if ap != "" && !strings.Contains(p, ap) {
			p = p + ", distinct unique face with: " + ap
		}
		return p + ", " + manjuPortraitSoloBgAnchor + manjuBeardEnforce(m)
	}
	// 物品管线(2026-08-29 架构重构·阿碧拟人实锤):物品角色在此分流到独立收尾
	// (manju_char_species.go),**不再进入下方人形链**——人形链的正面人脸锚
	// (front-facing portrait/symmetrical frontal face)与单人白底(solo portrait…
	// character…wearing exactly the outfit)对物品是强拟人邀请,正是
	// 「绿萝盆栽→女人脸+花盆头+藤蔓头发」的污染源。frontFace 参数对物品无效
	// (物品无正脸概念)。
	if manjuIsItem(m) {
		return manjuItemFinal(p, m)
	}
	if manjuStyleIs3D(ctx.style) {
		if !strings.Contains(p, "virtual digital human") {
			p = p + ", " + manju3DPortraitAnchor
		}
		if frontFace && !strings.Contains(p, "front-facing") {
			p = p + manjuPortraitFrontFace
		}
		if ap != "" && !strings.Contains(p, ap) {
			p = p + ", distinct unique face with: " + ap
		}
		return p + ", " + manjuPortraitSoloBgAnchor + manjuBeardEnforce(m)
	}
	p = manjuPortraitPromptFor(p, m)
	if frontFace && !strings.Contains(p, "front-facing") {
		p = p + manjuPortraitFrontFace
	}
	return p + ", " + manjuPortraitSoloBgAnchor
}

// manjuQStrip 清洗拼入 Q 版的 image_prompt 残段(2026-08-26:Q 版渲染出写实人物形象的另一根因)——
// 写实皮肤质感/电影镜头/质量词会把 chibi 拉向写实缩小版人物,一律剥除,只留身份特征词。
// 2026-08-27 三修:补动作残词(walking briskly 等主图动作描述混进 Q 版定妆,诱发动态
// 构图+姿态失控)与 ultra detailed 质量词。
// manjuBannedItemStrip 违禁物品词剥离(2026-09-01 用户规则:人物角色不能带烟、酒等
// 违规物品——角色卡 image_prompt/q_form 若有 cigarette/smoking/alcohol 等词,定妆/
// 视图/Q版 全部剥离(负面词是渲染期防御,正向剥离让素材侧不带烟酒);H3/云端审核
// 与画面合规双保险。
func manjuBannedItemStrip(s string) string {
	for _, w := range []string{
		"cigarette", "cigarettes", "smoking", "smokes", "smoked", "smoker", "cigar",
		"alcohol", "alcoholic", "beer", "beers", "wine", "wines", "liquor", "whiskey",
		"vodka", "brandy", "bottle of wine", "drinking alcohol", "drunk", "hangover",
		"烟", "香烟", "卷烟", "烟蒂", "酒", "酒杯", "啤酒", "白酒", "红酒", "醉",
	} {
		s = strings.ReplaceAll(s, w, "")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func manjuQStrip(s string) string {
	s = manjuBannedItemStrip(s)
	for _, w := range []string{
		"realistic skin texture with fine pores", "realistic skin texture", "fine pores",
		"photorealistic real-world environment", "photorealistic environment", "photorealistic",
		"realistic skin", "photorealistic", "Photorealistic",
		"cinematic film still", "cinematic lighting", "Cinematic film still",
		"85mm lens", "shallow depth of field", "bokeh background", "bokeh", "PBR materials",
		"8K ultra detailed", "8k ultra detailed", "8K", "8k", "soft facial lighting",
		"ultra detailed", "walking briskly", "walking quickly", "always walking",
		"walking down", "walking toward", "walking slowly", "walking ", "running ", "sitting ", "standing ",
		// 2026-09-01 光效词(苏晚萤 Q 版实锤):image_prompt 的 luminous eyes/gentle light
		// 等光效描述进 chibi prompt,img2img 0.93 高重绘下被放大成发光连体装/科幻光效,
		// Q 版画风与主图割裂——光效是光影描述,chibi 手办不需要,整段剥除
		"luminous dark almond eyes with a gentle light", "luminous eyes", "luminous", "with a gentle light",
		"glowing", "radiant", "ethereal glow", "soft glow", "gentle glow", "shimmering", "sparkling aura",
	} {
		s = strings.ReplaceAll(s, w, "")
		if len(w) > 1 {
			s = strings.ReplaceAll(s, strings.ToUpper(w[:1])+w[1:], "")
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// manjuViewAnchors full/side/detail 视角硬锚(前置,视图派生独占主导视角)。
// 2026-08-26 full 视图正面化:旧 "three-quarter or front view" 允许 3/4 侧(用户规则:
// 人物角色提示词必须正面人脸;全身立绘=正面全身,侧面归 side 视图)。
// 加 feet visible/standing on the ground 头到脚约束(用户反馈②:full 出半身照)。
// 2026-08-27 提为包级:视图生成与「复制提示词」接口(charImagePrompt)共用同一事实源。
var manjuViewAnchors = map[string]string{
	// 2026-08-27 用户反馈(小男孩全身图下半身裸露没穿裤子):全身视图锚此前只管
	// 「全身可见+视角」,下半身着装零约束——image_prompt 常只写上衣(儿童角色尤甚),
	// 下半身全靠模型自由发挥。full/side 补下半身硬锚:完整下装(裤/袍/裙随角色)、
	// 腿脚全程着装到鞋,禁光腿/缺下装。
	"full":   "FULL BODY view, standing full figure from head to toe, entire body visible including feet and shoes, standing on the ground, full figure framing with margin above head and below feet, front view facing the camera, wearing complete clothing on both upper and lower body, full lower-body garment (trousers, pants, robes or a skirt matching the character's outfit) fully covering the hips and legs down to the shoes, never bare legs, never missing trousers or skirt, single continuous figure, one character only, no duplication, no mirror image, no double exposure, no two figures stacked or side by side",
	"side":   "SIDE PROFILE view, face turned exactly 90 degrees to the side, strong profile silhouette, nose and chin clearly in profile, only one eye visible, head pointing sideways not toward the camera, full body seen from the side, full lower-body garment (trousers, robes or skirt matching the character's outfit) fully covering the hips and legs, never bare legs, single continuous figure, one character only, no duplication, no mirror image, no double exposure, no two figures stacked or side by side",
	"detail": "EXTREME CLOSE-UP detail shot, zoomed on the single most distinctive feature (ornament/pattern/hairstyle/scar), large detailed close-up composition, macro framing",
}

// manjuIdentityAnchor 身份锚:强约束多视图与主图同一个人——发色/发型/胡须/五官/服装逐项保留,
// 防"白发老者侧面变黑发"(2026-08-24 用户反馈)
const manjuIdentityAnchor = ", same character as the reference image (identical hair color and hairstyle, identical beard if present, identical facial features, identical costume colors and design)"

// manjuBeastViewAnchors 兽类视图锚(2026-08-28 用户反馈:灵宠小貔 full/Q 渲染出人形人物):
// 人形视图锚(FULL BODY standing figure/trousers/skirt 全是人衣语义)+image_prompt 里的
// 人互动描述("sitting on a young cultivator's shoulder")叠加,禁词压不住矛盾(矛盾=并集)
// → 兽类 full/side 必须用纯兽形锚:完整兽体占满画面、四足落地、无人无衣。
var manjuBeastViewAnchors = map[string]string{
	"full":   "FULL BODY view of the creature from head to tail, the complete beast body entirely filling the frame, front view facing the camera, purely animal creature form on four paws, absolutely no humans, no human body, no human figure, no human clothes, single continuous figure, one creature only, no duplication, no mirror image, no two figures stacked",
	"side":   "SIDE PROFILE view of the creature, the complete beast body seen from the side from head to tail, purely animal creature form on four paws, absolutely no humans, no human body, no human figure, no human clothes, single continuous figure, one creature only, no duplication, no mirror image, no double exposure, no two figures stacked",
	"detail": "EXTREME CLOSE-UP detail shot, zoomed on the creature's single most distinctive feature (markings/fur texture/eyes/horns), large detailed close-up composition, macro framing, no humans",
}

// manjuBeastIdentityAnchor 兽类身份锚:毛色/斑纹/物种跟随参考图(人形身份锚含 costume 服装词)
const manjuBeastIdentityAnchor = ", the exact same creature as the reference image (identical species, identical fur or scale colors and markings, identical proportions)"

// manjuBeastStrip 兽类 image_prompt 剥人元素(2026-08-28 实锤:小貔 image_prompt 含
// "sitting on a young cultivator's shoulder"——正向人物描述入独照提示词,任何禁词都压不住,
// 模型必然把那个人画出来;写实人像风格子句对兽形也是污染)。逗号切分剥含人互动/人形
// 措辞的子句,再替换残余 character 措辞为 creature。
func manjuBeastStrip(s string) string {
	humanKw := regexp.MustCompile(`(?i)\b(?:shoulder|shoulders|in someone'?s? arms|held by|carried by|perched on|handler|owner|companion|cultivator|disciple|man|woman|boy|girl|person|people)\b|人肩|肩上|怀中|主人|弟子`)
	parts := regexp.MustCompile(`[,.;，。；]`).Split(s, -1)
	keep := []string{}
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" || humanKw.MatchString(p) {
			continue
		}
		keep = append(keep, p)
	}
	out := strings.Join(keep, ", ")
	out = regexp.MustCompile(`(?i)East Asian/Chinese character|East Asian character|Chinese character`).ReplaceAllString(out, "mythical creature")
	out = regexp.MustCompile(`(?i)\bcharacter\b`).ReplaceAllString(out, "creature")
	// 2026-08-28(猫·二两 full 全身照画出人/人偶化):素材卡按 style 统一拼的 3D 人形锚
	// ——virtual digital human / BJD doll / porcelain-smooth skin——对兽形是拟人邀请,
	// 剥净人形措辞、皮肤质感换毛发质感。
	out = regexp.MustCompile(`(?i)next-generation 3D CGI render of a virtual digital human (?:creature|character)`).ReplaceAllString(out, "next-generation 3D CGI render")
	out = regexp.MustCompile(`(?i)virtual digital human (?:creature|character)|digital human (?:creature|character)`).ReplaceAllString(out, "")
	out = regexp.MustCompile(`(?i)fully dressed BJD doll aesthetic|BJD doll aesthetic,?`).ReplaceAllString(out, "")
	out = regexp.MustCompile(`(?i)porcelain-smooth skin with fine subsurface scattering`).ReplaceAllString(out, "smooth detailed fur and skin texture")
	return strings.TrimSpace(out)
}

// manjuMinorGuard 未成年人着装护栏(2026-08-27 用户反馈:小男孩全身图下半身裸露没穿裤子):
// 儿童角色是裸露风险最高的人群——image_prompt 常只写上衣(如 "7-year-old boy, striped
// T-shirt"),下半身零描述时模型按儿童夏日先验自由发挥。age/appearance/image_prompt/
// views 命中未成年人关键词即追加硬护栏:全身完整着装、仅露脸和手、家庭向设计。
// 成年角色不加(避免"child"措辞污染成年人提示词);兽类不加(兽形无人类着装语义)。
func manjuMinorGuard(m map[string]any) string {
	if manjuIsBeast(m) {
		return ""
	}
	vs, _ := m["views"].(map[string]any)
	src := strings.Join([]string{str(m["age"]), str(m["appearance"]), str(m["image_prompt"]), str(vs["full"]), str(vs["q"])}, " ")
	minor := regexp.MustCompile(`(?i)\b(?:boy|girl|child|kid|toddler)\b|童|孩|少年|少女|幼`).MatchString(src)
	if !minor {
		// 数字年龄仅 <18 算未成年("26-year-old woman"/"26岁" 不得误伤)
		for _, mm := range regexp.MustCompile(`(?i)(\d+)\s*(?:years?[- ]old|岁)`).FindAllStringSubmatch(src, -1) {
			if n, err := strconv.Atoi(mm[1]); err == nil && n > 0 && n < 18 {
				minor = true
				break
			}
		}
	}
	if !minor {
		return ""
	}
	return ", CHILD DRESS CODE: this is an underage minor character, fully clothed in a complete age-appropriate outfit covering the torso, shoulders, arms, hips, legs and ankles, modest family-friendly design, absolutely no nudity anywhere on the body, no bare legs, no bare torso, no exposed skin except face and hands"
}

// charFullRef 全身照引用(拷贝到 ComfyUI input,返回 input 相对文件名;无全身照返回空)。
// Q 版 img2img 的 init 用——chibi 是全身形态,主图是方形正脸特写,拿主图当 init 拉成
// 竖版再 0.93 重绘=畸形/身份漂移双根源(2026-08-28 用户实测实锤)。
func (ctx *manjuCtx) charFullRef(cid string) string {
	fp := filepath.Join(ctx.assetsDir, "characters", sanitizeFileName(cid)+"_full.png")
	if !fileExists(fp) {
		return ""
	}
	refName := "dir_char_full_" + sanitizeFileName(cid) + ".png"
	if copyFile(fp, filepath.Join(ctx.comfyInput, refName)) != nil {
		return ""
	}
	return refName
}

// manjuViewPromptBuild 全身视图提示词统一构建(生成链与角色管理「复制提示词」共用同一函数,
// 双侧口径恒一致):视角硬锚前置 + strip 清洗 + 身份锚后置 + 未成年人着装护栏。
// 2026-08-28 兽类分流(用户反馈灵宠 full 出人形):兽类用兽形锚+兽类身份锚+剥人元素,
// 人形着装/未成年人护栏不参与。
func manjuViewPromptBuild(p string, view string, m map[string]any) string {
	if manjuIsBeast(m) {
		return manjuBeastViewAnchors[view] + ", " + manjuBeastStrip(manjuViewStrip(p)) + manjuBeastIdentityAnchor
	}
	// 物品视图锚(2026-08-29 架构重构):人形视图锚(FULL BODY standing figure/
	// trousers/skirt 穿衣站立语义)+人形身份锚(identical hair/beard/facial features)
	// 对物品全是拟人邀请——物品用物品视角锚+物品身份锚(同一件物品的形态/容器/标记)
	if manjuIsItem(m) {
		return manjuItemViewAnchors[view] + ", " + manjuViewStrip(p) + manjuItemIdentityAnchor
	}
	// 无脸/剪影类角色(影子/雾/剪影形态,2026-09-01 阿影 side 上下双体实锤):
	// 人形 side 锚的 face/nose/chin/one eye 语义对无脸角色是乱画邀请(Krea2 img2img
	// 依锚尝试画脸与五官,黑雾人形被拆成上下两个体)。用剪影侧锚:纯轮廓单一体。
	if view == "side" && manjuFacelessChar(m) {
		return "SILHOUETTE PROFILE view, the single continuous dark figure seen from the side, pure outline and shadow mass with no facial features, no face, no eyes, no nose, no mouth, no limbs separated from the body, one continuous figure only, no duplication, no mirror image, no two figures stacked or split" + ", " + manjuViewStrip(p) + manjuIdentityAnchor + manjuMinorGuard(m)
	}
	return manjuViewAnchors[view] + ", " + manjuViewStrip(p) + manjuIdentityAnchor + manjuMinorGuard(m)
}

// manjuFacelessChar 无脸/剪影类角色判定(2026-09-01):非人且形象描述为影子/雾/
// 剪影/无五官——侧面视图必须用剪影锚,人形面部锚会诱导模型乱画(阿影 side 上下双体)
func manjuFacelessChar(m map[string]any) bool {
	if m == nil {
		return false
	}
	species := str(m["species"])
	if species != "" && species != "人" {
		low := strings.ToLower(str(m["image_prompt"]) + " " + species)
		return strings.Contains(low, "shadow") || strings.Contains(low, "mist") ||
			strings.Contains(low, "silhouette") || strings.Contains(low, "无脸") ||
			strings.Contains(low, "no facial") || strings.Contains(low, "影子")
	}
	return false
}

// manjuCharImagePrompt 角色<视图>形态图的最终生图提示词(2026-08-27 角色管理「复制提示词」用;
// 与生成链同一套构建函数,复制到的即这张图生成时的真实口径):
// kind=gacha=抽卡候选(纯文生图探索,无视角硬锚/身份锚);其余=正式资产口径
// (full/side/detail=视角硬锚+strip+身份锚,Q=chibi 构建);正面主图两口径一致
// (image_prompt+性别锚,portraitWF 正面人脸锚)。返回 最终正向 / 负向 提示词。
func manjuCharImagePrompt(ctx *manjuCtx, m map[string]any, char, view, kind string) (string, string) {
	var p string
	frontFace := true
	if view == "q" {
		p = manjuQPrompt(m)
		frontFace = false
	} else if kind != "gacha" && (view == "full" || view == "side" || view == "detail") {
		p = manjuViewPromptBuild(charViewPromptFor(ctx, char, view), view, m)
		frontFace = false
	} else {
		p = charViewPromptFor(ctx, char, view)
	}
	return ctx.portraitPromptFor(p, m, frontFace), ctx.charNegPrompt(m)
}

// manjuViewStrip 视图派生前清洗 image_prompt(2026-08-26 用户反馈②:full/side/detail 全渲染成正面照)——
// 素材/主定妆的 Front-facing portrait 前缀(正面人脸铁律产物)会把所有视图拉回正面半身特写构图;
// 视图的视角由 viewAnchor 独家主导,这里剥掉一切正面/特写措辞,只留身份与外观特征。
func manjuViewStrip(s string) string {
	s = manjuBannedItemStrip(s)
	for _, w := range []string{
		"Front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, no profile angle",
		"front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, no profile angle",
		"Front-facing portrait", "front-facing portrait", "Front facing portrait",
		"head facing the camera directly", "symmetrical frontal face", "both eyes evenly visible",
		"no profile angle", "facing the camera directly",
	} {
		s = strings.ReplaceAll(s, w, "")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// manjuHairPhrases 提取角色卡里所有头发/毛色描述短语(2026-08-27 用户反馈:叶澜黑白
// 挑染发 Q 版变纯黑——Q 版 denoise 0.93 高重绘下发色只靠 initImage,Z-Image 会把
// 双色挑染"平均"成单色;文本锚必须显式点名具体发色)。取 image_prompt/appearance/
// views.q 三个来源,按逗号/句号/分号切分,命中 hair/发/fur/wolf-cut/braids 等关键词的
// 子句全收(去重保序);image_prompt 优先(LLM 逐字提炼,信息最全)。
func manjuHairPhrases(m map[string]any) []string {
	vs, _ := m["views"].(map[string]any)
	sources := []string{str(m["image_prompt"]), str(m["appearance"]), str(vs["q"])}
	kw := regexp.MustCompile(`(?i)hair|wolf-cut|ponytail|braids?|bangs|locks|mane|highlight|streak|fur|pelt|feathers?|发|毛|鬃`)
	out := []string{}
	seen := map[string]bool{}
	for _, src := range sources {
		for _, part := range regexp.MustCompile(`[,.;，。；]`).Split(src, -1) {
			p := strings.TrimSpace(part)
			if p == "" || len(p) > 90 || seen[p] {
				continue
			}
			if kw.MatchString(p) {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// manjuHairAnchor Q 版发色锁(2026-08-27):显式点名发色 + 强制与参考图一致,
// 多色/挑染发(黑白、银白挑染)必须保持双色——这是高重绘下唯一可靠的文本防线。
func manjuHairAnchor(m map[string]any) string {
	phrases := manjuHairPhrases(m)
	if len(phrases) == 0 {
		return ""
	}
	// color_palette 第 5 位=发色(契约见方案模板),一并点名
	if cp := str(m["color_palette"]); cp != "" {
		hexes := regexp.MustCompile(`#[0-9A-Fa-f]{6}`).FindAllString(cp, -1)
		if len(hexes) >= 5 {
			phrases = append(phrases, "hair color from palette "+hexes[4])
		}
	}
	// 2026-09-01 强化(黄毛跟班/群演发型不匹配实锤):短语无颜色词时旧锁是空锁
	// (「keeps EXACTLY this hair color — short hair」无颜色可锁,模型自由发挥)。
	// 颜色+形状双重点名:有颜色词锁颜色,无颜色词也锁发型形状与主图一致。
	return "HAIR LOCK: the chibi keeps EXACTLY the same hairstyle and hair color as the reference portrait — " + strings.Join(phrases, "; ") + " — the hairstyle shape (length, parting, bun/braid/ponytail/bangs) must be identical to the reference; multi-tone or streaked hair (e.g. black-and-white two-tone) must stay multi-tone, never flatten into a single color"
}

// manjuFurAnchor 兽形毛色显式锁(2026-08-28 猫·二两案:主图橘白,Q版被画成黑灰狸花——
// img2img 0.93 高重绘身份靠文本,兽形Q版分支删 image_prompt 整段后橘白色词零出现,
// HAIR LOCK 只抓到无色名的 "dusty fur" 子句,模型按猫的默认先验随机出狸花)。
// 从主体子句(第一个逗号前,色词密集段)提取英文色名 + 中文记忆点色词映射,显式点名。
func manjuFurAnchor(m map[string]any) string {
	ip := str(m["image_prompt"])
	head := ip
	if i := strings.IndexAny(ip, ",;，；"); i > 0 {
		head = ip[:i]
	}
	low := strings.ToLower(head + " " + ip)
	// 英文色名(2026-08-29 阿影实锤:固定词表序 white 恒排 black 前,Q版「black blob+
	// white moon-crescent」被锁成「white and black fur」=白主色黑白反转串色;
	// 改按提示词中首次出现位置排序,主体色(前段)永远排在小色块(后段)之前)
	enKw := []string{"orange", "ginger", "golden", "yellow", "cream", "white", "snow-white", "black", "gray", "grey", "brown", "silver", "calico", "tabby", "tortoiseshell", "tuxedo", "tricolor", "bicolor", "blue-gray"}
	type colorHit struct {
		k   string
		pos int
	}
	var hits []colorHit
	for _, k := range enKw {
		if p := strings.Index(low, k); p >= 0 {
			hits = append(hits, colorHit{k, p})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	seen := map[string]bool{}
	var cols []string
	for _, h := range hits {
		if !seen[h.k] {
			seen[h.k] = true
			cols = append(cols, h.k)
		}
	}
	// 中文记忆点色词映射(橘白猫/三花/奶牛猫等)
	zhMap := [][2]string{{"雪白", "snow-white"}, {"橘白", "orange and white"}, {"橘", "orange"}, {"金黄", "golden"}, {"黄", "golden yellow"}, {"黑白", "black and white"}, {"奶牛", "black and white"}, {"三花", "calico"}, {"狸花", "brown tabby"}, {"白", "white"}, {"黑", "black"}, {"灰", "gray"}}
	for _, z := range zhMap {
		if !seen[z[1]] && strings.Contains(str(m["appearance"]), z[0]) {
			seen[z[1]] = true
			cols = append(cols, z[1])
		}
	}
	if len(cols) == 0 {
		return ""
	}
	return "FUR COLOR LOCK: " + strings.Join(cols, " and ") + " fur coloring, exactly the same coat colors and markings as the reference image, never recolored"
}

// manjuLooseClean 敞开感词清洗(2026-08-27 二修:柳含烟/魏鹤年/魏琰 宽袍 Q 版敞胸):
// 宽松/敞开/未扣的服饰描述复述进 chibi prompt 会强化潮玩素体敞袍先验(Z-Image 高重绘下
// "宽松官袍"= 敞袍),着装锁提取子句与 Q 版 img 残段一律剥除。
var manjuLooseClean = regexp.MustCompile(`(?i)\b(?:loose|flowing|unbuttoned|unzipped|open|casually worn)\s*`)

// manjuOutfitAnchor Q 版着装锁(2026-08-27 用户反馈:男性 Q 版外套敞开袒胸露乳——
// 潮玩/BJD 男娃先验是敞开外套露胸造型,末尾防裸词 "no shirtless" 语义盖不住「敞开
// 外套露胸」且位置太弱)。从 costume/image_prompt 提取服装子句显式前置,强制与定妆照
// 同一套完整着装、衣袍交领闭合、躯干全程覆盖。
// 当日二修(2026-08-27 用户反馈:柳含烟/魏鹤年/魏琮 宽袍/儒袍/官袍 Q 版仍敞胸):
// ①服装子句提取词扩到 袍/裙/衫/裳/gown/cloak/garment 全服饰;
// ②提取子句剥敞开感词(loose/open/unbuttoned 等——"宽松官袍"被复述进 prompt 反而
//   强化敞袍先验,Z-Image 高重绘下宽松描述=敞袍);
// ③负面句从「外套拉上扣好」扩到「宽袍交领闭合/覆盖锁骨与胸口/永不敞开袍服」——
//   古风袍服没有拉链扣子,原 "zipped and buttoned" 措辞对袍服无效是漏胸主因。
func manjuOutfitAnchor(m map[string]any) string {
	vs, _ := m["views"].(map[string]any)
	sources := []string{str(m["costume"]), str(m["image_prompt"]), str(vs["q"])}
	kw := regexp.MustCompile(`(?i)wear|suit|jacket|coat|outfit|hoodie|dress|robe|gown|cloak|garment|clothing|clothes|shirt|uniform|sweater|vest|garb|armor|armour|breastplate|pauldron|gauntlet|helmet|chainmail|铠甲|盔甲|甲胄|服装|穿着|袍|衣|裙|衫|裳`)
	out := []string{}
	seen := map[string]bool{}
	for _, src := range sources {
		for _, part := range regexp.MustCompile(`[,.;，。；]`).Split(src, -1) {
			p := strings.TrimSpace(part)
			if p == "" || len(p) > 90 || seen[p] {
				continue
			}
			if kw.MatchString(p) {
				p = strings.TrimSpace(manjuLooseClean.ReplaceAllString(p, ""))
				if p == "" {
					continue
				}
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return "OUTFIT LOCK: the chibi wears exactly the same complete outfit as the reference portrait" +
		func() string {
			if len(out) == 0 {
				return ""
			}
			return " — " + strings.Join(out, "; ")
		}() +
		" — torso and chest always fully covered by clothing, collar sits high and closed with overlapping lapels fully covering the collarbone and chest, robes gowns coats and jackets always fully closed at the chest, never an open robe, open coat or open jacket showing bare chest or cleavage"
}

// manjuFeatureKw 标志性视觉特征关键词(2026-08-28 用户实测:韩天枢Q版丢眼镜、屠夫Q版丢
// 单眼扫描仪眼罩+猩红液压钳——Q版 denoise 0.93 高重绘下小配件最容易被模型"平均"掉,
// 发色有 HAIR LOCK、服装有 OUTFIT LOCK,唯独面部/装备标志特征没有锁)。
var manjuFeatureKw = regexp.MustCompile(`(?i)glasses|visor|eyepatch|monocle|goggles|scanner|scar\b|prosthetic|pincer|claw|hook|mask|tattoo|holographic|cybernetic|mechanical (?:eye|arm|leg)|tactical (?:eye|light)|glowing (?:red|blue|cyan|gold) (?:eye|lens|light)|headband|hairpin|ear ?rings?|halo|horns?|third eye|data-chain|data chain`)

// manjuFeaturePhrases 提取角色卡里的标志性视觉特征子句(来源与 manjuHairPhrases 同法:
// image_prompt 优先,按逗号/分号切分,命中 manjuFeatureKw 的子句全收,去重保序)。
func manjuFeaturePhrases(m map[string]any) []string {
	vs, _ := m["views"].(map[string]any)
	sources := []string{str(m["image_prompt"]), str(m["appearance"]), str(vs["q"])}
	out := []string{}
	seen := map[string]bool{}
	for _, src := range sources {
		for _, part := range regexp.MustCompile(`[,.;，。；]`).Split(src, -1) {
			p := strings.TrimSpace(part)
			if p == "" || len(p) > 90 || seen[p] || !manjuFeatureKw.MatchString(p) {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// manjuFeatureAnchor Q版标志性特征锁(2026-08-28):把角色最具辨识度的面部/装备特征
// 显式点名并禁止省略——Q 版"缩头身"不等于"缩特征",眼镜/眼罩/机械臂/发光眼必须保留。
func manjuFeatureAnchor(m map[string]any) string {
	ps := manjuFeaturePhrases(m)
	if len(ps) == 0 {
		return ""
	}
	return "SIGNATURE FEATURE LOCK: the chibi keeps ALL of the character's signature facial and gear features exactly as in the reference portrait — " +
		strings.Join(ps, "; ") +
		" — every one of these signature features must be clearly visible on the chibi, never omitted, never simplified away"
}

// manjuIsNonPhysical 非实体角色(2026-08-28 用户实测:管理员=纯数据光生命,Q版被套上
// 实体衣服做成普通娃娃,与写实全息体差距巨大):image_prompt/appearance 声明无实体身体
// (holographic entity / pure data-light / no solid body / ghost / spirit)的角色,
// Q 版不套人类着装锁,走发光数据精灵形态。
func manjuIsNonPhysical(m map[string]any) bool {
	src := str(m["image_prompt"]) + " " + str(m["appearance"])
	return regexp.MustCompile(`(?i)no solid body|holographic entity|pure data-light|data-light|data spirit|wisp|ghostly|immaterial|ethereal (?:being|entity|spirit)`).MatchString(src)
}

// manjuQPrompt 构建 Q 版提示词(2026-08-25 用户规则;2026-08-26 修正:Q版=定妆照同一
// 角色的缩小版Q萌形象——手办/吉祥物感,不是小孩):
// ①妖兽/灵宠/神兽 → Q版=该妖兽本体萌化小兽形,禁止人形/人类宝宝;
// ②人类面容随角色本人(脸型/眼型/发型/年龄感/胡须),禁止儿童化/宝宝脸——
//   旧措辞 "a cute girl/boy" + chibi 上下文被模型画成小孩(用户反馈),改为
//   「同一角色缩小版」+ 年龄感保留 + 显式禁小孩禁词;
// ③Q版渲染基于正面定妆照 img2img(与 full/side/detail 一致,禁止形象大变)。
// manjuQOutfitCompact Q 版紧凑着装锁(2026-08-28 紧凑化):与 manjuOutfitAnchor 同源提取,
// 子句上限 3 + 短覆盖句——着装语义不减、字符减半(cfg=1.0 下提示词长度=遵循度,长=稀释)。
func manjuQOutfitCompact(m map[string]any) string {
	vs, _ := m["views"].(map[string]any)
	sources := []string{str(m["costume"]), str(m["image_prompt"]), str(vs["q"])}
	kw := regexp.MustCompile(`(?i)wear|suit|jacket|coat|outfit|hoodie|dress|robe|gown|cloak|garment|clothing|clothes|shirt|uniform|sweater|vest|garb|armor|armour|breastplate|pauldron|gauntlet|helmet|chainmail|铠甲|盔甲|甲胄|服装|穿着|袍|衣|裙|衫|裳`)
	out := []string{}
	seen := map[string]bool{}
	for _, src := range sources {
		for _, part := range regexp.MustCompile(`[,.;，。；]`).Split(src, -1) {
			p := strings.TrimSpace(part)
			if p == "" || len(p) > 90 || seen[p] || len(out) >= 5 {
				continue
			}
			if kw.MatchString(p) {
				p = strings.TrimSpace(manjuLooseClean.ReplaceAllString(p, ""))
				if p == "" {
					continue
				}
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	s := "OUTFIT LOCK: the chibi wears exactly the same complete outfit as the reference portrait"
	if len(out) > 0 {
		s += " — " + strings.Join(out, "; ")
	}
	return s + " — chest and torso always fully covered by clothing, robes and coats closed with overlapping lapels covering the collarbone"
}

// manjuQFormPrompt Q 版形态段(2026-08-28 两段式第①段,纯文生图):只管形态+画风+标志
// 特征(特征=构成的一部分,眼镜/机械钳直接进本体),不掺身份细节——短提示词=强遵循,
// 锁定两头身 3D 手办形态。
func manjuQFormPrompt(m map[string]any) string {
	if manjuIsBeast(m) {
		p := "3D rendered cute chibi collectible toy figure, small adorable round chibi animal, oversized head with big sparkling cute eyes, short stubby legs and tiny round paws, soft plush-like fluffy body, standing on a clean plain white background"
		if low := strings.ToLower(str(m["image_prompt"]) + " " + str(m["appearance"])); strings.Contains(low, "robot") || strings.Contains(low, "机械") {
			p = "3D rendered cute chibi collectible toy figure, small adorable round chibi robot animal with a smooth glossy rounded metallic body, oversized head with big sparkling cute lens eyes, short stubby legs and tiny round paws, clean seamless toy-like shell, standing on a clean plain white background"
		}
		if fa := manjuFeatureAnchor(m); fa != "" {
			p += ", " + fa
		}
		return p + ", a physical 3D collectible toy figure not a drawing, NOT a 2D flat anime illustration, not cel-shaded, NOT a human, no human face"
	}
	// 标志特征改写构成(眼镜/机械臂类:特征优先于呆萌模板,与 manjuQPrompt 同款检测)
	eyeCls, handCls := "big sparkling glossy eyes", "tiny stubby arms and legs, small round hands"
	if ps := manjuFeaturePhrases(m); len(ps) > 0 {
		low := strings.ToLower(strings.Join(ps, " "))
		if strings.Contains(low, "pincer") || strings.Contains(low, "claw") || strings.Contains(low, "hook") ||
			strings.Contains(low, "mechanical arm") || strings.Contains(low, "prosthetic arm") ||
			strings.Contains(low, "cybernetic arm") || strings.Contains(low, "mechanical hands") ||
			strings.Contains(low, "for arms") {
			handCls = "short stubby arms ending in the character's signature mechanical parts, short legs"
		}
		// 2026-08-28 三修:与 manjuQPrompt 同款精确化(manjuEyeClsFor),不混写 glasses/visor
		eyeCls = manjuEyeClsFor(low)
	}
	p := "3D rendered cute chibi collectible toy figure, exactly 2-head-tall super-deformed chibi proportions, the oversized round head takes up half of the total body height, " + eyeCls + ", " + handCls + ", soft round face with a small cute mouth, bright cheerful adorable expression with a happy smile and rosy cheeks, compact mini body, adorable huggable vinyl toy with soft matte finish"
	if manjuIsFemale(m) {
		p += ", clearly a cute chibi girl figure, flat chest, modest full outfit"
	} else if str(m["gender"]) == "男" {
		p += ", clearly a cute chibi boy figure"
	}
	if manjuIsOldMale(m) {
		p += ", elderly wrinkled old man face"
	}
	if fa := manjuFeatureAnchor(m); fa != "" {
		p += ", " + fa
	}
	return p + ", standing full figure from head to toe with margin, plain white background, NOT a realistic human, no realistic skin texture, no photorealism, a physical 3D collectible toy figure not a drawing, NOT a 2D flat anime illustration, not cel-shaded, no outlines"
}

// manjuStripCJK 剔除中文字符(图像模型不读中文,中文段=废 token 还挤占提示词权重;
// appearance 等中文记忆点只在 LLM/面容锚链路消费,进图像提示词前必须过滤)
func manjuStripCJK(s string) string {
	out := regexp.MustCompile(`[\p{Han}\p{P}]+`).ReplaceAllString(s, " ")
	out = regexp.MustCompile(`[，。；：、""''（）【】「」]+`).ReplaceAllString(out, " ")
	return strings.Join(strings.Fields(out), " ")
}

// manjuQPropClauses Q 版标志道具子句提取(2026-08-28 用户反馈「Q版缺正面照里的角色特征」:
// 佩剑/折扇/剑穗/玉佩等签名道具不含服装关键词,着装锁提取词表不收→道具从未进提示词;
// 姜璃的腰间长剑/冰蓝剑穗实测丢失)。从 image_prompt 提取含道具词的英文子句,上限 2。
func manjuQPropClauses(m map[string]any) []string {
	kw := regexp.MustCompile(`(?i)sword|blade|saber|fan|tassel|pendant|jade |scarf|spear|staff|bow|guqin|abacus|beads|gourd|whip|dagger|halberd|parasol|lantern|brush|scroll`)
	out := []string{}
	seen := map[string]bool{}
	for _, part := range regexp.MustCompile(`[,.;]`).Split(str(m["image_prompt"]), -1) {
		q := strings.TrimSpace(part)
		if q == "" || len(q) > 80 || seen[q] || len(out) >= 2 {
			continue
		}
		if kw.MatchString(q) {
			seen[q] = true
			out = append(out, q)
		}
	}
	return out
}

// manjuQIdentityPrompt Q 版身份段(2026-08-28 两段式第②段,img2img 0.5):保持输入图的
// Q 版形态不变(低重绘=形态保真),只注入身份——发色/服装/性别/面容/同人锚,全走文本锁。
func manjuQIdentityPrompt(m map[string]any) string {
	p := "keep exactly the same 3D chibi collectible toy figure as the input image (same 2-head-tall proportions, same pose, same toy style, same cheerful adorable expression, same plain white background), only apply this character's identity"
	if manjuIsBeast(m) {
		p += ", the same creature species, fur colors and markings as this character"
		if anchor := manjuHairAnchor(m); anchor != "" {
			p += ", " + anchor
		}
		if ap := manjuStripCJK(str(m["appearance"])); ap != "" {
			p += ", " + ap
		}
		return p + ", NOT a human"
	}
	if anchor := manjuHairAnchor(m); anchor != "" {
		p += ", " + anchor
	}
	p += ", " + manjuQOutfitCompact(m)
	if manjuIsFemale(m) {
		p += ", a cute miniature chibi of the same female character, clearly feminine face, modest outfit fully covering the chest and collarbone"
	} else if str(m["gender"]) == "男" {
		p += ", a cute miniature chibi of the same male character, clearly masculine young man's face, NOT a girl"
	}
	if ap := manjuStripCJK(str(m["appearance"])); ap != "" {
		p += ", " + ap
	}
	// 标志道具显式点名(佩剑/折扇/剑穗等;着装锁词表不收道具,不点名=丢失)
	if props := manjuQPropClauses(m); len(props) > 0 {
		p += ", carrying the character's signature items exactly as described: " + strings.Join(props, "; ")
	}
	p += ", same character as the reference portrait (identical hairstyle, hair color, outfit colors and design)"
	p += ", keeping the character's original age, NOT aged down, no baby face, body build strictly follows the original character, NOT chubby"
	p += ", fully clothed head to toe, no nudity, no exposed skin except face and hands"
	return p + manjuMinorGuard(m) + manjuBeardEnforce(m)
}

// manjuEyeClsFor 按角色卡真实眼部特征精确构建 Q 版眼睛构成(2026-08-28 老纪赛博护目镜
// 实例:旧措辞 "glasses/visor" 混写给模型两个选项,Krea-2 对 visor 的科幻先验更强,
// 挂脖老花镜被画成金属护目镜)。检测到什么写什么,绝不混写;挂脖镜(hanging+cord/neck)
// 保持挂脖形态、不架上眼睛。low = 特征短语小写拼接串。
func manjuEyeClsFor(low string) string {
	has := func(kws ...string) bool {
		for _, k := range kws {
			if strings.Contains(low, k) {
				return true
			}
		}
		return false
	}
	if has("glasses") && has("hanging") && (has("cord") || has("neck")) {
		return "big sparkling eyes, the character's signature glasses hanging on a cord around the neck, not worn over the eyes"
	}
	switch {
	case has("visor"):
		return "big sparkling eyes behind the character's signature visor, exactly the same visor as the reference portrait"
	case has("goggles"):
		return "big sparkling eyes behind the character's signature goggles, exactly the same goggles as the reference portrait"
	case has("eyepatch"):
		return "big sparkling eye visible on the uncovered side, the character's signature eyepatch kept exactly as the reference portrait"
	case has("monocle"):
		return "big sparkling eyes with the character's signature monocle, exactly the same monocle as the reference portrait"
	case has("scanner"):
		return "big sparkling eyes with the character's signature eye scanner device kept exactly as the reference portrait"
	case has("glasses"):
		return "big sparkling eyes behind the character's eyeglasses, wearing exactly the same glasses as the reference portrait"
	}
	return "big sparkling glossy eyes"
}

// manjuHasArmor 角色卡是否真有盔甲元素(2026-08-28 林小满Q版左肩金属护甲实例):Q 版
// 手办质感模板的 armor 材质词对无甲角色是污染——模型凭空给现代人画护甲;armor 材质
// 描述只对卡面真带盔甲的角色注入。词表与着装词表正则(manjuClothKW)盔甲段对齐,
// 不含 helmet(现代安全帽也是 helmet,误判会凭空画甲)。
func manjuHasArmor(m map[string]any) bool {
	vs, _ := m["views"].(map[string]any)
	blob := strings.ToLower(str(m["image_prompt"]) + " " + str(m["appearance"]) + " " + str(vs["q"]))
	for _, kw := range []string{"armor", "armour", "breastplate", "pauldron", "gauntlet", "chainmail", "铠甲", "盔甲", "甲胄"} {
		if strings.Contains(blob, kw) {
			return true
		}
	}
	return false
}

func manjuQPrompt(m map[string]any) string {
	vs, _ := m["views"].(map[string]any)
	base := str(vs["q"])
	// 2026-08-29 阿影实锤:角色卡的【Q版·内心戏专用提示词】独立段(解析为 q_form)
	// 是 Q 版形象的权威定义,优先于兽形 chibi 模板拼接(黑团子主色/蓝点眼/月牙毛
	// 的作者措辞,模板拼接的毛色锁会把手白月牙的 white 提到主体色位=黑白反转串色)。
	if base == "" {
		base = str(m["q_form"])
	}
	// 2026-08-26:Q 版拼入 image_prompt 前先 manjuQStrip 剥写实皮肤/镜头词——
	// 旧版整段拼接把 "realistic skin texture/85mm lens/cinematic" 带进 chibi prompt,
	// chibi 被稀释渲染成写实人物形象(用户反馈"Q版形象里还有写实人物")。
	img := manjuQStrip(manjuStripFullBody(str(m["image_prompt"])))
	// 兽类(2026-08-28 小貔 Q 版出人实锤):image_prompt 里的人互动子句
	// ("sitting on a young cultivator's shoulder")必须剥掉,禁词压不住正向人物描述
	if manjuIsBeast(m) {
		img = manjuBeastStrip(img)
	}
	// 物品类(2026-08-29 用户硬性规则):Q 版=物品本体萌化(可带呆萌表情,禁人形全身)
	if manjuIsItem(m) {
		img = manjuBeastStrip(img)
	}
	// 2026-08-27:主图 3D 锚的裸 "BJD doll aesthetic" 残词会唤起无衣素体先验,替换为着装版
	img = strings.ReplaceAll(img, "BJD doll aesthetic", "fully dressed BJD doll aesthetic")
	// 2026-08-27 二修:image_prompt 残段的宽松/敞开感词(loose minister robes 等)复述进
	// chibi prompt 会强化敞袍先验,与着装锁提取子句同款清洗
	img = manjuLooseClean.ReplaceAllString(img, "")
	ap := str(m["appearance"]); _ = ap
	if manjuIsBeast(m) {
		// 2026-08-28 修(用户实测老猫Q版"机械零件拼凑感/结构混乱"):①默认 plush 毛绒构成与
		// 机械兽物种打架——特征含 robotic/mechanical/metallic 时构成换光滑金属版;
		// ②删 img 整段拼接(image_prompt 科技感残段稀释萌宠指令,特征锁已覆盖,与人形
		// 分支同款处理);③"same fur and scale color" 对机械兽改为通用 body color。
		feat := manjuFeaturePhrases(m)
		featLow := strings.ToLower(strings.Join(feat, " "))
		isRobotic := strings.Contains(featLow, "robotic") || strings.Contains(featLow, "mechanical") ||
			strings.Contains(featLow, "metallic") || strings.Contains(featLow, "cyber")
		if base == "" {
			// 2026-08-27 五修:兽形 chibi 同款呆萌构成点名(大头圆眼短腿小爪)
			base = "3D rendered cute chibi collectible toy figure, small adorable round chibi animal, oversized head with big sparkling cute eyes, short stubby legs and tiny round paws, soft plush-like fluffy body"
			if isRobotic {
				base = "3D rendered cute chibi collectible toy figure, small adorable round chibi robot animal with a smooth glossy rounded metallic body, oversized head with big sparkling cute lens eyes, short stubby legs and tiny round paws, clean seamless toy-like shell, cute friendly creature design"
			}
		}
		p := base + ", chibi cute version of the same beast creature as in the reference image, same species, same body color and markings, small adorable chibi beast form, cute rounded chibi proportions"
		if anchor := manjuHairAnchor(m); anchor != "" {
			p = p + ", " + anchor
		}
		// 2026-08-28 毛色显式锁(猫·二两:主图橘白,Q版随机出黑灰狸花):高重绘下
		// "same body color" 引用式不够,色名必须显式进文本(与人形 HAIR LOCK 同级)。
		if fur := manjuFurAnchor(m); fur != "" {
			p = p + ", " + fur
		}
		// 2026-08-30 兽龄锚(三物种分流:人形皱纹措辞禁入兽脸):老年兽=口鼻/眼周
		// 毛色渐灰+老兽气质,不用人类皱纹词(兽脸上画人类皱纹=面部畸形)
		if ageAnchor := manjuQBeastAgeAnchor(m); ageAnchor != "" {
			p = p + ageAnchor
		}
		if ap != "" {
			p = p + ", distinct creature features: " + ap
		}
		if fa := manjuFeatureAnchor(m); fa != "" {
			p = p + ", " + fa
		}
		return p + ", NOT a human, NOT a human face, NOT a human body, NOT a humanoid, NOT a person, NOT wearing human clothes"
	}
	// 物品类 Q 版(2026-08-29 用户硬性规则:物品按物品渲染,Q 版=物品本体萌化——
	// 可带呆萌表情/小短手,主体仍是物品,禁人形全身/人脸/人衣)
	if manjuIsItem(m) {
		p := "3D rendered cute chibi collectible toy version of an item, small adorable rounded chibi object with big sparkling cute eyes on its surface and tiny stubby arms, cute friendly collectible design, same shape, same material, same engravings and same color markings as the reference item, glossy toy-like finish"
		if base != "" {
			p += ", " + base
		}
		if fa := manjuFeatureAnchor(m); fa != "" {
			p += ", " + fa
		}
		if ap != "" && !strings.Contains(p, ap) {
			p += ", distinct item features: " + ap
		}
		return p + ", NOT a person, NOT a humanoid, no human body, no human face, no limbs, no human clothes"
	}
	// 2026-08-28 非实体角色(用户实测:管理员=纯数据光生命,Q版被套实体衣服做成普通娃娃,
	// 与写实全息体差距巨大):不套人类着装锁/chibi手办构成,走发光数据精灵形态。
	if manjuIsNonPhysical(m) {
		p := "3D rendered cute chibi collectible toy figure, a palm-size adorable glowing holographic data spirit, exactly 2-head-tall super-deformed chibi proportions, oversized round head takes up half of the body height, translucent luminous body made of flowing cyan and gold light streams with floating data particles, big sparkling light-point eyes, tiny stubby arms and legs made of soft glowing code ribbons, gentle ethereal majestic aura, semi-transparent non-solid appearance"
		if base != "" {
			p += ", " + base
		}
		p += ", same entity as the reference image (same cyan and gold light colors, same flowing data-stream body, same data-particle aura, same light-point eyes), NOT a solid human, NOT wearing fabric clothes, no solid body, no physical clothing"
		if fa := manjuFeatureAnchor(m); fa != "" {
			p += ", " + fa
		}
		if ap != "" && !strings.Contains(p, ap) {
			p += ", " + ap
		}
		return p
	}
	if base == "" {
		// 2026-08-27 用户规则:Q 版体型跟随原角色——原角色不胖就不许渲染胖(删 chubby/squishy/
		// plush 等胖词,显式禁胖+体型跟随声明;chibi 只改头身比,不改胖瘦)。
		// 2026-08-27 五修(切 Krea-2 后呆萌感不足):chibi 构成逐项点名——大头占半身/短手
		// 短腿/小圆手/圆脸小嘴/大亮眼,把「Q版=呆萌」的形态学写成硬约束(Krea-2 强指令
		// 跟随,锚越具体执行越到位)。
		// 2026-08-28 修正(用户实测屠夫Q版液压钳变普通小圆手):chibi 构成的 "small round
		// hands" 与机械钳/义体手臂特征正面冲突,模型二选一永远选构成词——角色卡含机械
		// 手臂类特征时,构成改写为「短臂末端是角色的标志性机械部件」,特征优先于呆萌模板。
		handCls := "tiny stubby arms and legs, small round hands"
		eyeCls := "big sparkling glossy eyes"
		if ps := manjuFeaturePhrases(m); len(ps) > 0 {
			low := strings.ToLower(strings.Join(ps, " "))
			// 仅当特征明确是「手臂/钳」类(pincers/claws/hook/…for arms)才替换小圆手——
			// 义体手/白手套(glove concealing a prosthetic)是手部配饰不是武器臂,不替换。
			if strings.Contains(low, "pincer") || strings.Contains(low, "claw") || strings.Contains(low, "hook") ||
				strings.Contains(low, "mechanical arm") || strings.Contains(low, "prosthetic arm") ||
				strings.Contains(low, "cybernetic arm") || strings.Contains(low, "mechanical hands") ||
				strings.Contains(low, "for arms") {
				handCls = "short stubby arms ending in the character's signature mechanical parts (mechanical pincers / claws / cybernetic limbs kept exactly as the reference), short legs"
			}
			// 2026-08-28 二修(韩天枢Q版三轮丢眼镜):base 的 "big sparkling glossy eyes" 大亮眼
			// 构成与眼镜冲突,模型永远优先画无遮挡大眼——含眼镜/眼罩类特征时,构成直接改写为
			// 「戴着该角色标志眼镜的大眼」,让特征成为构成的一部分而非附加锁。
			// 2026-08-28 三修(老纪挂脖老花镜被画成赛博护目镜):改走 manjuEyeClsFor 精确
			// 措辞——检测到什么写什么,不再 "glasses/visor" 混写给模型二选一。
			eyeCls = manjuEyeClsFor(low)
		}
		// 2026-08-28 画风前置(用户反馈 Q 版变动漫):基底开头即声明 3D 手办形态——
		// 开头位置对 Krea-2 权重最高,"chibi cute style" 裸开头是动漫邀请词
		// (style=real 时 portraitPromptFor 走过插画分支,叠加标志特征角色 denoise 1.0
		// 文本唯一画风源 → 2D 赛璐璐动漫)。
		// 2026-08-28 盔甲条件化(林小满Q版左肩金属护甲实例):armor 材质词只给真有
		// 盔甲的角色,无甲角色用纯织物材质——凭空画甲的污染源。fur 同步去除(兽形走
		// 兽形分支不经过此基底,人形角色的 fur 是无毛可画的污染词)。
		mat := "physically accurate materials, detailed fabric weave textures"
		if manjuHasArmor(m) {
			mat = "physically accurate materials with realistic metal reflections on armor, detailed fabric weave textures"
		}
		base = "3D rendered cute chibi collectible toy figure, about 3-head-tall chibi proportions, a large round head taking about one third of the total body height (leaving room on the body for outfit details), " + eyeCls + ", " + handCls + ", soft round face with a small cute mouth, adorable expression with rosy cheeks, compact mini body following the character's original body build, adorable huggable figure, premium high-detail movie-grade CGI quality, cinematic studio lighting, " + mat + ", octane-render level of finish"
	}
	p := base
	// 2026-09-01 形态构成兜底(年轻教员 Q 版人物太大实锤):q_form 常只写「3-head-tall」
	// 缺头部占比/短手脚/大眼细节,覆盖模板构成后比例被稀释成正常人物——q_form 非空时
	// 也统一追加构成锚(身份内容以 q_form 为准,形态以构成锚为准,双管齐下)
	if base != "" && str(m["q_form"]) != "" {
		p += ", a tiny palm-sized chibi figure about 3-head-tall, oversized round head occupying about one third of the total figure height, tiny stubby arms and legs, small round hands, big sparkling eyes, soft round face with a small cute mouth"
	}
	// 2026-08-28 标志性特征锁前置(用户实测:韩天枢Q版丢眼镜/屠夫丢眼罩+液压钳——特征锁排
	// 在 identity 锚前仍不够,Krea-2 0.93 高重绘下后置文本权重被 chibi 构成+着装锁稀释;
	// 前置到 base 之后第一时间点名,与 HAIR LOCK 同级)。
	if fa := manjuFeatureAnchor(m); fa != "" {
		p += ", " + fa
	}
	if anchor := manjuHairAnchor(m); anchor != "" {
		p += ", " + anchor
	}
	// 着装锁(2026-08-27 用户反馈男性 Q 版袒胸露乳):前置强锚+显式禁「敞开外套露胸」
	// (旧防裸词 no shirtless 盖不住这种形态,且原位置在 prompt 末尾遵循弱)
	// 2026-08-28 紧凑化(用户反馈「同逻辑有的Q版有的手办有的动漫」):cfg=1.0 蒸馏模型
	// 无负面通道全靠正向,角色卡厚薄→提示词长度 2000~4300 漂移→遵循度抽奖。统一紧凑
	// 骨架:人人同结构等量、只有身份数据不同,全线压回呆萌手办定案版量级(~2000)。
	p += ", " + manjuQOutfitCompact(m)
	if manjuIsFemale(m) {
		// 女性端庄(2026-08-27 精卫袒胸三修精简版):feminine+手办语义唤起性感素体先验
		p += ", a cute miniature chibi of the same female character, clearly feminine face, modest outfit fully covering the chest and collarbone"
	} else if str(m["gender"]) == "男" {
		// 2026-08-28 强化(顾清寒男 Q 版被画成女):阴柔美男词+chibi 幼态先验滑向女娃,
		// masculine 单词压不住——面部结构/平胸/not a girl 三重加固(精简措辞)。
		// 2026-08-30 年龄分档:老年男不得再锚 "young man's face"(老年 Q 版全是年轻脸
		// 的直接根因之一),措辞按 manjuQMaleFaceCls 分档。
		p += ", a cute miniature chibi of the same male character, " + manjuQMaleFaceCls(m) + ", flat chest, NOT a girl"
	}
	// appearance 剔中文(图像模型不读中文,中文记忆点=废 token 挤占权重),补道具点名
	if apx := manjuStripCJK(ap); apx != "" && !strings.Contains(p, apx) {
		p = p + ", " + apx
	}
	if props := manjuQPropClauses(m); len(props) > 0 {
		p = p + ", carrying the character's signature items exactly as described: " + strings.Join(props, "; ")
	}
	p = p + ", same character as the reference image (identical hairstyle, hair color, outfit colors and design)"
	// 年龄锚对全员成立:成年人防幼态化,未成年防被画成更小的宝宝(保持各自原年龄)
	// 2026-08-30 分档正向锚(manjuQAgeAnchor):老年/中年/少年/儿童各档点名年龄相貌
	// 特征(老年=皱纹/松弛/祖辈气质)——chibi 基底是年轻萌模板,弱锚压不住,必须正向写。
	p += manjuQAgeAnchor(m)
	p = p + ", keeping the character's original age, NOT aged down, no baby face"
	p = p + ", body build strictly follows the original character, NOT chubby"
	p = p + ", fully clothed head to toe, no nudity, no exposed skin except face and hands"
	// 尾部特征大写收尾(双锚夹击补权重):"GLASSES KEPT. PINCERS KEPT."
	if ps := manjuFeaturePhrases(m); len(ps) > 0 {
		short := []string{}
		for _, ph := range ps {
			for _, k := range []string{"glasses", "visor", "eyepatch", "monocle", "goggles", "scanner", "scar", "prosthetic", "pincers", "claw", "hook", "mask", "tattoo", "holographic", "cybernetic", "mechanical eye", "tactical eye", "data-chain"} {
				if strings.Contains(strings.ToLower(ph), k) {
					up := strings.ToUpper(k)
					if !strings.Contains(strings.Join(short, " "), up) {
						short = append(short, up+" KEPT")
					}
					break
				}
			}
		}
		if len(short) > 0 {
			p = p + ", SIGNATURE FEATURES ALWAYS VISIBLE: " + strings.Join(short, ". ") + "."
		}
	}
	// 未成年人护栏(2026-08-27 小男孩全身图下半身裸露同源风险:手办/chibi 素体自带光腿
	// 娃体先验,儿童角色 Q 版同样必须全身完整着装)
	return p + manjuMinorGuard(m) + manjuBeardEnforce(m)
}

func (ctx *manjuCtx) portraitWF(prompt string, seed int, prefix string, char map[string]any, initImage string, initStrength float64) map[string]any {
	// 角色图(人物/妖兽)统一走拟动漫锚(2026-08-25 升级:按种族选锚——人类=东方人像锚,
	// 妖兽/灵宠=兽类锚防被画成人脸;女性/年轻男性强加无胡须纪律,胡须老者保留胡须)。
	// 2026-08-26 升级:改走 ctx.portraitPromptFor 分档——3D/BJD 风格用 3D 虚拟人锚不替换写实措辞,
	// Q 版走 chibi 锚,主定妆强制正面人脸锚(用户规则:人物角色提示词必须正面人脸)。
	prompt = ctx.portraitPromptFor(prompt, char, true)
	// 定妆引擎(2026-08-24 用户规则,视觉实测定案):
	//  **只走 Krea-2** —— 强指令跟随能把「stylized illustration, not photorealistic」执行到位,
	//  出半写实拟漫东方形象(用户要求拟漫化,避免真人照片=侵权)。
	//  Z-Image 实测出真人照片(is_real_person_photo=true,视觉模型判图确认),仅保留作场景图引擎。
	//  SDXL 全面禁用(观感差)。char_engine 字段保留兼容,但人物定妆一律 Krea-2。
	// 负面按物种(2026-08-29 架构重构):物品角色追加禁人负面(charNegPrompt,
	// 负面通道兜底正向锚);Q 版不经 portraitWF 此调用点时不受禁人负面影响
	// (用户规则「Q版形象不影响」)
	return wfKrea2(prompt, str(ctx.R["krea2_unet"]), str(ctx.R["krea2_clip"]), str(ctx.R["krea2_vae"]), seed, manjuPortraitW, manjuPortraitH, prefix, ctx.charNegPrompt(char), initImage, initStrength)
}

// comfyGenImage 提交图片工作流并复制结果到 dst,返回输出文件相对路径
func (ctx *manjuCtx) comfyGenImage(wf map[string]any, dst string, lg *manjuLogger, what string) error {
	pid, err := ctx.comfy.submit(wf)
	if err != nil {
		return err
	}
	lg.logf("  🎨 提交 " + what + " " + pid[:8] + "...")
	if err := ctx.comfy.wait(pid, 900*time.Second, 5*time.Second, lg.stopped); err != nil {
		return err
	}
	entry := ctx.comfy.history(pid)
	rel := comfyOutputImage(entry)
	if rel == "" {
		return fmt.Errorf("图片任务完成但未找到输出")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return copyFile(filepath.Join(ctx.comfyOutput, rel), dst)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func stageAssets(ctx *manjuCtx, lg *manjuLogger) error {
	// 定妆照/场景图也走 ComfyUI:未运行自动拉起
	if err := ctx.ensureComfyReady(lg); err != nil {
		return err
	}
	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		return err
	}
	amap := map[string]any{"characters": map[string]any{}, "scenes": map[string]any{}}
	if b, err := os.ReadFile(filepath.Join(ctx.assetsDir, "asset_map.json")); err == nil {
		_ = json.Unmarshal(b, &amap)
	}
	cmap, _ := amap["characters"].(map[string]any)
	smap, _ := amap["scenes"].(map[string]any)
	if cmap == nil {
		cmap = map[string]any{}
	}
	if smap == nil {
		smap = map[string]any{}
	}
	// 视图代数(2026-08-26 用户实测多张视图是老图):视图生成逻辑升级(Q版两头身/视图性别锚/
	// 兽形角色板/身份前缀清理)后,旧视图一次性删除按新逻辑重出——只删管线自产的
	// _full/_side/_detail/_q/_board 五类视图文件,主图/正脸/_gacha 抽卡历史不动;幂等。
	if g, _ := manjuToInt(amap["views_gen"]); g != manjuViewGen {
		n := 0
		if entries, rerr := os.ReadDir(filepath.Join(ctx.assetsDir, "characters")); rerr == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				nm := e.Name()
				for _, suf := range []string{"_full.png", "_side.png", "_detail.png", "_q.png", "_board.png"} {
					if strings.HasSuffix(nm, suf) {
						_ = os.Remove(filepath.Join(ctx.assetsDir, "characters", nm))
						n++
						break
					}
				}
			}
		}
		amap["views_gen"] = manjuViewGen
		lg.logf(fmt.Sprintf("♻️ 视图生成逻辑已升级:清除 %d 张旧视图,按新逻辑(Q版两头身/性别锚/兽形角色板)重新生成", n))
	}
	// Q 版代数(2026-08-27 用户反馈:叶澜黑白挑染发 Q 版变纯黑)——Q 版发色锁(HAIR LOCK
	// 显式点名发色)上线后,存量旧 Q 版必须重出;独立于 views_gen(只清 _q.png,不动其它视图)。
	if g, _ := manjuToInt(amap["q_gen"]); g != manjuQGen {
		n := 0
		if entries, rerr := os.ReadDir(filepath.Join(ctx.assetsDir, "characters")); rerr == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), "_q.png") {
					_ = os.Remove(filepath.Join(ctx.assetsDir, "characters", e.Name()))
					n++
				}
			}
		}
			amap["q_gen"] = manjuQGen
			if n > 0 {
				lg.logf(fmt.Sprintf("♻️ Q版生成逻辑已升级(年龄分档锚):清除 %d 张旧Q版,按新逻辑(老年/中年/少年儿童特征分档锚定+发色锁)重新生成", n))
			}
	}
	// 主图代数(2026-08-27 六修:用户四反馈——定妆多人/多外套/要纯白背景/日漫脸):
	// 单人白底服装严格锚上线,存量主图必须重出。只清 <char>.png 主图(无下划线后缀的
	// png;视图 mtime 联动重出,Q 版 q_gen 管,_gacha 抽卡历史/adopted 不动)。
	// 2026-08-29 gen5:物品管线独立重构(物种路由,阿碧「女人脸+花盆头」实锤)——
	// 人形链收尾词对物品的污染修复,存量主图+_form2(真身定妆同污染)一并清掉重出;
	// _form2 无 mtime 联动(生成条件只看存在),必须在清理清单里点名。
	if g, _ := manjuToInt(amap["portrait_gen"]); g != manjuPortraitGen {
		n := 0
		if entries, rerr := os.ReadDir(filepath.Join(ctx.assetsDir, "characters")); rerr == nil {
			for _, e := range entries {
				nm := e.Name()
				if e.IsDir() || !strings.HasSuffix(nm, ".png") ||
					(strings.Contains(nm, "_") && !strings.HasSuffix(nm, "_form2.png")) {
					continue
				}
				_ = os.Remove(filepath.Join(ctx.assetsDir, "characters", nm))
				n++
			}
		}
		amap["portrait_gen"] = manjuPortraitGen
		if n > 0 {
			lg.logf(fmt.Sprintf("♻️ 主图生成逻辑已升级(单人/纯白背景/服装严格):清除 %d 张旧主图重新定妆(视图/Q版联动重出)", n))
		}
	}
	chars, _ := plan["characters"].([]any)
	for _, c := range chars {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cid := str(m["id"])
		if cid == "" {
			continue
		}
		dst := filepath.Join(ctx.assetsDir, "characters", cid+".png")
		if !fileExists(dst) {
			lg.logf("🎨 角色定妆照: " + cid + " ...")
			wf := ctx.portraitWF(str(m["image_prompt"]), charSeed(cid, "main"), "manju_asset", m, "", 0)
			if err := ctx.comfyGenImage(wf, dst, lg, "角色 "+cid); err != nil {
				return fmt.Errorf("角色 %s 定妆照失败: %w", cid, err)
			}
		}
		// 双形态定妆(2026-08-26):角色卡含 second_form(素材「真身提示词」标注,萌宠双形态/
		// 兽形真身)→ 额外定妆 <id>_form2.png(同 Krea-2 链,seed 独立派生);
		// 渲染遇「真身·<角色名>」镜切换参考图(shotRefViews)
		if f2 := str(m["second_form"]); f2 != "" {
			f2Dst := filepath.Join(ctx.assetsDir, "characters", cid+"_form2.png")
			if !fileExists(f2Dst) {
				lg.logf("🎨 角色真身形态定妆: " + cid + " ...")
				wf2 := ctx.portraitWF(f2, charSeed(cid, "form2"), "manju_asset", m, "", 0)
				if err := ctx.comfyGenImage(wf2, f2Dst, lg, "角色 "+cid+" 真身"); err != nil {
					return fmt.Errorf("角色 %s 真身定妆失败: %w", cid, err)
				}
			}
			cmap[cid+"_form2"] = "characters/" + cid + "_form2.png"
		}
		cmap[cid] = "characters/" + cid + ".png"
		cmap[cid+"_face"] = "characters/" + cid + "_face.png"
		// 物品类(2026-08-29 用户硬性规则):无正脸概念,跳过 facecrop(引用主图本体)
		if !manjuIsItem(m) {
			if err := ctx.ensureFaceCrop(cid, manjuIsBeast(m), lg); err != nil {
				return err
			}
		}
		// 多视图(审计升级:角色管理/一条龙共用同一套视图资产,保障人物统一):
		// front=正脸特写(ensureFaceCrop 已生成)、full/side/detail 独立视图,
		// 用方案角色卡 views.<view> 提示词生成;旧方案无 views 则跳过(主图+正脸兜底)。
		// 用户反馈:视图纯文生图生成"不相干新角色"(含性别漂移)——视图基于主图
		// img2img(主图复制进 comfyInput 作 latent 起点,denoise 0.6 保留身份)。
		// 主图复制到 ComfyUI input(供 LoadImage 读取;按角色名安全命名)
		mainRef := ""
		if fileExists(dst) {
			refName := "dir_char_main_" + sanitizeFileName(cid) + ".png"
			if copyFile(dst, filepath.Join(ctx.comfyInput, refName)) == nil {
				mainRef = refName
			}
		}
		// 全身照引用:仅作展示/参考;Q 版底图已改回主图(2026-08-28 用户裁决,见 Q 分支注释)
		fullRef := ctx.charFullRef(cid)
		_ = fullRef
		// 视图换视角的 denoise 强度:img2img 换视角需要高 denoise 让模型彻底重绘构图——
		// 但过高(0.92)会把主图身份(发色/胡须/服装)全重绘掉(用户反馈:白发老者侧面变黑发)。
		// 2026-08-24 平衡:降 denoise 保留主图身份 + 提示词追加身份锚,两路夹击保统一。
		// 2026-08-26 用户反馈②(full/side/detail 全渲染成正面照)+manjuViewStrip 落地后重调:
		// full 0.82→0.93(实测 0.88 仍残留主图半身构图——与 Q 版同级才能彻底重构为头到脚全身,
		// 身份由 identityAnchor+appearance 文本锁;side 0.90 已验证可出真 90 度侧面);
		// side 0.85→0.90(90 度侧面需要彻底重绘面部朝向,身份由 identityAnchor+appearance 文本锁)。
		viewStrength := map[string]float64{"full": 0.93, "side": 0.90, "detail": 0.7}
		// 2026-08-27 用户裁决:角色板(board)删除——渲染参考(charViewRels)只用 front/full/detail/
		// 主图,前端角色列表也过滤 _board 不显示,板是零消费纯成本(每角色一张渲染时间,且网格
		// 重绘容易画风漂移)。存量 _board.png 由视图清理逻辑(views_gen 升级)统一删除。
		// 群演轻量卡(2026-08-27 群演分级):minor=true 跳过全部视图/Q版/双形态——
		// 只保留上方已生成的主图定妆照+正脸(1张图成本),渲染参考 front 缺视图自动降级主图。
		// 2026-09-01 群演卡补 side 视图:主持人/多角度镜头侧面渲染崩的修复——侧面参考图
		// 让 H3 侧面镜头有据可依(群演仍跳过 full/detail/q,只多 1 张 side)。
		// 2026-09-01 群演卡完整视图(用户问「主持人为什么只有正面和侧面」):minor
		// 轻量卡此前跳过 full/detail/q——主持人群演也需全身/细节/Q版(群演 Q 版
		// 渲染/审片判分可用);视图成本每卡 3 张,群演卡数量有限,完整视图收益更高。
		viewSet := []string{"full", "side", "detail", "q"}
		for _, view := range viewSet {
			var p string
			if view == "q" {
				// 2026-08-27 用户规则更新:Q 版资产全角色渲染(反派/配角同样出 Q 版资产备用)——
				// 旧「仅正角」限定取消;镜级内心戏用不用反派 Q 版仍由分镜/渲染规则决定,资产层不缺席。
				// 2026-08-25 用户规则:Q 版按角色种族/性别/年龄/胡须构建——
				// 妖兽/灵宠=萌化小兽本体(禁止人形);人类面容随角色本人(脸型/眼型/发型/胡须/年龄感),
				// 女性一律无胡须、年轻男性无胡须、胡须老者保留胡须;不再统一宝宝脸。
				p = manjuQPrompt(m)
			} else {
				// 2026-08-24 一致性:full/side/detail 用 image_prompt(含白发/胡须等身份特征)+视图修饰,
				// 弃用 LLM 泛化的 views.<view>(实测丢发色→侧面变黑发)
				p = charViewPromptFor(ctx, cid, view)
			}
			if p == "" {
				continue
			}
			vDst := filepath.Join(ctx.assetsDir, "characters", cid+"_"+view+".png")
			// 视图时效(2026-08-26 用户实测:换定妆照后视图仍是以前老图):除不存在外,
			// 视图早于主图(定妆照已更新/重新采纳)也必须重生成——旧视图挂在新主图旁,
			// 渲染参考与展示全都对不上(正脸 ensureFaceCrop 已有同款 mtime 联动)。
			needGen := !fileExists(vDst)
			if !needGen {
				if mf, e1 := os.Stat(dst); e1 == nil {
					if vf, e2 := os.Stat(vDst); e2 == nil && vf.ModTime().Before(mf.ModTime()) {
						needGen = true
						lg.logf(fmt.Sprintf("♻️ 角色 %s %s 视图早于主图(定妆照已更新),重新生成", cid, view))
					}
				}
			}
				if needGen {
					if view == "q" {
						// 2026-08-28 定案(用户裁决+全天实证):正面主图 img2img 单段生成。
						// 12:40 批(主图底+方幅+紧凑提示词)是全天最优——身份(发色/服装/配色)
						// 靠 init 自动携带,不依赖文本提取(两段式的词表漏洞:armor 不在着装词表→
						// 谢照盔甲变休闲服;发型提取失灵)。形态由紧凑提示词+方形同幅保证。
						// 特征通道 1.0 取消,统一 0.93(1.0 实测反而出半身写实)。
						lg.logf(fmt.Sprintf("🎨 角色 %s Q版形象(正面主图·方形同幅 img2img 0.93,身份随底图) ...", cid))
						wf := wfKrea2(ctx.portraitPromptFor(manjuQPrompt(m), m, false), str(ctx.R["krea2_unet"]), str(ctx.R["krea2_clip"]), str(ctx.R["krea2_vae"]), charSeed(cid, "q"), manjuPortraitW, manjuPortraitH, "manju_asset", ctx.negPrompt(), mainRef, 0.93)
						if err := ctx.comfyGenImage(wf, vDst, lg, "角色 "+cid+"(Q版)"); err != nil {
							lg.logf("  ⚠️ Q版形象生成失败: " + err.Error())
							continue
						}
					} else {
						st := 0.9
						if s, ok := viewStrength[view]; ok {
							st = s
						}
						// 视角硬锚前置 + 身份锚后置:换视角同时锁死同一人。
						// 2026-08-26 用户反馈②(full/side/detail 全渲染成正面照):p 先过 manjuViewStrip
						// 剥主定妆的 Front-facing portrait 前缀(正面半身构图把所有视图拉回正面特写),
						// 视角由 viewAnchor 独家主导;portraitPromptFor 一律不带正面锚(full 正面由锚承担)。
						anchored := manjuViewPromptBuild(p, view, m)
						// 2026-08-27 用户反馈(视图出动漫形象):full/side/detail 引擎从 Z-Image 切回 Krea-2
						// img2img——Z-Image 是照片向模型,大重绘(0.9+)按自身先验出图,3D 正向锚执行不到位
						// 导致画风与主图(3D)割裂;Krea-2 与主图同引擎,画风天然统一(主图 3D 达标即证明)。
						// 2026-08-24 旧结论「Krea-2 img2img 身份弱」系当时无 identityAnchor/appearance 锚,
						// 现在双文本锚+主图 initImage 三重锁,实测说话;Q 版保持 Z-Image(chibi 手办先验恰好合适)。
						lg.logf(fmt.Sprintf("🎨 角色 %s %s 视图(基于主图 Krea-2 img2img %.2f + 视角锚/身份锚保持同一人) ...", cid, view, st))
						vw, vh := manjuPortraitW, manjuPortraitH
						if view == "full" || view == "side" {
							vw, vh = manjuViewW, manjuViewH
						}
						wf := wfKrea2(ctx.portraitPromptFor(anchored, m, false), str(ctx.R["krea2_unet"]), str(ctx.R["krea2_clip"]), str(ctx.R["krea2_vae"]), charSeed(cid, view), vw, vh, "manju_asset", ctx.charNegPrompt(m), mainRef, st)
						if err := ctx.comfyGenImage(wf, vDst, lg, "角色 "+cid+"("+view+")"); err != nil {
							lg.logf("  ⚠️ " + view + " 视图生成失败(回退主图+正脸): " + err.Error())
							continue
						}
					}
				}
				cmap[cid+"_"+view] = "characters/" + cid + "_" + view + ".png"
			}
		}
		scenes, _ := plan["scenes"].([]any)
	for i, sc := range scenes {
		m, ok := sc.(map[string]any)
		if !ok {
			continue
		}
		sid := str(m["id"])
		if sid == "" {
			continue
		}
		dst := filepath.Join(ctx.assetsDir, "scenes", sid+".png")
		if !fileExists(dst) {
			lg.logf("🎨 场景图: " + sid + " ...")
			// 2026-08-24 用户反馈:场景渲染出人物——场景图必须强制空场景无人
			// (manjuSceneAnchor = "empty scene, no people";素材 image_prompt 可能不带,这里兜底强制)
			// 2026-08-26 补:3D 档场景走 manju3DSceneAnchor(3D 渲染虚拟场景,与角色 3D 风格统一;
			// 通用锚无风格措辞,Z-Image 按"真实摄影"先验出照片感)+ 演播厅类场景显式禁观众/空座位。
			scenePrompt := manjuSceneAnchor + ", " + str(m["image_prompt"]) + ", no people, no humans, no characters, no figures, no silhouettes"
			if manjuStyleIs3D(ctx.style) {
				scenePrompt = manju3DSceneAnchor + ", " + str(m["image_prompt"]) + ", no people, no humans, no characters, no figures, no silhouettes, no audience, no crowd, empty seats"
			}
			// 2026-08-24 用户反馈:场景图上下两张拼接——Z-Image 对极端竖比例(768×1344≈0.57)会把画面
			// 切成上下两段生成。场景图固定方形 1024×1024(与定妆照同策略,画幅无关),避免拼接感;
			// H3 引用时按需裁切参考图(比例不符的参考图 H3 会自动缩放,方形最稳)。
			wf := wfZImage(scenePrompt, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), 8000+i, manjuPortraitW, manjuPortraitH, "manju_asset", ctx.negPrompt(), "", 0)
			if err := ctx.comfyGenImage(wf, dst, lg, "场景 "+sid); err != nil {
				return fmt.Errorf("场景 %s 失败: %w", sid, err)
			}
		}
		smap[sid] = "scenes/" + sid + ".png"
		// FL2VA 双帧(审计升级 P1):启用 fl2va_end_frame 时生成场景尾帧(同 prompt 不同 seed +
		// 轻微运动提示,作为空镜 FL2VA 首尾插值的尾帧锚点,场景内运动更稳);默认关闭(成本翻倍)
		if b, _ := ctx.R["fl2va_end_frame"].(bool); b {
			endDst := filepath.Join(ctx.assetsDir, "scenes", sid+"_end.png")
			if !fileExists(endDst) {
				lg.logf("🎨 场景尾帧(FL2VA): " + sid + " ...")
				endPrompt := manjuSceneAnchor + ", " + str(m["image_prompt"]) + ", no people, no humans, no characters, no figures, no silhouettes, the same scene at a slightly later moment, subtle motion of elements (leaves drifting, water rippling, light shifting), consistent layout and lighting"
				wf := wfZImage(endPrompt, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), 9000+i, manjuPortraitW, manjuPortraitH, "manju_asset", ctx.negPrompt(), "", 0)
				if err := ctx.comfyGenImage(wf, endDst, lg, "场景尾帧 "+sid); err != nil {
					lg.logf("  ⚠️ 场景尾帧生成失败(回退单图 I2VA): " + err.Error())
				}
			}
		}
	}
	if b, err := json.MarshalIndent(amap, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(ctx.assetsDir, "asset_map.json"), b, 0644)
	}
	lg.logf(fmt.Sprintf("  ✅ 资产就绪: %d 角色 / %d 场景", len(cmap)/2, len(smap)))
	return nil
}

// ---- 条件缓存指纹 + 缓存名 ----

// assetsFingerprint 对 assets/characters + assets/scenes 下 png 的「文件名+mtime」做 MD5 取 8 位
func (ctx *manjuCtx) assetsFingerprint() string {
	var parts []string
	for _, dir := range []string{"characters", "scenes"} {
		entries, err := os.ReadDir(filepath.Join(ctx.assetsDir, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			parts = append(parts, e.Name()+"@"+strconv.FormatInt(info.ModTime().UnixNano(), 10)+"|"+strconv.FormatInt(info.Size(), 10))
		}
	}
	sort.Strings(parts)
	sum := md5Hex(strings.Join(parts, "|"))
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return sum
}

// shotCacheName 条件缓存名:项目_v版本_内容指纹。
// 内容指纹 = md5(提示词 + 角色列表 + 场景 + 画幅 + 帧数):同条件镜头共享同一份缓存,
// Qwen3-VL 只编码一次(原来每镜头独立缓存,重复编码浪费;相同条件的后续镜头直接复用)。
// 版本号进缓存名:逻辑升级(如多角色参考图)后旧缓存自动失效重编。
const manjuCacheVer = "v2"

func (ctx *manjuCtx) shotCacheName(s manjuShot) string {
	return ctx.shotCacheNameAt(s, ctx.w, ctx.h)
}

// shotCacheNameAt 指定宽高的缓存名(草稿/定稿分辨率各自独立缓存,互不挤占)
func (ctx *manjuCtx) shotCacheNameAt(s manjuShot, w, h int) string {
	proj := reNonWord.ReplaceAllString(ctx.project, "_")
	return fmt.Sprintf("%s_%s_c%s", proj, manjuCacheVer, ctx.shotCondFingerprintAt(s, w, h))
}

// shotCondFingerprint 镜头条件指纹:决定缓存是否可复用的全部输入
func (ctx *manjuCtx) shotCondFingerprint(s manjuShot) string {
	return ctx.shotCondFingerprintAt(s, ctx.w, ctx.h)
}

// shotCondFingerprintAt 指定宽高的条件指纹(草稿/定稿分开记账)
func (ctx *manjuCtx) shotCondFingerprintAt(s manjuShot, w, h int) string {
	hh := md5.New()
	// 参考图指纹:预编码把角色/场景参考烧进 .pt,定妆照采纳(或重生成)后必须重编码,
	// 否则缓存命中跳过、渲染继续用旧角色(换定妆照不生效的隐性根源);多视图全部计入
	refs := []string{}
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		for _, rel := range ctx.charViewRels(cid, i, n) {
			refs = append(refs, ctx.refStamp(rel))
		}
	}
	if s.Scene != "" {
		if rel := "scenes/" + s.Scene + ".png"; fileExists(filepath.Join(ctx.assetsDir, rel)) {
			refs = append(refs, ctx.refStamp(rel))
		}
	}
	// 审计 P2:FL2VA 尾帧维度——空镜 + fl2va_end_frame 开关时编码输入多一张 _end.png
	// (单图 I2VA → 双帧 Fl2VA),开关切换或尾帧图重新生成后缓存必须失效,
	// 否则旧模式缓存被新模式复用(双帧锚点丢失/多余),开关等于摆设
	endFrame := false
	if b, _ := ctx.R["fl2va_end_frame"].(bool); b && len(s.Characters) == 0 {
		endFrame = true
		if s.Scene != "" {
			if rel := "scenes/" + s.Scene + "_end.png"; fileExists(filepath.Join(ctx.assetsDir, rel)) {
				refs = append(refs, "end:"+ctx.refStamp(rel))
			}
		}
	}
	// 提示词走最终化纯函数(2026-08-27 指纹对称修复 + 2026-08-30 契约对齐):mark(渲染后)
	// 与 stale 检查(下次运行)都必须基于与渲染完全相同的"对齐+guard 最终化文本"算指纹,
	// 否则对齐改词不触发重渲(旧缓存继续指鹿为马)或恒 stale 反复重渲。
	finalPrompt := ctx.finalizeAlignedPrompt(s.H3Prompt, s, ctx.shotPicSlots(s))
	fmt.Fprintf(hh, "w=%d|h=%d|len=%d|chars=%s|scene=%s|refs=%s|fl2va_end=%t|prompt=%s",
		w, h, h3Length(s.Duration, ctx.fps),
		strings.Join(s.Characters, ","), s.Scene, strings.Join(refs, ","), endFrame, finalPrompt)
	sum := fmt.Sprintf("%x", hh.Sum(nil))
	if len(sum) > 10 {
		sum = sum[:10]
	}
	return sum
}

// ---- 预编码 ----

func (ctx *manjuCtx) ensureEncoded(s manjuShot, cacheName string, lg *manjuLogger) error {
	return ctx.ensureEncodedAt(s, cacheName, ctx.w, ctx.h, lg)
}

// manjuEncSingleflight 预编码 singleflight(审计 M2):同条件缓存并发提交只跑一次——
// 相邻镜头同 prompt/角色/场景时,预编码 goroutine 与渲染路径的 ensureEncodedAt 同时
// fileExists 未命中会双提交同 cacheName.pt,白白烧一次 Qwen3-VL 编码
// 审计 P7:用带引用计数的门闩(encGate)——释放执行权与删除表项不再是非原子两步:
// 旧实现 defer{<-gate; Delete} 在"释放后、删除前"窗口内,等待者 B 拿到旧门闩成为新执行者,
// 同时新调用者 C 已 LoadOrStore 新门闩 → 同 cacheName 双任务各烧一次编码。
// encGate.refs 计数所有等待者,全部退出后才 Delete,窗口消除。
type encGate struct {
	mu   sync.Mutex
	gate chan struct{}
	refs int
}

var manjuEncSingleflight sync.Map // cacheName → *encGate

// ensureEncodedAt 指定宽高的预编码(草稿/定稿各自的条件缓存)
func (ctx *manjuCtx) ensureEncodedAt(s manjuShot, cacheName string, w, h int, lg *manjuLogger) error {
	if fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
		return nil
	}
	// singleflight:同 cacheName 并发去重(先到者执行,后到者等文件出现或接力)。
	// 必须用缓冲 1 的 chan:无缓冲 chan 的发送在无接收者时永不成功,select 恒走 default,
	// 所有调用者都在等文件、没人真正执行编码 → 1900s 后全部"等待预编码完成超时"(必挂)。
	v, _ := manjuEncSingleflight.LoadOrStore(cacheName, &encGate{gate: make(chan struct{}, 1)})
	g := v.(*encGate)
	g.mu.Lock()
	g.refs++
	g.mu.Unlock()
	// 引用计数:全部调用者(执行者+等待者)退出后才删表项
	defer func() {
		g.mu.Lock()
		g.refs--
		if g.refs == 0 {
			manjuEncSingleflight.Delete(cacheName)
		}
		g.mu.Unlock()
	}()
	acquired := false
	for !acquired {
		select {
		case g.gate <- struct{}{}: // 拿到执行权
			acquired = true
		default:
			// 执行权被占:轮询等文件出现;执行者失败释放执行权(文件未生成)则接力重试
			deadline := time.Now().Add(1900 * time.Second)
			for time.Now().Before(deadline) && !acquired {
				if fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
					return nil
				}
				select {
				case g.gate <- struct{}{}:
					acquired = true
				default:
					time.Sleep(2 * time.Second)
				}
			}
			if !acquired {
				return fmt.Errorf("等待预编码完成超时: %s", cacheName)
			}
		}
	}
	defer func() { <-g.gate }() // 释放执行权(等文件出现提前返回时不持有)
	// 渲染输入副本(FL2VA 尾帧等每镜变量写入副本,防并发预编码 goroutine 竞态 ctx.R)
	r2 := map[string]any{}
	for k, v := range ctx.R {
		r2[k] = v
	}
	// FL2VA 尾帧(审计升级 P1):空镜镜头 + 开关开启 → 本镜场景的 _end.png 作尾帧锚点;
	// 有角色镜头删尾帧(Ref2VA 无此概念)
	// 审计 4.2:尾帧复制进 comfyInput 再传文件名(与场景图同口径)——本地绝对路径直传
	// LoadImage 在 ComfyUI 远程/容器化部署时读取失败,且与其它参考图语义不一致
	// 双帧实现:核心节点 MiniMaxH3ImageToVideo 支持 last_frame 参数(无需自定义节点)
	if len(s.Characters) == 0 {
		if b, _ := ctx.R["fl2va_end_frame"].(bool); b && s.Scene != "" {
			src := filepath.Join(ctx.assetsDir, "scenes", s.Scene+"_end.png")
			if fileExists(src) {
				name := fmt.Sprintf("dir_scene_%d_end.png", s.ID)
				if copyFile(src, filepath.Join(ctx.comfyInput, name)) == nil {
					r2["_scene_end"] = name
				}
			}
		}
	}
	lg.logf("  预编码提交...")
	// 审计 P3:停止后不提交新编码任务(预编码 goroutine 可能在停止瞬间拿到执行权,
	// 提交后 wait 立即感知停止,但 ComfyUI 仍会跑完该任务)
	if lg.stopped() {
		return fmt.Errorf("已停止")
	}
	wf := h3EncWorkflow(r2, s.H3Prompt, w, h, h3Length(s.Duration, ctx.fps),
		ctx.charRefNames(s), ctx.charVoiceNames(s), ctx.sceneRefName(s), cacheName, len(s.Characters) > 0)
	pid, err := ctx.comfy.submit(wf)
	if err != nil {
		return err
	}
	if err := ctx.comfy.wait(pid, 1800*time.Second, 10*time.Second, lg.stopped); err != nil {
		return err
	}
	// 落盘校验(2026-08-29 事故防御):ComfyUI 节点级输出缓存可能让同图重提全部短路
	// (H3CondSave 不执行、文件不落盘、任务仍报 success)——wait 通过但文件缺失必须
	// 显式报错,不能静默继续(否则渲染阶段 CondLoad 才暴露,白烧一次渲染任务)。
	if !fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
		return fmt.Errorf("镜头 %d 编码任务完成但缓存未落盘(%s.pt)——疑似 ComfyUI 节点缓存短路,请重启 ComfyUI 后重试", s.ID, cacheName)
	}
	lg.logf("  ✅ 镜头 " + strconv.Itoa(s.ID) + " 条件缓存完成 -> " + cacheName + ".pt")
	return nil
}

func stageEncode(ctx *manjuCtx, lg *manjuLogger) error {
	// 预编码走 ComfyUI:未运行自动拉起
	if err := ctx.ensureComfyReady(lg); err != nil {
		return err
	}
	// 编码前释放显存:assets 阶段加载的 ZImage/Lumina 常驻,不腾空间 Qwen3-VL 32B 加载会
	// 因显存不足阻塞,编码任务提交后 ComfyUI 挂起 → 渲染"显卡没动静"卡死(本 BUG 根因)
	ctx.freeComfyModels(lg)
	_, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	if len(selected) == 0 {
		lg.logf("  ⏭ 没有需要处理的镜头")
		return nil
	}
	// 预编码耗时提示:单镜 = 条件编码(~10min) + 视频采样(8 步 × 帧数 × ~1.42s/帧),
	// 实测 4s 镜约 30 分钟、12s 镜约 70 分钟。中途退出会打断采样、条件缓存不落盘,
	// 下次续跑该镜重编(此前"反复中断零进度"死循环的根因)——先给总览再逐镜提示,防误判卡死。
	minE, maxE, estTotal := 1<<30, 0, 0
	for _, s := range selected {
		e := manjuEncEstMin(s.Duration, ctx.fps)
		estTotal += e
		if e < minE {
			minE = e
		}
		if e > maxE {
			maxE = e
		}
	}
	lg.logf(fmt.Sprintf("  ⏱ 预编码 %d 镜:单镜约 %d~%d 分钟,预计共 %d 分钟(%d 小时 %d 分)——期间请勿退出应用,退出将中断当前镜编码",
		len(selected), minE, maxE, estTotal, estTotal/60, estTotal%60))
	for i, s := range selected {
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}
		cacheName := ctx.shotCacheName(s)
		if fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
			lg.logf("  跳过（缓存已存在）: " + cacheName + ".pt")
			continue
		}
		lg.logf(fmt.Sprintf("[%d/%d] 镜头 %d: [%s] %s（预计 %d 分钟/镜）", i+1, len(selected), s.ID, s.Scene, s.Camera, manjuEncEstMin(s.Duration, ctx.fps)))
		if err := ctx.ensureEncoded(s, cacheName, lg); err != nil {
			return fmt.Errorf("镜头 %d 预编码失败: %w", s.ID, err)
		}
	}
	lg.logf("🎉 预编码完成")
	return nil
}

// manjuEncEstMin 单镜 H3 预编码预计耗时(分钟):条件编码约 10min + 视频采样
// 8 步 × 帧数 × ~1.42s/帧(107 帧 ≈ 152s/步,实测)。仅作进度提示,不参与逻辑。
func manjuEncEstMin(seconds, fps int) int {
	frames := h3Length(seconds, fps)
	return 10 + frames*8*142/6000
}

// ensurePlanAndPrompts 方案 + 多切点分组 + 逐镜提示词(渲染/预编码前置)
func (ctx *manjuCtx) ensurePlanAndPrompts(lg *manjuLogger) (map[string]any, []manjuShot, error) {
	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		return nil, nil, err
	}
	shots, _ := planShots(plan)
	ctx.ensureTakes(plan, shots, lg) // 多切点长镜分组(experimental,默认关)
	shots = applyTakes(plan, shots)  // 组头时长=总和,内镜标 TakeTail
	if err := ctx.genShotPrompts(plan, shots, lg); err != nil {
		return nil, nil, err
	}
	plan, shots, err = ctx.loadPlan() // 重读(提示词已回写)
	if err != nil {
		return nil, nil, err
	}
	// 全局说话人注册表(2026-08-30 ver14):按全集首次发声顺序为有音色绑定角色分配
	// 全局 (Sx),finalize 汇点(finalizeAlignedPrompt)据此跨镜稳定说话人 ID——
	// 音色一致性依赖该注册表,必须先于任何 finalize 构建。
	ctx.speakerReg = ctx.buildSpeakerRegistry(shots)
	// 渲染输入最终化统一汇点(2026-08-29 根治「纪律从未生效」):encode 阶段与 render
	// 阶段各自从 plan 读 h3_prompt 提交——此前只有 renderShotTo 内 finalize,encode 阶段
	// 用原文预编码,条件缓存固化的是无纪律的原文;渲染 CondLoad 复用该缓存,FRAME/
	// MOTION/AUDIO/EXECUTION/CHAIN 全部纪律从未进入条件编码(实测 EP01 25 镜提交
	// 与 plan 完全一致 sim=1.000,无任何追加)。这里在共用入口 finalize 并回写 plan,
	// 指纹/编码/渲染三处拿同一份最终化文本,幂等(已含纪律不重复追加)。
	// 风格句写实化(2026-08-29 用户反馈「不是写实,怎么变成了卡通」):脚本风格句按立项
	// style 转写常带 anime-stylized/semi-realistic 动漫化措辞(万怪之主 56 章 251 处),
	// H3 出片角色即 3D 动漫感。写实书(非动漫非 3D)在汇点统一替换为 photorealistic
	// 并回写 plan——与纪律注入同位置,指纹/编码/渲染三处一致,自动触发全量重渲。
	realized := false
	if !manjuStyleIsAnime(ctx.style) && !manjuStyleIs3D(ctx.style) {
		for i := range shots {
			if rp := manjuRealizeStyle(shots[i].H3Prompt); rp != shots[i].H3Prompt {
				shots[i].H3Prompt = rp
				realized = true
			}
		}
	}
	finalized := realized
	for i := range shots {
		// plan 汇点在 assets 阶段之前,槽位走预期值(不探测文件)——实测恒 0 会把
		// <Picture N> 引用整集剥光并固化(2026-08-29 绿萝实锤,见 finalizeShotPromptSlots)
		fp := ctx.finalizeShotPromptSlots(shots[i].H3Prompt, shots[i], manjuExpectPicSlots(shots[i]), lg)
		if fp != shots[i].H3Prompt {
			shots[i].H3Prompt = fp
			finalized = true
		}
	}
	if finalized {
		// 回写 plan 并落盘:ensurePlanAndPrompts 是 encode/render 共用入口,后续阶段
		// loadPlan 重读必须拿到同一份最终化文本(指纹/编码/渲染三处一致)
		if arr, ok := plan["shots"].([]any); ok {
			byID := map[int]map[string]any{}
			for _, x := range arr {
				if m, ok := x.(map[string]any); ok {
					if id, ok := manjuToInt(m["shot_id"]); ok {
						byID[id] = m
					}
				}
			}
			for _, s := range shots {
				if m := byID[s.ID]; m != nil {
					m["h3_prompt"] = s.H3Prompt
				}
			}
		}
		_ = ctx.writePlan(plan)
	}
	return plan, applyTakes(plan, shots), nil
}

// shotsPerTake 多切点长镜每组镜头数(1=关闭;render.shots_per_take 2-3)
func (ctx *manjuCtx) shotsPerTake() int {
	if n, ok := manjuToInt(ctx.R["shots_per_take"]); ok && n >= 2 && n <= 3 {
		return n
	}
	return 1
}

// ensureTakes 多切点长镜分组(experimental):相邻、同场景镜头贪心打包(组内时长和 ≤15s、
// 数量 ≤shots_per_take)。分组确定性且只生成一次写回 plan.takes(提示词缓存稳定性依赖)。
func (ctx *manjuCtx) ensureTakes(plan map[string]any, shots []manjuShot, lg *manjuLogger) {
	// 2026-08-26 修复(用户实测万界召唤 16 分镜只渲染 8 个):脚本直出模式与多切点长镜
	// 根本不兼容——脚本每镜六段式提示词是逐字权威(节拍/时间码按单镜时长写死),takes 分组后
	// 组头沿用单镜提示词却要渲染「组内多镜时长之和」的长视频,组内其余镜头的画面与台词
	// 不会出现在任何提示词里 → 整镜内容丢失、产物数量凭空减半。脚本直出一律逐镜独立渲染;
	// 旧 plan 已写入的 takes 一并清除自愈(applyTakes 不再吞掉内镜)。
	if ctx.scriptMode {
		if plan["takes"] != nil {
			delete(plan, "takes")
			_ = ctx.writePlan(plan)
			lg.logf("  🎬 脚本直出:已清除多切点分组(takes),逐镜独立渲染(六段式提示词逐字权威,防丢镜)")
		}
		return
	}
	if ctx.shotsPerTake() < 2 {
		return
	}
	if plan["takes"] != nil {
		return // 已有分组复用(方案重生成时 takes 随旧方案一起消失,自动重分组)
	}
	var takes []any
	var cur []manjuShot
	flush := func() {
		defer func() { cur = nil }()
		if len(cur) < 2 {
			return
		}
		ids := make([]any, 0, len(cur))
		for _, s := range cur {
			ids = append(ids, s.ID)
		}
		takes = append(takes, ids)
	}
	for _, s := range shots {
		if len(cur) > 0 {
			last := cur[len(cur)-1]
			sum := 0
			for _, c := range cur {
				sum += c.Duration
			}
			// 新镜加入的约束:同场景 + 组内数量/时长上限
			if s.Scene != last.Scene || len(cur) >= ctx.shotsPerTake() || sum+s.Duration > 15 {
				flush()
			}
		}
		cur = append(cur, s)
	}
	flush()
	plan["takes"] = takes // 空也写(标记已分组,避免每次运行重复计算)
	if err := ctx.writePlan(plan); err == nil && len(takes) > 0 {
		n := 0
		for _, t := range takes {
			n += len(anyArr(t))
		}
		lg.logf(fmt.Sprintf("🎥 多切点长镜: %d 组(覆盖 %d 镜,组内 [Shot N] 切点一次生成)", len(takes), n))
	}
}

// applyTakes 解析 plan.takes:组头 Duration=组内总和(clamp 15s,渲染一次),
// 内镜标 TakeTail(渲染阶段跳过;字幕/ASR 由组头文件按 takes 合并承载)。
func applyTakes(plan map[string]any, shots []manjuShot) []manjuShot {
	byID := map[int]*manjuShot{}
	for i := range shots {
		byID[shots[i].ID] = &shots[i]
	}
	for _, g := range anyArr(plan["takes"]) {
		var ids []int
		for _, x := range anyArr(g) {
			if n, ok := manjuToInt(x); ok {
				ids = append(ids, n)
			}
		}
		if len(ids) < 2 {
			continue
		}
		head := byID[ids[0]]
		if head == nil {
			continue
		}
		sum := 0
		var group []manjuShot
		for _, id := range ids {
			s := byID[id]
			if s == nil {
				continue
			}
			group = append(group, *s)
			sum += s.Duration
			if id != ids[0] {
				s.TakeTail = true
			}
		}
		if sum > 15 {
			sum = 15 // API 时长上限 clamp
		}
		head.Duration = sum
		head.TakeGroup = group
	}
	return shots
}

// stagePlan 方案阶段:LLM 直出人物/场景/分镜 + 逐镜 H3 提示词
func stagePlan(ctx *manjuCtx, lg *manjuLogger) error {
	_, _, err := ctx.ensurePlanAndPrompts(lg)
	return err
}

// selectedShots 按 only 过滤(内镜恒过滤:由组头一次渲染覆盖)
func (ctx *manjuCtx) selectedShots(shots []manjuShot) []manjuShot {
	sel := map[int]bool{}
	all := ctx.only == ""
	if !all {
		for _, p := range strings.Split(ctx.only, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if i := strings.Index(p, "-"); i > 0 {
				a, _ := strconv.Atoi(p[:i])
				b, _ := strconv.Atoi(p[i+1:])
				for n := a; n <= b; n++ {
					sel[n] = true
				}
			} else if n, err := strconv.Atoi(p); err == nil {
				sel[n] = true
			}
		}
	}
	var out []manjuShot
	for _, s := range shots {
		if s.TakeTail {
			continue
		}
		if all || sel[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// ---- 参考图(复制到 ComfyUI input,LoadImage 直接按名读取) ----

// charRefNames 全部登场角色的参考图:按视图预算收集(正脸特写优先,可含全身/细节多视图),
// 同一角色多视图按 <Picture N..N+k> 顺序传入,与 prompt 的 subject_definitions 一一对应。
// 2026-08-29 审计 P0 收敛:Q版/真身切换统一走 shotViewRelsFor(与提示词侧 shotRefViews
// 同一数据源)——此前实际挂载走裸 charViewRels,真身镜提示词写 <Picture 1>=真身、
// 实际挂的是正脸/全身视图,双形态/Q版链功能性失效。
func (ctx *manjuCtx) charRefNames(s manjuShot) []string {
	var out []string
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		for j, rel := range ctx.shotViewRelsFor(s, cid, i, n) {
			name := fmt.Sprintf("dir_char_%d_%d_%d.png", s.ID, i, j)
			_ = copyFile(filepath.Join(ctx.assetsDir, rel), filepath.Join(ctx.comfyInput, name))
			out = append(out, name)
		}
	}
	return out
}

// shotViewRelsFor 该镜该角色的参考图相对路径清单(Q版/form2 切换单一数据源):
// 内心戏镜(narration 含「内心·」)→ [front, q];真身镜(镜头文本标「真身·角色名」
// 或含真身/化形关键词)且有 form2 资产 → [form2];否则 charViewRels 视图预算。
// charRefNames(实际挂载)与 shotRefViews(提示词 Picture 清单)共用,保证编号对齐。
func (ctx *manjuCtx) shotViewRelsFor(s manjuShot, cid string, i, n int) []string {
	rels := ctx.charViewRels(cid, i, n)
	// 内心戏(2026-08-23 用户规则):Q 版形象优先(front 锁脸 + q 呆萌)
	if strings.Contains(s.Narration, "内心·") {
		qRel := manjuViewRel(cid, "q")
		if fileExists(filepath.Join(ctx.assetsDir, qRel)) {
			rels = []string{manjuViewRel(cid, "front"), qRel}
		}
	}
	// 双形态切换(2026-08-26):整体切换为真身形态定妆
	if form2Rel := manjuViewRel(cid, "form2"); fileExists(filepath.Join(ctx.assetsDir, form2Rel)) && shotWantsTrueForm(s, cid) {
		rels = []string{form2Rel}
	}
	return rels
}

// charVoiceRef 角色配音音色音频相对 ComfyUI input 的路径;未绑定/未匹配返回空。
// 优先级(2026-08-27 自动选择):①用户显式绑定 voice_<项目>_<角色ID>.mp3;
// ②按角色人设自动匹配预置风格音色库(autoVoiceFor → lib_<音色名>.mp3,缺失自动补齐)。
// 确定性命名——voice/gen 生成、自动匹配、渲染端查找共用同一规则,plan 绑定仅存记录供回显。
func (ctx *manjuCtx) charVoiceRef(cid string) string {
	if cid == "" {
		return ""
	}
	// ① 用户显式绑定(权威副本在 voice_lib/,input 缺失时同步,防误删后失效)
	name := "voice_" + ctx.project + "_" + sanitizeFileName(cid) + ".mp3"
	rel := filepath.ToSlash(filepath.Join("audio", name))
	if !fileExists(filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))) {
		ctx.syncVoiceLibToComfy(rel)
	}
	if fileExists(filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))) {
		return rel
	}
	// ② 自动匹配(角色人设 → 风格音色库;2026-08-30 ver15 同档位差异化变体:
	// 两个中年男不再共用 male_mag 一个音色文件)
	lib := ctx.assignedVoiceFor(cid)
	if lib == "" {
		return ""
	}
	libName := "lib_" + lib + ".mp3"
	rel = filepath.ToSlash(filepath.Join("audio", libName))
	// 权威副本在 voice_lib/(2026-08-29 稳定化):input 副本缺失时从权威同步,
	// 权威也缺失才生成(生成写权威+同步 input),edge-tts 不可用时跳过该角色音色
	if !fileExists(filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))) {
		if !fileExists(ctx.voiceLibAuthoritative(rel)) {
			if err := ctx.genVoiceLibAudio(lib); err != nil {
				return "" // 生成失败(edge-tts 不可用/离线)跳过该角色音色,降级为无参考
			}
		} else {
			ctx.syncVoiceLibToComfy(rel)
		}
	}
	return rel
}

// charInfoFor 方案角色对象(id → 角色卡),懒加载 plan.characters;找不到返回 nil
func (ctx *manjuCtx) charInfoFor(cid string) map[string]any {
	if ctx.charInfo == nil {
		ctx.charInfo = map[string]map[string]any{}
		if plan, _, err := ctx.loadPlan(); err == nil {
			if arr, ok := plan["characters"].([]any); ok {
				for _, x := range arr {
					if m, ok := x.(map[string]any); ok {
						ctx.charInfo[str(m["id"])] = m
					}
				}
			}
		}
	}
	return ctx.charInfo[cid]
}

// autoVoiceFor 按角色人设自动匹配风格音色(2026-08-27 用户需求;同日矩阵化升级:
// 用户反馈须按年龄×性别细分且音色不太友好——旧版儿童共用少年声、女性中年/老年误用
// 粤语音色、反派女也是粤语,全是「不友好」来源)。
// 返回音色库 Key(lib_<Key>.mp3 与绑定下拉同标识)。
// 匹配优先级:兽类/灵宠→萌系;反派→低沉冷冽(强化辨识);性别×年龄矩阵;性别兜底;无信息→温柔女声。
// reManjuNumAge 数字年龄(2026-08-30 ver14):"74岁"/"60 岁"/"22岁"——autoVoiceFor
// 数字档位优先的依据,与 manjuAgeBand 同档位。
var reManjuNumAge = regexp.MustCompile(`(\d+)\s*岁`)

// voiceVariantsFor 档位 → 变体链(base 本身 + _2/_3 后缀;音色库有变体才返回多条)
func voiceVariantsFor(base string) []string {
	out := []string{base}
	if manjuVoiceLibFor(base+"_2") == nil {
		return out
	}
	for i := 2; ; i++ {
		k := base + "_" + strconv.Itoa(i)
		if manjuVoiceLibFor(k) == nil {
			break
		}
		out = append(out, k)
	}
	return out
}

// dialectVoiceFor 角色卡 → 热门方言音色(2026-08-30 ver15,用户要求「适当给一些
// 热门方言,语言不单一」):卡内「音色/声线/方言」字段或人设(记忆点/口癖/外观)
// 含地域词时分配对应方言音色(东北/陕西女声、四川/河南/广西/湖南男声、粤/台男女);
// 仅人设强关联才触发,无地域词的角色保持普通话档位(方言是语言调味,不能全员方言)。
// 性别不匹配跳过(如女角色配四川话但无声源→回退普通话档位)。
func (ctx *manjuCtx) dialectVoiceFor(cid string) string {
	c := ctx.charInfoFor(cid)
	if c == nil {
		return ""
	}
	gender := str(c["gender"])
	src := str(c["voice"]) + " " + str(c["age"]) + " " + str(c["appearance"]) + " " + str(c["costume"]) + " " + str(c["image_prompt"])
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(src, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("粤", "广东", "香港", "港风", "粤语", "广普"):
		if gender == "男" {
			return "hk_male"
		}
		return "hk_female"
	case has("台湾", "台普", "台妹"):
		if gender == "男" {
			return "tw_male"
		}
		return "tw_female"
	case has("四川", "川渝", "成都", "重庆", "川普"):
		if gender == "男" {
			return "cn_sichuan"
		}
	case has("河南", "郑州", "中原", "豫"):
		if gender == "男" {
			return "cn_henan"
		}
	case has("广西", "南宁", "桂林", "白话"):
		if gender == "男" {
			return "cn_guangxi"
		}
	case has("湖南", "长沙", "湘"):
		if gender == "男" {
			return "cn_hunan"
		}
	case has("东北", "辽宁", "吉林", "黑龙江", "大碴子"):
		if gender == "女" {
			return "cn_dongbei"
		}
	case has("陕西", "西安", "关中"):
		if gender == "女" {
			return "cn_shaanxi"
		}
	}
	return ""
}

// buildVoiceAssignment 全局音色分配(2026-08-30 ver15,用户实锤「玄经理和陈守家及
// 旁白全用同一个」的根治):按全集登场序,①方言角色(卡内音色/人设含地域词)分配
// 热门方言音色(语言不单一);②其余角色按档位轮转分配变体(male_mag/male_mag_2/
// male_mag_3)——不同角色不同音色文件,分得清谁是谁;档位无变体时仍共用。
// narrator 叙述音色不参与分配(旁白专属,见 manjuOffscreenBindings)。
func (ctx *manjuCtx) buildVoiceAssignment() map[string]string {
	assign := map[string]string{}
	used := map[string]int{}
	for _, cid := range ctx.charIDs() {
		if d := ctx.dialectVoiceFor(cid); d != "" {
			assign[cid] = d // 方言角色不占普通话档位变体配额
			continue
		}
		base := ctx.autoVoiceFor(cid)
		if base == "" {
			continue
		}
		vs := voiceVariantsFor(base)
		n := used[base]
		assign[cid] = vs[n%len(vs)]
		used[base] = n + 1
	}
	return assign
}

// charIDs 方案全部角色 id(按 plan.characters 顺序=登场序;懒加载)
func (ctx *manjuCtx) charIDs() []string {
	var out []string
	if plan, _, err := ctx.loadPlan(); err == nil {
		if arr, ok := plan["characters"].([]any); ok {
			for _, x := range arr {
				if m, ok := x.(map[string]any); ok {
					if id := str(m["id"]); id != "" {
						out = append(out, id)
					}
				}
			}
		}
	}
	return out
}

// assignedVoiceFor 角色最终音色 key(2026-08-30 ver15):voiceAssign(同档位差异化
// 变体)优先,缺省回退 autoVoiceFor 档位。懒构建(与 charInfoFor 同风格;主流程
// ensurePlanAndPrompts 已预构建,渲染期只读)。
func (ctx *manjuCtx) assignedVoiceFor(cid string) string {
	if ctx.voiceAssign == nil {
		ctx.voiceAssign = ctx.buildVoiceAssignment()
	}
	if v, ok := ctx.voiceAssign[cid]; ok {
		return v
	}
	return ctx.autoVoiceFor(cid)
}

// autoVoiceFor 自动音色匹配:按角色卡 种族/阵营/性别/年龄 返回音色库 Key
// (voice_lib 权威音色文件,详见 manjuVoiceLibFor)。返回 "" = 无角色卡。
func (ctx *manjuCtx) autoVoiceFor(cid string) string {
	c := ctx.charInfoFor(cid)
	if c == nil {
		return ""
	}
	gender := str(c["gender"])
	age := str(c["age"])
	role := str(c["role"])
	species := str(c["species"])
	// 非人种族(灵宠/妖兽/神兽/精怪/鬼物/机械):萌系活泼声
	if species != "" && species != "人" {
		return "beast_cute"
	}
	// 物品类(2026-08-29 用户硬性规则):有意识器物,萌系声线(中性呆萌)
	if manjuIsItem(c) {
		return "beast_cute"
	}
	// 反派:低沉磁性(男)/冷冽(女),强化辨识度
	if role == "反派" {
		if gender == "女" {
			return "female_deep"
		}
		return "male_deep"
	}
	// 年龄分档(2026-08-30 ver14 修复:age="74岁"/"22岁" 等数字年龄此前全部掉进默认
	// 青年声——白名单只认「老年/少年」等中文词;现与 manjuAgeBand 同档位数字优先:
	// ≥50 老年 / ≥40 中年 / <13 儿童 / <20 少年 / 其他 青年)
	band := ""
	if m := reManjuNumAge.FindStringSubmatch(age); len(m) > 0 {
		if n, aerr := strconv.Atoi(m[1]); aerr == nil && n > 0 {
			switch {
			case n >= 50:
				band = "elder"
			case n >= 40:
				band = "mature"
			case n < 13:
				band = "child"
			case n < 20:
				band = "teen"
			}
		}
	}
	if band == "" {
		child := strings.Contains(age, "儿童") || strings.Contains(age, "孩童") || strings.Contains(age, "幼") || strings.Contains(age, "稚") || strings.Contains(age, "小男") || strings.Contains(age, "小女")
		teen := strings.Contains(age, "少年") || strings.Contains(age, "少女") || strings.Contains(age, "小男孩") || strings.Contains(age, "小女孩") || strings.Contains(age, "萝莉") || strings.Contains(age, "正太")
		elder := strings.Contains(age, "老年") || strings.Contains(age, "老者") || strings.Contains(age, "暮年") || strings.Contains(age, "花甲")
		mature := strings.Contains(age, "中年") || strings.Contains(age, "成熟") || strings.Contains(age, "沉稳") || strings.Contains(age, "大叔") || strings.Contains(age, "御姐") || strings.Contains(age, "妇")
		switch {
		case child:
			band = "child"
		case teen:
			band = "teen"
		case elder:
			band = "elder"
		case mature:
			band = "mature"
		}
	}
	if gender == "女" {
		switch band {
		case "child":
			return "child_girl"
		case "teen":
			return "girl_lively"
		case "elder":
			return "female_elder"
		case "mature":
			return "female_mature"
		default:
			return "female_warm"
		}
	}
	if gender == "男" {
		switch band {
		case "child":
			return "child_boy"
		case "teen":
			return "boy_teen"
		case "elder":
			return "male_elder"
		case "mature":
			return "male_mag"
		default:
			return "male_sun"
		}
	}
	// 性别未知但有角色卡 → 温柔女声兜底(有音色比没有强,与旧版一致)
	return "female_warm"
}

// voiceLibRel 音色库文件相对路径(audio/lib_<key>.mp3)
func voiceLibRel(key string) string {
	return filepath.Join("audio", "lib_"+key+".mp3")
}

// voiceLibAuthoritative 音色库权威文件路径(voice_lib/,2026-08-29 稳定化):
// 音色是跨项目全局资源,权威副本固定在自包含根 voice_lib/,不随项目清理/ComfyUI
// 输入目录波动;InitPaths 未解析(测试/工具调用)时回退旧路径 comfyInput/audio。
func (ctx *manjuCtx) voiceLibAuthoritative(rel string) string {
	if VoiceLibDir != "" {
		return filepath.Join(VoiceLibDir, filepath.FromSlash(rel))
	}
	return filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))
}

// syncVoiceLibToComfy 把权威副本同步到 ComfyUI input(LoadAudio 只能读 input)。
// 幂等(目标存在即跳过),只增不删——input 副本随时可由权威目录重建,误删无害。
func (ctx *manjuCtx) syncVoiceLibToComfy(rel string) {
	src := ctx.voiceLibAuthoritative(rel)
	if !fileExists(src) {
		return
	}
	dst := filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))
	if fileExists(dst) {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	_ = copyFile(src, dst)
}

// genVoiceLibAudio 生成单个风格库音色参考音频:权威副本写入 voice_lib/,再同步副本到
// ComfyUI input/audio(幂等:已存在跳过)。key=音色库 Key(旧版传 edge 音色名的调用经
// manjuVoiceLibFor 兼容解析);派生变体的 Pitch/Rate(童声拔高/老年放缓)随条目带出。
func (ctx *manjuCtx) genVoiceLibAudio(key string) error {
	item := manjuVoiceLibFor(key)
	if item == nil {
		return fmt.Errorf("未知音色: %s", key)
	}
	if ctx.voiceLibDone == nil {
		ctx.voiceLibDone = map[string]bool{}
	}
	if ctx.voiceLibDone[item.Key] {
		return nil // 本 run 已尝试过(成功或失败都记,防重复生成)
	}
	ctx.voiceLibDone[item.Key] = true
	out := ctx.voiceLibAuthoritative(voiceLibRel(item.Key))
	if fileExists(out) {
		ctx.syncVoiceLibToComfy(voiceLibRel(item.Key))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	args := []string{"voice-gen", "--text", manjuVoiceGenText, "--voice", item.Name, "--out", out}
	if item.Pitch != "" {
		// 等号形式:负值(-15Hz)会被 argparse 误判为选项开关,--pitch=-15Hz 稳(实测坑)
		args = append(args, "--pitch="+item.Pitch)
	}
	if item.Rate != "" {
		args = append(args, "--rate="+item.Rate)
	}
	if _, err := ctx.runMediaOut(args...); err != nil {
		return fmt.Errorf("生成音色 %s 失败: %w", item.Key, err)
	}
	// 0 字节防护(2026-08-27 实测):不支持的音色 edge-tts 不报错但产出空文件,
	// LoadAudio 读空 mp3 会 400——生成后校验大小,空文件删除并报错
	if fi, serr := os.Stat(out); serr != nil || fi.Size() == 0 {
		_ = os.Remove(out)
		return fmt.Errorf("生成音色 %s 产出空文件(音色不可用?)", item.Key)
	}
	ctx.syncVoiceLibToComfy(voiceLibRel(item.Key))
	return nil
}

// ensureVoiceLib 预生成风格音色库中全部缺失的参考音频(权威目录 voice_lib/ 生成,
// 再全量同步副本到 ComfyUI input);返回 (生成数, 失败数)
func (ctx *manjuCtx) ensureVoiceLib() (int, int) {
	done, failed := 0, 0
	for _, v := range manjuVoiceLib {
		if err := ctx.genVoiceLibAudio(v.Key); err != nil {
			failed++
			continue
		}
		done++
	}
	return done, failed
}

// voiceBinding 单角色音色绑定(LLM 视角:角色 → <Audio N> 编号;ref_audios 挂载同序)
type voiceBinding struct {
	CharID string
	Audio  string // "<Audio N>"
	SubN   int    // 该角色的 Subject 编号(=登场序,官方 Audio 定义绑 <Subject M> (Sx) 用)
}

// voiceBindingsFor 该镜绑定音色角色的 <Audio N> 编号映射(按登场顺序,≤3;与 charVoiceNames 同序)。
// 注入 genShotPromptRaw 的 data,LLM 据此在 subject_definitions 写音色定义、对白处引用。
func (ctx *manjuCtx) voiceBindingsFor(s manjuShot) []voiceBinding {
	var out []voiceBinding
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		if ctx.charVoiceRef(cid) == "" {
			continue
		}
		out = append(out, voiceBinding{CharID: cid, Audio: fmt.Sprintf("<Audio %d>", len(out)+1), SubN: i + 1})
	}
	return out
}

// ensureVoiceBindings 音色定义兜底(2026-08-26):LLM 生成 h3_prompt 时漏写 <Audio> 定义
// 音色参考就进不了条件编码(实测 H3 音色跟随的前提是 prompt 有 <Audio> 引用)。
// 2026-08-30 五问整改:逐绑定角色检查——只补「该角色尚无 <Audio> 定义」的,不再整体
// bail(此前只要存在任意 1 条 <Audio>(如内心戏画外音定义),同镜登场角色定义缺失也
// 不补 → 说台词的登场角色无音色挂载,与画外共用默认声=配音分不清人物)。
// 2026-08-30 官方格式对齐:定义行绑 <Subject M>(ref-en §2.4 官方示例形态;绑中文名
// 时模型无法把音色与画面里的英文名角色关联=配音乱根源之一);行内 Sx 以登场序占位,
// 渲染前 alignAudioDefs 会按画面段实际发声顺序重写。
func ensureVoiceBindings(hp string, bindings []voiceBinding, c manjuRefContract) string {
	if len(bindings) == 0 || !strings.Contains(hp, "summary:") {
		return hp
	}
	var lines []string
	maxN := maxAudioNum(hp)
	for _, b := range bindings {
		if audioDefExistsFor(hp, b, c) {
			continue
		}
		maxN++
		lines = append(lines, fmt.Sprintf(
			"<Audio %d> is the voice-timbre reference for <Subject %d> (S%d), containing a spoken voiceover.",
			maxN, b.SubN, maxN))
	}
	if len(lines) == 0 {
		return hp
	}
	// subject_definitions 段末尾追加(六段式第一段,以 summary: 为界)
	idx := strings.Index(hp, "summary:")
	return hp[:idx] + strings.Join(lines, "\n") + "\n\n" + hp[idx:]
}

// audioDefExistsFor 该绑定角色是否已有 <Audio> 定义行(按定义目标解析,任意编号;
// LLM 直出绑中文名/对齐后绑 <Subject M> 两种形态都算已定义,防重复补写)
func audioDefExistsFor(hp string, b voiceBinding, c manjuRefContract) bool {
	for _, m := range reAudioDefLine.FindAllStringSubmatch(hp, -1) {
		cid, _ := manjuAudioDefTarget(m[2], c)
		if cid == b.CharID {
			return true
		}
	}
	return false
}

// maxAudioNum h3_prompt 中已存在的最大 <Audio N> 编号(0=无定义;补写/画外注入
// 都从 maxN+1 接续,避免与 LLM 已写编号(含内心戏画外定义)冲突)
func maxAudioNum(hp string) int {
	maxN := 0
	for _, m := range reAudioDefLine.FindAllStringSubmatch(hp, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxN {
			maxN = n
		}
	}
	return maxN
}

// audioNum 从 "<Audio N>" 提取 N
func audioNum(tag string) int {
	n := 0
	_, err := fmt.Sscanf(tag, "<Audio %d>", &n)
	if err != nil {
		return 1
	}
	return n
}

// offscreenVoice 画外说话者(群众/路人喊话,非 narrator 旁白)差异化音色绑定
type offscreenVoice struct {
	Desc string // 声线描述片段(仅诊断/日志用)
	Key  string // 音色库 Key
}

// reOffscreenAnchor 画外音句式锚点(官方 off-screen voiceover 句式的尾部标记)
var reOffscreenAnchor = regexp.MustCompile(`(?i)in an off-?screen voiceover`)

// manjuOffscreenDescs 提取 h3_prompt 中所有 off-screen voiceover 说话者描述段
// (锚点向前取上一个句号后的片段);narrator 旁白跳过(旁白不换音色,2026-08-29
// 用户反馈「路人配音和主角配音都是主角在说话」的修复输入)。
func manjuOffscreenDescs(hp string) []string {
	anchors := reOffscreenAnchor.FindAllStringIndex(hp, -1)
	var out []string
	for _, a := range anchors {
		seg := hp[:a[0]]
		if i := strings.LastIndex(seg, ". "); i >= 0 {
			seg = seg[i+2:]
		}
		low := strings.ToLower(seg)
		if strings.Contains(low, "narrator") {
			continue // 旁白:保持默认叙述音色,不参与差异化
		}
		desc := strings.TrimSpace(seg)
		desc = strings.TrimSuffix(desc, ",")
		if len(desc) > 140 {
			desc = desc[len(desc)-140:]
		}
		if len([]rune(desc)) < 8 {
			continue
		}
		out = append(out, desc)
	}
	return out
}

// manjuOffscreenKey 画外声线描述 → 差异化音色库 Key。与登场角色错开:默认男声用
// male_mag(中年磁性,与主角常配的 male_sun 阳光男声区分),默认女声用 female_mature
// (与 female_warm 区分);语气/年龄词优先(威压→male_deep、童声→child、老年→elder)。
func manjuOffscreenKey(desc string) string {
	low := strings.ToLower(desc)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	// 旁白/叙述(2026-08-30 ver15):独立叙述音色,与所有角色档位区分——此前
	// narrator 无性别词掉默认 male_mag,与中年男角色撞音色(用户实锤「旁白和
	// 角色分不清」)
	if has("narrator") {
		return "male_narrator"
	}
	female := has("woman", "female", "girl", "lady", "her", "she", "grandma", "aunt", "sister", "mrs", "mother")
	male := has("man", "male", "boy", "his", "he", "grandpa", "uncle", "brother", "mr", "father")
	// 注意:不用 "aged"(middle-aged 中年会误判老年)
	old := has("old", "elder", "elderly", "ancient", "grandma", "grandpa", "senior", "wrinkled")
	young := has("young", "kid", "child", "youth", "little", "eager", "teen")
	deep := has("gruff", "deep", "rough", "hoarse", "harsh", "gravelly")
	switch {
	case female && old:
		return "female_elder"
	case female && young:
		return "girl_lively"
	case female && deep:
		return "female_deep"
	case female:
		return "female_mature"
	case male && old:
		return "male_elder"
	case male && young:
		return "boy_teen"
	case male && deep:
		return "male_deep"
	default:
		return "male_mag"
	}
}

// offscreenVoiceKeyFor 画外说话者音色 key(2026-08-30 五问整改,挂载侧单一事实源):
// ①内心戏描述「the quiet inner voice of X」→ 归一匹配登场角色 → 该角色音色(与注入侧
//   innerVoiceFor→autoVoiceFor 同源)——此前挂载侧 manjuOffscreenKey 靠关键词猜音色,
//   呆萌兽音描述无性别/年龄词掉默认 male_mag,EP01 四镜内心戏全挂中年男声(描述与
//   参考音频自相矛盾=「配音分不清人物」直接来源);
// ②旁白「narrator ... calm, neutral storytelling voice」→ 叙述音色(与注入侧同源);
// ③其余(路人/群杂/环境喊话)才按声线关键词猜测(回退既有逻辑)。
func (ctx *manjuCtx) offscreenVoiceKeyFor(offDesc string, c manjuRefContract) string {
	low := strings.ToLower(offDesc)
	if i := strings.Index(low, "the quiet inner voice of "); i >= 0 {
		name := strings.TrimSpace(offDesc[i+len("the quiet inner voice of "):])
		if j := strings.IndexAny(name, ",，"); j >= 0 {
			name = name[:j]
		}
		if cid := charIDMatch(name, c); cid != "" {
			if k := ctx.autoVoiceFor(cid); k != "" {
				return k
			}
		}
	}
	if strings.Contains(low, "narrator") {
		return "male_narrator"
	}
	return manjuOffscreenKey(offDesc)
}

// manjuOffscreenBindings 该镜画外说话者绑定列表(按出现顺序;与 charVoiceNames 的
// <Audio N> 编号接续登场角色之后,一一对应)
// manjuOffscreenBindings 画外说话者差异化音色绑定清单。2026-08-30 ver14(问题⑥
// 内心戏音色):内心戏镜(innerCid 非空)的 narrator 画外音句不再跳过——绑定该角色
// 音色库 key,desc 改写为「角色内心声线」描述(H3 按描述区分内心戏与客观旁白/
// 对白:内心=角色声线基底 + quiet inner voice 语气,对白=同源声线+场上语气)。
func (ctx *manjuCtx) manjuOffscreenBindings(hp string, innerCid, innerKey string) []offscreenVoice {
	descs := manjuOffscreenDescs(hp)
	narrDescs := manjuNarratorDescs(hp)
	if innerKey != "" {
		// 内心戏镜: narrator 句=该角色内心,绑定角色音色(ver14)
		for _, d := range narrDescs {
			descs = append(descs, "inner:"+d)
		}
	} else if len(narrDescs) > 0 {
		// 客观旁白镜(2026-08-30 ver15):旁白绑定独立叙述音色——此前 narrator 不绑定
		// =H3 默认声易与角色撞(用户实锤「玄经理陈守家旁白全用同一个」),现在旁白
		// 固定 male_narrator 叙述声,与所有角色档位区分
		for _, d := range narrDescs {
			descs = append(descs, "narr:"+d)
		}
	}
	if len(descs) == 0 {
		return nil
	}
	out := make([]offscreenVoice, 0, len(descs))
	seen := map[string]bool{}
	phrase := ""
	if innerCid != "" {
		phrase = ctx.voiceTimbrePhrase(innerCid)
	}
	for _, d := range descs {
		var key, desc string
		if strings.HasPrefix(d, "inner:") {
			desc = "the quiet inner voice of " + innerCid
			if phrase != "" {
				desc += ", " + phrase
			}
			key = innerKey
		} else if strings.HasPrefix(d, "narr:") {
			desc = "the narrator with a calm, neutral storytelling voice"
			key = "male_narrator"
		} else {
			key = manjuOffscreenKey(d)
			desc = d
		}
		if seen[key] && len(out) > 0 {
			// 同音色画外说话者只绑一次(H3 音色参考一个就够,防 ref_audios 冗余)
			continue
		}
		seen[key] = true
		out = append(out, offscreenVoice{Desc: desc, Key: key})
	}
	return out
}

// manjuNarratorDescs 提取 narrator 画外音句描述(off-screen voiceover 句中带
// narrator 的):内心戏与客观旁白都由 narrator 句式念出(脚本直出/LLM 规则),
// 渲染端区分处理——内心戏绑定角色音色(ver14)、客观旁白绑定叙述音色(ver15)。
func manjuNarratorDescs(hp string) []string {
	if !strings.Contains(hp, "narrator") {
		return nil
	}
	anchors := reOffscreenAnchor.FindAllStringIndex(hp, -1)
	var out []string
	for _, a := range anchors {
		seg := hp[:a[0]]
		if i := strings.LastIndex(seg, ". "); i >= 0 {
			seg = seg[i+2:]
		}
		low := strings.ToLower(seg)
		if !strings.Contains(low, "narrator") {
			continue
		}
		desc := strings.TrimSpace(seg)
		desc = strings.TrimSuffix(desc, ",")
		if len(desc) > 140 {
			desc = desc[len(desc)-140:]
		}
		if len([]rune(desc)) < 8 {
			continue
		}
		out = append(out, desc)
	}
	return out
}

// innerVoiceFor 内心戏角色与其音色库 key(2026-08-30 ver14,问题⑥):从 narration 的
// 「内心·角色名:」前缀提取角色,归一匹配登场角色后取 autoVoiceFor 音色。
// 非内心戏镜返回 ("","")。
func (ctx *manjuCtx) innerVoiceFor(s manjuShot) (string, string) {
	for _, line := range strings.Split(s.Narration, "\n") {
		i := strings.Index(line, "内心·")
		if i < 0 {
			continue
		}
		rest := line[i+len("内心·"):]
		j := strings.IndexAny(rest, "：:")
		if j <= 0 {
			continue
		}
		name := strings.TrimSpace(rest[:j])
		for _, cid := range s.Characters {
			if cid == name || strings.HasSuffix(cid, "·"+name) || strings.HasSuffix(name, "·"+cid) {
				if k := ctx.autoVoiceFor(cid); k != "" {
					return cid, k
				}
			}
		}
		if k := ctx.autoVoiceFor(name); k != "" {
			return name, k
		}
	}
	return "", ""
}

// injectOffscreenVoiceBindings 在 subject_definitions 段末追加画外说话者的 <Audio N>
// 定义(接续登场角色编号)。幂等:已注入过画外定义(<Audio N> ... off-screen voice
// described as)则直接返回——同一镜重 finalize 时描述不变,不重复追加。
func injectOffscreenVoiceBindings(hp string, obs []offscreenVoice, startN int) string {
	if len(obs) == 0 || strings.Contains(hp, "for the off-screen voice described as") {
		return hp
	}
	idx := strings.Index(hp, "summary:")
	if idx < 0 {
		return hp
	}
	var lines []string
	n := startN
	for _, ob := range obs {
		n++
		lines = append(lines, fmt.Sprintf(
			"<Audio %d> is the voice-timbre reference for the off-screen voice described as %s, containing a spoken voiceover.",
			n, ob.Desc))
	}
	return hp[:idx] + strings.Join(lines, "\n") + "\n\n" + hp[idx:]
}

// reAudioDefLine <Audio N> 定义行(音色绑定的 prompt 侧来源,charVoiceNames 据此挂载
// ref_audios,保证编号/顺序与注入完全一致)
var reAudioDefLine = regexp.MustCompile(`(?m)^<Audio (\d+)> is the voice-timbre reference for (.+?), containing a spoken voiceover\.$`)

// charVoiceNames 该镜挂载的配音音色音频(ComfyUI input 相对路径),与 prompt 的
// <Audio N> 编号一一对应(2026-08-29 重写:单一来源=最终化 prompt 的 <Audio> 定义行,
// 不再按角色列表推导——画外说话者的 <Audio> 定义与登场角色同一机制注入,挂载顺序
// 天然一致;解析不到定义时回退旧逻辑(登场角色顺序)。)
func (ctx *manjuCtx) charVoiceNames(s manjuShot) []string {
	hp := s.H3Prompt
	if hp != "" {
		lines := reAudioDefLine.FindAllStringSubmatch(hp, -1)
		if len(lines) > 0 {
			byNum := make(map[int]string, len(lines))
			for _, m := range lines {
				n, err := strconv.Atoi(m[1])
				if err != nil || n < 1 {
					continue
				}
				byNum[n] = m[2]
			}
			maxN := 0
			for n := range byNum {
				if n > maxN {
					maxN = n
				}
			}
			var out []string
			c := ctx.refContractFor(s)
			for n := 1; n <= maxN; n++ {
				who := byNum[n]
				if who == "" {
					continue
				}
				rel := ""
				cid, offDesc := manjuAudioDefTarget(who, c)
					if offDesc != "" {
						// 画外说话者:音色 key 走单一事实源(内心→角色音色/旁白→叙述音色/
						// 其余→声线关键词,与注入侧同源;2026-08-30 五问整改)
						key := ctx.offscreenVoiceKeyFor(offDesc, c)
					if key != "" {
						rel = "audio/lib_" + key + ".mp3"
						if !fileExists(filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))) {
							if !fileExists(ctx.voiceLibAuthoritative(rel)) {
								if err := ctx.genVoiceLibAudio(key); err != nil {
									rel = ""
								}
							} else {
								ctx.syncVoiceLibToComfy(rel)
							}
						}
						if rel != "" {
							rel = filepath.ToSlash(rel)
						}
					}
				} else if cid != "" {
					rel = ctx.charVoiceRef(cid)
				}
				if rel != "" {
					out = append(out, rel)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// 回退:无 <Audio> 定义(旧方案/未 finalize)时按登场角色顺序
	var out []string
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		if rel := ctx.charVoiceRef(cid); rel != "" {
			out = append(out, rel)
		}
	}
	return out
}

// refRelFor 角色参考图相对路径:优先正脸特写(身份锁定强),缺省回退全身定妆照,都没有则空串
func (ctx *manjuCtx) refRelFor(cid string) string {
	rel := "characters/" + cid + "_face.png"
	if fileExists(filepath.Join(ctx.assetsDir, rel)) {
		return rel
	}
	rel = "characters/" + cid + ".png"
	if fileExists(filepath.Join(ctx.assetsDir, rel)) {
		return rel
	}
	return ""
}

// refStamp 参考图时效戳(路径@mtime纳秒:大小):采纳/重生成参考图后指纹自动变化,条件缓存随之重建
func (ctx *manjuCtx) refStamp(rel string) string {
	fi, err := os.Stat(filepath.Join(ctx.assetsDir, rel))
	if err != nil {
		return rel
	}
	return fmt.Sprintf("%s@%d:%d", rel, fi.ModTime().UnixNano(), fi.Size())
}

func (ctx *manjuCtx) sceneRefName(s manjuShot) string {
	if s.Scene == "" {
		return ""
	}
	rel := "scenes/" + s.Scene + ".png"
	if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
		return ""
	}
	name := fmt.Sprintf("dir_scene_%d.png", s.ID)
	_ = copyFile(filepath.Join(ctx.assetsDir, rel), filepath.Join(ctx.comfyInput, name))
	return name
}

// ---- 渲染 ----

func stageRender(ctx *manjuCtx, lg *manjuLogger) error {
	// ComfyUI 未运行则自动拉起(启动自动拉起外的渲染前兜底)
	if err := ctx.ensureComfyReady(lg); err != nil {
		return err
	}
	// SageAttn 节点缺失提前降级(生效参数与后续渲染一致;renderShotTo 内兜底所有路径)
	ctx.sageAttnGuard(lg)
	_, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	if len(selected) == 0 {
		lg.logf("  ⏭ 没有需要处理的镜头")
		return nil
	}
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if err := os.MkdirAll(clipsEp, 0755); err != nil {
		return err
	}
	// 目标集镜头全集(仅用于进度分母与接缝序号:接缝按全集序号)
	allShots := shots
	idxOf := map[int]int{}
	for i, s := range allShots {
		idxOf[s.ID] = i + 1
	}
	// 质检自愈:最近一次质检未过的镜头,下次渲染自动删旧重渲(坏产物不再卡死整条管线)。
	// 定点重渲:镜头框(only)显式指定 = 强制重渲,已有产物也覆盖(文档语义「局部重做/重渲失败镜」)。
	qcFailed := ctx.qcFailedShots()
	forceRR := ctx.only != ""
	if forceRR {
		lg.logf("  ♻️ 定点重渲: 镜头 " + ctx.only + " 已显式指定,已有产物将覆盖重渲")
	} else if len(qcFailed) > 0 {
		ids := make([]string, 0, len(qcFailed))
		for n := range qcFailed {
			ids = append(ids, strconv.Itoa(n))
		}
		sort.Strings(ids)
		lg.logf("  ♻️ 质检自愈: 上次未过镜头 " + strings.Join(ids, ",") + " 自动删旧重渲")
	}
	for i, s := range selected {
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}
		lg.logf(fmt.Sprintf("[%d/%d] 镜头 %d: [%s] %s", i+1, len(selected), s.ID, s.Scene, s.Camera))
		dst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
		if fileExists(dst) {
			if fi, err := os.Stat(dst); err == nil && fi.Size() == 0 {
				// 中断残留的 0 字节文件:跳过会让坏产物混进成片,QC 阶段才暴露,直接删除重渲
				_ = os.Remove(dst)
				lg.logf("  ⚠️ 发现 0 字节残留,删除重渲: " + dst)
			} else if ctx.shotManifestStatus(s) == "stale" {
				// 产物过期(提示词/定妆照/场景图/画幅已变):旧镜头会被跳过复用,必须删旧重渲
				lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 产物已过期(输入已变),删旧重渲(含条件缓存)")
				ctx.clearShotArtifacts(s)
			} else if forceRR {
				// 定点重渲:镜头框显式指定,覆盖已有产物(条件缓存复用:输入未变无需重编码)
				_ = os.Remove(dst)
				lg.logf("  ♻️ 镜头 " + strconv.Itoa(s.ID) + " 定点重渲(覆盖旧产物)")
			} else if qcFailed[s.ID] {
				// 质检自愈:上次质检未过的坏产物删旧重渲。重渲换 seed(见 rerunAttempt),
				// 否则 fixed seed 下同 seed 同画面,质检永远不过;轮数封顶后提示逃生门
				reruns := ctx.qcRerender[s.ID]
				if reruns >= 2 {
					lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 已重渲 " + strconv.Itoa(reruns) + " 次仍未过质检——建议用中断横幅「跳过失败镜/接受并合成」,或检查该镜提示词/参考图")
					continue
				}
				ctx.qcRerender[s.ID] = reruns + 1
				_ = os.Remove(dst)
				lg.logf("  ♻️ 镜头 " + strconv.Itoa(s.ID) + " 质检未过,换 seed 重渲(第 " + strconv.Itoa(reruns+1) + " 次)")
			} else {
				lg.logf("  跳过（已存在）: " + dst)
				continue
			}
		}
		// 流水线预编码:当前镜渲染等待期间,后台预提交下一镜的 Qwen3-VL 编码
		// (GPU 渲染与文本编码可并行,镜头间空窗从「编码+渲染」串行缩短为约一帧渲染时长)
		var preErr error
		var preWg sync.WaitGroup
		if i+1 < len(selected) {
			next := selected[i+1]
			nextDst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", next.ID))
			if !fileExists(nextDst) {
				preWg.Add(1)
				safeGo("preencode", lg, func() {
					defer preWg.Done()
					preErr = ctx.ensureEncoded(next, ctx.shotCacheName(next), lg)
				})
			}
		}
		// 该镜重渲轮次:质检自愈重渲用它换 seed(seedFor 在 fixed 策略下 attempt>0 也 +轮次),
		// 避免同 seed 同画面"重渲了但质检还是不过"的死循环;普通镜头沿用 forceAttempt
		rerunAttempt := ctx.forceAttempt
		if ctx.qcRerender[s.ID] > 0 {
			rerunAttempt = ctx.qcRerender[s.ID]
		}
		if err := ctx.renderShotTo(s, idxOf[s.ID], false, clipsEp, ctx.w, ctx.h, rerunAttempt, lg); err != nil {
			preWg.Wait()
			return err
		}
		// 等预编码收尾(通常渲染期间早已完成);失败在下一镜自己的 ensureEncoded 处暴露
		preWg.Wait()
		if preErr != nil {
			lg.logf("  ⚠️ 下一镜预编码失败(下一镜将串行重试): " + truncate(preErr.Error(), 100))
		}
	}
	// 质检报告已被本次渲染消费(失败镜头已重渲,重检前不再触发重复重渲)
	ctx.qcReportClear()
	lg.logf("🎉 渲染完成 -> " + clipsEp)
	return nil
}

// renderSingleShot 渲染单个镜头到定稿目录(定稿分辨率)。
func (ctx *manjuCtx) renderSingleShot(s manjuShot, idx int, fresh bool, lg *manjuLogger) error {
	return ctx.renderShotTo(s, idx, fresh, filepath.Join(ctx.clipsDir, ctx.episode), ctx.w, ctx.h, ctx.forceAttempt, lg)
}

// renderShotTo 渲染单个镜头(编码→提交→等待→取回;中断自动重试一次)。
// stageRender 与 Agent 流水线(单镜渲完即审)共用;dstDir/w/h/attempt 支持
// 草稿预审(半分辨率草稿)与 seed 重试策略(非 fixed 策略按 attempt 换 seed)。
// fresh=true 时独立生成不接缝(返工重渲镜:其首渲的接缝 latent 已被本次覆盖,
// 且下游镜基于旧 latent,再接缝只会放大跳变)。
func (ctx *manjuCtx) renderShotTo(s manjuShot, idx int, fresh bool, dstDir string, w, h, attempt int, lg *manjuLogger) error {
	// 2026-08-25 用户规则:渲染输入最终化(违规词同义/谐音替换 + 多余人脸硬约束),
	// 先于缓存指纹与预编码——指纹/编码/渲染三处用同一份最终化文本保持一致
	s.H3Prompt = ctx.finalizeShotPrompt(s.H3Prompt, s, lg)
	// SageAttn 节点缺失自动降级(覆盖 stageRender/Agent 流水线/定点返工/草稿预审全部渲染路径)
	ctx.sageAttnGuard(lg)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	dst := filepath.Join(dstDir, fmt.Sprintf("%02d.mp4", s.ID))
	cacheName := ctx.shotCacheNameAt(s, w, h)
	if err := ctx.ensureEncodedAt(s, cacheName, w, h, lg); err != nil {
		return fmt.Errorf("镜头 %d 预编码失败: %w", s.ID, err)
	}
	// 崩溃恢复:上次「已提交未收产物」的任务先尝试收回(已完成免重渲/在跑的等完再收/丢失的重新提交)。
	// 注意:定点重渲/质检自愈/agent 返工(attempt>0 或 fresh)必须先删旧产物再重渲,
	// 若此时 tryReclaim 从 history 收回旧任务的产物,会把"已决定重渲"的镜头静默替换成旧产物——
	// 必须跳过收回,直接重新提交。
	ckKey := strconv.Itoa(s.ID)
	if dstDir == ctx.draftDir() {
		ckKey += "@d"
	}
	if attempt == 0 && !fresh {
		if old := ctx.renderCKGet(ckKey); old != "" {
			if ok2, rerr := ctx.tryReclaim(old, dst, lg); ok2 {
				ctx.renderCKClear(ckKey)
				return nil
			} else if rerr != nil {
				lg.logf("  ⚠️ 上次未收产物的任务无法恢复(" + truncate(rerr.Error(), 120) + "),重新提交")
				ctx.renderCKClear(ckKey)
			} else {
				lg.logf("  ⚠️ 上次任务已丢失(ComfyUI 重启),重新提交")
				ctx.renderCKClear(ckKey)
			}
		}
	} else if ctx.renderCKGet(ckKey) != "" {
		// 重渲路径:旧检查点已无意义,直接清除,避免后续误命中
		ctx.renderCKClear(ckKey)
	}
	chained := !fresh && idx > 1 && fileExists(h3ContextLatentPath(ctx.comfyOutput, ctx.latentNS(), idx-1))
	if fresh {
		lg.logf("  ♻️ 镜头 " + strconv.Itoa(s.ID) + " 返工重渲:独立生成(不接缝)")
	}
	// 审计 P3:停止后严禁再提交——tryReclaim 等待期间用户可能已点停止,
	// 此时重提交会让 ComfyUI 继续烧 GPU 且无人打断
	if lg.stopped() {
		return fmt.Errorf("已停止")
	}
	submit := func() (string, error) {
		seed := ctx.seedFor(s.ID, attempt)
		if attempt > 0 && ctx.seedPolicy != "fixed" {
			lg.logf(fmt.Sprintf("  🎲 镜头 %d 第 %d 次尝试 seed=%d(策略 %s)", s.ID, attempt+1, seed, ctx.seedPolicy))
		}
		// 审计 3.3:R 副本注入 sage 结果(不写共享 ctx.R,防预编码 goroutine 并发读)
		rCopy := make(map[string]any, len(ctx.R)+2)
		for k, v := range ctx.R {
			rCopy[k] = v
		}
		ctx.applySageToR(rCopy)
		ctx.pddGuard(lg) // PDD 节点探测(每 run 一次)
		ctx.applyPddToR(rCopy)
		wf := h3RenderWorkflow(rCopy, seed, w, h, h3Length(s.Duration, ctx.fps),
			ctx.steps, cacheName, len(s.Characters) > 0, chained, idx-1, idx)
		return ctx.comfy.submit(wf)
	}
	pid, err := submit()
	if err != nil {
		return fmt.Errorf("镜头 %d 提交失败: %w", s.ID, err)
	}
	ctx.renderCKSet(ckKey, pid) // 提交即落盘:崩溃后可按 prompt_id 收回,绝不重复烧 GPU
	lg.logf("  渲染提交 " + pid[:8] + "...")
	t0 := time.Now()
	if err := ctx.comfy.wait(pid, 3600*time.Second, 10*time.Second, lg.stopped); err != nil {
		// 渲染异常/中断:不自动重渲染——直接返回错误,由管线收尾(用户可手动续跑/定点重渲)。
		// 旧逻辑"中断自动重试一次"会在 ComfyUI 异常/外部中断时静默重新提交,
		// 用户感知为"异常了还在自动烧 GPU"(升级优化:异常即停,不自动重试)
		// 清检查点:该任务已失败(非"未收产物"),避免下次续跑 tryReclaim 误收失败产物
		ctx.renderCKClear(ckKey)
		return fmt.Errorf("镜头 %d 渲染失败: %w", s.ID, err)
	}
	entry := ctx.comfy.history(pid)
	rel := comfyOutputVideo(entry)
	if rel == "" {
		return fmt.Errorf("镜头 %d 任务完成但未找到视频输出", s.ID)
	}
	if err := copyFile(filepath.Join(ctx.comfyOutput, rel), dst); err != nil {
		return fmt.Errorf("镜头 %d 复制视频失败: %w", s.ID, err)
	}
	ctx.renderCKClear(ckKey) // 产物已收,检查点使命完成
	if dstDir == filepath.Join(ctx.clipsDir, ctx.episode) {
		ctx.manifestMark(s, chained) // 定稿产物入清单(时效追踪;草稿不入)
	}
	lg.logf(fmt.Sprintf("  ✅ 镜头 %d 完成（耗时 %.1f 分钟）-> %s", s.ID, time.Since(t0).Minutes(), dst))
	return nil
}

// ---- 质检(venv PyAV) ----

func (ctx *manjuCtx) runMedia(lg *manjuLogger, args ...string) error {
	script := ensureMediaHelper()
	argv := append([]string{script}, args...)
	cmd := exec.Command(manjuPythonPath(), argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	// 审计 M3:子进程超时 + 停止感知——此前无超时且不查 lg.stopped(),
	// whisper/ffmpeg 挂死时停止无效、任务永久 running、续跑被全局闸门阻塞
	done := make(chan struct{})
	var exitCode int = -1
	go func() {
		if st, err := cmd.Process.Wait(); err == nil && st != nil {
			exitCode = st.ExitCode()
		}
		close(done)
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var tail []string // 输出尾部(错误时带异常堆栈,便于定位,如 facecrop 的 UnicodeEncodeError)
	scanDone := make(chan struct{})
	go func() {
		for sc.Scan() {
			line := sc.Text()
			lg.logf(line)
			tail = append(tail, line)
			if len(tail) > 12 {
				tail = tail[len(tail)-12:]
			}
		}
		close(scanDone)
	}()
	timer := time.NewTimer(25 * time.Minute)
	defer timer.Stop()
	stopTick := time.NewTicker(2 * time.Second)
	defer stopTick.Stop()
	for {
		select {
		case <-done:
			<-scanDone
			if exitCode != 0 {
				detail := strings.Join(tail, "\n")
				if len(detail) > 600 {
					detail = detail[len(detail)-600:]
				}
				return fmt.Errorf("媒体处理退出码非 0(%d)%s", exitCode, func() string {
					if strings.TrimSpace(detail) == "" {
						return ""
					}
					return ": " + strings.TrimSpace(detail)
				}())
			}
			return nil
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-scanDone
			return fmt.Errorf("媒体处理超时(>25 分钟),已终止")
		case <-stopTick.C:
			if lg.stopped() {
				_ = cmd.Process.Kill()
				<-scanDone
				return fmt.Errorf("已停止")
			}
		}
	}
}

// runVoiceover 旁白/画外音后期配音(2026-08-23:H3 本地对画面外音/旁白不生成音轨,
// edge-tts 兜底;失败返回错误,调用方跳过不阻断合成)
func (ctx *manjuCtx) runVoiceover(lg *manjuLogger) error {
	plan := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")
	if !fileExists(plan) {
		return fmt.Errorf("无方案文件 %s", plan)
	}
	args := []string{"voiceover", "--plan", plan, "--clips-dir", filepath.Join(ctx.clipsDir, ctx.episode), "--fps", strconv.Itoa(ctx.fps)}
	// ffmpeg 优先 ComfyUI venv(与渲染同源),否则依赖 PATH
	if ff := filepath.Join(ComfyRootDir, ".venv", "Scripts", "ffmpeg.exe"); fileExists(ff) {
		args = append(args, "--ffmpeg", ff)
	}
	lg.logf("🎙 旁白/画外音后期配音(edge-tts)...")
	return ctx.runMedia(lg, args...)
}

// hasTopLevelClips 集目录是否有顶层镜头 mp4(排除 _draft/2k 等工作子目录)
func hasTopLevelClips(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			return true
		}
	}
	return false
}

func stageQC(ctx *manjuCtx, lg *manjuLogger) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if !hasTopLevelClips(clipsEp) {
		// 该集无镜头(如全本自动分集下尚未渲染的集):无事可检,跳过而非报错
		lg.logf("  ⏭ 该集无镜头可质检，跳过")
		return nil
	}
	args := []string{"qc", "--dir", clipsEp}
	// 2026-08-27 升级:传 plan 供静音分级(有台词镜静音=真丢台词才判失败,纯空镜静音
	// 降软告警);段尾冻结自动截尾默认开(H3 固有特性,程序修优于换 seed 重渲烧 GPU),
	// render.defreeze=false 可关。
	if p := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"); fileExists(p) {
		args = append(args, "--plan", p)
	}
	if b, _ := ctx.R["defreeze"].(bool); !b {
		args = append(args, "--no-defreeze")
	}
	if ctx.only != "" {
		// 定点质检:镜头框显式指定时只检选中镜头(与定点重渲语义一致);范围展开为单号
		shots := expandShotList(ctx.only)
		args = append(args, "--shots", shots)
		lg.logf("  🔍 定点质检镜头: " + shots)
	}
	// 质检报告落盘(渲染阶段据此自动重渲未过镜头,「一条龙/续跑」可自行走通到合成)
	reportPath := ctx.qcReportPath()
	_ = os.MkdirAll(filepath.Dir(reportPath), 0755)
	args = append(args, "--json", reportPath)
	err := ctx.runMedia(lg, args...)
	// 跳过镜头(用户决定不修,质检不计失败、合成时排除):从失败集中剔除
	skip := ctx.qcSkipSet()
	failed := ctx.qcFailedShots()
	// 基础设施失败防线(审计 S4):脚本崩溃/python 缺失/报告未落盘 → 失败集为空,
	// 此时 err != nil 必须显式报错,绝不静默"通过"放坏片进成片
	if err != nil && len(failed) == 0 {
		return fmt.Errorf("质检执行失败: %w", err)
	}
	if len(skip) > 0 {
		ids := make([]string, 0, len(skip))
		for n := range skip {
			ids = append(ids, strconv.Itoa(n))
		}
		sort.Strings(ids)
		lg.logf("  ⏭ 已跳过镜头 " + strings.Join(ids, ",") + "(用户决定,不计质检失败、合成时排除)")
		for n := range skip {
			delete(failed, n)
		}
	}
	// 2026-08-26 升级:视觉抽检——对通过镜头抽帧判画面崩坏,命中追加 QC 报告触发重渲
	ctx.qcVisualCheck(lg, reportPath, failed)
	failed = ctx.qcFailedShots()
	if len(failed) > 0 {
		ids := make([]string, 0, len(failed))
		for n := range failed {
			ids = append(ids, strconv.Itoa(n))
		}
		sort.Strings(ids)
		lg.logf("  🔁 质检未过镜头 " + strings.Join(ids, ",") + " — 再点「一条龙/续跑」自动删旧重渲;或镜头框填编号(+集数)定点重渲")
		// 静音丢台词镜:换 seed 重渲未必念出台词,render.voiceover=true 可走 edge-tts
		// 后期补配音(只补静音镜,不会与 H3 原生音轨双声)——给用户一条明路
		if data, err := os.ReadFile(reportPath); err == nil {
			if strings.Contains(string(data), "静音丢台词") {
				lg.logf("  💡 有镜头静音丢台词:换 seed 重渲未必解决,可在设置中开启「后期配音」(render.voiceover)自动给静音镜补 TTS 人声")
			}
		}
		if ctx.qcAccept() {
			// 逃生门:用户已明确「接受质检结果」(坏镜进成片由用户决策),打警告继续
			lg.logf("  ✅ 已按用户决定「接受质检结果」,未过镜头将进成片 — 如需重渲请镜头框填编号")
			ctx.qcAcceptClear()
			return nil
		}
		// 修复 M6:err 为 nil(脚本 exit 0 但报告含失败镜)时不得包装出 "%!w(<nil>)"
		if err != nil {
			return fmt.Errorf("质检未通过(%d 镜): %w", len(failed), err)
		}
		return fmt.Errorf("质检未通过(%d 镜)", len(failed))
	}
	// 无失败(或失败镜头全部被用户跳过):视为通过(忽略 runMedia 因被跳过镜头产生的 exit 1)
	return nil
}

// ---- 质检逃生门(用户决策:跳过失败镜 / 接受质检结果) ----

// qcSkipSet 用户决定跳过的镜头:质检不计失败、合成时排除(来源 config.render.qc_skip_shots)
func (ctx *manjuCtx) qcSkipSet() map[int]bool {
	s := strings.TrimSpace(str(ctx.R["qc_skip_shots"]))
	if s == "" {
		return nil
	}
	out := map[int]bool{}
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// qcSkipList 跳过镜头逗号分隔串(合成脚本 --skip-shots 用;空=无)
func (ctx *manjuCtx) qcSkipList() string {
	skip := ctx.qcSkipSet()
	if len(skip) == 0 {
		return ""
	}
	ids := make([]int, 0, len(skip))
	for n := range skip {
		ids = append(ids, n)
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, n := range ids {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// qcAccept 用户是否已决定接受本次质检结果(失败不阻断,坏镜进成片由用户决策)
func (ctx *manjuCtx) qcAccept() bool {
	b, _ := ctx.R["qc_accept"].(bool)
	return b
}

// qcAcceptClear 消费后清除接受标记(一次性决策,避免后续质检永久不报错)
func (ctx *manjuCtx) qcAcceptClear() {
	// 审计 S8:逃生门标记消费与其它写 config 端点串行化,防 read-modify-write 互相覆盖
	mu := manjuConfigLock(ctx.configPath)
	mu.Lock()
	defer mu.Unlock()
	R := ctx.R
	delete(R, "qc_accept")
	cfg := ctx.cfg
	cfg["render"] = R
	_ = writeManjuConfig(ctx.configPath, cfg)
}

// ---- 质检报告(渲染自愈依据) ----

// expandShotList 把镜头筛选串展开为逗号分隔的单个编号(供质检 --shots 等)
// "1-3,5" → "1,2,3,5";空串返回空
func expandShotList(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	seen := map[int]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := strings.Index(p, "-"); i > 0 {
			a, _ := strconv.Atoi(strings.TrimSpace(p[:i]))
			b, _ := strconv.Atoi(strings.TrimSpace(p[i+1:]))
			if a <= 0 {
				a = 1
			}
			if b < a {
				a, b = b, a
			}
			for n := a; n <= b; n++ {
				seen[n] = true
			}
		} else if n, err := strconv.Atoi(p); err == nil {
			seen[n] = true
		}
	}
	ids := make([]int, 0, len(seen))
	for n := range seen {
		ids = append(ids, n)
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, n := range ids {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// qcReportPath 质检报告路径:<workdir>/qc/<ep>_qc.json(逐集独立,渲染阶段消费后清除)
func (ctx *manjuCtx) qcReportPath() string {
	return filepath.Join(ctx.workdir, "qc", ctx.episode+"_qc.json")
}

// qcFailedShots 读取最近一次质检报告中的失败镜头号(无报告/文件损坏返回空)
func (ctx *manjuCtx) qcFailedShots() map[int]bool {
	data, err := os.ReadFile(ctx.qcReportPath())
	if err != nil {
		return nil
	}
	var rep struct {
		Shots map[string]struct {
			OK bool `json:"ok"`
		} `json:"shots"`
	}
	if json.Unmarshal(data, &rep) != nil || rep.Shots == nil {
		return nil
	}
	out := map[int]bool{}
	for name, s := range rep.Shots {
		if s.OK {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(name, filepath.Ext(name))); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// qcReportClear 清除质检报告:渲染阶段消费后调用,避免未重检前重复触发重渲
func (ctx *manjuCtx) qcReportClear() {
	_ = os.Remove(ctx.qcReportPath())
}

// ---- 合成 ----

func stageAssemble(ctx *manjuCtx, lg *manjuLogger) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if !hasTopLevelClips(clipsEp) {
		// 该集无镜头(如全本自动分集下尚未渲染的集):无事可合成,跳过
		lg.logf("  ⏭ 该集无镜头可合成，跳过")
		return nil
	}
	// 审计 1.3:合成前比对方案镜头数与产物数——渲染中断/失败后直接点合成,
	// 成片会静默缺镜;缺镜时明确警告(不阻断,用户可见后再决定是否续跑补渲)
	if plan, _, perr := ctx.loadPlan(); perr == nil {
		planned := 0
		if arr, ok := plan["shots"].([]any); ok {
			for _, x := range arr {
				if m, ok := x.(map[string]any); ok {
					if m["take_group"] == nil { // 多切点长镜组头才算一镜(与渲染口径一致)
						planned++
					}
				}
			}
		}
		if planned > 0 {
			rendered := 0
			if entries, err := os.ReadDir(clipsEp); err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
						rendered++
					}
				}
			}
			if rendered < planned {
				lg.logf(fmt.Sprintf("  ⚠️ 警告: 方案 %d 镜,当前仅 %d 个镜头产物——成片将缺少 %d 镜(若渲染中断,请先续跑/定点重渲再合成)",
					planned, rendered, planned-rendered))
			}
		}
	}
	out := filepath.Join(ctx.workdir, ctx.episode+"_成片.mp4")
	mosaic := 0
	if MOD, ok := ctx.cfg["moderation"].(map[string]any); ok {
		if enabled, _ := MOD["mosaic_enabled"].(bool); enabled {
			mosaic = 16
			if n, ok := manjuToInt(MOD["mosaic_level"]); ok {
				mosaic = n
			}
		}
	}
	args := []string{"assemble", "--clips-dir", clipsEp, "--out", out, "--episode", ctx.episode, "--fps", strconv.Itoa(ctx.fps)}
	if mosaic > 0 {
		args = append(args, "--mosaic", strconv.Itoa(mosaic))
	}
	args = append(args, "--plan", filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"))
	// 字幕烧录开关(2026-08-24 默认关):H3 视频自带原生对白音轨(32kHz 立体声),
	// 烧录字幕多余(画面+口型+字幕三重信息)且触发成片终检 OCR 误报"字幕位文字"。
	// render.subtitle=true 才显式烧录;缺省/false 一律不烧。
	if sub, ok := ctx.R["subtitle"].(bool); !ok || !sub {
		args = append(args, "--no-subtitle")
	}
	// 2026-08-26 升级:默认导出对白/旁白 srt(成片同目录,不烧录),供上传平台/剪映用;
	// render.srt_out=false 可关闭。
	if so, ok := ctx.R["srt_out"].(bool); !ok || so {
		args = append(args, "--srt", strings.TrimSuffix(out, filepath.Ext(out))+".srt")
	}
	// 转场 + BGM(数据驱动配置;seam 接缝镜清单传给脚本强制硬切,叠化重影防线)
	// 2026-08-27 默认 cut→dissolve(用户反馈"镜头间不连贯像PPT"):非接缝边界默认轻叠化
	// 增强连贯性,接缝镜自动硬切不受影响;显式配置 render.transition 仍优先。
	trans := orDefault(str(ctx.R["transition"]), "dissolve")
	if !manjuTransitions[trans] {
		trans = "cut"
	}
	args = append(args, "--transition", trans)
	if hc := ctx.seamHardCuts(); hc != "" {
		args = append(args, "--hard-cuts", hc)
	}
	if bgm := strings.TrimSpace(str(ctx.R["bgm"])); bgm != "" {
		args = append(args, "--bgm", bgm)
		if g, ok := manjuToFloat(ctx.R["bgm_gain"]); ok && g > 0 {
			args = append(args, "--bgm-gain", strconv.FormatFloat(g, 'g', -1, 64))
		}
		if d, ok := manjuToFloat(ctx.R["bgm_duck"]); ok && d > 0 {
			args = append(args, "--bgm-duck", strconv.FormatFloat(d, 'g', -1, 64))
		}
	}
	// 跳过镜头(用户决定):合成时从成片中排除
	if sk := ctx.qcSkipList(); sk != "" {
		args = append(args, "--skip-shots", sk)
		lg.logf("  ⏭ 合成跳过镜头 " + sk + "(用户决定,不入成片)")
	}
	if err := ctx.runMedia(lg, args...); err != nil {
		return fmt.Errorf("合成失败: %w", err)
	}
	return nil
}

// seamHardCuts 接缝镜头号列表(MotionContext 渲染的镜头,其起始边界画面连续,
// 合成转场必须硬切;来源 manifest 的 seam 标记)
func (ctx *manjuCtx) seamHardCuts() string {
	manjuManifestMu.Lock()
	m := ctx.manifestLoad()
	manjuManifestMu.Unlock()
	ids := []int{}
	for k, e := range m.Shots {
		if e != nil && e.Seam {
			if n, err := strconv.Atoi(k); err == nil {
				ids = append(ids, n)
			}
		}
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, n := range ids {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// ---- 媒体辅助脚本 ----

func ensureMediaHelper() string {
	dir := filepath.Join(ManjuRootDir, "logs", "media")
	_ = os.MkdirAll(dir, 0755)
	p := filepath.Join(dir, "manju_media.py")
	// 总是覆盖提取(内嵌脚本随 exe 版本更新)
	_ = os.WriteFile(p, []byte(manjuMediaPy), 0644)
	// YuNet 人脸检测模型同步释放(py 按脚本同目录加载)
	_ = os.WriteFile(filepath.Join(dir, "face_detection_yunet_2023mar.onnx"), []byte(manjuHaarFaceXML), 0644)
	return p
}

// filesEqual 判断两文件内容是否相同(整文件 MD5,用于识别"_face 是主图副本"的旧状态)
func filesEqual(a, b string) bool {
	ha := fileMD5Hex(a)
	if ha == "" {
		return false
	}
	return ha == fileMD5Hex(b)
}

func fileMD5Hex(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := md5.Sum(data)
	return fmt.Sprintf("%x", h)
}

// ensureFaceCrop 确保角色有正脸特写参考(缺失/仍是主图副本/主图已更新时用 PIL 重裁)。
// beast=兽类角色:YuNet 只检测人脸,兽脸必然未命中(白跑一次检测+刷检测告警),
// 直接走启发式窗口。
func (ctx *manjuCtx) ensureFaceCrop(cid string, beast bool, lg *manjuLogger) error {
	// 物品豁免下沉到封装内(2026-08-29 审计 V2 修复:此前豁免只打在 stageAssets
	// 调用点,抽卡采纳链 manjuAdoptGacha 未复制 → 物品采纳后 _face 被人脸裁剪乱裁,
	// 渲染 front 参考引用裁坏图。豁免进封装,两个调用点自然一致)
	if m := ctx.charCardByName(cid); m != nil && manjuIsItem(m) {
		return nil
	}
	mainP := filepath.Join(ctx.assetsDir, "characters", cid+".png")
	faceP := filepath.Join(ctx.assetsDir, "characters", cid+"_face.png")
	if !fileExists(mainP) {
		return nil
	}
	// 跳过条件:正脸内容独立(≠主图副本)且不早于主图;
	// 采纳新定妆照/重生成主图后主图 mtime 变新 → 旧正脸不再匹配,自动重裁保证身份参考与主图同步
	if fileExists(faceP) {
		mf, e1 := os.Stat(mainP)
		ff, e2 := os.Stat(faceP)
		if e1 == nil && e2 == nil && !ff.ModTime().Before(mf.ModTime()) && !filesEqual(faceP, mainP) {
			return nil // 已有与主图同步的独立正脸
		}
	}
	args := []string{"facecrop", "--src", mainP, "--dst", faceP, "--ratio", fmt.Sprintf("%dx%d", ctx.w, ctx.h)}
	if beast {
		args = append(args, "--beast")
	}
	if err := ctx.runMedia(lg, args...); err != nil {
		return fmt.Errorf("角色 %s 正脸裁剪失败: %w", cid, err)
	}
	return nil
}

// charIsBeastByName 按角色名查方案角色卡判兽类(采纳流程等无角色卡在手时用)
func (ctx *manjuCtx) charIsBeastByName(cid string) bool {
	m := ctx.charCardByName(cid)
	return m != nil && manjuIsBeast(m)
}

// charCardByName 按名查方案角色卡(物种查询统一入口,facecrop/音色等旁路共用)
func (ctx *manjuCtx) charCardByName(cid string) map[string]any {
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return nil
	}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok && str(m["id"]) == cid {
			return m
		}
	}
	return nil
}

// ---- 环境自检 ----

func manjuEnvCheck(configPath string) string {
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		return "❌ config 读取失败: " + err.Error() + "\n[exit 1]"
	}
	var b strings.Builder
	ok := true
	b.WriteString("📁 项目: " + ctx.project + "\n")
	if fileExists(configPath) {
		b.WriteString("  ✅ config.json\n")
	} else {
		b.WriteString("  ❌ config.json 缺失\n")
		ok = false
	}
	b.WriteString("📖 小说: " + ctx.novel + "\n")
	if fileExists(ctx.novel) {
		data, _ := os.ReadFile(ctx.novel)
		b.WriteString(fmt.Sprintf("  ✅ 共 %d 章 / %d 字\n", len(allChapterNums(string(data))), len([]rune(string(data)))))
	} else {
		b.WriteString("  ❌ 小说文件不存在\n")
		ok = false
	}
	b.WriteString("🖥 ComfyUI: " + ctx.comfy.base + "\n")
	if v, err := ctx.comfy.online(); err == nil {
		b.WriteString("  ✅ 在线 v" + v + "\n")
	} else {
		b.WriteString("  ❌ 离线: " + err.Error() + "\n")
		ok = false
	}
	check := func(label, name string) string {
		p := filepath.Join(ctx.sharedModels, label, name)
		if fileExists(p) {
			return "  ✅ " + name + "\n"
		}
		ok = false
		return "  ❌ " + name + "（未找到）\n"
	}
	b.WriteString("🧠 H3 模型:\n")
	b.WriteString(check("diffusion_models", str(ctx.R["unet_ref2va"])))
	b.WriteString(check("diffusion_models", str(ctx.R["unet_fl2va"])))
	b.WriteString(check("text_encoders", str(ctx.R["clip"])))
	b.WriteString(check("vae", str(ctx.R["vae_video"])))
	b.WriteString(check("vae", str(ctx.R["vae_audio"])))
	if l := str(ctx.R["turbo_lora"]); l != "" {
		b.WriteString(check("loras", l))
	}
	if sageEnabled(ctx.R) {
		b.WriteString("⚡ SageAttention 加速(已开启):\n")
		sageNode := ""
		for _, n := range []string{"PathchSageAttentionKJ", "PatchSageAttentionKJ"} {
			if ctx.comfy.hasNode(n) {
				sageNode = n
				break
			}
		}
		if sageNode != "" {
			b.WriteString("  ✅ ComfyUI 节点 " + sageNode + " 可用(KJNodes)\n")
		} else {
			b.WriteString("  ❌ ComfyUI 缺少 PatchSageAttentionKJ 节点(安装 ComfyUI-KJNodes,或在渲染参数里关闭 SageAttention)\n")
			ok = false
		}
	}
	b.WriteString(check("diffusion_models", str(ctx.R["z_image_unet"])))
	b.WriteString(check("text_encoders", str(ctx.R["z_image_clip"])))
	b.WriteString(check("vae", str(ctx.R["z_image_vae"])))
	// 2026-08-24 用户规则:SDXL 已禁用——定妆照只用 Z-Image/Krea-2,不再检查/提示 SDXL checkpoint
	b.WriteString("🐍 PyAV(质检/合成): " + manjuPythonPath() + "\n")
	if fileExists(manjuPythonPath()) {
		b.WriteString("  ✅ venv python\n")
	} else {
		b.WriteString("  ❌ venv python 缺失\n")
		ok = false
	}
	rc := 0
	if !ok {
		rc = 1
	}
	return strings.TrimRight(b.String(), "\n") + "\n[exit " + strconv.Itoa(rc) + "]"
}

// ---- 新建项目(new_project 等价:小说目录自动识别 + 默认 config) ----

func manjuCreateProject(name, novel, apiKey string) (string, string, bool) {
	var out strings.Builder
	bad := map[rune]bool{'\\': true, '/': true, ':': true, '*': true, '?': true, '"': true, '<': true, '>': true, '|': true}
	clean := strings.Map(func(r rune) rune {
		if bad[r] {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if clean == "" || clean == "." {
		return "❌ 剧名无效\n[exit 1]", "", false
	}
	novel = strings.Trim(novel, `" `)
	// 输入方式二选一:novel 非空 = 小说解析模式(须在小说库根目录内,否则后续 GET 免 token
	// 入口经 newManjuCtx 动态扩 fs 白名单 → 任意文件读);novel 空 = 视频脚本直出模式
	// (创建后到「视频脚本直出」卡片粘贴 H3 官方格式脚本,config paths.script 由 script/save 写入)
	var novelFile, novelDir string
	if novel != "" {
		// 审计 F1:novel 必须位于小说库根目录内
		if _, gerr := manjuGuardNovel(novel); gerr != nil {
			out.WriteString("❌ 小说路径必须在小说库根目录内: " + gerr.Error() + "\n[exit 1]")
			return out.String(), "", false
		}
		novelFile, novelDir = resolveNovelPath(novel)
		if novelFile == "" {
			out.WriteString("❌ 小说路径无效或未找到正文文件: " + novel + "\n[exit 1]")
			return out.String(), "", false
		}
	}
	projDir := filepath.Join(ManjuRootDir, clean)
	if _, err := os.Stat(projDir); err == nil {
		out.WriteString("❌ 项目已存在: " + clean + "\n[exit 1]")
		return out.String(), "", false
	}
	for _, d := range []string{"", "analysis", "assets/characters", "assets/scenes", "clips"} {
		if err := os.MkdirAll(filepath.Join(projDir, d), 0755); err != nil {
			return "❌ 创建目录失败: " + err.Error() + "\n[exit 1]", "", false
		}
	}
	if apiKey == "" {
		apiKey = manjuDefaultAPIKey()
	}
	cfg := manjuDefaultConfig(clean, novelFile, novelDir, apiKey)
	// 2026-08-23 用户规则:渲染参数与风格创作期规划(爽文技能 立项.json render 字段),
	// 建项目时自动应用;config 已有非零显式值优先,规划作默认
	applyNovelRenderPlan(cfg, novelDir)
	if err := writeManjuConfig(filepath.Join(projDir, "config.json"), cfg); err != nil {
		return "❌ 写 config 失败: " + err.Error() + "\n[exit 1]", "", false
	}
	// 从小说目录检索封面图复制到项目 assets/，供左侧栏海报展示(仅小说模式)
	if novelDir != "" {
		if cover := copyProjectCover(novelDir, projDir); cover != "" {
			out.WriteString("  ✅ 封面已检索: " + filepath.Base(cover) + "\n")
		}
	}
	out.WriteString("📁 项目已创建: " + projDir + "\n")
	if novelFile != "" {
		out.WriteString("📖 小说: " + novelFile + "\n")
	} else {
		out.WriteString("🎬 视频脚本直出模式: 创建后到「视频脚本直出」卡片粘贴 H3 官方格式脚本\n")
	}
	out.WriteString("  ✅ config.json 已生成\n")
	return out.String(), filepath.Join(projDir, "config.json"), true
}

// applyNovelRenderPlan 读取小说目录 立项.json 的 render 规划,合并进 config.render
// (2026-08-23 用户规则:渲染参数与风格创作期规划,建项目自动应用;config 已有非零显式值优先,规划作默认)
func applyNovelRenderPlan(cfg map[string]any, novelDir string) {
	if novelDir == "" {
		return
	}
	planPath := filepath.Join(novelDir, "立项.json")
	if !fileExists(planPath) {
		return
	}
	var plan map[string]any
	if b, err := os.ReadFile(planPath); err == nil {
		_ = json.Unmarshal(b, &plan)
	}
	rp, _ := plan["render"].(map[string]any)
	if len(rp) == 0 {
		return
	}
	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
		cfg["render"] = R
	}
	for k, v := range rp {
		// char_engine 特判(2026-08-23):config 默认值 zimage 视为"未显式设置",
		// 规划给了 krea2/sdxl 时应用规划(否则用户创作期规划的定妆引擎不生效)
		if k == "char_engine" {
			cur := strings.TrimSpace(str(R["char_engine"]))
			plan := strings.TrimSpace(str(v))
			if (cur == "" || cur == "zimage") && plan != "" && plan != "zimage" {
				R["char_engine"] = plan
			}
			continue
		}
		if _, exists := R[k]; !exists || isZeroVal(R[k]) {
			R[k] = v
		}
	}
	// style 顶层(config.style):规划风格非空且 config 未设才应用
	if s := str(rp["style"]); s != "" && str(cfg["style"]) == "" {
		cfg["style"] = s
	}
}

// isZeroVal 渲染参数"未设置"判定(空串/0/false 视为未设,让规划默认生效)
func isZeroVal(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x) == ""
	case float64:
		return x == 0
	case int:
		return x == 0
	case bool:
		return !x
	}
	return false
}

// copyProjectCover 从小说目录检索封面图片(文件名含 主图/封面/cover/poster 优先)复制到项目 assets/，
// 返回复制后的绝对路径(未找到返回空)。文件名保留 "cover" 关键字供左侧栏 analyzeMedia 识别为海报封面。
func copyProjectCover(novelDir, projDir string) string {
	entries, err := os.ReadDir(novelDir)
	if err != nil {
		return ""
	}
	var cands []dirFile
	for _, e := range entries {
		if e.IsDir() || !isImageFile(e.Name()) {
			continue
		}
		cands = append(cands, dirFile{Name: e.Name(), Path: filepath.Join(novelDir, e.Name())})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		pi, pj := coverPriority(cands[i].Name), coverPriority(cands[j].Name)
		if pi != pj {
			return pi < pj
		}
		return cands[i].Name < cands[j].Name
	})
	src := cands[0].Path
	dst := filepath.Join(projDir, "assets", "cover"+strings.ToLower(filepath.Ext(src)))
	if copyFile(src, dst) != nil {
		return ""
	}
	return dst
}

// firstMdByPattern 在目录内找文件名含 pat 的第一个 .md
func firstMdByPattern(dir, pat string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		if strings.Contains(e.Name(), pat) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// resolveNovelPath 小说路径识别:文件直接用;目录则在其中找 正文/全本 类 md
// (支持 全本/xxx.md、正文/xxx.md 等子目录结构,如吞天废子:全本/吞天废子·全本.md)
func resolveNovelPath(novel string) (file, dir string) {
	novel = strings.Trim(novel, `" `)
	if novel == "" {
		return "", ""
	}
	if st, err := os.Stat(novel); err == nil {
		if !st.IsDir() {
			// 2026-08-30 回归修复(轮回欠费九世实锤「资产 0 角色/0 场景直接编码」):
			// 文件输入(全本 md)时 dir=父目录——父目录是布局子目录(全本/正文/素材)时
			// 必须上提到书根,否则 config.novel_dir=…/书/全本,素材卡(人物/场景提示词)
			// 与分镜脚本目录全部找不到 → 方案零角色卡 → 资产空转直接编码。
			d := filepath.Dir(novel)
			for i := 0; i < 3; i++ {
				base := filepath.Base(d)
				if !(base == "全本" || strings.Contains(base, "正文") || base == "素材" || base == "分镜脚本") {
					break
				}
				d = filepath.Dir(d)
			}
			return novel, d
		}
		dir = novel
		// 优先 全本 类(完整正文),其次 正文 类;先查根目录,再查同名子目录
		for _, pat := range []string{"全本", "正文"} {
			if f := firstMdByPattern(dir, pat); f != "" {
				return f, dir
			}
			subs, _ := os.ReadDir(dir)
			for _, s := range subs {
				if !s.IsDir() || !strings.Contains(s.Name(), pat) {
					continue
				}
				if f := firstMdByPattern(filepath.Join(dir, s.Name()), pat); f != "" {
					return f, dir
				}
			}
		}
		// 兜底:目录里第一个 .md(排除 设定/大纲)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			if strings.Contains(e.Name(), "设定") || strings.Contains(e.Name(), "大纲") {
				continue
			}
			return filepath.Join(dir, e.Name()), dir
		}
	}
	return "", ""
}

func manjuDefaultConfig(name, novelFile, novelDir, apiKey string) map[string]any {
	return map[string]any{
		// 默认风格:写实(real)——总集/画风基准默认写实电影级;旧 2.5d 动漫不是默认
		"style": "real",
		"llm": map[string]any{
			"api_key": apiKey, "base_url": "https://api.deepseek.com",
			"model": "deepseek-chat", "temperature": 0.4, "max_tokens": 8192, "request_timeout": 300,
		},
		"render": map[string]any{
			"width": 768, "height": 1344, "fps": 24, "steps": 20, "turbo_steps": 8, "seed": 1688,
			// 2026-08-25 最优默认(24GB 卡防爆显存 + 多镜渲染):单镜 5-7s、shots_per_take=2 同场景连拍
			// (6+6=12s≤15 分组有效)、FL2VA 尾帧锚定关闭(空镜成本减半)、standard 档、草稿预审开
			"min_shot_seconds": 5, "max_shot_seconds": 7, "shots_per_take": 2,
			"res_tier": "standard", "draft_judge": true, "draft_scale": 0.5,
			"fl2va_end_frame": false, "sage_attention": true, "seed_policy": "increment",
			// 2026-08-26 升级:官方 Context IR 空镜扩写默认开(有 MiniMax Key 才生效);
			// 渲染空闲 10 分钟自动 /free 释放显存(0=关);QC 视觉抽检默认开(本地 Qwen3-VL,失败不阻塞)
			"ir_expand": true, "idle_free_minutes": 10, "qc_vision": true,
			"comfy_url":      "http://127.0.0.1:8190",
			"neg_prompt":     "lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting",
			"unet_fl2va":     "MiniMax_H3_fl2va_pruned_int8_convrot.safetensors",
			"unet_ref2va":    "MiniMax_H3_ref2va_pruned_int8_convrot.safetensors",
			"clip":           "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
			"vae_video":      "minimax_h3_video_vae_int8_convrot.safetensors",
			"vae_audio":      "minimax_h3_audio_vae_fp32.safetensors",
			"z_image_unet":   "z_image_turbo_bf16.safetensors",
			"z_image_clip":   "qwen_3_4b.safetensors",
			"z_image_vae":    "ae.safetensors",
			"krea2_unet":     "krea2_turbo_fp8_scaled.safetensors",
			"krea2_clip":     "qwen3vl_4b_fp8_scaled.safetensors",
			"krea2_vae":      "qwen_image_vae.safetensors",
			// 2026-08-24 用户规则:定妆照默认 Krea-2(强指令跟随,能把"拟漫 stylized illustration"执行到位,
			// 出半写实拟漫东方形象);Z-Image 出真人照片(视觉模型实测 is_real_person_photo=true,侵权风险),
			// 仅保留作场景图引擎(无人脸)。SDXL 全面禁用。
			"char_engine":    "krea2",
			"voiceover":      false, // 2026-08-23 用户规则:默认 H3 自带配音;仅角色内心活动(Q版)需后期 TTS 时手动开启
			// 2026-08-26 Turbo LoRA 默认对齐磁盘实际部署(larryvrh 4step EMA 旧文件已停分发,
			// 此前新项目默认指旧名 → 体检「未找到」);fl2v v1.1 空镜/ref2v v0.1 角色镜,
			// 与全局 settings 默认(config.Render)一致。
			"turbo_lora":     "minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors",
			"turbo_lora_r2v": "minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors",
			// 不再配置 SDXL checkpoint(char_models/animagine_ckpt 置空,渲染不用它们)。
			"animagine_ckpt": "",
			"char_models":    map[string]any{},
			"chapters":       "1-3", "episode": "EP01",
		},
		"moderation": map[string]any{"banned_words": []any{}, "mosaic_enabled": false, "mosaic_level": 16},
		"knowledge": map[string]any{
			"characters": []any{"古风男性角色设计板.md", "女频短剧女主提示词模板.md", "古风角色发型提示词模板.md", "古风男主发型提示词模板.md"},
			"scenes":     []any{"仙侠场景提示词模板.md", "天庭场景提示词模板.md"},
			"storyboard": []any{"AI视频运镜提示词模板.md", "漫剧创作规范.md"},
		},
		"paths": map[string]any{
			"workdir":      filepath.Join(ManjuRootDir, name),
			"novel":        novelFile,
			"novel_dir":    novelDir,
			"analysis":     filepath.Join(ManjuRootDir, name, "analysis"),
			"assets":       filepath.Join(ManjuRootDir, name, "assets"),
			"clips":        filepath.Join(ManjuRootDir, name, "clips"),
			// 跟随生效的 ComfyUI 共享目录(而非硬编码 Desktop 路径):ComfyUI 可能按自包含
			// 目录启动,写死旧路径会让新项目的 comfy_output 与实际输出目录不一致,
			// 渲染"完成"但读产物报 not found。运行时 newManjuCtx 仍优先用 comfyParams 权威值。
			"comfy_input":  filepath.Join(ComfySharedDir, "input"),
			"comfy_output": filepath.Join(ComfySharedDir, "output"),
			"outline":      "", "setting": "",
		},
	}
}

// ---- 角色抽卡 ----

// gachaCharGender 角色性别(按性别选 SDXL checkpoint;写实风格走 Z-Image 无需性别)
func (ctx *manjuCtx) gachaCharGender(char string) string {
	return str(ctx.gachaCharInfo(char)["gender"])
}

// gachaCharInfo 抽卡用完整角色卡(2026-08-25:含 species/gender/age/appearance/image_prompt/views,
// 抽卡 Q 版需要种族/胡须/面容信息,不能再只传 gender)
func (ctx *manjuCtx) gachaCharInfo(char string) map[string]any {
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return map[string]any{}
	}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
			return m
		}
	}
	return map[string]any{}
}

// manjuGachaDraw 生成抽卡候选(随机 seed,一次可连抽 count 张)→ assets/characters/_gacha/<char>_<view>_s<seed>.png
// view 为空=正面主视图(半身立绘);可选 front/full/side/detail/q(角色卡 views 提示词,旧方案自动派生)。
// 候选落盘不覆盖(文件名含 seed),前端据此保留抽卡历史供对比挑选
func manjuGachaDraw(configPath, episode, char, view string, count int) ([]map[string]any, error) {
	if count < 1 {
		count = 1
	}
	if count > 8 {
		count = 8
	}
	view = strings.TrimSpace(view)
	// 抽卡是独立旁路入口(2026-08-29 实测修复:上次渲染被手动停止后 stopped 卡 true,
	// 抽卡任务提交 ComfyUI 后立即被 wait 停止感知 interrupt,候选永不落盘、无法采纳;
	// 与 manjuRun/agent/upscale/IR 各入口一致,旁路开始即复位停止标记;渲染运行中仍拒绝)
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		return nil, fmt.Errorf("渲染任务运行中,请先停止")
	}
	manjuState.stopped = false
	manjuState.mu.Unlock()
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return nil, err
	}
	if _, _, err := ctx.loadPlan(); err != nil {
		return nil, fmt.Errorf("角色方案未生成,请先「生成方案」")
	}
	// 完整角色卡(2026-08-25:种族/性别/年龄/胡须/面容/views 全量参与)
	charInfo := ctx.gachaCharInfo(char)
	// Q 版候选:基于全身照 img2img(2026-08-26 用户规则:Q版由全身照派生才合理,chibi 是全身形态;
	// 全身照缺失回退正面定妆照)
	mainRef := ""
	if view == "q" {
		for _, suffix := range []string{"_full", ""} {
			p := filepath.Join(ctx.assetsDir, "characters", sanitizeFileName(char)+suffix+".png")
			if !fileExists(p) {
				continue
			}
			refName := "dir_char_" + strings.TrimPrefix(suffix, "_") + "_" + sanitizeFileName(char) + ".png"
			if refName == "dir_char__"+sanitizeFileName(char)+".png" {
				refName = "dir_char_main_" + sanitizeFileName(char) + ".png"
			}
			if os.MkdirAll(ctx.comfyInput, 0755) == nil && copyFile(p, filepath.Join(ctx.comfyInput, refName)) == nil {
				mainRef = refName
				break
			}
		}
	}
	// Q 版用专用构建器(种族/性别/胡须/面容随角色);其余视图沿用角色卡 image_prompt+视图修饰
	prompt := charViewPromptFor(ctx, char, view)
	if view == "q" {
		prompt = manjuQPrompt(charInfo)
	}
	safe := sanitizeFileName(char)
	lg := &manjuLogger{state: manjuState}
	out := []map[string]any{}
	for i := 0; i < count; i++ {
		seed := randSeed()
		var wf map[string]any
		if view == "q" && mainRef != "" {
			// Q 版参考正面照 img2img(2026-08-27 三修切 Krea-2:Z-Image 高重绘下 chibi 手办
			// 素体先验压倒一切文本控制,精卫 Q 版袒胸实锾示正向锁/负面禁裸/init 闭合三重全被无视)
			wf = wfKrea2(ctx.portraitPromptFor(prompt, charInfo, false), str(ctx.R["krea2_unet"]), str(ctx.R["krea2_clip"]), str(ctx.R["krea2_vae"]), seed, manjuPortraitW, manjuPortraitH, "manju_gacha", ctx.negPrompt(), mainRef, manjuQStrength)
		} else {
			// 抽卡=换装探索,保持随机多样性(不基于主图 img2img;采纳后定妆照覆盖)
			wf = ctx.portraitWF(prompt, seed, "manju_gacha", charInfo, "", 0)
		}
		vTag := ""
		if view != "" {
			vTag = "_" + view
		}
		dst := filepath.Join(ctx.assetsDir, "characters", "_gacha", fmt.Sprintf("%s%s_s%d.png", safe, vTag, seed))
		if err := ctx.comfyGenImage(wf, dst, lg, "角色 "+char+"("+orDefault(view, "front")+")"); err != nil {
			if len(out) == 0 {
				return nil, err
			}
			break // 连抽中途失败:返回已生成的部分,不让一张失败全盘作废
		}
		out = append(out, map[string]any{"image": dst, "seed": seed, "view": view})
	}
	return out, nil
}

// charPromptFor 抽卡提示词:优先角色卡 image_prompt,兜底占位
func charPromptFor(ctx *manjuCtx, char string) string {
	return charViewPromptFor(ctx, char, "")
}

// manjuViewOrder 角色视图优先级(参考图传入顺序 = prompt Picture 编号顺序)
var manjuViewOrder = []string{"front", "full", "detail", "side"}

// charSeed 角色定妆照/视图的稳定随机种子:由角色名+视图派生(散列)。
// 同一角色跨重跑/跨项目 seed 稳定(定妆照不漂移),不同角色/不同视图 seed 不同
// (消除"多部小说主角 seed 相近 → 面容雷同"的根因——此前用固定 7000+i 递增,
// 相似 prompt + 相近 seed 生成相似面容)。
func charSeed(cid, view string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cid + "|" + view))
	return int(h.Sum32() & 0x7fffffff)
}

// manjuViewRel 视图 → 资产相对路径(front 正脸特写,其余独立视图文件)
func manjuViewRel(cid, view string) string {
	switch view {
	case "front":
		return "characters/" + cid + "_face.png"
	default:
		return "characters/" + cid + "_" + view + ".png"
	}
}

// charViewPromptFor 按视图取角色提示词。
// 2026-08-24 一致性修复:优先 image_prompt(含完整身份特征:发色/胡须/服装/五官) + 视图修饰——
// LLM 生成的 views.<view> 实测是泛化词(如"clear profile of hair"不含白发白须),
// 导致多视图 img2img 重绘时发色等身份特征丢失(用户反馈:白发老者侧面变黑发)。
// views.<view> 仅作无 image_prompt 时的兜底。
func charViewPromptFor(ctx *manjuCtx, char, view string) string {
	plan, _, err := ctx.loadPlan()
	if err == nil {
		for _, c := range anyArr(plan["characters"]) {
			if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
			// 角色板(2026-08-25 即梦角色版):统一走 manjuBoardPromptFor 三分类路由
			// (2026-08-29 审计 V1 修复:此处旧内联副本缺物品路由,物品 board 吃到人形板
			// 布局 "three full-body views of the same character"——与正确的三分类版本
			// 漂移成两份实现,删内联副本收敛到单一封装)
			if view == "board" {
				return ctx.manjuBoardPromptFor(char, m)
			}
			// 身份特征优先:image_prompt 含具体发色/胡须/服装,视图提示词必须带上;
			// 性别锚(2026-08-26):高 denoise 重绘下防性别漂移(墨姨 side 长胡须)
			if p := str(m["image_prompt"]); p != "" {
				// 2026-08-28(猫·二两 full 画出人):兽形视图先过 manjuBeastStrip 剥
				// 人互动子句+卡面 3D 人形锚(virtual digital human/BJD doll),并用
				// 兽形视图后缀(禁衣服禁人)——旧人形后缀 standing pose/complete
				// outfit/hairstyle 对四脚动物是穿衣服+拟人邀请。
				beast := manjuIsBeast(m)
				if beast {
					p = manjuBeastStrip(p)
				}
				if view == "" || view == "front" {
					return p + manjuViewGenderAnchor(m) // 半身立绘即正面主视图
				}
				suffix := manjuViewSuffix(view)
				if beast {
					suffix = manjuBeastViewSuffix(view)
				}
				// 物品视图后缀(2026-08-29 架构重构):人形后缀 standing pose/complete
				// outfit/hairstyle silhouette 对物品是拟人邀请——物品只描述本体,禁人
				if manjuIsItem(m) {
					suffix = manjuItemViewSuffix(view)
				}
				return p + manjuViewGenderAnchor(m) + ", " + suffix + manjuColorAnchor(m)
			}
				// 兜底:LLM 的 views.<view>
				if view != "" {
					if vs, ok := m["views"].(map[string]any); ok {
						if p := str(vs[view]); p != "" {
							return p
						}
					}
				}
				if p := str(m["image_prompt"]); p != "" {
					return p
				}
			}
		}
	}
	return "portrait of " + char + ", " + manjuAssetStyle(ctx.style) + ", upper body, detailed face, clean background"
}

// manjuBeastViewSuffix 兽形视图后缀(2026-08-28 猫·二两 full 全身照画出人:人形后缀
// "standing pose / complete outfit visible / hairstyle silhouette" 对四脚动物是
// 穿衣服+拟人邀请)——兽形视图只描述动物本体,显式禁衣服禁人。
func manjuBeastViewSuffix(view string) string {
	switch view {
	case "full":
		return "the complete animal body visible from nose to tail, natural quadruped animal pose, no clothing, no accessories, no human"
	case "side":
		return "90 degree side view of the same animal, full body animal silhouette, no human"
	case "detail":
		return "extreme close-up on the same animal's signature features (fur pattern / ears / eyes), sharp focus, no human"
	}
	return ""
}

// manjuViewSuffix 旧方案(无 views 字段)派生视图提示词的英文修饰后缀
func manjuViewSuffix(view string) string {
	switch view {
	case "full":
		return "full body, head to toe, standing pose, complete outfit visible"
	case "side":
		return "side profile, 90 degree side view, face and hairstyle silhouette, body side view"
	case "detail":
		return "extreme close-up on the signature detail (accessory / ornament / scar / hairdo), sharp focus, high detail"
	default:
		return ""
	}
}

// charViewRels 该角色在指定镜位的参考图相对路径列表(Picture 顺序):
// 预算:单角色 3 视图(front/full/detail),双角色每角色 2 视图(front/full),
// 三角色 主角 2 视图其余 1 视图——总参考 ≤8 张(另加场景 1 张,符合 H3 Omni-reference ≤9)。
// front 缺失回退主图;full/detail 缺失跳过(不降级,保持参考纯净)。
// shotWantsTrueForm 双形态切换判定(2026-08-26):镜头文本(画面/台词/旁白/六段式)含
// 「真身·<角色名>」/「化形·<角色名>」前缀约定,或同时含 真身/化形/原形/兽形 关键词与
// 角色名(正文写「小白真身显现」也能切换)。误切风险低:切换前提是该角色有 form2 资产
// (素材里明确写了真身提示词才会定妆 form2)。
func shotWantsTrueForm(s manjuShot, cid string) bool {
	pool := s.Action + " " + s.Dialogue + " " + s.Narration + " " + s.H3Prompt
	if strings.Contains(pool, "真身·"+cid) || strings.Contains(pool, "化形·"+cid) {
		return true
	}
	for _, kw := range []string{"真身", "化形", "原形", "兽形"} {
		if strings.Contains(pool, kw) && strings.Contains(pool, cid) {
			return true
		}
	}
	return false
}

func (ctx *manjuCtx) charViewRels(cid string, idx, total int) []string {
	var picks []string
	switch {
	case total <= 1:
		// 2026-09-01 加 side 侧面视图:参考图含侧面,H3 侧面镜头有据可依
		// (主持人/多角度镜头侧面渲染崩的修复);4 视图 ≤8 张预算
		picks = []string{"front", "full", "detail", "side"}
	case total == 2:
		picks = []string{"front", "full", "side"}
	default: // 3 角色
		if idx == 0 {
			picks = []string{"front", "full", "side"}
		} else {
			picks = []string{"front", "side"}
		}
	}
	out := []string{}
	for _, v := range picks {
		rel := manjuViewRel(cid, v)
		if v == "front" {
			// 正脸缺失回退主图(半身立绘仍可锁身份)
			if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
				rel = "characters/" + cid + ".png"
				if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
					continue
				}
			}
		} else if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// shotRefRoles 该镜有参考图的登场角色(顺序=characters 顺序,≤3):供提示词判断可写 <Picture N> 的角色
func (ctx *manjuCtx) shotRefRoles(s manjuShot) []string {
	var out []string
	n := len(s.Characters)
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		if len(ctx.charViewRels(cid, i, minInt(n, 3))) > 0 {
			out = append(out, cid)
		}
	}
	return out
}

// shotRefViews 该镜参考图平铺清单(角色+视图,顺序=charRefNames 传入顺序,与 Picture 编号一一对应):
// 供提示词生成写 <Picture N> 时知道每个角色有几个视图、对应哪些图。
// 返回形如 ["陈鱼(front)", "陈鱼(full)", "柳如烟(front)"]。
func (ctx *manjuCtx) shotRefViews(s manjuShot) []string {
	var out []string
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		// 2026-08-29 收敛:Q版/form2 切换统一走 shotViewRelsFor(与 charRefNames 同源,
		// 此处只负责把相对路径翻译成 "角色(视图)" 标签)
		for _, rel := range ctx.shotViewRelsFor(s, cid, i, n) {
			view := strings.TrimSuffix(filepath.Base(rel), ".png")
			view = strings.TrimPrefix(strings.TrimPrefix(view, sanitizeFileName(cid)+"_"), sanitizeFileName(cid))
			if view == "" || view == "face" {
				view = "front"
			}
			out = append(out, cid+"("+view+")")
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// manjuAdoptGacha 采纳抽卡候选为正式定妆照(覆盖 → mtime 变化 → 缓存指纹失效),并切正脸参考
// view 为空=主视图(characters/<char>.png);full/side/detail=对应视图文件。
// front 视图的采纳与主图相同(正脸从主图裁切,ensureFaceCrop 统一生成)。
func manjuAdoptGacha(configPath, episode, char, view, image string) error {
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return err
	}
	// 角色名 sanitize:防路径穿越(审计 H7——char 拼接进定妆照路径)
	char = sanitizeFileName(char)
	if char == "" || char == "." || char == ".." {
		return fmt.Errorf("非法角色名")
	}
	if !fileExists(image) {
		return fmt.Errorf("候选图不存在: %s", image)
	}
	// 候选图归属校验:image 必须位于本项目 assets/characters/_gacha 内(防任意文件拷贝)
	gachaDir := filepath.Join(ctx.assetsDir, "characters", "_gacha")
	imgClean := filepath.Clean(image)
	gachaClean := filepath.Clean(gachaDir)
	if imgClean == gachaClean || !strings.HasPrefix(imgClean, gachaClean+string(filepath.Separator)) {
		return fmt.Errorf("候选图必须在项目 _gacha 目录内")
	}
	view = strings.TrimSpace(view)
	dst := filepath.Join(ctx.assetsDir, "characters", char+".png")
	if view != "" && view != "front" {
		dst = filepath.Join(ctx.assetsDir, "characters", char+"_"+view+".png")
	}
	if err := copyFile(image, dst); err != nil {
		return err
	}
	manjuMarkAdopted(ctx, char, dst)
	// 主视图采纳(或重生成)后重切正脸特写参考,供 R2V 身份锁定;
	// 仅采纳 full/side/detail 视图时主图未变,正脸无需重切(跳过,节省一次裁剪)
	lg := &manjuLogger{state: manjuState}
	if view == "" || view == "front" {
		return ctx.ensureFaceCrop(char, ctx.charIsBeastByName(char), lg)
	}
	return nil
}

// manjuAdoptedFilePath 采纳标记:assets/characters/adopted.json,char → 正式定妆照路径
func manjuAdoptedFilePath(ctx *manjuCtx) string {
	return filepath.Join(ctx.assetsDir, "characters", "adopted.json")
}

// manjuMarkAdopted 记录角色已被采纳(产物-人物 据此展示采纳定妆照并标注)
func manjuMarkAdopted(ctx *manjuCtx, char, image string) {
	m := manjuAdoptedMap(ctx)
	m[char] = image
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(filepath.Dir(manjuAdoptedFilePath(ctx)), 0755)
	_ = os.WriteFile(manjuAdoptedFilePath(ctx), b, 0644)
}

// manjuAdoptedMap 读取采纳标记(文件不存在/损坏时为空 map)
func manjuAdoptedMap(ctx *manjuCtx) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(manjuAdoptedFilePath(ctx)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// manjuPlanCharacters 抽卡前置:生成角色/场景方案(复用已有,保证与渲染方案一致)
func manjuPlanCharacters(configPath, episode string) error {
	// 旁路入口复位停止标记(同 manjuGachaDraw 2026-08-29 实测:上次渲染被停止后
	// stopped 卡 true,方案生成/抽卡提交后立即被 interrupt,全部失败)
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		return fmt.Errorf("渲染任务运行中,请先停止")
	}
	manjuState.stopped = false
	manjuState.mu.Unlock()
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return err
	}
	lg := &manjuLogger{state: manjuState}
	if _, err := ctx.ensurePlan(lg); err != nil {
		return err
	}
	return nil
}

// ---- 小工具 ----

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

// reNonWord 缓存名清洗:保留中文/字母/数字/_/-,其余替换为 _ (与旧 Python \w unicode 行为一致)
var reNonWord = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)
