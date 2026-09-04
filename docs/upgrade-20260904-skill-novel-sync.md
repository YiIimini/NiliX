# 技能侧 + 三部小说存量同步升级(2026-09-04,画质升级的源头与存量落地)

承接同日渲染端画质升级(docs/upgrade-20260904-quality.md)——运镜官方词表返正与
提示词正文扩容两个根因,本篇完成「技能侧规范源头 + 现有三部小说全库存量」的同步。

## 一、背景

渲染端升级后仍有两层旧账:
1. **技能侧规范还在教黑话**:SKILL.md/分镜模板/派发模板/few-shot 示例的运镜措辞是
   2026-09-03 的"电影术语"(cinematic dolly push-in/orbital arc/crane rise)——行业黑话,
   H3 训练对齐的是官方 15 词表动词句式,服从性打折。且 storyboard_check 的 CAM_MOTION_SENT
   校验器一直认官方动词句式,示例却教黑话——校验器与示例不同源(契约 K 误报根源)。
2. **三部小说存量分镜**:1295 镜含黑话残留;99.8% 的镜(5178/5207)detailed_description
   ≤220 词(渲染端旧约束时代产物),远低于官方"350-500 词、as detailed as possible"。

## 二、技能侧修改(NiliX-Novel 仓库,6 文件)

| 文件 | 改动 |
|------|------|
| SKILL.md | 六段式运镜示例句改官方动词句式 + 返正说明 |
| references/H3分镜脚本文档模板.md | 第 3 条"电影级运镜措辞"整段替换为"官方词表运镜措辞"(推/拉=pushes in/pulls out、横移=trucks left/right、摇=pans left/right、跟=tracks the moving subject、环绕=arcs around the subject、升降=pedestals up/down、变焦=zooms in/out;幅度+速度三要素照写);示例句同步 |
| references/分镜派发模板.md | camera 字段注释的英文句式示例改官方 |
| agents/分镜示例.json | 2 处 few-shot 黑话句官方化 |
| references/教训台账.md | 追加 2 条:①运镜黑话负优化返正(措辞升级必须以官方指南词表为唯一标尺+与校验器词表同源核对)②提示词正文体积=画面细节密度 |
| scripts/storyboard_check.py | 无需改(校验器本就认官方动词句式;示例改官方后 K 契约自然同源命中) |

## 三、三部小说存量返工

### ① 运镜黑话机械返正(tools/rework_camera_officialize.py)

与渲染端 manjuOfficializeCameraVerbs 逐条同源(单一事实源),精确反解 2026-09-03
rework_cinematic_phrases.py 写入的 6 个模式,幂等,.bak 保护:

| 书 | 返正处数 | 残留 |
|----|---------|------|
| 万物皆可反悔 | 587 | 0 |
| 被论斤卖掉后我成了全网AI之母 | 0(分镜较新,无黑话) | 0 |
| 递了三千年葫芦,她给自己发了飞升任务 | 707 | 0 |
| **合计** | **1294** | **0** |

crane rise 3 处为气闸句叙事名词(非运镜指令),保留。

### ② detailed_description LLM 扩容(tools/rework_dd_expand.py)

逐镜 LLM 扩写到 250-350 词(deepseek-chat)。**安全设计**:
- 只重写 detailed_description 段,其余段逐字保留;
- 校验闭环(不过=保留原文不落盘):①`<d>` 台词块序列逐字一致 ②Subject/Picture/Audio/
  Shot/Sx 引用多重集合一致 ③词数 200-430 ④不新增中文(原文已有的中文角色名放行)
  ⑤气闸句/运镜句锚保留 ⑥失败重试 1 次;
- 断点续跑(tools/logs/dd_expand_progress.json 按镜记)+ .bak 保护 + --dry-run/--limit/--book;
- 试跑 20/20 全过,抽检:313 词、事实保真、台词逐字、运镜官方句式、细节自然
  (材质/光影/微表情/环境动效)。

结果:见文末「扩容结果」(全量跑完成后回填)。

### ③ 渲染联动(自动,无需手动)

分镜脚本变化→syncScriptFromNovel 同步工作副本→脚本指纹失配→「脚本已更换」清产物
重生成→h3_prompt 更新→shotCondFingerprint 变化→重编重渲。**三部书下次渲染自动全量
重出新片**(叠加同日 PDD 8步/fp16/max 质量档)。

## 四、验证

- 技能侧全仓扫描:dolly/orbital/crane 指令性残留 0(剩余命中均为"返正说明"文字)
- 黑话返正:dry-run 与落盘一致,零残留,幂等
- 扩容:试跑 20/20 全过 + 质量抽检;全库结果复验(storyboard_check 三本)+ 词数分布
  统计见文末
- 渲染端:go build/vet/全量单测全绿(本轮渲染端零改动,沿用上一轮)

## 五、扩容结果(全量完成)

- **成功 5160/5168 镜(99.85%)**,多轮收敛(首轮 4918 → 对白密集镜下限放宽后追加 242);
  残量 8 镜保留原文(0.15%,LLM 对该批镜扩不动,150-220 词可用,不构成废片)
- **扩容后词数分布**:<200 词 38 镜 / 200-250 词 364 镜 / **250-350 词 3916 镜(75.3%)** /
  350-430 词 889 镜——**250 词+占比从 0% 升至 92.3%**
- **零误伤**:175 文件全量比对 .bak,`<d>` 台词块逐字一致镜数 = 5207/5207(校验闭环有效)
- **storyboard_check 三本书复验全部 PASS**(覆盖率 100%);复验中暴露递葫芦第1章
  "全片轴时码"形态(镜 N prompt 写自身绝对时码 [Shot N] At MM:SS)——渲染端
  alignShotTimecodes 本就专门归一该形态(首带码段做 offset 平移 clip-local),
  校验器已同步放行(严格 +1 递增=兼容形态,打印 info 不 FAIL)
- 被论斤书 retention_analysis 含中文 2 字为既有小瑕疵(官方格式⑤ WARN,PASS 不受影响)
