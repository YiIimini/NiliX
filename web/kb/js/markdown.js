/* 轻量 Markdown 渲染(详情面板):标题/列表/表格/代码块/引用/图片/[[双链]] */
const Markdown = {
  _cat: "",
  esc(s) {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  },
  /* 图片相对路径(assets/xxx.jpg) → 资源 API 绝对地址;http(s)/已有 API 路径原样保留 */
  imgUrl(src) {
    src = (src || "").trim();
    if (/^(https?:)?\/\//.test(src) || src.startsWith("/api/")) return src;
    return (
      "/api/asset?cat=" +
      encodeURIComponent(this._cat || "") +
      "&p=" +
      encodeURIComponent(src)
    );
  },
  inline(s) {
    s = this.esc(s);
    s = s.replace(/`([^`]+)`/g, "<code>$1</code>");
    s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    s = s.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    // 图片 ![alt](src "title")
    s = s.replace(
      /!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
      (m, alt, src, title) =>
        `<img src="${this.imgUrl(src)}" alt="${alt}" title="${title || alt}" loading="lazy">`
    );
    s = s.replace(/\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g, (m, id, label) => {
      const safe = id.replace(/"/g, "&quot;");
      return `<span class="md-wiki" data-page="${safe}">${label || id}</span>`;
    });
    return s;
  },
  table(rows) {
    const cells = (r) => r.replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
    const header = cells(rows[0]);
    const body = rows
      .slice(1)
      .filter((r) => !/^\|?\s*:?-{2,}:?\s*\|/.test(r));
    let h =
      "<table><thead><tr>" +
      header.map((c) => `<th>${this.inline(c)}</th>`).join("") +
      "</tr></thead><tbody>";
    body.forEach((r) => {
      h +=
        "<tr>" + cells(r).map((c) => `<td>${this.inline(c)}</td>`).join("") + "</tr>";
    });
    return h + "</tbody></table>";
  },
  render(md, cat) {
    this._cat = cat || ""; // 当前页面分类,用于图片相对路径定位
    const lines = (md || "").split("\n");
    let html = "";
    let i = 0;
    let inCode = false;
    let codeBuf = [];
    const flushCode = () => {
      if (codeBuf.length) {
        html += "<pre><code>" + this.esc(codeBuf.join("\n")) + "</code></pre>";
        codeBuf = [];
      }
    };
    while (i < lines.length) {
      const line = lines[i];
      const t = line.trim();
      if (t.startsWith("```")) {
        if (inCode) { flushCode(); inCode = false; }
        else { flushCode(); inCode = true; }
        i++; continue;
      }
      if (inCode) { codeBuf.push(line); i++; continue; }
      if (!t) { i++; continue; }
      if (/^#{1,6}\s/.test(t)) {
        const level = t.match(/^#{1,6}/)[0].length;
        html += `<h${level}>${this.inline(t.replace(/^#{1,6}\s*/, ""))}</h${level}>`;
      } else if (/^\|.*\|$/.test(t)) {
        const rows = [];
        while (i < lines.length && /^\|.*\|$/.test(lines[i].trim())) {
          rows.push(lines[i].trim()); i++;
        }
        i--;
        html += this.table(rows);
      } else if (/^>\s?/.test(t)) {
        html += `<blockquote>${this.inline(t.replace(/^>\s?/, ""))}</blockquote>`;
      } else if (/^[-*]\s/.test(t)) {
        let list = `<ul><li>${this.inline(t.replace(/^[-*]\s/, ""))}</li>`;
        i++;
        while (i < lines.length && /^[-*]\s/.test(lines[i].trim())) {
          list += `<li>${this.inline(lines[i].trim().replace(/^[-*]\s/, ""))}</li>`;
          i++;
        }
        html += list + "</ul>";
        continue;
      } else if (/^\d+\.\s/.test(t)) {
        let list = `<ol><li>${this.inline(t.replace(/^\d+\.\s/, ""))}</li>`;
        i++;
        while (i < lines.length && /^\d+\.\s/.test(lines[i].trim())) {
          list += `<li>${this.inline(lines[i].trim().replace(/^\d+\.\s/, ""))}</li>`;
          i++;
        }
        html += list + "</ol>";
        continue;
      } else if (/^(-{3,}|\*{3,})$/.test(t)) {
        html += "<hr>";
      } else {
        html += `<p>${this.inline(t)}</p>`;
      }
      i++;
    }
    flushCode();
    return html;
  },
};
