/* 主窗口 frameless 控制(wails 单进程,外部 URL 无 wails runtime → HTTP 8799)。
   最小化/最大化/关闭按钮:index.html 内联 onclick 已绑定(HTML 原生,不依赖脚本执行,
   且避免与脚本 addEventListener 双重绑定导致一次点击触发两次请求)。
   本文件负责:
   1) topbar 空白区拖拽移动(/move 增量);
   2) 窗口边缘拖拽缩放(/resize 绝对尺寸)——frameless + 外部 URL 无 wails runtime,
      系统缩放热区不生效,只能前端边缘检测 + HTTP SetSize。 */
(function () {
  function ctlUrl(path) {
    return "http://127.0.0.1:8799" + path;
  }

  /* ---- 边缘缩放 ----
     在窗口四边/四角 8px 内按下鼠标 → 进入缩放:随鼠标移动累计 dx/dy,
     节流(>30ms 才发一次)避免高频 fetch。 */
  var RESIZE_EDGE = 8;
  var edge = null; // "n"|"s"|"e"|"w"|"ne"|"nw"|"se"|"sw"
  var sizing = false, sx = 0, sy = 0, sw = 0, sh = 0, lastSent = 0;

  function detectEdge(x, y, w, h) {
    var e = "";
    if (y <= RESIZE_EDGE) e += "n";
    else if (y >= h - RESIZE_EDGE) e += "s";
    if (x <= RESIZE_EDGE) e += "w";
    else if (x >= w - RESIZE_EDGE) e += "e";
    return e || null;
  }

  window.addEventListener("mousedown", function (e) {
    if (edge) return; // 已在缩放
    var ed = detectEdge(e.clientX, e.clientY, window.innerWidth, window.innerHeight);
    if (!ed) return;
    // 顶部边缘只允许中间区(避开右上角控制按钮/左上角品牌),四角正常
    if (ed === "n" && (e.clientX < 120 || e.clientX > window.innerWidth - 160)) return;
    edge = ed;
    sizing = true;
    sx = e.screenX; sy = e.screenY;
    sw = window.innerWidth; sh = window.innerHeight;
    e.preventDefault();
  });

  window.addEventListener("mousemove", function (e) {
    if (!sizing || !edge) return;
    var dx = e.screenX - sx, dy = e.screenY - sy;
    var nw = sw, nh = sh;
    if (edge.indexOf("e") >= 0) nw = sw + dx;
    if (edge.indexOf("s") >= 0) nh = sh + dy;
    if (edge.indexOf("w") >= 0) nw = sw - dx;
    if (edge.indexOf("n") >= 0) nh = sh - dy;
    nw = Math.max(900, nw);
    nh = Math.max(600, nh);
    var now = Date.now();
    if (now - lastSent > 30) {
      lastSent = now;
      fetch(ctlUrl("/resize?w=" + nw + "&h=" + nh)).catch(function () {});
    }
  });

  window.addEventListener("mouseup", function () {
    sizing = false;
    edge = null;
  });

  /* ---- topbar 空白区拖拽移动 ---- */
  var bind = function () {
    var topbar = document.querySelector(".topbar");
    if (topbar && !topbar.dataset.wdrag) {
      topbar.dataset.wdrag = "1";
      var dragging = false, lastX = 0, lastY = 0;
      topbar.addEventListener("mousedown", function (e) {
        if (e.target.closest(".controls, .nav, a, button, input, select, label")) return;
        dragging = true;
        lastX = e.screenX; lastY = e.screenY;
        e.preventDefault();
      });
      window.addEventListener("mousemove", function (e) {
        if (!dragging) return;
        var dx = e.screenX - lastX, dy = e.screenY - lastY;
        lastX = e.screenX; lastY = e.screenY;
        if (dx !== 0 || dy !== 0) fetch(ctlUrl("/move?dx=" + dx + "&dy=" + dy)).catch(function () {});
      });
      window.addEventListener("mouseup", function () { dragging = false; });
    }
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bind);
  } else {
    bind();
  }
  // SPA 路由切换后 topbar 若被重建,重试绑定(幂等)
  window.addEventListener("hashchange", function () { setTimeout(bind, 100); });
})();
