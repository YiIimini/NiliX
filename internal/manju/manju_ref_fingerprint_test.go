package manju

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 回归:换定妆照(采纳/重生成,内容与 mtime 变化)后,条件缓存指纹必须变化,
// 否则 .pt 缓存命中跳过重编码、渲染继续用旧角色("采纳后不作为定妆照"的隐性根源)
func TestShotCondFingerprintRefSensitivity(t *testing.T) {
	dir := t.TempDir()
	chars := filepath.Join(dir, "characters")
	if err := os.MkdirAll(chars, 0755); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{assetsDir: dir, fps: 24}
	s := manjuShot{ID: 1, Scene: "s1", Characters: []string{"陈鱼"}, H3Prompt: "p", Duration: 4}

	main := filepath.Join(chars, "陈鱼.png")
	if err := os.WriteFile(main, []byte("main-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	f1 := ctx.shotCondFingerprintAt(s, 1280, 720)

	// 模拟采纳:主图被覆盖(mtime/内容更新)
	time.Sleep(20 * time.Millisecond) // 保证 mtime 纳秒推进
	if err := os.WriteFile(main, []byte("main-v2-adopted"), 0644); err != nil {
		t.Fatal(err)
	}
	f2 := ctx.shotCondFingerprintAt(s, 1280, 720)
	if f1 == f2 {
		t.Fatalf("采纳新定妆照后指纹未变化: %s", f1)
	}

	// 正脸参考出现后优先计入指纹(即使主图未再变)
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(chars, "陈鱼_face.png"), []byte("face"), 0644); err != nil {
		t.Fatal(err)
	}
	f3 := ctx.shotCondFingerprintAt(s, 1280, 720)
	if f2 == f3 {
		t.Fatalf("正脸参考出现后指纹未变化: %s", f2)
	}
}

// 回归:refRelFor 取图优先级——正脸特写优先,缺失回退全身定妆照
func TestRefRelForFallback(t *testing.T) {
	dir := t.TempDir()
	chars := filepath.Join(dir, "characters")
	if err := os.MkdirAll(chars, 0755); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{assetsDir: dir}
	if got := ctx.refRelFor("甲"); got != "" {
		t.Fatalf("无图时应为空串,got %q", got)
	}
	main := filepath.Join(chars, "甲.png")
	_ = os.WriteFile(main, []byte("m"), 0644)
	if got := ctx.refRelFor("甲"); got != "characters/甲.png" {
		t.Fatalf("应回退主图,got %q", got)
	}
	_ = os.WriteFile(filepath.Join(chars, "甲_face.png"), []byte("f"), 0644)
	if got := ctx.refRelFor("甲"); got != "characters/甲_face.png" {
		t.Fatalf("应优先正脸,got %q", got)
	}
}
