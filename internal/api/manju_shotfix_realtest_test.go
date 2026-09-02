package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestShotFixRealShadows 真实《我的影子会咬人》EP01 数据验证分镜脚本内容级修复:
// ①镜12 非内心戏 chibi Subject 空引用 → 删行(Q版乱入根治)
// ②镜7 内心戏 chibi Subject 空引用 → 保句清引用
// ③镜6 画外·路人台词 "The spectator shouts" → off-screen voiceover 标注
// ④镜7/19 沈照 black hair → 按角色卡校正为 platinum-white
func TestShotFixRealShadows(t *testing.T) {
	planB, err := os.ReadFile("../../manju/我的影子会咬人/analysis/EP01_direct_plan.json")
	if err != nil {
		t.Skip("real plan not present")
	}
	var plan struct {
		Characters []map[string]any `json:"characters"`
		Shots      []map[string]any `json:"shots"`
	}
	if err := json.Unmarshal(planB, &plan); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{workdir: "D:/Ai/NiliX/manju/我的影子会咬人"}
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
		s := manjuShot{
			ID:         sid,
			Characters: chars,
			Scene:      str(sm["scene"]),
			Dialogue:   str(sm["dialogue"]),
			Narration:  str(sm["narration"]),
			Duration:   int(sm["duration"].(float64)),
			H3Prompt:   str(sm["h3_prompt"]),
		}
		hp := ctx.finalizeAlignedPrompt(s.H3Prompt, s, manjuExpectPicSlots(s))
		switch sid {
		case 6:
			// 镜6:画外·路人台词必须带 off-screen voiceover
			if !strings.Contains(hp, "off-screen voiceover") {
				t.Fatalf("镜6 画外台词应标注 off-screen voiceover")
			}
			// 且不应再是 "The spectator shouts: <d>" 裸开口(画面角色动嘴)
			if m := reOffscreenAnchor.FindString(hp); m == "" {
				t.Fatalf("镜6 应有 off-screen 锚点")
			}
		case 7:
			// 镜7:沈照发色校正为 platinum-white(角色卡权威)
			if strings.Contains(hp, "black hair") {
				t.Fatalf("镜7 沈照发色应为 platinum-white, 残留 black hair")
			}
			if !strings.Contains(hp, "platinum-white hair") && !strings.Contains(hp, "platinum hair") {
				t.Fatalf("镜7 沈照应含卡发色 platinum hair")
			}
			// 内心戏 chibi 空引用清理(不再有 "in ;")
			if strings.Contains(hp, "in ;") {
				t.Fatalf("镜7 内心戏 chibi 行仍有空引用 in ;")
			}
		case 12:
			// 镜12:非内心戏 chibi Subject 删行(无 Q版乱入)
			if strings.Contains(hp, "chibi") && strings.Contains(hp, "<Subject 4>") {
				t.Fatalf("镜12 非内心戏 chibi Subject 应删除")
			}
			if strings.Contains(hp, "in ;") {
				t.Fatalf("镜12 仍有空引用 in ;")
			}
		case 19:
			if strings.Contains(hp, "black hair") {
				t.Fatalf("镜19 沈照发色应为 platinum-white, 残留 black hair")
			}
		}
	}
}
