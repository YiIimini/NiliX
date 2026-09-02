# 升级记录:渲染保真度根治(纪律瘦身+运动镜断链)+ 技能侧同步 + UI 四项(2026-09-02)

## 背景

用户反馈:技能侧输出的分镜脚本 H3 渲染"变味"——人物站位、场景运镜没表现到位。深度排查(实证三件套:源→plan→ComfyUI 提交 diff + 光流测量 + 提交参数审计)锁定三个根因,按用户拍板 1→3→2 顺序全做,技能侧同步,另加 UI 四项。

## 根因(我的影子会咬人 EP01,19:17-20:20 新 exe 渲染实证)

1. **队尾纪律海啸**:镜7 提交全文 8453 字符,6770(80%)是 soundscape 后的 9 条纪律墙,CAMERA/POSITION 排最后 → 注意力稀释实测无效(镜4 横移 0.00px、镜7 center→x0.32)。
2. **pin 链式续写杀运动镜**:每镜 MotionContextLoadLatent pin 上一镜尾帧;与 pinned 帧矛盾的运镜按官方"矛盾=并集"被丢弃。首镜(无 pin)推近 +6.8% 生效,唯一横移镜(pin)完全没摇。
3. **脚本侧**:EP01 固定镜 20/25(80%)=成片站桩;镜4(横移)漏气闸句(其余镜都有)。

## 修复

### ① 纪律瘦身重排(渲染端,全库自动生效)
- `manjuFinalizePromptPure` 重写:**先删后插自愈**(manjuGuardStrip 剥除存量 9 条旧墙+合并版,存量 plan 下次 finalize 自动迁移)+ 队尾 ≤4 条紧凑版:
  - `AUDIO & LIP DISCIPLINE`(AUDIO+LIP 合并)
  - `IDENTITY CONSISTENCY`(有 <Picture> 才注入)
  - `FRAME DISCIPLINE` 紧凑版(-40% 字数)
  - `MOTION & SEAM DISCIPLINE`(MOTION+EXECUTION+CHAIN 三合一)
- `injectCameraDiscipline`/`injectPositionDiscipline`:队尾 → **detailed_description 段标题前**(高服从位,与 CROWD/SCREEN/OFF-SCREEN TASK 同槽);先删后插幂等;POSITION 仅有人物镜注入。
- 纪律文本变更 → 条件指纹全变 → 存量镜头自动 stale 重渲。

### ② 运动镜断链(渲染端)
- `renderShotTo`:`chained && !manjuCameraIsStatic(s.Camera)` → 断链直出(不加载 MotionContext),日志"运动镜断链直出"。非固定镜(推/拉/摇/移/跟/环绕)保证运镜执行,衔接处允许硬切。

### ③ 技能侧同步(NiliX-Novel)
- `分镜派发模板.md` 第 9 条:运镜分布硬约束(固定 ≤50% / 连续固定 ≤3 / 静态镜 ≤6s)+ 运动镜双铁律(气闸句必写+运镜句置 dd 首句)。
- `H3分镜脚本文档模板.md` 第 3 条:运镜句置首+运动镜气闸句(从承接构图起幅)+静态镜时长上限。
- 教训台账追加三条(纪律墙稀释/pin 杀运镜/固定占比)。

### UI 四项
1. **资产管理弹窗加高**:`.clib-grid` max-height 480px→64vh(min 320px)。
2. **资产详情弹窗去双窗+图片预览**:openCharLibDetail 先 closeModal 再开(不再叠两窗),加"← 返回资产库"按钮;资产视图图片点击 previewImage 大图预览。
3. **产物角色/场景自动换行**:`.manju-thumbs` flex 单行横滚 → flex-wrap: wrap 多行。
4. **产物视频 ⋮ 菜单新增「🎛 单镜画布调整」**:镜头文件(^\d+\.mp4)直达 openShotDebug(节点链可视化+按镜参数覆盖+保存重渲此镜)。

## 验证

- 全量 `go test ./internal/api/` PASS(4 个旧断言测试更新到新纪律语义:合并名/union 短语/紧凑 FRAME)。
- 冒烟测试:新结构 CAMERA/POSITION 在 dd 段前、队尾 4 条、幂等、旧墙剥净。
- `node --check` JS 语法 PASS;cache-buster 202609021130→202609022030(12 处)。
- enc_guard(internal/api + 技能 references)、deliver_check 全 PASS。
- NiliX.exe 重编译(20:58)。重启应用重跑即生效(纪律变更指纹失效自动全量重渲)。
