package manju

import (
	"os"
	"path/filepath"
	"testing"
)

// 回归(2026-08-29 用户实测「从目录检测分镜脚本检测不到也不能自选目录」):
// ①递归一层子目录——选小说库根(内含多本书)时,<书>/素材/分镜脚本 下的脚本要能检测到;
// ②本层与 素材/分镜脚本 两层原行为不回归。
func TestScanStoryboardDirRecursiveOneLevel(t *testing.T) {
	root := t.TempDir()
	// 书A:素材/分镜脚本 结构
	aDir := filepath.Join(root, "书A", "素材", "分镜脚本")
	if err := os.MkdirAll(aDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aDir, "第001章_测试_分镜脚本.md"), []byte("# 分镜脚本\n| 01 | 全景 |"), 0644); err != nil {
		t.Fatal(err)
	}
	// 书B:平铺在本层
	if err := os.WriteFile(filepath.Join(root, "书B_第002章_分镜脚本.md"), []byte("| 01 |"), 0644); err != nil {
		t.Fatal(err)
	}
	items, ep := scanStoryboardDir(root, "EP01")
	if ep != "EP01" {
		t.Fatalf("episode want EP01 got %s", ep)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if len(names) != 2 {
		t.Fatalf("书父目录应检测到 2 个分镜脚本(书A深层+书B本层), got %v", names)
	}
	found := false
	for _, n := range names {
		if n == "第001章_测试_分镜脚本.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("书A 素材/分镜脚本 深层脚本未被检测到, got %v", names)
	}
}
