# 场景渲染趋同根治(2026-09-01)

## 用户反馈
「场景渲染也有问题,基本生成的都是相同的室内图片,请排查处理」

## 根因(三层,逐一实锤)

### 第一层:场景卡 image_prompt 趋同(主因)
`tools/md2json.py` 的 `convert_scene_cards` 把 md 源的结构认错了:
- md 中「全局风格块/场景卡说明」在一级标题下、紧随其后的 ``` 代码块被当成**第一个场景**的 image_prompt;
- 各场景二级标题的代码块解析正常,但**第一个场景吞掉了全局风格**;
- 排查发现所有场景的 image_prompt 前缀雷同(全局风格句被拼进每张卡)→ 场景图全部按同风格同描述生成 → H3 参考图趋同 → 成片画面趋同。

**修复**:重写 `convert_scene_cards`——只认 `## 场景名(描述)` 二级标题 + 紧随 ``` 代码块;一级标题(书名/统一风格/场景卡说明)整段跳过。重转 7 本书,验证 0 个重复前缀,各场景 image_prompt 显著差异化(天才竞技场/学院宿舍/天台月光/牢房/问斩台/灵兽市场……)。

### 第二层:场景卡缺失(分镜引用无图)
重转后做全库匹配审计:分镜脚本 15788 处【场景名】引用,406 种场景在场景卡中**不存在**(md 源也不存在)——技能生成场景卡时没建全(山海宗记名台 79 处/扫账阁 60/灰白空间 60/保安室门口 58/断碑崖顶 56……)。无卡场景 → 渲染端无场景参考图 → H3 自由发挥,同样趋同。

**修复**:新增 `tools/scene_fill.py` 场景补卡工具——
- 从分镜脚本收集每个缺失场景的画面列 + 六段式 detailed_description 场景句;
- LLM(deepseek-chat)生成场景卡(id/description/image_prompt),4 并发;
- 人物词残留自动 LLM 清洗(场景图必须空无一人,禁止 person/people/crowd/disciples 等);
- 断点续跑(--resume 跳过已存在)。
- 结果:**406/406 补卡成功,0 失败**;补卡后全库匹配审计 **0 缺失**(15788/15788)。

### 第三层:缓存/代际不失效(补了也白补)
1. `novelFingerprint`(manju_pipeline.go)素材指纹只统计 `.md` 后缀——场景卡已 JSON 化,改卡不失效旧 plan → 旧 plan 场景池仍用旧卡。**修复**:指纹纳入 `.json` 素材(人物/场景提示词.json),补卡后旧 plan 自动过期重生成。
2. 场景图按「文件存在」生成、**无代际**(manju_pipeline.go:4691)——已存在的旧趋同场景图不会重生成。**修复**:高级清理(扫帚→高级)新增清 `assets/scenes` 场景图目录,清掉后按新卡重生成。

## 改动清单
| 文件 | 改动 |
|---|---|
| tools/md2json.py | convert_scene_cards 重写(二级标题+紧随代码块,跳一级标题) |
| tools/scene_fill.py | **新增**场景补卡工具(分镜提炼+LLM 生成+人物词清洗+断点续跑) |
| internal/api/manju_pipeline.go | novelFingerprint 纳入 .json 素材指纹 |
| internal/api/manju_cleanup.go | 高级清理新增清 assets/scenes |
| NiliX.exe | 重新编译(含以上 go 改动) |

## 存量数据
- 7 本书场景卡:18/44、98/98、62/62、59/59、80/80、102/102、64/64(补卡后总数 509 张,跨书独立)
- 分镜引用匹配:15788 处 → 0 缺失(修复前 3740 处缺失)
- 人物词清洗:40 张补卡时清洗 + 32 张全库复扫清洗;有效残留 1 处(告天墙九阙广场镜头视角词,渲染端强制 no people 兜底)

## 验证
- 全量单测 `go test ./...` 通过
- `enc_guard.py scan` 本次改动文件 0 异常(仅存量遗留 main.go 235 处 U+FFFD,不可逆)
- `deliver_check.py` PASS

## 用户操作
1. 替换 NiliX.exe(用户自重启,桌面进程红线)
2. 对受影响的漫剧项目:**高级清理**(清方案缓存 + 场景图;场景图会按新卡重新生成,每项目场景图数 = 分镜引用场景数,比原来多)
3. 重新渲染验证场景差异化

---

# 全库全面复查(2026-09-01 追加,用户点名「NiliX 也全面复查避免还有问题」)

## 复查结果:7 本书 524 章 storyboard_check 全部 PASS(修复前 6 本 FAIL)

