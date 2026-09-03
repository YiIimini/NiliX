package manju

// 脚本直出 × 多切点长镜回归测试(2026-08-26 用户实测万界召唤 16 分镜只渲染 8 个):
// 脚本每镜六段式提示词逐字权威(节拍/时间码按单镜时长写死),takes 分组后组头沿用
// 单镜提示词却渲染「组内多镜时长之和」的长视频,组内其余镜头画面与台词不进任何
// 提示词 → 整镜内容丢失。脚本直出一律逐镜独立渲染;旧 plan 已写入的 takes 清除自愈。

import (
	"os"
	"path/filepath"
	"testing"
)

const wanjie16Script = `# 《万界召唤》分镜脚本 · 第1章_十年剑意（第1集）

| 镜号 | 景别 | 运镜 | 画面内容 | 台词/旁白（带 ID） | 光影 | 音效 | 时长 |
|---|---|---|---|---|---|---|---|
| 01 | 大远景 | 慢推 | 白玉高台立于广场正中，秦召站在人群最外层仰看 | 旁白：这样的日子他过了十年。 | 冷调 | 人声 | 6s |
| 02 | 中景 | 固定 | 人群往前挤，唯秦召原地不动 | 内心·秦召：那道温热的剑意，是他全部的家当。 | 暖光 | 劈柴声 | 5s |
| 03 | 远景 | 缓摇 | 玄真子立于高台正中宣判 | (S1)玄真子："今日大典，只有一事。" | 顶光 | 钟磬 | 6s |
| 04 | 近景 | 急推 | 秦召瞳孔猛缩 | (S1)玄真子："剑意嫁与无咎。" | 逆光 | 心跳 | 5s |

### Shot 01
` + tplBacktick + `
detailed_description: Cinematic. [Shot 1] 秦召仰看高台。
` + tplBacktick + `

### Shot 02
` + tplBacktick + `
detailed_description: Cinematic. [Shot 2] 秦召原地不动。
` + tplBacktick + `

### Shot 03
` + tplBacktick + `
detailed_description: Cinematic. [Shot 3] 玄真子宣判。
` + tplBacktick + `

### Shot 04
` + tplBacktick + `
detailed_description: Cinematic. [Shot 4] 秦召瞳孔猛缩。
` + tplBacktick + `
`

func wanjieStyleCtx(t *testing.T, renderExtra string) *manjuCtx {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "EP01.md")
	_ = os.WriteFile(sp, []byte(wanjie16Script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"script":"`+filepath.ToSlash(sp)+`"},"render":{`+renderExtra+`}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	return ctx
}

// TestScriptModeTakesDisabled 脚本直出:即使 render.shots_per_take=2(AI 一条龙深度分析
// 曾默认建议并写回),ensureTakes 也不得分组——selectedShots 必须覆盖全部镜头、
// 时长保持脚本原值(不得被拉成组内之和)。
func TestScriptModeTakesDisabled(t *testing.T) {
	ctx := wanjieStyleCtx(t, `"shots_per_take":2`)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	// 预置旧 takes(模拟旧版本已写入的分组:1+2 一组),修复后必须被清除
	plan["takes"] = []any{[]any{1, 2}}
	ctx.ensureTakes(plan, mustPlanShots(t, plan), lg)
	if _, still := plan["takes"]; still {
		t.Fatalf("脚本直出应清除旧 plan.takes(自愈),仍存在: %v", plan["takes"])
	}
	shots := applyTakes(plan, mustPlanShots(t, plan))
	sel := ctx.selectedShots(shots)
	if len(sel) != 4 {
		t.Fatalf("脚本直出 shots_per_take=2 仍应逐镜渲染 4 个, got %d —— takes 分组回归!", len(sel))
	}
	for i, s := range sel {
		if s.Duration != []int{6, 5, 6, 5}[i] {
			t.Fatalf("镜头 %d 时长应保持脚本原值, got %d", s.ID, s.Duration)
		}
	}
	// 非 shots_per_take 配置(=1)本来就不分组,行为不变
	ctx2 := wanjieStyleCtx(t, "")
	plan2, err := ctx2.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	ctx2.ensureTakes(plan2, mustPlanShots(t, plan2), lg)
	if len(ctx2.selectedShots(applyTakes(plan2, mustPlanShots(t, plan2)))) != 4 {
		t.Fatalf("shots_per_take 缺省应 4 镜全渲染")
	}
}

// TestScriptParseVerUpgradeReplacesStalePlan 解析器代数升级自愈(2026-08-26 用户实测:
// 16 分镜脚本只渲染 8 个,普通一条龙):旧版解析失败静默回退 LLM 直出,LLM 拆了 8 镜落盘,
// 之后 plan 被永久复用。plan 无 script_parse_ver(或版本落后)→ ensurePlan 必须重新解析
// 替换为脚本真实镜数;带当前版本标记的 plan 正常复用不重解析。
func TestScriptParseVerUpgradeReplacesStalePlan(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "EP01.md")
	_ = os.WriteFile(sp, []byte(wanjie16Script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}

	// 模拟旧版 LLM 兜底落盘的 8 镜 plan:chapters="script"、无 script_parse_ver、
	// novel_fp 与当前脚本指纹一致(复用判定全过)
	shots8 := []any{}
	for i := 1; i <= 8; i++ {
		shots8 = append(shots8, map[string]any{"shot_id": i, "duration": 5, "h3_prompt": "LLM 兜底旧镜"})
	}
	old := map[string]any{"chapters": "script", "shots": shots8, "novel_fp": ctx.novelFingerprint()}
	if err := ctx.writePlan(old); err != nil {
		t.Fatalf("writePlan: %v", err)
	}

	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		t.Fatalf("ensurePlan: %v", err)
	}
	got, _ := planShots(plan)
	if len(got) != 4 {
		t.Fatalf("旧 8 镜 LLM 兜底 plan 应被重新解析替换为脚本真实镜数, got %d", len(got))
	}
	if v, _ := manjuToInt(plan["script_parse_ver"]); v != manjuScriptParseVer {
		t.Fatalf("新 plan 应带 script_parse_ver=%d, got %v", manjuScriptParseVer, plan["script_parse_ver"])
	}
	for _, s := range got {
		if s.H3Prompt == "LLM 兜底旧镜" {
			t.Fatalf("镜头 %d 仍是 LLM 兜底旧内容,未替换", s.ID)
		}
	}

	// 二次运行:版本已是当前 → 正常复用,不再触发重解析(shots 保持 16)
	plan2, err := ctx.ensurePlan(lg)
	if err != nil {
		t.Fatalf("ensurePlan 2nd: %v", err)
	}
	got2, _ := planShots(plan2)
	if len(got2) != len(got) {
		t.Fatalf("版本最新的 plan 应复用(镜数不变), got %d", len(got2))
	}
}

func mustPlanShots(t *testing.T, plan map[string]any) []manjuShot {
	t.Helper()
	shots, _ := planShots(plan)
	return shots
}
