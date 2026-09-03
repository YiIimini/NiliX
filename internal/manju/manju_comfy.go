package manju

// 漫剧管线 ComfyUI 客户端 + 工作流构建
// 节点图重建依据:ComfyUI /object_info 节点 schema + 官方 MiniMaxH3 模板(video_minimax_h3_r2v.json)
//   + ComfyUI-H3-Motion-Context 示例工作流 + ComfyUI-H3-ConditioningCache 源码。

import (
	"nilix/internal/util"
	"fmt"
	"log"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
)



// comfyReachable ComfyUI 服务本身是否可达(根路径快速探测,200ms 超时)。
// 丢失检测用它区分"服务忙"(接口超时=可达但忙)与"服务真没了"(不可达)。

// wait 轮询执行完成;中断/错误返回错误(含异常信息,供调用方判断是否重试)
// wait 轮询执行完成;中断/错误返回错误(含异常信息,供调用方判断是否重试)。
// stopped 为可选停止感知回调:用户点「停止」后立即返回"已停止"错误,
// 不再死等 ComfyUI(尤其卡在模型加载/排队的任务,/interrupt 无法中断它们)。
// 停止感知用 500ms 细粒度轮询(不随 poll 间隔变慢——poll 可能 10s,停止要立即生效)。

// comfyErrMsg 从 status 提取最可读的异常信息(优先 exception_message)

// 提取执行结果里的媒体文件(遍历所有节点输出)
func comfyOutputFiles(entry map[string]any, key string) []string {
	var out []string
	outputs, _ := entry["outputs"].(map[string]any)
	for _, v := range outputs {
		nm, _ := v.(map[string]any)
		arr, _ := nm[key].([]any)
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				fn, sub := str(m["filename"]), str(m["subfolder"])
				out = append(out, filepath.ToSlash(filepath.Join(sub, fn)))
			}
		}
	}
	return out
}

