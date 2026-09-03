package manju

// 测试辅助:manjuRoot 常量已由 InitPaths 运行时解析取代,测试统一用独立临时根初始化包级变量
// (生产路径由 main.InitPaths 注入;测试用固定临时根保证各用例互不污染真实项目)。

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	paths.ManjuRootDir = filepath.Join(os.TempDir(), "nilix-test-manju")
	paths.ComfySharedDir = filepath.Join(os.TempDir(), "nilix-test-comfy-shared")
	paths.NovelSkillDir = paths.LegacyNovelSkill() // 技能库用真实库(测试机已装;词库加载用例依赖它)
	// 全库扫描类用例(TestJSONScriptParseAll 等)指向真实书库;书库不存在时相关用例自动 skip
	if dirExists(filepath.Join("..", "..", "novel")) {
		paths.NovelRootDir = filepath.Join("..", "..", "novel")
	}
	_ = os.MkdirAll(paths.ManjuRootDir, 0755)
	os.Exit(m.Run())
}

// allBookDirs 动态列出书库全部书籍目录。不写死书名:用户删书/加书是常态,
// 硬编码清单会随书库漂移失效(2026-09-03 拆包时 7 本旧书名全部失效的教训)。
func allBookDirs(t *testing.T) []string {
	t.Helper()
	if paths.NovelRootDir == "" {
		t.Skip("无真实书库(novel/ 不存在),跳过全库扫描用例")
	}
	entries, err := os.ReadDir(paths.NovelRootDir)
	if err != nil {
		t.Skipf("书库不可读 %s: %v", paths.NovelRootDir, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, filepath.Join(paths.NovelRootDir, e.Name()))
		}
	}
	if len(dirs) == 0 {
		t.Skip("书库为空,跳过全库扫描用例")
	}
	return dirs
}
