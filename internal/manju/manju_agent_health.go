package manju

// 漫剧智能体 · 项目体检 / 一键修复 / 自然语言指令 / 记忆学习汇总。
// 体检为纯本地检查(不调 LLM,秒回),发现的可修复项可一键写回 config.json。

import (
	"nilix/internal/paths"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// manjuHealthItem 一条体检项
type manjuHealthItem struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Status  string `json:"status"` // ok / warn / bad
	Detail  string `json:"detail"`
	Fixable bool   `json:"fixable"`
	FixHint string `json:"fixHint,omitempty"`
}

// manjuHealthCheck 全项体检(本地、秒回、不调 LLM)
func manjuHealthCheck(ctx *manjuCtx) []manjuHealthItem {
	items := []manjuHealthItem{}
	R := ctx.R
	ok := func(label, detail string) manjuHealthItem { return manjuHealthItem{Key: label, Label: label, Status: "ok", Detail: detail} }
	// 1. 渲染风格
	if ctx.style == "" {
		items = append(items, manjuHealthItem{Key: "style", Label: "渲染风格", Status: "warn", Detail: "未设置风格", FixHint: "在渲染配置里选预设或输自定义"})
	} else {
		items = append(items, ok("style", "风格: "+ctx.style))
	}
	// 2. 输入源(小说正文 / 视频脚本直出,二选一)
	if ctx.scriptMode {
		// 脚本模式:检查脚本文件而非小说
		if !fileExists(ctx.novel) {
			items = append(items, manjuHealthItem{Key: "novel", Label: "视频脚本", Status: "bad", Detail: "脚本文件不存在: " + ctx.novel, FixHint: "「视频脚本直出」卡片重新粘贴脚本"})
		} else if b, err := os.ReadFile(ctx.novel); err != nil || len([]rune(string(b))) < 20 {
			items = append(items, manjuHealthItem{Key: "novel", Label: "视频脚本", Status: "warn", Detail: "脚本内容过少或读取失败", FixHint: "确认脚本包含 [Shot N] 分镜内容"})
		} else {
			items = append(items, manjuHealthItem{Key: "novel", Label: "视频脚本", Status: "ok", Detail: fmt.Sprintf("视频脚本 %d 字,脚本直出模式", len([]rune(string(b))))})
		}
	} else if ctx.novel == "" {
		items = append(items, manjuHealthItem{Key: "novel", Label: "小说正文", Status: "bad", Detail: "未设置小说文件", FixHint: "渲染配置 → 小说 里选正文,或「视频脚本直出」卡片粘贴脚本"})
	} else if !fileExists(ctx.novel) {
		items = append(items, manjuHealthItem{Key: "novel", Label: "小说正文", Status: "bad", Detail: "文件不存在: " + ctx.novel, FixHint: "检查 config.json 的 paths.novel"})
	} else if b, err := os.ReadFile(ctx.novel); err != nil || len([]rune(string(b))) < 200 {
		items = append(items, manjuHealthItem{Key: "novel", Label: "小说正文", Status: "warn", Detail: "内容过少或读取失败", FixHint: "确认正文是完整小说文件"})
	} else {
		items = append(items, ok("novel", fmt.Sprintf("正文 %d 字,可渲染", len([]rune(string(b))))))
	}
	// 2.5 分镜脚本目录(爽文技能阶段6 产物:H3分镜脚本文档模板生成,可一键导入脚本直出)
	if !ctx.scriptMode {
		if nv := strings.TrimSpace(str(ctx.P["novel"])); nv != "" {
			if ms, _ := filepath.Glob(filepath.Join(nv, "素材", "分镜脚本", "第*章*_分镜脚本.json")); len(ms) > 0 {
				items = append(items, ok("storyboard", fmt.Sprintf("小说分镜脚本 %d 集(素材/分镜脚本/),可一键导入脚本直出", len(ms))))
			}
		}
	}
	// 2.5 角色面容特征(2026-08-30 ver17 防男角色面容趋同):素材卡主要角色
	// image_prompt 需 ≥4 类具体五官特征+≥1 独有印记、无泛化词(handsome face 等),
	// 与技能侧 SKILL.md 面容独特性硬规范同口径。不达标→warn 提示(定妆图会撞脸)。
	items = append(items, manjuFaceFeatureItems(ctx)...)
	// 3. LLM 配置
	if ctx.llm == nil || ctx.llm.apiKey == "" {
		items = append(items, manjuHealthItem{Key: "llm", Label: "LLM 配置", Status: "bad", Detail: "未填 DeepSeek Key", FixHint: "设置 → 智能体调度 → 填 Key 并应用/保存"})
	} else {
		items = append(items, ok("llm", "已配置 "+ctx.llm.model))
	}
	// 4. ComfyUI 连通
	if ver, cerr := ctx.comfy.Online(); cerr != nil {
		items = append(items, manjuHealthItem{Key: "comfy", Label: "ComfyUI", Status: "warn", Detail: "连不上 " + ctx.comfy.Base + "(" + truncate(cerr.Error(), 50) + ")", FixHint: "启动 ComfyUI 或核对 comfy_url"})
	} else {
		items = append(items, ok("comfy", "在线 " + ver))
	}
	// 5. 关键模型存在性
	if missing := ctx.missingModels(); len(missing) > 0 {
		items = append(items, manjuHealthItem{Key: "models", Label: "关键模型", Status: "warn", Detail: "缺失: " + strings.Join(missing, ", "), FixHint: "放入 ComfyUI-Shared/models 对应子目录"})
	} else {
		items = append(items, ok("models", "SDXL/动漫/Z-Image 均就位"))
	}
	// 6. 渲染参数
	steps, _ := manjuToInt(R["steps"])
	turbo, _ := manjuToInt(R["turbo_steps"])
	if s := str(R["turbo_lora"]); s != "" {
		spec := turboLoRASpecOf(s)
		if turbo <= 0 {
			turbo = spec.Steps // 未配置 turbo_steps:展示 LoRA 参数表推荐步数
		}
		if steps > 0 && turbo > 0 && steps > turbo {
			items = append(items, manjuHealthItem{Key: "render_steps", Label: "采样步数", Status: "warn", Detail: fmt.Sprintf("已配 Turbo LoRA(%s,强度%.2f/%s) 但步数=%d,建议 %d 步(约%.1f倍提速)", s, spec.Strength, spec.Sampler, steps, turbo, float64(steps)/float64(turbo)), Fixable: true, FixHint: fmt.Sprintf("一键改为 %d 步", turbo)})
		} else {
			items = append(items, ok("render_steps", fmt.Sprintf("steps=%d turbo=%d(LoRA: %.2f/%s)", steps, turbo, spec.Strength, spec.Sampler)))
		}
	} else {
		items = append(items, ok("render_steps", fmt.Sprintf("steps=%d turbo=%d", steps, turbo)))
	}
	// 6.1 定妆引擎/字幕/配音(2026-08-23 新增)
	eng := strings.TrimSpace(str(R["char_engine"]))
	if eng == "" {
		eng = "zimage"
	}
	engDesc := map[string]string{"zimage": "Z-Image 写实(人物微动漫)", "krea2": "Krea-2 强指令", "sdxl": "SDXL 动漫"}[eng]
	items = append(items, ok("char_engine", "定妆引擎: "+engDesc))
	if eng == "krea2" {
		if !fileExists(filepath.Join(paths.ComfySharedDir, "models", "diffusion_models", str(R["krea2_unet"]))) {
			items = append(items, manjuHealthItem{Key: "krea2", Label: "Krea-2 权重", Status: "warn", Detail: "char_engine=krea2 但 Krea-2 主模型未下载,定妆会失败", FixHint: "下载 Comfy-Org/Krea-2 权重到共享 models/diffusion_models 或改用 zimage"})
		} else {
			items = append(items, ok("krea2", "Krea-2 权重就绪"))
		}
	}
	if sub, _ := R["subtitle"].(bool); !sub {
		items = append(items, ok("subtitle", "字幕: 不烧录(H3 原生对白)"))
	} else {
		items = append(items, ok("subtitle", "字幕: 烧录对白字幕"))
	}
	if vo, _ := R["voiceover"].(bool); vo {
		items = append(items, ok("voiceover", "旁白/画外音: edge-tts 后期配音兜底"))
	}
	seed, _ := manjuToInt(R["seed"])
	if seed == 0 {
		items = append(items, manjuHealthItem{Key: "render_seed", Label: "随机种子", Status: "warn", Detail: "seed 为空/0,跨镜头一致性无锚点", Fixable: true, FixHint: "一键设为 1688"})
	} else {
		items = append(items, ok("render_seed", fmt.Sprintf("seed=%d(全剧固定)", seed)))
	}
	fps, _ := manjuToInt(R["fps"])
	if fps < 8 || fps > 60 {
		items = append(items, manjuHealthItem{Key: "render_fps", Label: "帧率", Status: "bad", Detail: fmt.Sprintf("fps=%d 超出 8-60", fps), Fixable: true, FixHint: "一键改为 24"})
	} else {
		items = append(items, ok("render_fps", fmt.Sprintf("%d fps", fps)))
	}
	mi, _ := manjuToInt(R["min_shot_seconds"])
	ma, _ := manjuToInt(R["max_shot_seconds"])
	if mi > 0 && ma > 0 && mi > ma {
		items = append(items, manjuHealthItem{Key: "render_dur", Label: "镜头时长", Status: "bad", Detail: fmt.Sprintf("最短%d > 最长%d,矛盾", mi, ma), Fixable: true, FixHint: "一键改为 4-12s"})
	} else {
		items = append(items, ok("render_dur", fmt.Sprintf("%d-%d s", mi, ma)))
	}
	// 7. 审片官
	if loadAgentCfg(ctx).VisionModel == "" {
		items = append(items, manjuHealthItem{Key: "agent", Label: "审片官", Status: "warn", Detail: "未配置视觉模型,AI 一条龙只做机械质检不判分", FixHint: "设置 → 智能体调度 → 选视觉模型"})
	} else {
		items = append(items, ok("agent", "视觉模型就绪"))
	}
	// 8. 审片状态/学习记忆(agent_state.json):损坏或缺失会拖累审片报告面板
	// 2026-09-02:损坏项改可一键修复(原 FixHint 要求手动删文件,与「一键修复」定位不符)
	stPath := manjuAgentStatePath(ctx.project)
	running := manjuStateRunningFor(ctx.configPath)
	if b, err := os.ReadFile(stPath); err == nil {
		var st manjuAgentState
		if json.Unmarshal(b, &st) != nil {
			items = append(items, manjuHealthItem{Key: "agent_state", Label: "审片状态文件", Status: "bad", Detail: "agent_state.json 损坏",
				Fixable: !running, FixHint: "一键删除损坏文件(审片记忆重置,重跑 AI 一条龙自动重建)"})
		} else {
			items = append(items, ok("agent_state", fmt.Sprintf("记忆 %d 次运行 / %d 镜判分 / %d 次返工", st.Memory.RunCount, st.Memory.JudgedShots, st.Memory.ReworkCount)))
		}
	} else {
		items = append(items, manjuHealthItem{Key: "agent_state", Label: "审片状态文件", Status: "warn", Detail: "尚无审片记录", FixHint: "跑一次「AI 一条龙」后自动生成"})
	}
	// 8.5 运行状态残留(2026-09-02 用户要求:一键修复带自动清理残留配置与日志):
	// 非运行态下 run_state.json 是崩溃/中断/完成残留——不清会让页面继续弹「检测到已有
	// 任务」横幅、或磁盘 running 挂假渲染中(与高级清理 2b 同口径)。
	items = append(items, manjuRunStateHealthItem(ctx, running)...)
	// 8.6 日志残留:run.log(自动截断上限 512KB)+ 平台 crash.log(panic 追加无上限)。
	// 超阈值 → 可一键清空(顺带删本项目诊断快照,下次任务结束自动重建)。
	items = append(items, manjuLogsHealthItem(ctx, running)...)
	// 9. 渲染升级参数联动检查(草稿预审/转场/BGM/SageAttention/长镜)
	if dj, _ := R["draft_judge"].(bool); dj {
		if loadAgentCfg(ctx).VisionModel == "" {
			items = append(items, manjuHealthItem{Key: "draft_judge", Label: "草稿预审", Status: "warn", Detail: "已开启但未配置视觉模型——AI 一条龙不判分时草稿预审不会生效", FixHint: "设置 → 智能体调度 → 选视觉模型,或在渲染参数里关闭草稿预审"})
		} else {
			items = append(items, ok("draft_judge", "已开启(审片返工轮半分辨率,通过后全分辨率定稿)"))
		}
	}
	if bgm := strings.TrimSpace(str(R["bgm"])); bgm != "" {
		if !fileExists(bgm) {
			items = append(items, manjuHealthItem{Key: "bgm", Label: "BGM", Status: "bad", Detail: "文件不存在: " + bgm, FixHint: "修正 render.bgm 路径(合成对坏 BGM 会静默忽略)"})
		} else {
			items = append(items, ok("bgm", "就绪 "+filepath.Base(bgm)+"(对白自动闪避)"))
		}
	}
	if sageEnabled(R) {
		if _, cerr := ctx.comfy.Online(); cerr == nil && !ctx.comfy.HasNode("PatchSageAttentionKJ") {
			items = append(items, manjuHealthItem{Key: "sage", Label: "SageAttention", Status: "bad", Detail: "已开启但 ComfyUI 缺 PatchSageAttentionKJ 节点,渲染提交会失败", FixHint: "安装 ComfyUI-KJNodes,或在渲染参数里关闭 SageAttn"})
		} else if cerr == nil {
			items = append(items, ok("sage", "已开启(节点可用)"))
		}
	}
	if n, _ := manjuToInt(R["shots_per_take"]); n >= 2 {
		if ctx.scriptMode {
			// 脚本直出恒单镜(渲染层禁用 takes):配置了也如实说明,避免用户以为分组生效
			// 2026-08-30 用户要求可一键修复:改回单镜配置,与实际渲染行为对齐(消除配置与行为的偏差告警)
			items = append(items, manjuHealthItem{Key: "long_take", Label: "多切点长镜", Status: "warn",
				Detail:    fmt.Sprintf("配置 %d 镜/组,但脚本直出模式不分组(脚本六段式逐字权威,分组会丢镜),已按单镜渲染", n),
				Fixable:   true,
				FixHint:   "一键改回 1 镜/组,配置与脚本直出的实际渲染行为对齐"})
		} else {
			items = append(items, ok("long_take", fmt.Sprintf("多切点长镜 %d 镜/组(实验特性;相邻同场景镜头一次生成多机位切点)", n)))
		}
	}
	items = append(items, manjuPlanAuditItem(ctx))
	// 8. 更新升级排查(2026-08-29):ComfyUI 版本 / 插件更新 / PDD 加速模型
	// 数据复用版本管理快照(本地 origin/HEAD 秒回;GitHub 最新版本带 10min 缓存)
	snap := comfyVersionSnapshot()
	if cv, cvOK := snap["comfy"].(map[string]any); cvOK && cv["online"] == true {
		cur := str(cv["version"])
		latest := comfyLatestVerCached()
		if latest != "" && strings.TrimPrefix(cur, "v") != strings.TrimPrefix(latest, "v") {
			items = append(items, manjuHealthItem{Key: "comfy_ver", Label: "ComfyUI 版本", Status: "warn",
				Detail: fmt.Sprintf("当前 %s,官方最新 %s,建议升级", cur, latest),
				FixHint: "更新 ComfyUI 后需重启服务生效(版本管理弹窗可对比)"})
		} else if latest != "" {
			items = append(items, ok("comfy_ver", "ComfyUI "+cur+" 已是最新"))
		} else {
			items = append(items, ok("comfy_ver", "ComfyUI "+cur+"(官方最新版本获取失败,联网后重新体检)"))
		}
	}
	var upd []string
	if ps, psOK := snap["plugins"].([]any); psOK {
		for _, p := range ps {
			m, _ := p.(map[string]any)
			if m == nil || m["git"] != true {
				continue
			}
			if n, _ := manjuToInt(m["behind"]); n > 0 {
				upd = append(upd, fmt.Sprintf("%s↑%d", str(m["name"]), n))
			}
		}
	}
	if len(upd) > 0 {
		items = append(items, manjuHealthItem{Key: "plugin_upd", Label: "插件更新", Status: "warn",
			Detail: "有更新: " + strings.Join(upd, ", "),
			FixHint: "ComfyUI 页 → 版本管理 → 检查更新 → git pull(更新后需重启 ComfyUI)"})
	} else {
		items = append(items, ok("plugin_upd", "ComfyUI 插件均最新"))
	}
	if pdd, pdOK := snap["pdd"].(map[string]any); pdOK && pdd["available"] == true {
		items = append(items, ok("pdd", "PDD 8 步加速 LoRA 已就位("+str(pdd["file"])+")"))
	}
	return items
}

