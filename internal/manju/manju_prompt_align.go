package manju

// manju_prompt_align.go 提示词-渲染提交契约对齐(2026-08-30 H3 官方源码核验转化)。
//
// 官方实现事实(MiniMax-H3 仓库 chat_template + ComfyUI comfy/text_encoders/
// minimax.py + 官方 MiniMaxH3-Easy/_reference_conditioning,三处一致):
//   1. H3 的 Qwen3-VL 文本编码器在 tokenize 时把参考媒体自动列在用户提示词之前,
//      每张挂载图自带 "Picture N:" 前缀、每条挂载音频自带 "Audio N:" 前缀——
//      N 严格按挂载顺序 1 递增(tokenize_with_weights: add_text("<Picture %d>: ")
//      → add_vision(图) → … → add_text(用户全文))。
//   2. 因此提示词里的 <Picture N>/<Audio N> 不是作者可自由编排的记号,而是对输入流
//      里第 N 张图/第 N 条音频的指认——编号与挂载顺序错位 = 指鹿为马:模型把场景图
//      当人脸、把别人的音色安给不存在的说话者(EP01 实锤:Q 版小人占 <Picture 3>
//      而实际第 3 槽挂的是场景图;场景占 <Picture 1> 而第 1 槽挂的是人物正脸)。
//   3. 官方 Easy 生态用 @N 占位符 + _resolve_reference_prompt 在节点层消灭编号错位;
//      NiliX 走 LLM 直出没有这层,此前只有"超界剥除"(manjuStripDanglingPictureRefs),
//      N≤槽位数但语义错位的引用完全不设防。
//   4. 说话者 (Sx) 官方语义:按实际发声顺序分配一次、复用;每镜是独立 clip 独立编码,
//      镜内应从 S1 连续分配——跨镜全局编号在单镜渲染里是悬空引用(EP01 实锤:老赵
//      台词标 (S6)、路人标 (S11),音色定义只绑 S1/S2,模型认为存在未定义说话者)。
//   5. 官方提示词指南(base-en §4.4 / ref-en §5.4)要求 <d> 内必须带语言标签:
//      <d>[Chinese] 原文</d>。历史上 [中文](中文词)被逐字念出是标签语种用错,
//      正确修复是规范化为 [Chinese],而不是剥掉语言标签(裸中文让模型自行猜配音语言)。
//   6. 切点时码必须落在本镜时长内(base-en:"a strictly increasing cut time that
//      falls within the video duration");每镜独立渲染,时码应为 clip-local——
//      EP01 实锤:5 秒镜写 At 00:15.000(全片时间轴)完全越界。
//
// 模块化原则:每个对齐功能一个独立纯函数(独立可测),manjuAlignShotPrompt 统一
// 编排,manjuFinalizeAligned 是指纹/编码/渲染三处汇点的唯一入口(对齐后接既有
// manjuFinalizePromptPure;对齐改词即指纹变化,旧条件缓存自动失效,无需人工 bump)。

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ---- 渲染提交契约(单一事实源) ----

// manjuCharSlot 契约内单个登场角色的参考图槽位区间
type manjuCharSlot struct {
	ID       string // 角色 id(中文,与 plan/资产文件名一致)
	PicStart int    // 该角色第一张参考图槽位号(1 起)
	PicEnd   int    // 该角色最后一张参考图槽位号(含;无图=0)
}

// manjuRefContract 一镜的渲染提交契约:参考图/音频的实际挂载顺序与归属。
// 与 charRefNames/sceneRefName/voiceBindingsFor 的提交顺序同源推导,是提示词
// 对齐层的唯一依据——两侧永不漂移是对齐生效的前提。
type manjuRefContract struct {
	Chars       []manjuCharSlot // 登场角色(≤3,顺序=挂载顺序=plan characters 顺序)
	SceneName   string          // 场景中文名(场景行归属锚)
	SceneSlot   int             // 场景图槽位号(1 起;无场景图=0)
	PicSlots    int             // 参考图总槽位数
	VoiceRoster []string        // 有音色音频的角色 id(登场顺序;音频挂载顺序=此序)
}

// refContractFor 构造该镜的渲染提交契约。视图数与 charRefNames 同源
// (shotViewRelsFor:Q 版/form2 切换单一数据源),场景槽=人物视图之后,音色清单与
// voiceBindingsFor 同源——提示词对齐与实际挂载由同一契约推导。
func (ctx *manjuCtx) refContractFor(s manjuShot) manjuRefContract {
	c := manjuRefContract{SceneName: s.Scene}
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	slot := 0
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		cs := manjuCharSlot{ID: cid}
		if k := len(ctx.shotViewRelsFor(s, cid, i, n)); k > 0 {
			cs.PicStart = slot + 1
			cs.PicEnd = slot + k
			slot += k
		}
		c.Chars = append(c.Chars, cs)
	}
	if s.Scene != "" && fileExists(filepath.Join(ctx.assetsDir, "scenes", s.Scene+".png")) {
		slot++
		c.SceneSlot = slot
	}
	c.PicSlots = slot
	for _, cid := range s.Characters {
		if len(c.VoiceRoster) >= 3 {
			break
		}
		if ctx.charVoiceRef(cid) != "" {
			c.VoiceRoster = append(c.VoiceRoster, cid)
		}
	}
	return c
}

// ---- 编排入口 ----

// manjuAlignShotPrompt 契约对齐编排(纯函数,幂等)。顺序有依赖:
// Picture 重排最先(行归属解析依赖原始 subject_definitions 结构)→ 权威清单注入 →
// Sx 镜内重编(Audio 行的 Sx 需要画面段定序结果)→ Audio 规范化(Sx 用 dialogue
// 发声角色序重写——Audio 行内旧 Sx 本身是 LLM 乱编值,不可信)→ 语言标签 → 时码。
// dialogue:分镜台词原文("角色:台词"逐行),是发声角色顺序的确定性来源。
func manjuAlignShotPrompt(hp string, c manjuRefContract, durationSec int, dialogue string) string {
	return manjuAlignShotPromptReg(hp, c, durationSec, dialogue, nil)
}

// manjuAlignShotPromptReg 带全局说话人注册表的版本(2026-08-30 ver14):有音色绑定且
// 已注册的角色 (Sx) 跨镜全局稳定(官方契约 "A speaker keeps the same ID across shots",
// 修复逐镜重排导致的声线漂移);reg=nil 时与 manjuAlignShotPrompt 完全一致。
func manjuAlignShotPromptReg(hp string, c manjuRefContract, durationSec int, dialogue string, reg map[string]string) string {
	if hp == "" {
		return hp
	}
	hp = repairRetentionMarkers(hp)
	hp = alignPictureRefs(hp, c)
	hp = injectAttachmentManifest(hp, c)
	hp = alignSpeakerIDsReg(hp, c, reg, dialogue)
	hp = alignAudioDefsReg(hp, c, dialogue, reg)
	hp = alignDialogueLangTags(hp)
	hp = alignShotTimecodes(hp, durationSec)
	// 2026-09-02 分镜脚本内容级兜底(我的影子会咬人渲染异常实锤):
	// ①chibi Subject 空引用清理(非内心戏镜删行防 Q版乱入/内心戏镜保句清悬空);
	// ②画外·说话人台词强制 off-screen(镜6 spectator shouts 画面开口实锤)。
	// 纯函数,指纹/渲染共用;幂等。
	hp = fixShotPromptContent(hp, c, dialogue)
	return hp
}