func comfyOutputVideo(entry map[string]any) string {
	files := comfyOutputFiles(entry, "video")
	if len(files) > 0 {
		return files[0]
	}
	// SaveVideo 在 history 里以 images 键上报(animated=true)
	files = comfyOutputFiles(entry, "images")
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

func comfyOutputImage(entry map[string]any) string {
	files := comfyOutputFiles(entry, "images")
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// ---- 工作流构建 ----

// h3Length 镜头时长(秒) → 帧数(H3 17k+5 网格 @24fps,与官方模板 ComfyMathExpression 同公式)
// 注意 Go 的 % 对负数取模与 Python 不同,须归一为非负(否则长度会少 17 的倍数)
func h3Length(seconds, fps int) int {
	base := max(5, (seconds*fps+1)/2*2)
	delta := (5 - base%17) % 17
	if delta < 0 {
		delta += 17
	}
	return base + delta
}

// wfImage 通用图生图工作流(SDXL checkpoint 或 Z-Image unet),返回 SaveImage 节点 id
// initImage 非空时走 img2img:主图作 latent 起点(VAEEncode),denoise 0.6 保留身份、
// 按提示词重绘视角/构图(定妆照多视图与主图保持同一人,防"侧面/全身变成不相干新角色")
// 审计升级:denoise 0.6 对 Z-Image(turbo 8 步)重绘量太小,四视图全变正面——
// 视图换视角需要更高 denoise(0.8),用 initStrength 参数(缺省 0.6)
func wfImage(workflow map[string]any, typ, prompt, neg string, seed, w, h, steps int, cfg float64, ckpt, unet, clipName, clipType, vae, prefix, initImage string, initStrength float64) string {
	n := len(workflow) + 1
	add := func(classType string, inputs map[string]any) string {
		id := itoa(n)
		n++
		workflow[id] = map[string]any{"class_type": classType, "inputs": inputs}
		return id
	}
	var modelID, clipID, vaeID string
	if typ == "sdxl" {
		cid := add("CheckpointLoaderSimple", map[string]any{"ckpt_name": ckpt})
		modelID, clipID, vaeID = cid+"[0]", cid+"[1]", cid+"[2]"
	} else {
		mid := add("UNETLoader", map[string]any{"unet_name": unet, "weight_dtype": "default"})
		clipID = add("CLIPLoader", map[string]any{"clip_name": clipName, "type": clipType})
		vaeID = add("VAELoader", map[string]any{"vae_name": vae})
		modelID = mid + "[0]"
	}
	pos := add("CLIPTextEncode", map[string]any{"clip": refOf(clipID), "text": prompt})
	negID := add("CLIPTextEncode", map[string]any{"clip": refOf(clipID), "text": neg})
	var latentID string
	denoise := 1.0
	if initImage != "" {
		load := add("LoadImage", map[string]any{"image": initImage})
		// 2026-08-27 修复:img2img latent 尺寸此前=init 图原尺寸(w/h 参数被忽略)——主图
		// 方形 1024 导致 full/side 竖幅目标(832×1248)失效,视图永远方形半身。插 ImageScale
		// 把 init 图缩放到目标画幅(latent 尺寸随动);高重绘(0.9+)下 init 仅作构图引导。
		scaled := add("ImageScale", map[string]any{"image": refOf(load), "upscale_method": "lanczos", "width": w, "height": h, "crop": "disabled"})
		latentID = add("VAEEncode", map[string]any{"pixels": refOf(scaled), "vae": refOf(vaeID)})
		if initStrength > 0 && initStrength < 1 {
			denoise = initStrength
		} else {
			denoise = 0.6
		}
	} else if typ == "sdxl" {
		latentID = add("EmptyLatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	} else {
		latentID = add("EmptySD3LatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	}
	samp := add("KSampler", map[string]any{
		"model": refOf(modelID), "positive": refOf(pos), "negative": refOf(negID),
		"latent_image": refOf(latentID), "seed": seed, "steps": steps, "cfg": cfg,
		"sampler_name": "euler", "scheduler": "normal", "denoise": denoise,
	})
	dec := add("VAEDecode", map[string]any{"samples": refOf(samp), "vae": refOf(vaeID)})
	return add("SaveImage", map[string]any{"images": refOf(dec), "filename_prefix": prefix})
}

// refOf 把一个 "N" 或 "N[0]" 形式的引用转为 API 引用值
func refOf(id string) any {
	if i := strings.Index(id, "["); i > 0 {
		return []any{id[:i], mustAtoi(id[i+1 : len(id)-1])}
	}
	return []any{id, 0}
}

func itoa(n int) string { return util.Itoa(n) }
func mustAtoi(s string) int { return util.MustAtoi(s) }

// wfSDXL 人物定妆照/抽卡(SDXL checkpoint)——2026-08-24 用户规则:SDXL 已禁用(观感差),
// 定妆照一律 Z-Image/Krea-2。本函数保留仅供兼容历史 workflow/测试引用,生产管线不再调用。
func wfSDXL(prompt, ckpt string, seed, w, h int, prefix, neg, initImage string, initStrength float64) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "sdxl", prompt, neg, seed, w, h, 25, 7.0, ckpt, "", "", "", "", prefix, initImage, initStrength)
	return wf
}

// wfZImage 场景图/写实定妆照(8 步 turbo;neg 为负面提示词,缺省空串)
// initImage 非空 → img2img(保留身份重绘视角;视图生成用);initStrength 0-1(缺省 0.6)
func wfZImage(prompt, unet, clipName, vae string, seed, w, h int, prefix, neg, initImage string, initStrength float64) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "zimage", prompt, neg, seed, w, h, 8, 1.0, "", unet, clipName, "qwen_image", vae, prefix, initImage, initStrength)
	return wf
}

// wfKrea2 Krea-2 Turbo 定妆照(2026-08-23 接入:8 步蒸馏;超强指令跟随,
// 复杂妆造/精细风格/特殊审美定妆用;模型=Comfy-Org/Krea-2 的
// krea2_turbo_fp8_scaled + qwen3vl_4b_fp8_scaled + qwen_image_vae)
// CLIPLoader type 必须为 "krea2"(12 层 Qwen3-VL stack;用 qwen_image 会报
// "Krea2 expects conditioning with 12x2560=30720 features" 执行失败)
func wfKrea2(prompt, unet, clipName, vae string, seed, w, h int, prefix, neg, initImage string, initStrength float64) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "krea2", prompt, neg, seed, w, h, 8, 1.0, "", unet, clipName, "krea2", vae, prefix, initImage, initStrength)
	return wf
}

