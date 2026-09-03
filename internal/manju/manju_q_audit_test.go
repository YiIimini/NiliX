package manju

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-08-28 Q 版提示词污染审计回归(用户「全面审计,别老出问题」):
// 模板硬编码词与角色卡冲突的两起实锤——林小满(现代实习生)Q版左肩凭空金属护甲
// (armor 材质词)、老纪(挂脖老花镜)Q版赛博护目镜(glasses/visor 混写)。
// 断言:无甲角色不带 armor 材质、眼部措辞精确到角色真实特征、挂脖镜不上脸。

// 现代无甲角色:Q 版提示词不得出现任何 armor 材质/护甲引导词
func TestQAuditNoArmorForModernChar(t *testing.T) {
	m := map[string]any{
		"gender":       "女",
		"appearance":   "记忆点：①圆脸杏眼 ②小太阳补丁 ③帆布包",
		"image_prompt": "a 23-year-old female Chinese intern, round face with gentle downturned almond eyes, black hair in a low ponytail, wearing an off-white hoodie and jeans",
	}
	p := manjuQPrompt(m)
	for _, bad := range []string{"armor", "armour"} {
		if strings.Contains(strings.ToLower(p), bad) {
			t.Errorf("无甲角色Q版不得出现 %q(凭空画护甲):\n%s", bad, p)
		}
	}
}

// 有甲角色(古风武将):armor 材质词保留(有甲才给金属质感)
func TestQAuditArmorKeptForArmoredChar(t *testing.T) {
	m := map[string]any{
		"gender":       "男",
		"image_prompt": "a 35-year-old male Chinese general in dark lacquered armor with bronze pauldrons, wearing a crimson war cloak",
	}
	if !manjuHasArmor(m) {
		t.Fatalf("含 armor/pauldrons 的角色应判定有甲")
	}
	p := manjuQPrompt(m)
	if !strings.Contains(p, "metal reflections on armor") {
		t.Errorf("有甲角色应保留盔甲材质词:\n%s", p)
	}
}

// 老纪案回归:挂脖老花镜——眼镜挂脖不上脸,绝不出现 visor(赛博护目镜先验)
func TestQAuditNeckGlassesPrecise(t *testing.T) {
	m := map[string]any{
		"gender":       "男",
		"age":          "58岁",
		"appearance":   "记忆点：①灰白寸头+挂脖老花镜 ②洗旧蓝色工装 ③温和鱼尾纹",
		"image_prompt": "a 58-year-old male Chinese veteran facilities engineer, grey buzz-cut hair, reading glasses hanging on a cord around his neck, wearing a faded blue workman jumpsuit uniform",
	}
	p := manjuQPrompt(m)
	if strings.Contains(p, "visor") {
		t.Errorf("挂脖老花镜角色不得出现 visor(赛博护目镜先验):\n%s", p)
	}
	if !strings.Contains(p, "hanging on a cord around the neck") {
		t.Errorf("挂脖镜应保持挂脖形态:\n%s", p)
	}
}

// 普通框架眼镜:精确措辞含 glasses,不混写 visor/goggles
func TestQAuditPlainGlassesPrecise(t *testing.T) {
	low := strings.ToLower("round wire-frame glasses, kind gentle eyes")
	cls := manjuEyeClsFor(low)
	if !strings.Contains(cls, "exactly the same glasses") {
		t.Errorf("普通眼镜应精确措辞 glasses: %s", cls)
	}
	if strings.Contains(cls, "visor") || strings.Contains(cls, "goggles") {
		t.Errorf("普通眼镜不得混写 visor/goggles: %s", cls)
	}
}

