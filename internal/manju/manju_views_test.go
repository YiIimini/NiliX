package manju

import (
	"os"
	"path/filepath"
	"testing"
)

// 多视图升级回归:charViewRels 按角色数分配视图预算(front 回退主图,full/detail 缺失跳过)
func TestCharViewRelsBudget(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(assets, 0755)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets")}

	// 单角色:有 face+full+detail → 3 视图
	_ = os.WriteFile(filepath.Join(assets, "甲_face.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "甲_full.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "甲_detail.png"), []byte("f"), 0644)
	got := ctx.charViewRels("甲", 0, 1)
	want := []string{"characters/甲_face.png", "characters/甲_full.png", "characters/甲_detail.png"}
	if len(got) != len(want) {
		t.Fatalf("单角色视图 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("单角色视图 = %v, want %v", got, want)
		}
	}

	// 双角色:每角色 2 视图(front/full),detail 预算外
	got2 := ctx.charViewRels("甲", 0, 2)
	want2 := []string{"characters/甲_face.png", "characters/甲_full.png"}
	if len(got2) != len(want2) {
		t.Fatalf("双角色视图 = %v, want %v", got2, want2)
	}
	for i := range want2 {
		if got2[i] != want2[i] {
			t.Fatalf("双角色视图 = %v, want %v", got2, want2)
		}
	}
}

// 多视图升级回归:front 缺失回退主图;full 缺失跳过(不降级)
func TestCharViewRelsFallback(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(assets, 0755)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets")}

	// 只有主图:front 回退主图,full/detail 缺失跳过 → 1 视图
	_ = os.WriteFile(filepath.Join(assets, "乙.png"), []byte("m"), 0644)
	got := ctx.charViewRels("乙", 0, 1)
	if len(got) != 1 || got[0] != "characters/乙.png" {
		t.Fatalf("主图回退 = %v, want [characters/乙.png]", got)
	}

	// 有主图+full:front 回退主图 + full → 2 视图
	_ = os.WriteFile(filepath.Join(assets, "乙_full.png"), []byte("f"), 0644)
	got2 := ctx.charViewRels("乙", 0, 1)
	want2 := []string{"characters/乙.png", "characters/乙_full.png"}
	if len(got2) != len(want2) {
		t.Fatalf("主图+full = %v, want %v", got2, want2)
	}
	for i := range want2 {
		if got2[i] != want2[i] {
			t.Fatalf("主图+full = %v, want %v", got2, want2)
		}
	}
}

// 多视图升级回归:shotRefViews 平铺角色+视图清单,顺序与 Picture 编号对应
func TestShotRefViews(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(assets, 0755)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets")}
	// 陈鱼 face+full;柳如烟 face
	_ = os.WriteFile(filepath.Join(assets, "陈鱼_face.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "陈鱼_full.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "柳如烟_face.png"), []byte("f"), 0644)
	s := manjuShot{Characters: []string{"陈鱼", "柳如烟"}}
	got := ctx.shotRefViews(s)
	want := []string{"陈鱼(front)", "陈鱼(full)", "柳如烟(front)"}
	if len(got) != len(want) {
		t.Fatalf("shotRefViews = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shotRefViews = %v, want %v", got, want)
		}
	}
}

// 多视图升级回归:views 提示词——2026-08-24 起 image_prompt(含身份特征)优先,
// views.<view> 仅兜底(防"白发老者侧面变黑发":LLM 泛化 views 丢发色/胡须)
func TestCharViewPromptFor(t *testing.T) {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	_ = os.MkdirAll(analysis, 0755)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"),
		[]byte(`{"characters":[{"id":"甲","image_prompt":"front base, white beard","views":{"full":"full prompt","detail":"detail prompt"}}]}`), 0644)
	ctx := &manjuCtx{analysisDir: analysis, episode: "EP01", assetsDir: filepath.Join(dir, "assets")}
	// image_prompt 优先 + 视图后缀(身份特征必须带上)
	if got := charViewPromptFor(ctx, "甲", "full"); got != "front base, white beard, full body, head to toe, standing pose, complete outfit visible" {
		t.Fatalf("full 提示词 = %q, want image_prompt 优先(含身份特征)", got)
	}
	if got := charViewPromptFor(ctx, "甲", "side"); got != "front base, white beard, side profile, 90 degree side view, face and hairstyle silhouette, body side view" {
		t.Fatalf("side 派生 = %q", got)
	}
	if got := charViewPromptFor(ctx, "甲", ""); got != "front base, white beard" {
		t.Fatalf("主视图 = %q, want %q", got, "front base, white beard")
	}
	// 无 image_prompt 时兜底 views.<view>
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"),
		[]byte(`{"characters":[{"id":"乙","image_prompt":"","views":{"full":"full prompt"}}]}`), 0644)
	if got := charViewPromptFor(ctx, "乙", "full"); got != "full prompt" {
		t.Fatalf("兜底 views.full = %q, want %q", got, "full prompt")
	}
}

// TestCharSeedUnique 角色定妆照 seed:不同角色/视图 seed 不同,同角色跨调用稳定
// (防"多部小说主角 seed 相近 → 面容雷同"——此前固定 7000+i 递增)。
func TestCharSeedUnique(t *testing.T) {
	// 同角色同视图:稳定
	if s1, s2 := charSeed("顾青梧", "main"), charSeed("顾青梧", "main"); s1 != s2 {
		t.Fatalf("同角色 seed 不稳定: %d vs %d", s1, s2)
	}
	// 不同角色:不同 seed
	if s1, s2 := charSeed("顾青梧", "main"), charSeed("陈鱼", "main"); s1 == s2 {
		t.Fatalf("不同主角 seed 相同: %d", s1)
	}
	// 同角色不同视图:不同 seed
	if s1, s2 := charSeed("顾青梧", "main"), charSeed("顾青梧", "front"); s1 == s2 {
		t.Fatalf("同角色视图 seed 相同: %d", s1)
	}
	// 中文名与英文名不撞
	if s1, s2 := charSeed("陈鱼", "main"), charSeed("ChenYu", "main"); s1 == s2 {
		t.Fatalf("中文/英文同名 seed 相同: %d", s1)
	}
}
