package api

// manju_prompt_align_test.go 契约对齐七模块单测(2026-08-30 H3 官方源码核验转化)。
// 用例来源:①MiniMax-H3 官方仓库/ComfyUI 节点源码核验出的机制;②金丹一万重
// EP01 直出产物实锤的错位形态(Picture 语义错位/S 跳号/Audio 绑中文名/时码越界/
// retention 括号损坏)。每模块独立断言 + 编排入口幂等断言(幂等是对齐层能进指纹
// 汇点的前提:非幂等会导致同文本两次对齐出不同指纹,缓存恒失效)。

import (
	"strings"
	"testing"
)

// 金丹一万重 EP01 镜1 直出产物骨架(裁剪):老赵台词标 (S6)、Audio 绑中文名、
// <d> 无语言标签、场景行正常——Sx/Audio/语言标签三模块的真实回归输入。
const alignFixtureShot1 = `subject_definitions:
<Subject 1> is Jiang Que, an 18-year-old male Chinese servant disciple in <Picture 1>, with a lean wiry build and black hair tied with an old cloth strip.
<Subject 2> is Old Zhao, a 60-year-old Chinese porridge cook in <Picture 2>, with a wrinkled kind face, holding a chipped porcelain bowl.
<Subject 3> is the firewood courtyard before dawn in <Picture 3>, with stacked firewood and a large water vat.

<Audio 1> is the voice-timbre reference for the voice of 姜缺 (S1), containing a spoken voiceover.
<Audio 2> is the voice-timbre reference for the voice of 老赵 (S2), containing a spoken voiceover.

summary:
[reference generation] The target video shows Jiang Que grinding his old axe by the water vat while Old Zhao drinks porridge.

retention_analysis:
<Subject 1> (appears in Shot 1]): fully_preserved - grey robe retained.
<Subject 2> (appears in Shot 1]): fully_preserved - chipped bowl retained.
<Subject 3> (appears in Shot 1]): fully_preserved - firewood stacks retained.

detailed_description:
The target video uses a realistic live-action film style. [Shot 1] A medium shot frames the firewood courtyard: Jiang Que crouches at the left by the water vat. The old cook lowers his bowl, glances over, and says with a mumbling mouth (S6): <d>今儿大比,你磨它干啥。</d> Jiang Que keeps grinding, his lips moving as he answers plainly (S1): <d>测完灵,下午还有三十捆柴。</d> The camera pushes in with small amplitude at slow speed.

overall_soundscape:
Rhythmic whetting of stone on steel and distant mountain wind.

non_diegetic_music:
A quiet low bamboo flute at a slow tempo.`

// 金丹一万重 EP01 镜3 直出骨架:场景占 <Picture 1>、人物占 <Picture 2>(挂载顺序
// 实为人物图在前场景图后)、路人画外音标 (S11)、时码 At 00:15.000 越界(镜长 5 秒)。
const alignFixtureShot3 = `subject_definitions:
<Subject 1> is the spirit-testing plaza of Qingming Sect in <Picture 1>, with a ninety-foot grey stele at the north end and thousands of disciples in dark robes.
<Subject 2> is Jiang Que, the grey-robed servant youth in <Picture 2>, walking at the tail of the crowd.

summary:
[reference generation] The target video sweeps across the plaza as Jiang Que merges into the crowd.

retention_analysis:
<Subject 1> (appears in Shot 1]): fully_preserved - stele and crowd retained.
<Subject 2> (appears in Shot 1]): fully_preserved - grey robe retained.

detailed_description:
The target video uses a realistic live-action film style. [Shot 3] At 00:15.000, an extreme wide shot: the pale winter sun bleaches the spirit-testing stele. A young male voice among the onlookers, excited and carrying far (S11), says in an off-screen voiceover: <d>九千九百九十九人,一个不少。</d> while no on-screen character's lips move.

overall_soundscape:
A dense human murmur like a beehive and cold wind over stone flags.

non_diegetic_music:
Low sustained strings swelling slightly.`

