package manju
import (
	"strings"
	"testing"
)

// 阿碧素材卡解析回归(2026-08-29):主形象代码块=image_prompt、species/role 从标题显式字段提取
func TestParseAbiCharCard(t *testing.T) {
	md := `## 1. 阿碧(灵植·紫藤精,伪装绿萝;species: 灵植·紫藤精;role: 正角;非人)

**【主形象·伪装形态提示词】(image_prompt,植物本体)**
` + "```\nthe pothos plant itself: a potted pothos vine with glossy heart-shaped green leaves\n```\n" + `
**【Q版·内心戏专用提示词】(植物精灵萌化)**
` + "```\na chibi-style tiny plant spirit with a round face made of leaves\n```\n"
	cards := parseCharCards(md, "real", false)
	if len(cards) != 1 {
		t.Fatal("应解析出 1 张卡, 实际", len(cards))
	}
	c := cards[0]
	if c["id"] != "阿碧" {
		t.Error("id 错误:", c["id"])
	}
	if c["species"] != "灵植·紫藤精" {
		t.Error("species 未从标题字段提取:", c["species"])
	}
	if c["role"] != "正角" {
		t.Error("role 未从标题字段提取:", c["role"])
	}
	ip := c["image_prompt"].(string)
	if !abiContains(ip, "pothos plant itself") {
		t.Error("image_prompt 应为第一个代码块(主形象植物本体), 实际:", ip[:80])
	}
	if abiContains(ip, "chibi") {
		t.Error("image_prompt 不应是 Q 版:", ip[:80])
	}
}

func abiContains(s, sub string) bool { return strings.Contains(s, sub) }
