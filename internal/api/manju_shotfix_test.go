package api

import (
	"strings"
	"testing"
)

// TestFixChibiEmptyRefs ①chibi 空引用清理(2026-09-02):
// 非内心戏镜删 chibi Subject 行(镜12 Q版乱入);内心戏镜保句清悬空引用。
func TestFixChibiEmptyRefs(t *testing.T) {
	// 非内心戏镜:chibi Subject 行删除
	hp := "subject_definitions:\n<Subject 1> is Shen Zhao in <Picture 1>; <Audio 1> ...\n<Subject 4> is the chibi miniature version of A Ying in ; <Audio 4> ...\n\nsummary:\nX\n\ndetailed_description:\nY"
	out := fixChibiEmptyRefs(hp, "程野:你做了什么?")
	if strings.Contains(out, "chibi") {
		t.Fatalf("非内心戏镜 chibi Subject 应删除, got: %s", out)
	}
	// 内心戏镜:保句清空引用
	hp2 := "subject_definitions:\n<Subject 1> is Shen Zhao in <Picture 1>; <Audio 1> ...\n<Subject 3> is a chibi/miniature version of A Ying, a small cute shadow figure, in ; <Audio 3> ...\n\nsummary:\nX"
	out2 := fixChibiEmptyRefs(hp2, "内心·阿影:他竟敢这样看我")
	if !strings.Contains(out2, "chibi") {
		t.Fatalf("内心戏镜 chibi Subject 应保留, got: %s", out2)
	}
	if strings.Contains(out2, "in ;") {
		t.Fatalf("内心戏镜 chibi 行空引用应清理, got: %s", out2)
	}
}

// TestFixOffscreenDialogueSays ②画外·台词强制 off-screen(2026-09-02,镜6 实锤):
// 对话列 画外·路人 台词在详细描述被写成 "The spectator shouts: <d>" → 改写
// off-screen voiceover + lips-closed。
func TestFixOffscreenDialogueSays(t *testing.T) {
	hp := "detailed_description:\nThe spectator shouts: <d>[Chinese]榜七就这水平?</d> The shot ends.\n\noverall_soundscape:\nX"
	dialogue := "(S3)画外·路人:\"榜七就这水平?\""
	out := fixOffscreenDialogueSays(hp, dialogue)
	if !strings.Contains(out, "off-screen voiceover") {
		t.Fatalf("画外台词应标 off-screen voiceover, got: %s", out)
	}
	if !strings.Contains(out, "lips remain completely closed") {
		t.Fatalf("画外台词应补 lips-closed, got: %s", out)
	}
	// 画面角色开口(<Subject N> says)不应被改写
	hp2 := "detailed_description:\n<Subject 1> (S1) says: <d>[Chinese]你好</d> The end."
	out2 := fixOffscreenDialogueSays(hp2, "(S3)画外·路人:\"某句\"")
	if strings.Contains(out2, "off-screen voiceover") {
		t.Fatalf("<Subject N> says 画面角色开口不应改写, got: %s", out2)
	}
}

// TestFixSubjectHairColor ③发色与角色卡校正(2026-09-02,镜7/19 实锤):
// subject 行 black hair + 卡 platinum-white → 校正为 platinum-white hair;
// 卡无发色词/行无发色词不动。
func TestFixSubjectHairColor(t *testing.T) {
	ctx := &manjuCtx{}
	ctx.charInfo = map[string]map[string]any{
		"沈照": {"image_prompt": "a 19-year-old male student, platinum-white short hair, bright golden eyes"},
		"阿影": {"image_prompt": "a human-shaped silhouette of living black mist, no facial features"},
	}
	s := manjuShot{Characters: []string{"沈照", "阿影"}}
	hp := "subject_definitions:\n<Subject 1> is Shen Zhao, a handsome young man with short black hair and sharp features, in <Picture 1>; <Audio 1> ...\n<Subject 2> is A Ying, a shadow creature, in <Picture 2>; <Audio 2> ...\n\nsummary:\nX"
	out := ctx.fixSubjectHairColor(hp, s)
	if !strings.Contains(out, "platinum-white hair") {
		t.Fatalf("沈照发色应校正为 platinum-white hair, got: %s", out)
	}
	if strings.Contains(out, "black hair") {
		t.Fatalf("沈照发色不应残留 black hair, got: %s", out)
	}
	// 幂等:二次调用不再变
	out2 := ctx.fixSubjectHairColor(out, s)
	if out2 != out {
		t.Fatalf("发色校正应幂等")
	}
}

