// Package render 实现 H3 逐镜渲染引擎：构造 ComfyUI 工作流、提交、轮询并落盘产物。
package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nilix/internal/backend"
	"nilix/internal/config"
)

// Renderer 把单个镜头渲染成视频文件。
type Renderer struct {
	comfy      *backend.ComfyUIClient
	cfg        config.RenderSettings
	outDir     string
	comfyInput string
}

// NewRenderer 构造渲染器。
func NewRenderer(comfy *backend.ComfyUIClient, cfg config.RenderSettings, outDir, comfyInput string) *Renderer {
	return &Renderer{comfy: comfy, cfg: cfg, outDir: outDir, comfyInput: comfyInput}
}

// alignFrames 把帧数对齐到 H3 的 17k+5 帧网格。
func alignFrames(frames int) int {
	for frames%17 != 5 {
		frames++
	}
	return frames
}

// durationToFrames 秒数转帧数（对齐网格）。
func durationToFrames(sec float64, fps int) int {
	frames := int(sec*float64(fps) + 0.5)
	if frames < 5 {
		frames = 5
	}
	return alignFrames(frames)
}

// buildT2VWorkflow 构造 H3 文生视频（t2va）工作流，节点结构与官方模板一致。
func buildT2VWorkflow(cfg config.RenderSettings, prompt string, length, seed int, prefix string) map[string]any {
	return map[string]any{
		"1":  map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": cfg.UnetFL2VA, "weight_dtype": "default"}},
		"2":  map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{"clip_name": cfg.Clip, "type": "minimax"}},
		"3":  map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": cfg.VaeVideo}},
		"4":  map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": cfg.VaeAudio}},
		"5":  map[string]any{"class_type": "MiniMaxH3SigmaShift", "inputs": map[string]any{"model": []any{"1", 0}, "shift_video": 12.0, "shift_audio": 3.0}},
		"6":  map[string]any{"class_type": "MiniMaxH3ImageToVideo", "inputs": map[string]any{"clip": []any{"2", 0}, "vae": []any{"3", 0}, "prompt": prompt, "width": cfg.Width, "height": cfg.Height, "length": length}},
		"7":  map[string]any{"class_type": "KSamplerSelect", "inputs": map[string]any{"sampler_name": "res_multistep"}},
		"8":  map[string]any{"class_type": "BasicScheduler", "inputs": map[string]any{"model": []any{"5", 0}, "scheduler": "simple", "steps": cfg.Steps, "denoise": 1.0}},
		"9":  map[string]any{"class_type": "BasicGuider", "inputs": map[string]any{"model": []any{"5", 0}, "conditioning": []any{"6", 0}}},
		"10": map[string]any{"class_type": "RandomNoise", "inputs": map[string]any{"noise_seed": seed}},
		"11": map[string]any{"class_type": "SamplerCustomAdvanced", "inputs": map[string]any{"noise": []any{"10", 0}, "guider": []any{"9", 0}, "sampler": []any{"7", 0}, "sigmas": []any{"8", 0}, "latent_image": []any{"6", 1}}},
		"12": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"11", 0}, "vae": []any{"3", 0}}},
		"13": map[string]any{"class_type": "VAEDecodeAudio", "inputs": map[string]any{"samples": []any{"11", 1}, "vae": []any{"4", 0}}},
		"14": map[string]any{"class_type": "CreateVideo", "inputs": map[string]any{"images": []any{"12", 0}, "fps": float64(cfg.FPS), "audio": []any{"13", 0}}},
		"15": map[string]any{"class_type": "SaveVideo", "inputs": map[string]any{"video": []any{"14", 0}, "filename_prefix": prefix, "format": "mp4", "codec": "auto"}},
	}
}

// RenderShot 渲染单个镜头，下载产物到本地并返回文件路径。
func (r *Renderer) RenderShot(ctx context.Context, prompt string, durationSec float64, seed int, shotID string, refImages []string) (string, error) {
	if err := os.MkdirAll(r.outDir, 0o755); err != nil {
		return "", err
	}
	length := durationToFrames(durationSec, r.cfg.FPS)
	var wf map[string]any
	if len(refImages) > 0 {
		wf = buildR2VWorkflow(r.cfg, prompt, length, seed, refImages, "manju/"+shotID)
	} else {
		wf = buildT2VWorkflow(r.cfg, prompt, length, seed, "manju/"+shotID)
	}

	pr, err := r.comfy.SubmitPrompt(ctx, wf, "NiliX")
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		h, err := r.comfy.History(ctx, pr.PromptID)
		if err != nil {
			return "", err
		}
		entry, ok := h[pr.PromptID]
		if ok {
			if entry.Status.StatusStr == "error" {
				return "", fmt.Errorf("渲染失败（prompt_id=%s）", pr.PromptID)
			}
			if entry.Status.Completed || entry.Status.StatusStr == "success" {
				for _, out := range entry.Outputs {
					for _, m := range append(append([]backend.MediaFile{}, out.Images...), out.Videos...) {
						if isVideo(m.Filename) {
							data, err := r.comfy.Download(ctx, m.Filename, m.Subfolder, m.Type)
							if err != nil {
								return "", err
							}
							dst := filepath.Join(r.outDir, shotID+".mp4")
							if err := os.WriteFile(dst, data, 0o644); err != nil {
								return "", err
							}
							return dst, nil
						}
					}
				}
				return "", fmt.Errorf("渲染完成但无视频输出")
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return "", fmt.Errorf("镜头 %s 渲染超时（>15 分钟）", shotID)
}

// isVideo 判断文件名是否为视频文件。
func isVideo(fn string) bool {
	low := strings.ToLower(fn)
	return strings.HasSuffix(low, ".mp4") || strings.HasSuffix(low, ".webm") || strings.HasSuffix(low, ".mov")
}
