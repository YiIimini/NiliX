/* 管理页平台模式:小说=书架平台(书封→宽弹窗阅读:正文+右侧章节栏,可手动隐藏)
                   漫剧=影音平台(海报→单个视频弹窗页:播放器+选集+素材按分类排列) */
/* 本地 DOM 助手(manju.js 的 $ 是模块内 const,本文件作用域不可见——缺它创作弹窗按钮绑定会抛错) */
const $ = (id) => document.getElementById(id);
/* 阅读排版:字号/行距调节范围与默认值 */
const FS_RANGE = { min: 13, max: 26, def: 16 };
const LH_RANGE = { min: 1.3, max: 2.8, def: 2.0 };
const numIn = (v, d, min, max) => {
  const n = parseFloat(v);
  return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : d;
};
class DirView {
  constructor(key, opts) {
    this.key = key; // "novel" | "manju"
    if (key === "novel") {
      const saved = localStorage.getItem("kbw-novel-dir");
      if (saved) opts.root = saved;   // 导航设置里的小说目录优先
    }
    this.root = opts.root;
    this.id = opts.id;
    this.mode = opts.mode; // "book" | "film"
    // 静态隐藏项(代码内) + 用户通过三点菜单隐藏的项(localStorage 记忆)
    this._userHidden = [];
    try {
      this._userHidden = JSON.parse(localStorage.getItem("kbw-" + key + "-hidden") || "[]");
    } catch (e) {}
    this.hidden = (opts.hidden || []).concat(this._userHidden);
    this._bound = false;
    this._rendered = false;
    this._projects = [];
    this._detail = false; // 是否处于详情视图
    this._toc = null; // 当前书的目录 {chapters, extras}
    this._current = null; // 当前章节 {f, chIdx}
    this._tocOpen = true;
    // 排版设置(弹窗内 Aa 面板调节,localStorage 记忆)
    this._fs = numIn(localStorage.getItem("kbw-reader-fs"), FS_RANGE.def, FS_RANGE.min, FS_RANGE.max);
    this._lh = numIn(localStorage.getItem("kbw-reader-lh"), LH_RANGE.def, LH_RANGE.min, LH_RANGE.max);
  }

  enter() {
    this.bindControls();
    const dir = localStorage.getItem("kbw-" + this.key + "-dir") || this.root;
    if (dir !== this._lastDir) this._detail = false; // 目录切换:回到列表视图
    // 每次进入都重新拉取,避免外部删改(如删除项目)后仍显示旧记录
    this.render();
  }

  bindControls() {
    if (this._bound) return;
    this._bound = true;
    document.getElementById(this.id + "-refresh").addEventListener("click", () => this.render());
    // 列表搜索:书架/海报墙按作品名过滤(输入框 id = <实例id>-search)
    const searchIn = document.getElementById(this.id + "-search");
    if (searchIn) searchIn.addEventListener("input", () => this.applyListFilter());
    // 网页版爽文创作(shuangwen-novel 技能流程)
    const createBtn = document.getElementById("novel-create");
    if (createBtn && !createBtn._bound) {
      createBtn._bound = true;
      createBtn.addEventListener("click", () => NovelView.openNovelCreate());
    }
    // 回到前台时刷新(用户可能在外部删改项目/文件,切回标签页要看到最新)
    document.addEventListener("visibilitychange", () => {
      if (document.hidden) return;
      const view = document.getElementById("view-" + this.id);
      if (view && view.classList.contains("is-active")) this.render();
    });
    // 三点菜单:点击别处或按 Esc 关闭
    document.addEventListener("click", (e) => {
      if (!this._menu || !this._menu.classList.contains("is-open")) return;
      if (e.target.closest(".card-menu, .bk-more, .fm-more")) return;
      this.closeCardMenu();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") this.closeCardMenu();
    });
  }

  /* ---- 封面随机:存在多个封面候选时随机取一张 ---- */
  randomCover(p) {
    const cands = (p.covers || []).filter((c) => c.path);
    if (cands.length > 1) return cands[Math.floor(Math.random() * cands.length)].path;
    return cands.length ? cands[0].path : p.cover || "";
  }

  /* ---- 卡片右上角三点菜单:隐藏当前数据 ---- */
  ensureMenu() {
    if (this._menu) return this._menu;
    const menu = document.createElement("div");
    menu.className = "card-menu";
    menu.setAttribute("aria-hidden", "true");
    menu.innerHTML = `<button class="cm-item cm-hide">${I18N.t("book.hide")}</button>`;
    menu.querySelector(".cm-hide").addEventListener("click", (e) => {
      e.stopPropagation();
      this.hideCurrent();
    });
    document.body.appendChild(menu);
    this._menu = menu;
    return menu;
  }

  openCardMenu(anchor, p) {
    const menu = this.ensureMenu();
    const mw = menu.offsetWidth || 140;
    const mh = menu.offsetHeight || 80;
    let left, top;
    const card = anchor.closest(".bk-card, .fm-card");
    if (card) {
      // 弹窗居中显示在当前卡片上
      const r = card.getBoundingClientRect();
      left = r.left + r.width / 2 - mw / 2;
      top = r.top + r.height / 2 - mh / 2;
    } else {
      const r = anchor.getBoundingClientRect();
      left = r.left + r.width / 2 - mw / 2;
      top = r.bottom + 6;
    }
    left = Math.max(8, Math.min(left, window.innerWidth - mw - 8));
    top = Math.max(8, Math.min(top, window.innerHeight - mh - 8));
    menu.style.left = left + "px";
    menu.style.top = top + "px";
    this._menuName = p.name;
    menu.classList.add("is-open");
    menu.setAttribute("aria-hidden", "false");
  }

  closeCardMenu() {
    if (!this._menu) return;
    this._menu.classList.remove("is-open");
    this._menu.setAttribute("aria-hidden", "true");
    this._menuName = null;
  }

