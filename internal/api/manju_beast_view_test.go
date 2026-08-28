package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 兽类视图锚分流+剥人元素(2026-08-28 用户反馈:废根噬天灵宠小貔 full/Q 渲染出人形)
// 数据内联自真实角色卡(analysis/EP01_characters.json)关键段。
func TestBeastViewPromptBuild(t *testing.T) {
	xiaopi := map[string]any{
		"gender": "", "age": "", "species": "灵宠",
		"appearance": "记忆点:①巴掌大金毛绒球 ②额间铜钱纹 ③豆豆眼大圆嘴/小圆耳表情灯/秃尾蛋。",
		"image_prompt": "Front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, ancient Chinese xianxia cultivation world aesthetic, cute chibi Q-version mythical beast cub, palm-sized round golden fluffy ball, tiny beanie eyes and a big round mouth, sitting on a young cultivator's shoulder with warm furnace glow, ultra detailed, 8k, semi-realistic stylized illustration of an East Asian/Chinese character",
	}
	p := manjuViewPromptBuild(str(xiaopi["image_prompt"]), "full", xiaopi)
	low := strings.ToLower(p)
	// 人形锚/人衣语义必须不在
	for _, bad := range []string{"standing full figure", "trousers", "skirt", "never bare legs", "CHILD DRESS CODE", "identical costume colors"} {
		if strings.Contains(low, strings.ToLower(bad)) {
			t.Errorf("兽类 full 含人形语义 %q: %s", bad, p)
		}
	}
	// 人互动描述必须剥掉(正向人物描述=模型必然画人)
	for _, bad := range []string{"cultivator", "shoulder", "East Asian"} {
		if strings.Contains(low, strings.ToLower(bad)) {
			t.Errorf("兽类 full 残留人元素 %q: %s", bad, p)
		}
	}
	for _, want := range []string{"no humans", "purely animal creature form", manjuBeastIdentityAnchor} {
		if !strings.Contains(p, want) {
			t.Errorf("兽类 full 缺兽形锚 %q: %s", want, p)
		}
	}
	// Q 版兽类分支 img 段同样剥人(回归小貔 Q 出人)
	img := manjuQStrip(manjuStripFullBody(str(xiaopi["image_prompt"])))
	img = manjuBeastStrip(img)
	for _, bad := range []string{"cultivator", "shoulder"} {
		if strings.Contains(strings.ToLower(img), bad) {
			t.Errorf("兽类 Q img 段残留人元素 %q: %s", bad, img)
		}
	}
}

// 老者胡须跟随+老态锚(2026-08-28 用户反馈:80岁玄机老人 Q 版年轻化——无胡须词被 no beard 一刀切)
func TestBeardEnforceOldMale(t *testing.T) {
	elder := map[string]any{"gender": "男", "age": "80岁", "appearance": "枯瘦鹤发灰道袍,腕盘墨玉算珠"} // 无胡须词
	b := manjuBeardEnforce(elder)
	if strings.Contains(b, "no beard") {
		t.Errorf("无胡须词老者不应强制 no beard: %s", b)
	}
	if !strings.Contains(b, "wrinkled aged face") || !strings.Contains(b, "never a young face") {
		t.Errorf("老者缺老态锚: %s", b)
	}
	if !manjuIsOldMale(elder) {
		t.Errorf("80岁男应判老者")
	}
	bearded := map[string]any{"gender": "男", "age": "55岁", "appearance": "威严白须,青金宗袍"} // 有须词
	if b2 := manjuBeardEnforce(bearded); !strings.Contains(b2, "keeping the character's beard") {
		t.Errorf("白须老者应保留须: %s", b2)
	}
	young := map[string]any{"gender": "男", "age": "22岁"} // 年轻男仍 no beard
	if b3 := manjuBeardEnforce(young); !strings.Contains(b3, "no beard") {
		t.Errorf("年轻男应保持 no beard: %s", b3)
	}
	if manjuIsOldMale(young) {
		t.Errorf("22岁男不应判老者")
	}
	oldWord := map[string]any{"gender": "男", "age": "老年"}
	if !manjuIsOldMale(oldWord) {
		t.Errorf("「老年」应判老者")
	}
	female := map[string]any{"gender": "女", "age": "70岁"}
	if manjuIsOldMale(female) {
		t.Errorf("老年女不适用男老者锚")
	}
}

