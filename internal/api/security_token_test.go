package api

import (
	"nilix/internal/manju"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

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

// TestFSHandlerWhitelist /api/fs/read 根外路径 403,根内正常
func TestFSHandlerWhitelist(t *testing.T) {
	root := t.TempDir()
	manju.SetFSRoots(root)
	defer manju.SetFSRoots()
	secret := filepath.Join(root, "..", "secret.key")
	_ = os.WriteFile(secret, []byte("sk-leak"), 0644)

	m := http.NewServeMux()
	manju.RegisterRoutes(m)
	manju.RegisterFsRoutes(m)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/fs/read?path="+filepath.ToSlash(secret), nil)
	m.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("根外路径应 403,得到 %d: %s", w.Code, w.Body.String())
	}
	// 根内正常
	inner := filepath.Join(root, "a.md")
	_ = os.WriteFile(inner, []byte("ok"), 0644)
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/fs/read?path="+filepath.ToSlash(inner), nil)
	m.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("根内应 200,得到 %d", w2.Code)
	}
	_ = os.Remove(secret)
}
