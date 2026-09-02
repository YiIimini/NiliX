package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ensureVoiceBindings:LLM 漏写 <Audio> 定义时程序补写 subject_definitions,有引用则不重复
func TestEnsureVoiceBindings(t *testing.T) {
	// 2026-08-30 官方格式对齐:注入行绑 <Subject M>(M=登场序;Sx 占位由渲染前
	// alignAudioDefs 按画面段实际发声顺序重写),不再绑中文名
	bindings := []voiceBinding{{CharID: "阿拾", Audio: "<Audio 1>", SubN: 1}, {CharID: "陈鱼", Audio: "<Audio 2>", SubN: 2}}

	// 完全无引用 → 补写到 subject_definitions 段(以 summary: 为界)
	hp := "subject_definitions:\n<Subject 1> is ...\n\nsummary:\n[reference generation] ...\ndetailed_description:\n..."
	out := ensureVoiceBindings(hp, bindings, manjuRefContract{})
	if !strings.Contains(out, "<Audio 1> is the voice-timbre reference for <Subject 1> (S1), containing a spoken voiceover.") {
		t.Fatalf("补写缺少 <Audio 1> 定义:\n%s", out)
	}
	if !strings.Contains(out, "<Audio 2> is the voice-timbre reference for <Subject 2> (S2), containing a spoken voiceover.") {
		t.Fatalf("补写缺少 <Audio 2> 定义:\n%s", out)
	}
	// 定义必须在 subject_definitions 段(summary: 之前)
	si, mi := strings.Index(out, "<Audio 1>"), strings.Index(out, "summary:")
	if si < 0 || mi < 0 || si > mi {
		t.Fatalf("<Audio> 定义不在 subject_definitions 段")
	}

	// 全部角色已有定义 → 信任,不重复补写(2026-08-30 逐角色语义:只补缺的角色)
	hp2 := "subject_definitions:\n<Subject 1> is ...\n<Subject 2> is ...\n<Audio 1> is the voice-timbre reference for <Subject 1> (S1), containing a spoken voiceover.\n<Audio 2> is the voice-timbre reference for <Subject 2> (S2), containing a spoken voiceover.\n\nsummary:\n..."
	c2 := manjuRefContract{Chars: []manjuCharSlot{{ID: "阿拾"}, {ID: "陈鱼"}}}
	if out2 := ensureVoiceBindings(hp2, bindings, c2); out2 != hp2 {
		t.Fatalf("全部角色已定义时不应重复补写:\n%s", out2)
	}

	// 空绑定 → 原样返回
	if out3 := ensureVoiceBindings(hp, nil, manjuRefContract{}); out3 != hp {
		t.Fatalf("无绑定不应改动提示词")
	}
}