// manjuFinalizeAligned 指纹/编码/渲染三处汇点统一入口:先契约对齐、后既有最终化
// (guard 注入/违规词替换/超界剥除)。调用方一律走本函数,禁止直接调
// manjuFinalizePromptPure,否则指纹与渲染输入漂移(恒 stale 反复重渲的历史教训)。
func manjuFinalizeAligned(hp string, c manjuRefContract, durationSec int, dialogue string, hasChars bool, picSlots int) string {
	return manjuFinalizePromptPure(manjuAlignShotPrompt(hp, c, durationSec, dialogue), hasChars, picSlots)
}

// manjuFinalizeAlignedReg 带全局说话人注册表的汇点变体(2026-08-30 ver14,渲染/
// 指纹统一走 ctx.finalizeAlignedPrompt,见 manju_pipeline.go)。
func manjuFinalizeAlignedReg(hp string, c manjuRefContract, durationSec int, dialogue string, hasChars bool, picSlots int, reg map[string]string) string {
	return manjuFinalizePromptPure(manjuAlignShotPromptReg(hp, c, durationSec, dialogue, reg), hasChars, picSlots)
}

// ---- 模块一:retention_analysis 标记修复 ----

// reBrokenRetention 括号损坏形态:LLM 直出常见 "(appears in Shot 1])"(圆括号开口
// +方括号残尾);官方格式为 "<Subject 1> (appears in [Shot 1]):"。
var reBrokenRetention = regexp.MustCompile(`\(appears in (?:\[)?Shot (\d+)\]?\)`)

// repairRetentionMarkers 修复 retention 行括号损坏(独立可测):
// "(appears in Shot 1])" / "(appears in Shot 1]" → "(appears in [Shot 1])";幂等。
func repairRetentionMarkers(hp string) string {
	if !strings.Contains(hp, "appears in") {
		return hp
	}
	return reBrokenRetention.ReplaceAllString(hp, "(appears in [Shot $1])")
}

// ---- 模块二:Picture 槽位重排 ----

var rePicInLine = regexp.MustCompile(`<Picture\s*(\d+)\s*>`)

// 人物外观信号词(与 manju_script_parse.go 人物信号词同源思路):行内含这些词视为
// 人物定义行;配合环境词表做高置信行归属。低置信行一律不动(宁可漏判交给权威清单
// 与超界剥除兜底,不可错判把场景槽安到人物头上)。
var manjuPersonSignalWords = []string{
	"hair", "glasses", "shirt", "collar", "watch", "robe", "garment", "dress",
	"costume", "face", "beard", "mustache", "ponytail", "fringe", "eyebrow",
	"man", "woman", "boy", "girl", "youth", "elder", "child", "monk", "soldier",
	"servant", "disciple", "cook", "guard", "jailer", "maid", "narrator", "chibi",
}

// 环境信号词(既有 manjuStripDanglingPictureRefs envWords 基础上扩充场景类型)
var manjuEnvSignalWords = []string{
	"environment", "scene", "courtyard", "plaza", "street", "room", "office",
	"corridor", "hallway", "building", "city", "landscape", "interior", "hall",
	"temple", "palace", "mountain", "forest", "river", "bridge", "gate", "wall",
	"sky", "floor", "background", "shop", "kitchen", "market", "square", "alley",
}

// alignPictureRefs 把 subject_definitions 里的 <Picture N> 引用重排到与挂载顺序一致
// 的槽位(独立可测)。规则(高置信才动,防御性优先):
//   ① 行含 "environment" 或本镜场景中文名 → 场景行,引用重写为场景槽;
//   ② 行含人物外观信号词且不含环境信号词 → 人物行,按人物行序 i 对应登场角色 i,
//     行内第 t 个引用重写为该角色槽 PicStart+t-1;
//   ③ 其余含引用行:低置信不动(留给权威清单声明 + 超界剥除兜底);
//   ④ 旧号已被先行映射(冲突):后行不映射(留给剥除,不做二义替换)。
// 映射全文生效(detailed_description/retention/summary 同号引用一并修正),
// 两阶段占位替换防止链式串换(1→2、2→3 连环覆盖)。
func alignPictureRefs(hp string, c manjuRefContract) string {
	if !strings.Contains(hp, "<Picture") || len(c.Chars) == 0 {
		return hp
	}
	start := strings.Index(hp, "subject_definitions:")
	if start < 0 {
		return hp
	}
	segEnd := len(hp)
	if i := strings.Index(hp[start:], "\nsummary:"); i >= 0 {
		segEnd = start + i
	}
	seg := hp[start:segEnd]

	remap := map[string]bool{}       // 需要重排的行原文(行级替换,2026-09-03 换脸根治)
	lineMaps := map[string]map[int]int{} // 行原文 → 该行的旧号→新槽位映射
	stripLines := map[string]bool{} // 行原文 → 剥除标记(低置信行错指人物槽)
	personLine := 0
	charPicTop := 0 // 人物槽区间上界(全部人物视图总数)
	for _, cs := range c.Chars {
		if cs.PicEnd > charPicTop {
			charPicTop = cs.PicEnd
		}
	}
	recordLine := func(line string, oldN, newN int) {
		if oldN == newN {
			return // 已正确:无需登记(也避免占位替换空转)
		}
		if lineMaps[line] == nil {
			lineMaps[line] = map[int]int{}
		}
		if prev, dup := lineMaps[line][oldN]; dup && prev != newN {
			return // 行内冲突:放弃(同 recordPicRemap 语义)
		}
		lineMaps[line][oldN] = newN
		remap[line] = true
	}
	for _, line := range strings.Split(seg, "\n") {
		// 权威挂载清单是机器注入的确定性事实,不参与行归属判定(它一行内并列声明
		// 全部槽位,被当定义行解析会把已正确的编号再次改写=幂等破坏)
		if strings.Contains(line, manjuAttachmentManifestKey) {
			continue
		}
		if !strings.Contains(line, "<Picture") {
			continue
		}
		refs := rePicInLine.FindAllStringSubmatch(line, -1)
		if len(refs) == 0 {
			continue
		}
		isScene := c.SceneSlot > 0 && (strings.Contains(line, "environment") ||
			(c.SceneName != "" && strings.Contains(line, c.SceneName)))
		isPerson := !isScene && containsAnyWord(line, manjuPersonSignalWords) &&
			!containsAnyWord(line, manjuEnvSignalWords)
		switch {
		case isScene:
			for _, r := range refs {
				recordLine(line, mustAtoi(r[1]), c.SceneSlot)
			}
		case isPerson && personLine < len(c.Chars):
			cs := c.Chars[personLine]
			personLine++
			if cs.PicEnd < cs.PicStart {
				continue // 该角色无参考图:不映射(其行内引用留给剥除)
			}
			for t, r := range refs {
				slot := cs.PicStart + t
				if slot > cs.PicEnd {
					break // 行内引用数超过该角色视图数:多余不映射
				}
				recordLine(line, mustAtoi(r[1]), slot)
			}
		default:
			// 低置信行(人物行收满后的场景/Q版/道具行,无 environment 锚):
			// 引用落在人物槽区间=错指别人定妆照,剥标签保句子(宁可不指,不可错指);
			// 引用=场景槽(它可能真是场景行)或超界(交给超界剥除)则不动
			for _, r := range refs {
				if n := mustAtoi(r[1]); n >= 1 && n <= charPicTop {
					stripLines[line] = true
					break
				}
			}
		}
	}
	if len(remap) == 0 && len(stripLines) == 0 {
		return hp
	}
	// 先剥(基于原行号判定)后重排(剥后行已无标签,不受替换影响)
	for line := range stripLines {
		stripped := rePicInLine.ReplaceAllString(line, "")
		stripped = strings.ReplaceAll(stripped, " in ,", ",")
		stripped = strings.ReplaceAll(stripped, " in  ", " ")
		stripped = regexp.MustCompile(`\s{2,}`).ReplaceAllString(stripped, " ")
		hp = strings.ReplaceAll(hp, line, stripped)
	}
	// 2026-09-03 行级替换根治换脸(递了三千年 EP01 镜8 实锤):旧实现按 remap 全文
	// 替换 <Picture N>,前提假设"同一旧号全 prompt 同义"——但多主体错位场景下
	// Subject 1 的 <Picture 4> 需改 1、Subject 2 的 <Picture 4> 本就正确(4→4 不登记),
	// 全文替换把 Subject 2 正确的 4 也改成 1 → 两角色同指一张定妆照 = 换脸。
	// 改为只对"判定需要重排的行"做行内替换(两阶段占位防链式串换),其余行
	// (含已正确行/挂载清单/retention/summary)一概不动。漏改(少数同号异义引用)
	// 远轻于错改(换脸),挂载清单的权威声明继续兜底。
	for line, m := range lineMaps {
		hp = strings.ReplaceAll(hp, line, replacePictureTags(line, m))
	}
	return hp
}

