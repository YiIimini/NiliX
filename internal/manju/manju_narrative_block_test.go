package manju

// 叙事块(narrative_blocks,2026-09-04 STEP 2 多镜合渲转正)回归测试:
// 技能侧分镜源头声明 2-3 连续镜合一个叙事块(块级六段式官方 [Shot N] At 切点),
// 渲染端单次生成整块——切镜在视频内,根治硬拼感。覆盖:解析落 plan / 校验降级 /
// ensureTakes 保留 / applyTakes 块覆盖 / 定点映射 / 旧启发式 takes 清除自愈不变。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const blockBookScript = `{
  "book": "叙事块测试书",
  "episode": 1,
  "shots": [
    {"shot_id":1,"shot_size":"特写","camera":"Static","action":"【青萝村】小满抱葫芦","dialogue":"旁白：她递了十万九千五百碗水。","characters":["小满"],"duration":6,"h3_prompt":"subject_definitions:\n[Shot 1] solo prompt A"},
    {"shot_id":2,"shot_size":"中景","camera":"Pan","action":"【青萝村】村民围观","dialogue":"(S1)何大勇:\"把善缘簿交出来。\"","characters":["何大勇"],"duration":5,"h3_prompt":"summary:\nsolo prompt B"},
    {"shot_id":3,"shot_size":"远景","camera":"Static","action":"【山道】空景","duration":5,"h3_prompt":"summary:\nsolo prompt C"},
    {"shot_id":4,"shot_size":"近景","camera":"Static","action":"【山道】小满起身","dialogue":"旁白：风起了。","characters":["小满"],"duration":4,"h3_prompt":"summary:\nsolo prompt D"}
  ],
  "narrative_blocks": [
    {"shots":[1,2],"h3_prompt":"subject_definitions:\n[Shot 1] ... [Shot 2] At 00:06.000 block prompt covering both"},
    {"shots":[3,99],"h3_prompt":"bad: missing shot 99"},
    {"shots":[4],"h3_prompt":"single shot not a block [Shot 1]"},
    {"shots":[3,4,2],"h3_prompt":"non-consecutive [Shot 1]"}
  ]
}`

func blockCtx(t *testing.T) *manjuCtx {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "EP01.json")
	_ = os.WriteFile(sp, []byte(blockBookScript), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	return ctx
}

// 全链:解析 → plan.takes/takes_src → ensureTakes 保留 → applyTakes 块覆盖组头
func TestNarrativeBlockParseAndApply(t *testing.T) {
	ctx := blockCtx(t)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	if str(plan["takes_src"]) != "blocks" {
		t.Fatalf("takes_src = %v, want blocks", plan["takes_src"])
	}
	takes, _ := plan["takes"].([]any)
	if len(takes) != 1 {
		t.Fatalf("合法块应恰 1 组(其余降级), got %v", plan["takes"])
	}
	if g, _ := takes[0].([]any); len(g) != 2 || g[0] != 1 || g[1] != 2 {
		// json.Unmarshal 数值为 float64,双形态兼容
		if len(g) != 2 {
			t.Fatalf("组内应 [1,2], got %v", g)
		}
	}
	// script_parse_ver 已带当前代数
	if v, _ := manjuToInt(plan["script_parse_ver"]); v != manjuScriptParseVer {
		t.Fatalf("script_parse_ver = %v, want %d", plan["script_parse_ver"], manjuScriptParseVer)
	}

	shots := mustPlanShots(t, plan)
	ctx.ensureTakes(plan, shots, lg) // 脚本直出分支必须保留源头块
	if plan["takes"] == nil {
		t.Fatalf("源头叙事块被 ensureTakes 误清除")
	}
	shots = applyTakes(plan, shots)
	byID := map[int]manjuShot{}
	for _, s := range shots {
		byID[s.ID] = s
	}
	head, tail := byID[1], byID[2]
	if head.Duration != 11 {
		t.Fatalf("组头时长应=组和 6+5=11, got %d", head.Duration)
	}
	if !strings.Contains(head.H3Prompt, "block prompt covering both") {
		t.Fatalf("组头提示词应被块级六段式覆盖, got %.60s", head.H3Prompt)
	}
	if len(head.TakeGroup) != 2 {
		t.Fatalf("组头 TakeGroup 应 2 镜, got %d", len(head.TakeGroup))
	}
	if !tail.TakeTail {
		t.Fatalf("内镜 2 应标 TakeTail")
	}
	// 组内登场/台词并入组头(音色绑定与 QC 预期全组覆盖)
	if !strings.Contains(strings.Join(head.Characters, ","), "何大勇") {
		t.Fatalf("组头登场应并入组内说话人, got %v", head.Characters)
	}
	if !strings.Contains(head.Narration, "递了十万九千五百碗水") || !strings.Contains(head.Dialogue, "善缘簿") {
		t.Fatalf("组头台词/旁白未并组: D=%.40s N=%.40s", head.Dialogue, head.Narration)
	}
	// take_group 标记落 plan(合成镜数按组头计)
	for _, x := range anyArr(plan["shots"]) {
		if m, ok := x.(map[string]any); ok {
			if id, _ := manjuToInt(m["shot_id"]); id == 1 && m["take_group"] != true {
				t.Fatalf("组头 plan 条目应带 take_group=true")
			}
		}
	}
	// 选中集:默认 3 个渲染单元(块头+镜3+镜4)
	if sel := ctx.selectedShots(shots); len(sel) != 3 || sel[0].ID != 1 || sel[1].ID != 3 {
		t.Fatalf("默认渲染单元应 [块1,3,4], got %v", idsOf(sel))
	}
}

// 定点重渲:块内镜号映射到组头整块(防静默空跑)
func TestNarrativeBlockOnlyPromotesHead(t *testing.T) {
	ctx := blockCtx(t)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots := applyTakes(plan, mustPlanShots(t, plan))
	ctx2 := ctx
	ctx2.only = "2"
	sel := ctx2.selectedShots(shots)
	if len(sel) != 1 || sel[0].ID != 1 || sel[0].Duration != 11 {
		t.Fatalf("定点内镜 2 应映射组头整块(时长 11), got %v", idsOf(sel))
	}
	ctx3 := ctx
	ctx3.only = "3,4"
	if sel := ctx3.selectedShots(shots); len(sel) != 2 {
		t.Fatalf("定点未入块镜应逐镜, got %v", idsOf(sel))
	}
}

// 无叙事块脚本:脚本直出清除启发式 takes 的自愈行为不变(万界召唤 16→8 回归)
func TestNarrativeBlockAbsentKeepsLegacySelfHeal(t *testing.T) {
	ctx := wanjieStyleCtx(t, `"shots_per_take":2`)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	if plan["takes_src"] == "blocks" {
		t.Fatalf("无 narrative_blocks 的脚本不应有 takes_src=blocks")
	}
	plan["takes"] = []any{[]any{1, 2}} // 模拟旧启发式分组
	ctx.ensureTakes(plan, mustPlanShots(t, plan), lg)
	if plan["takes"] != nil {
		t.Fatalf("旧启发式 takes 应被清除(自愈),仍存在: %v", plan["takes"])
	}
}

// 校验矩阵:镜号缺失/不连续/单镜/无切点 → 降级逐镜(绝不丢镜)
func TestNarrativeBlockValidationMatrix(t *testing.T) {
	cases := []struct {
		name  string
		block scriptJSONBlock
		ok    bool
	}{
		{"合法两镜", scriptJSONBlock{Shots: []int{1, 2}, H3Prompt: "x [Shot 2] At 00:05.000"}, true},
		{"合法三镜", scriptJSONBlock{Shots: []int{2, 3, 4}, H3Prompt: "x [Shot 3] At 00:10.000"}, true},
		{"镜号缺失", scriptJSONBlock{Shots: []int{1, 99}, H3Prompt: "x [Shot 2]"}, false},
		{"镜号不连续", scriptJSONBlock{Shots: []int{1, 3}, H3Prompt: "x [Shot 2]"}, false},
		{"单镜非块", scriptJSONBlock{Shots: []int{1}, H3Prompt: "x [Shot 1]"}, false},
		{"无切点标记", scriptJSONBlock{Shots: []int{1, 2}, H3Prompt: "no cut marker"}, false},
		{"超15s上限", scriptJSONBlock{Shots: []int{1, 2, 3, 4}, H3Prompt: "x [Shot 2]"}, false}, // 6+5+5+4=20
	}
	plan := map[string]any{"shots": []any{
		map[string]any{"shot_id": 1, "duration": 6},
		map[string]any{"shot_id": 2, "duration": 5},
		map[string]any{"shot_id": 3, "duration": 5},
		map[string]any{"shot_id": 4, "duration": 4},
	}}
	for _, c := range cases {
		p := map[string]any{"shots": plan["shots"]}
		attachNarrativeBlocks(p, []scriptJSONBlock{c.block}, &manjuLogger{state: manjuState})
		got := p["takes"] != nil
		if got != c.ok {
			t.Fatalf("%s: 保留=%v, want %v", c.name, got, c.ok)
		}
	}
}

// 块间重叠:后块引用已用镜号 → 降级,先到先得
func TestNarrativeBlockOverlapDropped(t *testing.T) {
	plan := map[string]any{"shots": []any{
		map[string]any{"shot_id": 1, "duration": 5},
		map[string]any{"shot_id": 2, "duration": 5},
		map[string]any{"shot_id": 3, "duration": 5},
	}}
	attachNarrativeBlocks(plan, []scriptJSONBlock{
		{Shots: []int{1, 2}, H3Prompt: "a [Shot 2]"},
		{Shots: []int{2, 3}, H3Prompt: "b [Shot 2]"},
	}, &manjuLogger{state: manjuState})
	takes, _ := plan["takes"].([]any)
	if len(takes) != 1 {
		t.Fatalf("重叠块应只保留首个, got %v", plan["takes"])
	}
}

// 幽灵人声组感知:头静尾说的块不得进候选(纯函数级:applyTakes 后组头 Dialogue 并组)
func TestNarrativeBlockGhostEligibilityUnion(t *testing.T) {
	ctx := blockCtx(t)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots := applyTakes(plan, mustPlanShots(t, plan))
	for _, s := range shots {
		if s.ID == 1 {
			// 组头自身无台词列(旁白在)→ 并组后仍有台词内容,ghostVoiceEligible 应 false
			if ghostVoiceEligible(s.Dialogue, s.Narration, s.H3Prompt) {
				t.Fatalf("覆盖组内台词的组头不应判为无台词镜(防幽灵误检重渲循环)")
			}
		}
	}
}

// 幂等:二次解析/应用结果稳定(缓存与提示词稳定性依赖)
func TestNarrativeBlockIdempotent(t *testing.T) {
	ctx := blockCtx(t)
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	b1, _ := json.Marshal(plan["narrative_blocks"])
	shots1 := applyTakes(plan, mustPlanShots(t, plan))
	ctx.ensureTakes(plan, mustPlanShots(t, plan), lg)
	shots2 := applyTakes(plan, mustPlanShots(t, plan))
	if len(shots1) != len(shots2) {
		t.Fatalf("二次应用镜数漂移: %d vs %d", len(shots1), len(shots2))
	}
	b2, _ := json.Marshal(plan["narrative_blocks"])
	if string(b1) != string(b2) {
		t.Fatalf("narrative_blocks 二次运行不稳定")
	}
}

// 真实数据回归:被论斤第002章(LLM 返工 narrative_blocks 后)全链解析——
// 脚本指纹重解析 → takes 源头化 → applyTakes 块覆盖组头 → 选中集=渲染单元数
func TestNarrativeBlockRealChapter(t *testing.T) {
	const sb = `D:/Ai/NiliX/novel/被论斤卖掉后我成了全网AI之母/素材/分镜脚本/第002章_最后一次查房_分镜脚本.json`
	if _, err := os.Stat(sb); err != nil {
		t.Skip("真实分镜不在本机,跳过")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"script":"`+filepath.ToSlash(sb)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "EP02", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	takes := anyArr(plan["takes"])
	if len(takes) == 0 {
		t.Fatalf("真实叙事块章节应产出非空 takes")
	}
	blocks := anyArr(plan["narrative_blocks"])
	if len(blocks) != len(takes) {
		t.Fatalf("narrative_blocks(%d) 与 takes(%d) 数量不一致", len(blocks), len(takes))
	}
	shots := applyTakes(plan, mustPlanShots(t, plan))
	nUnit, nCovered, withCut := 0, 0, 0
	for _, s := range ctx.selectedShots(shots) {
		nUnit++
		if len(s.TakeGroup) >= 2 {
			nCovered += len(s.TakeGroup)
			if strings.Contains(s.H3Prompt, "[Shot 2] At ") {
				withCut++
			}
			if s.Duration > 15 {
				t.Fatalf("块 %d 时长 %d 超 15", s.ID, s.Duration)
			}
		}
	}
	t.Logf("真实章: %d 渲染单元(块 %d 覆盖 %d 镜,含官方切点块 %d)",
		nUnit, len(takes), nCovered, withCut)
	if withCut == 0 {
		t.Fatalf("没有任何块头携带 [Shot 2] At 官方切点")
	}
}

