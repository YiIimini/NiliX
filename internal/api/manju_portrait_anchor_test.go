package api

import (
	"strings"
	"testing"
)

// 写实拟动漫 prompt 硬规则(2026-08-24 用户规则升级):定妆照 prompt 必须
// ①禁日漫(不出现裸 anime 引导)②禁真人(photorealistic/real human/realistic photo 全替换)
// ③强制附加拟动漫锚(东方/中式面孔,非日漫,非真人,防侵权)。
func TestManjuPortraitPromptAnchor(t *testing.T) {
	// 素材里的真人写实措辞必须被替换(注意:锚里的 "not a photorealistic photo" 是防真人否定句,
	// 属正确措辞;这里只检查"独立的真人引导词"是否被替换,即替换后不应再出现 photorealistic, 后跟逗号的用法)
	p := manjuPortraitPrompt("Cinematic film still, photorealistic, a pretty 22-year-old East Asian woman, grey-blue apron, movie poster quality", "amber almond eyes, small mole on left earlobe, black hair bun, old wooden hairpin")
	if strings.Contains(strings.ToLower(p), "photorealistic, ") {
		t.Fatalf("真人写实措辞残留: %s", p)
	}
	for _, banned := range []string{"real human, ", "realistic photo, ", "realistic photograph, ", "realistic human, ", "photograph, "} {
		if strings.Contains(strings.ToLower(p), banned) {
			t.Fatalf("真人写实措辞残留 %q: %s", banned, p)
		}
	}
	// 拟动漫锚必须附加
	for _, want := range []string{"semi-realistic stylized illustration", "East Asian/Chinese", "not a Japanese anime", "not a photorealistic photo", "avoid japanese-style facial features", "avoid resembling any real person"} {
		if !strings.Contains(p, want) {
			t.Fatalf("拟动漫锚缺失 %q: %s", want, p)
		}
	}
	// 幂等:已带锚不重复追加
	p2 := manjuPortraitPrompt(p, "amber almond eyes, small mole on left earlobe")
	if strings.Count(p2, "not a Japanese anime") != 1 {
		t.Fatalf("拟动漫锚重复追加: %s", p2)
	}
}

// 风格措辞去裸 anime:2.5d/anime 预设的 asset 措辞不得含裸 "anime"(SDXL 关键词拉向日漫),
// 必须表达"东方风格化插画/半写实"方向。
func TestManjuStyleAssetNoBareAnime(t *testing.T) {
	for _, style := range []string{"2.5d", "anime"} {
		asset := manjuAssetStyle(style)
		if strings.Contains(asset, "anime style") || strings.Contains(asset, "detailed anime") {
			t.Fatalf("%s asset 措辞含裸 anime 引导: %q", style, asset)
		}
	}
	// real 不再引导真人照片
	assetReal := manjuAssetStyle("real")
	if strings.Contains(assetReal, "photorealistic") || strings.Contains(assetReal, "real human") {
		t.Fatalf("real asset 措辞引导真人: %q", assetReal)
	}
}

// scriptImagePrompt 素材抽卡同样执行拟动漫规则(真人措辞替换+锚附加)。
func TestScriptImagePromptNoRealNoAnime(t *testing.T) {
	block := "Cinematic film still, photorealistic, a 22-year-old East Asian woman, long black hair, movie poster quality"
	out := scriptImagePrompt(block, manjuAssetStyle("2.5d"))
	if strings.Contains(strings.ToLower(out), "photorealistic, ") {
		t.Fatalf("脚本素材抽卡仍含真人措辞: %s", out)
	}
	if !strings.Contains(out, "not a Japanese anime") || !strings.Contains(out, "semi-realistic stylized illustration") {
		t.Fatalf("脚本素材抽卡缺拟动漫锚: %s", out)
	}
}
