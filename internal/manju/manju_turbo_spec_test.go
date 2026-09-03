package manju

import (
	"fmt"
	"testing"
)

func TestTurboLoRASpec(t *testing.T) {
	cases := []struct {
		name            string
		strength        float64
		sampler, sched  string
		steps           int
	}{
		{"minimax_h3_fl2v_lightx2v_turbo_4step_v0.1_comfy_resized_avg_rank_21_bf16.safetensors", 1.0, "euler", "simple", 4},
		{"MINIMAX_H3_KIJAI_LIGHTX2V.safetensors", 1.0, "euler", "simple", 4},
		{"minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors", 1.0, "euler", "simple", 4},
		{"minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors", 1.0, "euler", "simple", 4},
		{"minimax_h3_turbo_4step_ema.safetensors", 0.8, "res_multistep", "simple", 8},
		{"", 0.8, "res_multistep", "simple", 8},
	}
	for _, c := range cases {
		s := turboLoRASpecOf(c.name)
		if s.Strength != c.strength || s.Sampler != c.sampler || s.Scheduler != c.sched || s.Steps != c.steps {
			t.Errorf("spec(%q) = %+v, want {%.2f %s %s %d}", c.name, s, c.strength, c.sampler, c.sched, c.steps)
		}
	}
	// shift 表:768p 版 6/3,544p 版与 ref2v 版 12/3,旧默认无 shift
	if s := turboLoRASpecOf("minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors"); s.VideoShift != 6 || s.AudioShift != 3 {
		t.Errorf("768p shift = %v/%v, want 6/3", s.VideoShift, s.AudioShift)
	}
	if s := turboLoRASpecOf("minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors"); s.VideoShift != 12 || !s.R2VOnly {
		t.Errorf("ref2v spec = %+v, want shift12/R2VOnly", s)
	}
	if s := turboLoRASpecOf("minimax_h3_turbo_4step_ema.safetensors"); s.VideoShift != 0 {
		t.Errorf("旧默认不应有 shift, got %v", s.VideoShift)
	}
	// 工作流参数透传:空镜(FL2V)挂 768p v1.1 → 1.0 / euler + SigmaShift(6,3)
	base := map[string]any{
		"turbo_lora": "minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors",
		"unet_ref2va": "u.safetensors", "unet_fl2va": "f.safetensors",
		"vae_video": "v.safetensors", "vae_audio": "a.safetensors",
		"steps": 20, "turbo_steps": 4,
	}
	wf := h3RenderWorkflow(base, 1, 768, 1344, 145, 4, "cache", false, false, 0, 1)
	got := map[string]any{}
	for _, n := range wf {
		m := n.(map[string]any)
		switch m["class_type"] {
		case "LoraLoaderModelOnly":
			got["strength"] = m["inputs"].(map[string]any)["strength_model"]
		case "KSamplerSelect":
			got["sampler"] = m["inputs"].(map[string]any)["sampler_name"]
		case "BasicScheduler":
			got["steps"] = m["inputs"].(map[string]any)["steps"]
		case "MiniMaxH3SigmaShift":
			in := m["inputs"].(map[string]any)
			got["shift"] = fmt.Sprintf("%v/%v", in["shift_video"], in["shift_audio"])
		}
	}
	if got["strength"] != 1.0 || got["sampler"] != "euler" || got["steps"] != 4 || got["shift"] != "6/3" {
		t.Errorf("FL2V params = %+v, want 1.0/euler/4/shift6-3", got)
	}
	// R2V(角色镜)保护:Kijai FL2V 专用 LoRA 自动摘除 → 无 LoRA 节点 + 默认采样器 + 全步数
	wf2 := h3RenderWorkflow(base, 1, 768, 1344, 145, 4, "cache", true, false, 0, 1)
	got2 := map[string]any{}
	for _, n := range wf2 {
		m := n.(map[string]any)
		switch m["class_type"] {
		case "LoraLoaderModelOnly":
			got2["lora"] = m["inputs"].(map[string]any)["lora_name"]
		case "KSamplerSelect":
			got2["sampler"] = m["inputs"].(map[string]any)["sampler_name"]
		case "BasicScheduler":
			got2["steps"] = m["inputs"].(map[string]any)["steps"]
		}
	}
	if got2["lora"] != nil || got2["sampler"] != "res_multistep" || got2["steps"] != 20 {
		t.Errorf("R2V 保护 = %+v, want 无LoRA/res_multistep/20步", got2)
	}
	// R2V 配了专用 LoRA(turbo_lora_r2v)则正常挂载
	r2v := map[string]any{}
	for k, v := range base {
		r2v[k] = v
	}
	r2v["turbo_lora_r2v"] = "minimax_h3_turbo_4step_ema.safetensors"
	wf3 := h3RenderWorkflow(r2v, 1, 768, 1344, 145, 4, "cache", true, false, 0, 1)
	for _, n := range wf3 {
		if m, ok := n.(map[string]any); ok && m["class_type"] == "LoraLoaderModelOnly" {
			in := m["inputs"].(map[string]any)
			if in["lora_name"] != "minimax_h3_turbo_4step_ema.safetensors" || in["strength_model"] != 0.8 {
				t.Errorf("R2V 专用 LoRA = %+v", in)
			}
		}
	}
	// R2V 专用 LoRA(ref2v)挂 R2V 镜:1.0/euler + SigmaShift(12,3);FL2V 空镜自动摘除
	r2vNew := map[string]any{}
	for k, v := range base {
		r2vNew[k] = v
	}
	r2vNew["turbo_lora_r2v"] = "minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors"
	wf4 := h3RenderWorkflow(r2vNew, 1, 768, 1344, 145, 4, "cache", true, false, 0, 1)
	shift := ""
	for _, n := range wf4 {
		if m, ok := n.(map[string]any); ok && m["class_type"] == "MiniMaxH3SigmaShift" {
			in := m["inputs"].(map[string]any)
			shift = fmt.Sprintf("%v/%v", in["shift_video"], in["shift_audio"])
		}
	}
	if shift != "12/3" {
		t.Errorf("ref2v R2V shift = %q, want 12/3", shift)
	}
	r2vOnlyMain := map[string]any{}
	for k, v := range base {
		r2vOnlyMain[k] = v
	}
	r2vOnlyMain["turbo_lora"] = "minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors" // 主 LoRA 误配 ref2v
	wf5 := h3RenderWorkflow(r2vOnlyMain, 1, 768, 1344, 145, 4, "cache", false, false, 0, 1) // 空镜
	hasLora := false
	for _, n := range wf5 {
		if m, ok := n.(map[string]any); ok && m["class_type"] == "LoraLoaderModelOnly" {
			hasLora = true
		}
	}
	if hasLora {
		t.Errorf("ref2v 专用 LoRA 不应挂 FL2V 空镜")
	}
}
