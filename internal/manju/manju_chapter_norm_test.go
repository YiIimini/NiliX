package manju

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"testing"
)

// 回归:旁路入口(抽卡方案 gacha/plan/智能体分析/诊断)构造 ctx 时章节语义必须归一——
// 此前 config render.chapters="0" 被当字面章节号,报"章节不存在: 0(该文件共 N 章)"。
// 规则与 manjuRun 一致:空/0=全书 1-N;集数纯数字 N>0 无条件覆盖为第 N 章。
func TestNewManjuCtxChapterNormalization(t *testing.T) {
	proj := "zz_ctx_norm_test"
	dir := filepath.Join(paths.ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	novel := filepath.Join(dir, "book.md")
	_ = os.WriteFile(novel, []byte("# 第1章 a\n# 第2章 b\n# 第3章 c\n"), 0644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"novel":"`+filepath.ToSlash(novel)+`","workdir":"`+filepath.ToSlash(dir)+`"},"render":{"chapters":"0"}}`), 0644)

	// 集数 2 → EP02 + 第 2 章(集数无条件覆盖,config 的 0 先归一全书再被覆盖)
	ctx, err := newManjuCtx(cfgPath, "2", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.episode != "EP02" || ctx.chapters != "2-2" {
		t.Fatalf("集数 2 应得 EP02/2-2,得 %s/%s", ctx.episode, ctx.chapters)
	}

	// 集数 0(自动)+ chapters 0 → 全书 1-3
	ctx2, err := newManjuCtx(cfgPath, "0", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx2.chapters != "1-3" {
		t.Fatalf("chapters 0 应归一全书 1-3,得 %s", ctx2.chapters)
	}

	// 显式章节范围(非 0)原样保留
	ctx3, _ := newManjuCtx(cfgPath, "0", "1-2", "", "")
	if ctx3.chapters != "1-2" {
		t.Fatalf("显式 1-2 应保留,得 %s", ctx3.chapters)
	}

	// 集数 EP 格式(主运行路径已解析后传入)不重复覆盖
	ctx4, _ := newManjuCtx(cfgPath, "EP02", "2-2", "", "")
	if ctx4.chapters != "2-2" || ctx4.episode != "EP02" {
		t.Fatalf("EP02/2-2 应原样,得 %s/%s", ctx4.episode, ctx4.chapters)
	}
}
