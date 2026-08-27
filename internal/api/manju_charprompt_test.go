package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// manjuCharImagePrompt 提示词口径(2026-08-27 角色管理「复制提示词」):
// 候选/正式/视图/Q版各自命中正确的构建链,复制到的即生成时的真实提示词。
func TestManjuCharImagePrompt(t *testing.T) {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	if err := os.MkdirAll(analysis, 0755); err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{
		"characters": []any{map[string]any{
			"id":          "墨姨",
			"gender":      "女",
			"age":         "老年",
			"species":     "人",
			"appearance":  "白发老妇,眼角细纹,发髻银簪",
			"costume":     "深灰布衣",
			"image_prompt": "Front-facing portrait of an elderly Chinese woman, white hair in a bun, ink-wash robes, semi-realistic stylized illustration",
		}},
		"shots": []any{},
	}
	b, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{analysisDir: analysis, episode: "EP01", style: "2.5d", R: map[string]any{}}
	m := ctx.gachaCharInfo("墨姨")
	if len(m) == 0 {
		t.Fatal("角色卡未加载")
	}

	// ① 正面候选/正式:image_prompt + 性别锚(女) + 拟漫锚 + 正面人脸锚
	pos, neg := manjuCharImagePrompt(ctx, m, "墨姨", "", "gacha")
	if !strings.Contains(pos, "elderly Chinese woman") {
		t.Errorf("正面提示词缺 image_prompt: %s", pos)
	}
	if !strings.Contains(pos, "a woman") || !strings.Contains(pos, "feminine") {
		t.Errorf("正面提示词缺性别锚: %s", pos)
	}
	if !strings.Contains(pos, "front-facing") {
		t.Errorf("正面提示词缺正面人脸锚: %s", pos)
	}
	if !strings.Contains(pos, "not a Japanese anime") {
		t.Errorf("正面提示词缺拟漫锚: %s", pos)
	}
	if neg == "" {
		t.Error("负向提示词为空")
	}
	posOfficial, _ := manjuCharImagePrompt(ctx, m, "墨姨", "", "official")
	if posOfficial != pos {
		t.Errorf("正面主图候选/正式口径应一致:\n候选=%s\n正式=%s", pos, posOfficial)
	}

	// ② 正式 side 视图:视角硬锚前置 + 身份锚;strip 剥掉 Front-facing 正面前缀
	posSide, _ := manjuCharImagePrompt(ctx, m, "墨姨", "side", "official")
	if !strings.HasPrefix(posSide, "SIDE PROFILE view") {
		t.Errorf("side 正式图应以视角硬锚开头: %s", posSide)
	}
	if !strings.Contains(posSide, "same character as the reference image") {
		t.Errorf("side 正式图缺身份锚: %s", posSide)
	}
	// ③ side 候选:无视角硬锚/身份锚(抽卡=纯文生图探索)
	posSideGacha, _ := manjuCharImagePrompt(ctx, m, "墨姨", "side", "gacha")
	if strings.Contains(posSideGacha, "SIDE PROFILE view") || strings.Contains(posSideGacha, "same character as the reference image") {
		t.Errorf("side 候选不应带正式资产锚: %s", posSideGacha)
	}

	// ④ Q版:chibi 构建 + 禁写实,无正面人脸锚
	posQ, _ := manjuCharImagePrompt(ctx, m, "墨姨", "q", "official")
	if !strings.Contains(posQ, "chibi") {
		t.Errorf("Q版提示词缺 chibi: %s", posQ)
	}
	if !strings.Contains(posQ, "NOT a realistic human") {
		t.Errorf("Q版提示词缺禁写实: %s", posQ)
	}
	if strings.Contains(posQ, "front-facing portrait") {
		t.Errorf("Q版不应带正面人脸锚: %s", posQ)
	}
}