// manjuPlanAuditItem 方案软告警体检(2026-08-26,机械质检汇总项):遍历项目已有各集方案
// (analysis/*_direct_plan.json),统计超时长上限镜/时长全同集/超 3 角色镜/超 20 字句/重复
// 提示词对——渲染前一眼看全,不必等 validatePlan 逐条翻运行日志。只读展示,不可自动修复。
func manjuPlanAuditItem(ctx *manjuCtx) manjuHealthItem {
	it := manjuHealthItem{Key: "plan_audit", Label: "方案体检", Status: "ok", Detail: "暂无方案(先跑一次方案阶段)"}
	plans, _ := filepath.Glob(filepath.Join(ctx.analysisDir, "*_direct_plan.json"))
	if len(plans) == 0 {
		return it
	}
	var warns []string
	overDur, overChar, overLine, dupPrompt, flatEps := 0, 0, 0, 0, 0
	for _, p := range plans {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var plan map[string]any
		if json.Unmarshal(b, &plan) != nil {
			continue
		}
		shots, serr := planShots(plan)
		if serr != nil || len(shots) == 0 {
			continue
		}
		durSet := map[int]bool{}
		seenP := map[string]bool{}
		for _, s := range shots {
			durSet[s.Duration] = true
			if s.Duration > ctx.maxSec {
				overDur++
			}
			if len(s.Characters) > 3 {
				overChar++
			}
			for _, line := range strings.Split(s.Dialogue, "\n") {
				if i := strings.IndexAny(line, ":："); i > 0 {
					if rc := len([]rune(stripSpeechPunct(strings.TrimSpace(line[i+1:])))); rc > 20 {
						overLine++
					}
				}
			}
			if s.H3Prompt != "" && !s.TakeTail {
				if seenP[s.H3Prompt] {
					dupPrompt++
				}
				seenP[s.H3Prompt] = true
			}
		}
		if len(shots) >= 6 && len(durSet) == 1 {
			flatEps++
		}
	}
	if overDur > 0 {
		warns = append(warns, fmt.Sprintf("%d 镜超时长上限 %ds", overDur, ctx.maxSec))
	}
	if overChar > 0 {
		warns = append(warns, fmt.Sprintf("%d 镜登场角色超 3(参考图截断)", overChar))
	}
	if overLine > 0 {
		warns = append(warns, fmt.Sprintf("%d 句对白超 20 字", overLine))
	}
	if dupPrompt > 0 {
		warns = append(warns, fmt.Sprintf("%d 镜提示词重复", dupPrompt))
	}
	if flatEps > 0 {
		warns = append(warns, fmt.Sprintf("%d 集时长全同(节奏单一)", flatEps))
	}
	if len(warns) == 0 {
		it.Detail = fmt.Sprintf("%d 集方案:时长/角色数/句长/重复均合规", len(plans))
		return it
	}
	it.Status = "warn"
	it.Detail = fmt.Sprintf("%d 集方案:%s(重新生成方案或在方案 JSON 里修正;脚本直出模式请改分镜脚本后重导入)", len(plans), strings.Join(warns, "、"))
	return it
}

