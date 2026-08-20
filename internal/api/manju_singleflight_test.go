package api

// 回归(审计 M2):预编码 singleflight 并发去重。
// 场景:渲染路径用后台 goroutine 预提交下一镜编码(manju_pipeline stageRender 的
// safeGo preencode),下一镜轮到时串行路径又调 ensureEncodedAt——同一 cacheName
// 并发双提交会白烧一次 Qwen3-VL 编码。singleflight 必须保证只提交一次,后到者等文件。
//
// 历史 bug:make(chan struct{}) 用无缓冲 channel——发送在无接收者时永不成功,
// select 恒走 default,所有调用者都在等文件、没人真正执行编码 → 1900s 后全部
// "等待预编码完成超时"(预编码必挂)。修复:缓冲 1 + 失败接力。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManjuEncSingleflightConcurrent(t *testing.T) {
	const cacheName = "test_sf_cache_1"
	sharedModels := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sharedModels, "conditioning"), 0755); err != nil {
		t.Fatal(err)
	}
	var submitCount int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt":
			atomic.AddInt32(&submitCount, 1)
			// 模拟 H3CondSave 落盘:提交即写条件缓存(实际由 ComfyUI 端编码完成后写)
			_ = os.WriteFile(filepath.Join(sharedModels, "conditioning", cacheName+".pt"), []byte("x"), 0644)
			w.Write([]byte(`{"prompt_id":"p1","number":1}`))
		case strings.HasPrefix(r.URL.Path, "/history"):
			w.Write([]byte(`{"p1":{"status":{"completed":true,"status_str":"success"}}}`))
		default:
			w.WriteHeader(200)
		}
	}))
	defer svr.Close()

	ctx := &manjuCtx{
		comfy:        newComfyClient(svr.URL),
		sharedModels: sharedModels,
		assetsDir:    t.TempDir(),
		comfyInput:   t.TempDir(),
		R:            map[string]any{},
		fps:          24,
		project:      "testsf",
	}
	lg := &manjuLogger{state: manjuState}
	shot := manjuShot{ID: 1, Scene: "", Camera: "缓慢推近", Duration: 4, H3Prompt: "test prompt", Characters: []string{}}

	const n = 2
	errs := make([]error, n)
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = ctx.ensureEncodedAt(shot, cacheName, 768, 1344, lg)
		}(i)
	}
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("并发预编码 60s 未完成(疑似 singleflight 死锁:无人拿到执行权)")
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d 预编码失败: %v", i, e)
		}
	}
	if got := atomic.LoadInt32(&submitCount); got != 1 {
		t.Fatalf("同一 cacheName 并发提交应只 1 次,实际 %d", got)
	}
	if !fileExists(filepath.Join(sharedModels, "conditioning", cacheName+".pt")) {
		t.Fatal("条件缓存 .pt 未生成")
	}
}

// 回归:执行者失败(提交报错、文件未生成)释放执行权后,等待者应接力重试而不是干等超时。
func TestManjuEncSingleflightRelayAfterFailure(t *testing.T) {
	const cacheName = "test_sf_cache_relay"
	sharedModels := t.TempDir()
	_ = os.MkdirAll(filepath.Join(sharedModels, "conditioning"), 0755)

	var submitCount int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt":
			// 第一次提交报错(模拟 ComfyUI 未就绪),第二次成功并落盘
			if atomic.AddInt32(&submitCount, 1) == 1 {
				w.WriteHeader(500)
				w.Write([]byte(`{"error":"boom"}`))
				return
			}
			_ = os.WriteFile(filepath.Join(sharedModels, "conditioning", cacheName+".pt"), []byte("x"), 0644)
			w.Write([]byte(`{"prompt_id":"p2","number":1}`))
		case strings.HasPrefix(r.URL.Path, "/history"):
			w.Write([]byte(`{"p2":{"status":{"completed":true,"status_str":"success"}}}`))
		default:
			w.WriteHeader(200)
		}
	}))
	defer svr.Close()

	ctx := &manjuCtx{
		comfy:        newComfyClient(svr.URL),
		sharedModels: sharedModels,
		assetsDir:    t.TempDir(),
		comfyInput:   t.TempDir(),
		R:            map[string]any{},
		fps:          24,
		project:      "testsf",
	}
	lg := &manjuLogger{state: manjuState}
	shot := manjuShot{ID: 1, Scene: "", Camera: "缓慢推近", Duration: 4, H3Prompt: "test prompt", Characters: []string{}}

	// 先让一个 goroutine 占住执行权并失败释放;随后等待者接力
	// (实际竞态:失败者释放 gate 后,等待者从 default 轮询中拿到执行权重试)
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { done <- ctx.ensureEncodedAt(shot, cacheName, 768, 1344, lg) }()
	}
	var errs []error
	timeout := time.After(60 * time.Second)
	for len(errs) < 3 {
		select {
		case e := <-done:
			errs = append(errs, e)
		case <-timeout:
			t.Fatalf("接力预编码 60s 未完成(共 %d 个返回,提交 %d 次)", len(errs), atomic.LoadInt32(&submitCount))
		}
	}
	// 首个执行者遇提交 500 直接失败(不等待接力);等待者接力成功 + 等文件成功 → 2 成功 1 失败
	success, fail := 0, 0
	for i, e := range errs {
		if e == nil {
			success++
		} else {
			fail++
			t.Logf("调用 %d 失败(预期:首次提交 500): %v", i, e)
		}
	}
	if success != 2 || fail != 1 {
		t.Fatalf("应 2 成功 1 失败(首个执行者提交 500),实际 success=%d fail=%d", success, fail)
	}
	if got := atomic.LoadInt32(&submitCount); got != 2 {
		t.Fatalf("首次失败后应接力重试共提交 2 次,实际 %d", got)
	}
}