### 发现并修复的问题(四类)

1. **对白覆盖缺失 66 句(48 章)**:正文对白没进分镜——成片丢对白的硬伤。
   - LLM 定点补全 42 章(tools/fix_sb_coverage.py:定位插入镜 + dialogue/h3_prompt 双写同步 + 插入后复验,3 轮重试)
   - 3 处**超长对白截断**(分镜 <d> 只写了前半,「403户,团子…」「我今年四十八…」)——程序补全截断
   - 重复插入防抖:LLM 多轮重插事故(第34章镜1 曾 4 行沈小满)→ 插入前去重
   - 文学性引号词豁免(storyboard_check 升级):「养伤」「已售」等非对白被误报——双向言语动词判定(引号前后)+ 固定词排除(听说/据说)+ 「道」须 X道 模式

2. **长句超 20 字 839 句(2371 镜)**:H3 口型/节奏上限硬约束违规。
   - fix_sb_quality.py 拆句:同行多句(「句1」「句2」连写)先切分,单句仍超按标点断点拆 ≤20 字;dialogue 行与 h3_prompt <d> 同断点双写同步(adds: 承接句式);引号样式保留

3. **[Shot N] 标记 465 镜异常 + 时间戳 3 章违规**:
   - 根因①:shot_id 字段本身错乱(第104章镜11 曾 shot_id=29,LLM 输出错号)——按数组重排
   - 根因②:h3_prompt 内 [Shot N] 重复标记(第1章镜17 双 [Shot 17])——首标记保留,后续删除
   - 根因③:At 时码格式漂移——HH:MM:SS.mmm(7383 镜)不符合渲染端契约(MM:SS.mmm)统一换算;双 At 删除;At 递减章(LLM 写错 15:00→00:00)按 duration 重算真实时间轴
   - 根因④:镜完全漏写 [Shot N](第50章镜5)——补标到 detailed_description 风格句后

4. **角色卡主形象缺失 8 张**(吞吞/柴老爹/小白/涂山杳杳/精卫/阿讹/九婴/镰主——纯 Q 版/真身形态卡)——LLM 补主形象 image_prompt(渲染端主定妆用)

### 修复工具
- tools/fix_sb_quality.py:拆句 + shot_id 重排 + [Shot N] 重写/补齐 + At 归一/重算(幂等,重跑指纹不变)
- tools/fix_sb_coverage.py:LLM 定点补对白(插入去重 + 复验循环)
- 备份:tools/logs/backup/sb_before_quality_fix_20260901(524 文件,可回滚)

### 渲染端兼容性确认
- adds 句式(<Subject N> (Sx) adds: <d>)——H3 从 <d> 配音 ✓;QC 期望台词从 plan dialogue 列逐行提取 ✓;音色挂载按 (Sx) 编号 ✓;不误标 off-screen ✓
- At 归一为 MM:SS.mmm 后与 reShotCut 契约一致 ✓

### 技能侧同步(已推送 e4191c9)
- storyboard_check:norm 补半角标点 / d_block_joined_norm 剥 [Chinese] 标签 / 文学性引号词豁免

### 已知存量 WARN(不阻断,交付说明)
- 语音超标 373 镜(台词 5字/秒 预算,轻微超标 H3 可念完;拆镜风险大未批量处理)
- 长句 WARN 60 句(引号混用提取假象为主,<d> 已拆好)

---

# 三问题综合整改(2026-09-01 追加:主次混乱/时长匹配/穿越漂移)

## 问题① 主角与配角渲染混乱(根因:角色名变体匹配不上 → 无参考图)
- 全库 1971 处 characters 声明/说话人不在角色卡:分镜用简称(杳杳×108、主持人×6、小汤),素材卡是完整名(涂山杳杳/天才榜主持人/孟小汤)——精确匹配不上 → 该角色无参考图 → H3 自由发挥 → 形象漂移/与主角混淆(「未分清主副」实锤根因)。
- **渲染端修复**:manjuResolveCharID 变体归一纯函数(精确→剥·/括号→后缀→包含,最短卡 ID 优先,旁白/画外/内心前缀排除),接入 characters 声明/说话人强制入画/scriptMinorCast 建卡前置;script_parse_ver 16→17 触发存量重解析。
- **存量补卡**:tools/char_fill.py 从六段式按 (Sx) 定位 subject 提炼 LLM 建群演卡,补 326 张(1971→45 处,剩余低频龙套由渲染端自动建卡兜底)。
- 单测:TestManjuResolveCharID(杳杳/主持人/小汤/括号/前缀/排除全场景)。

