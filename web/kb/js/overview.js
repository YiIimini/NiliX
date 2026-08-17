/* 二级页:最新总览(统计卡 + 分类分组 + 最近更新,15s 实时轮询) */
const OverviewView = {
  search: "", // 顶栏搜索词(过滤分类卡片/时间轴)
  async render() {
    let data;
    try {
      data = await App.api("/api/overview");
    } catch (e) {
      return;
    }
    const sc = document.querySelector(".view-overview");
    const scrollTop = sc ? sc.scrollTop : 0;
    App.setCats((data.categories || []).map((g) => g.name));
    this.renderStats(data);
    this.renderGroups(data.categories);
    this.renderRecent(data.recent);
    this.masonry(); // 分类瀑布:最短列优先分列
    this.layoutAll(); // S 形时间轴统一布线
    if (!this._resizeBound) {
      this._resizeBound = true;
      window.addEventListener("resize", () => {
        if (document.getElementById("view-overview").classList.contains("is-active")) {
          this.masonry();
          this.layoutAll();
        }
      });
    }
    this._sig = JSON.stringify(data);
    if (sc) sc.scrollTop = scrollTop; // 保留滚动位置
    if (App.pendingCat) {
      const el = document.getElementById("cat-" + encodeURIComponent(App.pendingCat));
      if (el) setTimeout(() => el.scrollIntoView({ behavior: "smooth", block: "start" }), 80);
      App.pendingCat = null;
    }
    App.updateLive("ov-live");
    this.startPolling();
  },

  /* 实时轮询:15s 检测知识库变化,变更时自动重渲染 */
  startPolling() {
    if (this._timer) return;
    this._timer = setInterval(async () => {
      if (!document.getElementById("view-overview").classList.contains("is-active")) return;
      try {
        const d = await App.api("/api/overview");
        const s = JSON.stringify(d);
        if (s !== this._sig) {
          App.clearPageCache();
          await this.render();
          App.updateLive("ov-live");
        }
      } catch (e) {}
    }, 15000);
  },

  renderStats(data) {
    const s = data.stats || {};
    // 最多页分类
    let topCat = null;
    (data.categories || []).forEach((g) => {
      if (!topCat || g.pages.length > topCat.pages.length) topCat = g;
    });
    const pages = s.pages || 0;
    // 每项指标分配主题派生色(随配色主题联动,7 项各不同)
    const pal = App.buildPalette();
    const map = {
      pages: [s.pages, "stats.pages", "i-page", pal[0]],
      categories: [s.categories, "stats.categories", "i-folder", pal[1]],
      links: [s.links, "stats.links", "i-link", pal[2]],
      words: [s.words, "stats.words", "i-text", pal[3]],
      avgWords: [pages ? Math.round((s.words || 0) / pages) : 0, "stats.avgWords", "i-gauge", pal[4]],
      lastUpdate: [App.fmtDate(s.lastUpdate).slice(5, 10), "stats.lastUpdate", "i-cal", pal[5]],
      topCat: [topCat ? topCat.pages.length : 0, topCat ? topCat.name : "—", "i-trophy", pal[6]],
    };
    // 按设置过滤启用的指标(顺序固定为默认顺序)
    const items = (App.overviewPrefs.stats || []).map((k) => map[k]).filter(Boolean);
    if (!items.length) {
      document.getElementById("stats").innerHTML = "";
      return;
    }
    document.getElementById("stats").innerHTML = items
      .map(
        ([n, k, ic, c]) =>
          `<div class="stat-card" style="--card-c:${c}"><div class="stat-ic"><svg class="ic" aria-hidden="true"><use href="#${ic}"/></svg></div><div class="stat-num">${n}</div><div class="stat-label">${I18N.t(k)}</div></div>`
      )
      .join("");
  },

  renderGroups(groups) {
    const wrap = document.getElementById("cat-groups");
    if (!groups || !groups.length) {
      wrap.innerHTML = "";
      return;
    }
    const limit = App.overviewPrefs.catLimit || 0; // 0 = 全部
    const q = (this.search || "").toLowerCase();
    const match = (p) =>
      !q ||
      (p.title || "").toLowerCase().includes(q) ||
      (p.snippet || "").toLowerCase().includes(q);
    wrap.innerHTML = groups
      .map((g, gi) => {
        const isSub = !!g.parent; // 子分类：缩进显示在大类下
        const pages = (limit > 0 ? g.pages.slice(0, limit) : g.pages).filter(match);
        if (q && !pages.length) return ""; // 搜索时无匹配的分类整体隐藏
        const color = App.catColor(g.name);
        const count =
          limit > 0 && g.pages.length > limit
            ? `${pages.length} / ${g.pages.length}`
            : `${pages.length}`;
        return `
        <div class="cat-group ${isSub ? "cat-sub" : ""}" id="cat-${encodeURIComponent(g.name)}">
          <div class="cat-head" data-cat="${g.name.replace(/"/g, "&quot;")}" style="color:${color}"><span class="dot" style="background:${color}"></span>${isSub ? `<span class="cat-sub-arrow">└</span>` : ""}<span class="cat-emoji">${App.catEmoji(g.name)}</span>${g.name}<span class="count">${count} ${I18N.t("stats.pages")}</span></div>
          <div class="cat-timeline" data-count="${pages.length}" data-start="${gi % 2}">
            <svg class="tl-curve" aria-hidden="true">
              <defs>
                <linearGradient id="ct-grad-${gi}" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0" stop-color="var(--accent)" stop-opacity=".10"/>
                  <stop offset="1" stop-color="var(--accent)" stop-opacity=".5"/>
                </linearGradient>
              </defs>
              <path d="" style="stroke:url(#ct-grad-${gi})"/>
            </svg>
            ${pages
              .map((p) => {
                const cat = App.catColor(g.name);
                return `
                <a class="ct-item" href="#/overview" data-page="${p.id.replace(/"/g, "&quot;")}">
                  <span class="tl-dot" style="background:${cat};box-shadow:0 0 10px ${cat},0 0 0 4px ${cat}30"></span>
                  <div class="ct-main">
                    <div class="ct-title" style="color:${cat}">${p.title}</div>
                    ${p.snippet ? `<div class="ct-snippet">${p.snippet}</div>` : ""}
                    <div class="ct-meta"><span class="cm-date">${App.fmtDate(p.mtime).slice(0, 10)}</span><span class="cm-words">${p.words}</span><span class="cm-links">↗ ${p.links}</span></div>
                  </div>
                </a>`;
              })
              .join("")}
          </div>
        </div>`;
      })
      .join("");
    wrap.querySelectorAll(".ct-item").forEach((c) =>
      c.addEventListener("click", (e) => {
        e.preventDefault(); // a 标签:阻止默认导航,打开详情弹窗
        App.openDetail(c.dataset.page);
      })
    );
  },

  renderRecent(list) {
    const el = document.getElementById("recent-list");
    if (!list || !list.length) {
      el.innerHTML = `<div class="chip">📭 ${I18N.t("recent.empty")}</div>`;
      return;
    }
    // 条数设置:0 = 全部;并应用搜索过滤
    const limit = App.overviewPrefs.recentCount || 0;
    const q = (this.search || "").toLowerCase();
    const items = (limit > 0 ? list.slice(0, limit) : list).filter(
      (p) => !q || (p.title || "").toLowerCase().includes(q)
    );
    if (!items.length) {
      el.innerHTML = `<div class="chip">🔍 ${I18N.t("recent.empty")}</div>`;
      return;
    }
    el.innerHTML = `
    <div class="timeline" data-count="${items.length}">
      <svg class="tl-curve" aria-hidden="true">
        <defs>
          <linearGradient id="tl-grad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stop-color="var(--accent)" stop-opacity=".18"/>
            <stop offset="1" stop-color="var(--accent)" stop-opacity=".8"/>
          </linearGradient>
        </defs>
        <path d=""/>
      </svg>
      ${items
        .map(
          (p) => {
            const color = App.catColor(p.category);
            return `
        <a class="tl-item" href="#/overview" data-page="${p.id.replace(/"/g, "&quot;")}">
          <span class="tl-dot" style="background:${color};box-shadow:0 0 10px ${color},0 0 0 4px ${color}30"></span>
          <div class="tl-main">
            <div class="tl-title" style="color:${color}">${p.title}</div>
            ${p.snippet ? `<div class="tl-snippet">${p.snippet}</div>` : ""}
            <div class="tl-time"><span class="cm-date">${App.fmtDate(p.mtime).slice(0, 10)}</span><span class="cm-words">${p.words}</span><span class="cm-links">↗ ${p.links}</span></div>
          </div>
        </a>`;
          }
        )
        .join("")}
    </div>`;
    el.querySelectorAll(".tl-item").forEach((c) =>
      c.addEventListener("click", (e) => {
        e.preventDefault(); // a 标签:阻止默认导航,打开详情弹窗
        App.openDetail(c.dataset.page);
      })
    );
  },

  /* 分类瀑布(masonry):块按"当前最短列"优先分配
   * 内容多的分类独自占位高,少的往下叠——块高由内容撑起,无悬空无空白 */
  masonry() {
    const wrap = document.getElementById("cat-groups");
    if (!wrap) return;
    const groups = Array.from(wrap.querySelectorAll(":scope > .cat-group"));
    if (!groups.length) return;
    const cols = window.innerWidth > 1450 ? 3 : window.innerWidth > 700 ? 2 : 1;
    const GAP = 18; // 与 CSS 的列内间距一致
    // 列容器数量与视口匹配
    let colsEl = Array.from(wrap.querySelectorAll(":scope > .cat-col"));
    while (colsEl.length < cols) {
      const c = document.createElement("div");
      c.className = "cat-col";
      wrap.appendChild(c);
      colsEl.push(c);
    }
    while (colsEl.length > cols) {
      colsEl.pop().remove();
    }
    colsEl.forEach((c) => (c.innerHTML = ""));
    // 逐块放入当前总高度最小的列
    const heights = colsEl.map(() => 0);
    groups.forEach((g) => {
      let min = 0;
      for (let i = 1; i < cols; i++) if (heights[i] < heights[min]) min = i;
      colsEl[min].appendChild(g);
      heights[min] += g.offsetHeight + GAP; // 实时测量(列等宽,高度稳定)
    });
  },

  /* 时间轴布线:
   * 左列/右列模式(rail>0):节点在侧边窄条内微摆成波,内容统一对侧(整齐)
   * 波浪模式:节点左右交替,卡片避让节点侧
   * opts: itemSel / padX / rail / railSide("left"|"right") / startRight */
  layoutTimeline(tl, opts = {}) {
    if (!tl) return;
    const W = tl.clientWidth || 300;
    const items = Array.from(tl.querySelectorAll(opts.itemSel || ".tl-item"));
    if (!items.length) return;
    const r = 5.5; // 圆点半径
    const startRight =
      opts.startRight !== undefined ? !!opts.startRight : tl.dataset.start === "1";
    const sideOf = (i) => (i % 2 === 1) !== startRight;
    const rail = opts.rail || 0;
    const railRight = opts.railSide === "right";
    const isRail = rail > 0;
    const padX = isRail ? 14 : opts.padX || Math.max(24, Math.round(W * 0.09));
    const padInline = Math.max(34, Math.round(W * 0.115) + 2); // 波浪模式卡片避让
    // 逐项实际高度累加,兼容不等高内容
    let y = 0;
    const pts = items.map((it, i) => {
      const h = it.offsetHeight || 58;
      let x;
      if (isRail) {
        x = railRight
          ? sideOf(i) ? W - 14 : W - rail + 12
          : sideOf(i) ? rail - 12 : padX;
      } else {
        x = sideOf(i) ? W - padX : padX;
      }
      const pt = { x, y: y + h / 2 };
      y += h;
      return pt;
    });
    // 相邻点间 cos 缓动采样:圆润过渡成波浪
    // 首尾各延伸一段,让首末节点嵌在曲线上而非悬在端点(单节点也有连线)
    const EXT = 14;
    const allPts = [
      { x: pts[0].x, y: Math.max(0, pts[0].y - EXT) },
      ...pts,
      { x: pts[pts.length - 1].x, y: pts[pts.length - 1].y + EXT },
    ];
    const steps = 20;
    let d = `M ${allPts[0].x} ${allPts[0].y}`;
    for (let i = 0; i < allPts.length - 1; i++) {
      const a = allPts[i], b = allPts[i + 1];
      for (let s = 1; s <= steps; s++) {
        const t = s / steps;
        const yv = a.y + (b.y - a.y) * t;
        const xv = a.x + (b.x - a.x) * (0.5 - 0.5 * Math.cos(Math.PI * t));
        d += ` L ${xv.toFixed(1)} ${yv.toFixed(1)}`;
      }
    }
    const path = tl.querySelector(".tl-curve path");
    if (path) path.setAttribute("d", d);
    items.forEach((it, i) => {
      const right = sideOf(i);
      const dot = it.querySelector(".tl-dot");
      if (right) {
        dot.style.left = "auto";
        dot.style.right = W - pts[i].x - r + "px";
      } else {
        dot.style.right = "auto";
        dot.style.left = pts[i].x - r + "px";
      }
      // 波浪模式:内容卡片避让节点侧(侧条模式卡片由 CSS 统一起点)
      if (it.classList.contains("ct-item") && !isRail) {
        it.style.paddingLeft = right ? "10px" : padInline + "px";
        it.style.paddingRight = right ? padInline + "px" : "10px";
      }
    });
  },

  /* 统一重排页面内所有时间轴(窗口缩放时保持波形自适应) */
  layoutAll() {
    document.querySelectorAll(".timeline, .cat-timeline").forEach((tl) => {
      const isCat = tl.classList.contains("cat-timeline");
      // 分类:左侧时间线(40px);最近更新:右侧时间线(40px)
      this.layoutTimeline(
        tl,
        isCat
          ? { itemSel: ".ct-item", rail: 40, railSide: "left" }
          : { itemSel: ".tl-item", rail: 40, railSide: "right" }
      );
    });
  },
};
