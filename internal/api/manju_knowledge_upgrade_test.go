package api

import (
	"strings"
	"testing"
)

// 2026-08-24 知识库升级(「H3提示词优化5层结构方法论」+「AIGC人物微表情设计指南」+
// 「官方风格技能与漫剧优化」+「H3长镜连续与工作室实战」+「H3社区生态与最佳实践」):
// 表演层纪律/节奏模型/BGM 定向/近景补偿/官方风格签名/冻结检测。

// 表演层纪律:情绪三层拆解(外部动作/生理反应/量化指标)+ 五维微表情 + 非对称克制 + 哭戏四梯度。
func TestPerformanceLayerDiscipline(t *testing.T) {
	sys := manjuShotPromptSystem(true, "2.5d")
	for _, want := range []string{
		"情绪三层拆解", "外部动作", "生理反应", "量化指标",
		"微表情五维拆解", "眉眼状态", "呼吸节奏", "光影质感",
		"非对称与克制中断", "哭戏四梯度", "强忍泪水", "崩溃大哭",
		"原子需求台账", "必须出现", "必须保持", "禁止出现",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuShotPromptSystem 缺少表演层纪律 %q", want)
		}
	}
	// 方案直出系统提示词同样携带表演层
	direct := manjuDirectSystem(map[string]any{}, "2.5d")
	for _, want := range []string{"表演层·强制", "瞳孔收缩", "喉结滚动", "单侧嘴角下沉"} {
		if !strings.Contains(direct, want) {
			t.Errorf("manjuDirectSystem 缺少表演层纪律 %q", want)
		}
	}
	// 脚本直出同样携带
	script := manjuScriptSystem(map[string]any{}, "2.5d")
	if !strings.Contains(script, "表演层·强制") || !strings.Contains(script, "哭戏四梯度") {
		t.Errorf("manjuScriptSystem 缺少表演层纪律")
	}
}

// 节奏模型:5s=3-4 beat / 10s=5-7 beat / 15s=6-9 beat + 峰值刹车 + 节奏意图词。
func TestRhythmModelDiscipline(t *testing.T) {
	direct := manjuDirectSystem(map[string]any{}, "real")
	for _, want := range []string{
		"节奏模型·强制", "3-4 个 beat", "5-7 个 beat", "6-9 个 beat",
		"峰值", "刹车", "setup", "impact", "brake", "settle",
	} {
		if !strings.Contains(direct, want) {
			t.Errorf("manjuDirectSystem 缺少节奏模型 %q", want)
		}
	}
	script := manjuScriptSystem(map[string]any{}, "real")
	for _, want := range []string{"节奏模型·强制", "5 秒镜=3-4 beat", "峰值镜前必有蓄势"} {
		if !strings.Contains(script, want) {
			t.Errorf("manjuScriptSystem 缺少节奏模型 %q", want)
		}
	}
}

// 近景补偿:人脸 token 数学 → 情感戏/对话强制近景特写。
func TestCloseUpCompensation(t *testing.T) {
	direct := manjuDirectSystem(map[string]any{}, "real")
	for _, want := range []string{"近景补偿·强制", "中景人脸仅约 2 token", "情感戏/对白戏/表情戏", "近景或特写"} {
		if !strings.Contains(direct, want) {
			t.Errorf("manjuDirectSystem 缺少近景补偿 %q", want)
		}
	}
	script := manjuScriptSystem(map[string]any{}, "real")
	if !strings.Contains(script, "近景补偿·强制") || !strings.Contains(script, "机位对准面部") {
		t.Errorf("manjuScriptSystem 缺少近景补偿")
	}
}

// BGM 定向文案:题材文化贴合乐器(古筝/竹笛/鼓组/钢琴/钟琴/木琴)进两套模板。
func TestBGMDirectionalCopy(t *testing.T) {
	sys := manjuShotPromptSystem(true, "2.5d")
	for _, want := range []string{
		"BGM 定向文案·强制", "古筝", "竹笛", "琵琶",
		"鼓组", "钢琴", "钟琴", "木琴", "ducking",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("shot 系统提示词缺少 BGM 定向文案 %q", want)
		}
	}
	if !strings.Contains(manjuFl2vaTpl, "BGM 定向文案·强制") {
		t.Errorf("FL2VA 模板缺少 BGM 定向文案")
	}
	if !strings.Contains(manjuRef2vaTpl, "BGM 定向文案·强制") {
		t.Errorf("Ref2VA 模板缺少 BGM 定向文案")
	}
}

// 官方风格签名:3d 用 Pixar 签名、papercraft 用纸艺定格签名、新增 minimal 极简产品预设。
func TestOfficialStyleSignatures(t *testing.T) {
	cases := map[string][]string{
		"3d":         {"Pixar-inspired 3D cartoon", "C4D + Octane look", "warm subsurface scattering skin"},
		"papercraft": {"handmade papercraft stop-motion", "miniature diorama", "layered cardboard cutouts", "matte paper textures", "2.5D parallax"},
		"handdrawn":  {"crayon/chalk/colored pencil/pastel texture", "slightly trembling lines", "frame-by-frame redraw feel"},
	}
	for style, wants := range cases {
		spec, ok := manjuStyles[style]
		if !ok {
			t.Fatalf("manjuStyles 缺预设 %q", style)
		}
		for _, want := range wants {
			if !strings.Contains(spec.asset, want) {
				t.Errorf("manjuStyles[%q].asset 缺少官方签名 %q", style, want)
			}
			if !strings.Contains(spec.opening, want) {
				t.Errorf("manjuStyles[%q].opening 缺少官方签名 %q", style, want)
			}
		}
	}
	if _, ok := manjuStyles["minimal"]; !ok {
		t.Errorf("manjuStyles 缺 minimal 极简产品预设")
	}
	if !strings.Contains(manjuStyles["minimal"].asset, "dark rim light") {
		t.Errorf("minimal 预设缺 white-tech/dark rim light 签名词")
	}
	// 组合风格仍按核心措辞拼接(3d+ink)
	joined := manjuStyleDesc("3d+ink")
	if !strings.Contains(joined.asset, "Pixar") || !strings.Contains(joined.asset, "ink wash") {
		t.Errorf("组合风格 3d+ink 未合并两预设签名: %q", joined.asset)
	}
}

// 表演层规则注入逐镜提示词系统后,规则号连续无断裂(29-33 存在)。
func TestShotRulesContinuity(t *testing.T) {
	sys := manjuShotPromptSystem(true, "real")
	for _, n := range []string{"29. 【表演层·情绪三层拆解", "30. 【微表情五维拆解", "31. 【哭戏四梯度", "32. 【非对称与克制中断", "33. 【原子需求台账"} {
		if !strings.Contains(sys, n) {
			t.Errorf("逐镜写作规则缺少 %q", n)
		}
	}
}
