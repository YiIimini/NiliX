/* 应用外壳:路由 / 主题 / i18n / 星空背景 / 详情面板 */
const App = {
  theme: "nebula",
  style: "default",
  pendingCat: null,
  pageCache: {},

  async init() {
    this.theme = localStorage.getItem("kbw-theme") || "nebula";
    this.applyTheme(this.theme, true);
    this.style = localStorage.getItem("kbw-style") || "default";
    this.applyStyle(this.style, true);
    await I18N.init();
    this.bindControls();
    this.initTooltip();
    this.initStars();
    window.addEventListener("hashchange", () => this.route());
    // 语言切换后重渲染当前视图(侧栏/图例/管理页为动态内容)
    document.addEventListener("i18n:changed", () => {
      const r = this.currentRoute();
      if (r === "comfy" && typeof ComfyView !== "undefined") ComfyView.renderStatus();
      else if (r === "novel" && typeof NovelView !== "undefined") NovelView.render();
      else if (r === "manju" && typeof ManjuView !== "undefined") ManjuView.render();
      else if (typeof GraphView !== "undefined") {
        GraphView.renderLegend();
        GraphView.renderSide(GraphView.selectedId);
        GraphView.renderBottom(GraphView.selectedId);
      }
    });
    document.getElementById("detail-close").addEventListener("click", () => this.closeDetail());
    document.addEventListener("click", (e) => {
      const dp = document.getElementById("detail");
      if (!dp || !dp.classList.contains("is-open")) return;
      if (e.target.closest("#detail, .graph-canvas, #side, .side-collapse")) return;
      this.closeDetail();
    });
    document.getElementById("detail").addEventListener("click", (e) => {
      const w = e.target.closest(".md-wiki");
      if (w) this.openDetail(w.dataset.page);
      const c = e.target.closest(".link-chip, .rel-item");
      if (c) this.openDetail(c.dataset.page);
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        this.closeDetail();
        const m = document.getElementById("settings-modal");
        if (m && m.classList.contains("is-open")) {
          m.classList.remove("is-open");
          m.setAttribute("aria-hidden", "true");
        }
      }
    });
    // overview 路由:详情为居中弹窗,点击遮罩/面板空白处关闭;
    // 点击数据条目则由条目自身切换详情,不触发关闭
    document.addEventListener("click", (e) => {
      if (this.currentRoute() !== "overview") return;
      const d = document.getElementById("detail");
      if (!d || !d.classList.contains("is-open")) return;
      if (e.target.closest(".ct-item, .tl-item")) return;
      if (e.target === d || !d.contains(e.target)) {
        this.closeDetail();
      }
    });
    this.restoreSideCollapse();
    this.applyNavVisibility();
    this.route();
  },

  bindControls() {
    // 顶栏搜索:按视图分发(关系图谱=搜节点,最新总览=搜内容,内嵌页禁用)
    const ts = document.getElementById("top-search");
    if (ts) {
      const syncSearch = () => {
        ts.placeholder = I18N.t("graph.search");
        ts.value = App.currentRoute() === "graph" && typeof GraphView !== "undefined" ? GraphView.search || "" : "";
      };
      let st = null;
      ts.addEventListener("input", () => {
        clearTimeout(st);
        st = setTimeout(() => {
          const q = ts.value.trim();
          if (App.currentRoute() === "graph" && typeof GraphView !== "undefined") {
            GraphView.search = q;
            GraphView.applyOption();
          }
        }, 180);
      });
      window.addEventListener("hashchange", syncSearch);
      document.addEventListener("i18n:changed", syncSearch);
      syncSearch();
    }
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
    // 小屏抽屉式侧栏开关
    const toggle = document.getElementById("side-toggle");
    if (toggle) {
      toggle.addEventListener("click", () =>
        document.getElementById("side").classList.toggle("open")
      );
    }
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
          method: "PUT", headers: { "Content-Type": "application/json" },
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
          const r = await fetch("/api/fs/select", { cache: "no-store" });
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
    // 桌面端侧栏折叠(记忆状态,图谱自动撑满);按钮悬浮贴侧栏右缘,折叠后贴页面左缘。
    // 侧栏宽度随断点变化(300/260/220),按钮 left 由实测宽度同步而非写死。
    const sideBtn = document.getElementById("side-collapse");
    const syncSideBtn = () => {
      if (!sideBtn) return;
      if (document.body.classList.contains("side-collapsed")) { sideBtn.style.right = "0px"; return; }
      const panel = document.querySelector(".side-panel");
      // computed 宽度在 view 隐藏(display:none)或折叠动画中也能取到断点目标值
      if (panel) sideBtn.style.right = parseFloat(getComputedStyle(panel).width) + "px";
    };
    if (sideBtn) {
      sideBtn.addEventListener("click", () => {
        document.body.classList.toggle("side-collapsed");
        localStorage.setItem("kbw-side-collapsed", document.body.classList.contains("side-collapsed") ? "1" : "0");
        syncSideBtn();
        setTimeout(syncSideBtn, 350); // 展开动画结束后按最终宽度再校一次
        setTimeout(() => {
          if (typeof GraphView !== "undefined" && GraphView.chart) GraphView.chart.resize();
        }, 350);
      });
      syncSideBtn();
      let syncTimer = null;
      window.addEventListener("resize", () => {
        clearTimeout(syncTimer);
        syncTimer = setTimeout(syncSideBtn, 150);
      });
    }
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
  restoreSideCollapse() {
    if (localStorage.getItem("kbw-side-collapsed") === "1") {
      document.body.classList.add("side-collapsed");
    }
  },

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
      if (typeof GraphView !== "undefined") GraphView.refreshTheme();
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
      html =
        `<div class="gt-line"><span class="gt-dot" style="background:${this.catColor(cat)}"></span>${this.catEmoji(cat)} ${cat}</div>` +
        `<div class="gt-title">${p.title || t.dataset.page}</div>` +
        `<div class="gt-meta">↗ ${(p.links || []).length} 关联 · ${p.words || 0} 字 · ${this.fmtDate(p.mtime)}</div>`;
    } else {
      // 分类 tag:图例/侧栏分组标题/分类头
      const name = t.dataset.cat || t.textContent.trim();
      html =
        `<div class="gt-line">${this.catEmoji(name)}<span>${name}</span></div>` +
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

  async api(path) {
    const r = await fetch(path);
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

  /* 当前路由:graph / comfy / novel / manju */
  currentRoute() {
    const hash = location.hash || "#/";
    if (hash.startsWith("#/comfy")) return "comfy";
    if (hash.startsWith("#/novel")) return "novel";
    if (hash.startsWith("#/manju")) return "manju";
    return "graph";
  },

  route() {
    const route = this.currentRoute();
    document.getElementById("view-graph").classList.toggle("is-active", route === "graph");
    document.getElementById("view-comfy").classList.toggle("is-active", route === "comfy");
    document.getElementById("view-novel").classList.toggle("is-active", route === "novel");
    document.getElementById("view-manju").classList.toggle("is-active", route === "manju");
    document.body.classList.toggle("view-comfy-active", route === "comfy");
    document.body.classList.toggle("view-dir-active", route === "novel" || route === "manju");
    document.body.classList.toggle("view-graph-active", route === "graph");
    document.querySelectorAll(".nav-link").forEach((a) =>
      a.classList.toggle("is-active", a.dataset.route === route)
    );
    this.closeDetail();
    // 路由切换时关闭管理页遗留弹窗(宽阅读器/单视频弹窗)
    if (typeof NovelView !== "undefined") NovelView.closeReader();
    if (typeof ManjuView !== "undefined") ManjuView.closeFilmModal();
    if (route === "comfy") ComfyView.enter();
    else if (route === "novel") NovelView.enter();
    else if (route === "manju") {
      ManjuView.enter();
      if (typeof ManjuWorkbench !== "undefined") ManjuWorkbench.enter();
    } else GraphView.render();
    // 离开漫剧管理页时停止其轮询(iframe/状态常驻仅在本页需要)
    if (route !== "manju" && typeof ManjuWorkbench !== "undefined") ManjuWorkbench.leave();
  },

  async openDetail(id) {
    let p;
    try {
      p = await this.getPage(id);
    } catch (e) {
      return;
    }
    document.getElementById("d-title").textContent = p.title || id;
    document.getElementById("d-title").style.color = App.catColor(p.category);
    const meta = [];
    if (p.category)
      meta.push(`<span class="chip"><span class="dot" style="background:${App.catColor(p.category)}"></span><b>${I18N.t("detail.category")}</b> ${p.category}</span>`);
    if (p.words) meta.push(`<span class="chip"><b>${I18N.t("detail.words")}</b> ${p.words}</span>`);
    if (p.mtime) meta.push(`<span class="chip"><b>${I18N.t("detail.updated")}</b> ${this.fmtDate(p.mtime)}</span>`);
    document.getElementById("d-meta").innerHTML = meta.join("");
    document.getElementById("d-content").innerHTML = Markdown.render(p.markdown || "", p.category);
    // 右栏关联文章:ID → 图谱节点(名称/分类色),点击联动更新整弹窗
    const esc = (s) => String(s == null ? "" : String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])));
    const lk = document.getElementById("d-links");
    const nodesById = typeof GraphView !== "undefined" && GraphView.data
      ? GraphView.data.nodes.reduce((m2, n) => (m2[n.id] = n, m2), {})
      : {};
    if (p.links && p.links.length) {
      lk.innerHTML = p.links
        .map((t) => {
          const n = nodesById[t];
          const name = n ? n.name : t;
          const cat = n ? n.category : "";
          const color = cat ? App.catColor(cat) : "var(--muted)";
          return `<div class="rel-item" data-page="${String(t).replace(/"/g, "&quot;")}">
            <span class="si-dot" style="background:${color}"></span>
            <span class="rel-name">${esc(name)}</span>
            ${cat ? `<span class="rel-cat" style="color:${color}">${esc(cat)}</span>` : ""}
          </div>`;
        })
        .join("");
    } else {
      lk.innerHTML = `<span class="chip">${I18N.t("detail.noLinks")}</span>`;
    }
    const d = document.getElementById("detail");
    d.classList.add("is-open");
    d.setAttribute("aria-hidden", "false");
    document.body.classList.add("panel-open"); // 图谱可视区右缩,节点居中
    // 每次打开/切换内容:正文滚动区回到顶部(不残留上次位置)
    const sc = d.querySelector(".detail-scroll");
    if (sc) sc.scrollTop = 0;
    this.afterPanelChange();
  },

  /* 详情面板开合过渡后,图谱 resize 重新布局(节点随可视区居中) */
  afterPanelChange() {
    setTimeout(() => {
      if (
        typeof GraphView !== "undefined" &&
        GraphView.chart &&
        document.getElementById("view-graph").classList.contains("is-active")
      ) {
        GraphView.chart.resize();
      }
    }, 320); // 等 CSS 0.3s 过渡
  },

  closeDetail() {
    const d = document.getElementById("detail");
    d.classList.remove("is-open");
    d.setAttribute("aria-hidden", "true");
    document.body.classList.remove("panel-open");
    this.afterPanelChange();
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