// missingModels 检查配置里引用的关键模型是否在磁盘上(检查点/UNET/CLIP/VAE/LoRA)。
// 目录兼容:UNET 类先查 diffusion_models 再查 unet;CLIP 类先查 text_encoders 再查 clip
// (ComfyUI 新版默认索引 diffusion_models/text_encoders,旧版用 unet/clip)。
func (ctx *manjuCtx) missingModels() []string {
	cands := []struct{ name, sub, alt string }{
		// 2026-08-24 用户规则:SDXL checkpoint 全面禁用(观感差),定妆照只用 Z-Image 或 Krea-2——
		// 不再检查 characterCkpt/SDXL 模型;Z-Image 三件套为常驻检查(Krea-2 引擎时追加其三件套)。
		{str(ctx.R["z_image_unet"]), "diffusion_models", "unet"},
		{str(ctx.R["z_image_clip"]), "text_encoders", "clip"},
		{str(ctx.R["z_image_vae"]), "vae", ""},
		{str(ctx.R["turbo_lora"]), "loras", ""},
		{str(ctx.R["unet_fl2va"]), "diffusion_models", "unet"},
		{str(ctx.R["unet_ref2va"]), "diffusion_models", "unet"},
		{str(ctx.R["vae_video"]), "vae", ""},
		{str(ctx.R["vae_audio"]), "vae", ""},
	}
	// Krea-2 定妆引擎(2026-08-23):char_engine=krea2 时检查三件套是否存在
	if eng := strings.TrimSpace(str(ctx.R["char_engine"])); eng == "krea2" {
		cands = append(cands,
			struct{ name, sub, alt string }{str(ctx.R["krea2_unet"]), "diffusion_models", "unet"},
			struct{ name, sub, alt string }{str(ctx.R["krea2_clip"]), "text_encoders", "clip"},
			struct{ name, sub, alt string }{str(ctx.R["krea2_vae"]), "vae", ""},
		)
	}
	seen := map[string]bool{}
	var missing []string
	for _, c := range cands {
		if c.name == "" || seen[c.name] {
			continue
		}
		seen[c.name] = true
		if !fileExists(filepath.Join(ctx.sharedModels, c.sub, c.name)) && (c.alt == "" || !fileExists(filepath.Join(ctx.sharedModels, c.alt, c.name))) {
			missing = append(missing, c.name)
		}
	}
	return missing
}

