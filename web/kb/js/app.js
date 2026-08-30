/* 应用外壳:路由 / 主题 / i18n / 星空背景 / 详情面板 */
/* HTML 转义(全局独立,manju.js 的对象内同名函数不共享作用域) */
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}
const App = {
  theme: "nebula",
  style: "default",
  pendingCat: null,
  pageCache: {},

  prefs: null,

  loadPrefs() {
    const d = { aiOn: true, aiStatus: true, aiQuotes: true, aiWander: true, aiFreq: 22000, aiSize: 104, aiWanderInt: 11000,
      aiChatHide: true, aiChatHideSec: 60, aiWaddle: true, aiWaddleAmp: 2.5, // 旧主窗口助手(已迁移桌宠,兼容保留)
      kbLayout: "force", kbShape: "mixed", kbCurve: 0.05, kbLineOp: 0.2, kbHoverLabel: true, kbLabels: 0,
      kbRepel: 260, kbDist: 100, kbGrav: 9 };
    let s = null;
    try { s = JSON.parse(localStorage.getItem("kbw-prefs") || "null"); } catch (e) {}
    this.prefs = Object.assign(d, s || {});
    if (s && (s.kbLayout === "radial" || s.kbLayout === "ring")) { this.prefs.kbLayout = "force"; }
    if (s && s.kbRepel === 340) { this.prefs.kbRepel = 260; this.prefs.kbDist = 100; this.prefs.kbGrav = 9; } // 紧凑迁移
    return this.prefs;
  },
  savePrefs() {
    try { localStorage.setItem("kbw-prefs", JSON.stringify(this.prefs)); } catch (e) {}
  },
  /* 设置弹窗:回填 + 即时生效 */
  bindPrefs() {
    const P = this.prefs;
    const el = (id) => document.getElementById(id);
    const set = (id, v) => { el(id).value = String(v); };
    const chk = (id, v) => { el(id).checked = !!v; };
    // AI 桌宠配置 → 已删除(2026-08-24 用户要求移除桌宠)
    // 灵动岛开关:读取后端 settings(默认启用),切换时写回(灵动岛轮询自动隐藏/显示)
    const islandToggle = el("set-island-on");
    if (islandToggle) {
      fetch("/api/island", { cache: "no-store" })
        .then((r) => r.json())
        .then((d) => { if (d && typeof d.enabled === "boolean") islandToggle.checked = d.enabled; })
        .catch(() => {});
      islandToggle.addEventListener("change", () => {
        fetch("/api/island", {
          method: "POST",
          headers: Object.assign({ "Content-Type": "application/json" }, this.nilixHeaders ? this.nilixHeaders({}) : {}),
          body: JSON.stringify({ enabled: islandToggle.checked }),
        }).catch(() => {});
      });
    }
  },

  async init() {
    // 首帧启动加载层撤场(2026-08-29):index.html 内联 boot-loader 覆盖 JS 链+I18N
    // 初始化期间的空白;初始化完成(或异常)都撤场,15s 内联超时兜底不困死
    const blDone = () => { try { window.__blDismiss && window.__blDismiss(); } catch (e) {} };
    try {
    this.theme = localStorage.getItem("kbw-theme") || "nebula";
    this.applyTheme(this.theme, true);
    this.style = localStorage.getItem("kbw-style") || "default";
    this.applyStyle(this.style, true);
    await I18N.init();
    this.bindControls();
    this.initTooltip();
    this.initStars();
    window.addEventListener("hashchange", () => this.route());
    // 语言切换后重渲染当前视图(侧栏/管理页为动态内容)
    document.addEventListener("i18n:changed", () => {
      const r = this.currentRoute();
      if (r === "comfy" && typeof ComfyView !== "undefined") ComfyView.renderStatus();
      else if (r === "novel" && typeof NovelView !== "undefined") NovelView.render();
      else if (r === "manju" && typeof ManjuView !== "undefined") ManjuView.render();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        const m = document.getElementById("settings-modal");
        if (m && m.classList.contains("is-open")) {
          m.classList.remove("is-open");
          m.setAttribute("aria-hidden", "true");
        }
      }
    });
    this.loadPrefs();
    this.bindPrefs();
    // 主窗口助手已迁移独立桌宠窗口 → 桌宠已删除(2026-08-24 用户要求)
    this.initTips();
    this.applyNavVisibility();
    this.route();
    } finally {
      // route() 已切出首屏视图壳,下一帧撤场(确保 loader 消失瞬间页面已绘制,
      // 各视图内部数据加载有自己的 loading 态接棒)
      requestAnimationFrame(() => setTimeout(blDone, 60));
    }
  },

  bindControls() {
    // 主题配色(设置弹窗内胶囊按钮,含内层装饰圆点)
    document.querySelectorAll(".theme-opt").forEach((b) =>
      b.addEventListener("click", () => this.applyTheme(b.dataset.theme))
    );
    document.querySelectorAll(".lang-btn").forEach((b) =>
      b.addEventListener("click", () => {
        localStorage.setItem("kbw-lang", b.dataset.lang);
        I18N.load(b.dataset.lang);
      })
    );
    document.querySelectorAll(".style-opt").forEach((b) =>
      b.addEventListener("click", () => this.applyStyle(b.dataset.style))
    );
    // 设置弹窗:开关 + 点击遮罩关闭
    const modal = document.getElementById("settings-modal");
    const openSettings = () => {
      modal.classList.add("is-open");
      modal.setAttribute("aria-hidden", "false");
      loadLLMSettings();
    };
    const closeSettings = () => {
      modal.classList.remove("is-open");
      modal.setAttribute("aria-hidden", "true");
    };
    document.getElementById("settings-btn").addEventListener("click", openSettings);
    document.getElementById("settings-close").addEventListener("click", closeSettings);
    // 2026-08-24 用户要求:所有页面刷新按钮集中到导航设置按钮前面——全局刷新按当前路由触发对应逻辑
    const navRefresh = document.getElementById("nav-refresh");
    if (navRefresh) navRefresh.addEventListener("click", () => {
      const route = this.currentRoute();
      if (route === "comfy") { if (typeof ComfyView !== "undefined") ComfyView.renderStatus(); }
      else if (route === "novel") { if (typeof NovelView !== "undefined") NovelView.render(); }
      else {
        if (typeof ManjuWorkbench !== "undefined") ManjuWorkbench.loadProjects();
        if (typeof ManjuView !== "undefined") ManjuView.render();
      }
    });
    modal.addEventListener("click", (e) => {
      if (e.target === modal) closeSettings();
    });
    // 剧本模型（LLM）设置：小说管理"生成剧本"依赖 /api/settings 的 LLM 配置
    async function loadLLMSettings() {
      try {
        const r = await fetch("/api/settings", { cache: "no-store" });
        if (!r.ok) return;
        const d = await r.json();
        const llm = (d.settings && d.settings.llm) || {};
        const keyEl = document.getElementById("set-llm-key");
        if (keyEl) {
          keyEl.value = d.api_key_set ? llm.api_key || "" : "";
          keyEl.placeholder = d.api_key_set ? "已保存（掩码），留空不修改" : "sk-...";
        }
        const baseEl = document.getElementById("set-llm-base");
        if (baseEl) baseEl.value = llm.base_url || "";
        const modelEl = document.getElementById("set-llm-model");
        if (modelEl) modelEl.value = llm.model || "";
      } catch (e) {}
    }
    async function saveLLMSettings() {
      const msg = document.getElementById("set-llm-msg");
      const btn = document.getElementById("set-llm-save");
      const setMsg = (txt, kind) => {
        if (!msg) return;
        msg.textContent = txt;
        msg.className = "llm-msg" + (kind ? " llm-msg-" + kind : "");
      };
      if (btn) btn.disabled = true;
      setMsg("保存中…", "");
      try {
        const r = await fetch("/api/settings", { cache: "no-store" });
        const d = await r.json();
        const cfg = d.settings || {};
        cfg.llm = cfg.llm || {};
        const baseEl = document.getElementById("set-llm-base");
        const modelEl = document.getElementById("set-llm-model");
        const keyEl = document.getElementById("set-llm-key");
        if (baseEl) cfg.llm.base_url = baseEl.value.trim();
        if (modelEl) cfg.llm.model = modelEl.value.trim();
        const key = keyEl ? keyEl.value.trim() : "";
        // 空输入沿用 GET 返回的掩码值（后端检测 **** 即不改）；输入新 key 则覆盖
        if (key) cfg.llm.api_key = key;
        const rr = await fetch("/api/settings", {
          method: "PUT", headers: this.nilixHeaders({ "Content-Type": "application/json" }),
          body: JSON.stringify(cfg),
        });
        if (rr.ok) {
          setMsg("✅ 已保存", "ok");
          loadLLMSettings();
        } else {
          const e = await rr.json().catch(() => ({}));
          setMsg("❌ 保存失败：" + (e.error || rr.status), "err");
        }
      } catch (e) {
        setMsg("❌ 保存失败：" + e, "err");
      }
      if (btn) btn.disabled = false;
    }
    const llmSave = document.getElementById("set-llm-save");
    if (llmSave) llmSave.addEventListener("click", saveLLMSettings);
    // 管理页设置:页面开关 + 目录选择(localStorage)
    ["novel", "manju"].forEach((key) => {
      const cb = document.getElementById("set-show-" + key);
      cb.checked = localStorage.getItem("kbw-show-" + key) !== "0";
      cb.addEventListener("change", () => {
        localStorage.setItem("kbw-show-" + key, cb.checked ? "1" : "0");
        this.applyNavVisibility();
      });
      const inp = document.getElementById("set-" + key + "-dir");
      inp.value = localStorage.getItem("kbw-" + key + "-dir") || "";
      const pick = document.getElementById("set-" + key + "-pick");
      pick.addEventListener("click", async () => {
        pick.disabled = true;
        pick.textContent = I18N.t("dir.picking");
        try {
          const r = await fetch("/api/fs/select", { method: "POST", cache: "no-store", headers: { "X-NiliX-Token": (window.NILIX_TOKEN || "") } });
          if (!r.ok) throw new Error("select failed");
          const d = await r.json();
          if (d.dir) {
            localStorage.setItem("kbw-" + key + "-dir", d.dir);
            inp.value = d.dir;
            // 当前页是对应管理页时立即重渲染
            if (App.currentRoute() === key) {
              if (key === "novel") NovelView.render();
              else ManjuView.render();
            }
          }
        } catch (e) {
          // 取消或失败:忽略(取消=正常)
        }
        pick.disabled = false;
        pick.textContent = I18N.t("dir.pick");
      });
    });

  },

  /* 管理页导航链接按设置开关显示/隐藏 */
  applyNavVisibility() {
    ["novel", "manju"].forEach((key) => {
      const show = localStorage.getItem("kbw-show-" + key) !== "0";
      const link = document.querySelector('.nav-link[data-route="' + key + '"]');
      if (link) link.style.display = show ? "" : "none";
    });
  },

  /* 恢复侧栏折叠记忆状态 */

  applyTheme(name, silent) {
    this.theme = name;
    document.documentElement.setAttribute("data-theme", name);
    localStorage.setItem("kbw-theme", name);
    this._palette = null; // 主题变化后重建派生色板
    document.querySelectorAll(".theme-opt, .theme-dot").forEach((b) =>
      b.classList.toggle("is-active", b.dataset.theme === name)
    );
    if (!silent) {
      document.dispatchEvent(new CustomEvent("theme:changed", { detail: name }));
    }
  },

  /* 最新总览展示偏好:统计卡指标 / 每分类页数上限 / 最近更新条数 */

  /* 材质风格:与配色主题正交(data-style 管质感,不重建色板) */
  applyStyle(name, silent) {
    this.style = name;
    document.documentElement.setAttribute("data-style", name);
    localStorage.setItem("kbw-style", name);
    document.querySelectorAll(".style-opt").forEach((b) =>
      b.classList.toggle("is-active", b.dataset.style === name)
    );
    if (!silent) {
      document.dispatchEvent(new CustomEvent("style:changed", { detail: name }));
    }
  },

  /* ---- 主题派生分类色板:圆球/图例/圆点随主题统一配色 ---- */
  setCats(names) {
    this._catNames = names || [];
    this._palette = null;
  },
  hexToHsl(hex) {
    const r = parseInt(hex.slice(1, 3), 16) / 255,
      g = parseInt(hex.slice(3, 5), 16) / 255,
      b = parseInt(hex.slice(5, 7), 16) / 255;
    const max = Math.max(r, g, b),
      min = Math.min(r, g, b);
    let h = 0,
      s = 0;
    const l = (max + min) / 2;
    if (max !== min) {
      const d = max - min;
      s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
      switch (max) {
        case r: h = (g - b) / d + (g < b ? 6 : 0); break;
        case g: h = (b - r) / d + 2; break;
        default: h = (r - g) / d + 4;
      }
      h *= 60;
    }
    return { h, s: s * 100, l: l * 100 };
  },
  hslToHex(h, s, l) {
    s /= 100; l /= 100;
    const k = (n) => (n + h / 30) % 12;
    const a = s * Math.min(l, 1 - l);
    const f = (n) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
    const to = (v) => Math.round(v * 255).toString(16).padStart(2, "0");
    return "#" + to(f(0)) + to(f(8)) + to(f(4));
  },
  buildPalette() {
    const cs = getComputedStyle(document.documentElement);
    let accent = cs.getPropertyValue("--accent").trim() || "#38bdf8";
    if (!accent.startsWith("#")) accent = "#38bdf8";
    const bg = cs.getPropertyValue("--bg").trim() || "#0a0e1a";
    const isLight = parseInt(bg.slice(1, 3), 16) > 0x99;
    const { h, s, l } = this.hexToHsl(accent);
    const offsets = [-160, -115, -70, -25, 20, 65, 110, 180]; // 7 分类 + 索引
    // 亮色主题:柔和饱和 + 中等明度(柔和中间调,与白色和谐);暗色主题:明亮饱和
    const sat = isLight ? Math.max(50, Math.min(60, s)) : Math.max(55, Math.min(92, s));
    const lig = isLight ? 52 : 62;
    return offsets.map((off) => this.hslToHex((h + off + 360) % 360, sat, lig));
  },
  /* 分类 → 主题派生色(按分类顺序稳定映射) */
  catColor(name) {
    if (!this._palette) this._palette = this.buildPalette();
    const idx = (this._catNames || []).indexOf(name);
    if (name === "索引") return this._palette[this._palette.length - 1];
    if (idx >= 0 && idx < this._palette.length - 1) return this._palette[idx];
    return this._palette[(this._palette.length - 2) % this._palette.length];
  },

  /* 分类 → 表情图标(关键词优先匹配,未命中用默认文件夹) */
  catEmoji(name) {
    if (!name) return "📂";
    const hit = App.CAT_EMOJI.find(([k]) => name.includes(k));
    return hit ? hit[1] : "📂";
  },
  CAT_EMOJI: [
    ["AI漫剧", "🎬"], ["开发工具", "🧰"], ["文档技能", "📝"],
    ["版本控制", "🔀"], ["知识库规范", "📚"], ["系统美化", "🎨"],
    ["系统优化", "⚡"], ["知识库", "📚"], ["工具", "🧰"],
    ["美化", "🎨"], ["优化", "⚡"], ["文档", "📝"],
    ["索引", "📑"], ["数据", "📊"], ["学习", "🎓"], ["教程", "📖"],
  ],

  /* 全局 tag 气泡:页面文本元素悬停 1s 展示分类 tag 内容 */
  initTooltip() {
    if (this._tt) return;
    const tt = document.createElement("div");
    tt.className = "g-tooltip";
    document.body.appendChild(tt);
    this._tt = tt;
    this._ttTarget = null;
    // 悬停目标:页面项(data-page)与分类标题(图例/侧栏分组/分类头)
    const SEL = "[data-page], .legend-item, .side-group-title, .cat-head";
    document.addEventListener("mouseover", (e) => {
      const t = e.target.closest(SEL);
      if (t === this._ttTarget && t) return; // 同一目标内移动不重置
      clearTimeout(this._ttTimer);
      this._ttTarget = t || null;
      this.hideTooltip();
      if (t) {
        this._ttTimer = setTimeout(() => this.showTooltip(t), 1000);
      }
    });
  },

  hideTooltip() {
    if (this._tt) {
      this._tt.classList.remove("is-on");
      this._tt.innerHTML = "";
    }
  },

  async showTooltip(t) {
    let html;
    if (t.hasAttribute("data-page")) {
      let p;
      try {
        p = await this.getPage(t.dataset.page);
      } catch (e) {
        return;
      }
      if (this._ttTarget !== t) return; // 已移开,丢弃过期结果
      const cat = p.category || "未分类";
      // 审计 H6:title/category 来自本地 md/README 文件(不可信输入),必须转义——
      // 恶意标题含 <img onerror=...> 悬停即 XSS,且页面内 window.NILIX_TOKEN 可直接读取
      html =
        `<div class="gt-line"><span class="gt-dot" style="background:${this.catColor(cat)}"></span>${this.catEmoji(cat)} ${esc(cat)}</div>` +
        `<div class="gt-title">${esc(p.title || t.dataset.page)}</div>` +
        `<div class="gt-meta">↗ ${(p.links || []).length} 关联 · ${p.words || 0} 字 · ${this.fmtDate(p.mtime)}</div>`;
    } else {
      // 分类 tag:图例/侧栏分组标题/分类头
      const name = t.dataset.cat || t.textContent.trim();
      html =
        `<div class="gt-line">${this.catEmoji(name)}<span>${esc(name)}</span></div>` +
        `<div class="gt-meta">知识分类 · 点击查看内容</div>`;
    }
    const tt = this._tt;
    tt.innerHTML = html;
    const r = t.getBoundingClientRect();
    tt.classList.add("is-on");
    const tw = tt.offsetWidth, th = tt.offsetHeight;
    let x = r.left + r.width / 2 - tw / 2;
    x = Math.max(8, Math.min(x, window.innerWidth - tw - 8));
    let y = r.top - th - 10;
    if (y < 8) y = r.bottom + 10;
    tt.style.left = x + "px";
    tt.style.top = y + "px";
  },

  nilixTok() {
    const t = window.NILIX_TOKEN;
    if (t && t.length > 8) return t;
    try { return localStorage.getItem("nilix_token") || ""; } catch (e) { return ""; }
  },
  nilixHeaders(h) {
    const t = this.nilixTok();
    if (!t) return h || {};
    return Object.assign({}, h || {}, { "X-NiliX-Token": t });
  },
  async api(path) {
    const tok = this.nilixTok();
    const r = await fetch(path, tok ? { headers: { "X-NiliX-Token": tok } } : {});
    if (!r.ok) throw new Error(path + " -> " + r.status);
    return r.json();
  },

  /* 页面详情缓存:底部栏与右侧详情共用,避免重复请求 */
  async getPage(id) {
    if (this.pageCache[id]) return this.pageCache[id];
    const p = await this.api("/api/page?id=" + encodeURIComponent(id));
    this.pageCache[id] = p;
    return p;
  },

  /* 内容变化后清空详情缓存 */
  clearPageCache() {
    this.pageCache = {};
  },

  /* 实时指示:显示最近自动刷新时间 */
  updateLive(elId) {
    const el = document.getElementById(elId);
    if (el) el.textContent = I18N.t("live.refreshed") + " " + new Date().toLocaleTimeString();
  },

  /* 助手交互:可拖拽;一步步步行移动(不闪现);默认停靠底部正中间;点击(未拖动)展开脚下输入框对话 */
  mascotInit() {
    const m = document.getElementById("ai-mascot");
    if (!m || m._init) return;
    m._init = true;
    this.bindAiChat();
    const r = m.getBoundingClientRect();
    m.style.right = "auto"; m.style.bottom = "auto";
    m.style.transform = "none"; // CSS 默认 translateX(-50%) 居中;转像素定位后不再偏移
    m.style.left = Math.max(8, (innerWidth - r.width) / 2) + "px"; // 默认位置:底部正中间
    m.style.top = Math.max(56, innerHeight - r.height - 18) + "px";
    m.style.cursor = "grab";
    let sx = 0, sy = 0, ox = 0, oy = 0, moved = false, down = false;
    m.addEventListener("pointerdown", (e) => {
      down = true; moved = false; sx = e.clientX; sy = e.clientY;
      const b = m.getBoundingClientRect(); ox = b.left; oy = b.top;
      this.mascotStopWalk();
      try { m.setPointerCapture(e.pointerId); } catch (err) {}
      m.style.cursor = "grabbing";
      e.preventDefault();
    });
    m.addEventListener("pointermove", (e) => {
      if (!down) return;
      const dx = e.clientX - sx, dy = e.clientY - sy;
      if (Math.abs(dx) + Math.abs(dy) > 6) moved = true;
      if (moved) {
        const w = m.offsetWidth, h = m.offsetHeight;
        m.style.left = Math.min(innerWidth - w - 6, Math.max(6, ox + dx)) + "px";
        m.style.top = Math.min(innerHeight - h - 6, Math.max(56, oy + dy)) + "px";
      }
    });
    const up = () => {
      if (!down) return;
      down = false;
      m.style.cursor = "grab";
      if (!moved) this.toggleAiChat(); // 点击(未拖动)= 展开/收起对话输入框
    };
    m.addEventListener("pointerup", up);
    m.addEventListener("pointercancel", up);
    // 随机漫步调度:小距离微步(当前位置附近 ±60~150px,像闲逛不是跑路)
    const wander = () => {
      // 切页踱步中 / 忙碌时先不闲逛,避免两条移动逻辑打架导致瞬移
      const walking = m.classList.contains("is-walking") || m.classList.contains("is-busy");
      if (!down && !walking && (!App.prefs || App.prefs.aiWander)) {
        const r = m.getBoundingClientRect();
        const w = m.offsetWidth, h = m.offsetHeight;
        const nx = r.left + (Math.random() * 180 - 90);   // 左右小挪
        const ny = r.top + (Math.random() * 160 - 80);    // 上下小挪
        m.style.left = Math.max(8, Math.min(innerWidth - w - 8, nx)) + "px";
        m.style.top = Math.max(56, Math.min(innerHeight - h - 12, ny)) + "px";
      }
      clearTimeout(this._mwT);
      const base = (App.prefs && App.prefs.aiWanderInt) || 11000;
      this._mwT = setTimeout(wander, base + Math.random() * base * 0.6);
    };
    this._mwT = setTimeout(wander, 5000);
    // 闲置名言轮播由设置面板(bindPrefs)按 aiFreq 建好,这里仅兜底(避免双定时器)
    if (!this._mqT) {
      this._mqT = setInterval(() => {
        if (!document.getElementById("ai-mascot") || document.getElementById("ai-mascot").classList.contains("is-busy")) return;
        const q = this.mascotQuote();
        if (q) this.mascotSay(q);
      }, 22000);
    }
  },

  /* 对话框跟随小助手:吸回模式(默认)实时锚定助手身边——上方优先,顶部空间不足转下方,
     全程出视口则贴边;助手被拖走/漫步时 150ms 跟随循环保持吸附 */
  aiChatSync() {
    const box = document.getElementById("ai-chat-box");
    const m = document.getElementById("ai-mascot");
    if (!box || !m || box.classList.contains("hidden") || this._aiChatFree) return;
    const mr = m.getBoundingClientRect();
    const bw = box.offsetWidth, bh = box.offsetHeight;
    let x = mr.left + mr.width / 2 - bw / 2;
    x = Math.max(8, Math.min(innerWidth - bw - 8, x));
    let y = mr.top - bh - 14;
    if (y < 56) y = mr.bottom + 14;
    y = Math.max(56, Math.min(innerHeight - bh - 8, y));
    box.style.transform = "none";
    box.style.bottom = "auto"; // 防御:确保 top+bottom 不同存,高度不被拉伸
    box.style.right = "auto";
    box.style.left = Math.round(x) + "px";
    box.style.top = Math.round(y) + "px";
  },
  aiChatFollowStart() {
    clearInterval(this._aiChatFollowT);
    this._aiChatFollowT = setInterval(() => {
      const box = document.getElementById("ai-chat-box");
      if (!box || box.classList.contains("hidden") || this._aiChatFree) {
        clearInterval(this._aiChatFollowT);
        this._aiChatFollowT = null;
        return;
      }
      this.aiChatSync();
    }, 150);
  },
  /* 对话框自由拖动:按住标题栏拖离助手即自由定位(localStorage 记忆),📌 吸回恢复跟随 */
  bindAiChatDrag() {
    if (this._aiChatDragBound) return;
    this._aiChatDragBound = true;
    const box = document.getElementById("ai-chat-box");
    const head = document.getElementById("ai-chat-head");
    if (!box || !head) return;
    // 关键:清掉 CSS 兜底的 bottom——fixed 同时有 top+bottom 会拉伸高度(视口-top-bottom),
    // top 偏大时对话框塌成一条线;定位从此全由 JS 的 top/left 控制
    box.style.bottom = "auto";
    box.style.right = "auto";
    try {
      const saved = JSON.parse(localStorage.getItem("kbw-ai-chat-pos") || "null");
      if (saved && saved.free) {
        this._aiChatFree = true;
        box.style.transform = "none";
        box.style.left = saved.x + "px";
        box.style.top = saved.y + "px";
      }
    } catch (e) {}
    this.aiChatDockUI();
    let sx = 0, sy = 0, ox = 0, oy = 0, down = false, moved = false;
    head.addEventListener("pointerdown", (e) => {
      if (e.target.closest("button")) return; // 按钮点击不触发拖动
      down = true; moved = false; sx = e.clientX; sy = e.clientY;
      const b = box.getBoundingClientRect(); ox = b.left; oy = b.top;
      try { head.setPointerCapture(e.pointerId); } catch (err) {}
      head.classList.add("dragging");
      e.preventDefault();
    });
    head.addEventListener("pointermove", (e) => {
      if (!down) return;
      const dx = e.clientX - sx, dy = e.clientY - sy;
      if (Math.abs(dx) + Math.abs(dy) > 4) moved = true;
      if (!moved) return;
      const bw = box.offsetWidth;
      const x = Math.max(8, Math.min(innerWidth - bw - 8, ox + dx));
      const y = Math.max(56, Math.min(innerHeight - 40, oy + dy));
      box.style.transform = "none";
      box.style.left = x + "px"; box.style.top = y + "px";
      this._aiChatFree = true;
      try { localStorage.setItem("kbw-ai-chat-pos", JSON.stringify({ free: true, x: Math.round(x), y: Math.round(y) })); } catch (err) {}
      this.aiChatDockUI();
    });
    const up = () => {
      if (!down) return;
      down = false;
      head.classList.remove("dragging");
    };
    head.addEventListener("pointerup", up);
    head.addEventListener("pointercancel", up);
    const dock = document.getElementById("ai-chat-dock");
    if (dock) dock.addEventListener("click", () => {
      this._aiChatFree = false;
      try { localStorage.setItem("kbw-ai-chat-pos", JSON.stringify({ free: false })); } catch (err) {}
      this.aiChatDockUI();
      this.aiChatSync();
      this.aiChatFollowStart();
    });
    const close = document.getElementById("ai-chat-close");
    if (close) close.addEventListener("click", () => box.classList.add("hidden"));
    window.addEventListener("resize", () => {
      if (!this._aiChatFree) { this.aiChatSync(); return; }
      const bw = box.offsetWidth;
      const x = Math.max(8, Math.min(innerWidth - bw - 8, parseFloat(box.style.left) || 0));
      const y = Math.max(56, Math.min(innerHeight - 40, parseFloat(box.style.top) || 56));
      box.style.left = x + "px"; box.style.top = y + "px";
    });
  },
  aiChatDockUI() {
    const dock = document.getElementById("ai-chat-dock");
    if (dock) dock.classList.toggle("on", !this._aiChatFree);
    const box = document.getElementById("ai-chat-box");
    if (box) box.classList.toggle("free", !!this._aiChatFree);
  },

  /* 点击助手:由气泡展开/收起对话窗口;Esc 或闲置(设置可配)自动收起;
     打开即吸到助手身边并启动跟随循环(自由模式下保持记忆位置) */
  toggleAiChat() {
    const box = document.getElementById("ai-chat-box");
    if (!box) return;
    const open = box.classList.toggle("hidden") === false;
    if (open) {
      this.bindAiChatDrag();
      this.aiChatSync();
      this.aiChatFollowStart();
      const inp = document.getElementById("ai-chat-input");
      if (inp) inp.focus();
    }
    this.armAiChatIdle();
  },

  /* 闲置自动收起:无交互(输入/新回复)超过设定秒数自动隐藏;设置关闭则不隐藏 */
  armAiChatIdle() {
    clearTimeout(this._aiChatIdleT);
    const P = this.prefs || {};
    if (!P.aiChatHide) return;
    const sec = Math.max(10, P.aiChatHideSec || 60) * 1000;
    this._aiChatIdleT = setTimeout(() => {
      const box = document.getElementById("ai-chat-box");
      if (box && !box.classList.contains("hidden")) box.classList.add("hidden");
    }, sec);
  },

  /* 输入框事件:Enter 发送(中文输入法选词回车不误发)、Esc 收起 */
  bindAiChat() {
    if (this._aiChatBound) return;
    this._aiChatBound = true;
    const send = () => {
      const inp = document.getElementById("ai-chat-input");
      if (!inp) return;
      const v = inp.value.trim();
      if (!v) return;
      inp.value = "";
      this.mascotChat(v);
      this.armAiChatIdle();
    };
    const sb = document.getElementById("ai-chat-send");
    if (sb) sb.addEventListener("click", send);
    const inp = document.getElementById("ai-chat-input");
    if (inp) inp.addEventListener("keydown", (e) => {
      if (e.isComposing || e.keyCode === 229) return;
      if (e.key === "Enter") send();
    });
    if (inp) inp.addEventListener("input", () => this.armAiChatIdle());
    // 快捷指令(体检/风格/总结/修复):点击即发送(对话框打开状态)
    document.querySelectorAll("#ai-chat-quick [data-qchat]").forEach((b) =>
      b.addEventListener("click", () => {
        const box = document.getElementById("ai-chat-box");
        if (box && box.classList.contains("hidden")) this.toggleAiChat();
        this.mascotChat(b.dataset.qchat);
        this.armAiChatIdle();
      })
    );
    document.addEventListener("keydown", (e) => {
      if (e.key !== "Escape") return;
      const box = document.getElementById("ai-chat-box");
      if (box && !box.classList.contains("hidden")) box.classList.add("hidden");
    });
  },

  /* 全局对话:调智能体聊天接口;回复显示在顶部回复横幅;action 在视频管理页委托其完整流程 */
  mascotChat(text) {
    this.aiReply("me", esc(text));
    const wb = (typeof ManjuWorkbench !== "undefined") ? ManjuWorkbench : null;
    const cfg = (wb && wb.project) || localStorage.getItem("manju-project") || "";
    if (!cfg) {
      this.aiReply("ag", "请先在<b>视频管理</b>页选择项目,我才能分析项目状态 🤖");
      return;
    }
    const inManju = !!(wb && wb.project === cfg);
    fetch("/api/manju/agent/chat", {
      method: "POST",
      headers: this.nilixHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify({ config: cfg, text }),
    }).then((r) => r.json()).then((d) => {
      const reply = (d.reply || "…").split("\n").map(esc).join("<br>");
      this.aiReply("ag", reply);
      this.mascotSay(d.reply || "…");
      if (d.action === "health") {
        if (inManju) wb.openHealth();
        else this.aiReply("ag", "👉 体检详情请到<b>视频管理</b>页查看(或点页面里的「🔍 项目体检」)");
      } else if (d.action === "style") {
        // 风格分析已由后端执行完(d.reply 带新旧风格与理由);刷新界面
        if (inManju) wb.loadProject();
      } else if (d.action === "fixall") {
        // 修复已由后端直接执行完(d.reply 带结果);这里只刷新界面
        if (inManju) {
          wb.loadHealth(false);
          wb.loadProject();
        }
      }
    }).catch((e) => { this.aiReply("ag", "❌ " + esc(e.message)); });
  },

  /* 气泡对话框:追加一条问答(你/🤖),保留最近 10 条;新回复重置闲置计时 */
  aiReply(who, html) {
    const box = document.getElementById("ai-chat-box");
    if (!box) return;
    box.classList.remove("hidden");
    const log = document.getElementById("ai-chat-log");
    const item = document.createElement("div");
    item.className = "ai-c-item " + who;
    item.innerHTML = '<span class="ai-c-who">' + (who === "me" ? "你" : "🤖") + '</span><span class="ai-c-txt">' + html + "</span>";
    log.appendChild(item);
    while (log.children.length > 10) log.removeChild(log.firstChild);
    log.scrollTop = log.scrollHeight;
    this.armAiChatIdle();
  },

  /* 一步步步行:每 45ms 挪一小步(≈10px),朝目标走,不闪现 */
  mascotWalkTo(x, y) {
    const m = document.getElementById("ai-mascot");
    if (!m) return;
    this.mascotStopWalk();
    m.classList.add("is-walking");
    const step = () => {
      const b = m.getBoundingClientRect();
      const dx = x - b.left, dy = y - b.top;
      const dist = Math.hypot(dx, dy);
      if (dist < 11) {
        m.style.left = x + "px"; m.style.top = y + "px";
        m.classList.remove("is-walking");
        return;
      }
      const v = 10 / dist;
      m.style.left = (b.left + dx * v) + "px";
      m.style.top = (b.top + dy * v) + "px";
      this._walkRaf = setTimeout(step, 45);
    };
    step();
  },
  mascotStopWalk() {
    clearTimeout(this._walkRaf);
    const m = document.getElementById("ai-mascot");
    if (m) m.classList.remove("is-walking");
  },

  /* 闲置名言/典故/成语(轮播) */
  mascotQuote() {
    if (!this._quotes) {
      this._quotes = [
        "不积跬步,无以至千里。——《荀子》",
        "路漫漫其修远兮,吾将上下而求索。——屈原",
        "山重水复疑无路,柳暗花明又一村。——陆游",
        "千里之行,始于足下。——《道德经》",
        "博观而约取,厚积而薄发。——苏轼",
        "宝剑锋从磨砺出,梅花香自苦寒来。",
        "纸上得来终觉浅,绝知此事要躬行。——陆游",
        "工欲善其事,必先利其器。——《论语》",
        "操千曲而后晓声,观千剑而后识器。——刘勰",
        "问渠那得清如许?为有源头活水来。——朱熹",
        "不畏浮云遮望眼,自缘身在最高层。——王安石",
        "长风破浪会有时,直挂云帆济沧海。——李白",
        "三人行,必有我师焉。——《论语》",
        "学而不思则罔,思而不学则殆。——《论语》",
        "绳锯木断,水滴石穿。——《汉书》",
        "它山之石,可以攻玉。——《诗经》",
        "欲速则不达,见小利则大事不成。——《论语》",
        "临渊羡鱼,不如退而结网。——《汉书》",
        "业精于勤,荒于嬉;行成于思,毁于随。——韩愈",
        "天下大事,必作于细。——《道德经》",
        "成语一刻 · 温故知新:温习旧知识,可有新体会",
        "成语一刻 · 集腋成裘:点滴积累,终成大器",
        "典故一刻 · 破釜沉舟:项羽渡漳水,皆沉船,示必死决心",
        "典故一刻 · 卧薪尝胆:勾践卧薪尝胆,十年生聚终灭吴",
        "典故一刻 · 囊萤映雪:车胤囊萤、孙康映雪,家贫苦读",
        "等待也是创作的一部分,灵感正在路上…",
        "休息一下吧,眼睛看看远处,思路会更清晰。",
      ];
      this._qi = Math.floor(Math.random() * this._quotes.length);
    }
    this._qi = (this._qi + 1) % this._quotes.length;
    return this._quotes[this._qi];
  },

  /* 分页面语境冒泡:切页时说一句应景的话并踱到该页合适角落 */
  mascotOnRoute(route) {
    const m = document.getElementById("ai-mascot");
    if (!m || !this.prefs || !this.prefs.aiOn || m.classList.contains("is-busy")) return;
    const ctx = {
      kb: ["知识如星海,节点连成网。", "一图胜千言,节点之间藏着联系。", "点击节点,打开一篇知识页试试。"],
      novel: ["读书破万卷,下笔如有神。——杜甫", "好故事都藏在下一章里。", "书架又厚了一点,继续写?"],
      manju: ["剧本、分镜、渲染,一步到位。", "审片官待命,随时开工一条龙。", "灵感 + 管线 = 成片。"],
      comfy: ["算力即生产力,GPU 已就绪。", "工作流跑起来,创意落成片。"],
    }[route];
    if (ctx) this.mascotSay(ctx[Math.floor(Math.random() * ctx.length)]);
    // 设置里关了闲逛移动 → 只在原地冒泡,不乱跑
    if (!this.prefs.aiWander) return;
    // 默认停靠:底部正中间(用户可拖拽离开)
    const w = m.offsetWidth, h = m.offsetHeight;
    const tx = Math.max(10, (innerWidth - w) / 2);
    const ty = Math.max(60, innerHeight - h - 18);
    this.mascotWalkTo(tx, ty);
  },

  /* 助手状态汇总:一处逻辑,manju 页轮询与全局轮询共用 */
  mascotStatus(s) {
    if (!this.prefs || !this.prefs.aiStatus) return;
    const a = s.agent || {};
    const bits = [];
    if (s.running) {
      const sn = { env: "项目体检", plan: "方案", assets: "资产", encode: "编码", render: "渲染", qc: "质检", assemble: "合成", upscale: "云端 2K" };
      const st = sn[s.stage || s.currentStage] || s.stage || s.currentStage || "处理中";
      const n = s.shotTotal ? " (" + s.shotCur + "/" + s.shotTotal + ")" : "";
      const t = s.elapsedSec ? " · " + (s.elapsedSec >= 60 ? Math.floor(s.elapsedSec / 60) + "分" + (s.elapsedSec % 60) + "秒" : s.elapsedSec + "秒") : "";
      bits.push(st + n + t);
    }
    if (a.planReview && a.planReview.score) bits.push("剧本复核 " + a.planReview.score + " 分");
    if (a.shots) {
      const bad = Object.values(a.shots).filter((x) => x && x.score != null && x.score < (a.passScore || 75)).length;
      if (bad) bits.push("审片 " + bad + " 镜待返工");
    }
    if (a.escalationCount) bits.push("⚠ " + a.escalationCount + " 镜升级待拍板");
    this.mascotSay(bits.length ? bits.join(" · ") : (s.done ? "上一轮已完成 ✅ 随时开工" : "AI 助手待命中 ✨"), s.running ? "busy" : "");
  },
  /* 全局轻轮询:任意页面云朵都实时(漫剧页由其自身 2s 轮询驱动,跳过免重复) */
  mascotLoop() {
    if (this._mascotTimer) return;
    this._mascotTimer = setInterval(async () => {
      try {
        if (this.currentRoute() === "manju") return;
        const cfg = localStorage.getItem("manju-project") || "";
        if (!cfg) { this.mascotSay("AI 助手待命中 ✨"); return; }
        const r = await fetch("/api/manju/status?config=" + encodeURIComponent(cfg), { cache: "no-store" });
        if (!r.ok) return;
        this.mascotStatus(await r.json());
      } catch (e) { /* 静默 */ }
    }, 3500);
  },

  /* 全站提示气泡:所有 title/data-tip 属性 → 精致小弹窗(替换浏览器丑原生提示)
     方案:悬停时读取 title → 迁移到 data-tip 并清空 title(抑制原生气泡,之后 title 属性
     不再存在但 data-tip 持久)→ 350ms 后于元素旁显示自定义气泡;离开元素即收起。
     动态生成内容由事件委托全覆盖,无需逐个初始化。 */
  initTips() {
    if (this._tipsInit) return;
    this._tipsInit = true;
    let tip = null, timer = null, cur = null;
    const hide = () => {
      clearTimeout(timer);
      cur = null;
      if (tip) { tip.remove(); tip = null; }
    };
    /* 锚定气泡:元素中心上方;视口贴边时水平夹紧、贴顶时翻到元素下方。
       箭头(::before)跟随元素中心,夹紧/翻转都不脱离目标——否则气泡会像在提示别的元素。 */
    const positionTip = (el) => {
      const r = el.getBoundingClientRect();
      const tw = tip.offsetWidth, th = tip.offsetHeight;
      let tx = r.left + r.width / 2 - tw / 2;
      tx = Math.max(8, Math.min(innerWidth - tw - 8, tx));
      let ty = r.top - th - 12;
      const flip = ty < 8;
      if (flip) ty = r.bottom + 12;              // 贴顶翻到下方,箭头翻底朝上
      tip.style.left = tx + "px";
      tip.style.top = ty + "px";
      tip.classList.toggle("flip", flip);
      // 箭头贴到元素中心正上方(气泡被夹紧后中心已偏离元素,箭头必须跟锚点走)
      const ax = r.left + r.width / 2 - tx;
      tip.style.setProperty("--tip-arrow-x", Math.max(10, Math.min(tw - 10, ax)) + "px");
    };
    const show = (el) => {
      if (tip) tip.remove();
      tip = document.createElement("div");
      tip.className = "tip-bubble";
      tip.textContent = el.dataset.tip || "";
      if (!tip.textContent.trim()) { tip = null; return; }
      document.body.appendChild(tip);
      positionTip(el);                           // offsetWidth 强制同步布局,首次即锚定元素
      tip.classList.add("show");
    };
    document.addEventListener("mouseover", (e) => {
      const t = e.target instanceof Element ? e.target : e.target.parentElement;
      const el = t ? t.closest("[title], [data-tip]") : null;
      if (!el) return;
      if (el === cur) return;                        // 已在展示(子元素间移动不重置)
      hide();                                        // 切目标:收起上一个
      cur = el;
      // 首次悬停:title → data-tip 迁移并清空原生 title(抑制浏览器原生气泡,仅一次)
      if (el.hasAttribute("title") && !el.dataset.tip) {
        el.dataset.tip = el.getAttribute("title");
        el.removeAttribute("title");
      }
      timer = setTimeout(() => { if (cur === el) show(el); }, 350);
    });
    // 气泡锚定目标元素(不跟随鼠标跳动):目标中心上方
    document.addEventListener("mousemove", (e) => {
      if (!cur || !tip) return;
      positionTip(cur);
    });
    document.addEventListener("mouseout", (e) => {
      const el = e.target instanceof Element ? e.target.closest("[title], [data-tip]") : null;
      const rt = e.relatedTarget instanceof Element ? e.relatedTarget : null;
      // 仅在真正离开当前目标(移入非 title 且非其子元素)时收起;进入子元素/另一 title 元素不闪
      const stillInside = cur && rt && (cur.contains(rt) || rt.closest("[title], [data-tip]"));
      if (el === cur && !stillInside) {
        hide();
      }
    });
    // 滚动/点击时收起,防气泡悬挂(弹窗开关/内容切换都会触发)
    document.addEventListener("scroll", hide, true);
    document.addEventListener("click", () => hide(), true);
  },

  /* 悬浮 AI 小助手:云朵播报;点击跳工作台 */
  mascotSay(text, mood) {
    if (!this.prefs || !this.prefs.aiOn) return;
    const b = document.getElementById("ai-bubble");
    if (!b || b.textContent === text) return;
    if (!/^\p{Emoji}/u.test(text) && !text.startsWith("💬")) text = "💬 " + text;  // 文字带表情
    b.textContent = text;                       // 气泡:整句直接显示(纯视觉美化,不做打字机)
    b.classList.remove("ai-pop");
    void b.offsetWidth; // 重触发动画
    b.classList.add("ai-pop");
    const m = document.getElementById("ai-mascot");
    if (m) m.classList.toggle("is-busy", mood === "busy");
    // 角色说话动画:说话时蹦跳摇摆(像在跑/说),结束后归位
    const av = document.getElementById("ai-avatar");
    if (av) {
      av.classList.remove("is-talking");
      void av.offsetWidth;
      av.classList.add("is-talking");
      clearTimeout(this._mascotTalkT);
      this._mascotTalkT = setTimeout(() => av.classList.remove("is-talking"), 1600);
    }
  },

  /* 当前路由:comfy / novel / manju(默认视频管理)——harness 已删除(2026-08-26 用户要求) */
  currentRoute() {
    const hash = location.hash || "#/manju";
    if (hash.startsWith("#/comfy")) return "comfy";
    if (hash.startsWith("#/novel")) return "novel";
    return "manju";
  },

  route() {
    const route = this.currentRoute();
    document.getElementById("view-comfy").classList.toggle("is-active", route === "comfy");
    document.getElementById("view-novel").classList.toggle("is-active", route === "novel");
    document.getElementById("view-manju").classList.toggle("is-active", route === "manju");
    document.body.classList.toggle("view-comfy-active", route === "comfy");
    document.body.classList.toggle("view-dir-active", route === "novel" || route === "manju");
    document.querySelectorAll(".nav-link").forEach((a) =>
      a.classList.toggle("is-active", a.dataset.route === route)
    );
    // 路由切换时关闭管理页遗留弹窗(宽阅读器/单视频弹窗)
    if (typeof NovelView !== "undefined") NovelView.closeReader();
    if (typeof ManjuView !== "undefined") ManjuView.closeFilmModal();
    if (route === "comfy" && typeof ComfyView !== "undefined") ComfyView.enter();
    else if (route === "novel") NovelView.enter();
    else {
      ManjuView.enter();
      if (typeof ManjuWorkbench !== "undefined") ManjuWorkbench.enter();
    }
    // 离开视频管理页时停止其轮询(iframe/状态常驻仅在本页需要)
    if (route !== "manju" && typeof ManjuWorkbench !== "undefined") ManjuWorkbench.leave();
    // 审计 2026-08-28:离开 Comfy 页对称停表(comfy.js leave 清两个定时器)
    if (route !== "comfy" && typeof ComfyView !== "undefined" && typeof ComfyView.leave === "function") ComfyView.leave();
    this.mascotOnRoute(route);
  },


  fmtDate(iso) {
    const dt = new Date(iso);
    if (isNaN(dt)) return iso;
    const p = (n) => String(n).padStart(2, "0");
    return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
  },

  initStars() {
    const canvas = document.getElementById("stars");
    const ctx = canvas.getContext("2d");
    let W = 0, H = 0;
    const resize = () => { W = canvas.width = window.innerWidth; H = canvas.height = window.innerHeight; };
    resize();
    window.addEventListener("resize", resize);
    const stars = [];
    const N = 90;
    for (let i = 0; i < N; i++)
      stars.push({
        x: Math.random() * 2000, y: Math.random() * 2000,
        r: Math.random() * 1.4 + 0.2, v: Math.random() * 0.35 + 0.05,
        a: Math.random() * Math.PI * 2,
      });
    const tick = () => {
      ctx.clearRect(0, 0, W, H);
      const cs = getComputedStyle(document.documentElement);
      const col = cs.getPropertyValue("--accent").trim() || "#38bdf8";
      ctx.globalAlpha = 0.55;
      ctx.fillStyle = col;
      for (const s of stars) {
        s.x += Math.cos(s.a) * s.v;
        s.y += Math.sin(s.a) * s.v;
        if (s.x < 0) s.x = W; if (s.x > W) s.x = 0;
        if (s.y < 0) s.y = H; if (s.y > H) s.y = 0;
        ctx.beginPath();
        ctx.arc(s.x % W, s.y % H, s.r, 0, Math.PI * 2);
        ctx.fill();
      }
      requestAnimationFrame(tick);
    };
    tick();
  },
};