// recordPicRemap 登记旧号→新槽位映射;旧号已被映射到不同新号(行间冲突)时放弃。
// 2026-09-03 行级替换后仅单测沿用(生产路径走 alignPictureRefs 内 recordLine)。
func recordPicRemap(remap map[int]int, oldN, newN int) {
	if oldN == newN {
		return // 已正确:无需登记(也避免占位替换空转)
	}
	if prev, dup := remap[oldN]; dup && prev != newN {
		return
	}
	remap[oldN] = newN
}

// replacePictureTags 按 remap 全文替换 <Picture N>(\x00PIC 占位两阶段防串换)
func replacePictureTags(hp string, remap map[int]int) string {
	for old, nw := range remap {
		hp = strings.ReplaceAll(hp, fmt.Sprintf("<Picture %d>", old), fmt.Sprintf("\x00PIC%d\x00", nw))
	}
	for _, nw := range remap {
		hp = strings.ReplaceAll(hp, fmt.Sprintf("\x00PIC%d\x00", nw), fmt.Sprintf("<Picture %d>", nw))
	}
	return hp
}

// containsAnyWord 行内含词表任一词条(小写包含匹配;词表为词干,覆盖屈折形态)
func containsAnyWord(line string, words []string) bool {
	low := strings.ToLower(line)
	for _, w := range words {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// ---- 模块三:权威挂载清单注入 ----

// manjuAttachmentManifestKey 权威清单幂等锚
const manjuAttachmentManifestKey = "REFERENCE ATTACHMENT ORDER (authoritative)"

// injectAttachmentManifest 在 subject_definitions 段首注入权威挂载清单(独立可测,
// 幂等)。官方输入流里模型逐槽看到 "Picture N: <像素>",本清单以确定性事实陈述
// 直接对冲 LLM 编号错位;同时声明场景槽是环境参考而非人脸(吸收 scene-only
// REFERENCE NOTE 语义)。清单用角色中文 id——挂载图即该角色定妆照,id 是唯一稳定锚,
// Qwen 系编码器对中英混合无理解障碍。
func injectAttachmentManifest(hp string, c manjuRefContract) string {
	if c.PicSlots == 0 || strings.Contains(hp, manjuAttachmentManifestKey) {
		return hp
	}
	var parts []string
	for _, cs := range c.Chars {
		if cs.PicEnd < cs.PicStart {
			continue
		}
		if cs.PicStart == cs.PicEnd {
			parts = append(parts, fmt.Sprintf("<Picture %d> is the character %s", cs.PicStart, cs.ID))
		} else {
			parts = append(parts, fmt.Sprintf("<Picture %d-%d> are the character %s (multiple views of the same person)", cs.PicStart, cs.PicEnd, cs.ID))
		}
	}
	if c.SceneSlot > 0 {
		parts = append(parts, fmt.Sprintf("<Picture %d> is the scene/environment reference, NOT a person - never copy a face from it onto any character", c.SceneSlot))
	}
	if len(parts) == 0 {
		return hp
	}
	manifest := manjuAttachmentManifestKey + ": " + strings.Join(parts, "; ") +
		". Use these exact tags with these exact meanings in every section below."
	idx := strings.Index(hp, "subject_definitions:")
	if idx < 0 {
		return hp
	}
	after := idx + len("subject_definitions:")
	trim := strings.TrimLeft(hp[after:], " \t\r\n")
	insertAt := len(hp) - len(trim)
	return hp[:insertAt] + manifest + "\n" + trim
}

// ---- 模块四:说话者镜内重编 ----

// reSpeakerID 说话者标记(含复合形态 (S1,S2))
var reSpeakerID = regexp.MustCompile(`\(S(\d+)(?:,S(\d+))*\)`)

// alignSpeakerIDs 把全片坐标的说话者 ID 重编为镜内连续编号(独立可测):在画面段
// (detailed_description / integrated_multimodal_description)按首次发声顺序收集
// (Sx) 重编为 S1..Sk,全文(含 subject_definitions 的 Audio 行)同步替换。
// 每镜独立编码渲染,镜内首名发声者应为 S1;跳号/沿用全片序号(EP01 实锤 S6/S11)
// 会在模型眼中制造"未定义的说话者",音色随机分配。
// alignSpeakerIDs 画面段说话者 (Sx) 镜内重编:按首次发声顺序连续分配 S1..Sk
// (官方 ref-en §5.4 镜内序)。无全局注册表时即此行为(无音色绑定角色的兜底)。
func alignSpeakerIDs(hp string) string {
	return alignSpeakerIDsReg(hp, manjuRefContract{}, nil, "")
}

// alignSpeakerIDsReg 带全局说话人注册表的版本(2026-08-30 ver14,官方契约
// "A speaker keeps the same ID across shots"):画面段 (Sx) 重写为已注册角色的
// 全局号(跨镜稳定,修复逐镜重排导致的声线漂移——与 Audio 定义行同号,防音色
// 错位);未注册角色(无音色绑定/旁白)保持镜内序。说话者→角色绑定两条确定路径:
// ①画面段 <Subject N> (Sx) 组合(LLM 直出 Subject 标签)②(Sx) says 锚点后的
// <d> 内容与 dialogue 台词逐字匹配(台词逐字保留契约,确定性来源)。
// 幂等(二次调用原始号即全局号)。
func alignSpeakerIDsReg(hp string, c manjuRefContract, reg map[string]string, dialogue string) string {
	if !strings.Contains(hp, "(S") {
		return hp
	}
	bodyStart := bodyRange(hp)
	if bodyStart < 0 {
		return hp
	}
	bodyEnd := len(hp)
	if i := strings.Index(hp[bodyStart:], "\noverall_soundscape:"); i >= 0 {
		bodyEnd = bodyStart + i
	}
	// 画面段 (Sx) → 全局号(全局化优先于镜内重编)
	fixed := map[string]string{}
	if len(reg) > 0 {
		if len(c.Chars) > 0 {
			for _, m := range reSubjectSpeakerRef.FindAllStringSubmatch(hp[bodyStart:bodyEnd], -1) {
				no := mustAtoi(m[1])
				if no >= 1 && no <= len(c.Chars) {
					if gsx, ok := reg[c.Chars[no-1].ID]; ok {
						fixed["S"+m[2]] = gsx
					}
				}
			}
		}
		if dialogue != "" {
			for _, m := range reSxSaysD.FindAllStringSubmatch(hp[bodyStart:bodyEnd], -1) {
				sx := m[1]
				if sx == "" {
					sx = m[2]
				}
				cid := dialogueSpeakerForContent(dialogue, c, m[3])
				if cid != "" && sx != "" {
					if gsx, ok := reg[cid]; ok {
						fixed["S"+sx] = gsx
					}
				}
			}
		}
	}
	remap := map[string]string{}
	order := 0
	for _, m := range reSpeakerID.FindAllStringSubmatch(hp[bodyStart:bodyEnd], -1) {
		for _, g := range m[1:] {
			if g == "" {
				continue
			}
			key := "S" + g
			if _, ok := remap[key]; ok {
				continue
			}
			if gsx, ok := fixed[key]; ok {
				remap[key] = gsx
				continue
			}
			order++
			remap[key] = "S" + strconv.Itoa(order)
		}
	}
	if len(remap) == 0 {
		return hp
	}
	return reSpeakerID.ReplaceAllStringFunc(hp, func(m string) string {
		subs := reSpeakerID.FindStringSubmatch(m)
		var ids []string
		unchanged := true
		for _, g := range subs[1:] {
			if g == "" {
				continue
			}
			nv, ok := remap["S"+g]
			if !ok {
				ids = append(ids, "S"+g) // 画面段外才出现的 Sx(如幽灵 Audio 行的绑定):保留原值
				continue
			}
			unchanged = false
			ids = append(ids, nv)
		}
		if unchanged {
			return m // 全部未映射:原样返回,避免空括号
		}
		return "(" + strings.Join(ids, ",") + ")"
	})
}

// reSxSaysD 画面段说话锚点:(Sx) 在 says 前(官方 <Subject N> (Sx) says: 句式)
// 或后(LLM 直出常见 "says with a mumbling mouth (S6)"),同句内取 <d> 原文;
// <d> 语言标签整体可选(裸 <d> 中文也匹配,标签由 alignDialogueLangTags 补)
var reSxSaysD = regexp.MustCompile(`(?:\(S(\d+)\)[^\n]{0,200}?says|says[^\n]{0,200}?\(S(\d+)\))[^\n]{0,120}?<d>(?:\[(?:Chinese|English|中文)\]?)?\s*([^<]+?)</d>`)

// normPromptText 提示词文本归一(去空白与常见标点;台词逐字匹配用)
var reNormPromptPunct = regexp.MustCompile(`[\s，。！？；：、—…·“”‘’"'()\[\]（）｛｝]+`)

func normPromptText(s string) string {
	return reNormPromptPunct.ReplaceAllString(s, "")
}

// dialogueSpeakerForContent 台词内容 → 说话者角色 id(dialogue 行「角色:台词」
// 逐字归一匹配;与 dialogueSpeakerIDs 的 match 归一逻辑同源,抽取共用)。
func dialogueSpeakerForContent(dialogue string, c manjuRefContract, content string) string {
	dn := normPromptText(content)
	if dn == "" {
		return ""
	}
	for _, line := range strings.Split(dialogue, "\n") {
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		ln := normPromptText(line[i+1:])
		if ln == "" {
			continue
		}
		if dn == ln || strings.Contains(dn, ln) || strings.Contains(ln, dn) {
			return charIDMatch(strings.TrimSpace(line[:i]), c)
		}
	}
	return ""
}

// charIDMatch 说话人名 → 契约角色 id(直配或「前缀·角色名」剥离;2026-08-30
// ver14 从 dialogueSpeakerIDs 的私有闭包抽取,注册表/台词匹配共用)。
func charIDMatch(name string, c manjuRefContract) string {
	name = strings.TrimSpace(name)
	for _, cs := range c.Chars {
		if cs.ID == name {
			return cs.ID
		}
	}
	// 2026-08-30 五问整改(问题②兜底):剥括号标注(「挑战者程野(剪影)」→「程野」)
	// 再匹配——括号说话人此前匹配不上,进不了 dialogueSpeakerIDs 与 Sx 注册表,
	// 跨镜声线漂移/画外判定失效。身份词+名字连写(「挑战者程野」)无分隔符,
	// 用后缀匹配兜底(名字≥2字,身份词在前)
	for _, cand := range speakerNameCandidates(name) {
		if cand == name {
			continue
		}
		for _, cs := range c.Chars {
			if cs.ID == cand || (len([]rune(cs.ID)) >= 2 && strings.HasSuffix(cand, cs.ID)) {
				return cs.ID
			}
		}
	}
	if i := strings.LastIndex(name, "·"); i >= 0 {
		name = strings.TrimSpace(name[i+len("·"):])
		for _, cs := range c.Chars {
			if cs.ID == name {
				return cs.ID
			}
		}
	}
	return ""
}

// speakerNameCandidates 说话人名字的归一候选(2026-08-30 五问整改):
// 「挑战者程野(剪影)」→ [挑战者程野(剪影), 挑战者程野, 程野]——括号标注是
// 分镜表里常见的画面限定写法,不是角色名的一部分。
func speakerNameCandidates(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := []string{name}
	for _, c := range []string{"（", "(", "【", "["} {
		close := map[string]string{"（": "）", "(": ")", "【": "】", "[": "]"}[c]
		if i := strings.Index(name, c); i > 0 {
			before := strings.TrimSpace(name[:i])
			out = append(out, before)
			if j := strings.Index(name, close); j > i {
				inner := strings.TrimSpace(name[i+1 : j])
				if inner != "" {
					out = append(out, inner)
				}
			}
			break
		}
	}
	return out
}

// manjuResolveCharID 角色名变体归一(2026-09-01 主次混乱/无参考图根因根治):
// 分镜 characters 声明/说话人常用简称/带前缀名,素材卡是完整名——精确匹配不上
// 就无参考图,H3 自由发挥(实测:杳杳×108 对不上卡「涂山杳杳」、主持人×6 对不上
// 「天才榜主持人」,杂毛/影子书主配角形象漂移)。归一顺序:
//  ①精确;②剥「·」/括号标注后精确;③卡 ID 以候选结尾(涂山杳杳⊃杳杳);
//  ④候选以卡 ID 结尾(挑战者程野⊃程野);⑤卡 ID 包含候选(天才榜主持人⊃主持人)
// 同一变体命中多卡时取最短卡 ID(最专一);候选/卡 ID 长度 <2 不参与变体匹配
// (防单字误配);旁白/画外/内心等非角色词直接不匹配。
func manjuResolveCharID(name string, charIDs []string) string {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) < 2 {
		return ""
	}
	for _, p := range []string{"旁白", "画外", "内心", "群杂"} {
		if strings.HasPrefix(name, p) {
			return ""
		}
	}
	for _, cs := range charIDs {
		if cs == name {
			return cs
		}
	}
	base := name
	if i := strings.LastIndex(base, "·"); i >= 0 {
		base = strings.TrimSpace(base[i+1:])
	}
	if base != name {
		for _, cs := range charIDs {
			if cs == base {
				return cs
			}
		}
	}
	best, bestLen := "", 0
	for _, cand := range speakerNameCandidates(name) {
		if len([]rune(cand)) < 2 {
			continue
		}
		for _, cs := range charIDs {
			n := len([]rune(cs))
			if n < 2 {
				continue
			}
			hit := false
			if n >= len([]rune(cand)) {
				hit = strings.HasSuffix(cs, cand) || strings.Contains(cs, cand)
			} else {
				hit = strings.HasSuffix(cand, cs)
			}
			if hit && (best == "" || n < bestLen) {
				best, bestLen = cs, n
			}
		}
	}
	return best
}

// bodyRange 画面段起点
func bodyRange(hp string) int {
	if i := strings.Index(hp, "detailed_description:"); i >= 0 {
		return i
	}
	if i := strings.Index(hp, "integrated_multimodal_description:"); i >= 0 {
		return i
	}
	return -1
}

// ---- 模块五:Audio 定义规范化 ----

var (
	reAudioDefLineAlign = regexp.MustCompile(`(?m)^<Audio (\d+)> is the voice-timbre reference for (.+?), containing a spoken voiceover\.?\r?$`)
	reAudioBindSubject  = regexp.MustCompile(`for (?:the voice of )?<Subject (\d+)>`)
	reSubjectSpeakerRef = regexp.MustCompile(`<Subject (\d+)> \(S(\d+)\)`)
	reAudioLineSx       = regexp.MustCompile(`\(S(\d+)`)
	reAudioRefPhrase    = regexp.MustCompile(`with voice timbre referencing <Audio (\d+)>`)
)

// alignAudioDefs 规范化 <Audio N> 定义(独立可测):
//   ① 绑定目标重写为官方格式 for <Subject M> (Sx)(官方 ref-en §2.4;绑中文名/
//     拿 Audio 序号当 Sx 是无效绑定,模型无从关联音色与说话者);
//   ② 编号压缩为 1..k 连续(登场角色按登场序在前,画外音接续)——挂载侧
//     charVoiceNames 按定义行序挂载,编号洞会错位指认音频;
//   ③ 行内 Sx 不可信(LLM 乱编,EP01 实锤绑 S2 实际发 S6),按优先级重定:
//     dialogue 发声角色序(确定性)> 画面段 <Subject M> (Sx) 组合 > 行内原值;
//     dialogue 中不发声的角色=定义冗余删除;
//   ④ 幽灵定义(无法归属实际音色)整行删除;正文 "with voice timbre referencing
//     <Audio N>" 的幽灵引用剥除。
func alignAudioDefs(hp string, c manjuRefContract, dialogue string) string {
	return alignAudioDefsReg(hp, c, dialogue, nil)
}

// alignAudioDefsReg 带全局说话人注册表的版本(2026-08-30 ver14):已注册角色的
// Audio 定义行 Sx 用全局号(与画面段 alignSpeakerIDsReg 一致,跨镜稳定);
// reg=nil 时与 alignAudioDefs 完全一致。
func alignAudioDefsReg(hp string, c manjuRefContract, dialogue string, reg map[string]string) string {
	if !strings.Contains(hp, "<Audio ") || len(c.VoiceRoster) == 0 {
		return hp
	}
	lines := strings.Split(hp, "\n")
	type audioDef struct {
		lineNo int
		target string // 角色 id 或 "@offscreen:"+声线描述
	}
	allAudio := map[int]bool{} // 全部 <Audio> 定义行(未被收录的=幽灵,删除)
	var defs []audioDef
	for i, line := range lines {
		m := reAudioDefLineAlign.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		allAudio[i] = true
		cid, offDesc := manjuAudioDefTarget(m[2], c)
		if cid == "" && offDesc == "" {
			continue // 幽灵定义(无法归属):不收录,循环里删除
		}
		target := cid
		if offDesc != "" {
			target = "@offscreen:" + offDesc
		}
		defs = append(defs, audioDef{lineNo: i, target: target})
	}
	if len(defs) == 0 {
		return hp
	}
	// 登场角色(Roster 序)在前,画外音按原行序接续
	sort.SliceStable(defs, func(a, b int) bool {
		ra, offa := audioRank(defs[a].target, c)
		rb, offb := audioRank(defs[b].target, c)
		if offa != offb {
			return !offa
		}
		return ra < rb
	})
	// 角色→实际说话 Sx:dialogue 发声角色序优先(确定性),全局注册表覆盖(跨镜稳定),
	// 画面段组合兜底
	cidSx := dialogueSpeakerIDs(dialogue, c)
	for cid, sx := range reg {
		if _, ok := cidSx[cid]; ok {
			cidSx[cid] = sx
		}
	}
	if bs := bodyRange(hp); bs >= 0 {
		for _, m := range reSubjectSpeakerRef.FindAllStringSubmatch(hp[bs:], -1) {
			k := mustAtoi(m[1])
			if k < 1 || k > len(c.Chars) {
				continue
			}
			if _, ok := cidSx[c.Chars[k-1].ID]; !ok {
				cidSx[c.Chars[k-1].ID] = m[2]
			}
		}
	}
	subjOf := func(cid string) int {
		for i, cs := range c.Chars {
			if cs.ID == cid {
				return i + 1
			}
		}
		return 0
	}
	newNo := 0
	drop := map[int]bool{}
	rewrite := map[int]string{}
	for _, d := range defs {
		if strings.HasPrefix(d.target, "@offscreen:") {
			newNo++
			// 2026-09-02 幂等锚保留(王牌三岁半 EP01 镜15 实锤):@offscreen 重写
			// 必须保留 "the off-screen voice described as" 前缀——injectOffscreen
			// VoiceBindings 的幂等锚就是该前缀,剥掉后同一镜每次 finalize 都会
			// 重新注入并叠加(plan 镜15 三行畸形 Audio 定义实锤:desc 还嵌了台词
			// <d> 块 → H3 把同一句念两遍)。
			rewrite[d.lineNo] = fmt.Sprintf(
				"<Audio %d> is the voice-timbre reference for the off-screen voice described as %s, containing a spoken voiceover.",
				newNo, strings.TrimPrefix(d.target, "@offscreen:"))
			continue
		}
		sub := subjOf(d.target)
		sx := cidSx[d.target] // dialogue 发声序(优先)/画面段组合(兜底)
		if sub == 0 || sx == "" {
			drop[d.lineNo] = true // 不在登场名单或本镜不发声:定义冗余
			continue
		}
		newNo++
		rewrite[d.lineNo] = fmt.Sprintf(
			"<Audio %d> is the voice-timbre reference for <Subject %d> (S%s), containing a spoken voiceover.",
			newNo, sub, sx)
	}
	if len(rewrite) == 0 && len(drop) == 0 && len(allAudio) == 0 {
		return hp
	}
	var out []string
	for i, line := range lines {
		if drop[i] || (allAudio[i] && rewrite[i] == "") {
			continue // 冗余定义与幽灵定义统一删除
		}
		if r, ok := rewrite[i]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, line)
	}
	merged := strings.Join(out, "\n")
	// 重写后定义行号已连续 1..k;正文引用号超出集合=幽灵引用,剥短语保主干
	defSet := map[int]bool{}
	for _, d := range reAudioDefLineAlign.FindAllStringSubmatch(merged, -1) {
		defSet[mustAtoi(d[1])] = true
	}
	merged = reAudioRefPhrase.ReplaceAllStringFunc(merged, func(m string) string {
		if defSet[mustAtoi(reAudioRefPhrase.FindStringSubmatch(m)[1])] {
			return m
		}
		return ""
	})
	// 剥除后残留双空格/句前空格清理
	merged = regexp.MustCompile(`  +`).ReplaceAllString(merged, " ")
	return merged
}

