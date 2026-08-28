package api

// 2026-08-26 用户反馈六项角色/渲染修复的回归测试:
// ①接缝音频上下文默认最小(多重配音) ②身份词前缀(盟友/灵宠)清理+role/species
// ③性别称谓兜底(墨姨) ④视图性别锚 ⑤Q版两头身 ⑥兽类角色板兽形布局

import (
	"strings"
	"testing"
)

// TestMotionAudioContextDefault 接缝音频上下文(多重配音根因):MotionContext 会把上一镜
// 尾部音频 pin 进本镜与 <d> 台词并行=两路人声交叠,默认 1(≈25ms);可配 1-96;禁 0
// (节点 a_frames=int(v) or span:0 是 falsy 反而取全窗口)。
func TestMotionAudioContextDefault(t *testing.T) {
	R := map[string]any{}
	wf := h3RenderWorkflow(R, 1, 768, 1344, 100, 8, "c", true, true, 1, 2)
	mc := wfFindNode(t, wf, "MiniMaxH3MotionContext")
	if got := valOf(mc, "audio_context_length"); got != "1" {
		t.Fatalf("默认 audio_context_length 应为 \"1\", got %v", got)
	}
	R["motion_audio_context"] = 24
	wf = h3RenderWorkflow(R, 1, 768, 1344, 100, 8, "c", true, true, 1, 2)
	mc = wfFindNode(t, wf, "MiniMaxH3MotionContext")
	if got := valOf(mc, "audio_context_length"); got != "24" {
		t.Fatalf("配置 24 应生效, got %v", got)
	}
	R["motion_audio_context"] = 0 // 0 禁止(节点 falsy 语义=全窗口)
	wf = h3RenderWorkflow(R, 1, 768, 1344, 100, 8, "c", true, true, 1, 2)
	mc = wfFindNode(t, wf, "MiniMaxH3MotionContext")
	if got := valOf(mc, "audio_context_length"); got != "1" {
		t.Fatalf("配置 0 应回退默认 1(节点 falsy 语义), got %v", got)
	}
}

func wfFindNode(t *testing.T, wf map[string]any, typ string) map[string]any {
	t.Helper()
	for _, v := range wf {
		if m, ok := v.(map[string]any); ok {
			if m["class_type"] == typ {
				return m
			}
		}
	}
	t.Fatalf("工作流缺节点 %s", typ)
	return nil
}

func valOf(m map[string]any, k string) any {
	w, ok := m["inputs"].(map[string]any)
	if !ok {
		return nil
	}
	return w[k]
}

