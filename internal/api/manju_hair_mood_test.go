package api

import (
	"strings"
	"testing"
)

// 2026-09-03 修仙界 EP01 四案回归:发色校正被 textured 子串劫持 / 镜15 ghosts 虚影 /
// 配音僵硬(情绪 delivery) / 内心 Q 版表情不符剧情

func moodTestCtx() *manjuCtx {
	ctx := &manjuCtx{}
	ctx.charInfo = map[string]map[string]any{
		"季一星": {
			"id": "季一星",
			"image_prompt": "Front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, a 26-year-old male Chinese young man, subtly anime-stylized semi-realistic character, handsome lean, neat short black textured fringe hair, sharp slender almond eyes, a small scar through the tail of his left eyebrow, wearing a plain grey-blue coarse cloth tunic",
			"voice": "青年男声,清亮带笑意,语速快,吐槽位重音",
		},
	}
	return ctx
}

// 卡发色提取:textured 不得劫持成 red(修仙界实锤:black textured fringe hair 里
// "textuRED" 子串被旧正则当成发色,卡权威=red 与镜内 red 错错一致,跳过替换)
func TestCardHairWordTexturedNotHijacked(t *testing.T) {
	card := moodTestCtx().charInfoFor("季一星")
	if got := manjuCardHairWord(card); got != "black" {
		t.Fatalf("卡发色应提取 black(被 textured 子串 red 劫持为 %q)", got)
	}
	// 长修饰链窗口:色词与 hair 隔 16 字符
	if !reCardHairPhrase.MatchString("neat short black textured fringe hair") {
		t.Fatal("长修饰链发色短语应命中(窗口 24)")
	}
}

// 集成:镜内 red hair 被按卡校正为 black(镜14 真实形态)
func TestFixSubjectHairColorRedToBlack(t *testing.T) {
	ctx := moodTestCtx()
	s := manjuShot{ID: 14, Characters: []string{"季一星"}}
	hp := "subject_definitions:\n<Subject 2> is Ji Yixing, a lean 26-year-old Chinese young man in <Picture 4>, sharp almond eyes, scar through the tail of his left eyebrow, neat red hair, wearing a grey-blue tunic.\n\nsummary:\n[reference generation] test."
	out := ctx.fixSubjectHairColor(hp, s)
	if strings.Contains(out, "red hair") {
		t.Fatalf("镜内 red hair 应被校正为 black hair: %s", out)
	}
	if !strings.Contains(out, "black hair") {
		t.Fatalf("应替换为卡发色 black hair: %s", out)
	}
	// 幂等
	if again := ctx.fixSubjectHairColor(out, s); again != out {
		t.Fatal("发色校正能量应幂等")
	}
}

// 镜15 飘逸:文学动词 ghosts(悄然浮现)→ H3 视觉化为虚影,替换为 lingers
func TestGhostWordReplaced(t *testing.T) {
	in := "A thumb-edge of another hand's smudge ghosts beside it; the page trembles."
	out, _ := manjuSanitizeRenderWords(in)
	if strings.Contains(out, "ghosts") {
		t.Fatalf("ghosts 应被替换: %s", out)
	}
	if !strings.Contains(out, "lingers beside") {
		t.Fatalf("应替换为 lingers beside: %s", out)
	}
}

// 情绪映射:narration 含「爹娘双亡」→ grief
func TestMoodOf(t *testing.T) {
	m := manjuMoodOf("内心·季一星:万象宗,东荒三流宗门。爹娘双亡的孤儿,上个月被人灌了酒,迷迷糊糊按了这枚手印。")
	if m == nil || !strings.Contains(m.delivery, "grief") {
		t.Fatalf("「爹娘双亡」应归 grief(低沉沉重), got %+v", m)
	}
	if m == nil || !strings.Contains(m.face, "sorrowful") {
		t.Fatalf("chibi 表情应为 sorrowful, got %+v", m)
	}
	if manjuMoodOf("") != nil {
		t.Fatal("空文本不应归情绪")
	}
}

// 内心 Q 版表情注入:镜16 形态(chibi 行无表情词→注入;有表情词→不动;幂等)
func TestFixChibiEmotion(t *testing.T) {
	ctx := moodTestCtx()
	s := manjuShot{ID: 16, Narration: "内心·季一星:爹娘双亡的孤儿,被人灌了酒按了手印。"}
	hp := "subject_definitions:\n<Subject 2> is the chibi version of Ji Yixing, a three-head-tall miniature with red hair, the left-eyebrow scar and a tiny grey-blue servant tunic, sitting cross-legged on his shoulder with a fist on its chin.\n"
	out := ctx.fixChibiEmotion(hp, s)
	if !strings.Contains(out, "current mood: a sorrowful drooping look") {
		t.Fatalf("chibi 行应注入悲情表情: %s", out)
	}
	if again := ctx.fixChibiEmotion(out, s); again != out {
		t.Fatal("chibi 表情注入应幂等(mood: 锚)")
	}
	// 已有表情词的行不动(分镜师原文优先)
	hp2 := "subject_definitions:\n<Subject 2> is the chibi version of Ji Yixing with a cheeky canine grin, standing on his shoulder.\n"
	if out2 := ctx.fixChibiEmotion(hp2, s); out2 != hp2 {
		t.Fatalf("已有表情词不得覆盖: %s", out2)
	}
	// 无情绪词 narration 不注入
	s3 := manjuShot{ID: 6, Narration: "内心·季一星:系统面板展开。"}
	if out3 := ctx.fixChibiEmotion(hp, s3); out3 != hp {
		t.Fatalf("无情绪词不应注入: %s", out3)
	}
}


// 2026-09-03 崩溃回归(渲染管线 panic: index out of range [0]):行内只有发饰匹配
// (白玉发簪)或无 hair 匹配时 manjuHairSubmatch 返回 nil,旧代码取 [0] 崩
func TestFixSubjectHairColorAccessoryOnlyNoPanic(t *testing.T) {
	ctx := moodTestCtx()
	s := manjuShot{ID: 7, Characters: []string{"季一星"}}
	// 行内只有发饰词 + 无 hair 匹配的行,不得 panic
	hp := "subject_definitions:\n<Subject 1> is Ji Yixing, wearing a white jade hair ornament and a silver hair pin, smiling.\n<Subject 2> is a ledger page.\n\nsummary:\ntest."
	out := ctx.fixSubjectHairColor(hp, s)
	if !strings.Contains(out, "white jade hair ornament") {
		t.Fatalf("发饰词不得被当发色改写: %s", out)
	}
}