  hideCurrent() {
    const name = this._menuName;
    this.closeCardMenu();
    if (!name || this.hidden.includes(name)) return;
    this.hidden.push(name);
    this._userHidden.push(name);
    localStorage.setItem("kbw-" + this.key + "-hidden", JSON.stringify(this._userHidden));
    this.render();
  }

  hueOf(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
    // 避开绿色系色相(黄绿~青 70-180),平移至蓝紫区
    if (h >= 70 && h < 180) h += 100;
    return h;
  }

  /* 卡片随机色相:每次渲染每张卡片随机取色,卡片与其按钮共用同一色相(避开绿色系) */
  cardHue() {
    let h = Math.floor(Math.random() * 360);
    if (h >= 70 && h < 180) h = (h + 100) % 360;
    return h;
  }

  fmtSize(s) {
    if (!s) return "0 B";
    if (s < 1024) return s + " B";
    if (s < 1048576) return (s / 1024).toFixed(1) + " KB";
    return (s / 1048576).toFixed(1) + " MB";
  }

  async load() {
    const errEl = document.getElementById(this.id + "-err");
    errEl.classList.add("hidden");
    const dir = localStorage.getItem("kbw-" + this.key + "-dir") || this.root;
    this._lastDir = dir;
    // 路径显示已并入页头标题区(小说列表(路径)),此处不再写独立元素
    try {
      const api = this.mode === "book" ? "/api/fs/analyze" : "/api/fs/media";
      const r = await fetch(api + "?dir=" + encodeURIComponent(dir), { cache: "no-store" });
      if (!r.ok) throw new Error("load failed");
      const d = await r.json();
      // 后端已过滤系统/备份目录,这里仅应用显式隐藏项
      this._projects = (d.projects || []).filter((p) => !this.hidden.includes(p.name));
      return true;
    } catch (e) {
      errEl.textContent = e.message || "无法读取目录";
      errEl.classList.remove("hidden");
      document.getElementById(this.id + "-list").innerHTML = "";
      return false;
    }
  }

  async render() {
    const listEl = document.getElementById(this.id + "-list");
    listEl.innerHTML = `<div class="dir-loading">…</div>`;
    if (!(await this.load())) return;
    this._rendered = true;
    this._detail = false;
    if (this.mode === "book") this.renderBookshelf();
    else this.renderWall();
  }

  /* ---- 小说:书架(竖版书封网格) ---- */
  renderBookshelf() {
    const listEl = document.getElementById(this.id + "-list");
    if (!this._projects.length) {
      listEl.innerHTML = `<div class="dir-empty">📭 ${I18N.t("dir.empty")}</div>`;
      return;
    }
    // 每张卡片随机取色(卡片与按钮共用,详情弹窗沿用卡片色)
    this._projects.forEach((p) => (p._hue = this.cardHue()));
    listEl.innerHTML = `
      <div class="shelf-head"><span class="shelf-count">小说列表（${this.root}）· ${this._projects.length} 部</span></div>
      <div class="bk-shelf">${this._projects.map((p, i) => this.bookCard(p, i)).join("")}</div>`;
    listEl.querySelectorAll(".bk-card").forEach((card, i) => {
      card.addEventListener("click", (e) => {
        e.stopPropagation(); // 防止"打开弹窗"的点击被 document 层当作外部点击而立刻关闭
        this.openBook(this._projects[i]);
      });
    });
    listEl.querySelectorAll(".bk-more").forEach((btn, i) => {
      btn.addEventListener("click", (e) => {
        e.stopPropagation(); // 阻止卡片点击打开阅读器
        this.openCardMenu(btn, this._projects[i]);
      });
    });
    this.applyListFilter();
  }

  /* 列表搜索:按卡片名称过滤书架/海报墙,刷新后保持过滤 */
  applyListFilter() {
    const input = document.getElementById(this.id + "-search");
    const listEl = document.getElementById(this.id + "-list");
    if (!input || !listEl) return;
    const q = input.value.trim().toLowerCase();
    let shown = 0, total = 0;
    listEl.querySelectorAll(".bk-card, .fm-card").forEach((c) => {
      total++;
      const name = c.querySelector(".bk-cname, .fm-cname");
      const hit = !q || (name && name.textContent.toLowerCase().includes(q));
      c.style.display = hit ? "" : "none";
      if (hit) shown++;
    });
    let empty = listEl.querySelector(".filter-empty");
    if (q && total && !shown) {
      if (!empty) { empty = document.createElement("div"); empty.className = "dir-empty filter-empty"; listEl.appendChild(empty); }
      empty.textContent = "🔍 没有匹配「" + input.value.trim() + "」的作品";
    } else if (empty) empty.remove();
  }

  bookCard(p, i) {
    const hue = p._hue;
    const chapters = (p.chapters || []).length;
    const extras = (p.extras || []).length;
    const meta =
      `${chapters}${I18N.t("book.ch")}` +
      (extras ? ` · ${extras} ${I18N.t("book.extras")}` : "") +
      ` · ${(p.words || 0).toLocaleString()} ${I18N.t("dir.words")}`;
    // 封面:存在多个候选时随机取一张,没有则显示"暂无封面"占位
    const cover = this.randomCover(p);
    const coverHtml = cover
      ? `<img class="bk-cover-img" src="/api/fs/file?path=${encodeURIComponent(cover)}" alt="" loading="lazy">`
      : `<div class="bk-no-cover"><span class="bk-art">📖</span><span class="bk-nocov-t">${I18N.t("book.noCover")}</span></div>`;
    return `
      <div class="bk-card" style="--hue:${hue}" data-i="${i}">
        <div class="bk-front">
          <div class="bk-shelf-spine"></div>
          ${coverHtml}
          <button class="bk-more" title="${I18N.t("book.more")}" aria-label="${I18N.t("book.more")}">⋮</button>
          <div class="bk-scrim">
            <div class="bk-cname">${p.name}</div>
            <div class="bk-cmeta">${meta}</div>
          </div>
          <div class="bk-bottom-bar"></div>
        </div>
        <div class="bk-under">
          <button class="bk-read">${I18N.t("book.read")}</button>
        </div>
      </div>`;
  }