// manjuRunStateHealthItem 运行状态残留体检项(2026-09-02):运行中 = 活状态不算残留;
// 非运行态下 run_state.json 存在即为残留——Running 崩溃残留(bad,页面挂假渲染中)、
// Stopped/Done 中断与完成残留(warn,「检测到已有任务」横幅源头)、JSON 损坏(bad)。
// 全部可一键清理;清 Running 残留 = 放弃自动续跑(渲染检查点保留,重跑幂等跳过已完成镜头)。
func manjuRunStateHealthItem(ctx *manjuCtx, running bool) []manjuHealthItem {
	if running {
		return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "ok", Detail: "任务运行中(活状态,不算残留)"}}
	}
	sp := manjuRunStatePath(ctx.project)
	if !fileExists(sp) {
		return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "ok", Detail: "空闲,无状态残留"}}
	}
	if ds := loadManjuDiskState(ctx.project); ds != nil {
		switch {
		case ds.Running:
			return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "bad",
				Detail: "run_state.json 崩溃残留(显示渲染中,实际无任务在跑)", Fixable: true,
				FixHint: "一键清理残留状态;将放弃自动续跑(渲染检查点保留,重跑自动跳过已完成镜头)"}}
		case ds.Stopped:
			return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "warn",
				Detail: fmt.Sprintf("上次任务被手动停止(%s),状态文件残留会弹「检测到已有任务」横幅", diskStateAge(ds.UpdatedAt)), Fixable: true,
				FixHint: "一键清理残留状态文件(不影响任何渲染产物)"}}
		default:
			return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "warn",
				Detail: fmt.Sprintf("上次任务已结束(%s),状态文件残留会弹「检测到已有任务」横幅", diskStateAge(ds.UpdatedAt)), Fixable: true,
				FixHint: "一键清理残留状态文件(不影响任何渲染产物)"}}
		}
	}
	return []manjuHealthItem{{Key: "run_state", Label: "运行状态文件", Status: "bad",
		Detail: "run_state.json 损坏(非合法 JSON)", Fixable: true,
		FixHint: "一键删除损坏的状态文件(下次任务自动重建)"}}
}

