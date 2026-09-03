package manju

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"testing"
)

// TestSyncScriptFromNovel 「改了 novel 源分镜但视频仍是旧剧情」主链路的回归测试:
// 脚本模式渲染读 workdir/script/EPxx.md 副本,指纹盯副本——源更新副本不换则一切照旧。
// syncScriptFromNovel 必须把源内容覆盖进副本,且副本 mtime 变化使指纹失配。
func TestSyncScriptFromNovel(t *testing.T) {
	proj := "zz_sync_script_test"
	dir := filepath.Join(paths.ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	// 小说根:素材/分镜脚本/第001章_x.md(源,内容 v2)
	novelRoot := filepath.Join(dir, "novelroot")
	sbDir := filepath.Join(novelRoot, "素材", "分镜脚本")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatal(err)
	}
	srcScript := filepath.Join(sbDir, "第001章_测试_分镜脚本.md")
	if err := os.WriteFile(srcScript, []byte("# 分镜 v2\n| 1 | 中景 | Static | 画面 | 台词 | 光 | 声 | 5s |"), 0644); err != nil {
		t.Fatal(err)
	}

	// workdir:script/EP01.md(旧副本,内容 v1)+ config paths(script/novel_dir)
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(workdir, "script"), 0755); err != nil {
		t.Fatal(err)
	}
	dstScript := filepath.Join(workdir, "script", "EP01.md")
	if err := os.WriteFile(dstScript, []byte("# 分镜 v1(旧剧情)"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := `{"paths":{"workdir":"` + filepath.ToSlash(workdir) + `","script":"` + filepath.ToSlash(dstScript) +
		`","novel_dir":"` + filepath.ToSlash(novelRoot) + `","novel":"` + filepath.ToSlash(novelRoot) + `"}}`
	if err := os.WriteFile(filepath.Join(workdir, "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := newManjuCtx(filepath.Join(workdir, "config.json"), "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.scriptMode {
		t.Fatal("应识别为脚本模式")
	}

	fpOld := ctx.novelFingerprint()
	lg := newManjuLogger(&manjuTask{}, nil, proj, "EP01")
	ctx.syncScriptFromNovel(lg)

	got, rerr := os.ReadFile(dstScript)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "# 分镜 v2\n| 1 | 中景 | Static | 画面 | 台词 | 光 | 声 | 5s |" {
		t.Errorf("副本应被源内容覆盖,实际: %q", string(got))
	}
	if fpNew := ctx.novelFingerprint(); fpNew == fpOld {
		t.Error("副本更新后指纹应变化(否则旧方案照旧复用,同步失效)")
	}

	// 内容一致时不得改写(mtime 不动)
	ctx2, err := newManjuCtx(filepath.Join(workdir, "config.json"), "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(dstScript)
	ctx2.syncScriptFromNovel(lg)
	st2, _ := os.Stat(dstScript)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("源与副本一致时不应重写副本")
	}
}

// TestPickStoryboardMatch 同章号多候选:备份文件让位,正片取 mtime 最新。
func TestPickStoryboardMatch(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "第001章_正片_分镜脚本.md")
	backup := filepath.Join(dir, "第001章_正片_分镜脚本_old.md")
	for _, p := range []string{fresh, backup} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got := pickStoryboardMatch([]string{backup, fresh}); got != fresh {
		t.Errorf("应选正片,实际: %s", got)
	}
	os.WriteFile(fresh, []byte("y"), 0644) // mtime 更新
	if got := pickStoryboardMatch([]string{backup, fresh}); got != fresh {
		t.Errorf("应选最新正片,实际: %s", got)
	}
	// 全部是备份文件时兜底取首个
	if got := pickStoryboardMatch([]string{backup}); got != backup {
		t.Errorf("全备份时应兜底取首个,实际: %s", got)
	}
	if got := pickStoryboardMatch(nil); got != "" {
		t.Errorf("空候选应返回空串,实际: %s", got)
	}
}
