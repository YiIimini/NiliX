package manju

import (
	"strings"
	"testing"
)

// 下半身着装锚+未成年人护栏(2026-08-27 用户反馈:小男孩全身图下半身裸露没穿裤子)
func TestManjuViewPromptBuildLowerBodyAnchor(t *testing.T) {
	boy := map[string]any{"gender": "男", "age": "7岁", "image_prompt": "a 7-year-old Chinese boy, striped T-shirt"}
	p := manjuViewPromptBuild(str(boy["image_prompt"]), "full", boy)
	for _, want := range []string{"FULL BODY view", "full lower-body garment", "never bare legs", "never missing trousers or skirt", manjuIdentityAnchor} {
		if !strings.Contains(p, want) {
			t.Errorf("full 视图缺锚 %q: %s", want, p)
		}
	}
	if !strings.Contains(p, "CHILD DRESS CODE") {
		t.Errorf("儿童角色 full 视图缺未成年人护栏: %s", p)
	}

	side := manjuViewPromptBuild(str(boy["image_prompt"]), "side", boy)
	if !strings.Contains(side, "never bare legs") || !strings.Contains(side, "CHILD DRESS CODE") {
		t.Errorf("side 视图缺下半身锚/未成年人护栏: %s", side)
	}

	// 成年人:下半身锚要有,CHILD DRESS CODE 不能有(child 措辞不污染成年人提示词)
	adult := map[string]any{"gender": "男", "age": "中年", "image_prompt": "a middle-aged Chinese man, dark robe"}
	pa := manjuViewPromptBuild(str(adult["image_prompt"]), "full", adult)
	if !strings.Contains(pa, "full lower-body garment") {
		t.Errorf("成年人 full 视图缺下半身锚: %s", pa)
	}
	if strings.Contains(pa, "CHILD DRESS CODE") {
		t.Errorf("成年人不应加未成年人护栏: %s", pa)
	}

	// 兽类:护栏不加(兽形无人类着装语义),但视图锚仍在
	beast := map[string]any{"gender": "雄性", "species": "灵宠", "image_prompt": "a small spirit beast, white fur"}
	pb := manjuViewPromptBuild(str(beast["image_prompt"]), "full", beast)
	if strings.Contains(pb, "CHILD DRESS CODE") {
		t.Errorf("兽类不应加未成年人护栏: %s", pb)
	}
}

// 未成年人关键词覆盖:中文称谓/英文 boy·girl·child/N 岁措辞都要命中
func TestManjuMinorGuardDetection(t *testing.T) {
	cases := []map[string]any{
		{"gender": "男", "age": "少年"},
		{"gender": "女", "age": "少女"},
		{"gender": "男", "age": "孩童"},
		{"gender": "男", "image_prompt": "a boy, full body"},
		{"gender": "女", "appearance": "小女孩模样,圆脸"},
		{"gender": "男", "image_prompt": "a 7-year-old Chinese boy, striped T-shirt"},
		{"gender": "女", "views": map[string]any{"q": "a cute girl chibi"}},
	}
	for i, m := range cases {
		if manjuMinorGuard(m) == "" {
			t.Errorf("case %d 未命中未成年人护栏: %v", i, m)
		}
	}
	adult := []map[string]any{
		{"gender": "女", "age": "26岁", "image_prompt": "a 26-year-old Chinese woman, long silver hair"},
		{"gender": "男", "age": "中年", "image_prompt": "an old man, full body"},
		{"gender": "女", "age": "青年"},
	}
	for i, m := range adult {
		if manjuMinorGuard(m) != "" {
			t.Errorf("成年人 case %d 误加未成年人护栏: %v", i, m)
		}
	}
}

// 负面词表:腰部以下裸露词组必须在内(此前只有躯干词)
func TestNegPromptLowerBodyTerms(t *testing.T) {
	ctx := &manjuCtx{R: map[string]any{}}
	neg := ctx.negPrompt()
	for _, want := range []string{"naked from the waist down", "no pants", "missing trousers", "bottomless", "exposed genitals"} {
		if !strings.Contains(neg, want) {
			t.Errorf("负面词缺下半身词 %q: %s", want, neg)
		}
	}
	// 已配置 render.neg_prompt 只增不减
	ctx2 := &manjuCtx{R: map[string]any{"neg_prompt": "extra term"}}
	if !strings.Contains(ctx2.negPrompt(), "extra term") || !strings.Contains(ctx2.negPrompt(), "no pants") {
		t.Errorf("自定义负面词叠加异常: %s", ctx2.negPrompt())
	}
}

// Q 版:儿童角色必须带未成年人护栏(手办素体光腿先验)
func TestManjuQPromptMinorGuard(t *testing.T) {
	boy := map[string]any{"gender": "男", "age": "少年", "appearance": "剑眉星目", "image_prompt": "a boy, full body"}
	q := manjuQPrompt(boy)
	if !strings.Contains(q, "CHILD DRESS CODE") {
		t.Errorf("儿童 Q 版缺未成年人护栏: %s", q)
	}
	man := map[string]any{"gender": "男", "age": "老年", "appearance": "花白络腮胡", "image_prompt": "an old man, full body"}
	if q2 := manjuQPrompt(man); strings.Contains(q2, "CHILD DRESS CODE") {
		t.Errorf("成年人 Q 版不应有未成年人护栏: %s", q2)
	}
}

// 视图代数:下半身着装锚上线必须 bump(存量全身图自愈重出)
func TestManjuViewGenBumped(t *testing.T) {
	if manjuViewGen < 7 {
		t.Errorf("下半身着装锚上线后 views_gen 必须 ≥7,当前 %d", manjuViewGen)
	}
}
