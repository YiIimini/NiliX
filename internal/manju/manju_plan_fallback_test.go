package manju

import (
	"os"
	"path/filepath"
	"testing"
)

// manjuFindPlanFile/manjuFindPlanDir:episode=0/空/未生成集时,回退扫描 analysis 取集号最小的方案。
func TestManjuFindPlanFallback(t *testing.T) {
	dir := t.TempDir()
	// EP01_characters.json(角色) + EP01_direct_plan.json(分镜) + EP03_direct_plan.json
	_ = os.WriteFile(filepath.Join(dir, "EP01_characters.json"), []byte(`{"characters":[]}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "EP01_direct_plan.json"), []byte(`{"shots":[]}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "EP03_direct_plan.json"), []byte(`{"shots":[]}`), 0644)

	// 角色方案:指定集优先 characters;0/空/不存在集 → 最小集号(EP01)
	cases := []struct{ ep, want string }{
		{"", "EP01_characters.json"},
		{"0", "EP01_characters.json"},
		{"EP01", "EP01_characters.json"},
		{"EP03", "EP03_direct_plan.json"},
		{"EP99", "EP01_characters.json"}, // 该集未生成 → 回退最小集
	}
	for _, c := range cases {
		got := filepath.Base(manjuFindPlanFile(dir, c.ep))
		if got != c.want {
			t.Errorf("FindPlanFile(ep=%q) = %s, want %s", c.ep, got, c.want)
		}
	}
	// 分镜方案:direct 优先;0 → EP01_direct_plan.json
	for _, c := range []struct{ ep, want string }{
		{"", "EP01_direct_plan.json"},
		{"0", "EP01_direct_plan.json"},
		{"EP03", "EP03_direct_plan.json"},
	} {
		got := filepath.Base(manjuFindPlanDir(dir, c.ep))
		if got != c.want {
			t.Errorf("FindPlanDir(ep=%q) = %s, want %s", c.ep, got, c.want)
		}
	}
}

// ctx.loadPlan 在 episode=0 时回退到最小集号方案(角色抽卡/管理弹窗数据源)。
func TestLoadPlanFallbackZero(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "EP01_direct_plan.json"),
		[]byte(`{"episode_title":"第一集","characters":[{"id":"陈鱼"}],"shots":[]}`), 0644)
	ctx := &manjuCtx{analysisDir: dir, episode: "0"}
	plan, _, err := ctx.loadPlan()
	if err != nil {
		t.Fatalf("loadPlan(episode=0) 失败: %v", err)
	}
	if m, ok := anyArr(plan["characters"])[0].(map[string]any); !ok || str(m["id"]) != "陈鱼" {
		t.Fatalf("应回退读取 EP01 方案的角色: %v", plan["characters"])
	}
}
