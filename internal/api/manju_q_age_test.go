package api

// manju_q_age_test.go Q 版年龄锚·三物种分流单测(2026-08-30 用户需求:
// 老年人生成的都是年轻 Q 版 → 年龄分档锚;且人/兽/物分档——人形用皱纹措辞,
// 兽形用口鼻灰白兽龄措辞,物品无年龄语义)。

import (
	"strings"
	"testing"
)

func qAgeChar(age, gender string, extra map[string]any) map[string]any {
	m := map[string]any{"id": "测试角色", "gender": gender, "age": age, "species": "人",
		"appearance": "普通面容", "image_prompt": "a man"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestManjuAgeBand(t *testing.T) {
	cases := map[string]string{
		"老年":    "elderly",
		"老者":    "elderly",
		"60岁":   "elderly",
		"花甲之年":  "elderly",
		"中年":    "middle",
		"45岁":   "middle",
		"少年":    "teen",
		"16岁":   "teen",
		"儿童":    "child",
		"8岁":    "child",
		"青年":    "adult",
		"二十六岁":  "adult", // 中文数字不误伤
	}
	for age, want := range cases {
		m := qAgeChar(age, "男", nil)
		m["appearance"] = "普通面容"
		m["image_prompt"] = "a man"
		if got := manjuAgeBand(m); got != want {
			t.Errorf("age=%q 应分档 %s, got %s", age, want, got)
		}
	}
}

func TestQAgeAnchorElderlyHuman(t *testing.T) {
	m := qAgeChar("老者", "男", map[string]any{
		"appearance": "gray beard and wrinkled face", "image_prompt": "an old man with a long beard"})
	anchor := manjuQAgeAnchor(m)
	for _, want := range []string{"ELDERLY", "wrinkles", "NOT a youthful face", "grandpa"} {
		if !strings.Contains(anchor, want) {
			t.Errorf("老年男锚应含 %q, got: %s", want, anchor)
		}
	}
	// 老年男 Q 版面部措辞不得再是 young man(用户实锤:老年人生成年轻脸的直接根因)
	if cls := manjuQMaleFaceCls(m); !strings.Contains(cls, "elderly") || strings.Contains(cls, "young") {
		t.Errorf("老年男面部措辞应为 elderly aged, got: %s", cls)
	}
	// 全 prompt 断言:manjuQPrompt 不含 young man's face
	p := manjuQPrompt(m)
	if strings.Contains(p, "young man's face") {
		t.Errorf("老年男 Q 版 prompt 不得含 young man's face")
	}
	if !strings.Contains(p, "wrinkles") {
		t.Errorf("老年男 Q 版 prompt 应含皱纹特征")
	}
}

func TestQAgeAnchorElderlyFemale(t *testing.T) {
	m := qAgeChar("60岁", "女", nil)
	anchor := manjuQAgeAnchor(m)
	if !strings.Contains(anchor, "grandma") || !strings.Contains(anchor, "wrinkles") {
		t.Errorf("老年女锚应含 grandma+wrinkles, got: %s", anchor)
	}
}

func TestQAgeAnchorMiddleAndYoung(t *testing.T) {
	mid := manjuQAgeAnchor(qAgeChar("中年", "男", nil))
	if !strings.Contains(mid, "middle-aged") {
		t.Errorf("中年锚应含 middle-aged, got: %s", mid)
	}
	if strings.Contains(mid, "ELDERLY") {
		t.Errorf("中年锚不得含老年措辞")
	}
	teen := manjuQAgeAnchor(qAgeChar("少年", "男", nil))
	if !strings.Contains(teen, "teenage") {
		t.Errorf("少年锚应含 teenage, got: %s", teen)
	}
	if adult := manjuQAgeAnchor(qAgeChar("青年", "男", nil)); adult != "" {
		t.Errorf("成年档默认不加锚, got: %s", adult)
	}
}

func TestQAgeAnchorSpeciesSplit(t *testing.T) {
	// 兽形:人形年龄词禁入(兽脸画人类皱纹=畸形),老年兽走口鼻灰白措辞
	beast := map[string]any{"id": "老灵龟", "gender": "", "age": "老年", "species": "灵宠",
		"appearance": "玄甲老龟", "image_prompt": "an old turtle with dark shell"}
	if !manjuIsBeast(beast) {
		t.Fatalf("灵宠应判定为兽类")
	}
	if a := manjuQAgeAnchor(beast); a != "" {
		t.Errorf("兽形不得走人形年龄锚, got: %s", a)
	}
	ba := manjuQBeastAgeAnchor(beast)
	for _, want := range []string{"ELDERLY animal", "greying around the muzzle"} {
		if !strings.Contains(ba, want) {
			t.Errorf("老年兽锚应含 %q, got: %s", want, ba)
		}
	}
	if strings.Contains(ba, "wrinkles") || strings.Contains(ba, "grandpa") {
		t.Errorf("兽龄锚不得含人类皱纹/祖辈措辞, got: %s", ba)
	}
	// 幼兽/成兽不注入兽龄锚
	if manjuQBeastAgeAnchor(map[string]any{"id": "小貔", "species": "灵宠", "age": "幼年"}) != "" {
		t.Errorf("幼兽不注入老年兽锚")
	}
	// 物品:无年龄语义,两个锚都恒空
	item := map[string]any{"id": "古镜", "species": "物品", "age": "千年", "image_prompt": "an ancient mirror"}
	if !manjuIsItem(item) {
		t.Fatalf("物品判定失败")
	}
	if a := manjuQAgeAnchor(item); a != "" {
		t.Errorf("物品不得注入人形年龄锚, got: %s", a)
	}
	if manjuQBeastAgeAnchor(item) != "" {
		t.Errorf("物品不得注入兽龄锚")
	}
}
