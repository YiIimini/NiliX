package api

// ---- 视频脚本直出 · 程序化解析(2026-08-24 用户要求:脚本直出不要再走 LLM 全量重出) ----
//
// 爽文技能(阶段6)产出的分镜脚本本身就是电影级分镜:分镜表 8 字段(镜号/景别/运镜/
// 画面内容/台词/光影/音效/时长)+ 每镜完整 H3 六段式提示词(站位/运镜/光影/音效/时间码
// 逐字写死)。此前 ensurePlan 把整份脚本丢给 LLM 全量重出方案 JSON——输出超长被截断、
// 且 LLM 重出的 h3_prompt 会丢掉脚本里精心设计的站位/运镜 → 渲染视频人物站位怪。
//
// 本解析器直接程序化提取:
//   - 分镜表行        → shots 元数据(shot_id/shot_size/camera/action/dialogue/narration/duration)
//   - ### Shot N 代码块 → 该镜 h3_prompt(六段式逐字保留,站位/运镜/光影/音效/时间码全在)
//   - 素材/人物生成提示词.md → characters 卡(英文 image_prompt 直接采用 + assetStyle)
//   - 素材/场景提示词.md    → scenes 卡(英文 image_prompt 直接采用)
//   - 脚本验收清单"五维差异化" → directing 五维
//
// 解析成功返回完整 plan;失败返回 error(调用方回退 LLM 直出,不阻断)。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// scriptShotRaw 分镜表一行解析出的镜头(未绑定角色/场景卡)
type scriptShotRaw struct {
	ID        int
	Scene     string // 从画面内容匹配到的场景卡 id(可为空,渲染兜底)
	ShotSize  string
	Camera    string
	Action    string
	Dialogue  string
	Narration string
	Light     string // 光影列(2026-08-30 ver14 保留:此前正则捕获后丢弃,LLM 重写路径失去光照信息)
	Sound     string // 音效列(同上)
	Style     string // 风格列(9 列格式,2026-08-30 ver14 保留:此前仅当时长兜底,从不入 shots)
	Duration  int
	H3Prompt  string
	// JSONChars JSON 分镜脚本的 shots[].characters 显式声明(2026-08-31 JSON 格式;
	// 组装登场角色时并入文本匹配结果,说话人仍强制入画)
	JSONChars []string
}

var (
	// 分镜表行:| 01 | 景别 | 运镜 | 画面内容 | 台词/旁白 | 光影 | 音效 | 时长 |(?m 按行匹配)
	// 2026-08-25 修复:列分隔符用 [^\n|] 禁止跨行——旧 [^|] 会跨行吞内容,把「二、每镜 H3 提示词」
	// 表格的 | 01 | Cinematic... 行误匹配成镜头(实测 EP01 17 镜被解析成 22 镜,提示词全文塞进景别列)。
	// 2026-08-25 H1 修复:第 9 列(风格列)可选——9 列格式(镜号|景别|运镜|画面|台词|光影|音效|风格|时长)
	// 时长列在第 9 列,正则必须把第 9 列也捕获(m[9]=时长),否则 m[0] 截断在第 8 列、时长解析回退 5s。
	// 必须加 $ 锚定:可选第 9 组无锚定时,贪婪尝试会吞掉下一行内容(FindAll 只返回 1 个匹配)。
	reScriptTableRow = regexp.MustCompile("(?m)^\\s*\\|\\s*(\\d+)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|\\s*([^\\n|]*?)\\s*\\|(?:\\s*([^\\n|]*?)\\s*\\|)?\\s*$")
	// ### Shot N 六段式代码块(反引号围栏,正则用拼接)
	reScriptShotBlock = regexp.MustCompile("(?s)###\\s*Shot\\s*(\\d+)[^\\n]*\\n\\s*```[^\\n]*\\n(.*?)\\n\\s*```\\s*")
	// 素材标题行解析(两种格式分别处理,避免一个正则塞太多分支):
	//  人物:## 1. 云晚（女主，22岁） 或 ## 2.1 主角 · 顾烬（男主，烛龙血脉）
	//  场景:### 场景一 · 烛龙村（雪夜废墟） 或 ## 1. 晚膳小馆（主角主场）
	// 通用:##/### 编号. 名字（描述）——编号后允许 身份词·名字 形式(如 2.1 主角 · 顾烬)
	// 2026-08-26 补:盟友/灵宠/坐骑/妖兽/神兽/精怪/兽宠 等身份词(用户实测「盟友 · 陈墨」
	//  「灵宠 · 吞吞」整串进了角色 id,渲染产物文件名带前缀且 beast/性别画像失准)
	// 2026-08-27 补:群演(群演轻量卡前缀,「群演 · 周管事」→ id=周管事 + minor:true)
	reMdTitle    = regexp.MustCompile(`(?m)^#{2,3}\s*(?:\d+(?:\.\d+)?[\.、]\s*)?(?:(?:主角|女主|男主|男配|女配|反派|助攻|盟友|调剂位|工具人反派|工具人|传说位|昆仑守山神|灵宠|兽宠|宠物|坐骑|妖兽|神兽|精怪|长老|群演)\s*[·:：\-—]\s*)?([^（(]+?)\s*[（(]([^）)]*)[）)]`)
	reSceneTitle = regexp.MustCompile(`(?m)^#{2,3}\s*(?:场景[一二三四五六七八九十]+\s*[·:：\-—]\s*|[a-zA-Z\d]+[\.、]\s*)([^（(]+?)\s*[（(]([^）)]*)[）)]`)
	// reSceneBareTitle 纯名字场景卡标题兜底(2026-08-29 绿萝实锤:32 张卡「## 星海大厦外景」
	// 无编号无括号,reSceneTitle/reMdTitle 都强制括号→一张不匹配→scenes=0,25 镜全无场景图,
	// 同场景跨镜长相漂移;文件里唯二带括号的标题是两张重复的封面备用卡,匹配后又被封面过滤
	// 拦掉,日志只剩「过滤 2 张」极具迷惑性)。仅收名字内无括号/冒号/逗号的短标题,配合
	// 「节内必须有提示词代码块」护栏 + 全局段黑名单,防把说明性小节收进卡池。
	reSceneBareTitle = regexp.MustCompile(`(?m)^#{2,3}\s*([^（(：:,\n]+?)\s*$`)
	// 全局段标题黑名单(人物生成提示词.md 中「统一风格前缀/统一质量后缀/通用负向词」等
	// 所有角色共用的字段;创作侧契约=一级标题,若误写二级/三级标题则被 reMdTitle 当角色,
	// parseCharCards 命中即跳过。关键词只匹配标题名,正常角色名不会含这些词)
	reManjuGlobalSection = regexp.MustCompile(`统一|共用|前缀|后缀|负向|负面|通用|说明|备注|清单|记忆点|全书`)
	// 角色卡音色字段(2026-08-30 ver15 技能侧配置):节内「音色:/声线:/方言:」行
	// (记忆点列表项常见「- 音色:xxx」,允许列表前缀)
	reCharVoiceLine = regexp.MustCompile(`(?m)^\s*(?:[-*]+\s*)?(?:音色|声线|方言)\s*[：:]\s*([^\n]+)`)
	// 素材代码块(英文提示词)
	reMdCodeBlock = regexp.MustCompile("(?s)```[^\\n]*\\n(.*?)\\n```")
	// 台词:(S1)沈玉衡:"晚老板..."(非贪婪到闭合引号,多句逐条匹配;兼容无引号句)
	// 2026-09-01 S\d+ 修复:原 (S\d) 只匹配 1 位编号,S10-S12(第173章写名之战
	// 群像,多角色喊同一句)整行匹配失败 → 权威集缺句 → 跨镜归属判定失效。
	// 同批:引号字符类加「」『』(第73章凶水会用「」包裹台词,漏匹配=权威集缺句)。
	reDialogue = regexp.MustCompile(`\(S\d+\)\s*([^：:]+?)\s*[：:]\s*["“「『]([^"”」』]+?)["”」』]|\(S\d+\)\s*([^：:]+?)\s*[：:]\s*([^"”」』]+)`)
	// 群演轻量卡(2026-08-27):分镜表说话人带 S 声线编号提取((S2)周管事: → 2,周管事)
	reSpeakerTag = regexp.MustCompile(`\(S(\d+)\)\s*([^：:"“]+?)\s*[：:]`)
	// 六段式主体定义行:"<Subject 2> is the white-haired steward in <Picture 1>, kneeling..."
	reSubjectLine = regexp.MustCompile(`(?m)^<Subject\s+\d+>\s+is\s+([^\n]+)$`)
	// 六段式台词句的说话人描述:"The old steward with a swallowed sob (S2) says:"
	reH3VoiceDesc = regexp.MustCompile(`([A-Za-z][^\n<>]{0,120}?)\s+\(S(\d+)\)\s+says:`)
	// 主体描述里的参考图引用(轻量卡形象提示词剥掉它)
	rePictureRef = regexp.MustCompile(`\s*in\s*<Picture\s+\d+>`)
	// 时长 "5s"/"6s"/"7s":宽松版(匹配列内任意 Ns,用于列捕获值);
	// 行尾锚定版(防画面内容里的"0-5s 节拍"等误匹配,用于整行回溯)。
	reDuration      = regexp.MustCompile(`(\d+)\s*s`)
	reDurationTail  = regexp.MustCompile(`(\d+)\s*s\s*\|?\s*$`)
)

// manjuIsOffScreenSpeaker 画外说话人(2026-08-27 用户规则:叙述优先群众议论化,
// 旁白禁止复述画面):分镜台词列说话人带 `画外·` 前缀(如 `(S2)画外·路人甲`)=
// 画外群杂——不入画/不生成角色卡/不占 3 角色名额/不触发说话人校验;
// 照常占语音预算,六段式写 off-screen voiceover(H3 按身份描述分配声线)。
func manjuIsOffScreenSpeaker(speaker string) bool {
	return strings.HasPrefix(strings.TrimSpace(speaker), "画外·")
}

// scriptMinorCast 群演轻量卡(2026-08-27 群演分级体系):有台词但无角色卡的说话人
// 自动建卡(minor:true)——这类人正脸开口说话,形象跨镜漂移最伤观感,值得一张参考图。
// 形象提示词取自其首次开口镜的六段式 subject_definitions 主体描述(脚本是权威,零 LLM
// 成本):按台词句 "The old steward (S2) says:" 的描述词与各 Subject 行词重叠匹配归属;
// 匹不上取该镜未被认领的首个 Subject;再兜底通用描述。资产阶段对 minor 卡只出 1 张
// 定妆照+正脸(跳过视图/Q版/音色);氛围群演(无名无台词,如"两名牢卒")不建卡,
// 由渲染纪律(manjuNoRefGuard/manjuFrameGuard)兜底。
func scriptMinorCast(raws []scriptShotRaw, known map[string]bool, lg *manjuLogger) []map[string]any {
	firstShot := map[string]int{}
	sNum := map[string]string{}
	var order []string
	for i := range raws {
			for _, m := range reSpeakerTag.FindAllStringSubmatch(raws[i].Dialogue, -1) {
				name := strings.TrimSpace(m[2])
				// 内心·(ver14 防御):正常已转 narration,漏网形态不建幽灵群演卡
				if name == "" || manjuIsOffScreenSpeaker(name) || strings.HasPrefix(name, "内心·") {
					continue
				}
				// 2026-09-01 变体归一:说话人简称命中有素材卡 → 不建重复自动卡
				// (杳杳→涂山杳杳,避免同一角色两卡形象冲突)
				if rid := manjuResolveCharID(name, knownKeys(known)); rid != "" {
					continue
				}
				if known[name] {
					continue
				}
			if _, ok := firstShot[name]; !ok {
				firstShot[name] = raws[i].ID
				order = append(order, name)
			}
			sNum[name] = m[1]
		}
	}
	if len(order) == 0 {
		return nil
	}
	rawByID := map[int]*scriptShotRaw{}
	for i := range raws {
		rawByID[raws[i].ID] = &raws[i]
	}
	// 主体描述清洗:剥 "in <Picture N>" 与首个逗号后的姿态/情绪分词(定妆照只要身份特征)
	cleanSubj := func(line string) string {
		line = rePictureRef.ReplaceAllString(strings.TrimSpace(line), "")
		if i := strings.Index(line, ","); i > 0 {
			line = line[:i]
		}
		return strings.TrimSpace(strings.Trim(line, "."))
	}
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.Fields(strings.ToLower(s)) {
			w = strings.Trim(w, ".,;:!?\"'-")
			if len([]rune(w)) >= 4 {
				out[w] = true
			}
		}
		return out
	}
	claimed := map[int]map[string]bool{} // 镜号 → 该镜已被其他群演认领的主体描述
	out := []map[string]any{}
	for _, name := range order {
		raw := rawByID[firstShot[name]]
		desc := ""
		if raw != nil && raw.H3Prompt != "" {
			// 台词句 "(SN) says:" 前的说话人描述 → 词集(与 Subject 行词重叠匹配归属)
			voiceWords := map[string]map[string]bool{}
			for _, m := range reH3VoiceDesc.FindAllStringSubmatch(raw.H3Prompt, -1) {
				voiceWords[m[2]] = words(m[1])
			}
			vw := voiceWords[sNum[name]]
			type cand struct {
				line  string
				score int
			}
			var cands []cand
			for _, m := range reSubjectLine.FindAllStringSubmatch(raw.H3Prompt, -1) {
				c := cleanSubj(m[1])
				if c == "" || (claimed[firstShot[name]] != nil && claimed[firstShot[name]][c]) {
					continue
				}
				sc := 0
				if vw != nil {
					for w := range words(c) {
						if vw[w] {
							sc++
						}
					}
				}
				cands = append(cands, cand{c, sc})
			}
			best := -1
			for i, c := range cands {
				if best < 0 || c.score > cands[best].score {
					best = i
				}
			}
			if best >= 0 {
				desc = cands[best].line
				if claimed[firstShot[name]] == nil {
					claimed[firstShot[name]] = map[string]bool{}
				}
				claimed[firstShot[name]][desc] = true
			}
		}
		if desc == "" {
			desc = "a minor supporting cast member of this drama, plain period costume, unremarkable commoner face"
		}
		out = append(out, map[string]any{
			"id": name, "minor": true, "role": "群演",
			"appearance": "", "costume": "", "image_prompt": desc,
		})
		lg.logf(fmt.Sprintf("  🎭 群演轻量卡: %s(首次开口镜 %d,形象取自脚本主体描述)", name, firstShot[name]))
	}
	return out
}

