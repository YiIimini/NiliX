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

    const nodes = [], links = [], legend = [];
    let total = 0;
    for (const c of this._cats) {
      const all = [...c.root, ...c.subs.flatMap((s) => s.pages)];
      if (!all.length || !catHit(c)) continue;
      const matched = all.filter(hit);
      const color = App.catColor(c.name);
      if (matched.length || !ql) {
        nodes.push({ id: "hub:" + c.name, name: "◈ " + c.name, kind: "hub", category: c.name,
          symbolSize: Math.min(58, 26 + matched.length * 0.28), itemStyle: { color },
          label: { show: true, color, fontSize: 12, fontWeight: 700 } });
      }
      for (const p of matched) {
        total++;
        nodes.push({ id: p.name, name: p.name, kind: "page", category: c.name,
          symbolSize: 7 + Math.min(8, (p.desc || "").length / 22),
          itemStyle: { color, opacity: 0.88 },
          label: { show: false } });
        links.push({ source: "hub:" + c.name, target: p.name,
          lineStyle: { color, width: 1, opacity: 0.22, curveness: 0.08 } });
      }
      legend.push({ name: c.name, color, n: matched.length, emoji: c.emoji || "" });
    }
    this._chart.setOption({
      backgroundColor: "transparent",
      animation: false,
      tooltip: { show: false },
      series: [{
        type: "graph", layout: "force", roam: true, draggable: true,
        force: { repulsion: 130, edgeLength: [34, 110], gravity: 0.055, friction: 0.5, layoutAnimation: false },
        edgeSymbol: ["none", "none"],
        emphasis: { focus: "adjacency", label: { show: true, fontSize: 11 }, itemStyle: { shadowBlur: 14 } },
        label: { position: "right", distance: 4 },
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
    document.querySelector("#view-kb .ov-sub").textContent =
      `${total} 个节点 · ${legend.length} 个大类星座 · 单文件索引 · 点击节点看详情`;
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
