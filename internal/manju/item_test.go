package manju
import (
	"strings"
	"testing"
)

func TestManjuIsItem(t *testing.T) {
	// species 权威
	if !manjuIsItem(map[string]any{"species": "神器", "id": "轩辕剑"}) {
		t.Error("species=神器 应判物品")
	}
	if !manjuIsItem(map[string]any{"species": "物品", "id": "灯"}) {
		t.Error("species=物品 应判物品")
	}
	// id 兜底
	if !manjuIsItem(map[string]any{"species": "", "id": "剑灵·青锋"}) {
		t.Error("id 含剑灵 应判物品")
	}
	if !manjuIsItem(map[string]any{"species": "", "role": "法宝·乾坤袋"}) {
		t.Error("role 含法宝 应判物品")
	}
	// 人/兽不误判
	if manjuIsItem(map[string]any{"species": "人", "id": "阿凯"}) {
		t.Error("人不应判物品")
	}
	if manjuIsItem(map[string]any{"species": "妖兽", "id": "小礼"}) {
		t.Error("妖兽不应判物品")
	}
	// 物品不判兽形
	if manjuIsBeast(map[string]any{"species": "神器", "id": "轩辕剑"}) {
		t.Error("物品不应判兽形(走物品链)")
	}
	// 物品锚文本:禁人形(2026-08-29 架构重构:物品走独立管线 manjuItemFinal,
	// 不再经人形主干 manjuPortraitPromptFor——该函数物品分支已删,防补丁回流)
	hp := manjuItemFinal("portrait of 轩辕剑, a majestic ancient sword", map[string]any{"species": "神器", "id": "轩辕剑"})
	if !strings.Contains(hp, "NOT a person") && !strings.Contains(hp, "no human") {
		t.Error("物品提示词缺禁人形锚:", hp)
	}
	if strings.Contains(hp, "East Asian/Chinese character") {
		t.Error("物品提示词不得带人形锚:", hp)
	}
}

func TestManjuIsItemPlant(t *testing.T) {
	// 别惹这盆绿萝·阿碧:灵植走物品渲染链(植物本体),不判兽形(2026-08-29)
	m := map[string]any{"species": "灵植·紫藤精", "id": "阿碧", "role": "正角"}
	if !manjuIsItem(m) {
		t.Error("灵植·紫藤精 应判物品(植物本体渲染)")
	}
	if manjuIsBeast(m) {
		t.Error("植物不应判兽形(走物品链而非兽形锚)")
	}
}