// h3EncWorkflow:角色镜挂 ref_audios.ref_audio_N 平铺键(与 ref_images 并列),空镜不挂
func TestEncWorkflowVoiceRefs(t *testing.T) {
	wf := h3EncWorkflow(map[string]any{
		"unet_fl2va": "f.safetensors", "unet_ref2va": "r.safetensors",
		"clip": "c.safetensors", "vae_video": "v.safetensors", "vae_audio": "a.safetensors",
	}, "prompt", 768, 1344, 145, []string{"img1.png"}, []string{"audio/voice_a.mp3", "audio/voice_b.mp3"}, "", "cache", true)

	loadAudios := map[string]string{} // nodeID -> audio 值
	var refKeys []string
	var refAudioVals []string
	// 第一遍收集 LoadAudio(nodeID→文件);第二遍收集 ref_audios 引用——
	// 单遍循环依赖 map 遍历顺序,RefToVideo 可能先于 LoadAudio 出现导致映射未填充
	for id, n := range wf {
		m, _ := n.(map[string]any)
		if m == nil {
			continue
		}
		if str(m["class_type"]) == "LoadAudio" {
			ins, _ := m["inputs"].(map[string]any)
			loadAudios[id] = str(ins["audio"])
		}
	}
	for _, n := range wf {
		m, _ := n.(map[string]any)
		if m == nil || str(m["class_type"]) != "MiniMaxH3ReferenceToVideo" {
			continue
		}
		ins, _ := m["inputs"].(map[string]any)
		for k, v := range ins {
			if strings.HasPrefix(k, "ref_audios.") {
				refKeys = append(refKeys, k)
				if arr, ok := v.([]any); ok && len(arr) > 0 {
					refAudioVals = append(refAudioVals, loadAudios[str(arr[0])])
				}
			}
		}
	}
	if len(refKeys) != 2 {
		t.Fatalf("ref_audios 平铺键数量错误: %v", refKeys)
	}
	// map 遍历无序,排序后断言(键集合 + 文件对应)
	sort.Strings(refKeys)
	sort.Strings(refAudioVals)
	if refKeys[0] != "ref_audios.ref_audio_0" || refKeys[1] != "ref_audios.ref_audio_1" {
		t.Fatalf("ref_audios 平铺键错误: %v", refKeys)
	}
	if len(refAudioVals) != 2 || refAudioVals[0] != "audio/voice_a.mp3" || refAudioVals[1] != "audio/voice_b.mp3" {
		t.Fatalf("ref_audios 引用与音频文件不对应(应 voice_a→Audio1/voice_b→Audio2): %v", refAudioVals)
	}

	// 空镜(MiniMaxH3ImageToVideo)无 ref_audios
	wf2 := h3EncWorkflow(map[string]any{
		"unet_fl2va": "f.safetensors", "unet_ref2va": "r.safetensors",
		"clip": "c.safetensors", "vae_video": "v.safetensors", "vae_audio": "a.safetensors",
	}, "prompt", 768, 1344, 145, nil, []string{"audio/voice_a.mp3"}, "scene.png", "cache", false)
	for _, n := range wf2 {
		m, _ := n.(map[string]any)
		if m == nil {
			continue
		}
		if str(m["class_type"]) == "LoadAudio" {
			t.Fatalf("空镜不应挂音色参考(ImageToVideo 无 ref_audios 输入)")
		}
	}
}

// audioNum:"<Audio 3>" → 3
func TestAudioNum(t *testing.T) {
	if n := audioNum("<Audio 3>"); n != 3 {
		t.Fatalf("audioNum(<Audio 3>) = %d, want 3", n)
	}
	if n := audioNum(""); n != 1 {
		t.Fatalf("audioNum('') = %d, want 1(兜底)", n)
	}
}

