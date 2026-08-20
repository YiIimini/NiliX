package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 回归:freeComfyModels 释放显存——编码前必须卸载 assets 阶段驻留的 ZImage/Lumina,
// 否则 Qwen3-VL 32B 加载因显存不足阻塞 → 编码任务提交后 ComfyUI 挂起、GPU 无动静。
func TestFreeComfyModels(t *testing.T) {
	var gotBody string
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/free" || r.Method != "POST" {
			t.Errorf("应 POST /free, got %s %s", r.Method, r.URL.Path)
		}
		buf := make([]byte, 200)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer svr.Close()

	ctx := &manjuCtx{comfy: newComfyClient(svr.URL)}
	lg := &manjuLogger{state: manjuState}
	ctx.freeComfyModels(lg) // 不 panic 且请求正确
	if gotBody == "" || gotBody != `{"unload_models": true, "free_memory": true}` {
		t.Fatalf("free 请求体异常: %q", gotBody)
	}
}
