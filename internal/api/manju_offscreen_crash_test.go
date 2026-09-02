package api

import (
	"strings"
	"testing"
)

// TestFixOffscreenNoCrash 崩溃回归(2026-09-02 王牌三岁半实锤 slice bounds [1326:573]):
// h3_prompt 在 detailed_description 之前就出现 <d>(subject_definitions/summary 段
// 残留),旧代码 segStart clamp 后 > start 越界 panic。修复:跳过画面段外的 <d>。
func TestFixOffscreenNoCrash(t *testing.T) {
	// 复现形态:subject_definitions 段有 <d>,detailed_description 在其后(画外台词)
	hp := "subject_definitions:\n<Subject 1> is a man in <Picture 1>; <Audio 1> ... containing a spoken voiceover.\n<d>[Chinese]某句残留</d>\n\nsummary:\nX\n\ndetailed_description:\nThe spectator shouts: <d>[Chinese]榜七就这水平?</d> The end."
	dialogue := "(S3)画外·路人:\"榜七就这水平?\""
	// 不应 panic
	out := fixOffscreenDialogueSays(hp, dialogue)
	// 画面段的画外台词仍应标注
	if !strings.Contains(out, "off-screen voiceover") {
		t.Fatalf("画面段画外台词应标注, got: %s", out)
	}
	// subject_definitions 段残留 <d> 不应被改写(不 panic 即通过)
	if strings.Count(out, "off-screen voiceover") != 1 {
		t.Fatalf("只应标注画面段 1 处, got: %s", out)
	}
	// 无 画外 对话列 → 原样返回
	if out2 := fixOffscreenDialogueSays(hp, "程野:你做什么?"); out2 != hp {
		t.Fatalf("无画外说话人不应改写")
	}
	// 极短 prompt(无 detailed_description)→ 原样
	if out3 := fixOffscreenDialogueSays("<d>你好</d>", "(S3)画外·路人:\"你好\""); out3 != "<d>你好</d>" {
		t.Fatalf("无 detailed_description 不应处理")
	}
}

// TestFixOffscreenMultipleD 多个 <d> 连续(offset 累加后边界仍正确)
func TestFixOffscreenMultipleD(t *testing.T) {
	hp := "detailed_description:\nThe man shouts: <d>[Chinese]第一句</d> Then the crowd yells: <d>[Chinese]第二句</d> End."
	dialogue := "(S3)画外·路人:\"第一句\"\n(S3)画外·路人:\"第二句\""
	out := fixOffscreenDialogueSays(hp, dialogue)
	if n := strings.Count(out, "off-screen voiceover"); n != 2 {
		t.Fatalf("两句画外台词都应标注, got %d: %s", n, out)
	}
	if n := strings.Count(out, "lips remain completely closed"); n != 2 {
		t.Fatalf("两句都应补 lips-closed, got %d: %s", n, out)
	}
}