// manjuEyeClsFor 全类型:visor/goggles/eyepatch/monocle/scanner 各自精确;无特征默认大亮眼
func TestQAuditEyeClsVariants(t *testing.T) {
	cases := []struct {
		low  string
		want string
	}{
		{"a tactical visor covering the eyes", "same visor"},
		{"welding goggles on the forehead", "same goggles"},
		{"a black leather eyepatch over the left eye", "eyepatch"},
		{"a brass monocle", "same monocle"},
		{"a glowing red eye scanner lens", "eye scanner"},
		{"nothing special about the eyes", "big sparkling glossy eyes"},
	}
	for _, c := range cases {
		if got := manjuEyeClsFor(c.low); !strings.Contains(got, c.want) {
			t.Errorf("eyeCls(%q) 应含 %q, got: %s", c.low, c.want, got)
		}
	}
}

// 特征锁全列举改中性:无镜无疤角色不得被点名 glasses/visors/scars
func TestQAuditFeatureAnchorNeutral(t *testing.T) {
	m := map[string]any{
		"gender":       "男",
		"image_prompt": "a tattooed swordsman with a hooked claw for arms",
	}
	a := manjuFeatureAnchor(m)
	for _, bad := range []string{"glasses", "visors", "scars"} {
		if strings.Contains(a, bad) {
			t.Errorf("特征锁不得全列举卡面没有的特征 %q:\n%s", bad, a)
		}
	}
}

// portraitPromptFor chibi 分支同规:无甲角色的Q版锚不含 armor 材质词
func TestQAuditPortraitPromptForChibiNoArmor(t *testing.T) {
	ctx := &manjuCtx{}
	m := map[string]any{
		"gender": "女",
		"image_prompt": "Front-facing portrait, a 23-year-old female Chinese intern wearing an off-white hoodie and jeans, chibi cute style",
	}
	p := ctx.portraitPromptFor(str(m["image_prompt"]), m, false)
	if !strings.Contains(p, "chibi") {
		t.Fatalf("应进 chibi 分支:\n%s", p)
	}
	if strings.Contains(p, "armor") {
		t.Errorf("无甲角色 chibi 锚不得含 armor 材质词:\n%s", p)
	}
	// 有甲角色保留
	m2 := map[string]any{
		"gender": "男",
		"image_prompt": "chibi cute style, a general in dark lacquered armor with bronze pauldrons",
	}
	p2 := ctx.portraitPromptFor(str(m2["image_prompt"]), m2, false)
	if !strings.Contains(p2, "armor and fabric") {
		t.Errorf("有甲角色 chibi 锚应保留 armor 材质词:\n%s", p2)
	}
}

// 兽形视图回归(2026-08-28 猫·二两 full 全身照画出人):视图提示词禁人形后缀
// (standing pose/complete outfit/hairstyle)、剥卡面 3D 人形锚(virtual digital
// human/BJD doll/porcelain skin),兽形后缀显式禁衣服禁人
func TestQAuditBeastViewPrompt(t *testing.T) {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	_ = os.MkdirAll(analysis, 0755)
	beastCard := `{"characters":[{"id":"猫","species":"灵宠","image_prompt":` +
		`"an orange and white chubby stray cat, bright amber eyes, sitting upright, next-generation 3D CGI render of a virtual digital human character, BJD doll aesthetic, porcelain-smooth skin with fine subsurface scattering"}]}`
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), []byte(beastCard), 0644)
	ctx := &manjuCtx{analysisDir: analysis, episode: "EP01", assetsDir: filepath.Join(dir, "assets")}

	for _, view := range []string{"full", "side", "detail"} {
		got := charViewPromptFor(ctx, "猫", view)
		low := strings.ToLower(got)
		for _, bad := range []string{"standing pose", "complete outfit", "hairstyle", "virtual digital human", "bjd doll", "porcelain-smooth skin"} {
			if strings.Contains(low, bad) {
				t.Errorf("兽形 %s 视图不得含人形词 %q:\n%s", view, bad, got)
			}
		}
		if !strings.Contains(low, "no human") {
			t.Errorf("兽形 %s 视图应显式禁人:\n%s", view, got)
		}
	}
	if got := charViewPromptFor(ctx, "猫", "full"); !strings.Contains(got, "quadruped animal pose") {
		t.Errorf("兽形 full 应为四脚动物姿态:\n%s", got)
	}

	// 人形视图不受影响(人形后缀保留)
	humanCard := `{"characters":[{"id":"甲","gender":"男","image_prompt":"front base, white beard"}]}`
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), []byte(humanCard), 0644)
	if got := charViewPromptFor(ctx, "甲", "full"); !strings.Contains(got, "standing pose, complete outfit visible") {
		t.Errorf("人形 full 后缀应保留:\n%s", got)
	}
}