// TestInjectCrowdDiscipline 远景人海纪律(2026-09-02 王牌三岁半镜1「三个外国人」;
// 二次修正按知识库《H3群演与Q版角色质量控制实战》:纪律判定看「人群词存在」而非
// characters 非空——群像无卡镜(chars 有主角但观众席无卡)最需要纪律最易漏)。
func TestInjectCrowdDiscipline(t *testing.T) {
	// 空镜+人群 → 注入
	hp := "detailed_description:\nAn extreme wide shot of the plaza, tens of thousands of spectators packed in tiered grandstands, the crowd's restless motion visible.\n\noverall_soundscape:\nX"
	out := injectCrowdDiscipline(hp, false)
	if !strings.Contains(out, "CROWD DISTANCE") {
		t.Fatalf("空镜人群应注入 CROWD DISTANCE: %s", out)
	}
	if !strings.Contains(out, "silhouettes") {
		t.Fatalf("纪律句应含正向剪影写法: %s", out)
	}
	// 幂等
	out2 := injectCrowdDiscipline(out, false)
	if out2 != out {
		t.Fatal("应幂等")
	}
	// 有登场角色但人群无卡 → 仍注入(知识库:「群像无卡镜最需要纪律却最容易被漏」;
	// 镜15 chars=[棠棠] 但观众席无卡,H3 照样自由发挥个体 → hasChars 不再是跳过条件)
	if out3 := injectCrowdDiscipline(hp, true); !strings.Contains(out3, "CROWD DISTANCE") {
		t.Fatalf("有登场角色+人群描述仍应注入(群像无卡镜最需纪律): %s", out3)
	}
	// 无人群描述 → 不注入
	if out4 := injectCrowdDiscipline("detailed_description:\nAn empty room.", false); strings.Contains(out4, "CROWD DISTANCE") {
		t.Fatal("无人群不应注入")
	}
	// 2026-09-02 位置升级:纪律句必须插在 detailed_description 段标题之前
	// (队尾纪律 H3 注意力弱,镜1 实测注入仍出「三个外国人」;OFF-SCREEN LINES
	// TASK 同位置服从度最高),且不得破坏画面段标题定位。
	pre := out[:strings.Index(out, "detailed_description:")]
	if !strings.Contains(pre, "CROWD DISTANCE") {
		t.Fatalf("纪律句应位于 detailed_description 段之前: %s", out[:200])
	}
	if idx := strings.Index(out, "CROWD DISTANCE"); idx > strings.Index(out, "detailed_description:") {
		t.Fatalf("纪律句不得位于 detailed_description 之后: %s", out[:300])
	}
	// 存量 plan 队尾旧句自愈迁移:旧版纪律追加在队尾,重跑须先删后插到段前
	legacy := hp + "\nCROWD DISTANCE: any crowd remains a distant sea of silhouettes.\nnon_diegetic_music:\nX"
	fixed := injectCrowdDiscipline(legacy, false)
	if di, ci := strings.Index(fixed, "detailed_description:"), strings.Index(fixed, "CROWD DISTANCE"); ci < 0 || ci > di {
		t.Fatalf("队尾旧句应自愈迁移到段前")
	}
	// 脚本侧已内嵌纪律句 → 不重复注入(2026-09-02 技能侧契约:正向剪影写法,
	// 知识库标尺「否定句对高重绘模型基本无效」→ 契约用 anonymous backs/silhouettes)
	embedded := "detailed_description:\nThe grandstands stay out of focus as rows of anonymous backs and blurred silhouettes seen from behind, tiny and featureless.\n\noverall_soundscape:\nX"
	if outE := injectCrowdDiscipline(embedded, false); strings.Contains(outE, "CROWD DISTANCE") {
		t.Fatalf("脚本侧已内嵌纪律句时不应重复注入: %s", outE)
	}
}

// TestInjectScreenDiscipline 屏内容兜底纪律(2026-09-02 全库统一,王牌三岁半
// 镜1「屏上三个外国人」实锤):显示型屏无内容说明 → 注入 SCREEN CONTENT;
// 已钉内容/画面框用法/家具屏 → 不注入。
func TestInjectScreenDiscipline(t *testing.T) {
	// ① 未钉内容全息屏 → 注入
	hp := "summary:\ntest\n\ndetailed_description:\n[Shot 1] An extreme wide shot frames the plaza, three massive floating holographic screens glowing white-blue over the stands, banners overhead.\n\noverall_soundscape:\nX"
	out := injectScreenDiscipline(hp)
	if !strings.Contains(out, "SCREEN CONTENT") {
		t.Fatalf("未钉内容全息屏应注入: %s", out)
	}
	if di, si := strings.Index(out, "detailed_description:"), strings.Index(out, "SCREEN CONTENT"); si < 0 || si > di {
		t.Fatalf("纪律句应位于 detailed_description 段前")
	}
	// 幂等
	if out2 := injectScreenDiscipline(out); out2 != out {
		t.Fatal("应幂等")
	}
	// ② 屏内容已写明(the words) → 不注入
	hp2 := "detailed_description:\nThe tester's screen blinks steadily with the words \"精神同步率检测\".\n\noverall_soundscape:\nX"
	if o := injectScreenDiscipline(hp2); strings.Contains(o, "SCREEN CONTENT") {
		t.Fatalf("已钉内容(the words)不应注入")
	}
	// ③ 内容写在邻句(跨句钉死合法) → 不注入
	hp3 := "detailed_description:\nThe camera holds a static close-up on the smartphone screen, which continues seamlessly from the previous shot. The screen now displays an e-commerce app with a product page and a price.\n\noverall_soundscape:\nX"
	if o := injectScreenDiscipline(hp3); strings.Contains(o, "SCREEN CONTENT") {
		t.Fatalf("邻句钉死不应注入")
	}
	// ④ screen=画面框(fills the screen) → 不注入
	hp4 := "detailed_description:\nNow the heroine is in the center of the frame, in extreme close-up, her face filling the screen, facing the camera.\n\noverall_soundscape:\nX"
	if o := injectScreenDiscipline(hp4); strings.Contains(o, "SCREEN CONTENT") {
		t.Fatalf("画面框用法不应注入")
	}
	// ⑤ 家具屏/展柜 → 不注入
	hp5 := "detailed_description:\nHe sits behind the wooden screen, warm lantern light on his thin face, the display case isolating the sound.\n\noverall_soundscape:\nX"
	if o := injectScreenDiscipline(hp5); strings.Contains(o, "SCREEN CONTENT") {
		t.Fatalf("家具屏/展柜不应注入")
	}
	// ⑥ 裸 "the screen" 无设备限定词 → 不注入(防误伤)
	hp6 := "detailed_description:\nHe looks at the screen and nods slowly.\n\noverall_soundscape:\nX"
	if o := injectScreenDiscipline(hp6); strings.Contains(o, "SCREEN CONTENT") {
		t.Fatalf("裸 screen 无限定词不应注入")
	}
}
