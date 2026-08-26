/* SysMon 灵动岛:顶部居中胶囊,点击展开为完整监测面板 */
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  // 灵动岛启用状态(系统设置弹窗开关;disabled 时 body 加 island-disabled 隐藏胶囊)
  let islandEnabled = true;
  let islandCheckCount = 0;

  /* ========== 绑定兼容层 ==========
     go-webview2 旧实现注入 window.setIsland/startComfy 等 Go 绑定;
     wails 胶囊加载 8787 外部 URL 无注入 → fallback 到 HTTP API:
       - 窗口尺寸/关闭: 本机 8788(capsule 进程控制端口)
       - ComfyUI/Harness 启停: 8787 主服务 HTTP API
       - 打开浏览器: window.open */
  function nilixTok() {
    var t = window.NILIX_TOKEN;
    if (t && t.length > 8) return t;
    try { return localStorage.getItem("nilix_token") || ""; } catch (e) { return ""; }
  }
  function httpPost(url) {
    // POST 到 8787 主服务:写请求需要 X-NiliX-Token 鉴权
    return fetch(url, { method: "POST", headers: nilixTok() ? { "X-NiliX-Token": nilixTok() } : {} }).then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); });
  }
  function httpGet(url) {
    // GET 到 8787 主服务(同源,无 CORS 预检)
    return fetch(url, { headers: nilixTok() ? { "X-NiliX-Token": nilixTok() } : {} }).then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); });
  }
  // 控制端口(8788/8799)调用:本地无鉴权,不带自定义头——
  // 带 X-NiliX-Token 的自定义头会让跨端口 fetch 触发 CORS 预检(OPTIONS),
  // 而 8788 只注册 GET /size,无 OPTIONS handler → 预检失败 → fetch 被浏览器拦截,
  // 导致 HUD 展开/收起失效(用户反馈"展开只到胶囊大小"的真根因)。
  function ctlGet(url) {
    return fetch(url).then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); });
  }
  if (typeof setIsland !== "function") {
    window.setIsland = (expanded, w, h) => {
      // 用页面测量的动态高度(islandHeight):展开面板贴合内容,底部不留空白;
      // 收起回胶囊 300x44
      const nw = expanded ? (w && w >= 200 ? w : 380) : 300;
      const nh = expanded ? (h && h >= 40 ? h : 420) : 44;
      ctlGet("http://127.0.0.1:8788/size?w=" + nw + "&h=" + nh).catch(() => {});
    };
  }
  if (typeof closeWin !== "function") {
    // 8788 控制端口:不带自定义头(避免 CORS 预检,见 ctlGet 注释)
    window.closeWin = () => ctlGet("http://127.0.0.1:8788/close");
  }
  if (typeof startComfy !== "function") window.startComfy = () => httpPost("http://127.0.0.1:8787/api/comfy/start");
  if (typeof stopComfy !== "function") window.stopComfy = () => httpPost("http://127.0.0.1:8787/api/comfy/stop");
  if (typeof openComfy !== "function") window.openComfy = () => window.open("http://127.0.0.1:8190");
  if (typeof openKB !== "function") window.openKB = () => window.open("http://127.0.0.1:8787");
  // Harness(DSH)启停桥接已随 HUD 监控行删除(2026-08-26 用户要求)
  // ZCode/BOT:HTTP API(NiliX 主服务,原 Go 绑定 HTTP 化,重构不阉割功能)
  if (typeof startZCode !== "function") window.startZCode = () => httpPost("http://127.0.0.1:8787/api/zcode/start");
  if (typeof stopZCode !== "function") window.stopZCode = () => httpPost("http://127.0.0.1:8787/api/zcode/stop");
  if (typeof stopBot !== "function") window.stopBot = () => httpPost("http://127.0.0.1:8787/api/bot/stop");
  if (typeof restartBot !== "function") window.restartBot = () => httpPost("http://127.0.0.1:8787/api/bot/restart");

  /* ========== 灵动岛展开/收起 ========== */
  let expanded = false;
  let lastCollapseAt = 0; // 收起时间戳:防收起动画后鼠标仍在胶囊位置导致闪烁重开
  let collapseTimer = null; // 收起淡出阶段计时器(170ms 后切 collapsed)
  // 展开面板内容高度(#full 为自然高度,设置面板/行显隐变化后重新测量)。
  // 注意:窗口收起态只有 44px 高,.card{position:fixed;inset:0} 占满视口,
  // #full 作为 flex 子元素 offsetHeight 被父容器(视口)钳制为 44——必须用
  // scrollHeight(内容实际滚动高度,不受父容器高度约束),否则展开只到胶囊大小。
  function islandHeight() {
    const el = $("full");
    if (!el) return 380;
    return Math.max(1, el.scrollHeight || el.offsetHeight);
  }
  // 按内容高度自适应窗口(offsetHeight 同步强制布局,类切换后立即量到最终高度)
  function syncIslandSize() {
    if (!expanded || typeof setIsland !== "function") return;
    setIsland(true, 380, islandHeight());
  }
  function setExpanded(on) {
    if (on === expanded) return;
    if (on && Date.now() - lastCollapseAt < 350) return;
    if (on) {
      expanded = true;
      clearTimeout(collapseTimer); // 取消待处理的收起切换(淡出中鼠标又移入)
      document.body.classList.remove("island-closing");
      document.body.classList.add("island-expanded");
      document.body.classList.remove("island-collapsed");
      // 展开:加 expanded 类(#full display:flex)后强制同步布局,立即测量
      // scrollHeight(内容实际高度,不受父容器 44px 视口钳制)——一次到位,
      // 不要"先弹大再缩"(用户明确反感先高后收的跳变)。
      // 若测量异常(内容未就绪 scrollHeight 过小)才退回到 600 兜底再校准。
      void document.body.offsetHeight; // 强制同步布局(读取触发 reflow)
      var h0 = islandHeight();
      if (typeof setIsland === "function") {
        // scrollHeight 正常(内容行已渲染,通常 200+)→ 直接按内容高度展开
        setIsland(true, 380, Math.max(300, h0 + 2));
      }
      // 兜底:内容延迟加载(数据填充后行高变化)时若实际更高则上调,不缩小
      setTimeout(function () {
        if (expanded && typeof setIsland === "function") {
          var h = islandHeight();
          if (h + 2 > 300) setIsland(true, 380, h + 2);
        }
      }, 400);
      setTimeout(function () {
        if (expanded && typeof setIsland === "function") {
          var h = islandHeight();
          if (h + 2 > 300) setIsland(true, 380, h + 2);
        }
      }, 900);
    } else {
      // 收起:先播面板淡出(island-closing),170ms 后再切 collapsed——与窗口缩小动画同步,
      // 避免"内容瞬间消失"的生硬切换(升级动效)
      expanded = false;
      lastCollapseAt = Date.now();
      document.body.classList.add("island-closing");
      if (typeof setIsland === "function") setIsland(false, 0, 0); // Go 侧窗口尺寸动画 + 圆角裁剪
      clearTimeout(collapseTimer);
      collapseTimer = setTimeout(() => {
        document.body.classList.remove("island-closing");
        document.body.classList.add("island-collapsed");
        document.body.classList.remove("island-expanded");
      }, 170);
    }
  }
  // 悬停胶囊展开;展开后鼠标落在面板内,离开面板才缩回
  $("pill").addEventListener("mouseenter", () => setExpanded(true));
  // 点击胶囊同样展开(透明窗口部分场景 mouseenter 不可达,mousedown 更可靠)
  $("pill").addEventListener("mousedown", () => setExpanded(true));
  $("full").addEventListener("mouseleave", () => setExpanded(false));
  // 顶部已无收起/关闭按钮(右上角仅状态灯);收起仍由鼠标移出面板触发
  const btnCollapse = $("btn-collapse");
  if (btnCollapse) btnCollapse.addEventListener("click", () => setExpanded(false));
  // ✕ 全退防误触:点击后按钮变「退出?」(红色发光,3 秒内再点才真正退出,超时复原)。
  // 灵动岛 ✕ = 整个应用退出,与 ▾(收起)相邻极易误点——点 ▾ 或鼠标离开面板会取消上膛。
  const btnClose = $("btn-close");
  let closeArmed = false, closeTimer = null;
  const cancelCloseArm = () => {
    if (!closeArmed) return;
    closeArmed = false;
    clearTimeout(closeTimer);
    if (btnClose) {
      btnClose.textContent = "✕";
      btnClose.classList.remove("armed");
      btnClose.title = "关闭";
    }
  };
  if (btnClose)
    btnClose.addEventListener("click", () => {
      if (!closeArmed) {
        closeArmed = true;
        btnClose.textContent = "退出?";
        btnClose.classList.add("armed");
        btnClose.title = "再点一次退出 NiliX";
        closeTimer = setTimeout(cancelCloseArm, 3000);
        return;
      }
      clearTimeout(closeTimer);
      if (typeof closeWin === "function") closeWin();
    });
  // 点收起或鼠标离开面板:取消退出上膛,防"想收起误点 ✕ 后又点一下"的误退
  if (btnCollapse) btnCollapse.addEventListener("click", cancelCloseArm);
  const fullPanel = $("full");
  if (fullPanel) fullPanel.addEventListener("mouseleave", cancelCloseArm);

  /* ---- 主题切换(配色已移入设置面板) ---- */
  function applySysTheme(light) {
    if (light) document.documentElement.setAttribute("data-theme", "light");
    else document.documentElement.removeAttribute("data-theme");
  }

  /* ---- 设置面板:配色 / KB 显示 / 特效 ---- */
  const settingsBtn = $("settings-btn"),
    panel = $("settings-panel");
  function panelOpen() {
    return panel && !panel.classList.contains("hidden");
  }
  if (settingsBtn) {
    settingsBtn.addEventListener("click", () => {
      panel.classList.toggle("hidden");
      syncIslandSize(); // 设置面板开合改变内容高度,展开态下自适应窗口
    });
  }
  document.addEventListener("click", (e) => {
    if (panelOpen() && !panel.contains(e.target) && settingsBtn && !settingsBtn.contains(e.target)) {
      panel.classList.add("hidden");
    }
  });

  function syncSetUI() {
    const light = document.documentElement.getAttribute("data-theme") === "light";
    document.querySelectorAll(".set-btn[data-theme]").forEach((b) =>
      b.classList.toggle("on", (b.dataset.theme === "light") === light)
    );
    $("set-fx").checked = localStorage.getItem("sysmon-effects") === "1"; // 默认关闭
    document.body.classList.toggle("no-effects", !$("set-fx").checked);
  }
  document.querySelectorAll(".set-btn[data-theme]").forEach((b) =>
    b.addEventListener("click", () => {
      const light = b.dataset.theme === "light";
      applySysTheme(light);
      localStorage.setItem("sysmon-theme", light ? "light" : "dark");
      syncSetUI();
    })
  );
  $("set-fx").addEventListener("change", () => {
    const on = $("set-fx").checked;
    localStorage.setItem("sysmon-effects", on ? "1" : "0");
    document.body.classList.toggle("no-effects", !on);
  });

  // 初始化:主题 / 特效
  applySysTheme(localStorage.getItem("sysmon-theme") === "light");
  syncSetUI();

  /* ========== 渲染 ========== */
  const fmtRate = (mbs) => (mbs >= 1 ? mbs.toFixed(1) + " MB/s" : Math.round(mbs * 1024) + " KB/s");
  const tempClass = (t) => (t < 60 ? "ok" : t <= 78 ? "warn" : "hot");
  const loadClass = (v) => (v < 50 ? "ok" : v < 75 ? "warn" : "hot"); // <50绿 / 50-75黄 / ≥75红
  const setFill = (el, pct) => {
    el.style.width = Math.max(0, Math.min(100, pct || 0)).toFixed(1) + "%";
  };
  /* 数值滚动:从当前值平滑动画到目标值(easeOutCubic);关闭特效时直接取值 */
  const animateNum = (el, target, suffix, decimals) => {
    const to = Number(target) || 0;
    const from = parseFloat(el.dataset.v || "0") || 0;
    if (document.body.classList.contains("no-effects") || Math.abs(to - from) < 0.005) {
      el.textContent = to.toFixed(decimals) + suffix;
      el.dataset.v = to;
      return;
    }
    const dur = 550,
      t0 = performance.now();
    const step = (t) => {
      const p = Math.min(1, (t - t0) / dur);
      const e = 1 - Math.pow(1 - p, 3); // easeOutCubic
      const v = from + (to - from) * e;
      el.textContent = v.toFixed(decimals) + suffix;
      if (p < 1) requestAnimationFrame(step);
      else el.dataset.v = to;
    };
    requestAnimationFrame(step);
  };
  const setMetric = (fillEl, valEl, pct, decimals) => {
    const cls = loadClass(pct || 0);
    fillEl.className = "fill " + cls;
    valEl.className = "val mono " + cls;
    animateNum(valEl, pct || 0, "%", decimals);
    setFill(fillEl, pct);
  };

  // 设备控制中心渲染:EC 可用→风扇/模式/开关实时态;被拒→解锁按钮引导提权
  function renderHW(hw) {
    const box = $("hw-box");
    if (!box) return;
    const fans = $("hw-fans"), fanmax = $("hw-fanmax"), note = $("hw-note");
    const modes = $("hw-modes"), unlock = $("hw-unlock");
    const cool = $("hw-cool"), oc = $("hw-oc");
    // GPU 风扇副行(独立于控制卡,CPU 风扇并入控制卡首行)
    if (hw.ok && hw.cpuFan > 0) {
      fans.textContent = "CPU " + hw.cpuFan + " RPM";
    } else { fans.textContent = "CPU --"; }
    if (hw.ok && hw.gpuFan > 0) {
      $("gpu-fan").textContent = "风扇 GPU " + hw.gpuFan + " RPM";
    } else { $("gpu-fan").textContent = ""; }
    if (hw.ok && hw.gpuFanMax > 0) {
      fanmax.textContent = "/ " + hw.gpuFanMax;
    } else { fanmax.textContent = ""; }

    const locked = !!hw.denied || (!hw.ok && !hw.admin);
    box.classList.toggle("locked", locked);
    if (unlock) unlock.classList.toggle("hidden", !locked);
    if (modes) modes.classList.toggle("hidden", locked);
    const swRow = document.querySelector(".hw-switches");
    if (swRow) {
      swRow.classList.toggle("hidden", false); // 超频免管理员,恒显示;制冷在锁定态禁用
      cool.disabled = locked;
    }
    if (locked) {
      if (note) note.textContent = "风扇/模式/核心温度需管理员令牌(超频免提权)";
      return;
    }
    if (!hw.ok) {
      if (note) note.textContent = "本机无雷神同源 root\\wmi ACPIMethod 通道";
      return;
    }
    if (note) note.textContent = "模式 " + (hw.modeName || hw.mode) + (hw.quickCool ? " · 制冷中" : "");
    if (hw.modeOK && modes) {
      modes.querySelectorAll(".hw-mode").forEach((b) => {
        b.classList.toggle("on", Number(b.dataset.mode) === hw.mode);
      });
    }
    if (cool && !cool.checkedLocked) cool.checked = !!hw.quickCool;
    if (oc) oc.checked = !!hw.overclock;
  }

  // 设备控制事件:模式/制冷/超频/提权解锁
  function hwPost(act, val) {
    const body = val === undefined ? { act: act } : { act: act, val: val };
    const headers = { "Content-Type": "application/json" };
    if (nilixTok()) headers["X-NiliX-Token"] = nilixTok();
    return fetch("/api/hwctl", { method: "POST", headers: headers, body: JSON.stringify(body) })
      .then((r) => {
        if (r.ok) return;
        return r.json().catch(() => ({})).then((j) => { throw new Error(j.error || "HTTP " + r.status); });
      });
  }
  function bindHW() {
    const modes = $("hw-modes");
    if (modes) modes.addEventListener("click", (e) => {
      const b = e.target.closest(".hw-mode");
      if (!b || b.classList.contains("on")) return;
      b.dataset.label = b.textContent; b.textContent = "…";
      hwPost("mode", Number(b.dataset.mode)).catch(alertErr).finally(() => {
        b.textContent = b.dataset.label || b.textContent;
      });
    });
    const cool = $("hw-cool");
    if (cool) cool.addEventListener("change", function () {
      const on = this.checked; this.checkedLocked = true;
      hwPost("cool", on).catch((e) => { this.checked = !on; alertErr(e); }).finally(() => { this.checkedLocked = false; });
    });
    const oc = $("hw-oc");
    if (oc) oc.addEventListener("change", function () {
      const on = this.checked;
      hwPost("oc", on).catch((e) => { this.checked = !on; alertErr(e); });
    });
    const unlock = $("hw-unlock");
    if (unlock) unlock.addEventListener("click", function () {
      if (!confirm("将以管理员身份重启 NiliX(UAC 弹窗确认一次),解锁风扇/性能模式/核心温度。继续?")) return;
      this.textContent = "提权中…"; this.disabled = true;
      hwPost("elevate").catch((e) => { alertErr(e); this.textContent = "🔒 解锁设备控制"; this.disabled = false; });
    });
  }
  function alertErr(e) {
    try { console.warn("hwctl:", e && e.message); } catch (_) {}
  }

  function renderCores(cores) {
    const el = $("cores");
    if (!cores || !cores.length) {
      el.innerHTML = "";
      el.dataset.n = "";
      return;
    }
    const n = String(cores.length);
    if (el.dataset.n !== n) {
      el.innerHTML = cores.map(() => '<div class="core"><i style="height:6%"></i></div>').join("");
      el.dataset.n = n;
    }
    Array.from(el.children).forEach((c, i) => {
      const v = Math.max(5, Math.min(100, cores[i] || 0));
      const bar = c.firstElementChild;
      bar.style.height = v + "%";
      bar.className = "i " + loadClass(v); // 每核按自身负载着色
    });
  }

  /* 连续失败计数:服务端不可达时 HUD 明确显示离线,恢复后自动回到正常 */
  let failCount = 0;
  function markOffline() {
    document.body.classList.add("offline");
    const sd = $("status-dot");
    if (sd) sd.className = "status-dot off";
    const btag = document.querySelector(".btag");
    if (btag) btag.textContent = "OFFLINE";
  }

  async function tick() {
    // 灵动岛启用状态(系统设置弹窗开关 → settings.json):disabled 时 CSS 隐藏整个胶囊
    // (窗口保留但内容全透明;不用 SW_HIDE——与 Go 侧 SetWindowPos/重绘冲突会导致启动黑窗闪烁)
    // 每 5 次轮询(约 7.5s)查一次,避免频繁请求
    if (++islandCheckCount % 5 === 1) {
      try {
        const ir = await fetch("/api/island", { cache: "no-store" });
        if (ir.ok) {
          const id = await ir.json();
          const on = !id || id.enabled !== false;
          if (on !== islandEnabled) {
            islandEnabled = on;
            document.body.classList.toggle("island-disabled", !on);
          }
        }
      } catch (e) { /* 静默:后端不可达保持现状 */ }
    }
    let s;
    try {
      const r = await fetch("/api/stats", { cache: "no-store" });
      if (!r.ok) throw new Error("HTTP " + r.status);
      s = await r.json();
    } catch (e) {
      failCount++;
      if (failCount >= 3) markOffline();
      return;
    }
    failCount = 0;
    document.body.classList.remove("offline");
    const sd = $("status-dot");
    if (sd) sd.className = "status-dot";
    const btag = document.querySelector(".btag");
    if (btag) btag.textContent = "LIVE";

    // 负载等级驱动霓虹灯(低=白 / 中=黄 / 高=红快闪),边框/状态点警示跟随
    const g0 = s.gpu;
    const peak = Math.max(
      s.cpu.usage || 0,
      s.mem.percent || 0,
      g0 && g0.present ? g0.usage || 0 : 0
    );
    document.body.classList.toggle("warn", peak >= 50 && peak < 75);
    document.body.classList.toggle("high", peak >= 75);

    // CPU
    setMetric($("cpu-fill"), $("cpu-val"), s.cpu.usage, 1);
    const ct = $("cpu-temp");
    if (s.cpu.hasTemp) {
      ct.textContent = Math.round(s.cpu.temp) + "°C";
      ct.className = "temp mono " + tempClass(s.cpu.temp);
    } else {
      ct.textContent = "N/A"; // 无传感器显示 N/A(与内存/视频管理一致)
      ct.className = "temp mono";
    }
    renderCores(s.cpu.cores);

    // 收起胶囊:三指标文字按负载实时变色(<50绿/50-75黄/≥75红)
    const setPill = (itemId, valId, v) => {
      const item = $(itemId),
        val = $(valId);
      if (item) item.className = "pill-item " + loadClass(v || 0);
      if (val) val.textContent = Math.round(v || 0) + "%";
    };
    setPill("pi-cpu", "pill-cpu", s.cpu.usage);
    setPill("pi-mem", "pill-mem", s.mem.percent);
    // GPU 胶囊:与展开态/视频管理一致显示显存使用率(渲染场景主指标)
    const pillGpu = g0 && g0.present
      ? (g0.memPercent != null && g0.memPercent > 0 ? g0.memPercent : g0.usage)
      : 0;
    setPill("pi-gpu", "pill-gpu", pillGpu);

    // MEM(温度依赖硬件传感器,经 LHM 读取;无传感器显示 N/A)
    setMetric($("mem-fill"), $("mem-val"), s.mem.percent, 0);
    if (s.mem.hasTemp) {
      $("mem-temp").textContent = Math.round(s.mem.temp) + "°C";
      $("mem-temp").className = "temp mono " + tempClass(s.mem.temp);
    } else {
      $("mem-temp").textContent = "N/A";
      $("mem-temp").className = "temp mono";
    }
    $("mem-sub").textContent = "已用 " + s.mem.used + " / " + s.mem.total + " · 可用 " + s.mem.available;

    // GPU:主数值/进度条 = 显存使用率(与视频管理运行状态一致;H3 渲染显存常满、
    // 算力利用率波动,显存占用率更有参考性);温度照常;副行显存与算力并列
    const g = s.gpu;
    if (g && g.present) {
      const gpuPct = g.memPercent != null && g.memPercent > 0 ? g.memPercent : g.usage;
      setMetric($("gpu-fill"), $("gpu-val"), gpuPct, 0);
      const gt = $("gpu-temp");
      // 温度 >0 才显示;nvidia-smi 偶发返回 0(查询竞态)时显示 N/A,不显示"0°C"误导
      if (g.temp > 0) {
        gt.textContent = Math.round(g.temp) + "°C";
        gt.className = "temp mono " + tempClass(g.temp);
      } else {
        gt.textContent = "N/A";
        gt.className = "temp mono";
      }
      // 显存:占用/总量(主数值已是显存使用率,小字不再重复百分比)+ 算力利用率
      const useStr = g.usage >= 0 ? " · 算力 " + Math.round(g.usage) + "%" : "";
      $("gpu-mem").textContent =
        "显存 " + g.memUsed + " / " + g.memTotal + useStr + (g.sharedUsed ? " · 共享 " + g.sharedUsed : "");
    } else {
      $("gpu-fill").className = "fill";
      $("gpu-val").className = "val mono";
      $("gpu-val").textContent = "N/A";
      $("gpu-temp").textContent = "N/A";
      $("gpu-mem").textContent = "未检测到 NVIDIA GPU";
    }

    // SSD
    $("ssd-r").textContent = "⤓ 读 " + fmtRate(s.disk.readMBs);
    $("ssd-w").textContent = "⤒ 写 " + fmtRate(s.disk.writeMBs);
    $("ssd-cap").textContent =
      s.disk.used + " / " + s.disk.total + " · " + (s.disk.percent || 0).toFixed(0) + "%";

    // NET
    $("net-d").textContent = "↓ " + fmtRate(s.net.rxRate);
    $("net-u").textContent = "↑ " + fmtRate(s.net.txRate);
    $("net-total").textContent = "下行 " + s.net.rxTotal + " · 上行 " + s.net.txTotal;

    // 设备控制中心(雷神同源 EC 通道):风扇/模式/制冷/超频(2026-08-26)
    renderHW(s.hw || {});

    // ZCode 桌面端状态
    const z = s.zcode || {},
      zDot = $("zcode-dot"),
      zTxt = $("zcode-txt"),
      zMeta = $("zcode-meta"),
      zBtn = $("zcode-start"),
      zBtnStop = $("zcode-stop");
    if (z.running) {
      zDot.className = "kb-dot on";
      zTxt.textContent = "运行中";
      zMeta.textContent = (z.count > 1 ? z.count + " 进程" : "运行中") + (z.pid ? " · PID " + z.pid : "");
      zBtn.classList.add("hidden");
      zBtnStop.classList.remove("hidden");
    } else {
      zDot.className = "kb-dot off";
      zTxt.textContent = "未运行";
      zMeta.textContent = "";
      zBtn.classList.remove("hidden");
      zBtnStop.classList.add("hidden");
    }

    // BOT 服务状态(微信/飞书运行时)
    const b = s.bot || {},
      bDot = $("bot-dot"),
      bTxt = $("bot-txt"),
      bMeta = $("bot-meta"),
      bBtnStop = $("bot-stop"),
      bBtnRestart = $("bot-restart");
    if (b.online) {
      bDot.className = "kb-dot on";
      bTxt.textContent = "在线";
      bMeta.textContent = (b.channels ? b.channels + " 通道" : "") + (b.pid ? " · PID " + b.pid : "");
      bBtnStop.classList.remove("hidden");
      bBtnRestart.classList.remove("hidden");
    } else {
      bDot.className = "kb-dot off";
      bTxt.textContent = "离线";
      bMeta.textContent = "";
      bBtnStop.classList.add("hidden");
      bBtnRestart.classList.add("hidden");
    }

    // ComfyUI 服务状态
    const cf = s.comfy || {},
      cfDot = $("comfy-dot"),
      cfTxt = $("comfy-txt"),
      cfMeta = $("comfy-meta"),
      cfStart = $("comfy-start"),
      cfStop = $("comfy-stop"),
      cfOpen = $("comfy-open");
    if (cf.online) {
      cfDot.className = "kb-dot on";
      cfTxt.textContent = "在线";
      // 端口/地址以服务端启动参数为准(与 Comfy 页面同源,改 settings.json 即同步)
      const cfPort = cf.startup && cf.startup.port ? ":" + cf.startup.port : ":8190";
      cfMeta.textContent = cfPort + (cf.version ? " · v" + cf.version : "");
      cfStart.classList.add("hidden");
      cfStop.classList.remove("hidden");
      cfOpen.classList.remove("hidden");
      // 启动中状态:已就绪 → 恢复启动按钮
      if (cfyStarting) {
        cfyStarting = false;
        cfStart.textContent = "启动";
        cfStart.disabled = false;
      }
    } else {
      cfDot.className = "kb-dot off";
      cfTxt.textContent = cfyStarting ? "启动中…" : "离线";
      cfMeta.textContent = "";
      cfStart.classList.remove("hidden");
      cfStop.classList.add("hidden");
      cfOpen.classList.add("hidden");
    }

    // N_X NiliX 主应用窗口状态(经 8799 /nx 探测;工作台按钮开/关主窗口)
    const nxDot = $("nilix-dot"),
      nxTxt = $("nilix-txt"),
      nxMeta = $("nilix-meta"),
      nxWb = $("nilix-wb");
    if (nxDot) {
      fetch("http://127.0.0.1:8799/nx", { cache: "no-store" })
        .then((r) => r.json())
        .then((nx) => {
          const open = !!nx.window_open;
          nxDot.className = "kb-dot " + (open ? "on" : "off");
          nxTxt.textContent = open ? "窗口已开" : "窗口已关";
          // 右侧文本:端口 + PID(版本号不显示)
          const nxParts = [];
          if (nx.port) nxParts.push(":" + nx.port);
          if (nx.pid) nxParts.push("PID " + nx.pid);
          nxMeta.textContent = nxParts.join(" · ");
          nxWb.classList.remove("hidden");
          nxWb.textContent = open ? "工作台" : "打开";
          nxWb.dataset.open = open ? "1" : "0"; // 工作台按钮切换依据
        })
        .catch(() => {
          nxDot.className = "kb-dot off";
          nxTxt.textContent = "—";
          nxMeta.textContent = "";
          nxWb.classList.remove("hidden");
          nxWb.textContent = "工作台";
        });
    }

  // 页脚
  $("foot-uptime").textContent = "运行 " + s.meta.uptime;
  $("foot-procs").textContent = "进程 " + s.meta.procs;
  $("status-dot").classList.add("live");
  }

  /* 右下角时钟:月/日 + 时间(走秒) + 星期;收起胶囊同步 HH:MM */
  function renderClock() {
    const el = $("foot-clock");
    if (!el) return;
    const d = new Date();
    const p = (n) => String(n).padStart(2, "0");
    const wd = "日一二三四五六"[d.getDay()];
    el.textContent =
      p(d.getMonth() + 1) + "/" + p(d.getDate()) +
      " " + p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds()) +
      " 周" + wd;
    const pclock = $("pill-clock");
    if (pclock) pclock.textContent = p(d.getHours()) + ":" + p(d.getMinutes()) + " " + wd;
  }

  // ZCode 启动按钮
  $("zcode-start").addEventListener("click", function () {
    if (typeof startZCode !== "function") return;
    const btn = this;
    btn.textContent = "启动中…";
    btn.disabled = true;
    startZCode()
      .then(() => {
        // tick() 轮询到运行后自动恢复
        setTimeout(() => {
          btn.textContent = "启动";
          btn.disabled = false;
        }, 8000);
      })
      .catch(() => {
        btn.textContent = "失败";
        setTimeout(() => {
          btn.textContent = "启动";
          btn.disabled = false;
        }, 1500);
      });
  });

  // ZCode 停止按钮(停止后 tick() 轮询到未运行自动切回「启动」)
  $("zcode-stop").addEventListener("click", function () {
    if (typeof stopZCode !== "function") return;
    const btn = this;
    btn.textContent = "停止中…";
    btn.disabled = true;
    stopZCode()
      .then(() => {
        setTimeout(() => {
          btn.textContent = "停止";
          btn.disabled = false;
        }, 3000);
      })
      .catch(() => {
        btn.textContent = "停止";
        btn.disabled = false;
      });
  });

  // BOT 停止/重启按钮(停止后 ZCode 约 5s 自动重建接管)
  const bindBotBtn = (id, fn) => {
    $(id).addEventListener("click", function () {
      if (typeof fn !== "function") return;
      const btn = this;
      const label = btn.textContent;
      btn.textContent = "执行中…";
      btn.disabled = true;
      fn()
        .then(() => {
          setTimeout(() => {
            btn.textContent = label;
            btn.disabled = false;
          }, 4000);
        })
        .catch(() => {
          btn.textContent = "失败";
          setTimeout(() => {
            btn.textContent = label;
            btn.disabled = false;
          }, 1500);
        });
    });
  };
  bindBotBtn("bot-stop", () => (typeof stopBot === "function" ? stopBot() : Promise.reject()));
  bindBotBtn("bot-restart", () => (typeof restartBot === "function" ? restartBot() : Promise.reject()));

  // N_X 工作台按钮:打开/关闭 NiliX 主应用窗口(经 8799 控制端口;不带头防 CORS 预检)
  const nxWb = $("nilix-wb");
  if (nxWb) {
    nxWb.addEventListener("click", function () {
      const btn = this;
      btn.disabled = true;
      const before = (nxWb.dataset.open === "1");
      const url = before ? "http://127.0.0.1:8799/close" : "http://127.0.0.1:8799/open";
      fetch(url, { cache: "no-store" })
        .then(() => {
          // 状态由 tick() 轮询 /nx 自动刷新(300ms 后)
          setTimeout(() => { if (btn) btn.disabled = false; }, 600);
        })
        .catch(() => {
          if (btn) { btn.disabled = false; btn.textContent = "失败"; setTimeout(() => { btn.textContent = "工作台"; }, 1200); }
        });
    });
  }

  // ComfyUI 启动按钮:启动中状态由 tick() 检测到在线后自动恢复;30s 兜底复位
  let cfyStarting = false;
  $("comfy-start").addEventListener("click", function () {
    if (typeof startComfy !== "function" || cfyStarting) return;
    const btn = this;
    cfyStarting = true;
    btn.textContent = "启动中…";
    btn.disabled = true;
    startComfy()
      .then(() => {
        setTimeout(() => {
          if (cfyStarting) {
            cfyStarting = false;
            btn.textContent = "启动";
            btn.disabled = false;
          }
        }, 30000);
      })
      .catch(() => {
        cfyStarting = false;
        btn.textContent = "失败";
        setTimeout(() => {
          btn.textContent = "启动";
          btn.disabled = false;
        }, 1500);
      });
  });

  // ComfyUI 停止按钮(停止后 tick() 轮询到离线自动切回「启动」)
  $("comfy-stop").addEventListener("click", function () {
    if (typeof stopComfy !== "function") return;
    const btn = this;
    btn.textContent = "停止中…";
    btn.disabled = true;
    stopComfy()
      .then(() => {
        setTimeout(() => {
          btn.textContent = "停止";
          btn.disabled = false;
        }, 3000);
      })
      .catch(() => {
        btn.textContent = "停止";
        btn.disabled = false;
      });
  });

  // ComfyUI 访问按钮
  $("comfy-open").addEventListener("click", () => {
    if (typeof openComfy === "function") openComfy();
  });

  // DSH/Harness 监控行已删除(2026-08-26 用户要求)——HTML row、tick 渲染与按钮绑定一并移除

  // 启动即查灵动岛启用状态(不等 5 次轮询):若系统设置已禁用,立即隐藏,避免黑底胶囊残留
  fetch("/api/island", { cache: "no-store" })
    .then((r) => r.json())
    .then((id) => {
      const on = !id || id.enabled !== false;
      islandEnabled = on;
      document.body.classList.toggle("island-disabled", !on);
    })
    .catch(() => {});

  // 启动自检:页面初始为收起态(collapsed),强制窗口回到 300x44——
  // 防"胶囊以 HUD 卡片高度展示"(历史展开尺寸残留/上次退出时未收起)。
  // 延迟到 Go 侧窗口就绪(impl 已建)再调,否则 SetSize 无效。
  setTimeout(function () {
    if (!expanded && typeof setIsland === "function") {
      setIsland(false, 0, 0); // 强制收起(8788 /size?w=300&h=44)
    }
  }, 600);

  // tick 兜底:收起态但窗口高度异常(>60)时强制收回——防偶发"收起动画被
  // 打断/收起 fetch 失败"导致窗口停留在展开高度(内容已隐藏=大黑框)。
  function enforceCollapsedSize() {
    if (expanded || document.body.classList.contains("island-expanded")) return;
    if (typeof setIsland !== "function") return;
    var card = $("card");
    if (!card) return;
    // 仅当窗口确实过大时收回(通过 islandHeight 无法知道窗口高,用页面可见性判断:
    // collapsed 态 #full display:none,若窗口被撑高,背景卡片会盖住胶囊以外区域)
    // 直接无条件校正一次(幂等,SetSize 300x44 无副作用)
    setIsland(false, 0, 0);
  }
  setInterval(enforceCollapsedSize, 3000);

  tick();
  setInterval(tick, 1500);
  renderClock();
  setInterval(renderClock, 1000);
  bindHW();
})();