// Q 版男性性别锚强化(2026-08-28 用户反馈:顾清寒男 Q 版被画成女——阴柔美男+chibi 幼态先验)
func TestQPromptMaleGenderAnchor(t *testing.T) {
	gu := map[string]any{
		"gender": "男", "age": "22岁", "role": "反派",
		"appearance": "皮笑肉不笑,月白锦袍,白玉骨扇",
		"image_prompt": "a 22-year-old East Asian senior disciple, elegant but faintly false handsome face, immaculate moon-white brocade robes, a male character",
	}
	q := manjuQPrompt(gu)
	for _, want := range []string{"clearly masculine young man's face", "flat chest", "NOT a girl"} {
		if !strings.Contains(q, want) {
			t.Errorf("男性 Q 版缺性别强化锚 %q: %s", want, q)
		}
	}
	// 紧凑化(2026-08-28):富卡角色 Q 提示词(含画风层)必须压回 ~3000 内——
	// cfg=1.0 无负面通道,长度=遵循度,4300 时代=同逻辑出图抽奖
	full := (&manjuCtx{style: "real", R: map[string]any{}}).portraitPromptFor(q, gu, false)
	if len(full) > 3200 {
		t.Errorf("Q 版提示词超长(%d>3200),指令稀释风险: %s", len(full), full[:200])
	}
	female := map[string]any{"gender": "女", "age": "26岁", "image_prompt": "a woman in red dress"}
	qf := manjuQPrompt(female)
	if !strings.Contains(qf, "clearly feminine face") {
		t.Errorf("女性 Q 版缺 feminine 锚: %s", qf)
	}
}

// Q 版画风与项目风格档解耦(2026-08-28 用户反馈「Q版变成动漫形象」):
// style=real 时不得落入插画措辞;3D 手办锚在基底与 portraitPromptFor 双路存在
func TestQPromptToyStyleAnchor(t *testing.T) {
	ctx := &manjuCtx{style: "real", R: map[string]any{}}
	m := map[string]any{"gender": "女", "age": "少女", "appearance": "圆脸", "image_prompt": "a young woman in hanfu dress"}
	q := manjuQPrompt(m)
	if !strings.Contains(q, "3D rendered cute chibi collectible toy figure") {
		t.Errorf("Q 版基底缺 3D 手办前置锚: %s", q[:200])
	}
	full := ctx.portraitPromptFor(q, m, false)
	for _, want := range []string{"cinematic movie-grade CGI rendering", "physically based materials", "NOT a 2D flat anime illustration", "not cel-shaded", "physical 3D collectible toy figure not a drawing"} {
		if !strings.Contains(full, want) {
			t.Errorf("Q 版(style=real)缺画风锚 %q", want)
		}
	}
	if strings.Contains(full, "chibi illustration style") {
		t.Errorf("style=real 不得落入插画分支(动漫邀请词): %s", full)
	}
	// 3D 档同样生效(统一锚,不分档)
	ctx3d := &manjuCtx{style: "nextgen3d", R: map[string]any{}}
	if f3 := ctx3d.portraitPromptFor(q, m, false); !strings.Contains(f3, "cinematic movie-grade CGI rendering") {
		t.Errorf("3D 档 Q 版画风锚缺失")
	}
	// 兽类基底同样 3D 前置
	beast := map[string]any{"gender": "", "species": "灵宠", "image_prompt": "a small spirit beast"}
	qb := manjuQPrompt(beast)
	if !strings.Contains(qb, "3D rendered cute chibi collectible toy figure") {
		t.Errorf("兽类 Q 版基底缺 3D 前置锚: %s", qb[:200])
	}
}

