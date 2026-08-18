package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputDelete(t *testing.T) {
	dir, cfgPath := verifyConfig(t, "")
	t.Logf("dir=%s", dir)
	clipsEp := filepath.Join(dir, "clips", "EP01")
	_ = os.MkdirAll(clipsEp, 0755)
	shot := filepath.Join(clipsEp, "01.mp4")
	final := filepath.Join(dir, "EP01_成片.mp4")
	_ = os.WriteFile(shot, []byte("x"), 0644)
	_ = os.WriteFile(final, []byte("x"), 0644)
	w, _ := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfgPath, "scope": "file", "path": `C:\Windows\notepad.exe`})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "拒绝") {
		t.Errorf("越权路径应 400: %d %s", w.Code, w.Body.String())
	}
	w2, out2 := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfgPath, "scope": "file", "path": shot})
	if w2.Code != 200 || !out2["ok"].(bool) {
		t.Fatalf("file 删除 HTTP %d %s", w2.Code, w2.Body.String())
	}
	if fileExists(shot) {
		t.Error("shot 未删除")
	}
	if !fileExists(final) {
		t.Error("成片不应被波及")
	}
	_ = os.WriteFile(shot, []byte("x"), 0644)
	w3, out3 := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfgPath, "scope": "episode", "episode": "EP01"})
	if w3.Code != 200 {
		t.Fatalf("episode 删除 HTTP %d %s", w3.Code, w3.Body.String())
	}
	t.Logf("removed=%v", out3["removed"])

	if fileExists(shot) || fileExists(final) {
		t.Errorf("集删除不彻底")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Error("工作目录本身不应被删")
	}
}