func alignFixtureContract() manjuRefContract {
	// 镜1 契约:姜缺 2 视图(front/full)+老赵 1 视图+场景图,共 4 槽;两人都有音色
	return manjuRefContract{
		Chars: []manjuCharSlot{
			{ID: "姜缺", PicStart: 1, PicEnd: 2},
			{ID: "老赵", PicStart: 3, PicEnd: 3},
		},
		SceneName:   "杂役房柴院",
		SceneSlot:   4,
		PicSlots:    4,
		VoiceRoster: []string{"姜缺", "老赵"},
	}
}

// 模块四:Sx 镜内重编——EP01 镜1 实锤:老赵台词标 (S6)、姜缺 (S1)。
// 官方语义:按画面段发声顺序从 S1 连续分配 → 老赵应为 S1(先说)、姜缺 S2。
func TestAlignSpeakerIDsRenumbersInClipOrder(t *testing.T) {
	out := alignSpeakerIDs(alignFixtureShot1)
	if strings.Contains(out, "(S6)") {
		t.Fatalf("全片坐标 (S6) 应被重编, got: %s", out)
	}
	if !strings.Contains(out, "says with a mumbling mouth (S1):") {
		t.Errorf("老赵先发声应为 (S1), got: %s", out)
	}
	if !strings.Contains(out, "answers plainly (S2):") {
		t.Errorf("姜缺后发声应为 (S2), got: %s", out)
	}
	// 幂等
	if again := alignSpeakerIDs(out); again != out {
		t.Errorf("alignSpeakerIDs 应幂等")
	}
}

// 模块四:Sx 重编覆盖 Audio 定义行(定义行的 Sx 与画面段实际说话者一致)
func TestAlignSpeakerIDsCoversAudioLines(t *testing.T) {
	out := alignSpeakerIDs(alignFixtureShot1)
	// 老赵=S1、姜缺=S2:Audio 定义行的 (S1)/(S2) 同步重编后指向新身份
	if !strings.Contains(out, "for the voice of 姜缺 (S2), containing") {
		t.Errorf("姜缺的 Audio 行 Sx 应随画面段重编为 S2(姜缺后发声), got 片段: %s",
			stringCut(out, "voice of 姜缺", "\n"))
	}
}

// 模块六:语言标签规范化——[中文]→[Chinese]、裸中文补标、[English] 不动
func TestAlignDialogueLangTags(t *testing.T) {
	in := "<d>[中文]陈默？</d> and <d>[chinese]你好</d> and <d>裸中文台词</d> and <d>[English] hello</d> and <d>[unclear] …</d>"
	out := alignDialogueLangTags(in)
	for _, want := range []string{"<d>[Chinese] 陈默？</d>", "<d>[Chinese] 你好</d>", "<d>[Chinese] 裸中文台词</d>", "<d>[English] hello</d>"} {
		if !strings.Contains(out, want) {
			t.Errorf("应含 %s, got: %s", want, out)
		}
	}
	if strings.Contains(out, "[中文]") || strings.Contains(out, "[chinese]") {
		t.Errorf("中文标签词应规范化, got: %s", out)
	}
	if again := alignDialogueLangTags(out); again != out {
		t.Errorf("alignDialogueLangTags 应幂等")
	}
}

// 模块七:时码 clip-local——EP01 镜3 实锤:5 秒镜写 At 00:15.000(全片轴)。
// 无有效锚点可平移时(唯一时码越界),剥时码保 [Shot 1] 句式。
func TestAlignShotTimecodesClipsToLocal(t *testing.T) {
	out := alignShotTimecodes(alignFixtureShot3, 5)
	if strings.Contains(out, "At 00:15.000") {
		t.Fatalf("越界全片时码应被剥除, got: %s", stringCut(out, "[Shot", "\n"))
	}
	if !strings.Contains(out, "[Shot 1]") {
		t.Errorf("镜号应重编为本镜 [Shot 1], got: %s", stringCut(out, "[Shot", "\n"))
	}
	// 平移场景:多切点全片轴 00:05.000/00:08.000 → 本镜 00:00.000 起剥、00:03.000
	in := "detailed_description:\n[Shot 2] At 00:05.000 first beat. [Shot 3] At 00:08.000 second beat."
	out2 := alignShotTimecodes(in, 10)
	if !strings.Contains(out2, "[Shot 1] first beat") {
		t.Errorf("平移到 0 点的首段应剥时码(官方:首段无时码), got: %s", out2)
	}
	if !strings.Contains(out2, "[Shot 2] At 00:03.000 second beat") {
		t.Errorf("后续切点应同步平移, got: %s", out2)
	}
	if again := alignShotTimecodes(out2, 10); again != out2 {
		t.Errorf("alignShotTimecodes 应幂等")
	}
}

