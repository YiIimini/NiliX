package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFSPathAllowed fs 根目录白名单:根内放行/根外拒绝/大小写不敏感/动态注册
func TestFSPathAllowed(t *testing.T) {
	SetFSRoots(`C:\Root\A`, `C:\Root\B`)
	defer SetFSRoots() // 清空(测试隔离)

	if !fsPathAllowed(`C:\Root\A`) || !fsPathAllowed(`C:\Root\A\sub\file.md`) {
		t.Errorf("根内应放行")
	}
	if !fsPathAllowed(`c:\root\a\SUB\x.json`) {
		t.Errorf("大小写不敏感应放行")
	}
	if fsPathAllowed(`C:\Root\C`) || fsPathAllowed(`C:\Other`) || fsPathAllowed(``) {
		t.Errorf("根外应拒绝")
	}
	// 前缀误判:Root\A2 不属于 Root\A
	if fsPathAllowed(`C:\Root\A2\secret`) {
		t.Errorf("相似前缀不应放行")
	}
	addFSRoot(`C:\Root\C`)
	if !fsPathAllowed(`C:\Root\C\deep\deep\f.mp4`) {
		t.Errorf("动态注册后应放行")
	}
}

// TestFSHandlerWhitelist /api/fs/read 根外路径 403,根内正常
func TestFSHandlerWhitelist(t *testing.T) {
	root := t.TempDir()
	SetFSRoots(root)
	defer SetFSRoots()
	secret := filepath.Join(root, "..", "secret.key")
	_ = os.WriteFile(secret, []byte("sk-leak"), 0644)

	srv := NewServer(nil, nil, nil, nil, nil, nil, root, nil, nil, "")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/fs/read?path="+filepath.ToSlash(secret), nil)
	srv.handleFSRead(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("根外路径应 403,得到 %d: %s", w.Code, w.Body.String())
	}
	// 根内正常
	inner := filepath.Join(root, "a.md")
	_ = os.WriteFile(inner, []byte("ok"), 0644)
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/fs/read?path="+filepath.ToSlash(inner), nil)
	srv.handleFSRead(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("根内应 200,得到 %d", w2.Code)
	}
	_ = os.Remove(secret)
}

// TestAuthMiddleware 非 GET 校验 token;GET 放行
func TestAuthMiddleware(t *testing.T) {
	old := sessionToken
	sessionToken = "tok-abc"
	defer func() { sessionToken = old }()

	s := &Server{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := s.auth(inner)

	// 无 token POST → 401
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 token POST 应 401,得到 %d", w.Code)
	}
	// 错误 token → 401
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/api/x", nil)
	r2.Header.Set("X-NiliX-Token", "wrong")
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("错误 token 应 401")
	}
	// 正确 token → 200
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("POST", "/api/x", nil)
	r3.Header.Set("X-NiliX-Token", "tok-abc")
	h.ServeHTTP(w3, r3)
	if w3.Code != 200 {
		t.Fatalf("正确 token 应放行,得到 %d", w3.Code)
	}
	// GET 免 token
	w4 := httptest.NewRecorder()
	h.ServeHTTP(w4, httptest.NewRequest("GET", "/api/x", nil))
	if w4.Code != 200 {
		t.Fatalf("GET 应放行")
	}
}

// TestHandleManageTokenInject 管理页注入会话令牌
func TestHandleManageTokenInject(t *testing.T) {
	old := sessionToken
	sessionToken = "tok-inject"
	defer func() { sessionToken = old }()
	s := &Server{indexHTML: []byte(`<head><script>window.NILIX_TOKEN="/*__NILIX_TOKEN__*/";</script></head>`)}
	w := httptest.NewRecorder()
	s.handleManage(w, httptest.NewRequest("GET", "/manage/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `window.NILIX_TOKEN="tok-inject"`) {
		t.Fatalf("令牌未注入: %s", body)
	}
	if strings.Contains(body, "__NILIX_TOKEN__") {
		t.Fatalf("占位符未替换干净")
	}
}

// TestAtomicWrite 原子写:文件可读且内容正确,临时文件不残留
func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := atomicWriteJSON(p, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m["a"] != float64(1) {
		t.Fatalf("原子写内容异常: %s", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}
}

// TestManjuUpscaleEstimate 2K 费用预估:有方案按方案时长×单价;无方案按目录镜头粗估
func TestManjuUpscaleEstimate(t *testing.T) {
	proj := "zz_estimate_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(filepath.Join(dir, "analysis"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "clips", "EP01"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"paths":{"workdir":"`+filepath.ToSlash(dir)+`","clips":"`+filepath.ToSlash(filepath.Join(dir, "clips"))+`","analysis":"`+filepath.ToSlash(filepath.Join(dir, "analysis"))+`"}}`), 0644)
	// 有方案:3 镜(5+8+4=17s)
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_direct_plan.json"),
		[]byte(`{"shots":[{"shot_id":1,"duration":5},{"shot_id":2,"duration":8},{"shot_id":3,"duration":4}]}`), 0644)
	ctx, _ := newManjuCtx(filepath.Join(dir, "config.json"), "EP01", "", "", "")
	e := ctx.manjuUpscaleEstimate("")
	if e["shots"] != 3 || e["durationSec"] != 17 || e["costCNY"] != 13.6 {
		t.Fatalf("有方案预估异常: %v", e)
	}
	// 指定镜头 only=1 → 5s × 0.8 = 4
	e2 := ctx.manjuUpscaleEstimate("1")
	if e2["shots"] != 1 || e2["costCNY"] != 4.0 {
		t.Fatalf("指定镜头预估异常: %v", e2)
	}
	// 无方案:目录 2 个 mp4 → 2×8s=16s
	_ = os.Remove(filepath.Join(dir, "analysis", "EP01_direct_plan.json"))
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "01.mp4"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "02.mp4"), []byte("x"), 0644)
	ctx2, _ := newManjuCtx(filepath.Join(dir, "config.json"), "EP01", "", "", "")
	e3 := ctx2.manjuUpscaleEstimate("")
	if e3["shots"] != 2 || e3["durationSec"] != 16 {
		t.Fatalf("无方案粗估异常: %v", e3)
	}
}

