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
		wf := ctx.portraitWF("a boy", 123, "manju_asset", map[string]any{"gender": "男"})
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
