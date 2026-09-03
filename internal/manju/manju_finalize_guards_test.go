package manju

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
	if !strings.Contains(out, "AUDIO & LIP DISCIPLINE") {
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
	if !strings.Contains(out, "AUDIO & LIP DISCIPLINE") {
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
	if !strings.Contains(out, "AUDIO & LIP DISCIPLINE") || !strings.Contains(out, "MOTION & SEAM DISCIPLINE") {
		t.Error("空镜缺台词/运动纪律")
	}

	// ④ 幂等:重复 finalize 不重复追加
	out2 := manjuFinalizePromptPure(out, false, 3)
	if strings.Count(out2, "AUDIO & LIP DISCIPLINE") != 1 || strings.Count(out2, "MOTION & SEAM DISCIPLINE") != 1 {
		t.Error("纪律重复追加")
	}
	if out2 != out {
		t.Error("幂等失败:二次 finalize 输出不一致")
	}
}

// 2026-09-03 静态镜矛盾指令根治:「固定（Static）」括号直取的 "Static" 旧判定
// Contains(ph,"static") 漏过大写 → 静态镜被注入"运镜必须可见、不许定机"的运动
// 分支(镜8/13/18 实锤)。归一后应走 static 分支。
func TestCameraStaticPhraseNoContradiction(t *testing.T) {
	if ph := manjuCameraPhrase("固定（Static）"); ph != "static locked-off camera" {
		t.Fatalf("「固定（Static）」应归一为 static locked-off camera, got %q", ph)
	}
	in := "subject_definitions:\n<Subject 1> is Xiao Man in <Picture 1>.\n\ndetailed_description:\n[Shot 1] She waits."
	out := injectCameraDiscipline(in, "固定（Static）")
	if !strings.Contains(out, "stays locked") {
		t.Errorf("静态镜应走 stays locked 分支, got: %s", stringCut(out, "CAMERA DISCIPLINE", "\n"))
	}
	if strings.Contains(out, "never settle into a static locked-off frame") {
		t.Errorf("静态镜不得注入运动纪律(自相矛盾), got: %s", stringCut(out, "CAMERA DISCIPLINE", "\n"))
	}
	// 运动镜方向性短语
	if ph := manjuCameraPhrase("左摇"); ph != "smooth pan to the left" {
		t.Errorf("「左摇」应有方向性短语, got %q", ph)
	}
	if ph := manjuCameraPhrase("缓推（Push In, small, slow）"); ph != "Push In, small, slow" {
		t.Errorf("括号英文三要素仍应直取, got %q", ph)
	}
}

// 2026-09-03 写实电影级 CINEMATOGRAPHY 纪律:写实向注入、动漫/3D 向跳过、
// chibi 内心镜例外放行、幂等。
func TestInjectCinematographyDiscipline(t *testing.T) {
	base := "subject_definitions:\n<Subject 1> is Xiao Man in <Picture 1>.\n\ndetailed_description:\n[Shot 1] She waits."
	realistic := strings.Replace(base, "She waits.", "photorealistic cinematic characters; she waits.", 1)
	out := injectCinematographyDiscipline(realistic)
	if !strings.Contains(out, "CINEMATOGRAPHY:") || !strings.Contains(out, "shallow depth of field") {
		t.Fatalf("写实向应注入电影级纪律")
	}
	if again := injectCinematographyDiscipline(out); again != out {
		t.Errorf("injectCinematographyDiscipline 应幂等")
	}
	anime := strings.Replace(base, "She waits.", "subtly anime-stylized characters; she waits.", 1)
	if out2 := injectCinematographyDiscipline(anime); strings.Contains(out2, "CINEMATOGRAPHY:") {
		t.Errorf("动漫向不应注入写实纪律")
	}
	chibi := strings.Replace(base, "She waits.", "photorealistic real girl with a chibi miniature stylized clone on her shoulder.", 1)
	if out3 := injectCinematographyDiscipline(chibi); !strings.Contains(out3, "CINEMATOGRAPHY:") {
		t.Errorf("chibi 内心镜(写实主体+Q版小人)应例外放行注入")
	}
	plain := injectCinematographyDiscipline(base)
	if strings.Contains(plain, "CINEMATOGRAPHY:") {
		t.Errorf("无写实锚词的提示词不应注入")
	}
}

// 2026-09-03 内心独白 narrator 定义行改绑:源头 h3_prompt 把内心独白写成 narrator
// 画外音定义 → 命中 injectOffscreenVoiceBindings 幂等锚,角色内心话被旁白音色念出
// (镜17/18/20 实锤)。fixInnerVoiceDefLine 应把绑 narrator 的定义行改写为角色内心
// 音色形态(挂载侧 offscreenVoiceKeyFor 按 "the quiet inner voice of X" 同源解析)。
func TestFixInnerVoiceDefLine(t *testing.T) {
	ctx := &manjuCtx{}
	in := `subject_definitions:
<Subject 1> is Xiao Man in <Picture 1>, a village girl.
<Audio 1> is the voice-timbre reference for the off-screen voice described as the narrator with a calm, neutral storytelling voice, containing a spoken voiceover.

summary:
[reference generation] test`
	out := ctx.fixInnerVoiceDefLine(in, "小满", "female_warm", "内心·小满(愁)：一碗水两文。")
	if !strings.Contains(out, "<Audio 1> is the voice-timbre reference for the quiet inner voice of 小满") {
		t.Fatalf("narrator 定义行应改绑角色内心音色, got: %s", stringCut(out, "<Audio 1>", "\n"))
	}
	if strings.Contains(out, "described as the narrator") {
		t.Errorf("narrator 措辞应被替换(角色内心≠旁白)")
	}
	if again := ctx.fixInnerVoiceDefLine(out, "小满", "female_warm", "内心·小满(愁)：一碗水两文。"); again != out {
		t.Errorf("fixInnerVoiceDefLine 应幂等")
	}
	// 非内心戏镜(narrator=客观旁白)不改写
	plain := ctx.fixInnerVoiceDefLine(in, "", "", "普通的客观旁白文本。")
	if plain != in {
		t.Errorf("客观旁白镜的 narrator 定义应保持(叙述音色语义正确)")
	}
}

// 2026-09-03 源头返工配套:源头分镜已把内心独白写成 "the quiet inner voice of X
// says in an off-screen voiceover"(rework_inner_voice.py)——渲染端叙述/内心链
// 必须识别该句式(inner 绑定),不得掉进通用声线猜测。
func TestNarratorDescsRecognizeInnerVoiceOf(t *testing.T) {
	hp := "subject_definitions:\n<Subject 1> is Xiao Man in <Picture 1>.\n\ndetailed_description:\n[Shot 1] She grips the ladle. The quiet inner voice of 小满 says in an off-screen voiceover, in a low sorrowful inward voice: <d>[Chinese] 一碗水两文。</d> while the on-screen character's lips remain completely closed."
	ds := manjuNarratorDescs(hp)
	if len(ds) == 0 {
		t.Fatalf("inner voice of 句式应被叙述/内心链识别(返回非空)")
	}
	if os := manjuOffscreenDescs(hp); len(os) != 0 {
		t.Errorf("inner voice of 句式不得进通用画外声线猜测, got %v", os)
	}
}
