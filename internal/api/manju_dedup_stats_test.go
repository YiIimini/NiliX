package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDedupStats 全库去重影响统计(日志断言,报告用)
func TestDedupStats(t *testing.T) {
	roots := []string{
		"../../novel/金丹一万重/素材/分镜脚本",
		"../../novel/杂毛神兽/素材/分镜脚本",
		"../../novel/我的影子会咬人/素材/分镜脚本",
	}
	totalFiles, totalDedup := 0, 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			totalFiles++
			b, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			raws, err := parseScriptJSON(string(toUTF8(b)))
			if err != nil {
				continue
			}
			manjuState.mu.Lock()
			manjuState.log = ""
			manjuState.mu.Unlock()
			lg := &manjuLogger{state: manjuState}
			scriptDedupShotLines(raws, lg)
			manjuState.mu.Lock()
			n := strings.Count(manjuState.log, "配音去重")
			manjuState.mu.Unlock()
			totalDedup += n
		}
	}
	t.Logf("扫描 %d 个分镜脚本,共去重 %d 处重复台词(跨镜串句+镜内重复)", totalFiles, totalDedup)
}
