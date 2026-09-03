# 2026-09-03 · 渲染漂移四修 + 写实电影级 + 画布四期 + 全库源头同步

> 本日三段工作:①单镜画布独立窗口工作台;②渲染漂移深度排查四修 + 写实电影级升级;③存量分镜全库返工 + 技能侧契约同步(双层仓库联动)。

## 一、渲染漂移四修(递了三千年葫芦 EP01 抽样实证)

用户主诉「镜头内容漂移,分镜脚本和 H3 渲染不匹配」。抽样对照分镜 JSON ↔ plan h3 ↔ 调试 API 最终 prompt,六段式字段层 100% 逐字保留,漂移全部在 h3_prompt 包装层,四个确切断链点:

| # | 问题 | 根因 | 修复(internal/api) |
|---|---|---|---|
| P0 | 镜8 两角色同脸(换脸) | `alignPictureRefs` 的 `replacePictureTags` 按 remap **全文**替换 Picture 标签,「同号全 prompt 同义」假设在多主体错位场景不成立(Subject1 的 P4 需改 1、Subject2 的 P4 本正确 4→4 不登记,全文替换把它也改成 1) | **行级替换**:`lineMaps[行原文]→该行映射`,只改判定需重排的行,已正确行/权威清单/retention 一概不动;漏改远轻于错改 |
| P1 | 内心独白被 narrator 旁白音色念出(镜17-20) | 源头 h3_prompt 自带 `<Audio 1>...the narrator...` 错误定义行,命中 `injectOffscreenVoiceBindings` 幂等锚,`innerVoiceFor` 改绑逻辑整体跳过 | 新增 `fixInnerVoiceDefLine`(幂等锚**之前**执行):narrator 定义行改写为 `the quiet inner voice of {角色id}, {音色短语}, voice style, delivered {情绪}`;desc 组装抽 `innerVoiceDesc` 单一事实源;挂载侧 `offscreenVoiceKeyFor` 按 inner voice 形态同源解析 |
| P2 | 静态镜被注入"运镜必须可见"矛盾指令 | camera「固定(Static)」括号直取 "Static",`Contains(ph,"static")` 大写漏过 → 走运动纪律分支 | static 判定 ToLower + `manjuNormalizeStaticPhrase` 归一裸静态词(Static/static camera/locked-off → canonical 短语) |
| P2 | 单段镜 detailed 被标 [Shot 2] | `alignShotTimecodes` 把 retention_analysis 的跨镜引用 `(appears in [Shot 1])` 计入镜内切点序号 | `\x00RETENTION\x00` 占位切段保护,段内原样 |

## 二、写实电影级升级(去 AI 味)

- **运镜映射电影化**(`manjuCameraPhrase`,23→43 条):方向性(左/右摇=smooth pan to the left/right、左/右横移=dolly truck to the left/right)、跟拍=steady cinematic tracking、环绕=slow orbital arc、变焦/手持/微移;已实证有效的「幅度+速度」结构保留;固定镜写法规范。
- **新增 CINEMATOGRAPHY 纪律**(`injectCinematographyDiscipline`,detailed_description 前高服从位,先删后插幂等):`photorealistic cinematic film look - natural motivated lighting, realistic skin/material textures, shallow depth of field, subtle film grain, muted filmic color grading, physically grounded weight; absolutely no anime/cartoon/CGI or over-stylized rendering`。条件=文本级写实判定(含 photorealistic/realistic 且不含 anime/cartoon/manga/3D render/stylized/illustration/pixar/low-poly;"stylized" 遇 chibi 例外放行=写实主体+Q版内心镜)。
- 与 `manjuRealizeStyle` 词级替换互补(词管风格句,纪律管镜头语言)。

## 三、存量全库返工(novel 3 本 208 章 6326 镜)

