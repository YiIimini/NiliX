package api

import (
	"strings"
	"testing"
)

// 2026-08-25 用户规则回归:Q版/视图/主图按角色种族/性别/年龄/胡须画像生成。
// ①妖兽灵宠 Q版=萌化小兽本体,禁止人形;②有胡须角色 Q版保留胡须;③女性无胡须;
// ④Q版面容随角色本人(非统一宝宝脸);⑤年轻男孩无胡须;⑥Q版基于正面照 img2img(见 stageAssets 实现)。

func TestManjuIsBeast(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		want bool
	}{
		{"species=妖兽", map[string]any{"species": "妖兽", "gender": "女"}, true},
		{"species=灵宠", map[string]any{"species": "灵宠", "gender": "男"}, true},
		{"species=神兽", map[string]any{"species": "神兽"}, true},
		{"species=人", map[string]any{"species": "人", "gender": "男"}, false},
		{"species=人形妖族", map[string]any{"species": "人形妖族", "gender": "女"}, false},
		{"role=正派灵宠", map[string]any{"role": "正派灵宠", "gender": "女"}, true},
		{"role=正角无species", map[string]any{"role": "正角", "gender": "男", "appearance": "一头通体雪白的狐狸"}, true},
		{"appearance兽形特征", map[string]any{"gender": "女", "appearance": "兽耳+九尾,毛茸茸"}, true},
		{"人形不误判-虎牙", map[string]any{"gender": "女", "appearance": "两颗小虎牙,笑时露齿"}, false},
		{"人形不误判-龙傲天", map[string]any{"gender": "男", "id": "龙傲天", "appearance": "剑眉星目"}, false},
		{"空卡", map[string]any{}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := manjuIsBeast(c.m); got != c.want {
			t.Errorf("%s: manjuIsBeast=%v want %v", c.name, got, c.want)
		}
	}
}

func TestManjuIsYoungMale(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		want bool
	}{
		{"少年", map[string]any{"gender": "男", "age": "少年"}, true},
		{"青少年", map[string]any{"gender": "男", "age": "青少年"}, true},
		{"青年", map[string]any{"gender": "男", "age": "青年"}, true},
		{"年轻男人", map[string]any{"gender": "男", "age": "年轻"}, true},
		{"16岁", map[string]any{"gender": "男", "age": "16岁"}, true},
		{"中年", map[string]any{"gender": "男", "age": "中年"}, false},
		{"老年", map[string]any{"gender": "男", "age": "老年"}, false},
		{"35岁", map[string]any{"gender": "男", "age": "35岁"}, false},
		{"女性少年不算", map[string]any{"gender": "女", "age": "少年"}, false},
	}
	for _, c := range cases {
		if got := manjuIsYoungMale(c.m); got != c.want {
			t.Errorf("%s: manjuIsYoungMale=%v want %v", c.name, got, c.want)
		}
	}
}

func TestManjuHasBeardAndEnforce(t *testing.T) {
	bearded := map[string]any{"gender": "男", "age": "老年", "appearance": "花白络腮胡,剑眉"}
	if !manjuHasBeard(bearded) {
		t.Error("老年男 appearance 含络腮胡应判为有胡须")
	}
	female := map[string]any{"gender": "女", "age": "青年", "appearance": "秀发如瀑"}
	young := map[string]any{"gender": "男", "age": "少年", "appearance": "清秀面容"}
	adult := map[string]any{"gender": "男", "age": "中年"}
	beast := map[string]any{"species": "灵宠", "gender": "女"}
	cases := []struct {
		name string
		m    map[string]any
		has  string
		no   string
	}{
		{"女性强制无胡须", female, "no beard", "keeping the character's beard"},
		{"年轻男性强制无胡须", young, "no beard", "keeping the character's beard"},
		{"胡须老者保留", bearded, "keeping the character's beard", "no beard"},
		{"无胡须成年男性默认无", adult, "no beard", "keeping the character's beard"},
		{"妖兽跳过胡须纪律", beast, "", "no beard"},
	}
	for _, c := range cases {
		got := manjuBeardEnforce(c.m)
		if c.has != "" && !strings.Contains(got, c.has) {
			t.Errorf("%s: beardEnforce 应含 %q,实际 %q", c.name, c.has, got)
		}
		if c.no != "" && strings.Contains(got, c.no) {
			t.Errorf("%s: beardEnforce 不应含 %q,实际 %q", c.name, c.no, got)
		}
	}
}

