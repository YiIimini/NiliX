# 分镜脚本内容级修复(2026-09-02,《我的影子会咬人》渲染异常)

> 用户反馈:01/04 站位与脚本不符、06 群众配音主角动嘴、07 白发变黑、
> 12 Q版乱入、16-24 形象飘逸/站位异常。

## 一、根因(全部在分镜脚本 h3_prompt 内容,技能侧 LLM 直出缺陷)

| 症状 | 根因 | 实证 |
|---|---|---|
| 07/19 白发变黑 | subject_definitions 角色发色写错:沈照卡 platinum-white,LLM 写 "short black hair" | 镜7/19 Subject 1 行 |
| 12 Q版乱入 | 非内心戏镜也定义 chibi/miniature Subject,且引用经对齐层重排后变 "in ;" 悬空,H3 拿无参考图 Subject 乱画 | 镜12 `<Subject 4> is the chibi... in ;` |
| 06 群众配音主角动嘴 | 台词列 `(S3)画外·路人`,h3 里被写成 "The spectator shouts: <d>"(画面角色开口) | 镜6 detailed_description |
| 16-24 形象飘逸 | subject 行英文名(Shen Zhao)与角色卡中文 ID(沈照)无关联,参考图按 characters 序挂载但 Subject 序=登场序错位(镜7 characters=[阿影,沈照] 但 Subject 1=沈照 2=阿影) | 镜7 subject_definitions |

## 二、渲染端修复(纯函数,指纹/渲染共用,幂等)

### ① fixChibiEmptyRefs(chibi Subject 空引用清理)
- 非内心戏镜(对话列无 内心·):删除 chibi/miniature Subject 整行 → Q版乱入根治;
- 内心戏镜:保留 Q 版主体、清除 "in ;" 悬空引用(形态/动作自述仍在)。

### ② fixOffscreenDialogueSays(画外·台词强制 off-screen)
- 对话列存在 `画外·` 前缀说话人时,detailed_description 中该 <d> 的引导语若为
  普通开口动词(says/shouts/speaks 等)且无 <Subject N> 主语且未标 off-screen
  → 改写 "in an off-screen voiceover" + 补 lips-closed 从句。
- 边界保护:句尾 </d> 时切片越界已修。

### ③ fixSubjectHairColor(发色与角色卡校正,ctx 层)
- subject 行发色词与角色卡 image_prompt 发色不一致 → 按卡替换(幂等)。
- 角色归属用**英文特征词重叠**匹配(Subject 编号不可靠——镜7 编号与登场序
  反转;名字不可靠——英文名 vs 中文 ID)。
- 发色词正则容忍中间形容词("short black hair"/"platinum-white short hair")。

### 生效
- 全部在 finalizeAlignedPrompt/manjuAlignShotPromptReg 汇点,存量项目下次
  "一条龙/续跑"自动生效(plan 指纹变化 → 旧镜 stale 自动重渲)。

## 三、技能侧契约(源头治理)
- H3分镜脚本文档模板 + SKILL.md 补三硬新增:
  ①Subject 顺序=characters 登场序(Q版/画外排最后);
  ②发色/毛色与角色卡 image_prompt 逐字一致;
  ③chibi/Q版 Subject 仅限内心戏镜,且必须引用 Q 版参考图。

## 四、验证
- 新增单测:TestFixChibiEmptyRefs / TestFixOffscreenDialogueSays /
  TestFixSubjectHairColor(纯函数)+ TestShotFixRealShadows(真实 EP01 数据
  镜6/7/12/19 全过)。
- 全量 `go test ./...` PASS;enc_guard 体检正常;NiliX.exe 已重编译。

---

# 追加:fixOffscreenDialogueSays 崩溃修复(2026-09-02 10:02 王牌三岁半实锤)

## 崩溃
`panic: runtime error: slice bounds out of range [1326:573]` — fixOffscreenDialogueSays
在 ensurePlanAndPrompts 最终化阶段越界(goroutine 堆栈:fixShotPromptContent →
manjuAlignShotPromptReg → finalizeAlignedPrompt → finalizeShotPromptSlots)。

## 根因(两处)
1. **画面段外 <d> 越界**:h3_prompt 在 detailed_description **之前**(subject_definitions/
   summary 段残留)<d> 时,`segStart` clamp 到 idx 后仍 > start → `out[segStart:start]`
   切片起点大于终点直接 panic。