// manjuNegPrompt 内置负面(2026-08-27 三修:前置服装禁裸段——精卫 Q 版袒胸实锤负面通道
// 此前对裸露零防御:正向否定句(no cleavage 写在正向里)对 Z-Image 遵循弱,负面词才是
// 强通道;chibi/手办/BJD 语义自带性感素体先验,必须负面显式压制)
// render.neg_prompt 未配置时的兜底;已配置时 negPrompt() 追加在内置之后(只增不减)。
// 2026-08-23 用户规则:动漫风格也禁止日本人物形象——禁日本式脸型/日漫大眼,不禁 anime/cartoon 风格词本身
// 2026-08-24 用户规则升级:加防真人(photorealistic/real person/actual photo)——定妆照必须「写实拟动漫」,
// 既不是日漫脸也不是真人照片(真人=侵权风险)。与正向 manjuPortraitAnchor 双路夹击。
// 2026-08-27 用户反馈(小男孩全身图下半身裸露没穿裤子):负面词此前全是躯干裸露词
// (bare chest/torso/...),腰部以下零覆盖——补下半身裸露词组(措辞精确到「腰部以下裸/
// 无下装/露下体」,不用 bare legs 字样以免误伤裙装角色的正常露小腿)。
const manjuNegPrompt = "nsfw, nudity, nude, naked, bare chest, bare torso, exposed chest, exposed torso, exposed breasts, cleavage, deep neckline, low-cut top, open jacket showing skin, open coat showing skin, open robe showing skin, unbuttoned shirt, lingerie, underwear as outerwear, shirtless, topless, naked from the waist down, no pants, missing trousers, missing skirt, no lower clothing, bottomless, bare hips, bare bottom, exposed crotch, exposed genitals, lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting, japanese anime face, japanese manga face, japanese-style face, japanese cartoon character, anime eyes, manga eyes, big sparkly anime eyes, sharp anime chin, photorealistic, real person, real human, actual photo, photograph, realistic photo, lifelike human, portrait photo, cigarette, smoking, cigar, smoke, alcohol, beer, wine, liquor, whiskey, bottle of wine, drunk, drinking alcohol"

// manjuModelRefs 载入 H3 三件套(clip / vae_video / vae_audio),返回 [clip, vae, audioVae]
func h3Loaders(workflow map[string]any, R map[string]any) (clip, vae, audioVae string) {
	clip = wfAdd(workflow, "CLIPLoader", map[string]any{"clip_name": str(R["clip"]), "type": "minimax"})
	vae = wfAdd(workflow, "VAELoader", map[string]any{"vae_name": str(R["vae_video"])})
	audioVae = wfAdd(workflow, "VAELoader", map[string]any{"vae_name": str(R["vae_audio"])})
	return
}

func wfAdd(workflow map[string]any, classType string, inputs map[string]any) string {
	id := itoa(len(workflow) + 1)
	workflow[id] = map[string]any{"class_type": classType, "inputs": inputs}
	return id
}