// autoVoiceFor 按角色人设自动匹配风格音色库(2026-08-27 矩阵化:年龄×性别全覆盖,
// 儿童男女分声、老年女不再误用粤语音色)
func TestAutoVoiceFor(t *testing.T) {
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"男童":   {"gender": "男", "age": "儿童"},
		"女童":   {"gender": "女", "age": "孩童"},
		"男少年":  {"gender": "男", "age": "少年"},
		"男青年":  {"gender": "男", "age": "青年"},
		"男中年":  {"gender": "男", "age": "中年"},
		"男老年":  {"gender": "男", "age": "老年"},
		"女少女":  {"gender": "女", "age": "少女"},
		"女青年":  {"gender": "女", "age": "青年"},
		"女中年":  {"gender": "女", "age": "中年"},
		"女老年":  {"gender": "女", "age": "老年"},
		"男反派":  {"gender": "男", "age": "青年", "role": "反派"},
		"女反派":  {"gender": "女", "age": "青年", "role": "反派"},
		"灵宠":   {"gender": "女", "age": "幼年", "species": "灵宠"},
		"无信息":  {},
		// 2026-08-30 ver14 数字年龄档位(此前关键词白名单全落空→默认青年声:
		// "74岁" 掉 male_sun/female_warm,与角色年龄严重脱节)
		"八岁女孩": {"gender": "女", "age": "8岁"},
		"十九少年": {"gender": "男", "age": "19岁"},
		"廿二青年": {"gender": "男", "age": "22岁"},
		"七四老妪": {"gender": "女", "age": "74岁"},
		"四八中男": {"gender": "男", "age": "48岁"},
	}}
	cases := []struct{ cid, want string }{
		{"男童", "child_boy"},
		{"女童", "child_girl"},
		{"男少年", "boy_teen"},
		{"男青年", "male_sun"},
		{"男中年", "male_mag"},
		{"男老年", "male_elder"},
		{"女少女", "girl_lively"},
		{"女青年", "female_warm"},
		{"女中年", "female_mature"},
		{"女老年", "female_elder"},
		{"男反派", "male_deep"},
		{"女反派", "female_deep"},
		{"灵宠", "beast_cute"},
		{"无信息", "female_warm"}, // 有角色卡但字段空 → 兜底温柔女声(有音色比没有强)
		{"八岁女孩", "child_girl"},
		{"十九少年", "boy_teen"},
		{"廿二青年", "male_sun"},
		{"七四老妪", "female_elder"},
		{"四八中男", "male_mag"},
	}
	for _, c := range cases {
		if got := ctx.autoVoiceFor(c.cid); got != c.want {
			t.Fatalf("autoVoiceFor(%s) = %s, want %s", c.cid, got, c.want)
		}
	}
	// 角色不存在(charInfo 无此 id)→ 空(不匹配)
	if got := ctx.autoVoiceFor("不存在"); got != "" {
		t.Fatalf("autoVoiceFor(不存在) = %s, want 空", got)
	}
	// 全部返回值必须是库内合法 Key(渲染端 lib_<Key>.mp3 依赖)
	for _, c := range cases {
		if manjuVoiceLibFor(ctx.autoVoiceFor(c.cid)) == nil {
			t.Fatalf("autoVoiceFor(%s) 返回值不在音色库: %s", c.cid, ctx.autoVoiceFor(c.cid))
		}
	}
}

// 音色库健康:Key 全局唯一(文件名/绑定标识);已下线音色不得回流;
// 矩阵必备档位齐全(儿童男女/少年少女/青年男女/中年男女/老年男女)
func TestManjuVoiceLibHealth(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range manjuVoiceLib {
		if v.Key == "" {
			t.Fatalf("音色库存在空 Key: %+v", v)
		}
		if seen[v.Key] {
			t.Fatalf("音色库 Key 重复: %s", v.Key)
		}
		seen[v.Key] = true
	}
	// 已实测下线的 edge 音色(NoAudioReceived/列表除名,2026-08-27 复核)
	for _, dead := range []string{"Xiaochen", "Xiaomo", "Xiaoshuang", "Xiaoyou", "Xiaohan", "Xiaoxuan"} {
		for _, v := range manjuVoiceLib {
			if strings.Contains(v.Name, dead) {
				t.Fatalf("音色库含已下线音色 %s(%s)", dead, v.Key)
			}
		}
	}
	for _, must := range []string{
		"child_boy", "child_girl", "boy_teen", "girl_lively",
		"male_sun", "female_warm", "male_mag", "female_mature", "male_elder", "female_elder",
	} {
		if manjuVoiceLibFor(must) == nil {
			t.Fatalf("音色库缺年龄×性别必备档位: %s", must)
		}
	}
	// 旧版 edge 音色名(存量 plan 绑定)仍可解析(Key 优先/Name 兼容)
	if manjuVoiceLibFor("zh-CN-XiaoxiaoNeural") == nil || manjuVoiceLibFor("zh-CN-YunxiNeural") == nil {
		t.Fatalf("旧版 edge 音色名兼容解析失效")
	}
}

