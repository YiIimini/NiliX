package manju

import (
	"nilix/internal/paths"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 回归(2026-08-26):配置引用的 turbo LoRA 文件不在磁盘时,newManjuCtx 自动回退
// 磁盘实际存在的同族文件并写回 config——「minimax_h3_turbo_4step_ema.safetensors(未找到)」
// 类报错的存量项目自愈;文件已存在时绝不多动(不覆盖用户自定义)。
func TestNormalizeTurboLoraFallback(t *testing.T) {
	oldShared := paths.ComfySharedDir
	defer func() { paths.ComfySharedDir = oldShared }()
	shared := t.TempDir()
	loras := filepath.Join(shared, "models", "loras")
	if err := os.MkdirAll(loras, 0755); err != nil {
		t.Fatal(err)
	}
	// 磁盘部署现状:只有新版双 LoRA(fl2v v1.1 空镜 / ref2v v0.1 角色镜),无旧 4step EMA
	for _, n := range []string{
		"minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors",
		"minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors",
	} {
		if err := os.WriteFile(filepath.Join(loras, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	paths.ComfySharedDir = shared

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"style": "real",
		"render": map[string]any{
			"turbo_lora":     "minimax_h3_turbo_4step_ema.safetensors", // 旧默认名,部署升级后已停发
			"turbo_lora_r2v": "no-such-r2v.safetensors",
			"comfy_url":      "http://127.0.0.1:8190",
		},
		"paths": map[string]any{
			"workdir": dir, "novel": filepath.Join(dir, "novel.md"),
			"comfy_input": filepath.Join(dir, "in"), "comfy_output": filepath.Join(dir, "out"),
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)

	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantFl2v := "minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors"
	wantR2V := "minimax_h3_ref2v_turbo_4step_v0.1_comfyui_bf16.safetensors"
	if got := str(ctx.R["turbo_lora"]); got != wantFl2v {
		t.Errorf("turbo_lora 应回退 %s, got %q", wantFl2v, got)
	}
	if got := str(ctx.R["turbo_lora_r2v"]); got != wantR2V {
		t.Errorf("turbo_lora_r2v 应回退 %s, got %q", wantR2V, got)
	}
	if ctx.loraRepair == "" {
		t.Error("loraRepair 应有修复提示")
	}
	// 落盘:重读 config 已是新名,体检不再报缺失
	cfg2, err := readManjuConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	R2, _ := cfg2["render"].(map[string]any)
	if got := str(R2["turbo_lora"]); got != wantFl2v {
		t.Errorf("config 应落盘新名, got %q", got)
	}
	// 文件已存在 → 二次创建不再改动、不落盘
	ctx2, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx2.loraRepair != "" {
		t.Errorf("文件已存在不应再修复, got %q", ctx2.loraRepair)
	}
}

// 回归:loras 目录不存在(测试/未部署环境)时保持原值不动,绝不静默清配置
func TestNormalizeTurboLoraNoDirKeepsValue(t *testing.T) {
	oldShared := paths.ComfySharedDir
	defer func() { paths.ComfySharedDir = oldShared }()
	paths.ComfySharedDir = t.TempDir() // 无 models/loras

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"style": "real",
		"render": map[string]any{
			"turbo_lora": "turbo-lora.safetensors",
			"comfy_url":  "http://127.0.0.1:8190",
		},
		"paths": map[string]any{
			"workdir": dir, "novel": filepath.Join(dir, "novel.md"),
			"comfy_input": filepath.Join(dir, "in"), "comfy_output": filepath.Join(dir, "out"),
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)

	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := str(ctx.R["turbo_lora"]); got != "turbo-lora.safetensors" {
		t.Errorf("目录缺失时不得改动, got %q", got)
	}
	if ctx.loraRepair != "" {
		t.Errorf("目录缺失时不得有修复提示, got %q", ctx.loraRepair)
	}
}
