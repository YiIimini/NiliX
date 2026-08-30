package api

// 角色形象生成·物品管线(2026-08-29 架构重构,阿碧「女人脸+花盆头」实锤治理):
// 此前所有角色共用一条人形主干(portraitPromptFor→manjuPortraitPromptFor),
// 兽形/物品都是主干上的 if 补丁+词替换——任何人形收尾词(正面人脸锚 front-facing
// portrait/symmetrical frontal face、单人白底 solo portrait…character…outfit、
// 人形视图后缀 standing pose/complete outfit)漏改一处,物品角色就被污染成拟人。
// 用户裁定架构原则:不同分类调用不同封装函数,模块化解耦,互不污染:
//   物种路由(唯一判定) → 三条独立管线各自完整收尾:
//   物品 = 本文件(manjuItemFinal / manjuItemViewSuffix / manjuItemNeg,零人形词);
//   兽形 = manjuBeastViewSuffix + manjuPortraitPromptFor 兽锚分支;
//   人形 = portraitPromptFor 原链(写实/3D 两档);
//   Q 版(chibi)独立于物种——用户规则「Q版形象不影响」,允许拟人,在路由最前分流。

import (
	"regexp"
	"strconv"
	"strings"
)

// manjuItemNeg 物品角色专属负面(负面通道硬禁一切人形;含「叶子拼的脸」——
// 阿碧实锤即 Krea-2 把植物词与人脸锚折中出的叶脸女人,负面必须点名植物拟人形态)
const manjuItemNeg = "human, person, man, woman, boy, girl, human face, human head, human body, humanoid, humanoid figure, face made of leaves, girl made of leaves, woman made of flowers, woman made of leaves, eyes made of leaves, leaves arranged as a face, face in the foliage, human skin, hands, arms, legs, wearing clothes, clothing on the plant, dressed plant"

// manjuItemSoloBgAnchor 物品定妆收尾锚(与人形 manjuPortraitSoloBgAnchor 成对,
// 零人形措辞:不提 character/outfit/portrait of a person)
const manjuItemSoloBgAnchor = "solo shot of only this one single object in the frame, absolutely no people, no person, no hands holding it, plain pure white background, clean white studio backdrop, no scenery, no environment, no background objects"

// manjuItemViewAnchors 物品视图硬锚(与人形 manjuViewAnchors 成对,零人形措辞):
// 人形锚 FULL BODY standing figure/trousers/skirt 对物品是穿衣服+站起来的人形邀请
var manjuItemViewAnchors = map[string]string{
	"full":   "COMPLETE OBJECT view, the entire item itself from top to bottom fully visible, still-life object photography framing with clear margin around the object, natural upright placement, front view, no person",
	"side":   "SIDE PROFILE view of the object, rotated exactly 90 degrees, the full object visible from the side, still-life framing, no person",
	"detail": "EXTREME CLOSE-UP detail shot, zoomed on the object's single most distinctive feature (the unique marked leaf / engraving / glowing vein / ornament), macro framing, no person",
}

// manjuItemIdentityAnchor 物品身份锚(与人形 manjuIdentityAnchor 成对):
// 锁「同一件物品」(形态/容器/纹理/标记/材质一致),不提 hair/beard/facial features
const manjuItemIdentityAnchor = ", the exact same object as the reference image (identical shape and proportions, identical pot or vessel, identical leaf colors and markings, identical material and texture, identical unique marked feature)"

// manjuItemViewSuffix 物品视图后缀:full/side/detail 只描述物品本体
// (人形视图后缀 standing pose/complete outfit/hairstyle silhouette 对物品是拟人邀请)
func manjuItemViewSuffix(view string) string {
	switch view {
	case "full":
		return "the complete object itself in full view, the entire plant from top leaves to pot base, natural still-life presentation, no person"
	case "side":
		return "90 degree side view of the same object, still-life silhouette, no person"
	case "detail":
		return "extreme close-up on the object's signature feature (the unique marked leaf / engraving / glowing vein), sharp focus, high detail, no person"
	}
	return ""
}

