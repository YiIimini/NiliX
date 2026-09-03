package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-03 幽灵人声检测:资格判定(无台词镜)+ QC 报告追加

func TestGhostVoiceEligible(t *testing.T) {
	const discipline = "\nAUDIO & LIP DISCIPLINE: every spoken line comes ONLY from <d> tags - never invent dialogue.\n"
	cases := []struct {
		name, dlg, nar, h3 string
		want               bool
	}{
		{"纯空镜", "无", "", "subject_definitions:\n<Subject 1> is the office." + discipline, true},
		{"有台词", "(S2)甲:\"你好\"", "", "…", false},
		{"台词空串", "", "", "…", true},
		{"有旁白", "无", "夜色渐深。", "…", false},
		{"h3 有真实台词标签", "无", "", "detailed_description:\nHe says: <d>[Chinese]你好</d> while seated." + discipline, false},
		{"仅纪律段引用 d 标签", "无", "", "detailed_description:\nA man sleeps." + discipline, true},
	}
	for _, c := range cases {
		if got := ghostVoiceEligible(c.dlg, c.nar, c.h3); got != c.want {
			t.Errorf("%s: eligible=%v want %v", c.name, got, c.want)
		}
	}
}

func TestAppendQCFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "qc.json")
	_ = os.WriteFile(p, []byte(`{"shots":{"01.mp4":{"ok":true,"flags":["音频:正常"]}}}`), 0o644)
	appendQCFlag(p, "01.mp4", "幽灵人声:检出疑似人声")
	b, _ := os.ReadFile(p)
	got := string(b)
	if !strings.Contains(got, `"ok": false`) || !strings.Contains(got, "幽灵人声:检出疑似人声") || !strings.Contains(got, "音频:正常") {
		t.Fatalf("flag 追加不符(应 ok=false+保留旧 flags):\n%s", got)
	}
	// 无该镜记录时新建
	appendQCFlag(p, "09.mp4", "幽灵人声:x")
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "09.mp4") {
		t.Fatal("无记录镜头应新建条目")
	}
}


// 2026-09-03 幽灵人声根治:无台词镜 finalize 走静音契约,有台词镜走原台词纪律
func TestSilentShotGuardRouting(t *testing.T) {
	// 无台词镜(修仙界镜1 形态:h3 无 <d>):注入静音契约,禁止残留旧台词纪律措辞
	silent := "subject_definitions:\n<Subject 1> is the late-night office.\ndetailed_description:\nThe camera pushes in on a slumped man.\noverall_soundscape:\nA computer-fan hum."
	out := manjuFinalizePromptPure(silent, false, 1)
	if !strings.Contains(out, "silent-acting shot") {
		t.Fatalf("无台词镜应注入静音契约: %s", out[len(out)-260:])
	}
	if strings.Contains(out, "comes ONLY from the text inside <d> tags") {
		t.Fatalf("无台词镜不得注入台词纪律(旧否定式=没说): %s", out[len(out)-260:])
	}
	// 有台词镜:走原台词纪律,不得带静音契约
	spoken := "detailed_description:\nHe looks up and says <d>[Chinese]谁在那?</d> into the dark."
	out2 := manjuFinalizePromptPure(spoken, true, 2)
	if !strings.Contains(out2, "comes ONLY from the text inside <d> tags") {
		t.Fatalf("有台词镜应保留台词纪律: %s", out2[len(out2)-260:])
	}
	if strings.Contains(out2, "silent-acting shot") {
		t.Fatalf("有台词镜不得注入静音契约: %s", out2[len(out2)-260:])
	}
	// 幂等:静音契约镜二次 finalize 结果不变(先删后插自愈)
	if again := manjuFinalizePromptPure(out, false, 1); again != out {
		t.Fatal("静音契约注入不幂等")
	}
}
