/* 知识库轻量索引页:只加载 README.md 一个文件解析出全部节点(大类/子类/页面),
   点页面按需取 /api/page 详情;hash 锚点 #/kb/<页名> 直达。全目录扫描一概不做。 */
const KbView = {
  _md: "",
  _cats: [],   // [{emoji, name, count, subs:[{name, pages:[{name,desc}]}], root:[...]}]
  _bound: false,

  async enter() {
    if (!this._bound) {
      this._bound = true;
      const s = document.getElementById("kb-search");
      let t = null;
      s.addEventListener("input", () => {
        clearTimeout(t);
        t = setTimeout(() => this.render(s.value.trim()), 160);
      });
      document.getElementById("kb-refresh").addEventListener("click", () => {
        this._md = "";
        this.enter();
      });
    }
    // 锚点:#/kb/页面名 → 渲染后直接开详情
    const anchor = decodeURIComponent((location.hash.split("/")[2] || "").trim());
    if (!this._md) {
      const box = document.getElementById("kb-graph");
      if (box) box.innerHTML = "";
      document.getElementById("kb-legend").innerHTML = "<span class='nv-status'>索引加载中…</span>";
      try {
        const r = await fetch("/api/page?id=README", { cache: "no-store" });
        if (!r.ok) throw new Error("HTTP " + r.status);
        const p = await r.json();
        this._md = p.markdown || "";
        this.parse();
      } catch (e) {
        document.getElementById("kb-legend").innerHTML = "<span class='nv-status'>📭 索引加载失败: " + e.message + "</span>";
        return;
      }
    }
    // 页面互链边:后端图谱接口有真实双链(2058 条),一次拉取缓存;没有时静默降级
    if (!this._edges) {
      try {
        const r2 = await fetch("/api/graph", { cache: "no-store" });
        if (r2.ok) {
          const g = await r2.json();
          this._edges = (g.links || []).filter((l) =>
            l.source !== "README" && l.target !== "README" &&
            !String(l.source).startsWith("cat:") && !String(l.target).startsWith("cat:"));
        }
      } catch (e2) { this._edges = []; }
    }
    this.render(document.getElementById("kb-search").value.trim());
    if (anchor) this.openPage(anchor);
  },

  /* 全图适配:force 布局收敛期间轮询取布局坐标,包围盒稳定后缩小到全部可见(只做一次) */
  _fitTry: 0,
  scheduleFit() {
    if (this._fitDone) return;
    this._fitTry = 0;
    this._lastFitZ = 0;
    const tick = () => {
      this._fitTry++;
      if (this._fitTry > 12) return;               // 最多 6s,不打扰用户
      const stable = this.fitAllTry();
      if (!stable) setTimeout(tick, 500);
    };
    setTimeout(tick, 300);
  },
  fitAllTry() {
    const c = this._chart;
    if (!c) return true;
    const series = c.getModel().getSeriesByIndex(0);
    const data = series && series.getData ? series.getData() : null;
    if (!data) return false;
    let x0 = 1e9, y0 = 1e9, x1 = -1e9, y1 = -1e9, n = 0;
    for (let i = 0; i < data.count(); i++) {
      const lay = data.getItemLayout(i);            // graph 布局 = [x, y] 像素(图坐标系)
      if (!lay || lay.length < 2 || isNaN(lay[0])) continue;
      let px;
      try { px = c.convertToPixel({ seriesIndex: 0 }, lay); } catch (e) { continue; }
      n++;
      x0 = Math.min(x0, px[0]); y0 = Math.min(y0, px[1]);
      x1 = Math.max(x1, px[0]); y1 = Math.max(y1, px[1]);
    }
    if (n < 5) return false;
    const el = document.getElementById("kb-graph");
    const w = (el && el.clientWidth) || 1200, h = (el && el.clientHeight) || 700;
    const cur = c.getOption().series[0].zoom || 1;      // 当前缩放(convertToPixel 坐标含它)
    const ratio = Math.min(w / Math.max(60, x1 - x0), h / Math.max(60, y1 - y0)) * 0.78;   // 余量留足,容忍布局微扩
    const z = Math.max(0.05, Math.min(1.6, cur * ratio)); // 相对当前缩放修正
    // 布局还在展开(坐标剧变)时不稳定,等 ratio 收敛
    if (Math.abs(ratio - this._lastFitZ) > 0.03 && this._fitTry < 6) {
      this._lastFitZ = ratio;
      return false;
    }
    this._fitDone = true;
    const cx = (x0 + x1) / 2, cy = (y0 + y1) / 2;
    c.setOption({ series: [{ zoom: z, center: [cx / w * 100 + "%", cy / h * 100 + "%"] }] }, { silent: true });
    return true;
  },

  /* 初始适配:按坐标包络盒算 zoom,仅布局模式变化或首次时重置(不打扰用户缩放) */
  _fitZoomFor(mode, nodes) {
    if (mode === "force") return 1;
    if (this._fitMode === mode && this._fitZoom) return this._fitZoom;
    const el = document.getElementById("kb-graph");
    let x0 = 1e9, y0 = 1e9, x1 = -1e9, y1 = -1e9;
    nodes.forEach((n) => {
      if (typeof n.x !== "number") return;
      x0 = Math.min(x0, n.x); y0 = Math.min(y0, n.y);
      x1 = Math.max(x1, n.x); y1 = Math.max(y1, n.y);
    });
    const w = (el && el.clientWidth) || 1200, h = (el && el.clientHeight) || 700;
    const z = Math.min(w / Math.max(80, x1 - x0), h / Math.max(80, y1 - y0)) * 0.86;
    this._fitMode = mode; this._fitZoom = Math.max(0.08, Math.min(1.4, z));
    return this._fitZoom;
  },

  /* 行星公转:全节点绕中心缓慢旋转(layout:none 坐标系;悬停/拖拽暂停;离开本页停止) */
  startSpin(nodes) {
    this.stopSpin();
    if (!nodes || !nodes.length) return;
    this._spinNodes = nodes;
    this._spinAng = this._spinAng || 0;
    this._spinPause = 0;
    if (this._chart) {
      // 注意:echarts off() 返回 undefined,不可链式 on
      const c = this._chart;
      c.off("mouseover", this._spinHovIn);
      c.off("mouseout", this._spinHovOut);
      this._spinHovIn = () => { this._spinPause++; };
      this._spinHovOut = () => { this._spinPause = Math.max(0, this._spinPause - 1); };
      c.on("mouseover", this._spinHovIn);
      c.on("mouseout", this._spinHovOut);
      const zr = c.getZr();
      if (zr) {
        zr.off("dragstart", this._spinDgIn);
        zr.off("dragend", this._spinDgOut);
        this._spinDgIn = () => { this._spinPause++; };
        this._spinDgOut = () => { this._spinPause = Math.max(0, this._spinPause - 1); };
        zr.on("dragstart", this._spinDgIn);
        zr.on("dragend", this._spinDgOut);
      }
    }
    const step = () => {
      const el = document.getElementById("kb-graph");
      if (!this._spinNodes || !this._chart || !el || !document.getElementById("view-kb").classList.contains("is-active")) return; // 离开页面自动停
      if (!this._spinPause) {
        this._spinAng += 0.0013;                    // 缓慢公转
        const cos = Math.cos(this._spinAng), sin = Math.sin(this._spinAng);
        this._spinNodes.forEach((n) => {
          if (typeof n.x !== "number") return;
          const x = n.x, y = n.y;
          n.x = x * cos - y * sin;
          n.y = x * sin + y * cos;
        });
        this._chart.setOption({ series: [{ data: this._spinNodes }] }, { lazyUpdate: true, silent: true });
      }
      this._spinT = setTimeout(step, 55);           // ≈18fps,肉眼顺滑且省电
    };
    step();
  },
  stopSpin() {
    clearTimeout(this._spinT);
    this._spinNodes = null;
  },

  /* 分类专属固定色:黄金角 HSL 散布,饱和/亮度收敛在高级感区间,同分类永远同色(图例一一对应) */
  catPalette(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
    const hue = Math.round(h % 360);
    return "hsl(" + hue + ", 62%, 60%)";
  },

  /* 解析 README 结构:## emoji 大类(N 页) / #### 子类(n) / - [[页]] — 描述 */
  parse() {
    const cats = [];
    const seenPage = {}; // 全局同名去重(索引冗余防崩)
    let cur = null, sub = null;
    for (const line of this._md.split("\n")) {
      const mh = line.match(/^## (.+?)\s*[((（]/);
      if (mh && /页/.test(line)) {
        const emojiM = mh[1].match(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u);
        const name = mh[1].replace(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/gu, "").trim();
        if (!name) continue;
        cur = { emoji: emojiM ? emojiM[0] : "📁", name, raw: mh[1], subs: [], root: [] };
        cats.push(cur); sub = null;
        continue;
      }
      const sh = line.match(/^#### (.+?)[(（]\d+[)）]\s*$/);
      if (sh && cur) {
        sub = { name: sh[1].trim(), pages: [] };
        cur.subs.push(sub);
        continue;
      }
      const im = line.match(/^-\s+\[\[([^\]]+)\]\]\s*(?:[—-]\s*(.*))?/);
      if (im && cur) {
        const name = im[1].trim();
        if (seenPage[name]) continue; // README 重复列名 → ECharts 重复节点会崩,只保留首个
        seenPage[name] = 1;
        const page = { name, desc: (im[2] || "").trim() };
        (sub ? sub.pages : cur.root).push(page);
      }
    }
    this._cats = cats;
  },

  /* Obsidian 式力导向图:节点=页面,枢纽=大类(同色星座),边=归属;点节点开详情(锚点),点枢纽下钻该类 */
  render(q) {
    const el = document.getElementById("kb-graph");
    if (!el || !this._cats.length) return;
    if (!this._chart) {
      this._chart = echarts.init(el);

      this._chart.on("click", (p) => {
        const d = p.data || {};
        if (d.kind === "page") this.openPage(d.id);
        else if (d.kind === "hub") { this._catFilter = this._catFilter === d.id ? null : d.id; this.render(document.getElementById("kb-search").value.trim()); }
      });
      window.addEventListener("resize", () => this._chart && this._chart.resize());
    }
    const ql = (q || "").toLowerCase();
    const hit = (p) => !ql || p.name.toLowerCase().includes(ql) || (p.desc || "").toLowerCase().includes(ql);
    const catHit = (c) => !this._catFilter || c.name === this._catFilter;

    // ===== 径向布局(预计算坐标,layout:'none' 零物理开销 → 拖拽缩放零卡顿) =====
    // README 居中 → 大类枢纽环列 → 页面绕各自枢纽成扇形簇(坐标由名字哈希决定,稳定不跳)
    const SYMS = ["circle", "rect", "triangle", "diamond", "pin", "roundRect", "arrow", "star"];
    const hash = (s) => { let h = 0; for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0; return h; };
    const nodes = [], links = [], legend = [];
    let total = 0;
    const visCats = this._cats.filter((c) => {
      const all = [...c.root, ...c.subs.flatMap((s) => s.pages)];
      return all.length && catHit(c);
    });
    const N = visCats.length || 1;
    const R1 = 190 + N * 36;                       // 枢纽环半径(更外扩,天然分散)
    const P = (typeof App !== "undefined" && App.prefs) ? App.prefs : {};
    const mode = P.kbLayout || "radial";
    const shapeOf = (h) => {
      const s = P.kbShape || "mixed";
      if (s === "circle") return "circle";
      if (s === "rect") return "rect";
      if (s === "diamond") return "diamond";
      return SYMS[h % SYMS.length];
    };
    const curve = (P.kbCurve != null ? P.kbCurve : 0.05);
    const lineOp = (P.kbLineOp != null ? P.kbLineOp : 0.2);
    // 删除前连线色=主题线色(--line)
    const lineColor = getComputedStyle(document.documentElement).getPropertyValue("--line").trim() || "rgba(128,128,128,.4)";
    const accent = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() || "#4A90D9";
    // 常显标签:按哈希取前 kbLabels 个页面亮名(枢纽恒显)
    const labelBudget = P.kbLabels || 0;
    // 中心:README 索引节点(force 模式不给坐标,物理模拟自然居中;radial/ring 固定原点)
    const central = mode !== "force";
    nodes.push({ id: "README", name: "README · 索引", kind: "page", category: "索引", x: 0, y: 0, fixed: central,
      symbol: "circle", symbolSize: 18,
      itemStyle: { color: App.catColor("索引"), borderColor: "#fff", borderWidth: 1.5, shadowBlur: 12 },
      label: { show: false } });
    visCats.forEach((c, ci) => {
      const ang = -Math.PI / 2 + ci * (Math.PI * 2 / N);
      const hx = Math.cos(ang) * R1, hy = Math.sin(ang) * R1;
      const matched = [...c.root, ...c.subs.flatMap((s) => s.pages)].filter(hit);
      const color = KbView.catPalette(c.name);
      const sym = SYMS[ci % SYMS.length];
      // 枢纽
      nodes.push({ id: "hub:" + c.name, name: "◈ " + c.name, kind: "hub", category: c.name, x: hx, y: hy, fixed: central,
        symbol: "circle", symbolSize: Math.min(30, 13 + matched.length * 0.3),
        itemStyle: { color, borderColor: "#fff", borderWidth: 1.2, shadowBlur: 10, shadowColor: color + "" },
        label: { show: false } });
      links.push({ source: "README", target: "hub:" + c.name,
        lineStyle: { color: lineColor, width: 1, opacity: 0.36, curveness: 0.1 } });
      // 页面簇:绕枢纽扇形散布(哈希抖动,确定性)
      const spread = (Math.PI * 2 / N) * 0.78;
      matched.forEach((p2, k) => {
        total++;
        const h = hash(p2.name);
        const t = matched.length === 1 ? 0.5 : k / (matched.length - 1);
        const a = ang - spread / 2 + t * spread + ((h % 17) - 8) * 0.012;
        let r = 130 + (h % 250) + Math.sqrt(k % 70) * 15;   // 更大散布半径,自然分散不扎堆
        let px = hx + Math.cos(a) * r, py = hy + Math.sin(a) * r;
        if (mode === "ring") { px = Math.cos(ang) * (R1 + 180 + (h % 90)); py = Math.sin(ang) * (R1 + 180 + (h % 90)); }
        const showLabel = labelBudget > 0 && (h % 97) < Math.max(1, Math.round(labelBudget / 5.8));
        nodes.push({ id: p2.name, name: p2.name, kind: "page", category: c.name,
          x: central ? px : undefined, y: central ? py : undefined,
          symbol: "circle", symbolSize: 3 + (h % 6),   // 删除前:大小按关联度小梯度
          itemStyle: { color, opacity: 0.92, borderColor: color, borderWidth: 0.6 },
          label: { show: false } });
        links.push({ source: "hub:" + c.name, target: p2.name,
          lineStyle: { color: lineColor, width: 1, opacity: 0.36, curveness: 0.1 } });
      });
      legend.push({ name: c.name, color, n: matched.length, emoji: c.emoji || "" });
    });
    // 页面↔页面双链弧线:互链的节点跨簇相连(同一对去重);跨类弧线更弯更亮,类内更柔
    if (this._edges && this._edges.length) {
      const nodeCat = {};
      nodes.forEach((n) => { if (n.kind === "page") nodeCat[n.id] = n.category; });
      const seen = new Set();
      for (const e of this._edges) {
        const a = nodeCat[e.source], b = nodeCat[e.target];
        if (!a || !b) continue;                     // 过滤后不存在的页
        const key = e.source < e.target ? e.source + "\u0000" + e.target : e.target + "\u0000" + e.source;
        if (seen.has(key)) continue;
        seen.add(key);
        const cross = a !== b;
        const hue = KbView.catPalette(a);
        links.push({
          source: e.source, target: e.target,
          lineStyle: { color: lineColor, width: 0.8, opacity: 0.3, curveness: 0.1 },
        });
      }
    }
    this._chart.setOption({
      backgroundColor: "transparent",
      animation: false,
      tooltip: { show: false },
      series: [{
        type: "graph",
        layout: mode === "force" ? "force" : "none",
        roam: true,
        draggable: true,
        force: mode === "force" ? {
          repulsion: (P.kbRepel != null ? P.kbRepel : 340),        // 删除前默认 340
          edgeLength: [60, 140],                                       // 删除前
          gravity: 0.06,                                               // 删除前
          friction: 0.5,
          layoutAnimation: true,          // Obsidian 灵魂:开场收敛动画
        } : undefined,
        progressive: 260, progressiveThreshold: 520,
        hoverAnimation: false,
        edgeSymbol: ["none", "none"],
        emphasis: { focus: "adjacency", label: { show: true, fontSize: 11.5, color: "#fff", fontWeight: 600 },
          lineStyle: { width: 1.4, opacity: 0.85, color: accent } },
        select: { label: { show: true, fontSize: 12, fontWeight: 700 }, itemStyle: { shadowBlur: 14 } },
        // 初始全图视野:按节点包络盒适配缩放(进入即看到全部节点);用户缩放后不重置
        center: ["50%", "50%"],
        zoom: mode === "force" ? 0.6 : this._fitZoomFor(mode, nodes),   // force: 先给个中远景,收敛后 fitAll
        label: { position: "right", distance: 4 },
        lineStyle: { curveness: 0.05 },
        data: nodes, links,
      }],
    }, true);
    this.stopSpin();  // 删除前无公转
    if (mode === "force") this.scheduleFit();
    document.getElementById("kb-legend").innerHTML = legend
      .map((l) => `<span class="kb-lg${this._catFilter === l.name ? " on" : ""}" data-cat="${l.name.replace(/"/g, "&quot;")}"><span class="gt-dot" style="background:${l.color}"></span>${l.emoji} ${l.name} <i>${l.n}</i></span>`).join("");
    document.querySelectorAll("#kb-legend .kb-lg").forEach((e2) =>
      e2.addEventListener("click", () => {
        this._catFilter = this._catFilter === e2.dataset.cat ? null : e2.dataset.cat;
        this.render(document.getElementById("kb-search").value.trim());
      })
    );
  },

  /* 详情:按需拉单页;hash 锚点同步(#/kb/<页名>),返回清锚点 */
  async openPage(name) {
    name = decodeURIComponent(name);
    const esc = (s) => String(s == null ? "" : String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])));
    const wb = typeof ManjuWorkbench !== "undefined" ? ManjuWorkbench : null;
    if (!wb) return;
    wb.openModal(name, `<div class="dir-loading">加载中…</div>`, true);
    location.hash = "#/kb/" + encodeURIComponent(name);
    try {
      const r = await fetch("/api/page?id=" + encodeURIComponent(name), { cache: "no-store" });
      if (!r.ok) throw new Error("页面不存在(索引可能过期,点右上角刷新)");
      const p = await r.json();
      wb.openModal(p.title || name,
        `<div class="kb-detail">
           <div class="kb-detail-meta">${esc(p.category || "")} · ${p.words || 0} 字${p.mtime ? " · 更新 " + esc(String(p.mtime).slice(0, 10)) : ""}</div>
           <div class="kb-detail-md markdown-body">${Markdown.render(p.markdown || "", p.category)}</div>
         </div>`, true);
    } catch (e) {
      wb.openModal(name, `<div class="dir-empty">❌ ${esc(e.message)}</div>`, true);
    }
  },
};
