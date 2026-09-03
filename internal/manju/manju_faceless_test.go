package manju

import (
	"strings"
	"testing"
)

// TestFacelessClassification 无脸剪影类角色分类(2026-09-01 阿影 side 双体实锤):
// ①影灵/影子类 species 非人但走剪影锚而非兽形锚;
// ②isNonPhysical 不被 "wisps of darkness" 误判(墨千秋实锤);
// ③剪影 Q 版不走兽形 fur 分支。
func TestFacelessClassification(t *testing.T) {
	aying := map[string]any{
		"species":      "影灵",
		"image_prompt": "a human-shaped silhouette made of living black mist and shadow, no facial features except two faint glowing blue dots for eyes",
		"appearance":   "影灵·影子精灵",
	}
	if !manjuFacelessChar(aying) {
		t.Fatal("阿影(影灵·人形剪影)应判无脸剪影类")
	}
	p := manjuViewPromptBuild("portrait", "side", aying)
	if !strings.Contains(p, "SILHOUETTE PROFILE") {
		t.Fatalf("阿影 side 应走剪影锚, got: %s", p[:80])
	}
	if strings.Contains(p, "four paws") || strings.Contains(p, "beast") {
		t.Fatalf("阿影 side 不应走兽形锚(四足兽), got: %s", p[:120])
	}
	q := manjuQPrompt(aying)
	if strings.Contains(q, "FUR COLOR LOCK") || strings.Contains(q, "same beast creature") {
		t.Fatalf("阿影 Q版不应走兽形 fur 分支, got: %s", q[:200])
	}
	if !strings.Contains(q, "shadow blob") {
		t.Fatalf("阿影 Q版应走剪影团子分支, got: %s", q[:150])
	}
	// 墨千秋:wisps of darkness 不应误判为非实体光精灵
	mo := map[string]any{
		"species":      "人",
		"image_prompt": "a 21-year-old male Chinese dark-attribute genius, wearing a black academy uniform with the collar popped, wisps of darkness curling at his feet",
		"appearance":   "人类·男,暗影系·天才榜第二",
		"gender":       "男",
		"age":          "21岁",
	}
	if manjuIsNonPhysical(mo) {
		t.Fatal("墨千秋(人类·暗影系)不应被 wisps of darkness 误判为非实体光精灵")
	}
	if manjuIsBeast(mo) {
		t.Fatal("墨千秋(人类)不应判兽形")
	}
	q2 := manjuQPrompt(mo)
	if strings.Contains(q2, "holographic data spirit") {
		t.Fatalf("墨千秋 Q版不应走光精灵分支, got: %s", q2[:200])
	}
	if !strings.Contains(q2, "black academy uniform") && !strings.Contains(q2, "OUTFIT LOCK") {
		t.Fatalf("墨千秋 Q版应保留人形着装锁, got: %s", q2[:200])
	}
	// 单角色硬锚覆盖所有人形 Q 版
	if !strings.Contains(q2, "one single chibi character only") {
		t.Fatalf("墨千秋 Q版缺单角色硬锚: %s", q2[:300])
	}
	// 无 palm-sized 残留
	if strings.Contains(q2, "palm-sized") || strings.Contains(q2, "palm-size") {
		t.Fatal("Q版不应再含 palm-sized(一大一小双人诱因)")
	}
}

// TestQPromptSingleFigure 人形 Q 版单角色硬锚(沈照/夜枭/沈伯/王胖子一大一小实锤)
func TestQPromptSingleFigure(t *testing.T) {
	for _, c := range []map[string]any{
		{"id": "沈照", "gender": "男", "age": "19岁", "species": "人", "q_form": "A chibi-style 3-head-tall cute version of a 19-year-old male student, short platinum-white hair, bright golden eyes", "image_prompt": "a 19-year-old male student, short platinum-white hair, wearing a dark academy uniform jacket", "appearance": "人类·男"},
		{"id": "王胖子", "gender": "男", "age": "19岁", "species": "人", "q_form": "A chibi 3-head-tall version of a 19-year-old Chinese male student, chubby round face", "image_prompt": "a 19-year-old Chinese male student, chubby round face, wearing an academy uniform", "appearance": "人类·男"},
	} {
		q := manjuQPrompt(c)
		if !strings.Contains(q, "one single chibi character only") {
			t.Fatalf("%s Q版缺单角色硬锚", c["id"])
		}
		if strings.Contains(q, "palm-sized") {
			t.Fatalf("%s Q版含 palm-sized", c["id"])
		}
	}
}

// 2026-09-03 根治回归(小天天实锤):manjuFacelessChar 旧版裸 Contains("shadow"/"mist")
// 把部位词 "dark under-eye shadows of exhaustion"(眼下乌青)误判成影子形态 → 正常彩色
// 少女被套剪影锚,full/side/detail/q 全渲染成黑剪影/双体/影子人/猫娘。收紧为形态签名
// 匹配;manjuIsBeast 同修(系统精灵等人形非人种族不再无条件四足兽锚)。

// 小天天真实卡(修仙界 素材/人物生成提示词.json,判定相关字段)
func facelessXiaoTiantian() map[string]any {
	return map[string]any{
		"id":      "小天天",
		"species": "系统精灵",
		"image_prompt": "Front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, a young female system spirit in a youthful petite form, subtly anime-stylized semi-realistic character, adorable round face with shoulder-length pink hair and straight bangs, dark under-eye shadows of exhaustion, a customer-service smile that looks stamped on yet trembling at the corner, a worn work badge with rubbed-off characters hanging on her chest, wearing a pale lilac service uniform",
	}
}

