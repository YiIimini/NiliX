package manju

// 2026-09-04 画质升级回归:
// ① 镜头指纹含采样参数维度(VAE/LoRA/步数/ref_image_size 变化 → 缓存/成片 stale)
// ② h3RefImageSize 默认 max(官方:identity fidelity 更强)
// ③ 存量运镜黑话反向归一(精确反解 rework_cinematic_phrases.py 的 6 模式,幂等)
// ④ 存量项目画质档一次性迁移 normalizeQualityUpgrade(quality_gen 版本键)

import (
	"encoding/json"
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShotFingerprintIncludesSamplerParams(t *testing.T) {
	shot := manjuShot{Scene: "s", Duration: 4, H3Prompt: "p"}
	ctx := &manjuCtx{w: 768, h: 1344, fps: 24, steps: 20}
	ctx.R = map[string]any{"vae_video": "int8.safetensors", "turbo_lora": "a.safetensors"}
	fpBase := ctx.shotCondFingerprintAt(shot, 768, 1344)
	// VAE 换档(int8→fp16)
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "a.safetensors"}
	if fp := ctx.shotCondFingerprintAt(shot, 768, 1344); fp == fpBase {
		t.Fatalf("vae_video 变化应改变指纹")
	}
	// LoRA 换档(4step→PDD)
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "b.safetensors"}
	fpLora := ctx.shotCondFingerprintAt(shot, 768, 1344)
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "c.safetensors"}
	if fp := ctx.shotCondFingerprintAt(shot, 768, 1344); fp == fpLora {
		t.Fatalf("turbo_lora 变化应改变指纹")
	}
	// 步数变化
	ctx.steps = 8
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "c.safetensors"}
	if fp := ctx.shotCondFingerprintAt(shot, 768, 1344); fp == fpLora {
		t.Fatalf("steps 变化应改变指纹")
	}
	// ref_image_size 变化(match→max)
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "c.safetensors", "ref_image_size": "match"}
	if fp := ctx.shotCondFingerprintAt(shot, 768, 1344); fp == fpLora {
		t.Fatalf("ref_image_size 变化应改变指纹")
	}
	// 同参数重复计算稳定(指纹确定性)
	ctx.R = map[string]any{"vae_video": "fp16.safetensors", "turbo_lora": "a.safetensors"}
	fp1 := ctx.shotCondFingerprintAt(shot, 768, 1344)
	fp2 := ctx.shotCondFingerprintAt(shot, 768, 1344)
	if fp1 != fp2 {
		t.Fatalf("同参数指纹应稳定: %s vs %s", fp1, fp2)
	}
}

func TestH3RefImageSizeDefaultMax(t *testing.T) {
	if v := h3RefImageSize(map[string]any{}); v != "max" {
		t.Fatalf("缺省应为 max(官方身份保真更强), got %q", v)
	}
	if v := h3RefImageSize(map[string]any{"ref_image_size": "match"}); v != "match" {
		t.Fatalf("显式 match 应保留, got %q", v)
	}
	if v := h3RefImageSize(map[string]any{"ref_image_size": "max"}); v != "max" {
		t.Fatalf("显式 max 应保留, got %q", v)
	}
	if v := h3RefImageSize(map[string]any{"ref_image_size": "bogus"}); v != "max" {
		t.Fatalf("非法值应回退 max, got %q", v)
	}
}

func TestOfficializeCameraVerbs(t *testing.T) {
	cases := []struct{ in, want string }{
		// rework_cinematic_phrases.py 写入的 6 模式精确反解(2026-09-03 负优化返正)
		{"The camera performs a slow cinematic dolly push-in with small amplitude toward her face.",
			"The camera pushes in with small amplitude at slow speed toward her face."},
		{"The camera performs a cinematic dolly push-in with large amplitude.",
			"The camera pushes in with large amplitude."},
		{"The camera performs a fast dolly pull-back with large amplitude.",
			"The camera pulls out with large amplitude at fast speed."},
		{"The camera performs a dolly pull-back with small amplitude.",
			"The camera pulls out with small amplitude."},
		{"The camera performs a slow orbital arc with small amplitude around the kneeling crowd.",
			"The camera arcs with small amplitude at slow speed around the kneeling crowd."},
		{"The camera performs an orbital arc with medium amplitude.",
			"The camera arcs with medium amplitude."},
		// 官方句式(新生成/契约返工产物)不命中
		{"The camera pushes in with small amplitude at slow speed.", "The camera pushes in with small amplitude at slow speed."},
		{"The camera pans left, revealing the courtyard.", "The camera pans left, revealing the courtyard."},
	}
	for _, c := range cases {
		if got := manjuOfficializeCameraVerbs(c.in); got != c.want {
			t.Errorf("manjuOfficializeCameraVerbs(%q)\n  = %q\n  want %q", c.in, got, c.want)
		}
	}
	// 幂等
	dirty := "The camera performs a slow cinematic dolly push-in with small amplitude."
	once := manjuOfficializeCameraVerbs(dirty)
	if twice := manjuOfficializeCameraVerbs(once); twice != once {
		t.Errorf("归一应幂等: once=%q twice=%q", once, twice)
	}
}

