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
    $("cfy-versions").addEventListener("click", () => this.openVersions());
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

  /* ---- 版本管理弹窗(2026-08-29):版本/模型/插件/工作流 检测展示,仅检测不更新 ---- */
  _vmEsc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]));
  },

  // 非 GET 请求需会话 token(后端 auth);token 由页面注入(localStorage 兜底)
  _vmToken() {
    return window.NILIX_TOKEN || localStorage.getItem("nilix_token") || "";
  },

  _vmSize(mb) {
    return mb >= 1024 ? (mb / 1024).toFixed(2) + " GB" : mb.toFixed(0) + " MB";
  },

  openVersions() {
    if (this._vmOverlay) this._vmOverlay.remove();
    const overlay = document.createElement("div");
    overlay.className = "vm-modal";
    overlay.innerHTML = `
      <div class="vm-panel">
        <div class="vm-head"><span class="vm-title">🛠 版本管理</span>
          <span class="vm-head-btns">
            <button class="vm-refresh" title="刷新(重新加载版本数据)" aria-label="刷新">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 11A8 8 0 1 0 18.4 16"/><path d="M20 5v6h-6"/></svg>
            </button>
            <button class="vm-close" title="关闭" aria-label="关闭">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
            </button>
          </span>
        </div>
        <div class="vm-body"><div class="vm-loading">加载中…</div></div>
      </div>`;
    overlay.querySelector(".vm-close").addEventListener("click", () => overlay.remove());
    overlay.querySelector(".vm-refresh").addEventListener("click", () => this._vmLoad());
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    document.body.appendChild(overlay);
    this._vmOverlay = overlay;
    this._vmLoad();
  },

  async _vmLoad() {
    const bodyEl = this._vmOverlay.querySelector(".vm-body");
    try {
      const info = await (await fetch("/api/comfy/versions", { cache: "no-store" })).json();

      const comfy = info.comfy || {};
      const comfyLatest = info.comfy_latest || "";
      const comfyCur = comfy.online ? (comfy.version || "—") : "服务未运行";
      // 版本对比:相同→两绿;不同→当前红+最新绿(未检查时最新显示 —)。
      // 比较前剥离前导 v/v(本地 0.34.0 vs tag v0.34.0 视为相同)
      const verEq = (a, b) => String(a || "").replace(/^v/i, "") === String(b || "").replace(/^v/i, "");
      const verCell = (cur, latest) => {
        if (!latest) return `<span class="vm-cur">${this._vmEsc(cur)}</span><span class="vm-lat">—</span>`;
        if (verEq(cur, latest)) return `<span class="vm-cur vm-ok">${this._vmEsc(cur)}</span><span class="vm-lat vm-ok">${this._vmEsc(latest)}</span>`;
        return `<span class="vm-cur vm-bad" title="当前版本落后">${this._vmEsc(cur)}</span><span class="vm-lat vm-ok">${this._vmEsc(latest)}</span>`;
      };
      const pdd = info.pdd || {};
      const pddBadge = pdd.available
        ? `<span class="vm-badge vm-badge-ok" title="已安装 PDD Acc 8 步加速 LoRA(turbo_lora 指向该文件即启用)">PDD 加速可用</span>`
        : `<span class="vm-badge" title="官方 8 步 PDD 加速 LoRA 未安装">PDD 加速未装</span>`;

      const modelGroups = Object.entries(info.models || {}).filter(([, v]) => v && v.length);
      let modelHtml = modelGroups.map(([label, items], gi) => {
        const rows = items.slice(0, 30).map((m) => `
          <div class="vm-row">
            <span class="vm-mname" title="${this._vmEsc(m.name)}">${this._vmEsc(m.name)}</span>
            <span class="vm-msize">${this._vmSize(m.size_mb)}</span>
            <span class="vm-mmtime">${this._vmEsc(m.mtime)}</span>
          </div>`).join("");
        const more = items.length > 30 ? `<div class="vm-more">…共 ${items.length} 个</div>` : "";
        return `
          <div class="vm-group">
            <div class="vm-group-head" data-target="vmg-${gi}">
              <span class="vm-arrow">▾</span>${this._vmEsc(label)}<span class="vm-count">${items.length}</span>
            </div>
            <div class="vm-group-body" id="vmg-${gi}">${rows}${more}</div>
          </div>`;
      }).join("") || `<div class="vm-empty">暂无模型</div>`;

      const plugins = info.plugins || [];
      let pluginHtml = plugins.map((p) => {
        if (!p.git) {
          return `<div class="vm-row"><span class="vm-pname">${this._vmEsc(p.name)}</span>
            <span class="vm-pmeta">非 git 安装</span><span class="vm-ver">${verCell(p.commit || "?", "")}</span></div>`;
        }
        const dirty = p.dirty ? `<span class="vm-badge vm-badge-warn" title="本地有未提交改动">改动</span>` : "";
        const remote = (p.remote || "").replace(/^https:\/\/github.com\//, "").replace(/\.git$/, "");
        return `<div class="vm-row"><span class="vm-pname" title="${this._vmEsc(p.remote || "")}">${this._vmEsc(p.name)}</span>
          <span class="vm-pmeta">${this._vmEsc(remote)} ${dirty}</span>
          <span class="vm-ver" data-plugin="${this._vmEsc(p.name)}">${verCell(p.commit || "?", p.latest || "")}</span></div>`;
      }).join("") || `<div class="vm-empty">无插件</div>`;

      const wfs = info.workflows || [];
      let wfHtml = wfs.map((f) => `
        <div class="vm-row"><span class="vm-mname" title="${this._vmEsc(f.name)}">${this._vmEsc(f.name)}</span>
          <span class="vm-pmeta">${this._vmEsc(f.dir)} · ${this._vmSize((f.size || 0) / 1048576)} · ${this._vmEsc(f.mtime)}</span></div>`).join("")
        || `<div class="vm-empty">暂无工作流</div>`;

      const modelTotal = Object.values(info.models || {}).reduce((n, a) => n + (a || []).length, 0);
      bodyEl.innerHTML = `
        <div class="vm-section">
          <div class="vm-sec-title">ComfyUI 版本<span class="vm-sec-hint">当前 / 最新</span></div>
          <div class="vm-row"><span class="vm-pname">comfyui</span>
            <span class="vm-ver">${verCell(comfyCur, comfyLatest)}</span>${pddBadge}</div>
        </div>
        <div class="vm-section">
          <div class="vm-sec-title">模型(${modelTotal})</div>
          <div class="vm-scroll">${modelHtml}</div>
        </div>
        <div class="vm-section">
          <div class="vm-sec-title">插件(${plugins.length})<button class="vm-btn vm-btn-primary" id="vm-check-btn">🔄 检查更新</button></div>
          <div class="vm-scroll">${pluginHtml}</div>
        </div>
        <div class="vm-section">
          <div class="vm-sec-title">工作流(${wfs.length})</div>
          <div class="vm-scroll vm-scroll-wf">${wfHtml}</div>
        </div>`;

      bodyEl.querySelectorAll(".vm-group-head").forEach((h) => {
        h.addEventListener("click", () => {
          const b = document.getElementById(h.dataset.target);
          if (!b) return;
          const collapsed = b.style.display === "none";
          b.style.display = collapsed ? "" : "none";
          h.querySelector(".vm-arrow").textContent = collapsed ? "▾" : "▸";
        });
      });

      // 检查更新(仅检测:git fetch + 落后提交数,不更新文件)
      const checkBtn = bodyEl.querySelector("#vm-check-btn");
      checkBtn.addEventListener("click", async (ev) => {
        const btn = ev.target;
        btn.disabled = true; btn.textContent = "检查中…(网络)";
        try {
          const r = await (await fetch("/api/comfy/plugins/check", {
            method: "POST",
            headers: { "X-NiliX-Token": this._vmToken() },
          })).json();
          const res = (r && r.plugins) || {};
          // 插件版本对比刷新(当前=本地 commit,最新=origin HEAD)
          bodyEl.querySelectorAll(".vm-ver[data-plugin]").forEach((el) => {
            const p = res[el.dataset.plugin];
            if (!p) return;
            if (p.error) { el.innerHTML = `<span class="vm-cur">?</span><span class="vm-lat">❓</span>`; el.title = p.error; }
            else {
              el.innerHTML = verCell(p.current || "?", p.latest || "");
              el.title = p.behind > 0
                ? "有更新:落后 " + p.behind + " 个提交(更新后需重启 ComfyUI)"
                : "已是最新";
            }
          });
          // ComfyUI 官方最新版本刷新(检查更新响应带回)
          if (r.comfy_latest) {
            const cvRow = bodyEl.querySelector(".vm-section .vm-row");
            if (cvRow && cvRow.querySelector(".vm-ver")) {
              const cur = cvRow.querySelector(".vm-cur") ? cvRow.querySelector(".vm-cur").textContent : "";
              cvRow.querySelector(".vm-ver").innerHTML = verCell(cur, r.comfy_latest);
            }
          }
        } catch (e) { btn.textContent = "检查失败"; }
        setTimeout(() => { btn.disabled = false; btn.textContent = "🔄 检查更新"; }, 2000);
      });
    } catch (e) {
      bodyEl.innerHTML = `<div class="vm-empty">加载失败:${this._vmEsc(e.message)}</div>`;
    }
  },
};