// TestManjuCleanup 产物清理:只删目标子目录,定妆照/定稿不动
func TestManjuCleanup(t *testing.T) {
	proj := "zz_cleanup_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	mk := func(p string) { _ = os.MkdirAll(p, 0755) }
	mk(filepath.Join(dir, "assets", "characters", "_gacha"))
	mk(filepath.Join(dir, "analysis", "_frames"))
	mk(filepath.Join(dir, "clips", "EP01", "2k"))
	mk(filepath.Join(dir, "assets", "characters"))
	mk(filepath.Join(dir, "clips", "EP01"))
	_ = os.WriteFile(filepath.Join(dir, "assets", "characters", "_gacha", "c_s1.png"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "analysis", "_frames", "f.jpg"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "2k", "01.mp4"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "assets", "characters", "c.png"), []byte("keep"), 0644) // 定妆照
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "01.mp4"), []byte("keep"), 0644)       // 定稿
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"paths":{"workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)

	w, res := doReq(t, "POST", "/api/manju/cleanup", map[string]any{
		"config": filepath.Join(dir, "config.json"), "targets": []any{"gacha", "frames", "2k"},
	})
	if w.Code != 200 || res["ok"] != true {
		t.Fatalf("清理失败: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "assets", "characters", "_gacha", "c_s1.png")); err == nil {
		t.Errorf("gacha 未清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "assets", "characters", "c.png")); err != nil {
		t.Errorf("定妆照被误删")
	}
	if _, err := os.Stat(filepath.Join(dir, "clips", "EP01", "01.mp4")); err != nil {
		t.Errorf("镜头定稿被误删")
	}
}

// TestTokenInjectRootPath 根路径(kb 工作台页)FileServer 注入会话令牌:
// HTML 替换占位符 → 真实 token;无占位符残留;静态 js 透传不缓冲
func TestTokenInjectRootPath(t *testing.T) {
	old := sessionToken
	sessionToken = "tok-root"
	defer func() { sessionToken = old }()

	// 模拟 kbFS:index.html 含占位符 + 一个 js
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<head><script>window.NILIX_TOKEN="/*__NILIX_TOKEN__*/";</script></head><body>kb</body>`)},
		"app.js":     &fstest.MapFile{Data: []byte(`console.log("static")`)},
	}
	s := &Server{kbFS: fsys}
	h := s.tokenInject(noCacheHTML(http.FileServer(http.FS(fsys))))

	// 根路径 HTML:占位符被替换
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `window.NILIX_TOKEN="tok-root"`) {
		t.Fatalf("根页令牌未注入: %s", body)
	}
	if strings.Contains(body, "__NILIX_TOKEN__") {
		t.Fatalf("占位符未替换干净")
	}
	// 静态 js:原样透传(不含占位符替换逻辑,内容不变)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "/app.js", nil))
	if w2.Body.String() != `console.log("static")` {
		t.Fatalf("静态文件应原样透传: %q", w2.Body.String())
	}
	// sessionToken 为空:不替换(占位符保留,前端走异步兜底)
	sessionToken = ""
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w3.Body.String(), "__NILIX_TOKEN__") {
		t.Fatalf("token 为空时不应替换")
	}
}

// TestTokenInjectNoTruncate token 注入不得截断 HTML:占位符(21B)→token(32B)长度变化后,
// 响应 body 必须完整(含尾部 </html>),Content-Length 不得保留旧值导致截断黑屏
func TestTokenInjectNoTruncate(t *testing.T) {
	old := sessionToken
	sessionToken = strings.Repeat("a", 32)
	defer func() { sessionToken = old }()

	html := `<html><head><script>window.NILIX_TOKEN="/*__NILIX_TOKEN__*/";</script></head><body>kb</body></html>`
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(html)}}
	s := &Server{kbFS: fsys}
	h := s.tokenInject(noCacheHTML(http.FileServer(http.FS(fsys))))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.HasSuffix(body, "</html>") {
		t.Fatalf("HTML 被截断(尾部缺失): %q", body[len(body)-60:])
	}
	if !strings.Contains(body, `window.NILIX_TOKEN="`+strings.Repeat("a", 32)+`"`) {
		t.Fatalf("token 未正确注入")
	}
	if cl := w.Header().Get("Content-Length"); cl != "" && cl != fmt.Sprint(len(body)) {
		t.Fatalf("Content-Length 与 body 长度不符: header=%s actual=%d", cl, len(body))
	}
}
