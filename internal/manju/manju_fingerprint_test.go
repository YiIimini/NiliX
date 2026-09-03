package manju

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 指纹推导:directing 五维 + 首尾景别 + 镜数
func TestPlanFingerprint(t *testing.T) {
	dir := t.TempDir()
	novel := filepath.Join(dir, "全本", "书.md")
	_ = os.MkdirAll(filepath.Dir(novel), 0o755)
	_ = os.WriteFile(novel, []byte("正文"), 0o644)
	ctx := &manjuCtx{episode: "EP01", novel: novel, style: "2.5d", analysisDir: filepath.Join(dir, "analysis")}
	plan := map[string]any{
		"directing": map[string]any{
			"time": "线性", "pov": "全知", "tempo": "加速爆发",
			"audio": "对白驱动", "ending": "悬而未决",
			"peak_device": "摘面具+近乎全黑", "climax_pattern": "6×特写连打",
		},
		"shots": []any{
			map[string]any{"shot_id": 1, "shot_size": "特写", "characters": []any{"甲"}},
			map[string]any{"shot_id": 2, "shot_size": "中景", "characters": []any{"甲"}},
			map[string]any{"shot_id": 3, "shot_size": "近景", "characters": []any{"甲"}},
		},
	}
	fp := ctx.planFingerprint(plan)
	if fp.ID != "EP01" || fp.Novel != "书.md" {
		t.Fatalf("指纹基础字段错: %+v", fp)
	}
	if fp.Vars["time"] != "线性" || fp.Vars["ending"] != "悬而未决" {
		t.Fatalf("五维未正确提取: %+v", fp.Vars)
	}
	if fp.PeakDevice != "摘面具+近乎全黑" || fp.ClimaxPattern != "6×特写连打" {
		t.Fatalf("signature 手法未提取: %+v", fp)
	}
	if fp.ShotCount != 3 || fp.OpeningSize != "特写" || fp.EndingSize != "近景" {
		t.Fatalf("signature 景别/镜数错: %+v", fp)
	}
}

// vars 撞 ≥3 维(近 3 集)判问题;撞 2 维不判
func TestFingerprintVarsCollision(t *testing.T) {
	base := map[string]string{"time": "线性", "pov": "全知", "tempo": "匀速", "audio": "BGM通铺", "ending": "空景收"}
	recent := []manjuFingerprint{
		{ID: "EP01", Vars: map[string]string{"time": "线性", "pov": "全知", "tempo": "匀速", "audio": "BGM通铺", "ending": "空景收"}},
		{ID: "EP02", Vars: map[string]string{"time": "线性", "pov": "全知", "tempo": "匀速", "audio": "BGM通铺", "ending": "空景收"}},
	}
	// 五维全撞 → 判
	fp := manjuFingerprint{Vars: base}
	if hits := fingerprintVarsCollision(fp, recent); len(hits) < 3 {
		t.Fatalf("五维全撞应判 ≥3 维,got %v", hits)
	}
	// 只撞 2 维(time/pov;改 tempo/audio/ending)→ 不判
	fp2 := manjuFingerprint{Vars: map[string]string{"time": "线性", "pov": "全知", "tempo": "前紧后松", "audio": "环境声", "ending": "悬而未决"}}
	if hits := fingerprintVarsCollision(fp2, recent); len(hits) >= 3 {
		t.Fatalf("只撞 2 维不应判,got %v", hits)
	}
	// 本集未填的维(audio/ending)不算撞;已填且匹配的只有 time/pov → 不判
	fp3 := manjuFingerprint{Vars: map[string]string{"time": "线性", "pov": "全知", "tempo": "前紧后松"}}
	if hits := fingerprintVarsCollision(fp3, recent); len(hits) >= 3 {
		t.Fatalf("未填维不应算撞,got %v", hits)
	}
}