// manjuItemStrip 剥物品 image_prompt/appearance 里的拟人脸诱导词:
// LLM 旧卡与素材常给植物写「round face / big sparkling eyes」(拟人邀请词),
// 物品主形象必须剥净。Q 版不受影响(chibi 分支在路由最前,不进物品管线)。
func manjuItemStrip(s string) string {
	for _, w := range []string{
		"with a round face made of fresh green leaves", "round face made of leaves",
		"a round face", "round face", "big round sparkling eyes", "sparkling eyes",
		"big glossy eyes", "big round eyes", "cute smug expression", "smug expression",
		"cute expression", "a cute face", "cute face", "humanoid figure", "a humanoid",
		"portrait of ", "Portrait of ",
		// 2026-08-29 审计 V3 止血:旧 plan/旧模板烤进 image_prompt 的人形锚残留
		// (scriptImagePrompt 物种路由已根治源头,此处剥存量)
		"semi-realistic stylized illustration of an East Asian/Chinese character",
		"East Asian/Chinese character", "East Asian/Chinese facial features",
		"subtly anime-stylized semi-realistic", "full body, head to toe",
		"full body", "head to toe", "7-head-tall", "natural 7-head-tall proportions",
		"standing pose", "complete outfit",
	} {
		s = strings.ReplaceAll(s, w, "")
	}
	s = strings.ReplaceAll(s, ", ,", ",")
	s = strings.ReplaceAll(s, ", ,", ",")
	return strings.Trim(s, " ,")
}

// manjuItemFinal 物品形象管线收尾(独立于人形链,零人形措辞):
// 物品本体 image_prompt + 外观特征 + 物品锚 + 物品白底锚。
// 不吃人形链任何收尾:无 frontFace(物品无正脸概念)、无 outfit/portrait 词、
// 无性别锚(manjuViewGenderAnchor 已豁免物品)。
func manjuItemFinal(prompt string, m map[string]any) string {
	p := manjuItemStrip(prompt)
	ap := manjuItemStrip(manjuSanitizeAppearance(m, str(m["appearance"])))
	if ap != "" && !strings.Contains(p, ap) {
		p = p + ", distinct item features: " + ap
	}
	return p + manjuItemAnchor + ", " + manjuItemSoloBgAnchor
}

// charNegPrompt 按物种的负面提示词(主图/视图/form2/抽卡正面统一取负面入口):
// 人形/兽形 = 内置负面;物品 = 追加禁人负面(负向通道兜底正向锚)。
// Q 版 img2img 不走此入口(用户规则「Q版形象不影响」,负面不禁人)。
func (ctx *manjuCtx) charNegPrompt(m map[string]any) string {
	neg := ctx.negPrompt()
	if m != nil && manjuIsItem(m) {
		neg = neg + ", " + manjuItemNeg
	}
	return neg
}

// ---- 角色年龄分档(Q 版年龄锚,2026-08-30 用户需求:老年人生成的都是年轻 Q 版,完全不符) ----

// manjuAgeBand 角色年龄分档(纯函数,机械判定不依赖 LLM):child/teen/adult/middle/elderly。
// 数据源=age+appearance+image_prompt(中文关键词 + 英文年龄词 + 数字年龄三路):
// chibi 基底是"圆脸红晕萌"年轻模板,年龄必须由机器按角色卡注入正向特征锚,
// 弱锚("keeping original age")与 LLM 模板教条在此前实测中都无法阻止幼态化。
func manjuAgeBand(m map[string]any) string {
	if m == nil {
		return "adult"
	}
	vs, _ := m["views"].(map[string]any)
	src := str(m["age"]) + " " + str(m["appearance"]) + " " + str(m["image_prompt"]) + " " + str(vs["q"])
	low := strings.ToLower(src)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(src, w) || strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	// 数字年龄(中文「N岁」/英文 "N years old";区间取首个命中档)
	for _, mm := range regexp.MustCompile(`(\d+)\s*(?:years?[- ]old|岁)`).FindAllStringSubmatch(low, -1) {
		if n, err := strconv.Atoi(mm[1]); err == nil && n > 0 {
			switch {
			case n >= 50:
				return "elderly"
			case n >= 40:
				return "middle"
			case n < 13:
				return "child"
			case n < 20:
				return "teen"
			default:
				return "adult"
			}
		}
	}
	switch {
	case has("老年", "老者", "老妇", "老汉", "花甲", "暮年", "古稀", "elderly", "old man", "old woman", "aged ", "grandpa", "grandma"):
		return "elderly"
	case has("中年", "中年人", "middle-aged", "middle aged"):
		return "middle"
	case has("儿童", "孩童", "幼童", "小男孩", "小女孩", "child", "kid", "toddler"):
		return "child"
	case has("少年", "少女", "正太", "萝莉", "teen", "teenage"):
		return "teen"
	}
	return "adult"
}