2. **多句连续画外第二句永不标注**:引导语窗口固定回溯 140 字符,跨过上一句时窗口内
   含已插入的 "off-screen voiceover" → 误判「已标注」跳过本句。

## 修复
- 跳过 `m[0] < idx` 的 <d>(只在画面段内标注);
- 引导语窗口上界改为 `prevDEnd`(上一句 </d> 之后)——多句连续各标各的;
- end 随插入同步累加(lips-closed 定位正确),每句处理完推进 prevDEnd;
- segStart > start 防御性 continue。

## 验证
- TestFixOffscreenNoCrash(复现形态:detailed 前 <d> + 画外对话,不 panic 且只标画面段)、
  TestFixOffscreenMultipleD(两句连续都标注)、TestWangpaiNoCrash(王牌三岁半 25 镜
  真实数据全量最终化无 panic)。
- 全量 go test PASS;NiliX.exe 已重编译(10:07)。

---

# 追加:内心段多行延续拆分修复(2026-09-02,《王牌三岁半》渲染与脚本对不上)

## 用户反馈
「《王牌三岁半》渲染的分镜跟脚本内容对不上」。

## 排查
逐镜对比源分镜脚本 vs plan:镜11 源 dialogue 是两行内心
`内心·棠棠："豆豆明明说测试台上有糖发，"` + `"怎么只有一个冰凉的环。"`
——plan 里第一行进 narration,第二行(无内心·前缀)被错拆进 **dialogue**,
且无说话人 → 渲染端误当角色台词/对不上脚本。

## 根因(parseScriptJSON)
台词列逐行拆分:只有**含「内心·」前缀的行**进 narration;内心段的多行引号
延续行(技能侧常见写法:前缀只在段首写一次)无前缀 → 落入普通 dialogue。

## 修复
- parseScriptJSON:上一行是内心/旁白段时,后续**引号开头**的延续行继续归
  narration(前缀只在段首保留一次);普通台词行照常进 dialogue。
- script_parse_ver 18→19(存量 plan 自动重解析)。
- 全库扫描:仅王牌三岁半镜11 命中该形态(废铁第15章镜22 内心各行都有前缀,
  不受影响)。

## 验证
- TestParseInnerMultiLine(两行内心全进 narration、dialogue 无残留);
- TestWangpaiParseInner(王牌三岁半真实脚本重解析:镜11 内心两句齐全、
  镜12 保留);
- 全量 go test PASS;exe 已重编译。

## 生效
替换 exe 后下次跑 plan 阶段自动按 ver19 重解析(日志「脚本解析器已升级」),
镜11 台词归位 narration,渲染配音/画面与脚本一致。

---

# 追加:远景人海「三个外国人」根治(2026-09-02,《王牌三岁半》镜1)

## 用户反馈
「镜1 内容里有三个外国人?」——镜1 是联邦测试大典会场**空镜**(无登场角色,
只描述万人看台远景),但 H3 渲染出三个清晰的外国人面孔。

## 根因
H3 模型对远景人群(crowd/spectators/tens of thousands)的常见行为:把远景人海
**具象化为几个清晰的个体面孔**,且拟人先验偏西方面孔 → 远景里出现「三个外国人」。
这是模型行为,不是脚本数据错误(脚本/场景图/提示词均无外国人元素,像素分析
确认场景图与渲染帧均无金发/西方面孔特写,肤色仅 0.3%)。

## 修复
1. **渲染端兜底**:finalizeAlignedPrompt 新增 `injectCrowdDiscipline`——空镜
   (无登场角色)且描述含 crowd/spectators/audience/grandstand/看台/观众/人海 时,
   队尾追加 CROWD DISTANCE 纪律句:人群保持远景剪影/模糊,无个体面孔/无人脸
   细节/人群成员不得成为前景主体。幂等。
2. **技能侧契约**:H3 模板补「远景人海剪影纪律」——远景人群只写 sea of
   silhouettes / blurred crowd shapes,禁止 individual faces;需要具体人物用近景+角色点名。

## 验证
- TestInjectCrowdDiscipline(空镜人群注入/有角色不注入/无人群不注入/幂等);
- 全量 go test PASS;exe 已重编译。
