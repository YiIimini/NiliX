package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVisionModelChainFallback(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = decodeJSONBody(r, &body)
		calls = append(calls, body.Model)
		if body.Model == "glm-4.6v-flash" {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok-from-backup"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	// 单模型自动补内置降级链;为测试加速,退避清零
	old := visionBackoffs
	visionBackoffs = []time.Duration{0, 0, 0}
	defer func() { visionBackoffs = old }()

	vc := NewVisionClient(srv.URL, "k", "glm-4.6v-flash", 5*60*1e9)
	if len(vc.Models) != 2 || vc.Models[1] != "glm-4v-flash" {
		t.Fatalf("内置降级链未生效: %v", vc.Models)
	}
	out, err := vc.chatImage("s", "u", nil, 0.1)
	if err != nil {
		t.Fatalf("降级调用失败: %v", err)
	}
	if out != "ok-from-backup" {
		t.Errorf("备模型响应未透传: %q", out)
	}
	if vc.LastUsedModel != "glm-4v-flash" {
		t.Errorf("LastUsedModel=%q, want glm-4v-flash", vc.LastUsedModel)
	}
	if len(calls) < 2 || !strings.Contains(strings.Join(calls, ","), "glm-4v-flash") {
		t.Errorf("未发生降级调用: %v", calls)
	}
	// 自定义逗号链
	vc2 := NewVisionClient(srv.URL, "k", "m-primary,glm-4.6v-flash,glm-4v-flash", 5*60*1e9)
	if len(vc2.Models) != 3 {
		t.Errorf("自定义链解析: %v", vc2.Models)
	}
	// 整链 429 → 明确错误(不空转)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	}))
	defer srv2.Close()
	vc3 := NewVisionClient(srv2.URL, "k", "glm-4.6v-flash", 5*60*1e9)
	if _, err := vc3.chatImage("s", "u", nil, 0.1); err == nil || !strings.Contains(err.Error(), "整链过载") {
		t.Errorf("整链 429 应明确报错: %v", err)
	}
}

func decodeJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