// 自备音色包:目录扫描只认 mp3/wav,pack: 前缀绑定值由后端复制不走 TTS
func TestManjuVoicePacksScan(t *testing.T) {
	dir := manjuVoicePacksDir()
	_ = os.MkdirAll(dir, 0755)
	defer os.RemoveAll(filepath.Join(dir, "_test_pack.mp3"))
	defer os.RemoveAll(filepath.Join(dir, "_test_pack.txt"))
	_ = os.WriteFile(filepath.Join(dir, "_test_pack.mp3"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "_test_pack.txt"), []byte("x"), 0644)
	found := false
	for _, f := range manjuVoicePacks() {
		if f == "_test_pack.mp3" {
			found = true
		}
		if strings.HasSuffix(f, ".txt") {
			t.Fatalf("音色包扫描误收非音频: %s", f)
		}
	}
	if !found {
		t.Fatalf("音色包扫描未发现 _test_pack.mp3(目录: %s)", dir)
	}
}

// charVoiceNames/voiceBindingsFor 集成:确定性命名查文件,无音色角色跳过,编号与登场顺序一致
func TestCharVoiceNamesIntegration(t *testing.T) {
	comfyIn := filepath.Join(os.TempDir(), "nilix-test-comfy-in")
	_ = os.MkdirAll(filepath.Join(comfyIn, "audio"), 0755)
	// 只给「阿拾」建音色文件(路人/小雅无音色)
	_ = os.WriteFile(filepath.Join(comfyIn, "audio", "voice_testproj_阿拾.mp3"), []byte("x"), 0644)
	ctx := &manjuCtx{project: "testproj", comfyInput: comfyIn}
	s := manjuShot{Characters: []string{"阿拾", "路人", "小雅"}}

	got := ctx.charVoiceNames(s)
	if len(got) != 1 || got[0] != "audio/voice_testproj_阿拾.mp3" {
		t.Fatalf("charVoiceNames = %v, want [audio/voice_testproj_阿拾.mp3](无音色角色跳过)", got)
	}
	vbs := ctx.voiceBindingsFor(s)
	if len(vbs) != 1 || vbs[0].CharID != "阿拾" || vbs[0].Audio != "<Audio 1>" {
		t.Fatalf("voiceBindingsFor = %+v, want 阿拾→<Audio 1>", vbs)
	}
	// 音色文件删除后视为未绑定
	_ = os.Remove(filepath.Join(comfyIn, "audio", "voice_testproj_阿拾.mp3"))
	if got := ctx.charVoiceNames(s); len(got) != 0 {
		t.Fatalf("音色文件删除后 charVoiceNames 应为空: %v", got)
	}
	// 超过 3 角色只取前 3
	for i := 0; i < 4; i++ {
		_ = os.WriteFile(filepath.Join(comfyIn, "audio", fmt.Sprintf("voice_testproj_c%d.mp3", i)), []byte("x"), 0644)
	}
	s4 := manjuShot{Characters: []string{"c0", "c1", "c2", "c3"}}
	if got := ctx.charVoiceNames(s4); len(got) != 3 {
		t.Fatalf("超过 3 角色应只取前 3: %v", got)
	}
}

// ---- 2026-08-30 ver15:同档位差异化变体 + 方言 + 旁白叙述音色 ----

func TestVoiceVariantsFor(t *testing.T) {
	vs := voiceVariantsFor("male_mag")
	if len(vs) < 2 || vs[0] != "male_mag" || vs[1] != "male_mag_2" {
		t.Fatalf("male_mag 变体链 = %v, want [male_mag male_mag_2 ...]", vs)
	}
	if len(voiceVariantsFor("hk_male")) != 1 {
		t.Fatalf("无变体条目应只返回自身")
	}
	if vs2 := voiceVariantsFor("male_sun"); len(vs2) < 2 || vs2[1] != "male_sun_2" {
		t.Fatalf("male_sun 变体链 = %v", vs2)
	}
}

