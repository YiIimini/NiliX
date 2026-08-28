package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 审计 2026-08-28 回归:HTTP handler panic 必须返回 500 JSON(此前 net/http 默认断连,
// 前端收到连接重置且无 crash 日志),不能向上抛导致整个服务挂掉。
func TestRecoverPanicMiddleware(t *testing.T) {
	s := &Server{}
	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("模拟 handler 崩溃")
	})
	ts := httptest.NewServer(s.recoverPanic(boom))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("请求应正常返回而非连接重置: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("应返回 500,得到 %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("响应应为合法 JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatal("响应应含 error 字段")
	}
}

// 审计 2026-08-28 回归:panic 中间件不干扰正常请求。
func TestRecoverPanicPassthrough(t *testing.T) {
	s := &Server{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
	})
	ts := httptest.NewServer(s.recoverPanic(ok))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正常请求应 200,得到 %d", resp.StatusCode)
	}
}
