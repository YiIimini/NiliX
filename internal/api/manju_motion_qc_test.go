package api

import (
	"strings"
	"testing"
)

// 2026-08-27 用户反馈:叶澜黑白挑染发 Q 版变纯黑——发色锁测试
func TestManjuHairAnchorTwoTone(t *testing.T) {
	m := map[string]any{
		"image_prompt": "Front-facing portrait, a 24-year-old man with silver-highlighted wolf-cut black hair, wearing a jacket, 8K",
		"appearance":   "symmetrical frontal face",
	}
	anchor := manjuHairAnchor(m)
	if !strings.Contains(anchor, "silver-highlighted wolf-cut black hair") {
		t.Fatalf("发色锚应含原始发色词, got: %s", anchor)
	}
	if !strings.Contains(anchor, "multi-tone") {
		t.Fatalf("发色锚必须显式声明多色发保持多色: %s", anchor)
	}
}

func TestManjuHairAnchorPalette(t *testing.T) {
	m := map[string]any{
		"image_prompt":  "black hair",
		"color_palette": "#1E2A33 #31414D #53606D #D8D1C6 #C7C7C7",
	}
	anchor := manjuHairAnchor(m)
	if !strings.Contains(anchor, "#C7C7C7") {
		t.Fatalf("发色锚应带配色板发色位(第5色): %s", anchor)
	}
}

func TestManjuQPromptHairLock(t *testing.T) {
	m := map[string]any{
		"gender":       "男",
		"image_prompt": "full body, a young man with silver-highlighted wolf-cut black hair, head to toe",
	}
	p := manjuQPrompt(m)
	if !strings.Contains(p, "HAIR LOCK") {
		t.Fatalf("Q版提示词必须含发色锁: %s", p)
	}
	if !strings.Contains(p, "silver-highlighted wolf-cut black hair") {
		t.Fatalf("Q版提示词应保留具体发色词: %s", p)
	}
	// 兽类分支同样要锁毛色
	beast := map[string]any{
		"gender": "公", "species": "灵宠",
		"image_prompt": "a white-furred fox spirit with golden mane, fur color black and white",
	}
	pb := manjuQPrompt(beast)
	if !strings.Contains(pb, "HAIR LOCK") || !strings.Contains(pb, "black and white") {
		t.Fatalf("兽类Q版应锁毛色: %s", pb)
	}
}

// manjuFinalizePromptPure 幂等性:指纹对称修复的基础(mark 与 stale 检查两侧一致)
func TestManjuFinalizePromptPureIdempotent(t *testing.T) {
	hp := "subject_definitions:\n<Subject 1> is a man.\n\ndetailed_description:\nThe camera pushes in."
	for _, hasChars := range []bool{true, false} {
		once := manjuFinalizePromptPure(hp, hasChars, 0)
		twice := manjuFinalizePromptPure(once, hasChars, 0)
		if once != twice {
			t.Fatalf("finalize 必须幂等(hasChars=%v):\nonce: %q\ntwice: %q", hasChars, once, twice)
		}
	}
	full := manjuFinalizePromptPure(hp, true, 3)
	if !strings.Contains(full, "FRAME DISCIPLINE") || !strings.Contains(full, "MOTION & SEAM DISCIPLINE") {
		t.Fatalf("有角色镜应同时含人脸纪律与运动纪律: %q", full)
	}
	if !strings.Contains(full, "renders as a union") {
		t.Fatalf("所有镜头应含接缝并集纪律(2026-09-02 并入 MOTION & SEAM,恒定注入保指纹稳定): %q", full)
	}
	noChar := manjuFinalizePromptPure(hp, false, 0)
	// 2026-08-27 语义更新:人物纪律按"提示词有无 subject_definitions"判定,不再只看
	// characters——群像无卡镜(群演无角色卡)同样必须有人脸纪律+无参考图纪律
	// (用户反馈 04/05 镜周管事渲染两次的根因)。
	if !strings.Contains(noChar, "FRAME DISCIPLINE") {
		t.Fatalf("有人物镜(characters 空)也应注入人脸纪律: %q", noChar)
	}
	if !strings.Contains(noChar, "no reference picture is attached") {
		t.Fatalf("有人物镜(characters 空)应注入无参考图纪律: %q", noChar)
	}
	if !strings.Contains(noChar, "MOTION & SEAM DISCIPLINE") || !strings.Contains(noChar, "renders as a union") || !strings.Contains(noChar, "AUDIO & LIP DISCIPLINE") {
		t.Fatalf("有人物镜(characters 空)也应注入运动/链式/台词纪律: %q", noChar)
	}
	// 真空镜(无 subject_definitions)不注入人物纪律,但运动/链式/台词纪律恒定
	scenery := "detailed_description:\nA wide shot of the empty corridor."
	noSub := manjuFinalizePromptPure(scenery, false, 3)
	if strings.Contains(noSub, "FRAME DISCIPLINE") || strings.Contains(noSub, "no reference picture is attached") {
		t.Fatalf("空镜不应注入人物纪律: %q", noSub)
	}
	if !strings.Contains(noSub, "MOTION & SEAM DISCIPLINE") || !strings.Contains(noSub, "renders as a union") || !strings.Contains(noSub, "AUDIO & LIP DISCIPLINE") {
		t.Fatalf("空镜也应注入运动/链式/台词纪律: %q", noSub)
	}
	if manjuFinalizePromptPure("", true, 0) != "" {
		t.Fatal("空提示词应原样返回")
	}
}