// 兽形判定回归(2026-08-28 猫·二两案):species 缺失的纯英文动物卡必须判兽形
// (此前 manjuBeastBody 全中文古风词,英文猫卡漏判人形→full 出人/Q版人形手办);
// 人形否决优先,狐耳娘/动物装人类不误判。
func TestQAuditBeastDetectEnglishAnimal(t *testing.T) {
	cat := map[string]any{
		"gender":       "",
		"appearance":   "记忆点：①橘白流浪猫 ②琥珀色眼睛",
		"image_prompt": "an orange and white chubby stray cat, bright amber eyes, slightly dusty fur, one ear with a small notch, sitting upright",
	}
	if !manjuIsBeast(cat) {
		t.Fatal("纯英文猫卡应判兽形(动物本体词兜底)")
	}
	dogCn := map[string]any{
		"image_prompt": "a loyal golden puppy, fluffy fur",
	}
	if !manjuIsBeast(dogCn) {
		t.Fatal("英文小狗卡应判兽形")
	}
	// 人形否决优先:狐耳娘是 26 岁女子+狐耳,不得判兽形
	foxGirl := map[string]any{
		"gender":       "女",
		"image_prompt": "a 26-year-old woman with fox ears and a fluffy tail, wearing a business suit",
	}
	if manjuIsBeast(foxGirl) {
		t.Fatal("人形主体(26-year-old woman)必须否决兽形判定")
	}
	// 英文动物词边界:category/dogma 不得误判
	tricky := map[string]any{
		"gender":       "男",
		"image_prompt": "a 30-year-old man in catalog-ordered dogma-themed outfit",
	}
	if manjuIsBeast(tricky) {
		t.Fatal("category/dogma 子串不得误判兽形(词边界)")
	}
}

// 兽形毛色锁回归(2026-08-28 猫·二两:主图橘白,Q版被画成黑灰狸花):
// 0.93 高重绘身份靠文本——色名必须显式进提示词,不能只靠 "same body color" 引用。
func TestQAuditBeastFurColorLock(t *testing.T) {
	cat := map[string]any{
		"appearance":   "记忆点：①橘白流浪猫 ②琥珀色眼睛",
		"image_prompt": "an orange and white chubby stray cat, bright amber eyes, slightly dusty fur, sitting upright",
	}
	fur := manjuFurAnchor(cat)
	for _, want := range []string{"orange", "white", "FUR COLOR LOCK"} {
		if !strings.Contains(fur, want) {
			t.Errorf("橘白猫毛色锁应含 %q: %s", want, fur)
		}
	}
	p := manjuQPrompt(cat)
	if !strings.Contains(p, "FUR COLOR LOCK") || !strings.Contains(p, "orange") {
		t.Errorf("兽形Q版提示词应有显式毛色锁:\n%s", p)
	}
	if strings.Contains(p, "NOT wearing human clothes") == false {
		t.Errorf("兽形Q版应保留禁人衣词")
	}
	// 无色词卡:空锁不误报
	plain := map[string]any{"image_prompt": "a small spirit beast"}
	if got := manjuFurAnchor(plain); got != "" {
		t.Errorf("无色词卡应为空锁, got %q", got)
	}
	// 中文记忆点兜底:纯中文色词也能映射
	zh := map[string]any{"appearance": "三花猫,圆脸", "image_prompt": "a small cat"}
	if got := manjuFurAnchor(zh); !strings.Contains(got, "calico") {
		t.Errorf("三花中文应映射 calico: %s", got)
	}
}
