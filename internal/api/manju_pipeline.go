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

	"nilix/internal/agent"
)

//go:embed scripts/manju_media.py
var manjuMediaPy string

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
	scriptMode   bool   // 视频脚本直出模式(输入为 H3 官方格式的分镜脚本 md,而非小说正文)
	auto         bool   // 全本自动分集模式:该集章节由引擎按内容量切分(方案复用校验用)
	llm          *manjuLLM
	comfy        *comfyClient
	comfyOutput  string
	comfyInput   string
	sharedModels string
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
	seedPolicy   string  // seed 重试策略:fixed(默认全剧固定)/increment(重试 seed+N)/random(重试换随机)
	resTier      string  // 分辨率档位(空/custom=手动宽高;draft/standard/fhd 见 manjuResTiers)
	draftJudge   bool    // 智能模式草稿预审:审片返工轮用缩放分辨率草稿,全部通过后全分辨率定稿重渲
	draftScale   float64 // 草稿缩放(0.2-0.95,默认 0.5;0.5 ≈ 1/4 像素量)
	forceAttempt int     // 定点返工等外部路径传入的重试序号(seed 策略用它换 seed;0=首渲)
	sageChecked  bool    // SageAttn 节点探测已完成(每 run 一次,避免逐镜 HTTP 探测)
	sageOK       bool    // PatchSageAttentionKJ 节点存在
	sageNodeName string  // 实际存在的 SageAttn 节点名(Pathch/Patch 拼写兼容;审计 3.3 从 R 移出)
	qcRerender   map[int]int // 质检自愈重渲轮数(镜头号 → 已重渲次数;换 seed 重渲,上限后提示逃生门)
	visionOnce   sync.Once
	vision       *agent.VisionClient // 每 run 共享(粘性降级状态跨镜头保留)
}

