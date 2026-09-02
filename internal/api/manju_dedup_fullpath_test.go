package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDedupFullPath 完整 buildPlanFromRaws 路径验证镜15 去重(王牌三岁半真实数据)
func TestDedupFullPath(t *testing.T) {
	if _, err := os.ReadFile("../../novel/王牌三岁半/素材/分镜脚本/第1章_0.3%_分镜脚本.json"); err != nil {
		t.Skip("素材缺失")
	}
	dir := t.TempDir()
	// 模拟 project:素材在脚本所在目录
	ctx := &manjuCtx{
		workdir: filepath.Join(dir, "proj"),
	}
	_ = os.MkdirAll(ctx.workdir, 0o755)
	// 直接调 buildPlanFromRaws 需要素材,改用 scriptParsePlan 全路径
	ctx.novel = "../../novel/王牌三岁半/素材/分镜脚本/第1章_0.3%_分镜脚本.json"
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots, _ := planShots(plan)
	for _, s := range shots {
		if s.ID != 15 {
			continue
		}
		t.Logf("镜15 h3 联邦史上最低 次数: %d", strings.Count(s.H3Prompt, "联邦史上最低"))
		t.Logf("H3:[NL]%s", s.H3Prompt)
		if strings.Count(s.H3Prompt, "联邦史上最低") > 1 {
			t.Fatalf("完整路径镜15 仍重复")
		}
	}
}
