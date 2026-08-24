# NiliX 全面审计报告与升级修复方案

> 审计日期：2026-08-22 · 审计对象：`C:\Mi\Ai\WorkBench\NiliX`（Go 单服务 8787，小说→漫剧 H3 视频流水线）
> 方法：4 路并行深度审计（渲染管线核心 / AI 审片智能体 / Novel 创作管线 / 服务运维安全）+ MiniMax-H3 官方文档对照 + 关键项代码级抽查复核（S 级发现全部逐行验证属实）。
> 状态：本报告为审计结论快照；修复进展见文末「修复日志」。

---

## 一、总体评价

工程化水平**明显高于同类本地工具**：原子写、panic 兜底、断点续跑、渲染检查点（prompt_id 落盘 + history 收回）、manifest 指纹 stale 自愈、八维度 Go 侧加权（不采信模型自报）、H3 提示词纪律体系（六段式/官方标签/CFG-distilled 无负面词）、云端 2K 预校验等都很扎实，主路径稳定。

**核心短板集中在三处**：
1. **失败路径静默吞错**——QC 当通过、错误信息裸拼 JSON 损坏、err 丢失；
2. **多入口全局一致性**——config 无锁、删除无任务闸门、后台 goroutine 防护不统一；
3. **信任边界没收住**——GET 全开放 + 密钥明文落盘 + config 参数无白名单 → 本机任意网页/进程可窃取密钥。

---

## 二、【严重】缺陷（10 条）

| # | 缺陷 | 证据（文件:行） | 影响 |
|---|---|---|---|
| S1 | **未授权密钥/文件读取链闭合**：GET 不鉴权 + 密钥明文落盘 + config 参数无白名单。子链：①`/api/settings` 浅拷贝致 Agent 视觉 Key 明文返回；②`/api/manju/project?config=` 任意路径读 JSON 掏 `llm.api_key`；③`/api/fs/read` 白名单内直读项目 config.json（明文 key）；④`/api/manju/notify` 明文返回通知 Token | api.go:200-208 / manju.go:539-562 / fs.go:596-617 / manju.go:2019 | 本机任意进程/网页窃取全部密钥 |
| S2 | **后台 goroutine 无 panic 兜底 → 整机崩溃**：rework/预编码/判分/ASR goroutine 均无 recover；`vision_model=","` 即触发索引越界 panic 崩掉 8787 | manju_agent.go:1172,1218-1226,1232 / vision.go:100 | 一次误配置/意外 panic 即整机宕机，渲染中任务丢失 |
| S3 | **返工删稿丢章**：auto 审稿 <70 分先删后写，重写失败静默吞掉；Score 为 LLM 自报，缺字段=0 → 恒 <70 → 误删好章；全本.md 判重缺陷致旧坏稿保留或同章重复 | novel_create.go:690-697 / 682 / 501-519 | 章节永久丢失；全本（渲染正文源）与正文失同步 |
| S4 | **QC 静默通过**：`stageQC` 的 `err` 只在失败镜头>0 分支消费，脚本崩溃/python 缺失/报告未落盘 → 失败集空 → 返回 nil → 坏片直进成片 | manju_pipeline.go:2109-2141 | 质量防线整体失效，无日志无告警 |
| S5 | **判分失败被当"不合格"→ 空转烧钱**：视觉 API 故障与判分未过同入失败集 → 修复师零反馈瞎改 → 删产物重渲 → 再判分，烧满 MaxRetries 轮 GPU+LLM+VLM | manju_agent.go:1247,1293-1309 | 高峰期视觉过载时三重费用浪费 |
| S6 | **h3_context 接缝 latent 跨项目/方案污染**：文件名只含镜头序号、无项目/集命名空间且从不清理 → 项目 B 接续项目 A 画面；草稿/定稿分辨率混用 | manju_comfy.go:465-482 / manju_pipeline.go:1997 | 画面串戏，潜伏性正确性 BUG |
| S7 | **运行中可删除项目/清理产物**：delete/cleanup 不检查 running → 与渲染并发 `os.RemoveAll` 撕扯 | manju_delete.go:14-45 / manju_cleanup.go:14-53 | 产物半残/状态失联/僵尸项目 |
| S8 | **全局状态无锁**：config.json 读-改-写无互斥；`manjuGlobalAgent` 无锁；`novel_state.json` 4 goroutine 并行 RMW | manju.go:391-418 / manju_agent.go:45-81 / novel_create.go:855-870 | 设置丢失/判分中途切模型/创作档案覆盖 |
| S9 | **"停止"链路失效**：kill 硬编码 8190；comfy.wait 无停止钩子（空转 3600s）；runMedia 子进程无超时无停止感知 | manju.go:1337-1343 / manju_comfy.go:108-127 / manju_pipeline.go:2055-2075 | 自定义端口停止失效；子进程挂死永久 running |
| S10 | **手动宽高不对齐 32**：`manjuAlign32` 只用于档位/草稿路径，custom 手动值直进 H3 VAE 网格 | manju_pipeline.go:209-218 / manju_comfy.go:352-371 | 非法尺寸 400 或破损产物 |