// 模块一:retention 括号修复——EP01 实锤 "(appears in Shot 1])"
func TestRepairRetentionMarkers(t *testing.T) {
	in := "<Subject 1> (appears in Shot 1]): fully_preserved - robe retained."
	out := repairRetentionMarkers(in)
	if !strings.Contains(out, "(appears in [Shot 1])") {
		t.Errorf("应修复为 (appears in [Shot 1]), got: %s", out)
	}
	if again := repairRetentionMarkers(out); again != out {
		t.Errorf("repairRetentionMarkers 应幂等")
	}
}

// 模块三:权威挂载清单注入——槽位归属声明 + 幂等
func TestInjectAttachmentManifest(t *testing.T) {
	out := injectAttachmentManifest(alignFixtureShot1, alignFixtureContract())
	if !strings.Contains(out, manjuAttachmentManifestKey) {
		t.Fatalf("应注入权威清单")
	}
	if !strings.Contains(out, "<Picture 1-2> are the character 姜缺") {
		t.Errorf("姜缺槽区间应声明为 1-2, got: %s", stringCut(out, manjuAttachmentManifestKey, "\n"))
	}
	if !strings.Contains(out, "<Picture 4> is the scene/environment reference") {
		t.Errorf("场景槽应声明为 4 且非人脸, got: %s", stringCut(out, manjuAttachmentManifestKey, "\n"))
	}
	if again := injectAttachmentManifest(out, alignFixtureContract()); again != out {
		t.Errorf("injectAttachmentManifest 应幂等")
	}
}

// 模块五:Audio 规范化——绑中文名→绑 <Subject M> (Sx)、编号压缩、幽灵清理。
// 前置:Sx 重编先行(老赵 S1、姜缺 S2)。
func TestAlignAudioDefsBindsSubject(t *testing.T) {
	pre := alignSpeakerIDs(alignFixtureShot1)
	out := alignAudioDefs(pre, alignFixtureContract(), "群演·老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。")
	if strings.Contains(out, "for the voice of 姜缺") || strings.Contains(out, "for the voice of 老赵") {
		t.Fatalf("Audio 定义应重绑 <Subject M> 而非中文名, got: %s",
			stringCut(out, "<Audio 1>", "\n")+stringCut(out, "<Audio 2>", "\n"))
	}
	if !strings.Contains(out, "for <Subject 1> (S2), containing") {
		t.Errorf("姜缺=Subject 1、后发声 S2:应写 for <Subject 1> (S2), got: %s", stringCut(out, "<Audio ", "\n"))
	}
	if !strings.Contains(out, "for <Subject 2> (S1), containing") {
		t.Errorf("老赵=Subject 2、先发声 S1:应写 for <Subject 2> (S1), got: %s", stringCut(out, "<Audio ", "\n"))
	}
	if again := alignAudioDefs(out, alignFixtureContract(), "群演·老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。"); again != out {
		t.Errorf("alignAudioDefs 应幂等")
	}
}

// 模块五:幽灵 Audio(无法归属)整行删除
func TestAlignAudioDefsDropsGhost(t *testing.T) {
	in := strings.Replace(alignFixtureShot1,
		"<Audio 2> is the voice-timbre reference for the voice of 老赵 (S2), containing a spoken voiceover.",
		"<Audio 2> is the voice-timbre reference for the voice of 路人甲 (S9), containing a spoken voiceover.",
		1)
	out := alignAudioDefs(alignSpeakerIDs(in), alignFixtureContract(), "群演·老赵:今儿大比。")
	if strings.Contains(out, "路人甲") {
		t.Errorf("幽灵 Audio 定义(路人甲不在登场名单)应删除, got: %s", stringCut(out, "<Audio 2>", "\n"))
	}
}

