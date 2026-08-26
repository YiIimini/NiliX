package api

// 2026-08-26 管线升级批次测试:
//   P0-2 语音预算(dialogue+narration 合并、chars_per_sec 可配)
//   P0-3 min/max_shot_seconds 激活(validatePlan 用配置区间、脚本模式仍按 API 硬域)
//   P1-6/P1-7 拆镜密度与时长规则注入提示词、每镜 ≤3 角色/对白 ≤20 字
//   P1-5 双形态切换判定
//   P2-8 立项.render 变更自动同步
//   P2-9 方案软告警体检项

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManjuSpeechChars(t *testing.T) {
	// dialogue 每行去说话人前缀(中英文冒号),narration 去「旁白:」前缀,标点不计
	got := manjuSpeechChars("甲:你好，世界！\n乙：今天天气不错。", "旁白：临江老街，晚膳小馆。")
	if want := 4 + 6 + 8; got != want {
		t.Fatalf("manjuSpeechChars = %d, want %d", got, want)
	}
	if manjuSpeechChars("", "") != 0 {
		t.Fatalf("空输入应 0")
	}
}

func TestValidatePlanUpgrades(t *testing.T) {
	mkPlan := func(ids ...string) map[string]any {
		cs := []any{}
		for _, id := range ids {
			cs = append(cs, map[string]any{"id": id})
		}
		return map[string]any{"characters": cs}
	}
	join := func(ps []string) string { return strings.Join(ps, "\n") }

	// 1) 时长区间激活:LLM 模式用 ctx.minSec/maxSec(13s 超 12 上限);脚本模式仍按 4-15 硬域(13s 合法)
	ctx := &manjuCtx{minSec: 4, maxSec: 12, charsPerSec: 4.0}
	probs := ctx.validatePlan(mkPlan("甲"), []manjuShot{{ID: 1, Duration: 13, Characters: []string{"甲"}}})
	if !strings.Contains(join(probs), "超出 4-12") {
		t.Fatalf("LLM 模式应按配置区间 4-12 报时长问题, got %v", probs)
	}
	ctx.scriptMode = true
	probs = ctx.validatePlan(mkPlan("甲"), []manjuShot{{ID: 1, Duration: 13, Characters: []string{"甲"}}})
	if strings.Contains(join(probs), "超出") {
		t.Fatalf("脚本模式 13s 属 API 硬域 4-15,不应报时长问题, got %v", probs)
	}
	ctx.scriptMode = false

	// 2) 旁白语音预算:narration 30 字 4s 镜(旧版只算 dialogue,旁白零校验)→ 现在必须报
	probs = ctx.validatePlan(mkPlan("甲"), []manjuShot{{
		ID: 2, Duration: 4, Characters: []string{"甲"},
		Dialogue: "甲:好的。", Narration: "旁白：" + strings.Repeat("长", 30),
	}})
	if !strings.Contains(join(probs), "台词+旁白") {
		t.Fatalf("旁白应纳入语音预算, got %v", probs)
	}

	// 3) 每镜 ≤3 角色
	probs = ctx.validatePlan(mkPlan("甲", "乙", "丙", "丁"), []manjuShot{{
		ID: 3, Duration: 5, Characters: []string{"甲", "乙", "丙", "丁"},
	}})
	if !strings.Contains(join(probs), "超 3") {
		t.Fatalf("4 角色镜应报问题, got %v", probs)
	}

	// 4) 对白 ≤20 字/句
	probs = ctx.validatePlan(mkPlan("甲"), []manjuShot{{
		ID: 4, Duration: 15, Characters: []string{"甲"},
		Dialogue: "甲:" + strings.Repeat("话", 25),
	}})
	if !strings.Contains(join(probs), "超 20 字") {
		t.Fatalf("25 字对白应报问题, got %v", probs)
	}

	// 5) 合规镜零问题
	probs = ctx.validatePlan(mkPlan("甲", "乙"), []manjuShot{{
		ID: 5, Duration: 6, Characters: []string{"甲"},
		Dialogue: "甲:你敢动她试试？", Narration: "旁白：夜色渐深。",
	}})
	if len(probs) != 0 {
		t.Fatalf("合规镜应零问题, got %v", probs)
	}
}

func TestManjuDurationRule(t *testing.T) {
	// 配置注入:min/max/chars_per_sec 生效
	cfg := map[string]any{"render": map[string]any{
		"min_shot_seconds": 3, "max_shot_seconds": 10, "chars_per_sec": 5,
	}}
	rule := manjuDurationRule(cfg)
	if !strings.Contains(rule, "(3-10 秒)") || !strings.Contains(rule, "约 5 字/秒") {
		t.Fatalf("时长规则应注入配置区间/字速, got: %s", rule)
	}
	if !strings.Contains(rule, "拆镜密度") || !strings.Contains(rule, "11-17 镜") {
		t.Fatalf("拆镜密度指引缺失, got: %s", rule)
	}
	// 默认:4-12 / 4 字每秒
	rule = manjuDurationRule(map[string]any{})
	if !strings.Contains(rule, "(4-12 秒)") || !strings.Contains(rule, "约 4 字/秒") {
		t.Fatalf("默认应为 4-12 / 4 字每秒, got: %s", rule)
	}
	// 两个系统提示词占位符都已替换(防漏 Replace 导致字面量入 prompt)
	for name, sys := range map[string]string{
		"direct": manjuDirectSystem(map[string]any{}, "real"),
		"script": manjuScriptSystem(map[string]any{}, "real"),
	} {
		if strings.Contains(sys, "{MANJU_DURATION_RULE}") {
			t.Fatalf("%s 系统提示词占位符未替换", name)
		}
		if !strings.Contains(sys, "【时长硬约束】") {
			t.Fatalf("%s 系统提示词缺时长硬约束", name)
		}
	}
}