## 问题② 镜头时长 vs 配音时长
- 渲染端 plan 期已按「去标点字数÷4字/s」补偿时长,但分镜文件 duration 未同步 → 用户要求文件级匹配。
- **分镜 duration 同步**:309 镜按渲染端口径重算(去标点/4,ceil,clamp 15);超 15s 上限的 41 镜拆镜(tools/split_long_shots.py:台词按 60 字/组打包,新镜继承场景,h3 拆 <d>,shot_id 重排)。
- **拆句补全**:同行多句/引号未闭合/内心旁白行(第33章镜6 实锤)全部按标点断点拆 ≤20 字。
- check 语音预算口径与渲染端对齐(5→4字/s、去标点、剥多行前缀)——误报 733→59 处(剩余为 4.1-4.2 字/s 边缘,渲染端 plan 补偿兜底不截断)。

## 问题③ 穿越式漂移/不属于本剧内容
- 跨书角色名检测:老周/老赵为本书同名群演缺卡(非串剧),但暴露同名跨书风险——补卡后按项目素材隔离(渲染端卡集 per 项目)无串卡路径。
- 画面穿越主因=无参考图镜 H3 自由发挥(问题①修复后根治);条件缓存/接缝 latent 项目前缀隔离已确认无跨项目污染。

## 验证
- 全库 7 本 storyboard_check 全 PASS(覆盖 0 缺失/时间戳 0/语音超标 59 边缘 WARN)
- go vet/全量单测/deliver_check 全过;NiliX.exe 已重编译(ver17)
- 技能侧 storyboard_check 已推送(5d2377b)

---

# 渲染崩溃修复(2026-09-01 追加:panic assignment to entry in nil map)

## 崩溃现场
轮回欠费九世 EP01 渲染时 panic「assignment to entry in nil map」,堆栈:
alignAudioDefsReg(manju_prompt_align.go:700)→ manjuAlignShotPromptReg → finalize 链。

## 根因
`dialogueSpeakerIDs` 对**空 dialogue 镜**返回 nil map;alignAudioDefsReg 的
「画面段组合兜底」分支(`if _, ok := cidSx[cid]; !ok { cidSx[cid] = m[2] }`)
对 nil map 赋值 → panic。触发条件:空 dialogue 镜的 h3 画面段含
`<Subject N> (Sx)`(含「remains silent」类静默描述也命中 reSubjectSpeakerRef)。

## 修复(三层)
1. **代码**:dialogueSpeakerIDs 空 dialogue 返回空 map(非 nil)——所有 nil map 赋值路径根治;
   回归单测 TestAlignAudioDefsEmptyDialogueNoPanic(静默镜 + 说话引用镜两场景)。
2. **数据**:LLM 直出/补对白时台词写进 h3 <d> 但 dialogue 列空(198 镜实测,
   音色挂载/QC 期望缺失)——tools/backfill_dialogue.py 从 h3 按
   `<Subject N> (Sx) says/adds(: in an off-screen voiceover)?: <d>…</d>` 回填
   dialogue 行(角色名取 characters[N-1] 挂载序),152 镜回填。
3. **验证**:修复后全库 7 本 PASS(覆盖 0/时间戳 0);剩余 110「触发镜」实为
   静默镜((S1) remains silent 误匹配),修复后不崩、无需回填;exe 已重编译。

---

# 苏晚萤 Q 版事故修复(2026-09-01 追加:Q 版凭想象生成)

## 用户反馈
「苏晚萤的 Q 版形象未正常生成」+「Q 版形象需要生成对应角色正面照对应风格的,不能凭想象生成(如技能侧可全角色Q版形象提示词统一输出)」

## 排查
- 苏晚萤(金丹一万重,女主)Q 版图**生成了但内容异常**:GLM 视觉实测——比例是 Q 版(大头短身),但「发光卷曲流苏/发光连体装/科幻光效风格」,与主图(拟动漫写实)割裂。
- 根因①:角色卡**缺 q_form**(技能生成时遗漏,全库正角仅苏晚萤/孟小汤两张漏)——渲染端退模板拼接,image_prompt 的「luminous eyes / with a gentle light」光效词未被 manjuQStrip 剥除,img2img 0.93 高重绘下光效词放大成发光特效(22 张卡含光效词,同类隐患)。
- 根因②:技能侧 q_form 契约只说「仅正角」,未强制必填统一输出。

