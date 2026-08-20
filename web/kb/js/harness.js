/* 二级页:DeepSeek Harness(AI 智能体工作台,服务状态 + 内嵌 Web UI iframe) */
const HarnessView = {
  online: false,
  frameLoaded: false, // iframe 是否已注入真实地址(懒加载,首次在线后注入)
  frameReady: false, // 当前注入内容是否已加载完成
  starting: false, // 启动中
  restarting: false, // 重启中

  HARNESS_URL: "http://127.0.0.1:3080",

  enter() {
    this.bindControls();
    this.renderStatus();
    this.startPolling();
  },

  bindControls() {
    if (this._bound) return;
    this._bound = true;
    const $ = (id) => document.getElementById(id);
    $("hrs-refresh").addEventListener("click", () => this.renderStatus());
    $("hrs-start").addEventListener("click", () => this.startService());
    $("hrs-start-big").addEventListener("click", () => this.startService());
    $("hrs-restart").addEventListener("click", () => this.restartService());
    $("hrs-open").addEventListener("click", () => {
      if (this.online) window.open(this.url(), "_blank");
    });
    const frame = document.getElementById("hrs-frame");
    frame.addEventListener("load", () => this.onFrameReady());
    frame.addEventListener("error", () => this.onFrameReady());
  },

  onFrameReady() {
    this.frameReady = true;
    const loading = document.getElementById("hrs-loading");
    if (loading) loading.classList.add("hidden");
  },

  async getStatus() {
    try {
      const r = await fetch("/api/harness", { cache: "no-store" });
      if (!r.ok) return null;
      return await r.json();
    } catch (e) {
      return null;
    }
  },

  setErr(msg) {
    const el = document.getElementById("hrs-err");
    el.textContent = msg || "";
    el.classList.toggle("hidden", !msg);
  },

  /* 按钮组统一状态(禁用 + 文案) */
  setButtons(kind, on) {
    const map = {
      start: { ids: ["hrs-start", "hrs-start-big"], busy: "启动中…", idle: "启动服务" },
      restart: { ids: ["hrs-restart"], busy: "重启中…", idle: "重启服务" },
    };
    const { ids, busy, idle } = map[kind];
    ids.forEach((id) => {
      const b = document.getElementById(id);
      b.textContent = on ? busy : idle;
      b.disabled = on;
    });
    if (kind === "start") this.starting = on;
    else this.restarting = on;
  },

  markUnreachable() {
    if (this.starting || this.restarting) return;
    const dot = document.querySelector("#view-harness .hrs-dot");
    const txt = document.getElementById("hrs-txt");
    if (dot) dot.className = "hrs-dot off";
    if (txt) txt.textContent = "服务不可达";
    const live = document.getElementById("hrs-live");
    if (live) live.textContent = new Date().toLocaleTimeString() + " · 连接失败";
  },

  /* 探测服务状态并刷新状态栏 / iframe / 离线占位 */
  async renderStatus() {
    const d = await this.getStatus();
    if (!d) {
      this.markUnreachable();
      return;
    }
    const online = !!d.online;
    const becameOnline = online && !this.online;
    this.online = online;

    const dot = document.querySelector("#view-harness .hrs-dot");
    const txt = document.getElementById("hrs-txt");
    const offline = document.getElementById("hrs-offline");
    const loading = document.getElementById("hrs-loading");
    const frame = document.getElementById("hrs-frame");
    const startBtn = document.getElementById("hrs-start");
    const restartBtn = document.getElementById("hrs-restart");
    const openBtn = document.getElementById("hrs-open");
    const live = document.getElementById("hrs-live");

    dot.className = "hrs-dot " + (online ? "on" : "off");
    if (this.starting) txt.textContent = "启动中…";
    else if (this.restarting) txt.textContent = "重启中…";
    else txt.textContent = online ? "运行中" : "已停止";
    offline.classList.toggle("hidden", online);
    frame.classList.toggle("hidden", !online);
    if (loading) loading.classList.toggle("hidden", !online || this.frameReady);
    startBtn.classList.toggle("hidden", online);
    restartBtn.classList.toggle("hidden", !online);
    openBtn.classList.toggle("hidden", !online);
    if (live) live.textContent = new Date().toLocaleTimeString();

    // 首次在线注入 iframe 地址;离线 → 在线翻转时重载
    if (online && (!this.frameLoaded || becameOnline)) {
      this.frameLoaded = true;
      this.frameReady = false;
      frame.src = this.url() + "/";
      if (loading) loading.classList.remove("hidden");
    }
    if (this.starting && online) this.setButtons("start", false);
    if (this.restarting && online) this.setButtons("restart", false);
  },

  url() { return this.HARNESS_URL; },

  /* 启动服务,轮询直至就绪;失败显示错误行 */
  async startService() {
    if (this.starting) return;
    this.setErr("");
    this.setButtons("start", true);
    try {
      const r = await fetch("/api/harness/start", { method: "POST", headers: window.nilixHeaders ? window.nilixHeaders({}) : {} });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        throw new Error(body.error || "start failed");
      }
    } catch (e) {
      this.setButtons("start", false);
      this.setErr("启动失败 " + (e.message || ""));
      return;
    }
    const t0 = Date.now();
    while (Date.now() - t0 < 60000) {
      await new Promise((res) => setTimeout(res, 2000));
      await this.renderStatus();
      if (this.online) break;
    }
    this.setButtons("start", false);
    if (!this.online) this.setErr("启动超时(60s)");
  },

  /* 重启服务,轮询确认重新在线 */
  async restartService() {
    if (this.restarting) return;
    this.setErr("");
    this.setButtons("restart", true);
    try {
      const r = await fetch("/api/harness/restart", { method: "POST", headers: window.nilixHeaders ? window.nilixHeaders({}) : {} });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        throw new Error(body.error || "restart failed");
      }
    } catch (e) {
      this.setButtons("restart", false);
      this.setErr("重启失败 " + (e.message || ""));
      return;
    }
    const t0 = Date.now();
    while (Date.now() - t0 < 60000) {
      await new Promise((res) => setTimeout(res, 2000));
      await this.renderStatus();
      if (this.online) break;
    }
    this.setButtons("restart", false);
    if (!this.online) this.setErr("重启超时(60s)");
  },

  /* 实时轮询:10s 检测服务状态(仅 Harness 页激活时) */
  startPolling() {
    if (this._timer) return;
    this._timer = setInterval(() => {
      if (!document.getElementById("view-harness").classList.contains("is-active")) return;
      this.renderStatus();
    }, 10000);
  },
};