func TestManjuQPrompt(t *testing.T) {
	beast := map[string]any{"species": "灵宠", "role": "正角", "gender": "女", "appearance": "通体雪白的九尾狐", "image_prompt": "a white nine-tailed fox, full body, head to toe"}
	q := manjuQPrompt(beast)
	if !strings.Contains(q, "NOT a human") {
		t.Errorf("妖兽 Q版必须禁止人形,实际: %s", q)
	}
	if strings.Contains(q, "a cute girl") || strings.Contains(q, "a cute boy") {
		t.Errorf("妖兽 Q版禁止人类性别词,实际: %s", q)
	}
	if strings.Contains(q, "full body, head to toe") {
		t.Errorf("妖兽 Q版应剔除全身立绘指令(与 chibi 冲突),实际: %s", q)
	}

	female := map[string]any{"gender": "女", "age": "青年", "role": "正角", "appearance": "丹凤眼,鹅蛋脸,slender phoenix eyes and an oval face", "image_prompt": "a girl with long black hair, full body"}
	q = manjuQPrompt(female)
	if !strings.Contains(q, "miniature chibi of the same female character") || !strings.Contains(q, "no beard") {
		t.Errorf("女性 Q版应为同一角色缩小版且无胡须,实际: %s", q)
	}
	// 2026-08-28:appearance 注入只认英文段(图像模型不读中文,中文面容词=废 token,
		// 已 manjuStripCJK 剔除;面容身份由 init 主图携带+image_prompt 英文词承载)
	if !strings.Contains(q, "phoenix eyes") {
		t.Errorf("Q版面容应随角色本人(英文 appearance 注入),实际: %s", q)
	}
	if strings.Contains(q, "丹凤眼") {
		t.Errorf("中文面容词不应再注入图像提示词,实际: %s", q)
	}
	// 2026-08-26 用户规则:Q版=定妆照缩小版 Q 萌,不是小孩——
	// 旧措辞 "a cute girl/boy" 是幼态化(儿童)高发词,禁止回归
	if strings.Contains(q, "a cute girl") || strings.Contains(q, "a cute boy") || strings.Contains(q, "a cute male character") {
		t.Errorf("Q版禁止幼态措辞(a cute girl/boy),实际: %s", q)
	}
	if !strings.Contains(q, "keeping the character's original age") {
		t.Errorf("Q版必须显式禁儿童化并保留原年龄感,实际: %s", q)
	}

	beardedOld := map[string]any{"gender": "男", "age": "老年", "role": "正角", "appearance": "花白络腮胡", "image_prompt": "an old man, full body"}
	q = manjuQPrompt(beardedOld)
	if !strings.Contains(q, "keeping the character's beard") {
		t.Errorf("胡须老者 Q版必须保留胡须,实际: %s", q)
	}

	youngBoy := map[string]any{"gender": "男", "age": "少年", "role": "正角", "appearance": "剑眉星目", "image_prompt": "a boy, full body"}
	q = manjuQPrompt(youngBoy)
	if !strings.Contains(q, "no beard") {
		t.Errorf("年轻男孩 Q版必须无胡须,实际: %s", q)
	}
	if !strings.Contains(q, "same character as the reference image") {
		t.Errorf("Q版必须带身份锚(基于正面照),实际: %s", q)
	}
}

func TestManjuPortraitPromptForBeastAnchor(t *testing.T) {
	beast := map[string]any{"species": "神兽", "appearance": "赤鳞龙角", "image_prompt": "semi-realistic stylized illustration of an East Asian/Chinese character, a majestic beast"}
	p := manjuPortraitPromptFor(beast["image_prompt"].(string), beast)
	if strings.Contains(p, "East Asian/Chinese character") {
		t.Errorf("妖兽主图不得残留人类锚,实际: %s", p)
	}
	if !strings.Contains(p, "fantastical beast creature") {
		t.Errorf("妖兽主图应带兽类锚,实际: %s", p)
	}
	female := map[string]any{"gender": "女", "appearance": "丹凤眼"}
	p2 := manjuPortraitPromptFor("portrait of a woman", female)
	if !strings.Contains(p2, "no beard") {
		t.Errorf("女性主图应强制无胡须,实际: %s", p2)
	}
}