// audioRank 登场角色在音色清单中的序;画外音返回 (0,true)
func audioRank(target string, c manjuRefContract) (int, bool) {
	if strings.HasPrefix(target, "@offscreen:") {
		return 0, true
	}
	for i, id := range c.VoiceRoster {
		if id == target {
			return i, false
		}
	}
	return len(c.VoiceRoster), false
}

// dialogueSpeakerIDs 从分镜台词原文("角色:台词"逐行)提取发声角色顺序(独立可测):
// 返回 角色 id → Sx(按台词中首次开口顺序 S1..,与 alignSpeakerIDs 的画面段首现
// 顺序天然一致)。角色名兼容「群演·老赵」前缀形态(取 · 后段匹配登场名单);
// 不在登场名单的名字跳过(旁白/画外音不属于登场角色音色绑定)。
func dialogueSpeakerIDs(dialogue string, c manjuRefContract) map[string]string {
	if dialogue == "" {
		// 2026-09-01 崩溃修复:返回 nil 时调用方「画面段组合兜底」对 nil map
		// 赋值 → panic「assignment to entry in nil map」(空镜 h3 含 <Subject N>
		// (Sx) 引用时触发,轮回欠费九世 EP01 渲染崩溃)。空镜无发声者,空 map 即可。
		return map[string]string{}
	}
	out := map[string]string{}
	order := 0
	for _, line := range strings.Split(dialogue, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		cid := charIDMatch(line[:i], c)
		if cid == "" {
			continue
		}
		if _, ok := out[cid]; ok {
			continue // 同角色只记首次开口
		}
		order++
		out[cid] = strconv.Itoa(order)
	}
	return out
}

