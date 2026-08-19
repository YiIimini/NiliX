package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
