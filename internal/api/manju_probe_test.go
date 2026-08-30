package api

// manju_probe_test.go 新建项目目录探测单测(2026-08-30 用户需求:选目录自动解析,
// 未满足条件显示具体错误)。用临时目录构造四种典型布局:
// 完整书根(全本+分镜+提示词)/仅全本/仅分镜/空目录/库根(多本书)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func probeMkBook(t *testing.T, root string, withNovel, withStory, withPrompts bool) {
	t.Helper()
	if withNovel {
		os.MkdirAll(filepath.Join(root, "全本"), 0755)
		os.WriteFile(filepath.Join(root, "全本", "测试书·全本.md"), []byte("# 全本\n第一章 测试"), 0644)
		os.MkdirAll(filepath.Join(root, "正文", "卷一"), 0755)
		os.WriteFile(filepath.Join(root, "正文", "卷一", "第1章_测试.md"), []byte("正文"), 0644)
	}
	if withStory {
		os.MkdirAll(filepath.Join(root, "素材", "分镜脚本"), 0755)
		os.WriteFile(filepath.Join(root, "素材", "分镜脚本", "第001章_测试_分镜脚本.md"), []byte("[Shot 1] 测试"), 0644)
		os.WriteFile(filepath.Join(root, "素材", "分镜脚本", "第002章_测试_分镜脚本.md"), []byte("[Shot 1] 测试"), 0644)
	}
	if withPrompts {
		os.WriteFile(filepath.Join(root, "素材", "人物生成提示词.md"), []byte("人物"), 0644)
		os.WriteFile(filepath.Join(root, "素材", "场景提示词.md"), []byte("场景"), 0644)
		os.WriteFile(filepath.Join(root, "素材", "渲染提示词总集.md"), []byte("渲染"), 0644)
	}
}

func TestProbeManjuDirFullBook(t *testing.T) {
	root := t.TempDir()
	probeMkBook(t, root, true, true, true)
	p := probeManjuDir(root)
	if !p.OK {
		t.Fatalf("完整书根应 ok, errors=%v", p.Errors)
	}
	if p.NovelFile == "" || !strings.Contains(p.NovelFile, "全本") {
		t.Errorf("应检测到全本 md, got %q", p.NovelFile)
	}
	if p.StoryN != 2 {
		t.Errorf("应检测到 2 个分镜脚本, got %d", p.StoryN)
	}
	if p.Chapters != 1 {
		t.Errorf("正文分章计数应为 1, got %d", p.Chapters)
	}
	if len(p.Prompts) != 3 {
		t.Errorf("应检测到 3 个提示词文件, got %v", p.Prompts)
	}
	if p.Name == "" || p.Name == "." {
		t.Errorf("剧名应取目录名, got %q", p.Name)
	}
}

func TestProbeManjuDirNovelOnly(t *testing.T) {
	root := t.TempDir()
	probeMkBook(t, root, true, false, false)
	p := probeManjuDir(root)
	if !p.OK || p.NovelFile == "" {
		t.Fatalf("仅全本应 ok, errors=%v", p.Errors)
	}
	if !strings.Contains(strings.Join(p.Notes, ";"), "LLM 直出") {
		t.Errorf("应提示走 LLM 直出, notes=%v", p.Notes)
	}
}

func TestProbeManjuDirStoryOnly(t *testing.T) {
	root := t.TempDir()
	probeMkBook(t, root, false, true, false)
	p := probeManjuDir(root)
	if !p.OK || p.StoryN != 2 {
		t.Fatalf("仅分镜应 ok, got %+v", p)
	}
	if !strings.Contains(strings.Join(p.Notes, ";"), "视频脚本直出") {
		t.Errorf("应提示脚本直出模式, notes=%v", p.Notes)
	}
}

func TestProbeManjuDirEmpty(t *testing.T) {
	root := t.TempDir()
	p := probeManjuDir(root)
	if p.OK {
		t.Fatalf("空目录不应 ok")
	}
	joined := strings.Join(p.Errors, ";")
	for _, want := range []string{"未找到小说全本", "未找到分镜脚本", "至少需要"} {
		if !strings.Contains(joined, want) {
			t.Errorf("错误信息应含「%s」, got: %v", want, p.Errors)
		}
	}
}

func TestProbeManjuDirLibraryRoot(t *testing.T) {
	lib := t.TempDir()
	probeMkBook(t, filepath.Join(lib, "书甲"), true, true, false)
	probeMkBook(t, filepath.Join(lib, "书乙"), true, false, false)
	p := probeManjuDir(lib)
	if p.OK {
		t.Fatalf("库根不应直接 ok(须进入具体书目录)")
	}
	if len(p.Books) != 2 {
		t.Errorf("应检出 2 本书, got %v", p.Books)
	}
	if !strings.Contains(strings.Join(p.Errors, ";"), "书库根目录") {
		t.Errorf("错误应说明是库根, got %v", p.Errors)
	}
}

func TestProbeManjuDirFileInput(t *testing.T) {
	root := t.TempDir()
	probeMkBook(t, root, true, false, false)
	p := probeManjuDir(filepath.Join(root, "全本", "测试书·全本.md"))
	if !p.OK || p.NovelFile == "" {
		t.Fatalf("全本文件输入应归一化到书根并 ok, got %+v", p)
	}
}
