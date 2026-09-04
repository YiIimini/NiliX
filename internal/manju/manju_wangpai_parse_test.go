package manju

import (
	"os"
	"strings"
	"testing"
)

// TestWangpaiParseInner 王牌三岁半真实脚本重解析:镜11 内心两行全进 narration,
// 镜12 内心保留,不再有"无说话人 dialogue 残留"。
func TestWangpaiParseInner(t *testing.T) {
	b, err := os.ReadFile("../../novel/王牌三岁半/素材/分镜脚本/第1章_0.3%_分镜脚本.json")
	if err != nil {
		t.Skip("脚本不存在")
	}
	raws, _, err := parseScriptJSON(string(toUTF8(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raws {
		switch r.ID {
		case 11:
			if !strings.Contains(r.Narration, "豆豆明明说") || !strings.Contains(r.Narration, "怎么只有一个冰凉的环") {
				t.Fatalf("镜11 内心两句都应进 narration: %q", r.Narration)
			}
			if r.Dialogue != "" {
				t.Fatalf("镜11 dialogue 不应有残留: %q", r.Dialogue)
			}
		case 12:
			if !strings.Contains(r.Narration, "想回家") {
				t.Fatalf("镜12 内心应保留: %q", r.Narration)
			}
		}
	}
}