// manjuAudioDefTarget 解析 <Audio> 定义行绑定目标(挂载侧 charVoiceNames 与对齐侧
// alignAudioDefs 共用的单一解析逻辑):返回角色 id(空=非登场角色)或画外音声线描述
// (非空=画外音)。兼容两种形态:官方对齐后 "for <Subject M> (Sx)" 与历史中文名
// "for the voice of 中文名 (Sx)"。
func manjuAudioDefTarget(who string, c manjuRefContract) (cid, offscreenDesc string) {
	// 2026-08-30 五问整改:兼容「for 」前缀缺失形态——reAudioDefLine 捕获组从
	// "reference for " 之后开始,官方对齐形态「<Subject 1> (S1)」无 for 前缀,
	// 此前解析恒空(挂载侧靠回退角色顺序碰巧对上,画外定义截断错位)
	if !strings.HasPrefix(strings.TrimSpace(who), "for ") {
		who = "for " + strings.TrimSpace(who)
	}
	if i := strings.Index(who, "the off-screen voice"); i >= 0 {
		desc := strings.TrimPrefix(who[i:], "the off-screen voice described as ")
		if j := strings.Index(desc, " (S"); j >= 0 {
			desc = desc[:j]
		}
		return "", strings.TrimSpace(desc)
	}
	if sm := reAudioBindSubject.FindStringSubmatch(who); sm != nil {
		if n := mustAtoi(sm[1]); n >= 1 && n <= len(c.Chars) {
			return c.Chars[n-1].ID, ""
		}
		return "", ""
	}
	if i := strings.Index(who, "the voice of "); i >= 0 {
		name := who[i+len("the voice of "):]
		if j := strings.Index(name, " (S"); j >= 0 {
			name = name[:j]
		}
		name = strings.TrimSpace(name)
		for _, cs := range c.Chars {
			if cs.ID == name {
				return name, ""
			}
		}
	}
	return "", ""
}

