package manju

import (
	"strings"
	"testing"
)

// 2026-08-29 阿影 Q 版串色实锤回归:非人角色卡内【Q版·内心戏专用提示词】段写在
// 主形象段之前——旧解析恒取第一个代码块,Q版黑团子提示词被当主形象拼人形风格锚,
// 影灵主图渲染成银白团子(黑↔白反转)。修复后:主形象块跳过带形态标记的块,
// Q版段独立解析为 q_form,渲染端 manjuQPrompt 优先取。
func TestMultiFormCardMainBlock(t *testing.T) {
	// 阿影卡真实结构(Q版段在前、主形象段居中、记忆点在后的第三代多形态格式)
	body := "> **物种说明**:非人主角,渲染走非人渲染链。\n\n" +
		"**【Q版·内心戏专用提示词】(非人·影灵,动漫风格不受限,仅内心戏镜头使用)**\n```\n" +
		"a chibi-style tiny shadow spirit, a round fluffy black blob with two glowing blue dot eyes\n```\n\n" +
		"**【主形象·影子形态提示词】(日常,非人·无实体,正片主图)**\n```\n" +
		"a living shadow creature flat on the ground, pure black liquid silhouette with faint blue glow\n```\n\n" +
		"> 阿影记忆点:①幽蓝光点眼睛②头顶月牙白毛\n"
	main := scriptMainBlock(body)
	if !strings.Contains(main, "living shadow creature") || strings.Contains(main, "chibi") {
		t.Fatalf("主形象块应取影子形态段(非Q版段),实取:%s", main)
	}
	q := scriptQForm(body)
	if !strings.Contains(q, "chibi-style tiny shadow spirit") || !strings.Contains(q, "black blob") {
		t.Fatalf("Q版段应独立提取,实取:%s", q)
	}
	// 真身段兼容回归:主形象在前、真身在后的既有格式不受影响
	old := "```\nmain portrait prompt here\n```\n\n**真身提示词**:\n```\nancient wisteria tree form\n```\n"
	if m := scriptMainBlock(old); !strings.Contains(m, "main portrait") {
		t.Fatalf("单块/主形象在前的旧格式退化:%s", m)
	}
	if f := scriptSecondForm(old); !strings.Contains(f, "wisteria") {
		t.Fatalf("真身段提取退化:%s", f)
	}
}

// 毛色锁出现序(2026-08-29 阿影实锤):black blob 主体色在前段、white 月牙毛在后段,
// 锁文本必须是「black and white」(主体色优先),不再是固定词表序的「white and black」。
func TestFurAnchorColorOrder(t *testing.T) {
	m := map[string]any{
		"image_prompt": "a round fluffy black blob with a small white moon-crescent hair strand, cute smug",
	}
	fur := manjuFurAnchor(m)
	if !strings.Contains(fur, "black and white") || strings.Contains(fur, "white and black") {
		t.Fatalf("毛色锁主体色应在前(主体色在前):%s", fur)
	}
	// 反例:白主体+黑斑(主体色 white 在前段)保持「white and black」
	m2 := map[string]any{
		"image_prompt": "a snow-white cat with black patches on the back",
	}
	fur2 := manjuFurAnchor(m2)
	if !strings.Contains(fur2, "white") || !strings.Contains(fur2, "black") {
		t.Fatalf("双色猫锁应含两色:%s", fur2)
	}
}
