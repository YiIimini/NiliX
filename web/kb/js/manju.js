/* 二级页:漫剧管理(原生工作台) —— 项目/小说/渲染配置/阶段执行/状态/产物 + 右侧成品列表收缩栏 */
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

  const INT_KEYS = ["width", "height", "fps", "steps", "turbo_steps", "seed", "min_shot_seconds", "max_shot_seconds"];
  const STR_KEYS = ["comfy_url", "unet_fl2va", "unet_ref2va", "clip", "vae_video", "vae_audio",
    "z_image_unet", "z_image_clip", "z_image_vae", "turbo_lora",
    "chapters", "episode", "shots"];

  /* 数值字段默认值(与 direct_pipeline/new_project.py 保持一致,配置缺省/为空时回填,避免输入框空白) */
  const NUM_DEFAULTS = {
    "manju-width": 768, "manju-height": 1344, "manju-fps": 24,
    "manju-steps": 20, "manju-turbo": 8, "manju-seed": 1688,
    "manju-minsec": 4, "manju-maxsec": 12, "manju-mosaic-level": 16,
    "manju-draft-scale": 0.5,
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
      "3. <b>小说来源</b>：默认带出项目配置的小说；点「选择文件」换任意小说（运行时覆盖，不改配置）",
      "4. <b>角色管理</b>：在「渲染配置」里点「角色管理」按钮，抽卡/采纳生成定妆照",
      "5. 填<b>章节</b>（如 1-3）与<b>集号</b>（EP01），点「一条龙」",
      "6. 一条龙 = 方案→资产→编码→渲染→质检→合成；中断后点「▶ 续跑」断点续跑",
    ] },
    { ic: "🎭", t: "角色管理", ps: [
      "入口在「渲染配置」卡右上角：「角色管理」按钮打开抽卡弹窗",
      "先「生成方案」产出角色列表，再逐角色抽卡",
      "抽卡 = 按角色 image_prompt + 随机 seed 生成候选定妆照，可反复抽换选最佳",
      "「采纳」把候选设为正式定妆照（覆盖旧图 → 缓存指纹失效 → 自动重新预编码/渲染）",
      "采纳后右侧栏「产物」的人物缩略图自动刷新",
      "正脸参考 <code>_face.png</code> 从定妆照切「完整头部+肩部」用于 R2V 锁脸",
    ] },
    { ic: "🎬", t: "执行管线", ps: [
      "阶段按钮带序号：⓪环境自检 ①方案 ②资产 ③编码 ④渲染 ⑤质检 ⑥合成（顺序执行）",
      "第一排单跑某阶段；「快速执行」下：<b>一条龙</b> = 全流程自动化",
      "中断后点<b>▶ 续跑</b>：从上次断点继续，已完成阶段幂等跳过",
      "点「环境自检」可先体检 ComfyUI / 模型 / 依赖是否就绪",
    ] },
    { ic: "🤖", t: "智能体调度", ps: [
      "点<b>🤖 智能一条龙</b>先弹窗询问：<b>「是」</b>= Agent 深度分析小说内容，自动推荐并更新渲染风格（可组合叠加，如 2.5D+水墨）后走全流程；<b>「否」</b>= 按当前渲染配置直接走智能一条龙",
      "智能一条龙 = 一条龙 + 智能体：渲染完成后<b>审片官逐镜判分</b>（八维度，对齐 H3 官方能力）",
      "<b>🔍 项目体检</b>：一键诊断 配置/小说/LLM/ComfyUI/模型/渲染参数/审片官，可修复项（步数/种子/帧率/时长）一键写回 config",
      "<b>💬 右栏可对智能体说话</b>：体检 / 推荐风格 / 审片报告 / 总结 / 修复，支持快捷指令按钮",
      "<b>🧠 学习档案</b>：跨次运行记忆——运行次数、审片均分趋势、高频问题、最近风格选择；阶段失败自动<b>智能诊断</b>给出原因与修复建议",
      "未达标镜头<b>自动返工</b>：修复师按审片意见改写 H3 提示词 → 删缓存定点重渲染（预算默认 2 轮，防无限重试）",
      "预算耗尽仍不达标 → <b>推送微信</b> + 右栏「审片报告」升级卡，点「重试此镜 / 忽略」人工拍板",
      "视觉模型在<b>设置 → 智能体调度</b> 配置（OpenAI 兼容；推荐智谱 <b>glm-4.6v-flash</b> 免费，<b>429 高峰自动退避重试并降级 glm-4v-flash</b>，自定义可填逗号链）；Key 顺序:项目配置 → 项目 DeepSeek → 环境变量 GLM_VISION_API_KEY",
      "「测试视觉模型」<b>随时可点</b>：没有定妆照时自动用合成测试图验证连通（约 5-10s）",
      "未配置视觉模型时自动降级：仅机械质检（黑屏/无声）+ 升级，不判分不返工",
      "<b>剧本师复核</b>在方案阶段给出节奏/台词/爽点评议（低于 60 分推送提醒），只报告不改动方案",
    ] },
    { ic: "⚙️", t: "设置与通知", ps: [
      "「设置」弹窗聚合三块：<b>智能体调度</b>（文本模型 DeepSeek Key + 视觉模型）/ <b>微信通知</b> / <b>配置管理</b>",
      "<b>智能模式开关切换即时保存</b>，勾选后刷新不回落；其余字段改动后点「保存智能体配置」",
      "微信通知选渠道后出现<b>对应官网按钮</b>（新标签打开去拿 SendKey/Token）；保存后重开设置会正确回填",
      "配置管理：导出 JSON 跨项目复用 / 导入回填表单 / 恢复默认（768×1344 / 20 步 / 2.5D）",
      "通知在<b>阶段切换</b>与<b>审片升级</b>时推送，测试按钮先落盘再实发一条",
    ] },
    { ic: "🎨", t: "渲染风格", ps: [
      "8 个预设：<b>2.5D 动漫 / 写实 / 3D CG / 二次元 / 手绘 / 纸艺 / 粘土 / 水墨</b>",
      "<b>预设可多选叠加</b>：点击即选中/取消，可同时组合多个，如 2.5D+水墨",
      "<b>自定义风格</b>：在下方输入英文描述(如 cyberpunk / pixel art)点「应用」,<b>自动叠加到当前预设</b>;与预设重复的词(如已选 2.5D 再输 2.5D)自动过滤",
      "风格为<b>提示词级注入</b>：拼成一句英文分别写入定妆照/场景图、每镜 Ref2VA 开头与空镜 [Shot 1],全片画风统一",
      "点风格区右上角 <b>「?」</b>可查看 8 个官方示例动图,并<b>详细展示当前风格解析后的英文措辞</b>(三个注入位置各一段)",
      "组合含<b>写实</b>时定妆照走 Z-Image(真人级)；改后下次运行生效(已渲染镜头不受影响)",
    ] },
    { ic: "📐", t: "渲染参数", ps: [
      "<b>画幅</b>官方 6 档：21:9 / 16:9 / 4:3 / 1:1 / 3:4 / 9:16，竖屏短剧推荐 <b>9:16（768×1344）</b>",
      "<b>档位</b>快捷切换分辨率：416P 草稿（快速试片）→ 768P 标准（默认）→ 1088P 高清，按画幅等比换算并对齐 32；选「手动宽高」则直接用上面的宽高值",
      "<b>步数</b>默认 20；<b>Turbo步</b>默认 8（8 步 ≈ 20 步画质、约 2.9 倍提速）",
      "<b>seed</b> 全剧固定保证跨镜头一致；<b>seed策略</b>控制返工：固定（默认）/ 重试递增（第 N 次返工 seed+N）/ 重试随机（返工换新随机）——返工仍抽同一 seed 等于重抽同一命运的卡",
      "<b>SageAttn</b>：SageAttention 注意力加速补丁（需 ComfyUI-KJNodes），RTX 50 系白捡提速；开启后「环境自检」会校验节点是否可用",
      "<b>草稿预审</b>（智能一条龙）：审片返工轮用缩放分辨率草稿（默认 0.5 ≈ 1/4 像素量，可调 0.2-0.95），全部落定后自动<b>全分辨率定稿重渲</b>——审片轮 GPU 时间约降 3/4，定稿零返工",
      "<b>时长</b> min/max 4–15s，大模型逐镜时长在此区间自动 clamp",
    ] },
    { ic: "🔗", t: "模型与一致性", ps: [
      "高级模型配置为<b>下拉选择</b>（从 ComfyUI 模型目录读取），避免手输误操作",
      "人物一致性 = H3 <b>R2V 参考链</b>：定妆照作为 <code>&lt;Picture 1&gt;</code> 锁脸/服装",
      "<b>定妆照按风格分流</b>：写实风格用 <b>Z-Image</b>（真人级）；其余风格用 <b>SDXL checkpoint</b>（char_models 按性别选：男 sd_xl_base / 女 animagine）",
      "场景图始终用 Z-Image；负面提示词作用于定妆照与场景图生成",
      "<b>模型名须与磁盘完全一致</b>（含大小写）；配置里的模型已缺失会标注「已缺失」",
    ] },
    { ic: "✍️", t: "H3 提示词规范", ps: [
      "有角色 = <b>六段式 Ref2VA</b>；空镜 = <b>三段式 FL2VA</b>（由引擎自动选择）",
      "对白 <code>&lt;d&gt;[中文]&lt;/d&gt;</code> 原词保留；说话者 <code>(S1)(S2)</code> 跨镜一致",
      "H3 为 <b>CFG-distilled 无负面词</b>：排除项（无水印/字幕）写进正文散文",
      "引擎已内置亮度护栏与运镜规范，无需手写",
    ] },
    { ic: "📚", t: "整本小说 → 多集", ps: [
      "「全本」把章节范围设为 1-999，引擎自动分段分集，无需手动换集号",
      "<b>按卷分集</b>：小说目录为「正文/卷一_标题/…」卷结构时（如吞天废子），每卷自动一集（EP01=卷一、EP02=卷二…）",
      "无卷结构时按内容量分段（每集约 1.8 万字，约 2-3 章）",
      "分段为确定性规则：同一本小说每次全本运行的分集完全一致，可安全续跑；已完成的集自动跳过",
      "定妆照/场景图跨集自动复用，全季人物形象一致",
    ] },
    { ic: "🎯", t: "镜头(可选)", ps: [
      "只影响<b>编码/渲染</b>：留空=全集所有镜头",
      "填 <b>1,2</b> / <b>1-3</b> / <b>1,3-5</b> 自由组合",
      "典型用途：试拍先跑 1,2 看效果 / 断点续跑只重渲失败镜 / 局部重做（覆盖 NN.mp4）",
    ] },
    { ic: "⏯", t: "断点续跑", ps: [
      "任何阶段中断，点「▶ 续跑」即可继续（方案/缓存/镜头各自幂等）",
      "只重渲失败镜头：镜头框填编号（如 7）再点渲染",
      "缓存名带「项目_集号_镜头」前缀，多项目互不串用",
    ] },
    { ic: "📺", t: "运行与质检", ps: [
      "任务后台静默运行，<b>不会弹终端窗口</b>；进度看「运行状态」卡日志实时滚动",
      "运行状态卡：横向时间轴展示 7 阶段进度 + 产物缩略图",
      "左侧「成品列表」标题右侧可<b>搜索过滤</b>项目；小说管理页也支持书架搜索与阅读器全书搜索",
      "质检标准：时长达标 / 音轨 ≥1 / 近黑帧 ≤50% / 解码正常",
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

  async function api(path, opts) {
    const r = await fetch(path, Object.assign({ cache: "no-store" }, opts || {}));
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
    style: "2.5d",
    chapters: "", episode: "", only: "", novel: "",
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
      this.chapters = ls("chapters") || "1-3";
      this.episode = ls("episode") || "EP01";
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

    setErr(msg) {
      const el = $("manju-err");
      el.textContent = msg || "";
      el.classList.toggle("hidden", !msg);
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

      // 小说
      // 小说
      $("manju-novel").addEventListener("input", (e) => { this.novel = e.target.value; ls("novel", this.novel); });
      $("manju-novel-paste").addEventListener("click", () => this.openPaste());
      $("manju-novel-pick").addEventListener("click", () => this.openPicker("novel"));
      $("manju-novel-detect").addEventListener("click", () => this.detectNovel());

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
      $("manju-style-apply").addEventListener("click", () => {
        const v = $("manju-style-custom").value.trim();
        if (!v) { this.setErr("请先输入自定义风格描述"); return; }
        this.style = this.combineCustom(v);
        $("manju-style-custom").value = ""; // 已变成标签,输入框清空待下一次输入
        this.renderStyle();
        this.saveDraft();
      });
      // 画幅
      document.querySelectorAll("#manju-ratio button").forEach((b) =>
        b.addEventListener("click", () => {
          const wh = RATIOS[b.dataset.ratio];
          if (wh) { $("manju-width").value = wh[0]; $("manju-height").value = wh[1]; }
          this.renderRatio();
          this.saveDraft();
        })
      );
      $("manju-width").addEventListener("input", () => { this.renderRatio(); this.saveDraft(); });
      $("manju-height").addEventListener("input", () => { this.renderRatio(); this.saveDraft(); });
      // 渲染配置其余字段:变化即本地记忆(input 覆盖输入框,change 覆盖下拉/复选框)
      this.renderInputIds().forEach((id) => {
        $(id).addEventListener("input", () => this.saveDraft());
        $(id).addEventListener("change", () => this.saveDraft());
      });
      $("manju-mosaic-enabled").addEventListener("change", () => this.saveDraft());
      $("manju-sage").addEventListener("change", () => this.saveDraft());
      $("manju-draft-judge").addEventListener("change", () => this.saveDraft());

      // 高级模型折叠
      $("manju-adv-toggle").addEventListener("click", () => {
        const adv = $("manju-adv");
        const open = adv.classList.toggle("hidden") === false;
        $("manju-adv-toggle").textContent = (open ? "▾ " : "▸ ") + "高级模型配置";
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

      // 阶段执行
      document.querySelectorAll("#view-manju [data-phase]").forEach((b) =>
        b.addEventListener("click", () => this.runStage(b.dataset.phase))
      );
      $("manju-resume").addEventListener("click", () => this.runResume());
      $("manju-agent-run").addEventListener("click", () => this.runAgent());
      $("manju-health").addEventListener("click", () => this.openHealth());
      $("manju-env").addEventListener("click", () => this.doEnv());
      $("manju-stop").addEventListener("click", () => this.stop());
      $("manju-clear-log").addEventListener("click", () => { $("manju-log").textContent = "(就绪)"; });

      // 弹窗
      $("manju-modal-close").addEventListener("click", () => this.closeModal());
      $("manju-modal").addEventListener("click", (e) => { if (e.target === $("manju-modal")) this.closeModal(); });
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && !$("manju-modal").classList.contains("hidden")) this.closeModal();
      });

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
      head.addEventListener("click", (e) => {
        if (e.target.closest("#manju-trailer")) return; // 预告片按钮不触发折叠
        this._setOutputsFold(!body.classList.contains("is-folded"));
      });
      if (localStorage.getItem("manju-out-collapsed") === "1") this._setOutputsFold(true);
      // 预告片:按审片分数自动剪辑高分镜头
      const tr = $("manju-trailer");
      if (tr) tr.addEventListener("click", () => this.makeTrailer());
    },

    /* 预告片自动剪辑:高分镜头掐头去尾拼接 30s(音量归一+字幕),产物落工作目录 */
    makeTrailer() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      const btn = $("manju-trailer");
      btn.disabled = true;
      btn.textContent = "剪辑中…";
      post("/api/manju/trailer", { config: this.project, episode: this.episode, target: 30 }).then((r) => {
        this.setErr("");
        $("manju-log").textContent = "(🎬 预告片已生成: " + r.file + "(选 " + r.shots + " 个高分镜头) → 工作目录 " + this.episode + "_预告片.mp4)";
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
      get("/api/manju/settings" + q)
        .catch(() => ({}))
        .then((n) => notifyP.then((nf) => agP.then((ag) => {
          if (gen !== this._modalGen) return;   // 弹窗已被关闭/切换:放弃渲染,不弹回
          this.renderSettings(Object.assign({}, n, nf), ag);
        })));
    },
    renderSettings(n, ag) {
      n = n || {};
      ag = ag || {};
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
      this.openModal("设置",
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
                const defSvc = (n.defaultBaseUrl && n.defaultModel)
                  ? n.defaultBaseUrl + " / " + n.defaultModel
                  : "https://api.deepseek.com / deepseek-chat";
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
              <div id="manju-apikey-status" class="manju-set-status">${keyStatus}</div>`;
              })()}

              <div class="manju-set-sub">👁 视觉模型 · 审片官（未配置时仅机械质检，不判分不返工）</div>
              ${(() => {
                const gd = ag.globalDefaults || {};
                // 项目缺失(config 不存在):视觉区回显全局默认(项目未配置时本就自动用全局),避免整块空白
                const projMissing = !!ag.projectMissing;
                const vModel = ag.visionModel || (projMissing ? gd.visionModel : "") || "";
                const vUrl = ag.visionBaseUrl || (projMissing ? gd.visionBaseUrl : "") || "";
                const vHasKey = ag.hasVisionKey || (projMissing ? !!gd.hasVisionKey : false);
                const presetHit = VISION_PRESETS.find((p) => p.id === vModel);
                const isCustom = vModel && !presetHit;
                return `
              ${projMissing ? '<div class="manju-set-status st-bad">⚠️ 项目 config 不存在（目录已删除/未创建），以下展示<b>全局默认</b>配置，重建项目后自动生效</div>' : ""}
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
              </div>
              <div class="manju-set-sub">☁️ 云端 2K 定稿 · MiniMax(审片通过的本地定稿镜提交云端升 2K,本地 GPU 零负担)</div>
              <div class="manju-field-row">
                <label>API Key</label>
                <input id="manju-ag-mmkey" class="manju-input manju-mono" type="password" placeholder="${ag.hasMinimaxKey ? "已保存(" + esc(ag.minimaxKeyMasked || "") + "),留空沿用" : "MiniMax 平台 API Key(产物区「☁️ 2K」按钮用)"}" spellcheck="false" autocomplete="off">
              </div>
              <div class="manju-set-status">本地 768×1344 / 24fps / 17k+5 帧网格产物与官方 /v2/video_regeneration 预校验完全兼容:提交前本地体检(32 整除/面积/帧率/帧网格/音轨/50MB),2K 产物落 clips/集/2k/。国内平台在项目 config.render.minimax_base_url 填 https://api.minimaxi.com</div>
              <div class="manju-set-status">审片八维度对齐 MiniMax H3 官方能力：主体/场景一致性(Ref2VA 参考保持)、动作/运镜符合(多模态指令遵循)、可见性(近黑防线)、技术质量(畸变/水印)、风格、口型对白。低分镜头由修复师改写 H3 提示词后自动定点重渲染（「🤖 智能一条龙」走全流程）。点「🤖 智能一条龙」会先询问是否让 Agent 深度分析小说内容并更新渲染风格（是=分析后更新；否=按当前配置直接跑）。</div>
              ${(() => {
                const g = ag.globalDefaults || {};
                if (!g.visionModel && !g.hasVisionKey && !g.enabled) return "";
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
              <div class="manju-set-grid">
                <div class="manju-set-item">
                  <div class="manju-set-item-title">导出配置</div>
                  <div class="manju-meta">把当前渲染配置(风格/数值/模型/审核)导出为 JSON 文件,可跨项目复用。</div>
                  <div class="manju-set-actions">
                    <button id="mc-export" class="hrs-btn hrs-btn-primary">导出为 JSON</button>
                  </div>
                </div>
                <div class="manju-set-item">
                  <div class="manju-set-item-title">导入配置</div>
                  <div class="manju-meta">从 JSON 文件导入渲染配置(应用到当前表单,点「保存参数」写入项目)。</div>
                  <div class="manju-set-actions">
                    <input id="mc-import-file" type="file" accept=".json,application/json" style="display:none">
                    <button id="mc-import" class="hrs-btn">选择 JSON 文件</button>
                    <span id="mc-import-msg" class="manju-meta manju-set-msg"></span>
                  </div>
                </div>
                <div class="manju-set-item">
                  <div class="manju-set-item-title">恢复默认</div>
                  <div class="manju-meta">将渲染配置恢复到默认值(768×1344 / 20步 / 2.5D 动漫等),当前项目需重新保存。</div>
                  <div class="manju-set-actions">
                    <button id="mc-reset" class="hrs-btn hrs-btn-danger">恢复默认</button>
                  </div>
                </div>
              </div>
            </div>
          </div>
          </div>
        </div>`, true);
      $("manju-apikey-save").addEventListener("click", () => this.saveApiKey(false));
      $("manju-apikey-apply").addEventListener("click", () => this.saveApiKey(true));
      $("manju-llm-svc").addEventListener("change", () => this.syncLLMForm());
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
    /* 视觉模型表单联动:预设自动带地址并提示 Key 去处,自定义时展开两行 */
    syncVisionForm() {
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
        // 体检预热:发现可修复项 → 右栏 Agent 面板出主动建议横幅(仅一次/会话)
        this._healthTipDismissed = false;
        this.loadHealth(false);
      }).catch((e) => { this.info = null; this.renderChips(); });
    },

    renderChips() {
      const el = $("manju-chips");
      if (!this.info) { el.innerHTML = ""; return; }
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
      ].filter(Boolean);
      el.innerHTML = items.map((c) => `<span class="manju-chip">${esc(c)}</span>`).join("");
    },

    /* ---- 表单回填 ---- */
    renderInputIds() {
      return ["manju-width", "manju-height", "manju-fps", "manju-steps", "manju-turbo", "manju-seed",
        "manju-minsec", "manju-maxsec", "manju-comfy-url", "manju-neg-prompt", "manju-unet-fl2va", "manju-unet-ref2va",
        "manju-clip", "manju-vae-video", "manju-vae-audio", "manju-zimage-unet", "manju-zimage-clip",
        "manju-zimage-vae", "manju-turbo-lora", "manju-turbo-lora-r2v", "manju-char-male", "manju-char-female", "manju-animagine",
        "manju-banned-words", "manju-mosaic-level", "manju-res-tier", "manju-seed-policy", "manju-draft-scale"];
    },
    draftKey() { return "render-" + (this.project || ""); },
    /* 渲染配置草稿记忆:未点「保存参数」的编辑也随刷新保留,按项目隔离 */
    saveDraft() {
      if (!this.project) return;
      const d = { style: this.style };
      this.renderInputIds().forEach((id) => { d[id] = $(id).value; });
      d.mosaicEnabled = $("manju-mosaic-enabled").checked;
      d.sageEnabled = $("manju-sage").checked;
      d.draftJudge = $("manju-draft-judge").checked;
      try { localStorage.setItem("manju-" + this.draftKey(), JSON.stringify(d)); } catch (e) {}
    },
    loadDraft() {
      try {
        const s = localStorage.getItem("manju-" + this.draftKey());
        return s ? JSON.parse(s) : null;
      } catch (e) { return null; }
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
      $("manju-seed-policy").value = "fixed";
    },

    fillForm() {
      const R = (this.info && this.info.render) || {};
      const draft = this.loadDraft();
      this.style = (draft && draft.style) || (this.info && this.info.style) || "2.5d";
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
      num("manju-draft-scale", R.draft_scale != null && R.draft_scale !== "" ? R.draft_scale : 0.5);
      // 未保存编辑优先:用草稿覆盖 config.json 的回填值
      if (draft) {
        this.renderInputIds().forEach((id) => {
          if (draft[id] !== undefined && draft[id] !== "") $(id).value = draft[id];
        });
        if (draft.mosaicEnabled !== undefined) $("manju-mosaic-enabled").checked = !!draft.mosaicEnabled;
        if (draft.sageEnabled !== undefined) $("manju-sage").checked = !!draft.sageEnabled;
        if (draft.draftJudge !== undefined) $("manju-draft-judge").checked = !!draft.draftJudge;
      }
      this.renderStyle();
      this.renderRatio();
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
      return new Set(String(this.style || "").split("+").map((s) => s.trim()).filter((s) => s && STYLE_CN[s] !== undefined));
    },

    /* 风格展示:预设转中文名、自定义词原样,多元素以 + 连接(卡片/chips 用) */
    styleLabel(style) {
      if (!style) return "";
      return String(style).split("+").map((s) => s.trim()).filter(Boolean)
        .map((s) => STYLE_CN[s] || s).join(" + ");
    },

    /* 风格切换:点击切换选中态(可多选叠加,至少保留一个);组合 key 以 + 分隔存 this.style */
    toggleStyle(key, multi) {
      const keys = this.styleKeys();
      if (multi) {
        if (keys.has(key)) keys.delete(key); else keys.add(key);
        if (keys.size === 0) keys.add(key); // 至少保留一个预设
      } else {
        keys.clear();
        keys.add(key);
      }
      this.style = [...keys].join("+");
      this.renderStyle();
      this.saveDraft();
    },

    /* 自定义风格「应用」= 累加:新输入词追加到当前风格(预设+旧自定义 TAG)之后,
    重复词自动过滤(大小写不敏感/含中文名);输入预设 key 或中文名(如 水墨)归一为
    预设 key(对应按钮点亮);删除词走 TAG 右上角 × */
    combineCustom(v) {
      const parts = String(this.style || "").split("+").map((s) => s.trim()).filter(Boolean);
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

    renderStyle() {
      const keys = this.styleKeys();
      document.querySelectorAll("#manju-style button[data-style]").forEach((b) =>
        b.classList.toggle("on", keys.has(b.dataset.style))
      );
      // 自定义风格:style 中非预设部分渲染为 TAG 标签(输入框上方,点 × 删除)
      const tags = $("manju-custom-tags");
      if (tags) {
        const nonPreset = String(this.style || "").split("+").map((s) => s.trim())
          .filter((s) => s && STYLE_CN[s] === undefined);
        tags.innerHTML = nonPreset.map((w) =>
          `<span class="style-tag">${esc(w)}<i class="style-tag-x" data-word="${esc(w)}" title="删除该风格">×</i></span>`).join("");
        tags.classList.toggle("has-tags", nonPreset.length > 0);
        tags.querySelectorAll(".style-tag-x").forEach((x) =>
          x.addEventListener("click", () => this.removeCustomWord(x.dataset.word))
        );
      }
    },

    /* 删除一个自定义风格 TAG:从 style 组合中移除该词;删空且无预设时回退默认 2.5D */
    removeCustomWord(word) {
      const parts = String(this.style || "").split("+").map((s) => s.trim()).filter(Boolean);
      const rest = parts.filter((p) => p !== word);
      this.style = rest.length ? rest.join("+") : "2.5d";
      this.renderStyle();
      this.saveDraft();
    },

    renderRatio() {
      const w = Number($("manju-width").value), h = Number($("manju-height").value);
      document.querySelectorAll("#manju-ratio button").forEach((b) => {
        const wh = RATIOS[b.dataset.ratio];
        b.classList.toggle("on", !!wh && wh[0] === w && wh[1] === h);
      });
    },

    /* 高级模型配置：从 ComfyUI 模型目录加载可选模型，填充下拉（避免手输误操作） */
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
          el.textContent = "共 " + r.count + " 章（第 " + r.first + "–" + r.last + " 章）· " + (r.chars / 10000).toFixed(2) + " 万字" + (overridden ? " · 已覆盖默认配置" : "");
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
      body.banned_words = this.strVal("manju-banned-words").split("\n").map((s) => s.trim()).filter(Boolean);
      body.mosaic_enabled = $("manju-mosaic-enabled").checked;
      body.mosaic_level = this.intVal("manju-mosaic-level");
      body.res_tier = this.strVal("manju-res-tier");
      body.seed_policy = this.strVal("manju-seed-policy") || "fixed";
      body.sage_attention = $("manju-sage").checked;
      body.draft_judge = $("manju-draft-judge").checked;
      const ds = parseFloat($("manju-draft-scale").value);
      if (!isNaN(ds)) body.draft_scale = ds;
      return body;
    },

    /* 应用渲染配置到表单(导出/恢复默认后回填) */
    applyRenderConfig(cfg) {
      if (!cfg) return;
      const R = cfg.render || cfg;
      this.style = cfg.style || this.style;
      const set = (id, v) => { if (v !== undefined && v !== null && v !== "") $(id).value = v; };
      INT_KEYS.forEach((k) => set("manju-" + { width: "width", height: "height", fps: "fps", steps: "steps", turbo_steps: "turbo", seed: "seed", min_shot_seconds: "minsec", max_shot_seconds: "maxsec" }[k], R[k]));
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
      set("manju-draft-scale", R.draft_scale != null && R.draft_scale !== "" ? R.draft_scale : 0.5);
      this.renderStyle();
      this.renderRatio();
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
          this.saveDraft();
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
      // 恢复默认:清空草稿 + 用 NUM_DEFAULTS 回填表单 + 默认风格
      this.clearDraft();
      this.style = "2.5d";
      this.clearForm();
      this.renderStyle();
      this.renderRatio();
      this.closeModal();
      const msg = $("manju-render-msg");
      if (msg) { msg.textContent = "✅ 已恢复默认值(点「保存参数」写入项目)"; setTimeout(() => { msg.textContent = ""; }, 3000); }
    },

    saveRender() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      const body = this.collectRenderConfig();
      body.config = this.project;
      const msg = $("manju-render-msg");
      msg.textContent = "保存中…";
      post("/api/manju/render", body).then((r) => {
        if (r.ok) {
          msg.textContent = "✅ 参数已保存到 config.json";
          msg.style.color = "";
          this.info = Object.assign({}, this.info, { render: r.render, style: r.style, moderation: r.moderation });
          this.clearDraft();
          this.renderChips();
          setTimeout(() => { msg.textContent = ""; }, 3000);
        } else msg.textContent = "❌ " + (r.error || "保存失败");
      }).catch((e) => { msg.textContent = "❌ " + e.message; });
    },

    mapInt(k) {
      const m = {
        width: "manju-width", height: "manju-height", fps: "manju-fps", steps: "manju-steps",
        turbo_steps: "manju-turbo", seed: "manju-seed", min_shot_seconds: "manju-minsec",
        max_shot_seconds: "manju-maxsec",
      };
      const v = $(m[k]).value;
      return v === "" ? undefined : parseInt(v, 10);
    },
    mapStr(k) {
      const m = {
        comfy_url: "manju-comfy-url", neg_prompt: "manju-neg-prompt", unet_fl2va: "manju-unet-fl2va", unet_ref2va: "manju-unet-ref2va",
        clip: "manju-clip", vae_video: "manju-vae-video", vae_audio: "manju-vae-audio",
        z_image_unet: "manju-zimage-unet", z_image_clip: "manju-zimage-clip", z_image_vae: "manju-zimage-vae",
        turbo_lora: "manju-turbo-lora", turbo_lora_r2v: "manju-turbo-lora-r2v",
        chapters: "manju-chapters", episode: "manju-episode", shots: "manju-only",
      };
      return $(m[k]).value.trim();
    },

    /* ---- 阶段执行 ---- */
    runStage(phase) {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      $("manju-log").textContent = "(启动 " + phase + " ...)";
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: phase, only: this.only, novel: this.novel,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* 一键续跑:从上次中断处继续(= 一条龙,管线幂等自动跳过已完成阶段) */
    runResume() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      const last = this.status && this.status.currentStage ? this.status.currentStage : "";
      const hint = last ? "，上次中断于「" + last + "」阶段" : "";
      $("manju-log").textContent = "(▶ 续跑启动" + hint + "，幂等跳过已完成阶段 ...)";
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: "all", only: this.only, novel: this.novel,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* 智能一条龙:先询问是否让 Agent 深度分析小说并更新渲染配置(主要是风格),再走全流程 */
    runAgent() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      this.openModal("🤖 智能一条龙",
        `<div class="manju-confirm">
          <p class="mc-q">Agent 会自动检测小说内容深度分析，调整更新渲染配置参数？</p>
          <p class="mc-d">「是」：Agent 深度分析本章节内容，自动推荐并更新渲染风格（支持预设组合，如 2.5D+水墨），随后走渲染流程；<br>「否」：按当前渲染配置直接走智能一条龙（剧本复核 → 渲染 → 审片判分 → 自动返工）。</p>
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
        this.saveDraft();
        return r;
      });
    },

    /* 「是」分支:深度分析 → 更新渲染风格 → 走智能一条龙 */
    agentStyleThenRun() {
      $("manju-log").textContent = "(🤖 深度分析小说内容，推荐并更新渲染风格 ...)";
      this.styleAnalyze().then((r) => {
        $("manju-log").textContent = "(🤖 风格已更新：" + this.styleLabel(r.old) + " → " + this.styleLabel(r.style) +
          (r.reason ? "，" + r.reason : "") + "，走渲染流程 ...)";
        this.runAgentFlow();
      }).catch((e) => this.setErr("深度分析失败：" + e.message));
    },

    /* 智能一条龙本体:一条龙 + 智能体调度(剧本复核 → 渲染 → 审片判分 → 自动返工 → 例外升级) */
    runAgentFlow() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      $("manju-log").textContent = "(🤖 智能一条龙启动: 剧本复核 → 渲染 → 审片官判分 → 未达标自动返工 ...)";
      post("/api/manju/run", {
        config: this.project, chapters: this.chapters, episode: this.episode,
        phase: "all", only: this.only, novel: this.novel, agent: true,
      }).then(() => { this.poll(); }).catch((e) => this.setErr(e.message));
    },

    /* ---- 项目体检:全项诊断 + 一键修复 ---- */
    openHealth() {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      this.openModal("🔍 项目体检", '<div class="mj-health"><div class="mj-health-load">🤖 智能体正在体检项目…</div></div>', true);
      this.loadHealth(true, this._modalGen);
    },
    loadHealth(showModal, gen) {
      const render = (items) => {
        // 代次守卫:弹窗已被关闭/切换 → 放弃渲染,不弹回
        if (gen !== undefined && gen !== this._modalGen) return;
        const n = { ok: 0, warn: 0, bad: 0 };
        items.forEach((it) => n[it.status]++);
        const ic = { ok: "✅", warn: "⚠️", bad: "❌" };
        const body = `<div class="mj-health">
          <div class="mj-health-head">🤖 智能体检 · <b class="mj-hb-bad">${n.bad} 项异常</b> / <b class="mj-hb-warn">${n.warn} 项建议</b> / ${n.ok} 项正常</div>
          <div class="mj-health-items">
            ${items.map((it) => `
            <div class="mj-health-item ${it.status}">
              <div class="mj-hi-main">
                <span class="mj-hi-ic">${ic[it.status] || "•"}</span>
                <b>${esc(it.label)}</b>
                <span class="mj-hi-detail">${esc(it.detail)}</span>
              </div>
              ${it.fixable ? `<button class="hrs-btn hrs-btn-primary mj-hi-fix" data-fix="${esc(it.key)}">一键修复</button>` : ""}
              ${(!it.fixable && it.fixHint) ? `<span class="mj-hi-hint">${esc(it.fixHint)}</span>` : ""}
            </div>`).join("")}
          </div>
          <div class="manju-meta" style="margin-top:10px">体检为本地秒查(不调用模型);「一键修复」直接写回 config.json 渲染配置。</div>
        </div>`;
        if (showModal) this.openModal("🔍 项目体检", body, true);
        this._health = items;
        this._healthFix = items.filter((it) => it.fixable && it.status !== "ok");
        if (!showModal) this.renderAgent(); // 非弹窗模式(预热):刷新右栏面板出建议横幅
        // 修复按钮绑定(弹窗刚生成时)
        document.querySelectorAll("#manju-modal .mj-hi-fix").forEach((b) =>
          b.addEventListener("click", () => this.fixHealth(b.dataset.fix, b))
        );
      };
      if (showModal) {
        get("/api/manju/agent/health?config=" + encodeURIComponent(this.project)).then((r) => render(r.items || [])).catch((e) => {
          if (gen !== undefined && gen !== this._modalGen) return;
          this.openModal("🔍 项目体检", '<div class="mj-health"><div class="mj-health-load">体检失败: ' + esc(e.message) + '</div></div>', true);
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
        this.loadHealth(true, gen);
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

    doEnv() {
      if (!this.project) return;
      $("manju-env-card").classList.remove("hidden");
      $("manju-env-out").textContent = "(运行环境自检...)";
      post("/api/manju/env", { config: this.project }).then((r) => {
        $("manju-env-out").textContent = (r.output || "") + "\n[exit " + r.exitCode + "]";
      }).catch((e) => { $("manju-env-out").textContent = "错误: " + e.message; });
    },

    stop() {
      if (this.stopping) return;
      this.stopping = true;
      const btn = $("manju-stop");
      btn.textContent = "停止中…";
      post("/api/manju/kill", {}).then((r) => {
        btn.textContent = "停止";
        this.stopping = false;
        $("manju-log").textContent += "\n⏹ 已发送停止，正在结束进程树…";
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
        this.reportMascot(s);
      }).catch(() => {});
    },

    /* 悬浮助手云朵:汇总逻辑统一在 App.mascotStatus */
    reportMascot(s) {
      if (typeof App !== "undefined" && App.mascotStatus) App.mascotStatus(s);
    },
    renderStatus() {
      const s = this.status;
      const dot = document.querySelector("#manju-status .hrs-dot");
      const txt = $("manju-txt");
      const badge = $("manju-badge");
      const stopBtn = $("manju-stop");
      const log = $("manju-log");

      if (s.running) {
        dot.className = "hrs-dot on";
        txt.textContent = "运行中 · " + s.stage;
        badge.className = "manju-badge manju-badge-run";
        badge.innerHTML = '<span class="manju-spinner"></span><span>' + s.stage + " 运行中 " + fmtTime(s.elapsedSec) + "</span>";
        stopBtn.disabled = false;
      } else {
        dot.className = "hrs-dot off";
        txt.textContent = "空闲";
        stopBtn.disabled = true;
        if (s.stopped) {
          badge.className = "manju-badge manju-badge-stop";
          badge.textContent = "⏹ 已手动停止 · 总耗时 " + fmtTime(s.elapsedSec);
        } else if (s.rc !== null && s.rc !== undefined) {
          if (s.rc === 0) { badge.className = "manju-badge manju-badge-ok"; badge.textContent = "✅ 上次任务成功 · 总耗时 " + fmtTime(s.elapsedSec); }
          else { badge.className = "manju-badge manju-badge-err"; badge.textContent = "❌ 上次任务 rc=" + s.rc + " · 总耗时 " + fmtTime(s.elapsedSec); }
        } else {
          badge.className = "manju-badge manju-badge-idle";
          badge.textContent = "空闲";
        }
      }
      if (s.logTail !== undefined && s.logTail !== log.dataset.last) {
        log.textContent = s.logTail || "(就绪)";
        log.dataset.last = s.logTail;
        log.scrollTop = log.scrollHeight;
      }
      this.renderProgress();
      this.renderFlow();
      this.renderAgent();
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

    /* 管线流程图(垂直步进) */
    renderFlow() {
      const el = $("manju-flow");
      if (!el) return;
      const s = this.status || {};
      const cur = s.currentStage || "";
      let curIdx = FLOW.findIndex((f) => f.key === cur);
      const running = !!s.running;
      const failed = s.done && s.rc !== null && s.rc !== undefined && s.rc !== 0;

      el.innerHTML = `<div class="manju-flow">` + FLOW.map((f, i) => {
        let cls = "pending", mark = "";
        if (i < curIdx) { cls = "done"; mark = "✓"; }
        else if (i === curIdx) {
          if (running) cls = "current";
          else if (failed) { cls = "failed"; mark = "✗"; }
          else { cls = "done"; mark = "✓"; }
        }
        if (!running && s.done && s.rc === 0) { cls = "done"; mark = "✓"; }
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
      const adopted = (this.outputs && this.outputs.characters) || [];
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
      if (!this.project) return;
      this.renderGachaModal();          // 先渲染(可能已有 plan)
      this.loadPlan(() => this.renderGachaModal()); // 拉最新方案再刷一次
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

    renderGachaModal() {
      const p = this.plan || {};
      const currentChars = (this.outputs && this.outputs.characters) || [];
      const findCurrent = (id) => {
        const m = currentChars.find((c) => (c.name || "").replace(/\.[^.]+$/, "") === id);
        return m ? m.path : "";
      };
      const fileUrl = (p2) => "/api/fs/file?path=" + encodeURIComponent(p2);
      let html = "";
      if (!p.exists) {
        html = `<div class="manju-empty">
          <div>📋 暂无角色方案</div>
          <div class="manju-meta" style="margin-top:4px">抽卡需要先由「方案」阶段生成角色列表</div>
          <button id="mg-plan" class="hrs-btn hrs-btn-primary" style="margin-top:10px">▶ 生成方案</button>
        </div>`;
      } else {
        const chars = p.characters || [];
        html = chars.length
          ? `<div class="manju-gacha-grid">` + chars.map((c) => {
              const g = this.gacha[c.id] || {};
              const img = g.image || findCurrent(c.id);
              const preview = img ? `<img class="manju-char-img" src="${fileUrl(img)}" alt="${esc(c.id)}" data-img="${esc(img)}" data-name="${esc(c.id)}" title="点击预览大图">` : '<div class="manju-gacha-ph">未抽卡</div>';
              return `<div class="manju-char">
                <div class="manju-char-head">
                  <span class="manju-char-name">${esc(c.id)}</span>
                  <span class="manju-char-tag">${esc(c.gender || "")}${c.age ? "·" + esc(c.age) : ""}</span>
                </div>
                <div class="manju-char-preview">${preview}</div>
                <div class="manju-char-actions">
                  <button class="hrs-btn" data-gacha="${esc(c.id)}">🎲 抽卡</button>
                  <button class="hrs-btn" data-upload="${esc(c.id)}" title="上传本地角色图并采纳为正式定妆照">📤 上传</button>
                  <button class="hrs-btn hrs-btn-primary" data-adopt="${esc(c.id)}" ${g.image ? "" : "disabled"}>采纳</button>
                </div>
              </div>`;
            }).join("") + `</div>
          <div class="manju-meta" style="margin-top:8px">📤 上传：选择本地图片，保存为正式定妆照并自动生成正脸参考，后续渲染以此为准。</div>
          <input id="manju-upload-file" type="file" accept="image/png,image/jpeg,image/webp" style="display:none">`
          : '<div class="manju-empty">方案中无角色</div>';
      }
      this.openModal("角色抽卡", html, true);
      const planBtn = $("mg-plan");
      if (planBtn) planBtn.addEventListener("click", () => this.genCharacters());
      document.querySelectorAll("#manju-modal [data-gacha]").forEach((b) =>
        b.addEventListener("click", () => this.drawGacha(b.dataset.gacha, b))
      );
      document.querySelectorAll("#manju-modal [data-upload]").forEach((b) =>
        b.addEventListener("click", () => this.uploadCharPick(b.dataset.upload))
      );
      const upFile = $("manju-upload-file");
      if (upFile) upFile.addEventListener("change", (e) => this.uploadCharFile(e.target.files[0]));
      document.querySelectorAll("#manju-modal [data-adopt]").forEach((b) =>
        b.addEventListener("click", () => this.adoptGacha(b.dataset.adopt))
      );
      // 角色照片点击预览大图
      document.querySelectorAll("#manju-modal .manju-char-img").forEach((el) =>
        el.addEventListener("click", () => this.previewImage(el.dataset.img, el.dataset.name))
      );
    },

    drawGacha(charId, btn) {
      if (!this.project) return;
      btn.textContent = "抽卡中…";
      btn.disabled = true;
      post("/api/manju/gacha", { config: this.project, episode: this.episode, char: charId }).then((r) => {
        btn.textContent = "🎲 抽卡";
        btn.disabled = false;
        if (r.ok) {
          this.gacha[charId] = { image: r.image, seed: r.seed };
          this.renderGachaModal();
        } else this.setErr((r.error || "抽卡失败").trim());
      }).catch((e) => {
        btn.textContent = "🎲 抽卡";
        btn.disabled = false;
        this.setErr(e.message);
      });
    },

    adoptGacha(charId) {
      const g = this.gacha[charId];
      if (!this.project || !g || !g.image) return;
      post("/api/manju/gacha/adopt", { config: this.project, episode: this.episode, char: charId, image: g.image }).then((r) => {
        if (r.ok) {
          g.adopted = true;
          this.renderGachaModal();
          this.refreshOutputs();
        } else this.setErr((r.error || "采纳失败").trim());
      }).catch((e) => this.setErr(e.message));
    },

    /* 上传本地角色图:选图后直接采纳为正式定妆照(自动生成正脸参考) */
    uploadCharPick(charId) {
      this._uploadChar = charId;
      const f = $("manju-upload-file");
      if (!f) return;
      f.value = "";
      f.click();
    },
    uploadCharFile(file) {
      if (!file || !this._uploadChar) return;
      const charId = this._uploadChar;
      this._uploadChar = "";
      if (this.status.running) { this.setErr("任务运行中，请结束后再上传角色图"); return; }
      const fd = new FormData();
      fd.append("config", this.project);
      fd.append("episode", this.episode);
      fd.append("char", charId);
      fd.append("file", file);
      fetch("/api/manju/gacha/upload", { method: "POST", body: fd, cache: "no-store" })
        .then((r) => r.json())
        .then((r) => {
          if (r.ok) {
            this.gacha[charId] = { image: r.image, seed: 0, adopted: true };
            this.renderGachaModal();
            this.refreshOutputs();
          } else this.setErr("上传采纳失败: " + (r.error || ""));
        })
        .catch((e) => this.setErr("上传失败: " + e.message));
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
      // 人物只显示正式定妆照(排除 _face.png 正脸参考,避免重复)
      const chars = (o.characters || []).filter((c) => !/_face\./i.test(c.name || ""));
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
      const renderEp = (ep) => {
        const clips = ep.clips || [];
        const ups = ep.upscaled || [];
        const final = ep.final;
        const vids = (final ? [final] : []).concat(clips);
        if (!vids.length && !ups.length) return "";
        const arts = Object.keys(ep.artifacts || {});
        const secKey = "video-" + (ep.episode || "");
        let h = `<div class="manju-out-sec${this._secFolded(secKey) ? " is-folded" : ""}" data-sec="${secKey}">`;
        h += `<div class="manju-out-title"><span class="manju-sec-foldbtn">${this._secFolded(secKey) ? "▸" : "▾"}</span>🎬 ${esc(ep.episode)} <span class="manju-out-count">${clips.length} 镜头${final ? " · 成片" : ""}${ups.length ? " · ☁️2K×" + ups.length : ""}</span>${clips.length ? `<button class="hrs-btn manju-up2k-btn" data-up2k="${esc(ep.episode || "")}" title="云端 2K 定稿:本地定稿镜提交 MiniMax 升 2K(需在设置里填 MiniMax Key),产物落 clips/${esc(ep.episode || "")}/2k/">☁️ 2K</button>` : ""}</div>`;
        h += `<div class="manju-sec-body">`;
        h += `<div class="manju-vids">${vids.map((v) => {
          const isFinal = v === final;
          return `<div class="manju-vid${isFinal ? " manju-vid-final" : ""}" data-video="${esc(v.path)}" data-name="${esc(v.name)}" title="播放 ${esc(v.name)}"><span class="manju-vid-play">▶</span><span class="manju-vid-name">${esc(v.name)}</span><span class="manju-meta">${isFinal ? "成片 · " : ""}${fmtSize(v.size)}</span><button class="manju-vid-menu" data-menu="${esc(v.path)}" data-ep="${esc(ep.episode || "")}" data-isfinal="${isFinal ? "1" : ""}" title="更多操作">⋮</button></div>`;
        }).join("")}</div>`;
        if (ups.length) h += `<div class="manju-vids" style="margin-top:6px">${ups.map((v) => `<div class="manju-vid manju-vid-2k" data-video="${esc(v.path)}" data-name="${esc(v.name)}" title="播放 ${esc(v.name)}(云端 2K)"><span class="manju-vid-play">▶</span><span class="manju-vid-name">☁️ ${esc(v.name)}</span><span class="manju-meta">2K · ${fmtSize(v.size)}</span><button class="manju-vid-menu" data-menu="${esc(v.path)}" data-ep="${esc(ep.episode || "")}" data-isfinal="0" title="更多操作">⋮</button></div>`).join("")}</div>`;
        if (arts.length) h += `<div class="manju-meta" style="margin-top:6px">📋 ${arts.map(esc).join("、")}</div>`;
        h += `</div></div>`;
        return h;
      };
      html += eps.map(renderEp).join("");

      // 仅出方案尚未渲染的集
      const plannedOnly = eps.filter((ep) => !(ep.clips || []).length && !ep.final).map((ep) => ep.episode);
      if (plannedOnly.length) {
        html += `<div class="manju-meta" style="margin-top:8px">📋 已出方案未渲染：${plannedOnly.map(esc).join("、")}</div>`;
      }

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
      $("manju-outputs").querySelectorAll(".manju-up2k-btn").forEach((btn) =>
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.startUpscale(btn.dataset.up2k, "");
        })
      );
    },

    /* 云端 2K 定稿:整集(shots 空)或指定镜头;后台任务,进度走运行日志 */
    startUpscale(ep, shots) {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      if (this.status.running) { this.setErr("已有任务运行中，先停止"); return; }
      this.setErr("");
      $("manju-log").textContent = "(☁️ 云端 2K 定稿提交中 ...)";
      post("/api/manju/upscale2k", {
        config: this.project, episode: ep || this.episode, shots: shots || "",
      }).then(() => { this.poll(); })
        .catch((e) => this.setErr(e.message));
    },

    /* 产物操作弹窗:镜头文件附「云端 2K」入口;删除支持 文件级(单 mp4)/ 集级 */
    openDeleteMenu(path, ep, isFinal) {
      if (!this.project) { this.setErr("请先选择项目"); return; }
      const fname = path.split(/[\\/]/).pop() || "";
      const isShot = /^\d+\.mp4$/i.test(fname); // 本地定稿镜头(非成片/预告片/2K 产物)
      this.openModal("🛠 产物操作",
        `<div class="manju-confirm">
          <p class="mc-q">要做什么?</p>
          <p class="mc-d">文件:${esc(fname)}${isFinal ? "(成片)" : isShot ? "(镜头)" : ""}${ep ? "<br>集:${esc(ep)}" : ""}</p>
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
        $("manju-log").textContent = "(🗑 已删除:" + (r.removed || []).join("、") + ")";
        this.refreshOutputs();
      }).catch((e) => {
        btns.forEach((b) => { if (b) b.disabled = false; });
        this.setErr(e.message);
      });
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
      get("/api/manju/style?style=" + encodeURIComponent(this.style || "2.5d")).then((r) => {
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
      this.renderPreview();
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
      this.openModal(`${cur.name || "预览"}（${i + 1}/${list.length}）`,
        `<div class="manju-img-preview">
          ${arrows}
          <div class="manju-pv-stage">
            <img src="/api/fs/file?path=${encodeURIComponent(cur.path)}" alt="">
            ${list.length > 1 ? `<span class="manju-pv-count">${i + 1} / ${list.length}</span>` : ""}
          </div>
        </div>`);
      const prev = $("mp-prev"), next = $("mp-next");
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
        if ($("manju-modal").classList.contains("hidden")) return;
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

    /* ---- 弹窗 ---- */
    openModal(title, bodyHtml, wide) {
      if (!this._bound) this.bind();   // 自愈:任何页面(未进漫剧页)调用弹窗都先绑定关闭/遮罩/Esc
      // 代次守卫:任何开/关弹窗都会使挂起的异步渲染(设置/体检)失效,
      // 防止"请求完成时弹窗已被关闭 → 又弹回来"的关闭失效竞态
      this._modalGen = (this._modalGen || 0) + 1;
      $("manju-modal-title").textContent = title;
      $("manju-modal-body").innerHTML = bodyHtml;
      const panel = document.querySelector("#manju-modal .manju-modal-panel");
      if (panel) panel.classList.toggle("wide", !!wide);
      $("manju-modal").classList.remove("hidden");
    },
    closeModal() {
      this._modalGen = (this._modalGen || 0) + 1;   // 关闭即作废所有挂起的异步渲染
      if (this._pvKey) {
        document.removeEventListener("keydown", this._pvKey);
        this._pvKey = null;
      }
      const v = $("manju-modal-body").querySelector("video");
      if (v) { v.pause(); v.removeAttribute("src"); }
      $("manju-modal").classList.add("hidden");
      this.picker = null;
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
    openPaste() {
      this.pasteTitle = ""; this.pasteText = ""; this.pasting = false;
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

    /* 新建项目 */
    openCreate() {
      this.createName = ""; this.createNovel = ""; this.createKey = ""; this.createErr = ""; this.creating = false; this.savedKey = "";
      get("/api/manju/settings").then((r) => { if (r && r.hasKey && r.masked) this.savedKey = r.masked; this.renderCreate(); }).catch(() => this.renderCreate());
    },
    renderCreate() {
      const saved = this.savedKey ? "（已存默认: " + esc(this.savedKey) + "）" : "";
      this.openModal("新建项目",
        `<div class="manju-form">
          <label>剧名（项目目录名）</label>
          <input id="mc-name" class="manju-input" placeholder="如：吞灵帝尊" value="${esc(this.createName)}">
          <label>小说目录（含正文/设定/大纲）</label>
          <div class="manju-row">
            <input id="mc-novel" class="manju-input manju-wide" placeholder="选择小说目录或粘贴路径（自动识别 正文/设定集/分卷大纲）" value="${esc(this.createNovel)}">
            <button id="mc-novel-pick" class="hrs-btn">选择目录</button>
          </div>
          <label>DeepSeek API Key${saved}</label>
          <input id="mc-key" class="manju-input" placeholder="${this.savedKey ? "留空自动用默认 Key" : "sk-...（留空则用已保存的默认 Key）"}">
          <label class="manju-check"><input id="mc-remember" type="checkbox" checked> 记住为默认 Key（下次新建自动使用）</label>
          <div id="mc-err" class="manju-err-text"></div>
          <div class="manju-row">
            <button id="mc-cancel" class="hrs-btn">取消</button>
            <button id="mc-do" class="hrs-btn hrs-btn-primary">创建项目</button>
          </div>
        </div>`);
      $("mc-name").addEventListener("input", (e) => { this.createName = e.target.value; });
      $("mc-novel").addEventListener("input", (e) => { this.createNovel = e.target.value; });
      $("mc-key").addEventListener("input", (e) => { this.createKey = e.target.value; });
      $("mc-novel-pick").addEventListener("click", () => this.openPicker("create"));
      $("mc-cancel").addEventListener("click", () => this.closeModal());
      $("mc-do").addEventListener("click", () => this.doCreate());
    },
    doCreate() {
      this.createName = $("mc-name").value.trim();
      this.createNovel = $("mc-novel").value.trim();
      this.createKey = $("mc-key").value.trim();
      if (!this.createName) { $("mc-err").textContent = "请填剧名"; return; }
      if (!this.createNovel) { $("mc-err").textContent = "请选择小说目录"; return; }
      this.creating = true;
      $("mc-do").textContent = "创建中…";
      $("mc-do").disabled = true;
      const remember = $("mc-remember").checked;
      const key = this.createKey;
      const saveFirst = (remember && key)
        ? post("/api/manju/settings", { apiKey: key }).catch(() => ({}))
        : Promise.resolve({});
      saveFirst.then(() => post("/api/manju/create", { name: this.createName, novel: this.createNovel, apiKey: key })).then((r) => {
        this.creating = false;
        if (r.ok) {
          this.closeModal();
          // 新建时选的是「目录」,正文文件由 new_project.py 解析写入 config;
          // 这里清空小说值,让 loadProject 从新项目 config 读到正确的正文文件(避免把目录当小说传给管线)
          this.novel = "";
          $("manju-novel").value = "";
          ls("novel", "");
          this.loadProjects(r.configPath);
        } else {
          $("mc-do").textContent = "创建项目";
          $("mc-do").disabled = false;
          $("mc-err").textContent = (r.output || r.error || "创建失败").trim();
        }
      }).catch((e) => {
        this.creating = false;
        $("mc-do").textContent = "创建项目";
        $("mc-do").disabled = false;
        $("mc-err").textContent = "创建失败: " + e.message;
      });
    },

    /* 文件选择 */
    openPicker(mode) {
      const cur = mode === "create" ? this.createNovel : this.novel;
      const start = cur ? dirOf(cur) : "C:/Mi/Ai/WorkBench/novel";
      this.picker = { mode, dir: start, entries: [], error: "" };
      this.listDir(start);
    },
    listDir(dir) {
      get("/api/fs/list?dir=" + encodeURIComponent(dir)).then((r) => {
        this.picker.dir = r.dir || dir;
        this.picker.entries = r.items || [];
        this.picker.error = r.error || "";
        this.renderPicker();
      }).catch((e) => {
        this.picker.error = e.message;
        this.renderPicker();
      });
    },
    renderPicker() {
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
        : "";
      this.openModal(p.mode === "create" ? "选择小说目录" : "选择小说文件",
        `<div class="manju-picker">
          <div class="manju-row">
            <span class="manju-meta manju-picker-path">${esc(p.dir)}</span>
            ${adoptDir}
            <button id="mp-up" class="hrs-btn">上级</button>
            <button id="mp-cancel" class="hrs-btn">取消</button>
          </div>
          ${p.error ? `<div class="manju-err-text">${esc(p.error)}</div>` : ""}
          <div class="manju-filelist">${entries || '<div class="manju-empty">空目录</div>'}</div>
        </div>`);
      document.querySelectorAll("#manju-modal .manju-file").forEach((f) =>
        f.addEventListener("click", () => this.pickGo(f.dataset.name, f.dataset.dir === "1"))
      );
      $("mp-up").addEventListener("click", () => this.listDir(dirOf(p.dir)));
      $("mp-cancel").addEventListener("click", () => this.closeModal());
      if (p.mode === "create") {
        $("mp-adopt").addEventListener("click", () => this.pickDir(p.dir));
      }
    },
    pickDir(dir) {
      this.createNovel = dir;
      // 剧名自动填充目录名（用户可再改）
      if (!this.createName) {
        this.createName = String(dir).split(/[\\/]+/).filter(Boolean).pop() || "";
      }
      this.renderCreate();
    },
    pickGo(name, isDir) {
      const base = String(this.picker.dir).replace(/[\\/]+$/, "");
      const next = base + "/" + name;
      if (isDir) this.listDir(next);
      else {
        if (this.picker.mode === "create") {
          this.createNovel = next;
          if (!this.createName) {
            this.createName = String(base).split(/[\\/]+/).filter(Boolean).pop() || "";
          }
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
})();