// manjuLogsHealthItem 日志残留体检项(2026-09-02):run.log 超 256KB(自动截断上限的一半)
// 或 crash.log 超 64KB(panic 追加无上限)→ 可一键清空,顺带删本项目诊断快照。
func manjuLogsHealthItem(ctx *manjuCtx, running bool) []manjuHealthItem {
	fsize := func(p string) int64 {
		if fi, err := os.Stat(p); err == nil {
			return fi.Size()
		}
		return 0
	}
	kb := func(n int64) string { return fmt.Sprintf("%d KB", (n+1023)>>10) }
	runLog, crashLog := fsize(manjuRunLogPath(ctx.project)), fsize(filepath.Join(paths.ManjuRootDir, "logs", "crash.log"))
	if running {
		return []manjuHealthItem{{Key: "logs", Label: "运行日志", Status: "ok", Detail: fmt.Sprintf("运行日志 %s + 崩溃日志 %s(任务运行中,结束后可清理)", kb(runLog), kb(crashLog))}}
	}
	if runLog > 256<<10 || crashLog > 64<<10 {
		return []manjuHealthItem{{Key: "logs", Label: "运行日志", Status: "warn",
			Detail:    fmt.Sprintf("运行日志 %s + 崩溃日志 %s 堆积超阈值", kb(runLog), kb(crashLog)),
			Fixable:   true,
			FixHint:   "一键清空运行日志/崩溃日志,并删除本项目诊断快照(下次任务自动重建,不影响产物)"}}
	}
	return []manjuHealthItem{{Key: "logs", Label: "运行日志", Status: "ok", Detail: fmt.Sprintf("运行日志 %s + 崩溃日志 %s", kb(runLog), kb(crashLog))}}
}

// diskStateAge 状态文件 UpdatedAt → 人话时长(未知/刚刚/N 分钟前/N 小时前/N 天前)
func diskStateAge(ts int64) string {
	if ts <= 0 {
		return "时间未知"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}

// manjuHealth 体检接口:返回全项清单 + ok/warn/bad 汇总
func manjuHealth(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	ctx, err := newManjuCtx(cp, "", "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := manjuHealthCheck(ctx)
	n := map[string]int{}
	for _, it := range items {
		n[it.Status]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": n})
}

// manjuApplyHealthFix 对 config 应用单项修复(返回是否实际改动);体检弹窗/聊天「修复」共用
func manjuApplyHealthFix(configPath, key string) (bool, error) {
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		return false, err
	}
	cfg := ctx.cfg
	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
		cfg["render"] = R
	}
	switch key {
	case "render_steps":
		want := turboLoRASpecOf(str(R["turbo_lora"])).Steps
		if t, ok := manjuToInt(R["turbo_steps"]); ok && t > 0 {
			want = t // 用户显式配置的 turbo_steps 优先
		}
		if want > 0 {
			R["steps"] = want
			if _, ok := R["turbo_steps"]; !ok {
				R["turbo_steps"] = want // 顺手落盘推荐值,后续展示/判断一致
			}
		} else {
			return false, nil
		}
	case "render_seed":
		R["seed"] = 1688
	case "render_fps":
		R["fps"] = 24
	case "render_dur":
		R["min_shot_seconds"] = 4
		R["max_shot_seconds"] = 12
	case "long_take":
		// 2026-08-30:脚本直出模式配置了 N 镜/组但渲染层恒单镜——改回 1 对齐实际行为
		R["shots_per_take"] = 1
	case "agent_state":
		// 2026-09-02:删除损坏的审片状态文件(只删「确认损坏」的,完好记忆不动);
		// 运行中审片链路会写该文件,拒绝清理
		if manjuStateRunningFor(configPath) {
			return false, fmt.Errorf("项目正在渲染中,请停止后再清理")
		}
		sp := manjuAgentStatePath(ctx.project)
		if b, err := os.ReadFile(sp); err == nil {
			var st manjuAgentState
			if json.Unmarshal(b, &st) == nil {
				return false, nil // 文件完好:体检项不应再报损坏,幂等无改动
			}
			if err := os.Remove(sp); err != nil {
				return false, fmt.Errorf("删除失败: %v", err)
			}
			return true, nil
		}
		return false, nil
	case "run_state":
		// 2026-09-02:清理非运行态下的 run_state.json 残留(崩溃/中断/完成态,与高级清理 2b 同口径);
		// 运行中该文件是活状态,拒绝(AutoRecoverRendering 崩溃续跑也依赖它)
		if manjuStateRunningFor(configPath) {
			return false, fmt.Errorf("项目正在渲染中,请停止后再清理")
		}
		sp := manjuRunStatePath(ctx.project)
		if !fileExists(sp) {
			return false, nil // 无残留:幂等无改动
		}
		if err := os.Remove(sp); err != nil {
			return false, fmt.Errorf("删除失败: %v", err)
		}
		return true, nil
	case "logs":
		// 2026-09-02:清空运行日志/崩溃日志 + 删本项目诊断快照(下次任务结束自动重建);
		// 运行中 run.log 正在被写,拒绝。媒体工具目录(logs/media)与平台其它日志不动。
		if manjuStateRunningFor(configPath) {
			return false, fmt.Errorf("项目正在渲染中,请停止后再清理")
		}
		changed := false
		for _, p := range []string{
			manjuRunLogPath(ctx.project),
			filepath.Join(paths.ManjuRootDir, "logs", "crash.log"),
			manjuDiagnosePath(ctx.project), // 删除而非截断:快照整体重生成
		} {
			if !fileExists(p) {
				continue
			}
			if strings.HasSuffix(p, ".json") {
				if os.Remove(p) == nil {
					changed = true
				}
				continue
			}
			if fi, serr := os.Stat(p); serr != nil || fi.Size() == 0 {
				continue // 空文件无需截断(幂等:重复修复不误报改动)
			}
			if os.Truncate(p, 0) == nil {
				changed = true
			}
		}
		manjuState.mu.Lock()
		manjuState.log = "" // 内存日志同步清空(高级清理同款),防空闲轮询从内存回弹
		manjuState.mu.Unlock()
		return changed, nil
	default:
		return false, fmt.Errorf("该检查项不可自动修复")
	}
	return true, writeManjuConfig(configPath, cfg)
}

// manjuFixAll 体检 + 自动修复全部可修复项(后端直接执行;聊天「修复」与前端 fixAllHealth 共用)
func manjuFixAll(configPath string) (fixed []string, errs []string) {
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, it := range manjuHealthCheck(ctx) {
		if !it.Fixable || it.Status == "ok" {
			continue
		}
		ok, err := manjuApplyHealthFix(configPath, it.Key)
		if err != nil {
			errs = append(errs, it.Label+": "+err.Error())
			continue
		}
		if ok {
			fixed = append(fixed, it.Label)
		}
	}
	return fixed, errs
}

// manjuHealthFix 一键修复可修复的体检项(写回 config.json 后重跑体检)
func manjuHealthFix(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	configPath := str(body["config"])
	key := str(body["key"])
	if configPath == "" || key == "" {
		http.Error(w, `{"error":"missing config/key"}`, http.StatusBadRequest)
		return
	}
	// 审计 F4:一键修复会写回 config.json,config 必须过 guard
	if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	} else {
		configPath = cp
	}
	applied, err := manjuApplyHealthFix(configPath, key)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = applied
	ctx2, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": manjuHealthCheck(ctx2)})
}

