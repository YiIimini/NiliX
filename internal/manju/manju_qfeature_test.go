package manju

import (
	"strings"
	"testing"
)

// 2026-08-28 苏砚bug回归:「女主之父」不得命中「女主」判成女——亲缘称谓优先。
func TestScriptGenderOfKinship(t *testing.T) {
	cases := map[string]string{
		"## 6. 苏砚（暗线·科学家，女主之父）": "男",
		"## 3. 韩老夫人（男主之母，太后）":   "女",
		"## 1. 苏晚（女主·义体医生）":      "女",
		"## 2. 白羽（男主·剑修）":        "男",
	}
	for head, want := range cases {
		if got := scriptGenderOf(head, strings.SplitN(strings.SplitN(head, "（", 2)[1], "）", 2)[0], ""); got != want {
			t.Errorf("%s gender=%s, want %s", head, got, want)
		}
	}
}

// 2026-08-28 Q版标志性特征锁:韩天枢眼镜/屠夫扫描仪眼罩必须进 Q 版提示词并被显式锁定;
// 管理员(纯数据光生命)走非实体精灵分支,不套实体着装锁。
func TestManjuQFeatureLockAndNonPhysical(t *testing.T) {
	han := map[string]any{
		"id": "韩天枢", "gender": "男", "appearance": "眼镜反光遮眼",
		"image_prompt": "a 50-year-old CEO, cold unreadable eyes hidden behind glasses reflections, immaculate dark grey suit, a white glove on his left hand",
		"views":        map[string]any{},
	}
	q := manjuQPrompt(han)
	if !strings.Contains(q, "SIGNATURE FEATURE LOCK") || !strings.Contains(q, "glasses reflections") {
		t.Errorf("韩天枢 Q 版缺特征锁: %s", q)
	}

	butcher := map[string]any{
		"id": "屠夫", "gender": "男",
		"image_prompt": "an enforcer, a single-eye scanner visor, massive crimson hydraulic dismantling pincers for arms, shaved head",
		"views":        map[string]any{},
	}
	q2 := manjuQPrompt(butcher)
	if !strings.Contains(q2, "single-eye scanner visor") || !strings.Contains(q2, "pincers") {
		t.Errorf("屠夫 Q 版缺眼罩/液压钳特征锁: %s", q2)
	}

	admin := map[string]any{
		"id": "管理员",
		"image_prompt": "a shimmering holographic entity of pure data-light, a humanoid figure formed from flowing streams of cyan and gold code, no solid body",
		"views": map[string]any{},
	}
	q3 := manjuQPrompt(admin)
	if !manjuIsNonPhysical(admin) {
		t.Errorf("管理员应判定为非实体角色")
	}
	if !strings.Contains(q3, "holographic data spirit") || strings.Contains(q3, "OUTFIT LOCK") {
		t.Errorf("管理员 Q 版应走发光数据精灵分支且不套实体着装锁: %s", q3)
	}

	// 普通人类角色不受非实体分支影响
	normal := map[string]any{
		"id": "柒", "gender": "男",
		"image_prompt": "a battle-worn military android soldier, one glowing red tactical eye, silver-grey armored frame",
		"views":        map[string]any{},
	}
	if manjuIsNonPhysical(normal) {
		t.Errorf("柒不应误判为非实体角色")
	}
}