## 修复
1. **渲染端**:manjuQStrip 补光效词剥除(luminous/glowing/radiant/shimmering/gentle light 等),人形/兽形 Q 版统一受益(22 张卡)。
2. **数据**:LLM 补苏晚萤/孟小汤 q_form(基于各自 image_prompt 正面照提炼,同一人同风格,chibi+标志特征+年龄感)。
3. **技能侧**(已推送 6bb2c52):q_form 契约强化——正角必填、统一输出不得遗漏,基于正面定妆照提炼。
4. Q 版生成链路确认:渲染端 Q 版 = q_form(权威) + 正面主图 img2img 0.93(身份随底图)——已满足「基于正面照」;exe 已重编译。

## 用户操作
替换 NiliX.exe → 删除 manju/金丹一万重/assets/characters/苏晚萤_q.png(或对该项目高级清理)→ 重渲资产阶段按新 q_form 重出 Q 版。

---

# 全角色 q_form + 分镜细中细(2026-09-01 追加:用户规则升级)

## 需求
1. 全部角色 Q 版形象提示词技能侧统一输出(不只正角)
2. 分镜脚本细中细:正文每段内容全部镜头渲染到位

## 实施
### ① 全角色 q_form(技能侧统一输出)
- 技能侧契约:q_form 从「仅正角」改为**全部角色统一输出**(正角/反派/功能配角/群演),基于各自正面定妆照 image_prompt 提炼(同一人同风格+chibi+标志特征+年龄感,兽类萌化小兽/物品本体萌化)
- 存量:tools/qform_fill.py 全库补写 **450 张**(438+12 增量),0 缺漏
- 渲染端:Q 版资产生成 2026-08-27 已全角色放开(确认);manju_llm.go LLM 直出 schema 的 6 处 Q 版纪律同步改为全员(编译验证)
- 苏晚萤/孟小汤等正角此前漏 q_form → 本次全员覆盖根治

### ② 分镜细中细(正文全部镜头渲染到位)
- 技能侧规则(已推送 fb8febc):段落级穷尽(正文每段至少一镜)/动作分解(关键动作起步-进行-完成 2-3 镜)/神态微表情入镜/记忆点道具特写镜/一镜一拍推荐
- tools/storyboard_regen.py 密度规则 80-130 字/镜 → **50-80 字/镜**(细中细)
- 存量:39 章低密度章(正文>80字/镜,最高 118 字/镜)后台全量重拆(storyboard_regen 两阶段+确定性修复+三层校验)
- 渲染端无需改动(镜头数无上限,分镜即权威)

---

# 知识库 H3 知识深度分析取长补短(2026-09-01 追加)

## 知识源(知识库最新 H3 知识,已深度分析)
1. **MiniMaxH3提示词保姆级教程五步导演法**(2026-09-01 入库):选模式→拆三部门→六件事写镜头→一致性约束→声音分层
2. **MiniMaxH3官方TurboLoRA四档实测**(2026-09-01 入库):4step+0.31MP 提速 2.68×,画质 ±7% 内
3. **H3群演与Q版角色质量控制实战**:FRAME/NOREF/AUDIO 纪律+引擎选择+负面词追加语义

## 现状核查(逐条对照,已有 vs 缺口)
| 知识要点 | NiliX 现状 | 处置 |
|---|---|---|
| 说话人稳定编号+首次声音描述 | Sx 全局注册表 ✓ | 已有 |
| 旁白嘴唇闭合 | off-screen voiceover 句式 ✓ | 已有 |
| FRAME 纪律判定=subject_definitions 非空 | 2026-08-27 已按此修 ✓ | 已有 |
| 配乐 ducking(对白避让) | manju_llm.go:1093 已有 ✓ | 已有 |
| TurboLoRA 4step | lightx2v v1.1 已接(8-26)✓ | 已有 |
| 身份一致性约束句(防五官漂移) | **缺失** | **新增 IDENTITY CONSISTENCY 纪律** |
| 复杂动作拆 3-6 子动作+对白移动运镜不叠加 | 部分(细中细有动作分解) | **补完整条款** |
| 单一运镜(禁堆冲突运动词) | 缺失明确条款 | **补条款** |
| 配乐避让完整表述(降音量→结束后回升) | ducking 有,完整表述缺 | **补条款** |

## 实施
### 渲染端(manju_pipeline.go)
- 新增 manjuConsistencyGuard(IDENTITY CONSISTENCY):「人物脸部特征/发型/服装/配饰/身体比例/画面位置/场景布局/光线方向全程不变,匹配参考图」——**有 <Picture N> 引用才注入**(无参考图镜不约束文字成型),幂等锚防叠加;finalize 汇点注入(改词=存量 plan 缓存失效,重渲预期);单测 TestConsistencyGuardInject 固化。
- exe 已重编译。