// TestH3RenderWorkflowPDDInputs 2026-09-04 400 修复回归:MiniMaxH3PDDAccApply
// 节点真实 schema 必填 pdd_file/nfe/lora_strength/head_strength/on_off_grid
// (源码 nodes.py INPUT_TYPES 权威)——旧代码只传 model+lora_name(字段名就错),
// 提交 400 required_input_missing ×5,PDD 模式此前从未跑通。
func TestH3RenderWorkflowPDDInputs(t *testing.T) {
	R := map[string]any{
		"unet_ref2va": "ref2va.safetensors", "unet_fl2va": "fl2va.safetensors",
		"vae_video": "v.safetensors", "vae_audio": "a.safetensors",
		"turbo_lora": "minimax_h3_fl2va_pdd_acc_8step_comfyui.safetensors",
		"turbo_lora_r2v": "minimax_h3_ref2va_pdd_acc_8step_comfyui.safetensors",
		"_pdd_ok": true,
	}
	wf := h3RenderWorkflow(R, 1, 768, 1344, 107, 8, "cache", true, false, 0, 1)
	for id, n := range wf {
		node, _ := n.(map[string]any)
		if node == nil || str(node["class_type"]) != "MiniMaxH3PDDAccApply" {
			continue
		}
		inputs, _ := node["inputs"].(map[string]any)
		for _, k := range []string{"model", "pdd_file", "nfe", "lora_strength", "head_strength", "on_off_grid"} {
			if _, ok := inputs[k]; !ok {
				t.Errorf("PDD 节点缺必填输入 %s(节点 %s): %v", k, id, inputs)
			}
		}
		if v := str(inputs["pdd_file"]); v != "minimax_h3_ref2va_pdd_acc_8step_comfyui.safetensors" {
			t.Errorf("角色镜 pdd_file 应为 r2v 文件, got %q", v)
		}
		if v := str(inputs["nfe"]); v != "8" {
			t.Errorf("nfe 应为 8, got %q", v)
		}
		return
	}
	t.Fatal("未找到 MiniMaxH3PDDAccApply 节点")
}

func TestNormalizeQualityUpgrade(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "loras"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "vae"), 0755)
	for _, f := range []string{
		"minimax_h3_fl2va_pdd_acc_8step_comfyui.safetensors",
		"minimax_h3_ref2va_pdd_acc_8step_comfyui.safetensors",
	} {
		_ = os.WriteFile(filepath.Join(dir, "loras", f), []byte("lora"), 0644)
	}
	_ = os.WriteFile(filepath.Join(dir, "vae", "minimax_h3_video_vae_fp16.safetensors"), []byte("vae"), 0644)

	R := map[string]any{
		"vae_video":      "minimax_h3_video_vae_int8_convrot.safetensors",
		"turbo_lora":     "minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors",
		"turbo_lora_r2v": "minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors",
		"turbo_steps":    4,
	}
	cfg := map[string]any{"render": R}
	ctx := &manjuCtx{sharedModels: dir, R: R, cfg: cfg, configPath: filepath.Join(dir, "config.json")}
	ctx.normalizeQualityUpgrade()
	if v := str(R["vae_video"]); v != "minimax_h3_video_vae_fp16.safetensors" {
		t.Fatalf("VAE 应迁 fp16, got %q", v)
	}
	if v := str(R["turbo_lora"]); !strings.Contains(v, "pdd_acc") {
		t.Fatalf("fl2v LoRA 应迁 PDD, got %q", v)
	}
	if v := str(R["turbo_lora_r2v"]); !strings.Contains(v, "pdd_acc") {
		t.Fatalf("ref2v LoRA 应迁 PDD, got %q", v)
	}
	if n, _ := manjuToInt(R["turbo_steps"]); n != 8 {
		t.Fatalf("PDD 蒸馏步数应归 8, got %d", n)
	}
	if v := str(R["ref_image_size"]); v != "max" {
		t.Fatalf("ref_image_size 应迁 max, got %q", v)
	}
	if g, _ := manjuToInt(R["quality_gen"]); g != 2 {
		t.Fatalf("quality_gen 版本键应写入 2, got %d", g)
	}
	if !strings.Contains(ctx.loraRepair, "画质档升级") {
		t.Fatalf("应产生迁移提示: %q", ctx.loraRepair)
	}
	// 幂等:版本键已写,用户改回 4step 后不再被覆盖
	R["turbo_lora"] = "minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors"
	ctx.normalizeQualityUpgrade()
	if v := str(R["turbo_lora"]); !strings.Contains(v, "4step") {
		t.Fatalf("版本键生效后不应重复迁移, got %q", v)
	}
}

