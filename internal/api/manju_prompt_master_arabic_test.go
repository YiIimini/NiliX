package api

import (
	"strings"
	"testing"
)

// 2026-08-24 实测修复:技能阶段6 生成的 渲染提示词总集.md 用**阿拉伯数字**节标题
// (## 1. 渲染风格提示词 / ## 2. 全局负面提示词 …),而 parseRenderPromptMaster 原先
// 只按中文数字(一/二/三)匹配——导致真实项目的 负面提示词 完全丢失(NegPrompt=空),
// 风格段仅靠标题「统一」二字含「一」侥幸误匹配。
// 本测试用与真实产物完全一致的阿拉伯数字格式验证五段全部解析到位。
func TestParseRenderPromptMasterArabicNumbers(t *testing.T) {
	content := `# 《仙厨小饭馆》渲染提示词总集（渲染管线直用·只读本文件即可开工）

## 1. 渲染风格提示词（全剧统一风格）

` + "```" + `
Cinematic film still, photorealistic, urban fantasy light comedy, warm golden kitchen light against cool blue neon, 8k, movie poster quality
` + "```" + `

## 2. 全局负面提示词

` + "```" + `
japanese anime face, japanese manga face, anime eyes, big sparkly anime eyes, watermark, text, deformed, extra fingers
` + "```" + `

## 3. 角色提示词段

### 3.1 云晚（女主·灵厨/仙厨）
` + "```" + `
Cinematic film still, photorealistic, a pretty 22-year-old East Asian woman, oval face, apricot almond eyes, small mole on left earlobe, holding a chipped iron spatula
` + "```" + `

## 4. 场景提示词段

### 4.1 晚膳小馆
` + "```" + `
Cinematic film still, photorealistic, a small old noodle shop on a rainy old street, warm golden kitchen light against cool blue neon
` + "```" + `

## 5. H3 Ref2VA 六段式母版（转 H3 直通渲染用）

` + "```" + `
subject_definitions: <Subject 1> is the character in <Picture 1> ...
` + "```" + `
`
	a := parseRenderPromptMaster(content)
	if a == nil {
		t.Fatalf("阿拉伯数字节标题总集解析返回 nil(真实技能产物格式,必须支持)")
	}
	if !strings.Contains(a.StylePrompt, "urban fantasy light comedy") {
		t.Errorf("StylePrompt 未解析阿拉伯数字节: %q", a.StylePrompt)
	}
	if !strings.Contains(a.NegPrompt, "japanese anime face") || !strings.Contains(a.NegPrompt, "watermark") {
		t.Errorf("NegPrompt 未解析阿拉伯数字节(2026-08-24 修复点): %q", a.NegPrompt)
	}
	if !strings.Contains(a.CharPrompt, "云晚") || !strings.Contains(a.CharPrompt, "spatula") {
		t.Errorf("CharPrompt 未解析: %q", a.CharPrompt)
	}
	if !strings.Contains(a.ScenePrompt, "晚膳小馆") {
		t.Errorf("ScenePrompt 未解析: %q", a.ScenePrompt)
	}
	if !strings.Contains(a.ExtraPrompt, "subject_definitions") {
		t.Errorf("H3母版未入 ExtraPrompt: %q", a.ExtraPrompt)
	}
}

// 中文数字节标题(旧测试覆盖的格式)必须仍然支持,不得回归
func TestParseRenderPromptMasterChineseNumbersStillWorks(t *testing.T) {
	content := `# 测试书 渲染提示词总集

## 一、漫剧渲染风格提示词
` + "```" + `
Cinematic film still, photorealistic, xianxia fantasy, 8k
` + "```" + `

## 二、全局负面提示词
` + "```" + `
anime, cartoon, watermark
` + "```" + `

## 三、角色提示词
### 3.1 陈鱼
` + "```" + `
dead-fish eyes
` + "```" + `

## 四、场景提示词
| 场景 | 提示词 |
|---|---|
| 山门 | ` + "`" + `Cinematic empty scene of mountain gate` + "`" + ` |
`
	a := parseRenderPromptMaster(content)
	if a == nil {
		t.Fatalf("中文数字节标题总集解析返回 nil")
	}
	if !strings.Contains(a.StylePrompt, "xianxia") {
		t.Errorf("StylePrompt = %q", a.StylePrompt)
	}
	if !strings.Contains(a.NegPrompt, "watermark") {
		t.Errorf("NegPrompt = %q", a.NegPrompt)
	}
	if !strings.Contains(a.CharPrompt, "陈鱼") {
		t.Errorf("CharPrompt = %q", a.CharPrompt)
	}
	if !strings.Contains(a.ScenePrompt, "山门") {
		t.Errorf("ScenePrompt = %q", a.ScenePrompt)
	}
}
