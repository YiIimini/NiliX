package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestVisionStickyFallback 粘性降级:429 降级成功后,冷却窗内后续调用直接从备模型开始
// (不再重烧主模型退避);冷却结束回探主模型;主模型恢复后粘性清除。
func TestVisionStickyFallback(t *testing.T) {
	old, oldCd := visionBackoffs, visionStickyCooldown
	visionBackoffs = []time.Duration{0, 0, 0}
	visionStickyCooldown = 60 * time.Millisecond // 冷却窗缩短,便于测试回探
	defer func() { visionBackoffs, visionStickyCooldown = old, oldCd }()

	var mu sync.Mutex
	calls := map[string]int{}
	primaryOK := false // 主模型是否已"恢复"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = decodeJSONBody(r, &body)
		mu.Lock()
		calls[body.Model]++
		pOK := primaryOK
		mu.Unlock()
		if body.Model == "glm-4.6v-flash" && !pOK {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok","finish_reason":"stop"}}]}`))
	}))
	defer srv.Close()

	vc := NewVisionClient(srv.URL, "k", "glm-4.6v-flash", 5*60*1e9)
	// 第 1 次调用:主模型 4 次 429(首试+3 退避)→ 降级备模型成功
	if _, err := vc.chatImage("s", "u", nil, 0.1); err != nil {
		t.Fatalf("第 1 次调用应降级成功: %v", err)
	}
	mu.Lock()
	p1, b1 := calls["glm-4.6v-flash"], calls["glm-4v-flash"]
	mu.Unlock()
	if p1 != 4 || b1 != 1 {
		t.Fatalf("第 1 次应为主 4 次+备 1 次: primary=%d backup=%d", p1, b1)
	}
	// 第 2 次调用(冷却窗内):直接从备模型开始,主模型不再被尝试
	if _, err := vc.chatImage("s", "u", nil, 0.1); err != nil {
		t.Fatalf("第 2 次调用失败: %v", err)
	}
	mu.Lock()
	p2 := calls["glm-4.6v-flash"]
	mu.Unlock()
	if p2 != 4 {
		t.Fatalf("粘性窗口内不应再打主模型: primary=%d(应保持 4)", p2)
	}
	// 冷却结束:第 3 次调用回探主模型(仍 429,再降级)
	time.Sleep(80 * time.Millisecond)
	if _, err := vc.chatImage("s", "u", nil, 0.1); err != nil {
		t.Fatalf("第 3 次调用失败: %v", err)
	}
	mu.Lock()
	p3 := calls["glm-4.6v-flash"]
	mu.Unlock()
	if p3 != 8 {
		t.Fatalf("冷却后应回探主模型(再 +4 次): primary=%d(应 8)", p3)
	}
	// 主模型恢复 + 冷却结束:回探主模型成功,粘性清除;此后一直走主模型
	mu.Lock()
	primaryOK = true
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	if _, err := vc.chatImage("s", "u", nil, 0.1); err != nil || vc.LastUsedModel != "glm-4.6v-flash" {
		t.Fatalf("主模型恢复后应回探成功: %v %s", err, vc.LastUsedModel)
	}
	for i := 0; i < 2; i++ {
		if _, err := vc.chatImage("s", "u", nil, 0.1); err != nil {
			t.Fatalf("粘性清除后调用失败: %v", err)
		}
	}
	mu.Lock()
	bFinal := calls["glm-4v-flash"]
	mu.Unlock()
	if bFinal != 3 { // 第 1、2(粘性)、3 次用过备模型;主模型恢复后(4-6 次)不再碰
		t.Fatalf("主模型恢复后不应再碰备模型: backup=%d(应 3)", bFinal)
	}
}

// TestVisionCircuitBreaker 整链熔断:全链 429 → 熔断窗内后续调用快速失败(不重烧退避);
// 窗结束自动恢复;单次成功不清熔断(熔断只由整链失败触发)
func TestVisionCircuitBreaker(t *testing.T) {
	old, oldCd := visionBackoffs, visionCircuitCooldown
	visionBackoffs = []time.Duration{0, 0, 0}
	visionCircuitCooldown = 80 * time.Millisecond
	defer func() { visionBackoffs, visionCircuitCooldown = old, oldCd }()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	}))
	defer srv.Close()
	vc := NewVisionClient(srv.URL, "k", "glm-4.6v-flash", 5*60*1e9)
	// 第 1 次:主+备各 4 次尝试(2 模型 × 4)全 429 → 熔断置位
	if _, err := vc.chatImage("s", "u", nil, 0.1); err == nil {
		t.Fatal("应整链失败")
	}
	first := calls
	// 熔断窗内:下一次快速失败,不再发请求
	if _, err := vc.chatImage("s", "u", nil, 0.1); err == nil || !strings.Contains(err.Error(), "熔断") {
		t.Fatalf("熔断窗内应快速失败: %v", err)
	}
	if calls != first {
		t.Fatalf("熔断窗内不应再发请求: before=%d after=%d", first, calls)
	}
	// 窗结束自动恢复(再烧一轮)
	time.Sleep(100 * time.Millisecond)
	if _, err := vc.chatImage("s", "u", nil, 0.1); err == nil {
		t.Fatal("恢复后应仍失败(服务端仍 429)")
	}
	if calls <= first {
		t.Fatalf("熔断恢复后应重试请求: calls=%d", calls)
	}
}