// manjuQGen 代数:Q 版升级后旧 _q.png 必须清理重出(独立于 views_gen)
func TestManjuQGenValue(t *testing.T) {
	if manjuQGen < 4 {
		t.Fatalf("Q版着装锁强化代数应为 4(旧Q版才会清理重出), got %d", manjuQGen)
	}
}

// 2026-08-27 用户反馈:男性 Q 版外套敞开袒胸露乳——着装锁测试
func TestManjuQPromptOutfitLock(t *testing.T) {
	m := map[string]any{
		"gender":       "男",
		"image_prompt": "full body, a young man wearing a loose black stage outfit, BJD doll aesthetic, realistic skin texture",
	}
	p := manjuQPrompt(m)
	if !strings.Contains(p, "OUTFIT LOCK") {
		t.Fatalf("Q版提示词必须含着装锁: %s", p)
	}
	if !strings.Contains(p, "black stage outfit") {
		t.Fatalf("着装锁应保留具体服装词: %s", p)
	}
	if strings.Contains(p, "loose black stage outfit") {
		t.Fatalf("着装锁必须剥除敞开感词(宽松描述会强化敞袍先验): %s", p)
	}
	if !strings.Contains(p, "robes and coats closed") {
		t.Fatalf("着装锁必须显式要求衣袍交领闭合: %s", p)
	}
	if !strings.Contains(p, "overlapping lapels") {
		t.Fatalf("着装锁必须要求交领重叠遮胸: %s", p)
	}
	if !strings.Contains(p, "chest and torso always fully covered by clothing") {
		t.Fatalf("着装锁必须显式禁袒胸: %s", p)
	}
	// BJD 素体残词必须替换为着装版
	if strings.Contains(p, "BJD doll aesthetic") && !strings.Contains(p, "fully dressed BJD doll aesthetic") {
		t.Fatalf("裸 BJD 素体措辞必须替换为着装版: %s", p)
	}
	// 防裸覆盖句应在强位置(紧随 OUTFIT LOCK 提取子句,先于性别/末尾通用句)
	if strings.Index(p, "OUTFIT LOCK") > strings.Index(p, "chest and torso always fully covered by clothing") {
		t.Fatalf("着装覆盖句应随 OUTFIT LOCK 前置: %s", p)
	}
}

// 2026-08-27 二修:柳含烟/魏鹤年/魏琮 宽袍/儒袍/官袍 Q 版仍敞胸——袍服敞开形态全覆盖
func TestManjuQPromptRobeChestCovered(t *testing.T) {
	// 古风宽袍(魏鹤年:宽松官袍)——"zipped/buttoned" 措辞对无拉链袍服无效,必须交领闭合措辞
	m := map[string]any{
		"gender":       "男",
		"image_prompt": "a 58-year-old man in loose minister robes in dark slate color, holding prayer beads",
	}
	p := manjuQPrompt(m)
	if strings.Contains(p, "loose minister robes") {
		t.Fatalf("宽松袍描述必须剥敞开感词: %s", p)
	}
	if !strings.Contains(p, "robes and coats closed with overlapping lapels") {
		t.Fatalf("Q版提示词必须禁敞袍/敞外套形态: %s", p)
	}
	// 女性(柳含烟:素净丝袍)——必须禁露胸/乳沟,且保留丝袍服装词
	f := map[string]any{
		"gender":       "女",
		"image_prompt": "a noblewoman in plain expensive silk robes of a dutiful housewife",
	}
	pf := manjuQPrompt(f)
	if !strings.Contains(pf, "silk robes") {
		t.Fatalf("女性着装锁应保留具体服装词: %s", pf)
	}
	if !strings.Contains(pf, "modest outfit fully covering the chest and collarbone") {
		t.Fatalf("女性Q版必须禁乳沟/露胸: %s", pf)
	}
	// 性别空(魏琮:绀青儒袍)——通用强防裸句必须兜底生效
	g := map[string]any{
		"image_prompt": "a young scholar-official in showy azure scholar robe with jade pendant",
	}
	pg := manjuQPrompt(g)
	if !strings.Contains(pg, "overlapping lapels") || !strings.Contains(pg, "robes and coats closed") {
		t.Fatalf("性别空角色也必须交领闭合/禁敞袍: %s", pg)
	}
}