// containsAny text 是否包含任一关键词
func containsAny(text string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// manjuAgentChat 智能对话:三层路由。
// ①确定性指令(体检/风格/审片/总结/修复)直接触发动作;
// ②未命中 → LLM 自由对话:注入项目实时上下文(风格/运行状态/审片摘要/记忆/最近错误),
//   大模型以漫剧智能体人设回答任何问题,并可自主判断调用动作(输出 action 字段);
// ③LLM 不可用(未配 Key/调用失败)→ 回退固定指令提示。
func manjuAgentChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	configPath := str(body["config"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	text := strings.ToLower(strings.TrimSpace(str(body["text"])))
	rawText := strings.TrimSpace(str(body["text"]))
	reply, action := "", ""
	project := filepath.Base(filepath.Dir(configPath))

	// ① 确定性指令路由(短语明确命中,不走 LLM,秒回)
	routed := true
	switch {
	case text == "":
		reply = "我是漫剧智能体 🤖,可以对我说:体检 / 推荐风格 / 审片报告 / 总结 / 修复,也可以直接用自然语言问我任何问题(如「项目有什么问题」「画面太暗怎么调」)"
	case containsAny(text, "体检", "检查", "健康", "诊断", "分析项目", "看看项目"):
		action = "health"
	case containsAny(text, "风格", "画风", "推荐风格"):
		action = "style"
		// 后端直接执行深度分析(用项目 config 内的章节/集号),任何入口说「推荐风格」都真分析
		if res, serr := manjuStyleAnalyzeRun(configPath, "", "", ""); serr == nil {
			reply = fmt.Sprintf("✅ 风格已更新:%s → %s(%s)",
				styleLabelCN(str(res["old"])), styleLabelCN(str(res["style"])), truncate(str(res["reason"]), 60))
		} else {
			reply = "❌ 风格分析失败:" + truncate(serr.Error(), 100)
		}
	case containsAny(text, "审片", "判分", "分数", "报告", "得分"):
		sum := agentStatusSummary(configPath)
		shots := anyArr(sum["shots"])
		pass, failed, esc := 0, 0, 0
		for _, s := range shots {
			m := s.(map[string]any)
			if st := str(m["status"]); st == "pass" || st == "fixed" || st == "accepted" {
				pass++
			} else if st == "failed" {
				failed++
			}
		}
		esc = len(anyArr(sum["escalations"]))
		if len(shots) == 0 {
			reply = "还没有审片记录。跑一次「🤖 AI 一条龙」后,我会逐镜判分并给出报告。"
		} else {
			reply = fmt.Sprintf("📊 审片报告:共审 %d 镜 — ✅ %d 通过 / ⚠️ %d 待处理%s。点右上「审片报告」面板可看每镜维度详情与升级卡。",
				len(shots), pass, failed, map[bool]string{true: fmt.Sprintf(" / 🚨 %d 待拍板", esc)}[esc > 0])
		}
	case containsAny(text, "总结", "记忆", "学习", "统计", "回顾", "趋势", "档案", "经验"):
		reply = manjuMemorySummary(project)
	case containsAny(text, "修复", "处理问题", "修一下", "修了", "都修", "修问题", "优化", "完善", "升级", "调整配置", "调整一下"):
		// 后端直接执行修复(不依赖前端实例方法),任何入口说「修复」都立竿见影
		action = "fixall"
		fixed, errs := manjuFixAll(configPath)
		switch {
		case len(fixed) > 0:
			reply = "🔧 已自动修复 " + strconv.Itoa(len(fixed)) + " 项:" + strings.Join(fixed, "、")
			if len(errs) > 0 {
				reply += ";另有 " + strconv.Itoa(len(errs)) + " 项失败(" + truncate(strings.Join(errs, ";"), 80) + ")"
			}
		case len(errs) > 0:
			reply = "❌ 修复失败:" + truncate(strings.Join(errs, ";"), 120)
		default:
			reply = "ℹ️ 体检过一遍,当前没有可自动修复的项(步数/种子/帧率/时长均正常);其余异常项需要手动处理,对我说「体检」看详情"
		}
	default:
		routed = false
	}
	if routed {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": reply, "action": action})
		return
	}

	// ② LLM 自由对话:带项目实时上下文
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true,
			"reply": "项目配置读取失败:" + err.Error(), "action": ""})
		return
	}
	if ctx.llm == nil || ctx.llm.apiKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true,
			"reply": "我目前只支持固定指令:体检 / 推荐风格 / 审片报告 / 总结 / 修复。\n配置 DeepSeek Key(设置 → 智能体调度)后,我就能用自然语言回答任何问题、帮你分析项目。", "action": ""})
		return
	}
	sys := manjuAgentChatSystem()
	user := manjuAgentChatContext(ctx, configPath, project)
	user += "\n\n【用户】" + rawText
	out, lerr := ctx.llm.chatJSON(sys, user, 0.5)
	if lerr == nil {
		reply = str(out["reply"])
		act := str(out["action"])
		switch act { // 白名单:LLM 只能触发这几个动作
		case "health", "style", "fixall":
			action = act
		}
		if reply != "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": reply, "action": action})
			return
		}
	}
	// ③ 兜底
	msg := "这个问题我一时答不上来 😅 固定指令随时可用:体检 / 推荐风格 / 审片报告 / 总结 / 修复"
	if lerr != nil {
		msg += "\n(自由对话调用模型失败:" + truncate(lerr.Error(), 80) + ")"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": msg, "action": ""})
}

