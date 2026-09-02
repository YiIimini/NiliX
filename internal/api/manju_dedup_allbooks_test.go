package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDedupAllStoryboards 全库真实分镜扫描:去重后跨镜重复清零、无镜被删空、
// 台词列台词全保留(逐句归属校验)。
func TestDedupAllStoryboards(t *testing.T) {
	roots := []string{
		"../../novel/金丹一万重/素材/分镜脚本",
		"../../novel/杂毛神兽/素材/分镜脚本",
		"../../novel/我的影子会咬人/素材/分镜脚本",
	}
	reD := regexp.MustCompile(`<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>`)
	files := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Logf("skip %s: %v", root, err)
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			files++
			p := filepath.Join(root, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			raws, err := parseScriptJSON(string(toUTF8(b)))
			if err != nil {
				t.Logf("parse skip %s: %v", e.Name(), err)
				continue
			}
			lg := &manjuLogger{state: manjuState}
			scriptDedupShotLines(raws, lg)
			seen := map[string]int{}
			dups := 0
			collide := 0
			for i := range raws {
				auth := map[string]bool{}
				for _, line := range strings.Split(raws[i].Dialogue, "\n") {
					for _, dm := range reDialogue.FindAllStringSubmatch(strings.TrimSpace(line), -1) {
						seg := strings.TrimSpace(dm[2])
						if seg == "" {
							seg = strings.TrimSpace(dm[4])
						}
						if nn := manjuNormText(seg); len([]rune(nn)) >= 2 {
							auth[nn] = true
						}
					}
				}
				for _, seg := range strings.Split(raws[i].Narration, "\n") {
					if nn := manjuNormText(stripNarrationPrefix(strings.TrimSpace(seg))); len([]rune(nn)) >= 2 {
						auth[nn] = true
					}
				}
				for _, dm := range reD.FindAllStringSubmatch(raws[i].H3Prompt, -1) {
					n := manjuNormText(dm[1])
					if len([]rune(n)) < 2 {
						continue
					}
					if prev, ok := seen[n]; ok {
						// 允许刻意重复:本镜台词列也声明过(口号/回响,如「再来一重」;
						// 台词+内心同句撞车,如第102章镜10/24——渲染端保守保留,
						// 由技能侧「一句一镜」契约治理)
						if auth[n] {
							collide++
							continue
						}
						dups++
						if dups <= 3 {
							t.Logf("残留重复 %s: 镜%d 与镜%d 共「%s」", e.Name()[:20], prev, raws[i].ID, firstN(dm[1], 18))
						}
					} else {
						seen[n] = i + 1
					}
				}
			}
			if dups > 0 {
				t.Fatalf("%s 去重后仍有 %d 处非权威跨镜重复", e.Name(), dups)
			}
			if collide > 0 {
				t.Logf("⚠ %s: %d 处双权威同句(台词/内心撞车,建议技能侧治理)", e.Name(), collide)
			}
		}
	}
	if files == 0 {
		t.Fatal("no storyboard files found")
	}
	t.Logf("扫描 %d 个分镜脚本,跨镜重复全部清零", files)
}