// knownKeys map key 快照(scriptMinorCast 变体归一用,避免在循环里反复分配)
func knownKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// manjuScriptParseVer 脚本程序化解析器代数:写入 plan.script_parse_ver,ensurePlan 复用
// 校验发现版本落后 → 强制重新解析替换旧 plan。背景(2026-08-26 用户实测:16 分镜脚本
// 只渲染 8 个,普通一条龙):旧版解析器对脚本解析失败时静默回退 LLM 直出,LLM 拆镜数
// 不受脚本约束(旧版无拆镜密度强制,8 镜常见),plan 落盘后 chapters="script"+指纹一致
// → 永久复用,升级解析器也不自愈。bump 此值即可让全部脚本直出项目自动重解析。
// 1=初版(无版本标记的存量 plan);2=分镜表跨行/9列时长/FormatB/镜号去重/时长语音补偿/
// 机械质检;3=身份词前缀清理(盟友/灵宠·名字)+role/species 画像+中文称谓性别兜底;
// 4=人物生成提示词全局段过滤(统一风格前缀/质量后缀/通用负向词等误写二级标题不再当角色);
// 5=画外群杂契约(台词说话人「画外·」前缀豁免强制入画与角色校验——叙述群众议论化);
// 6=群演轻量卡(有台词无角色卡的说话人自动建卡 minor:true,characters 纳入说话人——
//   挂参考图锁形象,资产阶段只出 1 张定妆照,氛围群演仍走渲染纪律);
// 7=素材四硬规范回写(人物生成提示词存量卡补性别词 male/female+剥背景词/场景叙事句
//   ——plan.characters 缓存的是旧素材内容,须重解析吸收);
// 8=场景匹配根治(2026-08-28 EP01 事故:办公室戏 18 镜全挂封面卡「城市夜景大远景」):
//   ①matchSceneByAlias 重写为场景卡动态特征词滑窗匹配(旧硬编码别名表是美食书的规则,
//   跨书污染);②封面备用卡不入匹配池与 scenes;③最高频兜底需 ≥2 票(1 票不当选);
//   ④空镜向后继承(特写镜归前后场景);⑤全失效宁可留空不挂错图;
// 9=h3 捞人收紧(2026-08-28 过捞修复:卡间独有词重叠≥2 替代卡内词重叠≥3——共享模板词
//   cinematic/photorealistic/doll 让镜3 把 12 角色全捞进 chars;主体句限定 subject_
//   definitions 区,detailed_description 长句与任何卡都能凑够重叠);
// 10=伪场景判定分组(2026-08-28 误杀修复:「深夜工位」desc 含合法标注「全书视觉锚」
//   被「全书」整卡误杀,EP01 丢主场景——伪配置组[负向/统一风格/全书]只查 id,封面组
//   [封面/备用/开篇/终章]查 id+desc);
// 11=h3 捞人三层收紧(2026-08-29 镜9 误捞:短词 dark 恰为陈默卡独有,环境句「dark
//   office aisle」触发捞人,镜9 4 角色超 H3 参考图上限告警)——独有词≥5字母+主体句
//   须含人物外观信号词(hair/glasses/shirt/…),环境/道具句不参与捞人;
// 12=场景卡三级兜底+plan 汇点槽位时机(2026-08-29 绿萝全书实锤:渲染画面与分镜脚本
//   脱节双根因)——①parseSceneCards 新增纯名字标题兜底(绿萝 32 张卡「## 星海大厦外景」
//   无编号无括号,reSceneTitle/reMdTitle 强制括号一张不匹配→scenes=0,25 镜全无场景图,
//   同场景跨镜长相漂移;文件唯二带括号标题是两张重复封面备用卡,匹配后又被封面过滤拦掉,
//   日志只剩「过滤 2 张」极具迷惑性)——重解析后场景匹配池/场景图恢复;②plan 汇点
//   (ensurePlanAndPrompts)finalize 早于定妆照生成,实测槽位恒 0 把全部 <Picture N>
//   引用剥除并回写固化,渲染时人物参考图整集失效(人物长相与角色卡无关)——改用预期
//   槽位纯函数(manjuExpectPicSlots),重解析后 h3_prompt 恢复脚本原文含 Picture 引用;
// 13=角色节多形态段错位修复(2026-08-29 阿影 Q 版串色实锤:【Q版·内心戏专用提示词】段
//   写在主形象段前,旧逻辑恒取第一个代码块→Q版黑团子提示词被当主形象拼人形风格锚,
//   影灵主图出银白团子黑↔白串色,影子形态正主提示词被顶掉)——scriptMainBlock 主形象块
//   跳过带 Q版/真身/形态标记的代码块,Q版段独立解析为 q_form(manjuQPrompt 优先取);
//   兽形 Q 版毛色锁改按提示词出现序排色(主体色永远在小色块前,white 不再恒排 black 前)。
//   plan.characters 缓存旧素材内容,须重解析吸收。
// 14=六维度升级(2026-08-30):内心·前缀保留+光影/音效/风格三列保留+Sx 说话人注册表。
// 15=五问整改(2026-08-30):①机械补写剥「内心·/旁白」前缀与引号+内容命中即不补
//   (内心配音重复根治,EP01 镜4/5/6/14 每镜重复画外音句随重解析消失);②scriptValidateShots
//   新增站位/运镜/动作-对白三项 WARN 检查(站位不清/运镜失配/说话人无动作在解析期暴露);
//   ③拆镜密度放宽(每 80-130 字一镜,内容完整优先,拆镜数不设上限)。
// 16=JSON 分镜脚本格式(2026-08-31 技能侧新格式:md 表格弃用——表格断行丢镜
//   (EP01 25 镜解析成 21 镜/541 行异常)根治;scriptParsePlan 检测 .json/首字符 {
//   结构化解析,shots[].characters 显式声明并入登场角色;存量 336 章已 md→json 转换)。
// 17=2026-09-01 角色名变体归一(manjuResolveCharID):characters 声明/说话人简称
//    (杳杳/主持人/小汤)归一到素材卡全名(涂山杳杳/天才榜主持人/孟小汤),精确匹配
//    不上=无参考图=主次混乱/形象漂移(杂毛杳杳×108、影子主持人×6 实锤);
//    scriptMinorCast 建卡前置归一,有素材卡不建重复自动卡。
// 18=2026-09-01 配音重复根治(scriptDedupShotLines):跨镜串句删除(总览镜把
//    整段对话写进第一镜,后续分解镜各带一句 → 同一句台词多镜重复念两遍,
//    EP01 镜1 三句 vs 镜2/3 实锤)+ 同镜半角/全角双版本去重;以台词列为
//    权威逐句归属,即兴台词/口号/连环「无」不误删。
// 19=2026-09-02 内心段多行延续修复(王牌三岁半镜11 实锤):「内心·棠棠:"豆豆…,"
//    \n"怎么只有一个冰凉的环。"」后续引号延续行无前缀 → 旧逻辑错拆进 Dialogue
//    (无说话人,渲染对不上脚本);延续行归 narration。
// 20=2026-09-02 配音重复三连根治(王牌三岁半 EP01 镜15 实锤):①manjuOffscreenDescs
//    提取画外声线描述跨台词块(同一长句两句画外音,desc 含 <d> 台词 → Audio 定义行
//    内嵌台词 → H3 念两遍);②alignAudioDefsReg @offscreen 重写剥掉幂等锚前缀 → 每轮
//    finalize 重复注入叠加;③补写判定 key 剥引号(带引号台词与六段式 <d> 无引号形态
//    匹配不上 → 误补写重复 <d>)。bump 强制存量 plan 重解析。
// 21=2026-09-03 登场角色误捞根治(修仙界EP01 老K实锤):manjuCharsFromH3 对每卡独立
//    判定无同句竞争——镜1主体句是季一星本人定义行,季一星卡命中5词正确,老K卡(同为
//    上班族形象)独有词 crumpled/office 命中2词即入画,镜1~8连续误挂老K参考图 → H3
//    给画外角色编人声(无台词镜出说话声)。收紧双层:④a 声明优先(已判定登场人数 ≥
//    主体句数时不捞,声明权威只补真漏);④b 同句唯一归属(每句只归重叠最多且严格
//    领先次名者,平票不归属)。bump 强制存量 plan 重解析。
const manjuScriptParseVer = 22