### 技能侧(三处同步)
- tools/storyboard_regen.py COMPACT_SYSTEM 规则 10 补:动作拆小(3-6 连续子动作)/单一运镜/配乐避让(对白时降音量→结束后回升,禁情绪形容词)
- internal/api/manju_llm.go LLM 直出规则补同款条款
- 技能侧 references/分镜派发模板.md 补同款条款

## 验证
- go build/全量单测/deliver_check 全过
- 39 章细拆后台任务(细中细规则)运行中,完成后全库复验

---

# 知识库仙侠打戏工作流取长补短(2026-09-01 追加)

## 知识源
「AI短剧仙侠打戏角色卡场景卡工作流」(亮哥讲智能,2026-08-28):角色三视图锁脸/场景功能分区/场景全景+近景双卡/特效锚定式设计/视频提示词只管动作。

## 现状对照
| 要点 | NiliX 现状 | 处置 |
|---|---|---|
| 角色三视图锁脸 | front/full/side/detail 视图体系 ✓ | 已有 |
| 视频提示词只管动作 | 参考图锁定+IDENTITY CONSISTENCY ✓ | 已有 |
| 特效锚定(附着载体防漂浮) | **缺失** | **补条款** |
| 场景服务动作(功能分区) | **缺失** | **补条款** |
| 地面材质细节(近景卡作用) | **缺失** | **补条款(文本层)** |
| 场景全景+近景双卡资产 | 单卡体系 | 文本条款覆盖(资产改造边际收益低,不做) |

## 实施(三处同步)
- tools/storyboard_regen.py COMPACT_SYSTEM 规则 10:特效锚定(载体→路径→落点,禁凭空漂浮)/场景服务动作(可蹬踏柱/可借力壁/破坏承接点)/地面材质(裂纹/碎石/切割痕/烧蚀)
- internal/api/manju_llm.go LLM 直出规则同款
- 技能侧 references/分镜派发模板.md 同款

---

# 普通模式去 LLM 直出整改(2026-09-01 追加:分镜产出统一交给技能侧)

## 用户规则
「普通模式无脚本时直接提示使用 AI 一条龙,而不是 LLM 直出——彻底把分镜产出统一交给技能侧」

