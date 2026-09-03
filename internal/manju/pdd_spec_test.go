package manju
import "testing"

func TestTurboLoRASpecPDD(t *testing.T) {
	s := turboLoRASpecOf("minimax_h3_fl2va_pdd_acc_8step_comfyui.safetensors")
	if !s.PDD {
		t.Error("pdd_acc 文件名应识别 PDD 模式")
	}
	if s.Steps != 8 || s.Sampler != "euler" || s.VideoShift != 12 || s.AudioShift != 3 {
		t.Error("PDD 配方错误:", s)
	}
	s2 := turboLoRASpecOf("minimax_h3_ref2va_pdd_acc_8step_comfyui.safetensors")
	if !s2.PDD {
		t.Error("ref2va pdd_acc 应识别 PDD")
	}
	// 既有 turbo 不受影响
	s3 := turboLoRASpecOf("minimax_h3_fl2v_turbo_4step_v1.1_768p_comfyui_bf16.safetensors")
	if s3.PDD || s3.Steps != 4 || s3.VideoShift != 6 {
		t.Error("4step turbo 被误判:", s3)
	}
}
