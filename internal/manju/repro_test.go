package manju
import (
	"strings"
	"testing"
)

// 复现:别惹这盆绿萝 第1章脚本 shot3 重复 <d> 补写问题
func TestReproScriptPatch(t *testing.T) {
	if !fileExists("D:/Ai/NiliX/manju/别惹这盆绿萝/config.json") {
		t.Skip("别惹这盆绿萝 项目工作区不在(可能被清理),跳过复现测试")
	}
	ctx, err := newManjuCtx("D:/Ai/NiliX/manju/别惹这盆绿萝/config.json", "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx.novel = "D:/Ai/NiliX/novel/别惹这盆绿萝/素材/分镜脚本/第1章_走廊尽头那盆绿萝_分镜脚本.md"
	lg := newManjuLogger(&manjuTask{}, nil, "repro", "EP01")
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatal(err)
	}
	shots, _ := plan["shots"].([]any)
	for _, x := range shots {
		m, _ := x.(map[string]any)
		id, _ := manjuToInt(m["shot_id"])
		if id != 3 {
			continue
		}
		hp := str(m["h3_prompt"])
		n := strings.Count(hp, "<d>")
		dialogue := str(m["dialogue"])
		line := dialogue
		if j := strings.IndexAny(line, ":："); j >= 0 && j < 16 {
			line = strings.TrimSpace(line[j+1:])
		}
		runes := []rune(line)
		key := string(runes[:minInt(8, len(runes))])
		for _, ch := range dialogue {
			if ch > 127 {
				t.Logf("  char %q U+%04X", ch, ch)
			}
		}
		t.Logf("dialogue=%q line=%q key=%q contains=%v", dialogue, line, key, strings.Contains(hp, key))
		t.Logf("shot3 <d> 数量: %d", n)
		if n > 1 {
			t.Errorf("shot3 重复 <d>: %d 处", n)
		}
	}
}