// sageAttnGuard 检查 SageAttention 节点可用性:ComfyUI 未装对应节点时
// 硬提交会 400 missing_node_type 失败——可选加速项不阻塞渲染,自动降级关闭并提示。
// 每 run 只探测一次(逐镜探测太慢);装好节点后 config 里开关仍是开的,下次 run 自动恢复。
// 注意 KJNodes 上游把类名拼错为 PathchSageAttentionKJ(非 Patch),两个名字都探测取实际存在者。
// 审计 3.3:探测结果写入 ctx 字段而非 ctx.R——预编码 goroutine 与渲染主 goroutine 并发,
// 各自通过 applySageToR 把结果注入自己的 R 副本,消除"主流程写 R 时 goroutine 读 R"的
// 并发 map 读写(此前靠调用顺序侥幸规避)
func (ctx *manjuCtx) sageAttnGuard(lg *manjuLogger) {
	b, _ := ctx.R["sage_attention"].(bool)
	if !b || ctx.sageChecked {
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
	if ctx.sageOK && ctx.sageNodeName != "" {
		R["sage_node_name"] = ctx.sageNodeName
	} else {
		R["sage_attention"] = false
	}
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
		lg.logf("  ⚠️ 释放 ComfyUI 显存失败(忽略,继续): " + truncate(err.Error(), 80))
		return
	}
	defer resp.Body.Close()
	// 释放后稍等模型卸载完成(大模型卸载需数秒)
	time.Sleep(2 * time.Second)
	lg.logf("  🧹 已释放 ComfyUI 模型显存(编码前腾出 Qwen3-VL 空间)")
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
	if sp := strings.TrimSpace(str(P["script"])); sp != "" && fileExists(sp) {
		ctx.scriptMode = true
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

// latentNS 接缝 latent 命名空间(审计 S6):<项目>_<集>,防跨项目/跨方案/草稿定稿分辨率串接
func (ctx *manjuCtx) latentNS() string {
	ns := strings.TrimSpace(str(ctx.R["_latent_ns"]))
	if ns == "" {
		return "default"
	}
	return ns
}

// seedFor 重试 seed 策略:fixed=恒定(跨镜一致基线);increment=第 N 次重试 seed+N;
// random=重试换新随机(首渲仍用配置 seed 保持全剧基线)。attempt=0 表示首次渲染。
func (ctx *manjuCtx) seedFor(attempt int) int {
	switch ctx.seedPolicy {
	case "increment":
		return ctx.seed + attempt
	case "random":
		if attempt > 0 {
			return randSeed()
		}
	default:
		// fixed:重渲(attempt>0)也换 seed——否则同 seed 同画面,质检重渲/定点返工/终审重渲
		// 永远产出相同结果,质检不过的死循环无法打破
		if attempt > 0 {
			return ctx.seed + attempt
		}
	}
	return ctx.seed
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
	l.logf("━━━ 阶段 " + key + " ━━━")
}

func (l *manjuLogger) stopped() bool {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.stopped
}

// ---- 运行主循环(替代原 manjuWorker 的 Python 子进程) ----

// manjuPipelineRun 执行一个或多个阶段,返回退出码(0 成功)
func manjuPipelineRun(ctx *manjuCtx, phase string, lg *manjuLogger) int {
	stages := []string{"plan", "assets", "encode", "render", "qc", "assemble"}
	if phase != "all" {
		stages = []string{phase}
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
	return 0
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

// novelRootDir 小说项目根目录:优先 config 的 novel_dir;缺省时小说文件位于 全本/正文 子目录则向上取一级
func (ctx *manjuCtx) novelRootDir() string {
	if d := strings.TrimSpace(str(ctx.P["novel_dir"])); d != "" {
		return d
	}
	d := filepath.Dir(ctx.novel)
	base := strings.ToLower(filepath.Base(d))
	if base == "全本" || strings.Contains(base, "正文") {
		return filepath.Dir(d)
	}
	return d
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

// manjuChapterTotal 小说总章数(章节 0 默认值解析:从 config 读小说文件统计 # 第N章)
func manjuChapterTotal(configPath string) int {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return 0
	}
	P, _ := cfg["paths"].(map[string]any)
	novel := str(P["novel"])
	if novel == "" || !fileExists(novel) {
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
	H3Prompt   string
	TakeTail   bool        // 多切点长镜的内镜:不独立渲染,由组头一次生成覆盖
	TakeGroup  []manjuShot // 多切点长镜组头携带整组(含自身;单镜为空)
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
	plan, _, err := ctx.loadPlan()
	if err == nil {
		planC := str(plan["chapters"])
		legacy := planC == ""                               // 旧方案无分段标记
		reuse := planC == ctx.chapters                      // 范围一致
		reuse = reuse || (legacy && !ctx.auto)              // 旧方案普通模式兼容复用
		reuse = reuse || chapterRangeFullBook(ctx.chapters) // 全本请求:沿用该集既有方案
		// 小说内容指纹:改过正文必须重新生成方案,否则渲染的还是旧剧情
		// (「改了小说但视频对不上」的头号原因);旧方案无指纹记录则不强制,兼容老项目
		fp := ctx.novelFingerprint()
		planFp := str(plan["novel_fp"])
		if reuse && fp != "" && planFp != "" && planFp != fp {
			lg.logf("⚠️ 小说正文已修改(" + planFp + " → " + fp + ")，方案过期，重新生成并清空该集旧产物")
			reuse = false
			ctx.clearEpisodeArtifacts(lg)
		}
		if reuse {
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
		// 超长静默截断会让超出部分的剧情根本没进方案,视频自然对不上——必须明示
		lg.logf("  ⚠️ 内容 " + strconv.Itoa(runes) + " 字超出 20000 字上限,超出部分可能未被方案覆盖(建议缩小章节范围或分集)")
	}
	lg.logf("🤖 大模型直出 人物/场景/分镜" + manjuModeTag(ctx.scriptMode) + "...")
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
	for _, s := range shots {
		for _, ch := range s.Characters {
			if ch != "" && !charNames[ch] {
				problems = append(problems, fmt.Sprintf("镜头 %d 登场角色「%s」缺少角色卡(Ref2VA 将无参考图)", s.ID, ch))
			}
		}
		if s.Duration < 4 || s.Duration > 15 {
			problems = append(problems, fmt.Sprintf("镜头 %d 时长 %d 超出 4-15 秒", s.ID, s.Duration))
		}
		if s.Dialogue != "" {
			dialChars := 0
			for _, line := range strings.Split(s.Dialogue, "\n") {
				line = strings.TrimSpace(line)
				if i := strings.Index(line, ":"); i >= 0 {
					line = strings.TrimSpace(line[i+1:])
				}
				dialChars += len([]rune(line))
			}
			need := float64(dialChars) / 4.0 // 中文约 4 字/秒
			if need > float64(s.Duration) {
				problems = append(problems, fmt.Sprintf("镜头 %d 台词约需 %.1fs 但时长仅 %.1fs(可能截断)", s.ID, need, float64(s.Duration)))
			}
			for _, line := range strings.Split(s.Dialogue, "\n") {
				if i := strings.Index(line, ":"); i > 0 {
					speaker := strings.TrimSpace(line[:i])
					if !charNames[speaker] {
						problems = append(problems, fmt.Sprintf("镜头 %d 台词说话人「%s」不在登场角色内", s.ID, speaker))
					}
				}
			}
		}
	}
	// 反同质化(整合 ai-film-skills fingerprint.md):vars 近 3 集撞 ≥3 维 / signature 全历史撞 ≥2 项
	// → 判问题进既有「带意见修复重试」闭环(修复不了沿用原方案,不阻断)
	problems = append(problems, ctx.planFingerprintProblems(plan)...)
	return problems
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
				if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					continue
				}
				// 只纳入与方案相关的素材(排除正文/全本章节文件——正文由 ctx.novel 指纹覆盖)
				low := strings.ToLower(e.Name())
				rel := strings.ToLower(strings.TrimPrefix(p, root))
				if strings.Contains(low, "人物") || strings.Contains(low, "角色") ||
					strings.Contains(low, "场景") || strings.Contains(low, "封面") ||
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
	for _, s := range shots {
		if s.TakeTail {
			continue // 长镜内镜不独立生成提示词(由组头多切点提示词覆盖)
		}
		if s.H3Prompt == "" && prompts[strconv.Itoa(s.ID)] == "" {
			need = true
			break
		}
	}
	if !need {
		return nil
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
	// 15 镜 × 10-30s 串行 → 并发后墙钟时间约 1/4;提示词按镜 ID 落 map,顺序无关
	var todo []manjuShot
	for _, s := range shots {
		if s.TakeTail {
			continue
		}
		if s.H3Prompt == "" && prompts[strconv.Itoa(s.ID)] == "" {
			todo = append(todo, s)
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
				hp, err := ctx.genShotPrompt(s, charMap, sceneMap)
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
				hp2, err2 := ctx.genShotPromptWithFix(s, charMap, sceneMap, fix)
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
	return problems
}

// genShotPromptWithFix 带修复意见重新生成单镜提示词(校验未过修复重试用)
func (ctx *manjuCtx) genShotPromptWithFix(s manjuShot, charMap, sceneMap map[string]map[string]any, fix string) (string, error) {
	return ctx.genShotPromptRaw(s, charMap, sceneMap, fix)
}

func (ctx *manjuCtx) genShotPrompt(s manjuShot, charMap, sceneMap map[string]map[string]any) (string, error) {
	return ctx.genShotPromptRaw(s, charMap, sceneMap, "")
}

// genShotPromptRaw 生成单镜 H3 提示词;fix 非空时追加修复意见(校验未过修复重试用)
func (ctx *manjuCtx) genShotPromptRaw(s manjuShot, charMap, sceneMap map[string]map[string]any, fix string) (string, error) {
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
		return "", err
	}
	hp := str(out["h3_prompt"])
	if hp == "" {
		return "", fmt.Errorf("LLM 未返回 h3_prompt")
	}
	return hp, nil
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

// negPrompt 负面提示词(角色/场景图生成):优先配置 render.neg_prompt(用户配置),
// 空则用内置默认。用户规则(2026-08):普通管线不解析小说总集负面——
// 总集负面仅在「AI 一条龙」分析时读入并写回配置。
func (ctx *manjuCtx) negPrompt() string {
	if s := strings.TrimSpace(str(ctx.R["neg_prompt"])); s != "" {
		return s
	}
	return manjuNegPrompt
}

// characterCkpt 角色定妆照 checkpoint(按性别,兜底 animagine/sd_xl_base)
func (ctx *manjuCtx) characterCkpt(char map[string]any) string {
	cm, _ := ctx.R["char_models"].(map[string]any)
	g := str(char["gender"])
	if g == "男" {
		if s := str(cm["男"]); s != "" {
			return s
		}
	}
	if g == "女" {
		if s := str(cm["女"]); s != "" {
			return s
		}
	}
	if s := str(ctx.R["animagine_ckpt"]); s != "" {
		return s
	}
	return "sd_xl_base_1.0.safetensors"
}

// manjuPortraitW/H 定妆照固定尺寸(SDXL 原生最佳 1024×1024):
// 定妆照必须与项目画幅/分辨率档位完全解耦——同一角色在横屏/竖屏/不同档位项目里
// 若按各自 ctx.w×ctx.h 生成,构图比例与细节密度漂移,H3 参考图内容随之变化导致角色不一致。
// 固定尺寸后同角色跨项目同 prompt 同尺寸 → 形象唯一稳定。
const manjuPortraitW, manjuPortraitH = 1024, 1024

// portraitWF 定妆照工作流按风格分流:含写实元素用 Z-Image(真人级),其余用 SDXL checkpoint。
// 尺寸固定为标准 1024×1024(与项目画幅无关);正脸参考(ensureFaceCrop)再从该图按视频比例裁切。
// initImage 非空 → img2img(主图作 latent 起点保留身份,视图 full/side/detail 用,
// 防生成不相干新角色)。initStrength:denoise 强度——视图换视角需要更高(0.8)才不像主图正面,
// 0.6 对 turbo 模型重绘量太小(四视图全变正面,用户反馈)。
func (ctx *manjuCtx) portraitWF(prompt string, seed int, prefix string, char map[string]any, initImage string, initStrength float64) map[string]any {
	// 定妆引擎(2026-08-23 用户规则:按创作需求选):render.char_engine = krea2/zimage/sdxl
	// 缺省:风格含 real → Z-Image(写实人像质优);krea2=强指令跟随(复杂妆造/精细风格定妆)
	eng := strings.TrimSpace(str(ctx.R["char_engine"]))
	if eng == "krea2" {
		return wfKrea2(prompt, str(ctx.R["krea2_unet"]), str(ctx.R["krea2_clip"]), str(ctx.R["krea2_vae"]), seed, manjuPortraitW, manjuPortraitH, prefix, ctx.negPrompt(), initImage, initStrength)
	}
	if manjuStyleHas(ctx.style, "real") {
		return wfZImage(prompt, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), seed, manjuPortraitW, manjuPortraitH, prefix, ctx.negPrompt(), initImage, initStrength)
	}
	return wfSDXL(prompt, ctx.characterCkpt(char), seed, manjuPortraitW, manjuPortraitH, prefix, ctx.negPrompt(), initImage, initStrength)
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
		cmap[cid] = "characters/" + cid + ".png"
		cmap[cid+"_face"] = "characters/" + cid + "_face.png"
		if err := ctx.ensureFaceCrop(cid, lg); err != nil {
			return err
		}
		// 多视图(审计升级:角色管理/一条龙共用同一套视图资产,保障人物统一):
		// front=正脸特写(ensureFaceCrop 已生成)、full/side/detail 独立视图,
		// 用方案角色卡 views.<view> 提示词生成;旧方案无 views 则跳过(主图+正脸兜底)。
		// 用户反馈:视图纯文生图生成"不相干新角色"(含性别漂移)——视图基于主图
		// img2img(主图复制进 comfyInput 作 latent 起点,denoise 0.6 保留身份)。
		vs, _ := m["views"].(map[string]any)
		// 主图复制到 ComfyUI input(供 LoadImage 读取;按角色名安全命名)
		mainRef := ""
		if fileExists(dst) {
			refName := "dir_char_main_" + sanitizeFileName(cid) + ".png"
			if copyFile(dst, filepath.Join(ctx.comfyInput, refName)) == nil {
				mainRef = refName
			}
		}
		// 视图换视角的 denoise 强度:img2img 换视角需要高 denoise 让模型彻底重绘构图——
		// 0.8 对 Z-Image(turbo)仍大量保留主图正面构图,四视图全变正面(用户二次反馈)。
		// 提高:full 0.9(全身重绘)、side 0.92(侧面最难)、detail 0.8(细节特写保留身份)。
		// 同时提示词前置视角硬锚(英文强约束词,防模型沿主图正面惯性出图)。
		viewStrength := map[string]float64{"full": 0.9, "side": 0.92, "detail": 0.8}
		viewAnchor := map[string]string{
			"full":   "FULL BODY view, standing full figure from head to toe, entire body visible, three-quarter or front view",
			"side":   "SIDE PROFILE view, face turned exactly 90 degrees to the side, strong profile silhouette, nose and chin clearly in profile",
			"detail": "EXTREME CLOSE-UP detail shot, zoomed on the single most distinctive feature (ornament/pattern/hairstyle/scar), large detailed close-up composition",
		}
		for _, view := range []string{"full", "side", "detail", "q"} {
			p := str(vs[view])
			if p == "" {
				continue
			}
			vDst := filepath.Join(ctx.assetsDir, "characters", cid+"_"+view+".png")
			if !fileExists(vDst) {
				if view == "q" {
					// Q 版呆萌形象(2026-08-23 用户规则:内心独白渲染用):独立文生图,
					// 不基于主图(全新呆萌形象,保留角色标志特征)
					lg.logf(fmt.Sprintf("🎨 角色 %s Q版呆萌形象(内心独白专用) ...", cid))
					wf := ctx.portraitWF(p, charSeed(cid, "q"), "manju_asset", m, "", 0)
					if err := ctx.comfyGenImage(wf, vDst, lg, "角色 "+cid+"(Q版)"); err != nil {
						lg.logf("  ⚠️ Q版形象生成失败: " + err.Error())
						continue
					}
				} else {
					st := 0.9
					if s, ok := viewStrength[view]; ok {
						st = s
					}
					// 视角硬锚前置:与原有提示词拼接(原提示词已含视角描述,再强锚一遍确保权重)
					anchored := viewAnchor[view] + ", " + p
					lg.logf(fmt.Sprintf("🎨 角色 %s %s 视图(基于主图 img2img %.2f + 视角锚保持同一人) ...", cid, view, st))
					// 2026-08-23 一致性修复:多视图(全/侧/细节)固定用 Z-Image img2img 保身份——
					// Krea-2 是纯文生图架构,img2img 身份保持弱(实测视图完全变人);
					// Z-Image 已验证 img2img 保身份(主图→视角重绘)。主图仍按 char_engine(Krea-2 定身份)。
					wf := wfZImage(anchored, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), charSeed(cid, view), manjuPortraitW, manjuPortraitH, "manju_asset", ctx.negPrompt(), mainRef, st)
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
			wf := wfZImage(str(m["image_prompt"]), str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), 8000+i, ctx.w, ctx.h, "manju_asset", ctx.negPrompt(), "", 0)
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
				endPrompt := str(m["image_prompt"]) + ", the same scene at a slightly later moment, subtle motion of elements (leaves drifting, water rippling, light shifting), consistent layout and lighting"
				wf := wfZImage(endPrompt, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), 9000+i, ctx.w, ctx.h, "manju_asset", ctx.negPrompt(), "", 0)
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
	fmt.Fprintf(hh, "w=%d|h=%d|len=%d|chars=%s|scene=%s|refs=%s|fl2va_end=%t|prompt=%s",
		w, h, h3Length(s.Duration, ctx.fps),
		strings.Join(s.Characters, ","), s.Scene, strings.Join(refs, ","), endFrame, s.H3Prompt)
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
		ctx.charRefNames(s), ctx.sceneRefName(s), cacheName, len(s.Characters) > 0)
	pid, err := ctx.comfy.submit(wf)
	if err != nil {
		return err
	}
	if err := ctx.comfy.wait(pid, 1800*time.Second, 10*time.Second, lg.stopped); err != nil {
		return err
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
		for j, rel := range ctx.charViewRels(cid, i, n) {
			name := fmt.Sprintf("dir_char_%d_%d_%d.png", s.ID, i, j)
			_ = copyFile(filepath.Join(ctx.assetsDir, rel), filepath.Join(ctx.comfyInput, name))
			out = append(out, name)
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
		seed := ctx.seedFor(attempt)
		if attempt > 0 && ctx.seedPolicy != "fixed" {
			lg.logf(fmt.Sprintf("  🎲 镜头 %d 第 %d 次尝试 seed=%d(策略 %s)", s.ID, attempt+1, seed, ctx.seedPolicy))
		}
		// 审计 3.3:R 副本注入 sage 结果(不写共享 ctx.R,防预编码 goroutine 并发读)
		rCopy := make(map[string]any, len(ctx.R)+2)
		for k, v := range ctx.R {
			rCopy[k] = v
		}
		ctx.applySageToR(rCopy)
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
	if len(failed) > 0 {
		ids := make([]string, 0, len(failed))
		for n := range failed {
			ids = append(ids, strconv.Itoa(n))
		}
		sort.Strings(ids)
		lg.logf("  🔁 质检未过镜头 " + strings.Join(ids, ",") + " — 再点「一条龙/续跑」自动删旧重渲;或镜头框填编号(+集数)定点重渲")
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
	// 字幕烧录开关(2026-08-23 用户反馈):render.subtitle=false → 不烧字幕(对白仅 H3 原生音轨)
	if sub, ok := ctx.R["subtitle"].(bool); ok && !sub {
		args = append(args, "--no-subtitle")
	}
	// 转场 + BGM(数据驱动配置;seam 接缝镜清单传给脚本强制硬切,叠化重影防线)
	trans := orDefault(str(ctx.R["transition"]), "cut")
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

// ensureFaceCrop 确保角色有正脸特写参考(缺失/仍是主图副本/主图已更新时用 PIL 重裁)
func (ctx *manjuCtx) ensureFaceCrop(cid string, lg *manjuLogger) error {
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
	if err := ctx.runMedia(lg, "facecrop", "--src", mainP, "--dst", faceP, "--ratio", fmt.Sprintf("%dx%d", ctx.w, ctx.h)); err != nil {
		return fmt.Errorf("角色 %s 正脸裁剪失败: %w", cid, err)
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
	if b2, _ := ctx.R["sage_attention"].(bool); b2 {
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
	b.WriteString("🖼 SDXL checkpoint(非写实风格定妆照):\n")
	b.WriteString(check("checkpoints", "sd_xl_base_1.0.safetensors"))
	b.WriteString(check("checkpoints", "animagine-xl-3.1.safetensors"))
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
			return novel, filepath.Dir(novel)
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
			"min_shot_seconds": 4, "max_shot_seconds": 12,
			"comfy_url":      "http://127.0.0.1:8190",
			"neg_prompt":     "lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting",
			"unet_fl2va":     "MiniMax_H3_fl2va_pruned_int8_convrot.safetensors",
			"unet_ref2va":    "MiniMax_H3_ref2va_pruned_int8_convrot.safetensors",
			"clip":           "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
			"vae_video":      "minimax_h3_video_vae_fp16.safetensors",
			"vae_audio":      "minimax_h3_audio_vae_fp32.safetensors",
			"z_image_unet":   "z_image_turbo_bf16.safetensors",
			"z_image_clip":   "qwen_3_4b.safetensors",
			"z_image_vae":    "ae.safetensors",
			"krea2_unet":     "krea2_turbo_fp8_scaled.safetensors",
			"krea2_clip":     "qwen3vl_4b_fp8_scaled.safetensors",
			"krea2_vae":      "qwen_image_vae.safetensors",
			"char_engine":    "zimage", // 定妆引擎:zimage(默认写实)/krea2(强指令跟随)/sdxl
			"voiceover":      false,    // 2026-08-23 用户规则:默认 H3 自带配音;仅角色内心活动(Q版)需后期 TTS 时手动开启
			"turbo_lora":     "minimax_h3_turbo_4step_ema.safetensors",
			"animagine_ckpt": "animagine-xl-3.1.safetensors",
			"char_models":    map[string]any{"男": "sd_xl_base_1.0.safetensors", "女": "animagine-xl-3.1.safetensors"},
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
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return ""
	}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
			return str(m["gender"])
		}
	}
	return ""
}

// manjuGachaDraw 生成抽卡候选(随机 seed,一次可连抽 count 张)→ assets/characters/_gacha/<char>_<view>_s<seed>.png
// view 为空=正面主视图(半身立绘);可选 front/full/side/detail(角色卡 views 提示词,旧方案自动派生)。
// 候选落盘不覆盖(文件名含 seed),前端据此保留抽卡历史供对比挑选
func manjuGachaDraw(configPath, episode, char, view string, count int) ([]map[string]any, error) {
	if count < 1 {
		count = 1
	}
	if count > 8 {
		count = 8
	}
	view = strings.TrimSpace(view)
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return nil, err
	}
	if _, _, err := ctx.loadPlan(); err != nil {
		return nil, fmt.Errorf("角色方案未生成,请先「生成方案」")
	}
	// 抽卡候选与正式定妆照同款模型:写实→Z-Image,其余→SDXL checkpoint
	charInfo := map[string]any{"gender": ctx.gachaCharGender(char)}
	prompt := charViewPromptFor(ctx, char, view)
	safe := sanitizeFileName(char)
	lg := &manjuLogger{state: manjuState}
	out := []map[string]any{}
	for i := 0; i < count; i++ {
		seed := randSeed()
		// 抽卡=换装探索,保持随机多样性(不基于主图 img2img;采纳后定妆照覆盖)
		wf := ctx.portraitWF(prompt, seed, "manju_gacha", charInfo, "", 0)
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

// charViewPromptFor 按视图取角色提示词:优先角色卡 views.<view>,兜底 image_prompt + 视图修饰
func charViewPromptFor(ctx *manjuCtx, char, view string) string {
	plan, _, err := ctx.loadPlan()
	if err == nil {
		for _, c := range anyArr(plan["characters"]) {
			if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
				if view != "" {
					if vs, ok := m["views"].(map[string]any); ok {
						if p := str(vs[view]); p != "" {
							return p
						}
					}
				}
				if p := str(m["image_prompt"]); p != "" {
					if view == "" || view == "front" {
						return p // 半身立绘即正面主视图
					}
					return p + ", " + manjuViewSuffix(view) // 旧方案无 views:派生修饰
				}
			}
		}
	}
	return "portrait of " + char + ", " + manjuAssetStyle(ctx.style) + ", upper body, detailed face, clean background"
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
func (ctx *manjuCtx) charViewRels(cid string, idx, total int) []string {
	var picks []string
	switch {
	case total <= 1:
		picks = []string{"front", "full", "detail"}
	case total == 2:
		picks = []string{"front", "full"}
	default: // 3 角色
		if idx == 0 {
			picks = []string{"front", "full"}
		} else {
			picks = []string{"front"}
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
	// 内心戏镜(2026-08-23 用户规则):narration 含「内心·」→ 该角色参考优先用 Q 版图
	innerChars := map[string]bool{}
	if strings.Contains(s.Narration, "内心·") {
		for _, cid := range s.Characters {
			innerChars[cid] = true
		}
	}
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		rels := ctx.charViewRels(cid, i, n)
		if innerChars[cid] {
			// 内心戏:Q 版形象优先(front 锁脸 + q 呆萌),供 h3_prompt 引用 Q 版图
			qRel := manjuViewRel(cid, "q")
			if fileExists(filepath.Join(ctx.assetsDir, qRel)) {
				rels = []string{manjuViewRel(cid, "front"), qRel}
			}
		}
		for _, rel := range rels {
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
		return ctx.ensureFaceCrop(char, lg)
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
