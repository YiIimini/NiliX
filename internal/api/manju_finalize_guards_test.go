package api

import (
	"strings"
	"testing"
)

// manjuFinalizePromptPure 人物/台词纪律覆盖面(2026-08-27 双反馈修复):
// ① 群像无卡镜(subject_definitions 有群演、characters 空)必须补上 frameGuard+
//    NoRefGuard(周管事被复制进 04/05 镜的根因);② 所有镜 AUDIO DISCIPLINE(03 镜
// "asks one flat question" 间接引语被 H3 念出来的根因);③ 空镜不误伤;④ 幂等。
func TestFinalizePromptGuards(t *testing.T) {
	subjectShot := "subject_definitions:\n<Subject 1> is the short fat dungeon warden in <Picture 1>.\n<Subject 2> is the white-haired steward in <Picture 1>.\n\nsummary:\n[reference generation] test.\n\ndetailed_description:\ntwo jailers take the old steward by the arms."
	charShot := "subject_definitions:\n<Subject 1> is a young woman in <Picture 1>.\n\ndetailed_description:\nShe says: <d>[中文]府里，还好么。</d>"
	sceneryShot := "detailed_description:\nA wide shot of the empty dungeon corridor, dripping water."

	// ① 群像无卡镜:characters 空,但有主体 → frameGuard + NoRef + Audio 全到位
	// (0 槽=纯无图;1 槽=只挂场景图 → scene-only 变体,人物禁抄场景图脸)
	out := manjuFinalizePromptPure(subjectShot, false, 0)
	if !strings.Contains(out, "never show the same character twice") {
		t.Error("群像无卡镜缺 frameGuard(同一人物不得出现两次)")
	}
	if !strings.Contains(out, "no reference picture is attached") {
		t.Error("群像无卡镜缺无参考图纪律(禁止形象互抄)")
	}
	if !strings.Contains(out, "AUDIO DISCIPLINE") {
		t.Error("群像无卡镜缺台词纪律")
	}
	outSceneOnly := manjuFinalizePromptPure(subjectShot, false, 1)
	if !strings.Contains(outSceneOnly, "scene/environment reference") {
		t.Error("只挂场景图的群像镜缺 scene-only 纪律(人物禁抄场景图)")
	}

	// ② 角色镜:frameGuard 在,但不需要 NoRef(有参考图)
	out = manjuFinalizePromptPure(charShot, true, 3)
	if !strings.Contains(out, "never show the same character twice") {
		t.Error("角色镜缺 frameGuard")
	}
	if strings.Contains(out, "no reference picture is attached") {
		t.Error("角色镜不应有无参考图纪律")
	}
	if !strings.Contains(out, "AUDIO DISCIPLINE") {
		t.Error("角色镜缺台词纪律")
	}

	// ③ 空镜:无人物纪律,但台词纪律仍加(旁白也在 <d>,不误伤)
	out = manjuFinalizePromptPure(sceneryShot, false, 3)
	if strings.Contains(out, "never show the same character twice") {
		t.Error("空镜不应有人物纪律")
	}
	if strings.Contains(out, "no reference picture is attached") {
		t.Error("空镜不应有无参考图纪律")
	}
	if !strings.Contains(out, "AUDIO DISCIPLINE") || !strings.Contains(out, "MOTION DISCIPLINE") {
		t.Error("空镜缺台词/运动纪律")
	}

	// ④ 幂等:重复 finalize 不重复追加
	out2 := manjuFinalizePromptPure(out, false, 3)
	if strings.Count(out2, "AUDIO DISCIPLINE") != 1 || strings.Count(out2, "MOTION DISCIPLINE") != 1 {
		t.Error("纪律重复追加")
	}
	if out2 != out {
		t.Error("幂等失败:二次 finalize 输出不一致")
	}
}
