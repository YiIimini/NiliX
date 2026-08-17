/* 首页:知识关系图谱(ECharts force 布局 · Obsidian 风格)
 * 参考 Obsidian Graph View:细点节点、细淡连线、无箭头、标签悬停显示、
 * 悬停高亮相邻、图例可开关分类、搜索聚焦 */
const GraphView = {
  chart: null,
  data: null,
  meta: null,
  selectedCats: new Set(), // 图例复选:选中的分类高亮(默认全不选中,节点照常全展示)
  search: "",
  selectedId: null,

  async load() {
    if (this.data && this.meta) return;
    const [g, m] = await Promise.all([App.api("/api/graph"), App.api("/api/meta")]);
    this.data = g;
    this.meta = m;
    App.setCats((m.categories || []).map((c) => c.name));
  },

  async render() {
    await this.load();
    const el = document.getElementById("graph");
    const stale = echarts.getInstanceByDom(el);
    if (stale) stale.dispose();
    this.chart = echarts.init(el);
    window.addEventListener("resize", () => this.chart && this.chart.resize());
    this.bindMagnet();
    this.applyOption();
    this.renderLegend();
    this.chart.off("click");
    this.chart.on("click", (p) => {
      // 点击节点:选中并看详情;点击空白:取消选中(不再跳转总览页)
      if (p.dataType === "node" && !p.data.isHub) this.select(p.data.id);
      else this.select(null);
    });
    // 光标:节点悬停变小手可点,连线悬停保持默认箭头
    this.chart.on("mouseover", (p) => {
      const zr = p.event && p.event.target;
      if (zr) zr.cursor = p.dataType === "node" ? "pointer" : "default";
    });
    this.chart.on("globalout", () => {
      const el = document.getElementById("graph");
      if (el) el.style.cursor = "default";
    });
    this.renderSide(this.selectedId);
    this.renderBottom(this.selectedId);
    this._sig = this.signature();
    App.updateLive("graph-live");
    this.startPolling();
  },

  /* 数据签名:内容变化检测 */
  signature() {
    return JSON.stringify(this.data) + "|" + JSON.stringify(this.meta);
  },

  /* 实时轮询:15s 检测知识库变化,变更时自动重渲染 */
  startPolling() {
    if (this._pollTimer) return;
    this._pollTimer = setInterval(async () => {
      if (!document.getElementById("view-graph").classList.contains("is-active")) return;
      try {
        const [g, m] = await Promise.all([App.api("/api/graph"), App.api("/api/meta")]);
        const s = JSON.stringify(g) + "|" + JSON.stringify(m);
        if (s !== this._sig) {
          this.data = g;
          this.meta = m;
          this._sig = s;
          App.clearPageCache();
          this.applyOption();
          this.renderLegend();
          this.renderSide(this.selectedId);
          App.updateLive("graph-live");
        }
      } catch (e) {}
    }, 15000);
  },

  /* 选中/取消选中:同步左侧栏 + 底部栏 + 右侧详情 */
  select(id) {
    this.selectedId = id;
    if (this.chart) {
      if (id) this.chart.dispatchAction({ type: "select", seriesIndex: 0, name: id });
      else this.chart.dispatchAction({ type: "unselect", seriesIndex: 0 });
    }
    this.renderSide(id);
    this.renderBottom(id);
    if (id) App.openDetail(id);
    else App.closeDetail();
  },

  refreshTheme() {
    if (this.chart && document.getElementById("view-graph").classList.contains("is-active")) {
      this.applyOption();
      this.renderLegend();
      if (this.selectedId) {
        this.chart.dispatchAction({ type: "select", seriesIndex: 0, name: this.selectedId });
      }
    }
  },

  /* 左侧栏:选中→选中节点+关联;未选中→全部页面(按分类分组) */
  renderSide(id) {
    const titleEl = document.getElementById("side-title");
    const countEl = document.getElementById("side-count");
    const listEl = document.getElementById("side-list");
    if (!listEl) return;
    const item = (n, active) =>
      `<div class="side-item ${active ? "is-active" : ""}" data-page="${n.id.replace(/"/g, "&quot;")}">
        <span class="si-dot" style="background:${App.catColor(n.category)}"></span>
        <span class="si-name" style="color:${App.catColor(n.category)}">${n.name}</span>
        <span class="si-meta">↗ ${n.size || 0}</span>
      </div>`;

    const icon = document.getElementById("side-icon");
    if (id) {
      titleEl.textContent = I18N.t("side.title.related");
      if (icon) icon.setAttribute("href", "#i-conn");
      // 选中节点本身置顶(高亮),下方为关联页面
      const self = this.data.nodes.find((n) => n.id === id);
      const nbrs = this.neighbors(id);
      countEl.textContent = (self ? 1 : 0) + nbrs.length;
      let html = "";
      if (self) html += item(self, true);
      html += nbrs.map((n) => item(n, false)).join("");
      listEl.innerHTML =
        html || `<div class="side-empty">🔗 ${I18N.t("side.empty")}</div>`;
    } else {
      if (icon) icon.setAttribute("href", "#i-list");
      const nodes = this.data.nodes.filter((n) => !n.isHub && !n.isIndex);
      if (this._topFilter) {
        // 大类下钻:该大类页面按子类分组,顶部返回行
        titleEl.textContent = this._topFilter;
        const sub = nodes.filter((n) => (n.top || n.category) === this._topFilter);
        countEl.textContent = sub.length;
        const groups = {};
        sub.forEach((n) => { (groups[n.category] = groups[n.category] || []).push(n); });
        const entries = Object.entries(groups).map(([cat, list]) => {
          list.sort((a, b) => (b.mtime || "").localeCompare(a.mtime || ""));
          return [cat, list, (list[0] && list[0].mtime) || ""];
        });
        entries.sort((a, b) => b[2].localeCompare(a[2]));
        let html = `<div class="side-item side-back" title="返回大类列表"><span class="si-dot" style="background:var(--muted)">←</span><span class="si-name" style="color:var(--muted)">全部大类</span></div>`;
        entries.forEach(([cat, list]) => {
          const color = App.catColor(cat);
          html += `<div class="side-group-title" style="color:${color}"><span class="gdot" style="background:${color}"></span>${cat}</div>`;
          html += list.map((n) => item(n, false)).join("");
        });
        listEl.innerHTML = html;
      } else {
        // 索引只列根目录大类(小类/页面不直接显示),点击下钻
        titleEl.textContent = I18N.t("side.title.all");
        const counts = {};
        nodes.forEach((n) => { const t = n.top || n.category; counts[t] = (counts[t] || 0) + 1; });
        const entries = Object.entries(counts).sort((a, b) => b[1] - a[1]);
        countEl.textContent = entries.length;
        listEl.innerHTML = entries.map(([cat, cnt]) => {
          const color = App.catColor(cat);
          return `<div class="side-item side-cat" data-cat="${cat.replace(/"/g, "&quot;")}" title="查看「${cat}」大类(含 ${cnt} 页)">
            <span class="si-dot" style="background:${color}"></span>
            <span class="si-name" style="color:${color}">${cat}</span>
            <span class="si-meta">📄 ${cnt}</span>
          </div>`;
        }).join("");
      }
      listEl.querySelectorAll(".side-cat").forEach((el2) =>
        el2.addEventListener("click", () => { this._topFilter = el2.dataset.cat; this.renderSide(null); })
      );
      const back = listEl.querySelector(".side-back");
      if (back) back.addEventListener("click", () => { this._topFilter = null; this.renderSide(null); });
      // 下钻视图里的页面项:点击选中图谱节点
      listEl.querySelectorAll(".side-item[data-page]").forEach((el2) =>
        el2.addEventListener("click", () => this.select(el2.dataset.page))
      );
      return;
    }
    listEl.querySelectorAll(".side-item").forEach((el2) =>
      el2.addEventListener("click", () => this.select(el2.dataset.page))
    );
  },

  /* 选中节点的关联页面(图谱边的双向邻居) */
  neighbors(id) {
    const out = [];
    (this.data.links || []).forEach((l) => {
      if (l.source === id && !l.target.startsWith("cat:")) out.push(l.target);
      else if (l.target === id && !l.source.startsWith("cat:")) out.push(l.source);
    });
    const byId = {};
    this.data.nodes.forEach((n) => (byId[n.id] = n));
    const seen = new Set();
    return out
      .map((nid) => byId[nid])
      .filter((n) => n && !seen.has(n.id) && seen.add(n.id));
  },

  /* 底部正中心:选中节点内容摘要;未选中隐藏(带竞态保护) */
  async renderBottom(id) {
    const bar = document.getElementById("bottom-bar");
    if (!bar) return;
    if (!id) {
      bar.classList.add("hidden");
      return;
    }
    const token = (this._bottomToken = (this._bottomToken || 0) + 1);
    let p;
    try {
      p = await App.getPage(id);
    } catch (e) {
      if (token === this._bottomToken) bar.classList.add("hidden");
      return;
    }
    if (token !== this._bottomToken) return; // 已切换到其他选择,丢弃过期结果
    document.getElementById("bb-dot").style.background = App.catColor(p.category);
    const bbTitle = document.getElementById("bb-title");
    bbTitle.textContent = p.title;
    bbTitle.style.color = App.catColor(p.category);
    document.getElementById("bb-meta").textContent =
      "↗ " + (p.links || []).length + " 关联 · " + (p.category || "—");
    bar.classList.remove("hidden");
  },

  /* 搜索由顶栏统一接管(顶栏 top-search 按视图分发) */

  /* 磁吸:鼠标靠近节点(阈值内)时,最近的节点自动吸附到鼠标上,方便点击 */
  bindMagnet() {
    const el = document.getElementById("graph");
    if (!el || this._magnetBound) return;
    this._magnetBound = true;
    let raf = null;
    el.addEventListener("mousemove", (e) => {
      if (raf) return;
      raf = requestAnimationFrame(() => {
        raf = null;
        this.magnetTick(e);
      });
    });
    el.addEventListener("mouseleave", () => this.releaseMagnet());
  },

  magnetTick(e) {
    const el = document.getElementById("graph");
    const rect = el.getBoundingClientRect();
    const mx = e.clientX - rect.left, my = e.clientY - rect.top;
    const chart = this.chart;
    if (!chart) return;
    const sdata = chart.getOption().series[0].data;
    if (!sdata || !sdata.length) return;
    const TH = 64; // 吸附阈值(px)
    let best = null, bestD = TH * TH;
    for (const d of sdata) {
      if (d.isHub) continue;
      const dx = mx - (d.x || 0), dy = my - (d.y || 0);
      const dd = dx * dx + dy * dy;
      if (dd < bestD) { bestD = dd; best = d; }
    }
    if (best && best.id !== this._magnet) this.releaseMagnet();
    if (best) {
      this._magnet = best.id;
      const col = App.catColor(best.category);
      chart.setOption({
        series: [{
          data: [{
            id: best.id,
            x: mx, y: my, fixed: true,
            itemStyle: { shadowBlur: 22, shadowColor: col + "cc" },
          }],
        }],
      });
      // 磁吸更新后保持选中节点的高亮与标签不消失
      if (this.selectedId) {
        chart.dispatchAction({ type: "select", seriesIndex: 0, name: this.selectedId });
      }
    }
  },

  /* 释放磁吸:节点回归力导向布局 */
  releaseMagnet() {
    if (!this._magnet) return;
    const id = this._magnet;
    this._magnet = null;
    if (this.chart) {
      this.chart.setOption({ series: [{ data: [{ id, fixed: false }] }] });
    }
  },

  /* 过滤:去掉枢纽节点 → 搜索聚焦(保留匹配节点与其相邻);分类不过滤,图例仅控制高亮 */
  filteredData() {
    let nodes = this.data.nodes.filter((n) => !n.isHub);
    const ids = new Set(nodes.map((n) => n.id));
    let links = this.data.links.filter(
      (l) =>
        !l.source.startsWith("cat:") &&
        !l.target.startsWith("cat:") &&
        ids.has(l.source) &&
        ids.has(l.target)
    );
    if (this.search) {
      const q = this.search.toLowerCase();
      const matched = new Set(
        nodes.filter((n) => (n.name || n.id).toLowerCase().includes(q)).map((n) => n.id)
      );
      const keep = new Set(matched);
      links.forEach((l) => {
        if (matched.has(l.source) || matched.has(l.target)) {
          keep.add(l.source);
          keep.add(l.target);
        }
      });
      nodes = nodes.filter((n) => keep.has(n.id));
      links = links.filter((l) => keep.has(l.source) && keep.has(l.target));
    }
    return { nodes, links };
  },

  applyOption() {
    const { nodes, links } = this.filteredData();
    const cs = getComputedStyle(document.documentElement);
    const v = (n) => cs.getPropertyValue(n).trim();
    const text = v("--text") || "#e6edf7";
    const muted = v("--muted") || "#8b98ad";
    const line = v("--line") || "rgba(148,183,255,0.14)";
    const accent = v("--accent") || "#38bdf8";

    const sel = this.selectedCats;
    const active = sel.size > 0; // 有复选分类时:选中节点高亮,其余淡化
    const byId = {};
    nodes.forEach((n) => (byId[n.id] = n));
    const data = nodes.map((n) => {
      const col = App.catColor(n.category); // 主题派生色,与主题统一
      const isSel = active && sel.has(n.category);
      return {
        id: n.id,
        name: n.name,
        category: n.category,
        isIndex: n.isIndex,
        symbolSize: Math.max(5.5, Math.min(10, 4 + (n.size || 1) * 0.9)) * (isSel ? 1.15 : 1),
        label: { color: col }, // 节点文字与节点球同色
        // 选中态:节点球分类色外圈发光 + 文字同色发光(持久高亮)
        select: {
          label: { textShadowBlur: 9, textShadowColor: col + "cc" },
          itemStyle: {
            borderColor: col,
            borderWidth: 2.2,
            shadowColor: col + "cc",
            shadowBlur: 24,
            opacity: 1,
          },
        },
        itemStyle: {
          color: col,
          shadowColor: col + (isSel ? "aa" : "22"),
          shadowBlur: isSel ? 14 : 3,
          opacity: active && !isSel ? 0.18 : 0.94,
        },
      };
    });
    // 连线:选中分类相关的边保留可见,其余淡化;hover 时线自身加亮加宽(两端节点由 focus 高亮)
    const linkData = links.map((l) => {
      const s = byId[l.source], t = byId[l.target];
      const on = active && ((s && sel.has(s.category)) || (t && sel.has(t.category)));
      return {
        source: l.source,
        target: l.target,
        lineStyle: { opacity: active ? (on ? 0.5 : 0.08) : 0.36 },
        emphasis: {
          lineStyle: {
            width: 2.4,
            opacity: 0.95,
            color: accent,
            shadowBlur: 10,
            shadowColor: accent,
          },
        },
      };
    });

    this.chart.setOption(
      {
        backgroundColor: "transparent",
        animation: false,
        series: [
          {
            type: "graph",
            layout: "force",
            roam: true,
            draggable: true,
            selectedMode: "single",
            force: {
              repulsion: 340,
              edgeLength: [60, 140],
              gravity: 0.06,
              friction: 0.5,
              layoutAnimation: false,
            },
            // Obsidian:无箭头(需显式禁用,否则走 ECharts 默认 circle+arrow)
            edgeSymbol: ["none", "none"],
            label: {
              show: false, // 默认隐藏,悬停/选中才显示(颜色取各节点分类色)
              position: "right",
              distance: 5,
              fontSize: 11.5,
            },
            emphasis: {
              focus: "adjacency",
              label: { show: true, fontWeight: 600 },
              lineStyle: { width: 1.4, opacity: 0.85, color: accent },
              itemStyle: { shadowBlur: 16 },
            },
            select: {
              label: { show: true, fontWeight: 700 },
              itemStyle: { shadowBlur: 20 },
            },
            lineStyle: { color: line, width: 1, curveness: 0.1, opacity: 0.36 },
            data,
            links: linkData,
          },
        ],
      },
      true
    );
    // 重渲染(搜索/图例/主题)后保持选中高亮
    if (this.selectedId) {
      this.chart.dispatchAction({ type: "select", seriesIndex: 0, name: this.selectedId });
    }
  },

  renderLegend() {
    const el = document.getElementById("graph-legend");
    if (!el) return;
    const cats = this.meta.categories || [];
    const allNames = [...cats.map((c) => c.name), "索引"]; // 导航条全部条目(含索引)
    const item = (name, label) => {
      const on = this.selectedCats.has(name);
      return `<span class="legend-item ${on ? "is-on" : ""}" data-cat="${name.replace(/"/g, "&quot;")}" style="color:${App.catColor(name)}"><span class="legend-dot" style="background:${App.catColor(name)};color:${App.catColor(name)}"></span>${label}</span>`;
    };
    el.innerHTML =
      cats.map((c) => item(c.name, c.name)).join("") +
      item("索引", I18N.t("graph.index"));
    const syncOn = () =>
      el.querySelectorAll(".legend-item").forEach((el2) =>
        el2.classList.toggle("is-on", this.selectedCats.has(el2.dataset.cat))
      );
    el.querySelectorAll(".legend-item").forEach((it) =>
      it.addEventListener("click", () => {
        const name = it.dataset.cat;
        if (name === "索引") {
          // 索引 = 全选/全不选切换(全选:导航条全高亮;再点:全部还原)
          if (this.selectedCats.size === allNames.length) this.selectedCats.clear();
          else this.selectedCats = new Set(allNames);
        } else if (this.selectedCats.has(name)) {
          this.selectedCats.delete(name);
        } else {
          this.selectedCats.add(name);
        }
        syncOn();
        this.applyOption();
      })
    );
  },
};
