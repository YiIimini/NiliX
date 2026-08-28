/* 二级页:Comfy UI(服务状态 + 完整内嵌 Web UI iframe) */
const ComfyView = {
  online: false,
  frameLoaded: false, // iframe 是否已注入真实地址(懒加载,首次在线后注入)
  frameReady: false, // 当前注入内容是否已加载完成(加载中显示暗色占位,消除白闪)
  starting: false, // 启动中(按钮禁用,状态行显示启动中)
  stopping: false, // 停止中

  COMFY_URL: "http://127.0.0.1:8190",

  enter() {
    this.bindControls();
    this.renderStatus();
    this.startPolling();
  },

  leave() {
    // 审计 2026-08-28:离开 Comfy 页停表——此前定时器常驻仅靠 is-active 短路,
    // 与 manju 页 enter/leave 对称清理不一致,长会话下两个定时器空转
    if (this._timer) { clearInterval(this._timer); this._timer = null; }
    if (this._logTimer) { clearInterval(this._logTimer); this._logTimer = null; }
  },

  bindControls() {
    if (this._bound) return;
    this._bound = true;
    const $ = (id) => document.getElementById(id);
    $("cfy-refresh").addEventListener("click", () => this.renderStatus());
    $("cfy-start").addEventListener("click", () => this.startService());
    $("cfy-start-big").addEventListener("click", () => this.startService());
    $("cfy-stop").addEventListener("click", () => this.stopService());
    $("cfy-open").addEventListener("click", () => {
      if (this.online) window.open(this.comfyUrl(), "_blank");
    });
    $("cfy-log-btn").addEventListener("click", () => this.toggleLog());
    // 2026-08-26 底部常驻日志行:点击同样展开/收起完整面板
    const logBar = document.getElementById("cfy-log-bar");
    if (logBar) logBar.addEventListener("click", () => this.toggleLog());
    // 内嵌页面加载完成(或失败)后收起加载占位
    const frame = document.getElementById("cfy-frame");
    frame.addEventListener("load", () => this.onFrameReady());
    frame.addEventListener("error", () => this.onFrameReady());
  },

  onFrameReady() {
    this.frameReady = true;
    const loading = document.getElementById("cfy-loading");
    if (loading) loading.classList.add("hidden");
  },

  async getStatus() {
    try {
      const r = await fetch("/api/comfy", { cache: "no-store" });
      if (!r.ok) return null;
      return await r.json();
    } catch (e) {
      return null;
    }
  },

  setErr(msg) {
    const el = document.getElementById("cfy-err");
    el.textContent = msg || "";
    el.classList.toggle("hidden", !msg);
  },

  /* 启动/停止按钮组统一状态(禁用 + 文案) */
  setButtonsState(kind, on) {
    const texts = {
      start: ["comfy.start", "comfy.starting"],
      stop: ["comfy.stop", "comfy.stopping"],
    };
    const ids = {
      start: ["cfy-start", "cfy-start-big"],
      stop: ["cfy-stop"],
    };
    const [idle, busy] = texts[kind];
    ids[kind].forEach((id) => {
      const b = document.getElementById(id);
      b.textContent = I18N.t(on ? busy : idle);
      b.disabled = on;
    });
    if (kind === "start") this.starting = on;
    else this.stopping = on;
  },

  toggleLog() {
    const wrap = document.getElementById("cfy-log-wrap");
    wrap.classList.toggle("hidden");
    document.getElementById("cfy-log-btn").classList.toggle("is-on", !wrap.classList.contains("hidden"));
    if (!wrap.classList.contains("hidden")) this.renderLog();
  },

  async renderLog() {
    const d = await this.getStatus();
    if (!d) return;
    const el = document.getElementById("cfy-log");
    const txt = d.logTail && d.logTail.trim() ? d.logTail : I18N.t("comfy.noLog");
    if (el.textContent !== txt) el.textContent = txt;
    el.scrollTop = el.scrollHeight;
    this.renderLogLine(txt);
  },

  /* 2026-08-26 底部常驻日志行:取日志尾部最后一条非空行同步展示(真实运行日志,
     来自后端 logs/comfy.log 尾部;截断超长行防撑破布局) */
  renderLogLine(tail) {
    const line = document.getElementById("cfy-log-line");
    if (!line) return;
    let last = "";
    if (tail && tail.trim()) {
      const rows = tail.split("\n");
      for (let i = rows.length - 1; i >= 0; i--) {
        const r = rows[i].trim();
        if (r) { last = r; break; }
      }
    }
    if (last.length > 240) last = "…" + last.slice(-240);
    const next = last || (this.online ? "暂无日志(ComfyUI 由外部启动,读不到其控制台输出)" : "服务未运行,等待启动…");
    if (line.textContent !== next) line.textContent = next;
  },

  /* 服务不可达:状态行/指示灯明确离线(不再静默卡旧值) */
  markUnreachable() {
    if (this.starting || this.stopping) return; // 操作进行中不覆盖状态文案
    const dot = document.querySelector("#view-comfy .hrs-dot");
    const txt = document.getElementById("cfy-txt");
    if (dot) dot.className = "hrs-dot off";
    if (txt) txt.textContent = "服务不可达";
    const live = document.getElementById("cfy-live");
    if (live) live.textContent = I18N.t("live.refreshed") + " " + new Date().toLocaleTimeString() + " · 连接失败";
  },

  /* 探测服务状态并刷新状态栏 / iframe / 离线占位 / 日志 */
  async renderStatus() {
    const d = await this.getStatus();
    if (!d) {
      this.markUnreachable();
      return;
    }
    const online = !!d.online;
    const becameOnline = online && !this.online;
    this.online = online;
    // 启动参数单一数据源:以服务端返回为准(HUD 卡片与本站共用,改 settings.json 即同步)
    if (d.startup && d.startup.url) {
      this._url = d.startup.url;
      const offSub = document.querySelector("#cfy-offline .hrs-off-s");
      if (offSub) offSub.textContent = I18N.t("comfy.offlineSub") + "（" + d.startup.url + "）";
    }

    const dot = document.querySelector("#view-comfy .hrs-dot");
    const txt = document.getElementById("cfy-txt");
    const offline = document.getElementById("cfy-offline");
    const loading = document.getElementById("cfy-loading");
    const frame = document.getElementById("cfy-frame");
    const startBtn = document.getElementById("cfy-start");
    const stopBtn = document.getElementById("cfy-stop");
    const openBtn = document.getElementById("cfy-open");
    const live = document.getElementById("cfy-live");

    dot.className = "hrs-dot " + (online ? "on" : "off");
    if (this.starting) txt.textContent = I18N.t("comfy.starting");
    else if (this.stopping) txt.textContent = I18N.t("comfy.stopping");
    else txt.textContent = I18N.t(online ? "comfy.online" : "comfy.offline");
    offline.classList.toggle("hidden", online);
    frame.classList.toggle("hidden", !online);
    if (loading) loading.classList.toggle("hidden", !online || this.frameReady);
    startBtn.classList.toggle("hidden", online);
    stopBtn.classList.toggle("hidden", !online);
    openBtn.classList.toggle("hidden", !online);
    if (live) live.textContent = I18N.t("live.refreshed") + " " + new Date().toLocaleTimeString();

    // 首次在线注入 iframe 地址;离线 → 在线翻转时重载(断线重连)
    // 注入期间显示暗色加载占位,页面 load 后收起(消除加载期白底闪烁)
    if (online && (!this.frameLoaded || becameOnline)) {
      this.frameLoaded = true;
      this.frameReady = false;
      frame.src = this.comfyUrl() + "/";
      if (loading) loading.classList.remove("hidden");
    }
    // 操作完成:启动/停止状态机复位
    if (this.starting && online) this.setButtonsState("start", false);
    if (this.stopping && !online) this.setButtonsState("stop", false);

    // 日志面板展开时跟随刷新
    if (!document.getElementById("cfy-log-wrap").classList.contains("hidden")) {
      this.renderLog();
    }
  },

  comfyUrl() { return this._url || this.COMFY_URL; },

  /* 启动服务,轮询直至就绪;失败显示错误行 */
  async startService() {
    if (this.starting) return;
    this.setErr("");
    this.setButtonsState("start", true);
    try {
      const r = await fetch("/api/comfy/start", { method: "POST", headers: window.nilixHeaders ? window.nilixHeaders({}) : {} });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        throw new Error(body.error || "start failed");
      }
    } catch (e) {
      this.setButtonsState("start", false);
      this.setErr(I18N.t("comfy.errStart") + " " + (e.message || ""));
      return;
    }
    // ComfyUI 启动较慢(导入节点/初始化),轮询直到可用(上限 60s)
    const t0 = Date.now();
    while (Date.now() - t0 < 60000) {
      await new Promise((res) => setTimeout(res, 2000));
      await this.renderStatus();
      if (this.online) break;
    }
    this.setButtonsState("start", false);
    if (!this.online) this.setErr("启动超时(60s),请展开日志面板查看启动输出");
  },

  /* 停止服务,轮询确认离线 */
  async stopService() {
    if (this.stopping) return;
    this.setErr("");
    this.setButtonsState("stop", true);
    try {
      const r = await fetch("/api/comfy/stop", { method: "POST", headers: window.nilixHeaders ? window.nilixHeaders({}) : {} });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        throw new Error(body.error || "stop failed");
      }
    } catch (e) {
      this.setButtonsState("stop", false);
      this.setErr(I18N.t("comfy.errStop") + " " + (e.message || ""));
      return;
    }
    const t0 = Date.now();
    while (Date.now() - t0 < 20000) {
      await new Promise((res) => setTimeout(res, 1000));
      await this.renderStatus();
      if (!this.online) break;
    }
    this.setButtonsState("stop", false);
    if (this.online) this.setErr("停止超时(20s),请查看日志面板;必要时手动结束进程");
  },

  /* 实时轮询:10s 检测服务状态(仅 Comfy 页激活时);
     日志轮询 2s 一次(底部日志行同步展示真实运行日志,本地文件 tail 开销极小) */
  startPolling() {
    if (this._timer) return;
    this._timer = setInterval(() => {
      if (!document.getElementById("view-comfy").classList.contains("is-active")) return;
      this.renderStatus();
    }, 10000);
    this._logTimer = setInterval(() => {
      if (!document.getElementById("view-comfy").classList.contains("is-active")) return;
      this.renderLog();
    }, 2000);
    this.renderLog(); // 进入页面立即刷一次日志行
  },
};
