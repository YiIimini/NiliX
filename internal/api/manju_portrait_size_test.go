package api

import (
	"strings"
	"testing"
)

// 定妆照尺寸固定 1024×1024,与项目画幅/分辨率解耦:横屏/竖屏/不同档位项目生成的定妆照同尺寸同构图,
// 保证同一角色跨项目形象一致(H3 参考图内容随画幅漂移会导致角色不一致)。
func TestManjuPortraitFixedSize(t *testing.T) {
	// 构造两个画幅完全不同的 ctx(横屏 1344x768 / 竖屏 768x1344),风格非写实走 SDXL
	for _, c := range []struct{ name string; w, h int }{
		{"landscape", 1344, 768},
		{"portrait", 768, 1344},
	} {
		ctx := &manjuCtx{style: "2.5d+ink", w: c.w, h: c.h, R: map[string]any{"char_models": map[string]any{"男": "animagine-xl-3.1.safetensors"}, "animagine_ckpt": "animagine-xl-3.1.safetensors"}}
		wf := ctx.portraitWF("a boy", 123, "manju_asset", map[string]any{"gender": "男"}, "", 0)
		var w, h int
		for _, node := range wf {
			m, _ := node.(map[string]any)
			if m == nil {
				continue
			}
			if ct, _ := m["class_type"].(string); strings.Contains(ct, "LatentImage") {
				ins, _ := m["inputs"].(map[string]any)
				w, _ = ins["width"].(int)
				h, _ = ins["height"].(int)
			}
		}
		if w != manjuPortraitW || h != manjuPortraitH {
			t.Errorf("[%s] 定妆照尺寸 = %dx%d, want %dx%d(固定,与画幅解耦)", c.name, w, h, manjuPortraitW, manjuPortraitH)
		}
	}
}

// 视图一致性回归:initImage 非空时工作流必须走 img2img——LoadImage + VAEEncode
// 替代 EmptyLatent,KSampler denoise=0.6(保留主图身份,防视图生成不相干新角色)
func TestPortraitWFImg2Img(t *testing.T) {
	ctx := &manjuCtx{style: "real", R: map[string]any{
		"z_image_unet": "u.safetensors", "z_image_clip": "c.safetensors", "z_image_vae": "v.safetensors",
	}}
	wf := ctx.portraitWF("side view", 42, "manju_asset", map[string]any{"gender": "女"}, "dir_char_main_zz.png", 0.8)
	hasLoad, hasEnc, hasEmpty := false, false, false
	denoise := -1.0
	for _, node := range wf {
		m, _ := node.(map[string]any)
		if m == nil {
			continue
		}
		switch m["class_type"] {
		case "LoadImage":
			hasLoad = true
			if ins, _ := m["inputs"].(map[string]any); ins["image"] != "dir_char_main_zz.png" {
				t.Fatalf("LoadImage 未指向主图: %v", ins)
			}
		case "VAEEncode":
			hasEnc = true
		case "EmptySD3LatentImage", "EmptyLatentImage":
			hasEmpty = true
		case "KSampler":
			if ins, _ := m["inputs"].(map[string]any); ins["denoise"] != nil {
				denoise, _ = ins["denoise"].(float64)
			}
		}
	}
	if !hasLoad || !hasEnc {
		t.Fatalf("img2img 应含 LoadImage+VAEEncode: load=%v enc=%v", hasLoad, hasEnc)
	}
	if hasEmpty {
		t.Fatalf("img2img 不应使用 EmptyLatent")
	}
	if denoise != 0.8 {
		t.Fatalf("img2img denoise 应为 0.8,得到 %v", denoise)
	}
	// 无 initImage → 纯文生图(EmptyLatent + denoise 1.0)
	wf2 := ctx.portraitWF("portrait", 1, "manju_asset", map[string]any{"gender": "女"}, "", 0)
	hasEmpty2, d2 := false, -1.0
	for _, node := range wf2 {
		m, _ := node.(map[string]any)
		if m == nil {
			continue
		}
		if m["class_type"] == "EmptySD3LatentImage" || m["class_type"] == "EmptyLatentImage" {
			hasEmpty2 = true
		}
		if m["class_type"] == "KSampler" {
			if ins, _ := m["inputs"].(map[string]any); ins["denoise"] != nil {
				d2, _ = ins["denoise"].(float64)
			}
		}
	}
	if !hasEmpty2 || d2 != 1.0 {
		t.Fatalf("纯文生图应 EmptyLatent + denoise 1.0: empty=%v denoise=%v", hasEmpty2, d2)
	}
}