// h3EncWorkflow 预编码工作流(只跑 Qwen3-VL,无 UNET;输出 CondSave 缓存 .pt)
// hasChar: 有角色 → MiniMaxH3ReferenceToVideo(角色+场景多参考);空镜 → MiniMaxH3ImageToVideo(场景首帧),
// 若 R["_scene_end"] 提供尾帧且节点可用 → MiniMaxH3Fl2VA 首尾双帧插值(审计升级 P1)
// charRefs: 全部登场角色的参考图(正脸优先),多角色同镜逐一传入锁身份
// charVoices(2026-08-26 音色锁定):该镜登场说话角色的配音音色音频(ComfyUI input 相对路径,
// 如 audio/voice_EP01_阿拾.mp3),按登场顺序传入,与 prompt 的 <Audio N> 编号一一对应;
// 仅角色镜(Ref2VA)支持音频参考,空镜节点无 ref_audios 输入
func h3EncWorkflow(R map[string]any, prompt string, w, h, length int, charRefs []string, charVoices []string, sceneRef, cacheName string, hasChar bool) map[string]any {
	wf := map[string]any{}
	clip, vae, audioVae := h3Loaders(wf, R)
	var condID string
	if hasChar {
		inputs := map[string]any{
			"clip": refOf(clip), "vae": refOf(vae), "audio_vae": refOf(audioVae),
			"prompt": prompt, "width": w, "height": h, "length": length, "ref_image_size": "match",
		}
		var refs []any
		for _, cr := range charRefs { // 多角色:每个登场角色一张参考图(正脸优先)
			if cr == "" {
				continue
			}
			refs = append(refs, refOf(wfAdd(wf, "LoadImage", map[string]any{"image": cr})))
		}
		if sceneRef != "" {
			refs = append(refs, refOf(wfAdd(wf, "LoadImage", map[string]any{"image": sceneRef})))
		}
		// 2026-08-24 实测修复:ref_images 必须用 Autogrow 平铺键(ref_image_0/1/2...)。
		// 旧代码传数组 []any——ComfyUI 新版(MiniMaxH3ReferenceToVideo 的 ref_images 是
		// COMFY_AUTOGROW_V3,TemplatePrefix "ref_image_")静默忽略数组,参考图从未编进条件缓存,
		// 渲染全部按纯文本生成 → 同一场景镜头画面趋同(用户实测 EP01 六镜几乎一模一样)。
		// 实测:数组/嵌套 dict 均只产出 3 tokens 纯文本缓存(63KB);平铺键产出 5120 tokens
		// 含参考图编码(21MB)。补齐场景图后序号从角色图之后继续。
		if len(refs) > 0 {
			for i, r := range refs {
				inputs[fmt.Sprintf("ref_images.ref_image_%d", i)] = r
			}
		}
		// 音色参考(2026-08-26 H3 原生音色锁定,验证通过):该镜说话角色的配音音色音频,
		// 平铺键 ref_audios.ref_audio_N(与 ref_images 并列,互不冲突),<Audio N+1> 与
		// prompt subject_definitions 的音色定义一一对应
		for i, av := range charVoices {
			if av == "" {
				continue
			}
			aud := wfAdd(wf, "LoadAudio", map[string]any{"audio": av})
			inputs[fmt.Sprintf("ref_audios.ref_audio_%d", i)] = refOf(aud)
		}
		condID = wfAdd(wf, "MiniMaxH3ReferenceToVideo", inputs)
	} else {
		var sceneLoad string
		if sceneRef != "" {
			sceneLoad = wfAdd(wf, "LoadImage", map[string]any{"image": sceneRef})
		}
		// 双帧(FL2VA 语义)与单帧统一走核心节点 MiniMaxH3ImageToVideo:
		// ComfyUI 0.33+ 核心节点自带 first_frame + last_frame 双帧插值参数
		// (last_frame 即尾帧锚点)。不再用自定义节点 MiniMaxH3Fl2VA——
		// 该节点不存在(ComfyUI 对缺失节点 /object_info 也返回 200 空对象,
		// 提交时 400 missing_node_type,用户实测)。
		inputs := map[string]any{
			"clip": refOf(clip), "vae": refOf(vae),
			"prompt": prompt, "width": w, "height": h, "length": length,
		}
		if sceneLoad != "" {
			inputs["first_frame"] = refOf(sceneLoad)
		}
		// 尾帧存在(fl2va_end_frame 开启 + 场景尾帧已生成) → 首尾双帧插值
		if end := strings.TrimSpace(str(R["_scene_end"])); end != "" {
			endLoad := wfAdd(wf, "LoadImage", map[string]any{"image": end})
			inputs["last_frame"] = refOf(endLoad)
		}
		condID = wfAdd(wf, "MiniMaxH3ImageToVideo", inputs)
	}
	wfAdd(wf, "MiniMaxH3CondSave", map[string]any{"conditioning": refOf(condID), "cache_name": cacheName})
	return wf
}

