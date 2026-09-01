# 分镜脚本 JSON 化(2026-08-31,script_parse_ver 15→16)

用户规则:「爽文小说技能输出提示词和分镜脚本直接输出 JSON 格式,NiliX 同步调整,
后续解析更方便更容易维护」。

## 背景:md 表格格式的致命缺陷

Markdown 分镜表格在 LLM 输出换行到画面/台词列时**断行丢列**——实测 541 行异常
(列数只剩 5),Go 解析器 reScriptTableRow 命中 21/25 行,EP01 渲染 plan 只有 21 镜
(丢 4 镜),用户 4 个问题(重复配音/不细腻/角色不匹配/Q版)部分源于此。

## JSON schema(新格式)

```json
{
  "book": "《书名》", "episode": 1, "chapter_title": "章节名",
  "global_style": "全局风格句",
  "bridge": {"prev_ending": "", "opening_beat": "", "position": "", "closing_hook": "", "next_entry": ""},
  "shots": [{
    "shot_id": 1, "shot_size": "景别", "camera": "运镜",
    "action": "【场景名】画面", "dialogue": "(S1)角色:\"台词\" 多行\\n分隔",
    "characters": ["登场角色"], "light": "光影", "sound": "音效", "duration": 5,
    "style": "可选", "h3_prompt": "六段式全文"
  }]
}
```

硬约束:镜号 1 连续严格递增;dialogue 只写台词/内心/旁白(画面信息一律进 action);
characters 含说话人(渲染端挂参考图依据,漏写=无脸);h3_prompt 六字段缺一不可、
同一句只出现一次;4-15s 且语音预算不超。

## 改动清单

1. **NiliX 解析器**(internal/api/manju_script_parse.go,ver 15→16):
   - scriptParsePlan 检测 .json/首字符 `{` → parseScriptJSON 结构化解析
   - 公共组装抽为 buildPlanFromRaws(md/JSON 共用:语音补偿/质检/角色场景卡/场景匹配)
   - shots[].characters 显式声明并入登场角色(文本匹配兜底,说话人仍强制入画)
   - 内心/旁白双写去重(第 7 项质检):同句既被 <Subject N> (Sx) says 又 narrator
     画外音 → 保留画外音版删角色开口版(防重复配音+乱对嘴型,用户问题 1 脚本根源)
2. **存量转换**:tools/md2json.py——336 章 md → JSON(断行修复:非 | 开头行并入
   上一行直到列数达标),md 已删除(备份在 tools/logs/backup/)
3. **重写工具**:tools/storyboard_regen.py 输出 JSON(assemble_json,含 generated 标记)
4. **技能侧**:
   - references/分镜派发模板.md 输出段改为 JSON schema + 六条硬约束
   - SKILL.md 格式契约/质检对象更新为 .json
   - scripts/storyboard_check.py 支持 JSON(json_to_md 渲染回等效 md 复用全部分析)

## 验证

- 全量 JSON 解析:11383 镜零丢镜、六段式齐全(TestJSONScriptParseAll)
- storyboard_check 第1章 100% PASS、全本 98.7%(与 md 一致,转换无损)
- go build/test 全过;enc_guard 零异常;deliver_check PASS

## 生效

- 渲染时 plan 自动重解析(ver16 > 旧 plan ver15/14)→ 旧 plan 丢弃,JSON 全量吸收
- 新脚本(技能生成/工具重写)直接输出 JSON
- md 兼容解析保留(旧项目不破坏),新项目一律 JSON

## 追加(同日二批):素材卡 JSON 化 + 脚本检测修复

用户实测(金丹一万重 10:15):「方案生成无效…追加精简约束重试」——有 JSON 脚本
却走 LLM 直出。根因:`autoStoryboardForEpisode` 匹配分镜脚本硬编码 `.md`,
JSON 化后 .md 删除 → 0 命中 → 回退 LLM 直出(1.8 万字章节注入 LLM 输出截断)。

修复:
1. **autoStoryboardForEpisode .json 优先匹配**(md 兼容)——EP01→第1章_*.json,
   TestAutoStoryboardFindsJSON 覆盖
2. **人物生成提示词.json / 场景提示词.json**(技能侧新格式,NiliX 解析优先 JSON,
   md 兼容):parseCharCardsJSON/parseSceneCardsJSON(字段与 md 解析同构:
   id/gender/age/role/species/voice/memories/image_prompt/q_form/second_form)
3. 存量 7 本素材卡 md→JSON 转换(tools/md2json.py cards 模式,md 保留双格式),
   全库 92 角色卡全有 image_prompt(TestCardsJSONAllBooks)
4. 技能侧 SKILL.md 素材契约补充 JSON schema;提示文案/体检适配 .json
5. 《杂毛神兽》188 章全面重写(此前用户暂缓,本次全库同步;后台进行中,
   输出 JSON 新格式)

## 追加(同日三批):全库同步 + 脚本检测修复验证

1. **autoStoryboardForEpisode .json 匹配修复**(用户 10:15 实测「方案生成无效」根因):
   有 JSON 脚本却走 LLM 直出——匹配硬编码 .md。已改 .json 优先(md 兼容),
   TestAutoStoryboardFindsJSON 覆盖。