## 实施(两处封堵)
1. **普通模式无脚本** → ensurePlan 检测不到 素材/分镜脚本/*.json 时,返回明确错误:
   「未检测到分镜脚本:普通模式仅支持脚本直出,分镜产出统一由技能侧完成。请①先用爽文小说技能生成分镜脚本,或②改用「AI 一条龙」走 LLM+Agent 全流程直出」——不再静默 LLM 直出。
2. **脚本模式 h3 缺失** → genShotPrompts 检出缺 h3_prompt 的镜时报错提示修正脚本(技能侧 json 为唯一权威,不用 LLM 补——补的 h3 丢站位/运镜/特效锚定);LLM 直出/agentMode 不受影响。
3. **保留兼容**:手动粘贴脚本/旧 md 脚本解析失败仍回退 LLM 直出(用户主动提供的脚本,非技能侧产出;技能侧 json 标准格式解析必成功)。
4. 日志口径:脚本直出明示「🎬 脚本直出(零 LLM)」,不再打印「🤖 大模型直出」误导。

## 分流终态
- 普通一条龙/小说导入/全本自动分集:检测 json 分镜脚本 → 有则脚本直出(零 LLM),无则提示用 AI 一条龙
- AI 一条龙(agentMode):全走 LLM+Agent(用户规则,不受影响)

---

# 存量小说同步升级整改完成(2026-09-01 终验)

## 整改项(7 本书全部完成)
1. **39+ 章低密度细拆**(细中细规则 50-80 字/镜):--chapters 章号跨书匹配共重写 135 章(含密度已达标章,统一按新规则刷新),重写后镜头数普遍 +30-80%(如杂毛53章 47 镜/56章 52 镜/164章 51 镜,正文 30-40 字/镜)
2. **重写后修复链**:拆句 549 镜/拆镜 16 镜/Shot 标记重写/时长同步(重写引入的长句/时间戳/语音问题全清)
3. **覆盖补全**:重写丢失对白 4 章 6 句 LLM 定点补回
4. **角色卡**:缺卡 1971→3 处(补 74 张:画面上下文针对性补卡,群体泛指/物品单次合理豁免);q_form 全角色 450 张
5. **场景卡**:406 补卡覆盖 0 缺失(前期)

## 终验
- 全库 7 本 storyboard_check **ALL PASS**(覆盖 0 缺失/时间戳 0)
- 语音超标 127 处边缘 WARN(渲染端 plan 按 4字/s 补偿兜底,不截断)
- 交付检查 PASS

---

# 视图/Q版九类问题整改(2026-09-01 追加:用户实测 9 项)

## 问题与根因(逐项实锤)
| # | 问题 | 根因 |
|---|---|---|
| 1 | 阿影/群演·路人 侧面异常 | 阿影走 SILHOUETTE 剪影锚(2026-09-01 已修);群演·路人=普通卡侧面异常待重渲验证 |
| 2 | 墨千秋/裁判/群演·路人 全身异常 | 视图基于 image_prompt(img2img),与主图 mtime 联动重生成 |
| 3 | 记者/宿管阿姨/食堂阿姨 Q 版异常 | q_form 发型/形态细节不足(旧 q_form 无六硬规范) |
| 4 | 老板/雷光/天才榜少年 Q 版撞脸 | q_form 模板同质(young Chinese male+short hair 千篇一律,辨识特征不足) |
| 5 | 小月 正面猫/视图人 | **兽形词表缺 creature/fur** → manjuIsBeast 判人形 → 视图按人画 |
| 6 | 黄毛跟班 Q 版发色不匹配 | HAIR LOCK 无颜色词=空锁(「keeps EXACTLY this hair color — short hair」无颜色可锁) |
| 7 | 年轻教员 Q 版人物太大 | q_form 覆盖模板形态构成(只写 3-head-tall,缺大头/短手脚细节) |
| 8 | 人物带烟酒 | 素材卡含 cigarette/wine flask 等词(瘦守卫/钱不换/抱朴子) |
| 9 | 群演·路人甲等 Q 版发型不匹配 | 5 个后补卡 **q_form 为空**(char_fill 补卡后没补 q_form)→ 模板拼接 |

## 修复
### 渲染侧(manju_pipeline.go/manju_comfy.go)
1. **兽形词表扩充**:manjuAnimalEnRe 补 creature/fur/paws/whiskers/fluffy/beast/animal/palm-sized/tail(species 缺失的兽卡不再漏判人形)
2. **禁烟酒**:manjuNegPrompt 加 cigarette/smoking/alcohol 等负面词;新增 manjuBannedItemStrip 正向剥离(定妆/视图/Q版三处接入)
3. **HAIR LOCK 强化**:颜色+形状双重点名(无颜色词也锁发型形状与主图一致)
4. **形态构成兜底**:q_form 非空也追加「大头占半身/短手短脚/大亮眼」构成锚(比例不再稀释)
5. 单测:TestManjuIsBeastCreatureWords/TestManjuBannedItemStrip

### 技能侧(已推送)
6. SKILL.md q_form 契约升级为**六硬规范**:发型显式锁(发色+发型词逐字一致)/辨识特征≥2(防撞脸)/形态构成(大头占半身)/禁违禁物品/同一人/比例占画面约 60%
7. tools/qform_fill.py 生成 SYS 同步六硬规范;manju_llm.go 角色卡 schema 加禁烟酒条款

### 存量修正
8. 17 个问题角色 q_form 按六硬规范重生成(阿影/墨千秋/裁判/记者/宿管阿姨/食堂阿姨/老板/雷光/天才榜少年/小月/黄毛跟班/年轻教员/群演·路人/路人甲/路人乙/影傀/观众/同桌)——发型显式色名(黄毛「dyed yellow slicked-back hair」/路人甲「short black hair」)+辨识特征+形态构成
9. 烟酒词清理:瘦守卫 cigarette 移除;钱不换/抱朴子 gourd wine flask→gourd flask;误报(烟火气/如烟比喻/否定式)保留

## 用户操作
替换 NiliX.exe → 对「我的影子会咬人」等受影响项目**高级清理**(清 characters 资产)→ 重渲资产阶段按新 q_form/新判定重出视图与 Q 版。

---

# Q 版渲染范围回退仅正角(2026-09-01 追加:用户规则最终版)

## 用户规则
Q 版形象只有正角(女主/男主/正角)渲染,配角/群演不生成 Q 版资产;内心活动只有正角才有(正角内心=Q 版演绎),其他角色内心镜头=写实正脸+画外音。**技能侧与渲染侧任何修改必须同步调整(持久规则)。**

## 实施
### 渲染侧(manju_pipeline.go/manju_llm.go)
1. **Q 版资产仅正角**:资产阶段 viewSet 循环 view=="q" 时 `manjuIsLeadRole`(role=女主/男主/正角/主角)过滤——配角/群演跳过 q 视图生成
2. **挂载自洽**:shotViewRelsFor 内心镜挂 q 图前检查 fileExists——非正角无 q.png 自动降级写实正脸(无需额外改)
3. **manju_llm.go 6 处纪律回退**:「全部角色内心可用 Q 版」→「仅正角内心 Q 版,反派/功能配角/群演内心=写实正脸+画外音」
4. 技能侧 q_form 契约:全员输出备而不用,渲染端仅正角生成

### 技能侧(SKILL.md,已推送)
5. q_form 契约注明:全员输出备而不用·渲染端仅正角生成 Q 版资产;内心戏规则本就正确(正角 Q 版/其他写实+画外音,派发模板 53 行)

---

# ComfyUI 插件排查更新(2026-09-01)

## 排查结果(8 个 git 插件)
| 插件 | 版本状态 | 处置 |
|---|---|---|
| ComfyUI-H3-ConditioningCache | 本地=远程(自研,third_party 源) | 无需更新 |
| ComfyUI-H3-Motion-Context | 本地=远程(**NiliX 核心节点源**:ReferenceToVideo/SigmaShift/ImageToVideo) | 无需更新 |
| ComfyUI-MiniMax-H3-PDD-Acc | 8335330→311a65d | **更新**(量化 trunk 指纹崩溃修复/离线 bake/模型验证开关) |
| ComfyUI-MiniMaxH3-Easy | 33b6a79→ebf7493(v1.1.0 大改版,节点体系重构为 Easy* 系列) | **更新**(NiliX 不调用 Easy 节点,兼容无影响) |
| rgthree-comfy | 35c9f1e→2c5342a | **更新**(扩展更新机制缓存) |
| ComfyUI-KJNodes | 3f20054→e8e88f7(ghproxy.net 证书过期→换直连 fetch 后还原) | **更新**(结构改为 nodes/ 分包) |
| ComfyUI_IPAdapter_plus | 本地=远程 | 无需更新 |
| was-node-suite-comfyui | 本地=远程 | 不动(缺 numba 属既有环境问题,NiliX 不依赖) |

## 验证
- py_compile 全过(KJNodes nodes/*.py 分包结构)
- ComfyUI 0.34.0 启动加载:核心插件全部 OK,1245 节点在线;
  MiniMaxH3ReferenceToVideo/SigmaShift/ImageToVideo/PDDAccApply/Easy 全部 ✓
- 回滚点:PDD-Acc 83353308 / Easy 33b6a795 / rgthree 35c9f1e1 / KJNodes 3f200542

---

# 二合一(管线+画布)阶段一:镜头级可视化调试面板(2026-09-01)

## 用户需求
「实现二合一吧,目前我测试就是麻烦」——管线为主+画布为辅:点开任一镜头看到工作流节点图,参数可改(seed/步数/采样器/引擎/负面词),保存后只重渲该镜。

## 后端
1. **按镜参数覆盖机制**(internal/api/manju_shot_override.go):`analysis/<ep>_shot_overrides.json`(seed/steps/sampler/turbo_lora/pdd/sage/neg_prompt/note);空覆盖=清除;「-」=禁用引擎
2. **读取点改造**:seedFor override.seed 优先(重试仍递增);renderShotTo 参数注入(steps/sampler/turbo_lora/r2v/neg override 优先);**shotRenderFingerprint 纳入覆盖指纹**——覆盖变化→该镜 stale→自动重渲,其它镜不动
3. **新 API**(manju_shot_debug.go):
   - `GET /api/manju/shot/workflow` → 节点链(UNETLoader→引擎分支→采样→输出)+参数(当前生效)+参考图清单+缓存名+接缝标记
   - `POST /api/manju/shot/override` → 保存/清空覆盖
   - `GET /api/manju/shot/overrides`、`GET /api/manju/shot/asset`(参考图)
4. 单测:TestShotOverride*(指纹/存取/seedFor)+TestShotWorkflowAPI(httptest 集成:节点链/参考图/覆盖生效/指纹变化)

## 前端(web/kb)
5. 镜头管理弹窗每镜卡片加「🎛 调试」按钮 → 镜头调试弹窗:工作流节点图(横向节点块+箭头+参数)+参考图缩略图+参数表单(●=镜级覆盖)+「保存参数/保存并重渲此镜/重置默认」
6. app.css 新增 .dbg-*/.wf-* 样式(复用 themes.css 变量)

