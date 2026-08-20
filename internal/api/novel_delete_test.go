package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 回归:小说删除接口——此前 handleNovelDelete 误用 fileExists(要求非目录)检查小说目录,
// 目录恒返回 false → 删除确认后恒报「小说不存在」,前端表现为"确认删除无反应"。
func TestNovelDeleteDirCheck(t *testing.T) {
	oldRoot := NovelRootDir
	defer func() { NovelRootDir = oldRoot }()
	dir := t.TempDir()
	NovelRootDir = dir
	proj := filepath.Join(dir, "测试书")
	_ = os.MkdirAll(filepath.Join(proj, "正文"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "正文", "第001章.md"), []byte("x"), 0644)
	s := &Server{}
	body, _ := json.Marshal(map[string]string{"title": "测试书"})
	req := httptest.NewRequest("POST", "/api/novel/delete", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleNovelDelete(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ok"] != true {
		t.Fatalf("ok != true: %s", w.Body.String())
	}
	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Fatalf("目录未删除: %s", proj)
	}
}

// 回归:越权路径安全——title 含路径穿越片段时消毒后不匹配任何真实目录,必须拒绝而非删除
func TestNovelDeletePathTraversalRejected(t *testing.T) {
	oldRoot := NovelRootDir
	defer func() { NovelRootDir = oldRoot }()
	dir := t.TempDir()
	NovelRootDir = dir
	s := &Server{}
	body, _ := json.Marshal(map[string]string{"title": `..\..\Windows`})
	req := httptest.NewRequest("POST", "/api/novel/delete", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleNovelDelete(w, req)
	if w.Code == 200 {
		t.Fatalf("越权路径被接受: %s", w.Body.String())
	}
	// 小说根目录本身也必须保留(不能删除根)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("小说根目录被误删: %v", err)
	}
}