// 模块五:共用解析 manjuAudioDefTarget——对齐后 <Subject M> 与历史中文名双格式
func TestManjuAudioDefTarget(t *testing.T) {
	c := alignFixtureContract()
	if cid, off := manjuAudioDefTarget("for <Subject 1> (S2)", c); cid != "姜缺" || off != "" {
		t.Errorf("Subject 1 应解析为 姜缺, got cid=%q off=%q", cid, off)
	}
	if cid, off := manjuAudioDefTarget("for the voice of 老赵 (S1)", c); cid != "老赵" || off != "" {
		t.Errorf("历史中文名应解析为 老赵, got cid=%q off=%q", cid, off)
	}
	if _, off := manjuAudioDefTarget("for the off-screen voice described as A young male voice, excited (S3)", c); off == "" {
		t.Errorf("画外音应返回声线描述")
	}
	if cid, _ := manjuAudioDefTarget("for the voice of 路人甲 (S9)", c); cid != "" {
		t.Errorf("非登场角色应解析为空, got %q", cid)
	}
}

// 模块二:Picture 槽位重排——镜2 实锤场景:Q 版小人占 <Picture 3>(实际第 3 槽=老赵)。
// 契约:姜缺 2 视图(槽 1-2)+老赵 1 视图(槽 3)+场景(槽 4);LLM 写法姜缺 P1、
// 老赵 P2、Q 版 P3——人物行按序重排:姜缺 P1(不变)、老赵 P2→P3;Q 版行
// (chibi 人物信号、非登场角色=第 3 人物行超契约)不映射。
func TestAlignPictureRefsRenumbersToMountOrder(t *testing.T) {
	in := `subject_definitions:
<Subject 1> is Jiang Que, the grey-robed servant youth in <Picture 1>, with bright determined eyes and black hair.
<Subject 2> is Old Zhao, the porridge cook in <Picture 2>, holding his chipped bowl with both hands.
<Subject 3> is a chibi mini-Jiang Que in <Picture 3>, with the same grey robe.

summary:
[reference generation] test`
	out := alignPictureRefs(in, alignFixtureContract())
	if !strings.Contains(out, "porridge cook in <Picture 3>") {
		t.Errorf("老赵(第 2 人物行)应重排到其槽区间起点 3, got: %s", stringCut(out, "<Subject 2>", "\n"))
	}
	if !strings.Contains(out, "grey-robed servant youth in <Picture 1>") {
		t.Errorf("姜缺(第 1 人物行)应保持槽 1, got: %s", stringCut(out, "<Subject 1>", "\n"))
	}
	// 幂等
	if again := alignPictureRefs(out, alignFixtureContract()); again != out {
		t.Errorf("alignPictureRefs 应幂等")
	}
}

// 模块二:场景行含 "environment" → 场景槽
func TestAlignPictureRefsSceneLine(t *testing.T) {
	in := `subject_definitions:
<Subject 1> is Jiang Que in <Picture 1>.
<Subject 2> is the firewood courtyard environment in <Picture 2>, with stacked firewood.

summary:
[reference generation] test`
	out := alignPictureRefs(in, alignFixtureContract())
	if !strings.Contains(out, "firewood courtyard environment in <Picture 4>") {
		t.Errorf("场景行应重排到场景槽 4, got: %s", stringCut(out, "<Subject 2>", "\n"))
	}
}