// ---- 模块六:<d> 语言标签规范化 ----

var (
	reDialogueTagged = regexp.MustCompile(`(<d>)\s*\[\s*([^<>\[\]]*?)\s*\]\s*`)
)

// alignDialogueLangTags 语言标签规范化(独立可测):
//   ① <d>[中文]X</d> / <d>[chinese]X</d> → <d>[Chinese]X</d>(官方标签为英文写法;
//     历史上中文词标签被逐字念出的根因是标签语种错,不是标签不该存在);
//   ② 裸 <d>中文…</d>(无标签且内容以汉字开头)→ <d>[Chinese]中文…</d>
//     (官方 base-en §4.4:d 标签内只放语言标签+原话;裸文本让模型猜配音语言)。
// 非中文标签([English]/[unclear] 等)原样保留;幂等。
func alignDialogueLangTags(hp string) string {
	if !strings.Contains(hp, "<d>") {
		return hp
	}
	hp = reDialogueTagged.ReplaceAllStringFunc(hp, func(m string) string {
		sub := reDialogueTagged.FindStringSubmatch(m)
		switch strings.ToLower(sub[2]) {
		case "中文", "chinese", "mandarin":
			return sub[1] + "[Chinese] "
		}
		return m
	})
	return tagBareChinese(hp)
}

// tagBareChinese 裸 <d> 且首个非空白字符是汉字 → 紧贴开标签补 [Chinese]+空格
// (与官方示例形态 <d>[English] text</d> 一致,与已带标签路径的空格形态统一)
func tagBareChinese(hp string) string {
	var b strings.Builder
	rest := hp
	for {
		i := strings.Index(rest, "<d>")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+3])
		rest = rest[i+3:]
		k := 0
		for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t' || rest[k] == '\n' || rest[k] == '\r') {
			k++
		}
		if k < len(rest) && rest[k] != '[' {
			r := []rune(rest[k:])[0]
			if unicode.Is(unicode.Han, r) {
				b.WriteString("[Chinese] ")
			}
		}
	}
}

// ---- 模块七:切点时码 clip-local 化 ----

var reShotCut = regexp.MustCompile(`\[Shot (\d+)\](?:\s+At\s+(\d{2}):(\d{2})\.(\d{3}))?`)

// ---- 模块八:契约校验(LLM 直出自修通道) ----

// validatePromptContract 直出提示词的契约校验(独立可测):genShotPrompts 校验未过会
// 携带问题清单让 LLM 重写(genShotPromptWithFix),把契约错位消灭在 plan 期——渲染端
// manjuAlignShotPrompt 是机器兜底,本校验是源头自修,两层各司其职。
// 校验项:①Picture 引用超预期槽位(挂载顺序编号上限);②说话者编号跳号
// (镜内应从 S1 连续);③切点时码越界(≥本镜时长,全片时间轴残留);④台词缺
// 官方语言标签(<d> 后直接是中文)。
func validatePromptContract(hp string, s manjuShot, expectSlots int) []string {
	var problems []string
	// ①Picture 超界
	for _, m := range rePicInLine.FindAllStringSubmatch(hp, -1) {
		if n := mustAtoi(m[1]); n > expectSlots {
			problems = append(problems, fmt.Sprintf(
				"<Picture %d> 超出参考图槽位数 %d(挂载顺序编号上限;参考 ref_available 清单逐一对齐,场景图=清单最后一项)", n, expectSlots))
			break
		}
	}
	// ②Sx 跳号(画面段)
	if bs := bodyRange(hp); bs >= 0 {
		be := len(hp)
		if i := strings.Index(hp[bs:], "\noverall_soundscape:"); i >= 0 {
			be = bs + i
		}
		seen := map[int]bool{}
		maxS := 0
		for _, m := range reSpeakerID.FindAllStringSubmatch(hp[bs:be], -1) {
			for _, g := range m[1:] {
				if g == "" {
					continue
				}
				n := mustAtoi(g)
				seen[n] = true
				if n > maxS {
					maxS = n
				}
			}
		}
		if maxS > len(seen) {
			problems = append(problems, fmt.Sprintf(
				"说话者编号跳号:最大 (S%d) 但镜内只有 %d 个说话者——本镜独立渲染,按发声顺序从 S1 连续分配", maxS, len(seen)))
		}
	}
	// ③时码越界(全片轴残留)
	for _, m := range reShotCut.FindAllStringSubmatch(hp, -1) {
		if m[2] == "" {
			continue
		}
		t := float64(mustAtoi(m[2])*60+mustAtoi(m[3])) + float64(mustAtoi(m[4]))/1000
		if s.Duration > 0 && t >= float64(s.Duration)+0.5 {
			problems = append(problems, fmt.Sprintf(
				"切点时码 %02s:%02s.%03s 超出本镜时长 %d 秒——时码用本镜时间轴(从 0 起),严禁全片累计时间", m[2], m[3], m[4], s.Duration))
			break
		}
	}
	// ④台词缺语言标签(仅当有 <d> 且存在裸中文开标签)
	if strings.Contains(hp, "<d>") {
		for _, seg := range strings.Split(hp, "<d>") {
			if seg == "" {
				continue
			}
			k := 0
			for k < len(seg) && (seg[k] == ' ' || seg[k] == '\t' || seg[k] == '\n' || seg[k] == '\r') {
				k++
			}
			if k < len(seg) && seg[k] != '[' {
				r := []rune(seg[k:])[0]
				if unicode.Is(unicode.Han, r) {
					problems = append(problems, "台词 <d> 缺官方语言标签:中文台词写 <d>[Chinese] 台词原文</d>(标签词用英文 Chinese)")
					break
				}
			}
		}
	}
	// ⑤内心戏镜 Q 版主体(2026-08-30 ver14,问题⑤ Q 版动作适配):narration 含
	// 「内心·」的内心戏镜,画面主体应为该角色 Q 版形象并在 subject_definitions
	// 单独定义(chibi/miniature)——LLM 直出缺 Q 版主体=Q 版参考图挂上也无从演绎。
	// 反派/配角的写实+画外音内心戏不带 chibi 词,此校验只对生成路径生效(脚本
	// 逐字保留的存量镜不经过 validateShotPrompt),误报代价=一次修复重写,可接受。
	if strings.Contains(s.Narration, "内心·") {
		low := strings.ToLower(hp)
		if !strings.Contains(low, "chibi") && !strings.Contains(low, "miniature") && !strings.Contains(low, "q-version") && !strings.Contains(low, "q version") {
			problems = append(problems, "内心戏镜缺 Q 版主体:内心独白(内心·角色名)镜的画面主体应为该角色 Q 版形象(chibi miniature),subject_definitions 单独定义 Subject 并引用其 Q 版参考图;Q 版动作须具象演绎内心内容(数数→掰指头/思考→托腮等)")
		}
	}
	return problems
}