document.addEventListener("DOMContentLoaded", () => App.init());

/* ============ NilixTip 统一悬停气泡(2026-08-26)============
 * 接管原生 title:系统白底气泡与深色主题割裂、长文本排版差。
 * 深色玻璃拟态 + 智能上下翻转 + 屏幕边界防溢;动态 title 走 NilixSetTip 同步刷新。 */
(function () {
  let tipEl = null, cur = null, showT = null;
  function ensure() {
    if (tipEl) return tipEl;
    tipEl = document.createElement("div");
    tipEl.className = "nilix-tip";
    tipEl.setAttribute("role", "tooltip");
    document.body.appendChild(tipEl);
    return tipEl;
  }
  function show(el) {
    const text = el.getAttribute("data-tip");
    if (!text) return;
    const t = ensure();
    t.textContent = text;
    t.classList.add("show");
    t.style.left = "0px"; t.style.top = "0px"; // 复位后再量尺寸
    const r = el.getBoundingClientRect();
    const tw = t.offsetWidth, th = t.offsetHeight;
    let x = r.left + r.width / 2 - tw / 2;
    x = Math.max(8, Math.min(x, window.innerWidth - tw - 8));
    let y = r.top - th - 8;
    if (y < 6) y = r.bottom + 8; // 顶部放不下翻到下方
    t.style.left = x + "px"; t.style.top = y + "px";
  }
  function hide() {
    if (tipEl) tipEl.classList.remove("show");
    cur = null;
  }
  // 动态 title 更新入口:气泡激活时同步刷新文字,否则照常写 title
  window.NilixSetTip = function (el, text) {
    if (!el) return;
    if (!text) { el.removeAttribute("title"); el.removeAttribute("data-tip"); return; }
    if (cur === el && tipEl) { el.setAttribute("data-tip", text); tipEl.textContent = text; return; }
    if (el.hasAttribute("data-tip")) el.setAttribute("data-tip", text);
    else el.title = text;
  };
  document.addEventListener("mouseover", (e) => {
    const el = e.target.closest ? e.target.closest("[title]") : null;
    if (el === cur) return;
    hide();
    clearTimeout(showT);
    if (!el) return;
    showT = setTimeout(() => {
      const text = el.getAttribute("title");
      if (!text) return;
      el.setAttribute("data-tip", text);
      el.removeAttribute("title"); // 防原生气泡冒出
      cur = el;
      show(el);
    }, 320);
  });
  document.addEventListener("mouseout", (e) => {
    const el = e.target.closest ? e.target.closest("[data-tip]") : null;
    if (el && !el.contains(e.relatedTarget)) {
      clearTimeout(showT);
      hide();
    }
  });
  ["scroll", "mousedown", "keydown"].forEach((ev) => window.addEventListener(ev, hide, true));
})();