  /* ---- 宽弹窗阅读:正文 + 右侧章节栏 ---- */
  openBook(p) {
    const chapters = p.chapters || [];
    const extras = p.extras || [];
    if (!chapters.length && !extras.length) return;
    this._toc = { chapters, extras };
    this._bookName = p.name;
    this._bookCache = {}; // 全书搜索的章节正文缓存(换书重置)
    this._searchSeq = 0;
    const reader = document.getElementById("reader");
    document.getElementById("reader-title").textContent = p.name;
    this.renderToc();
    // 目录显隐:窄屏默认隐藏,桌面记忆用户选择
    const saved = localStorage.getItem("kbw-reader-toc");
    this._tocOpen = saved ? saved === "1" : window.innerWidth > 900;
    reader.classList.add("is-open");
    reader.setAttribute("aria-hidden", "false");
    this.applyTocState();
    this.applyType();
    this.closeTypePop();
    const start = chapters[0] || extras[0];
    this.loadChapter(start, chapters.indexOf(start));
    this.bindReader();
  }

  renderToc() {
    const el = document.getElementById("reader-toc");
    const toc = this._toc;
    if (!el || !toc) return;
    const name = (f) => f.name.replace(/\.(md|markdown|txt)$/i, "");
    let html = "";
    if (toc.chapters.length) {
      html += `<div class="rt-group"><div class="rt-group-head"><span>📖 ${I18N.t("book.chapters")}</span><span class="rt-count">${toc.chapters.length}</span></div>`;
      html += toc.chapters
        .map(
          (c, i) =>
            `<div class="rt-item" data-kind="ch" data-i="${i}" title="${c.path}"><span class="rt-no">${c.no && c.no < 1e9 ? String(c.no).padStart(3, "0") : String(i + 1).padStart(3, "0")}</span><span class="rt-name">${name(c)}</span></div>`
        )
        .join("");
      html += `</div>`;
    }
    if (toc.extras.length) {
      html += `<div class="rt-group"><div class="rt-group-head"><span>📑 ${I18N.t("book.extras")}</span><span class="rt-count">${toc.extras.length}</span></div>`;
      html += toc.extras
        .map(
          (f, i) =>
            `<div class="rt-item" data-kind="ex" data-i="${i}" title="${f.path}"><span class="rt-ic">📄</span><span class="rt-name">${name(f)}</span></div>`
        )
        .join("");
      html += `</div>`;
    }
    el.innerHTML = html;
    el.querySelectorAll(".rt-item").forEach((it) =>
      it.addEventListener("click", () => {
        const list = it.dataset.kind === "ch" ? toc.chapters : toc.extras;
        const f = list[Number(it.dataset.i)];
        this.loadChapter(f, toc.chapters.indexOf(f));
      })
    );
  }

  async loadChapter(f, chIdx) {
    if (!f) return;
    this._current = { f, chIdx };
    const title = f.name.replace(/\.(md|markdown|txt)$/i, "");
    document.getElementById("reader-title").textContent = this._bookName + " · " + title;
    const sc = document.getElementById("reader-content");
    sc.innerHTML = `<div class="dir-loading">…</div>`;
    try {
      const r = await fetch("/api/fs/read?path=" + encodeURIComponent(f.path), { cache: "no-store" });
      if (!r.ok) throw new Error("read failed");
      const d = await r.json();
      const isMd = /\.(md|markdown)$/i.test(d.ext || "");
      sc.innerHTML = isMd ? Markdown.render(d.content || "", "") : `<pre>${this.escapeHtml(d.content || "")}</pre>`;
    } catch (e) {
      sc.innerHTML = `<div class="dir-empty">⚠️ ${I18N.t("book.readError")}</div>`;
    }
    this.renderNav();
    this.highlightToc(f);
    sc.scrollTop = 0;
  }

  renderNav() {
    const nav = document.getElementById("reader-nav");
    if (!nav) return;
    nav.innerHTML = "";
    const chs = (this._toc || {}).chapters || [];
    if (!chs.length) return;
    const idx = this._current && this._current.chIdx >= 0 ? this._current.chIdx : -1;
    const prev = document.createElement("button");
    prev.className = "rn-btn";
    prev.textContent = "‹ " + I18N.t("book.prev");
    prev.disabled = idx <= 0;
    prev.addEventListener("click", () => {
      const c = chs[idx - 1];
      if (c) this.loadChapter(c, idx - 1);
    });
    const next = document.createElement("button");
    next.className = "rn-btn";
    next.textContent = I18N.t("book.next") + " ›";
    next.disabled = idx < 0 || idx >= chs.length - 1;
    next.addEventListener("click", () => {
      const c = chs[idx + 1];
      if (c) this.loadChapter(c, idx + 1);
    });
    const pos = document.createElement("span");
    pos.className = "rn-pos";
    pos.textContent = idx >= 0 ? idx + 1 + " / " + chs.length : "—";
    nav.appendChild(prev);
    nav.appendChild(pos);
    nav.appendChild(next);
  }

  highlightToc(f) {
    const el = document.getElementById("reader-toc");
    if (!el || !this._toc) return;
    el.querySelectorAll(".rt-item").forEach((it) => {
      const list = it.dataset.kind === "ch" ? this._toc.chapters : this._toc.extras;
      it.classList.toggle("is-active", list[Number(it.dataset.i)] === f);
    });
    const act = el.querySelector(".rt-item.is-active");
    if (act) act.scrollIntoView({ block: "nearest" });
  }

  applyTocState() {
    const reader = document.getElementById("reader");
    if (reader) reader.classList.toggle("toc-hidden", !this._tocOpen);
    const btn = document.getElementById("reader-toc-btn");
    if (btn) btn.classList.toggle("is-on", this._tocOpen);
    const label = document.getElementById("reader-toc-label");
    if (label) label.textContent = this._tocOpen ? I18N.t("book.hideToc") : I18N.t("book.toc");
  }