## 三、【高】缺陷（17 条）

| # | 缺陷 | 来源 |
|---|---|---|
| H1 | 无 Host/Origin 校验 → DNS rebinding 全量接管写 API（token 由服务端渲染进 HTML） | 安全 |
| H2 | ComfyUI 一键安装无 SHA256 校验（投毒即任意代码执行）+ pip 无版本锁定 + 安装"停止"无效 | 安全 |
| H3 | 路径穿越写入：`/api/render` shotID 未消毒；gacha adopt 角色名未 sanitize → 任意路径覆盖写 | 安全+管线 |
| H4 | stopComfy 按端口 taskkill 误杀无关进程；自启进程 PID 不记录 | 安全 |
| H5 | base_url/comfy_url 零校验 → Key 外带通道（改 base_url → test 接口发 Bearer 给攻击者） | 安全 |
| H6 | 前端存储型 XSS：KB tooltip 未转义 + 全站无 CSP + token 存 localStorage | 安全 |
| H7 | 指纹漏渲染参数（steps/turbo_lora/sampler）→ 改参数被"跳过（已存在）"静默忽略 | 管线 |
| H8 | 断点续跑重复判分（无幂等）→ 每次续跑整集重烧 VLM 费 | 智能体 |
| H9 | 换集清空被 plan 提前写 Episode 破坏 → EP01 旧判分/升级残留混入 EP02 | 智能体 |
| H10 | 智能模式升级微信通知从不触发（唯一推送在死代码中） | 智能体 |
| H11 | 弱项阈值 60 与及格线 75 失配 → 74 分"全面平庸"镜无修复指引空转 | 智能体 |
| H12 | LLM 调用零重试/零退避：plan 一次 429 即中断整条 AI 一条龙 | 智能体 |
| H13 | 硬编码路径群：`novelRootDir`（大写 Novel 与可配置双轨）、manjuNovelSave、KB 注入路径 | Novel+安全 |
| H14 | auto 无 plan 标记默认 total=600 狂写 600 章 | Novel |
| H15 | 错误响应裸拼 JSON（含引号/换行即非法 JSON，6+ 处） | 管线 |
| H16 | 剧本生成无长度截断（整本 120 万字灌单条 prompt） | Novel |
| H17 | 双链解析不跳过代码块 → 图谱幽灵边；同名页 ~N 去重致入链错指 | Novel |

## 四、【中】精选（19 条）

- **健壮性**：`retryable` 大小写缺陷（超时错误匹配不到）· runMedia/qa_check 无超时挂死 · 检查点 90s"任务丢失"误判重复烧 GPU · 同条件镜头预编码重复提交 · 渲染任务无上限无清理 · 参考图生成无限轮询 · 成片 faststart 整文件读内存（GB 级 OOM）· HTTP 无超时/请求体上限 · 看门狗残留标记竞态（崩溃不重启）· `recoverKey` 破坏性自愈（瞬时读错误即配置清零）
- **正确性**：`netRate` 恒 0（diskRate/netRate 共用 prevTime）· sysmon 硬编码 8190 · status `stage` 字段语义不一致 · 学习记忆非幂等累计 · 全局单任务锁（无法多项目并行）· vision base_url 缺省回退 LLM 地址（常见配置坑）· Prompt 注入面（小说原文直入判分/修复上下文）· 卷名未消毒（Windows 保留字符整卷瘫痪）
- **其他**：fs 白名单无 symlink 解析 + config 动态注入根目录 · GET /api/fs/select 弹系统对话框 · token 静默降级（rand.Read 失败裸奔）· token 永不过期 · auto 每章审稿翻倍成本 · 全本追加 O(n²) · 多 GPU 只取第一块 · LHM 传感器无自愈