// signature 撞 ≥2 项(全历史)判问题;shot_count ±10% 内算撞
func TestFingerprintSignatureCollision(t *testing.T) {
	hist := []manjuFingerprint{
		{ID: "EP01", Date: "2026-08-01", ShotCount: 18, OpeningSize: "特写", EndingSize: "大远景", PeakDevice: "摘面具+近乎全黑", ClimaxPattern: "6×特写连打"},
	}
	// 撞 3 项 → 判
	fp := manjuFingerprint{ShotCount: 17, OpeningSize: "特写", EndingSize: "大远景", PeakDevice: "摘面具+近乎全黑"}
	if hits := fingerprintSignatureCollision(fp, hist); len(hits) != 1 {
		t.Fatalf("应判 1 条撞(3 项),got %v", hits)
	}
	// 只撞 1 项(景别;镜头数 20 vs 18 差超 ±10%% 不算)→ 不判
	fp2 := manjuFingerprint{ShotCount: 20, OpeningSize: "近景", EndingSize: "大远景"}
	if hits := fingerprintSignatureCollision(fp2, hist); len(hits) != 0 {
		t.Fatalf("只撞 1 项不应判,got %v", hits)
	}
	// 镜头数差超 ±10% → 不算撞
	fp3 := manjuFingerprint{ShotCount: 24, EndingSize: "大远景"}
	if hits := fingerprintSignatureCollision(fp3, hist); len(hits) != 0 {
		t.Fatalf("镜数 24 vs 18 差超 10%% 且只撞 1 项不应判,got %v", hits)
	}
}

// 记录-回填-同集号替换 + 查重问题闭环(经 validatePlan)
func TestPlanFingerprintRecordAndCheck(t *testing.T) {
	dir := t.TempDir()
	novel := filepath.Join(dir, "全本", "书.md")
	_ = os.MkdirAll(filepath.Dir(novel), 0o755)
	_ = os.WriteFile(novel, []byte("正文"), 0o644)
	analysis := filepath.Join(dir, "analysis")
	ctx := &manjuCtx{episode: "EP01", project: "测试剧", novel: novel, style: "2.5d", analysisDir: analysis}
	plan := map[string]any{
		"directing": map[string]any{"time": "线性", "pov": "全知", "tempo": "匀速", "audio": "BGM通铺", "ending": "空景收"},
		"shots":     []any{map[string]any{"shot_id": 1, "shot_size": "大远景"}},
	}
	ctx.recordPlanFingerprint(plan, nil)
	if _, err := os.Stat(manjuFingerprintPath(analysis)); err != nil {
		t.Fatalf("指纹文件未生成: %v", err)
	}
	// 同集号再次记录 → 幂等替换(文件只有 1 行)
	ctx.recordPlanFingerprint(plan, nil)
	if recs := manjuFingerprintLoad(manjuFingerprintPath(analysis)); len(recs) != 1 {
		t.Fatalf("同集号应幂等替换,got %d 条", len(recs))
	}
	// EP02 方案与 EP01 五维全撞 + signature 撞(镜数 1≈1/景别) → validatePlan 判问题
	ctx2 := &manjuCtx{episode: "EP02", project: "测试剧", novel: novel, style: "2.5d", analysisDir: analysis}
	plan2 := map[string]any{
		"directing": map[string]any{"time": "线性", "pov": "全知", "tempo": "匀速", "audio": "BGM通铺", "ending": "空景收"},
		"shots":     []any{map[string]any{"shot_id": 1, "shot_size": "大远景"}},
	}
	probs := ctx2.planFingerprintProblems(plan2)
	found := false
	for _, p := range probs {
		if strings.Contains(p, "防同质化") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("EP02 五维全撞应判防同质化问题,got %v", probs)
	}
	// EP02 差异化(改 tempo/audio/ending + 首尾景别 + 镜数)→ 无问题
	plan2["directing"] = map[string]any{"time": "线性", "pov": "全知", "tempo": "前紧后松", "audio": "对白驱动", "ending": "日常化"}
	var shots2 []any
	for i := 1; i <= 5; i++ {
		sz := "中景"
		if i == 1 {
			sz = "特写"
		} else if i == 5 {
			sz = "近景"
		}
		shots2 = append(shots2, map[string]any{"shot_id": i, "shot_size": sz})
	}
	plan2["shots"] = shots2
	if probs = ctx2.planFingerprintProblems(plan2); len(probs) != 0 {
		t.Fatalf("差异化后不应有问题,got %v", probs)
	}
	// 历史摘要:EP01 在列
	summary := ctx2.manjuFingerprintHistorySummary(plan2)
	if !strings.Contains(summary, "EP01") || !strings.Contains(summary, "directing 五维禁止") {
		t.Fatalf("历史摘要缺失: %q", summary)
	}
}
