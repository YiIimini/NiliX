package api

// 体检「残留配置与日志」一键清理回归(2026-09-02):
// ① run_state.json 残留(崩溃/停止/完成/损坏)体检识别 + 一键删除 + 幂等 + 运行中保护
// ② agent_state.json 损坏一键删除(完好记忆不误删)
// ③ 运行日志/崩溃日志/诊断快照超阈值一键清空 + 幂等
// ④ manjuFixAll 全量覆盖新 case(体检弹窗「🔧 一键修复」与聊天「修复」共用入口)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHealthResidueProj 构造临时项目(位于 ManjuRootDir 下:run_state/run.log 均按
// ManjuRootDir/<项目>/ 解析,TempDir 无法覆盖),返回 (项目名, config路径, 清理函数)
func newHealthResidueProj(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(ManjuRootDir, name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"style":"2.5d","render":{},"paths":{"workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	return name, cfg
}

// healthItemByKey 从体检清单里取指定项
func healthItemByKey(t *testing.T, ctx *manjuCtx, key string) manjuHealthItem {
	t.Helper()
	for _, it := range manjuHealthCheck(ctx) {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("体检缺少 %s 项", key)
	return manjuHealthItem{}
}

// fakeRunning 临时把全局任务标记为「该项目运行中」(测运行中保护),defer 自动复位
func fakeRunning(t *testing.T, project string) {
	t.Helper()
	manjuState.mu.Lock()
	manjuState.running, manjuState.project = true, project
	manjuState.mu.Unlock()
	t.Cleanup(func() {
		manjuState.mu.Lock()
		manjuState.running, manjuState.project = false, ""
		manjuState.mu.Unlock()
	})
}

func TestHealthRunStateResidue(t *testing.T) {
	proj, cfg := newHealthResidueProj(t, "zz_health_resd_test")
	ctx, err := newManjuCtx(cfg, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}

	// 1) 无文件 → ok
	if it := healthItemByKey(t, ctx, "run_state"); it.Status != "ok" {
		t.Fatalf("无 run_state 应 ok: %+v", it)
	}

	// 2) 完成残留 → warn + 可修复;一键删除后回到 ok
	writeManjuDiskState(proj, &manjuDiskState{Done: true, UpdatedAt: time.Now().Unix()})
	it := healthItemByKey(t, ctx, "run_state")
	if it.Status != "warn" || !it.Fixable {
		t.Fatalf("完成残留应 warn+fixable: %+v", it)
	}
	if changed, ferr := manjuApplyHealthFix(cfg, "run_state"); ferr != nil || !changed {
		t.Fatalf("一键清理失败: changed=%v err=%v", changed, ferr)
	}
	if fileExists(manjuRunStatePath(proj)) {
		t.Fatal("run_state.json 未删除")
	}
	if it := healthItemByKey(t, ctx, "run_state"); it.Status != "ok" {
		t.Fatalf("清理后应 ok: %+v", it)
	}

	// 3) 崩溃残留(Running=true 但无任务)→ bad + 可修复
	writeManjuDiskState(proj, &manjuDiskState{Running: true, UpdatedAt: time.Now().Unix()})
	if it := healthItemByKey(t, ctx, "run_state"); it.Status != "bad" || !it.Fixable {
		t.Fatalf("崩溃残留应 bad+fixable: %+v", it)
	}

	// 4) 损坏 JSON → bad + 可修复;修复后文件删除
	_ = os.WriteFile(manjuRunStatePath(proj), []byte("{not json"), 0644)
	if it := healthItemByKey(t, ctx, "run_state"); it.Status != "bad" || !it.Fixable {
		t.Fatalf("损坏状态应 bad+fixable: %+v", it)
	}
	if changed, ferr := manjuApplyHealthFix(cfg, "run_state"); ferr != nil || !changed {
		t.Fatalf("损坏状态一键清理失败: %v %v", changed, ferr)
	}

	// 5) 幂等:无残留时再修不报错、不误报改动
	if changed, ferr := manjuApplyHealthFix(cfg, "run_state"); ferr != nil || changed {
		t.Fatalf("幂等失败: changed=%v err=%v", changed, ferr)
	}

	// 6) 运行中保护:活状态拒绝清理
	writeManjuDiskState(proj, &manjuDiskState{Done: true, Stopped: true, UpdatedAt: time.Now().Unix()})
	fakeRunning(t, proj)
	if it := healthItemByKey(t, ctx, "run_state"); it.Status != "ok" || it.Fixable {
		t.Fatalf("运行中应为 ok(活状态)且不可清: %+v", it)
	}
	if _, ferr := manjuApplyHealthFix(cfg, "run_state"); ferr == nil || !strings.Contains(ferr.Error(), "渲染中") {
		t.Fatalf("运行中清理应被拒绝,got: %v", ferr)
	}
}

func TestHealthAgentStateCorruptFix(t *testing.T) {
	proj, cfg := newHealthResidueProj(t, "zz_health_astate_test")
	ctx, err := newManjuCtx(cfg, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}

	// 1) 损坏文件 → bad + 可一键删除;删除后体检回到「尚无审片记录」
	_ = os.WriteFile(manjuAgentStatePath(proj), []byte("{{{broken"), 0644)
	it := healthItemByKey(t, ctx, "agent_state")
	if it.Status != "bad" || !it.Fixable {
		t.Fatalf("损坏 agent_state 应 bad+fixable: %+v", it)
	}
	if changed, ferr := manjuApplyHealthFix(cfg, "agent_state"); ferr != nil || !changed {
		t.Fatalf("一键删除失败: %v %v", changed, ferr)
	}
	if fileExists(manjuAgentStatePath(proj)) {
		t.Fatal("损坏 agent_state.json 未删除")
	}
	if it := healthItemByKey(t, ctx, "agent_state"); it.Status != "warn" || it.Detail != "尚无审片记录" {
		t.Fatalf("删除后应回到尚无记录: %+v", it)
	}

	// 2) 完好记忆不误删:合法 JSON 时修复为幂等无改动
	_ = os.WriteFile(manjuAgentStatePath(proj), []byte(`{"shots":{},"memory":{"runCount":3}}`), 0644)
	if changed, ferr := manjuApplyHealthFix(cfg, "agent_state"); ferr != nil || changed {
		t.Fatalf("完好文件不应被删: changed=%v err=%v", changed, ferr)
	}
	if !fileExists(manjuAgentStatePath(proj)) {
		t.Fatal("完好 agent_state.json 被误删")
	}

	// 3) 运行中保护
	fakeRunning(t, proj)
	_ = os.WriteFile(manjuAgentStatePath(proj), []byte("{{{broken"), 0644)
	if it := healthItemByKey(t, ctx, "agent_state"); it.Fixable {
		t.Fatalf("运行中损坏项不应标 fixable(审片链路在写): %+v", it)
	}
	if _, ferr := manjuApplyHealthFix(cfg, "agent_state"); ferr == nil {
		t.Fatal("运行中清理应被拒绝")
	}
}

func TestHealthLogsResidueFix(t *testing.T) {
	proj, cfg := newHealthResidueProj(t, "zz_health_logs_test")
	ctx, err := newManjuCtx(cfg, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	_ = os.MkdirAll(manjuDiagnoseDir(), 0755)

	// 1) 小日志 → ok 不可修
	_ = os.WriteFile(manjuRunLogPath(proj), []byte("log line\n"), 0644)
	if it := healthItemByKey(t, ctx, "logs"); it.Status != "ok" || it.Fixable {
		t.Fatalf("小日志应 ok: %+v", it)
	}

	// 2) 超阈值(run.log>256KB / crash.log>64KB)→ warn + 一键清空
	//    crash.log 是平台级真实文件:备份-清理-断言-恢复
	big := strings.Repeat("x", 300<<10)
	_ = os.WriteFile(manjuRunLogPath(proj), []byte(big), 0644)
	crashPath := filepath.Join(ManjuRootDir, "logs", "crash.log")
	crashBak, crashHad := []byte(nil), false
	if b, err := os.ReadFile(crashPath); err == nil {
		crashBak, crashHad = b, true
	}
	_ = os.WriteFile(crashPath, []byte(strings.Repeat("c", 70<<10)), 0644)
	diagPath := manjuDiagnosePath(proj)
	_ = os.WriteFile(diagPath, []byte(`{"project":"`+proj+`"}`), 0644)
	defer func() { // 恢复平台 crash.log 原状
		if crashHad {
			_ = os.WriteFile(crashPath, crashBak, 0644)
		} else {
			_ = os.Remove(crashPath)
		}
	}()

	it := healthItemByKey(t, ctx, "logs")
	if it.Status != "warn" || !it.Fixable {
		t.Fatalf("超阈值日志应 warn+fixable: %+v", it)
	}
	if changed, ferr := manjuApplyHealthFix(cfg, "logs"); ferr != nil || !changed {
		t.Fatalf("一键清空失败: %v %v", changed, ferr)
	}
	if fi, _ := os.Stat(manjuRunLogPath(proj)); fi.Size() != 0 {
		t.Fatal("run.log 未截断")
	}
	if fi, _ := os.Stat(crashPath); fi.Size() != 0 {
		t.Fatal("crash.log 未截断")
	}
	if fileExists(diagPath) {
		t.Fatal("诊断快照未删除")
	}
	if it := healthItemByKey(t, ctx, "logs"); it.Status != "ok" {
		t.Fatalf("清理后应 ok: %+v", it)
	}

	// 3) 幂等:再修不误报改动
	if changed, ferr := manjuApplyHealthFix(cfg, "logs"); ferr != nil || changed {
		t.Fatalf("logs 幂等失败: changed=%v err=%v", changed, ferr)
	}

	// 4) 运行中保护
	fakeRunning(t, proj)
	if it := healthItemByKey(t, ctx, "logs"); it.Fixable {
		t.Fatalf("运行中日志项不应标 fixable: %+v", it)
	}
	if _, ferr := manjuApplyHealthFix(cfg, "logs"); ferr == nil {
		t.Fatal("运行中清理日志应被拒绝")
	}
}

func TestFixAllCoversResidue(t *testing.T) {
	proj, cfg := newHealthResidueProj(t, "zz_health_fixall_test")
	// 三类残留同时存在 + 一个参数类可修复项(seed 空)
	writeManjuDiskState(proj, &manjuDiskState{Done: true, UpdatedAt: time.Now().Unix()})
	_ = os.WriteFile(manjuAgentStatePath(proj), []byte("{{{broken"), 0644)
	_ = os.WriteFile(manjuRunLogPath(proj), []byte(strings.Repeat("x", 300<<10)), 0644)

	fixed, errs := manjuFixAll(cfg)
	if len(errs) > 0 {
		t.Fatalf("fixall 报错: %v", errs)
	}
	joined := strings.Join(fixed, ",")
	for _, want := range []string{"随机种子", "审片状态文件", "运行状态文件", "运行日志"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fixall 未覆盖 %s: %v", want, fixed)
		}
	}
	if fileExists(manjuRunStatePath(proj)) || fileExists(manjuAgentStatePath(proj)) {
		t.Fatal("残留状态文件未清理")
	}
	if fi, _ := os.Stat(manjuRunLogPath(proj)); fi != nil && fi.Size() != 0 {
		t.Fatal("run.log 未截断")
	}
	// 再跑一遍:全部幂等,无可修复项
	fixed2, errs2 := manjuFixAll(cfg)
	if len(errs2) > 0 || len(fixed2) != 0 {
		t.Fatalf("二轮 fixall 应无动作: %v %v", fixed2, errs2)
	}
}
