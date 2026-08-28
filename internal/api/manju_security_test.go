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

// 审计 2026-08-28 回归:atomicWrite 拒绝 base 为 ".." 的路径(泄漏根因——曾实测
// 409 个 "...tmp*" 文件污染调用方目录),且 Rename 失败时清理临时文件不残留。
func TestAtomicWriteRejectsDirPath(t *testing.T) {
	if err := atomicWrite("..", []byte("x")); err == nil {
		t.Fatal("传目录路径应报错")
	}
	if err := atomicWrite("", []byte("x")); err == nil {
		t.Fatal("空路径应报错")
	}
	if err := atomicWrite(".", []byte("x")); err == nil {
		t.Fatal("点路径应报错")
	}
}

func TestAtomicWriteCleansTmpOnRenameFail(t *testing.T) {
	dir := t.TempDir()
	// 目标位置放一个同名目录,os.Rename(文件→已存在目录) 必失败
	target := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("x")); err == nil {
		t.Fatal("目标为目录时应报错")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".state.json.tmp") {
			t.Fatalf("Rename 失败后临时文件未清理: %s", e.Name())
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

// TestManjuCacheClear 一键清缓存(2026-08-25 扫帚按钮):方案 JSON/镜头 mp4/条件缓存/接缝 latent 清除,
// 定妆照/场景图/成片不动;支持 Query 与 body 两种 config 传参。
func TestManjuCacheClear(t *testing.T) {
	old := comfyParams()
	SetComfyParams("", "", "") // 重置生效启动参数,让 ctx 回退 config 的 comfy_output(测试隔离)
	defer SetComfyParams(old.url, old.in, old.out)
	proj := "zz_cacheclear_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	mk := func(p string) { _ = os.MkdirAll(p, 0755) }
	mk(filepath.Join(dir, "analysis"))
	mk(filepath.Join(dir, "assets", "characters"))
	mk(filepath.Join(dir, "clips", "EP01"))
	mk(filepath.Join(dir, "comfy_output", "h3_context", "zz_cacheclear_test_EP01"))
	// 其他项目的 latent(不得误删)
	mk(filepath.Join(dir, "comfy_output", "h3_context", "other_proj_EP01"))
	// 方案缓存
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_direct_plan.json"), []byte(`{"shots":[{"shot_id":1,"duration":5}]}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_characters.json"), []byte(`{}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_shots_prompts.json"), []byte(`{}`), 0644)
	// 镜头 mp4
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "01.mp4"), []byte("x"), 0644)
	// 接缝 latent(本项目的清,其他项目的不动)
	_ = os.WriteFile(filepath.Join(dir, "comfy_output", "h3_context", "zz_cacheclear_test_EP01", "lat.pt"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "comfy_output", "h3_context", "other_proj_EP01", "lat.pt"), []byte("keep"), 0644)
	// 定妆照/成片(不得误删)
	_ = os.WriteFile(filepath.Join(dir, "assets", "characters", "c.png"), []byte("keep"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "EP01_成片.mp4"), []byte("keep"), 0644)
	cfg := `{"paths":{"workdir":"` + filepath.ToSlash(dir) + `","comfy_output":"` + filepath.ToSlash(filepath.Join(dir, "comfy_output")) + `","comfy_shared":"` + filepath.ToSlash(dir) + `"},"project":"` + proj + `"}`
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0644)

	w, res := doReq(t, "POST", "/api/manju/cache/clear", map[string]any{
		"config": filepath.Join(dir, "config.json"),
	})
	if w.Code != 200 || res["ok"] != true {
		t.Fatalf("清缓存失败: %d %s", w.Code, w.Body.String())
	}
	// 方案 JSON 已清
	if _, err := os.Stat(filepath.Join(dir, "analysis", "EP01_direct_plan.json")); err == nil {
		t.Errorf("方案 JSON 未清理")
	}
	// 镜头 mp4 已清
	if _, err := os.Stat(filepath.Join(dir, "clips", "EP01", "01.mp4")); err == nil {
		t.Errorf("镜头 mp4 未清理")
	}
	// 接缝 latent 已清(本项目)
	if _, err := os.Stat(filepath.Join(dir, "comfy_output", "h3_context", "zz_cacheclear_test_EP01", "lat.pt")); err == nil {
		t.Errorf("接缝 latent 未清理")
	}
	// 其他项目 latent 未误删
	if _, err := os.Stat(filepath.Join(dir, "comfy_output", "h3_context", "other_proj_EP01", "lat.pt")); err != nil {
		t.Errorf("其他项目 latent 被误删")
	}
	// 定妆照/成片未误删
	if _, err := os.Stat(filepath.Join(dir, "assets", "characters", "c.png")); err != nil {
		t.Errorf("定妆照被误删")
	}
	if _, err := os.Stat(filepath.Join(dir, "EP01_成片.mp4")); err != nil {
		t.Errorf("成片被误删")
	}
}

// TestManjuCacheClearAdvanced 高级清理(2026-08-25「高级」按钮):普通清理基础上额外清空
// ComfyUI 共享 input/output 目录产物;模型权重(../models)与目录本身不触碰。
func TestManjuCacheClearAdvanced(t *testing.T) {
	oldComfy := comfyParams()
	SetComfyParams("", "", "")
	defer SetComfyParams(oldComfy.url, oldComfy.in, oldComfy.out)
	oldShared := ComfySharedDir
	shared := filepath.Join(os.TempDir(), "nilix-test-comfy-shared-adv")
	ComfySharedDir = shared
	defer func() { ComfySharedDir = oldShared }()
	_ = os.RemoveAll(shared)
	defer os.RemoveAll(shared)
	mk := func(p string) { _ = os.MkdirAll(p, 0755) }
	mk(filepath.Join(shared, "input"))
	mk(filepath.Join(shared, "output"))
	mk(filepath.Join(shared, "models")) // 权重目录:高级清理不得触碰
	_ = os.WriteFile(filepath.Join(shared, "input", "ref.png"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(shared, "output", "manju_asset_00001_.png"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(shared, "output", "EP01_成片.mp4"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(shared, "models", "huge.safetensors"), []byte("keep"), 0644)

	proj := "zz_cacheclear_adv_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	mk(filepath.Join(dir, "analysis"))
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_direct_plan.json"), []byte(`{}`), 0644)
	cfg := `{"paths":{"workdir":"` + filepath.ToSlash(dir) + `","comfy_output":"` + filepath.ToSlash(filepath.Join(shared, "output")) + `","comfy_shared":"` + filepath.ToSlash(shared) + `"},"project":"` + proj + `"}`
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0644)

	w, res := doReq(t, "POST", "/api/manju/cache/clear", map[string]any{
		"config": filepath.Join(dir, "config.json"), "advanced": true,
	})
	if w.Code != 200 || res["ok"] != true {
		t.Fatalf("高级清理失败: %d %s", w.Code, w.Body.String())
	}
	if res["advanced"] != true {
		t.Fatalf("advanced 未回传: %v", res)
	}
	// input/output 产物已清
	if _, err := os.Stat(filepath.Join(shared, "input", "ref.png")); err == nil {
		t.Errorf("input 产物未清理")
	}
	if _, err := os.Stat(filepath.Join(shared, "output", "manju_asset_00001_.png")); err == nil {
		t.Errorf("output 产物未清理")
	}
	if _, err := os.Stat(filepath.Join(shared, "output", "EP01_成片.mp4")); err == nil {
		t.Errorf("output 成片未清理")
	}
	// 模型权重未触碰
	if _, err := os.Stat(filepath.Join(shared, "models", "huge.safetensors")); err != nil {
		t.Errorf("模型权重被误删")
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

// TestManjuGuardNovelCreate 审计 F1:创建项目时 novel 必须位于小说库根内——
// 越界路径被拒绝,不能进入 config(防任意文件读链)
func TestManjuGuardNovelCreate(t *testing.T) {
	oldNovel := NovelRootDir
	root := t.TempDir()
	NovelRootDir = root
	defer func() { NovelRootDir = oldNovel }()
	// 清理测试项目残留(共享测试根,重复运行会"项目已存在")
	for _, p := range []string{"guard_ok", "guard_bad", "guard_dotdot"} {
		_ = os.RemoveAll(filepath.Join(ManjuRootDir, p))
	}
	defer func() {
		for _, p := range []string{"guard_ok", "guard_bad", "guard_dotdot"} {
			_ = os.RemoveAll(filepath.Join(ManjuRootDir, p))
		}
	}()

	// 根内小说:允许
	in := filepath.Join(root, "book.md")
	_ = os.WriteFile(in, []byte("x"), 0644)
	out, _, ok := manjuCreateProject("guard_ok", in, "key")
	if !ok {
		t.Fatalf("根内 novel 应允许: %s", out)
	}
	// 根外文件:拒绝
	outside := filepath.Join(filepath.Dir(root), "secret.md")
	_ = os.WriteFile(outside, []byte("sk"), 0644)
	out2, _, ok2 := manjuCreateProject("guard_bad", outside, "key")
	if ok2 {
		t.Fatalf("根外 novel 应拒绝: %s", out2)
	}
	// 相对路径穿越:拒绝
	out3, _, ok3 := manjuCreateProject("guard_dotdot", `..\secret.md`, "key")
	if ok3 {
		t.Fatalf("穿越 novel 应拒绝: %s", out3)
	}
	_ = os.Remove(outside)
}

// TestManjuGachaPlanGuard 审计 F2:gacha/plan 的 config 必须归属项目根,
// 越界 config 被拒(任意 JSON 读改写防护)
func TestManjuGachaPlanGuard(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(outside, []byte(`{"a":1}`), 0644)
	w, _ := doReq(t, "POST", "/api/manju/gacha/plan", map[string]any{
		"config": outside, "chapters": "1-3", "episode": "1",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("越界 config 应 403,得到 %d: %s", w.Code, w.Body.String())
	}
}

// TestNotifyTokenMasked 审计 F11:GET /api/manju/notify 不得返回明文 Token(掩码)
func TestNotifyTokenMasked(t *testing.T) {
	oldFile := manjuNotifyFile
	dir := t.TempDir()
	manjuNotifyFile = filepath.Join(dir, "notify.json")
	defer func() { manjuNotifyFile = oldFile }()
	_ = os.WriteFile(manjuNotifyFile, []byte(`{"enabled":true,"channel":"serverchan","token":"sk-abcdef1234567890"}`), 0644)

	w, out := doReq(t, "GET", "/api/manju/notify", nil)
	if w.Code != 200 {
		t.Fatalf("GET notify 应 200: %d", w.Code)
	}
	tok := str(out["token"])
	if tok == "sk-abcdef1234567890" || !strings.Contains(tok, "****") {
		t.Fatalf("Token 应掩码返回,得到: %q", tok)
	}
}