// 块级一镜实渲试点(渲染验收铁律:渲染输出类改动必须端到端实跑,禁纸面整合)。
// 环境变量 NILIX_BLOCK_PILOT=1 才执行——真提交本地 ComfyUI(排队不抢占),
// 渲染递葫芦 EP02(第002章已 narrative_blocks 返工)第一个块头,断言:
// 产物存在 + 时长≈组内和 + ASR 可辨组内台词(多切点生成的人声/画面行为验证)。
func TestNarrativeBlockRenderPilot(t *testing.T) {
	if os.Getenv("NILIX_BLOCK_PILOT") == "" {
		t.Skip("设 NILIX_BLOCK_PILOT=1 执行真渲染试点")
	}
	const cfg = `D:/Ai/NiliX/manju/递了三千年葫芦，她给自己发了飞升任务/config.json`
	if _, err := os.Stat(cfg); err != nil {
		t.Skip("递葫芦项目不在本机")
	}
	ctx, err := newManjuCtx(cfg, "EP02", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	_, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		t.Fatalf("ensurePlanAndPrompts: %v", err)
	}
	var head *manjuShot
	want := 0
	for i := range shots {
		if len(shots[i].TakeGroup) >= 2 {
			head = &shots[i]
			for _, g := range shots[i].TakeGroup {
				want += g.Duration
			}
			break
		}
	}
	if head == nil {
		t.Fatalf("EP02 应有叙事块(第002章已返工)")
	}
	t.Logf("试点块头: 镜 %d 组 %d 镜 时长和 %ds", head.ID, len(head.TakeGroup), want)
	dst := filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", head.ID))
	_ = os.Remove(dst)
	if err := ctx.renderShotTo(*head, head.ID, false, filepath.Dir(dst), ctx.w, ctx.h, 0, lg); err != nil {
		t.Fatalf("renderShotTo: %v", err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() == 0 {
		t.Fatalf("产物缺失/0字节: %s", dst)
	} else {
		t.Logf("块产物: %s (%d bytes) —— 人工核验多切点切换与组内台词", dst, fi.Size())
	}
}

func idsOf(ss []manjuShot) []int {
	out := make([]int, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
