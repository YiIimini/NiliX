package manju

import (
	"os"
	"path/filepath"
	"testing"
)

// 回归:outputs.characters 必须包含多视图文件(_full/_side/_detail),
// 前端角色管理按视图名匹配展示——此前仅主视图能取到正式图,切到其他视图预览空白。
func TestOutputsIncludeViewFiles(t *testing.T) {
	dir := t.TempDir()
	chars := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(chars, 0755)
	for _, f := range []string{"顾念念.png", "顾念念_face.png", "顾念念_full.png", "顾念念_side.png", "顾念念_detail.png", "_gacha"} {
		p := filepath.Join(chars, f)
		if f == "_gacha" {
			_ = os.MkdirAll(p, 0755)
			continue
		}
		_ = os.WriteFile(p, []byte("x"), 0644)
	}
	exts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	got := listManjuMedia(chars, exts)
	names := map[string]bool{}
	for _, m := range got {
		names[str(m["name"])] = true
	}
	for _, want := range []string{"顾念念.png", "顾念念_full.png", "顾念念_side.png", "顾念念_detail.png"} {
		if !names[want] {
			t.Fatalf("outputs 缺少 %s, got %v", want, got)
		}
	}
	if names["_gacha"] {
		t.Fatalf("_gacha 子目录不应混入")
	}
}
