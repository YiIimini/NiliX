/* 主窗口 frameless 控制(wails 单进程,外部 URL 无 wails runtime → HTTP 8799)。
   最小化/最大化/关闭按钮:index.html 内联 onclick 已绑定(HTML 原生,不依赖脚本执行,
   且避免与脚本 addEventListener 双重绑定导致一次点击触发两次请求)。
   本文件只负责 topbar 空白区拖拽(/move 增量)。 */
(function () {
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
        if (dx !== 0 || dy !== 0) fetch("http://127.0.0.1:8799/move?dx=" + dx + "&dy=" + dy).catch(function () {});
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
