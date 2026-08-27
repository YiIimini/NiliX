package api

import (
	"strings"
	"testing"
)

// scriptMinorCast 群演轻量卡(2026-08-27 群演分级):有台词无角色卡的说话人自动建卡,
// 形象取自首次开口镜的 subject_definitions(S 编号描述词重叠匹配归属)。
func TestScriptMinorCast(t *testing.T) {
	raws := []scriptShotRaw{
		{ID: 2, Dialogue: `(S1)王三："周管事，快些，上头只放一炷香。"
(S2)周管事："老奴……来送小姐最后一程。"`, H3Prompt: `subject_definitions:
<Subject 1> is the short fat dungeon warden in <Picture 1>, bowing and shuffling backward to welcome someone unseen.
<Subject 2> is the white-haired steward in <Picture 1>, being hustled away by two jailers into the shadow of a corridor pillar.

detailed_description:
The fat warden with a hushed voice (S1) says: <d>[中文]周管事，快些。</d> The old steward with a trembling voice (S2) says: <d>[中文]老奴……来送小姐最后一程。</d>`},
		{ID: 3, Dialogue: `(S3)谢照："府里，还好么。"
(S2)周管事："小姐，饭菜，要吃净。"`, H3Prompt: `subject_definitions:
<Subject 1> is a 17-year-old East Asian young woman in <Picture 1>, with a high ponytail of jet-black hair.
<Subject 2> is the white-haired steward in <Picture 1>, kneeling with lowered head, tears striking the food-box lid.

detailed_description:
The young woman with a calm, clear voice (S3) says: <d>[中文]府里，还好么。</d>`},
	}
	known := map[string]bool{"谢照": true}
	cards := scriptMinorCast(raws, known, &manjuLogger{state: manjuState})

	if len(cards) != 2 {
		t.Fatalf("应建 2 张群演卡(王三/周管事), got %d: %v", len(cards), cards)
	}
	byID := map[string]map[string]any{}
	for _, c := range cards {
		byID[str(c["id"])] = c
	}
	// 王三:词重叠 "warden" → 矮胖牢头;剥 in <Picture 1> 与姿态分词
	if p := str(byID["王三"]["image_prompt"]); !strings.Contains(p, "warden") || strings.Contains(p, "<Picture") || strings.Contains(p, "bowing") {
		t.Errorf("王三形象提取错误: %q", p)
	}
	// 周管事:词重叠 "steward" → 白发管事;不含谢照的 young woman
	if p := str(byID["周管事"]["image_prompt"]); !strings.Contains(p, "steward") || !strings.Contains(p, "white-haired") || strings.Contains(p, "kneeling") {
		t.Errorf("周管事形象提取错误: %q", p)
	}
	for _, c := range cards {
		if b, _ := c["minor"].(bool); !b {
			t.Errorf("%s 应标 minor:true", str(c["id"]))
		}
	}

	// 画外说话人不建卡;已知角色不重复建卡
	off := []scriptShotRaw{{ID: 1, Dialogue: `(S9)画外·路人甲："听说了吗。"`}}
	if cards := scriptMinorCast(off, known, &manjuLogger{state: manjuState}); len(cards) != 0 {
		t.Errorf("画外说话人不应建卡: %v", cards)
	}
	// 无六段式主体行 → 通用兜底描述(不空)
	noSubj := []scriptShotRaw{{ID: 1, Dialogue: `(S5)无名差役："走。"`}}
	if cards := scriptMinorCast(noSubj, known, &manjuLogger{state: manjuState}); len(cards) != 1 || str(cards[0]["image_prompt"]) == "" {
		t.Errorf("无主体行应兜底通用描述: %v", cards)
	}
}

// 素材直出的群演条目(技能源头契约,2026-08-27):「群演 · 名字」前缀 → 剥前缀取 id +
// minor:true(轻量卡);主要角色条目不受影响。
func TestParseCharCardsMinor(t *testing.T) {
	fence := "```"
	text := "# 《测试》人物生成提示词(写实)\n\n" +
		"## 1. 谢照(女主,17岁)\n记忆点:凤眼/红绳/白马甲\n\n" + fence + "\n" +
		"Front-facing portrait of a 17-year-old East Asian young woman, high ponytail of jet-black hair.\n" + fence + "\n\n" +
		"## 2. 群演 · 周管事(男,老年,白发老仆)\n" + fence + "\n" +
		"Front-facing portrait of an elderly Chinese steward, white hair in a bun with a wooden hairpin, grey servant robes.\n" + fence + "\n\n" +
		"## 3. 群演 · 王三(男,中年,矮胖牢头)\n" + fence + "\n" +
		"Front-facing portrait of a short fat middle-aged dungeon warden, lantern in hand.\n" + fence + "\n"
	cards := parseCharCards(text, manjuAssetStyle("real"), false)
	byID := map[string]map[string]any{}
	for _, c := range cards {
		byID[str(c["id"])] = c
	}
	if len(cards) != 3 {
		t.Fatalf("应解析 3 张卡, got %d: %v", len(cards), byID)
	}
	// 主角不标 minor
	if b, _ := byID["谢照"]["minor"].(bool); b {
		t.Error("主要角色不应标 minor")
	}
	// 群演:前缀剥离 + minor + 性别/提示词
	for _, name := range []string{"周管事", "王三"} {
		c := byID[name]
		if c == nil {
			t.Fatalf("群演卡缺失: %s", name)
		}
		if b, _ := c["minor"].(bool); !b {
			t.Errorf("%s 应标 minor:true(「群演 ·」前缀)", name)
		}
		if str(c["role"]) != "群演" {
			t.Errorf("%s role 应为 群演: %v", name, c["role"])
		}
		if p := str(c["image_prompt"]); !strings.Contains(p, "Front-facing portrait") {
			t.Errorf("%s 提示词应保留: %q", name, p)
		}
		if g := str(c["gender"]); g != "男" {
			t.Errorf("%s 性别应解析为 男: %q", name, g)
		}
	}
}