// scriptParsePlan 脚本直出程序化解析入口。
// 解析出 characters/scenes/shots/directing/episode_title/chapters=script。
// 任一步关键缺失(无分镜表行 / 无 Shot 代码块)返回 error → 调用方回退 LLM。
// 2026-08-31 双格式:JSON 分镜脚本(技能侧新格式,.json 或首字符 {)结构化解析;
// Markdown 分镜脚本(分镜表 + ### Shot N 六段式)原有解析。JSON 后 md 弃用。
func (ctx *manjuCtx) scriptParsePlan(lg *manjuLogger) (map[string]any, error) {
	if ctx.novel == "" {
		return nil, fmt.Errorf("脚本路径为空")
	}
	b, err := os.ReadFile(ctx.novel)
	if err != nil {
		return nil, fmt.Errorf("读脚本失败: %w", err)
	}
	text := string(toUTF8(b))
	trimmed := strings.TrimSpace(text)

	// ---- JSON 分镜脚本分支(2026-08-31 技能侧新格式) ----
	if strings.HasSuffix(ctx.novel, ".json") || strings.HasPrefix(trimmed, "{") {
		raws, err := parseScriptJSON(text)
		if err != nil {
			return nil, fmt.Errorf("JSON 分镜脚本解析失败: %w", err)
		}
		if len(raws) == 0 {
			return nil, fmt.Errorf("JSON 分镜脚本无镜头(shots 为空)")
		}
		lg.logf(fmt.Sprintf("  📦 JSON 分镜脚本: %d 镜结构化解析(字段直读,零表格断行风险)", len(raws)))
		return ctx.buildPlanFromRaws(raws, text, lg)
	}

	// 1) 每镜六段式代码块 → h3_prompt 逐字保留
	shotPrompts := map[int]string{}
	for _, m := range reScriptShotBlock.FindAllStringSubmatch(text, -1) {
		if n, aerr := strconv.Atoi(m[1]); aerr == nil {
			shotPrompts[n] = strings.TrimSpace(m[2])
		}
	}
	// 2026-08-25 H2 修复:Format B 脚本的六段式写在 2 列表格里(`| 01 | [Shot 1] Cinematic... |`,
	// 混沌灵根/镇厄奶团等老脚本格式),没有 `### Shot N` 代码块标题 → 上面正则 0 命中 →
	// 每镜 H3Prompt 为空 → 管线逐镜 LLM 重生成,脚本导演设计(站位/节拍/光声味)全丢、台词被改写。
	// 这里补 Format B 解析:匹配"每镜 H3 提示词"章节下的 `| N | detailed_description... |` 行,
	// 把第二列英文提示词作为该镜 h3_prompt(缺失的 subject_definitions 等由逐镜生成兜底填充,
	// 但 detailed_description 本体=脚本原样,不再被 LLM 重写)。
	if len(shotPrompts) == 0 {
		// "## 二、每镜 H3 提示词" 或 "每镜 H3 提示词(六段式" 之后的行
		reFormatB := regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|\s*\[?Shot\s*\d+\s*\]?\s*(Cinematic[^\n|]*)\s*\|`)
		for _, m := range reFormatB.FindAllStringSubmatch(text, -1) {
			if n, aerr := strconv.Atoi(m[1]); aerr == nil {
				p := strings.TrimSpace(m[2])
				// 补全为近似六段式:detailed_description 原样 + 其余字段留待逐镜生成填充
				if strings.Contains(p, "Cinematic") {
					shotPrompts[n] = p
				}
			}
		}
		if len(shotPrompts) > 0 {
			lg.logf(fmt.Sprintf("  📝 Format B 脚本:从 2 列表格提取 %d 镜 detailed_description(不再 LLM 重写)", len(shotPrompts)))
		}
	}

	// 2) 分镜表行 → shots 元数据
	var raws []scriptShotRaw
	seenIDs := map[int]bool{}
	for _, m := range reScriptTableRow.FindAllStringSubmatch(text, -1) {
		id, aerr := strconv.Atoi(strings.TrimSpace(m[1]))
		if aerr != nil || id <= 0 {
			continue
		}
		// 2026-08-25 防御:H3 六段式提示词表数据行(第一列是镜号、第二列是英文提示词)不是分镜表,
		// 若其内容被跨行/异常匹配进来,景别列会是英文提示词开头——直接跳过,避免污染 shots。
		col2 := strings.TrimSpace(m[2])
		if strings.HasPrefix(col2, "Cinematic") || strings.Contains(col2, "detailed_description") || strings.Contains(col2, "six-part") || strings.HasPrefix(col2, "The target video") {
			continue
		}
		// 2026-08-25 M1 修复:分镜表偶有镜号重复(FL2VA 空镜行复用真实镜号,如 `| 18 | FL2VA 空镜 —— ... |`)。
		// 渲染层按 shot_id 写 %02d.mp4,重复 ID 会同名覆盖→丢镜。解析时给后续重复镜号递增分配
		// 下一个未占用的 ID,并记录告警(空镜仍是独立镜头,必须渲染,不能跳过)。
		if seenIDs[id] {
			orig := id
			for seenIDs[id] {
				id++
			}
			lg.logf(fmt.Sprintf("  ⚠️ 分镜表镜号 %d 重复(%s…),已重分配为 %d(防同名覆盖)", orig, firstN(col2, 20), id))
		}
		seenIDs[id] = true
		raw := scriptShotRaw{
			ID:       id,
			ShotSize: col2,
			Camera:   strings.TrimSpace(m[3]),
			Action:   strings.TrimSpace(m[4]),
			Light:    strings.TrimSpace(m[6]),
			Sound:    strings.TrimSpace(m[7]),
			Duration: 5,
		}
		// 风格列:仅 9 列格式存在(m[9]=时长非空 → m[8]=风格);8 列格式 m[8]=时长,不误认
		if strings.TrimSpace(m[9]) != "" {
			raw.Style = strings.TrimSpace(m[8])
		}
		// 台词/旁白列:旁白前缀→narration;其余 (Sx)角色:"..." → dialogue
		dialCol := strings.TrimSpace(m[5])
		if i := strings.Index(dialCol, "旁白"); i >= 0 {
			rest := dialCol[i:]
			rest = strings.TrimPrefix(rest, "旁白")
			rest = strings.TrimPrefix(rest, "：")
			rest = strings.TrimPrefix(rest, ":")
			if j := strings.Index(rest, "(S"); j >= 0 {
				rest = rest[:j]
			}
			rest = strings.TrimSpace(rest)
			rest = strings.Trim(rest, "。！？.!? ")
			if rest != "" {
				raw.Narration = "旁白：" + rest + "。"
			}
		}
		// 内心独白(2026-08-30 ver14 修复):「内心·角色名:内容」→ narration 保留「内心·」
		// 前缀——渲染端 Q 版挂载(shotViewRelsFor 判 Narration 含「内心·」)与画外音音色
		// 差异化都依赖该前缀。此前裸写(无 S 号)被整句丢弃、带 (Sx) 的「内心·」被
		// reDialogue 当普通说话人→scriptMinorCast 建幽灵群演卡,Q 版参考图永不挂载。
		if j := strings.Index(dialCol, "内心·"); j >= 0 {
			inner := strings.TrimPrefix(dialCol[j:], "内心·")
			k := strings.Index(inner, "：")
			if k < 0 {
				k = strings.Index(inner, ":")
			}
			if k > 0 {
				name := strings.TrimSpace(inner[:k])
				// 2026-09-01 全角冒号字节修复(同 stripNarrationPrefix):
				// 内容从完整 rune 后取,防 \xbc\x9a 残留进 narration。
				_, size := utf8.DecodeRuneInString(inner[k:])
				content := strings.TrimSpace(inner[k+size:])
				content = strings.Trim(content, "。！？.!? ")
				if name != "" && content != "" {
					line := "内心·" + name + ":" + content + "。"
					if raw.Narration != "" {
						raw.Narration += "\n" + line
					} else {
						raw.Narration = line
					}
				}
			}
			dialCol = dialCol[:j] // 剔除内心段,防 reDialogue 把「内心·」当说话人
		}
		for _, dm := range reDialogue.FindAllStringSubmatch(dialCol, -1) {
			speaker, line := dm[1], dm[2]
			if speaker == "" { // 无引号分支
				speaker, line = dm[3], dm[4]
			}
			speaker = strings.TrimSpace(speaker)
			line = strings.TrimSpace(line)
			if speaker != "" && line != "" {
				if raw.Dialogue != "" {
					raw.Dialogue += "\n"
				}
				raw.Dialogue += speaker + ":" + line
			}
		}
		// 时长列:9 列格式取 m[9](时长在第 9 列),8 列格式取 m[8](时长在第 8 列);
		// 再兜底从整行行尾提取(防列错位)。H1 修复:此前只取 m[8],9 列脚本时长列被风格列顶掉→全 5s。
		durCol := strings.TrimSpace(m[9])
		if durCol == "" {
			durCol = strings.TrimSpace(m[8])
		}
		dm := reDuration.FindStringSubmatch(durCol)
		if dm == nil {
			dm = reDurationTail.FindStringSubmatch(m[0])
		}
		if dm != nil {
			if n, aerr := strconv.Atoi(dm[1]); aerr == nil && n >= 4 && n <= 15 {
				raw.Duration = n
			}
		}
		raw.H3Prompt = shotPrompts[id]
		raws = append(raws, raw)
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("脚本无分镜表行(未识别 | 镜号 | ... | 时长 | 表格)")
	}
	return ctx.buildPlanFromRaws(raws, text, lg)
}

// ---- JSON 分镜脚本(2026-08-31 技能侧新格式,md 表格弃用) ----
// Schema:
//
//	{
//	  "book": "《书名》", "episode": 1, "chapter_title": "章节名", "global_style": "风格句",
//	  "bridge": {"prev_ending": "", "opening_beat": "", "position": "", "closing_hook": "", "next_entry": ""},
//	  "shots": [{
//	    "shot_id": 1, "shot_size": "景别", "camera": "运镜", "action": "【场景名】画面",
//	    "dialogue": "(S1)角色:\"...\" / 内心·角色:\"...\" / 旁白：... / 无(多行 \n 分隔)",
//	    "characters": ["登场角色"], "light": "光影", "sound": "音效", "duration": 5,
//	    "style": "可选风格列", "h3_prompt": "六段式全文"
//	  }]
//	}

type scriptShotJSON struct {
	ShotID     int      `json:"shot_id"`
	ShotSize   string   `json:"shot_size"`
	Camera     string   `json:"camera"`
	Action     string   `json:"action"`
	Dialogue   string   `json:"dialogue"`
	Characters []string `json:"characters"`
	Light      string   `json:"light"`
	Sound      string   `json:"sound"`
	Duration   int      `json:"duration"`
	Style      string   `json:"style"`
	H3Prompt   string   `json:"h3_prompt"`
}

type scriptJSON struct {
	Book         string            `json:"book"`
	Episode      int               `json:"episode"`
	ChapterTitle string            `json:"chapter_title"`
	GlobalStyle  string            `json:"global_style"`
	Bridge       map[string]string `json:"bridge"`
	Shots        []scriptShotJSON  `json:"shots"`
}

// parseScriptJSON JSON 分镜脚本 → []scriptShotRaw(与 md 路径等效,后续组装共用):
// 台词列按行拆分「内心·/旁白」进 Narration(前缀保留,渲染端 Q 版/音色差异化依赖);
// 镜号重复递增分配(防同名覆盖,同 md 路径);时长 4-15 校验。
func parseScriptJSON(text string) ([]scriptShotRaw, error) {
	var sj scriptJSON
	if err := json.Unmarshal([]byte(text), &sj); err != nil {
		return nil, fmt.Errorf("JSON 格式错误: %w", err)
	}
	var raws []scriptShotRaw
	seenIDs := map[int]bool{}
	for _, s := range sj.Shots {
		id := s.ShotID
		if id <= 0 {
			continue
		}
		// 镜号重复递增分配(防同名覆盖,同 md 路径)
		if seenIDs[id] {
			for seenIDs[id] {
				id++
			}
		}
		seenIDs[id] = true
		dur := s.Duration
		if dur < 4 || dur > 15 {
			dur = 5
		}
		raw := scriptShotRaw{
			ID:        id,
			ShotSize:  strings.TrimSpace(s.ShotSize),
			Camera:    strings.TrimSpace(s.Camera),
			Action:    strings.TrimSpace(s.Action),
			Light:     strings.TrimSpace(s.Light),
			Sound:     strings.TrimSpace(s.Sound),
			Style:     strings.TrimSpace(s.Style),
			Duration:  dur,
			H3Prompt:  strings.TrimSpace(s.H3Prompt),
			JSONChars: s.Characters,
		}
		// 台词列逐行拆分:内心·/旁白 → Narration(前缀保留),其余 → Dialogue。
		// 2026-09-02 内心段多行延续修复(王牌三岁半镜11 实锤):内心段常写成
		// 「内心·棠棠:"豆豆明明说…,"\n"怎么只有一个冰凉的环。"」——第一行带
		// 内心·前缀,后续引号延续行无前缀 → 旧逻辑把延续行错拆进 Dialogue
		// (无说话人=对不上脚本/渲染端误当角色台词)。引号开头/上一行是内心的
		// 延续行继续归 narration(前缀只在段首保留一次)。
		inNarr := false // 上一行是否内心/旁白段(延续行判定)
		for _, line := range strings.Split(s.Dialogue, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if i := strings.Index(line, "内心·"); i >= 0 {
				if raw.Narration != "" {
					raw.Narration += "\n"
				}
				raw.Narration += strings.TrimSpace(line[i:])
				inNarr = true
				continue
			}
			if strings.HasPrefix(line, "旁白") {
				if raw.Narration != "" {
					raw.Narration += "\n"
				}
				raw.Narration += line
				inNarr = true
				continue
			}
			// 内心/旁白段的引号延续行(无前缀):归 narration 并补「内心·」段标记
			if inNarr && (strings.HasPrefix(line, "\"") || strings.HasPrefix(line, "“") || strings.HasPrefix(line, "「")) {
				if raw.Narration != "" {
					raw.Narration += "\n"
				}
				raw.Narration += line
				continue
			}
			inNarr = false
			if raw.Dialogue != "" {
				raw.Dialogue += "\n"
			}
			raw.Dialogue += line
		}
		raws = append(raws, raw)
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("shots 为空")
	}
	return raws, nil
}

// buildPlanFromRaws 从分镜表行(raws)组装方案(md 与 JSON 分镜脚本共用,
// 2026-08-31 抽取):语音预算补偿 → 机械质检 → 角色/场景卡 → 场景匹配/登场角色 →
// 场景收尾三件套 → plan。
func (ctx *manjuCtx) buildPlanFromRaws(raws []scriptShotRaw, text string, lg *manjuLogger) (map[string]any, error) {
	// 2026-08-26 语音预算自动补偿:台词+旁白总字数 ÷ 字速 > 时长 → 自动延长该镜时长
	// (clamp 到 15s API 上限)。此前旁白零预算,超预算镜 H3 念一半就切(「画面有字无配音」
	// 的渲染侧诱因);脚本为权威不改动内容,只补时长让语音念得完。
	for i := range raws {
		chars := manjuSpeechChars(raws[i].Dialogue, raws[i].Narration)
		if chars == 0 || ctx.charsPerSec <= 0 {
			continue
		}
		need := int(math.Ceil(float64(chars) / ctx.charsPerSec))
		if need <= raws[i].Duration {
			continue
		}
		adj := need
		if adj > 15 {
			adj = 15
		}
		if need > 15 {
			lg.logf(fmt.Sprintf("  ⚠️ 镜头 %d 语音 %d 字约需 %ds,超出 API 上限,已按 15s 渲染(建议精简台词或拆镜)", raws[i].ID, chars, need))
		} else {
			lg.logf(fmt.Sprintf("  ⚠️ 镜头 %d 语音 %d 字约需 %ds > 脚本时长 %ds,已自动延长时长(防台词截断)", raws[i].ID, chars, need, raws[i].Duration))
		}
		raws[i].Duration = adj
	}

	// 2026-08-26 分镜机械质检(对齐爽文技能 storyboard_check 六项中管线侧此前缺失的三项):
	// 时间戳递增 / 时长多样性 / 台词-六段式同步。机械能查的不烧 GPU 不耗视觉模型,导入期发现导入期修。
	scriptValidateShots(raws, lg)
	// 2026-09-01 配音重复根治:跨镜/镜内台词去重(总览镜串句)——
	// LLM 生成 h3_prompt 时习惯把「整段对话」写进场景第一镜(总览镜)的
	// detailed_description,后续分解镜又各带一句 → 同一句台词在多个镜的
	// <d> 中重复,H3 每镜独立配音 → 同一句被念多遍(EP01 镜1 三句 vs 镜2/3
	// 各重复一句实锤;用户反馈「镜头变多后配音重复更厉害」)。
	// 规则见 scriptDedupShotLines 注释;必须在 Validate(补写)之后执行,
	// 否则补写会把已删串句重新补回。
	scriptDedupShotLines(raws, lg)

	// 3) 角色/场景卡:素材(workdir/素材/ 优先,其次 novel 素材目录)
	charCards, sceneCards := ctx.parseScriptAssetCards(lg)
	// 2026-08-28 封面备用卡不入正片场景池(EP01 实锤:「城市夜景大远景」desc 写「封面备用·
	// 开篇/终章」,混进场景池后被兜底逻辑传染全片)。它不属于任何正片镜头,匹配池和
	// scenes 列表都不进;loadPlan 的 manjuSanitizePlanIDs 同步 drop(双保险)。
	usableScenes := make([]map[string]any, 0, len(sceneCards))
	for _, sc := range sceneCards {
		id, _ := sc["id"].(string)
		desc, _ := sc["description"].(string)
		if manjuSceneDropped(id, desc) {
			continue
		}
		usableScenes = append(usableScenes, sc)
	}
	if len(usableScenes) < len(sceneCards) {
		lg.logf(fmt.Sprintf("  🚫 场景卡过滤:%d 张封面备用/伪配置卡不参与正片匹配", len(sceneCards)-len(usableScenes)))
	}
	sceneCards = usableScenes
	// 4) 组装 plan
	charsArr := []any{}
	for _, c := range charCards {
		charsArr = append(charsArr, c)
	}
	scenesArr := []any{}
	for _, s := range sceneCards {
		scenesArr = append(scenesArr, s)
	}
	// 角色名列表(用于从画面内容/台词列匹配登场角色)
	charIDs := []string{}
	for _, c := range charCards {
		if id, _ := c["id"].(string); id != "" {
			charIDs = append(charIDs, id)
		}
	}
	// 群演轻量卡(2026-08-27 群演分级):台词说话人无角色卡 → 自动建卡入名单
	// (characters 纳入说话人=挂参考图锁形象,charRefNames/指纹链路现成)
	knownSet := map[string]bool{}
	for _, cid := range charIDs {
		knownSet[cid] = true
	}
	if minors := scriptMinorCast(raws, knownSet, lg); len(minors) > 0 {
		for _, mc := range minors {
			charsArr = append(charsArr, mc)
			if id, _ := mc["id"].(string); id != "" {
				charIDs = append(charIDs, id)
			}
		}
	}
	allIDs := map[string]bool{}
	for _, cid := range charIDs {
		allIDs[cid] = true
	}
	// 场景名匹配:画面内容含场景卡 id → 绑定;无场景卡的镜 scene 留空(渲染兜底)
	sceneIDs := []string{}
	for _, s := range sceneCards {
		if id, _ := s["id"].(string); id != "" {
			sceneIDs = append(sceneIDs, id)
		}
	}
	shotsArr := []any{}
	missingPrompt := 0
	// 2026-08-25 场景继承:分镜表无场景列,画面/台词/旁白常不写场景名(EP01 实测仅首镜匹配"深渊坑")。
	// 电影语法默认同场景连续拍摄——镜头无 scene 时继承上一镜;循环后仍空的用全集最高频场景兜底。
	lastScene := ""
	for _, raw := range raws {
		// 场景匹配:画面内容 + 台词/旁白 + h3_prompt 一起纳入匹配池(脚本场景名常出现在
		// 旁白/六段式里而非画面内容列);未命中再按"别名关键词"兜底(如 小馆/店堂→晚膳小馆)
		scene := ""
		scenePool := raw.Action + " " + raw.Dialogue + " " + raw.Narration + " " + raw.H3Prompt
		for _, sid := range sceneIDs {
			if sid != "" && strings.Contains(scenePool, sid) {
				scene = sid
				break
			}
		}
		if scene == "" {
			scene = matchSceneByAlias(scenePool, sceneCards)
		}
		if scene == "" {
			scene = lastScene // 继承上一镜(同场景连续)
		} else {
			lastScene = scene
		}
		// 登场角色:台词说话人 + 画面内容出现的角色名(缺一不可,谁在场谁入画)
		charSet := map[string]bool{}
		pool := raw.Action + " " + raw.Dialogue + " " + raw.Narration
		for _, cid := range charIDs {
			if cid != "" && strings.Contains(pool, cid) {
				charSet[cid] = true
			}
		}
		// JSON 分镜脚本显式 characters 声明并入(2026-08-31:LLM 拆镜时已判定在场,
		// 文本匹配漏判的(代称/英文名)由声明兜底;说话人仍强制入画)
		// 2026-09-01 变体归一:声明常用简称(杳杳/主持人/小汤),素材卡是完整名
		// (涂山杳杳/天才榜主持人/孟小汤)——精确匹配不上=无参考图=形象漂移主次混乱
		for _, cid := range raw.JSONChars {
			if cid == "" {
				continue
			}
			if allIDs[cid] {
				charSet[cid] = true
				continue
			}
			if rid := manjuResolveCharID(cid, charIDs); rid != "" {
				charSet[rid] = true
			}
		}
		// 2026-08-28 EP01 人物不一致根治:脚本画面叙述常用「男人/屏幕前的男人」代称(镜3
		// h3 写 "Chen Mo in <Picture 1>",画面列只写「屏幕前的男人」),名字匹配判空 →
		// 渲染端不挂人物参考图,h3 的 <Picture 1> 错位指到场景图 → H3 拿城市夜景图当
		// 陈默的长相参考,人物与定妆照完全脱钩。这里用 h3 主体句与角色卡英文提示词的
		// 特征词重叠把漏判人物捞回(有参考图,Picture 编号自然对齐)。
		// declared=当前已判定登场人数(声明/文本匹配/说话人):不少于主体句数时不捞(2026-09-03)。
		if raw.H3Prompt != "" {
			for _, cid := range manjuCharsFromH3(raw.H3Prompt, charCards, charIDs, len(charSet)) {
				charSet[cid] = true
			}
		}
		// 台词说话人强制入画(谁说的就是谁);画外群杂豁免(2026-08-27:画外·前缀
		// =画外群众议论,不入画不占角色名额——叙述群众议论化的解析契约)
		// 2026-09-01 变体归一:说话人简称先归一到素材卡(杳杳→涂山杳杳,不建重复自动卡)
		for _, dm := range reDialogue.FindAllStringSubmatch(raw.Dialogue+" "+raw.Narration, -1) {
			speaker := strings.TrimSpace(dm[1])
			if speaker == "" {
				speaker = strings.TrimSpace(dm[3])
			}
			if speaker != "" && !manjuIsOffScreenSpeaker(speaker) {
				if rid := manjuResolveCharID(speaker, charIDs); rid != "" {
					charSet[rid] = true
				} else {
					charSet[speaker] = true
				}
			}
		}
		// charArr 排序(2026-08-27 群演分级):说话人优先——参考图名额 ≤3,开口的人
		// 必须有脸(超员时台词人物先挂参考图,落选者由渲染纪律兜底),其余按角色名单补位
		spkOrder := []string{}
		for _, dm := range reDialogue.FindAllStringSubmatch(raw.Dialogue+" "+raw.Narration, -1) {
			speaker := strings.TrimSpace(dm[1])
			if speaker == "" {
				speaker = strings.TrimSpace(dm[3])
			}
			if rid := manjuResolveCharID(speaker, charIDs); rid != "" {
				speaker = rid
			}
			if speaker == "" || manjuIsOffScreenSpeaker(speaker) || !charSet[speaker] || !allIDs[speaker] {
				continue
			}
			dup := false
			for _, s := range spkOrder {
				if s == speaker {
					dup = true
					break
				}
			}
			if !dup {
				spkOrder = append(spkOrder, speaker)
			}
		}
		charArr := []any{}
		for _, cid := range spkOrder {
			charArr = append(charArr, cid)
		}
		for _, cid := range charIDs {
			inSpk := false
			for _, s := range spkOrder {
				if s == cid {
					inSpk = true
					break
				}
			}
			if charSet[cid] && !inSpk {
				charArr = append(charArr, cid)
			}
		}
		shot := map[string]any{
			"shot_id":    raw.ID,
			"scene":      scene,
			"shot_size":  raw.ShotSize,
			"camera":     raw.Camera,
			"action":     raw.Action,
			"dialogue":   raw.Dialogue,
			"narration":  raw.Narration,
			"light":      raw.Light,
			"sound":      raw.Sound,
			"style":      raw.Style,
			"duration":   raw.Duration,
			"h3_prompt":  raw.H3Prompt,
			"characters": charArr,
		}
		if raw.H3Prompt == "" {
			missingPrompt++
		}
		shotsArr = append(shotsArr, shot)
	}
	// 2026-08-28 场景收尾三件套(EP01 事故根治:办公室戏 18 镜全挂「城市夜景大远景」封面卡):
	// ①向后继承:精确/特征匹配都未命中且前面没有可继承场景的镜(如桌面特写),从最近的后
	//   续命中镜回填——电影语法特写镜属于前后所在场景,「镜2 桌面特写」归入「镜9 深夜工位」;
	// ②最高频兜底:首镜仍空时用全集最高频场景统一,但需 ≥2 票——1 票当选没有统计意义
	//   (旧逻辑里唯一命中的封面卡 1 票就把 17 个空镜全部传染);
	// ③宁可空不乱挂:全部失效后 scene 留空,渲染端空 scene 不挂场景参考图(h3 文本自述
	//   场景),绝不挂错图撕裂图文。
	for i := len(shotsArr) - 1; i >= 0; i-- {
		m, _ := shotsArr[i].(map[string]any)
		if m == nil || str(m["scene"]) != "" {
			continue
		}
		for j := i + 1; j < len(shotsArr); j++ {
			m2, _ := shotsArr[j].(map[string]any)
			if m2 != nil && str(m2["scene"]) != "" {
				m["scene"] = str(m2["scene"])
				break
			}
		}
	}
	if len(shotsArr) > 0 {
		if s0, _ := shotsArr[0].(map[string]any); s0 != nil && str(s0["scene"]) == "" {
			freq := map[string]int{}
			top, topN := "", 0
			for _, sh := range shotsArr {
				if m, _ := sh.(map[string]any); m != nil {
					if sid := str(m["scene"]); sid != "" {
						freq[sid]++
						if freq[sid] > topN {
							top, topN = sid, freq[sid]
						}
					}
				}
			}
			if topN >= 2 {
				for _, sh := range shotsArr {
					if m, _ := sh.(map[string]any); m != nil && str(m["scene"]) == "" {
						m["scene"] = top
					}
				}
			}
		}
	}
	if missingPrompt > 0 {
		lg.logf(fmt.Sprintf("  📝 脚本有 %d 镜缺六段式提示词,由逐镜生成补齐", missingPrompt))
	}
	directing := scriptDirectingFrom(text)
	plan := map[string]any{
		"episode_title":    scriptEpisodeTitle(text),
		"chapters":         "script",
		"script_parse_ver": manjuScriptParseVer,
		"characters":       charsArr,
		"scenes":           scenesArr,
		"shots":            shotsArr,
		"directing":        directing,
	}
	if len(shotsArr) == 0 {
		return nil, fmt.Errorf("脚本解析后无镜头")
	}
	return plan, nil
}

// scriptValidateShots 分镜脚本机械质检(2026-08-26,对齐爽文技能阶段4.5 storyboard_check):
//   1. [Shot N] At MM:SS.mmm 时间戳跨镜严格递增——递减/重复说明切点错乱,H3 按时间码对齐参考会穿帮;
//   2. 时长多样性——全部镜头同秒数=节奏单一(机械拆镜典型症状);
//   3. 分镜表台词/旁白 ↔ 六段式提示词同步——每句取前 8 字在 h3_prompt 中找不到,说明该句
//      没进 <d> 标记或 overall_soundscape(混沌灵根 953 处缺失的主形态:台词列有、提示词没有,
//      成片「画面有字无配音」)。
// 全部为告警(不阻断渲染):脚本仍是权威,问题留给作者改脚本后重导入。
func scriptValidateShots(raws []scriptShotRaw, lg *manjuLogger) {
	if lg == nil || len(raws) == 0 {
		return
	}
	// 1) 时间戳单调递增(六段式提示词内 At MM:SS.mmm 序列,跨镜拼接后相邻比较)
	reTS := regexp.MustCompile(`At\s*(\d{1,2}):(\d{2})[.,](\d{1,3})`)
	type tsMark struct{ shot, ms int }
	var marks []tsMark
	for _, r := range raws {
		for _, m := range reTS.FindAllStringSubmatch(r.H3Prompt, -1) {
			mm, _ := strconv.Atoi(m[1])
			ss, _ := strconv.Atoi(m[2])
			msStr := m[3]
			for len(msStr) < 3 {
				msStr += "0"
			}
			frac, _ := strconv.Atoi(msStr[:3])
			marks = append(marks, tsMark{r.ID, mm*60000 + ss*1000 + frac})
		}
	}
	for i := 1; i < len(marks); i++ {
		if marks[i].ms <= marks[i-1].ms {
			lg.logf(fmt.Sprintf("  ⚠️ 机械质检: 镜头 %d 的 [Shot N] At 时间戳未严格晚于上一处(镜头 %d)——切点时间码必须递增,请修正脚本", marks[i].shot, marks[i-1].shot))
		}
	}
	// 2) 时长多样性
	durSet := map[int]bool{}
	for _, r := range raws {
		durSet[r.Duration] = true
	}
	if len(raws) >= 6 && len(durSet) == 1 {
		lg.logf(fmt.Sprintf("  ⚠️ 机械质检: %d 镜时长全部 %ds,节奏单一(建议按内容疏密分布,快节奏镜短、抒情镜长)", len(raws), raws[0].Duration))
	}
	// 3) 台词/旁白 ↔ 六段式同步:未同步句**自动补写**进提示词(对白 <d>/旁白画外音 <d>),
	//    2026-08-26 升级:此前仅告警——分镜表台词列有、六段式没有时,渲染真的没有此句配音
	//    (混沌灵根 15 镜实测),「提示用户核对」不如程序直接修好。
	for i := range raws {
		r := &raws[i]
		if r.H3Prompt == "" {
			continue
		}
		miss := 0
		var patch string
		for _, line := range strings.Split(r.Dialogue, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// 剥「说话人:」前缀(2026-08-29 修复:旧 j<16 是字节索引阈值,「群演·贾秘书」
			// 6 汉字=18 字节被挡 → 剥前缀失败 → key 带前缀匹配不上六段式 → 误补写重复 <d>
			// (别惹这盆绿萝 镜头3/4/7 配音重复实锤)。用 rune 级索引,不混字节/rune。
			rs := []rune(line)
			ci := -1
			for i, ch := range rs {
				if ch == ':' || ch == '：' {
					ci = i
					break
				}
			}
			if ci >= 0 {
				line = strings.TrimSpace(string(rs[ci+1:]))
			}
			// 2026-09-02 剥首尾引号(王牌三岁半 EP01 镜15 实锤):台词列「(S25)画外·
			// 观众乙:"联邦史上最低！"」剥前缀后仍带引号 → key 带引号与六段式 <d> 内
			// 无引号台词比对恒 miss → 每次导入误补写一条带引号的重复 <d>(run.log
			// 「镜头 15 检出 2 句未同步已补写」即此)→ 配音重复。仅剥首尾成对引号,
			// 台词正文不变。
			rs = []rune(line)
			if len(rs) >= 2 {
				q0, q1 := rs[0], rs[len(rs)-1]
				if (q0 == '"' && q1 == '"') || (q0 == '“' && q1 == '”') ||
					(q0 == '「' && q1 == '」') || (q0 == '『' && q1 == '』') {
					line = string(rs[1 : len(rs)-1])
				}
			}
			key := string([]rune(line)[:minInt(8, len([]rune(line)))])
			if len([]rune(key)) < 4 || strings.Contains(r.H3Prompt, key) {
				continue
			}
			miss++
			patch += "\n<d>" + line + "</d>"
		}
		// 旁白/内心逐行同步(2026-08-30 五问修复):旧逻辑只剥「旁白」前缀,「内心·角色名:」
		// 前缀留在 key 里恒匹配不上 h3_prompt(LLM 已剥离前缀的正确写法)→ 每次导入都补出
		// 一条带「内心·阿影:」字面量+引号的重复画外音 <d> 句(EP01 镜4/5/6/14 实锤,成片
		// 同一句内心独白念两遍且把标签念出来)。现在逐行处理:剥前缀与引号,行内逐句判定,
		// 任一子句内容命中 h3_prompt 即视为已同步不补。
		for _, narrLine := range strings.Split(r.Narration, "\n") {
			n := stripNarrationPrefix(strings.TrimSpace(narrLine))
			if n == "" {
				continue
			}
			// 逐句判定:行内多句(旁白+内心并存/两句连写)任一子句缺失才补整行
			allHit := true
			for _, sub := range splitNarrationSentences(n) {
				key := string([]rune(sub)[:minInt(8, len([]rune(sub)))])
				if len([]rune(key)) < 4 {
					continue
				}
				if !strings.Contains(r.H3Prompt, key) {
					allHit = false
					break
				}
			}
			if allHit {
				continue
			}
			miss++
			// 官方画外音写法(base-en.txt §4.4):旁白/内心由 H3 直出,不用 TTS
			patch += "\nThe narrator says in an off-screen voiceover: <d>" + n + "</d> while the on-screen characters' lips remain completely closed."
		}
		if miss > 0 {
			// 插到最后一个 </d> 之后(与既有对白同段);无 </d> 则追加末尾
			if k := strings.LastIndex(r.H3Prompt, "</d>"); k >= 0 {
				k += len("</d>")
				r.H3Prompt = r.H3Prompt[:k] + patch + r.H3Prompt[k:]
			} else {
				r.H3Prompt += patch
			}
			lg.logf(fmt.Sprintf("  ✅ 机械质检: 镜头 %d 检出 %d 句台词/旁白/内心未同步进六段式,已自动补写(<d>对白/画外音,渲染含此句配音)", r.ID, miss))
		}
	}
	// 4) 站位完整性 WARN(2026-08-30 五问整改,问题③):detailed_description 段零位置词
	//    =画面人物无屏幕位置/朝向,渲染站位全靠模型自由发挥(EP01 14 镜中 8 镜零位置词
	//    实锤)。只告警不阻断(脚本权威,作者改脚本后重导入)。
	// 5) 运镜 WARN(2026-08-30 五问整改,问题④):运镜列缺失/长固定镜(静态机位+画面
	//    动作衰减=成片趋静)。
	// 6) 动作-对白协调 WARN(2026-08-30 五问整改,问题⑤):有台词镜的画面描述无任何
	//    动作/表情词——说话人可能呆立念白(动作僵硬来源之一)。
	rePosWord := regexp.MustCompile(`(?i)\b(left|right|center|centre|foreground|midground|background|behind|beside|in front of|facing|turned away)\b`)
	reActionWord := regexp.MustCompile(`(?i)\b(turns?|stands?|walks?|raises?|lowers?|looks?|glances?|smiles?|frowns?|clenches?|grips?|steps?|leans?|gestures?|sighs?|nods?|shakes?|opens?|reaches?|moves?|holds?|sits?|rises?|bows?|points?|throws?|catches?|kneels?|waves?|shrugs?|blinks?|stares?|tilts?|rolls?)\b|转身|抬头|低头|握拳|咬牙|皱眉|抬手|迈步|坐下|站起|点头|摇头|开口|微笑|回头|耸肩|眯眼|攥拳|侧身|俯身|背对`)
	for i := range raws {
		r := &raws[i]
		dd := r.H3Prompt
		if a := strings.Index(dd, "detailed_description:"); a >= 0 {
			dd = dd[a+len("detailed_description:"):]
			if b := strings.Index(dd, "overall_soundscape:"); b >= 0 {
				dd = dd[:b]
			}
		} else {
			dd = ""
		}
		if dd == "" {
			continue
		}
		if !rePosWord.MatchString(dd) {
			lg.logf(fmt.Sprintf("  ⚠️ 机械质检: 镜头 %d 画面描述无任何人物位置/朝向词(left/right/center/foreground/facing 等)——站位信息缺失,建议按【位置锚定纪律】补写谁在画面哪侧、谁对着谁", r.ID))
		}
		if cm := strings.TrimSpace(r.Camera); cm == "" {
			lg.logf(fmt.Sprintf("  ⚠️ 机械质检: 镜头 %d 运镜列为空——按默认机位渲染,建议写清运镜(类型+幅度+速度,如 缓推/Push In, small, slow)", r.ID))
		} else if strings.Contains(cm, "固定") && r.Duration >= 6 {
			lg.logf(fmt.Sprintf("  ⚠️ 机械质检: 镜头 %d 为长固定镜(%ds)——静态机位+画面动作衰减=成片趋静,建议改缓推/微摇或缩短时长", r.ID, r.Duration))
		}
		if strings.TrimSpace(r.Dialogue) != "" && !reActionWord.MatchString(dd) {
			lg.logf(fmt.Sprintf("  ⚠️ 机械质检: 镜头 %d 有台词但画面描述无动作/表情词——说话人可能呆立念白,建议补该角色的动作/微表情", r.ID))
		}
	}
	// 7) 内心/旁白双写去重(2026-08-31 用户实测「内心配音重复」的脚本层根源):
	//    LLM 直出时同一句内心/旁白常被写两次——一次 <Subject N> (Sx) says:(内心被
	//    安到画面角色开口,还会乱对嘴型),一次 narrator off-screen voiceover。
	//    程序保留画外音句式那条,删除角色 says 版(内心/旁白禁止角色开口)。
	//    实测形态(第8章镜18):「<Subject 1> (S5) says: <d>放眼皮底下…</d>」+
	//    「The narrator says in an off-screen voiceover: <d>放眼皮底下…</d>」。
	reSubjSaysD := regexp.MustCompile(`<Subject\s+\d+>\s*\(S\d+\)\s+says:.*?<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>`)
	for i := range raws {
		r := &raws[i]
		hp := r.H3Prompt
		if hp == "" || !strings.Contains(hp, "<d>") {
			continue
		}
		// 收集画外音句式 <d> 内容(norm)作为「权威保留集」
		keep := map[string]bool{}
		for _, m := range reOffscreenD.FindAllStringSubmatch(hp, -1) {
			keep[manjuNormText(m[1])] = true
		}
		if len(keep) == 0 {
			continue
		}
		changed := false
		hp = reSubjSaysD.ReplaceAllStringFunc(hp, func(m string) string {
			sm := reSubjSaysD.FindStringSubmatch(m)
			if len(sm) > 1 && keep[manjuNormText(sm[1])] {
				changed = true
				return "" // 该句已在画外音中,删除角色 says 版
			}
			return m
		})
		if changed {
			// 清理残留的空行/多余空格
			hp = regexp.MustCompile(`\n{3,}`).ReplaceAllString(hp, "\n\n")
			r.H3Prompt = strings.TrimSpace(hp)
			lg.logf(fmt.Sprintf("  ✅ 机械质检: 镜头 %d 内心/旁白双写已去重(保留 off-screen voiceover 版,删除角色开口版——防重复配音与乱对嘴型)", r.ID))
		}
	}
}

// reOffscreenD 画外音句式 <d> 内容提取(去重保留集)
var reOffscreenD = regexp.MustCompile(`in an off-?screen voiceover:.*?<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>`)

// scriptDedupShotLines 跨镜/镜内台词去重(2026-09-01 配音重复根治,用户反馈
// 「配音不对,老是重复;镜头变多后更厉害」的渲染端兜底)。实证形态:
//   - EP01 镜1(总览镜)detailed_description 写「老赵问 + 姜缺两答」三句,
//     镜2/3 又各重复一句 → 同一句台词被 H3 念两遍;
//   - 第23章镜4/5/17 同镜内半角/全角标点双版本各写一遍 → 镜内重复。
// 规则(以分镜表台词列为权威,逐句归属):
//   ① 每镜权威集 = 该镜 dialogue 引号内台词 + narration(剥前缀)归一化;
//   ② <d> 台词归在本镜权威集(精确相等或本镜权威是其子串——合并句形态,
//      第10章镜11 h3 一句=台词列 4 句合并)→ 保留(本镜该念);
//   ③ 不在本镜权威集、但与其它镜权威集**整句精确相等** → 判定「总览镜串句」,
//      删除该 <d> 句(连同引导语,如 "He replies without looking up, his tone
//      flat:"——回溯到最近句号/换行,不残留悬空引导语);
//   ④ 任何镜权威集都不在(LLM 自由发挥的群众呼喊/即兴台词,如第10章镜13
//      「哎哟…疼…」)→ 保留,绝不误删;
//   ⑤ 同镜内同一句出现 ≥2 次(半角/全角双版本)→ 只保留第一处;
//   ⑥ 极短词(归一化后 <2 字,如「无」「吃」「…」)→ 不参与任何判定,保留
//      ——避免误删刻意呼应(第42章 16-21 镜连环「无」是剧情设计)。
// 跨镜判定只用整句精确相等(不用子串):跨镜串句形态是「整句重复」,子串包含
// 在跨镜场景误删风险高(短句撞长句)、收益低。删除后清理残留空行;幂等。
func scriptDedupShotLines(raws []scriptShotRaw, lg *manjuLogger) {
	if len(raws) < 2 {
		return
	}
	// 每镜权威集:本镜台词列(引号内)+ 旁白/内心(剥前缀)归一化。
	// dialogue 按行拆再提取:无引号台词形态 `(Sx)角色:内容` 的 [^"”]+ 字符类
	// 含换行,不拆行会把多句台词吞成一句合并 norm(第50章镜15 实锤)→ 精确
	// 归属判定失败,跨镜重复漏删。
	dialogueLines := func(d string) []string {
		var out []string
		for _, l := range strings.Split(d, "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	auths := make([]map[string]bool, len(raws))
	for i := range raws {
		set := map[string]bool{}
		for _, line := range dialogueLines(raws[i].Dialogue) {
			for _, seg := range manjuDialogueContents(line) {
				if n := manjuNormText(seg); len([]rune(n)) >= 2 {
					set[n] = true
				}
			}
		}
		for _, seg := range strings.Split(raws[i].Narration, "\n") {
			body := stripNarrationPrefix(strings.TrimSpace(seg))
			if n := manjuNormText(body); len([]rune(n)) >= 2 {
				set[n] = true
			}
		}
		auths[i] = set
	}
	// 每镜 h3_prompt 中所有 <d> 台词(含 [Chinese]/[中文] 语言标签变体)
	reD := regexp.MustCompile(`<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>`)
	for i := range raws {
		hp := raws[i].H3Prompt
		if hp == "" || !strings.Contains(hp, "<d>") {
			continue
		}
		seenInShot := map[string]bool{} // 镜内去重
		changed := false
		var kept []string
		pos := 0
		for _, m := range reD.FindAllStringSubmatchIndex(hp, -1) {
			raw := hp[m[2]:m[3]]
			n := manjuNormText(raw)
			seg := hp[pos:m[0]] // 本段台词前的原文(含引导语)
			// ⑥ 极短词不参与**跨镜**判定(避免误删刻意呼应,第42章 16-21 镜
			// 连环「无」是剧情设计);但同镜内重复念两遍仍是真重复,照常去重。
			if len([]rune(n)) < 2 {
				if seenInShot[n] {
					lg.logf(fmt.Sprintf("  🎙 配音去重: 镜头 %d 同镜重复台词「%s」已删(保留第一处)", raws[i].ID, firstN(raw, 20)))
					changed = true
					pos = m[1]
					continue
				}
				seenInShot[n] = true
				kept = append(kept, seg, hp[m[0]:m[1]])
				pos = m[1]
				continue
			}
			// ① 本镜权威(精确或合并句包含)→ 保留
			if auths[i][n] || authContains(auths[i], n) {
				if seenInShot[n] {
					// ⑤ 镜内重复:同镜已保留过此句,删后续(连同引导语)
					lg.logf(fmt.Sprintf("  🎙 配音去重: 镜头 %d 同镜重复台词「%s」已删(保留第一处)", raws[i].ID, firstN(raw, 20)))
					changed = true
					pos = m[1]
					continue
				}
				seenInShot[n] = true
				kept = append(kept, seg, hp[m[0]:m[1]])
				pos = m[1]
				continue
			}
			// ②③ 不在本镜权威 → 与其它镜权威集整句精确比对(总览镜串句形态)
			owner := manjuShotOwnerOf(raws, raw, n, i)
			if owner == 0 {
				// ④ 任何镜都不在 → 即兴台词,保留
				kept = append(kept, seg, hp[m[0]:m[1]])
				pos = m[1]
				continue
			}
			// 串句:删引导语(回溯最近句号/换行,防悬空)+ <d> 句
			leadStart := m[0]
			for k := m[0] - 1; k >= pos-1 && m[0]-k <= 200; k-- {
				if k < 0 || hp[k] == '.' || hp[k] == '\n' {
					leadStart = k + 1
					break
				}
			}
			if leadStart < pos {
				leadStart = pos
			}
			kept = append(kept, hp[pos:leadStart])
			lg.logf(fmt.Sprintf("  🎙 配音去重: 镜头 %d 串句「%s」已删(该句属镜 %d 台词,防跨镜重复配音)", raws[i].ID, firstN(raw, 20), owner))
			changed = true
			pos = m[1]
		}
		if !changed {
			continue
		}
		kept = append(kept, hp[pos:])
		out := strings.Join(kept, "")
		out = regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")
		out = strings.TrimSpace(out)
		raws[i].H3Prompt = out
	}
}

// manjuDialogueContents 单行台词列 → 台词内容列表(去说话人前缀/引号)。
// reDialogue 主路径之外兜底两种畸形形态(2026-09-01 实测):
//   - `(S1)白泽·小白:""可账册…"` 空引号占位+内容(第155章镜9):reDialogue
//     只匹配到空引号 → 内容落空 → 权威集缺句 → 跨镜归属判定失败;
//   - 无引号句 `(S1)老赵:今儿大比` 的引号残留。
// 主路径(引号内+无引号)优先,兜底取剥前缀后剩余非空段。
func manjuDialogueContents(line string) []string {
	line = strings.TrimSpace(line)
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.Trim(s, "\"“”'‘’ ")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	// 主路径:reDialogue 引号内(m[2])/无引号(m[4])
	for _, m := range reDialogue.FindAllStringSubmatch(line, -1) {
		seg := strings.TrimSpace(m[2])
		if seg == "" {
			seg = strings.TrimSpace(m[4])
		}
		add(seg)
	}
	if len(out) > 0 {
		return out
	}
	// 兜底:剥 (Sx)说话人: 前缀后取剩余(空引号占位形态)
	if i := strings.Index(line, "("); i >= 0 {
		if j := strings.IndexAny(line[i:], "：:"); j >= 0 {
			rest := line[i+j:]
			_, size := utf8.DecodeRuneInString(rest)
			add(strings.TrimSpace(rest[size:]))
			if len(out) > 0 {
				return out
			}
		}
	}
	// 最终兜底:整行剥引号(无说话人前缀的裸台词行)
	add(line)
	return out
}

// authContains 权威集包含判定:本镜权威条目是台词 n 的子串(处理 h3 中台词
// 被合并的形态——第10章镜11 h3 一句「杂役听令!…逐出宗门!」= 台词列 4 句合并)。
func authContains(set map[string]bool, n string) bool {
	for a := range set {
		if len([]rune(a)) >= 4 && strings.Contains(n, a) {
			return true
		}
		if len([]rune(n)) >= 4 && strings.Contains(a, n) {
			return true
		}
	}
	return false
}

// manjuShotOwnerOf 查台词 n 精确归属于哪一镜(串句删除判定/日志;无归属返回 0)。
// dialogue 按行拆再提取(同 scriptDedupShotLines 注释:无引号台词跨行吞句会
// 使合并 norm 无法精确命中,串句漏删)。
func manjuShotOwnerOf(raws []scriptShotRaw, raw, n string, skip int) int {
	for j := range raws {
		if j == skip {
			continue
		}
		for _, line := range strings.Split(raws[j].Dialogue, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, seg := range manjuDialogueContents(line) {
				if manjuNormText(seg) == n {
					return raws[j].ID
				}
			}
		}
		for _, seg := range strings.Split(raws[j].Narration, "\n") {
			if manjuNormText(stripNarrationPrefix(strings.TrimSpace(seg))) == n {
				return raws[j].ID
			}
		}
		// 合并句归属(2026-09-01):h3 中一句台词可能是该镜台词列多行合并
		// (第40章夜枭镜18 内心三行合并成一句、镜19 整句重复)——整句 norm 与
		// 任一行都不等 → 精确命中失败 → 串句漏删。用原始文本按句末标点拆
		// 子句,每一子句都能在该镜权威行中精确找到 → 归属该镜。
		if len([]rune(raw)) >= 8 {
			clauses := splitNarrationSentences(raw)
			if len(clauses) >= 2 {
				all := true
				for _, c := range clauses {
					cn := manjuNormText(c)
					found := false
					for _, seg := range strings.Split(raws[j].Narration, "\n") {
						if manjuNormText(stripNarrationPrefix(strings.TrimSpace(seg))) == cn {
							found = true
							break
						}
					}
					if !found {
						all = false
						break
					}
				}
				if all {
					return raws[j].ID
				}
			}
		}
	}
	return 0
}

// manjuNormText 台词/提示词片段归一(去空白与常见标点,去重比对用)
func manjuNormText(s string) string {
	return regexp.MustCompile(`[\s,，。.．!！?？\-—·、:；:;"'‘’“”()\[\]{}<>《》【】~～…]+`).ReplaceAllString(s, "")
}

// stripNarrationPrefix 剥旁白/内心行前缀与杂质,取纯内容(2026-08-30 五问整改):
// 「内心·角色名:内容。」「旁白:内容。」「内容(S1)」统一归一为「内容」——
// 前缀/引号/说话人标注留在补写 key 里会导致与 h3_prompt 的内容命中判定失败,
// 每次导入都补出带「内心·阿影:」字面量的重复画外音 <d> 句(EP01 镜4/5/6/14 实锤)。
func stripNarrationPrefix(line string) string {
	// 1) 「内心·角色名:」前缀:取角色名后第一个冒号之后为内容
	if i := strings.Index(line, "内心·"); i >= 0 {
		rest := line[i+len("内心·"):]
		// 2026-09-01 全角冒号字节修复:strings.IndexAny 返回字节索引,「：」是
		// 3 字节 UTF-8(EF BC 9A),j+1 只跳 1 字节会把 \xbc\x9a 尾字节残留进
		// 内容(第102章镜24 实锤:auth key 变 \xbc\x9a本狐偷东西吃… → 与 h3
		// <d> 台词 norm 失配 → 跨镜去重/归属判定全失效)。按 rune 完整跳过。
		if j := strings.IndexAny(rest, "：:"); j >= 0 {
			_, size := utf8.DecodeRuneInString(rest[j:])
			rest = rest[j+size:]
		}
		line = rest
	}
	// 2) 「旁白」前缀与冒号
	line = strings.TrimPrefix(line, "旁白")
	line = strings.TrimPrefix(strings.TrimPrefix(line, ":"), "：")
	// 3) (Sx) 说话人标注(可能出现在内容尾部)
	if j := strings.Index(line, "(S"); j >= 0 {
		line = line[:j]
	}
	// 4) 首尾引号(全半角/弯引号)与收尾标点循环剥(交替出现时一轮剥不干净)
	for {
		before := line
		line = strings.Trim(line, "「」『』\"'“”‘’ ")
		line = strings.Trim(line, "。！？.!?…~ ")
		if line == before {
			break
		}
	}
	line = strings.TrimSpace(line)
	// 5) 中间成对引号(「"句一。""句二。"」)→ 句号,逐句判定才能拆开
	line = strings.ReplaceAll(line, "\"\"", "。")
	line = strings.ReplaceAll(line, "''", "。")
	line = strings.TrimSpace(line)
	// 6) 内容尾部补规范句号(补写 <d> 内容以「。」收尾,与原文风格一致)
	line = line + "。"
	// 防御:剥完为空或只剩单字(内容过短无判定意义)
	if len([]rune(line)) < 2 {
		return ""
	}
	return line
}

// splitNarrationSentences 按中文句末标点拆句(保留标点;用于补写判定逐句命中)
func splitNarrationSentences(n string) []string {
	var out []string
	var cur []rune
	for _, ch := range n {
		cur = append(cur, ch)
		if ch == '。' || ch == '！' || ch == '？' {
			out = append(out, string(cur))
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// parseScriptAssetCards 从素材解析角色/场景卡:
//   - workdir/素材/人物生成提示词.md → characters(id/age/gender/appearance/costume/image_prompt)
//   - workdir/素材/场景提示词.md     → scenes(id/description/image_prompt)
// 缺素材文件时返回空卡列表(角色卡缺失会在后续由 plan 兜底/角色管理补)。
// 素材文件可能 GBK/UTF-8 混合,统一 toUTF8。
// 2026-08-24 修复:自动切脚本直出(小说目录导入)时素材在 novel_dir/素材/,不只查 workdir——
// 两个位置都找(workdir 优先,novel_dir 兜底)。
func (ctx *manjuCtx) parseScriptAssetCards(lg *manjuLogger) (chars []map[string]any, scenes []map[string]any) {
	roots := []string{}
	if ctx.workdir != "" {
		roots = append(roots, filepathJoin(ctx.workdir, "素材"))
	}
	if d := ctx.novelRootDir(); d != "" {
		roots = append(roots, filepathJoin(d, "素材"))
	}
	// 素材解析失败不阻断方案:返回空,调用方回退 LLM 或角色管理补
	// 2026-08-31 JSON 素材卡优先(技能侧新格式),md 兼容旧项目
	charFile := firstExisting(roots, "人物生成提示词.json")
	if charFile == "" {
		charFile = firstExisting(roots, "人物生成提示词.md")
	}
	sceneFile := firstExisting(roots, "场景提示词.json")
	if sceneFile == "" {
		sceneFile = firstExisting(roots, "场景提示词.md")
	}
	assetStyle := manjuAssetStyle(ctx.style)

	if charFile != "" {
		if b, err := os.ReadFile(charFile); err == nil {
			if strings.HasSuffix(charFile, ".json") {
				chars = parseCharCardsJSON(string(toUTF8(b)))
			} else {
				chars = parseCharCards(string(toUTF8(b)), assetStyle, manjuStyleIs3D(ctx.style))
			}
		}
	}
	if sceneFile != "" {
		if b, err := os.ReadFile(sceneFile); err == nil {
			if strings.HasSuffix(sceneFile, ".json") {
				scenes = parseSceneCardsJSON(string(toUTF8(b)))
			} else {
				scenes = parseSceneCards(string(toUTF8(b)), assetStyle, manjuStyleIs3D(ctx.style))
			}
		}
	}
	if len(chars) == 0 {
		lg.logf("  ⚠️ 素材/人物生成提示词(.json/.md) 未找到或解析为空,角色卡需由角色管理补")
	}
	return chars, scenes
}

// parseCharCardsJSON 解析 人物生成提示词.json → characters 卡(2026-08-31 技能侧
// 新格式,md 弃用)。字段:id/name/gender/age/role/species/appearance/costume/voice/
// memories/image_prompt/q_form/second_form/minor;与 md 解析输出同构(渲染端
// 消费字段一致)。
func parseCharCardsJSON(text string) []map[string]any {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return nil
	}
	var out []map[string]any
	for _, c := range arr {
		id := str(c["id"])
		if id == "" {
			id = str(c["name"])
		}
		if id == "" {
			continue
		}
		age := str(c["age"])
		// 2026-09-01 age 兜底:image_prompt 里的 "N-year-old"(JSON 素材 age 缺失时,
		// 如「(人类·男,星陨组织头目)」无年龄数字但提示词带 45-year-old——年龄档位
		// 决定 Q 版皱纹/音色/老年特征,缺失=Q 版无年龄感)
		if age == "" {
			if m := regexp.MustCompile(`(\d+)-year-old|(\d+)\s*岁`).FindStringSubmatch(str(c["image_prompt"])); len(m) > 1 {
				if m[1] != "" {
					age = m[1] + "岁"
				} else {
					age = m[2]
				}
			}
		}
		card := map[string]any{
			"id":           id,
			"gender":       str(c["gender"]),
			"age":          age,
			"appearance":   str(c["appearance"]),
			"costume":      str(c["costume"]),
			"image_prompt": str(c["image_prompt"]),
			"views":        map[string]any{},
		}
		for k, v := range map[string]string{
			"role": "role", "species": "species", "voice": "voice",
			"memories": "memories", "q_form": "q_form", "second_form": "second_form",
		} {
			if s := str(c[k]); s != "" {
				card[v] = s
			}
		}
		if b, _ := c["minor"].(bool); b {
			card["minor"] = true
		}
		out = append(out, card)
	}
	return out
}

// parseSceneCardsJSON 解析 场景提示词.json → scenes 卡(2026-08-31 新格式)
func parseSceneCardsJSON(text string) []map[string]any {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return nil
	}
	var out []map[string]any
	for _, s := range arr {
		id := str(s["id"])
		if id == "" {
			id = str(s["name"])
		}
		if id == "" {
			continue
		}
		card := map[string]any{
			"id":          id,
			"description": str(s["description"]),
			"image_prompt": str(s["image_prompt"]),
		}
		out = append(out, card)
	}
	return out
}

// parseCharCards 解析 人物生成提示词.md → characters 卡。
// 格式:## N. 名字（女主，22岁，身份） 或 ## 2.1 主角 · 顾烬（男主，烛龙血脉） + 英文 image_prompt。
// image_prompt 支持两种写法:```代码块``` 或 行内 "**生图提示词**：Cinematic..."(吞噬山海素材格式)。
// 2026-08-26 is3D:次世代3D/BJD 风格分档——不做写实词替换、附 3D 虚拟人锚+正面人脸锚
// (用户反馈"选写实/3D 风格却渲染成动漫形象"的根因是旧链路强制 stylized illustration/painterly)。
func parseCharCards(text, assetStyle string, is3D bool) []map[string]any {
	var out []map[string]any
	sections := splitMdSections(text)
	for _, sec := range sections {
		tm := reMdTitle.FindStringSubmatch(sec.head)
		if tm == nil {
			continue
		}
		name := scriptCleanName(tm[1])
		desc := strings.TrimSpace(tm[2])
		if name == "" {
			continue
		}
		// 全局段过滤(2026-08-26 用户实测:顶流遗产把「统一风格前缀(所有角色共用)」等
		// 全局共用字段写成二级标题 → reMdTitle 匹配成角色卡,产出伪角色+伪视图资产。
		// 创作侧契约=全局段一级标题、角色卡二级标题;此处黑名单兜底兼容存量书与格式漂移。
		if reManjuGlobalSection.MatchString(name) {
			continue
		}
		block := scriptMainBlock(sec.body)
		if block == "" {
			// 行内英文提示词:支持 "**生图提示词**：Cinematic..." / "生图提示词（磕碜反派）**：Cinematic..."
			// (关键词可被 ** 加粗包裹,可带括号后缀,冒号前可有任意星号/括号)
			if im := regexp.MustCompile(`(?s)(?:生图提示词|正向提示词|正向|提示词)(?:（[^）]*）)?\s*\*{0,2}\s*[：:]\s*(Cinematic[^\n]*)`).FindStringSubmatch(sec.body); im != nil {
				block = strings.TrimSpace(im[1])
			}
		}
		gender := scriptGenderOf(sec.head, desc, block)
		if gender == "" {
			// 素材契约(2026-08-27 群演卡):括号描述首段=性别(「男,老年,白发老仆」)——
			// 群演卡无 女主/男主 身份词与中文称谓,scriptGenderOf 判不出时按首段权威取
			seg := desc
			if i := strings.IndexAny(seg, ",,"); i > 0 {
				seg = seg[:i]
			}
			seg = strings.TrimSpace(seg)
			if seg == "男" || seg == "女" {
				gender = seg
			}
		}
		// 身份词画像(2026-08-26):「盟友 · 陈墨」「灵宠 · 吞吞」等身份前缀解析出 role/species——
		// id 已去前缀,beast/性别/胡须/Q 版兽形/兽形角色板全靠卡上的 species/role 判定。
		// 2026-08-29 提前解析:image_prompt 构建就要用物种路由(物品不烤人形锚)
		role, species := scriptRoleSpecies(sec.head)
		isItem := manjuIsItem(map[string]any{"species": species, "role": role, "id": name, "image_prompt": block})
		card := map[string]any{
			"id":           name,
			"gender":       gender,
			"age":          scriptAgeOf(desc + " " + block),
			"appearance":   scriptAppearanceOf(sec.body, block),
			"costume":      "",
			"image_prompt": scriptImagePrompt(block, assetStyle, is3D, isItem),
			"views":        map[string]any{},
		}
		// 音色字段(2026-08-30 ver15 技能侧配置优先):角色卡节内「音色:/声线:/方言:」
		// 行 → card["voice"],渲染端 dialectVoiceFor(方言)/voiceTimbrePhrase 优先使用。
		// 技能侧约定:配角/喜剧/地域角色可配热门方言(东北/陕西/粤/台),主角/正派
		// 默认普通话档位,同剧角色音色互异(分得清谁是谁)。
		if vm := reCharVoiceLine.FindStringSubmatch(sec.body); vm != nil {
			if v := strings.TrimSpace(vm[1]); v != "" && !strings.HasPrefix(v, "（") {
				card["voice"] = v
			}
		}
		if role != "" {
			card["role"] = role
		}
		if species != "" {
			card["species"] = species
		}
		// 群演轻量卡(2026-08-27 群演分级·技能侧源头):爽文技能素材直出的「群演 · 名字」
		// 条目 → minor:true(资产阶段只出 1 张定妆照+正脸,跳过视图/Q版);说话人在
		// characters 内=挂参考图锁形象。与 scriptMinorCast 兜底互补:素材直出优先
		// (形象由作者掌控),素材缺勤的说话人由解析器从六段式主体描述自动补卡。
		if strings.Contains(sec.head, "群演") {
			card["minor"] = true
			card["role"] = "群演"
		}
		// 双形态(2026-08-26):素材角色节含「真身提示词」标注(萌宠 Q版↔神话真身、人形↔兽形)
		// → second_form 字段,assets 阶段额外定妆 <id>_form2.png,渲染遇「真身·<角色名>」镜切换
		if f2 := scriptSecondForm(sec.body); f2 != "" {
			card["second_form"] = scriptImagePrompt(f2, assetStyle, is3D, isItem)
		}
		// Q版专属段(2026-08-29 阿影实锤):非人角色的【Q版·内心戏专用提示词】独立代码块
		// → q_form,渲染端 manjuQPrompt 优先取它(卡内作者措辞=Q版形象的权威定义),
		// 兜底才是兽形 chibi 模板拼接。
		if q := scriptQForm(sec.body); q != "" {
			card["q_form"] = q
		}
		out = append(out, card)
	}
	return out
}

// reTrueFormInline 行内第二形态标注:「真身提示词:Cinematic...」(兼容 化形/原形/兽形/第二形态
// 变体、括号后缀、可加粗;与素材「生图提示词」行内格式同风格,供技能文档约定)
var reTrueFormInline = regexp.MustCompile(`(?:真身|化形|原形|兽形|第二形态)(?:（[^）]*）)?(?:生图)?提示词(?:（[^）]*）)?\s*\*{0,2}\s*[：:]\s*((?:Cinematic|Next-gen)[^\n]*)`)

// scriptSecondForm 提取角色素材节的第二形态(真身/化形)提示词:
// ①行内标注「真身提示词:Cinematic...」(推荐格式,技能文档已约定);
// ②节内多个代码块时,主形象块之外、上方 150 字符内含 真身/化形/原形/兽形/第二形态 字样的块。
// 返回原始提示词(拟漫净化由调用方 scriptImagePrompt 统一处理);未标注返回空。
func scriptSecondForm(body string) string {
	if m := reTrueFormInline.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(m[1])
	}
	blocks := reMdCodeBlock.FindAllStringSubmatchIndex(body, -1)
	if len(blocks) < 2 {
		return ""
	}
	hasKw := func(s string) bool {
		for _, k := range []string{"真身", "化形", "原形", "兽形", "第二形态"} {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}
	ctxBefore := func(i int) string {
		start := 0
		if i > 0 {
			start = blocks[i-1][1]
		}
		if blocks[i][0]-start > 150 {
			start = blocks[i][0] - 150
		}
		return body[start:blocks[i][0]]
	}
	mainIdx := -1
	for i := range blocks {
		if !hasKw(ctxBefore(i)) {
			mainIdx = i
			break
		}
	}
	for i := range blocks {
		if i <= mainIdx {
			continue
		}
		if hasKw(ctxBefore(i)) {
			return strings.TrimSpace(body[blocks[i][2]:blocks[i][3]])
		}
	}
	return ""
}

// scriptMainBlock 角色节主形象代码块:第一个上方标记窗(150字符)内不含
// Q版/真身/化形/原形/兽形/第二形态 标记的代码块。2026-08-29 阿影实锤:
// 【Q版·内心戏专用提示词】段写在主形象段之前,旧逻辑恒取第一个代码块——
// Q版黑团子提示词被当主形象拼人形风格锚渲染,影灵主图出银白团子(黑↔白串色),
// 影子形态正主提示词被顶掉。全部带标记时回退第一个块(保底不空)。
func scriptMainBlock(body string) string {
	blocks := reMdCodeBlock.FindAllStringSubmatchIndex(body, -1)
	if len(blocks) == 0 {
		return ""
	}
	hasKw := func(s string) bool {
		for _, k := range []string{"真身", "化形", "原形", "兽形", "第二形态", "Q版", "Q 版"} {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}
	for i := range blocks {
		start := 0
		if i > 0 {
			start = blocks[i-1][1]
		}
		if blocks[i][0]-start > 150 {
			start = blocks[i][0] - 150
		}
		if !hasKw(body[start:blocks[i][0]]) {
			return strings.TrimSpace(body[blocks[i][2]:blocks[i][3]])
		}
	}
	return strings.TrimSpace(body[blocks[0][2]:blocks[0][3]])
}

// scriptQForm 角色节 Q 版专属提示词块:上方标记窗内含「Q版」标记的代码块
// (与 scriptSecondForm 同款定位法);未标注返回空(渲染端兜底 chibi 模板)。
func scriptQForm(body string) string {
	blocks := reMdCodeBlock.FindAllStringSubmatchIndex(body, -1)
	for i := range blocks {
		start := 0
		if i > 0 {
			start = blocks[i-1][1]
		}
		if blocks[i][0]-start > 150 {
			start = blocks[i][0] - 150
		}
		ctx := body[start:blocks[i][0]]
		if strings.Contains(ctx, "Q版") || strings.Contains(ctx, "Q 版") {
			return strings.TrimSpace(body[blocks[i][2]:blocks[i][3]])
		}
	}
	return ""
}

// parseSceneCards 解析 场景提示词.md → scenes 卡。
// 格式:### 场景一 · 烛龙村（雪夜废墟） 或 ## 1. 晚膳小馆（主角主场） + 英文 image_prompt 代码块。
// 只收"场景N·名字"或"编号. 名字"形式的标题,过滤"主场场景清单/色锚系统"等说明性大节。
// 2026-08-29 三级兜底:纯名字标题「## 星海大厦外景」(绿萝第三代格式)——要求节内有
// 提示词代码块才算卡(护栏,防误收说明性小节),全局段黑名单双保险。
func parseSceneCards(text, assetStyle string, is3D bool) []map[string]any {
	var out []map[string]any
	sections := splitMdSections(text)
	for _, sec := range sections {
		tm := reSceneTitle.FindStringSubmatch(sec.head)
		if tm == nil {
			tm = reMdTitle.FindStringSubmatch(sec.head)
		}
		bare := false
		if tm == nil {
			bt := reSceneBareTitle.FindStringSubmatch(sec.head)
			if bt == nil {
				continue
			}
			tm = []string{bt[0], bt[1], ""}
			bare = true
		}
		name := scriptCleanName(tm[1])
		// 过滤说明性标题(场景清单/色锚系统/用途等,非具体场景卡)
		if name == "" || strings.Contains(name, "场景清单") || strings.Contains(name, "色锚") || strings.Contains(name, "清单") || strings.Contains(name, "系统") || strings.Contains(name, "说明") {
			continue
		}
		// 纯名字兜底分支的护栏:全局共用段(统一风格/通用负向等写成二级标题的格式漂移)
		// 与无提示词的空节都不是场景卡
		if bare && (reManjuGlobalSection.MatchString(name) || !reMdCodeBlock.MatchString(sec.body)) {
			continue
		}
		block := ""
		if bm := reMdCodeBlock.FindStringSubmatch(sec.body); bm != nil {
			block = strings.TrimSpace(bm[1])
		}
		if block == "" {
			// 行内英文提示词:"**提示词**：Cinematic..." / "**生图提示词**：Cinematic..."
			if im := regexp.MustCompile(`(?s)(?:提示词|生图提示词|正向提示词)(?:（[^）]*）)?\s*\*{0,2}\s*[：:]\s*(Cinematic[^\n]*)`).FindStringSubmatch(sec.body); im != nil {
				block = strings.TrimSpace(im[1])
			}
		}
		if block == "" && !strings.Contains(sec.head, "场景") {
			continue // 无提示词且标题不含"场景"字样的节不是场景卡
		}
		// description:视觉锚定物/光线行(取非代码块中文)
		desc := strings.TrimSpace(tm[2])
		for _, line := range strings.Split(sec.body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "```") || strings.Contains(line, "Cinematic") {
				continue
			}
			line = strings.TrimPrefix(line, "-")
			line = strings.TrimSpace(line)
			if line != "" {
				if desc != "" {
					desc += "；"
				}
				desc += line
			}
		}
		out = append(out, map[string]any{
			"id":          name,
			"description": desc,
			"image_prompt": scriptScenePrompt(block, assetStyle, is3D),
		})
	}
	return out
}

// scriptScenePrompt 场景卡 image_prompt 组装(2026-08-24 用户反馈:场景图出人物):
// 场景图必须空场景无人——①不附加人物锚(manjuPortraitAnchor 含 "East Asian/Chinese character" 会诱导画人物,
// 此前误用 scriptImagePrompt 导致场景图渲染出角色);②替换 photorealistic→semi-realistic stylized;
// ③强制附加 manjuSceneAnchor("empty scene, no people") + 多重 no people 排除词。
// 2026-08-26 is3D 分档:3D 风格不做写实词替换(3D 渲染语汇直接生效),锚用 manju3DSceneAnchor。
func scriptScenePrompt(block, assetStyle string, is3D bool) string {
	if is3D {
		if block == "" {
			return manju3DSceneAnchor + ", no people, no humans, no characters, no figures, no silhouettes"
		}
		p := block
		if !strings.Contains(p, "3D rendered") && !strings.Contains(p, "3D render") {
			p = manju3DSceneAnchor + ", " + p
		}
		for _, w := range []string{", villagers", ", villager", ", people", ", crowd", ", humans", ", figures", ", soldiers", ", black-armored soldiers", ", soldiers in black armor"} {
			p = strings.ReplaceAll(p, w, "")
		}
		return p + ", no people, no humans, no characters, no figures, no silhouettes"
	}
	if block == "" {
		return manjuSceneAnchor + ", " + assetStyle + ", no people, no humans, no characters, no figures, no silhouettes"
	}
	p := block
	for _, re := range []struct{ old, neu string }{
		{"photorealistic", "semi-realistic stylized"},
		{"realistic photo", "stylized illustration"},
		{"real human", "environment"},
		{"realistic photograph", "stylized illustration"},
		{"photograph", "painterly illustration"},
	} {
		p = strings.ReplaceAll(p, re.old, re.neu)
	}
	// 场景锚强制前置(空场景无人,防模型画人物)
	if !strings.Contains(p, "empty scene") {
		p = manjuSceneAnchor + ", " + p
	}
	// no people 排除词(场景 prompt 里若有 人物/村民/村民/人群 等词也清掉)
	for _, w := range []string{", villagers", ", villager", ", people", ", crowd", ", humans", ", figures", ", soldiers", ", black-armored soldiers", ", soldiers in black armor"} {
		p = strings.ReplaceAll(p, w, "")
	}
	if assetStyle != "" && !strings.Contains(p, "stylized CG") && !strings.Contains(p, "ink wash") && !strings.Contains(p, "East Asian art") {
		p = p + ", " + assetStyle
	}
	p = p + ", no people, no humans, no characters, no figures, no silhouettes"
	return p
}

// scriptCleanName 清洗标题名字:去 "2.1 "/"主角 · "/"场景一 · "/"女主 · " 等前缀与多余空白。
func scriptCleanName(raw string) string {
	s := strings.TrimSpace(raw)
	// 去开头编号前缀 "2.1 " / "2. " / "1 " (形如 "2.1 主角 · 顾烬" → "主角 · 顾烬")
	s = regexp.MustCompile(`^\d+(?:\.\d+)?\s*[\.、]?\s*`).ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	// 去 主角/女主/反派/助攻/调剂位/工具人/工具人反派/传说位/昆仑守山神 等身份词 + 分隔符
	// 2026-08-26 补盟友/灵宠/坐骑/妖兽/神兽等身份词(与 reMdTitle 身份词组同步)——
	// 此前「盟友 · 陈墨」「灵宠 · 吞吞」整串进角色 id,产物文件名带前缀且性别/种族画像失准
	segs := []string{"主角", "女主", "男主", "男配", "女配", "反派", "助攻", "盟友", "调剂位", "工具人反派", "工具人", "传说位", "昆仑守山神", "灵宠", "兽宠", "宠物", "坐骑", "妖兽", "神兽", "精怪"}
	for _, seg := range segs {
		if strings.HasPrefix(s, seg) {
			s = strings.TrimPrefix(s, seg)
			s = strings.TrimLeft(s, " ·:：—")
			break
		}
	}
	// 去 场景N 前缀(场景卡名字如 "烛龙村" 保留;若残留 场景X 则去之)
	if strings.HasPrefix(s, "场景") {
		s = regexp.MustCompile(`^场景[一二三四五六七八九十]+\s*[·:：\-—]\s*`).ReplaceAllString(s, "")
	}
	return strings.TrimSpace(s)
}

// scriptImagePrompt 角色 image_prompt 组装(2026-08-24 用户规则升级:写实拟动漫,禁日漫+禁真人):
// 素材英文提示词原样保留外观/服装描述,但强制:
//  1) 替换 photorealistic/realistic photo/real human 等真人写实措辞 → 半写实拟动漫表述;
//  2) 追加统一拟动漫正向锚 manjuPortraitAnchor(东方/中式面孔,非日漫,非真人,防侵权)。
// 与 LLM 直出方案的 image_prompt 同口径(锚一致),保证定妆照与视频画风统一。
// 2026-08-26 is3D 分档(用户反馈"选写实/3D 风格却渲染成动漫形象"):次世代3D/BJD 风格
// 不做写实词替换、不拼插画风 assetStyle、不附插画风锚——3D 渲染虚拟人本身非真人照片,
// 改附 manju3DPortraitAnchor + 正面人脸锚(用户规则:人物角色提示词必须正面人脸)。
// scriptImagePrompt 素材代码块 → image_prompt(物种路由版,2026-08-29 审计 V3 根治):
// 物品类(器物/植物/法宝,species 或代码块植物本体词判定)**不烤人形锚**——此前对
// 所有物种统一拼 manjuPortraitAnchor(East Asian/Chinese character)+防真人词替换,
// 物品 image_prompt 被污染成「物品本体+人形锚」自相矛盾,正是「绿萝→女人脸」的源头。
func scriptImagePrompt(block, assetStyle string, is3D bool, isItem bool) string {
	if isItem {
		// 物品管线:画物品本体,无正面人脸锚/无人形锚/无防真人替换(那些是人形语义)
		if block == "" {
			return "the item itself, " + assetStyle + manjuItemAnchor
		}
		p := block
		if assetStyle != "" && !strings.Contains(p, "3D") && !strings.Contains(p, "CG") {
			p = p + ", " + assetStyle
		}
		if !strings.Contains(p, "NOT a person") {
			p = p + manjuItemAnchor
		}
		return p
	}
	if is3D {
		if block == "" {
			return manju3DPortraitAnchor + manjuPortraitFrontFace
		}
		p := block
		if !strings.Contains(p, "virtual digital human") {
			p = p + ", " + manju3DPortraitAnchor
		}
		if !strings.Contains(p, "front-facing") {
			p = p + manjuPortraitFrontFace
		}
		return p
	}
	if block == "" {
		return assetStyle + ", " + manjuPortraitAnchor
	}
	// 防真人:素材的写实照片措辞替换为半写实拟动漫(残留 photorealistic 会把模型拉向真人脸=侵权)
	for _, re := range []struct{ old, neu string }{
		{"photorealistic", "semi-realistic stylized"},
		{"realistic photo", "stylized illustration"},
		{"real human", "stylized character"},
		{"realistic photograph", "stylized illustration"},
		{"realistic human", "stylized character"},
		{"photograph", "painterly illustration"},
	} {
		block = strings.ReplaceAll(block, re.old, re.neu)
		block = strings.ReplaceAll(block, strings.ToUpper(re.old[:1])+re.old[1:], re.neu)
	}
	// 风格措辞合并(素材常无风格词;有则跳过避免重复堆叠)
	if assetStyle != "" && !strings.Contains(block, "stylized CG") && !strings.Contains(block, "ink wash") && !strings.Contains(block, "East Asian art") {
		block = block + ", " + assetStyle
	}
	// 拟动漫正向锚强制附加(禁日漫/禁真人/东方面孔——不依赖 LLM 自觉)
	if !strings.Contains(block, "not a Japanese anime") {
		block = block + ", " + manjuPortraitAnchor
	}
	return block
}

// scriptRoleSpecies 素材标题身份词 → role/species(2026-08-26):
// 灵宠/坐骑/妖兽/神兽/灵植等 → species(非人形,Q 版萌兽/兽形角色板靠它判定);
// 盟友/主角/女主/助攻 → 正角;反派/工具人 → 反派/功能配角。
// 2026-08-29 扩展:①植物类词(灵植/植物/藤精/花精/树精/盆栽)判 species(物品渲染链);
// ②标题内显式字段「species: X;role: Y」(素材契约,阿碧卡格式)优先于身份词。
func scriptRoleSpecies(head string) (role, species string) {
	// 显式字段优先:「species: 灵植·紫藤精」「role: 正角」(分号/逗号分隔均可)
	if m := regexp.MustCompile(`species\s*[:：]\s*([^;,，)）]+)`).FindStringSubmatch(head); m != nil {
		species = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`role\s*[:：]\s*([^;,，)）]+)`).FindStringSubmatch(head); m != nil {
		role = strings.TrimSpace(m[1])
	}
	if role != "" || species != "" {
		return role, species
	}
	for _, k := range []string{"灵宠", "兽宠", "宠物", "坐骑", "妖兽", "神兽", "精怪", "灵植", "植物", "盆栽", "藤精", "花精", "树精"} {
		if strings.Contains(head, k) {
			return "", k
		}
	}
	switch {
	case strings.Contains(head, "工具人反派"), strings.Contains(head, "反派"):
		return "反派", ""
	case strings.Contains(head, "工具人"):
		return "功能配角", ""
	case strings.Contains(head, "主角"), strings.Contains(head, "女主"), strings.Contains(head, "男主"),
		strings.Contains(head, "助攻"), strings.Contains(head, "盟友"):
		return "正角", ""
	}
	return "", ""
}

// scriptGenderOf 性别判定:①标题身份词(女主/男主/女配/男配) ②中文称谓 ③描述 ④英文提示词。
// 2026-08-24 用户要求:Q版/多视图必须区分男女——gender 是唯一权威来源,解析必须可靠。
// 2026-08-26 补中文称谓兜底(用户实测「墨姨」判不出性别 → side 视图被画成有胡须的男人)。
// 用单词边界匹配英文性别词(避免 "woman" 里的 "man"、"hunter's" 里的 "her" 误判)。
func scriptGenderOf(head, desc, block string) string {
	low := strings.ToLower(head + " " + desc + " " + block)
	// 2026-08-28 修复(用户实测:苏砚「女主之父」被命中「女主」判成女):亲缘/配偶称谓
	// 比身份词优先——「女主之父」「男主之母」说的是这个角色本人的性别关系,身份词
	// (女主/男主)说的是关联角色,本末不能倒置。先判 之父/母亲 这类硬称谓。
	for _, k := range []string{"之父", "父亲", "爸爸", "爹", "夫君", "相公", "丈夫", "继父", "养父"} {
		if strings.Contains(low, k) {
			return "男"
		}
	}
	for _, k := range []string{"之母", "母亲", "妈妈", "娘亲", "妻子", "遗孀", "继母", "养母"} {
		if strings.Contains(low, k) {
			return "女"
		}
	}
	if strings.Contains(low, "女主") || strings.Contains(low, "女配") || strings.Contains(low, "女主角") || strings.Contains(low, "女神") {
		return "女"
	}
	if strings.Contains(low, "男主") || strings.Contains(low, "男配") || strings.Contains(low, "男主角") || strings.Contains(low, "之子") || strings.Contains(low, "少主") {
		return "男"
	}
	// 中文称谓(标题/描述):姨/婆/嫂/娘/婶/姐/夫人/姑娘… → 女;叔/伯/爷/翁/汉/哥/少爷/公子… → 男。
	// 单字避雷:「姑」不收单字(姑苏地名)、「公」不收单字(公主是女)。
	name := head + " " + desc
	for _, k := range []string{"姨", "婆", "嫂", "娘", "婶", "姐", "妹", "夫人", "娘子", "姑娘", "丫鬟", "婢女", "小姐", "妇", "媪"} {
		if strings.Contains(name, k) {
			return "女"
		}
	}
	for _, k := range []string{"叔", "伯", "爷", "翁", "汉", "哥", "兄", "弟", "少爷", "公子", "老爷", "郎", "叟"} {
		if strings.Contains(name, k) {
			return "男"
		}
	}
	word := func(w string) bool { return regexp.MustCompile(`\b` + w + `\b`).MatchString(low) }
	if word("woman") || word("girl") || word("female") || word("she") || word("her") || word("queen") || word("goddess") || word("lady") {
		return "女"
	}
	if word("man") || word("boy") || word("male") || word("he") || word("king") || word("elder") || word("master") {
		return "男"
	}
	return ""
}

// scriptAgeOf 从描述提取年龄("22岁"/"22-year-old")。
func scriptAgeOf(desc string) string {
	re := regexp.MustCompile(`(\d+)\s*岁|(\d+)-year-old`)
	if m := re.FindStringSubmatch(desc); m != nil {
		if m[1] != "" {
			return m[1] + "岁"
		}
		if m[2] != "" {
			return m[2] + "岁"
		}
	}
	return ""
}

// scriptAppearanceOf 提取角色独特面容特征(防不同角色撞脸,2026-08-24 用户要求)。
// 优先:素材"辨识度/记忆点"中文行;其次:英文提示词里的外貌特征(发色/眼/疤/胡须/体态/服装等)。
// 返回独有特征串,注入定妆照 prompt 作为 "distinct unique face" 锚。
func scriptAppearanceOf(body, block string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") || strings.Contains(line, "Cinematic") {
			continue
		}
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "辨识度") || strings.HasPrefix(line, "记忆点") || strings.HasPrefix(line, "判词") || strings.HasPrefix(line, "具名道具") || strings.HasPrefix(line, "动作习惯") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		return strings.Join(lines, "；")
	}
	// 英文提示词提取外貌特征(跳过画风/镜头/光线/情绪等通用词,保留发色/眼/疤/服装/体态独有词)
	if block != "" {
		keep := []string{}
		for _, seg := range regexp.MustCompile(`[^,]+`).FindAllString(block, -1) {
			s := strings.TrimSpace(seg)
			ls := strings.ToLower(s)
			if strings.Contains(ls, "cinematic") || strings.Contains(ls, "film still") || strings.Contains(ls, "stylized") ||
				strings.Contains(ls, "east asian") || strings.Contains(ls, "lens") || strings.Contains(ls, "depth of field") ||
				strings.Contains(ls, "ultra detailed") || strings.Contains(ls, "movie poster") || strings.Contains(ls, "light") ||
				strings.Contains(ls, "mood") || strings.Contains(ls, "atmosphere") || strings.Contains(ls, "man of") ||
				strings.Contains(ls, "woman of") || strings.Contains(ls, "youth of") || strings.Contains(ls, "of eighteen") ||
				strings.Contains(ls, "of seventeen") || strings.Contains(ls, "of his") || strings.Contains(ls, "of her") ||
				strings.Contains(ls, "of thirty") || strings.Contains(ls, "of fifty") {
				continue
			}
			if strings.Contains(ls, "hair") || strings.Contains(ls, "eye") || strings.Contains(ls, "scar") ||
				strings.Contains(ls, "mole") || strings.Contains(ls, "beard") || strings.Contains(ls, "whisker") ||
				strings.Contains(ls, "face") || strings.Contains(ls, "chin") || strings.Contains(ls, "nose") ||
				strings.Contains(ls, "brow") || strings.Contains(ls, "skin") || strings.Contains(ls, "mark") ||
				strings.Contains(ls, "robe") || strings.Contains(ls, "dress") || strings.Contains(ls, "armor") ||
				strings.Contains(ls, "clothes") || strings.Contains(ls, "chain") || strings.Contains(ls, "pendant") ||
				strings.Contains(ls, "scale") || strings.Contains(ls, "fur") || strings.Contains(ls, "tail") ||
				strings.Contains(ls, "figure") || strings.Contains(ls, "body") || strings.Contains(ls, "worn") ||
				strings.Contains(ls, "pupil") || strings.Contains(ls, "tabby") || strings.Contains(ls, "tiger") {
				keep = append(keep, s)
			}
		}
		if len(keep) > 0 {
			return strings.Join(keep, ", ")
		}
	}
	return ""
}