// 三督导真实卡:白瓷无脸面具+灰雾 = 真·无脸剪影形态(正判必须保留)
func facelessSanDudao() map[string]any {
	return map[string]any{
		"id":      "三督导",
		"species": "系统精灵",
		"image_prompt": "Front-facing portrait, head facing the camera directly, symmetrical frontal composition, an eerie faceless white porcelain mask with no eyes no nose no mouth only a smooth blank surface, wearing an identical stark white hooded robe with a high collar, faint grey mist curling at the shoulders, a faint scar-like hairline crack running down the mask forehead, unnervingly empty and identical presence, unsettling void-like pressure, pure white background, single figure only",
	}
}

func TestFacelessCharFalsePositives(t *testing.T) {
	// 小天天:眼下乌青(部位词)不得判剪影
	if manjuFacelessChar(facelessXiaoTiantian()) {
		t.Fatal("小天天:dark under-eye shadows(眼下乌青)是面部细节,不得误判剪影形态")
	}
	// 部位/装饰词变体全不命中
	for _, ip := range []string{
		"subtle eye shadows and dark circles under her tired eyes",
		"the character stands in a misty morning alley",
		"wisps of steam curling from the teacup at her side",
	} {
		m := map[string]any{"id": "x", "species": "精灵", "image_prompt": ip}
		if manjuFacelessChar(m) {
			t.Errorf("部位/装饰措辞不得误判剪影: %s", ip)
		}
	}
	// 人类门槛保留:species=人即使含形态词也不走剪影锚
	if manjuFacelessChar(map[string]any{"id": "x", "species": "人", "image_prompt": "a living shadow figure"}) {
		t.Fatal("species=人 必须走人形管线")
	}
}

func TestFacelessCharTruePositives(t *testing.T) {
	// 三督导:faceless 白面具(真实卡)
	if !manjuFacelessChar(facelessSanDudao()) {
		t.Fatal("三督导:faceless 无脸面具+灰雾是剪影形态,正判必须保留")
	}
	// 阿影(影灵=人形黑雾剪影)典型措辞
	for _, ip := range []string{
		"a humanoid figure of black mist and shadow with two faint glowing blue dot eyes",
		"a living shadow with no distinguishable face",
		"an entity made of black mist, humanoid proportions",
		"一个人形黑雾剪影,没有五官",
	} {
		m := map[string]any{"id": "x", "species": "影灵", "image_prompt": ip}
		if !manjuFacelessChar(m) {
			t.Errorf("影子形态正判不得漏: %s", ip)
		}
	}
	// 黑雾(2026-09-03 首轮收紧时的漏判回归):卡文语序是 "black mist in the shape of
	// a crouching figure"(mist 与 figure 分离),非紧邻组合;appearance 中文「一团
	// 黑色雾气」是主体描述——两路都必须命中。
	heiwu := map[string]any{
		"id":           "黑雾",
		"species":      "影",
		"image_prompt": "a faint, almost translucent black mist in the shape of a crouching figure, barely holding together, with two dim blue glowing dots for eyes, its edges fraying into ragged wisps of black smoke",
		"appearance":   "一团几乎透明的黑色雾气，呈蹲伏的人形轮廓，边缘模糊飘散",
	}
	if !manjuFacelessChar(heiwu) {
		t.Fatal("黑雾:mist in the shape of figure / 一团黑色雾气 是剪影形态,不得漏判")
	}
}

func TestBeastHumanoidRaceSpecies(t *testing.T) {
	// 小天天:系统精灵=人形种族,不判四足兽
	if manjuIsBeast(facelessXiaoTiantian()) {
		t.Fatal("小天天:species=系统精灵 是人形种族,不得走四足兽形锚")
	}
	// 人形种族词表各词不判兽(species 权威层否决)
	for _, sp := range []string{"光精灵", "花仙子", "女神", "机器人", "仿生人", "智能生命"} {
		if manjuIsBeast(map[string]any{"id": "x", "species": sp, "image_prompt": "a young female character portrait"}) {
			t.Errorf("人形种族不得判兽: %s", sp)
		}
	}
	// 真兽形照判:species 神兽/妖兽(词面不含人形种族词)
	for _, sp := range []string{"神兽", "妖兽", "灵宠", "九尾狐"} {
		if !manjuIsBeast(map[string]any{"id": "x", "species": sp}) {
			t.Errorf("真兽形态 species 必须判兽: %s", sp)
		}
	}
	// 人形种族但形象本体是兽(兜底捞回):精灵 species+英文兽词卡面
	if !manjuIsBeast(map[string]any{
		"id": "x", "species": "精灵",
		"image_prompt": "a small fluffy creature with soft fur and paws, round ears",
	}) {
		t.Fatal("species=精灵 但形象是兽类本体(creature/fur/paws),兜底必须捞回兽形")
	}
	// 原有约束保留:species=人 不判兽
	if manjuIsBeast(map[string]any{"id": "x", "species": "人"}) {
		t.Fatal("species=人 不得判兽")
	}
}

// 集成:视图构建分流——小天天走人形锚,三督导走剪影锚(同一本书两个系统精灵各归其位)
func TestViewPromptBuildRouting(t *testing.T) {
	xt := manjuViewPromptBuild(facelessXiaoTiantian()["image_prompt"].(string), "full", facelessXiaoTiantian())
	if strings.Contains(xt, "shadow figure") || strings.Contains(xt, "shadow mass") {
		t.Fatalf("小天天 full 不得带剪影锚: %s", xt[:160])
	}
	if !strings.Contains(xt, "FULL BODY view") {
		t.Fatalf("小天天 full 应走人形全身锚: %s", xt[:160])
	}
	sd := manjuViewPromptBuild(facelessSanDudao()["image_prompt"].(string), "full", facelessSanDudao())
	if !strings.Contains(sd, "shadow figure") {
		t.Fatalf("三督导 full 应保留剪影锚: %s", sd[:160])
	}
}