// manjuAgentChatSystem 自由对话系统提示词:漫剧智能体人设 + 可调用动作说明
func manjuAgentChatSystem() string {
	return `你是 NiliX 漫剧工作台的智能体助手,帮用户把小说一键变成漫剧成片(管线:方案分镜 → 定妆照/场景图 → 预编码 → H3 渲染 → 质检+ASR 台词核对+审片判分 → 自动返工 → 合成)。
根据给到的项目上下文,用中文简洁、口语化回答用户问题;不知道的事实直说,不编造。
你可以在需要时调用动作,在 action 字段输出:
- health:项目体检(诊断配置/模型/参数并给修复建议)——用户问「有没有问题/哪里要改」时用
- style:深度分析小说并更新渲染风格——用户问「什么风格合适/帮我选风格」时用
- fixall:自动修复可修复的配置问题——用户明确要你修时用
不需要动作时 action 留空字符串。回答控制在 120 字内,可给具体建议(如参数怎么调、哪一步出问题)。
输出严格 JSON:{"reply":"给用户的回答","action":""}`
}

// manjuAgentChatContext 组装项目实时上下文(风格/运行状态/审片/记忆/最近错误)
func manjuAgentChatContext(ctx *manjuCtx, configPath, project string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【项目上下文】\n项目: %s\n当前风格: %s\n", ctx.project, styleLabelCN(ctx.style))
	if st := manjuStatusFor(configPath); st != nil {
		if run, _ := st["running"].(bool); run {
			fmt.Fprintf(&b, "运行状态: 运行中(%v)\n", st["stage"])
		} else if rc, ok := st["rc"]; ok && rc != nil {
			fmt.Fprintf(&b, "运行状态: 空闲(上次退出码 %v)\n", rc)
		} else {
			b.WriteString("运行状态: 空闲\n")
		}
	}
	sum := agentStatusSummary(configPath)
	if n := len(anyArr(sum["shots"])); n > 0 {
		pass, failed := 0, 0
		for _, s := range anyArr(sum["shots"]) {
			if m, ok := s.(map[string]any); ok {
				switch str(m["status"]) {
				case "pass", "fixed", "accepted":
					pass++
				case "failed":
					failed++
				}
			}
		}
		fmt.Fprintf(&b, "审片: %d 镜(%d 通过 / %d 未过), 待拍板 %d\n",
			n, pass, failed, len(anyArr(sum["escalations"])))
	} else {
		b.WriteString("审片: 尚无记录\n")
	}
	if mem := manjuMemorySummary(project); !strings.Contains(mem, "没有学习记录") {
		b.WriteString("记忆: " + strings.ReplaceAll(mem, "\n", "; ") + "\n")
	}
	if le, _ := sum["lastError"].(map[string]any); le != nil {
		fmt.Fprintf(&b, "最近错误: 阶段 %s · %s\n", str(le["stage"]), str(le["diagnosis"]))
	}
	if str(ctx.novel) == "" {
		b.WriteString("注意: 项目尚未配置小说正文\n")
	}
	return b.String()
}

// ---- 记忆学习汇总 ----

