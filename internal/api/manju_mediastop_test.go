package api

import (
	"strings"
	"testing"
	"time"
)

// 回归:runMediaOutStop 停止感知——停止回调触发时立即杀子进程并返回"已停止"
// (ASR/inspect 子进程卡住时,用户点「停止」不再等 25 分钟超时)
func TestRunMediaOutStop(t *testing.T) {
	py := manjuPythonPath()
	if !fileExists(py) {
		t.Skip("无 python 环境")
	}
	ctx := &manjuCtx{}
	// 用 media helper 的 probe 子命令(会快速完成),但停止回调先触发 → 应返回"已停止"
	stopped := true // 一开始就停止:验证停止优先
	t0 := time.Now()
	out, err := ctx.runMediaOutStop([]string{"probe", "--file", "C:\\Windows\\win.ini"}, func() bool { return stopped })
	_ = out
	if err == nil || !strings.Contains(err.Error(), "已停止") {
		t.Fatalf("停止应优先返回已停止, got %v", err)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("停止响应过慢: %v", d)
	}
}