// 视图/Q 代数:兽类锚与性别老态修正上线必须 bump(存量坏产物自愈重出)
func TestViewQGenBumped(t *testing.T) {
	if manjuViewGen < 9 {
		t.Errorf("兽形视图修复(兽形后缀+剥人形锚)上线后 views_gen 必须 ≥9,当前 %d", manjuViewGen)
	}
	if manjuQGen < 20 {
		t.Errorf("Q 版提示词污染审计(armor条件化/eyeCls精确化)上线后 q_gen 必须 ≥20,当前 %d", manjuQGen)
	}
}

// 两段式 Q 版(2026-08-28):形态段纯形态+特征(无身份词),身份段保形态+注入身份
func TestQTwoPassPrompts(t *testing.T) {
	gu := map[string]any{"gender": "男", "age": "22岁", "appearance": "皮笑肉不笑", "image_prompt": "a 22-year-old East Asian senior disciple in immaculate moon-white brocade robes, holding a white jade folding fan"}
	form := manjuQFormPrompt(gu)
	ident := manjuQIdentityPrompt(gu)
	for _, want := range []string{"2-head-tall", "3D rendered", "plain white background"} {
		if !strings.Contains(form, want) {
			t.Errorf("形态段缺 %q: %s", want, form)
		}
	}
	for _, bad := range []string{"OUTFIT LOCK", "HAIR LOCK", "same character as the reference"} {
		if strings.Contains(form, bad) {
			t.Errorf("形态段不应掺身份词 %q: %s", bad, form)
		}
	}
	for _, want := range []string{"keep exactly the same 3D chibi collectible toy figure", "OUTFIT LOCK", "same character as the reference portrait", "clearly masculine"} {
		if !strings.Contains(ident, want) {
			t.Errorf("身份段缺 %q: %s", want, ident)
		}
	}
	if len(form) > 900 {
		t.Errorf("形态段超长(%d>900): %s", len(form), form)
	}
	// 特征进形态段(眼镜类;2026-08-28 eyeCls 精确化:普通眼镜措辞含 glasses,不再混写 visor)
	glasses := map[string]any{"gender": "男", "image_prompt": "a cyberpunk CEO with round glasses"}
	if f2 := manjuQFormPrompt(glasses); !strings.Contains(f2, "glasses") || strings.Contains(f2, "visor") {
		t.Errorf("特征应进形态段构成且不混写 visor: %s", f2)
	}
	// 兽类两段
	beast := map[string]any{"species": "灵宠", "image_prompt": "a small golden spirit beast"}
	if fb := manjuQFormPrompt(beast); !strings.Contains(fb, "NOT a human") {
		t.Errorf("兽类形态段缺禁人形: %s", fb)
	}
	if ib := manjuQIdentityPrompt(beast); !strings.Contains(ib, "fur colors and markings") {
		t.Errorf("兽类身份段缺毛色锚: %s", ib)
	}
}

// charFullRef 全身照引用:存在即拷贝到 input 并返回引用名;不存在返回空
// (Q 版 init 依赖;2026-08-28 修复「Q版没按全身照生成」的取值函数)
func TestCharFullRef(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "nilix-test-fullref")
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	ctx := &manjuCtx{
		assetsDir:  dir,
		comfyInput: filepath.Join(dir, "input"),
	}
	_ = os.MkdirAll(ctx.comfyInput, 0755)
	if r := ctx.charFullRef("阿拾"); r != "" {
		t.Fatalf("无全身照应返回空, got %s", r)
	}
	full := filepath.Join(dir, "characters", "阿拾_full.png")
	_ = os.MkdirAll(filepath.Dir(full), 0755)
	_ = os.WriteFile(full, []byte("png"), 0644)
	r := ctx.charFullRef("阿拾")
	if r != "dir_char_full_阿拾.png" {
		t.Fatalf("全身照引用名错误: %s", r)
	}
	if !fileExists(filepath.Join(ctx.comfyInput, r)) {
		t.Fatalf("全身照未拷贝到 ComfyUI input")
	}
}
