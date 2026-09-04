# 画质升级:对标官方最佳实践根治「成片拉跨」(2026-09-04)

用户主诉:渲染视频效果拉跨,不如 LibTV / Seedance 2.5 等。全网资料搜集(官方指南全文
+ ComfyUI 官方文档 + 社区 benchmark)+ 本地全链路排查后,定位五个根因并全部修复。

## 一、调研结论(差距来源)

与 Seedance 2.5(字节闭源 API,1080P/30 秒多镜头叙事)的差距里,**模型代差与分辨率
天花板是硬边界**(本地 H3 权重原生 768 短边、面积上限 768×1344;1088P 档超上限约 2 倍
属实验位),但本地链路存在 **5 个可根治的画质折损点**——全部是我们自己的配置与措辞
问题,不是模型上限:

| # | 折损点 | 实锤依据 |
|---|--------|----------|
| 1 | **全部镜头跑 4 步蒸馏 LoRA**(lightx2v 4step ×2) | ComfyUI 官方文档明示 Turbo LoRA "音频和运动质量略降";社区 benchmark 实测 14 步内质量随步数单调上升,4 步远低于甜点;本地已装阿里 PDD Acc 8step 官方蒸馏却未启用 |
| 2 | **视频 VAE 用 int8 量化**(convrot) | 官方模型清单/官方模板均为 fp16;int8 解码有损 |
| 3 | **ref_image_size=match 写死** | 官方文档:`max`(保留最高 2048 短边编码)身份保真更强;match 把 1024+ 定妆照缩到 768 再编码,人脸细节折损 |
| 4 | **运镜黑话负优化** | 2026-09-03「电影化返工」把官方句式 `the camera pushes in with small amplitude at slow speed` 反向改成 `performs a slow cinematic dolly push-in...`——官方指南词表只有 15 个 motion type(Push In/Pull Out/Pan/Truck/Pedestal/Arc/Tracking…),H3 训练对齐的是官方动词句式,dolly/orbital/crane 黑话服从性打折。全库 2761 处存量中招 |
| 5 | **提示词正文被压到官方建议的一半**(150-220 词) | 官方 ref 指令原文:"Make detailed_description as detailed and explicit as possible"、正文 normally 350-500 词;旧系统约束压到 150-220 词,画面细节(构图/材质/光影)不足→空洞感 |

另有一个**指纹缺口**:镜头指纹(shotCondFingerprintAt)不含 VAE/LoRA/步数/ref_image_size
——换质量配置后旧缓存与旧成片继续命中,升级静默失效。本轮已补齐。

## 二、改动清单

| 文件 | 改动 |
|------|------|
| internal/manju/manju_comfy.go | `ref_image_size` 从写死 match 改可配置(默认 **max**,新增 `h3RefImageSize`) |
| internal/manju/manju_pipeline.go | ① 指纹补采样参数维度(vae_video/turbo_lora×2/steps/ref_image_size),配置变化即全量 stale 重编重渲;② `manjuCameraPhrase` 43 条映射全面对齐官方 15 词表(动词句式:`pushes in with small amplitude at slow speed`/`trucks left`/`arcs around the subject`/`pedestals up at slow speed`…;角度类→`frames the subject from a low angle`);③ 新增 `manjuOfficializeCameraVerbs` 存量黑话反向归一(精确反解 rework_cinematic_phrases.py 的 6 模式,幂等,挂渲染/指纹共用汇点,存量 plan 自愈);④ CAMERA DISCIPLINE 注入句式改官方自然英语句(直取名词短语仍走 performs 包装);⑤ `normalizeQualityUpgrade` 存量项目一次性迁移(quality_gen=2 版本键:4step→PDD 8step、int8 VAE→fp16、match→max;PDD 蒸馏步数归 8;迁移后不再重复触发,手动改回不被覆盖);⑥ 默认配置切质量档;⑦ CINEMATOGRAPHY 纪律压缩 ~30% |
| internal/manju/manju_llm.go | 提示词体积约束扩容:detailed_description **150-220→250-350 英文词**,h3_prompt 上限 1500→2200 tokens(官方对齐,正文信息密度直接决定画面细节) |
| internal/config/config.go | 全局默认同步:fp16 VAE / PDD 8step ×2 / turbo_steps 8 / 新增 `RefImageSize` 强类型字段(防 Load→Save 抹掉) |
| internal/render/r2v.go | 轻量渲染链 ref_image_size 同步 max 可配置 |
| internal/manju/manju_quality_upgrade_test.go | 新增 4 组回归:指纹采样参数维度 / refImageSize 默认 / 黑话归一 6 模式+幂等 / 存量迁移+版本键幂等 |
| web/kb/index.html + js/manju.js | 1088P 档标注「超模型上限·实验」+ tooltip 警示;使用说明补画质档条目;版本号 bump |
| tools/research_h3/ | 官方两份提示词指南原文存档(guide_base.md / guide_ref.md,调研依据) |

## 三、使用说明

- **存量项目**:下次运行自动迁移到质量档(渲染日志提示「🎨 画质档升级」),采样参数
  进指纹后全部镜头自动 stale 重编重渲——升级即刻生效,无需手动清理。
- **速度回退**:需要快出片时,高级配置改回 `turbo_lora`/`turbo_lora_r2v` =
  4step 文件 + `vae_video` = int8 + `ref_image_size` = match 即可(指纹随之变化再触发
  重渲)。PDD 模式依赖 ComfyUI-MiniMax-H3-PDD-Acc 节点(已部署;未装时自动回退
  全步数 20 步并告警)。
- **渲染时长预期**:768P @ 24GB 卡,PDD 8 步约为 4 步的 ~2 倍时长;预编码 ref_image_size
  =max 略慢(参考图原分辨率编码)。质量优先是本轮主诉导向的默认。
- **更高清晰度**:本地天花板=768P(模型原生面积上限 768×1344);1088P 档已标注实验位。
  对标 Seedance/LibTV 的 1080P+ 观感走「云端 2K 精修」(MiniMax API 重生成,已有通路)。

## 四、验证

- `go build ./...` / `go vet ./...` 全绿
- `go test ./...` 全量单测通过(含 4 组新回归;3 处旧运镜短语断言随官方词表更新)
- 改动文件全部 UTF-8 合法、非 0 字节(enc_guard/deliver_check 抽验;全库扫描剩余
  异常均为 comfyui 第三方包与项目运行日志 run.log,非交付代码)
- NiliX.exe 已重编译(10:02),按惯例用户自重启生效

## 五、后续可选方向(未做,供决策)

1. **风格 embedding**:Comfy-Org/MiniMax-H3 官方仓 10 个效果 embedding(bullet_time/
   storm_magic 等,PR #50)——特效镜可用;无 photoreal 专用 embedding,对写实主链
   收益有限。注意必须放句子中部才生效。
2. **多镜合渲叙事强化**:官方 [Shot N] At MM:SS.mmm 单视频多镜头切换语法(现有
   shots_per_take=2 已部分利用),可扩展到 2-3 镜一组提升叙事连贯(对标 Seedance 2.5
   的 30 秒多镜头叙事卖点)。
3. **云端 2K 精修批量化**:成片后自动 2K 重生成(0.80 元/秒,一集约数百元),做成
   可选开关。
