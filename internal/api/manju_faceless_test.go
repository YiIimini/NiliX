package api

import (
	"strings"
	"testing"
)

// TestFacelessClassification 无脸剪影类角色分类(2026-09-01 阿影 side 双体实锤):
// ①影灵/影子类 species 非人但走剪影锚而非兽形锚;
// ②isNonPhysical 不被 "wisps of darkness" 误判(墨千秋实锤);
// ③剪影 Q 版不走兽形 fur 分支。
func TestFacelessClassification(t *testing.T) {
	aying := map[string]any{
		"species":      "影灵",
		"image_prompt": "a human-shaped silhouette made of living black mist and shadow, no facial features except two faint glowing blue dots for eyes",
		"appearance":   "影灵·影子精灵",
	}
	if !manjuFacelessChar(aying) {
		t.Fatal("阿影(影灵·人形剪影)应判无脸剪影类")
	}
	p := manjuViewPromptBuild("portrait", "side", aying)
	if !strings.Contains(p, "SILHOUETTE PROFILE") {
		t.Fatalf("阿影 side 应走剪影锚, got: %s", p[:80])
	}
	if strings.Contains(p, "four paws") || strings.Contains(p, "beast") {
		t.Fatalf("阿影 side 不应走兽形锚(四足兽), got: %s", p[:120])
	}
	q := manjuQPrompt(aying)
	if strings.Contains(q, "FUR COLOR LOCK") || strings.Contains(q, "same beast creature") {
		t.Fatalf("阿影 Q版不应走兽形 fur 分支, got: %s", q[:200])
	}
	if !strings.Contains(q, "shadow blob") {
		t.Fatalf("阿影 Q版应走剪影团子分支, got: %s", q[:150])
	}
	// 墨千秋:wisps of darkness 不应误判为非实体光精灵
	mo := map[string]any{
		"species":      "人",
		"image_prompt": "a 21-year-old male Chinese dark-attribute genius, wearing a black academy uniform with the collar popped, wisps of darkness curling at his feet",
		"appearance":   "人类·男,暗影系·天才榜第二",
		"gender":       "男",
		"age":          "21岁",
	}
	if manjuIsNonPhysical(mo) {
		t.Fatal("墨千秋(人类·暗影系)不应被 wisps of darkness 误判为非实体光精灵")
	}
	if manjuIsBeast(mo) {
		t.Fatal("墨千秋(人类)不应判兽形")
	}
	q2 := manjuQPrompt(mo)
	if strings.Contains(q2, "holographic data spirit") {
		t.Fatalf("墨千秋 Q版不应走光精灵分支, got: %s", q2[:200])
	}
	if !strings.Contains(q2, "black academy uniform") && !strings.Contains(q2, "OUTFIT LOCK") {
		t.Fatalf("墨千秋 Q版应保留人形着装锁, got: %s", q2[:200])
	}
	// 单角色硬锚覆盖所有人形 Q 版
	if !strings.Contains(q2, "one single chibi character only") {
		t.Fatalf("墨千秋 Q版缺单角色硬锚: %s", q2[:300])
	}
	// 无 palm-sized 残留
	if strings.Contains(q2, "palm-sized") || strings.Contains(q2, "palm-size") {
		t.Fatal("Q版不应再含 palm-sized(一大一小双人诱因)")
	}
}

// TestQPromptSingleFigure 人形 Q 版单角色硬锚(沈照/夜枭/沈伯/王胖子一大一小实锤)
func TestQPromptSingleFigure(t *testing.T) {
	for _, c := range []map[string]any{
		{"id": "沈照", "gender": "男", "age": "19岁", "species": "人", "q_form": "A chibi-style 3-head-tall cute version of a 19-year-old male student, short platinum-white hair, bright golden eyes", "image_prompt": "a 19-year-old male student, short platinum-white hair, wearing a dark academy uniform jacket", "appearance": "人类·男"},
		{"id": "王胖子", "gender": "男", "age": "19岁", "species": "人", "q_form": "A chibi 3-head-tall version of a 19-year-old Chinese male student, chubby round face", "image_prompt": "a 19-year-old Chinese male student, chubby round face, wearing an academy uniform", "appearance": "人类·男"},
	} {
		q := manjuQPrompt(c)
		if !strings.Contains(q, "one single chibi character only") {
			t.Fatalf("%s Q版缺单角色硬锚", c["id"])
		}
		if strings.Contains(q, "palm-sized") {
			t.Fatalf("%s Q版含 palm-sized", c["id"])
		}
	}
}
