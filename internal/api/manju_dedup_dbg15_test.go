package api

import (
	"os"
	"testing"
)

func TestDedupDbg15(t *testing.T) {
	b, err := os.ReadFile("../../novel/王牌三岁半/素材/分镜脚本/第1章_0.3%_分镜脚本.json")
	if err != nil {
		t.Skip()
	}
	raws, err := parseScriptJSON(string(toUTF8(b)))
	if err != nil {
		t.Fatal(err)
	}
	lg := &manjuLogger{state: manjuState}
	// 先跑 validate(补写),再跑 dedup(与 buildPlanFromRaws 同序)
	scriptValidateShots(raws, lg)
	scriptDedupShotLines(raws, lg)
	for _, r := range raws {
		if r.ID != 15 {
			continue
		}
		t.Logf("镜15 dialogue=%q", r.Dialogue)
		t.Logf("镜15 h3 联邦史上最低 出现次数: %d", countStr(r.H3Prompt, "联邦史上最低"))
	}
}

func countStr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); {
		j := indexStr(s, sub, i)
		if j < 0 {
			break
		}
		n++
		i = j + len(sub)
	}
	return n
}

func indexStr(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
