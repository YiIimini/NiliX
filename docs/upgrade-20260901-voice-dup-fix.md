# 配音重复根治 + 内心音色固定(2026-09-01,script_parse_ver 17→18)

> 用户反馈:「配音不对,老是重复?分镜镜头变多了,配音重复更厉害了」+「内心配音音色需要固定,不能随机」。

## 一、配音重复根因(分镜跨镜串句 + 镜内重复)

### 实证形态
- **总览镜串句(主因)**:LLM 生成 h3_prompt 时,习惯把「整段对话」写进场景第一镜(总览镜)的
  detailed_description,后续分解镜又各带一句 → 同一句台词在多个镜的 `<d>` 中重复,
  H3 每镜独立配音 → 同一句被念多遍。EP01 实锤:镜1 写「老赵问 + 姜缺两答」三句,
  镜2/3 又各重复一句(「测完灵…」「天塌下来…」各念两遍)。
- **镜内重复**:第23章镜4/5/17 同镜内半角/全角标点双版本各写一遍。
- **台词+内心同句撞车**:第102章镜10(涂山杳杳台词)与镜24(白泽·小白内心)同句
  ——双权威,渲染端保守保留,由技能侧「一句一镜」契约治理。

### 渲染端修复(scriptDedupShotLines)
- **规则(以分镜表台词列为权威,逐句归属)**:
  ① 每镜权威集 = 该镜 dialogue 引号内台词 + narration(剥前缀)归一化;
  ② `<d>` 台词归在本镜权威集(精确或合并句包含)→ 保留;
  ③ 不在本镜权威、但与其它镜权威集**整句精确相等** → 总览镜串句,删除(连同引导语);
  ④ 任何镜权威集都不在(LLM 即兴台词)→ 保留,绝不误删;
  ⑤ 同镜内同一句 ≥2 次 → 只保留第一处;
  ⑥ 极短词(归一化 <2 字,如「无」「吃」「…」)→ 不参与跨镜判定(第42章连环「无」是剧情设计)。
- 挂载点:`buildPlanFromRaws` 中 `scriptValidateShots`(补写)之后执行。
- `script_parse_ver 17→18`:存量脚本直出项目下次 ensurePlan 自动重解析生效。

### 顺带修复的三个解析器 bug(全库扫描时暴露)
1. **全角冒号字节错位**:`strings.IndexAny` 返回字节索引,「：」是 3 字节 UTF-8,
   `j+1` 只跳 1 字节 → `\xbc\x9a` 尾字节残留进内心句内容 → 权威集 norm 与 h3 台词
   失配,去重/归属判定全失效(第102章镜24 实锤)。stripNarrationPrefix / 内心列解析 /
   manjuSpeechChars 三处统一 `utf8.DecodeRuneInString` 完整跳 rune。
2. **reDialogue 的 `\(S\d\)` 只匹配 1 位编号**:S10-S12(第173章写名之战群像)
   整行匹配失败 → 权威集缺句。改 `\(S\d+\)`,引号字符类补「」『』(第73章凶水会)。
3. **畸形台词行 `:""内容"`(空引号占位)**:reDialogue 只匹配到空引号,内容落空
   (第155章镜9)。新增 manjuDialogueContents 统一提取(主路径+剥前缀兜底)。

### 技能侧契约(NiliX-Novel SKILL.md)
「每镜只写本镜台词」硬新增:detailed_description 的 `<d>` 只写本镜 dialogue/narration
列台词;禁止总览镜串句;禁止同镜双标点版本;禁止台词+内心同句撞车;口号/回响以
每镜台词列都声明为准。

## 二、内心配音音色固定(assignedVoiceFor 同源)

### 根因
注入侧 `innerVoiceFor` 用 `autoVoiceFor`(恒返回档位基底,如 male_sun),而描述短语
`voiceTimbrePhrase` 走 `assignedVoiceFor`(变体,如 male_sun_2):
- 挂载音频 = 基底 lib_male_sun.mp3,prompt 描述 = 变体短语 → 描述与参考音频打架
  = 内心音色「随机/漂移」;
- 同档角色内心全挤同一个基底声、内心与对白声不同源。

### 修复
`innerVoiceFor` 与挂载侧 `offscreenVoiceKeyFor` 的内心分支统一改 `assignedVoiceFor`
——内心 = 角色本声(与对白同源同变体),跨镜固定。旁白仍走独立叙述音色不受影响。

## 三、验证
- 单测:TestScriptDedupShotLines / TestScriptDedupShotLinesEdge(跨镜串句/镜内重复/
  即兴保留/短词/幂等)、TestDedupRealChapter1(真实第1章)、TestDedupAllStoryboards
  (全库 300 脚本非权威跨镜重复清零)、TestInnerVoiceAssignedFixed(内心=对白同变体+跨镜恒定)。
- 全库影响:300 个分镜脚本,去重 115 处重复台词。
- 全量 `go test ./...` PASS;enc_guard 体检改动文件全部正常;NiliX.exe 已重编译。

## 四、生效方式
脚本直出项目由 script_parse_ver=18 自动重解析(plan 重建后旧镜头产物因指纹变化
自动删旧重渲);渲染已进行的项目在下次「一条龙/续跑」时自动生效。
