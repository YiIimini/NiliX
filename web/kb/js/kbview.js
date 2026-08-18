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
      const box = document.getElementById("kb-cats");
      box.innerHTML = `<div class="dir-loading">索引加载中…</div>`;
      try {
        const r = await fetch("/api/page?id=README", { cache: "no-store" });
        if (!r.ok) throw new Error("HTTP " + r.status);
        const p = await r.json();
        this._md = p.markdown || "";
        this.parse();
      } catch (e) {
        box.innerHTML = `<div class="dir-empty">📭 索引加载失败: ${e.message}</div>`;
        return;
      }
    }
    this.render(document.getElementById("kb-search").value.trim());
    if (anchor) this.openPage(anchor);
  },

  /* 解析 README 结构:## emoji 大类(N 页) / #### 子类(n) / - [[页]] — 描述 */
  parse() {
    const cats = [];
    let cur = null, sub = null;
    for (const line of this._md.split("\n")) {
      const mh = line.match(/^## ([^ (（]+)[ (（]?/);
      if (mh && /页/.test(line)) {
        cur = { emoji: "", name: mh[1].replace(/^[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]+\s*/u, ""), raw: mh[1], subs: [], root: [] };
        cur.emoji = (mh[1].match(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u) || [""])[0];
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
        const page = { name: im[1].trim(), desc: (im[2] || "").trim() };
        (sub ? sub.pages : cur.root).push(page);
      }
    }
    this._cats = cats;
  },

  render(q) {
    const box = document.getElementById("kb-cats");
    const ql = (q || "").toLowerCase();
    const hit = (p) => !ql || p.name.toLowerCase().includes(ql) || (p.desc || "").toLowerCase().includes(ql);
    let html = "";
    let total = 0;
    for (const c of this._cats) {
      const subs = c.subs.map((s) => ({ ...s, pages: s.pages.filter(hit) })).filter((s) => s.pages.length);
      const root = c.root.filter(hit);
      const n = root.length + subs.reduce((a, s) => a + s.pages.length, 0);
      if (!n) continue;
      total += n;
      html += `<div class="kb-cat" data-cat="${c.name.replace(/"/g, "&quot;")}">
        <div class="kb-cat-head"><span class="kb-cat-emoji">${c.emoji || "📁"}</span><span class="kb-cat-name">${c.name}</span><span class="kb-cat-count">${n} 页</span></div>
        <div class="kb-cat-body">`;
      const chip = (p) => `<span class="kb-page" data-page="${p.name.replace(/"/g, "&quot;")}" title="${(p.desc || p.name).replace(/"/g, "&quot;")}">${p.name}</span>`;
      html += root.map(chip).join("");
      for (const s of subs) {
        html += `<div class="kb-sub"><div class="kb-sub-name">${s.name} <i>${s.pages.length}</i></div><div class="kb-sub-pages">${s.pages.map(chip).join("")}</div></div>`;
      }
      html += `</div></div>`;
    }
    box.innerHTML = html || `<div class="dir-empty">${q ? "🔍 没有匹配「" + q + "」的知识页" : "📭 索引为空"}</div>`;
    document.querySelector("#view-kb .ov-sub").textContent = `共 ${total} 页${q ? " 匹配" : ""} · 单文件索引 · 锚点直达`;
    box.querySelectorAll(".kb-page").forEach((el) =>
      el.addEventListener("click", () => this.openPage(el.dataset.page))
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
