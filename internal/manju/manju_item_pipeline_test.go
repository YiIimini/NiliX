package manju

// 物品管线架构测试(2026-08-29 阿碧「女人脸+花盆头」实锤治理):
// 物种路由 → 三条独立封装管线(人类/兽形/物品),物品管线与人形链零共享措辞。
// 断言:物品角色在所有形象生成路径(主图/视图/视角锚/负面)零人形词;
// 人形角色不回归(正面人脸铁律保留);Q 版不受物品禁人影响(允许拟人)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 人形诱导词清单:物品管线的任何输出(正向 prompt)命中即污染
var itemHumanWords = []string{
	"front-facing portrait", "symmetrical frontal face", "both eyes evenly visible",
	"standing pose", "standing full figure", "FULL BODY view", "trousers", "skirt",
	"wearing exactly the outfit", "wearing complete clothing", "identical hair",
	"hairstyle", "beard", "facial features", "round face", "sparkling eyes",
	"big glossy eyes", "portrait of ", "solo portrait, only this one single character",
}

func itemTestCard() map[string]any {
	return map[string]any{
		"id":      "阿碧",
		"species": "灵植·紫藤精",
		"gender":  "",
		"age":     "",
		"appearance": "detailed leaf texture, no human body, no human face, no human clothes",
		"image_prompt": "the pothos plant itself: a potted pothos vine with glossy heart-shaped green leaves cascading down a small ceramic pot, " +
			"one single leaf with a thin golden vein hidden among the lower leaves, healthy vibrant green color",
		"second_form": "an ancient three-hundred-year-old wisteria tree spirit in full bloom, gnarled twisting trunk like a coiled dragon, cascading waterfall of purple wisteria flowers",
	}
}

func itemTestCtx(t *testing.T) *manjuCtx {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	if err := os.MkdirAll(analysis, 0755); err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{"characters": []any{itemTestCard()}, "shots": []any{}}
	b, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	return &manjuCtx{analysisDir: analysis, episode: "EP01", style: "real", project: "t", R: map[string]any{}}
}

// ① 主图/定妆路径:portraitPromptFor(物品卡, frontFace=true)——正面人脸锚
// 曾对所有角色强拼,是阿碧拟人的头号污染源;物品必须零人形收尾
func TestItemPortraitPipelineZeroHumanWords(t *testing.T) {
	ctx := itemTestCtx(t)
	m := itemTestCard()
	p := ctx.portraitPromptFor(str(m["image_prompt"]), m, true)
	for _, w := range itemHumanWords {
		if strings.Contains(p, w) {
			t.Errorf("物品主图 prompt 含人形词 %q:\n%s", w, p)
		}
	}
	for _, want := range []string{"potted pothos vine", "NOT a person", "no human face", "only this one single object"} {
		if !strings.Contains(p, want) {
			t.Errorf("物品主图 prompt 缺 %q:\n%s", want, p)
		}
	}
}

// ② 真身形态(form2)同管线:second_form 也走 portraitWF→portraitPromptFor
func TestItemTrueFormZeroHumanWords(t *testing.T) {
	ctx := itemTestCtx(t)
	m := itemTestCard()
	p := ctx.portraitPromptFor(str(m["second_form"]), m, false)
	for _, w := range itemHumanWords {
		if strings.Contains(p, w) {
			t.Errorf("物品真身 prompt 含人形词 %q:\n%s", w, p)
		}
	}
	if !strings.Contains(p, "wisteria") {
		t.Errorf("物品真身 prompt 丢失本体词:\n%s", p)
	}
}

// ③ 视图路径:charViewPromptFor(基于 plan 角色卡)的 full/side/detail 视图后缀
func TestItemViewSuffixRouting(t *testing.T) {
	ctx := itemTestCtx(t)
	for _, view := range []string{"full", "side", "detail"} {
		p := charViewPromptFor(ctx, "阿碧", view)
		for _, w := range []string{"standing pose", "complete outfit", "hairstyle silhouette", "full body, head to toe"} {
			if strings.Contains(p, w) {
				t.Errorf("物品 %s 视图 prompt 含人形词 %q:\n%s", view, w, p)
			}
		}
		if !strings.Contains(p, "no person") {
			t.Errorf("物品 %s 视图 prompt 缺禁人词:\n%s", view, p)
		}
		if !strings.Contains(p, "potted pothos") {
			t.Errorf("物品 %s 视图 prompt 丢失本体词:\n%s", view, p)
		}
	}
}

// ④ 视角硬锚:manjuViewPromptBuild 物品分支(人形 FULL BODY/trousers 锚曾直拼物品视图)
func TestItemViewAnchorRouting(t *testing.T) {
	m := itemTestCard()
	for _, view := range []string{"full", "side", "detail"} {
		p := manjuViewPromptBuild("the pothos plant itself with glossy heart-shaped green leaves", view, m)
		for _, w := range []string{"standing full figure", "trousers", "identical hair color", "beard", "facial features"} {
			if strings.Contains(p, w) {
				t.Errorf("物品 %s 视角锚含人形词 %q:\n%s", view, w, p)
			}
		}
		if !strings.Contains(p, "same object as the reference image") {
			t.Errorf("物品 %s 视角锚缺物品身份锚:\n%s", view, p)
		}
	}
}