// mdSection 素材 md 的一节:## N. 标题 到下一个 ## 之间。
type mdSection struct{ head, body string }

// splitMdSections 按 ## N. 分节(标题行+后续直到下一个 ##)。
func splitMdSections(text string) []mdSection {
	var out []mdSection
	lines := strings.Split(text, "\n")
	var cur *mdSection
	flush := func() {
		if cur != nil && cur.head != "" {
			out = append(out, *cur)
		}
	}
	for _, line := range lines {
		// 同时识别 ## 与 ### 标题(人物文件用 ##,场景文件用 ### 分节)
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			flush()
			cur = &mdSection{head: line, body: ""}
			continue
		}
		if cur != nil {
			cur.body += line + "\n"
		}
	}
	flush()
	return out
}

// scriptDirectingFrom 从脚本验收清单"五维差异化"行解析 directing 五维;解析不出给默认。
func scriptDirectingFrom(text string) map[string]any {
	d := map[string]any{
		"time": "线性", "pov": "全知", "tempo": "匀速",
		"audio": "对白驱动", "ending": "空景收",
		"peak_device":    "从脚本分镜表提取(程序化解析)",
		"climax_pattern": "从脚本分镜表提取(程序化解析)",
	}
	// "五维差异化...：时间结构=...；视点=...；节奏=...；声音=...；结尾=..."
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "五维差异化") && !strings.Contains(line, "时间结构") {
			continue
		}
		get := func(key string) string {
			i := strings.Index(line, key)
			if i < 0 {
				return ""
			}
			rest := line[i+len(key):]
			rest = strings.TrimPrefix(rest, "=")
			rest = strings.TrimPrefix(rest, "：")
			j := strings.IndexAny(rest, "；;，,")
			if j >= 0 {
				rest = rest[:j]
			}
			return strings.TrimSpace(rest)
		}
		if v := get("时间结构"); v != "" {
			d["time"] = v
		}
		if v := get("视点"); v != "" {
			d["pov"] = v
		}
		if v := get("节奏"); v != "" {
			d["tempo"] = v
		}
		if v := get("声音"); v != "" {
			d["audio"] = v
		}
		if v := get("结尾"); v != "" {
			d["ending"] = v
		}
	}
	return d
}