## 验证
- JS node --check ✓、go 全量单测 ✓、deliver_check PASS、exe 已重编译
- 手动验证步骤:替换 exe → 镜头管理 → 调试 → 改 seed/步数 → 保存并重渲此镜 → 仅该镜重渲(指纹变化自动 stale)

## 后续批次
- 二期:节点图可交互(点节点改参数)+中间产物预览(首帧/条件缓存状态)
- 三期:拖拽连线/换节点(Easy 接入)+工作流模板保存

---

# 配音不随角色/重复根治(2026-09-01 追加:实证链路断点)

## 用户反馈(多次整改未解决)
「配音依然不根据角色来定义音色,配音仍然总是重复」

## 实证链路排查(非表面整改)
全链路验证:角色卡 voice 字段 ✓ → plan Audio 定义 ✓ → ref_audio 挂载(官方平铺键结构一致 ✓)→ voice_lib 39 个音色文件 ✓ → edge-tts 环境 ✓ → 同步逻辑 ✓ → 分镜 Audio 定义行带音色短语 ✓。
**真正断点(H3 核心机制)**:H3 按提示词里的**音色身份短语**分配角色声线(comfy_extras/nodes_minimax_h3.py 实证:ref_audios 是整体音色基底,角色区分靠提示词短语)——而短语由 LLM 逐镜自由发挥,实测三病灶:
1. **错配**:女主金珠短语=「middle-aged man with a deep rough voice」(中年男声)、男主石敢当=「young woman」(女声)——短语与角色卡完全脱节
2. **雷同**:「young man with a clear steady voice」遍地复用(多角色同短语)→ H3 生成同一声线 → 听感重复
3. **漂移**:同一角色多名字形态(金珠/Jinzhu/Jin Zhu、小白/白泽·小白/拼音)各一套短语,跨镜声线漂移
4. **注入缺口**:injectAudioTimbrePhrases(ver14)只在短语**缺失**时注入——LLM 写了错配/雷同短语时直接透传!

