package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 回归:comfy.wait 停止感知——用户点「停止」后快速返回,不再死等 ComfyUI。
// 根因:停止只发 /interrupt(仅中断正在采样的任务),卡在模型加载/排队的任务
// 无法被中断,旧 wait 会等满超时(3600s)→ "点停止没反应、还在渲染"。
func TestComfyWaitStoppedFast(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`)) // 空 history:模拟任务卡在加载/排队
	}))
	defer svr.Close()
	c := newComfyClient(svr.URL)
	stopped := false
	t0 := time.Now()
	done := make(chan error, 1)
	go func() { done <- c.wait("p", 3600*time.Second, 10*time.Second, func() bool { return stopped }) }()
	time.Sleep(300 * time.Millisecond)
	stopped = true // 模拟用户点停止
	select {
	case err := <-done:
		if err == nil || err.Error() != "已停止" {
			t.Fatalf("应返回已停止, got %v", err)
		}
		if d := time.Since(t0); d > 2*time.Second {
			t.Fatalf("停止响应过慢: %v(应 500ms 内)", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait 未在停止后返回(死等)")
	}
}

// 回归:正常等待时(未停止)wait 保持轮询直到任务完成
func TestComfyWaitCompletes(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"p":{"status":{"status_str":"success","completed":true}}}`))
	}))
	defer svr.Close()
	c := newComfyClient(svr.URL)
	err := c.wait("p", 10*time.Second, 100*time.Millisecond, func() bool { return false })
	if err != nil {
		t.Fatalf("任务完成应返回 nil, got %v", err)
	}
}