func TestDialectVoiceFor(t *testing.T) {
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"东北大婶": {"gender": "女", "appearance": "东北大妈,红棉袄"},
		"川渝老板": {"gender": "男", "voice": "四川男声,热辣"},
		"河南大叔": {"gender": "男", "appearance": "河南老农"},
		"粤语仔":  {"gender": "男", "voice": "粤语,港风"},
		"台湾妹":  {"gender": "女", "voice": "台普"},
		"东北大汉": {"gender": "男", "appearance": "东北大汉"}, // 东北男声无声源→回退
		"普通话男": {"gender": "男", "age": "45岁"},            // 无地域词→普通话档位
	}}
	cases := []struct{ cid, want string }{
		{"东北大婶", "cn_dongbei"},
		{"川渝老板", "cn_sichuan"},
		{"河南大叔", "cn_henan"},
		{"粤语仔", "hk_male"},
		{"台湾妹", "tw_female"},
		{"东北大汉", ""}, // 东北无男声源,性别不匹配→回退
		{"普通话男", ""},
	}
	for _, c := range cases {
		if got := ctx.dialectVoiceFor(c.cid); got != c.want {
			t.Fatalf("dialectVoiceFor(%s) = %q, want %q", c.cid, got, c.want)
		}
	}
}

// 同档位角色分配不同变体:陈守家(45男)+玄经理(40男)两个中年男不得同音色
func TestBuildVoiceAssignmentVariants(t *testing.T) {
	// buildVoiceAssignment 依赖 charIDs(loadPlan),这里直接验证分配逻辑核心:
	// voiceVariantsFor 轮转——两个同档位角色 index 0/1 → male_mag / male_mag_2
	vs := voiceVariantsFor("male_mag")
	if vs[0] == vs[1%len(vs)] {
		t.Fatalf("同档位变体轮转不能同音色")
	}
}

func TestManjuOffscreenKeyNarrator(t *testing.T) {
	if got := manjuOffscreenKey("The narrator with a calm, neutral storytelling voice"); got != "male_narrator" {
		t.Fatalf("narrator desc 应绑定叙述音色, got %q", got)
	}
	// 角色/群杂推断不受影响
	if got := manjuOffscreenKey("A hushed middle-aged woman's voice off-screen among the onlookers"); got != "female_mature" {
		t.Fatalf("画外女声推断 = %q, want female_mature", got)
	}
	if got := manjuOffscreenKey("a man speaking Mandarin with a lively Sichuan accent"); got != "male_mag" {
		t.Fatalf("方言描述应落普通话档位兜底, got %q", got)
	}
}

// 角色卡「音色」行 → card["voice"](技能侧配置优先)
func TestParseCharCardsVoiceField(t *testing.T) {
	text := `# 《测试》人物生成提示词（风格说明）

# 统一风格前缀（所有角色共用）
...
## 1. 陈守家（主角·下岗保安）
- 记忆点：深蓝旧保安服/红绳项链/掉漆保温杯
- 音色：中年磁性男声，语速偏慢
` + "```\nCinematic film still, photorealistic, a 45-year-old male Chinese security guard.\n```" + `
## 2. 玄经理（物业经理·规则控）
- 记忆点：壳形公文包/老花镜/一沓表格
- 音色：中年沉稳男声
` + "```\nCinematic film still, photorealistic, a 40-year-old male Chinese office manager.\n```" + `
`
	cards := parseCharCards(text, "", false)
	if len(cards) != 2 {
		t.Fatalf("角色卡数 = %d, want 2", len(cards))
	}
	byID := map[string]string{}
	for _, c := range cards {
		byID[str(c["id"])] = str(c["voice"])
	}
	if byID["陈守家"] != "中年磁性男声，语速偏慢" {
		t.Fatalf("陈守家 voice = %q", byID["陈守家"])
	}
	if byID["玄经理"] != "中年沉稳男声" {
		t.Fatalf("玄经理 voice = %q", byID["玄经理"])
	}
}

