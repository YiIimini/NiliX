package manju

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"testing"
)

func TestManjuDeleteProject(t *testing.T) {
	// 在 paths.ManjuRootDir 下建一个真实项目(唯一名)用于删除测试
	proj := "zz_delete_test"
	dir := filepath.Join(paths.ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(filepath.Join(dir, "clips", "EP01"), 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"2.5d","render":{},"paths":{"workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "clips", "EP01", "01.mp4"), []byte("x"), 0644)

	// 非法输入:空 config / 保留目录 / 越权
	for _, bad := range []map[string]any{
		{"config": ""},
		{"config": `C:\Mi\Ai\WorkBench\manju\logs\config.json`},
		{"config": `C:\Mi\Ai\WorkBench\manju\..\..\Windows\config.json`},
	} {
		w, _ := doReq(t, "POST", "/api/manju/delete", bad)
		if w.Code == 200 { // 安全第一:绝不允许删成功(400/404 均可,反正目录在 paths.ManjuRootDir 内不存在)
			t.Errorf("非法输入不应删除成功: %v", bad)
		}
	}
	// 正常删除整个目录
	w, out := doReq(t, "POST", "/api/manju/delete", map[string]any{"config": cfgPath})
	if w.Code != 200 || !out["ok"].(bool) {
		t.Fatalf("删除 HTTP %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("项目目录未被删除: %s", dir)
	}
	// 重删 → 404
	w2, _ := doReq(t, "POST", "/api/manju/delete", map[string]any{"config": cfgPath})
	if w2.Code != 404 {
		t.Errorf("重删应 404, got %d", w2.Code)
	}
}
