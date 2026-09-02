package api

import (
	"encoding/json"
	"os"
	"testing"
)

// TestWangpaiNoCrash 崩溃项目真实数据回归(2026-09-02 王牌三岁半 slice bounds
// [1326:573]):对 plan 全部镜头跑最终化(与 ensurePlanAndPrompts 同链),不得 panic。
func TestWangpaiNoCrash(t *testing.T) {
	planB, err := os.ReadFile("../../manju/王牌三岁半/analysis/EP01_direct_plan.json")
	if err != nil {
		t.Skip("王牌三岁半 plan 不存在")
	}
	var plan struct {
		Characters []map[string]any `json:"characters"`
		Shots      []map[string]any `json:"shots"`
	}
	if err := json.Unmarshal(planB, &plan); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{workdir: "D:/Ai/NiliX/manju/王牌三岁半"}
	ctx.charInfo = map[string]map[string]any{}
	for _, c := range plan.Characters {
		ctx.charInfo[str(c["id"])] = c
	}
	for _, sm := range plan.Shots {
		sid := int(sm["shot_id"].(float64))
		var chars []string
		for _, x := range anyArr(sm["characters"]) {
			if c, ok := x.(string); ok {
				chars = append(chars, c)
			}
		}
		s := manjuShot{ID: sid, Characters: chars, Scene: str(sm["scene"]),
			Dialogue: str(sm["dialogue"]), Narration: str(sm["narration"]),
			Duration: int(sm["duration"].(float64)), H3Prompt: str(sm["h3_prompt"])}
		hp := ctx.finalizeAlignedPrompt(s.H3Prompt, s, manjuExpectPicSlots(s))
		if hp == "" {
			t.Fatalf("镜%d 最终化后为空", sid)
		}
	}
	t.Logf("王牌三岁半 %d 镜全量最终化无 panic", len(plan.Shots))
}