// manjuQAgeAnchor Q 版年龄特征锚(独立纯函数,manjuQPrompt 接线)。
// 【三物种分流·2026-08-30 用户规则】Q 版按 人/兽/物 分档:
//   人形 = 本函数(皱纹/松弛/祖辈气质/成熟面容/少年儿童各档);
//   兽形 = manjuQBeastAgeAnchor(兽龄特征:口鼻灰白/老兽气质,严禁人类皱纹措辞);
//   物品 = 无年龄语义(返回空,不注入任何年龄词)。
// 只改头身比不改年龄是 Q 版的形态学原则(chibi ≠ 年轻化)。
func manjuQAgeAnchor(m map[string]any) string {
	if manjuIsBeast(m) || manjuIsItem(m) {
		return "" // 兽/物走各自锚,人形年龄词对兽/物是污染
	}
	switch manjuAgeBand(m) {
	case "elderly":
		gm := "grandpa"
		if manjuIsFemale(m) {
			gm = "grandma"
		}
		return ", an ELDERLY chibi with a clearly aged face: visible forehead wrinkles, crow's feet at the eye corners, smile lines around the mouth, gently sagging round cheeks, kindly " + gm + " charm, hair exactly as the reference (grey or white kept if the reference shows it), clearly an elderly " + gm + " version of the character, NOT a youthful face, NOT smooth young skin"
	case "middle":
		return ", a middle-aged chibi, mature settled face with faint forehead lines and a dignified grown-up look, clearly middle-aged, not a teenager"
	case "teen":
		return ", a teenage chibi, youthful adolescent face accurate to the character's age, not a toddler"
	case "child":
		return ", a little child chibi, small kid face accurate to the character's age"
	}
	return ""
}

// manjuQMaleFaceCls Q 版男性面部措辞按年龄分档(替换旧硬编码 "young man's face"——
// 老年男性被该措辞直接锚成年轻人,2026-08-30 用户实锤)
func manjuQMaleFaceCls(m map[string]any) string {
	switch manjuAgeBand(m) {
	case "elderly":
		return "clearly masculine elderly man's aged wrinkled face"
	case "middle":
		return "clearly masculine middle-aged man's mature face"
	case "teen", "child":
		return "clearly boyish face"
	}
	return "clearly masculine young man's face"
}

// manjuQBeastAgeAnchor 兽形 Q 版兽龄锚(独立纯函数):老年兽用兽的年龄特征——
// 口鼻部/眼周毛色渐灰、温和沉稳的老兽气质;严禁人类皱纹/祖辈措辞(兽脸上画
// 人类皱纹=面部畸形)。物品无年龄语义恒空;成年兽不注入(默认即成兽萌)。
func manjuQBeastAgeAnchor(m map[string]any) string {
	if !manjuIsBeast(m) || manjuAgeBand(m) != "elderly" {
		return ""
	}
	return ", an ELDERLY animal chibi: fur slightly greying around the muzzle and eye areas, gentle calm aged-animal charm, wise old beast expression, same species and same body markings as the reference, NOT a young cub"
}