// alignShotTimecodes 全片时间轴 → 本镜时间轴(独立可测):
//   ① 首个带时码的 Shot 段若 t0>0,全部时码平移 -t0(每镜独立渲染时轴从 0 起;
//     EP01 实锤:5 秒镜写 At 00:15.000 即全片轴残留);
//   ② 平移后越界(≥镜长+0.5s 容差)的时码剥除,保留切镜描述句;
//   ③ Shot 编号按出现顺序重编为 1..m(镜内自成序列;官方:首段无时码)。
func alignShotTimecodes(hp string, durationSec int) string {
	if !strings.Contains(hp, "[Shot ") {
		return hp
	}
	// 2026-09-03 retention 段保护:retention_analysis 里的 [Shot N] 是「该角色曾出现
	// 在全片镜 N」的跨镜引用(如 "(appears in [Shot 1])"),不是本镜切点——旧实现
	// 全文统一重编号会把它计入序号,把 detailed_description 的首段顶成 [Shot 2]/[Shot 3],
	// 排查对账时镜号对不上(递了三千年 EP01 单段镜实锤)。切段保护:段外重编号,段内原样。
	keep := ""
	if i := strings.Index(hp, "retention_analysis:"); i >= 0 {
		end := len(hp)
		if j := strings.Index(hp[i:], "\ndetailed_description:"); j >= 0 {
			end = i + j
		}
		keep = hp[i:end]
		hp = hp[:i] + "\x00RETENTION\x00" + hp[end:]
	}
	marks := reShotCut.FindAllStringSubmatch(hp, -1)
	if len(marks) == 0 {
		return strings.ReplaceAll(hp, "\x00RETENTION\x00", keep)
	}
	offset := 0.0
	// 全片轴残留特征=首个 Shot 段就带时码(官方:镜内首段无时码);后续段带码属正常
	// 镜内切点,不平移——用首个带码段做 offset 会在二次对齐时把已平移的切点再吞一次
	if marks[0][2] != "" {
		t0 := float64(mustAtoi(marks[0][2])*60+mustAtoi(marks[0][3])) + float64(mustAtoi(marks[0][4]))/1000
		if t0 > 0.05 {
			offset = t0
		}
	}
	shotNo := 0
	dur := float64(durationSec) + 0.5
	out := reShotCut.ReplaceAllStringFunc(hp, func(m string) string {
		sub := reShotCut.FindStringSubmatch(m)
		shotNo++
		if sub[2] == "" {
			return fmt.Sprintf("[Shot %d]", shotNo)
		}
		t := float64(mustAtoi(sub[2])*60+mustAtoi(sub[3])) + float64(mustAtoi(sub[4]))/1000 - offset
		if t < 0 {
			t = 0
		}
		if t < 0.05 {
			// 平移到 0 点=本镜首段:官方"首段无时码",剥 At 标记
			return fmt.Sprintf("[Shot %d]", shotNo)
		}
		if durationSec > 0 && t >= dur {
			return fmt.Sprintf("[Shot %d]", shotNo) // 越界:剥时码保切镜句
		}
		total := int(t * 1000)
		return fmt.Sprintf("[Shot %d] At %02d:%02d.%03d", shotNo, total/60000, (total/1000)%60, total%1000)
	})
	return strings.ReplaceAll(out, "\x00RETENTION\x00", keep)
}

// reBareSays 裸 (Sx) says: 形态(捕获组 1 为空=画外说话者;<Subject N> 前缀=画面角色)。
// 兼容 (S1) says / (S1), says / (S1) says: 画外句逗号变体(EP01 镜5 实锤形态)。
// 对齐层把画面发声角色统一写成 <Subject N> (Sx) says 形态(alignSpeakerIDsReg),
// 不带 Subject 前缀的裸 (Sx) says: 即画外说话者——渲染端凭此判定口径。
var reBareSays = regexp.MustCompile(`(<Subject \d+>\s+)?(\(S\d+\)[,，\s]*says:)`)

// markOffscreenSays 画外说话句机械 off-screen 标注(2026-08-30 五问整改,问题②
// 「乱对角色嘴型」执行层):裸 (Sx) says: 若未写 off-screen voiceover 标记,H3 会把
// 台词安给画面角色动嘴(EP01 镜5/11 程野不在场却普通 says 句式实锤)。机械改写:
// ① (Sx) says: → (Sx) says in an off-screen voiceover:(幂等:改写后形态不再命中
//   reBareSays;已含 off-screen 的句不动);
// ② 该句最后 </d> 后补 while the on-screen characters' lips remain completely closed
//   (句内已含 lips-closed 则跳过,幂等)。
// 纯函数,在 finalizeAlignedPrompt 统一汇点调用——指纹/预编码/渲染三侧一致。
func markOffscreenSays(hp string) string {
	if !strings.Contains(hp, "says:") {
		return hp
	}
	hp = reBareSays.ReplaceAllStringFunc(hp, func(m string) string {
		sm := reBareSays.FindStringSubmatch(m)
		if sm[1] != "" {
			return m // <Subject N> (Sx) says: 画面角色,不动
		}
		return strings.Replace(m, "says:", "says in an off-screen voiceover:", 1)
	})
	// ② 补 lips-closed 从句(按锚点顺序处理,插入长度累加偏移)
	if !strings.Contains(hp, "off-screen voiceover") {
		return hp
	}
	clause := " while the on-screen characters' lips remain completely closed."
	out := hp
	offset := 0
	for _, a := range reOffscreenAnchor.FindAllStringIndex(hp, -1) {
		segStart := a[1] + offset
		segEnd := len(out)
		if nl := strings.Index(out[segStart:], "\n"); nl >= 0 {
			segEnd = segStart + nl
		}
		seg := out[segStart:segEnd]
		if strings.Contains(seg, "lips remain completely closed") || !strings.Contains(seg, "</d>") {
			continue
		}
		ins := segStart + strings.LastIndex(seg, "</d>") + len("</d>")
		out = out[:ins] + clause + out[ins:]
		offset += len(clause)
	}
	return out
}