// 模块八:契约校验——四类问题各出一策
func TestValidatePromptContract(t *testing.T) {
	s := manjuShot{Duration: 5, Characters: []string{"姜缺", "老赵"}}
	bad := "subject_definitions:\n<Subject 1> is Jiang Que in <Picture 7>.\n\nsummary:\n[reference generation] t\n\ndetailed_description:\n[Shot 2] At 00:15.000, he (S6) says: <d>裸台词</d>."
	problems := validatePromptContract(bad, s, 4)
	joined := strings.Join(problems, "|")
	for _, want := range []string{"Picture", "说话者编号跳号", "时码", "语言标签"} {
		if !strings.Contains(joined, want) {
			t.Errorf("校验应报「%s」问题, got: %v", want, problems)
		}
	}
	good := "subject_definitions:\n<Subject 1> is Jiang Que in <Picture 1>.\n\nsummary:\n[reference generation] t\n\ndetailed_description:\n[Shot 1] He (S1) says: <d>[Chinese] 台词</d>."
	if ps := validatePromptContract(good, s, 4); len(ps) != 0 {
		t.Errorf("合规提示词不应报问题, got: %v", ps)
	}
}

// 编排入口:EP01 镜1 全链 + 幂等(对齐两次结果一致——进指纹汇点的前提)
func TestManjuAlignShotPromptEndToEnd(t *testing.T) {
	out := manjuAlignShotPrompt(alignFixtureShot1, alignFixtureContract(), 5, "群演·老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。")
	for _, want := range []string{
		"<d>[Chinese] 今儿大比,你磨它干啥。</d>",   // 语言标签
		"says with a mumbling mouth (S1):",       // Sx 镜内重编
		"for <Subject 2> (S1), containing",        // Audio 绑 Subject+实际 Sx
		manjuAttachmentManifestKey,               // 权威清单
		"(appears in [Shot 1])",                   // retention 修复
	} {
		if !strings.Contains(out, want) {
			t.Errorf("对齐结果应含 %q", want)
		}
	}
	if strings.Contains(out, "[中文]") || strings.Contains(out, "the voice of 姜缺") {
		t.Errorf("旧形态应被规范化")
	}
	if again := manjuAlignShotPrompt(out, alignFixtureContract(), 5, "群演·老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。"); again != out {
		t.Errorf("manjuAlignShotPrompt 应幂等(两次对齐结果不一致=指纹恒失效)")
	}
}

// stringCut 取子串片段(失败信息截断用)
func stringCut(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return "(未找到 " + start + ")"
	}
	rest := s[i:]
	if j := strings.Index(rest, end); j > 0 {
		return rest[:j]
	}
	if len(rest) > 120 {
		return rest[:120]
	}
	return rest
}

// 2026-08-30 ver14:全局说话人注册表——有音色绑定角色跨镜 (Sx) 稳定(官方契约
// "A speaker keeps the same ID across shots")。fixture 老赵台词标 (S6)、姜缺 (S1);
// 注册表按全集首次发声序分配(老赵 S1、姜缺 S2)→ 画面段应重写为全局号,
// 而非镜内重排(老赵 S1、姜缺 S2 恰好相同,这里注册表故意反序验证注册表优先)。
func TestAlignSpeakerIDsGlobalRegistry(t *testing.T) {
	// 注册表故意与镜内发声序(老赵先说)相反,验证全局号优先于镜内重编
	reg := map[string]string{"老赵": "S2", "姜缺": "S1"}
	c := alignFixtureContract()
	out := alignSpeakerIDsReg(alignFixtureShot1, c, reg, "老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。")
	for _, want := range []string{
		"says with a mumbling mouth (S2)", // 老赵(S6)→ 全局 S2
		"he answers plainly (S1)",         // 姜缺(S1)→ 全局 S1
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("alignSpeakerIDsReg 未按全局注册表重写,缺: %s\n---\n%s", want, stringCut(out, "detailed_description:", "overall_soundscape:"))
		}
	}
	// 幂等:二次调用原始号即全局号,输出不变
	if again := alignSpeakerIDsReg(out, c, reg, "老赵:今儿大比,你磨它干啥。\n姜缺:测完灵,下午还有三十捆柴。"); again != out {
		t.Fatalf("alignSpeakerIDsReg 应幂等(两次对齐结果不一致=指纹恒失效)")
	}
}