- **tools/rework_inner_voice.py**:内心独白镜(333 处 narrator 写法,98.8%)主语机械改写 `The quiet inner voice of {角色id} says in an off-screen voiceover`;只动内心镜,客观旁白不碰;331 处落盘(2 镜无 narrator 主语由渲染端兜);.bak 备份。
- **tools/rework_cinematic_phrases.py**:①风格句写实化 6328 处——替换表与渲染端 `manjuRealizeStyle` **完全同款**(含存量特有 semi-realized 拼写变体),源头写对后渲染端替换幂等不命中;②运镜句电影化 2761 处(pushes in/pulls back/orbits → performs a slow cinematic dolly push-in/dolly pull-back/orbital arc with ...);pan/track/holds 已是电影语言不动。
- 复验:anime-stylized 残留 0、旧运镜句残留 0、幂等复跑零命中、JSON 0 损坏、storyboard_check 三本 PASS。
- 渲染端配套:`manjuNarratorDescs`/`manjuOffscreenDescs` 识别 "inner voice of" 句式(防源头改对后渲染端锚失效;开头短路必须同时放行 inner voice of——单测实锤)。
- 存量 6326/6326 镜(100%)含写实锚 → CINEMATOGRAPHY 纪律注入全覆盖;提示词指纹链自动 stale 重渲,无需 bump 版本。

## 四、技能侧契约同步(NiliX-Novel 仓库)

- 内心独白硬契约:`The quiet inner voice of 角色名 says in an off-screen voiceover, in a 情绪 inward voice`(禁 narrator)——落地 H3分镜脚本文档模板(5处)/分镜派发模板(2处)/分镜师.md 检查表/few-shot 分镜示例/SKILL.md(3处)。
- 电影级措辞:运镜写法条(电影术语+方向词汇表,固定镜 stays locked off)+写实风格句固定措辞(动机光/浅景深/胶片调色/物理重量)+few-shot 10 镜风格句/2 运镜句同步。
- storyboard_check.py 新增**契约 T**:写实锚与 anime-stylized/semi-realistic 并存 → WARN(源头防线);三本复验零误报。
- 教训台账新增「内心独白 narrator 音色错人」条目(根因/对策/方法论沉淀)。

## 五、画布四期:单镜调试独立窗口工作台

- 弹窗重构两栏布局(左画布 46vh/右参数栏 322px;≤1120px 退化堆叠),弹窗与工作台共用 `_shotDebugHTML`/`_shotDebugWire`。
- 「⛶ 独立窗口」按钮 → `window.open(pathname + "#shotstudio=cfg/ep/shot", "nilix_shotstudio")` 命名窗口;接收端 `enter()` 解析 hash → `_studioPending` → `loadProjects(cfg)` 就绪后 `openShotStudio`(97vw×94vh 全屏工作台);工作台内隐藏按钮防套娃。
- 模式感知:studio 保存后原地重开刷新覆盖徽章;模板库 reopen 回调;重置走 uiConfirm。
- 参数卡片化(dbg2-field:图标+镜级覆盖徽章+全局值 hint+focus 高亮)。

## 六、build.bat 编码根治

- 根因:cmd 按 GBK 解析 bat,UTF-8 中文注释破坏 PowerShell `^` 多行续行 → 注释碎片变伪命令(Git Bash `cmd /c` 必现)。
- 修复:版本刷新外置 `tools/build_refresh_version.ps1`(UTF-8 with BOM,按脚本位置推导根目录,0 匹配报错退出);bat 纯 ASCII+CRLF;启动/停止.bat 无 `^` 续行不触发,未动。

## 验证

- 新增负面用例单测:换脸行级(真实 plan 冒烟:镜8 两角色各指各槽)/retention 保护/静态矛盾/电影级三分支/内心音色改绑幂等/inner voice 识别——全 PASS;`go test ./internal/api/` 全量 PASS(skip 外部书库依赖项)。
- enc_guard:internal/tools/技能仓库全部正常。版本 bump 202609032237,exe 已重编译。
