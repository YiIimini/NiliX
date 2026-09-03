package manju

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDedupRealChapter1 真实第1章数据验证:镜1 串句删除、镜2/3 保留、跨镜重复清零。
func TestDedupRealChapter1(t *testing.T) {
	b, err := os.ReadFile("../../novel/金丹一万重/素材/分镜脚本/第1章_全宗第九千九百九十九_分镜脚本.json")
	if err != nil {
		t.Skip("real storyboard not present")
	}
	raws, err := parseScriptJSON(string(toUTF8(b)))
	if err != nil {
		t.Fatal(err)
	}
	lg := &manjuLogger{state: manjuState}
	scriptDedupShotLines(raws, lg)
	reD := regexp.MustCompile(`<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>`)
	seen := map[string]bool{}
	dups := 0
	for i := range raws {
		for _, m := range reD.FindAllStringSubmatch(raws[i].H3Prompt, -1) {
			n := manjuNormText(m[1])
			if len([]rune(n)) < 2 {
				continue
			}
			if seen[n] {
				dups++
				t.Logf("残留重复: 镜%d %q", raws[i].ID, m[1])
			}
			seen[n] = true
		}
	}
	if dups > 0 {
		t.Fatalf("第1章去重后仍有 %d 处跨镜重复", dups)
	}
	// 镜1 串句已删:不再包含镜2/3 台词
	if strings.Contains(raws[0].H3Prompt, "测完灵") || strings.Contains(raws[0].H3Prompt, "天塌下来") {
		t.Fatalf("镜1 串句未删净")
	}
	if !strings.Contains(raws[0].H3Prompt, "今儿大比") {
		t.Fatalf("镜1 本镜台词被误删")
	}
	if !strings.Contains(raws[1].H3Prompt, "测完灵") || !strings.Contains(raws[2].H3Prompt, "天塌下来") {
		t.Fatalf("镜2/3 权威台词被误删")
	}
}
