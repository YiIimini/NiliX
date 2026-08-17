# NiliX

本地 AI 漫剧生产工作台 —— Go 单服务集成：知识库图谱 + 小说管理 + **小说 → 漫剧全自动流水线**（MiniMax H3 R2V）+ 智能体审片返工 + 系统监测灵动岛。

一台 RTX 5090 Laptop + ComfyUI 即可完成「小说 → 分镜 → 定妆照 → 逐镜视频 → 成片」的全流程，无需打开 ComfyUI 界面。

---

## 核心能力

| 模块 | 说明 |
| --- | --- |
| **关系图谱** | 内置知识库（`zhishiku` Markdown + 双链）的可视化图谱 / 总览 / 全文检索 |
| **小说管理** | 书架浏览、阅读器（全书搜索 / 排版记忆）、一键转剧本 |
| **漫剧管理** | 六阶段流水线：方案 → 资产 → 编码 → 渲染 → 质检 → 合成；断点续跑、条件缓存、8 风格预设、8 种画幅 |
| **智能体调度** | 🤖 智能一条龙：剧本师复核 → 审片官逐镜 VLM 判分（八维度对齐 H3 官方能力）→ 修复师自动改写提示词定点返工 → 微信升级通知 |
| **系统监测** | 托盘 + 灵动岛悬浮窗（CPU/内存/GPU/磁盘/网络 + ComfyUI/Kb/ZCode/Bot 服务管控） |

## 快速开始

```bat
:: 编译(GUI 子系统,无终端黑窗;首次需 go-winres 生成图标资源)
build.bat

:: 启动(默认 8787,托盘常驻)
启动服务.bat
```

打开 `http://127.0.0.1:8787/`。开机自启：托盘菜单勾选（写 HKCU Run 键）。

### 依赖

- **Go 1.22+**（`GOPROXY=https://goproxy.cn,direct`）
- **ComfyUI**（端口 8190）：MiniMax H3（FL2VA/Ref2VA）、Z-Image、SDXL 等模型
- **FFmpeg**（合成阶段）
- **DeepSeek API Key**（剧本/方案生成；设置 → 智能体调度）
- **视觉模型**（审片官；推荐智谱 `glm-4.6v-flash` 免费，OpenAI 兼容）
- `lhmsensor/`：LibreHardwareMonitor 温度传感器（可选，缺失自动降级）

## 架构

```
main.go                 入口:工作目录切换 / 看门狗 / HTTP / 灵动岛 / 托盘
internal/
  api/                  全部 HTTP 路由
    manju*.go           漫剧工作台 18 个 /api/manju/* + 智能体端点
    manju_agent.go      智能体编排:判分/返工闭环/升级/状态落盘
    api.go              设置/脚本/渲染/comfy/fs + 静态服务(HTML no-cache)
    scripts/manju_media.py  抽帧/质检(go:embed 运行时释放)
  agent/                智能体包:vision 客户端 / 审片官 / 修复师 / 维度权重
  render/               ComfyUI 工作流驱动(Z-Image 资产 / H3 T2V / R2V)
  storyboard/           小说 → 角色卡/场景卡/镜头表/H3 提示词(DeepSeek)
  assemble/             FFmpeg 合成(concat + loudnorm + faststart)
  verify/               成片机械质检
  kb_work/              知识库解析/图谱(纯标准库)
  island/               WebView2 灵动岛窗口(置顶/圆角/透明)
  sysmon/               系统监测采集(gopsutil + nvidia-smi + LHM)
  autostart/ watchdog/  注册表自启 / 单实例互斥体
web/
  kb/                   主页面(图谱/总览/小说/漫剧/ComfyUI 五页)
  island/               灵动岛前端
  index.html            /manage/ 管理页(设置)
third_party/go-webview2 本地补丁(SetTransparent 真透明)
```

## 端口与配置

- **8787**：NiliX 主服务；**8190**：ComfyUI（由本服务代管启停）
- `settings.json`：服务配置（LLM/渲染/路径，AES-GCM 加密 Key，首次运行自动生成）
- `server/settings.json`：漫剧默认 DeepSeek Key（明文，**勿入库**）
- `<项目>/config.json`：每项目渲染配置 + `agent` 节（视觉模型/及格线/返工轮数）
- `<项目>/agent_state.json`：审片报告与升级状态（换集自动清空）

## 关键机制

- **两阶段条件缓存**：`资产指纹_a<hash>_s<镜号>.pt`——换定妆照自动失效；**改提示词必须删 .pt**（缓存名不含提示词指纹）
- **审片八维度**：主体 20 / 场景 12 / 动作 15 / 运镜 10 / 可见性 15(近黑防线) / 技术 15 / 风格 8 / 口型 5，加权分 Go 侧计算不信任模型自报
- **断点续跑**：全阶段幂等，`▶ 续跑` 从断点继续；H3 输出仅首帧 IDR，剪辑必须重编码
- **静态资源版本化**：HTML `no-cache` + 资产 `?v=` 参数，改前端必须 bump 版本号

## 官方参数基线

768×1344(9:16) / 20 步（Turbo LoRA 8 步 ≈ 2.9× 提速）/ seed 全剧固定 / 时长 4-15s / `<d>中文</d>` 原生对白。

## License

私有项目，仅限本机使用。