// matchSceneByAlias 场景卡别名匹配:画面描述词(小馆/店堂/门口/灶台/老街…) → 场景卡 id。
// 别名表是通用电影语法词 → 场景类型,命中后还需该场景卡描述含对应关键词才算(防错配)。
// manjuCharsFromH3 从六段式主体句捞回漏判登场角色(2026-08-28 EP01 人物不一致根治)。
// 脚本画面列常以「屏幕前的男人」代称不写角色名,但 h3 的 subject_definitions 会照角色卡
// 写英文外观(short messy black hair, black-framed glasses…)——人物主体句与角色卡的
// **独有外观词**重叠 ≥2 才认定在场。五层收紧(2026-08-29 镜9 误捞 / 2026-09-03 老K误捞实测):
//   ①独有词=该卡有而其它卡没有(消共享模板词 cinematic/photorealistic/doll/aesthetic
//     ——第一版"卡内词重叠≥3"曾把 12 角色全捞进 chars);
//   ②独有词长度 ≥5 字母:dark/black 这类短常见词可能在全书恰好独有(陈默卡 dark circles
//     的 dark),环境句「dark office aisle」就会被当成陈默在场——镜9 曾因此捞出 4 人
//     超参考图上限;glasses/flannel/ponytail 才有外观专属性;
//   ③主体句必须含人物外观信号词(hair/glasses/shirt/collar/watch/skin/…):环境/道具
//     主体句(office aisle/monitor face)不参与捞人;
//   ④a 声明优先(2026-09-03):已判定登场人数(显式声明/文本匹配/说话人)不少于主体句数
//     时不捞——修仙界EP01实锤:镜1主体句是季一星本人定义行,季一星卡命中5词正确,但老K卡
//     (同为上班族形象)独有词 crumpled/office 也命中2词即入画,镜1~8连续误挂老K参考图,
//     H3 给画外角色编人声;
//   ④b 同句唯一归属(2026-09-03):每个主体句只归重叠词最多且严格领先次名的角色,
//     平票/领先不足不归属——相似形象不再搭车入画。
func manjuCharsFromH3(h3 string, charCards []map[string]any, charIDs []string, declared int) []string {
	// 人物外观信号词:句含其一才算"人物主体句"(环境/道具句跳过)
	personSignals := []string{"hair", "glasses", "shirt", "face", "eyes", "collar",
		"watch", "skin", "jacket", "dress", "beard", "ponytail", "blouse", "suit",
		"mustache", "bangs", "braid", "waistcoat", "sweater", "hoodie"}
	// 主体句:subject_definitions 区内含 <Subject N> is 且带人物外观信号词的行
	subLines := []string{}
	if i := strings.Index(strings.ToLower(h3), "subject_definitions"); i >= 0 {
		section := h3[i:]
		for _, stop := range []string{"summary:", "retention_analysis:", "detailed_description:", "overall_soundscape:", "non_diegetic_music:"} {
			if j := strings.Index(strings.ToLower(section), stop); j > 0 {
				section = section[:j]
			}
		}
		for _, line := range strings.Split(section, "\n") {
			low := strings.ToLower(line)
			if !strings.Contains(low, "<subject") || !strings.Contains(low, " is ") {
				continue
			}
			isPerson := false
			for _, sig := range personSignals {
				if strings.Contains(low, sig) {
					isPerson = true
					break
				}
			}
			if isPerson {
				subLines = append(subLines, low)
			}
		}
	}
	if len(subLines) == 0 {
		return nil
	}
	// 2026-09-03 门槛④(老K误捞根治,修仙界EP01实锤):镜1主体句是季一星本人定义行,
	// 季一星卡命中5词正确归属,但老K卡(同为上班族形象)独有词 crumpled/office 也命中
	// 2词即入画——旧逻辑对每卡独立判定、无同句竞争,形象相近的角色被成对误捞(镜1~8
	// 连续多挂老K参考图 → H3 给画外角色编人声)。收紧为双层:
	// ④a 声明优先:已判定登场人数(declared,含显式声明/文本匹配/说话人)不少于主体句
	//     数时不捞——声明权威,只补"句多于人"的真漏;
	// ④b 同句唯一归属:每个主体句只归重叠词最多且严格领先次名的角色(≥2 门槛保留),
	//     平票/领先不足不归属——相似形象的次名不再搭车入画。
	if declared >= len(subLines) {
		return nil
	}
	// 卡间独有词(≥5 字母,word -> 拥有它的卡数;只留仅 1 卡有的词)
	wordOwners := map[string]int{}
	cardWords := map[string]map[string]bool{}
	for _, sc := range charCards {
		id, _ := sc["id"].(string)
		prompt, _ := sc["image_prompt"].(string)
		if id == "" || prompt == "" {
			continue
		}
		words := map[string]bool{}
		for _, w := range manjuEnFeatureWords(prompt) {
			if len(w) >= 5 {
				words[w] = true
			}
		}
		cardWords[id] = words
		for w := range words {
			wordOwners[w]++
		}
	}
	h3Low := strings.ToLower(h3)
	outSet := map[string]bool{}
	var out []string
	addOut := func(cid string) {
		if cid != "" && !outSet[cid] {
			outSet[cid] = true
			out = append(out, cid)
		}
	}
	// 中文名直写兜底(脚本文风混杂时):全角色逐一直写检查
	for _, cid := range charIDs {
		if cid != "" && strings.Contains(h3Low, strings.ToLower(cid)) {
			addOut(cid)
		}
	}
	// 逐句竞争归属:每句计算各卡重叠数,唯一最大者(严格领先次名,≥2)归属
	overlapOf := func(line string, cid string) int {
		n := 0
		for w := range cardWords[cid] {
			if wordOwners[w] == 1 && strings.Contains(line, w) {
				n++
			}
		}
		return n
	}
	for _, line := range subLines {
		best, bestN, secondN := "", 0, 0
		for _, cid := range charIDs {
			if cid == "" || len(cardWords[cid]) == 0 {
				continue
			}
			n := overlapOf(line, cid)
			if n > bestN {
				secondN = bestN
				best, bestN = cid, n
			} else if n > secondN {
				secondN = n
			}
		}
		if bestN >= 2 && bestN > secondN {
			addOut(best)
		}
	}
	return out
}