// ⑤ 负面通道:物品负面硬禁人形(含「叶子拼的脸」植物拟人形态点名)
func TestItemNegativeGuard(t *testing.T) {
	ctx := itemTestCtx(t)
	neg := ctx.charNegPrompt(itemTestCard())
	for _, w := range []string{"human, person", "human face", "face made of leaves", "wearing clothes"} {
		if !strings.Contains(neg, w) {
			t.Errorf("物品负面缺 %q:\n%s", w, neg)
		}
	}
	// 人形/兽形不加物品负面
	humanNeg := ctx.charNegPrompt(map[string]any{"id": "苏苗苗", "species": "人", "gender": "女"})
	if strings.Contains(humanNeg, "face made of leaves") {
		t.Error("人形角色不应追加物品禁人负面")
	}
}

// ⑥ Q 版不受影响:物品 Q 版(chibi)允许拟人——用户规则「Q版形象不影响」
func TestItemQVersionStillChibi(t *testing.T) {
	ctx := itemTestCtx(t)
	m := itemTestCard()
	q := ctx.portraitPromptFor(manjuQPrompt(m), m, false)
	if !strings.Contains(q, "chibi") {
		t.Errorf("物品 Q 版应保留 chibi 形态词:\n%s", q)
	}
}

// ⑦ 人形角色不回归:正面人脸铁律(人物角色提示词必须正面人脸)保留
func TestHumanPortraitKeepsFrontFace(t *testing.T) {
	ctx := itemTestCtx(t)
	m := map[string]any{
		"id": "苏苗苗", "species": "人", "gender": "女", "age": "23岁",
		"appearance": "圆脸杏眼,眼下小痣",
		"image_prompt": "a 23-year-old female Chinese horticulture worker, round face with gentle almond eyes, black hair in a low ponytail",
	}
	p := ctx.portraitPromptFor(str(m["image_prompt"]), m, true)
	if !strings.Contains(p, "front-facing portrait") {
		t.Errorf("人形主图应保留正面人脸锚:\n%s", p)
	}
}

// ⑧ manjuItemStrip:剥素材/LLM 卡残留的拟人脸诱导词
func TestItemStripFaceWords(t *testing.T) {
	in := "a cute rounded potted pothos sprout with a round face made of fresh green leaves, big round sparkling eyes, no human body"
	out := manjuItemStrip(in)
	for _, w := range []string{"round face", "sparkling eyes"} {
		if strings.Contains(out, w) {
			t.Errorf("物品 strip 后仍含拟人脸词 %q: %s", w, out)
		}
	}
	if !strings.Contains(out, "potted pothos") {
		t.Errorf("物品 strip 误伤本体词: %s", out)
	}
}

// ⑨ 参考图挂载与提示词清单同源(2026-08-29 审计 P0 收敛):
// charRefNames(实际挂载)与 shotRefViews(Picture 清单)必须经 shotViewRelsFor
// 同一数据源——此前真身镜/Q版镜切换只存在于提示词侧,双形态功能性失效
func TestShotRefSync(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{assetsDir: dir, comfyInput: filepath.Join(dir, "_in"), project: "t", R: map[string]any{}}
	if err := os.MkdirAll(ctx.comfyInput, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "characters"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"阿碧.png", "阿碧_face.png", "阿碧_full.png", "阿碧_detail.png", "阿碧_q.png", "阿碧_form2.png"} {
		if err := os.WriteFile(filepath.Join(dir, "characters", f), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// 内心戏镜:清单 = [front, q],挂载数量一致
	sInner := manjuShot{ID: 1, Characters: []string{"阿碧"}, Narration: "内心·它想"}
	tags := ctx.shotRefViews(sInner)
	if len(tags) != 2 || tags[0] != "阿碧(front)" || tags[1] != "阿碧(q)" {
		t.Fatalf("内心戏镜清单应为 [front,q]: %v", tags)
	}
	if refs := ctx.charRefNames(sInner); len(refs) != len(tags) {
		t.Fatalf("内心戏镜挂载(%d)与清单(%d)不同源: %v vs %v", len(refs), len(tags), refs, tags)
	}
	// 真身镜:清单 = [form2],挂载一致(收敛前挂载走裸 charViewRels=3 视图,错位)
	sForm2 := manjuShot{ID: 2, Characters: []string{"阿碧"}, Action: "真身·阿碧显现"}
	tags2 := ctx.shotRefViews(sForm2)
	if len(tags2) != 1 || tags2[0] != "阿碧(form2)" {
		t.Fatalf("真身镜清单应为 [form2]: %v", tags2)
	}
	if refs2 := ctx.charRefNames(sForm2); len(refs2) != len(tags2) {
		t.Fatalf("真身镜挂载(%d)与清单(%d)不同源: %v vs %v", len(refs2), len(tags2), refs2, tags2)
	}
	// 普通镜:单角色预算 [front, full, detail]
	sNorm := manjuShot{ID: 3, Characters: []string{"阿碧"}}
	tags3 := ctx.shotRefViews(sNorm)
	if len(tags3) != 3 || tags3[2] != "阿碧(detail)" {
		t.Fatalf("普通镜清单应为 [front,full,detail]: %v", tags3)
	}
}
