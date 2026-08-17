package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nilix/internal/config"
)

// buildT2IWorkflow 构造 Z-Image 文生图工作流（生成参考图）。
func buildT2IWorkflow(cfg config.RenderSettings, prompt string, width, height, seed int, prefix string) map[string]any {
	return map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": cfg.ZImageUnet, "weight_dtype": "default"}},
		"2": map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{"clip_name": cfg.ZImageClip, "type": "lumina2"}},
		"3": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": cfg.ZImageVae}},
		"4": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": prompt, "clip": []any{"2", 0}}},
		"5": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "", "clip": []any{"2", 0}}},
		"6": map[string]any{"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": width, "height": height, "batch_size": 1}},
		"7": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"model": []any{"1", 0}, "seed": seed, "steps": 8, "cfg": 1.0, "sampler_name": "euler", "scheduler": "simple", "positive": []any{"4", 0}, "negative": []any{"5", 0}, "latent_image": []any{"6", 0}, "denoise": 1.0}},
		"8": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"7", 0}, "vae": []any{"3", 0}}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"images": []any{"8", 0}, "filename_prefix": prefix}},
	}
}

// generateImage 生成一张参考图，下载保存到本地目录并返回文件名。
func (r *Renderer) generateImage(ctx context.Context, prompt string, width, height, seed int, prefix string, saveDir string) (string, error) {
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return "", err
	}
	wf := buildT2IWorkflow(r.cfg, prompt, width, height, seed, "assets/"+prefix)
	pr, err := r.comfy.SubmitPrompt(ctx, wf, "NiliX")
	if err != nil {
		return "", err
	}

	for {
		h, err := r.comfy.History(ctx, pr.PromptID)
		if err != nil {
			return "", err
		}
		if entry, ok := h[pr.PromptID]; ok {
			if entry.Status.StatusStr == "error" {
				return "", fmt.Errorf("生成参考图失败（prompt_id=%s）", pr.PromptID)
			}
			if entry.Status.Completed || entry.Status.StatusStr == "success" {
				for _, out := range entry.Outputs {
					for _, img := range out.Images {
						data, err := r.comfy.Download(ctx, img.Filename, img.Subfolder, img.Type)
						if err != nil {
							return "", err
						}
						dst := filepath.Join(saveDir, prefix+".png")
						if err := os.WriteFile(dst, data, 0o644); err != nil {
							return "", err
						}
						return prefix + ".png", nil
					}
				}
				return "", fmt.Errorf("生成参考图无输出")
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// timeAfter 是 time.After 的别名（便于测试注入）。
func timeAfter(d time.Duration) <-chan time.Time {
	return time.After(d)
}