// manjuMemorySummary 把项目记忆整理成一段可读总结(审片/学习统计)
func manjuMemorySummary(project string) string {
	st := loadAgentState(project)
	m := st.Memory
	if m.RunCount == 0 && len(m.ScoreTrend) == 0 && len(m.StyleChoices) == 0 && len(m.IssueStats) == 0 {
		return "我还没有学习记录。跑一次「AI 一条龙」或「深度分析风格」后,我会积累审片问题、分数趋势与风格选择经验。"
	}
	var b strings.Builder
	if m.RunCount > 0 {
		fmt.Fprintf(&b, "🧠 已为你运行 %d 次 · 审片 %d 镜 · 自动返工 %d 次\n", m.RunCount, m.JudgedShots, m.ReworkCount)
	}
	if len(m.ScoreTrend) > 0 {
		first, last := m.ScoreTrend[0], m.ScoreTrend[len(m.ScoreTrend)-1]
		delta := last.Score - first.Score
		trend := "持平"
		if delta > 1.5 {
			trend = "↑ 提升 " + fmt.Sprintf("%.0f", delta) + " 分"
		} else if delta < -1.5 {
			trend = "↓ 回落 " + fmt.Sprintf("%.0f", -delta) + " 分"
		}
		fmt.Fprintf(&b, "📈 审片均分 %.0f(%d 镜)%s\n", last.Score, last.Count, trend)
	}
	if len(m.IssueStats) > 0 {
		type kv struct{ k string; v int }
		var top []kv
		for k, v := range m.IssueStats {
			top = append(top, kv{k, v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
		b.WriteString("🔁 高频问题: ")
		for i, t := range top {
			if i >= 3 {
				break
			}
			if i > 0 {
				b.WriteString(" / ")
			}
			fmt.Fprintf(&b, "%s×%d", t.k, t.v)
		}
		b.WriteString("\n")
	}
	if len(m.StyleChoices) > 0 {
		last := m.StyleChoices[len(m.StyleChoices)-1]
		fmt.Fprintf(&b, "🎨 最近风格: %s → %s(%s)\n", styleLabelCN(last.Old), styleLabelCN(last.New), last.Reason)
	}
	return strings.TrimRight(b.String(), "\n")
}

// manjuStyleCNName 预设 key → 中文名(与前端 STYLE_CN 一致,仅展示用)
var manjuStyleCNName = map[string]string{
	"2.5d": "2.5D 动漫", "real": "写实", "3d": "3D CG", "anime": "二次元",
	"handdrawn": "手绘", "papercraft": "纸艺", "clay": "粘土", "ink": "水墨",
}

// styleLabelCN 风格值转中文展示(组合元素逐个转中文,自定义词原样)
func styleLabelCN(style string) string {
	if style == "" {
		return "未设置"
	}
	parts := strings.Split(style, "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if cn, ok := manjuStyleCNName[p]; ok {
			parts[i] = cn
		} else {
			parts[i] = p
		}
	}
	return strings.Join(parts, " + ")
}

// manjuDiagnoseError 阶段失败诊断:错误模式 → 诊断结论 + 建议
func manjuDiagnoseError(stage string, err error) (string, string) {
	e := strings.ToLower(err.Error())
	switch {
	case strings.Contains(e, "safetensors") || strings.Contains(e, "checkpoint") || strings.Contains(e, "not found") && strings.Contains(e, "model"):
		return "模型缺失/不匹配", "检查 ComfyUI 模型目录与配置里的模型名是否完全一致(含大小写),缺失模型补齐后重试"
	case strings.Contains(e, "out of memory") || strings.Contains(e, "cuda out") || strings.Contains(e, "oom"):
		return "显存不足(OOM)", "ComfyUI 面板 /free 释放显存,或降低画幅、分阶段渲染;小显存建议 768×1344 以下"
	case strings.Contains(e, "connection refused") || strings.Contains(e, "connect") || strings.Contains(e, "comfy"):
		return "ComfyUI 未连通", "确认 ComfyUI 已启动且 comfy_url 正确(默认 127.0.0.1:8190)"
	case strings.Contains(e, "401") || strings.Contains(e, "unauthorized") || strings.Contains(e, "invalid api key"):
		return "API Key 无效", "在 设置 → 智能体调度 重新填写 Key 并保存后重试"
	case strings.Contains(e, "timeout") || strings.Contains(e, "timed out"):
		return "请求超时", "网络波动或服务繁忙,稍后重试;反复超时可检查代理/网络"
	case strings.Contains(e, "json") || strings.Contains(e, "parse") || strings.Contains(e, "unmarshal") ||
		strings.Contains(e, "invalid character") || strings.Contains(e, "looking for beginning"):
		return "LLM 输出异常", "模型输出非预期格式,自动重试一次;仍失败可降低 max_tokens 或换模型"
	default:
		return "未知错误", "点「环境自检」体检项目,或查看运行日志定位具体阶段"
	}
}

// ---- 角色面容特征检测(2026-08-30 ver17 防面容趋同) ----

// manjuFaceFeatureItems 项目体检「角色面容特征」检查项:素材卡主要角色(非群演、
// 非人影灵)的 image_prompt 需 ≥4 类具体五官特征 + ≥1 独有印记、无泛化词。
// 规则与技能侧 SKILL.md 面容独特性硬规范同口径(防男角色定妆撞脸)。
func manjuFaceFeatureItems(ctx *manjuCtx) []manjuHealthItem {
	roots := []string{}
	if ctx.workdir != "" {
		roots = append(roots, filepathJoin(ctx.workdir, "素材"))
	}
	if d := ctx.novelRootDir(); d != "" {
		roots = append(roots, filepathJoin(d, "素材"))
	}
	charFile := firstExisting(roots, "人物生成提示词.md")
	if charFile == "" {
		return nil // 无素材卡:不检查(方案直出路径另有 LLM 纪律)
	}
	b, err := os.ReadFile(charFile)
	if err != nil {
		return nil
	}
	cards := parseCharCards(string(toUTF8(b)), manjuAssetStyle(ctx.style), manjuStyleIs3D(ctx.style))
	bad := []string{}
	for _, c := range cards {
		id, _ := c["id"].(string)
		if id == "" {
			continue
		}
		// 群演轻量卡/非人影灵:面容不适用
		if v, _ := c["minor"].(bool); v {
			continue
		}
		if strings.Contains(id, "影灵") || strings.Contains(id, "影子") {
			continue
		}
		// 非人异物(蠹/镰主/器物/兽形本体等):本体特征非人脸,面容检测不适用
		if sp := str(c["species"]); sp != "" && sp != "人" {
			continue
		}
		if img := str(c["image_prompt"]); img != "" {
			if why := manjuFaceWeakness(img); why != "" {
				bad = append(bad, id+"("+why+")")
			}
		}
	}
	if len(bad) == 0 {
		return []manjuHealthItem{{Key: "face", Label: "角色面容特征", Status: "ok", Detail: "全部主要角色 ≥4 类五官特征+独有印记"}}
	}
	shows := strings.Join(bad, "、")
	if len(shows) > 120 {
		shows = shows[:120] + "…"
	}
	return []manjuHealthItem{{Key: "face", Label: "角色面容特征", Status: "warn",
		Detail:   "面容特征不足/泛化词,定妆图易撞脸: " + shows,
		FixHint:  "按 SKILL.md 面容独特性硬规范补写 ≥4 类具体五官特征+1 独有印记(眼型/眉型/鼻型/唇型/脸型/肤质/发型+scar/mole 等),禁 handsome face 类泛化词",
		Fixable:  false}}
}

// manjuFaceWeakness 单卡面容检测:返回不达标原因(空=达标)。
// 特征类别≥4 且(有印记或胡须)且无泛化词。
func manjuFaceWeakness(img string) string {
	low := strings.ToLower(img)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	cats := 0
	if has("almond eyes", "slanting eyes", "narrow eyes", "round eyes", "droopy eyes", "deep-set eyes", "sharp eyes", "keen eyes", "warm eyes", "dark eyes", "bright eyes", "sunken eyes", "beady eyes", "piercing eyes", "gentle eyes", "sleepy eyes", "hooded eyes", "big round eyes") {
		cats++
	}
	if has("thick brows", "arched brows", "straight brows", "fierce brows", "bushy brows", "heavy brows", "slanting brows", "thick eyebrows") {
		cats++
	}
	if has("straight nose", "hooked nose", "snub nose", "broad nose", "aquiline nose", "flat nose", "bulbous nose") {
		cats++
	}
	if has("thin lips", "full lips", "firm lips", "tight lips", "full mouth") {
		cats++
	}
	if has("square face", "angular jaw", "round face", "oval face", "lean face", "gaunt face", "long face", "broad face", "chiseled jaw", "strong jaw", "soft jaw", "sunken cheeks", "hollow cheeks", "high cheekbones", "haggard face") {
		cats++
	}
	if has("weather-beaten", "weathered", "wrinkled", "leathery", "sallow", "ruddy", "sun-darkened", "lined", "calloused", "greasy", "pallid") {
		cats++
	}
	if has("crew cut", "buzz cut", "long hair", "short hair", "slicked-back", "ponytail", "bun", "bald", "white hair", "grey hair", "gray hair", "black hair", "braid", "curly hair", "mohawk", "side parting", "middle part", "tousled", "shaved head", "thin hair", "wispy hair", "salt-and-pepper hair", "receding hairline") {
		cats++
	}
	mark := has("scar", "mole", "birthmark", "earring", "tattoo", "gold tooth", "freckles", "beauty mark", "missing tooth", "broken nose", "blind eye", "glass eye", "eyepatch", "twin scars", "brand mark") ||
		has("beard", "mustache", "goatee", "stubble", "whiskers", "sideburns")
	generic := has("handsome face", "fair face", "standard face", "ordinary face", "good-looking", "attractive face", "clean-cut face", "regular features")
	switch {
	case generic:
		return "含泛化词"
	case cats < 4:
		return fmt.Sprintf("五官特征仅 %d 类(<4)", cats)
	case !mark:
		return "无独有印记/胡须"
	}
	return ""
}