// 2026-08-25 用户规则回归:渲染提示词最终化——违规词同义/谐音替换 + 多余人脸硬约束。
func TestManjuSanitizeRenderWords(t *testing.T) {
	out, repl := manjuSanitizeRenderWords("他下令屠城,尸横遍野,血流成河")
	if strings.Contains(out, "屠城") || strings.Contains(out, "尸横遍野") || strings.Contains(out, "血流成河") {
		t.Errorf("暴力词未替换: %s", out)
	}
	if repl["屠城"] != 1 || repl["尸横遍野"] != 1 {
		t.Errorf("替换记录不对: %v", repl)
	}
	if !strings.Contains(out, "血洗城池") || !strings.Contains(out, "尸骸遍地") {
		t.Errorf("同义替换缺失: %s", out)
	}
	clean, repl2 := manjuSanitizeRenderWords("他提剑而立,风拂衣袂")
	if clean != "他提剑而立,风拂衣袂" || len(repl2) != 0 {
		t.Errorf("普通文本被误改: %s %v", clean, repl2)
	}
}

func TestManjuFinalizeShotPrompt(t *testing.T) {
	ctx := &manjuCtx{}
	lg := &manjuLogger{state: manjuState}
	s := manjuShot{ID: 5, Characters: []string{"阿拾"}}
	out := ctx.finalizeShotPrompt("detailed_description: 他下令屠城 ...", s, lg)
	if strings.Contains(out, "屠城") {
		t.Errorf("finalize 未替换违规词: %s", out)
	}
	if !strings.Contains(out, "no extra faces") {
		t.Errorf("finalize 未加多余人脸硬约束: %s", out)
	}
	if !strings.Contains(out, "never show the same character twice") || !strings.Contains(out, "reuse another character's look") {
		t.Errorf("finalize 未加人物不重复/独立形象约束(2026-08-27 分镜4重复人物反馈): %s", out)
	}
	out2 := ctx.finalizeShotPrompt(out, s, lg)
	if strings.Count(out2, "FRAME DISCIPLINE") != 1 {
		t.Errorf("finalize 非幂等(重复追加): %s", out2)
	}
	s2 := manjuShot{ID: 6}
	out3 := ctx.finalizeShotPrompt("integrated_multimodal_description: 空镜", s2, lg)
	if strings.Contains(out3, "no extra faces") {
		t.Errorf("空镜不应加多余人脸约束: %s", out3)
	}
}

// 2026-08-25 用户要求:分镜头必须按顺序渲染——planShots 统一按 shot_id 升序,
// 方案 shots 数组乱序(LLM 直出/修复回写偶发)不得影响渲染/接缝/进度顺序。
func TestPlanShotsSortsByID(t *testing.T) {
	plan := map[string]any{"shots": []any{
		map[string]any{"shot_id": 5, "scene": "a", "duration": 4},
		map[string]any{"shot_id": 2, "scene": "b", "duration": 5},
		map[string]any{"shot_id": 9, "scene": "c", "duration": 6},
		map[string]any{"shot_id": 1, "scene": "d", "duration": 4},
	}}
	shots, err := planShots(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 5, 9}
	for i, s := range shots {
		if s.ID != want[i] {
			t.Fatalf("planShots 顺序 = %v,期望 %v", shots, want)
		}
		if s.Duration == 0 {
			t.Errorf("镜头 %d 缺省时长未归一为 5", s.ID)
		}
	}
}

// 2026-08-25 即梦角色版落地:角色板(角色资料卡)资产——配色锚 + 角色板提示词兜底
func TestManjuColorAnchorAndBoard(t *testing.T) {
	m := map[string]any{"color_palette": "#1E2A33 #31414D #53606D #D8D1C6 #A89B8B"}
	a := manjuColorAnchor(m)
	if !strings.Contains(a, "#1E2A33") || !strings.Contains(a, "HEX") {
		t.Errorf("配色锚缺失: %s", a)
	}
	if manjuColorAnchor(map[string]any{}) != "" {
		t.Error("无配色时应返回空")
	}
	ctx := &manjuCtx{}
	m2 := map[string]any{"image_prompt": "a xianxia cultivator", "color_palette": "#1E2A33", "appearance": "眉如墨画"}
	bp := ctx.manjuBoardPromptFor("墨珩", m2)
	if !strings.Contains(bp, "character reference board") || !strings.Contains(bp, "#1E2A33") {
		t.Errorf("角色板兜底缺失(布局/配色): %s", bp)
	}
	m3 := map[string]any{"board_prompt": "custom board with #112233"}
	if p := ctx.manjuBoardPromptFor("墨珩", m3); !strings.Contains(p, "#112233") {
		t.Errorf("board_prompt 未优先: %s", p)
	}
}
