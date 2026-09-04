package render

import (
	"fmt"

	"nilix/internal/config"
)

// buildR2VWorkflow 构造 H3 参考生视频（ref2va）工作流，refImages 为参考图文件名（位于 ComfyUI input 目录）。
func buildR2VWorkflow(cfg config.RenderSettings, prompt string, length, seed int, refImages []string, prefix string) map[string]any {
	wf := map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": cfg.UnetRef2VA, "weight_dtype": "default"}},
		"2": map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{"clip_name": cfg.Clip, "type": "minimax"}},
		"3": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": cfg.VaeVideo}},
		"4": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": cfg.VaeAudio}},
		"5": map[string]any{"class_type": "MiniMaxH3SigmaShift", "inputs": map[string]any{"model": []any{"1", 0}, "shift_video": 12.0, "shift_audio": 3.0}},
	}

	// 参考图 LoadImage 节点 + autogrow ref_images。
	refImagesInput := map[string]any{}
	for i, img := range refImages {
		nodeID := fmt.Sprintf("10%d", i) // 100, 101, ...
		wf[nodeID] = map[string]any{"class_type": "LoadImage", "inputs": map[string]any{"image": img}}
		refImagesInput[fmt.Sprintf("ref_image_%d", i)] = []any{nodeID, 0}
	}

	// ref_image_size:max=保留参考图原分辨率编码(官方:identity fidelity 更强,2026-09-04)
	refSize := cfg.RefImageSize
	if refSize != "match" && refSize != "max" {
		refSize = "max"
	}
	wf["50"] = map[string]any{"class_type": "MiniMaxH3ReferenceToVideo", "inputs": map[string]any{
		"clip":           []any{"2", 0},
		"vae":            []any{"3", 0},
		"audio_vae":      []any{"4", 0},
		"prompt":         prompt,
		"width":          cfg.Width,
		"height":         cfg.Height,
		"length":         length,
		"ref_image_size": refSize,
		"ref_images":     refImagesInput,
	}}

	wf["51"] = map[string]any{"class_type": "KSamplerSelect", "inputs": map[string]any{"sampler_name": "res_multistep"}}
	wf["52"] = map[string]any{"class_type": "BasicScheduler", "inputs": map[string]any{"model": []any{"5", 0}, "scheduler": "simple", "steps": cfg.Steps, "denoise": 1.0}}
	wf["53"] = map[string]any{"class_type": "BasicGuider", "inputs": map[string]any{"model": []any{"5", 0}, "conditioning": []any{"50", 0}}}
	wf["54"] = map[string]any{"class_type": "RandomNoise", "inputs": map[string]any{"noise_seed": seed}}
	wf["55"] = map[string]any{"class_type": "SamplerCustomAdvanced", "inputs": map[string]any{"noise": []any{"54", 0}, "guider": []any{"53", 0}, "sampler": []any{"51", 0}, "sigmas": []any{"52", 0}, "latent_image": []any{"50", 1}}}
	wf["56"] = map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"55", 0}, "vae": []any{"3", 0}}}
	wf["57"] = map[string]any{"class_type": "VAEDecodeAudio", "inputs": map[string]any{"samples": []any{"55", 1}, "vae": []any{"4", 0}}}
	wf["58"] = map[string]any{"class_type": "CreateVideo", "inputs": map[string]any{"images": []any{"56", 0}, "fps": float64(cfg.FPS), "audio": []any{"57", 0}}}
	wf["59"] = map[string]any{"class_type": "SaveVideo", "inputs": map[string]any{"video": []any{"58", 0}, "filename_prefix": prefix, "format": "mp4", "codec": "auto"}}
	return wf
}