2. **《杂毛神兽》188 章全面重写完成**(此前暂缓,本次全库同步):新规则拆镜
   (80-130 字/镜)+ JSON 输出;含 6 章补跑与 split 阈值修复(74 字切分,
   修复恰好 75 字不切导致 82 字单组)。旧 md 188 个移入 tools/logs/backup/杂毛神兽/。
3. **素材卡 JSON 化**:人物生成提示词.json / 场景提示词.json(NiliX 解析优先,
   md 兼容);存量 7 本转换(全库 92 角色卡全有 image_prompt);技能侧 SKILL.md
   素材契约补充 JSON schema。
4. 全库 7 本最终:16879 镜(旧约 12200,+38%),站位 98%/动作 97%/运镜 95%,
   解析零丢镜;杂毛神兽 check 对白覆盖 98.8%(缺失为文学性引号词)。

## 追加(09-01):一条龙/AI 一条龙分流规则(用户需求)

- **普通一条龙/小说导入/全本自动分集**:检测目录里是否存在对应集的 json 分镜脚本
  (第N章_*.json)→ 有则脚本直出(零 LLM,脚本权威);不存在才走 LLM 直出方案。
- **AI 一条龙(agentMode=true)**:全走 LLM + Agent 全流程(剧本复核→渲染→审片判分→
  自动返工),**跳过脚本检测强制 LLM 直出**——manjuCtx 新增 agentMode 字段,
  ensurePlan 两处脚本检测加 `!ctx.agentMode` 条件;startManjuRun 单集/自动分集
  路径均透传。
- 生效方式:重启 NiliX.exe(旧 exe 无 .json 检测,全本自动分集曾回退 LLM 直出)。

## 追加(09-01 二批):渲染效果优化(Q版/侧面)

1. **Q 版提示词错位根因修复**:md→JSON 素材卡转换时多形态段提取错误——阿影卡
   第一个代码块是 Q 版提示词,被当主形象 image_prompt,q_form 为空(渲染端主形象
   用 Q 版词、内心镜 Q 版挂载无 q_form)。转换器修复:按形态标记分配
   (【Q版】→q_form、【主形象】/无标记→image_prompt、【真身/化形】→second_form),
   7 本全部重转;阿影三形态齐(影子形态/Q版黑团子/化形少年)。
2. **程野补角色卡**:重要配角(第1章挑战者,后续常驻)此前无卡无参考图,剪影
   渲染无据——补卡(眉骨疤/石拳灰壳/挑战者劲装)+重转。
3. **side 侧面视图接入**:
   - charViewRels 挂载 picks 加 side(单角色 4 视图/双角色 3+3/三角色 3+2+2,
     总参考 ≤8 张+场景 1 张,符合 H3 Omni-reference ≤9)
   - manjuExpectPicSlots 槽位同步(4/6/7,防绿萝式剥引用事故)
   - 群演轻量卡(minor)资产阶段补 side 视图(只多 1 张)——主持人侧面镜头
     有侧面参考图,渲染崩修复
   - 槽位测试更新(TestManjuExpectPicSlots 4/6/7)
4. NiliX.exe 已重编译(09-01 01:31,含 JSON 检测/agentMode 分流/Q版/side 全部修复)。

## 追加(09-01 三批):无脸角色 side 视图修复

阿影 side 视图生成「上下两个」实锤:Krea2 img2img 基于主图(黑雾人形)生成侧面时,
人形 side 锚的 face/nose/chin/one eye 语义对无脸角色是乱画邀请,模型尝试画脸五官,
把黑雾人形拆成上下两个体。

修复(三层):
1. 人形/兽形 side 锚加防重复句:single continuous figure, one character only,
   no duplication, no mirror image, no double exposure, no two figures stacked
2. manjuFacelessChar 判定(非人且形象含 shadow/mist/silhouette/无脸/影子)→ side 走
   剪影锚:SILHOUETTE PROFILE view, single continuous dark figure, no facial
   features, no two figures stacked or split
3. NiliX.exe 重编译(09-01 01:38)

## 追加(09-01 四批):Q版年龄/双体/群演视图/发型差距

用户 10 项资产效果反馈深度排查修复:
1. **Q 版无年龄感(夜枭/沈伯/白眉)**:age 字段缺失根因——转换器只从括号描述提取
   年龄,而卡标题「(人类·男,星陨组织头目)」无数字,image_prompt 首词带 45-year-old
   未提取。修复:转换器+Go parseCharCardsJSON 双兜底(image_prompt "N-year-old"),
   7 本重转,age 覆盖 77/93(夜枭45/沈伯58/白眉70岁归位)→ Q 版年龄锚(皱纹/老年
   特征)生效。
2. **墨千秋 full 左右双体**:full 锚此前无防双体句(上批只加了 side)。full 锚
   (人形/兽形)补 single continuous figure, no duplication, no mirror image。
3. **主持人只有正面和侧面(问 10)**:群演卡(minor)设计只出主图+正脸+side——
   用户期望完整视图。已改:minor 卡也生成 full/detail/q 完整视图(群演 Q 版/全身/
   细节均可用)。
4. **Q 版与正面差距大(王胖子/主持人/星陨打手/记者/宿管阿姨)**:age 缺失导致
   年龄/体型锚失效是主因之一(已修);群演卡完整视图后 Q 版可正常生成对比;
   发型跟随 HAIR LOCK 已前置,剩余差距待重生成后按产物微调。
5. NiliX.exe 重编译(09-01 01:56)。
