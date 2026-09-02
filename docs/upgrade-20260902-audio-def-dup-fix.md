# 升级记录:Audio 定义行内嵌台词根治(2026-09-02)

## 背景

《王牌三岁半》EP01 重跑后配音仍重复、渲染与脚本不符(用户 2026-09-02 愤怒反馈「全部重新来,还是一样的」)。此前已有 scriptDedupShotLines(ver18)做跨镜串句去重,但**plan 落盘产物中台词仍出现两遍**,且 parse 层测试(scriptParsePlan 直接产物)只有 1 遍——差异在渲染期 finalize 链注入。

## 根因(镜15 实锤,plan h3 6999 字符 vs 源 2029 字符)

源脚本 detailed_description 一句内连写两句画外音(官方 off-screen voiceover 句式):

```
Two voices cut through the roar: a middle-aged man's voice off-screen in the stands (S25), jeering
and loud, says in an off-screen voiceover: <d>联邦史上最低！</d> and a woman's voice off-screen
(S26), spiteful and shrill, adds in an off-screen voiceover: <d>绝缘体！上辈子造了孽！</d>
```

finalize 链 `manjuOffscreenDescs` 以 `in an off-screen voiceover` 为锚点向前取「上一句号后片段」当画外声线描述——**两句画外音在同一句内无句号分隔**,第二个锚点回溯时把前一句的 `<d>台词</d>` 一起收进 desc(140 字符截断后更甚),注入 Audio 定义行:

```
<Audio 3> is the voice-timbre reference for loud, says in an off-screen voiceover: <d>[Chinese] 联邦史上最低！</d> ...
```

→ 同一句台词在 h3 出现两处(详细描述一段 + Audio 定义行一段),H3 念两遍 = 用户听到的重复配音。

### 三个独立缺陷叠加

| # | 缺陷 | 位置 | 后果 |
|---|------|------|------|
| A | 声线描述提取跨 `<d>` 台词块 | `manjuOffscreenDescs` | 台词被写进 Audio 定义行 → 念两遍 |
| B | `alignAudioDefsReg` @offscreen 重写剥掉 `the off-screen voice described as` 前缀 | `alignAudioDefsReg` | `injectOffscreenVoiceBindings` 幂等锚失效 → 每次 finalize 重复注入叠加(plan 出现 3 行畸形 Audio 定义) |
| C | 补写判定 key 带引号 | `scriptValidateShots` | 台词列「(S25)画外·观众乙:」剥前缀后仍带引号,与 h3 内 `<d>` 无引号形态比对恒 miss → 每次导入误补写重复 `<d>`(run.log 镜15「检出 2 句已自动补写」即此) |

### 为什么用户重跑无效

plan.script_parse_ver=19 == 代码 19 → ensurePlan **直接复用旧 plan 不重新解析**,固化的畸形 h3 原样进渲染。修复必须 bump 版本号强制存量 plan 重解析。

## 修复

1. `manjuOffscreenDescs`:锚点前段若含 `</d>`,从最后一个 `</d>` 之后取段;兜底再剥 `<d` 之前内容——**台词永不进声线描述**。
2. `alignAudioDefsReg` @offscreen 重写保留 `the off-screen voice described as` 前缀(幂等锚持续命中,防叠加注入)。
3. `scriptValidateShots` 台词补写判定 key 剥首尾成对引号(`""`/`“”`/`「」`/`『』`)。
4. `manjuScriptParseVer` 19→20:强制存量 plan 重解析自愈。

## 验证

- 新增 `TestOffscreenDescsNoDialogue`:desc 提取与注入行均不得含 `<d>`/台词内容(PASS)。
- `TestDedupFullPath`(完整 scriptParsePlan 路径):镜15 h3 `联邦史上最低` 次数 = 1(PASS)。
- 全量 `go test ./internal/api/` PASS。
- 全库审计:仅《王牌三岁半》EP01 镜15 一处 plan 受影响(ver19),bump 后重跑自动重解析。
- NiliX.exe 已重编译(build.bat),用户重启应用后生效(plan 重解析自动触发)。