## 五、渲染管线 H3 升级优化

- **已验证合规**：六段式/三段式、`<Picture N>` 纪律、retention 四值、scenetrans/cutoff/[unclear]、运镜词表、CFG 无负面词转正面排除句、17k+5 帧网格、MotionContext 接缝（22+24）、Turbo LoRA 参数表、SageAttention 双名探测、云端 2K 预校验、fhd 1088（H3 原生 2K 合规，仅文档"短边≤768"表述需修正）
- **P0 门禁**：plan 产物运行时硬校验（角色卡完整性/时长-台词量/说话人纪律）+ 逐镜 h3_prompt 结构校验（六段齐全/`<d>`/`<Picture N>` 比对 ref 名单）+ qc 音轨规格校验（32kHz 立体声）
- **P1 质量**：空镜/转场升级真 FL2VA 双帧模式（模板已具备，工作流只喂单图）、多角色镜参考图扩容（当前 ≤3）、社区 kernel 提速跟进
- **P2 云端**：ICR（In-Context Regeneration）评估、h3.c/vLLM 调研
- **P3 工程**：接缝 latent 清理（与 S6 合并）、fhd 云端 2K UI 提示、模板语义注释

## 六、修复路线图

- **P0 安全止血（1–2 天）**：S1 密钥读取链闭合 → H1 Host/Origin 校验 + CSP → S2 safeGo panic 兜底 → S4 QC 一行修复 → S5 判分 error/failed 分流
- **P1 正确性（3–5 天）**：S3 返工链路重构 → S6 latent 命名空间+清理 → S7 任务闸门统一 → S8 全局锁 → S9 停止链路 → S10 32 对齐 → H13 硬编码路径清零
- **P2 健壮性（1 周+）**：LLM 重试退避 + 指纹补参数 → 判分幂等 + 换集状态机 → 安装 SHA256 + 可取消 → 输入 sanitize 收口 → writeErr 统一 → 中项逐条
- **P3 体验与工程（持续）**：前端 XSS + CSP → auto 上限收敛 → 剧本分段 → 升级通知 → H3 门禁 → 测试补齐

---

## 七、分报告（4 路原始审计）

### 7.1 渲染管线核心（manju_pipeline / manju.go / render_ck / cleanup / delete / stats）

**严重**：S1 QC 静默通过（2109-2141）；S2 rework goroutine 无 recover（1907-1981）；S3 h3_context latent 跨项目污染（comfy 465-482 / pipeline 1997）；S4 运行中可删项目（delete 14-45 / cleanup 14-53）。

**高**：H1 kill 硬编码 8190（manju.go:1337-1343）；H2 手动宽高不对齐 32（209-218）；H3 comfy.wait 无停止钩子（comfy 108-127）；H4 错误 JSON 裸拼（manju.go:547,592 等 6+ 处）；H5 config 无锁（391-418）；H6 指纹漏渲染参数（1519-1545）；H7 gacha adopt 路径穿越（2732-2748）。

**中**：M1 检查点 90s 误判窗口（render_ck 71-111）；M2 同条件预编码重复提交（1914-1928）；M3 runMedia 无超时（2055-2075）；M4 fhd 1088 超自述合规线（comfy 350，实际模型原生 2K 支持）；M5 `<Picture N>` 无运行时校验；M6 `%!w(<nil>)` 包装；M7 status stage 语义不一致（manju.go:1420/1469）；M8 updateShotPrompt 非原子写；M9 崩溃恢复 elapsed 丢失；M10 outputs 全量重算指纹。

**低**：L1 fixed/increment seed 策略行为相同；L2 范围不交换；L3 copyFile 整文件读内存；L4 mtime 秒粒度；L5 llm_stats 全量读写；L6 通知新建 client；L7 atomicWrite 残留 .tmp；L8 novel-save 硬编码；L9 集数 0 回退命名；L10 崩溃恢复丢 only；L11 encode 阶段无流水线重叠；L13 runManjuSync 无界 Buffer；L15 磁盘态 done 默认。

