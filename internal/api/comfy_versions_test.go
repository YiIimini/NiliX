package api

import (
	"testing"
)

// 版本管理数据组装验证(2026-08-29):直接调用快照函数,不依赖 HTTP 服务。
func TestComfyVersionSnapshot(t *testing.T) {
	// 局部指向真实目录,defer 恢复(避免污染其他测试的路径假设)
	oldRoot, oldShared := ComfyRootDir, ComfySharedDir
	ComfyRootDir = "D:/Ai/NiliX/comfyui/ComfyUI"
	ComfySharedDir = "D:/Ai/NiliX/comfyui/shared"
	defer func() { ComfyRootDir, ComfySharedDir = oldRoot, oldShared }()
	info := comfyVersionSnapshot()
	if info == nil {
		t.Fatal("快照为空")
	}
	models, _ := info["models"].(map[string][]any)
	if len(models) == 0 {
		t.Error("模型分组为空")
	}
	// loras 分组存在
	if _, ok := models["LoRA 加速/风格"]; !ok {
		t.Error("缺 LoRA 分组")
	}
	plugins, _ := info["plugins"].([]any)
	if len(plugins) == 0 {
		t.Error("插件列表为空")
	}
	// git 插件字段完整
	for _, p := range plugins {
		m, _ := p.(map[string]any)
		if m == nil {
			continue
		}
		if m["git"] == true {
			if _, ok := m["commit"]; !ok {
				t.Error("git 插件缺 commit:", m["name"])
			}
		}
	}
	if _, ok := info["workflows"]; !ok {
		t.Error("缺工作流清单")
	}
	if _, ok := info["pdd"]; !ok {
		t.Error("缺 PDD 状态")
	}
}
