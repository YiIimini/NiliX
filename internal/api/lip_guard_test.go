package api

import (
	"strings"
	"testing"
)

func TestLipGuardInjection(t *testing.T) {
	scenery := "detailed_description:\nAn over-the-shoulder shot of A-Kai on the field path."
	out := manjuFinalizePromptPure(scenery, true, 3)
	if !strings.Contains(out, "LIP DISCIPLINE") {
		t.Error("缺 LIP DISCIPLINE")
	}
	// 2026-09-03 无台词镜走静音契约("lips completely closed"),有台词镜走原版
	// ("lips remain completely closed")——两版都承载唇闭合约束
	if !strings.Contains(out, "lips remain completely closed") && !strings.Contains(out, "lips completely closed") {
		t.Error("LIP 纪律缺闭合约束")
	}
	// EXECUTION 不再诱导画面角色动嘴(删除了 mouth movement 措辞)
	if strings.Contains(out, "lip movements must be performed") || strings.Contains(out, "mouth movement") {
		t.Error("EXECUTION 仍含诱导动嘴措辞")
	}
	out2 := manjuFinalizePromptPure(out, true, 3)
	if out2 != out {
		t.Error("LIP 注入不幂等")
	}
}