// TestManjuFaceWeaknessShapeTolerant 2026-09-04 检测器正则化回归:
// 旧整词子串匹配形态敏感——"almond-shaped dark brown eyes" 不含 "almond eyes"
// 子串即漏检,小满 6 类特征+印记被判 cats=2 误报实锤。正则允许形容词插入/连字符。
func TestManjuFaceWeaknessShapeTolerant(t *testing.T) {
	// 小满实卡(节选):6 类 + 印记,宽正则应达标(旧词表误报 cats=2)
	xiaoman := "round baby face, large round almond-shaped dark brown eyes, a small dark beauty mark below her left eye, soft gently arched black eyebrows, small snub nose, small full cherry-pink lips, black hair tied in two neat hair buns"
	if why := manjuFaceWeakness(xiaoman); why != "" {
		t.Errorf("小满式形态应达标, got %q", why)
	}
	// 旧形态词仍命中
	oldForm := "almond eyes, thick brows, straight nose, thin lips, square face, short hair, a scar on the cheek"
	if why := manjuFaceWeakness(oldForm); why != "" {
		t.Errorf("标准形态应达标, got %q", why)
	}
	// 真缺特征仍报
	thin := "a handsome face, short black hair, wearing a dark robe"
	if why := manjuFaceWeakness(thin); why != "含泛化词" {
		t.Errorf("泛化词应报, got %q", why)
	}
	if why := manjuFaceWeakness("sharp eyes, long hair"); why == "" {
		t.Errorf("类不足应报")
	}
}

// TestGenVoiceLibExternalSource 2026-09-04 外部音源机制回归:权威目录 .src 标记
// 存在时 genVoiceLibAudio 跳过 edge-tts 合成(只同步),手工导入的真实干声
// 永不被 meta 一致性自愈覆盖。
func TestGenVoiceLibExternalSource(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "audio")
	_ = os.MkdirAll(audio, 0755)
	mp3 := filepath.Join(audio, "lib_male_deep.mp3")
	_ = os.WriteFile(mp3, []byte("real-voice-130kb"), 0644)
	_ = os.WriteFile(filepath.Join(audio, "lib_male_deep.src"), []byte("imported"), 0644)
	// 权威目录走全局 paths.VoiceLibDir(TestMain 指向真实库)——测试临时替换并恢复
	oldLib := paths.VoiceLibDir
	paths.VoiceLibDir = dir
	defer func() { paths.VoiceLibDir = oldLib }()
	ctx := &manjuCtx{sharedModels: dir, comfyInput: filepath.Join(dir, "input")}
	_ = os.MkdirAll(ctx.comfyInput, 0755)
	// 有 .src 标记:即使无 meta 也直接同步成功(不调 edge-tts)
	if err := ctx.genVoiceLibAudio("male_deep"); err != nil { // 表内 key 无 lib_ 前缀(文件名才加)
		t.Fatalf("外部音源应直接同步成功, got %v", err)
	}
	// input 副本已同步且内容未被改写
	b, _ := os.ReadFile(filepath.Join(ctx.comfyInput, "audio", "lib_male_deep.mp3"))
	if string(b) != "real-voice-130kb" {
		t.Fatalf("外部音源内容不得被覆盖, got %q", string(b)[:min(len(b), 20)])
	}
}

// TestManjuEnvCheckModelLines 2026-09-04 回归:check 签名改为 (name, dirs...) 后
// Z-Image 三行调用漏改,目录名被当文件名报"❌ diffusion_models(未找到)"——
// 断言模型行输出的是文件名 ✅,不出现目录名 ❌。
func TestManjuEnvCheckModelLines(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"diffusion_models", "text_encoders", "vae"} {
		_ = os.MkdirAll(filepath.Join(dir, "models", sub), 0755)
	}
	touch := func(sub, name string) {
		_ = os.WriteFile(filepath.Join(dir, "models", sub, name), []byte("x"), 0644)
	}
	touch("diffusion_models", "zu.safetensors")
	touch("text_encoders", "zc.safetensors")
	touch("vae", "zv.safetensors")
	touch("diffusion_models", "ru.safetensors")
	novel := filepath.Join(dir, "novel.md")
	_ = os.WriteFile(novel, []byte("第一章 测试"), 0644)
	cfg := map[string]any{
		"render": map[string]any{
			"comfy_url": "http://127.0.0.1:1", "unet_ref2va": "ru.safetensors",
			"z_image_unet": "zu.safetensors", "z_image_clip": "zc.safetensors", "z_image_vae": "zv.safetensors",
		},
		"paths": map[string]any{"workdir": dir, "novel": novel},
	}
	cfgPath := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, b, 0644)
	oldShared := paths.ComfySharedDir
	paths.ComfySharedDir = dir
	defer func() { paths.ComfySharedDir = oldShared }()
	out := manjuEnvCheck(cfgPath)
	for _, want := range []string{"✅ ru.safetensors", "✅ zu.safetensors", "✅ zc.safetensors", "✅ zv.safetensors"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应含 %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"❌ diffusion_models", "❌ text_encoders", "❌ vae"} {
		if strings.Contains(out, bad) {
			t.Errorf("输出不得把目录名当缺失文件 %s:\n%s", bad, out)
		}
	}
}