// fixShotPromptContent 分镜脚本内容级兜底(2026-09-02,我的影子会咬人渲染异常实锤):
// 技能侧 LLM 直出的 h3_prompt 存在脚本内容缺陷,渲染端逐字保留导致渲染异常,
// 这里机械修正(纯函数,指纹/渲染共用,幂等):
//   ①chibi/miniature Subject 空引用清理:LLM 自编号 "in <Picture 3>" 经
//     alignPictureRefs 按角色挂载槽重排后变 "in ;" 悬空 → H3 拿到无参考图
//     Subject=Q版乱入/形象飘逸(镜12 非内心戏镜 chibi Aying 乱入实锤)。
//     内心戏镜(对话列含 内心·)保留 Q 版 Subject 并清悬空引用;非内心戏镜
//     整行删除(画面不需要 Q 版,删掉防乱入)。
//   ②画外·说话人台词强制 off-screen:台词列 (S3)画外·路人 在 detailed_description
//     被 LLM 写成 "The spectator shouts: <d>..."(画面角色开口)→ H3 让画面人物
//     动嘴(镜6 群众配音主角动嘴实锤)。凡对话列 画外· 前缀的说话句,其 <d> 在
//     detailed_description 中若以普通 says/shouts/speaks 引导(非 <Subject N>),
//     改写为 off-screen voiceover 句式 + lips closed。
func fixShotPromptContent(hp string, c manjuRefContract, dialogue string) string {
	if hp == "" {
		return hp
	}
	hp = fixChibiEmptyRefs(hp, dialogue)
	hp = fixOffscreenDialogueSays(hp, dialogue)
	return hp
}

// fixChibiEmptyRefs 清理 chibi Subject 空引用(2026-09-02):
// 行内含 chibi/miniature 且 "in ;" / "in ," 悬空引用(对齐层剥除后残留)→
// 内心戏镜保留(清悬空引用保句,形态/动作自述仍在),非内心戏镜删行防乱入。
func fixChibiEmptyRefs(hp string, dialogue string) string {
	if !strings.Contains(hp, "chibi") && !strings.Contains(hp, "miniature") {
		return hp
	}
	innerShot := strings.Contains(dialogue, "内心·")
	var out []string
	changed := false
	for _, line := range strings.Split(hp, "\n") {
		low := strings.ToLower(line)
		isChibi := strings.Contains(low, "chibi") || strings.Contains(low, "miniature")
		emptyRef := strings.Contains(line, "in ;") || strings.Contains(line, "in ,") ||
			strings.Contains(line, "in  ;")
		if !isChibi || !strings.Contains(line, "<Subject") || !strings.Contains(line, "<Audio") {
			out = append(out, line)
			continue
		}
		if !emptyRef {
			out = append(out, line)
			continue
		}
		if innerShot {
			// 内心戏镜:保留 Q 版主体但去掉悬空引用(防无图 Subject 乱画);
			// 引用清理后仍是有效 chibi 描述(形态/动作自述),H3 按文本演绎
			fixed := regexp.MustCompile(`in\s*[;,]+`).ReplaceAllString(line, "")
			fixed = regexp.MustCompile(`\s{2,}`).ReplaceAllString(fixed, " ")
			out = append(out, fixed)
			if fixed != line {
				changed = true
			}
		} else {
			// 非内心戏镜:Q 版 Subject 属乱入(镜12 实锤),整行删除
			changed = true
			continue
		}
	}
	if !changed {
		return hp
	}
	res := strings.Join(out, "\n")
	res = regexp.MustCompile(`\n{3,}`).ReplaceAllString(res, "\n\n")
	return strings.TrimSpace(res)
}

// fixOffscreenDialogueSays 画外说话句强制 off-screen(2026-09-02,镜6 实锤):
// 对话列 "画外·路人:台词" → detailed_description 若把该句写成普通开口句
// ("The spectator shouts: <d>...")→ 改写 "in an off-screen voiceover" +
// lips-closed。判定:对话列存在画外·前缀说话人,且详细描述中该 <d> 前
// 引导语含开口动词且无 <Subject N> 主语且无 off-screen 标记 → 机械标注。
func fixOffscreenDialogueSays(hp string, dialogue string) string {
	// 对话列是否有画外说话人
	hasOff := false
	for _, m := range reDialogue.FindAllStringSubmatch(dialogue, -1) {
		speaker := strings.TrimSpace(m[1])
		if speaker == "" {
			speaker = strings.TrimSpace(m[3])
		}
		if strings.HasPrefix(speaker, "画外") {
			hasOff = true
			break
		}
	}
	if !hasOff || !strings.Contains(hp, "<d>") {
		return hp
	}
	idx := strings.Index(hp, "detailed_description:")
	if idx < 0 {
		return hp
	}
	reD := regexp.MustCompile(`<d>(?:\[Chinese\]|\[中文\])?[^<]*</d>`)
	out := hp
	offset := 0
	prevDEnd := idx // 上个 </d> 之后(引导语窗口下界,防跨句误判)
	for _, m := range reD.FindAllStringIndex(hp, -1) {
		start := m[0] + offset
		end := m[1] + offset
		// 2026-09-02 崩溃修复(王牌三岁半实锤:slice bounds [1326:573]):
		// detailed_description 之前的 <d>(subject_definitions/summary 段残留)
		// 不参与画外标注——只处理画面段内的台词句。旧代码 segStart clamp 到
		// idx 后仍可能 > start(该 <d> 在 detailed_description 之前)→ 越界。
		if m[0] < idx {
			continue
		}
		// 引导语窗口:上界=上一句 </d> 之后(多句连续台词时,窗口跨过上一句
		// 会把已插入的 off-screen 标注误判为「已标注」而跳过本句——2026-09-02
		// 王牌三岁半/多句画外实锤,第二句永不标注);下界=detailed_description 段首。
		segStart := start - 140
		if segStart < prevDEnd {
			segStart = prevDEnd
		}
		if segStart < idx {
			segStart = idx
		}
		if segStart > start {
			continue // 防御:引导语窗口无效
		}
		lead := out[segStart:start]
		if i := strings.LastIndex(lead, ". "); i >= 0 {
			lead = lead[i+2:]
		}
		low := strings.ToLower(lead)
		if strings.Contains(low, "off-screen") || strings.Contains(low, "offscreen") ||
			strings.Contains(low, "<subject") {
			prevDEnd = end
			continue // 已标注画外 或 画面角色开口
		}
		// 引导语含开口动词 → 画外说话句(无主语=LLM 把画外台词安给了画面描述)
		verbIdx := -1
		verbLen := 0
		for _, v := range []string{"calls out", "announces", "shouts", "speaks", "yells", "says", "asks"} {
			if i := strings.LastIndex(low, v); i > verbIdx {
				verbIdx = i
				verbLen = len(v)
			}
		}
		if verbIdx < 0 {
			prevDEnd = end
			continue
		}
		// 动词在 lead 内起点 → 全局位置
		insAt := start - (len(lead) - verbIdx)
		if insAt < segStart || insAt > start {
			prevDEnd = end
			continue
		}
		insert := " in an off-screen voiceover"
		out = out[:insAt+verbLen] + insert + out[insAt+verbLen:]
		offset += len(insert)
		end += len(insert)
		// lips-closed 从句补在 </d> 后(边界保护:句尾时 end2 可能接近/等于 len)
		clause := " while the on-screen characters' lips remain completely closed"
		end2 := end
		probeEnd := end2 + len(clause) + 2
		if probeEnd > len(out) {
			probeEnd = len(out)
		}
		if !strings.Contains(out[end2:probeEnd], "lips remain") {
			out = out[:end2] + clause + out[end2:]
			offset += len(clause)
			end += len(clause)
		}
		prevDEnd = end
	}
	return out
}