### 7.2 AI 审片智能体（agent / arbiter / fixer / judge / vision + manju_agent）

**严重**：S1 子 goroutine panic 无兜底（1172/1218-1226/1232，vision.go:100 越界）；S2 判分失败与不合格未分流 → 空转返工烧预算（1247/1293-1309）。

**高**：H1 续跑重复判分（1190-1192）；H2 manjuGlobalAgent 无锁（45/73/81）；H3 LLM 零重试（llm 88-142）；H4 换集清空被 plan 破坏（979-984/408/1477-1480）；H5 升级通知在死代码（1317-1327/1579-1690）；H6 弱项阈值 60 与及格线失配（agent.go:167）。

**中**：M1 retryable 大小写（vision 265-276）；M2 runMediaOut 无超时（700-715）；M3 学习记忆非幂等（1524-1574）；M4 全局单任务锁（manju.go:1135/manju_agent.go:211）；M5 Prompt 注入面（judge 64-76/fixer 37-47）；M6 vision base_url 回退陷阱（118-122）。

**低**：L1 指纹冗余+注释矛盾；L2 状态写盘错误静默；L4 明文 key 注释误导；L5 maskKeys 只匹配 api_key；L6 chat 日志混入运行池；L8 GET /api/manju/agent 免鉴权；L9 剧本复核只前 24 镜；L10 通知阈值 60 无联动。

### 7.3 Novel 创作管线（novel_create / novel_skill / script / storyboard / kb_work）

**严重**：S1 auto 返工先删后写（690-697）；S2 score 零值误删（682+384-393）；S3 全本判重缺陷（501-519）；S4 novel_state 无锁并发（855-870）。

**高**：H1 novelRootDir 硬编码（24）；H2 auto 默认 600 章（637-644）；H3 qa_check 无超时（novel_skill 91-101）；H4 卷名未消毒（1004-1015）；H5 剧本无长度截断（generate 72-75）；H6 双链解析不跳代码块（parser 229-231）；H7 create 不校验章节数连续性；H8 LLM 截断未检测（backend/llm.go 100-110）；H9 stop 后 Err 残留。

**中**：无 ctx 取消/无重试（92,304,377,963）· 全本追加 O(n²)（506）· auto 每章审稿翻倍（678-702）· progress 假估算（252-254）· statusAll 计数虚高（794-799）· 卷首章 prevTail 占位（929）· KB 无文件监听（parser 116-181）· 同名页去重错指（151-164）· 字节截断乱码（generate 115）· 镜头参数无夹逼（generate 75-88）· KB 路径硬编码（manju_llm.go:189）· 解码错误静默（46,200,289,326）· GET 免 token（api 146-156）· touchNovelState 非原子（872-882）。

**低**：保留设备名未处理（27,35）· 卷名 Sscanf 截断（1008）· 双层标题冗余（1018+518）· 字符串排序（529-535）· 误判已写（626-636）· git pull 无超时（109-117）· 子串误报（42-44）· 字数口径不一致（1025）· Page id 大小写敏感（kb 42-49）· map 只增不删（562-565）· extractJSON 误剥（93-101）。

### 7.4 服务运维安全（api / fs / comfy / config / watchdog / sysmon / island / main）