// TestScriptCleanNameAllies 素材身份词前缀清理(「盟友 · 陈墨」「灵宠 · 吞吞」整串进 id 修复)
func TestScriptCleanNameAllies(t *testing.T) {
	cases := []struct{ in, want string }{
		{"盟友 · 陈墨", "陈墨"},
		{"灵宠 · 吞吞", "吞吞"},
		{"坐骑 · 黑风驹", "黑风驹"},
		{"妖兽 · 赤炎狼", "赤炎狼"},
		{"主角 · 顾烬", "顾烬"},
		{"云晚", "云晚"},
	}
	for _, c := range cases {
		if got := scriptCleanName(c.in); got != c.want {
			t.Errorf("scriptCleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// reMdTitle 对「盟友 · 陈墨(…)」标题:捕获名字不含身份词前缀
	m := reMdTitle.FindStringSubmatch("## 3. 盟友 · 陈墨（男主角挚友，22岁）")
	if m == nil || scriptCleanName(m[1]) != "陈墨" {
		t.Fatalf("reMdTitle 盟友标题应剥离前缀: %v", m)
	}
}

// TestScriptRoleSpecies 身份词 → role/species(灵宠等 → species 驱动兽形 Q 版/角色板)
func TestScriptRoleSpecies(t *testing.T) {
	if r, s := scriptRoleSpecies("## 1. 灵宠 · 吞吞（吞天兽）"); s != "灵宠" {
		t.Fatalf("灵宠标题应解析 species=灵宠, got role=%q species=%q", r, s)
	}
	if r, s := scriptRoleSpecies("## 2. 盟友 · 陈墨（男主挚友）"); r != "正角" {
		t.Fatalf("盟友应 role=正角, got role=%q species=%q", r, s)
	}
	if r, s := scriptRoleSpecies("## 3. 反派 · 裴无咎（伪善）"); r != "反派" {
		t.Fatalf("反派应 role=反派, got %q %q", r, s)
	}
	if r, s := scriptRoleSpecies("## 4. 云晚（女主，22岁）"); r != "正角" || s != "" {
		t.Fatalf("女主应 role=正角, got %q %q", r, s)
	}
	if r, s := scriptRoleSpecies("## 5. 路人甲（神秘人）"); r != "" || s != "" {
		t.Fatalf("无身份词不应有 role/species, got %q %q", r, s)
	}
}

// TestScriptGenderByAppellation 中文称谓性别兜底(「墨姨」无显式性别 → side 视图曾被画成有胡须的男人)
func TestScriptGenderByAppellation(t *testing.T) {
	cases := []struct {
		head, desc, want string
	}{
		{"## 3. 墨姨（客栈老板娘，45岁）", "客栈老板娘", "女"},
		{"## 2. 张婶（邻居）", "邻居婶婶", "女"},
		{"## 5. 老爷（家主）", "家中老爷", "男"},
		{"## 6. 王叔（铁匠）", "铁匠大叔", "男"},
		{"云晚（女主）", "少女", "女"},
		{"路人甲", "神秘人", ""},
	}
	for _, c := range cases {
		if got := scriptGenderOf(c.head, c.desc, ""); got != c.want {
			t.Errorf("scriptGenderOf(%q,%q) = %q, want %q", c.head, c.desc, got, c.want)
		}
	}
}

// TestViewGenderAnchor 视图性别锚(高 denoise 重绘下防性别漂移)
func TestViewGenderAnchor(t *testing.T) {
	if a := manjuViewGenderAnchor(map[string]any{"gender": "女"}); !strings.Contains(a, "woman") {
		t.Fatalf("女性视图应带 woman 正向锚, got %q", a)
	}
	if a := manjuViewGenderAnchor(map[string]any{"gender": "男", "age": "中年"}); !strings.Contains(a, "man") {
		t.Fatalf("男性视图应带 man 正向锚, got %q", a)
	}
	if a := manjuViewGenderAnchor(map[string]any{}); a != "" {
		t.Fatalf("gender 空不应加锚, got %q", a)
	}
	if a := manjuViewGenderAnchor(map[string]any{"gender": "女", "species": "灵宠"}); a != "" {
		t.Fatalf("兽类不加人类性别锚, got %q", a)
	}
}

// TestQPromptChibiProportion Q 版比例(2026-08-28 用户好标准校准:3头身——2头身身体太小
// 容纳不了铠甲/纹样细节,服装还原度是用户第一优先级)
func TestQPromptChibiProportion(t *testing.T) {
	q := manjuQPrompt(map[string]any{"gender": "男", "age": "中年", "role": "正角", "appearance": "剑眉"})
	if !strings.Contains(q, "3-head-tall chibi proportions") {
		t.Fatalf("Q 版默认 base 应含 3-head-tall chibi proportions(2026-08-28 用户好标准实拍校准:3头身才有服装细节空间), got: %s", q)
	}
}

// TestBeastBoardLayout 兽类角色板(「灵宠 · 吞吞_board」出人形修复):
// 兽类忽略 LLM 人形 board_prompt,用兽形板布局并禁人形。
func TestBeastBoardLayout(t *testing.T) {
	ctx := &manjuCtx{}
	beast := map[string]any{
		"id": "吞吞", "species": "灵宠",
		"image_prompt": "a small white beast",
		"board_prompt": "character reference board (role info sheet): three full-body views of a humanoid...",
	}
	p := ctx.manjuBoardPromptFor("吞吞", beast)
	if !strings.Contains(p, "creature reference board") || !strings.Contains(p, "NOT a humanoid") {
		t.Fatalf("兽类角色板应用兽形布局+禁人形, got: %s", p)
	}
	human := map[string]any{"id": "陈墨", "gender": "男", "board_prompt": "custom board layout with #ABCDEF"}
	if p2 := ctx.manjuBoardPromptFor("陈墨", human); !strings.Contains(p2, "custom board layout") {
		t.Fatalf("人类角色板应沿用 LLM board_prompt, got: %s", p2)
	}
}
