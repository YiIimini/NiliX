package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 回归:ComfyUI 输出目录一致性——newManjuCtx 必须优先用生效启动参数 comfyParams.out,
// 否则 config 里的旧 comfy_output(硬编码 Desktop 共享目录)与实际输出目录不一致,
// 任务"完成"但从错误目录读产物报「open ... output\xxx.png: not found」。
func TestNewManjuCtxComfyOutputPriority(t *testing.T) {
	oldOut, oldIn := comfyParams.out, comfyParams.in
	comfyParams.out, comfyParams.in = "", ""
	defer func() { comfyParams.out, comfyParams.in = oldOut, oldIn }()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"style":  "2.5d",
		"render": map[string]any{"comfy_url": "http://127.0.0.1:8190"},
		"paths": map[string]any{
			"workdir": dir, "novel": filepath.Join(dir, "novel.md"),
			"comfy_input": filepath.Join(dir, "old-in"), "comfy_output": filepath.Join(dir, "old-out"),
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)
	// comfyParams 未注入 → 回退 config 值
	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.comfyOutput != filepath.Join(dir, "old-out") {
		t.Fatalf("空 comfyParams 时应回退 config: %q", ctx.comfyOutput)
	}
	// comfyParams 已注入(实际运行) → 优先于 config 旧值
	comfyParams.out = filepath.Join(dir, "live-out")
	comfyParams.in = filepath.Join(dir, "live-in")
	ctx2, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx2.comfyOutput != filepath.Join(dir, "live-out") {
		t.Fatalf("comfyParams.out 应优先: %q", ctx2.comfyOutput)
	}
	if ctx2.comfyInput != filepath.Join(dir, "live-in") {
		t.Fatalf("comfyParams.in 应优先: %q", ctx2.comfyInput)
	}
}

// 回归:新项目默认 comfy_input/comfy_output 跟随 ComfySharedDir(而非硬编码 Desktop 路径)
func TestManjuDefaultConfigComfyPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := manjuDefaultConfig("测试", filepath.Join(dir, "novel.md"), dir, "key")
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		t.Fatal("paths 缺失")
	}
	ci, _ := P["comfy_input"].(string)
	co, _ := P["comfy_output"].(string)
	if co == "" || ci == "" {
		t.Fatalf("comfy 路径为空: in=%q out=%q", ci, co)
	}
	// 不得残留旧的硬编码 Desktop 共享目录(与运行时 ComfyUI 输出目录脱节的根源)
	if !filepath.IsAbs(ci) || !filepath.IsAbs(co) {
		t.Fatalf("comfy 路径非绝对: in=%q out=%q", ci, co)
	}
}