**严重**：S1 GET /api/settings 明文视觉 Key（api.go:200-208）；S2 /api/manju/* config 无白名单任意 JSON 读（manju.go:539-562,1573-1640,1727-1815,937-968）；S3 /api/fs/read 直读明文 config.json（fs.go:596-617）；S4 /api/manju/notify 明文凭据（manju.go:2019-2021）。

**高**：H1 无 Host 校验 DNS rebinding（main 427-437）；H2 安装无校验和（comfy_install 53-74,98-162）；H3 shotID 穿越（render.go:104/manager.go:96）；H4 stopComfy 按端口误杀（comfy 77-93,193-202）；H5 base_url 外带（api 210-238）；H6 前端 XSS（app.js 374-407）。

**中**：M1 netRate 恒 0（sysmon 404-469）；M2 sysmon 硬编码 8190（240,262-281）；M3 密钥明文落盘多处（manju.go:41,471-473）；M4 recoverKey 破坏性自愈（config 106,232-245）；M5 加密被 HTTP 明文回流；M6 安装停止无效（comfy_install 361-369）；M7 任务无上限（manager 56-70）；M8 参考图无限轮询（asset 39-70）；M9 faststart 整读（verify 94-102）；M10 硬编码目录（novel_create 24/manju.go:986）；M11 fs 白名单无 symlink + 动态膨胀（fs 456-512）；M12 GET /fs/select 弹框（fs 586-594）；M13 看门狗标记竞态（main 558-610）；M14 HTTP 无超时（main 433）。

**低**：token 静默降级（main 421-424）· token 永不过期 · autostart 清理缺失 · 多 GPU 只取第一块 · island 回调 panic 隔离不完整 · LHM 无自愈 · startComfy TOCTOU · 标题未限长 · 日志明文 0644 · kb 图无 Cache-Control。

---

## 八、待验证项（16 条）

管线：ComfyUI interrupt 后 history 行为（error 条目 vs 消失）· 1088P 实测 · 非 32 倍数提交行为（400 vs 自动对齐）· SageAttention 节点名版本 · 保存 width 后旧缓存兼容。
安全：`/clips/{file}` %2e%2e 解码 fuzz · markdown 双解码 XSS 实测 · 8.3 短名/UNC 前缀白名单可达性 · cleanup/upscale 路径参数白名单核验 · 安装目录前缀误放行。
智能体：token 记账口径（finish_reason=length 漏计）· 兜底分 70 对总分分布影响 · arbiter regenerate 双渲成本确认 · 修复师截断重试缺失 · 维度分与 issues 一致性。

---

## 修复日志

> 本表记录实际代码修复进度（每批修复后 `go build` 验证 + 全量 `go test` 通过）。

| 日期 | 批次 | 修复项 | 状态 |
|---|---|---|---|
| 2026-08-22 | P0-1 | **S1 密钥读取链**：settings Agent 视觉 Key 掩码（深拷贝防写回）；manjuProject llm key 掩码；新增 `manju_guard.go`（manjuGuardConfig/manjuGuardNovel/config 白名单）应用于 20+ 端点（project/outputs/plan/novelInfo/env/status/run/saveRender/settingsPost/gacha×4/health/upscale×2/jianying/cleanup/GET agent）；novel 归属校验 + 64MB 大文件护栏 | ✅ |
| 2026-08-22 | P0-1 | **H1 Host 校验**：localHostOnly + validLocalHost（Routes 外层，仅 127.0.0.1/localhost/::1），DNS rebinding 防线 | ✅ |
| 2026-08-22 | P0-1 | **S2 panic 兜底**：safeGo 封装应用于 9 处 goroutine（preencode/judge/asr/rework/rework-preencode/genprompt/upscale2k/novel-auto/novel-vol）；NewVisionClient 空模型守卫（返回 nil 降级）+ VisionReady 解析校验 | ✅ |
| 2026-08-22 | P0-1 | **S4 QC 静默通过**：`err!=nil && 失败集空 → 显式报错`；M6 `%!w(<nil>)` 包装修复；**S5 判分失败/不合格分流**（服务不可用直接升级不空转返工） | ✅ |
| 2026-08-22 | P0-1 | **S10 手动宽高 32 对齐**（newManjuCtx 收口）；**S7 任务闸门**（delete/cleanup 运行中 409）；**S9 kill 用任务项目 comfy_url**（不再硬编码 8190） | ✅ |
| 2026-08-22 | P0-2 | **S3 返工链路**：先写后删（rename 备份 + 失败恢复 + 显式报错）；score 0-100 校验（缺字段不再触发误删）；appendToFullBook 按章号替换（全本与正文一致） | ✅ |
| 2026-08-22 | P0-2 | **S6 latent 命名空间**：h3_context/`<项目>_<集>`/clip_NNNNN + 随集清理（防跨项目/方案/分辨率串接） | ✅ |
| 2026-08-22 | P0-2 | **S8 全局锁**：manjuGlobalAgent RWMutex；novel_state 并入按书锁 + 原子写；config 事务锁（qcAcceptClear 逃生门） | ✅ |
| 2026-08-22 | P0-3 | **H13 硬编码路径**：novelRoot() 读可配置 NovelRootDir；manjuNovelSave 去硬编码；**H12 LLM 退避重试**（429/5xx/网络 3 次，2s/4s） | ✅ |
| 2026-08-22 | P0-3 | **M1 netRate 恒 0**：prevDiskTime/prevNetTime 拆分；**H3 shotID 穿越**：safeName 白名单化（render.RenderShot） | ✅ |
| 2026-08-22 | P1-1 | **H15 writeErr 统一**：14 处裸拼 JSON 错误全部改 JSON 安全序列化；**H8 判分幂等**（已 pass 且产物未变跳过重判）；**H9 换集翻页统一到 plan 阶段**；**H10 升级通知补齐**（智能模式坏镜升级推送）；**H4 stopComfy 按 PID**（防误杀同端口进程） | ✅ |
| 2026-08-22 | P1-1 | **H14 auto 上限收敛**（无 plan 标注不再默认 600 章，按已写+7 且 ≤200）；**H7 指纹补渲染参数**（steps/turbo_lora/sampler/sage/policy 纳入 manifest 指纹）；**H16 剧本长度收口**（12000 字截断 + rune 截断修乱码）；**H17 双链解析跳代码块** | ✅ |
| 2026-08-22 | P1-2 | **retryable 大小写**（超时错误小写化匹配 + DeadlineExceeded）；**runMedia/runMediaOut 超时+停止感知**（25 分钟上限，whisper/ffmpeg 挂死不再永久阻塞）；**HTTP 服务超时配置**（slowloris 防线）；**stage 语义统一**（磁盘态也返回阶段名+episode 字段） | ✅ |
| 2026-08-22 | P1-2 | **卷名消毒**（H4 Novel）；**检查点 90s 误判窗口**（inQueue 队列感知 + 延长观察窗，防双任务烧两遍 GPU）；**faststart 分块扫描**（GB 级成片不再整读内存）；**asset 生成超时**（15 分钟上限）；**渲染任务并发上限 + TTL 清理** | ✅ |
| 2026-08-22 | P1-3 | **记忆幂等**（同集只汇总一次，返工计数/问题统计/趋势不再虚高）；**看门狗标记竞态**（主进程启动清旧标记，崩溃必重启）；**fs symlink 解析**（根+路径双 EvalSymlinks 规范化，junction 不再越界）；**GET fs/select 改 POST**（+前端同步，防恶意弹窗）；**recoverKey 瞬时重试 + 非破坏**（坏文件保留 .corrupt） | ✅ |
| 2026-08-22 | P1-4 | **预编码 singleflight**（同条件缓存并发只提交一次，省 Qwen3-VL 重复编码）；**vision base_url 回退降级**（模型名与端点不匹配不再静默用错端点空转）；**prompt 注入隔离**（judge/fixer 数据边界声明，小说原文/旧提示词不再能注入指令）；**前端 XSS 转义 + CSP**（tooltip title/category 转义 + frame-ancestors/X-Frame-Options） | ✅ |
| 2026-08-22 | P1-4 | **H2 安装完整性**（7z t 压缩包校验 + 解压后 main.py 检查，半下载损坏不再静默产出残缺程序）；**M6 安装可取消**（cancelled 标志贯穿下载循环，点停止真正中断、不再"显示已停实际已装"） | ✅ |
| 2026-08-20 | 验证 | `go build ./...` + `go vet ./...` + 全量 `go test ./...` 全部通过 | ✅ |
| — | 设计取舍 | M4（智能体）per-project 锁：全局状态锁为短持有（loadAgentState+save 毫秒级），单 GPU 本就单任务运行，跨项目串行影响可忽略，保留现状 | 评估 |
| 2026-08-20 | H3升级 | **P0 门禁①plan 硬校验**：`validatePlan`（角色卡完整性/时长 4-15/台词-时长量/说话人纪律）不达标自动带意见 LLM 修复重试；**②逐镜 h3_prompt 结构校验**：`validateShotPrompt`（六段/三段字段齐全、`<d>` 台词、`<Picture N>` 参考、台词首句入 <d>）不达标带意见修复重试；**③qc 音轨规格**：manju_media.py 检出 32kHz/立体声，采样率或声道异常在合成前拦截 | ✅ |
| 2026-08-20 | H3升级 | **P1 FL2VA 双帧**：空镜镜头可选首尾双图插值——工作流层 `h3EncWorkflow` 支持 `MiniMaxH3Fl2VA`（`R["_scene_end"]` 尾帧 + 节点探测 `fl2vaNodeAvailable` 缺失自动回退单图）；资产层 `fl2va_end_frame` 开关生成场景尾帧 `_end.png`（同 prompt 不同 seed + 轻微运动提示）；渲染输入副本防并发竞态；配置字段已注册可前端设置 | ✅ |
| 2026-08-20 | 打包 | `go build -ldflags "-H windowsgui -s -w" -o NiliX.exe` 完成，前端 `?v=20260820a2` 已 embed | ✅ |
| 2026-08-20 | 前端补齐 | **差集审计**（后端路由 vs 前端调用）：发现并补齐 3 项后端有前端无的功能——①**知识图谱页恢复**（后端 /api/graph+/api/meta+/api/page 全在，前端"关系图谱页已移除"）：新增 `graph.js`（力导向 SVG 布局/分类着色/枢纽节点/点击详情 Markdown 渲染/双链跳转/⟳重扫 POST /api/reload），nav 第 4 页「知识图谱」；②**设置·测试连接**（/api/settings/test 此前前端 0 引用）：漫剧设置弹窗智能体调度区加「🔌 测试连接」按钮（测全局 LLM + ComfyUI 连通）；③**FL2VA 双帧勾选**（后端字段已注册前端缺失，补 8 处表单接入） | ✅ |
| 2026-08-20 | 差集结论 | 其余后端独有 API 均属设计定位：/api/render*+/api/outputs（旧渲染任务，主管线为 manju，只读诊断保留）、/api/script/*（剧本预览/诊断，已被 manju 管线覆盖）、/api/stats（前端经 /api/meta 聚合）、/api/fs/analyze 等诊断端点——不作前端 UI，报告备案 | 说明 |
| 2026-08-24 | 知识库升级 | **表演层纪律（5 层结构方法论 + 微表情指南整合）**：情绪三层拆解（外部动作/生理反应/量化指标）+ 微表情五维（眉眼/嘴角/肌肉/呼吸/光影）+ 哭戏四梯度（强忍→无声→抽泣→崩溃）+ 非对称克制中断 + 原子需求台账（必须出现/保持/允许/禁止）——注入 manjuDirectSystem/manjuScriptSystem/manjuShotWritingRules（规则 29-33），专治蜡像脸 | ✅ |
| 2026-08-24 | 知识库升级 | **官方风格签名**：manjuStyles 8 预设升级为官方风格签名段（Pixar 3D/纸拼贴/纸艺定格/手绘实拍/极简产品签名词+负向词转正面），新增 minimal 预设；组合风格仍按核心措辞拼接 | ✅ |
| 2026-08-24 | 知识库升级 | **节奏模型 + 近景补偿 + BGM 定向**：每镜 beat 数（5s=3-4/10s=5-7 含峰值刹车/15s=6-9）+ 节奏意图词（setup/impact/brake/settle）进方案/脚本系统；人脸 token 数学 → 情感戏/对话强制近景特写；non_diegetic_music 按题材文化贴合选乐器（古筝/竹笛/鼓组/钢琴/钟琴/木琴）+ ducking | ✅ |
| 2026-08-24 | 知识库升级 | **QC 冻结检测**：manju_media.py check_video/inspect 增加 freeze_ratio（末尾 25% 采样窗口相邻灰度均值差 <0.8 占比），>0.6 判段尾冻结告警（H3 段尾提前到达 Last Frame 静止的典型病）——黑屏/静音/冻结三道防线齐备 | ✅ |
| 2026-08-24 | 验证 | `go build ./...` + 全量 `go test ./...` 全绿（新增 manju_knowledge_upgrade_test.go 6 项：表演层/节奏/近景/BGM/风格签名/规则连续性）；NiliX.exe 重编（GUI 版）+ 重启验证 8787 正常 | ✅ |
