package api

import (
	"strings"
	"testing"
)

// manjuResolveCharID 角色名变体归一(2026-09-01 主次混乱/无参考图根因):
// 分镜 characters 声明/说话人用简称,素材卡是完整名——精确匹配不上=无参考图。
// 实测事故:杳杳×108 对不上「涂山杳杳」、主持人×6 对不上「天才榜主持人」。
func TestManjuResolveCharID(t *testing.T) {
	ids := []string{"涂山杳杳", "沈照", "天才榜主持人", "孟小汤", "陈默", "老周", "周老"}
	cases := []struct {
		name string
		want string
	}{
		{"杳杳", "涂山杳杳"},        // 卡 ID 以候选结尾(简称→全名)
		{"涂山杳杳", "涂山杳杳"},      // 精确
		{"主持人", "天才榜主持人"},     // 卡 ID 包含候选
		{"孟小汤", "孟小汤"},        // 精确
		{"小汤", "孟小汤"},         // 卡 ID 以候选结尾
		{"挑战者程野", ""},         // 不在卡集
		{"旁白", ""},             // 非角色词排除
		{"画外·路人甲", ""},        // 画外前缀排除
		{"内心·阿影", ""},          // 内心前缀排除
		{"沈照(剪影)", "沈照"},         // 括号标注剥除后精确命中
		{"老周", "老周"},           // 精确(多卡同尾取最短前先精确)
	}
	for _, c := range cases {
		got := manjuResolveCharID(c.name, ids)
		if got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
	// 多卡同尾取最短:「老周」vs「周老」都不包含对方;「小汤」唯一
	if got := manjuResolveCharID("老周", []string{"周老", "老周"}); got != "老周" {
		t.Errorf("精确优先: want 老周, got %q", got)
	}
	if got := manjuResolveCharID("汤", []string{"孟小汤"}); got != "" {
		t.Errorf("单字候选不参与变体: want empty, got %q", got)
	}
}

// 2026-09-01 崩溃回归:空 dialogue 镜 h3 画面段含 <Subject N> (Sx)(含静默描述
// 「remains silent」)时,alignAudioDefsReg 曾对 nil map 赋值 panic
// 「assignment to entry in nil map」(轮回欠费九世 EP01 渲染崩溃)。
func TestAlignAudioDefsEmptyDialogueNoPanic(t *testing.T) {
	c := manjuRefContract{
		Chars: []manjuCharSlot{{ID: "金珠"}},
	}
	hp := "subject_definitions:\n<Subject 1> is Jin Zhu in <Picture 1>, a young female accountant.\n\n" +
		"detailed_description:\nThe camera holds a close-up. <Subject 1> (S1) remains silent, her lips pressed together.\n\n" +
		"overall_soundscape: faint heartbeat.\nnon_diegetic_music: low strings."
	// dialogue 为空(静默镜)——修复前此处 panic
	out := manjuAlignShotPromptReg(hp, c, 5, "", map[string]string{})
	if out == "" {
		t.Fatal("空输出")
	}
	// 说话引用句(dialogue 空但 h3 有 <d> 台词)同样不崩
	hp2 := "detailed_description:\nThe scene continues. <Subject 1> (S2) says: <d>[Chinese] 吞！</d>\n\noverall_soundscape: x.\nnon_diegetic_music: y."
	if out2 := manjuAlignShotPromptReg(hp2, c, 5, "", map[string]string{"金珠": "S2"}); out2 == "" {
		t.Fatal("空输出2")
	}
}

// 2026-09-01 知识库五步导演法整合:身份一致性纪律注入——
// 有人物参考图(<Picture N>)注入 IDENTITY CONSISTENCY,无参考图不注入;幂等(重跑不叠加)。
func TestConsistencyGuardInject(t *testing.T) {
	hpPic := "subject_definitions:\n<Subject 1> is Jin Zhu in <Picture 1>.\n\ndetailed_description:\nThe scene.\n"
	out := manjuFinalizePromptPure(hpPic, true, 4)
	if !strings.Contains(out, "IDENTITY CONSISTENCY") {
		t.Fatal("有 Picture 引用应注入 IDENTITY CONSISTENCY")
	}
	if strings.Count(out, "IDENTITY CONSISTENCY") != 1 {
		t.Fatal("幂等:只注入一次")
	}
	hpNoPic := "subject_definitions:\n<Subject 1> is Jin Zhu.\n\ndetailed_description:\nThe scene.\n"
	out2 := manjuFinalizePromptPure(hpNoPic, true, 0)
	if strings.Contains(out2, "IDENTITY CONSISTENCY") {
		t.Fatal("无 Picture 引用不应注入 IDENTITY CONSISTENCY")
	}
}

// 2026-09-01 小月案回归:image_prompt「tiny palm-sized creature with soft fur」species
// 缺失时须判兽形(creature/fur 词表扩充前判人形 → 视图按人画,正面却按兽画)。
func TestManjuIsBeastCreatureWords(t *testing.T) {
	m := map[string]any{
		"id": "小月", "role": "群演", "species": "",
		"image_prompt": "a tiny palm-sized creature with a white crescent moon mark on its head, cute, soft fur, big eyes, pure white background",
	}
	if !manjuIsBeast(m) {
		t.Fatal("creature/fur 卡应判兽形")
	}
	// 人形否决不回归:26 岁美妆博主(九尾狐精人设)仍判人形
	m2 := map[string]any{
		"id": "九尾", "role": "正角", "species": "",
		"image_prompt": "a 26-year-old woman with long silver hair and fox ears, beauty blogger",
	}
	if manjuIsBeast(m2) {
		t.Fatal("含 N-year-old woman 的人形妖怪不应判兽形")
	}
}

// 2026-09-01 禁烟酒:正向剥离函数把 cigarette/wine 等词移除
func TestManjuBannedItemStrip(t *testing.T) {
	in := "a man with a cigarette in his mouth, holding a gourd wine flask, wearing a robe"
	out := manjuBannedItemStrip(in)
	for _, w := range []string{"cigarette", "wine"} {
		if strings.Contains(out, w) {
			t.Fatalf("应剥离 %s: %q", w, out)
		}
	}
	if !strings.Contains(out, "gourd flask") {
		t.Fatalf("gourd flask 应保留: %q", out)
	}
}

// 2026-09-01 配音不随角色/重复根治:Audio 定义短语校正(错配修正/幂等/变体差异化)
func TestInjectAudioTimbrePhrasesFix(t *testing.T) {
	ctx := &manjuCtx{R: map[string]any{}}
	// 构造角色卡:金珠=女主(年轻女),石敢当=男主(青年男)
	ctx.voiceAssign = map[string]string{"金珠": "female_warm", "石敢当": "male_sun_2", "老周": "male_elder"}
	c := manjuRefContract{
		Chars: []manjuCharSlot{{ID: "金珠"}, {ID: "石敢当"}, {ID: "老周"}},
	}
	// ① 错配短语(LLM 写的「middle-aged man with a deep rough voice」)→ 校正为角色标准
	hp := "subject_definitions:\n<Audio 1> is the voice-timbre reference for <Subject 1> (S1), with a middle-aged man with a deep rough voice, containing a spoken voiceover.\n\n<Audio 2> is the voice-timbre reference for <Subject 2> (S2), with a young man with a clear steady voice, containing a spoken voiceover.\n\n<Audio 3> is the voice-timbre reference for <Subject 3> (S3), containing a spoken voiceover.\n\ndetailed_description:\nThe scene.\n"
	out := ctx.injectAudioTimbrePhrases(hp, c)
	if !strings.Contains(out, "a young woman's voice, warm and gentle") {
		t.Fatalf("金珠错配短语未校正: %s", out[:200])
	}
	if !strings.Contains(out, "a young man's voice, slightly husky and low") {
		t.Fatalf("石敢当变体短语未生效: %s", out[:250])
	}
	if !strings.Contains(out, "an old man's voice, low and weathered") {
		t.Fatalf("老周裸行未注入: %s", out[:300])
	}
	// ② 幂等:重跑不叠加不漂移
	out2 := ctx.injectAudioTimbrePhrases(out, c)
	if out != out2 {
		t.Fatalf("非幂等:\n%s\n---\n%s", out, out2)
	}
	// ③ 变体差异化:同档两角色短语互异
	ctx2 := &manjuCtx{R: map[string]any{}}
	ctx2.voiceAssign = map[string]string{"甲": "male_sun", "乙": "male_sun_2"}
	if manjuVoicePhraseFor("male_sun") == manjuVoicePhraseFor("male_sun_2") {
		t.Fatal("同档变体短语必须互异")
	}
}
