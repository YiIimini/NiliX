package manju

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-01 二合一阶段一:按镜参数覆盖机制单测

// 覆盖指纹:内容→指纹串;空覆盖→空串(不参与指纹)
func TestShotOverrideFingerprint(t *testing.T) {
	if s := shotOverrideFingerprint(manjuShotOverride{}); s != "" {
		t.Fatalf("空覆盖应返回空串, got %q", s)
	}
	seed := 42
	steps := 12
	o := manjuShotOverride{Seed: &seed, Steps: &steps, Sampler: "euler", PDD: boolPtr(true)}
	s1 := shotOverrideFingerprint(o)
	if s1 == "" || !strings.Contains(s1, "seed=42") || !strings.Contains(s1, "steps=12") || !strings.Contains(s1, "sampler=euler") {
		t.Fatalf("指纹应含全部覆盖项: %q", s1)
	}
	o2 := o
	o2.Steps = nil
	s2 := shotOverrideFingerprint(o2)
	if s1 == s2 {
		t.Fatalf("覆盖内容变化指纹必须变化")
	}
}

// 覆盖存取:set→load→清空
func TestShotOverrideStore(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: dir, episode: "EP01"}
	seed := 7
	if err := ctx.setShotOverride(3, manjuShotOverride{Seed: &seed, Note: "测试"}); err != nil {
		t.Fatal(err)
	}
	ov := ctx.shotOverrideFor(3)
	if ov.Seed == nil || *ov.Seed != 7 {
		t.Fatalf("覆盖未生效: %+v", ov)
	}
	if _, err := os.Stat(filepath.Join(dir, "EP01_shot_overrides.json")); err != nil {
		t.Fatalf("覆盖文件未落盘: %v", err)
	}
	// 空覆盖=清除
	if err := ctx.setShotOverride(3, manjuShotOverride{}); err != nil {
		t.Fatal(err)
	}
	if ov2 := ctx.shotOverrideFor(3); ov2.Seed != nil {
		t.Fatalf("空覆盖应清除, got %+v", ov2)
	}
	// 全部清空
	_ = ctx.setShotOverride(5, manjuShotOverride{Note: "x"})
	_ = ctx.clearShotOverrides()
	if n := len(ctx.loadShotOverrides()); n != 0 {
		t.Fatalf("清空后应无覆盖, got %d", n)
	}
}

// seedFor:override.seed 优先(重试 attempt 仍递增)
func TestShotOverrideSeedFor(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: dir, episode: "EP01", seed: 100, seedPolicy: "fixed"}
	ctx.R = map[string]any{}
	seed := 500
	_ = ctx.setShotOverride(2, manjuShotOverride{Seed: &seed})
	if got := ctx.seedFor(2, 0); got != 500 {
		t.Fatalf("override seed 应优先: got %d", got)
	}
	if got := ctx.seedFor(2, 1); got != 501 {
		t.Fatalf("重试应递增: got %d", got)
	}
	// 清除覆盖后:全局派生
	_ = ctx.setShotOverride(2, manjuShotOverride{})
	if got := ctx.seedFor(2, 0); got != 102 {
		t.Fatalf("无覆盖应走全局派生: got %d", got)
	}
}

func boolPtr(b bool) *bool { return &b }