func TestShotWantsTrueForm(t *testing.T) {
	if !shotWantsTrueForm(manjuShot{Narration: "真身·小白：雷光缠身。"}, "小白") {
		t.Fatalf("「真身·角色名」前缀应触发切换")
	}
	if !shotWantsTrueForm(manjuShot{Action: "小白的兽形真身显现，雷光缠身"}, "小白") {
		t.Fatalf("画面含 真身+角色名 应触发切换")
	}
	if shotWantsTrueForm(manjuShot{Action: "小白趴在云晚肩头打盹", Narration: "旁白：日子安稳。", H3Prompt: "xiaobai sleeps"}, "小白") {
		t.Fatalf("无真身关键词不应切换")
	}
}

func TestSyncLixiRenderPlan(t *testing.T) {
	dir := t.TempDir()
	novelDir := filepath.Join(dir, "novel")
	_ = os.MkdirAll(novelDir, 0o755)
	_ = os.WriteFile(filepath.Join(novelDir, "立项.json"), []byte(`{"render":{"chars_per_sec":5,"min_shot_seconds":3,"style":"real"}}`), 0o644)

	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"novel":"`+filepath.ToSlash(novelDir)+`"},"render":{}}`), 0o644)

	// 首次:立项规划合并 + 提示消息
	if msg := syncLixiRenderPlan(cfgPath); msg == "" {
		t.Fatalf("立项变更应产生同步提示")
	}
	cfg, _ := readManjuConfig(cfgPath)
	R, _ := cfg["render"].(map[string]any)
	if f, _ := manjuToFloat(R["chars_per_sec"]); f != 5 {
		t.Fatalf("chars_per_sec=5 应合并, got %+v", R)
	}
	if str(R["_lixi_fp"]) == "" {
		t.Fatalf("应记录立项指纹")
	}
	// 幂等:指纹一致时不再同步
	if msg := syncLixiRenderPlan(cfgPath); msg != "" {
		t.Fatalf("指纹一致不应重复同步, got %q", msg)
	}
	// 立项变更(mtime 变)后再次同步;显式配置不被覆盖
	_ = os.WriteFile(filepath.Join(novelDir, "立项.json"), []byte(`{"render":{"chars_per_sec":6,"max_shot_seconds":8}}`), 0o644)
	os.Chtimes(filepath.Join(novelDir, "立项.json"), time.Now(), time.Now())
	if msg := syncLixiRenderPlan(cfgPath); msg == "" {
		t.Fatalf("立项更新应再次同步")
	}
	cfg, _ = readManjuConfig(cfgPath)
	R, _ = cfg["render"].(map[string]any)
	if f, _ := manjuToFloat(R["chars_per_sec"]); f != 5 {
		t.Fatalf("已有显式值不被覆盖(5 保留), got %+v", R)
	}
	if n, _ := manjuToInt(R["max_shot_seconds"]); n != 8 {
		t.Fatalf("新键应合并(8 引入), got %+v", R)
	}
}

func TestManjuPlanAuditItem(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: dir, minSec: 4, maxSec: 12}
	// 无方案 → ok 占位
	if it := manjuPlanAuditItem(ctx); it.Status != "ok" {
		t.Fatalf("无方案应为 ok, got %+v", it)
	}
	// 1 集:13s 超上限 + 4 角色 + 25 字句 + 重复提示词
	plan := map[string]any{
		"characters": []any{map[string]any{"id": "甲"}, map[string]any{"id": "乙"}, map[string]any{"id": "丙"}, map[string]any{"id": "丁"}},
		"shots": []any{
			map[string]any{"shot_id": 1, "duration": 13, "characters": []any{"甲", "乙", "丙", "丁"}, "dialogue": "甲:" + strings.Repeat("话", 25), "h3_prompt": "Cinematic A"},
			map[string]any{"shot_id": 2, "duration": 6, "characters": []any{"甲"}, "h3_prompt": "Cinematic A"},
		},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(dir, "EP01_direct_plan.json"), b, 0o644)
	it := manjuPlanAuditItem(ctx)
	if it.Status != "warn" {
		t.Fatalf("有违规应 warn, got %+v", it)
	}
	for _, want := range []string{"超时长上限", "超 3", "超 20 字", "提示词重复"} {
		if !strings.Contains(it.Detail, want) {
			t.Fatalf("体检明细缺 %q: %s", want, it.Detail)
		}
	}
}