// turboLoRASpec 不同 Turbo LoRA 的最优参数(按文件名识别,数据驱动可扩展):
// 采样器/强度/步数/shift 不兼容会明显劣化画质甚至出废片,换 LoRA 无需改代码。
// 2026-08-26 v0.34.0 升级同步 lightx2v 官方家族(ModelTC/Minimax-H3-Turbo specs +官方工作流):
// 参数依据 = 官方 example_workflows 实测值:euler + 强度 1.0 + MiniMaxH3SigmaShift + simple,
// FL2V 768p 版 shift 6/3(训练分辨率 1344×768,与生产 768×1344 对口),544p/Ref2V 版 12/3。
type turboLoRASpec struct {
	Strength   float64
	Sampler    string
	Scheduler  string
	Steps      int
	FL2VOnly   bool    // FL2V 专用蒸馏版:R2V 镜头不挂(除非另配 turbo_lora_r2v)
	R2VOnly    bool    // R2V 专用蒸馏版(lightx2v ref2v):FL2V 空镜不挂,自动回退全步数
	VideoShift float64 // >0 时 model 链挂 MiniMaxH3SigmaShift(蒸馏训练 shift,官方工作流同款)
	AudioShift float64
	PDD        bool // PDD Acc 模式(2026-08-29):MiniMaxH3PDDAccApply 专用节点应用+输出 sigmas,
	// shift 12/3 由节点内置校验,euler+cfg 1.0+steps 8 为官方强制配方;不叠加其它 distill LoRA
}

// turboLoRASpecOf 不同 Turbo LoRA 的最优参数(按文件名识别,数据驱动可扩展):
// 采样器/强度/步数/shift 不兼容会明显劣化画质甚至出废片,换 LoRA 无需改代码。
// 2026-08-29 PDD Acc 接入:alibaba-pai MiniMax-H3-Acc-LoRAs(官方 8 步 Parallel
// Decoding Distillation,2026-08-26 发布)——专用节点 MiniMaxH3PDDAccApply 应用
// LoRA+PDD head bank 并输出 sigmas,euler/cfg 1.0/shift 12-3 为强制配方(节点内置校验),
// 不叠加其它 distill LoRA。文件名含 pdd_acc 即启用,与既有 4step turbo 并存可选。
func turboLoRASpecOf(name string) turboLoRASpec {
	n := strings.ToLower(name)
	step := 4
	if strings.Contains(n, "8step") {
		step = 8
	}
	switch {
	case strings.Contains(n, "pdd_acc"):
		return turboLoRASpec{Strength: 1.0, Sampler: "euler", Scheduler: "simple", Steps: 8, PDD: true, VideoShift: 12, AudioShift: 3}
	case strings.Contains(n, "ref2v") && (strings.Contains(n, "turbo") || strings.Contains(n, "step")):
		// lightx2v Ref2VA Turbo(角色镜专用):官方 ref2v 工作流 euler/1.0/Shift(12,3)
		return turboLoRASpec{Strength: 1.0, Sampler: "euler", Scheduler: "simple", Steps: step, R2VOnly: true, VideoShift: 12, AudioShift: 3}
	case strings.Contains(n, "fl2v") || strings.Contains(n, "lightx2v") || strings.Contains(n, "kijai"):
		// lightx2v FL2VA Turbo 家族(空镜/文生视频,均为 4/8 步蒸馏,缺步数标记按 4 步):
		// 768p 版 shift 6/3(训练分辨率 1344×768=生产档),544p 版 12/3
		shift := 12.0
		if strings.Contains(n, "768p") {
			shift = 6.0
		}
		return turboLoRASpec{Strength: 1.0, Sampler: "euler", Scheduler: "simple", Steps: step, FL2VOnly: true, VideoShift: shift, AudioShift: 3}
	}
	// larryvrh 系 4step EMA 等旧默认(FL2V/R2V 通用,无官方 shift)
	return turboLoRASpec{Strength: 0.8, Sampler: "res_multistep", Scheduler: "simple", Steps: 8}
}

