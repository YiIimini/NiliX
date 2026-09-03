package manju

import (
	"strings"
	"testing"
)

// 禁日漫硬规则(2026-08-24 用户反复强调):定妆照模型绝不允许日漫系 checkpoint——
// 即便 char_models/animagine_ckpt 显式配置了 animagine 等日漫模型,characterCkpt 必须强制回退。
func TestCharacterCkptBanAnime(t *testing.T) {
	ctx := &manjuCtx{R: map[string]any{
		"char_models":    map[string]any{"男": "animagine-xl-3.1.safetensors", "女": "animagine-xl-3.1.safetensors"},
		"animagine_ckpt": "animagine-xl-3.1.safetensors",
	}}
	// 男女都显式配了 animagine(日漫)→ 必须回退通用底
	if got := ctx.characterCkpt(map[string]any{"gender": "女"}); got != "" {
		t.Fatalf("女 用日漫模型应被拒并回退, got %q", got)
	}
	if got := ctx.characterCkpt(map[string]any{"gender": "男"}); got != "" {
		t.Fatalf("男 用日漫模型应被拒并回退, got %q", got)
	}
	// 无性别 → 兜底 animagine_ckpt(也是日漫)→ 同样拒绝
	if got := ctx.characterCkpt(map[string]any{}); got != "" {
		t.Fatalf("无性别兜底日漫模型应被拒, got %q", got)
	}
	// 非日漫模型正常放行
	ctx2 := &manjuCtx{R: map[string]any{
		"char_models":    map[string]any{"女": "sd_xl_base_1.0.safetensors"},
		"animagine_ckpt": "sd_xl_base_1.0.safetensors",
	}}
	if got := ctx2.characterCkpt(map[string]any{"gender": "女"}); got != "sd_xl_base_1.0.safetensors" {
		t.Fatalf("非日漫模型应放行, got %q", got)
	}
	// 其他日漫系关键词同样拦截
	for _, kw := range []string{"anything-v3-0.safetensors", "counterfeitxl.safetensors", "meinamix_11.safetensors", "nijijourney-xl.safetensors"} {
		ctx3 := &manjuCtx{R: map[string]any{"char_models": map[string]any{"女": kw}}}
		if got := ctx3.characterCkpt(map[string]any{"gender": "女"}); got != "" {
			t.Fatalf("日漫系 %q 应被拒, got %q", kw, got)
		}
	}
}

// 定妆照引擎(2026-08-24 用户规则,视觉实测定案):
// 一律 Krea-2(强指令跟随→拟漫 stylized illustration,非真人非日漫);Z-Image 出真人照片仅场景图用;
// SDXL 全面禁用。任何风格(写实/2.5d/动漫/水墨)的定妆照都不得出现 SDXL checkpoint 或 Z-Image。
func TestPortraitWFNoAnime(t *testing.T) {
	// 风格化组合(real+2.5d+ink):走 Krea-2(CLIPLoader type=krea2),不得出现 SDXL/Z-Image
	ctx := &manjuCtx{style: "real+2.5d+ink", R: map[string]any{
		"char_engine":     "zimage", // 旧配置 char_engine 不影响:定妆一律 Krea-2
		"krea2_unet":      "ku.safetensors", "krea2_clip": "kc.safetensors", "krea2_vae": "kv.safetensors",
		"z_image_unet":    "u.safetensors", "z_image_clip": "c.safetensors", "z_image_vae": "v.safetensors",
	}}
	wf := ctx.portraitWF("portrait", 1, "manju_asset", map[string]any{"gender": "女"}, "", 0)
	hasCkpt, hasZImage, hasKrea := false, false, false
	for _, node := range wf {
		m, _ := node.(map[string]any)
		if m == nil {
			continue
		}
		switch m["class_type"] {
		case "CheckpointLoaderSimple":
			hasCkpt = true
		case "UNETLoader":
			if ins, _ := m["inputs"].(map[string]any); ins != nil {
				if strings.Contains(strings.ToLower(str(ins["unet_name"])), "z_image") {
					hasZImage = true
				}
			}
		case "CLIPLoader":
			if ins, _ := m["inputs"].(map[string]any); ins != nil && ins["type"] == "krea2" {
				hasKrea = true
			}
		}
	}
	if hasCkpt {
		t.Fatalf("定妆照不得出现 SDXL checkpoint(SDXL 已禁用)")
	}
	if hasZImage {
		t.Fatalf("定妆照不得用 Z-Image(出真人照片,仅场景图可用)")
	}
	if !hasKrea {
		t.Fatalf("定妆照应走 Krea-2(拟漫), got: %v", wf)
	}
	// 纯写实:同样 Krea-2
	ctx2 := &manjuCtx{style: "real", R: map[string]any{
		"krea2_unet": "ku.safetensors", "krea2_clip": "kc.safetensors", "krea2_vae": "kv.safetensors",
		"z_image_unet": "u.safetensors", "z_image_clip": "c.safetensors", "z_image_vae": "v.safetensors",
	}}
	wf2 := ctx2.portraitWF("portrait", 1, "manju_asset", map[string]any{"gender": "女"}, "", 0)
	hasZ2 := false
	for _, node := range wf2 {
		m, _ := node.(map[string]any)
		if m == nil {
			continue
		}
		if m["class_type"] == "UNETLoader" {
			if ins, _ := m["inputs"].(map[string]any); ins != nil && strings.Contains(strings.ToLower(str(ins["unet_name"])), "z_image") {
				hasZ2 = true
			}
		}
	}
	if hasZ2 {
		t.Fatalf("纯写实定妆照也不得用 Z-Image(出真人照片),应走 Krea-2")
	}
}
