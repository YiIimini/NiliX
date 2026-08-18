package api

import "testing"

func TestTurboLoRASpec(t *testing.T) {
	cases := []struct {
		name            string
		strength        float64
		sampler, sched  string
		steps           int
	}{
		{"minimax_h3_fl2v_lightx2v_turbo_4step_v0.1_comfy_resized_avg_rank_21_bf16.safetensors", 0.75, "sa_solver", "simple", 4},
		{"MINIMAX_H3_KIJAI_LIGHTX2V.safetensors", 0.75, "sa_solver", "simple", 4},
		{"minimax_h3_turbo_4step_ema.safetensors", 0.8, "res_multistep", "simple", 8},
		{"", 0.8, "res_multistep", "simple", 8},
	}
	for _, c := range cases {
		s := turboLoRASpecOf(c.name)
		if s.Strength != c.strength || s.Sampler != c.sampler || s.Scheduler != c.sched || s.Steps != c.steps {
			t.Errorf("spec(%q) = %+v, want {%.2f %s %s %d}", c.name, s, c.strength, c.sampler, c.sched, c.steps)
		}
	}
	// 工作流参数透传:空镜(FL2V)挂 Kijai LoRA → 0.75 / sa_solver
	base := map[string]any{
		"turbo_lora": "minimax_h3_fl2v_lightx2v_turbo_4step_v0.1_comfy_resized_avg_rank_21_bf16.safetensors",
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
		}
	}
	if got["strength"] != 0.75 || got["sampler"] != "sa_solver" || got["steps"] != 4 {
		t.Errorf("FL2V params = %+v, want 0.75/sa_solver/4", got)
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
}