// manjuResTiers 数据驱动分辨率档位:tier → 短边像素(对齐 minimax-h3-starter 的 416P/768P 分层理念)。
// 档位按配置画幅等比换算,两边都对齐 32(H3 VAE 32× 下采样网格;1080 非 32 倍数,fhd 用 1088);
// 未配置/custom = 直接用 width×height 手动值。新增档位只改这张表(数据驱动,不加代码分支)。
var manjuResTiers = map[string]int{"draft": 416, "standard": 768, "fhd": 1088}

// manjuAlign32 对齐到最近的 32 倍数(至少 32)
func manjuAlign32(n int) int {
	if n < 32 {
		return 32
	}
	return (n + 16) / 32 * 32
}

// manjuResTierDims 档位 × 画幅 → 实际宽高(保持宽高比,短边=档位值,另一边对齐 32);
// tier 无效返回原值 + false(调用方保持手动宽高)。
func manjuResTierDims(tier string, w, h int) (int, int, bool) {
	short, ok := manjuResTiers[tier]
	if !ok || w <= 0 || h <= 0 {
		return w, h, false
	}
	if w <= h { // 竖屏:宽为短边
		return manjuAlign32(short), manjuAlign32(h * short / w), true
	}
	return manjuAlign32(w * short / h), manjuAlign32(short), true
}

