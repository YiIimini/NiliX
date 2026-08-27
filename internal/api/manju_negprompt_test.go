package api

import (
	"strings"
	"testing"
)

// negPrompt 追加语义(2026-08-27 三修):用户配置不得替换内置安全底线——
// 精卫 Q 版袒胸实锤:自定义负面只剩画质词时,禁裸/禁日漫/禁真人全线裸奔。
func TestNegPromptAppendNotReplace(t *testing.T) {
	ctx := &manjuCtx{R: map[string]any{}}
	base := ctx.negPrompt()
	// 内置底线:服装禁裸 + 禁日漫 + 禁真人 三段都必须在
	for _, want := range []string{"nsfw", "bare chest", "cleavage", "japanese anime face", "photorealistic"} {
		if !strings.Contains(base, want) {
			t.Errorf("内置负面缺底线词 %q: %s", want, base)
		}
	}
	// 用户自定义=追加,内置词保留
	ctx2 := &manjuCtx{R: map[string]any{"neg_prompt": "my custom quality words"}}
	got := ctx2.negPrompt()
	if !strings.Contains(got, "my custom quality words") {
		t.Errorf("用户自定义词丢失: %s", got)
	}
	for _, want := range []string{"nsfw", "bare chest", "japanese anime face"} {
		if !strings.Contains(got, want) {
			t.Errorf("用户配置追加后内置底线词 %q 丢失(替换语义回归): %s", want, got)
		}
	}
	if !strings.HasPrefix(got, manjuNegPrompt) {
		t.Errorf("内置底线应在前(高权重位): %s", got)
	}
}

// Q 版女性分支端庄锚(2026-08-27 三修:精卫袒胸)
func TestQPromptFemaleModestAnchor(t *testing.T) {
	m := map[string]any{"gender": "女", "appearance": "圆脸黑发", "image_prompt": "a 24-year-old Chinese office girl with a small yellow flower, wearing a practical office uniform and a canvas shoulder bag, walking briskly"}
	p := manjuQPrompt(m)
	if !strings.Contains(p, "modest high-neckline outfit fully covering the chest") {
		t.Errorf("女性 Q 版缺端庄高领锚: %s", p)
	}
	// 动作残词剥除
	if strings.Contains(p, "walking briskly") {
		t.Errorf("Q 版未剥动作残词 walking briskly: %s", p)
	}
}

// 兽形判定③兜底人形否决(2026-08-27 五修:九尾 Q 版出正常比例动漫人):
// 人形妖怪(image_prompt 写人形人设)不得因「记忆点含尾巴/九尾」误判兽形;
// species 权威与纯兽形提示词照判。
func TestManjuIsBeastHumanFigureVeto(t *testing.T) {
	// 九尾:人形美妆博主人设,记忆点含"九条秃尾巴" → 不判兽(走人形 Q 版分支)
	jiuwei := map[string]any{
		"gender":       "女",
		"appearance":   "记忆点：①银灰渐变长发 ②眼线永远完美 ③独处时露出九条秃尾巴（本体真相）",
		"image_prompt": "a 26-year-old Chinese beauty vlogger, long silver-grey gradient hair, perfect winged eyeliner, wearing a chic casual outfit",
	}
	if manjuIsBeast(jiuwei) {
		t.Error("人形妖怪(九尾,image_prompt=beauty vlogger)被误判兽形——Q 版会走 chibi beast+NOT a human 矛盾分支")
	}
	// 真兽:species 权威不受人形否决影响(即便 image_prompt 偶含人形词)
	beast := map[string]any{"species": "灵宠", "image_prompt": "a small furry fox spirit companion"}
	if !manjuIsBeast(beast) {
		t.Error("species=灵宠 应判兽形")
	}
	// 兜底:无 species、appearance 中文兽形特征 → 判兽(词表为中文,英文兽词不扫)
	furry := map[string]any{"appearance": "通体雪白毛茸茸的小兽，九条尾巴", "image_prompt": "a fluffy white spirit beast"}
	if !manjuIsBeast(furry) {
		t.Error("中文兽形特征 appearance 应由兜底判兽形")
	}
}

// Q 版呆萌构成锚(2026-08-27 五修:切 Krea-2 后呆萌感不足,形态学逐项点名)
func TestQPromptChibiCuteAnchors(t *testing.T) {
	m := map[string]any{"gender": "女", "appearance": "圆脸", "image_prompt": "a 26-year-old Chinese woman, long silver hair"}
	p := manjuQPrompt(m)
	for _, want := range []string{"2-head-tall super-deformed", "takes up half of the total body height", "tiny stubby arms and legs", "small round hands", "small cute mouth"} {
		if !strings.Contains(p, want) {
			t.Errorf("人形 Q 版缺呆萌构成锚 %q: %s", want, p)
		}
	}
}

// 定妆统一锚(2026-08-27 六修):单人+纯白背景+服装严格;背景词剥除;chibi 防日漫
func TestPortraitSoloBgAnchor(t *testing.T) {
	ctx := &manjuCtx{style: "nextgen3d", R: map[string]any{}}
	m := map[string]any{"gender": "男", "appearance": "圆脸", "image_prompt": "a 7-year-old Chinese boy, striped T-shirt, standing on tiptoe staring at four adults, evening alley background, office background"}
	out := ctx.portraitPromptFor(str(m["image_prompt"]), m, true)
	// 背景词剥除
	if strings.Contains(strings.ToLower(out), "alley background") || strings.Contains(strings.ToLower(out), "office background") {
		t.Errorf("背景词未剥除: %s", out)
	}
	// 单人+白底+服装严格锚
	for _, want := range []string{"only this one single character", "plain pure white background", "no additional outerwear"} {
		if !strings.Contains(out, want) {
			t.Errorf("定妆缺锚 %q: %s", want, out)
		}
	}
	// chibi 分支:防日漫 + 白底
	q := ctx.portraitPromptFor("chibi cute style, 2-head-tall, a cute character", m, false)
	if !strings.Contains(q, "NOT a Japanese anime style") {
		t.Errorf("Q 版缺防日漫锚: %s", q)
	}
	if !strings.Contains(q, "plain pure white background") {
		t.Errorf("Q 版缺白底锚: %s", q)
	}
}