// TestAutoVoiceForVoiceLibPriority 2026-09-04 创作侧音色确认:角色卡 voice_lib
// (声源档位 key)最优先于 role/性别/年龄自动匹配;非法值忽略回退自动。
func TestAutoVoiceForVoiceLibPriority(t *testing.T) {
	ctx := &manjuCtx{}
	ctx.charInfo = map[string]map[string]any{}
	// 反派角色但创作时显式绑定好听档 male_sun → 尊重创作选择
	ctx.charInfo["甲"] = map[string]any{"gender": "男", "age": "25岁", "role": "反派", "species": "人", "voice_lib": "male_sun"}
	if got := ctx.autoVoiceFor("甲"); got != "male_sun" {
		t.Fatalf("voice_lib 应最优先, got %q", got)
	}
	// 非法值 → 回退自动匹配(反派→male_deep)
	ctx.charInfo["乙"] = map[string]any{"gender": "男", "age": "25岁", "role": "反派", "species": "人", "voice_lib": "不存在的档位"}
	if got := ctx.autoVoiceFor("乙"); got != "male_deep" {
		t.Fatalf("非法 voice_lib 应回退自动匹配, got %q", got)
	}
	// 未绑定 → 自动匹配照旧
	ctx.charInfo["丙"] = map[string]any{"gender": "女", "age": "8岁", "role": "正角", "species": "人"}
	if got := ctx.autoVoiceFor("丙"); got != "child_girl" {
		t.Fatalf("无 voice_lib 走自动匹配, got %q", got)
	}
}

// TestVoiceBindingsSkipSilentShot 2026-09-04 无台词镜悬空 Audio 行根治:
// voiceBindingsFor 此前按登场角色(不看台词)建绑定,ensureVoiceBindings 注入
// "containing a spoken voiceover" 定义行而 ref_audios 无从挂载(空)——文本承诺
// 画外音+音频为空 = H3 幻觉补人声(被论斤 EP01 镜1/2 无台词出怪配音实锤)。
// 无台词镜(与 ghostVoiceEligible 同口径)不绑;有台词/旁白/内心镜照旧。
func TestVoiceBindingsSkipSilentShot(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "audio")
	_ = os.MkdirAll(audio, 0755)
	_ = os.WriteFile(filepath.Join(audio, "lib_female_warm.mp3"), []byte("voice-bytes"), 0644)
	oldLib := paths.VoiceLibDir
	paths.VoiceLibDir = dir
	defer func() { paths.VoiceLibDir = oldLib }()
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"小铁": {"gender": "女", "age": "20岁", "role": "正角", "species": "人"},
	}, comfyInput: filepath.Join(dir, "input")}
	silent := manjuShot{ID: 1, Characters: []string{"小铁"}, Dialogue: "无", H3Prompt: "subject_definitions:\n<Subject 1> is Xiao Tie.\n\ndetailed_description:\n[Shot 1] She walks."}
	if got := ctx.voiceBindingsFor(silent); len(got) != 0 {
		t.Fatalf("无台词镜不应绑定音色(悬空 Audio 行=幽灵人声诱因), got %v", got)
	}
	spoken := silent
	spoken.Dialogue = "(S1)小铁:「粥还在锅里呢。」"
	spoken.H3Prompt += "\n<Subject 1> (S1) says: <d>[Chinese] 粥还在锅里呢。</d>"
	if got := ctx.voiceBindingsFor(spoken); len(got) == 0 {
		t.Fatal("有台词镜应保留音色绑定")
	}
	inner := silent
	inner.Narration = "内心·小铁:今天的任务……"
	if got := ctx.voiceBindingsFor(inner); len(got) == 0 {
		t.Fatal("内心戏镜应保留音色绑定(内心画外音需要)")
	}
}
