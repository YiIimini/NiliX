# 升级记录:全库(8 本书)人群/屏内容统一排查治理(2026-09-02)

## 背景

用户指令「所有小说都进行统一排查处理」——将王牌三岁半人群/悬浮屏治理(知识库《H3群演与Q版角色质量控制实战》标尺)推广到全部书库。

## 摸底

书库 8 本 / 636 章 / 20979 镜,四类问题全量扫描:
- 屏内容未钉死(句级初扫 1149):大量误报——"screen" 在六段式里多指**画面框本身**(fills the screen)、家具屏(wooden/paper)、展柜(display case)、画外音句式(on-screen);
- 人群否定句(no individual face 等):**0 镜**(仅王牌三岁半本次已改);
- 个体化诱发词(25 镜):逐例定性后 9 镜真阳性,其余为误报(Subject 引用的角色/已写背面/半透明幻影/兽首/画外声音/儿童特定群体);
- heads bobbing 人群语境:~10 处。

**决策:拒绝纯正则批量改 636 章**(教训台账「机械拆镜之戒」:机械修复机械=系统性缺陷),采用源侧逐例修复+渲染端兜底双层。

## 源侧修复(14 处,逐例人工定性)

| 书 | 章/镜 | 修复 |
|---|---|---|
| 人算不如天算 | 14/1 | 人群 heads bobbing → the whole mass swaying |
| 人算不如天算 | 14/13 | 无名个体(挥拳男/抱包袱女) → 群体动作(fists rise / bundles clutched) |
| 人算不如天算 | 22/12 | bodies shifting, arms waving, heads bobbing → as one mass |
| 人算不如天算 | 42/18 | 剪影 heads bobbing → swaying |
| 全小区就我一个活人 | 51/27 | 妖众 heads bobbing → rocking with laughter |
| 废铁按斤卖 | 21/9 | 草帽男喊话 → 帽檐阴影遮脸(正向避脸) |
| 我的影子会咬人 | 14/2 | 学生 heads bobbing → murmuring |
| 我的影子会咬人 | 3/29 | 人群 heads bobbing+eyes darting → 群体转向公告 |
| 杂毛神兽 | 12/11 | 剪影 heads bobbing → stirring restlessly |
| 杂毛神兽 | 123/10 | 剪影 heads bobbing → stirring in agitation |
| 杂毛神兽 | 170/13 | 村民 heads bobbing → shoulders shaking |
| 王牌三岁半 | 2/12 | 西装男伸手 → a hand in a suit sleeve(去个体) |
| 王牌三岁半 | 31/8 | 教室 heads bobbing → 删(浪潮句已足) |
| 王牌三岁半 | 67/20 | 法庭个体(拭泪女/工装男) → 群体动作(hands press to mouths) |

不动(误报定性):Subject 引用角色(影子38/24)、背面写法(王牌76/24)、半透明幻影(金丹35/12)、兽首(杂毛120/122/128)、儿童特定群(王牌21/5)、画外声音(轮回1/6)、有卡角色(全小区50/34 紫裙大妈)。

## 渲染端:injectScreenDiscipline 屏内容兜底纪律(全库统一)

新增 `injectScreenDiscipline`(finalizeAlignedPrompt 链,CROWD DISTANCE 之后):显示型屏(全息/悬浮/巨幕/手机/监控限定词)在提示词内无任何内容说明时,注入 SCREEN CONTENT 纪律(正向:屏上只放抽象数据/发光文字/数字,禁人脸特写屏)——源侧 7b 契约管新章,guard 管全库存量,双层。

判定(句级+双层钉死判定,宁缺勿滥):
- 屏句 = 含 screen/display 实词且非「画面框/家具屏/展柜/画外音」句式;
- 全镜任一屏句含内容词(numeral/words/shows/displays/footage/photo…)→ 已钉死跳过;
- 否则能力屏句 ±1 邻句含内容词 → 该屏已钉死;
- 全部屏句未钉死才注入,插 detailed_description 段前(高服从位),幂等锚 SCREEN CONTENT。

**真实库触发率验证:68/20979 镜(0.32%)**,抽验样本全部真阳性(手机屏无内容/全息屏 flicker 无内容)。

## 验证

- 全库终审计:个体化真阳性 0、人群 heads bobbing 真阳性 0(2 残留均为误报);
- go test ./internal/api/ 全量 PASS(TestInjectScreenDiscipline 六场景+TestInjectCrowdDiscipline);
- enc_guard scan novel:1384 文件全部 UTF-8 正常;
- NiliX.exe 重编译(17:07)。
- 修改过的源脚本指纹变化 → 各书下次渲染自动重解析(syncScriptFromNovel→novel_fp 失配)。

## 关联

- 知识库标尺:创作管理/H3群演与Q版角色质量控制实战.md
- 技能侧:H3分镜脚本文档模板.md 第 7 条(正向剪影)+ 7b(屏内容钉死)、分镜派发模板.md
- 前序:upgrade-20260902-audio-def-dup-fix.md、upgrade-20260902-crowd-screen-kb-fix.md