  toggleToc() {
    this._tocOpen = !this._tocOpen;
    localStorage.setItem("kbw-reader-toc", this._tocOpen ? "1" : "0");
    this.applyTocState();
  }

  /* ---- 排版:字号/行距(弹窗内 Aa 面板调节,localStorage 记忆) ---- */
  applyType() {
    const reader = document.getElementById("reader");
    if (!reader) return;
    reader.style.setProperty("--reader-fs", this._fs + "px");
    reader.style.setProperty("--reader-lh", String(this._lh));
    const fv = document.getElementById("type-fs-val");
    if (fv) fv.textContent = this._fs + "px";
    const lv = document.getElementById("type-lh-val");
    if (lv) lv.textContent = this._lh.toFixed(1);
    const set = (id, on) => {
      const b = document.getElementById(id);
      if (b) b.disabled = !on;
    };
    set("type-fs-minus", this._fs > FS_RANGE.min);
    set("type-fs-plus", this._fs < FS_RANGE.max);
    set("type-lh-minus", this._lh > LH_RANGE.min + 1e-9);
    set("type-lh-plus", this._lh < LH_RANGE.max - 1e-9);
  }

  setType(kind, d) {
    if (kind === "fs") {
      this._fs = Math.min(FS_RANGE.max, Math.max(FS_RANGE.min, this._fs + d));
      localStorage.setItem("kbw-reader-fs", this._fs);
    } else {
      this._lh = Math.round((this._lh + d) * 10) / 10; // 避免浮点漂移
      this._lh = Math.min(LH_RANGE.max, Math.max(LH_RANGE.min, this._lh));
      localStorage.setItem("kbw-reader-lh", this._lh);
    }
    this.applyType();
  }

  resetType() {
    this._fs = FS_RANGE.def;
    this._lh = LH_RANGE.def;
    localStorage.removeItem("kbw-reader-fs");
    localStorage.removeItem("kbw-reader-lh");
    this.applyType();
  }

  toggleTypePop() {
    const pop = document.getElementById("reader-type-pop");
    const btn = document.getElementById("reader-type-btn");
    if (!pop) return;
    const on = !pop.classList.contains("is-on");
    pop.classList.toggle("is-on", on);
    pop.setAttribute("aria-hidden", String(!on));
    if (btn) {
      btn.classList.toggle("is-on", on);
      btn.setAttribute("aria-expanded", String(on));
    }
  }

  closeTypePop() {
    const pop = document.getElementById("reader-type-pop");
    if (pop && pop.classList.contains("is-on")) {
      pop.classList.remove("is-on");
      pop.setAttribute("aria-hidden", "true");
    }
    const btn = document.getElementById("reader-type-btn");
    if (btn) {
      btn.classList.remove("is-on");
      btn.setAttribute("aria-expanded", "false");
    }
  }

