package manju
import (
	"strings"
	"testing"
)

func TestOffscreenVoiceBindings(t *testing.T) {
	// 镜4 句式:画外·路人甲喊话(gruff and impatient 是不匹配词?脚本里是 eager young voice)
	hp4 := "subject_definitions:\n<Subject 1> is A-Kai.\n\nsummary:\ntest\n\ndetailed_description:\n" +
		"A distant player's voice (S6) shouts from the village direction, gruff and impatient, in an off-screen voiceover: <d>阿凯!走了!</d> " +
		"The narrator says in an off-screen voiceover: <d>旁白不换音色。</d>"
	descs := manjuOffscreenDescs(hp4)
	// 旁白跳过,只留画外路人
	found := false
	for _, d := range descs {
		if strings.Contains(d, "distant player") {
			found = true
		}
		if strings.Contains(strings.ToLower(d), "narrator") {
			t.Error("旁白不应进入差异化:", d)
		}
	}
	if !found {
		t.Error("未解析到画外路人描述:", descs)
	}
	// 语气词 gruff → male_deep(与主角 male_sun 错开)
	key := manjuOffscreenKey(descs[0])
	if key != "male_deep" {
		t.Error("gruff 男声应映射 male_deep,实际:", key)
	}
	// 注入 + 编号接续(登场 1 个 → 画外从 2 开始)
	obs := []offscreenVoice{{Desc: descs[0], Key: key}}
	out := injectOffscreenVoiceBindings(hp4, obs, 1)
	if !strings.Contains(out, "<Audio 2> is the voice-timbre reference for the off-screen voice") {
		t.Error("画外 <Audio 2> 定义未注入")
	}
	// 幂等:再次注入同描述不重复
	out2 := injectOffscreenVoiceBindings(out, []offscreenVoice{{Desc: descs[0], Key: key}}, 1)
	if strings.Count(out2, "<Audio 2>") != 1 {
		t.Error("画外绑定不幂等")
	}
	// 女性画外声线 → 女性音色
	if k := manjuOffscreenKey("A hushed middle-aged woman's voice among the onlookers, gossiping"); k != "female_mature" {
		t.Error("中年女声应 female_mature:", k)
	}
	if k := manjuOffscreenKey("An old man's croaking voice"); k != "male_elder" {
		t.Error("老年男声应 male_elder:", k)
	}
	// 默认男声(male_mag)与主角 male_sun 错开
	if k := manjuOffscreenKey("A player's voice"); k != "male_mag" {
		t.Error("默认男声应 male_mag:", k)
	}
}

// TestOffscreenDescsNoDialogue 2026-09-02 配音重复根治(王牌三岁半 EP01 镜15 实锤):
// 同一长句内两句画外音连写时,第二锚点向前回溯会把前一句的 <d> 台词块卷进
// "声线描述" → 注入 Audio 定义行 → 同一句台词在 h3 出现两处 → H3 念两遍。
func TestOffscreenDescsNoDialogue(t *testing.T) {
	hp := "detailed_description:\n[Shot 15] At 01:08.000 the clip opens. " +
		"Two voices cut through the roar: a middle-aged man's voice off-screen in the stands (S25), jeering and loud, says in an off-screen voiceover: <d>联邦史上最低！</d> " +
		"and a woman's voice off-screen (S26), spiteful and shrill, adds in an off-screen voiceover: <d>绝缘体！上辈子造了孽！</d> while no on-screen character's lips move."
	for _, d := range manjuOffscreenDescs(hp) {
		if strings.Contains(d, "<d") || strings.Contains(d, "联邦史上最低") || strings.Contains(d, "绝缘体") {
			t.Errorf("声线描述混入台词: %q", d)
		}
	}
	obs := []offscreenVoice{}
	descs := manjuOffscreenDescs(hp)
	seen := map[string]bool{}
	for _, d := range descs {
		k := manjuOffscreenKey(d)
		if seen[k] {
			continue
		}
		seen[k] = true
		obs = append(obs, offscreenVoice{Desc: d, Key: k})
	}
	out := injectOffscreenVoiceBindings(hp, obs, 2)
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "<Audio ") && strings.Contains(ln, "<d") {
			t.Errorf("Audio 定义行内嵌台词: %s", ln)
		}
	}
}
