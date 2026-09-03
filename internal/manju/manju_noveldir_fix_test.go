package manju

// 回归(2026-08-30 轮回欠费九世实锤):创建项目传全本 md 文件时 novel_dir 被写成
// …/书/全本 → 素材卡(人物/场景提示词)与分镜脚本目录全部落空 → 方案零角色卡
// → 资产 0 角色/0 场景直接编码。修复:resolveNovelPath 文件输入上提书根 +
// novelRootDir 存量错指自愈。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNovelPathFileInputLiftsToBookRoot(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "测试书")
	os.MkdirAll(filepath.Join(book, "全本"), 0755)
	os.MkdirAll(filepath.Join(book, "素材"), 0755)
	full := filepath.Join(book, "全本", "测试书·全本.md")
	os.WriteFile(full, []byte("正文"), 0644)
	os.WriteFile(filepath.Join(book, "素材", "人物生成提示词.md"), []byte("## 角色\n甲"), 0644)

	// 文件输入:dir 必须是书根(非 全本 子目录)
	f, d := resolveNovelPath(full)
	if f != full {
		t.Fatalf("应原样返回全本文件, got %s", f)
	}
	if d != book {
		t.Fatalf("novel_dir 应上提到书根 %s, got %s", book, d)
	}
	if !strings.HasSuffix(filepath.ToSlash(d), "/测试书") || strings.HasSuffix(filepath.ToSlash(d), "/全本") {
		t.Errorf("novel_dir 不得指向 全本 子目录: %s", d)
	}

	// 目录输入(旧链路行为不回归):dir=书根本身
	f2, d2 := resolveNovelPath(book)
	if d2 != book || f2 != full {
		t.Fatalf("目录输入 dir 应为书根、file 应解析到全本 md, got (%s, %s)", f2, d2)
	}
}

func TestNovelRootDirSelfHealsLayoutSubdir(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "测试书2")
	os.MkdirAll(filepath.Join(book, "全本"), 0755)
	os.MkdirAll(filepath.Join(book, "素材"), 0755)

	// 存量错 config:novel_dir=…/全本(其下无素材)→ novelRootDir 上提书根
	ctx := &manjuCtx{P: map[string]any{"novel_dir": filepath.Join(book, "全本")}}
	if got := ctx.novelRootDir(); got != book {
		t.Fatalf("存量 novel_dir 指向 全本 子目录应自愈到书根 %s, got %s", book, got)
	}
	// 正确 config(书根)不受影响
	ctx2 := &manjuCtx{P: map[string]any{"novel_dir": book}}
	if got := ctx2.novelRootDir(); got != book {
		t.Fatalf("正确 novel_dir 不应被改动, got %s", got)
	}
	// 真在素材子目录下有素材的目录(极端布局)不自愈误伤:…/素材 下有 素材/ 才保持
	nested := filepath.Join(book, "素材")
	os.MkdirAll(filepath.Join(nested, "素材"), 0755)
	ctx3 := &manjuCtx{P: map[string]any{"novel_dir": nested}}
	if got := ctx3.novelRootDir(); got != nested {
		t.Fatalf("其下确有 素材/ 的目录不应上提, got %s", got)
	}
}
