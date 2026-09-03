package manju

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"testing"
)

// 清空日志三清(2026-08-28 修复「清空日志按钮无效」):只清内存时,空闲轮询的 logTail
// 从磁盘(run_state.json logTail 字段 + run.log 文件)读回,2 秒后旧日志必回弹。
func TestLogClearThreeWay(t *testing.T) {
	proj := "zz_logclear_test"
	dir := filepath.Join(paths.ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"style":"2.5d","render":{},"paths":{"workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	_ = os.WriteFile(manjuRunLogPath(proj), []byte("━━━ 阶段 plan ━━━\n旧日志行\n"), 0644)
	writeManjuDiskState(proj, &manjuDiskState{LogTail: "旧日志行", Done: true, Stopped: true})

	w, out := doReq(t, "POST", "/api/manju/log/clear", map[string]any{"config": cfg})
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("清空日志 HTTP %d %v", w.Code, out)
	}
	if out["runLog"] != true || out["runState"] != true {
		t.Fatalf("磁盘双清未执行(只清了内存=无效复发): %v", out)
	}
	if b, _ := os.ReadFile(manjuRunLogPath(proj)); len(b) != 0 {
		t.Fatalf("run.log 未截断: %q", string(b))
	}
	if ds := loadManjuDiskState(proj); ds == nil || ds.LogTail != "" {
		t.Fatalf("run_state logTail 未清: %+v", ds)
	}
	// 中断横幅驱动字段(Stopped/Done)不受清日志影响(清日志≠清运行状态)
	if ds := loadManjuDiskState(proj); ds == nil || !ds.Stopped {
		t.Fatalf("清日志不应重置运行状态字段")
	}
}