// manjuEnFeatureWords 英文提示词特征词集(小写、去停用词,只留 ≥3 字母实词):
// 用于主体句 ↔ 角色卡的重叠匹配
func manjuEnFeatureWords(s string) []string {
	stop := map[string]bool{
		"the": true, "and": true, "with": true, "his": true, "her": true, "she": true,
		"has": true, "have": true, "are": true, "was": true, "for": true, "from": true,
		"this": true, "that": true, "into": true, "over": true, "under": true, "near": true,
		"who": true, "which": true, "while": true, "wearing": true, "wears": true, "man": true,
		"woman": true, "old": true, "young": true, "year": true, "years": true, "age": true,
		"character": true, "form": true, "living": true, "picture": true, "subject": true,
	}
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if len(w) < 3 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// manjuSceneDropped 伪场景判定(解析期与 loadPlan sanitize 双处共用,保证黑名单永远一致)。
// 判定分组(2026-08-28 实测修正):
//   封面组[封面/备用/开篇/终章]查 id+desc——「城市夜景大远景」的 desc 写「封面备用·
//   开篇/终章」(素材解析时标题括号并入 desc),封面卡不属于任何正片镜头;
//   伪配置组[负向/统一风格/全书…]只查 id——素材全局配置段(通用负向词/统一风格前缀/
//   质量后缀/色锚系统)的名字本身命中;desc 里的「全书视觉锚」是合法主场景标注
//   (「深夜工位（主场景·夜·全书视觉锚）」曾被「全书」误杀,EP01 整本书丢了主场景)。
func manjuSceneDropped(id string, desc string) bool {
	for _, k := range []string{"封面", "备用", "开篇", "终章"} {
		if strings.Contains(strings.ToLower(id), strings.ToLower(k)) ||
			strings.Contains(strings.ToLower(desc), strings.ToLower(k)) {
			return true
		}
	}
	for _, k := range []string{"负向", "负面", "negative", "统一风格", "质量后缀", "统一前缀", "色锚", "记忆点", "全书"} {
		if strings.Contains(strings.ToLower(id), strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// matchSceneByAlias 场景特征词匹配(2026-08-28 重写:旧硬编码别名表是美食书的规则,
// 跨书污染——本书镜12 pool 含「门口」→ 旧规则 need「夜景」命中 id 含「夜景」的封面
// 备用卡「城市夜景大远景」,再被最高频兜底传染全片,办公室戏全挂城市大远景参考图)。
// 改为从场景卡自身动态提取实体特征词匹配,任何书通用:
//   特征词 = id/description 的所有 ≥2 字连续中文子串(滑窗),剔除镜头语言泛词;
//   pool 命中特征词数最多者胜,平局取 id 短者(更专一,如「深夜工位」vs「开放工位区」
//   同中「工位」,取前者)。封面/备用卡已在上游 manjuSanitizePlanIDs drop,不进匹配池。
func matchSceneByAlias(pool string, sceneCards []map[string]any) string {
	best, bestHits, bestLen := "", 0, 0
	for _, sc := range sceneCards {
		id, _ := sc["id"].(string)
		if id == "" {
			continue
		}
		desc, _ := sc["description"].(string)
		if manjuSceneDropped(id, desc) {
			continue // 纯函数自洽:封面/伪配置卡即使混进列表也不参与匹配(上游已滤,双保险)
		}
		hits := 0
		for _, w := range manjuSceneFeatureWords(id + " " + desc) {
			if strings.Contains(pool, w) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		idLen := len([]rune(id))
		if hits > bestHits || (hits == bestHits && (best == "" || idLen < bestLen)) {
			best, bestHits, bestLen = id, hits, idLen
		}
	}
	return best
}

// manjuSceneFeatureWords 场景卡实体特征词:文本里所有 ≥2 字连续中文子串(滑窗),
// 剔除镜头语言/时间氛围泛词(城市/夜景/远景/深夜…任何场景都可能沾边,不构成场景身份)。
// 滑窗而非分词:中文无分词依赖,「深夜工位」能拆出「工位」给 pool 含「工位隔断」的镜头命中。
func manjuSceneFeatureWords(s string) []string {
	var out []string
	seen := map[string]bool{}
	runes := []rune(s)
	// 先取连续中文段,段内滑窗产 2..4 字子串(再长子串由精确匹配层覆盖,无需特征层)
	segs := [][]rune{}
	cur := []rune{}
	for _, r := range runes {
		if r >= 0x4e00 && r <= 0x9fff {
			cur = append(cur, r)
		} else {
			if len(cur) > 0 {
				segs = append(segs, cur)
				cur = nil
			}
		}
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	for _, seg := range segs {
		for n := 2; n <= 4; n++ {
			for i := 0; i+n <= len(seg); i++ {
				w := string(seg[i : i+n])
				if manjuSceneGenericWord(w) || seen[w] {
					continue
				}
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// manjuSceneGenericWord 镜头语言/时间氛围/素材元词——不构成场景身份的泛词
func manjuSceneGenericWord(w string) bool {
	switch w {
	case "城市", "夜景", "远景", "大远景", "全景", "特写", "近景", "中景", "外景", "内景",
		"白天", "深夜", "凌晨", "夜晚", "晚上", "清晨", "黄昏", "室内", "室外", "封面", "备用",
		"开篇", "终章", "说明", "场景", "画面", "风格", "统一", "全局", "通用", "备用场景":
		return true
	}
	return false
}

// firstN 取字符串前 n 个字符(日志告警用,防长文本刷屏)。
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// scriptEpisodeTitle 从脚本首行标题提取集标题。
func scriptEpisodeTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			line = strings.TrimPrefix(line, "#")
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "《")
			if i := strings.Index(line, "》"); i >= 0 {
				return line[:i]
			}
			if i := strings.Index(line, "分镜脚本"); i > 0 {
				return strings.TrimSpace(line[:i])
			}
			return line
		}
	}
	return ""
}

// firstExisting 在多个根目录里找第一个存在的文件(dir, name)。
func firstExisting(roots []string, name string) string {
	for _, r := range roots {
		if r == "" {
			continue
		}
		p := filepathJoin(r, name)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// filepathJoin 兼容路径拼接(素材目录名固定,无需跨平台复杂处理)。
func filepathJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimRight(dir, `/\`) + "/" + name
}