// h3RenderWorkflow 采样渲染工作流:CondLoad 加载条件缓存(跳过重复 Qwen3-VL 编码)
// + EmptyMiniMaxH3LatentAV 空 AV latent + Turbo LoRA + 可选 MotionContext 接缝。
// 有角色用 ref2va 模型,空镜用 fl2va;接缝时 LoadLatent(prevIdx) → MotionContext → Trim,
// 无论是否接缝都 SaveLatent(curIdx),供下一镜续接。
// latent 命名空间取自 R["_latent_ns"](审计 S6):output/h3_context/<ns>/clip_NNNNN,
// 防跨项目/跨方案/草稿定稿分辨率互相串接;缺省回退 default(测试兼容)。
func h3RenderWorkflow(R map[string]any, seed, w, h, length, steps int, cacheName string, hasChar, chained bool, prevIdx, curIdx int) map[string]any {
	wf := map[string]any{}
	latentNS := strings.TrimSpace(str(R["_latent_ns"]))
	if latentNS == "" {
		latentNS = "default"
	}
	unetName := str(R["unet_ref2va"])
	if !hasChar {
		unetName = str(R["unet_fl2va"])
	}
	model := wfAdd(wf, "UNETLoader", map[string]any{"unet_name": unetName, "weight_dtype": "default"})
	// Turbo LoRA 选择与参数自动适配:
	// - R2V(角色镜)优先 turbo_lora_r2v(未配置沿用 turbo_lora);
	//   若解析到 FL2V 专用蒸馏版(Kijai LightX2V)则 R2V 自动摘除并回退全步数,防不兼容劣化
	// - FL2V(空镜)用 turbo_lora;强度/采样器按 LoRA 类型参数表
	loraName := str(R["turbo_lora"])
	if hasChar {
		if r2v := str(R["turbo_lora_r2v"]); r2v != "" {
			loraName = r2v
		}
	}
	spec := turboLoRASpecOf(loraName)
	if hasChar && spec.FL2VOnly && str(R["turbo_lora_r2v"]) == "" {
		// FL2V 专用 LoRA 不挂 R2V:LoRA/采样器/步数全部回退默认,保证角色镜质量
		loraName = ""
		spec = turboLoRASpecOf("")
		if n, ok := manjuToInt(R["steps"]); ok && n > 0 {
			steps = n
		}
	}
	if !hasChar && spec.R2VOnly {
		// 对称回退:R2V 专用 LoRA(lightx2v ref2v)不挂 FL2V 空镜
		loraName = ""
		spec = turboLoRASpecOf("")
		if n, ok := manjuToInt(R["steps"]); ok && n > 0 {
			steps = n
		}
	}
	// LoRA 应用:PDD Acc 走专用节点(输出 [0]=model,[1]=sigmas,shift 12/3 内置校验);
	// 普通 distill LoRA 走 LoraLoaderModelOnly + MiniMaxH3SigmaShift。
	// PDD 节点未安装(ComfyUI 未重启/缺 custom_node)时回退普通模式并告警——
	// 提交 400 missing_node_type 会白烧一轮,探测优于失败。
	pddApplyID := ""
	if spec.PDD && loraName != "" {
		if ok, _ := R["_pdd_ok"].(bool); ok {
			a := wfAdd(wf, "MiniMaxH3PDDAccApply", map[string]any{"model": refOf(model), "lora_name": loraName})
			model = a + "[0]"
			pddApplyID = a + "[1]"
		} else {
			log.Printf("⚠️ PDD Acc 节点未安装(ComfyUI-MiniMax-H3-PDD-Acc),回退普通 LoRA 模式: %s", loraName)
			loraName = ""
			spec = turboLoRASpecOf("")
		}
	}
	if loraName != "" && !spec.PDD {
		model = wfAdd(wf, "LoraLoaderModelOnly", map[string]any{"model": refOf(model), "lora_name": loraName, "strength_model": spec.Strength})
	}
	// SageAttention 加速补丁(KJNodes,starter 官方工作流同款):
	// 长序列注意力量化加速,RTX 50 系白捡提速。默认关。
	// 节点名用 sageAttnGuard 探测到的实际注册名——KJNodes 上游把类名拼错为
	// PathchSageAttentionKJ(非 Patch),用错名字 ComfyUI 会报 missing_node_type 400。
		if sageEnabled(R) {
			nodeName := "PatchSageAttentionKJ"
			if n := str(R["sage_node_name"]); n != "" {
				nodeName = n
			}
			model = wfAdd(wf, nodeName, map[string]any{
				"model": refOf(model), "sage_attention": "auto", "allow_compile": false,
			})
		}
	// SigmaShift 挂 LoRA 之后(官方 lightx2v 工作流:蒸馏 shift 是采样网格的一部分,
	// BasicGuider 与 BasicScheduler 共用 shift 后的 model;768p 版 6/3,544p/Ref2V 版 12/3)。
	// PDD 模式不挂(节点内置 shift 校验,配错拒绝运行)
	if spec.VideoShift > 0 && !spec.PDD {
		model = wfAdd(wf, "MiniMaxH3SigmaShift", map[string]any{
			"model": refOf(model), "shift_video": spec.VideoShift, "shift_audio": spec.AudioShift,
		})
	}
	vae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_video"])})
	audioVae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_audio"])})

	condID := wfAdd(wf, "MiniMaxH3CondLoad", map[string]any{"cache_name": cacheName})
	latentID := wfAdd(wf, "EmptyMiniMaxH3LatentAV", map[string]any{"width": w, "height": h, "length": length})

	// 接缝:MotionContext(condLoad 条件 + 上一镜 latent)→ conditioning + trim_frames
	trimFramesID := ""
	if chained {
		latLoad := wfAdd(wf, "MiniMaxH3MotionContextLoadLatent", map[string]any{"latent_path": "h3_context/" + latentNS, "clip_index": prevIdx})
		// audio_context_length 默认 1(≈25ms,2026-08-26 多重配音修复):MotionContext 会把上一镜
		// 尾部音频 pin 进本镜,H3 设计上"续念"该音频——与 <d> 标记的本镜台词并行 = 两路人声
		// 交叠(上一镜台词尾音被重复念一遍)。短剧一镜一句台词、台词结尾即切镜,叠音伤害远大于
		// 音频不连续,默认只 pin 25ms 保音画对齐。需要音频连续接缝可配 render.motion_audio_context
		// =24(0.6s)。注意节点语义 a_frames = int(v) or span:传 0 是 falsy 反而取全窗口,禁传 0。
		audioCtx := "1"
		if n, ok := manjuToInt(R["motion_audio_context"]); ok && n >= 1 && n <= 96 {
			audioCtx = strconv.Itoa(n)
		}
		mc := wfAdd(wf, "MiniMaxH3MotionContext", map[string]any{
			"conditioning": refOf(condID), "vae": refOf(vae), "latent": refOf(latentID),
			"context_length": "22", "audio_context_length": audioCtx,
			"context_latent": refOf(latLoad),
		})
		// MotionContext 输出 0=conditioning, 1=trim_frames
		condID = mc + "[0]"
		trimFramesID = mc + "[1]"
	}

	// PDD Acc 官方强制配方 cfg=1.0(无 CFG 单次前向);普通模式沿用默认
	guiderInputs := map[string]any{"model": refOf(model), "conditioning": refOf(condID)}
	if spec.PDD {
		guiderInputs["cfg"] = 1.0
	}
	guider := wfAdd(wf, "BasicGuider", guiderInputs)
	noise := wfAdd(wf, "RandomNoise", map[string]any{"noise_seed": seed})
	// 采样器随 Turbo LoRA 类型:Kijai LightX2V 4步版必须 sa_solver(er_sde 亦可),旧 larryvrh 系用 res_multistep
	sampler := wfAdd(wf, "KSamplerSelect", map[string]any{"sampler_name": spec.Sampler})
	// sigmas 来源:PDD 模式用 Apply 节点输出(含训练 shift 的采样网格),普通模式 BasicScheduler
	sigmasID := pddApplyID
	if sigmasID == "" {
		sigmasID = wfAdd(wf, "BasicScheduler", map[string]any{"model": refOf(model), "scheduler": spec.Scheduler, "steps": steps, "denoise": 1.0})
	}
	samp := wfAdd(wf, "SamplerCustomAdvanced", map[string]any{
		"noise": refOf(noise), "guider": refOf(guider), "sampler": refOf(sampler),
		"sigmas": refOf(sigmasID), "latent_image": refOf(latentID),
	})

	imgID := wfAdd(wf, "VAEDecode", map[string]any{"samples": refOf(samp), "vae": refOf(vae)})
	audID := wfAdd(wf, "VAEDecodeAudio", map[string]any{"samples": refOf(samp), "vae": refOf(audioVae)})

	// 接缝裁掉 burn-in 前缀(视频/音频),否则直接用解码输出
	if chained {
		trim := wfAdd(wf, "MiniMaxH3MotionContextTrim", map[string]any{
			"images": refOf(imgID), "trim_frames": refOf(trimFramesID), "audio": refOf(audID),
			"fps": float64(h3Fps(R)), "match_tail": true,
		})
		imgID = trim + "[0]"
		audID = trim + "[1]"
	}
	video := wfAdd(wf, "CreateVideo", map[string]any{"images": refOf(imgID), "fps": float64(h3Fps(R)), "audio": refOf(audID)})
	wfAdd(wf, "SaveVideo", map[string]any{"video": refOf(video), "filename_prefix": "manju", "format": "auto", "codec": "auto"})

	// 保存当前镜 latent 供下一镜续接(总是保存,即使未接缝);按命名空间隔离(审计 S6)
	wfAdd(wf, "MiniMaxH3MotionContextSaveLatent", map[string]any{
		"latent": refOf(samp), "filename_prefix": "h3_context/" + latentNS + "/clip", "clip_index": curIdx,
	})
	return wf
}

func h3Fps(R map[string]any) int {
	fps := 24
	if n, ok := manjuToInt(R["fps"]); ok && n > 0 {
		fps = n
	}
	return fps
}

// h3ContextLatentPath 上一镜 latent 落盘路径(output/h3_context/<ns>/clip_%05d.safetensors);
// ns 为命名空间(项目_集,审计 S6 防跨项目串接)
func h3ContextLatentPath(comfyOutput, ns string, idx int) string {
	return filepath.Join(comfyOutput, "h3_context", ns, fmt.Sprintf("clip_%05d.safetensors", idx))
}

func h3CachePath(sharedModels, cacheName string) string {
	return filepath.Join(sharedModels, "conditioning", cacheName+".pt")
}

// randSeed 抽卡随机 seed
func randSeed() int {
	return rand.Intn(1<<31 - 1)
}

// dirExists 判断目录存在
func dirExists(p string) bool { return util.DirExists(p) }

// fileExists 判断文件存在
func fileExists(p string) bool { return util.FileExists(p) }