  bindReader() {
    if (this._readerBound) return;
    this._readerBound = true;
    const reader = document.getElementById("reader");
    document.getElementById("reader-close").addEventListener("click", () => this.closeReader());
    document.getElementById("reader-toc-btn").addEventListener("click", () => this.toggleToc());
    document.getElementById("reader-script-btn").addEventListener("click", () => this.makeManju());
    // 全书搜索:章节标题 + 正文(防抖 250ms,正文按需并行拉取并缓存)
    const searchIn = document.getElementById("reader-search");
    let searchTimer = null;
    searchIn.addEventListener("input", () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => this.searchBook(searchIn.value), 250);
    });
    searchIn.addEventListener("keydown", (e) => {
      e.stopPropagation(); // 输入中不触发左右方向键翻章
      if (e.key === "Escape") document.getElementById("reader-search-pop").hidden = true;
    });
    // 排版面板:开关 + 字号/行距步进 + 恢复默认
    document.getElementById("reader-type-btn").addEventListener("click", (e) => {
      e.stopPropagation();
      this.toggleTypePop();
    });
    const stepBind = (id, kind, d) =>
      document.getElementById(id).addEventListener("click", (e) => {
        e.stopPropagation();
        this.setType(kind, d);
      });
    stepBind("type-fs-minus", "fs", -1);
    stepBind("type-fs-plus", "fs", 1);
    stepBind("type-lh-minus", "lh", -0.1);
    stepBind("type-lh-plus", "lh", 0.1);
    document.getElementById("type-reset").addEventListener("click", (e) => {
      e.stopPropagation();
      this.resetType();
    });
    document.addEventListener("click", (e) => {
      if (!reader.classList.contains("is-open")) return;
      if (e.target.closest(".rt-item, .rn-btn")) return;
      this.closeTypePop();
      if (e.target === reader || !reader.contains(e.target)) this.closeReader();
    });
    document.addEventListener("keydown", (e) => {
      if (!reader.classList.contains("is-open")) return;
      const chs = (this._toc || {}).chapters || [];
      const idx = this._current ? this._current.chIdx : -1;
      if (e.key === "Escape") {
        // 先关排版面板,再关阅读器
        const pop = document.getElementById("reader-type-pop");
        if (pop && pop.classList.contains("is-on")) return this.closeTypePop();
        return this.closeReader();
      }
      if (e.key === "ArrowLeft" && idx > 0) this.loadChapter(chs[idx - 1], idx - 1);
      else if (e.key === "ArrowRight" && idx >= 0 && idx < chs.length - 1) this.loadChapter(chs[idx + 1], idx + 1);
    });
  }

  closeReader() {
    const reader = document.getElementById("reader");
    reader.classList.remove("is-open");
    reader.setAttribute("aria-hidden", "true");
    this.closeTypePop();
    const searchPop = document.getElementById("reader-search-pop");
    if (searchPop) searchPop.hidden = true;
    const searchIn = document.getElementById("reader-search");
    if (searchIn) searchIn.value = "";
    this._bookCache = {};
    this._toc = null;
    this._current = null;
    this.closeScript();
  }

  /* ---- 网页版爽文创作:立项(大纲) → 逐章/自动连写,固化 shuangwen-novel 流程 ---- */
  _nvStop = false;
  async openNovelCreate() {
    const wb = typeof ManjuWorkbench !== "undefined" ? ManjuWorkbench : null;
    if (!wb) return;
    wb.openModal("✍ 爽文小说创作", `
      <div class="nv-wrap">
        <div class="nv-hero">
          <div class="nv-hero-ic">📖</div>
          <div>
            <div class="nv-hero-t">爽文一条龙 · 创作工坊</div>
            <div class="nv-hero-s">立项 → 大纲 → 逐章写作(每章 ≥1280 字 · 7 章一卷)</div>
          </div>
        </div>

        <div class="nv-sec">
          <div class="nv-sec-t">📝 项目信息</div>
          <div class="nv-form">
            <div class="nv-row"><label>书名</label><input id="nv-title" class="manju-input" placeholder="如:吞天废子" spellcheck="false"></div>
            <div class="nv-grid2">
              <div class="nv-row"><label>题材</label><input id="nv-genre" class="manju-input" placeholder="玄幻逆袭(留空自动)" spellcheck="false"></div>
              <div class="nv-row"><label>风格</label><input id="nv-style" class="manju-input" placeholder="热血爽文(留空自动)" spellcheck="false"></div>
            </div>
            <div class="nv-row"><label>章节数</label><input id="nv-count" class="manju-input manju-num" type="number" min="8" max="300" value="56"><span class="nv-hint">硬规范每章 ≥1280 字 · 7 章/卷</span></div>
          </div>
        </div>

        <div class="nv-sec">
          <div class="nv-sec-t">🚀 生成控制</div>
          <div class="nv-actions">
            <button id="nv-start" class="hrs-btn hrs-btn-primary">🚀 立项生成大纲</button>
            <span class="nv-status" id="nv-status"></span>
          </div>
        </div>

        <div id="nv-work" class="nv-sec nv-work hidden">
          <div class="nv-sec-t">⏳ 写作进度
            <span class="nv-percent" id="nv-percent">0%</span>
          </div>
          <div class="nv-bar"><div class="nv-bar-fill" id="nv-bar"></div></div>
          <div class="nv-actions">
            <button id="nv-next" class="hrs-btn">✍ 生成下一章</button>
            <button id="nv-auto" class="hrs-btn hrs-btn-primary">⚡ 自动连写</button>
            <button id="nv-stop" class="hrs-btn hrs-btn-danger">■ 停止</button>
            <span class="nv-status" id="nv-progress"></span>
          </div>
          <div class="nv-state" id="nv-state">准备就绪,点击上方按钮开始写作</div>
          <div id="nv-chapters" class="nv-chapters"></div>
        </div>
      </div>`);
    $("nv-start").addEventListener("click", () => this.nvCreate());
    $("nv-next").addEventListener("click", () => this.nvChapter(false));
    $("nv-auto").addEventListener("click", () => this.nvChapter(true));
    $("nv-stop").addEventListener("click", () => { this._nvStop = true; });
    this.nvRefresh();
  }
  async nvCreate() {
    const title = $("nv-title").value.trim();
    if (!title) { $("nv-status").textContent = "请先填书名"; return; }
    this._nvTitle = title;
    $("nv-status").textContent = "大纲生成中(约 1-2 分钟,请勿关弹窗)…";
    $("nv-start").disabled = true;
    try {
      const r = await fetch("/api/novel/create", { method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title, genre: $("nv-genre").value.trim(), style: $("nv-style").value.trim(), chapters: parseInt($("nv-count").value, 10) || 56 }) });
      const j = await r.json();
      if (!r.ok) throw new Error(j.error || "HTTP " + r.status);
      $("nv-status").textContent = j.exists ? "已有大纲,续写模式" : "✅ 大纲完成";
      $("nv-work").classList.remove("hidden");
      this._nvTotal = j.chapters || 56;
      await this.nvRefresh();
    } catch (e) {
      $("nv-status").textContent = "❌ " + e.message;
    }
    $("nv-start").disabled = false;
  }
  async nvRefresh() {
    if (!this._nvTitle) return;
    try {
      const r = await fetch("/api/novel/progress?title=" + encodeURIComponent(this._nvTitle));
      const j = await r.json();
      this._nvDone = new Set((j.chapters || []).map((c) => c.no));
      this._nvTotal = this._nvTotal || Math.max(56, (j.chapters || []).length + 1);
      const box = $("nv-chapters");
      if (!box) return;
      let html = "";
      const cur = this._nvCur || 0;
      for (let n = 1; n <= this._nvTotal; n++) {
        const done = this._nvDone.has(n);
        const act = n === cur;
        html += `<span class="nv-ch ${done ? "done" : ""} ${act ? "act" : ""}" title="第 ${n} 章">${done ? "✓" : n}</span>`;
      }
      box.innerHTML = html;
      const p = $("nv-progress");
      if (p) p.textContent = `已写 ${this._nvDone.size} / ${this._nvTotal} 章`;
      const bar = $("nv-bar"), pct = $("nv-percent");
      if (bar && this._nvTotal) {
        const v = Math.round(this._nvDone.size / this._nvTotal * 100);
        bar.style.width = Math.min(100, v) + "%";
        if (pct) pct.textContent = v + "%";
      }
    } catch (e) { /* 忽略 */ }
  }
  async nvChapter(auto) {
    if (!this._nvTitle) return;
    this._nvStop = false;
    do {
      const next = (() => { for (let n = 1; n <= (this._nvTotal || 56); n++) if (!this._nvDone || !this._nvDone.has(n)) return n; return 0; })();
      if (!next) {
        $("nv-progress").textContent = "🎉 全本完成!";
        const st = $("nv-state"); if (st) { st.className = "nv-state ok"; st.textContent = "🎉 全本完成,关闭弹窗即可在书架阅读"; }
        break;
      }
      this._nvCur = next;
      $("nv-progress").textContent = `第 ${next} 章写作中(约 30-60s)…`;
      const st = $("nv-state");
      if (st) { st.className = "nv-state busy"; st.textContent = "✍ 正在写第 " + next + " 章,AI 构思与码字中…"; }
      if (typeof App !== "undefined" && App.mascotSay) App.mascotSay(`小说《${this._nvTitle}》第 ${next} 章写作中…`, "busy");
      try {
        const r = await fetch("/api/novel/chapter", { method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ title: this._nvTitle, no: next }) });
        const j = await r.json();
        if (!r.ok) { $("nv-progress").textContent = "❌ 第" + next + "章: " + (j.error || r.status); break; }
      } catch (e) {
        $("nv-progress").textContent = "❌ " + e.message;
        const st2 = $("nv-state"); if (st2) { st2.className = "nv-state err"; st2.textContent = "❌ " + e.message; }
        break;
      }
      this._nvCur = 0;
      await this.nvRefresh();
      if (this._nvDone && this._nvDone.size === this._nvTotal) {
        const st3 = $("nv-state"); if (st3) { st3.className = "nv-state ok"; st3.textContent = "🎉 全本完成!关闭弹窗即可在书架阅读"; }
      }
      if (!auto || this._nvStop) break;
    } while (true);
    this.render();
  }

  /* ---- 全书搜索:标题命中优先,否则正文含关键词给上下文摘要,点击跳章 ---- */
  async searchBook(q) {
    const pop = document.getElementById("reader-search-pop");
    q = String(q || "").trim();
    if (!q) { pop.hidden = true; pop.innerHTML = ""; return; }
    const toc = this._toc;
    if (!toc) return;
    const seq = ++this._searchSeq;
    const ql = q.toLowerCase();
    const pool = [
      ...toc.chapters.map((f) => ({ f })),
      ...toc.extras.map((f) => ({ f })),
    ];
    pop.innerHTML = `<div class="rs-item rs-loading">正在搜索 ${pool.length} 个文件…</div>`;
    pop.hidden = false;
    this._bookCache = this._bookCache || {};
    await Promise.all(pool.map(async ({ f }) => {
      if (f.path in this._bookCache) return;
      try {
        const r = await fetch("/api/fs/read?path=" + encodeURIComponent(f.path), { cache: "force-cache" });
        this._bookCache[f.path] = r.ok ? await r.text() : "";
      } catch { this._bookCache[f.path] = ""; }
    }));
    if (seq !== this._searchSeq) return; // 已被更新的输入取代
    const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
    const results = [];
    for (const { f } of pool) {
      const title = f.name.replace(/\.(md|markdown|txt)$/i, "");
      if (title.toLowerCase().includes(ql)) {
        results.push({ f, title, snip: "" });
        if (results.length >= 30) break;
        continue;
      }
      const text = this._bookCache[f.path] || "";
      const i = text.toLowerCase().indexOf(ql);
      if (i >= 0) {
        const snip = text.slice(Math.max(0, i - 36), i + q.length + 60).replace(/\s+/g, " ").trim();
        results.push({ f, title, snip });
        if (results.length >= 30) break;
      }
    }
    if (!results.length) {
      pop.innerHTML = `<div class="rs-item rs-empty">没有匹配「${esc(q)}」的内容</div>`;
      return;
    }
    pop.innerHTML = results.map((r) => {
      let snip = esc(r.snip);
      const qi = snip.toLowerCase().indexOf(ql);
      if (qi >= 0) snip = snip.slice(0, qi) + "<mark>" + snip.slice(qi, qi + q.length) + "</mark>" + snip.slice(qi + q.length);
      return `<div class="rs-item" data-f="${esc(r.f.path)}"><span class="rs-name">${esc(r.title)}</span>${snip ? `<span class="rs-snip">…${snip}…</span>` : ""}</div>`;
    }).join("");
    pop.querySelectorAll(".rs-item[data-f]").forEach((el) =>
      el.addEventListener("click", () => {
        const r = results.find((x) => x.f.path === el.dataset.f);
        pop.hidden = true;
        if (r) this.loadChapter(r.f, toc.chapters.indexOf(r.f));
      })
    );
  }

  /* ---- 漫剧制作:当前小说 → 漫剧管理一条龙(查重 → 重跑/续跑/创建) ---- */
  // 从章节文件路径推导小说目录(小说根目录下第一级)
  novelDirOf(chapterPath) {
    const root = String(this.root || "").replace(/[\\/]+$/, "");
    const d = String(chapterPath || "").replace(/[\\/]+$/, "");
    if (!root || !d.toLowerCase().startsWith(root.toLowerCase())) return "";
    const rest = d.slice(root.length).replace(/^[\\/]+/, "");
    const book = rest.split(/[\\/]+/)[0];
    return book ? root + "/" + book : "";
  }
  ensureManju() {
    if (typeof ManjuWorkbench === "undefined") return false;
    if (!ManjuWorkbench._bound) ManjuWorkbench.bind();
    return true;
  }
  alertManju(html) {
    if (!this.ensureManju()) return;
    ManjuWorkbench.openModal("漫剧制作",
      `<div style="padding:8px 0;line-height:1.7">${html}</div>
       <div class="manju-row" style="margin-top:12px"><button id="mm-ok" class="hrs-btn hrs-btn-primary">确定</button></div>`);
    const ok = document.getElementById("mm-ok");
    if (ok) ok.addEventListener("click", () => ManjuWorkbench.closeModal());
  }
  async makeManju() {
    const toc = this._toc;
    const chs = (toc && toc.chapters) || [];
    if (!chs.length) return;
    if (!this.ensureManju()) return;
    const novelDir = this.novelDirOf(chs[0].path);
    if (!novelDir) return;
    const btn = document.getElementById("reader-script-btn");
    const setBtn = (busy, label) => {
      if (!btn) return;
      btn.disabled = busy;
      const span = btn.querySelector("span");
      if (span) span.textContent = label;
    };
    setBtn(true, "查询中…");
    try {
      // 查重:同名(小说目录)是否已存在于漫剧管理
      const found = await fetch("/api/manju/find?novel=" + encodeURIComponent(novelDir), { cache: "no-store" }).then((r) => r.json());
      if (found.exists) {
        this.promptManjuRun(found);
      } else if (found.nameTaken) {
        this.alertManju(`已存在同名项目「<b>${this.escapeHtml(found.nameTaken)}</b>」，但其小说目录与当前书籍不同，请到漫剧管理中确认后再操作。`);
      } else {
        await this.createManju(novelDir);
      }
    } catch (e) {
      this.alertManju("漫剧制作失败: " + this.escapeHtml(String((e && e.message) || e)));
    } finally {
      setBtn(false, I18N.t("book.script"));
    }
  }
  /* 项目已存在:提醒选择 重跑(一条龙)/续跑(接着跑)/取消 */
  promptManjuRun(found) {
    const chapters = found.chapters || "1-999";
    const episode = found.episode || "EP01";
    const self = this;
    ManjuWorkbench.openModal("漫剧制作",
      `<div style="text-align:center;padding:6px 0 2px">
        <div style="font-size:14px;font-weight:600;margin-bottom:6px">小说《${this.escapeHtml(found.name)}》在漫剧管理中已存在</div>
        <div class="manju-meta" style="margin-bottom:14px;line-height:1.7">
          小说目录：${this.escapeHtml(found.novelDir || found.novel || "")}<br>
          重跑 = 清空旧方案/镜头/缓存后从头渲染；续跑 = 从上次断点继续（已完成阶段/镜头自动跳过）
        </div>
        <div class="manju-row" style="justify-content:center;gap:10px">
          <button id="mm-rerun" class="hrs-btn hrs-btn-danger">🔁 重跑（一条龙）</button>
          <button id="mm-resume" class="hrs-btn hrs-btn-primary">▶ 续跑（接着跑）</button>
          <button id="mm-cancel" class="hrs-btn">取消</button>
        </div>
      </div>`, false);
    document.getElementById("mm-rerun").addEventListener("click", () => {
      ManjuWorkbench.closeModal();
      self.startManjuRun(found.configPath, chapters, episode, true);
    });
    document.getElementById("mm-resume").addEventListener("click", () => {
      ManjuWorkbench.closeModal();
      self.startManjuRun(found.configPath, chapters, episode, false);
    });
    document.getElementById("mm-cancel").addEventListener("click", () => ManjuWorkbench.closeModal());
  }
  /* 项目不存在:自动新建项目(封面/配置一并生成),交给用户在漫剧管理中配置参数后再启动 */
  async createManju(novelDir) {
    const name = novelDir.split(/[\\/]+/).filter(Boolean).pop() || "";
    if (!name) return;
    ManjuWorkbench.openModal("漫剧制作", `<div class="dir-loading">正在创建漫剧项目《${this.escapeHtml(name)}》…</div>`);
    try {
      const r = await fetch("/api/manju/create", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, novel: novelDir, apiKey: "" }),
      }).then((r) => r.json());
      ManjuWorkbench.closeModal();
      if (!r.ok || !r.configPath) {
        this.alertManju("创建项目失败: " + this.escapeHtml(String((r.output || r.error || "未知错误")).trim()));
        return;
      }
      // 只创建不自动跑:风格/画幅等参数需用户设置,切到漫剧管理后自行点「一条龙」
      try { localStorage.setItem("manju-project", r.configPath); } catch (e) {}
      this.closeReader();
      location.hash = "#/manju";
      const msg = document.getElementById("manju-render-msg");
      if (msg) msg.textContent = "✅ 项目已创建：请在「渲染配置」设置风格/画幅等参数后点「一条龙」";
    } catch (e) {
      ManjuWorkbench.closeModal();
      this.alertManju("创建项目失败: " + this.escapeHtml(String((e && e.message) || e)));
    }
  }
  /* 启动一条龙:跑全本自动分集,切到漫剧管理看进度 */
  startManjuRun(configPath, chapters, episode, fresh) {
    try { localStorage.setItem("manju-project", configPath); } catch (e) {}
    const btn = document.getElementById("reader-script-btn");
    const setBtn = (busy, label) => {
      if (!btn) return;
      btn.disabled = busy;
      const span = btn.querySelector("span");
      if (span) span.textContent = label;
    };
    setBtn(true, "启动中…");
    fetch("/api/manju/run", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: configPath, chapters, episode, phase: "all", fresh: !!fresh }),
    }).then((r) => r.json()).then((d) => {
      setBtn(false, I18N.t("book.script"));
      if (d.error) { this.alertManju("启动失败: " + this.escapeHtml(d.error)); return; }
      this.closeReader();
      location.hash = "#/manju";
    }).catch((e) => {
      setBtn(false, I18N.t("book.script"));
      this.alertManju("启动失败: " + this.escapeHtml(String((e && e.message) || e)));
    });
  }

  closeScript() {
    const sc = document.getElementById("reader-script");
    const content = document.getElementById("reader-content");
    const nav = document.getElementById("reader-nav");
    if (sc) {
      sc.classList.add("hidden");
      sc.innerHTML = "";
    }
    if (content) content.classList.remove("hidden");
    if (nav) nav.classList.remove("hidden");
  }

  /* ---- 漫剧:影音平台(海报墙:仓库网格,后端已按最近修改倒序) ---- */
  renderWall() {
    const listEl = document.getElementById(this.id + "-list");
    if (!this._projects.length) {
      listEl.innerHTML = `<div class="dir-empty">📭 ${I18N.t("dir.empty")}</div>`;
      return;
    }
    // 每张卡片随机取色(卡片与按钮共用,详情弹窗沿用卡片色)
    this._projects.forEach((p) => (p._hue = this.cardHue()));
    listEl.innerHTML = `
      <div class="shelf-head"><span class="shelf-count">🗂 ${I18N.t("media.repo")} · ${this._projects.length} ${I18N.t("media.works")}</span></div>
      <div class="fm-wall">${this._projects.map((p, i) => this.filmCard(p, i)).join("")}</div>`;
    listEl.querySelectorAll(".fm-card").forEach((card, i) => {
      card.addEventListener("click", (e) => {
        e.stopPropagation(); // 防止"打开弹窗"的点击被 document 层当作外部点击而立刻关闭
        this.openFilmModal(this._projects[i]);
      });
    });
    listEl.querySelectorAll(".fm-more").forEach((btn, i) => {
      btn.addEventListener("click", (e) => {
        e.stopPropagation(); // 阻止卡片点击打开弹窗
        this.openCardMenu(btn, this._projects[i]);
      });
    });
    this.applyListFilter();
  }

  /* 封面区:有封面图显示图片,没有则显示"暂无封面"占位 */
  coverArea(p) {
    const cover = this.randomCover(p); // 小说目录多张封面随机展示
    return cover
      ? `<img class="fm-cover-img" src="/api/fs/file?path=${encodeURIComponent(cover)}" alt="" loading="lazy">`
      : `<div class="fm-no-cover"><span class="fm-no-ic">🎬</span><span class="fm-no-t">${I18N.t("book.noCover")}</span></div>`;
  }

  filmCard(p, i) {
    const hue = p._hue;
    const eps = this.mediaCount(p, "videos");
    const imgs = this.mediaCount(p, "images");
    const tts = this.mediaCount(p, "tts");
    const meta = `${eps} ${I18N.t("media.videos")} · 🖼 ${imgs} · 🎙 ${tts}`;
    return `
      <div class="fm-card" style="--hue:${hue}" data-i="${i}">
        <div class="fm-poster">
          ${this.coverArea(p)}
          <button class="fm-more" title="${I18N.t("book.more")}" aria-label="${I18N.t("book.more")}">⋮</button>
          <span class="fm-play">▶</span>
          <div class="fm-scrim">
            <div class="fm-cname">${p.name}</div>
            <div class="fm-cmeta">${meta}</div>
          </div>
        </div>
      </div>`;
  }

  /* ---- 单个视频弹窗页:点击项目封面只展示视频(播放器 + 选集) ---- */
  openFilmModal(p) {
    const modal = document.getElementById("media-modal");
    if (!modal) return;
    document.getElementById("media-title").textContent = p.name;
    const content = document.getElementById("media-content");
    content.innerHTML = this.filmDetailHtml(p);
    modal.classList.add("is-open");
    modal.setAttribute("aria-hidden", "false");
    this.bindMediaModal();
    // 选集切换播放
    const player = content.querySelector("video");
    if (player) {
      const eps = content.querySelectorAll(".ep-item");
      const now = content.querySelector(".fm-now");
      eps.forEach((ep) =>
        ep.addEventListener("click", () => {
          eps.forEach((x) => x.classList.remove("is-active"));
          ep.classList.add("is-active");
          player.src = ep.dataset.src;
          player.play();
          if (now) now.textContent = "▶ " + ep.dataset.name;
        })
      );
    }
  }

  /* 单个视频弹窗页:只展示视频(播放器 + 选集) */
  filmDetailHtml(p) {
    const hue = p._hue != null ? p._hue : this.hueOf(p.name);
    const url = (x) => "/api/fs/file?path=" + encodeURIComponent(x);
    const vids = [
      ...((p.clips || {}).items || []),
      ...((p.final || {}).items || []),
      ...((p.videos || {}).items || []),
    ];
    if (!vids.length) return `<div class="dir-empty">—</div>`;
    let html = `<div class="fm-detail" style="--hue:${hue}">`;
    html += `<div class="fm-player"><video src="${url(vids[0].path)}" controls autoplay></video><div class="fm-now">▶ ${vids[0].name}</div></div>`;
    if (vids.length > 1) {
      html += `<div class="fm-episodes">${vids
        .map(
          (v, i) =>
            `<div class="ep-item ${i === 0 ? "is-active" : ""}" data-src="${url(v.path)}" data-name="${v.name.replace(/"/g, "&quot;")}"><span class="ep-no">${String(i + 1).padStart(2, "0")}</span><span class="ep-name">${v.name}</span></div>`
        )
        .join("")}</div>`;
    }
    html += `</div>`;
    return html;
  }

  bindMediaModal() {
    if (this._mmBound) return;
    this._mmBound = true;
    const modal = document.getElementById("media-modal");
    document.getElementById("media-close").addEventListener("click", () => this.closeFilmModal());
    document.addEventListener("click", (e) => {
      if (!modal.classList.contains("is-open")) return;
      if (e.target.closest(".ep-item")) return;
      if (e.target === modal || !modal.contains(e.target)) this.closeFilmModal();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && modal.classList.contains("is-open")) this.closeFilmModal();
    });
  }

  closeFilmModal() {
    const modal = document.getElementById("media-modal");
    modal.classList.remove("is-open");
    modal.setAttribute("aria-hidden", "true");
    const v = modal.querySelector("video");
    if (v) v.pause();
  }

  /* 三类统计 */
  mediaCount(p, kind) {
    const countOf = (k) => (p[k] || {}).count || 0;
    if (kind === "images") return countOf("characters") + countOf("scenes") + countOf("images");
    if (kind === "videos") return countOf("clips") + countOf("final") + countOf("videos");
    return countOf("tts");
  }

  escapeHtml(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
}

/* 实例:小说(书架平台) / 漫剧(影音平台) */
const NovelView = new DirView("novel", { root: "C:\\Mi\\Ai\\WorkBench\\novel", id: "novel", mode: "book" });
const ManjuView = new DirView("manju", { root: "C:\\Mi\\Ai\\WorkBench\\manju", id: "manju", mode: "film", hidden: ["manju_pipeline"] });
