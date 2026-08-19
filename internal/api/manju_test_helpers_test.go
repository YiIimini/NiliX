package api

// 测试辅助:manjuRoot 常量已由 InitPaths 运行时解析取代,测试统一用独立临时根初始化包级变量
// (生产路径由 main.InitPaths 注入;测试用固定临时根保证各用例互不污染真实项目)。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	ManjuRootDir = filepath.Join(os.TempDir(), "nilix-test-manju")
	ComfySharedDir = filepath.Join(os.TempDir(), "nilix-test-comfy-shared")
	NovelSkillDir = legacyNovelSkill // 技能库用真实库(测试机已装;词库加载用例依赖它)
	_ = os.MkdirAll(ManjuRootDir, 0755)
	os.Exit(m.Run())
}
