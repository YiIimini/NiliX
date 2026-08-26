/* 二级页:视频管理(原生工作台) —— 项目/小说/渲染配置/阶段执行/状态/产物 + 右侧成品列表收缩栏 */
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);

  /* 视觉模型预设:用户只选模型 + 填 Key,API 地址自动带出(自定义兜底) */
  const VISION_PRESETS = [
    { id: "glm-4.6v-flash", url: "https://open.bigmodel.cn/api/paas/v4", label: "智谱 glm-4.6v-flash（免费·推荐）", hint: "智谱 Key：open.bigmodel.cn 控制台 → API 密钥" },
    { id: "glm-4v-flash", url: "https://open.bigmodel.cn/api/paas/v4", label: "智谱 glm-4v-flash（免费·备选）", hint: "智谱 Key：open.bigmodel.cn 控制台 → API 密钥" },
    { id: "glm-4.6v", url: "https://open.bigmodel.cn/api/paas/v4", label: "智谱 glm-4.6v（付费旗舰）", hint: "智谱 Key：open.bigmodel.cn 控制台 → API 密钥" },
    { id: "qwen-vl-max", url: "https://dashscope.aliyuncs.com/compatible-mode/v1", label: "通义 qwen-vl-max", hint: "阿里云百炼 Key：bailian.console.aliyun.com" },
    { id: "qwen-vl-plus", url: "https://dashscope.aliyuncs.com/compatible-mode/v1", label: "通义 qwen-vl-plus", hint: "阿里云百炼 Key：bailian.console.aliyun.com" },
  ];

  /* 文本模型服务预设(方案生成/剧本师/修复师):选服务自动带出接口地址+模型 ID,自定义兜底(OpenAI 兼容) */
  const LLM_PRESETS = [
    { id: "deepseek", url: "https://api.deepseek.com", model: "deepseek-chat", label: "DeepSeek（默认·推荐）", hint: "Key：platform.deepseek.com → API Keys" },
    { id: "glm", url: "https://open.bigmodel.cn/api/paas/v4", model: "glm-4-flash", label: "智谱 GLM-4-Flash（免费）", hint: "Key：open.bigmodel.cn 控制台 → API 密钥" },
    { id: "qwen", url: "https://dashscope.aliyuncs.com/compatible-mode/v1", model: "qwen-plus", label: "通义千问 qwen-plus", hint: "Key：bailian.console.aliyun.com" },
    { id: "kimi", url: "https://api.moonshot.cn/v1", model: "moonshot-v1-8k", label: "Kimi moonshot-v1", hint: "Key：platform.moonshot.cn" },
  ];

  /* 画幅预设(官方 6 档) → 宽×高 */
  const RATIOS = {
    "21:9": [1344, 576],
    "16:9": [1344, 768],
    "4:3": [1024, 768],
    "1:1": [1024, 1024],
    "3:4": [768, 1024],
    "9:16": [768, 1344],
  };

  /* 预设风格(单一数据源:按钮渲染/多选组合/卡片展示共用;key 须与后端 manjuStyles 一致,加预设只改这里) */
  const STYLE_PRESETS = [
    ["2.5d", "2.5D 动漫"], ["real", "写实"], ["3d", "3D CG"], ["anime", "二次元"],
    ["handdrawn", "手绘"], ["papercraft", "纸艺"], ["clay", "粘土"], ["ink", "水墨"],
  ];
  const STYLE_CN = Object.fromEntries(STYLE_PRESETS);

  /* 分辨率档位中文名(chips 展示;key 与后端 manjuResTiers 一致) */
  const RES_TIER_CN = { draft: "416P 草稿", standard: "768P 标准", fhd: "1088P 高清" };

  const INT_KEYS = ["width", "height", "fps", "steps", "turbo_steps", "seed", "min_shot_seconds", "max_shot_seconds", "shots_per_take"];
  const STR_KEYS = ["comfy_url", "unet_fl2va", "unet_ref2va", "clip", "vae_video", "vae_audio",
    "z_image_unet", "z_image_clip", "z_image_vae", "turbo_lora", "turbo_lora_r2v", "animagine_ckpt",
    "chapters", "episode", "shots"];

  /* 数值字段默认值(与 direct_pipeline/new_project.py 保持一致,配置缺省/为空时回填,避免输入框空白) */
  const NUM_DEFAULTS = {
    "manju-width": 768, "manju-height": 1344, "manju-fps": 24,
    "manju-steps": 20, "manju-turbo": 8, "manju-seed": 1688,
    "manju-minsec": 4, "manju-maxsec": 12, "manju-mosaic-level": 16,
    "manju-draft-scale": 0.5, "manju-bgm-gain": 0.28, "manju-take": 1,
  };

  /* 负面提示词默认值(与后端 manjuNegPrompt 一致;配置缺省/为空时回填展示) */
  const NEG_PROMPT_DEFAULT = "lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting";

  /* 管线阶段(流程图顺序) */
  const FLOW = [
    { key: "env", name: "环境" },
    { key: "plan", name: "方案" },
    { key: "assets", name: "资产" },
    { key: "encode", name: "编码" },
    { key: "render", name: "渲染" },
    { key: "qc", name: "质检" },
    { key: "assemble", name: "合成" },
  ];

  /* 审片八维度(与后端 internal/agent Dims 一致,对齐 H3 官方能力边界):key/中文名/权重% */
  const AGENT_DIMS = [
    ["identity", "主体一致性", 20], ["scene", "场景还原", 12], ["action", "动作符合", 15],
    ["camera", "运镜符合", 10], ["visibility", "主体可见性", 15], ["tech", "技术质量", 15],
    ["style", "风格统一", 8], ["lips", "口型对白", 5],
  ];

  /* 使用说明(完整 16 节,长文案 JS 常量) */
  const GUIDE = [
    { ic: "🚀", t: "快速开始", ps: [
      "1. <b>新建项目</b>：填剧名、选小说、可填 DeepSeek API Key，一键生成完整配置",
      "2. 顶部选择<b>项目</b>（自动扫描 manju 目录）",
      "3. <b>内容来源</b>（小说解析 / 视频脚本直出 二选一）：按解析结果自动展示——默认小说解析带出项目配置的小说（点「选择文件」可换，运行时覆盖不改配置）；启用脚本直出后自动切到脚本区块",
      "4. <b>角色管理</b>：在「渲染配置」里点「角色管理」按钮，抽卡/采纳生成定妆照",
      "5. <b>章节</b>默认 0=解析小说总章数(可改具体范围)；<b>集数</b>默认 0=按总章数每章一集全渲染；集数 N>0 只渲染第 N 集(第 N 章)",
      "6. 一条龙 = 方案→资产→编码→渲染→质检→合成；中断/失败后,右侧栏状态区出现<b>黄色提示条</b>(上次中断于 X 阶段),点<b>▶ 续跑</b>一键恢复(幂等跳过已完成)",
    ] },
    { ic: "🎬", t: "视频脚本直出", ps: [
      "输入方式二选一（内容来源卡片<b>自动按解析结果展示</b>）：📖 小说解析 或 🎬 视频脚本直出",
      "新建项目弹窗选「🎬 视频脚本直出」：可直接粘贴 H3 官方格式分镜脚本 / 插入官方模板 / <b>📁 从目录检测分镜脚本</b>（手动选任意目录 → 自动识别 → 创建后自动导入）",
      "<b>选择目录检测脚本</b>：目录里有分镜脚本（文件名含「分镜脚本/分镜」，或内容含 <code>[Shot N]</code>/分镜表）→ 列表点击<b>直接读取</b>并启用脚本直出；目录无脚本 → 引导把该目录当小说走 <b>LLM 直出</b>（小说解析）",
      "<b>剧名自动填充</b>：选小说目录或脚本目录都会自动填项目名（手动输入后不再覆盖；创建时仍有兜底）",
      "主页「内容来源」卡片按解析结果自动展示当前模式；「清除脚本(回小说)」一键切回小说解析",
      "脚本为镜头级分镜（<code>[Shot N]</code> 时间码/画面/台词/音效/环境声/配乐），LLM 一步直出完整渲染方案；脚本预览超长自动滚动",
    ] },
    { ic: "🎭", t: "角色管理", ps: [
      "入口在「渲染配置」卡右上角：「角色管理」按钮打开抽卡弹窗",
      "先「生成方案」产出角色列表，再逐角色抽卡",
      "抽卡 = 按角色 image_prompt + 随机 seed 连抽多张候选定妆照，候选累积保留可对比点选，采纳最佳（支持全员抽卡批量出卡）",
      "「采纳」把候选设为正式定妆照（覆盖旧图 → 缓存指纹失效 → 自动重新预编码/渲染）",
      "采纳后右侧栏「产物」的人物缩略图自动刷新",
      "正脸参考 <code>_face.png</code> 从定妆照切「完整头部+肩部」用于 R2V 锁脸",
      "<b>多视图</b>：每个角色支持 正面/全身/侧面/细节 四个视图 tab，各视图独立抽卡/采纳；渲染时同一角色多视图全部作为 H3 参考图传入，人物更统一",
      "定妆照固定 <b>1024×1024</b> 标准尺寸（与项目画幅/分辨率档位无关）：同一角色在横屏/竖屏/不同档位项目里形象一致；场景图仍按项目画幅生成",
    ] },
    { ic: "🎬", t: "执行管线", ps: [
      "阶段按钮带序号：⓪项目体检 ①方案 ②资产 ③编码 ④渲染 ⑤质检 ⑥合成（顺序执行）",
      "第一排单跑某阶段；「快速执行」下：<b>一条龙</b> = 全流程自动化",
      "中断后点<b>▶ 续跑</b>：从上次断点继续，已完成阶段幂等跳过；<b>崩溃恢复</b>：渲染中提交即落盘 prompt_id，续跑先查 ComfyUI history 收回已完成任务，绝不重复烧 GPU",
      "点「项目体检」：环境自检（ComfyUI / 模型 / 依赖就绪性）+ 智能诊断（配置/参数/审片官），可修复项一键写回 config",
      "产物区每集可：<b>☁️ 2K</b>（本地定稿镜提交 MiniMax 云端升 2K，设置里填 Key）/ <b>📦 剪映</b>（导出视频+字幕轨草稿，可继续编辑；需 venv 装 pyJianYingDraft）",
      "镜头 ⋮ 菜单支持<b>单镜云端 2K</b>；<b>⚠️ 已过期</b>徽标 = 提示词/定妆照已变，下次渲染自动删旧重渲",
      "产物区<b>🧹 清理</b>：清理抽卡候选/审片抽帧/云端 2K(均可重新生成,定妆照/定稿/成片不动)",
      "每次任务结束自动写<b>诊断快照</b>到 <code>manju/logs/diagnose/&lt;项目&gt;_diagnose.json</code>(配置 Key 打码+状态+项目体检),反馈问题时直接提供该文件即可定位",
    ] },
    { ic: "🤖", t: "智能体调度", ps: [
      "点<b>🤖 AI 一条龙</b>先弹窗询问：<b>「是」</b>= Agent 深度分析小说内容，自动推荐并更新渲染风格（可组合叠加，如 2.5D+水墨）后走全流程；<b>「否」</b>= 按当前渲染配置直接走AI 一条龙",
      "AI 一条龙 = 一条龙 + 智能体：渲染完成后<b>审片官逐镜判分</b>（八维度，对齐 H3 官方能力）",
      "<b>🔍 项目体检</b>（执行管线 ⓪）：环境自检（ComfyUI/模型/依赖就绪性）+ 智能诊断（配置/小说/LLM/渲染参数/审片官），可修复项一键写回 config",
      "<b>💬 右栏可对智能体说话</b>：体检 / 推荐风格 / 审片报告 / 总结 / 修复，支持快捷指令按钮",
      "<b>🧠 学习档案</b>：跨次运行记忆——运行次数、审片均分趋势、高频问题、最近风格选择；阶段失败自动<b>智能诊断</b>给出原因与修复建议",
      "未达标镜头<b>自动返工</b>：修复师按审片意见改写 H3 提示词 → 删缓存定点重渲染（预算默认 2 轮，防无限重试）",
      "预算耗尽仍不达标 → <b>🤖 AI 终审自动拍板</b>(默认开):接受该镜最佳结果或按原文从零重写提示词再试一轮,无需人工介入;终审决策记录在审片报告(悬停镜头编号可看),事后可点「重试此镜」覆盖;设置里关闭「自动拍板」则恢复升级卡人工拍板",
      "视觉模型在<b>设置 → 智能体调度</b> 配置（OpenAI 兼容；推荐智谱 <b>glm-4.6v-flash</b> 免费，<b>429 高峰自动退避重试并降级 glm-4v-flash</b>，自定义可填逗号链）；Key 顺序:项目配置 → 项目 DeepSeek → 环境变量 GLM_VISION_API_KEY",
      "「测试视觉模型」<b>随时可点</b>：没有定妆照时自动用合成测试图验证连通（约 5-10s）",
      "未配置视觉模型时自动降级：仅机械质检（黑屏/无声）+ 升级，不判分不返工",
      "<b>剧本师复核</b>在方案阶段给出节奏/台词/爽点评议（低于 60 分推送提醒），只报告不改动方案",
    ] },
    { ic: "⚙️", t: "设置与通知", ps: [
      "「设置」弹窗聚合三块：<b>智能体调度</b>（文本模型 DeepSeek Key + 视觉模型 + 云端 2K Key）/ <b>微信通知</b> / <b>配置管理</b>",
      "「判分并发」= 视觉判分 API 并发(1-4)：免费档保持 2(调高易 429,会自动退避+粘性降级+整链熔断),付费 Key 可 3-4 提速审片",
      "「🤖 自动拍板」= 预算耗尽 AI 终审自动决策(接受该镜最佳 / 从零重写再试一轮),无需人工介入;关闭则恢复升级卡人工拍板",
      "<b>智能模式开关切换即时保存</b>，勾选后刷新不回落；其余字段改动后点「保存智能体配置」",
      "微信通知选渠道后出现<b>对应官网按钮</b>（新标签打开去拿 SendKey/Token）；保存后重开设置会正确回填",
      "配置管理：导出 JSON 跨项目复用 / 导入回填表单 / 恢复默认（768×1344 / 20 步 / 2.5D）",
      "通知在<b>阶段切换</b>与<b>审片升级</b>时推送，测试按钮先落盘再实发一条",
    ] },
    { ic: "🎨", t: "渲染风格", ps: [
      "8 个预设：<b>2.5D 动漫 / 写实 / 3D CG / 二次元 / 手绘 / 纸艺 / 粘土 / 水墨</b>",
      "<b>预设可多选叠加</b>：点击即选中/取消，可同时组合多个，如 2.5D+水墨",
      "<b>自定义风格</b>：输入框可一次输入<b>多个元素</b>——用 + 或逗号分隔(如「real+cyberpunk」「水墨+古风」),<b>回车或点「应用」自动解析</b>逐词叠加,无需逐个输入;英文风格词原样使用,中文风格词自动翻译成英文(白名单:写实/动漫/水墨/赛博朋克/古风/国潮/科幻/Q版等)",
      "<b>风格词自动净化</b>(2026-08-25)：<b>非美术风格词会被自动剔除</b>并日志提示——角色塑造/内容价值观/括号指令(如「反派磕碜」「Q版呆萌可爱小角色(用于对应角色的动态内心独白)」「真情实意」「玄幻修仙」等跨书残留)混进英文提示词会把画面拉偏(实测赛博朋克剧被拉向修仙、角色被画成 Q 版),一律不再注入 image_prompt/H3 提示词",
      "风格为<b>提示词级注入</b>：拼成一句英文分别写入定妆照/场景图、每镜 Ref2VA 开头与空镜 [Shot 1],全片画风统一",
      "点风格区右上角 <b>「?」</b>可查看 8 个官方示例动图,并<b>详细展示当前风格解析后的英文措辞</b>(三个注入位置各一段)",
      "定妆照<b>统一 Z-Image / Krea-2 出图</b>(2026-08-24 用户规则:<b>SDXL 已禁用</b>)：Z-Image 写实拟动漫(默认)或 Krea-2 强指令;禁日漫(animagine 等一律拒绝)、禁真人照片(防侵权);改后下次运行生效(已渲染镜头不受影响)",
    ] },
    { ic: "📐", t: "渲染参数", ps: [
      "<b>画幅</b>官方 6 档：21:9 / 16:9 / 4:3 / 1:1 / 3:4 / 9:16，竖屏短剧推荐 <b>9:16（768×1344）</b>",
      "<b>档位</b>快捷切换分辨率：416P 草稿（快速试片）→ 768P 标准（默认）→ 1088P 高清，按画幅等比换算并对齐 32；选「手动宽高」则直接用上面的宽高值",
      "<b>步数</b>默认 20；<b>Turbo步</b>默认 8（8 步 ≈ 20 步画质、约 2.9 倍提速）",
      "<b>seed</b> 全剧固定保证跨镜头一致；<b>seed策略</b>控制返工：固定（默认）/ 重试递增（第 N 次返工 seed+N）/ 重试随机（返工换新随机）——返工仍抽同一 seed 等于重抽同一命运的卡",
      "<b>SageAttn</b>：SageAttention 注意力加速补丁（需 ComfyUI-KJNodes），RTX 50 系白捡提速；开启后「项目体检」会校验节点是否可用",
      "<b>草稿预审</b>（AI 一条龙）：审片返工轮用缩放分辨率草稿（默认 0.5 ≈ 1/4 像素量，可调 0.2-0.95），全部落定后自动<b>全分辨率定稿重渲</b>——审片轮 GPU 时间约降 3/4，定稿零返工",
      "<b>时长</b> min/max 4–15s，大模型逐镜时长在此区间自动 clamp",
    ] },
    { ic: "🔗", t: "模型与一致性", ps: [
      "高级配置为<b>下拉选择</b>（从 ComfyUI 模型目录读取），避免手输误操作",
      "人物一致性 = H3 <b>R2V 参考链</b>：定妆照作为 <code>&lt;Picture 1&gt;</code> 锁脸/服装",
      "<b>定妆照统一 Z-Image / Krea-2</b>(2026-08-24 用户规则:SDXL 已禁用):Z-Image 写实拟动漫(默认)或 Krea-2 强指令;禁日漫模型、禁真人照片(防侵权),定妆照与视频画风一致",
      "场景图始终用 Z-Image；负面提示词作用于定妆照与场景图生成",
      "<b>模型名须与磁盘完全一致</b>（含大小写）；配置里的模型已缺失会标注「已缺失」",
    ] },
    { ic: "✍️", t: "H3 提示词规范", ps: [
      "有角色 = <b>六段式 Ref2VA</b>；空镜 = <b>三段式 FL2VA</b>（由引擎自动选择）",
      "对白 <code>&lt;d&gt;[中文]&lt;/d&gt;</code> 原词保留；说话者 <code>(S1)(S2)</code> 跨镜一致",
      "H3 为 <b>CFG-distilled 无负面词</b>：排除项（无水印/字幕）写进正文散文",
      "引擎已内置亮度护栏与运镜规范，无需手写",
    ] },
    { ic: "🗂️", t: "换电脑迁移 / 自包含部署", ps: [
      "整个 NiliX 目录自包含:ComfyUI 放 <code>NiliX/comfyui/ComfyUI</code> + 模型放 <code>NiliX/comfyui/shared/</code>、小说放 <code>NiliX/novel/</code>、项目放 <code>NiliX/manju/</code>、技能放 <code>NiliX/skills/shuangwen-novel/</code>,拷走即用",
      "设置弹窗「目录与部署」:5 个路径可显式指定(留空=自动:优先 exe 目录子目录,其次旧位置);保存后即时生效(ComfyUI 需重启)",
      "旧安装(Comfy-Desktop 版 ComfyUI / C:\Mi\Ai\WorkBench 数据)留空即自动沿用,无需改配置",
      "小说续作技能是 git 仓库时,可在「目录与部署」点「🔄 更新技能」git pull 同步",
    ] },
    { ic: "📦", t: "小说素材自动利用", ps: [
      "小说项目目录按约定组织(全本/正文/素材/设定集/封面),引擎<b>自动发现并全部利用</b>,素材白准备不浪费:",
      "<b>素材/人物生成提示词.md</b>(含 人物/角色 的 md) → 角色定妆照 image_prompt 必须贴合其外观/服装/气质/记忆点",
      "<b>素材/场景*.md</b> → 场景图 image_prompt 贴合其场景描述;<b>素材/其它 md</b> → 道具/氛围参考",
      "<b>设定集/*.md</b>(设定集与大纲/创作指令卡/写作规范等) → 世界观/角色/剧情线/文风必须贴合,禁止与设定冲突",
      "<b>封面/封面提示词.md</b> → 全剧美术基调与封面一致;封面图自动用作项目海报",
      "运行日志会打印「📎 已利用小说素材: xxx、yyy…」——没看到说明素材目录命名不在约定内(检查文件名含 人物/角色/场景/设定)",
    ] },
    { ic: "📚", t: "整本小说 → 多集", ps: [
      "章节与集数<b>默认都是 0</b>：0=解析小说总章数作为实际值——章节 0 → 全书范围，集数 0 → 每章一集全渲染（第 N 章 = 第 N 集）；也可章节填具体范围(如 1-10)限定，集数填 N 只渲染第 N 集；「全本」仍可用（1-999）",
      "<b>按卷分集</b>：小说目录为「正文/卷一_标题/…」卷结构时（如吞天废子），每卷自动一集（EP01=卷一、EP02=卷二…）",
      "无卷结构时按内容量分段（每集约 1.8 万字，约 2-3 章）",
      "分段为确定性规则：同一本小说每次全本运行的分集完全一致，可安全续跑；已完成的集自动跳过",
      "定妆照/场景图跨集自动复用，全季人物形象一致",
    ] },
    { ic: "🎯", t: "镜头(可选)", ps: [
      "只影响<b>编码/渲染/质检</b>：留空=全集所有镜头",
      "填 <b>1,2</b> / <b>1-3</b> / <b>1,3-5</b> 自由组合",
      "典型用途：试拍先跑 1,2 看效果 / 局部重做（<b>覆盖</b> NN.mp4）/ 重渲质检不过的镜头",
      "<b>填了镜头号 = 定点强制重渲</b>：已有产物也会覆盖重渲（只作用于<b>当前集</b>，集数 0 时为首集，不再逐集跑）",
    ] },
    { ic: "⏯", t: "断点续跑", ps: [
      "任何阶段中断/失败,右侧栏状态区<b>自动出现黄色提示条</b>(上次中断于 X 阶段),点「▶ 续跑」一键恢复;也可手动点「▶ 续跑」",
      "渲染中崩溃/被杀/重启:<b>渲染检查点</b>自动收回上次已提交未收的产物(查 ComfyUI history 免重渲,绝不重复烧 GPU)",
      "<b>质检不过不卡死</b>：再点「一条龙/续跑」自动删旧重渲质检未过的镜头；也可在中断横幅直接选<b>「⏭ 跳过失败镜并续跑」</b>(该镜不计质检、不进成片)或<b>「✅ 接受并合成」</b>(未过镜头进成片,由你决策)",
      "缓存名带「项目_集号_镜头」前缀，多项目互不串用（集号由集数自动转 EPxx）",
    ] },
    { ic: "📺", t: "运行与质检", ps: [
      "任务后台静默运行，<b>不会弹终端窗口</b>；进度看「运行状态」卡日志实时滚动",
      "运行日志为<b>竖向时间轴</b>：阶段名在左(发光大节点)、时间在右,按类型着色(错误红/升级红晕/警告琥珀/审片主色/镜头进度蓝),超 300 行自动折叠(完整日志在项目 run.log)",
      "运行状态卡：横向时间轴展示 7 阶段进度 + 产物缩略图",
      "左侧「成品列表」标题右侧可<b>搜索过滤</b>项目；小说管理页也支持书架搜索与阅读器全书搜索",
      "质检标准：时长达标 / 音轨 ≥1 / 近黑帧 ≤50% / 解码正常",
      "质检结果落盘为报告：未过镜头下次渲染自动重渲；质检日志会直接列出失败镜头号；中断横幅可一键「跳过失败镜/接受结果」逃生",
      "想中途停：点红色「停止」；刷新页面不丢任务",
    ] },
    { ic: "⚠️", t: "常见坑", ps: [
      "API Key 可留空：自动用已保存的默认 Key",
      "模型名必须与磁盘完全一致（MiniMax_H3_* 首字母大写）",
      "custom_nodes 升级后需重启 ComfyUI",
      "H3 输出自带对白/旁白/环境音/配乐——<b>不要再 TTS、烧字幕、混音</b>",
    ] },
    { ic: "📁", t: "产物位置", ps: [
      "方案: <code>analysis/&lt;集&gt;_direct_plan.json</code>",
      "镜头: <code>clips/&lt;集&gt;/NN.mp4</code>",
      "成片: <code>&lt;剧名&gt;/&lt;集&gt;_成片.mp4</code>",
      "诊断快照: <code>manju/logs/diagnose/&lt;项目&gt;_diagnose.json</code>(每次任务结束自动生成,覆盖保留最近一次;含配置 Key 打码/状态/项目体检)",
      "云端 2K: <code>clips/&lt;集&gt;/2k/NN.mp4</code>;剪映草稿: <code>&lt;剧名&gt;/剪映草稿/</code>",
    ] },
  ];

  function dirOf(p) {
    const i = Math.max(String(p || "").lastIndexOf("/"), String(p || "").lastIndexOf("\\"));
    return i > 0 ? String(p).slice(0, i) : String(p || "");
  }
  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
  }
  function fmtTime(s) {
    s = Math.max(0, Math.floor(s || 0));
    if (s < 60) return s + "s";
    const m = Math.floor(s / 60), r = s % 60;
    return m + "分" + (r ? r + "秒" : "");
  }
  function fmtSize(b) {
    if (!b) return "0 B";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0, v = b;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(i ? 2 : 0) + " " + u[i];
  }

  function nilixTok() {
    const t = window.NILIX_TOKEN;
    if (t && t.length > 8) return t;
    try { return localStorage.getItem("nilix_token") || ""; } catch (e) { return ""; }
  }
  async function api(path, opts) {
    const o = Object.assign({ cache: "no-store" }, opts || {});
    const tok = nilixTok();
    if (tok) { o.headers = Object.assign({}, o.headers || {}); o.headers["X-NiliX-Token"] = tok; }
    const r = await fetch(path, o);
    if (!r.ok) {
      let msg = "HTTP " + r.status;
      try { const b = await r.json(); if (b && b.error) msg = b.error; } catch (e) {}
      throw new Error(msg);
    }
    return r.json();
  }
  const get = (p) => api(p);
  const post = (p, body) => api(p, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });

  const ls = (k, v) => {
    try {
      if (v === undefined) return localStorage.getItem("manju-" + k) || "";
      localStorage.setItem("manju-" + k, v);
    } catch (e) {}
  };

  const ManjuWorkbench = {
    projects: [],
    project: "",
    info: null,
    style: "real",
    chapters: "0", episode: "0", only: "", novel: "",
    _novelProject: "", // 当前小说来源所属的项目,切项目时据此回填该项目的默认小说
    novelInfo: null,
    status: { running: false, stage: "", rc: null, elapsedSec: 0 },
    agent: null,
    outputs: { clips: [], artifacts: {}, final: null },
    plan: null,
    gacha: {},   // charId -> {image, seed, adopted}
    timer: null,
    stopping: false,
    createName: "", createNovel: "", createKey: "", createErr: "", creating: false, savedKey: "",
    picker: null,
    _bound: false,

    enter() {
      this.bind();
      this.chapters = ls("chapters") || "0"; // 章节 0=按小说总章数
      this.episode = ls("episode") || "0"; // 集数:0=按章节数自动,1=第1集...
      this.only = ls("only");
      this.novel = ls("novel");
      $("manju-chapters").value = this.chapters;
      $("manju-episode").value = this.episode;
      $("manju-only").value = this.only;
      $("manju-novel").value = this.novel;
      this.loadProjects(ls("project"));
      this.poll();
      if (this.timer) clearInterval(this.timer);
      this.timer = setInterval(() => this.poll(), 2000);
    },

    leave() {
      if (this.timer) clearInterval(this.timer);
      this.timer = null;
    },

    /* tip 气泡(2026-08-24 用户要求:所有提示都是 tip 气泡)——右上角悬浮,自动消失;
       kind: success 成功 / error 失败 / info 信息 */
    showTip(msg, kind) {
      if (!msg) return;
      let box = $("manju-tips");
      if (!box) {
        box = document.createElement("div");
        box.id = "manju-tips";
        box.className = "manju-tips";
        document.body.appendChild(box);
      }
      const t = document.createElement("div");
      t.className = "manju-tip " + (kind || "info");
      t.textContent = msg;
      box.appendChild(t);
      setTimeout(() => { t.classList.add("out"); setTimeout(() => t.remove(), 350); }, 3800);
    },

    setErr(msg) {
      // 2026-08-24 用户要求:提示统一 tip 气泡(不再用页面顶部常驻红字)
      this.showTip(msg, "error");
    },

    /* 绿色成功提示(复用人性化前缀,3.8 秒后自动消失) */
    setNote(msg) {
      this.showTip(msg, "success");
    },

    /* 中断续跑提示:上次任务被中断/失败且日志有失败痕迹 → 动态创建「一键续跑」横幅;
       无内容/无中断/无痕迹 → 彻底移除 DOM(静态 HTML 中不存在该元素,杜绝空提示条)
       质检失败时额外给逃生门:跳过失败镜并续跑 / 接受质检结果并合成(坏镜进成片由用户决策) */
    interruptTipHTML(s) {
      const m = (s.logTail || "").match(/质检未过镜头\s*([\d,]+)/);
      let qcBtns = "";
      if (s.currentStage === "qc" && m) {
        qcBtns = `<button id="mi-tip-skip" class="hrs-btn" title="这些镜头不再质检、也不进成片,继续跑完合成">⏭ 跳过失败镜</button>` +
          `<button id="mi-tip-accept" class="hrs-btn" title="接受当前结果,未过镜头将进成片(风险由你决定)">✅ 接受并合成</button>`;
      }
      // 布局:文字一行在上,按钮一行在下居中(用户明确要求)
      return `<span class="mi-tip-t">${this.interruptTipText(s)}</span>` +
        `<span class="mi-tip-actions"><button id="mi-tip-resume" class="hrs-btn hrs-btn-primary">▶ 续跑</button>${qcBtns}</span>`;
    },
    bindInterruptTip(div) {
      // 局部查询(div 内),避免全局 $() 命中被轮询重建的旧按钮
      const btn = div.querySelector("#mi-tip-resume");
      if (btn) btn.addEventListener("click", () => this.resumeFromTip(div));
      const skipBtn = div.querySelector("#mi-tip-skip");
      if (skipBtn) skipBtn.addEventListener("click", () => {
        const m = ((this.status && this.status.logTail) || "").match(/质检未过镜头\s*([\d,]+)/);
        if (!m || !this.project) { div.remove(); this.runResume(); return; }
        skipBtn.disabled = true; skipBtn.textContent = "处理中…";
        post("/api/manju/qc/decision", { config: this.project, action: "skip", shots: m[1] })
          .then(() => { div.remove(); this.runResume(); })
          .catch((e) => { skipBtn.disabled = false; skipBtn.textContent = "⏭ 跳过失败镜"; this.setErr("跳过失败镜失败: " + e.message); });
      });
      const accBtn = div.querySelector("#mi-tip-accept");
      if (accBtn) accBtn.addEventListener("click", () => {
        if (!this.project) { div.remove(); this.runResume(); return; }
        accBtn.disabled = true; accBtn.textContent = "处理中…";
        post("/api/manju/qc/decision", { config: this.project, action: "accept" })
          .then(() => { div.remove(); this.runResume(); })
          .catch((e) => { accBtn.disabled = false; accBtn.textContent = "✅ 接受并合成"; this.setErr("接受质检结果失败: " + e.message); });
      });
    },

    /* 续跑统一入口:立即禁用按钮防连点 + 移除横幅,再启动(避免 poll 重建竞态导致点了没反应) */
    resumeFromTip(div) {
      const btn = div ? div.querySelector("#mi-tip-resume") : null;
      if (btn) { btn.disabled = true; btn.textContent = "启动中…"; }
      if (div) div.remove();
      this.runResume();
    },
    renderInterruptTip() {
      const s = this.status || {};
      const interrupted = !s.running && (s.stopped || (s.rc !== null && s.rc !== undefined && s.rc !== 0));
      // 失败痕迹:日志里真有 ❌/失败/手动停止/⏹ 才提示,防止 rc 残留造成「没内容也显示」
      const hasFail = s.logTail && (/❌|失败|已手动停止|⏹/).test(s.logTail);
      const show = interrupted && this.project && s.logTail && hasFail;
      let tip = $("manju-interrupt-tip");
      if (!show) {
        if (tip) tip.remove(); // 平时 DOM 彻底无此元素
        return;
      }
      if (tip) {
        // 已存在:仅更新文本区,不动按钮 DOM(保持已绑定的事件,避免轮询重建导致点击丢失)
        const t = tip.querySelector(".mi-tip-t");
        if (t) t.textContent = this.interruptTipText(s);
        const qcOn = s.currentStage === "qc" && (s.logTail || "").match(/质检未过镜头\s*([\d,]+)/);
        const skipBtn = tip.querySelector(".mi-tip-skip");
        const accBtn = tip.querySelector(".mi-tip-accept");
        if (skipBtn) skipBtn.style.display = qcOn ? "" : "none";
        if (accBtn) accBtn.style.display = qcOn ? "" : "none";
        return;
      }
      const html = this.interruptTipHTML(s);
      const wrap = $("manju-side-body");
      if (!wrap) return;
      const div = document.createElement("div");
      div.id = "manju-interrupt-tip";
      div.className = "manju-interrupt-tip";
      div.innerHTML = html;
      const firstRow = wrap.querySelector(".manju-row");
      wrap.insertBefore(div, firstRow ? firstRow.nextSibling : wrap.firstChild);
      this.bindInterruptTip(div);
    },

    /* 横幅文字(不含按钮),供 poll 更新文本时复用 */
    interruptTipText(s) {
      const st = s.currentStage ? ("上次中断于「" + s.currentStage + "」阶段") : "检测到上次运行中断";
      const m = (s.logTail || "").match(/质检未过镜头\s*([\d,]+)/);
      return `⚠️ ${st} — 可一键续跑(幂等跳过已完成)${m ? ` · 质检未过镜头 ${m[1]}` : ""}`;
    },


    /* ---- 运行日志:按阶段分组折叠展示,倒序显示(2026-08-25 用户要求:最新在最上) ----
       完整日志(不截断)按「━━━ 阶段 xxx ━━━」分段:
       阶段按时间倒序渲染——最新阶段在最上方且默认展开,历史阶段按时间倒序往下排;
       每段内部的行同样倒序(该段最新的行在最上面);产物收起状态下日志完整展开到底。 */
    renderLog(text) {
      const log = $("manju-log");
      if (!log) return;
      const src = String(text || "");
      this._logSrc = src;
      if (log.dataset.last === src) return;
      // 2026-08-24 用户反馈:运行中日志节点乱跳——先记录用户手动展开/收起的段,
      // 重渲染后恢复(否则每 2 秒 poll 重建 innerHTML 会把用户展开的历史阶段重置收起)
      const prevOpen = {};
      log.querySelectorAll("details.mj-tl2-sec").forEach((d) => {
        const b = d.querySelector("summary b");
        if (b) prevOpen[b.textContent] = d.open;
      });
      // 2026-08-25 倒序:阅读位置=顶部(最新),只有用户停在顶部时才跟随滚动到顶
      const nearTop = log.scrollTop < 80;
      const wasEmpty = !log.dataset.last;
      log.dataset.last = src;
      const stageCN = { env: "项目体检", plan: "方案", assets: "资产", encode: "编码", render: "渲染", qc: "质检", assemble: "合成", upscale: "云端 2K" };
      const lines = src.split("\n");
      // 按阶段分段(普通行归入当前阶段;前置无阶段行归入"运行")
      const sections = [];
      let cur = null;
      for (const raw of lines) {
        const st = raw.match(/━━━ 阶段 (\w+) ━━━/);
        if (st) {
          cur = { name: st[1], cn: stageCN[st[1]] || st[1], rows: [], err: false, ok: false };
          sections.push(cur);
          continue;
        }
        if (!raw.trim()) continue;
        if (!cur) { cur = { name: "", cn: "运行", rows: [], err: false, ok: false }; sections.push(cur); }
        cur.rows.push(raw);
        if (/❌/.test(raw)) cur.err = true;
        if (/✅|🎉/.test(raw)) cur.ok = true;
      }
      const last = sections.length ? sections[sections.length - 1] : null;
      // 渲染(倒序):最新阶段(=源日志最后一段)排最上并强制展开,历史阶段按时间倒序下排
      const secs = [...sections].reverse();
      let html = `<div class="mj-tl2">`;
      secs.forEach((sec) => {
        const isLast = sec === last && sec.rows.length > 0;
        const status = sec.err ? "❌" : (isLast ? "⏳" : (sec.ok ? "✅" : ""));
        // 2026-08-25 日志面板终版(用户截图证实:flex/grid 列布局在部分行内容列塌陷 2px):
        //  彻底放弃列布局,改纯文本流式行——时间戳+内容同一行 block,任何浏览器 100% 不塌陷。
        //  语义着色保留(✅绿/❌红/⚠️橙/[i/n]蓝/图标紫),阶段分组折叠保留。
        const rowHtml = (r) => {
          // 时间戳拆出
          let ts = "";
          let body = r;
          const tm = r.match(/^(\[\d{2}:\d{2}:\d{2}\])\s*/);
          if (tm) { ts = tm[1]; body = r.slice(tm[0].length); }
          // 阶段横幅(━━━ 阶段 xxx ━━━)
          if (/━━━\s*阶段/.test(body)) {
            const stm = body.match(/阶段\s*(\w+)\s*━━━/);
            const cn = stm ? (stageCN[stm[1]] || stm[1]) : "运行";
            return `<div class="mj-stage-banner">📌 ${esc(cn)}</div>`;
          }
          let cls = "";
          if (/❌|失败|错误|异常|已停止/.test(body)) cls = "err";
          else if (/✅|🎉|完成|成功/.test(body)) cls = "ok";
          else if (/⚠️|警告|未通过|超时|缺失/.test(body)) cls = "warn";
          else if (/^\[\d+\/\d+\]/.test(body)) cls = "prog";
          else if (/^(🎨|🎬|📖|🤖|🧬|📎|🔧|♻️|⏹|📝|✨|⏱|🧹|🎞)/.test(body)) cls = "mj-ic"; // 2026-08-25 修复:原 "ic" 命中全局 .ic{width:15px} 图标规则,日志行被压成 15x15
          return `<div class="mj-ln2 ${cls}">${ts ? `<span class="mj-ln2-ts">${esc(ts)}</span> ` : ""}<span class="mj-ln2-txt">${esc(body)}</span></div>`;
        };
        // 2026-08-25 倒序:段内行最新在上
        const rowsTxt = [...sec.rows].reverse().map(rowHtml).join("");
        // 用户手动展开过的保持;从未见过的新段默认收起(除当前段)
        const userOpen = prevOpen[sec.cn] !== undefined ? prevOpen[sec.cn] : false;
        const open = isLast ? true : userOpen;
        const stateDot = sec.err ? '<span class="mj-dot mj-dot-err" title="本阶段有错误"></span>'
          : (isLast && sec.rows.length > 0 ? '<span class="mj-dot mj-dot-run" title="进行中"></span>'
            : (sec.ok ? '<span class="mj-dot mj-dot-ok" title="已完成"></span>' : ''));
        html += `<details class="mj-tl2-sec ${sec.err ? "err" : ""}" ${open ? "open" : ""}>
          <summary>${stateDot}${status ? status + " " : ""}<b>${esc(sec.cn)}</b>${sec.name ? " <i>(" + esc(sec.name) + ")</i>" : ""} <span class="mj-tl2-cnt">${sec.rows.length} 行</span>${isLast ? " <em>当前</em>" : ""}</summary>
          <div class="mj-tl2-body">${rowsTxt}</div>
        </details>`;
      });
      html += `</div>`;
      log.innerHTML = html;
      // 倒序日志:默认停在顶部(最新);用户下滚看历史后不强制
      if (nearTop || wasEmpty) log.scrollTop = 0;
    },

    /* 一次性提示(启动/提交中等瞬时文案):下次 poll 会用时间轴接管 */
    logNote(msg) {
      const log = $("manju-log");
      if (!log) return;
      log.textContent = msg;
      delete log.dataset.last;
      this._logSrc = "";
    },

    /* ---- 初始化绑定 ---- */
    bind() {
      if (this._bound) return;
      this._bound = true;

      $("manju-refresh").addEventListener("click", () => this.loadProjects());
      // 回到前台时刷新项目列表(用户可能在外部删除项目)
      document.addEventListener("visibilitychange", () => {
        if (document.hidden) return;
        const view = document.getElementById("view-manju");
        if (view && view.classList.contains("is-active")) this.loadProjects();
      });
      $("manju-project").addEventListener("change", (e) => { this.project = e.target.value; ls("project", this.project); this.loadProject(); });
      $("manju-create").addEventListener("click", () => this.openCreate());
      $("manju-guide").addEventListener("click", () => this.openGuide());

      // 通知配置(设置弹窗)
      $("manju-settings").addEventListener("click", () => this.openSettings());
      // 角色抽卡卡片默认收起,点击标题展开/收起
      $("manju-gacha-head").addEventListener("click", () => this.toggleGacha());

      // 悬浮项目卡「参数」大类分项收起/展开(2026-08-25 用户要求)
      const chipsTitle = $("manju-chips-title");
      if (chipsTitle) chipsTitle.addEventListener("click", () => {
        const f = !chipsTitle.classList.contains("is-folded");
        chipsTitle.classList.toggle("is-folded", f);
        const cEl = $("manju-chips");
        if (cEl) cEl.classList.toggle("is-folded", f);
        const fb = chipsTitle.querySelector(".manju-sec-foldbtn");
        if (fb) fb.textContent = f ? "▸" : "▾";
        try { localStorage.setItem("manju-chips-fold", f ? "1" : "0"); } catch (e) {}
      });

      // 小说
      // 小说
      $("manju-novel").addEventListener("input", (e) => { this.novel = e.target.value; ls("novel", this.novel); });
      $("manju-novel-paste").addEventListener("click", () => this.openPaste());
      $("manju-novel-pick").addEventListener("click", () => this.openPicker("novel"));
      $("manju-novel-detect").addEventListener("click", () => this.detectNovel());

      // 视频脚本直出(H3 官方格式分镜脚本,与小说解析二选一)
      $("manju-script-paste").addEventListener("click", () => this.openScriptPaste());
      $("manju-script-clear").addEventListener("click", () => this.doScriptClear());
      // 2026-08-24 用户要求:「小说来源」+「视频脚本直出」合并为「内容来源」大卡片——
      // radio 切换显示对应区块(仅显示切换,不改配置)
      $("mc-content-novel").addEventListener("change", () => this.applyContentMode());
      $("mc-content-script").addEventListener("change", () => this.applyContentMode());

      // 视频管理卡片折叠(项目/渲染配置/小说来源/执行管线/快速执行):点标题收起/展开 + localStorage 记忆
      const foldLs = (k, v) => {
        try { return v === undefined ? localStorage.getItem("manju-fold-" + k) : localStorage.setItem("manju-fold-" + k, v); }
        catch (e) { return null; }
      };
      document.querySelectorAll(".manju-fold-head").forEach((h) => {
        const card = h.closest(".manju-card");
        const k = h.dataset.fold;
        if (!card || !k) return;
        if (foldLs(k) === "1") card.classList.add("folded"); // 恢复记忆(默认展开)
        h.addEventListener("click", (e) => {
          if (e.target.closest("button, a, input, select, label")) return; // 标题区按钮不触发折叠
          card.classList.toggle("folded");
          foldLs(k, card.classList.contains("folded") ? "1" : "0");
        });
      });
      document.querySelectorAll(".manju-fold-quick").forEach((l) => {
        const k = l.dataset.fold;
        if (!k) return;
        if (foldLs(k) === "1") l.classList.add("folded");
        l.addEventListener("click", () => {
          l.classList.toggle("folded");
          foldLs(k, l.classList.contains("folded") ? "1" : "0");
        });
      });

      // 风格:预设按钮按单一数据源渲染;点击即多选切换(点中=选中,再点=取消,组合以 + 连接)
      this.renderStylePresets();
      $("manju-style").addEventListener("click", (e) => {
        const b = e.target.closest("button[data-style]");
        if (!b) return;
        this.toggleStyle(b.dataset.style, true);
      });
      // 风格预览:官方示例 GIF(MiniMax H3 官方技能仓库素材,已本地化)
      $("manju-style-help").addEventListener("click", () => this.openStylePreview());
      // 自定义风格:输入英文风格描述点「应用」,以 TAG 标签叠加展示在输入框上方(重复词自动过滤)
      // 应用:点击按钮或回车(Enter)均可——支持一次输入多元素(+ 或逗号分隔,如
      // 「东方神话+东方修仙+东方玄幻+美女如云」),自动解析逐词叠加,无需逐个输入
      const applyCustom = () => {
        const v = $("manju-style-custom").value.trim();
        if (!v) { this.setErr("请先输入自定义风格描述"); return; }
        this.style = this.combineCustom(v);
        $("manju-style-custom").value = ""; // 已变成标签,输入框清空待下一次输入
        this.renderStyle();
        this.scheduleAutoSave();
      };
      $("manju-style-apply").addEventListener("click", applyCustom);
      $("manju-style-custom").addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); applyCustom(); }
      });
      // 画幅
      document.querySelectorAll("#manju-ratio button").forEach((b) =>
        b.addEventListener("click", () => {
          const wh = RATIOS[b.dataset.ratio];
          if (wh) { $("manju-width").value = wh[0]; $("manju-height").value = wh[1]; }
          this.renderRatio();
          this.scheduleAutoSave();
        })
      );
      $("manju-width").addEventListener("input", () => { this.renderRatio(); this.scheduleAutoSave(); });
      $("manju-height").addEventListener("input", () => { this.renderRatio(); this.scheduleAutoSave(); });
      // 渲染配置其余字段:变化即本地记忆(input 覆盖输入框,change 覆盖下拉/复选框)
      this.renderInputIds().forEach((id) => {
        $(id).addEventListener("input", () => this.scheduleAutoSave());
        $(id).addEventListener("change", () => this.scheduleAutoSave());
      });
      $("manju-mosaic-enabled").addEventListener("change", () => this.scheduleAutoSave());
      $("manju-sage").addEventListener("change", () => { this.syncBoostButtons(); this.scheduleAutoSave(); });
      $("manju-draft-judge").addEventListener("change", () => { this.syncBoostButtons(); this.scheduleAutoSave(); });
      $("manju-fl2va").addEventListener("change", () => { this.syncBoostButtons(); this.scheduleAutoSave(); });
      // 增强胶囊(SageAttn/草稿预审/FL2VA):点击切换隐藏 checkbox 数据源 + 胶囊 on 态
      const boostMap = { sage: "manju-sage", draft: "manju-draft-judge", fl2va: "manju-fl2va" };
      document.querySelectorAll("#manju-boost [data-boost]").forEach((b) =>
        b.addEventListener("click", () => {
          const cb = $(boostMap[b.dataset.boost]);
          if (!cb) return;
          cb.checked = !cb.checked;
          this.syncBoostButtons();
          this.scheduleAutoSave();
        })
      );

      // 高级配置折叠
      $("manju-adv-toggle").addEventListener("click", () => {
        const adv = $("manju-adv");
        const open = adv.classList.toggle("hidden") === false;
        $("manju-adv-toggle").textContent = (open ? "▾ " : "▸ ") + "高级配置";
      });
      // 加载模型下拉选项(首次绑定)
      this.loadModels();

      $("manju-save-render").addEventListener("click", () => this.saveRender());
      // 角色管理：打开抽卡/采纳弹窗（角色抽卡卡片已隐藏，入口移到渲染配置）
      $("manju-char-manage").addEventListener("click", () => this.openGachaModal());
      // 配置管理：导入/导出
      $("manju-config-manage").addEventListener("click", () => this.openSettings());
      // 恢复默认
      $("manju-config-reset").addEventListener("click", () => this.resetConfig());

      // 章节/集号/镜头
      $("manju-chapters").addEventListener("input", (e) => { this.chapters = e.target.value; ls("chapters", this.chapters); });
      // 集号防抖:打字 EP1→EP10 期间只发最后一次请求,避免旧集号响应后到覆盖新数据
      let epTimer = null;
      $("manju-episode").addEventListener("input", (e) => {
        this.episode = e.target.value; ls("episode", this.episode);
        clearTimeout(epTimer);
        epTimer = setTimeout(() => this.refreshOutputs(), 300);
      });
      $("manju-only").addEventListener("input", (e) => { this.only = e.target.value; ls("only", this.only); });
      $("manju-wholebook").addEventListener("click", () => { this.chapters = "1-999"; $("manju-chapters").value = this.chapters; ls("chapters", this.chapters); });

      // 阶段执行:一条龙(all)路由进 runStage → runAllCheck(已有流程检测:从头重渲 or 续跑)
      document.querySelectorAll("#view-manju [data-phase]").forEach((b) =>
        b.addEventListener("click", () => this.runStage(b.dataset.phase))
      );
      $("manju-resume").addEventListener("click", () => this.runResume());
      $("manju-agent-run").addEventListener("click", () => this.runAgent());
      $("manju-env").addEventListener("click", () => this.openHealth());
      $("manju-stop").addEventListener("click", () => this.stop());
      // 清空日志(2026-08-26 修复:此前只在前端盖一条「(就绪)」,后端 logTail 未清,
      // 2 秒轮询把旧日志又拉回来)——调后端清 manjuState.log,本地同步置空即时反馈
      $("manju-clear-log").addEventListener("click", () => {
        post("/api/manju/log/clear", {})
          .then(() => { if (this.status) this.status.logTail = ""; this.logNote("(已清空)"); })
          .catch(() => this.logNote("(清空失败: 后端未响应)"));
      });

      // 弹窗:多级弹窗(2026-08-24 用户要求)——每个弹窗独立遮罩层压栈管理,
      // 弹窗里再弹窗时父弹窗保留,关闭子弹窗自动露出父弹窗(绑定见 openModal)
      // Esc 单例监听:一次 Esc 只关当前顶层弹窗(每层各自绑定会一次关多层)
      document.addEventListener("keydown", (e) => { if (e.key === "Escape") this.closeModal(); });

      // 左右栏拖拽改变宽度
      this.bindResizers();
      this.bindSideCollapse();

      // 右栏产物区折叠/展开
      this.bindOutputsFold();
    },

    /* 右栏产物区折叠/展开:仅折叠「产物」主体,运行状态区不受影响,状态记忆 localStorage */
    bindOutputsFold() {
      const head = $("manju-out-fold");
      const body = $("manju-out-body");
      if (!head || !body) return;
      head.addEventListener("click", () => {
        // 清理/预告片已移至产物 body 工具行,不再冒泡到头部
        this._setOutputsFold(!body.classList.contains("is-folded"));
      });
      if (localStorage.getItem("manju-out-collapsed") === "1") this._setOutputsFold(true);
      // 预告片:按审片分数自动剪辑高分镜头
      const tr = $("manju-trailer");
      if (tr) tr.addEventListener("click", () => this.makeTrailer());
      const cleanBtn = $("manju-cleanup");
      if (cleanBtn) cleanBtn.addEventListener("click", () => this.openCleanup());
      // 2026-08-25 用户要求:导航栏右侧「扫帚」按钮,一键清理旧缓存(方案/镜头/条件缓存/接缝)
      const ccBtn = $("nav-cache-clear");
      if (ccBtn) ccBtn.addEventListener("click", () => this.clearCache());
    },

    /* 一键清理旧缓存(2026-08-25):弹窗二选一——
       「确认」= 普通清理:analysis 方案 JSON + 镜头 mp4 + 条件缓存 + 接缝 latent(定妆照/场景图/成片/2K 不动);
       「高级」= 普通清理 + 清空 ComfyUI 共享 input/output 目录全部产物(彻底清场,慎用)。
       用途:分镜脚本修复后旧 plan 的错误时长/缺失台词会让修复不生效;新项目也常被旧缓存影响。 */
    clearCache() {
      if (!this.project) { this.setErr("请先选择项目后再清理缓存"); return; }
      const fmtB = this._fmtBytes;
      this.openModal("🧹 清理缓存",
        `<div class="manju-confirm">
          <p class="mc-q">选择清理范围:</p>
          <div class="mc-opt" style="margin-bottom:14px">
            <p style="margin:6px 0;font-size:13px;color:var(--muted)"><b style="color:var(--text)">确认(普通清理)</b> — 删除本项目旧缓存:方案 JSON(plan/characters/prompts) + 镜头 mp4 + 条件缓存 + 接缝 latent。<br>定妆照 / 场景图 / 成片 / 2K 产物不受影响。</p>
            <p style="margin:6px 0;font-size:13px;color:var(--muted)"><b style="color:var(--err)">高级(彻底清场)</b> — 在普通清理基础上,<b style="color:var(--err)">删除 ComfyUI 共享 input 与 output 目录里的全部产物</b>(含其他项目的图片/视频/中间产物)并<b style="color:var(--err)">清空运行日志数据</b>(界面日志与各项目 run.log)。<br><span style="color:var(--err)">⚠ 渲染产物将被清空,需重新渲染;请确认 ComfyUI 未在运行关键任务。</span></p>
          </div>
          <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px">
            <button id="cc-confirm" class="hrs-btn hrs-btn-primary">确认(普通)</button>
            <button id="cc-advanced" class="hrs-btn hrs-btn-danger" style="background:var(--err);color:#fff">高级(清 ComfyUI)</button>
            <button id="cc-cancel" class="hrs-btn">取消</button>
          </div>
        </div>`);
      const btn = $("nav-cache-clear");
      $("cc-cancel").addEventListener("click", () => this.closeModal());
      $("cc-confirm").addEventListener("click", () => { this.closeModal(); this._runCacheClear(btn, false); });
      $("cc-advanced").addEventListener("click", () => {
        if (!confirm("高级清理将删除 ComfyUI 共享 input/output 目录里的全部产物(含其他项目的图片/视频)。\n此操作不可恢复,确定继续？")) return;
        this.closeModal();
        this._runCacheClear(btn, true);
      });
    },
    _runCacheClear(btn, advanced) {
      if (btn) { btn.disabled = true; btn.style.opacity = ".5"; }
      post("/api/manju/cache/clear", { config: this.project, advanced: !!advanced })
        .then((r) => {
          const parts = (r.cleaned || []).map((c) => `${c.target} ${c.files}个(${this._fmtBytes(c.bytes || 0)})`);
          this.setErr("🧹 缓存已清理" + (advanced ? "(高级:含 ComfyUI input/output)" : "") + ": " + (parts.join("、") || "无残留"));
          setTimeout(() => this.setErr(""), 6000);
          this.refreshOutputs();
          this.loadPlan();
        }).catch((e) => this.setErr(e.message))
        .finally(() => { if (btn) { btn.disabled = false; btn.style.opacity = ""; } });
    },
    _fmtBytes(n) {
      if (n == null) return "0B";
      return n >= 1048576 ? (n / 1048576).toFixed(1) + "MB" : n >= 1024 ? Math.round(n / 1024) + "KB" : n + "B";
    },

    /* 预告片自动剪辑:高分镜头掐头去尾拼接 30s(音量归一+字幕),产物落工作目录 */
    makeTrailer() {
      if (this.denyNoProject()) return;
      const btn = $("manju-trailer");
      btn.disabled = true;
      btn.textContent = "剪辑中…";
      post("/api/manju/trailer", { config: this.project, episode: this.episode, target: 30 }).then((r) => {
        this.setErr("");
        this.logNote("(🎬 预告片已生成: " + r.file + "(选 " + r.shots + " 个高分镜头) → 工作目录 " + this.episode + "_预告片.mp4)");
        this.refreshOutputs();
      }).catch((e) => this.setErr(e.message))
        .finally(() => { btn.disabled = false; btn.textContent = "🎬 预告片"; });
    },
    _setOutputsFold(collapsed) {
      const body = $("manju-out-body");
      if (!body) return;
      body.classList.toggle("is-folded", !!collapsed);
      const head = $("manju-out-fold");
      if (head) head.classList.toggle("is-collapsed", !!collapsed);
      const btn = $("manju-out-fold-btn");
      if (btn) btn.textContent = collapsed ? "▸" : "▾";
      try { localStorage.setItem("manju-out-collapsed", collapsed ? "1" : "0"); } catch (e) {}
    },

    /* 折叠/展开某侧栏(side: left|right),切换按钮固定在分隔槽居中 */
    _sideCollapse(side, collapsed) {
      const body = document.querySelector(".view-manju .manju-body");
      if (!body) return;
      const varName = side === "left" ? "--mj-left-w" : "--mj-right-w";
      const sidebar = side === "left" ? $("manju-side-left") : $("manju-side-right");
      const toggleBtn = side === "left" ? $("manju-left-toggle") : $("manju-right-toggle");
      if (!sidebar) return;
      this._prevW = this._prevW || {};
      if (collapsed) {
        const cur = parseFloat(getComputedStyle(body).getPropertyValue(varName));
        if (cur > 0) this._prevW[varName] = cur;
        body.style.setProperty(varName, "0px");
        sidebar.classList.add("collapsed");
      } else {
        body.style.setProperty(varName, (this._prevW[varName] || (side === "left" ? 280 : 360)) + "px");
        sidebar.classList.remove("collapsed");
      }
      // 更新切换按钮方向与状态(左:展开◂/收起▸;右:展开▸/收起◂)
      if (toggleBtn) {
        const chev = side === "left" ? (collapsed ? "▸" : "◂") : (collapsed ? "◂" : "▸");
        toggleBtn.textContent = chev;
        toggleBtn.classList.toggle("is-collapsed", collapsed);
      }
      localStorage.setItem("manju" + varName + "-collapsed", collapsed ? "1" : "0");
    },

    bindSideCollapse() {
      const toggle = (id, side) => {
        const btn = $(id);
        if (!btn) return;
        // 阻止冒泡到分隔条,避免触发拖拽
        btn.addEventListener("mousedown", (e) => e.stopPropagation());
        btn.addEventListener("click", () => {
          const sidebar = side === "left" ? $("manju-side-left") : $("manju-side-right");
          const collapsed = sidebar && sidebar.classList.contains("collapsed");
          this._sideCollapse(side, !collapsed);
        });
      };
      toggle("manju-left-toggle", "left");
      toggle("manju-right-toggle", "right");
      if (localStorage.getItem("manju--mj-left-w-collapsed") === "1") this._sideCollapse("left", true);
      if (localStorage.getItem("manju--mj-right-w-collapsed") === "1") this._sideCollapse("right", true);
    },

    bindResizers() {
      const body = document.querySelector(".view-manju .manju-body");
      if (!body) return;
      const setup = (dividerId, varName, minW, dir, side) => {
        const divider = $(dividerId);
        if (!divider) return;
        let startX = 0, startW = 0;
        const onMove = (e) => {
          const w = Math.max(minW, Math.min(900, startW + dir * (e.clientX - startX)));
          body.style.setProperty(varName, w + "px");
          // 拖动改变宽度时,若侧栏处于收起态,直接展开(不覆盖拖动宽度)
          const sidebar = side === "left" ? $("manju-side-left") : $("manju-side-right");
          if (sidebar && sidebar.classList.contains("collapsed")) {
            sidebar.classList.remove("collapsed");
            const tbtn = side === "left" ? $("manju-left-toggle") : $("manju-right-toggle");
            if (tbtn) { tbtn.textContent = side === "left" ? "◂" : "▸"; tbtn.classList.remove("is-collapsed"); }
            localStorage.setItem("manju" + varName + "-collapsed", "0");
          }
        };
        const onUp = () => {
          divider.classList.remove("dragging");
          document.body.style.cursor = "";
          document.body.style.userSelect = "";
          document.removeEventListener("mousemove", onMove);
          document.removeEventListener("mouseup", onUp);
          const v = parseFloat(getComputedStyle(body).getPropertyValue(varName));
          if (v) localStorage.setItem("manju" + varName, String(v));
        };
        divider.addEventListener("mousedown", (e) => {
          startX = e.clientX;
          startW = parseFloat(getComputedStyle(body).getPropertyValue(varName)) || minW;
          divider.classList.add("dragging");
          document.body.style.cursor = "col-resize";
          document.body.style.userSelect = "none";
          document.addEventListener("mousemove", onMove);
          document.addEventListener("mouseup", onUp);
          e.preventDefault();
        });
      };
      setup("manju-divider-left", "--mj-left-w", 200, 1, "left");   // 拖右增宽
      setup("manju-divider-right", "--mj-right-w", 280, -1, "right"); // 拖右减宽
      const lw = localStorage.getItem("manju--mj-left-w");
      const rw = localStorage.getItem("manju--mj-right-w");
      if (lw) body.style.setProperty("--mj-left-w", lw + "px");
      if (rw) body.style.setProperty("--mj-right-w", rw + "px");
    },

    /* ---- 项目 ---- */
    loadProjects(sel) {
      get("/api/manju/projects").then((r) => {
        this.projects = r.projects || [];
        this.pruneStaleDrafts();
        const selEl = $("manju-project");
        selEl.innerHTML = '<option value="">— 选择项目 —</option>' +
          this.projects.map((p) => `<option value="${esc(p.configPath)}">${esc(p.name)}</option>`).join("");
        const saved = sel || ls("project");
        if (saved && this.projects.some((p) => p.configPath === saved)) this.project = saved;
        else if (this.projects.length) this.project = this.projects[0].configPath;
        else this.project = "";
        selEl.value = this.project;
        ls("project", this.project); // 始终同步,项目被删后清掉旧值
        if (this.project) { this.loadProject(); }
        else { this.resetProjectData(); }
      }).catch((e) => this.setErr("项目列表失败: " + e.message));
    },

    /* 清空当前项目相关状态(项目已删除/无项目时,避免残留旧数据) */
    resetProjectData() {
      this.info = null;
      this.outputs = { clips: [], artifacts: {}, final: null };
      this.plan = null;
      this.gacha = {};
      // 清空小说来源:无项目时不得残留 localStorage 里上次项目的旧值
      this.novel = "";
      this._novelProject = "";
      const nv = $("manju-novel");
      if (nv) nv.value = "";
      ls("novel", "");
      const nm = $("manju-novel-meta");
      if (nm) nm.textContent = "";
      // 清空运行状态:无项目时不得残留上次磁盘状态(如 run.state.json 的旧项目)
      this.status = { running: false, stage: "", currentStage: "", stageIdx: -1, shotCur: 0, shotTotal: 0, progress: 0, done: false, rc: null, stopped: false, elapsedSec: 0, logTail: "" };
      this.agent = null;
      this.renderStatus();
      this.renderChips();
      this.clearForm();
      const outEl = $("manju-outputs");
      if (outEl) outEl.innerHTML = `<div class="manju-empty">请选择项目</div>`;
      const gaEl = $("manju-gacha");
      if (gaEl) gaEl.innerHTML = `<div class="manju-empty">请选择项目后抽卡</div>`;
    },

    /* 清理已删除项目的渲染配置草稿(localStorage 键 manju-render-<configPath>) */
    pruneStaleDrafts() {
      try {
        const keep = new Set(this.projects.map((p) => p.configPath));
        for (let i = localStorage.length - 1; i >= 0; i--) {
          const k = localStorage.key(i);
          if (k && k.startsWith("manju-render-")) {
            const cfg = k.slice("manju-render-".length);
            if (cfg && !keep.has(cfg)) localStorage.removeItem(k);
          }
          // 旧版未按项目隔离的折叠键(manju-sec-<分区>),已废弃,清理避免跨项目残留
          if (k && k.startsWith("manju-sec-")) {
            const rest = k.slice("manju-sec-".length);
            if (rest === "char" || rest === "scene" || rest.indexOf("video") === 0) localStorage.removeItem(k);
          }
        }
      } catch (e) {}
    },

    /* ---- 微信通知 ---- */
    notifyChannel() { return $("manju-notify-channel").value; },
    /* 根据渠道动态显示对应字段,并返回当前表单的配置对象 */
    notifyForm() {
      const ch = this.notifyChannel();
      const tokenLabel = $("manju-notify-token-label");
      const showEndpoint = ch === "wecom" || ch === "custom";
      const showUid = ch === "wxpusher";
      $("manju-notify-endpoint-wrap").hidden = !showEndpoint;
      $("manju-notify-uid-wrap").hidden = !showUid;
      $("manju-notify-token-wrap").hidden = showEndpoint && ch === "wecom"; // 企业微信只需 webhook 地址
      // 渠道官网:选中即显示对应入口,点击新标签打开(自定义 Webhook 无官网)
      const site = {
        serverchan: ["Server酱官网", "https://sct.ftqq.com/"],
        pushplus: ["PushPlus 官网", "https://www.pushplus.plus/"],
        wecom: ["企业微信机器人文档", "https://developer.work.weixin.qq.com/document/path/91770"],
        wxpusher: ["WxPusher 官网", "https://wxpusher.zjiecode.com/"],
      }[ch];
      const siteBtn = $("manju-notify-site");
      if (siteBtn) {
        siteBtn.hidden = !site;
        if (site) { siteBtn.href = site[1]; siteBtn.textContent = site[0] + " ↗"; siteBtn.title = site[1]; }
      }
      if (ch === "serverchan") tokenLabel.textContent = "SendKey";
      else if (ch === "pushplus") tokenLabel.textContent = "Token";
      else if (ch === "wxpusher") tokenLabel.textContent = "AppToken";
      else if (ch === "wecom") tokenLabel.textContent = "Token（可选）";
      else tokenLabel.textContent = "Token（可选）";
      return {
        channel: ch,
        endpoint: $("manju-notify-endpoint").value.trim(),
        token: $("manju-notify-token").value.trim(),
        uid: $("manju-notify-uid").value.trim(),
      };
    },
    /* 设置弹窗:智能体 + DeepSeek API Key + 微信通知(使用说明按钮后的设置按钮进入) */
    openSettings() {
      // 立即开壳(加载中),避免异步请求期间点按钮无反应;代次守卫防"关闭后又弹回"
      this.openModal("设置", '<div class="dir-loading">加载中…</div>', true);
      const gen = this._modalGen;
      const q = this.project ? "?config=" + encodeURIComponent(this.project) : "";
      const agP = this.project ? get("/api/manju/agent" + q).catch(() => null) : Promise.resolve(null);
      // 通知配置单独存于 notify.json,须与渲染设置合并回填,否则重开弹窗显示为空
      const notifyP = get("/api/manju/notify").catch(() => ({}));
      const pathsP = get("/api/manju/paths").catch(() => null);
      get("/api/manju/settings" + q)
        .catch(() => ({}))
        .then((n) => notifyP.then((nf) => agP.then((ag) => pathsP.then((paths) => {
          if (gen !== this._modalGen) return;   // 弹窗已被关闭/切换:放弃渲染,不弹回
          this.renderSettings(Object.assign({}, n, nf), ag, paths);
        }))));
    },
    renderSettings(n, ag, paths) {
      n = n || {};
      const agFail = ag == null; // 智能体配置请求失败(null)标记:视觉区提示而非静默全空
      ag = ag || {};
      // 默认服务描述:LLM 区回填与绑定区「未配置」提示共用。
      // 注意:必须定义在函数顶层——曾定义在 LLM 区 IIFE 内,绑定区引用抛 ReferenceError
      // 导致其后的按钮绑定(微信通知保存等)全部中断,表现为"配置了保存不了"
      const defSvc = (n.defaultBaseUrl && n.defaultModel)
        ? n.defaultBaseUrl + " / " + n.defaultModel
        : "https://api.deepseek.com / deepseek-chat";
      // 「目录与部署」区:5 个路径输入 + 生效值 + ComfyUI/技能状态(数据来自 /api/manju/paths)
      const _paths = paths || {};
      const pathsHTML = (() => {
        if (!_paths.effective) return '<div class="manju-meta">路径配置加载失败</div>';
        const cfg = _paths.configured || {};
        const eff = _paths.effective || {};
        const rows = [
          ["mp-manju-root", "manju_root", "项目目录(漫剧项目)", ""],
          ["mp-novel-root", "novel_root", "小说库根目录", ""],
          ["mp-comfy-root", "comfy_root", "ComfyUI 安装(含 main.py/.venv)", ""],
          ["mp-comfy-shared", "comfy_shared", "ComfyUI 共享(模型/输入/输出)", ""],
          ["mp-comfy-output", "comfy_output", "ComfyUI 成品输出目录", "自动(跟随共享目录 output)"],
          ["mp-novel-skill", "novel_skill", "小说续作技能(词库/qa)", ""],
        ].map(([id, key, label, ph]) =>
          `<div class="manju-set-item">
            <div class="manju-set-item-title">${label}</div>
            <input id="${id}" class="manju-input manju-mono" placeholder="${ph || "自动(exe 目录/" + key + ")"}" value="${esc(cfg[key] || "")}" spellcheck="false" autocomplete="off">
            <div class="manju-meta">生效: ${esc(eff[key] || "--")}</div>
          </div>`
        ).join("");
        const cfy = _paths.comfy || {};
        const sk = _paths.skill || {};
        const status = _paths.comfy
          ? `🖥 ComfyUI 目录: ${cfy.exists ? "✅ 存在" : "❌ 不存在(需安装或改路径)"}${cfy.venv ? " · venv 就绪" : " · venv 缺失(需重新安装依赖)"}${sk.exists ? " · 技能库 ✅" : " · 技能库缺失"}`
          : "";
        const installBtn = _paths.comfy && !cfy.exists
          ? `<div class="manju-set-actions" style="margin-top:10px">
              <button id="ci-install" class="hrs-btn hrs-btn-primary">🚀 一键安装 ComfyUI(自动下载程序+节点+模型)</button>
              <button id="ci-stop" class="hrs-btn hidden">⏹ 停止</button>
              <span id="ci-msg" class="manju-meta manju-set-msg"></span>
            </div>
            <pre id="ci-log" class="manju-log hidden" style="max-height:160px;overflow:auto;margin-top:6px;font-size:10.5px;white-space:pre-wrap"></pre>`
          : "";
        return `<div class="manju-meta">换电脑/迁移:把整个 NiliX 目录(含 comfyui/ novel/ manju/ skills/)拷走即用。路径留空=自动解析(优先 exe 目录子目录,其次旧位置)。</div>
          <div class="manju-set-grid manju-set-grid-2">${rows}</div>
          <div class="manju-set-actions" style="margin-top:10px">
            <button id="mp-save" class="hrs-btn hrs-btn-primary">保存路径配置</button>
            ${sk.isGit ? `<button id="mp-skill-update" class="hrs-btn">🔄 更新技能(git pull)</button>` : ""}
            <span id="mp-msg" class="manju-meta manju-set-msg"></span>
          </div>
          ${installBtn}
          ${status ? `<div class="manju-set-status">${status}</div>` : ""}
          <div class="manju-set-status">改路径后 ComfyUI 需重启才用新目录;项目/小说/技能目录即时生效。</div>`;
      })();
      const ch = n.channel || "serverchan";
      const chOpts = [
        ["serverchan", "Server酱"], ["pushplus", "PushPlus"], ["wecom", "企业微信群机器人"],
        ["wxpusher", "WxPusher"], ["custom", "自定义 Webhook"],
      ].map(([v, label]) => `<option value="${v}" ${ch === v ? "selected" : ""}>${label}</option>`).join("");
      const keyStatus = n.projectKey
        ? `<span class="st-ok">✅ 当前项目已设置 Key（${esc(n.projectMasked || "")}）</span>`
        : (n.hasKey
          ? `<span class="st-ok">✅ 已保存默认 Key（${esc(n.masked || "")}）</span>`
          : `<span class="st-bad">⚠️ 未设置任何 Key，LLM 调用会报 401</span>`);
      this.rerenderModal("设置",
        `<div class="manju-form manju-settings">
          <div class="manju-set-card manju-set-hero">
            <div class="manju-set-head">
              <span class="manju-set-icon">🤖</span>
              <span class="manju-set-title">智能体调度</span>
              <label class="manju-set-switch"><input type="checkbox" id="manju-ag-enabled" ${ag.agentEnabled ? "checked" : ""}><span class="manju-set-switch-track"><span class="manju-set-switch-dot"></span></span>智能模式</label>
            </div>
            <div class="manju-set-body">
              <div class="manju-set-sub">🧠 文本模型 · 方案生成 / 剧本师 / 修复师</div>
              ${(() => {
                /* 回填当前服务:按 项目 base_url+model → 匹配预设;匹配不到且有值 → 自定义 */
                const curUrl = (n.projectBaseUrl || "").replace(/\/+$/, "");
                const curModel = n.projectModel || "";
                const llmHit = LLM_PRESETS.find((p) => p.url.replace(/\/+$/, "") === curUrl && p.model === curModel);
                const llmCustom = !llmHit && (curUrl || curModel);
                const llmSel = llmHit ? llmHit.id : (llmCustom ? "__custom__" : "");
                return `
              <div class="manju-field-row">
                <label>服务商</label>
                <select id="manju-llm-svc" class="manju-input">
                  <option value="" ${llmSel ? "" : "selected"}>未配置（默认 DeepSeek）</option>
                  ${LLM_PRESETS.map((p) => `<option value="${p.id}" ${llmSel === p.id ? "selected" : ""}>${p.label}</option>`).join("")}
                  <option value="__custom__" ${llmCustom ? "selected" : ""}>自定义（OpenAI 兼容）…</option>
                </select>
              </div>
              <div class="manju-field-row" id="manju-llm-url-row" ${llmCustom ? "" : 'style="display:none"'}>
                <label>接口地址</label>
                <input id="manju-llm-baseurl" class="manju-input manju-mono" value="${esc(llmCustom ? curUrl : "")}" placeholder="如 https://api.deepseek.com 或 https://…/v1" spellcheck="false">
              </div>
              <div class="manju-field-row" id="manju-llm-model-row" ${llmCustom ? "" : 'style="display:none"'}>
                <label>模型 ID</label>
                <input id="manju-llm-model" class="manju-input manju-mono" value="${esc(llmCustom ? curModel : "")}" placeholder="如 deepseek-chat" spellcheck="false">
              </div>
              <div class="manju-field-row">
                <label>API Key</label>
                <input id="manju-apikey" class="manju-input manju-mono" type="password" placeholder="sk-…（留空则用已保存的默认 Key）" spellcheck="false" autocomplete="off">
                <button id="manju-apikey-apply" class="hrs-btn hrs-btn-primary">应用到项目</button>
                <button id="manju-apikey-save" class="hrs-btn">存为默认</button>
              </div>
              <div class="manju-set-status" id="manju-llm-svc-hint"></div>
              <div id="manju-apikey-status" class="manju-set-status">${keyStatus}</div>
              <div class="manju-field-row">
                <button id="manju-llm-test" class="hrs-btn">🔌 测试连接</button>
                <span id="manju-llm-test-result" class="manju-set-status"></span>
              </div>`;
              })()}

              <div class="manju-set-sub">👁 视觉模型 · 审片官（未配置时仅机械质检，不判分不返工）</div>
              ${(() => {
                const gd = ag.globalDefaults || {};
                // 项目缺失(config 不存在):视觉区回显全局默认(项目未配置时本就自动用全局),避免整块空白
                const projMissing = !!ag.projectMissing;
                // 项目配置优先;未单独配置时回显全局默认——运行时 loadAgentCfg 本就项目空→回退全局,
                // 若这里不回显,用户会看到"视觉模型被清空"(实际配置在全局默认里一直生效)
                const vModel = ag.visionModel || gd.visionModel || "";
                const vUrl = ag.visionBaseUrl || gd.visionBaseUrl || "";
                const vHasKey = ag.hasVisionKey || !!gd.hasVisionKey;
                // 项目存在但未单独配置、正回显全局默认:标注来源,避免误以为被清空
                const usingGlobal = !projMissing && !ag.visionModel && (gd.visionModel || gd.hasVisionKey);
                const presetHit = VISION_PRESETS.find((p) => p.id === vModel);
                const isCustom = vModel && !presetHit;
                return `
              ${projMissing ? '<div class="manju-set-status st-bad">⚠️ 项目 config 不存在（目录已删除/未创建），以下展示<b>全局默认</b>配置，重建项目后自动生效</div>' : ""}
              ${usingGlobal ? '<div class="manju-set-status st-bad">⚠️ 当前项目未单独配置视觉模型，以下展示<b>全局默认</b>值（运行中一直在生效）；点「保存智能体配置」即写入当前项目</div>' : ""}
              ${agFail ? '<div class="manju-set-status st-bad">⚠️ 智能体配置读取失败（服务异常），请关闭弹窗重开重试</div>' : ""}
              <div class="manju-field-row">
                <label>模型</label>
                <select id="manju-ag-model" class="manju-input">
                  <option value="" ${vModel ? "" : "selected"}>请选择视觉模型…</option>
                  ${VISION_PRESETS.map((p) => `<option value="${p.id}" ${presetHit && presetHit.id === p.id ? "selected" : ""}>${p.label}</option>`).join("")}
                  <option value="__custom__" ${isCustom ? "selected" : ""}>自定义…</option>
                </select>
              </div>
              <div class="manju-field-row" id="manju-ag-model-custom-row" ${isCustom ? "" : 'style="display:none"'}>
                <label>模型 ID</label>
                <input id="manju-ag-model-custom" class="manju-input manju-mono" value="${esc(isCustom ? vModel : "")}" placeholder="模型 ID,支持逗号降级链如 glm-4.6v-flash,glm-4v-flash" spellcheck="false" title="主模型 429 过载时按 4/10/20s 退避重试,耗尽自动降级备模型继续判分;单模型自动补内置链(智谱免费档)">
              </div>
              <div class="manju-field-row" id="manju-ag-url-row" ${isCustom ? "" : 'style="display:none"'}>
                <label>API 地址</label>
                <input id="manju-ag-url" class="manju-input manju-mono" value="${esc(vUrl)}" placeholder="OpenAI 兼容地址，如 https://open.bigmodel.cn/api/paas/v4" spellcheck="false">
              </div>
              <div class="manju-field-row">
                <label>API Key</label>
                <input id="manju-ag-key" class="manju-input manju-mono" type="password" placeholder="${vHasKey ? (projMissing ? "全局默认已存 Key，留空沿用" : "已保存(" + esc(ag.visionKeyMasked || "") + ")，留空沿用") : "粘贴所选模型对应平台的 API Key"}" spellcheck="false" autocomplete="off">
              </div>
              <div class="manju-set-status" id="manju-ag-key-hint"></div>`;
              })()}
              <div class="manju-set-row2">
                <div class="manju-field-row">
                  <label>及格线</label><input id="manju-ag-pass" class="manju-input manju-num" type="number" min="40" max="100" value="${Math.round(ag.passScore || 75)}"><span class="manju-set-unit">分</span>
                </div>
                <div class="manju-field-row">
                  <label>返工轮数</label><input id="manju-ag-retries" class="manju-input manju-num" type="number" min="0" max="4" value="${ag.maxRetries == null ? 2 : ag.maxRetries}"><span class="manju-set-unit">轮</span>
                </div>
                <div class="manju-field-row">
                  <label>判分并发</label><input id="manju-ag-conc" class="manju-input manju-num" type="number" min="1" max="4" value="${ag.judgeConcurrency == null ? 2 : ag.judgeConcurrency}" title="视觉判分 API 并发上限(1-4)。免费档(智谱 flash)建议 2——调高易触发 429(会自动退避+粘性降级);付费 Key 可调 3-4 提速审片"><span class="manju-set-unit">路</span>
                </div>
              </div>
              <label class="manju-check" style="margin-top:2px"><input type="checkbox" id="manju-ag-auto" ${ag.autoResolve === false ? "" : "checked"}> 🤖 预算耗尽 AI 终审自动拍板（接受该镜最佳结果 / 从零重写提示词再试一轮，不等人拍板；审片报告可事后重试）</label>
              <div class="manju-set-sub">☁️ 云端 2K 定稿 · MiniMax(审片通过的本地定稿镜提交云端升 2K,本地 GPU 零负担)</div>
              <div class="manju-field-row">
                <label>API Key</label>
                <input id="manju-ag-mmkey" class="manju-input manju-mono" type="password" placeholder="${ag.hasMinimaxKey ? "已保存(" + esc(ag.minimaxKeyMasked || "") + "),留空沿用" : "MiniMax 平台 API Key(产物区「☁️ 2K」按钮用)"}" spellcheck="false" autocomplete="off">
              </div>
              <div class="manju-set-status">本地 768×1344 / 24fps / 17k+5 帧网格产物与官方 /v2/video_regeneration 预校验完全兼容:提交前本地体检(32 整除/面积/帧率/帧网格/音轨/50MB),2K 产物落 clips/集/2k/。国内平台在项目 config.render.minimax_base_url 填 https://api.minimaxi.com</div>
              <div class="manju-set-status">审片八维度对齐 MiniMax H3 官方能力：主体/场景一致性(Ref2VA 参考保持)、动作/运镜符合(多模态指令遵循)、可见性(近黑防线)、技术质量(畸变/水印)、风格、口型对白。低分镜头由修复师改写 H3 提示词后自动定点重渲染（「🤖 AI 一条龙」走全流程）。点「🤖 AI 一条龙」会先询问是否让 Agent 深度分析小说内容并更新渲染风格（是=分析后更新；否=按当前配置直接跑）。</div>
              ${(() => {
                const g = ag.globalDefaults || {};
                // 恒显示:未配置也明确告知,避免"视觉区整块空白误以为被清空"
                return `<div class="manju-set-status manju-set-global">🌐 全局默认（所有项目共用）：${g.visionModel ? esc(g.visionModel) : "未设模型"}${g.hasVisionKey ? " · Key 已存" : ""} · 及格 ${Math.round(g.passScore || 75)} · 返工 ${g.maxRetries == null ? 2 : g.maxRetries} 轮${g.enabled ? "" : " · 智能模式默认关"}。项目未单独配置时自动使用，点「另存为全局默认」可更新。</div>`;
              })()}
              <div class="manju-set-actions">
                <button id="manju-ag-save" class="hrs-btn hrs-btn-primary">保存智能体配置</button>
                <button id="manju-ag-test" class="hrs-btn">测试视觉模型</button>
                <button id="manju-ag-global" class="hrs-btn" title="当前表单另存为全局默认：所有项目未单独配置时自动使用（含视觉模型/及格线/返工轮数）">另存为全局默认</button>
                <span id="manju-ag-msg" class="manju-meta manju-set-msg"></span>
              </div>
            </div>
          </div>
          <div class="manju-set-row2 manju-set-row2-cards">
          <div class="manju-set-card">
            <div class="manju-set-head">
              <span class="manju-set-icon">📣</span>
              <span class="manju-set-title">微信通知</span>
            </div>
            <div class="manju-set-body">
              <label class="manju-check"><input type="checkbox" id="manju-notify-enabled" ${n.enabled ? "checked" : ""}> 启用阶段切换 / 审片升级通知</label>
              <div class="manju-field-row">
                <label>推送渠道</label>
                <select id="manju-notify-channel" class="manju-input">${chOpts}</select>
                <a id="manju-notify-site" class="hrs-btn manju-notify-site" target="_blank" rel="noopener">官网 ↗</a>
              </div>
              <div class="manju-field-row" id="manju-notify-token-wrap">
                <label id="manju-notify-token-label">SendKey</label>
                <input id="manju-notify-token" class="manju-input manju-mono" value="${esc(n.token || "")}" spellcheck="false">
              </div>
              <div class="manju-field-row" id="manju-notify-endpoint-wrap" hidden>
                <label>Webhook 地址</label>
                <input id="manju-notify-endpoint" class="manju-input manju-mono" value="${esc(n.endpoint || "")}" spellcheck="false" placeholder="https://…">
              </div>
              <div class="manju-field-row" id="manju-notify-uid-wrap" hidden>
                <label>UID</label>
                <input id="manju-notify-uid" class="manju-input manju-mono" value="${esc(n.uid || "")}" spellcheck="false" placeholder="UID_xxx,UID_yyy（多个用逗号分隔）">
              </div>
              <div class="manju-set-actions">
                <button id="manju-notify-save" class="hrs-btn hrs-btn-primary">保存通知配置</button>
                <button id="manju-notify-test" class="hrs-btn">发送测试</button>
                <span id="manju-notify-msg" class="manju-meta manju-set-msg"></span>
              </div>
            </div>
          </div>
          <div class="manju-set-card">
            <div class="manju-set-head">
              <span class="manju-set-icon">📦</span>
              <span class="manju-set-title">配置管理</span>
            </div>
            <div class="manju-set-body">
              <div class="manju-set-line">
                <button id="mc-export" class="hrs-btn hrs-btn-primary">导出为 JSON</button>
                <span class="manju-meta">导出当前渲染配置为 JSON，可跨项目复用</span>
              </div>
              <div class="manju-set-line">
                <button id="mc-import" class="hrs-btn">选择 JSON 文件</button>
                <span class="manju-meta">从 JSON 文件导入渲染配置，点「保存参数」写入项目</span>
                <input id="mc-import-file" type="file" accept=".json,application/json" style="display:none">
              </div>
              <div class="manju-set-line">
                <button id="mc-reset" class="hrs-btn hrs-btn-danger">恢复默认</button>
                <span class="manju-meta">按当前参数为默认（保留当前值，移除自定义风格），需重新保存</span>
              </div>
              <span id="mc-import-msg" class="manju-meta manju-set-msg"></span>
            </div>
          </div>
          </div>
          <div class="manju-set-card">
            <div class="manju-set-head">
              <span class="manju-set-icon">🗂️</span>
              <span class="manju-set-title">目录与部署</span>
            </div>
            <div class="manju-set-body">
              ${pathsHTML}
            </div>
          </div>
        </div>`, true);
      // ComfyUI 一键安装:启动 + 轮询进度 + 停止
      const ciInstall = $("ci-install");
      if (ciInstall) ciInstall.addEventListener("click", () => {
        const msg = $("ci-msg"), lg = $("ci-log"), stop = $("ci-stop");
        if (msg) msg.textContent = "安装启动中…";
        post("/api/comfy/install", {}).then((r) => {
          if (!r.ok) { if (msg) msg.textContent = "❌ " + (r.error || "启动失败"); return; }
          if (msg) msg.textContent = "⏳ 安装中(程序约1.5GB,模型数十GB,可后台等待)…";
          if (lg) { lg.classList.remove("hidden"); lg.textContent = "(开始下载…)"; }
          if (stop) stop.classList.remove("hidden");
          this._ciTimer = setInterval(() => {
            get("/api/comfy/install/status").then((st) => {
              if (lg && st.log) lg.textContent = st.log;
              if (msg) {
                if (st.running) msg.textContent = "⏳ " + (st.item || st.step || "安装中") + " …";
                else if (st.done) {
                  clearInterval(this._ciTimer); this._ciTimer = null;
                  if (stop) stop.classList.add("hidden");
                  msg.textContent = st.rc === 0 ? "✅ 安装完成,点 ComfyUI 页「启动」即可使用" : ("❌ " + (st.err || "安装未完成"));
                  this.openSettings(); // 刷新路径状态
                }
              }
            }).catch(() => {});
          }, 2000);
        }).catch((e) => { if (msg) msg.textContent = "❌ " + e.message; });
      });
      const ciStop = $("ci-stop");
      if (ciStop) ciStop.addEventListener("click", () => {
        post("/api/comfy/install/stop", {}).then(() => {
          const msg = $("ci-msg");
          if (msg) msg.textContent = "⏹ 已请求停止";
        });
      });
      // mp-save 仅在 paths 接口返回 effective 时渲染;失败/缺失时无该按钮,
      // 必须 if 保护——否则 $() 为 null 抛错会中断其后所有绑定(与 defSvc 同款事故)
      const mpSave = $("mp-save");
      if (mpSave) mpSave.addEventListener("click", () => {
        const msg = $("mp-msg");
        if (msg) msg.textContent = "保存中…";
        post("/api/manju/paths", {
          manju_root: $("mp-manju-root").value.trim(),
          novel_root: $("mp-novel-root").value.trim(),
          comfy_root: $("mp-comfy-root").value.trim(),
          comfy_shared: $("mp-comfy-shared").value.trim(),
          comfy_output: $("mp-comfy-output").value.trim(),
          novel_skill: $("mp-novel-skill").value.trim(),
        }).then((r) => {
          if (msg) msg.textContent = r.ok ? "✅ 已保存并生效(ComfyUI 重启后完全生效)" : ("❌ " + (r.error || "保存失败"));
          this.openSettings();
        }).catch((e) => { if (msg) msg.textContent = "❌ " + e.message; });
      });
      const mpSkill = $("mp-skill-update");
      if (mpSkill) mpSkill.addEventListener("click", () => {
        const msg = $("mp-msg");
        if (msg) msg.textContent = "git pull 中…";
        post("/api/manju/skill/update", {}).then((r) => {
          if (msg) msg.textContent = r.ok ? "✅ " + (r.output || "已更新") : ("❌ " + (r.error || "更新失败") + (r.output ? " " + r.output : ""));
        }).catch((e) => { if (msg) msg.textContent = "❌ " + e.message; });
      });
      $("manju-apikey-save").addEventListener("click", () => this.saveApiKey(false));
      $("manju-apikey-apply").addEventListener("click", () => this.saveApiKey(true));
      $("manju-llm-svc").addEventListener("change", () => this.syncLLMForm());
      // 审计升级:设置页「测试连接」——调 /api/settings/test 测全局默认 LLM + ComfyUI 连通性
      $("manju-llm-test").addEventListener("click", () => this.testLLM());
      $("manju-ag-save").addEventListener("click", () => this.saveAgentCfg());
      $("manju-ag-test").addEventListener("click", () => this.testVision());
      $("manju-ag-global").addEventListener("click", () => this.saveAgentCfgGlobal());
      $("manju-ag-model").addEventListener("change", () => this.syncVisionForm());
      this.syncVisionForm();
      this._llmDefSvc = defSvc;   // 供「未配置」提示展示真实默认服务(存为默认的地址/模型)
      this.syncLLMForm();
      // 智能模式开关:切换即时保存(勾选后刷新不再回落)
      $("manju-ag-enabled").addEventListener("change", () => {
        const msg = $("manju-ag-msg");
        if (!this.project) {
          $("manju-ag-enabled").checked = !$("manju-ag-enabled").checked; // 无项目:回滚,避免"以为已保存"
          msg.textContent = "⚠️ 请先选择项目再切换智能模式";
          return;
        }
        msg.textContent = "保存中…";
        this.saveAgentCfgQuiet().then(() => {
          msg.textContent = $("manju-ag-enabled").checked ? "✅ 智能模式已开启" : "✅ 智能模式已关闭";
          setTimeout(() => { if (msg.textContent.startsWith("✅")) msg.textContent = ""; }, 3000);
          this.poll();
        }).catch((e) => { msg.textContent = "❌ " + e.message; });
      });
      $("manju-notify-channel").addEventListener("change", () => this.notifyForm());
      $("manju-notify-save").addEventListener("click", () => this.saveNotify());
      $("manju-notify-test").addEventListener("click", () => this.testNotify());
      this.notifyForm();
      // 配置管理:导出/导入/恢复默认(原独立弹窗并入设置)
      $("mc-export").addEventListener("click", () => this.exportConfig());
      $("mc-import").addEventListener("click", () => $("mc-import-file").click());
      $("mc-import-file").addEventListener("change", (e) => this.importConfig(e.target.files[0]));
      $("mc-reset").addEventListener("click", () => this.resetConfig());
    },
    /* 文本模型表单联动:预设自动带出接口地址+模型 ID 并提示 Key 去处,自定义时展开地址/模型输入 */
    syncLLMForm() {
      const sel = $("manju-llm-svc");
      if (!sel) return;
      const custom = sel.value === "__custom__";
      $("manju-llm-url-row").style.display = custom ? "" : "none";
      $("manju-llm-model-row").style.display = custom ? "" : "none";
      const hint = $("manju-llm-svc-hint");
      if (custom) {
        hint.textContent = "自定义模式：填 OpenAI 兼容接口地址与模型 ID（如本地 Ollama/LM Studio/中转站），再填对应 Key";
      } else if (sel.value) {
        const preset = LLM_PRESETS.find((p) => p.id === sel.value);
        hint.textContent = "自动使用 " + preset.url + " / " + preset.model + "｜" + preset.hint;
      } else {
        hint.textContent = "未配置：使用默认服务（" + this._llmDefSvc + "）；Key 留空按 项目配置 → 默认 Key 顺序兜底";
      }
    },
    llmFormValues() {
      const sel = $("manju-llm-svc").value;
      if (sel === "__custom__") {
        return { baseUrl: $("manju-llm-baseurl").value.trim(), model: $("manju-llm-model").value.trim() };
      }
      const preset = LLM_PRESETS.find((p) => p.id === sel);
      return { baseUrl: preset ? preset.url : "", model: preset ? preset.model : "" };
    },
    saveApiKey(applyToProject) {
      const st = $("manju-apikey-status");
      const key = $("manju-apikey").value.trim();
      const v = this.llmFormValues();
      if (!key && !v.baseUrl && !v.model) { st.textContent = "请先粘贴 API Key 或选择服务"; return; }
      if (applyToProject && !this.project) { st.textContent = "请先选择项目再应用到项目"; return; }
      st.textContent = "保存中…";
      const btns = [$("manju-apikey-save"), $("manju-apikey-apply")];
      btns.forEach((b) => { b.disabled = true; }); // 防连点重复提交
      const body = { apiKey: key };
      if (v.baseUrl) body.baseUrl = v.baseUrl;
      if (v.model) body.model = v.model;
      if (applyToProject) body.config = this.project;
      post("/api/manju/settings", body).then(() => {
        const svc = v.baseUrl ? "（含服务 " + v.model + "）" : "";
        st.textContent = applyToProject ? "✅ 已写入当前项目" + svc : "✅ 已保存为默认" + svc;
        if (applyToProject) this.loadProject();
      }).catch((e) => { st.textContent = "❌ " + e.message; })
        .finally(() => { btns.forEach((b) => { b.disabled = false; }); });
    },
    /* 审计升级:设置页「测试连接」——/api/settings/test 测全局默认 LLM 与 ComfyUI 连通 */
    testLLM() {
      const res = $("manju-llm-test-result");
      res.textContent = "测试中…";
      post("/api/settings/test").then((r) => {
        const parts = [];
        if (r.llm) parts.push(r.llm.ok ? "✅ LLM " + (r.llm.message || "连通") : "❌ LLM " + (r.llm.message || "失败"));
        if (r.comfyui) parts.push(r.comfyui.ok ? "✅ ComfyUI " + (r.comfyui.message || "在线") : "❌ ComfyUI " + (r.comfyui.message || "离线"));
        res.textContent = parts.join(" · ") || "测试完成";
        res.style.color = parts.some((p) => p.indexOf("❌") >= 0) ? "#f85149" : "#3fb950";
      }).catch((e) => { res.textContent = "❌ " + e.message; res.style.color = "#f85149"; });
    },
    /* 视觉模型表单联动:预设自动带地址并提示 Key 去处,自定义时展开两行 */    syncVisionForm() {
      const sel = $("manju-ag-model");
      if (!sel) return;
      const custom = sel.value === "__custom__";
      const preset = VISION_PRESETS.find((p) => p.id === sel.value);
      $("manju-ag-model-custom-row").style.display = custom ? "" : "none";
      $("manju-ag-url-row").style.display = custom ? "" : "none";
      $("manju-ag-key-hint").textContent = preset
        ? "自动使用 " + preset.url + "｜" + preset.hint + "｜高峰 429 自动退避并降级 glm-4v-flash"
        : custom ? "自定义模式：填模型 ID(可逗号降级链)、API 地址与对应 Key" : "选好模型后只填 API Key 即可，接口地址自动带出；Key 留空时按 项目配置 → 项目 LLM Key → 环境变量 GLM_VISION_API_KEY 顺序兜底";
    },
    visionFormValues() {
      const sel = $("manju-ag-model").value;
      if (sel === "__custom__") {
        return { model: $("manju-ag-model-custom").value.trim(), url: $("manju-ag-url").value.trim() };
      }
      const preset = VISION_PRESETS.find((p) => p.id === sel);
      return { model: sel, url: preset ? preset.url : "" };
    },
    /* 智能体配置保存(写入项目 config.json 的 agent 节;留空字段沿用旧值) */
    saveAgentCfg() {
      if (!this.project) return;
      const msg = $("manju-ag-msg");
      const btn = $("manju-ag-save");
      msg.textContent = "保存中…";
      btn.disabled = true;
      const v = this.visionFormValues();
      post("/api/manju/agent/settings", {
        config: this.project,
        agent: {
          enabled: $("manju-ag-enabled").checked,
          vision_base_url: v.url,
          vision_api_key: $("manju-ag-key").value.trim(),
          vision_model: v.model,
          pass_score: parseFloat($("manju-ag-pass").value) || 75,
          max_retries: parseInt($("manju-ag-retries").value, 10),
          judge_concurrency: parseInt($("manju-ag-conc").value, 10) || 2,
          auto_resolve: $("manju-ag-auto") ? $("manju-ag-auto").checked : true,
        },
        minimax_api_key: $("manju-ag-mmkey") ? $("manju-ag-mmkey").value.trim() : "",
      }).then(() => {
        msg.textContent = "✅ 已保存";
        setTimeout(() => { msg.textContent = ""; }, 3000);
        this.poll();
      }).catch((e) => { msg.textContent = "❌ " + e.message; })
        .finally(() => { btn.disabled = false; });
    },
    /* 视觉模型连通测试(拿项目第一张定妆照问一句话) */
    testVision() {
      if (!this.project) return;
      const msg = $("manju-ag-msg");
      const btn = $("manju-ag-test");
      const model = this.visionFormValues().model;
      if (!model) { msg.textContent = "请先选择视觉模型"; return; }
      msg.textContent = "测试中(先保存再测)…";
      btn.disabled = true;
      this.saveAgentCfgQuiet().then(() => {
        msg.textContent = "测试中…";
        return post("/api/manju/agent/vision-test", { config: this.project });
      }).then((r) => {
        msg.textContent = r.ok ? "✅ 连通正常（" + (r.visionModel || "") + "）" : "❌ " + (r.error || "失败");
      }).catch((e) => { msg.textContent = "❌ " + e.message; })
        .finally(() => { btn.disabled = false; });
    },
    /* 当前表单另存为全局默认(settings.json agent 节):所有项目未单独配置时自动使用 */
    saveAgentCfgGlobal() {
      const msg = $("manju-ag-msg");
      const btn = $("manju-ag-global");
      const v = this.visionFormValues();
      if (!v.model && !$("manju-ag-enabled").checked) { msg.textContent = "请先选择视觉模型或开启智能模式"; return; }
      msg.textContent = "保存全局默认…";
      btn.disabled = true;
      post("/api/manju/agent/settings", {
        config: this.project,
        global: "true",
        agent: {
          enabled: $("manju-ag-enabled").checked,
          vision_base_url: v.url,
          vision_api_key: $("manju-ag-key").value.trim(),
          vision_model: v.model,
          pass_score: parseFloat($("manju-ag-pass").value) || 75,
          max_retries: parseInt($("manju-ag-retries").value, 10),
          judge_concurrency: parseInt($("manju-ag-conc").value, 10) || 2,
          auto_resolve: $("manju-ag-auto") ? $("manju-ag-auto").checked : true,
        },
        minimax_api_key: $("manju-ag-mmkey") ? $("manju-ag-mmkey").value.trim() : "",
      }).then(() => {
        msg.textContent = "✅ 已存为全局默认（所有项目共用）";
        setTimeout(() => { msg.textContent = ""; }, 4000);
      }).catch((e) => { msg.textContent = "❌ " + e.message; })
        .finally(() => { btn.disabled = false; });
    },
    saveAgentCfgQuiet() {
      const v = this.visionFormValues();
      return post("/api/manju/agent/settings", {
        config: this.project,
        agent: {
          enabled: $("manju-ag-enabled").checked,
          vision_base_url: v.url,
          vision_api_key: $("manju-ag-key").value.trim(),
          vision_model: v.model,
          pass_score: parseFloat($("manju-ag-pass").value) || 75,
          max_retries: parseInt($("manju-ag-retries").value, 10),
          judge_concurrency: parseInt($("manju-ag-conc").value, 10) || 2,
          auto_resolve: $("manju-ag-auto") ? $("manju-ag-auto").checked : true,
        },
        minimax_api_key: $("manju-ag-mmkey") ? $("manju-ag-mmkey").value.trim() : "",
      });
    },
    saveNotify() {
      const msg = $("manju-notify-msg");
      const btn = $("manju-notify-save");
      msg.textContent = "保存中…";
      btn.disabled = true;
      const body = this.notifyForm();
      body.enabled = $("manju-notify-enabled").checked;
      post("/api/manju/notify", body).then(() => {
        msg.textContent = "✅ 通知配置已保存";
        setTimeout(() => { msg.textContent = ""; }, 3000);
      }).catch((e) => { msg.textContent = "❌ " + e.message; })
        .finally(() => { btn.disabled = false; });
    },
    testNotify() {
      const msg = $("manju-notify-msg");
      const btn = $("manju-notify-test");
      msg.textContent = "发送测试…";
      btn.disabled = true;
      // 先落盘当前配置,再触发测试推送(测试读取已保存配置)
      const body = this.notifyForm();
      body.enabled = true;
      post("/api/manju/notify", body)
        .then(() => post("/api/manju/notify/test", { message: "漫剧通知测试(来自 kb-workbench)" }))
        .then(() => {
          msg.textContent = "✅ 测试通知已发送";
          setTimeout(() => { msg.textContent = ""; }, 3000);
        }).catch((e) => { msg.textContent = "❌ " + e.message; })
        .finally(() => { btn.disabled = false; });
    },

    loadProject() {
      if (!this.project) return;
      get("/api/manju/project?config=" + encodeURIComponent(this.project)).then((r) => {
        this.info = r;
        // 小说来源同步当前项目:切到新项目时回填该项目配置的小说(用户手动改过则保留)
        if (this._novelProject !== this.project) {
          this._novelProject = this.project;
          this.novel = (r && r.paths && r.paths.novel) || "";
          $("manju-novel").value = this.novel;
          ls("novel", this.novel);
          this.detectNovel();
        }
        this.fillForm();
        // fillForm 之后再拉模型列表:下拉 option 已就绪,回填不会因竞态丢失(首次 bind 已拉过一次)
        this.loadModels();
        this.renderChips();
        this.refreshOutputs();
        this.refreshScriptStatus(); // 视频脚本直出模式状态(与小说解析二选一)
        // 体检预热:发现可修复项 → 右栏 Agent 面板出主动建议横幅(仅一次/会话)
        this._healthTipDismissed = false;
        this.loadHealth(false);
      }).catch((e) => { this.info = null; this.renderChips(); });
    },

    renderChips() {
      const el = $("manju-chips");
      const tt = $("manju-chips-title");
      const hideAll = () => { el.innerHTML = ""; el.hidden = true; if (tt) tt.hidden = true; };
      if (!this.info) { hideAll(); return; }
      const R = this.info.render || {};
      const model = (this.info.llm || {}).model;
      const items = [
        "风格：" + this.styleLabel(this.info.style),
        model && "LLM：" + model,
        R.comfy_url && "ComfyUI：" + R.comfy_url,
        R.width && R.height && R.fps && "画幅：" + R.width + "×" + R.height + " @" + R.fps + "fps",
        R.res_tier && R.res_tier !== "custom" && "档位：" + (RES_TIER_CN[R.res_tier] || R.res_tier),
        R.steps && (R.turbo_lora ? "步数：" + R.steps + " → Turbo " + R.turbo_steps : "步数：" + R.steps),
        R.seed !== undefined && R.seed !== null && R.seed !== "" && "seed：" + R.seed + (R.seed_policy && R.seed_policy !== "fixed" ? "(" + (R.seed_policy === "increment" ? "重试递增" : "重试随机") + ")" : ""),
        R.sage_attention && "⚡SageAttn",
        R.draft_judge && "📐草稿预审",
        R.fl2va_end_frame && "🖼️FL2VA",
        R.transition && R.transition !== "cut" && "转场:" + ({ fade: "闪黑", dissolve: "叠化" }[R.transition] || R.transition),
        R.bgm && "🎵BGM",
        R.shots_per_take && R.shots_per_take > 1 && "🎥长镜×" + R.shots_per_take,
      ].filter(Boolean);
      if (items.length === 0) { hideAll(); return; } // 无数据:整个参数卡(含边框)都不显示
      el.hidden = false;
      el.innerHTML = items.map((c) => `<span class="manju-chip">${esc(c)}</span>`).join("");
      if (tt) {
        tt.hidden = false;
        // 2026-08-25 用户要求:悬浮项目卡「参数」单独收起/展开(大类分项,状态记忆)
        const folded = this._chipsFolded();
        tt.classList.toggle("is-folded", folded);
        el.classList.toggle("is-folded", folded);
        const fb = tt.querySelector(".manju-sec-foldbtn");
        if (fb) fb.textContent = folded ? "▸" : "▾";
      }
    },
    _chipsFolded() {
      let v = null;
      try { v = localStorage.getItem("manju-chips-fold"); } catch (e) {}
      return v === "1";
    },

    /* ---- 表单回填 ---- */
    renderInputIds() {
      return ["manju-width", "manju-height", "manju-fps", "manju-steps", "manju-turbo", "manju-seed",
        "manju-minsec", "manju-maxsec", "manju-comfy-url", "manju-neg-prompt", "manju-unet-fl2va", "manju-unet-ref2va",
        "manju-clip", "manju-vae-video", "manju-vae-audio", "manju-zimage-unet", "manju-zimage-clip",
        "manju-zimage-vae", "manju-turbo-lora", "manju-turbo-lora-r2v", "manju-char-male", "manju-char-female", "manju-animagine",
        "manju-banned-words", "manju-mosaic-level", "manju-res-tier", "manju-seed-policy", "manju-draft-scale",
        "manju-transition", "manju-bgm", "manju-bgm-gain", "manju-take",
        "manju-char-engine", "manju-krea2-unet", "manju-krea2-clip", "manju-krea2-vae"];
    },
    draftKey() { return "render-" + (this.project || ""); },
    /* 渲染配置自动保存(用户要求:填了参数自动保存,不用手动点保存按钮):
       1) 立即写 localStorage 草稿(刷新不丢,保留现有机制);
       2) 防抖 2 秒后静默自动提交到后端 config.json(统一提交,无需手动点)。
       防重复:同一时刻只允许一个在途请求;运行中不提交(避免与渲染冲突);
       提交失败保留草稿并提示(下次变化自动重试)。 */
    autoSaveTimer: null,
    autoSavePending: false,
    _autoSaveBusy: false,
    scheduleAutoSave() {
      if (!this.project) return;
      // 立即落本地草稿(刷新不丢)
      this.saveDraftLocal();
      // 防抖 2s 统一提交后端
      clearTimeout(this.autoSaveTimer);
      this.autoSaveTimer = setTimeout(() => this.autoSaveSubmit(), 2000);
    },
    saveDraftLocal() {
      if (!this.project) return;
      const d = { style: this.style };
      this.renderInputIds().forEach((id) => { d[id] = $(id).value; });
      d.mosaicEnabled = $("manju-mosaic-enabled").checked;
      d.sageEnabled = $("manju-sage").checked;
      d.draftJudge = $("manju-draft-judge").checked;
      d.fl2vaEndFrame = $("manju-fl2va").checked;
      d.subtitle = $("manju-subtitle").checked;
      d.voiceover = $("manju-voiceover").checked;
      try { localStorage.setItem("manju-" + this.draftKey(), JSON.stringify(d)); } catch (e) {}
    },
    autoSaveSubmit(manual) {
      if (!this.project || this._autoSaveBusy) return Promise.resolve(false);
      // 运行中不自动提交(渲染期间改参数下次变化再提交,避免与运行参数冲突);手动保存除外
      if (!manual && this.status && this.status.running) return Promise.resolve(false);
      this._autoSaveBusy = true;
      const body = this.collectRenderConfig();
      body.config = this.project;
      return post("/api/manju/render", body).then((r) => {
        this._autoSaveBusy = false;
        if (r.ok) {
          // 成功后同步回填服务端归一化值并清草稿(与手动保存一致)
          this.info = Object.assign({}, this.info, { render: r.render, style: r.style, moderation: r.moderation });
          this.clearDraft();
          this.renderChips();
          return true;
        } else {
          // 失败保留草稿(下次变化自动重试),静默提示不打扰
          const msg = $("manju-render-msg");
          if (msg) { msg.textContent = (manual ? "❌ 保存失败: " : "⏳ 自动保存失败(已保留草稿): ") + (r.error || "未知错误"); msg.style.color = manual ? "" : "#e0a64a"; setTimeout(() => { msg.textContent = ""; }, 4000); }
          return false;
        }
      }).catch((e) => {
        this._autoSaveBusy = false;
        const msg = $("manju-render-msg");
        if (msg) { msg.textContent = (manual ? "❌ 保存失败: " : "⏳ 自动保存失败(已保留草稿): ") + e.message; msg.style.color = manual ? "" : "#e0a64a"; setTimeout(() => { msg.textContent = ""; }, 4000); }
        return false;
      });
    },
    /* 手动保存(兜底:自动保存失败/运行前确保落盘),与自动提交共用逻辑 */
    saveRender() {
      if (this.denyNoProject()) return;
      clearTimeout(this.autoSaveTimer);
      const msg = $("manju-render-msg");
      if (msg) { msg.textContent = "保存中…"; msg.style.color = ""; }
      this.autoSaveSubmit(true).then((ok) => {
        if (msg && ok) { msg.textContent = "✅ 参数已保存到 config.json"; setTimeout(() => { msg.textContent = ""; }, 3000); }
      });
    },
    loadDraft() {
      try {
        const s = localStorage.getItem("manju-" + this.draftKey());
        return s ? JSON.parse(s) : null;
      } catch (e) { return null; }
    },
    /* 增强胶囊同步:checkbox 状态 → 胶囊 on 态(风格同款分段控件可视化) */
    syncBoostButtons() {
      const map = [["sage", "manju-sage"], ["draft", "manju-draft-judge"], ["fl2va", "manju-fl2va"]];
      document.querySelectorAll("#manju-boost [data-boost]").forEach((b) => {
        const id = { sage: "manju-sage", draft: "manju-draft-judge", fl2va: "manju-fl2va" }[b.dataset.boost];
        const cb = $(id);
        b.classList.toggle("on", !!(cb && cb.checked));
      });
    },
    clearDraft() {
      try { localStorage.removeItem("manju-" + this.draftKey()); } catch (e) {}
    },
    clearForm() {
      this.renderInputIds().forEach((id) => { $(id).value = NUM_DEFAULTS[id] !== undefined ? NUM_DEFAULTS[id] : ""; });
      $("manju-neg-prompt").value = NEG_PROMPT_DEFAULT;
      $("manju-mosaic-enabled").checked = false;
      $("manju-sage").checked = false;
      $("manju-draft-judge").checked = false;
      $("manju-fl2va").checked = false;
      $("manju-seed-policy").value = "fixed";
      $("manju-transition").value = "cut";
      this.syncBoostButtons();
    },

    fillForm() {
      const R = (this.info && this.info.render) || {};
      const draft = this.loadDraft();
      // 默认风格:写实(real)——与后端 manjuDefaultConfig 一致,旧 2.5d 不是默认
      this.style = (draft && draft.style) || (this.info && this.info.style) || "real";
      const set = (id, v) => { if (v !== undefined && v !== null) $(id).value = v; };
      const num = (id, v) => { $(id).value = (v !== undefined && v !== null && v !== "") ? v : NUM_DEFAULTS[id]; };
      num("manju-width", R.width);
      num("manju-height", R.height);
      num("manju-fps", R.fps);
      num("manju-steps", R.steps);
      num("manju-turbo", R.turbo_steps);
      num("manju-seed", R.seed);
      num("manju-minsec", R.min_shot_seconds);
      num("manju-maxsec", R.max_shot_seconds);
      num("manju-take", R.shots_per_take != null && R.shots_per_take !== "" ? R.shots_per_take : 1);
      // 章节/集数是「每次运行的即时参数」:loadProject 恒重置为 0(0=解析小说总章数)。
      // 不回填 config.render.chapters/episode——那是上次运行由 writeManjuRunParams 写入的
      // 残留(如 1-1/EP01),回填会覆盖默认 0 造成「自动变成 1-1/1」的错觉。
      set("manju-chapters", "0");
      num("manju-episode", 0);
      this.chapters = "0";
      this.episode = "0";
      set("manju-comfy-url", R.comfy_url);
      set("manju-neg-prompt", R.neg_prompt || NEG_PROMPT_DEFAULT);
      set("manju-unet-fl2va", R.unet_fl2va);
      set("manju-unet-ref2va", R.unet_ref2va);
      set("manju-clip", R.clip);
      set("manju-vae-video", R.vae_video);
      set("manju-vae-audio", R.vae_audio);
      set("manju-zimage-unet", R.z_image_unet);
      set("manju-zimage-clip", R.z_image_clip);
      set("manju-zimage-vae", R.z_image_vae);
      set("manju-turbo-lora", R.turbo_lora);
      set("manju-turbo-lora-r2v", R.turbo_lora_r2v);
      const cm = R.char_models || {};
      set("manju-char-male", cm["男"]);
      set("manju-char-female", cm["女"]);
      set("manju-animagine", R.animagine_ckpt);
      const MOD = (this.info && this.info.moderation) || {};
      set("manju-banned-words", Array.isArray(MOD.banned_words) ? MOD.banned_words.join("\n") : "");
      $("manju-mosaic-enabled").checked = !!MOD.mosaic_enabled;
      set("manju-mosaic-level", MOD.mosaic_level != null ? MOD.mosaic_level : 16);
      // 渲染升级项:档位/seed策略/SageAttention/草稿预审
      set("manju-res-tier", R.res_tier || "");
      set("manju-seed-policy", R.seed_policy || "fixed");
      $("manju-sage").checked = !!R.sage_attention;
      $("manju-draft-judge").checked = !!R.draft_judge;
      $("manju-fl2va").checked = !!R.fl2va_end_frame;
      num("manju-draft-scale", R.draft_scale != null && R.draft_scale !== "" ? R.draft_scale : 0.5);
      set("manju-transition", R.transition || "cut");
      set("manju-bgm", R.bgm || "");
      num("manju-bgm-gain", R.bgm_gain != null && R.bgm_gain !== "" ? R.bgm_gain : 0.28);
      // 2026-08-23 新增:定妆引擎/字幕/配音/Krea2 回填(解析脚本后同步显示)
      set("manju-char-engine", R.char_engine || "zimage");
      $("manju-subtitle").checked = R.subtitle === true; // 2026-08-24 默认关:H3 原生对白音轨已含台词
      $("manju-voiceover").checked = !!R.voiceover;
      set("manju-krea2-unet", R.krea2_unet);
      set("manju-krea2-clip", R.krea2_clip);
      set("manju-krea2-vae", R.krea2_vae);
      // 未保存编辑优先:用草稿覆盖 config.json 的回填值
      if (draft) {
        this.renderInputIds().forEach((id) => {
          if (draft[id] !== undefined && draft[id] !== "") $(id).value = draft[id];
        });
        if (draft.mosaicEnabled !== undefined) $("manju-mosaic-enabled").checked = !!draft.mosaicEnabled;
        if (draft.sageEnabled !== undefined) $("manju-sage").checked = !!draft.sageEnabled;
        if (draft.draftJudge !== undefined) $("manju-draft-judge").checked = !!draft.draftJudge;
        if (draft.fl2vaEndFrame !== undefined) $("manju-fl2va").checked = !!draft.fl2vaEndFrame;
      }
      this.renderStyle();
      this.renderRatio();
      this.syncBoostButtons();
    },

    /* 预设按钮渲染:从 STYLE_PRESETS 单一数据源生成(保留下方的「?」帮助按钮) */
    renderStylePresets() {
      const seg = $("manju-style");
      if (!seg) return;
      seg.querySelectorAll("button[data-style]").forEach((b) => b.remove());
      const frag = document.createDocumentFragment();
      STYLE_PRESETS.forEach(([key, label]) => {
        const b = document.createElement("button");
        b.className = "hrs-btn";
        b.dataset.style = key;
        b.textContent = label;
        b.title = "点击选中/取消（可多选叠加组合）";
        frag.appendChild(b);
      });
      seg.insertBefore(frag, $("manju-style-help"));
    },

    /* 当前 style 中选中的预设 key 集合(组合以 + 分隔;不在预设表内的段落视为自定义词) */
    styleKeys() {
      return new Set(this.styleWords().filter((s) => STYLE_CN[s] !== undefined));
    },

    /* 风格展示:预设转中文名、自定义词原样,多元素以 + 连接(卡片/chips 用) */
    styleLabel(style) {
      if (!style) return "";
      return String(style).split(/[+,]+/).map((s) => s.trim()).filter(Boolean)
        .map((s) => STYLE_CN[s] || s).join(" + ");
    },

    /* 风格切换:点击切换选中态(预设与自定义 TAG 是累加关系——切换预设不得丢失
       自定义词,含总集风格解析出的独立 tag)。组合以 + 分隔存 this.style。 */
    toggleStyle(key, multi) {
      const words = this.styleWords();          // 全部元素(预设 + 自定义)
      const keys = this.styleKeys();            // 预设部分
      if (multi) {
        if (keys.has(key)) {
          // 取消该预设:仅移除 key,自定义词保留
          const rest = words.filter((w) => w !== key);
          this.style = rest.length ? rest.join("+") : "real";
        } else {
          // 选中该预设:累加在自定义词之后(不重复)
          if (!words.includes(key)) words.push(key);
          this.style = words.join("+");
        }
      } else {
        // 单选:清掉其他预设,保留选中预设 + 全部自定义词
        const customs = words.filter((w) => STYLE_CN[w] === undefined);
        this.style = [key].concat(customs).join("+");
      }
      this.renderStyle();
      this.scheduleAutoSave();
    },

    /* 自定义风格「应用」= 累加:新输入词追加到当前风格(预设+旧自定义 TAG)之后,
    重复词自动过滤(大小写不敏感/含中文名);输入预设 key 或中文名(如 水墨)归一为
    预设 key(对应按钮点亮);删除词走 TAG 右上角 × */
    combineCustom(v) {
      const parts = this.styleWords();
      const seen = new Set(parts.flatMap((k) => [k.toLowerCase(), (STYLE_CN[k] || "").toLowerCase()]).filter(Boolean));
      String(v).split(/[,+]+/).map((s) => s.trim()).filter(Boolean).forEach((s) => {
        let norm = s;
        const low = s.toLowerCase(), flat = low.replace(/\s+/g, "");
        for (const [k, label] of STYLE_PRESETS) {
          if (low === k || flat === String(label).replace(/\s+/g, "")) { norm = k; break; }
        }
        const key = norm.toLowerCase();
        if (!seen.has(key)) { seen.add(key); parts.push(norm); }
      });
      return parts.join("+");
    },

    /* 风格元素拆分:预设组合以 + 分隔;总集风格/自定义长句含逗号(Cinematic film still,
       live-action, photorealistic, ...)——逗号与 + 都拆成独立元素,前端按独立 TAG 展示 */
    styleWords() {
      return String(this.style || "").split(/[+,]+/).map((s) => s.trim()).filter(Boolean);
    },

    renderStyle() {
      const keys = this.styleKeys();
      document.querySelectorAll("#manju-style button[data-style]").forEach((b) =>
        b.classList.toggle("on", keys.has(b.dataset.style))
      );
      // 自定义风格:style 中非预设部分渲染为 TAG 标签(输入框上方,点 × 删除);
      // 总集风格等逗号分隔长句也逐段拆成独立 TAG(用户反馈:风格需拆独立 tag 展示)
      const tags = $("manju-custom-tags");
      if (tags) {
        const nonPreset = this.styleWords()
          .filter((s) => STYLE_CN[s] === undefined && !STYLE_PRESETS.some(([k]) => k === s.toLowerCase()));
        tags.innerHTML = nonPreset.map((w) =>
          `<span class="style-tag">${esc(w)}<i class="style-tag-x" data-word="${esc(w)}" title="删除该风格">×</i></span>`).join("");
        tags.classList.toggle("has-tags", nonPreset.length > 0);
        tags.querySelectorAll(".style-tag-x").forEach((x) =>
          x.addEventListener("click", () => this.removeCustomWord(x.dataset.word))
        );
      }
    },

    /* 删除一个自定义风格 TAG:从 style 组合中移除该词(逗号/加号分隔均支持);
       删空且无预设时回退默认 写实(real) */
    removeCustomWord(word) {
      const rest = this.styleWords().filter((p) => p !== word);
      this.style = rest.length ? rest.join("+") : "real";
      this.renderStyle();
      this.scheduleAutoSave();
    },

    renderRatio() {
      const w = Number($("manju-width").value), h = Number($("manju-height").value);
      document.querySelectorAll("#manju-ratio button").forEach((b) => {
        const wh = RATIOS[b.dataset.ratio];
        b.classList.toggle("on", !!wh && wh[0] === w && wh[1] === h);
      });
    },

    /* 高级配置：从 ComfyUI 模型目录加载可选模型，填充下拉（避免手输误操作） */
    loadModels() {
      get("/api/manju/models").then((r) => {
        const opts = r || {};
        document.querySelectorAll("#manju-adv select[data-model]").forEach((sel) => {
          const names = opts[sel.dataset.model] || [];
          const cur = sel.value; // fillForm 已回填的 config 模型名
          let html = '<option value="">—</option>';
          names.forEach((n) => { html += `<option value="${esc(n)}">${esc(n)}</option>`; });
          sel.innerHTML = html;
          if (cur) {
            if (names.indexOf(cur) >= 0) {
              sel.value = cur;
            } else {
              // 配置里的模型名已不在目录中：保留原值并标注缺失
              sel.insertAdjacentHTML("beforeend", `<option value="${esc(cur)}" selected>${esc(cur)}（已缺失）</option>`);
            }
          }
        });
      }).catch(() => {});
    },

    /* ---- 小说 ---- */
    detectNovel() {
      if (!this.novel) return;
      get("/api/manju/novel?config=" + encodeURIComponent(this.project || "") + "&novel=" + encodeURIComponent(this.novel)).then((r) => {
        this.novelInfo = r;
        const el = $("manju-novel-meta");
        if (r.error) el.innerHTML = `<span class="manju-err-text">${esc(r.error)}</span>`;
        else if (r.count) {
          const overridden = this.novel && this.info && this.info.paths && this.novel !== this.info.paths.novel;
          // 默认(章节/集数=0)会每章一集全渲染:明确告知任务规模,防误触超长任务
          const epHint = (!this.chapters || this.chapters === "0") && (!this.episode || this.episode === "0")
            ? " · 默认将渲染 " + r.count + " 集（每章一集）" : "";
          el.textContent = "共 " + r.count + " 章（第 " + r.first + "–" + r.last + " 章）· " + (r.chars / 10000).toFixed(2) + " 万字" + epHint + (overridden ? " · 已覆盖默认配置" : "");
        } else el.textContent = "";
      }).catch(() => { this.novelInfo = null; });
    },

    /* ---- 渲染参数保存 ---- */
    intVal(id) { const v = $(id).value; return v === "" ? undefined : parseInt(v, 10); },
    strVal(id) { return $(id).value.trim(); },

    /* 收集当前渲染配置(含风格/数值/模型/审核/渲染升级项) */
    collectRenderConfig() {
      const body = { style: this.style };
      INT_KEYS.forEach((k) => { const v = this.mapInt(k); if (v !== undefined) body[k] = v; });
      STR_KEYS.forEach((k) => { const v = this.mapStr(k); if (v) body[k] = v; });
      body.neg_prompt = this.strVal("manju-neg-prompt") || "";
      // 角色模型 checkpoint(char_models.男/女):前端 camelCase 提交,后端 setCharModel 写入 render.char_models
      const charM = this.strVal("manju-char-male"), charF = this.strVal("manju-char-female");
      if (charM) body.charModelMale = charM;
      if (charF) body.charModelFemale = charF;
      body.banned_words = this.strVal("manju-banned-words").split("\n").map((s) => s.trim()).filter(Boolean);
      body.mosaic_enabled = $("manju-mosaic-enabled").checked;
      body.mosaic_level = this.intVal("manju-mosaic-level");
      body.res_tier = this.strVal("manju-res-tier");
      body.seed_policy = this.strVal("manju-seed-policy") || "fixed";
      body.sage_attention = $("manju-sage").checked;
      body.draft_judge = $("manju-draft-judge").checked;
      body.fl2va_end_frame = $("manju-fl2va").checked;
      const ds = parseFloat($("manju-draft-scale").value);
      if (!isNaN(ds)) body.draft_scale = ds;
      body.transition = this.strVal("manju-transition") || "cut";
      body.bgm = this.strVal("manju-bgm");
      const bg = parseFloat($("manju-bgm-gain").value);
      if (!isNaN(bg)) body.bgm_gain = bg;
      // 2026-08-23 新增:定妆引擎/字幕/配音/Krea2
      body.char_engine = this.strVal("manju-char-engine") || "zimage";
      body.subtitle = $("manju-subtitle").checked;
      body.voiceover = $("manju-voiceover").checked;
      body.krea2_unet = this.strVal("manju-krea2-unet");
      body.krea2_clip = this.strVal("manju-krea2-clip");
      body.krea2_vae = this.strVal("manju-krea2-vae");
      return body;
    },

    /* 应用渲染配置到表单(导出/恢复默认后回填) */
    applyRenderConfig(cfg) {
      if (!cfg) return;
      const R = cfg.render || cfg;
      this.style = cfg.style || this.style;
      const set = (id, v) => { if (v !== undefined && v !== null && v !== "") $(id).value = v; };
      INT_KEYS.forEach((k) => set("manju-" + { width: "width", height: "height", fps: "fps", steps: "steps", turbo_steps: "turbo", seed: "seed", min_shot_seconds: "minsec", max_shot_seconds: "maxsec", shots_per_take: "take" }[k], R[k]));
      set("manju-comfy-url", R.comfy_url);
      set("manju-neg-prompt", R.neg_prompt || NEG_PROMPT_DEFAULT);
      set("manju-unet-fl2va", R.unet_fl2va);
      set("manju-unet-ref2va", R.unet_ref2va);
      set("manju-clip", R.clip);
      set("manju-vae-video", R.vae_video);
      set("manju-vae-audio", R.vae_audio);
      set("manju-zimage-unet", R.z_image_unet);
      set("manju-zimage-clip", R.z_image_clip);
      set("manju-zimage-vae", R.z_image_vae);
      set("manju-turbo-lora", R.turbo_lora);
      set("manju-turbo-lora-r2v", R.turbo_lora_r2v);
      const cm = R.char_models || {};
      set("manju-char-male", cm["男"]);
      set("manju-char-female", cm["女"]);
      set("manju-animagine", R.animagine_ckpt);
      const MOD = cfg.moderation || {};
      set("manju-banned-words", Array.isArray(MOD.banned_words) ? MOD.banned_words.join("\n") : "");
      $("manju-mosaic-enabled").checked = !!MOD.mosaic_enabled;
      set("manju-mosaic-level", MOD.mosaic_level);
      set("manju-res-tier", R.res_tier || "");
      set("manju-seed-policy", R.seed_policy || "fixed");
      $("manju-sage").checked = !!R.sage_attention;
      $("manju-draft-judge").checked = !!R.draft_judge;
      $("manju-fl2va").checked = !!R.fl2va_end_frame;
      set("manju-draft-scale", R.draft_scale != null && R.draft_scale !== "" ? R.draft_scale : 0.5);
      set("manju-transition", R.transition || "cut");
      set("manju-bgm", R.bgm || "");
      set("manju-bgm-gain", R.bgm_gain != null && R.bgm_gain !== "" ? R.bgm_gain : 0.28);
      set("manju-take", R.shots_per_take != null && R.shots_per_take !== "" ? R.shots_per_take : 1);
      // 2026-08-23 新增:定妆引擎/字幕/配音/Krea2 回填
      set("manju-char-engine", R.char_engine || "zimage");
      $("manju-subtitle").checked = R.subtitle === true; // 2026-08-24 默认关:H3 原生对白音轨已含台词,烧录字幕多余且触发 OCR 误报
      $("manju-voiceover").checked = !!R.voiceover;
      set("manju-krea2-unet", R.krea2_unet);
      set("manju-krea2-clip", R.krea2_clip);
      set("manju-krea2-vae", R.krea2_vae);
      this.renderStyle();
      this.renderRatio();
      this.syncBoostButtons();
    },

    /* 配置管理弹窗:导出 / 导入 / 恢复默认 */
    exportConfig() {
      const cfg = this.collectRenderConfig();
      const blob = new Blob([JSON.stringify(cfg, null, 2)], { type: "application/json" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `manju-render-${this.project ? this.project.split(/[\\/]/).pop() : "config"}.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 5000);
      this.closeModal();
    },
    importConfig(file) {
      if (!file) return;
      const msg = $("mc-import-msg");
      msg.textContent = "解析中…";
      const reader = new FileReader();
      reader.onload = () => {
        try {
          const cfg = JSON.parse(reader.result);
          this.applyRenderConfig(cfg);
          this.scheduleAutoSave();
          msg.textContent = "✅ 已导入并应用到表单(点「保存参数」写入项目)";
          msg.style.color = "";
          setTimeout(() => this.closeModal(), 1600);
        } catch (e) {
          msg.textContent = "❌ 配置解析失败: " + e.message;
          msg.style.color = "#e7000b";
        }
      };
      reader.readAsText(file);
    },
    resetConfig() {
      // 恢复默认 = 以当前值为默认基准:保留当前表单参数(自定义风格不参与默认),
      // 仅清空草稿并移除自定义风格标签——不再跳回出厂默认(768×1344/20步/写实)
      this.clearDraft();
      const kept = this.styleWords().filter((s) => STYLE_CN[s] !== undefined);
      this.style = kept.length ? kept.join("+") : "real";
      this.renderStyle();
      this.renderRatio();
      this.closeModal();
      const msg = $("manju-render-msg");
      if (msg) { msg.textContent = "✅ 已按当前参数为默认(自定义风格已移除;点「保存参数」写入项目)"; setTimeout(() => { msg.textContent = ""; }, 3000); }
    },

    mapInt(k) {
      const m = {
        width: "manju-width", height: "manju-height", fps: "manju-fps", steps: "manju-steps",
        turbo_steps: "manju-turbo", seed: "manju-seed", min_shot_seconds: "manju-minsec",
        max_shot_seconds: "manju-maxsec", shots_per_take: "manju-take",
      };
      const v = $(m[k]).value;
      return v === "" ? undefined : parseInt(v, 10);
    },
    mapStr(k) {
      const m = {
        comfy_url: "manju-comfy-url", neg_prompt: "manju-neg-prompt", unet_fl2va: "manju-unet-fl2va", unet_ref2va: "manju-unet-ref2va",
        clip: "manju-clip", vae_video: "manju-vae-video", vae_audio: "manju-vae-audio",
        z_image_unet: "manju-zimage-unet", z_image_clip: "manju-zimage-clip", z_image_vae: "manju-zimage-vae",
        turbo_lora: "manju-turbo-lora", turbo_lora_r2v: "manju-turbo-lora-r2v", animagine_ckpt: "manju-animagine",
        chapters: "manju-chapters", episode: "manju-episode", shots: "manju-only",
      };
      return $(m[k]).value.trim();
    },

    /* 运行中点击执行类按钮的统一拦截:弹醒目模态(静默 setErr 用户常以为"没反应"),
       告知当前正在跑什么阶段,引导停止或等待 */
    denyIfRunning() {
      if (!this.status.running) return false;
      const stageCN = { env: "项目体检", plan: "方案", assets: "资产", encode: "编码", render: "渲染", qc: "质检", assemble: "合成", upscale: "云端 2K", all: "一条龙" };
      const st = stageCN[this.status.currentStage] || this.status.currentStage || "运行中";
      this.openModal("⏳ 已有任务运行中",
        `<div class="manju-confirm">
          <p class="mc-q">当前有任务正在执行（${esc(st)} 阶段），不能同时启动新任务</p>
          <p class="mc-d">可能是崩溃恢复自动续跑的任务。请到「运行状态」查看进度；<br>如需改跑其他内容，先点红色「停止」按钮结束当前任务。</p>
          <div class="manju-row" style="justify-content:center;margin-top:16px">
            <button id="mc-ok-run" class="hrs-btn hrs-btn-primary">知道了</button>
          </div>
        </div>`);
      const ok = $("mc-ok-run");
      if (ok) ok.addEventListener("click", () => this.closeModal());
      return true;
    },

    /* 未选项目点击执行类按钮的统一拦截:弹醒目模态(静默 setErr 用户常以为"没反应") */
    denyNoProject() {
      if (this.project) return false;
      this.openModal("📌 请先选择项目",
        `<div class="manju-confirm">
          <p class="mc-q">请先在顶部「项目」下拉框选择一个项目，再执行该操作</p>
          <p class="mc-d">还没有项目？点左上「+ 新建项目」创建，或到「小说」页先创作一部作品。</p>
          <div class="manju-row" style="justify-content:center;margin-top:16px">
            <button id="mc-ok-proj" class="hrs-btn hrs-btn-primary">知道了</button>
          </div>
        </div>`);
      const ok = $("mc-ok-proj");
      if (ok) ok.addEventListener("click", () => this.closeModal());
      return true;
    },

    /* ---- 阶段执行 ---- */
    runStage(phase) {
      if (this.denyNoProject()) return;
      if (this.denyIfRunning()) return;
      if (phase === "all") { this.runAllCheck(); return; } // 一条龙:先检测已有流程,再决定从头 or 续跑
      this._agentRun = false; // 单阶段执行不是 AI 一条龙
      this.setErr("");
      this.logNote("(启动 " + phase + " ...)");
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: phase, only: this.only, novel: this.novel,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* 一条龙:先检测项目是否已有渲染流程产物(方案/镜头/成片)——有则询问「从头重渲 or 续跑」 */
    runAllCheck() {
      get("/api/manju/flow?config=" + encodeURIComponent(this.project)).then((f) => {
        if (!f || !f.hasFlow) { this.runAll(false); return; }
        this.openModal("🔄 检测到已有渲染流程",
          `<div class="manju-confirm">
            <p class="mc-q">项目已存在渲染产物（${esc(f.detail || "方案/镜头/成片")}）</p>
            <p class="mc-d">「从头渲染」清空旧产物（方案/镜头/成片/缓存，定妆照与场景图保留）重新生成；<br>「续跑」跳过已完成阶段，从上次中断处继续。</p>
            <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px">
              <button id="mc-all-fresh" class="hrs-btn hrs-btn-primary">🔄 从头渲染</button>
              <button id="mc-all-resume" class="hrs-btn">▶ 续跑</button>
            </div>
          </div>`);
        $("mc-all-fresh").addEventListener("click", () => { this.closeModal(); this.runAll(true); });
        $("mc-all-resume").addEventListener("click", () => { this.closeModal(); this.runAll(false); });
      }).catch((e) => this.setErr("检测渲染流程失败: " + e.message));
    },

    /* 一条龙本体:phase=all;fresh=true 从头重渲(清旧产物),false 续跑(幂等跳过已完成) */
    runAll(fresh) {
      if (this.denyNoProject()) return;
      if (this.denyIfRunning()) return;
      this._agentRun = false; // 普通一条龙(非 AI 一条龙)
      this.setErr("");
      this.logNote(fresh
        ? "(🔄 一条龙 · 从头渲染: 清空旧产物重新生成 ...)"
        : "(▶ 一条龙 · 续跑: 幂等跳过已完成阶段 ...)");
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: "all", only: this.only, novel: this.novel, fresh: fresh,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* 一键续跑:从上次中断处继续(= 一条龙,管线幂等自动跳过已完成阶段) */
    runResume() {
      if (this.denyNoProject()) return;
      if (this.denyIfRunning()) return;
      this._agentRun = false; // 续跑按普通一条龙
      this.setErr("");
      const last = this.status && this.status.currentStage ? this.status.currentStage : "";
      const hint = last ? "，上次中断于「" + last + "」阶段" : "";
      this.logNote("(▶ 续跑启动" + hint + "，幂等跳过已完成阶段 ...)");
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: "all", only: this.only, novel: this.novel,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* AI 一条龙:先询问是否让 Agent 深度分析小说并更新渲染配置(主要是风格),再走全流程 */
    runAgent() {
      if (this.denyNoProject()) return;
      if (this.denyIfRunning()) return;
      this.setErr("");
      this.openModal("🤖 AI 一条龙",
        `<div class="manju-confirm">
          <p class="mc-q">Agent 深度分析小说内容，自动更新渲染配置？</p>
          <p class="mc-d">「是」：Agent 分析本章节题材与节奏，自动更新<b>渲染风格</b>（支持组合，如 2.5D+水墨）与<b>渲染参数</b>（分辨率档位 / 草稿预审 / seed 策略 / 转场 / 长镜），随后走渲染流程；<br>「否」：按当前渲染配置直接走 AI 一条龙（剧本复核 → 渲染 → 审片判分 → 自动返工）。</p>
          <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px">
            <button id="mc-yes" class="hrs-btn hrs-btn-primary">是</button>
            <button id="mc-no" class="hrs-btn">否</button>
          </div>
        </div>`);
      $("mc-yes").addEventListener("click", () => { this.closeModal(); this.agentStyleThenRun(); });
      $("mc-no").addEventListener("click", () => { this.closeModal(); this.runAgentFlow(); });
    },

    /* 深度分析风格(共用):调 LLM 分析章节 → 更新渲染风格 → 返回结果(调用方决定后续动作) */
    styleAnalyze() {
      return post("/api/manju/agent/style", {
        config: this.project, chapters: this.chapters, episode: this.episode, novel: this.novel,
      }).then((r) => {
        this.style = r.style;
        this.renderStyle();
        this.scheduleAutoSave();
        return r;
      });
    },

    /* 「是」分支:深度分析 → 保留用户基底风格,补充题材元素 → 走 AI 一条龙 */
    agentStyleThenRun() {
      this.logNote("(🤖 深度分析小说内容，保留你选择的风格为基底，补充题材元素 ...)");
      this.styleAnalyze().then((r) => {
        const ps = r.params && Object.keys(r.params).length
          ? " · 参数: " + Object.entries(r.params).map(([k, v]) => k + "=" + v).join(" / ") : "";
        // r.style = 用户基底 + LLM 补充元素(后端合并去重);r.added = 新增元素
        const added = r.added ? "，新增: " + r.added : "";
        this.logNote("(🤖 风格已更新：" + this.styleLabel(r.style) + added +
          (r.reason ? "，" + r.reason : "") + ps + "，走渲染流程 ...");
        this.loadProject(); // 参数已写入 config,回填表单与 chips
        this.runAgentFlow();
      }).catch((e) => this.setErr("深度分析失败：" + e.message));
    },

    /* AI 一条龙本体:一条龙 + 智能体调度(剧本复核 → 渲染 → 审片判分 → 自动返工 → 例外升级) */
    runAgentFlow() {
      if (this.denyNoProject()) return;
      if (this.denyIfRunning()) return;
      this._agentRun = true; // 标记本次为 AI 一条龙(按钮动态文字仅此模式显示)
      this.setErr("");
      const draft = !!$("manju-draft-judge") && $("manju-draft-judge").checked;
      this.logNote("(🤖 AI 一条龙启动: 剧本复核 → 渲染 → 审片官判分 → 未达标自动返工 → 终审拍板"
        + (draft ? "，草稿预审: 审片轮缩放草稿 → 落定后全分辨率定稿" : "") + " ...)");
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: "all", only: this.only, novel: this.novel, agent: true,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* ---- 项目体检:全项诊断 + 一键修复 ---- */
    /* 项目体检(整合):智能体检 items(可一键修复) + 环境自检文本(ComfyUI/模型/依赖就绪性,只读) */
    openHealth() {
      if (this.denyNoProject()) return;
      this.openModal("🔍 项目体检", `<div class="mj-health">
        <div class="mj-health-load" id="mj-hp-load">🤖 智能体正在体检项目…</div>
        <div id="mj-hp-items"></div>
        <div class="mj-hp-env-sec">
          <div class="mj-hp-env-title">🧰 环境自检（ComfyUI / 模型 / 依赖就绪性，只读）</div>
          <pre id="mj-hp-env" class="manju-log">(运行中…)</pre>
        </div>
      </div>`, true);
      const gen = this._modalGen;
      post("/api/manju/env", { config: this.project }).then((r) => {
        if (gen !== this._modalGen) return;
        const el = $("mj-hp-env");
        if (el) el.textContent = (r.output || "") + "\n[exit " + r.exitCode + "]";
      }).catch((e) => {
        if (gen !== this._modalGen) return;
        const el = $("mj-hp-env");
        if (el) el.textContent = "错误: " + e.message;
      });
      get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r) => {
        if (gen !== this._modalGen) return;
        const el = $("mj-hp-items");
        if (!el) return;
        el.innerHTML = this._healthItemsHTML(r.items || []);
        this._bindHealthFix();
        const loadEl = $("mj-hp-load");
        if (loadEl) loadEl.remove();
      }).catch(() => {});
    },

    /* 智能体检 items → HTML(徽章计数 + 逐项状态 + 可修复按钮) */
    _healthItemsHTML(items) {
      const n = { ok: 0, warn: 0, bad: 0 };
      items.forEach((it) => n[it.status]++);
      const ic = { ok: "✅", warn: "⚠️", bad: "❌" };
      return `<div class="mj-health-head">
        <span class="mj-hh-title">🤖 智能体检</span>
        <span class="mj-hh-pill bad">❌ 异常 ${n.bad}</span>
        <span class="mj-hh-pill warn">⚠️ 建议 ${n.warn}</span>
        <span class="mj-hh-pill ok">✅ 正常 ${n.ok}</span>
      </div>
      <div class="mj-health-items">
        ${items.map((it) => `
        <div class="mj-health-item ${it.status}">
          <span class="mj-hi-ic">${ic[it.status] || "•"}</span>
          <div class="mj-hi-body">
            <div class="mj-hi-top">
              <b>${esc(it.label)}</b>
              ${it.fixable ? `<button class="hrs-btn hrs-btn-primary mj-hi-fix" data-fix="${esc(it.key)}">一键修复</button>` : ""}
            </div>
            <div class="mj-hi-detail">${esc(it.detail)}</div>
            ${(!it.fixable && it.fixHint) ? `<div class="mj-hi-hint">💡 ${esc(it.fixHint)}</div>` : ""}
          </div>
        </div>`).join("")}
      </div>
      <div class="mj-health-foot">体检为本地秒查(不调用模型);「一键修复」直接写回 config.json 渲染配置。</div>`;
    },

    /* 绑定当前弹窗内的「一键修复」按钮 */
    _bindHealthFix() {
      if (!this._modalEl) return;
      this._modalEl.querySelectorAll(".mj-hi-fix").forEach((b) =>
        b.addEventListener("click", () => this.fixHealth(b.dataset.fix, b))
      );
    },

    /* 体检预热(非弹窗):刷新右栏 Agent 面板建议横幅 */
    loadHealth(showModal, gen) {
      const render = (items) => {
        // 代次守卫:弹窗已被关闭/切换 → 放弃渲染,不弹回
        if (gen !== undefined && gen !== this._modalGen) return;
        if (showModal) {
          this.rerenderModal("🔍 项目体检", this._healthItemsHTML(items), true);
        } else {
          this._health = items;
          this._healthFix = items.filter((it) => it.fixable && it.status !== "ok");
          this.renderAgent();
        }
        this._health = items;
        this._healthFix = items.filter((it) => it.fixable && it.status !== "ok");
        this._bindHealthFix();
      };
      if (showModal) {
        get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r) => render(r.items || [])).catch((e) => {
          if (gen !== undefined && gen !== this._modalGen) return;
          this.rerenderModal("🔍 项目体检", '<div class="mj-health"><div class="mj-health-load">体检失败: ' + esc(e.message) + '</div></div>', true);
        });
      } else {
        get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r) => render(r.items || [])).catch(() => {});
      }
    },
    fixHealth(key, btn) {
      if (!this.project) return;
      const gen = this._modalGen;   // 修复期间弹窗被关闭 → 不再刷新弹窗内容
      if (btn) { btn.disabled = true; btn.textContent = "修复中…"; }
      post("/api/manju/agent/health/fix", { config: this.project, key }).then((r) => {
        if (gen !== this._modalGen) return; // 弹窗已关闭:不再刷新
        // 只刷新弹窗内的智能体检区(环境自检区保留)
        get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r2) => {
          if (gen !== this._modalGen) return;
          const el = $("mj-hp-items");
          if (el) {
            el.innerHTML = this._healthItemsHTML(r2.items || []);
            this._bindHealthFix();
          }
        }).catch(() => {});
        this.loadProject();
      }).catch((e) => {
        if (btn) { btn.disabled = false; btn.textContent = "一键修复"; }
        this.setErr("修复失败：" + e.message);
      });
    },
    fixAllHealth() {
      return get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r) => {
        const need = (r.items || []).filter((it) => it.fixable && it.status !== "ok");
        if (!need.length) return { fixed: [] };
        const chain = need.reduce((p, it) => p.then(() => post("/api/manju/agent/health/fix", { config: this.project, key: it.key }).catch(() => {})), Promise.resolve());
        return chain.then(() => ({ fixed: need.map((x) => x.label) }));
      });
    },

    /* ---- Agent 对话:已迁至底部 AI 助手气泡对话框(App.mascotChat/aiReply) ---- */

    /* ---- 智能体:审片报告面板 + 升级处理 ---- */
    resolveEsc(shot, action) {
      if (!this.project) return;
      post("/api/manju/agent/resolve", {
        config: this.project, episode: this.episode, shot, action,
      }).then(() => this.poll()).catch((e) => this.setErr(e.message));
    },

    /* 点分数块 = 重审该镜(需已配置视觉模型;结论弹窗展示) */
    rejudge(shot) {
      if (!this.project) return;
      if (!this.agent || !this.agent.visionModel) { this.setErr("未配置视觉模型(设置 → 智能体)"); return; }
      if (this.status.running) { this.setErr("任务运行中，结束后再重审"); return; }
      const panel = $("ai-ag-panel");
      if (panel) panel.dataset.busy = String(shot);
      post("/api/manju/agent/judge", {
        config: this.project, episode: this.episode, shot,
      }).then((r) => {
        if (panel) delete panel.dataset.busy;
        if (!r.ok) { this.setErr(r.error || "重审失败"); return; }
        this.showJudgment(shot, r.judgment);
        this.poll();
      }).catch((e) => { if (panel) delete panel.dataset.busy; this.setErr(e.message); });
    },

    showJudgment(shot, j) {
      j = j || {};
      const dims = j.dimensions || {};
      const dimRows = AGENT_DIMS.map(([k, name, w]) => {
        const v = dims[k];
        if (v === undefined || v === null) return "";
        const pct = Math.max(0, Math.min(100, Math.round(v)));
        const cls = pct >= 75 ? "ok" : pct >= 60 ? "mid" : "bad";
        return `<div class="mj-jd-row"><span class="mj-jd-name">${name} <i>${w}%</i></span>
          <span class="mj-jd-bar"><b class="${cls}" style="width:${pct}%"></b></span><span class="mj-jd-val ${cls}">${pct}</span></div>`;
      }).join("");
      const statusCN = { pass: "✅ 通过", fixed: "✅ 返工后通过", failed: "❌ 未达标", accepted: "☑️ 已人工接受", pending: "⏳ 待审" }[j.status] || j.status;
      this.openModal(`🤖 审片详情 · 镜头 ${shot}`,
        `<div class="manju-form manju-judge">
          <div class="mj-jd-score ${j.score >= 75 ? "ok" : j.score >= 60 ? "mid" : "bad"}">${Math.round(j.score || 0)}<small> / 100</small></div>
          <div class="mj-jd-status">${statusCN}${j.retries ? ` · 已返工 ${j.retries} 轮` : ""}${j.fallback ? " · ⚠️ 有维度缺失按 70 兜底" : ""}</div>
          <div class="mj-jd-dims">${dimRows}</div>
          ${(j.qcFlags || []).length ? `<div class="mj-jd-sec">机械质检：${esc(j.qcFlags.join("、"))}</div>` : ""}
          ${(j.issues || []).length ? `<div class="mj-jd-sec">问题清单：<ul>${j.issues.map((x) => `<li>${esc(x)}</li>`).join("")}</ul></div>` : ""}
          ${j.suggestion ? `<div class="mj-jd-sec">修复建议：${esc(j.suggestion)}</div>` : ""}
          ${j.error ? `<div class="mj-jd-sec" style="color:#d25c4d">审片失败：${esc(j.error)}</div>` : ""}
        </div>`, true);
    },

    renderAgent() {
      // Agent 状态卡(审片/档案/建议)渲染进底部 AI 助手对话框顶部;聊天走对话框输入区
      const el = $("ai-ag-panel");
      if (!el) return;
      const ag = this.agent;
      const mem = (ag && ag.memory) || {};
      const lastErr = ag && ag.lastError;
      const trend = mem.scoreTrend || [];
      const lastPt = trend[trend.length - 1];
      const issueTop = Object.entries(mem.issueStats || {}).sort((a, b) => b[1] - a[1]).slice(0, 3);
      const styleChoices = mem.styleChoices || [];
      const styleLast = styleChoices[styleChoices.length - 1];
      const memLines = [];
      if (mem.runCount) memLines.push(`🏃 运行 ${mem.runCount} 次 · 审片 ${mem.judgedShots || 0} 镜 · 返工 ${mem.reworkCount || 0} 次`);
      // token 用量记账(文本+视觉全部外部调用,分模型累计)
      const st = (ag && ag.llmStats) || {};
      if (st.calls) {
        const wan = (n) => n >= 10000 ? (n / 10000).toFixed(1) + " 万" : String(n);
        const models = Object.entries(st.byModel || {}).sort((a, b) => (b[1].totalTokens || 0) - (a[1].totalTokens || 0))
          .map(([m, e]) => `${m} ${e.calls}次/${wan(e.totalTokens || 0)}`).join(" · ");
        memLines.push(`🪙 累计 ${st.calls} 次调用 / ${wan(st.totalTokens || 0)} tokens${models ? "(" + models + ")" : ""}`);
      }
      if (lastPt) memLines.push(`📈 最近审片均分 ${Math.round(lastPt.score)} 分(${lastPt.count} 镜)`);
      if (issueTop.length) memLines.push(`🔁 高频问题: ${issueTop.map(([k, v]) => `${k.length > 14 ? k.slice(0, 14) + "…" : k}×${v}`).join(" / ")}`);
      if (styleLast) memLines.push(`🎨 最近风格: ${this.styleLabel(styleLast.old)} → ${this.styleLabel(styleLast.new)}`);
      // 主动建议横幅:点 × 后本会话不再显示(loadProject 时重置)
      const needFix = this._healthTipDismissed ? [] : (this._healthFix || []).filter((it) => it.status !== "ok");
      const hasData = !!(needFix.length || (ag && (ag.configured || (ag.shots || []).length || (ag.escalations || []).length ||
        memLines.length || lastErr)));
      if (!hasData) {
        el.classList.add("hidden");
        return;
      }
      el.classList.remove("hidden");
      // 状态卡主体(轮询重建,内容比对跳过——点击/滚动不丢)
      const body = el;
      const shots = (ag && ag.shots) || [];
      const pass = shots.filter((s) => s.status === "pass" || s.status === "fixed").length;
      const failed = shots.filter((s) => s.status === "failed").length;
      const escs = (ag && ag.escalations) || [];
      const chips = shots.map((s) => {
        const cls = s.status === "pass" ? "ok" : s.status === "fixed" ? "ok fixed" : s.status === "accepted" ? "acc" : s.status === "pending" ? "pend" : "bad";
        const dims = s.dimensions || {};
        const tipParts = [`镜${s.id} · ${Math.round(s.score || 0)}分`];
        AGENT_DIMS.forEach(([k, name]) => { if (dims[k] !== undefined) tipParts.push(`${name} ${Math.round(dims[k])}`); });
        (s.issues || []).forEach((x) => tipParts.push("· " + x));
        if (s.arbiter) tipParts.push("🤖 " + s.arbiter);
        if (s.retries) tipParts.push(`返工${s.retries}轮`);
        const busy = el.dataset.busy === String(s.id) ? " busy" : "";
        return `<span class="mj-ag-chip ${cls}${busy}" data-shot="${s.id}" title="${esc(tipParts.join("\n"))}">${s.id}<b>${Math.round(s.score || 0)}</b>${s.retries ? `<i>r${s.retries}</i>` : ""}</span>`;
      }).join("");
      const escCards = escs.map((e) => `
        <div class="mj-ag-esc">
          <div class="mj-ag-esc-info">镜 ${e.shot} · ${Math.round(e.score || 0)}分 · ${esc(e.reason || "未达标")}</div>
          <div class="mj-ag-esc-acts">
            <button class="hrs-btn hrs-btn-primary" data-esc-retry="${e.shot}">重试此镜</button>
            <button class="hrs-btn" data-esc-ignore="${e.shot}">忽略</button>
          </div>
        </div>`).join("");
      const rv = ag && ag.planReview;
      const review = rv ? `<div class="mj-ag-review ${(rv.score || 0) < 60 ? "low" : ""}">📖 剧本复核 ${Math.round(rv.score || 0)} 分${(rv.issues || []).length ? " · " + esc(rv.issues[0]) : ""}</div>` : "";
      const errCard = (lastErr && lastErr.stage && !(this.status && this.status.running)) ? `
        <div class="mj-ag-err"><b>❌ 上次中断于「${esc(lastErr.stage)}」· 🤖 ${esc(lastErr.diagnosis || "未知错误")}</b>
        <span>${esc(lastErr.suggestion || "")}</span></div>` : "";
      const memCard = memLines.length ? `
        <div class="mj-ag-mem"><div class="mj-ag-mem-t">🧠 学习档案</div>
        <div class="mj-ag-mem-l">${memLines.map((l) => `<div>${esc(l)}</div>`).join("")}</div></div>` : "";
      const tipCard = needFix.length ? `
        <div class="mj-ag-tip" id="mj-ag-tip">
          <span class="mj-tip-t">💡 体检发现 ${needFix.length} 项可优化: ${needFix.map((x) => esc(x.label)).join("、")}</span>
          <span class="mj-tip-acts"><button class="hrs-btn hrs-btn-primary" id="mj-tip-open">查看并修复</button><button class="hrs-btn" id="mj-tip-x">×</button></span>
        </div>` : "";
      const html = `
        ${errCard}
        ${memCard}
        ${tipCard}
        <div class="mj-ag-head">
          <span class="mj-ag-title">🤖 审片报告</span>
          <span class="mj-ag-meta">${ag ? (esc(ag.visionModel) || "未配置视觉模型") : "未运行"}${ag ? " · 及格 " + Math.round(ag.passScore || 75) + " · 返工≤" + (ag.maxRetries == null ? 2 : ag.maxRetries) : ""}</span>
        </div>
        ${review}
        <div class="mj-ag-summary">${pass} 通过 / ${failed} 待处理${escs.length ? ` / <b class="mj-ag-esc-n">${escs.length} 待拍板</b>` : ""}</div>
        <div class="mj-ag-chips">${chips}</div>
        ${escs.length ? `<div class="mj-ag-escs">${escCards}</div>` : ""}
        ${ag && !ag.visionModel ? `<div class="mj-ag-hint">⚙️ 设置 → 智能体：填写视觉模型后启用逐镜判分（未配置时仅机械质检与升级）</div>` : ""}`;
      // 内容比对:无变化跳过重建(2s 轮询不再销毁按钮/重置滚动,点击事件不丢失)
      if (body.dataset.lastHtml === html) return;
      body.dataset.lastHtml = html;
      body.innerHTML = html;
      body.querySelectorAll("[data-esc-retry]").forEach((b) => b.addEventListener("click", () => this.resolveEsc(parseInt(b.dataset.escRetry, 10), "retry")));
      body.querySelectorAll("[data-esc-ignore]").forEach((b) => b.addEventListener("click", () => this.resolveEsc(parseInt(b.dataset.escIgnore, 10), "ignore")));
      body.querySelectorAll(".mj-ag-chip").forEach((c) => c.addEventListener("click", () => this.rejudge(parseInt(c.dataset.shot, 10))));
      const open = body.querySelector("#mj-tip-open");
      if (open) open.addEventListener("click", () => this.openHealth());
      const x = body.querySelector("#mj-tip-x");
      if (x) x.addEventListener("click", () => { this._healthTipDismissed = true; this.renderAgent(); });
    },


    stop() {
      if (this.stopping) return;
      this.stopping = true;
      const btn = $("manju-stop");
      btn.textContent = "停止中…";
      post("/api/manju/kill", {}).then((r) => {
        btn.textContent = "停止";
        this.stopping = false;
        this.renderLog((this._logSrc || "") + "\n⏹ 已发送停止，正在结束进程树…");
        this.poll();
      }).catch((e) => {
        btn.textContent = "停止";
        this.stopping = false;
        this.setErr(e.message);
      });
    },

    /* ---- 状态轮询 ---- */
    poll() {
      if (!document.getElementById("view-manju").classList.contains("is-active")) return;
      // 系统监测 + ComfyUI 状态灯不依赖项目选择:无项目也必须刷新(否则"检测中…"永不更新)
      this.pollSysmon();
      if (!this.project) {
        // 无项目:显示空闲,不请求后端旧状态(避免 run.state.json 残留的旧项目状态污染)
        this.status = { running: false, stage: "", currentStage: "", stageIdx: -1, shotCur: 0, shotTotal: 0, progress: 0, done: false, rc: null, stopped: false, elapsedSec: 0, logTail: "" };
        this.renderStatus();
        return;
      }
      const q = "?config=" + encodeURIComponent(this.project);
      const reqProject = this.project; // 代次守卫:切项目后旧响应直接丢弃,防串项目
      get("/api/manju/status" + q).then((s) => {
        if (reqProject !== this.project) return;
        this.status = s;
        this.agent = s.agent || null;
        this.renderStatus();
        if (!s.running && s.done) this.refreshOutputs();
        else if (s.running) this.refreshOutputs(); // 运行中也刷新产物(镜头/定稿实时出现),完成后 badge 即时显示成片
        this.reportMascot(s);
      }).catch(() => {});
    },

    /* 本机系统状态(CPU/内存/GPU 占用+温度):每轮轮询顺带刷新(2s),不单独起计时器 */
    pollSysmon() {
      const wrap = $("manju-sysmon");
      if (!wrap) return;
      get("/api/stats").then((r) => {
        const c = r.cpu || {}, m = r.mem || {}, g = r.gpu || {};
        const set = (k, v, cls) => {
          const b = wrap.querySelector(`[data-k="${k}"] b`);
          if (b) { b.textContent = v; b.className = cls || ""; }
        };
        const setT = (k, v, cls) => {
          const em = wrap.querySelector(`[data-k="${k}"] em`);
          if (em) { em.textContent = v; em.className = cls || ""; }
        };
        /* 底部进度条:负载百分比 → 填充宽度 + 负载色(GPU 无数据保持空) */
        const setBar = (k, v, present) => {
          const fill = wrap.querySelector(`[data-k="${k}"] .ms-fill`);
          if (!fill) return;
          if (!present || typeof v !== "number") { fill.style.width = "0%"; fill.className = "ms-fill"; return; }
          fill.style.width = Math.max(0, Math.min(100, v)) + "%";
          fill.className = "ms-fill " + (v >= 90 ? "crit" : v >= 70 ? "hot" : "");
        };
        const pct = (v) => (typeof v === "number" ? Math.round(v) + "%" : "--");
        const temp = (v, has) => (has && typeof v === "number" ? Math.round(v) + "℃" : "N/A");
        set("cpu", pct(c.usage), c.usage >= 90 ? "crit" : c.usage >= 70 ? "hot" : "");
        setT("cpu", temp(c.temp, c.hasTemp), c.temp >= 85 ? "crit" : c.temp >= 70 ? "hot" : "");
        setBar("cpu", c.usage, true);
        set("mem", pct(m.percent), m.percent >= 90 ? "crit" : m.percent >= 75 ? "hot" : "");
        // 内存副值显示已用/总量(比恒 N/A 的内存温度有用);温度并入 title
        const memIt = wrap.querySelector('[data-k="mem"]');
        setT("mem", m.used && m.total ? m.used + "/" + m.total : "N/A");
        if (memIt) window.NilixSetTip ? NilixSetTip(memIt, "内存 " + pct(m.percent) + (m.hasTemp ? " · " + Math.round(m.temp) + "℃" : "") + (m.available ? " · 可用 " + m.available : "")) : (memIt.title = "内存");
        setBar("mem", m.percent, true);
        // GPU:优先显示显存使用率(H3 渲染显存常满、算力利用率波动无参考性);无显卡显示 N/A
        const gpuPct = g.present ? (g.memPercent != null && g.memPercent > 0 ? g.memPercent : g.usage) : null;
        set("gpu", gpuPct != null ? Math.round(gpuPct) + "%" : "N/A", gpuPct != null ? (gpuPct >= 90 ? "crit" : gpuPct >= 70 ? "hot" : "") : "");
        setT("gpu", g.present ? temp(g.temp, g.temp > 0) : "N/A", g.temp >= 85 ? "crit" : g.temp >= 70 ? "hot" : "");
        setBar("gpu", gpuPct != null ? gpuPct : 0, g.present);
        // 风扇转速(雷神同源 EC 通道;需管理员,未解锁时留空)+ GPU 卡 title 补显存/算力明细
        const hw = r.hw || {};
        const fanTxt = (v) => (hw.ok && v > 0 ? Math.round(v) + "%" : "");   // EC 风扇值为占空比 0-100
        ["cpu", "gpu"].forEach((k) => {
          const el = wrap.querySelector(`[data-k="${k}"] .ms-fan`);
          if (el) el.textContent = fanTxt(k === "cpu" ? hw.cpuFan : hw.gpuFan);
        });
        const gpuIt = wrap.querySelector('[data-k="gpu"]');
        if (gpuIt && g.present) {
          NilixSetTip(gpuIt, "GPU 显存 " + g.memUsed + "/" + g.memTotal + "(" + Math.round(g.memPercent || 0) + "%)"
            + " · 算力 " + Math.round(g.usage) + "% · 温度 " + temp(g.temp, g.temp > 0)
            + (g.sharedUsed ? " · 共享 " + g.sharedUsed : ""));
        }
        const cpuIt = wrap.querySelector('[data-k="cpu"]');
        if (cpuIt) {
          const cores = (c.cores || []).length;
          NilixSetTip(cpuIt, "CPU " + pct(c.usage) + " · " + cores + " 线程" + (c.hasTemp ? " · 核心 " + Math.round(c.temp) + "℃" : "")
            + (hw.ok && hw.cpuFan > 0 ? " · 风扇 " + hw.cpuFan + "%" : ""));
        }
        // 温度不可用时给出可行动的提示:CPU 核心温度唯一来源 LHM 需要管理员权限(2026-08-26)
        const cpuEm = wrap.querySelector('[data-k="cpu"] em');
        if (cpuEm) window.NilixSetTip ? NilixSetTip(cpuEm, c.hasTemp ? "" : "CPU 核心温度需以管理员身份运行 NiliX 才能读取(灵动岛「设备控制」可一键提权)") : (cpuEm.title = "");
        // ComfyUI 服务状态灯(视频管理页顶部:ComfyUI · 运行中/已停止)
        const cfy = r.comfy || {};
        const cDot = document.querySelector("#manju-cfy-status .hrs-dot");
        const cTxt = $("manju-cfy-txt");
        if (cDot) cDot.className = "hrs-dot " + (cfy.online ? "on" : "off");
        if (cTxt) cTxt.textContent = "ComfyUI · " + (cfy.online ? "运行中" : "已停止");
      }).catch(() => {});
    },

    /* 阶段英文 key → 中文名(运行状态/气泡共用;日志时间轴另有局部表) */
    stageCN(k) {
      const map = { env: "项目体检", plan: "方案", assets: "资产", encode: "编码", render: "渲染", qc: "质检", assemble: "合成", upscale: "云端 2K" };
      return map[k] || k || "准备";
    },

    /* 悬浮助手云朵 → 桌宠已删除(2026-08-24 用户要求):保留空实现避免调用点报错 */
    reportMascot(s) {
    },
    renderStatus() {
      const s = this.status;
      const dot = document.querySelector("#manju-status .hrs-dot");
      const txt = $("manju-txt");
      const badge = $("manju-badge");
      const stopBtn = $("manju-stop");
      const log = $("manju-log");

      if (s.running) {
        const stageName = this.stageCN(s.stage || s.currentStage || "");
        dot.className = "hrs-dot on";
        txt.textContent = "运行中 · " + stageName;
        // AI 一条龙运行中:badge 附审片进度(已判/总数,实时可感知智能体闭环)
        let agentNote = "";
        if (this.agent && (this.agent.shots || []).length) {
          const judged = this.agent.shots.filter((x) => x.status !== "pending").length;
          agentNote = ` · 审片 ${judged}/${this.agent.shots.length}`;
        }
        badge.className = "manju-badge manju-badge-run";
        badge.innerHTML = '<span class="manju-spinner"></span><span>' + stageName + " 运行中 " + fmtTime(s.elapsedSec) + agentNote + "</span>";
        stopBtn.disabled = false;
      } else {
        dot.className = "hrs-dot off";
        txt.textContent = "空闲";
        stopBtn.disabled = true;
        if (s.stopped) {
          badge.className = "manju-badge manju-badge-stop";
          badge.textContent = "⏹ 已手动停止 · 总耗时 " + fmtTime(s.elapsedSec);
        } else if (s.rc !== null && s.rc !== undefined) {
          if (s.rc === 0) {
            // 成片就绪:badge 附「查看成片」直达入口(从产物区取本集成片路径)
            const finalP = this.finalVideoPath();
            if (finalP) {
              badge.className = "manju-badge manju-badge-ok manju-badge-done";
              badge.innerHTML = '✅ 成片已生成 · ' + fmtTime(s.elapsedSec) +
                ' <button id="manju-badge-final" class="manju-badge-btn" title="播放成片">▶ 查看成片</button>';
            } else {
              badge.className = "manju-badge manju-badge-ok";
              badge.textContent = "✅ 上次任务成功 · 总耗时 " + fmtTime(s.elapsedSec);
            }
          } else { badge.className = "manju-badge manju-badge-err"; badge.textContent = "❌ 上次任务 rc=" + s.rc + " · 总耗时 " + fmtTime(s.elapsedSec); }
        } else {
          badge.className = "manju-badge manju-badge-idle";
          badge.textContent = "空闲";
        }
      }
      // 2026-08-24 修复:日志节点反复隐藏/显示——根因是 logFull(全文)与 logTail(尾部截断)
      // 两个内容不同的源,logFull 无变化时 fallback logTail 会造成每 2 秒在两者间交替重建,
      // logTail 视图里前面的已通过阶段节点整段消失,下一轮 logFull 又回来 → 节点闪跳。
      // 修复:logFull 存在时只用 logFull(单源),logTail 仅作 logFull 缺失/为空时的兜底。
      if (s.logFull !== undefined && s.logFull !== "") {
        if (s.logFull !== log.dataset.last) this.renderLog(s.logFull);
      } else if (s.logTail !== undefined && s.logTail !== log.dataset.last) {
        this.renderLog(s.logTail || "");
      }
      this.renderInterruptTip();
      this.renderProgress();
      this.renderFlow();
      this.renderStageButtons();
      this.renderAgent();
      // badge「查看成片」直达播放
      const fb = $("manju-badge-final");
      if (fb) fb.addEventListener("click", (e) => {
        e.stopPropagation();
        const p = this.finalVideoPath();
        if (p) this.previewVideo(p, "成片");
      });
    },

    /* 当前集/最近集的成片路径(产物区已加载时),无则空串 */
    finalVideoPath() {
      if (!this.outputs || !this.outputs.episodes || !this.outputs.episodes.length) return "";
      const eps = this.outputs.episodes;
      const cur = this.episode || "";
      let hit = eps.find((e) => e.episode === cur) || eps[eps.length - 1];
      if (hit && hit.final) return hit.final.path;
      // 单集时可能 final 在顶层(兼容旧结构)
      if (this.outputs.final) return this.outputs.final.path;
      return "";
    },

    /* 阶段按钮运行态:当前阶段高亮 + 已完成阶段打 ✓(与流程图同步,直观点选入口) */
    renderStageButtons() {
      const s = this.status || {};
      const cur = s.currentStage || "";
      const running = !!s.running;
      // 2026-08-24 修复:done=失败才标 fail;成功(rc===0)当前阶段标 done(不是 fail)
      const failed = s.done && s.rc !== null && s.rc !== undefined && s.rc !== 0;
      const STAGE_ORDER = ["env", "plan", "assets", "encode", "render", "qc", "assemble"];
      let curIdx = STAGE_ORDER.indexOf(cur);
      document.querySelectorAll("#view-manju [data-phase]").forEach((b) => {
        const ph = b.dataset.phase;
        b.classList.remove("mj-ph-run", "mj-ph-done", "mj-ph-fail");
        if (ph === "all") return; // 一条龙按钮不加阶段态
        const idx = STAGE_ORDER.indexOf(ph);
        if (idx < 0) return;
        if (curIdx >= 0 && idx === curIdx) {
          if (running) b.classList.add("mj-ph-run");
          else if (failed) b.classList.add("mj-ph-fail");
          else b.classList.add("mj-ph-done");
        } else if (curIdx >= 0 && idx < curIdx) {
          b.classList.add("mj-ph-done");
        }
      });
      const agentBtn = $("manju-agent-run");
      // 仅 AI 一条龙(agent 模式)运行时按钮显示动态文字;普通一条龙/单阶段/续跑保持原名
      if (agentBtn && running && this._agentRun) {
        const st = s.currentStage || s.stage || "";
        agentBtn.textContent = st === "qc" ? "🤖 AI 一条龙 · 审片中…" :
          (st === "render" ? "🤖 AI 一条龙 · 渲染中…" : "🤖 AI 一条龙 · 运行中…");
      } else if (agentBtn && agentBtn.textContent !== "🤖 AI 一条龙") {
        agentBtn.textContent = "🤖 AI 一条龙";
      }
    },

    /* 实时进度条:运行中显示阶段+镜头进度,结束后收成 100% 或隐藏 */
    renderProgress() {
      const s = this.status || {};
      const wrap = $("manju-progress-wrap");
      const bar = $("manju-progress-bar");
      const txt = $("manju-progress-text");
      const pct = $("manju-progress-pct");
      if (!wrap || !bar) return;
      const stageName = (FLOW.find((f) => f.key === s.currentStage) || {}).name || s.currentStage || "";
      let label = "", val = 0;
      if (s.running) {
        val = Math.round(s.progress || 0);
        if (s.shotTotal > 0 && (s.currentStage === "render" || s.currentStage === "encode")) {
          label = `${stageName} · 镜头 ${s.shotCur}/${s.shotTotal}`;
        } else {
          const idx = s.stageIdx >= 0 ? s.stageIdx + 1 : 1;
          label = `${stageName || "准备"} · 阶段 ${idx}/${s.stageTotal}`;
        }
      } else if (s.done && s.rc === 0) {
        val = 100; label = "已完成 100% · 总耗时 " + fmtTime(s.elapsedSec);
      } else if (s.done && s.rc !== 0) {
        val = 100; label = "已结束（失败）· 总耗时 " + fmtTime(s.elapsedSec);
      } else {
        wrap.hidden = true; return;
      }
      wrap.hidden = false;
      bar.style.width = Math.max(0, Math.min(100, val)) + "%";
      txt.textContent = label;
      pct.textContent = Math.round(val) + "%";
    },

    /* 管线流程图(横向步进) */
    renderFlow() {
      const el = $("manju-flow");
      if (!el) return;
      const s = this.status || {};
      const cur = s.currentStage || "";
      let curIdx = FLOW.findIndex((f) => f.key === cur);
      const running = !!s.running;
      const failed = s.done && s.rc !== null && s.rc !== undefined && s.rc !== 0;
      // 2026-08-24 用户反馈:日志顶部闪白——状态未变化时不重建(每 2 秒 poll 全量 innerHTML 重建会闪)
      const sig = [cur, running, failed, s.done].join("|");
      if (this._flowSig === sig) return;
      this._flowSig = sig;

      el.innerHTML = `<div class="manju-flow">` + FLOW.map((f, i) => {
        let cls = "pending", mark = "";
        // 2026-08-24 修复:任务完成时只标「实际跑到 currentStage」之前的阶段为 done,
        // 之后未执行的阶段保持 pending——此前 !running&&done&&rc===0 把全部节点标 ✓,
        // 单阶段运行(如只跑 plan)时资产/渲染/合成被误标为已完成(节点与实际流程不符)。
        const ranUpTo = curIdx >= 0 ? curIdx : (s.done && s.rc === 0 ? FLOW.length - 1 : -1);
        if (i < ranUpTo) { cls = "done"; mark = "✓"; }
        else if (i === ranUpTo) {
          if (running) cls = "current";
          else if (failed) { cls = "failed"; mark = "✗"; }
          else if (s.done) { cls = "done"; mark = "✓"; }
        }
        return `<div class="manju-flow-step ${cls}">
          <span class="manju-flow-dot"></span>
          <span class="manju-flow-name">${f.name}</span>
          <span class="manju-flow-mark">${mark}</span>
        </div>`;
      }).join("") + `</div>`;
    },

    /* ---- 产物 ---- */
    refreshOutputs() {
      if (!this.project) return;
      // 产物按集区分:一次拉回全部集(人物/场景为全项目共享)
      // 代次守卫:切项目/改集号后旧响应直接丢弃,防串数据
      const reqProject = this.project, reqEp = this.episode;
      get("/api/manju/outputs?config=" + encodeURIComponent(this.project)).then((r) => {
        if (reqProject !== this.project) return;
        this.outputs = r;
        this.renderOutputs();
        this.renderGacha();
      }).catch(() => {});
      this.loadPlan();
    },

    /* ---- 角色抽卡 ---- */
    loadPlan(cb) {
      if (!this.project) return;
      const reqProject = this.project, reqEp = this.episode;
      get("/api/manju/plan?config=" + encodeURIComponent(this.project) + "&episode=" + encodeURIComponent(this.episode)).then((r) => {
        if (reqProject !== this.project || reqEp !== this.episode) return;
        this.plan = r;
        // 合并磁盘抽卡历史(后端按视图分组扫描 _gacha 候选,跨会话保留);本会话已抽的不覆盖
        (r.characters || []).forEach((c) => {
          if (!this.gacha[c.id]) this.gacha[c.id] = { views: {}, cur: "" };
          (c.views || []).forEach((v) => {
            const key = v.view || "";
            if (this.gacha[c.id].views[key]) return;
            const list = (v.gacha || []).map((g) => ({ image: g.image, seed: g.seed }));
            this.gacha[c.id].views[key] = { list, sel: list.length ? 0 : -1, ready: !!v.ready };
          });
          // 旧结构迁移:单视图历史并入主视图(front)
          if (!this.gacha[c.id].views[""] && c.gacha && c.gacha.length) {
            const list = c.gacha.map((g) => ({ image: g.image, seed: g.seed }));
            this.gacha[c.id].views[""] = { list, sel: list.length ? 0 : -1, ready: !!this.officialChar(c.id) };
          }
          if (!this.gacha[c.id].views[""]) this.gacha[c.id].views[""] = { list: [], sel: -1, ready: !!this.officialChar(c.id) };
        });
        if (cb) cb(); else this.renderGacha();
      }).catch(() => {});
    },

    /* 主页角色抽卡:只显示采纳后的定妆照 + 信息 */
    toggleGacha() {
      const body = $("manju-gacha");
      const btn = $("manju-gacha-fold");
      if (!body) return;
      const folded = body.classList.toggle("hidden");
      if (btn) btn.textContent = folded ? "▸" : "▾";
    },

    renderGacha() {
      const el = $("manju-gacha");
      if (!el) return;
      const adopted = ((this.outputs && this.outputs.characters) || [])
        .filter((c) => !/(_face|_full|_side|_detail|_board)\./i.test(c.name || ""));
      const fileUrl = (p) => "/api/fs/file?path=" + encodeURIComponent(p);
      let html = "";
      if (!adopted.length) {
        html = '<div class="manju-empty">尚无采纳的定妆照</div>';
      } else {
        html = `<div class="manju-thumbs">` + adopted.map((c) =>
          `<div class="manju-thumb" data-img="${esc(c.path)}" data-name="${esc(c.name)}" title="${esc(c.name)}">
            <img src="${fileUrl(c.path)}" loading="lazy" alt="">
            <span class="manju-thumb-name">${esc(c.name)}</span>
          </div>`
        ).join("") + `</div>`;
      }
      html += `<div class="manju-row" style="margin-top:10px">
        <button id="manju-gacha-open" class="hrs-btn hrs-btn-primary">🎲 抽卡 / 管理角色</button>
      </div>`;
      el.innerHTML = html;
      el.querySelectorAll(".manju-thumb").forEach((t) =>
        t.addEventListener("click", () => this.previewImage(t.dataset.img, t.dataset.name))
      );
      $("manju-gacha-open").addEventListener("click", () => this.openGachaModal());
    },

    /* 打开抽卡弹窗:完整 抽卡/采纳 操作在此进行 */
    openGachaModal() {
      if (this.denyNoProject()) return;
      const gen = this._modalGen; // 代次守卫:弹窗关闭/切换后放弃回弹,防止劫持当前弹窗
      this.renderGachaModal(true);      // 首次打开:压新层
      this.loadPlan(() => { if (gen === this._modalGen) this.renderGachaModal(); }); // 拉最新方案原地刷新
    },

    /* 生成角色/场景方案(前置·角色抽卡,char_gacha.py --plan-characters) */
    genCharacters() {
      if (!this.project) return;
      const btn = $("mg-plan");
      if (btn) { btn.textContent = "生成中…"; btn.disabled = true; }
      post("/api/manju/gacha/plan", { config: this.project, chapters: this.chapters, episode: this.episode, shots: this.only }).then((r) => {
        if (r.ok) {
          this.loadPlan(() => this.renderGachaModal());
        } else this.setErr((r.error || "生成失败").trim());
      }).catch((e) => this.setErr(e.message)).finally(() => {
        if (btn) { btn.textContent = "▶ 生成方案"; btn.disabled = false; }
      });
    },

    /* 正式定妆照路径(已采纳/管线产出),无则空串 */
    officialChar(id) {
      const m = ((this.outputs && this.outputs.characters) || [])
        .find((c) => (c.name || "").replace(/\.[^.]+$/, "") === id);
      return m ? m.path : "";
    },

    /* 指定视图的正式图路径:主视图=定妆照;full/side/detail=对应视图文件
       (outputs.characters 含全部视图 png,按文件名 <id>_<view>.png 匹配) */
    charViewOfficial(id, view) {
      if (!view) return this.officialChar(id);
      const m = ((this.outputs && this.outputs.characters) || [])
        .find((c) => (c.name || "").replace(/\.[^.]+$/, "") === id + "_" + view);
      return m ? m.path : "";
    },

    /* 主视图(front)定妆照是否就绪(有正式主图文件) */
    charMainReady(id) {
      const g = this.gacha[id];
      if (g && g.views && g.views[""]) {
        if (g.views[""].ready) return true;
        // 兜底:outputs 里已有正式主图但 plan 未同步(如 assets 刚生成)
        return !!this.officialChar(id);
      }
      return !!this.officialChar(id);
    },

    /* 指定视图正式文件是否就绪(主视图=定妆照;其余=对应视图文件) */
    charViewReady(id, view) {
      if (!view) return this.charMainReady(id);
      const g = this.gacha[id];
      if (g && g.views && g.views[view] && g.views[view].ready) return true;
      return !!this.charViewOfficial(id, view);
    },

    renderGachaModal(fresh) {
      // fresh=true=首次打开(压新层);内部刷新(视图/候选/抽卡完成)=原地替换,不压栈
      const p = this.plan || {};
      const fileUrl = (p2) => "/api/fs/file?path=" + encodeURIComponent(p2);
      // 2026-08-25 即梦角色版:新增「角色板」视图(三视图+特写+服饰+表情+配色+人设整合图,控全剧一致性)
      // 2026-08-26 用户要求展示顺序:角色板(权威整合展示,基于主图生成)置顶 → 正面/全身/侧面/细节/Q版
      const VIEWS = [["board", "角色板", "🪪"], ["", "正面", "🎭"], ["full", "全身", "🧍"], ["side", "侧面", "↔️"], ["detail", "细节", "🔍"], ["q", "Q版", "🐣"]];
      // 2026-08-26 用户要求:角色卡标注 年龄/性别/角色定位(正角/反派/配角/演员/灵宠/妖兽)
      const roleTag = (c) => {
        if (c.species) return c.species;          // 灵宠/妖兽/神兽/精怪…
        if (c.role === "反派") return "反派";
        if (c.role === "功能配角") return "配角";
        if (c.role === "正角") return "正角";
        return "演员";                             // 无标注的普通角色
      };
      let html = "";
      if (!p.exists) {
        html = `<div class="manju-empty">
          <div>📋 暂无角色方案</div>
          <div class="manju-meta" style="margin-top:4px">抽卡需要先由「方案」阶段生成角色列表</div>
          <button id="mg-plan" class="hrs-btn hrs-btn-primary" style="margin-top:10px">▶ 生成方案</button>
        </div>`;
      } else {
        const chars = p.characters || [];
        const pending = chars.filter((c) => !this.charMainReady(c.id)); // 还没有主视图定妆照的角色
        const cntSaved = ls("gachaCount") || "4";
        html = chars.length
          ? `<div class="manju-gacha-toolbar">
              <span class="manju-meta">每次</span>
              <select id="mg-count">${[1, 2, 4, 6].map((n) => `<option value="${n}"${String(n) === cntSaved ? " selected" : ""}>${n} 张</option>`).join("")}</select>
              <button id="mg-draw-all" class="hrs-btn"${pending.length ? "" : " disabled"}>🎲 全员抽卡${pending.length ? `（${pending.length} 位未定妆）` : ""}</button>
              <span class="manju-meta" style="margin-left:auto">多视图独立抽卡 · 采纳当前选中</span>
            </div>
          <div class="manju-gacha-grid">` + chars.map((c) => {
              const g = this.gacha[c.id] || (this.gacha[c.id] = { views: {}, cur: "" });
              const curView = g.cur || "";
              // 视图 tab
              const tabs = VIEWS.map(([v, label, icon]) => {
                const vg = g.views[v] || (g.views[v] = { list: [], sel: -1, ready: false });
                const tag = vg.ready || this.charViewOfficial(c.id, v) ? "✓" : "";
                return `<button class="manju-view-tab${v === curView ? " sel" : ""}" data-viewtab="${esc(c.id)}" data-view="${esc(v)}" title="${esc(label)}">${icon}${esc(label)}${tag ? `<i class="manju-view-ok">${tag}</i>` : ""}</button>`;
              }).join("");
              const vg = g.views[curView] || (g.views[curView] = { list: [], sel: -1, ready: false });
              const cur = vg.sel >= 0 && vg.list[vg.sel] ? vg.list[vg.sel] : null;
              // 当前视图已采纳的正式图(主视图=定妆照;full/side/detail=对应视图文件)
              const official = this.charViewOfficial(c.id, curView);
              const img = (cur && cur.image) || official || "";
              // 预览区状态角标:已定 ✓ / 未抽卡提示(悬浮在图片上方)
              const badge = img
                ? (vg.ready || official ? `<span class="manju-char-badge ok">✓ 已定</span>` : `<span class="manju-char-badge">候选</span>`)
                : (vg.ready ? `<span class="manju-char-badge ok">✓ 已定</span>` : "");
              const preview = img
                ? `<img class="manju-char-img" src="${fileUrl(img)}" alt="${esc(c.id)}" data-img="${esc(img)}" data-name="${esc(c.id)}" title="点击预览大图" onerror="this.classList.add('img-err');this.insertAdjacentHTML('afterend','<div class=&quot;img-err-ph&quot;>🖼 图片加载失败</div>');">${badge}`
                : `<div class="manju-gacha-ph">${vg.ready ? "✓ 已定" : "未抽卡"}</div>${badge}`;
              const strip = vg.list.length
                ? `<div class="manju-cand-strip">` + vg.list.map((it, i) =>
                    `<button class="manju-cand${i === vg.sel ? " sel" : ""}" data-cand="${esc(c.id)}" data-view="${esc(curView)}" data-idx="${i}" title="seed ${it.seed}"><img src="${fileUrl(it.image)}" loading="lazy" alt=""></button>`).join("") + `</div>`
                : "";
              const readyTag = vg.ready ? `<span class="manju-gacha-oktag">✓</span>` : "";
              return `<div class="manju-char">
                <div class="manju-char-head">
                  <span class="manju-char-name" title="${esc(c.id)}">${esc(c.id)}</span>
                  <span class="manju-char-tag">${readyTag}${[c.gender, c.age, roleTag(c)].filter(Boolean).map(esc).join("·")}</span>
                </div>
                <div class="manju-view-tabs">${tabs}</div>
                <div class="manju-char-preview">${preview}</div>
                ${strip}
                <div class="manju-char-actions">
                  <button class="hrs-btn" data-gacha="${esc(c.id)}" data-view="${esc(curView)}">🎲 抽卡</button>
                  <button class="hrs-btn" data-upload="${esc(c.id)}" data-view="${esc(curView)}" title="上传本地角色图并采纳为正式定妆照">📤 上传</button>
                  <button class="hrs-btn hrs-btn-primary" data-adopt="${esc(c.id)}" data-view="${esc(curView)}" ${cur ? "" : "disabled"}>采纳</button>
                </div>
              </div>`;
            }).join("") + `</div>
          <div class="manju-meta" style="margin-top:8px">📤 上传：选择本地图片，保存为正式定妆照并自动生成正脸参考，后续渲染以此为准。多视图（正面/全身/侧面/细节）让 H3 参考更完整，人物更统一。</div>
          <input id="manju-upload-file" type="file" accept="image/png,image/jpeg,image/webp" style="display:none">`
          : '<div class="manju-empty">方案中无角色</div>';
      }
      if (fresh) this.openModal("角色抽卡", html, true);
      else this.rerenderModal("角色抽卡", html, true);
      const planBtn = $("mg-plan");
      if (planBtn) planBtn.addEventListener("click", () => this.genCharacters());
      const cnt = $("mg-count");
      if (cnt) cnt.addEventListener("change", () => ls("gachaCount", cnt.value));
      const da = $("mg-draw-all");
      if (da) da.addEventListener("click", () => this.drawAllGacha());
      const mgel = this._modalEl; // 多级弹窗:只绑定当前弹窗内的抽卡控件
      if (!mgel) return;
      mgel.querySelectorAll("[data-gacha]").forEach((b) =>
        b.addEventListener("click", () => this.drawGacha(b.dataset.gacha, b, b.dataset.view || ""))
      );
      mgel.querySelectorAll("[data-upload]").forEach((b) =>
        b.addEventListener("click", () => this.uploadCharPick(b.dataset.upload, b.dataset.view || ""))
      );
      const upFile = $("manju-upload-file");
      if (upFile) upFile.addEventListener("change", (e) => this.uploadCharFile(e.target.files[0]));
      mgel.querySelectorAll("[data-adopt]").forEach((b) =>
        b.addEventListener("click", () => this.adoptGacha(b.dataset.adopt, b.dataset.view || ""))
      );
      // 视图 tab 切换
      mgel.querySelectorAll("[data-viewtab]").forEach((b) =>
        b.addEventListener("click", () => {
          const g = this.gacha[b.dataset.viewtab];
          if (!g) return;
          g.cur = b.dataset.view || "";
          this.renderGachaModal();
        })
      );
      // 候选缩略图点选:切换当前查看/采纳的候选(按视图)
      mgel.querySelectorAll("[data-cand]").forEach((b) =>
        b.addEventListener("click", () => {
          const g = this.gacha[b.dataset.cand];
          if (!g) return;
          const vg = g.views[b.dataset.view || ""];
          if (!vg) return;
          const idx = parseInt(b.dataset.idx, 10);
          if (idx >= 0 && idx < vg.list.length) { vg.sel = idx; this.renderGachaModal(); }
        })
      );
      // 角色照片点击预览大图
      mgel.querySelectorAll(".manju-char-img").forEach((el) =>
        el.addEventListener("click", () => this.previewImage(el.dataset.img, el.dataset.name))
      );
    },

    /* 每次抽卡张数:取弹窗下拉当前值(localStorage 记忆) */
    gachaCount() {
      const sel = $("mg-count");
      if (sel) return parseInt(sel.value, 10) || 4;
      return parseInt(ls("gachaCount") || "4", 10) || 4;
    },

    /* 新候选并入对应视图抽卡历史并自动选中第一张新卡(历史不覆盖,旧候选一直可回看) */
    gachaAdd(charId, view, imgs) {
      const g = this.gacha[charId] || (this.gacha[charId] = { views: {}, cur: "" });
      const vg = g.views[view || ""] || (g.views[view || ""] = { list: [], sel: -1, ready: false });
      const start = vg.list.length;
      imgs.forEach((im) => vg.list.push({ image: im.image, seed: im.seed }));
      vg.sel = start;
    },

    drawGacha(charId, btn, view) {
      if (!this.project) return;
      if (this.denyIfRunning()) return; // 抽卡走 ComfyUI 出图,渲染任务运行中不抢 GPU
      const gen = this._modalGen; // 代次守卫:抽卡耗时期间弹窗被关闭则不回弹
      const n = this.gachaCount();
      if (btn) { btn.textContent = "抽卡中…"; btn.disabled = true; }
      post("/api/manju/gacha", { config: this.project, episode: this.episode, char: charId, view: view || "", count: n }).then((r) => {
        if (btn) { btn.textContent = "🎲 抽卡"; btn.disabled = false; }
        if (gen !== this._modalGen) return;
        if (r.ok && r.images && r.images.length) {
          this.gachaAdd(charId, view || "", r.images);
          this.renderGachaModal();
        } else this.setErr((r.error || "抽卡失败").trim());
      }).catch((e) => {
        if (btn) { btn.textContent = "🎲 抽卡"; btn.disabled = false; }
        if (gen === this._modalGen) this.setErr(e.message);
      });
    },

    /* 全员抽卡:为所有还没有主视图定妆照的角色各连抽一轮主视图(串行逐个出卡,实时刷新进度) */
    async drawAllGacha() {
      if (!this.project) return;
      if (this.denyIfRunning()) return;
      const chars = ((this.plan && this.plan.characters) || []).filter((c) => !this.charMainReady(c.id));
      if (!chars.length) return;
      const gen = this._modalGen;
      const n = this.gachaCount();
      for (let i = 0; i < chars.length; i++) {
        if (gen !== this._modalGen) return; // 弹窗已关闭:停止批量,不打扰新弹窗
        const btn = $("mg-draw-all");
        if (btn) { btn.disabled = true; btn.textContent = `抽卡中 ${i + 1}/${chars.length}（${chars[i].id}）…`; }
        try {
          const r = await post("/api/manju/gacha", { config: this.project, episode: this.episode, char: chars[i].id, view: "", count: n });
          if (gen !== this._modalGen) return;
          if (r.ok && r.images && r.images.length) this.gachaAdd(chars[i].id, "", r.images);
          else this.setErr((r.error || chars[i].id + " 抽卡失败").trim());
        } catch (e) { this.setErr(e.message); }
        if (gen !== this._modalGen) return;
        this.renderGachaModal();
      }
      if (gen === this._modalGen) {
        this.renderGachaModal();
        this.logNote("(🎲 全员抽卡完成:" + chars.length + " 位角色已出新候选)");
      }
    },

    adoptGacha(charId, view) {
      const g = this.gacha[charId];
      const vg = g && g.views[view || ""];
      const cur = vg && vg.sel >= 0 && vg.list[vg.sel] ? vg.list[vg.sel] : null;
      if (!this.project || !cur) return;
      const gen = this._modalGen; // 代次守卫:采纳请求期间弹窗被关闭则不回弹
      post("/api/manju/gacha/adopt", { config: this.project, episode: this.episode, char: charId, view: view || "", image: cur.image }).then((r) => {
        if (gen !== this._modalGen) return;
        if (r.ok) {
          this.renderGachaModal();
          this.refreshOutputs();
        } else this.setErr((r.error || "采纳失败").trim());
      }).catch((e) => { if (gen === this._modalGen) this.setErr(e.message); });
    },

    /* 上传本地角色图:选图后直接采纳为正式定妆照(自动生成正脸参考) */
    uploadCharPick(charId, view) {
      this._uploadChar = charId;
      this._uploadView = view || "";
      const f = $("manju-upload-file");
      if (!f) return;
      f.value = "";
      f.click();
    },
    uploadCharFile(file) {
      if (!file || !this._uploadChar) return;
      const charId = this._uploadChar;
      const view = this._uploadView || "";
      this._uploadChar = "";
      this._uploadView = "";
      if (this.denyIfRunning()) return; // 渲染任务运行中不上传/覆盖定妆照
      const gen = this._modalGen; // 代次守卫:上传耗时期间弹窗被关闭则不回弹
      const fd = new FormData();
      fd.append("config", this.project);
      fd.append("episode", this.episode);
      fd.append("char", charId);
      fd.append("view", view);
      fd.append("file", file);
      fetch("/api/manju/gacha/upload", { method: "POST", body: fd, cache: "no-store", headers: { "X-NiliX-Token": nilixTok() } })
        .then((r) => r.json())
        .then((r) => {
          if (this._modalGen !== gen) return; // 代次守卫:上传耗时期间弹窗被关闭则不回弹
          if (r.ok) {
            this.gachaAdd(charId, view, [{ image: r.image, seed: 0 }]);
            this.renderGachaModal();
            this.refreshOutputs();
          } else this.setErr("上传采纳失败: " + (r.error || ""));
        })
        .catch((e) => { if (this._modalGen === gen) this.setErr("上传失败: " + e.message); });
    },

    /* 产物分区折叠状态:人物/场景默认折叠,视频默认展开;用户切换后按 localStorage 记忆 */
    /* 产物分区折叠状态:人物/场景默认折叠,视频默认展开;按项目隔离存储(避免跨项目串状态) */
    secKey(key) {
      const proj = this.project ? String(this.project).split(/[\\/]+/).filter(Boolean).pop() || "" : "";
      return "manju-sec-" + proj + "-" + key;
    },
    _secFolded(key) {
      let v = null;
      try { v = localStorage.getItem(this.secKey(key)); } catch (e) {}
      if (v === null) return key === "char" || key === "scene";
      return v === "1";
    },

    renderOutputs() {
      const o = this.outputs;
      // 人物只显示正式定妆照(排除 _face/_full/_side/_detail 视图与正脸参考,避免重复)
      const chars = (o.characters || []).filter((c) => !/(_face|_full|_side|_detail|_board)\./i.test(c.name || ""));
      const scenes = o.scenes || [];
      const eps = o.episodes || [];
      const fileUrl = (p) => "/api/fs/file?path=" + encodeURIComponent(p);

      // 产物面板标题:共几集
      const epEl = $("manju-outputs-ep");
      if (epEl) epEl.textContent = eps.length ? eps.length + " 集" : "—";

      let html = "";

      // 人物定妆照(默认折叠):采纳过的显示正式定妆照并标注 ✓
      html += `<div class="manju-out-sec${this._secFolded("char") ? " is-folded" : ""}" data-sec="char">`;
      html += `<div class="manju-out-title"><span class="manju-sec-foldbtn">${this._secFolded("char") ? "▸" : "▾"}</span>🎭 人物 <span class="manju-out-count">${chars.length}</span></div>`;
      html += `<div class="manju-sec-body">`;
      html += chars.length
        ? `<div class="manju-thumbs">${chars.map((c) => {
            const tag = c.adopted ? `<span class="manju-adopted-tag">✓ 已采纳</span>` : "";
            return `<div class="manju-thumb" data-img="${esc(c.path)}" data-name="${esc(c.name)}" title="预览 ${esc(c.name)}"><img src="${fileUrl(c.path)}&v=${c.v || ""}" loading="lazy" alt=""><span class="manju-thumb-name">${esc(c.name)}</span>${tag}</div>`;
          }).join("")}</div>`
        : `<div class="manju-empty">暂无人物定妆照</div>`;
      html += `</div></div>`;

      // 场景图(默认折叠)
      html += `<div class="manju-out-sec${this._secFolded("scene") ? " is-folded" : ""}" data-sec="scene">`;
      html += `<div class="manju-out-title"><span class="manju-sec-foldbtn">${this._secFolded("scene") ? "▸" : "▾"}</span>🏞 场景 <span class="manju-out-count">${scenes.length}</span></div>`;
      html += `<div class="manju-sec-body">`;
      html += scenes.length
        ? `<div class="manju-thumbs">${scenes.map((c) => `<div class="manju-thumb" data-img="${esc(c.path)}" data-name="${esc(c.name)}" title="预览 ${esc(c.name)}"><img src="${fileUrl(c.path)}" loading="lazy" alt=""><span class="manju-thumb-name">${esc(c.name)}</span></div>`).join("")}</div>`
        : `<div class="manju-empty">暂无场景图</div>`;
      html += `</div></div>`;

      // 视频按集区分(默认展开,每集独立折叠记忆;成片置顶高亮;云端 2K 产物带徽标)
      // 2026-08-24 用户反馈:产物区只显示 EP01——EP02 只有方案(无镜头)被 renderEp 整块 return 丢弃。
      // 修复:有方案文件也算该集存在,集区块照常展示(无视频时占位「已出方案 · 待渲染」)
      const renderEp = (ep) => {
        const clips = ep.clips || [];
        const ups = ep.upscaled || [];
        const final = ep.final;
        const vids = (final ? [final] : []).concat(clips);
        const arts = Object.keys(ep.artifacts || {});
        // 该集完全无产物(无镜头/2K/成片/方案文件)才跳过;已出方案的集也占位展示
        if (!vids.length && !ups.length && !arts.length) return "";
        const secKey = "video-" + (ep.episode || "");
        const hasVids = !!(vids.length || ups.length);
        let h = `<div class="manju-out-sec${this._secFolded(secKey) ? " is-folded" : ""}" data-sec="${secKey}">`;
        h += `<div class="manju-out-title"><span class="manju-sec-foldbtn">${this._secFolded(secKey) ? "▸" : "▾"}</span>🎬 ${esc(ep.episode)} <span class="manju-out-count">${clips.length} 镜头${final ? " · 成片" : ""}${ups.length ? " · ☁️2K×" + ups.length : ""}${!hasVids && arts.length ? " · 已出方案" : ""}</span>${clips.length ? `<span class="manju-out-actions"><button class="hrs-btn manju-up2k-btn" data-up2k="${esc(ep.episode || "")}" title="云端 2K 定稿:本地定稿镜提交 MiniMax 升 2K(需在设置里填 MiniMax Key),产物落 clips/${esc(ep.episode || "")}/2k/">☁️ 2K</button><button class="hrs-btn manju-up2k-btn" data-jy="${esc(ep.episode || "")}" title="导出剪映草稿:视频轨+字幕轨(不烧录,可在剪映继续编辑);需 venv 安装 pyJianYingDraft">📦 剪映</button></span>` : ""}</div>`;
        h += `<div class="manju-sec-body">`;
        if (hasVids) {
          h += `<div class="manju-vids">${vids.map((v) => {
            const isFinal = v === final;
            return `<div class="manju-vid${isFinal ? " manju-vid-final" : ""}" data-video="${esc(v.path)}" data-name="${esc(v.name)}" title="播放 ${esc(v.name)}"><span class="manju-vid-play">▶</span><span class="manju-vid-name">${esc(v.name)}</span><span class="manju-meta">${isFinal ? "成片 · " : ""}${fmtSize(v.size)}</span>${v.stale ? '<span class="manju-stale-tag" title="提示词/定妆照/画幅已变,此产物是旧的;下次渲染自动删旧重渲">⚠️ 已过期</span>' : ""}<button class="manju-vid-menu" data-menu="${esc(v.path)}" data-ep="${esc(ep.episode || "")}" data-isfinal="${isFinal ? "1" : ""}" title="更多操作">⋮</button></div>`;
          }).join("")}</div>`;
          if (ups.length) h += `<div class="manju-vids" style="margin-top:6px">${ups.map((v) => `<div class="manju-vid manju-vid-2k" data-video="${esc(v.path)}" data-name="${esc(v.name)}" title="播放 ${esc(v.name)}(云端 2K)"><span class="manju-vid-play">▶</span><span class="manju-vid-name">☁️ ${esc(v.name)}</span><span class="manju-meta">2K · ${fmtSize(v.size)}</span><button class="manju-vid-menu" data-menu="${esc(v.path)}" data-ep="${esc(ep.episode || "")}" data-isfinal="0" title="更多操作">⋮</button></div>`).join("")}</div>`;
          if (arts.length) h += `<div class="manju-meta" style="margin-top:6px">📋 ${arts.map(esc).join("、")}</div>`;
        } else {
          h += `<div class="manju-empty">📋 已出方案 · 待渲染（点「一条龙 / 续跑」生成该集镜头）</div>`;
        }
        h += `</div></div>`;
        return h;
      };
      html += eps.map(renderEp).join("");

      $("manju-outputs").innerHTML = html;

      // 分区折叠:点击标题折叠/展开(人物/场景默认折叠,切换后按 localStorage 记忆)
      $("manju-outputs").querySelectorAll(".manju-out-sec").forEach((sec) => {
        const title = sec.querySelector(".manju-out-title");
        if (!title) return;
        title.addEventListener("click", () => {
          const folded = !sec.classList.contains("is-folded");
          sec.classList.toggle("is-folded", folded);
          const btn = sec.querySelector(".manju-sec-foldbtn");
          if (btn) btn.textContent = folded ? "▸" : "▾";
          const key = sec.dataset.sec;
          if (key) { try { localStorage.setItem(this.secKey(key), folded ? "1" : "0"); } catch (e) {} }
        });
      });

      // 绑定预览
      $("manju-outputs").querySelectorAll(".manju-thumb").forEach((el) =>
        el.addEventListener("click", () => this.previewImage(el.dataset.img, el.dataset.name))
      );
      $("manju-outputs").querySelectorAll(".manju-vid").forEach((el) =>
        el.addEventListener("click", () => this.previewVideo(el.dataset.video, el.dataset.name))
      );
      // ⋮ 菜单:删除此文件 / 删除本集目录(阻止冒泡,不触发播放)
      $("manju-outputs").querySelectorAll(".manju-vid-menu").forEach((btn) =>
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.openDeleteMenu(btn.dataset.menu, btn.dataset.ep, btn.dataset.isfinal === "1");
        })
      );
      $("manju-outputs").querySelectorAll(".manju-up2k-btn[data-up2k]").forEach((btn) =>
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.startUpscale(btn.dataset.up2k, "");
        })
      );
      $("manju-outputs").querySelectorAll(".manju-up2k-btn[data-jy]").forEach((btn) =>
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.exportJianying(btn.dataset.jy);
        })
      );
    },

    /* 剪映草稿导出(同步):视频轨+字幕轨,返回草稿路径;未装 pyJianYingDraft 时透出安装指引 */
    exportJianying(ep) {
      if (this.denyNoProject()) return;
      this.logNote("(📦 剪映草稿导出中 ...)");
      post("/api/manju/jianying", { config: this.project, episode: ep || this.episode })
        .then((r) => {
          if (r.ok) {
            this.openModal("📦 剪映草稿已导出",
              `<div class="manju-confirm"><p class="mc-d">草稿目录:<br><b>${esc(r.draft || "")}</b></p>` +
              (r.copiedTo ? `<p class="mc-d">已自动复制到剪映草稿目录:<br>${esc(r.copiedTo)}</p>` : `<p class="mc-d">把该目录复制到剪映草稿位置(剪映设置可查)即可打开继续编辑;在项目 config.render.jianying_dir 填草稿目录可自动复制</p>`) +
              `</div>`);
          } else {
            this.openModal("📦 导出失败", `<div class="manju-confirm"><p class="mc-d" style="white-space:pre-wrap">${esc(r.error || "未知错误")}</p></div>`);
          }
        }).catch((e) => { this.setErr(e.message); });
    },

    /* 云端 2K 定稿:整集(shots 空)或指定镜头;后台任务,进度走运行日志 */
    /* 云端 2K 定稿:先请求费用预估,弹出准入确认(总时长/费用/产物落点),确认后提交 */
    startUpscale(ep, shots) {
      if (this.denyNoProject()) return;
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      const cfg = this.project, ep2 = ep || this.episode, shots2 = shots || "";
      get("/api/manju/upscale2k/estimate?config=" + encodeURIComponent(cfg) +
        "&episode=" + encodeURIComponent(ep2) + "&shots=" + encodeURIComponent(shots2))
        .then((e) => {
          const n = e.shots || 0, sec = e.durationSec || 0, cost = e.costCNY || 0;
          this.openModal("☁️ 云端 2K 定稿",
            `<div class="manju-confirm">
              <p class="mc-q">确认提交云端 2K 重生成?</p>
              <p class="mc-d">本地定稿镜头 ${n} 个 · 预计总时长 ${sec}s · 估算费用 <b>¥${cost}</b><br>每镜 2-8 分钟,整集可能 1-2 小时;产物落 <code>clips/${esc(ep2)}/2k/</code>,本地 GPU 零负担</p>
              <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px">
                <button id="up2k-yes" class="hrs-btn hrs-btn-primary">确认提交</button>
                <button id="up2k-no" class="hrs-btn">取消</button>
              </div>
            </div>`);
          $("up2k-yes").addEventListener("click", () => {
            this.closeModal();
            this.logNote("(☁️ 云端 2K 定稿提交中 ...)");
            post("/api/manju/upscale2k", { config: cfg, episode: ep2, shots: shots2 })
              .then(() => { this.poll(); }).catch((e2) => this.setErr(e2.message));
          });
          $("up2k-no").addEventListener("click", () => this.closeModal());
        })
        .catch((e) => this.setErr(e.message));
    },

    /* 产物操作弹窗:镜头文件附「云端 2K」入口;删除支持 文件级(单 mp4)/ 集级 */
    openDeleteMenu(path, ep, isFinal) {
      if (this.denyNoProject()) return;
      const fname = path.split(/[\\/]/).pop() || "";
      const isShot = /^\d+\.mp4$/i.test(fname); // 本地定稿镜头(非成片/预告片/2K 产物)
      this.openModal("🛠 产物操作",
        `<div class="manju-confirm">
          <p class="mc-q">要做什么?</p>
          <p class="mc-d">文件:${esc(fname)}${isFinal ? "(成片)" : isShot ? "(镜头)" : ""}${ep ? "<br>集:" + esc(ep) : ""}</p>
          <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px;flex-wrap:wrap">
            ${isShot ? '<button id="up-2k" class="hrs-btn hrs-btn-primary" title="此镜提交 MiniMax 云端升 2K(需设置里填 Key)">☁️ 此镜云端 2K</button>' : ""}
            <button id="del-file" class="hrs-btn">删除此文件</button>
            <button id="del-ep" class="hrs-btn hrs-btn-danger">删除本集全部</button>
            <button id="del-cancel" class="hrs-btn">取消</button>
          </div>
        </div>`);
      $("del-cancel").addEventListener("click", () => this.closeModal());
      $("del-file").addEventListener("click", () => this.deleteOutput("file", path, ep));
      $("del-ep").addEventListener("click", () => this.deleteOutput("episode", "", ep));
      const upBtn = $("up-2k");
      if (upBtn) upBtn.addEventListener("click", () => {
        this.closeModal();
        this.startUpscale(ep || this.episode, String(parseInt(fname, 10) || ""));
      });
    },

    deleteOutput(scope, path, ep) {
      const btns = [$("del-file"), $("del-ep")];
      btns.forEach((b) => { if (b) b.disabled = true; });
      post("/api/manju/output/delete", {
        config: this.project, scope, path, episode: ep || this.episode,
      }).then((r) => {
        this.closeModal();
        this.logNote("(🗑 已删除:" + (r.removed || []).join("、") + ")");
        this.refreshOutputs();
      }).catch((e) => {
        btns.forEach((b) => { if (b) b.disabled = false; });
        this.setErr(e.message);
      });
    },

    /* 产物清理:展示三类可再生成产物占用,勾选后一键清理(定妆照/定稿/成片不动) */
    openCleanup() {
      if (this.denyNoProject()) return;
      get("/api/manju/cleanup/sizes?config=" + encodeURIComponent(this.project)).then((sizes) => {
        const fmtB = (n) => n >= 1048576 ? (n / 1048576).toFixed(1) + " MB" : Math.round(n / 1024) + " KB";
        const row = (key, label, hint) => {
          const s = (sizes || {})[key] || {};
          return `<label class="manju-check" style="padding:4px 0"><input type="checkbox" id="cl-${key}" checked> ${label} <span class="manju-meta">(${s.files || 0} 个 · ${fmtB(s.bytes || 0)})</span><span class="manju-meta">${hint}</span></label>`;
        };
        this.openModal("🧹 清理产物",
          `<div class="manju-confirm">
            <p class="mc-q">选择要清理的产物(均可重新生成):</p>
            ${row("gacha", "抽卡候选 _gacha", "已采纳的定妆照不受影响")}
            ${row("frames", "审片抽帧 _frames", "每次审片自动重抽")}
            ${row("2k", "云端 2K 产物", "重新提交即再生")}
            <div class="manju-row" style="justify-content:center;gap:12px;margin-top:16px">
              <button id="cl-go" class="hrs-btn hrs-btn-primary">清理选中</button>
              <button id="cl-cancel" class="hrs-btn">取消</button>
            </div>
          </div>`);
        $("cl-cancel").addEventListener("click", () => this.closeModal());
        $("cl-go").addEventListener("click", () => {
          const targets = ["gacha", "frames", "2k"].filter((k) => $("cl-" + k) && $("cl-" + k).checked);
          if (!targets.length) { this.closeModal(); return; }
          post("/api/manju/cleanup", { config: this.project, targets })
            .then((r) => {
              this.closeModal();
              const parts = (r.cleaned || []).map((c) => `${c.target} ${c.files}个`);
              this.setErr("🧹 已清理: " + (parts.join("、") || "无"));
              setTimeout(() => this.setErr(""), 5000);
              this.refreshOutputs();
            }).catch((e) => this.setErr(e.message));
        });
      }).catch((e) => this.setErr(e.message));
    },

    /* 图片预览(灯箱,上一张/下一张 分列图片左右两侧,参考侧栏收起按钮风格) */
    /* 风格预览弹窗:官方 8 风格示例动图 + 对应预设标注 */
    openStylePreview() {
      const S = [
        ["3d-animation-short-generator.gif", "3D 动画短片", "预设:3D CG"],
        ["handdrawn-live-video-generator.gif", "手绘真人实拍", "预设:手绘"],
        ["papercraft-stop-motion-explainer.gif", "纸艺定格动画", "预设:纸艺"],
        ["paper-collage-explainer-generator.gif", "纸片拼贴讲解", "预设:纸艺/粘土"],
        ["music-video-subtitle-generator.gif", "MV 字幕视频", "预设:2.5D 动漫"],
        ["brand-promo-video-generator.gif", "品牌宣传大片", "预设:写实"],
        ["co-op-game-intro-generator.gif", "游戏开场 CG", "预设:3D CG/二次元"],
        ["minimalist-product-ad-generator.gif", "极简产品广告", "预设:写实"],
      ];
      this.openModal("风格预览 · MiniMax H3 官方示例",
        `<div class="manju-style-cur" id="manju-style-cur"><span class="msc-k">当前风格</span><b>${esc(this.styleLabel(this.style))}</b><span class="msc-load">（解析中…）</span></div>
        <div class="manju-style-grid">
          ${S.map(([f, name, tag]) => `
          <div class="manju-style-card">
            <div class="manju-style-gif"><img src="/assets/styles/${f}" alt="${name}" loading="lazy"></div>
            <div class="manju-style-name">${name}</div>
            <div class="manju-style-tag">${tag}</div>
          </div>`).join("")}
        </div>
        <div class="manju-meta" style="margin-top:10px">示例动图来自 MiniMax H3 官方技能仓库(本地化展示);默认 8 预设为其提示词级风格映射,<b>可多选叠加</b>(点击预设即选中、再点取消,可组合多个如 2.5D+水墨),也可输自定义英文描述(点「应用」叠加到预设,重复词自动过滤)。</div>`, true);
      this.loadStyleDetail();
    },

    /* 拉取当前风格的解析措辞(定妆照/场景图、Ref2VA 开头、空镜 [Shot 1]),填充「?」弹窗详细说明 */
    loadStyleDetail() {
      const el = $("manju-style-cur");
      if (!el) return;
      get("/api/manju/style?style=" + encodeURIComponent(this.style || "real")).then((r) => {
        if (!el) return;
        if (!r || !r.asset) { el.innerHTML = "当前风格解析失败"; return; }
        const rows = [["定妆照/场景图", r.asset], ["Ref2VA 开头", r.opening], ["空镜 [Shot 1]", r.shot1]]
          .map(([k, v]) => `<div class="msc-row"><span class="msc-k">${k}</span><code>${esc(v)}</code></div>`).join("");
        el.innerHTML = `<div class="msc-name">当前风格：<b>${esc(this.styleLabel(r.style))}</b></div>${rows}`;
      }).catch(() => { if (el) el.innerHTML = "当前风格解析失败"; });
    },
    previewImage(path, name) {
      // 收集当前文档所有可预览图片(按 DOM 顺序),定位当前图索引
      const imgs = Array.from(document.querySelectorAll("[data-img]"))
        .map((el) => ({ path: el.dataset.img, name: el.dataset.name || "" }))
        .filter((x) => x.path);
      const idx = imgs.findIndex((x) => x.path === path);
      this._previewImgs = imgs;
      this._previewIdx = idx >= 0 ? idx : 0;
      // 2026-08-24 用户反馈:预览器应单窗口浏览当前项目全部产出图,不要一张图一个弹窗——
      // 已有预览弹窗(未关闭)时原地复用并提升到栈顶,不再压新层
      if (this._pvOverlay && document.body.contains(this._pvOverlay)) this.promotePreview();
      this.renderPreview();
    },
    /* 预览弹窗复用:把已开的预览弹窗提升到弹窗栈顶(翻页/再点图共用同一窗口) */
    promotePreview() {
      const stack = this._modalStack = this._modalStack || [];
      const idx = stack.indexOf(this._pvOverlay);
      if (idx >= 0) stack.splice(idx, 1);
      stack.push(this._pvOverlay);
      this._pvOverlay.style.zIndex = String(1200 + stack.length * 10);
      this._modalEl = this._pvOverlay;
      document.body.appendChild(this._pvOverlay); // 移到 DOM 末尾确保可见
    },
    renderPreview() {
      const list = this._previewImgs || [];
      const i = Math.max(0, Math.min(list.length - 1, this._previewIdx || 0));
      const cur = list[i];
      if (!cur) return;
      const arrows = list.length > 1
        ? `
        <button id="mp-prev" class="manju-pv-arrow" title="上一张 (←)" ${i === 0 ? "disabled" : ""}>◂</button>
        <button id="mp-next" class="manju-pv-arrow" title="下一张 (→)" ${i === list.length - 1 ? "disabled" : ""}>▸</button>`
        : "";
      const title = `${cur.name || "预览"}（${i + 1}/${list.length}）`;
      const body = `<div class="manju-img-preview">
          ${arrows}
          <div class="manju-pv-stage">
            <img src="/api/fs/file?path=${encodeURIComponent(cur.path)}" alt="">
            ${list.length > 1 ? `<span class="manju-pv-count">${i + 1} / ${list.length}</span>` : ""}
          </div>
        </div>`;
      // 2026-08-24 用户反馈:单窗口预览——首次才 openModal 压层,
      // 翻页/再点图(已有预览弹窗)原地刷新同一窗口,不再每张图叠一个弹窗
      if (!this._pvOverlay || !document.body.contains(this._pvOverlay)) {
        this.openModal(title, body);
        this._pvOverlay = this._modalEl;
      } else {
        this._pvOverlay.querySelector(".manju-modal-title").textContent = title;
        this._pvOverlay.querySelector(".manju-modal-body").innerHTML = body;
      }
      const pv = this._pvOverlay;
      const prev = pv.querySelector("#mp-prev"), next = pv.querySelector("#mp-next");
      const go = (d) => {
        if (list.length < 2) return;
        const ni = Math.max(0, Math.min(list.length - 1, i + d));
        if (ni === i) return;
        this._previewIdx = ni;
        this.renderPreview();
      };
      if (prev) prev.addEventListener("click", () => go(-1));
      if (next) next.addEventListener("click", () => go(1));
      // 键盘 ←/→ 翻页(每次渲染重新绑定,避免重复监听)
      if (this._pvKey) document.removeEventListener("keydown", this._pvKey);
      this._pvKey = (e) => {
        if (!this._modalEl) return; // 当前无弹窗(多级弹窗:仅顶层响应翻页)
        if (e.key === "ArrowLeft") { e.preventDefault(); go(-1); }
        else if (e.key === "ArrowRight") { e.preventDefault(); go(1); }
      };
      document.addEventListener("keydown", this._pvKey);
    },

    /* 视频预览(播放器) */
    previewVideo(path, name) {
      this.openModal(name || "播放",
        `<div class="manju-video-preview"><video src="/api/fs/file?path=${encodeURIComponent(path)}" controls autoplay></video></div>`);
    },

    /* ---- 弹窗(多级:2026-08-24 用户要求) ---- */
    /* 每个弹窗独立遮罩层,压栈管理——弹窗里再弹窗时父弹窗保留在后面,
       关闭子弹窗自动露出父弹窗,不再"弹窗里弹窗就全部关闭"。 */
    openModal(title, bodyHtml, wide) {
      if (!this._bound) this.bind();   // 自愈:任何页面(未进漫剧页)调用弹窗都先绑定
      // 代次守卫:任何开/关弹窗都会使挂起的异步渲染(设置/体检)失效,
      // 防止"请求完成时弹窗已被关闭 → 又弹回来"的关闭失效竞态
      this._modalGen = (this._modalGen || 0) + 1;
      const overlay = document.createElement("div");
      overlay.className = "manju-modal";
      this._modalStack = this._modalStack || [];
      overlay.style.zIndex = String(1200 + this._modalStack.length * 10); // 后开的在上层
      overlay.innerHTML =
        `<div class="manju-modal-panel${wide ? " wide" : ""}">
          <div class="manju-modal-head">
            <h3 class="manju-modal-title"></h3>
            <button class="manju-modal-close" title="关闭" aria-label="Close">✕</button>
          </div>
          <div class="manju-modal-body"></div>
        </div>`;
      overlay.querySelector(".manju-modal-title").textContent = title;
      overlay.querySelector(".manju-modal-body").innerHTML = bodyHtml;
      // 关闭:关闭按钮 / 点遮罩空白(Esc 由 bind 单例监听,一次只关顶层)
      overlay.querySelector(".manju-modal-close").addEventListener("click", () => this.closeModal());
      overlay.addEventListener("click", (e) => { if (e.target === overlay) this.closeModal(); });
      this._modalEl = overlay;
      this._modalStack.push(overlay);
      document.body.appendChild(overlay);
    },
    closeModal() {
      this._modalGen = (this._modalGen || 0) + 1;   // 关闭即作废所有挂起的异步渲染
      if (this._pvKey) {
        document.removeEventListener("keydown", this._pvKey);
        this._pvKey = null;
      }
      const overlay = this._modalEl;
      if (overlay) {
        if (overlay === this._pvOverlay) this._pvOverlay = null; // 预览弹窗关闭:清引用,下次重新开
        const v = overlay.querySelector("video");
        if (v) { v.pause(); v.removeAttribute("src"); }
        overlay.remove();
      }
      this._modalStack = this._modalStack || [];
      this._modalStack.pop();
      this._modalEl = this._modalStack.length ? this._modalStack[this._modalStack.length - 1] : null;
      this.picker = null;
    },
    /* 原地重渲染当前顶层弹窗(不压新层)——目录翻页/抽卡视图切换等"同弹窗刷新"场景,
       避免多级弹窗栈膨胀;当前无弹窗时退回 openModal 新开 */
    rerenderModal(title, bodyHtml, wide) {
      if (!this._modalEl) { this.openModal(title, bodyHtml, wide); return; }
      const overlay = this._modalEl;
      overlay.querySelector(".manju-modal-title").textContent = title;
      overlay.querySelector(".manju-modal-body").innerHTML = bodyHtml;
      const panel = overlay.querySelector(".manju-modal-panel");
      if (panel) panel.classList.toggle("wide", !!wide);
    },

    /* 使用说明 */
    openGuide() {
      const html = `<div class="manju-guide">
        <div class="mg-hero">
          <div class="mg-hero-ic">🎬</div>
          <div class="mg-hero-t">漫剧生产 · 使用说明</div>
          <div class="mg-hero-s">小说 → 方案 → 资产 → 渲染 → 合成，一站式漫剧工作台</div>
        </div>
        <div class="mg-grid">` +
        GUIDE.map((g, i) =>
          `<div class="manju-guide mg-card">
            <div class="mg-head">
              <span class="mg-no">${String(i + 1).padStart(2, "0")}</span>
              <span class="mg-ic">${g.ic}</span>
              <h4>${esc(g.t)}</h4>
            </div>
            <ul class="mg-list">${g.ps.map((p) => `<li>${p}</li>`).join("")}</ul>
          </div>`
        ).join("") +
        `</div></div>`;
      this.openModal("使用说明", html, true);
    },

    /* 粘贴小说文章:直接粘贴正文保存为 .md，免去先建目录/文件 */
    openPaste() {      this.pasteTitle = ""; this.pasteText = ""; this.pasting = false;
      this.openModal("粘贴小说文章",
        `<div class="manju-form">
          <label>标题（可选，用作文件名）</label>
          <input id="mp-title" class="manju-input" placeholder="如：吞灵帝尊">
          <label>小说文章正文（可直接粘贴，自动保存为 .md）</label>
          <textarea id="mp-text" class="manju-input manju-textarea" rows="11" spellcheck="false" placeholder="粘贴小说正文…（无「# 第N章」标题时整篇作为一个素材直出）"></textarea>
          <div id="mp-err" class="manju-err-text"></div>
          <div class="manju-row">
            <button id="mp-cancel" class="hrs-btn">取消</button>
            <button id="mp-do" class="hrs-btn hrs-btn-primary">保存并识别</button>
          </div>
        </div>`);
      $("mp-title").addEventListener("input", (e) => { this.pasteTitle = e.target.value; });
      $("mp-text").addEventListener("input", (e) => { this.pasteText = e.target.value; });
      $("mp-cancel").addEventListener("click", () => this.closeModal());
      $("mp-do").addEventListener("click", () => this.doSavePaste());
    },
    doSavePaste() {
      this.pasteTitle = $("mp-title").value.trim();
      this.pasteText = $("mp-text").value.trim();
      if (!this.pasteText) { $("mp-err").textContent = "请粘贴小说文章正文"; return; }
      this.pasting = true;
      $("mp-do").textContent = "保存中…";
      $("mp-do").disabled = true;
      post("/api/manju/novel/save", { text: this.pasteText, title: this.pasteTitle }).then((r) => {
        this.pasting = false;
        if (r && r.path) {
          this.closeModal();
          this.novel = r.path;
          $("manju-novel").value = r.path;
          ls("novel", r.path);
          this.detectNovel();
        } else {
          $("mp-do").textContent = "保存并识别";
          $("mp-do").disabled = false;
          $("mp-err").textContent = (r && r.error) || "保存失败";
        }
      }).catch((e) => {
        this.pasting = false;
        $("mp-do").textContent = "保存并识别";
        $("mp-do").disabled = false;
        $("mp-err").textContent = "保存失败: " + e.message;
      });
    },

    /* ---- 视频脚本直出(H3 官方格式分镜脚本,与小说解析二选一) ---- */

    // 脚本格式示例模板(官方 [Shot N] 时间码/画面/台词/音效 + 环境声/配乐)
    scriptTemplate() {
      return `# 视频渲染脚本 EP01(示例模板,可整体替换)
【风格】Cinematic, live-action, 东方古风, 暖色调烛光
【时长】12 秒 / 2 镜

[Shot 1] 中景,缓慢推近。客栈内,青年(苏白,黑色长发束起,剑眉,深青色劲装)坐在桌边,手指轻叩桌面。
台词:苏白:"来了。"
音效:雨声,木地板吱呀。

[Shot 2] At 00:05.000,硬切,特写。女子推门而入,斗笠滴着水,抬头看向苏白。
台词:(无)
音效:门轴吱呀,雨声骤密。

环境声:整段雨声持续,木结构客栈的低频嗡鸣。
配乐:古琴慢板,音量渐强后回落。`;
    },

    /* 粘贴视频脚本:弹窗(可选带模板) → script/save 启用脚本直出模式 */
    openScriptPaste(withTemplate) {
      this.scriptText = withTemplate ? this.scriptTemplate() : "";
      this.scriptSaving = false;
      this.openModal("🎬 粘贴视频脚本（H3 官方格式直出）",
        `<div class="manju-form">
          <div class="manju-meta" style="margin:0 0 8px">
            LLM 将按 MiniMax H3 官方规范(<b>[Shot N] At MM:SS.mmm</b> 时间码 / 画面 / 台词 / 音效 / 环境声 / 配乐)
            一步直出完整渲染方案(角色/场景/分镜/<b>逐镜 H3 提示词</b>)。保存后本集方案自动重新生成;
            「清除脚本」可回到小说解析模式。脚本写法建议:<br>
            ① 每镜:景别 + 运镜 + 人物动作 + 台词(标注说话人) + 音效;<br>
            ② 多镜用 <code>[Shot 2] At 00:05.000</code> 标切点;<br>
            ③ 末尾可写「环境声:…」与「配乐:…」两段。
          </div>
          <label>视频渲染脚本正文（markdown，H3 官方分镜格式）</label>
          <textarea id="msp-text" class="manju-input manju-textarea" rows="14" spellcheck="false" placeholder="粘贴分镜脚本…（[Shot 1] 景别,运镜。画面动作。&#10;台词:角色:&quot;原文&quot;&#10;音效:…）">${esc(this.scriptText)}</textarea>
          <div id="msp-err" class="manju-err-text"></div>
          <div class="manju-row" style="justify-content:flex-start;margin:6px 0">
            <button id="msp-scan" class="hrs-btn">📁 选择目录检测脚本</button>
            <span style="opacity:.6;font-size:12px;margin-left:8px">手动选任意目录 → 自动检测分镜脚本:有则直接读取,无则引导 LLM 直出</span>
          </div>
          <div class="manju-row" style="justify-content:flex-start;margin:6px 0">
            <button id="msp-import" class="hrs-btn">📂 从小说分镜脚本导入（阶段6 产物）</button>
            <span style="opacity:.6;font-size:12px;margin-left:8px">自动定位 小说目录/素材/分镜脚本/第NNN章…_分镜脚本.md（EP01→第001章）</span>
          </div>
          <div class="manju-row">
            <button id="msp-cancel" class="hrs-btn">取消</button>
            <button id="msp-do" class="hrs-btn hrs-btn-primary">保存并启用脚本直出</button>
          </div>
        </div>`);
      $("msp-text").addEventListener("input", (e) => { this.scriptText = e.target.value; });
      $("msp-cancel").addEventListener("click", () => this.closeModal());
      $("msp-do").addEventListener("click", () => this.doSaveScript());
      $("msp-scan").addEventListener("click", () => this.openPicker("scan"));
      $("msp-import").addEventListener("click", () => this.importScriptFromNovel());
    },
    doSaveScript() {
      this.scriptText = $("msp-text").value.trim();
      if (!this.scriptText) { $("msp-err").textContent = "请粘贴视频脚本正文"; return; }
      if (!this.project) { $("msp-err").textContent = "请先选择项目"; return; }
      const projName = this.projName();
      this.scriptSaving = true;
      $("msp-do").textContent = "保存中…";
      $("msp-do").disabled = true;
      post("/api/manju/script/save", { project: projName, episode: this.episode, text: this.scriptText }).then((r) => {
        this.scriptSaving = false;
        if (r && r.ok) {
          this.closeModal();
          this.refreshScriptStatus();
          this.loadProject(); // 2026-08-23:解析脚本后同步刷新渲染配置显示(脚本模式参数)
          this.setNote("🎬 脚本直出模式已启用: " + (r.path || "") + "（下次运行方案将以脚本为准）");
        } else {
          $("msp-do").textContent = "保存并启用脚本直出";
          $("msp-do").disabled = false;
          $("msp-err").textContent = (r && r.error) || "保存失败";
        }
      }).catch((e) => {
        this.scriptSaving = false;
        $("msp-do").textContent = "保存并启用脚本直出";
        $("msp-do").disabled = false;
        $("msp-err").textContent = "保存失败: " + e.message;
      });
    },
    doScriptClear() {
      if (!confirm("清除视频脚本并回到小说解析模式？本集已生成的方案不会自动删除(下次运行按小说重新生成)。")) return;
      post("/api/manju/script/clear?project=" + encodeURIComponent(this.projName()), {}).then((r) => {
        this.refreshScriptStatus();
        // 2026-08-24 合并「内容来源」卡片:清除脚本=回小说 → 自动切回小说区块
        const sr = $("mc-content-script");
        if (sr && sr.checked) { sr.checked = false; this.applyContentMode(); }
        this.setNote((r && r.ok) ? "已清除脚本,回到小说解析模式" : "清除失败: " + ((r && r.error) || ""));
      }).catch((e) => this.setErr("清除失败: " + e.message));
    },
    /* 从小说项目 素材/分镜脚本/(爽文技能阶段6 产物)导入本集分镜脚本,启用脚本直出模式 */
    importScriptFromNovel() {
      if (!this.project) { $("msp-err").textContent = "请先选择项目"; return; }
      const projName = this.projName();
      const ep = this.episode || "EP01";
      const btn = $("msp-import");
      if (btn) { btn.disabled = true; btn.textContent = "导入中…"; }
      post("/api/manju/script/import-from-novel", { project: projName, episode: ep }).then((r) => {
        if (btn) { btn.disabled = false; btn.textContent = "📂 从小说分镜脚本导入（阶段6 产物）"; }
        if (r && r.ok) {
          this.closeModal();
          this.refreshScriptStatus();
          this.loadProject(); // 2026-08-23:从小说分镜脚本导入后同步刷新渲染配置
          this.setNote("🎬 已从小说分镜脚本导入(" + (r.source || "") + ")，脚本直出模式已启用");
        } else {
          $("msp-err").textContent = (r && r.error) || "导入失败:请确认小说项目已生成 素材/分镜脚本/";
        }
      }).catch((e) => {
        if (btn) { btn.disabled = false; btn.textContent = "📂 从小说分镜脚本导入（阶段6 产物）"; }
        $("msp-err").textContent = "导入失败: " + e.message;
      });
    },
    /* ---- 手动选择目录 → 检测分镜脚本(有则直接读取,无则引导 LLM 直出) ---- */
    /* mode: import=主页导入当前项目 / create=新建项目(记住脚本,创建后自动导入) */
    scanScriptDir(dir, mode) {
      this.scanDir = dir;
      this.scanMode = mode || "import";
      if (mode === "create") this.autoFillCreateName(dir); // 选脚本目录自动填充剧名
      this.closeModal();
      const ep = this.episode || "EP01";
      this.setNote("🔍 正在检测目录分镜脚本: " + dir + " …");
      post("/api/manju/script/scan-dir", { dir, episode: ep }).then((r) => {
        if (!r || !r.ok) { this.setNote("❌ 目录检测失败: " + ((r && r.error) || "未知错误")); return; }
        if (r.hasStoryboard && r.storyboards && r.storyboards.length) {
          this.showScanResult(r); // 有分镜脚本 → 列表直接读取
        } else {
          this.showScanEmpty(r);  // 无 → 引导 LLM 直出
        }
      }).catch((e) => this.setNote("❌ 目录检测失败: " + e.message));
    },
    showScanResult(r) {
      const rows = (r.storyboards || []).map((s) => `
        <div class="manju-file" data-file="${esc(s.path)}" data-ep="${esc(s.episode || "")}">
          <span class="manju-file-name">🎬 ${esc(s.name)}${s.selected ? " <span class='manju-md'>(匹配本集)</span>" : ""}</span>
          <span class="manju-meta">${s.episode ? "集号 " + esc(s.episode) + " · " : ""}${fmtSize(s.size)}</span>
          ${s.preview ? `<div class="manju-meta" style="white-space:pre-wrap;margin-top:2px">${esc(s.preview)}</div>` : ""}
        </div>`).join("");
      this.openModal("检测到分镜脚本",
        `<div class="manju-picker">
          <div class="manju-meta" style="margin:0 0 8px">目录: <code>${esc(r.dir)}</code> · 共 ${(r.storyboards || []).length} 个分镜脚本（已按章号排序）<br>📥 全部导入 = 每章一个脚本 → EP01/EP02…（渲染时按集号选对应脚本）<br>点击单项 → 只导入为当前集脚本（${esc(r.episode)}）</div>
          <div class="manju-filelist">${rows || '<div class="manju-empty">无</div>'}</div>
          <div class="manju-row">
            <button id="msr-all" class="hrs-btn hrs-btn-primary">📥 全部导入</button>
            <button id="msr-rescan" class="hrs-btn">📁 换目录</button>
            <button id="msr-cancel" class="hrs-btn">取消</button>
          </div>
        </div>`);
      if (!this._modalEl) return;
      this._modalEl.querySelectorAll(".manju-file").forEach((f) =>
        f.addEventListener("click", () => this.scanMode === "create"
          ? this.rememberScriptFile(f.dataset.file, f.dataset.ep)
          : this.importScriptFile(f.dataset.file, f.dataset.ep)));
      $("msr-all").addEventListener("click", () => this.importScriptAllFromDir(r.dir));
      $("msr-rescan").addEventListener("click", () => this.openPicker(this.scanMode === "create" ? "scan-create" : "scan"));
      $("msr-cancel").addEventListener("click", () => this.closeModal());
    },
    /* 全部导入:目录里所有分镜脚本按章号 → script/EP01..EPxx.md(每章一集)。
       新建场景(scanMode=create):记住目录,创建项目后自动批量导入 */
    importScriptAllFromDir(dir) {
      if (this.scanMode === "create") {
        this.createScriptFrom = dir;   // 目录(全部导入)
        this.createScriptAll = true;
        this.createScriptEp = this.episode || "EP01";
        this.closeModal();
        this.renderCreate();
        this.setNote("已选择目录，点「创建项目」后自动导入所有分镜脚本（每章一集）");
        return;
      }
      if (!this.project) { this.closeModal(); this.setNote("请先选择项目"); return; }
      this.setNote("正在批量导入分镜脚本…");
      post("/api/manju/script/import-all-dir", { project: this.projName(), episode: this.episode || "EP01", dir }).then((r) => {
        if (r && r.ok) {
          this.closeModal();
          this.refreshScriptStatus();
          this.loadProject(); // 脚本模式参数同步刷新
          this.setNote("已全部导入 " + (r.imported || 0) + "/" + (r.total || 0) + " 个分镜脚本（每章一集），渲染配置选集号即可选择对应脚本");
        } else {
          this.setNote("批量导入失败: " + ((r && r.error) || "未知错误"));
          this.closeModal();
        }
      }).catch((e) => { this.setNote("批量导入失败: " + e.message); this.closeModal(); });
    },
    /* 新建场景:记住选中的分镜脚本路径,创建项目后自动导入 */
    rememberScriptFile(file, ep) {
      this.createScriptFrom = file;
      this.createScriptEp = ep || "EP01";
      this.closeModal();
      this.renderCreate();
      this.setNote("📌 已选择分镜脚本，点「创建项目」后自动导入并启用脚本直出");
    },
    /* 直接读取检测到的分镜脚本 → 导入为当前集脚本,启用脚本直出 */
    importScriptFile(file, ep) {
      if (!this.project) { this.closeModal(); this.setNote("❌ 请先选择项目"); return; }
      const episode = ep || this.episode || "EP01";
      this.setNote("📥 正在读取分镜脚本…");
      post("/api/manju/script/import-dir", { project: this.projName(), episode, file }).then((r) => {
        if (r && r.ok) {
          this.closeModal();
          this.refreshScriptStatus();
          this.loadProject(); // 脚本模式参数同步刷新
          this.setNote("🎬 已直接读取分镜脚本(" + (r.source || "") + ")，脚本直出模式已启用");
        } else {
          this.setNote("❌ 读取失败: " + ((r && r.error) || "未知错误"));
          this.closeModal();
        }
      }).catch((e) => { this.setNote("❌ 读取失败: " + e.message); this.closeModal(); });
    },
    /* 目录无分镜脚本 → 引导 LLM 直出(小说解析模式) */
    showScanEmpty(r) {
      this.openModal("目录未检测到分镜脚本",
        `<div class="manju-form">
          <div class="manju-meta" style="margin:0 0 8px">目录 <code>${esc(r.dir)}</code> 中未找到分镜脚本<br>（识别规则：文件名含「分镜脚本/分镜」，或内容含 <code>[Shot N]</code> / 分镜表）。</div>
          <div class="manju-meta" style="margin:0 0 8px">💡 <b>LLM 直出</b>：若该目录含小说正文（<code>正文/</code> 或 <code>全本/*.md</code>），将其作为小说来源即可由 LLM 一步直出完整渲染方案：<br>· 新建项目 → 选「📖 小说解析」并把此目录设为小说目录；<br>· 已有项目 → 主页「小说来源」填此目录后重新生成方案。</div>
          <div class="manju-row">
            <button id="mse-rescan" class="hrs-btn">📁 换目录</button>
            <button id="mse-cancel" class="hrs-btn">取消</button>
          </div>
        </div>`);
      $("mse-rescan").addEventListener("click", () => this.openPicker(this.scanMode === "create" ? "scan-create" : "scan"));
      $("mse-cancel").addEventListener("click", () => this.closeModal());
    },
    projName() {
      // this.project 是完整 configPath(.../manju/<项目名>/config.json) → 取目录名
      return (this.project || "").split(/[\\/]/).filter(Boolean).slice(-2, -1)[0] || "";
    },
    /* 内容来源卡片:小说解析 / 视频脚本直出 区块显示切换(仅显示,不改配置)
       自动模式:加载项目/刷新状态时按解析结果(脚本 active?)自动展示对应区块 */
    applyContentMode(noAuto, skipRefresh) {
      const script = $("mc-content-script") && $("mc-content-script").checked;
      const nb = $("content-novel-block");
      const sb = $("content-script-block");
      if (nb) nb.style.display = script ? "none" : "";
      if (sb) sb.style.display = script ? "" : "none";
      // 切到脚本区块时刷新脚本状态(noAuto:手动切换不触发自动切回,避免弹回;skipRefresh:内部联动只切显示)
      if (script && this.project && !skipRefresh) this.refreshScriptStatus(noAuto);
    },
    refreshScriptStatus(noAuto) {
      const el = $("manju-script-meta");
      const clearBtn = $("manju-script-clear");
      if (!el) return;
      if (!this.project) { el.innerHTML = ""; if (clearBtn) clearBtn.classList.add("hidden"); return; }
      get("/api/manju/script?project=" + encodeURIComponent(this.projName())).then((r) => {
        if (r && r.active) {
          el.innerHTML = "🎬 <b>脚本直出模式</b>：<code>" + esc(r.path || "") + "</code>（" + (r.bytes || 0) + " 字）<br>" +
            "<div style='opacity:.75;margin-top:4px;white-space:pre-wrap;max-height:150px;overflow-y:auto;padding-right:6px;line-height:1.7'>" + esc(r.preview || "") + "</div>";
          if (clearBtn) clearBtn.classList.remove("hidden");
          // 内容来源卡片自动联动:解析结果是脚本 → 自动切到脚本区块
          const sr = $("mc-content-script");
          if (sr && !sr.checked) { sr.checked = true; this.applyContentMode(true, true); }
        } else {
          el.innerHTML = "小说解析模式（未启用脚本直出）";
          if (clearBtn) clearBtn.classList.add("hidden");
          // 内容来源卡片自动联动:解析结果是小说 → 自动切回小说区块(手动切换除外)
          if (!noAuto) {
            const sr = $("mc-content-script");
            if (sr && sr.checked) { sr.checked = false; this.applyContentMode(true, true); }
          }
        }
      }).catch(() => {});
    },

    /* 新建项目 */
    openCreate() {
      this.createName = ""; this.createNovel = ""; this.createKey = ""; this.createErr = ""; this.creating = false; this.savedKey = "";
      this._createNameAuto = ""; // 剧名自动填充标记(选目录后自动填,用户手动输入即失效)
      this.createScriptFrom = ""; this.createScriptEp = ""; this.createScriptAll = false; // 新建场景记住的脚本/目录(创建后自动导入),每次重置
      get("/api/manju/settings").then((r) => { if (r && r.hasKey && r.masked) this.savedKey = r.masked; this.renderCreate(); }).catch(() => this.renderCreate());
    },
    renderCreate() {
      const saved = this.savedKey ? "（已存默认: " + esc(this.savedKey) + "）" : "";
      if (this.createMode === undefined) this.createMode = "novel";
      const html = `<div class="manju-form">
          <label>剧名（项目目录名）</label>
          <input id="mc-name" class="manju-input" placeholder="如：吞灵帝尊" value="${esc(this.createName)}">
          <label>输入方式</label>
          <div class="manju-row">
            <label class="manju-check"><input id="mc-mode-novel" type="radio" name="mc-mode" ${this.createMode === "novel" ? "checked" : ""}> 📖 小说解析</label>
            <label class="manju-check"><input id="mc-mode-script" type="radio" name="mc-mode" ${this.createMode === "script" ? "checked" : ""}> 🎬 视频脚本直出</label>
          </div>
          <div id="mc-novel-row">
            <label>小说目录（含正文/设定/大纲）</label>
            <div class="manju-row">
              <input id="mc-novel" class="manju-input manju-wide" placeholder="选择小说目录或粘贴路径（自动识别 正文/设定集/分卷大纲）" value="${esc(this.createNovel)}">
              <button id="mc-novel-pick" class="hrs-btn">选择目录</button>
            </div>
          </div>
          <div id="mc-script-tip" class="manju-meta" style="${this.createMode === "script" ? "" : "display:none"}">🎬 视频脚本直出模式：直接粘贴 H3 官方格式分镜脚本（或留空创建后从小说分镜脚本导入/主页补充），LLM 一步直出渲染方案（逐镜 H3 提示词）。</div>
          <div id="mc-script-row" style="${this.createMode === "script" ? "" : "display:none"}">
            <label>视频脚本正文（H3 官方格式分镜脚本，可直接粘贴；留空=创建后再配置）</label>
            <textarea id="mc-script-text" class="manju-input manju-textarea" rows="8" spellcheck="false" placeholder="[Shot 1] 景别,运镜。画面动作。&#10;台词:角色:&quot;原文&quot;&#10;音效:…">${esc(this.createScript)}</textarea>
            <div class="manju-row" style="margin-top:6px">
              <button id="mc-script-tpl" class="hrs-btn">📋 插入官方格式模板</button>
              <button id="mc-script-scan" class="hrs-btn">📁 从目录检测分镜脚本</button>
              <span class="manju-meta" style="margin-left:8px">创建后自动启用脚本直出</span>
            </div>
            ${this.createScriptFrom ? `<div class="manju-meta" style="color:var(--ok);margin-top:4px">📌 已选择${this.createScriptAll ? "目录（全部导入）" : "分镜脚本"}: <code>${esc(this.createScriptFrom.split(/[\\/]+/).filter(Boolean).pop())}</code> — 点「创建项目」后自动${this.createScriptAll ? "导入所有分镜脚本" : "导入"}</div>` : ""}
          </div>
          <label>DeepSeek API Key${saved}</label>
          <input id="mc-key" class="manju-input" placeholder="${this.savedKey ? "留空自动用默认 Key" : "sk-...（留空则用已保存的默认 Key）"}">
          <label class="manju-check"><input id="mc-remember" type="checkbox" checked> 记住为默认 Key（下次新建自动使用）</label>
          <div id="mc-err" class="manju-err-text"></div>
          <div class="manju-row">
            <button id="mc-cancel" class="hrs-btn">取消</button>
            <button id="mc-do" class="hrs-btn hrs-btn-primary">创建项目</button>
          </div>
        </div>`;
      // 2026-08-24 关键修复(用户反馈:全部导入后新建弹窗按钮全失效):
      // 多级弹窗下若当前顶层已是新建弹窗则【原地重渲染】(rerenderModal 不压新栈),
      // 否则首次才 openModal 压栈。此前每次 renderCreate 都 openModal 压新栈,
      // 而 $("mc-*") 全局绑定落在旧弹窗 → 新弹窗(用户看到的顶层)按钮全部无绑定。
      const isFresh = !(this._modalEl && this._modalEl.querySelector("#mc-name"));
      if (isFresh) this.openModal("新建项目", html);
      else this.rerenderModal("新建项目", html);
      // 绑定必须限定当前弹窗(this._modalEl),多级弹窗下全局 $() 会绑定到旧弹窗
      const root = this._modalEl;
      if (!root) return;
      root.querySelector("#mc-name").addEventListener("input", (e) => { this.createName = e.target.value; this._createNameAuto = ""; }); // 手动输入后不再自动覆盖剧名
      root.querySelector("#mc-novel").addEventListener("input", (e) => { this.createNovel = e.target.value; });
      root.querySelector("#mc-key").addEventListener("input", (e) => { this.createKey = e.target.value; });
      root.querySelector("#mc-novel-pick").addEventListener("click", () => this.openPicker("create"));
      root.querySelector("#mc-cancel").addEventListener("click", () => this.closeModal());
      root.querySelector("#mc-do").addEventListener("click", () => this.doCreate());
      const st = root.querySelector("#mc-script-text");
      if (st) st.addEventListener("input", (e) => { this.createScript = e.target.value; });
      const tpl = root.querySelector("#mc-script-tpl");
      if (tpl) tpl.addEventListener("click", () => {
        const s = root.querySelector("#mc-script-text");
        // 2026-08-23 用户要求:模板示例移至新建项目弹窗——用完整 H3 官方格式模板
        s.value = this.scriptTemplate();
        this.createScript = s.value;
      });
      const scan = root.querySelector("#mc-script-scan");
      if (scan) scan.addEventListener("click", () => this.openPicker("scan-create"));
      // 输入方式切换:脚本直出模式隐藏小说目录行,显示脚本粘贴区(2026-08-23 用户要求:新建弹窗直接配置脚本)
      const applyMode = () => {
        this.createMode = root.querySelector("#mc-mode-script").checked ? "script" : "novel";
        root.querySelector("#mc-novel-row").style.display = this.createMode === "script" ? "none" : "";
        root.querySelector("#mc-script-tip").style.display = this.createMode === "script" ? "" : "none";
        root.querySelector("#mc-script-row").style.display = this.createMode === "script" ? "" : "none";
      };
      root.querySelector("#mc-mode-novel").addEventListener("change", applyMode);
      root.querySelector("#mc-mode-script").addEventListener("change", applyMode);
    },
    doCreate() {
      // 2026-08-24 修复:多级弹窗下必须限定当前弹窗(全局 $("mc-*") 会读到旧弹窗)
      const root = this._modalEl;
      if (!root) return;
      this.createName = root.querySelector("#mc-name").value.trim();
      this.createNovel = this.createMode === "script" ? "" : root.querySelector("#mc-novel").value.trim();
      this.createKey = root.querySelector("#mc-key").value.trim();
      // 剧名兜底(2026-08-24 用户要求:选目录即自动填充;LLM 直出时才需要用户填):
      // 选了小说目录/脚本目录但剧名仍为空(被手动清空)→ 自动补上
      // (createScriptFrom 可能是分镜脚本文件 → 取所在目录名;也可能是目录 → 直接取)
      if (!this.createName) {
        let src = this.createNovel || this.createScriptFrom || "";
        if (src) {
          const parts = String(src).split(/[\\/]+/).filter(Boolean);
          if (parts.length && /\.(md|txt|markdown)$/i.test(parts[parts.length - 1])) parts.pop(); // 脚本文件 → 父目录
          if (parts.length) this.autoFillCreateName(parts.join("/"), true);
        }
        this.createName = this.createName || "";
      }
      if (!this.createName) { const m = "请填剧名（或选择小说目录/脚本目录自动填充）"; root.querySelector("#mc-err").textContent = m; this.showTip(m, "error"); return; }
      if (this.createMode !== "script" && !this.createNovel) { const m = "请选择小说目录（或切换「视频脚本直出」模式）"; root.querySelector("#mc-err").textContent = m; this.showTip(m, "error"); return; }
      this.creating = true;
      root.querySelector("#mc-do").textContent = "创建中…";
      root.querySelector("#mc-do").disabled = true;
      const remember = root.querySelector("#mc-remember").checked;
      const key = this.createKey;
      const saveFirst = (remember && key)
        ? post("/api/manju/settings", { apiKey: key }).catch(() => ({}))
        : Promise.resolve({});
      saveFirst.then(() => post("/api/manju/create", { name: this.createName, novel: this.createNovel, apiKey: key })).then((r) => {
        this.creating = false;
        if (r.ok) {
          this.closeModal();
          // 新建时选的是「目录」,正文文件由创建逻辑解析写入 config;
          // 这里清空小说值,让 loadProject 从新项目 config 读到正确的正文文件(避免把目录当小说传给管线)
          this.novel = "";
          $("manju-novel").value = "";
          ls("novel", "");
          this.loadProjects(r.configPath);
          if (r.scriptMode) {
            // 2026-08-23 用户要求:视频脚本直出配置直接在新建弹窗操作——
            // 新建时粘贴的脚本自动保存并启用脚本直出;留空则提示后续配置
            const proj = String(r.configPath || "").split(/[\\/]/).filter(Boolean).slice(-2, -1)[0] || this.createName;
            if (this.createScriptFrom) {
              // 「📁 从目录检测分镜脚本」选中的 → 创建后自动导入:
              // createScriptAll=true 全部导入(目录里所有脚本每章一集),否则单项导入
              const imp = this.createScriptAll
                ? post("/api/manju/script/import-all-dir", { project: proj, episode: this.createScriptEp || "EP01", dir: this.createScriptFrom })
                : post("/api/manju/script/import-dir", { project: proj, episode: this.createScriptEp || "EP01", file: this.createScriptFrom });
              imp.then((sr) => {
                this.refreshScriptStatus();
                this.setNote(this.createScriptAll
                  ? (sr && sr.ok ? "🎬 项目已创建，已全部导入 " + (sr.imported || 0) + "/" + (sr.total || 0) + " 个分镜脚本（每章一集）" : "🎬 项目已创建（脚本导入失败，可到主页补充）")
                  : (sr && sr.ok ? "🎬 项目已创建，分镜脚本已直接导入（脚本直出已启用）" : "🎬 项目已创建（脚本导入失败，可到主页补充）"));
              }).catch(() => this.setNote("🎬 项目已创建（脚本导入失败，可到主页补充）"));
              this.createScriptFrom = ""; this.createScriptEp = ""; this.createScriptAll = false;
            } else {
              const script = (this.createScript || "").trim();
              if (script) {
                post("/api/manju/script/save", { project: proj, episode: "EP01", text: script }).then((sr) => {
                  this.refreshScriptStatus();
                  this.setNote(sr && sr.ok ? "🎬 项目已创建，脚本直出已启用（脚本已保存）" : "🎬 项目已创建（脚本直出）");
                }).catch(() => this.setNote("🎬 项目已创建（脚本直出，脚本保存失败可到主页补充）"));
              } else {
                this.setNote("🎬 项目已创建（视频脚本直出）→ 主页卡片粘贴脚本、从小说分镜脚本导入或选择目录检测");
              }
            }
          }
        } else {
          root.querySelector("#mc-do").textContent = "创建项目";
          root.querySelector("#mc-do").disabled = false;
          root.querySelector("#mc-err").textContent = (r.output || r.error || "创建失败").trim();
        }
      }).catch((e) => {
        this.creating = false;
        root.querySelector("#mc-do").textContent = "创建项目";
        root.querySelector("#mc-do").disabled = false;
        // 2026-08-24:失败必须可见(此前静默恢复按钮→用户"点了没反应")
        const msg = (e && e.message) || "创建失败";
        root.querySelector("#mc-err").textContent = msg;
        this.setNote("创建项目失败: " + msg);
      });
    },

    /* 文件选择 */
    openPicker(mode) {
      const cur = mode === "create" ? this.createNovel : (mode === "scan" || mode === "scan-create" ? this.scanDir : this.novel);
      const start = cur ? dirOf(cur) : "C:/Mi/Ai/WorkBench/novel";
      this.picker = { mode, dir: start, entries: [], error: "" };
      this.listDir(start, true); // 首次:压新层(父弹窗保留,多级弹窗)
    },
    listDir(dir, fresh) {
      get("/api/fs/list?dir=" + encodeURIComponent(dir)).then((r) => {
        this.picker.dir = r.dir || dir;
        this.picker.entries = r.items || [];
        this.picker.error = r.error || "";
        this.renderPicker(fresh);
      }).catch((e) => {
        this.picker.error = e.message;
        this.renderPicker(fresh);
      });
    },
    renderPicker(fresh) {
      const p = this.picker;
      const entries = p.entries.map((e) => {
        const isDir = e.isDir;
        const isMd = !isDir && String(e.name).toLowerCase().endsWith(".md");
        return `<div class="manju-file" data-name="${esc(e.name)}" data-dir="${isDir ? "1" : "0"}">
          <span class="manju-file-name">${isDir ? "📁" : "📄"} <span class="${isMd ? "manju-md" : ""}">${esc(e.name)}</span></span>
          <span class="manju-meta">${isDir ? "目录" : fmtSize(e.size)}</span>
        </div>`;
      }).join("");
      const adoptDir = p.mode === "create"
        ? `<button id="mp-adopt" class="hrs-btn hrs-btn-primary">选择此目录</button>`
        : (p.mode === "scan" || p.mode === "scan-create"
          ? `<button id="mp-adopt" class="hrs-btn hrs-btn-primary">检测此目录</button>`
          : "");
      const html = `<div class="manju-picker">
          <div class="manju-row">
            <span class="manju-meta manju-picker-path">${esc(p.dir)}</span>
            ${adoptDir}
            <button id="mp-up" class="hrs-btn">上级</button>
            <button id="mp-cancel" class="hrs-btn">取消</button>
          </div>
          ${p.error ? `<div class="manju-err-text">${esc(p.error)}</div>` : ""}
          <div class="manju-filelist">${entries || '<div class="manju-empty">空目录</div>'}</div>
        </div>`;
      const title = p.mode === "create" ? "选择小说目录" : (p.mode === "scan" || p.mode === "scan-create" ? "选择目录检测分镜脚本" : "选择小说文件");
      // 首次=压新层(父弹窗保留);上级/翻页=原地替换不压栈
      if (fresh) this.openModal(title, html);
      else this.rerenderModal(title, html);
      if (this._modalEl) this._modalEl.querySelectorAll(".manju-file").forEach((f) =>
        f.addEventListener("click", () => this.pickGo(f.dataset.name, f.dataset.dir === "1"))
      );
      $("mp-up").addEventListener("click", () => this.listDir(dirOf(p.dir)));
      $("mp-cancel").addEventListener("click", () => this.closeModal());
      if (p.mode === "create") {
        $("mp-adopt").addEventListener("click", () => this.pickDir(p.dir));
      } else if (p.mode === "scan") {
        $("mp-adopt").addEventListener("click", () => this.scanScriptDir(p.dir, "import"));
      } else if (p.mode === "scan-create") {
        $("mp-adopt").addEventListener("click", () => this.scanScriptDir(p.dir, "create"));
      }
    },
    pickDir(dir) {
      this.createNovel = dir;
      // 选小说目录自动填充剧名(用户手动改过则不覆盖;换目录时若未手动改则跟随更新)
      this.autoFillCreateName(dir);
      this.renderCreate();
    },
    /* 剧名自动填充:取目录名;用户未手动改(空/仍是上次自动值)才更新;force=空名也直接填 */
    autoFillCreateName(dir, force) {
      const name = String(dir).split(/[\\/]+/).filter(Boolean).pop() || "";
      if (!name) return;
      if (force || !this.createName || this.createName === this._createNameAuto) {
        this.createName = name;
        this._createNameAuto = name;
      }
    },
    pickGo(name, isDir) {
      const base = String(this.picker.dir).replace(/[\\/]+$/, "");
      const next = base + "/" + name;
      if (isDir) this.listDir(next);
      else {
        if (this.picker.mode === "create") {
          this.createNovel = next;
          this.autoFillCreateName(base); // 选小说文件所在目录自动填充剧名
          this.renderCreate();
        } else {
          this.novel = next;
          ls("novel", this.novel);
          $("manju-novel").value = this.novel;
          this.detectNovel();
          this.closeModal();
        }
      }
    },
  };

  window.ManjuWorkbench = ManjuWorkbench;

  // 2026-08-24:全局 JS 错误可见化(此前"点击没反应"= 异常被吞)
  window.addEventListener("error", (e) => {
    if (ManjuWorkbench.showTip) ManjuWorkbench.showTip("页面错误: " + (e.message || "未知错误"), "error");
  });
})();
