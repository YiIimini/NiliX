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
    this.render(document.getElementById("kb-search").value.trim());
    if (anchor) this.openPage(anchor);
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
    const R1 = 150 + N * 26;                       // 枢纽环半径
    // 中心:README 索引节点
    nodes.push({ id: "README", name: "README · 索引", kind: "page", category: "索引", x: 0, y: 0,
      symbol: "circle", symbolSize: 58,
      itemStyle: { color: App.catColor("索引") !== "#999" ? App.catColor("索引") : "#E8C268", borderColor: "#fff", borderWidth: 2, shadowBlur: 26, shadowColor: "rgba(232,194,104,.55)" },
      label: { show: true, position: "bottom", distance: 8, color: "#E8C268", fontSize: 13, fontWeight: 800 } });
    visCats.forEach((c, ci) => {
      const ang = -Math.PI / 2 + ci * (Math.PI * 2 / N);
      const hx = Math.cos(ang) * R1, hy = Math.sin(ang) * R1;
      const matched = [...c.root, ...c.subs.flatMap((s) => s.pages)].filter(hit);
      const color = App.catColor(c.name);
      const sym = SYMS[ci % SYMS.length];
      // 枢纽
      nodes.push({ id: "hub:" + c.name, name: "◈ " + c.name, kind: "hub", category: c.name, x: hx, y: hy,
        symbol: "circle", symbolSize: Math.min(46, 24 + matched.length * 0.22),
        itemStyle: { color, borderColor: color, borderWidth: 2, shadowBlur: 14, shadowColor: color + "" },
        label: { show: true, color, fontSize: 12, fontWeight: 700, position: "top", distance: 6 } });
      links.push({ source: "README", target: "hub:" + c.name,
        lineStyle: { color, width: 2.2, opacity: 0.5, curveness: 0.04 } });
      // 页面簇:绕枢纽扇形散布(哈希抖动,确定性)
      const spread = (Math.PI * 2 / N) * 0.78;
      matched.forEach((p2, k) => {
        total++;
        const h = hash(p2.name);
        const t = matched.length === 1 ? 0.5 : k / (matched.length - 1);
        const a = ang - spread / 2 + t * spread + ((h % 17) - 8) * 0.012;
        const r = 70 + (h % 130) + Math.sqrt(k % 40) * 16;
        nodes.push({ id: p2.name, name: p2.name, kind: "page", category: c.name,
          x: hx + Math.cos(a) * r, y: hy + Math.sin(a) * r,
          symbol: SYMS[h % SYMS.length], symbolSize: 6.5 + (h % 5) + Math.min(6, (p2.desc || "").length / 24),
          itemStyle: { color, opacity: 0.9, borderColor: color, borderWidth: 0.6 },
          label: { show: false } });
        links.push({ source: "hub:" + c.name, target: p2.name,
          lineStyle: { color, width: 1, opacity: 0.2, curveness: 0.05 } });
      });
      legend.push({ name: c.name, color, n: matched.length, emoji: c.emoji || "" });
    });
    this._chart.setOption({
      backgroundColor: "transparent",
      animation: false,
      tooltip: { show: false },
      series: [{
        type: "graph",
        layout: "none",                 // 坐标已预算,交互零物理开销
        roam: true,                     // 缩放/平移纯画布变换
        draggable: true,
        progressive: 260, progressiveThreshold: 520,  // 渐进渲染,大图不卡
        hoverAnimation: false,
        edgeSymbol: ["none", "none"],
        emphasis: { label: { show: true, fontSize: 11, color: "#fff" }, itemStyle: { shadowBlur: 12 } },
        label: { position: "right", distance: 4 },
        lineStyle: { curveness: 0.05 },
        data: nodes, links,
      }],
    }, true);
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