// TestInnerVoiceAssignedFixed 内心配音音色固定(2026-09-01 用户反馈「内心音色不能随机」):
// ① 内心 key 必须与角色对白同源同变体(assignedVoiceFor)——此前 autoVoiceFor 恒基底
//    (male_sun)而描述短语 voiceTimbrePhrase 走变体(male_sun_2):音频挂基底、prompt
//    描述变体,描述与参考音频打架=随机漂移;同档角色内心挤同一基底声、内心与对白
//    不同声;
// ② 同一角色内心音色跨镜恒定(两次调用结果一致);
// ③ 挂载侧 offscreenVoiceKeyFor 解析「quiet inner voice of X」与注入侧 innerVoiceFor 同源。
func TestInnerVoiceAssignedFixed(t *testing.T) {
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"阿影": {"gender": "男", "age": "22岁"},
		"小白": {"gender": "女", "age": "18岁"},
	}}
	// voiceAssign 模拟:阿影=变体 2、小白=变体 2(差异化分配后)
	ctx.voiceAssign = map[string]string{"阿影": "male_sun_2", "小白": "girl_lively_2"}
	shot := manjuShot{Characters: []string{"阿影", "小白"}, Narration: "内心·阿影：他竟敢这样看我。"}
	cid, key := ctx.innerVoiceFor(shot)
	if cid != "阿影" || key != "male_sun_2" {
		t.Fatalf("内心音色应与对白同源变体: cid=%s key=%s, want 阿影/male_sun_2", cid, key)
	}
	// 跨镜恒定:再次调用结果一致
	cid2, key2 := ctx.innerVoiceFor(shot)
	if cid2 != cid || key2 != key {
		t.Fatalf("内心音色跨镜漂移: (%s,%s) → (%s,%s)", cid, key, cid2, key2)
	}
	// 挂载侧与注入侧同源:offscreenVoiceKeyFor 解析注入的 desc 得到同一变体 key
	hp := "subject_definitions:\n<Subject 1> is 阿影.\n\nsummary:\n.\n\ndetailed_description:\nThe narrator says in an off-screen voiceover: <d>他竟敢这样看我。</d> while the on-screen characters' lips remain completely closed."
	obs := ctx.manjuOffscreenBindings(hp, cid, key)
	found := false
	for _, ob := range obs {
		if strings.Contains(ob.Desc, "quiet inner voice of 阿影") {
			found = true
			// 挂载侧解析 desc → key 必须与注入 key 一致(否则音频与描述错配)
			if got := ctx.offscreenVoiceKeyFor(ob.Desc, ctx.refContractFor(shot)); got != key {
				t.Fatalf("挂载侧内心音色 %s != 注入侧 %s(描述/音频错配=随机漂移)", got, key)
			}
		}
	}
	if !found {
		t.Fatalf("内心戏镜应绑定角色内心音色, obs=%+v", obs)
	}
	// 描述短语与变体一致:voiceTimbrePhrase(阿影)应含变体短语
	phrase := ctx.voiceTimbrePhrase("阿影")
	if phrase != manjuVoicePhraseFor("male_sun_2") {
		t.Fatalf("内心描述短语应走 assignedVoiceFor 变体: %q", phrase)
	}
}

// TestInnerVoiceFallbackAuto 无 voiceAssign(懒构建未跑/角色无卡)时内心回退
// autoVoiceFor 档位——仍与角色对白 autoVoiceFor 同源(不会随机到别档)。
func TestInnerVoiceFallbackAuto(t *testing.T) {
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"阿影": {"gender": "男", "age": "22岁"},
	}}
	shot := manjuShot{Characters: []string{"阿影"}, Narration: "内心·阿影：他竟敢这样看我。"}
	_, key := ctx.innerVoiceFor(shot)
	if key != "male_sun" {
		t.Fatalf("无分配时内心应回退档位基底 male_sun, got %s", key)
	}
}