## 修复(渲染端权威化,LLM 不再决定音色)
1. **变体差异化短语表**:manjuVoicePhraseFor 按音色 key+变体(_2/_3)输出互异短语(两个青年男=clear steady / slightly husky low / resonant confident)
2. **voiceTimbrePhrase 用 assignedVoiceFor**(含变体轮转)替代 autoVoiceFor(恒基底)——同档角色短语互异
3. **injectAudioTimbrePhrases 升级校正模式**:有短语行→按角色卡标准替换(错配修正);无短语行→注入;幂等(替换后=标准,重跑不叠加)
4. 单测:错配修正/裸行注入/幂等/变体差异化

## 技能侧(已推送 79455fc)
分镜派发模板音色短语硬约束:①性别年龄与角色卡一致 ②同书角色短语互异禁雷同 ③说话人用角色卡标准名禁拼音混用;注明渲染端程序校正兜底。

## 效果
替换 exe 后重渲:**音色短语由渲染端按角色卡程序生成**——女主=年轻女声(不再中年男)、同档角色声线显著区分、跨镜短语逐字一致。存量分镜无需重写(渲染端校正自动生效)。

---

# Q 版渲染异常根治(2026-09-01 追加:主角 role 缺失实锤)

## 用户反馈
「Q 版形象渲染异常」

## 根因(数据实证)
**主角角色卡 role 字段缺失**(md2json 早期转换丢失)——阿影/沈照/苏挽月(影子书男女主)、小白/涂山杳杳(杂毛男女主)、石敢当/钱不换(人算)、甲一/柴小满/林铁柱(废铁)、陈念念(全小区)等 role 全空 → `manjuIsLeadRole` 判定 false → **主角 Q 版资产不生成** → 内心镜头降级写实(用户看到的「Q 版异常」)。内心证据:阿影 278 次/小白 52 次/涂山杳杳 30 次内心戏。

## 修复(三层)
1. **渲染端兜底**:manjuIsLeadRole 改 ctx 方法——role 明确非正角不兜底;role 缺失时按「方案中 内心·角色名 出现过」判定(技能契约:只有正角有内心戏=正角证据)——存量/新书主角 Q 版不再缺席
2. **存量数据**:全库补 role——内心证据(正角,铁证)+登场频次≥80(主要角色);62 个正角全部有 q_form(59+3 补齐)
3. **技能侧**:SKILL.md role 字段必填契约(正角=女主/男主/正角/主角+正派灵宠/重要正派助攻;判定依赖 role)

## 验证
- 全库 62 正角 62 q_form 全覆盖;单测/交付检查 PASS;exe 已重编译