// 2026-08-30 ver14:Audio 定义行音色指纹短语注入——<Audio N> 行按角色音色档位
// 编译官方身份短语(年龄段+音色质感),H3 按短语分配声线(年龄感来自短语)。
func TestManjuVoicePhraseFor(t *testing.T) {
	cases := map[string]string{
		"male_elder":    "an old man's voice, low and weathered",
		"female_elder":  "an elderly woman's voice, warm and crackly",
		"child_girl":    "a little girl's voice, high and bright",
		"male_sun":      "a young man's voice, clear and steady",
		"male_mag":      "a middle-aged man's voice, calm and deep",
		"beast_cute":    "a cute, playful creature voice",
		"unknown_key":   "",
	}
	for k, want := range cases {
		got := manjuVoicePhraseFor(k)
		if want == "" {
			if got != "" {
				t.Fatalf("manjuVoicePhraseFor(%s) = %q, want 空", k, got)
			}
			continue
		}
		if !strings.HasPrefix(got, want[:len(want)-1]) && !strings.Contains(got, want) {
			t.Fatalf("manjuVoicePhraseFor(%s) = %q, want 含 %q", k, got, want)
		}
	}
}

// 2026-09-03 行级替换根治换脸(递了三千年 EP01 镜8 实锤):两人物行同写
// <Picture 2>,第一行(甲)需重排到槽 1,第二行(乙)本来就正确(槽 2)。
// 旧实现按 remap 全文替换 → 乙的 2 也被改成 1 → 两角色同指一张定妆照=换脸。
func TestAlignPictureRefsLineLocalNoCrossover(t *testing.T) {
	c := manjuRefContract{
		Chars: []manjuCharSlot{
			{ID: "甲", PicStart: 1, PicEnd: 1},
			{ID: "乙", PicStart: 2, PicEnd: 2},
		},
		SceneName: "柴院", SceneSlot: 3, PicSlots: 3,
	}
	in := `subject_definitions:
<Subject 1> is Jia, the man in <Picture 2>, wearing grey.
<Subject 2> is Yi, the woman in <Picture 2>, wearing red.

summary:
[reference generation] test`
	out := alignPictureRefs(in, c)
	if !strings.Contains(out, "Jia, the man in <Picture 1>") {
		t.Errorf("甲行应重排到槽 1, got: %s", stringCut(out, "<Subject 1>", "\n"))
	}
	if !strings.Contains(out, "Yi, the woman in <Picture 2>") {
		t.Errorf("乙行本就正确应保持槽 2(行级替换根治换脸), got: %s", stringCut(out, "<Subject 2>", "\n"))
	}
	if again := alignPictureRefs(out, c); again != out {
		t.Errorf("alignPictureRefs 应幂等")
	}
}

// 2026-09-03 retention 段保护:retention_analysis 的 [Shot N] 是跨镜引用,
// 不参与镜内切点重编号——detailed 段首段不得被顶成 [Shot 2]。
func TestAlignShotTimecodesRetentionProtected(t *testing.T) {
	in := `subject_definitions:
<Subject 1> is Xiao Man in <Picture 1>.

summary:
[reference generation] test

retention_analysis:
Xiao Man (appears in [Shot 1]) - keep identical face/hair/outfit.

detailed_description:
[Shot 1] The girl hugs the gourd in the rain.`
	out := alignShotTimecodes(in, 5)
	if !strings.Contains(out, "retention_analysis:\nXiao Man (appears in [Shot 1])") {
		t.Errorf("retention 段的 [Shot 1] 引用应原样保留, got: %s", stringCut(out, "retention_analysis:", "\n\n"))
	}
	if !strings.Contains(out, "detailed_description:\n[Shot 1] The girl hugs") {
		t.Errorf("detailed 首段应仍为 [Shot 1](不被 retention 标签吃序号), got: %s", stringCut(out, "detailed_description:", "\n"))
	}
	if again := alignShotTimecodes(out, 5); again != out {
		t.Errorf("alignShotTimecodes 应幂等")
	}
}
