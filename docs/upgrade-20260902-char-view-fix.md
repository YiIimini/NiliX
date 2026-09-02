# 角色视图/Q版资产生成根治(2026-09-02,《我的影子会咬人》管线实锤)

> 用户反馈:①阿影 侧面生成异常(两个相同角色);②沈照/夜枭/沈伯/王胖子 Q版
> 形象「一大一小两个」;③墨千秋 全身双人+Q版与正面不匹配(头/手/脚)。
> 用户质疑:「有让正角在技能端直出 Q版形象提示词啊,为什么早上修了还是原样?」

## 一、为什么早上修了还保持原样

`manjuFacelessChar`(无脸剪影判定)早上已提交(ac7a147,18:13),但它挂在
`manjuViewPromptBuild` 的 **isBeast 之后**——阿影 species=影灵,先被
`manjuIsBeast` 拦截走「四足兽形锚」(purely animal creature form on four paws),
剪影分支永远执行不到。判定顺序错误 = 修复未生效的根因。本次把剪影判定提到
最前(真正接管),并补 full/detail 剪影锚 + 剪影 Q 版分支。

## 二、三个根因与修复(portrait_gen 5→6 / views_gen 10→11 / q_gen 23→24)

### ① 无脸剪影类角色(阿影 side 双体/双人)
- 根因:species=影灵(非人)→ `manjuIsBeast=true` → side/full 走兽形锚
  「four paws / no humans」,而阿影实际是「人形黑雾剪影」——兽形锚与人形剪影
  互相矛盾,Krea-2 把黑雾人形拆成上下两个体/两个相同角色;Q 版同样走兽形
  fur 分支(黑雾剪影套毛茸茸兽形)。
- 修复:
  - `manjuViewPromptBuild` 判定顺序:无脸剪影类 **先于** isBeast;
  - 新增 `manjuFacelessViewAnchors` full/side/detail 剪影锚(纯轮廓单一体,
    禁人脸/禁四足兽/禁肢体分离);
  - `manjuQPrompt` 新增剪影 Q 版分支:黑雾团子+蓝点眼+雾缕,禁人脸禁毛禁兽形。

### ② Q版「一大一小两个」(沈照/夜枭/沈伯/王胖子)
- 根因:2026-09-01 加的形态构成兜底写 "a tiny **palm-sized** chibi figure"——
  palm-sized 语义=「画面里一个巴掌大的小人」,叠加 img2img 底图(正常比例主图)
  后模型把两者都画出来=一大一小两个角色。
- 修复:删 palm-sized,改「a single 3-head-tall chibi figure filling the frame」
  + 显式单角色锚(no second figure / no miniature version / no size contrast);
  非实体分支的 palm-size 同步删除;所有人形 Q 版收尾统一单角色硬锚。

### ③ 墨千秋 full 双人 + Q版不匹配
- 根因 A(Q版不匹配):image_prompt 的 "**wisps** of darkness at his feet" 被
  `manjuIsNonPhysical` 正则裸 `wisp`(无词边界)命中 → 人类暗影系角色被判成
  「发光数据精灵」→ Q 版走 holographic data spirit 分支(半透明光流体),
  与正面(黑发暗影人类)头/手/脚全不对。
- 修复:正则收窄为 `(?:light|energy)\s*wisp` + 数据/全息/幽灵确凿语义词,
  排除 darkness/shadow/mist 语境。
- 根因 B(full 双人):主定妆 prompt 无单角色约束,黑雾缕被画成第二个人
  (视图以主图为 img2img 底图,主图双人=视图全双人)。
- 修复:主定妆 `manjuPortraitPrompt` 统一收尾单角色硬锚
  (one single character only, no second figure, no shadow figure)。

## 三、技能侧契约(NiliX-Novel SKILL.md)
q_form 六硬规范补 ⑦⑧:禁 palm-sized/tiny palm-size 措辞(一大一小双人诱因);
单角色只写一个角色,氛围副体(黑雾/光尘/阴影)不得写成第二个人物。

## 四、验证
- 新增单测:TestFacelessClassification(阿影剪影锚/Q版不走兽形、墨千秋不误判
  光精灵/保留着装锁、单角色锚、无 palm-sized)、TestQPromptSingleFigure。
- 全量 `go test ./internal/api/` PASS;enc_guard 体检正常;NiliX.exe 已重编译。

## 五、生效方式
代数 bump(portrait_gen=6 / views_gen=11 / q_gen=24)→ 该项目下次跑 assets 阶段
自动清除全部旧主图/视图/Q版按新提示词重出(视图/Q版 mtime 联动)。其他项目
(非影灵/无 palm-sized 存量)仅 Q 版受单角色锚影响,重跑 assets 自动刷新。
