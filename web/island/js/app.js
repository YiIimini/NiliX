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
  function httpPost(url) {
    return fetch(url, { method: "POST" }).then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); });
  }
  function httpGet(url) {
    return fetch(url).then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); });
  }
  if (typeof setIsland !== "function") {
    window.setIsland = (expanded, w, h) => {
      // 用页面测量的动态高度(islandHeight):展开面板贴合内容,底部不留空白;
      // 收起回胶囊 300x44
      const nw = expanded ? (w && w >= 200 ? w : 380) : 300;
      const nh = expanded ? (h && h >= 40 ? h : 420) : 44;
      httpGet("http://127.0.0.1:8788/size?w=" + nw + "&h=" + nh).catch(() => {});
    };
  }
  if (typeof closeWin !== "function") {
    window.closeWin = () => httpGet("http://127.0.0.1:8788/close");
  }
  if (typeof startComfy !== "function") window.startComfy = () => httpPost("http://127.0.0.1:8787/api/comfy/start");
  if (typeof stopComfy !== "function") window.stopComfy = () => httpPost("http://127.0.0.1:8787/api/comfy/stop");
  if (typeof openComfy !== "function") window.openComfy = () => window.open("http://127.0.0.1:8190");
  if (typeof openKB !== "function") window.openKB = () => window.open("http://127.0.0.1:8787");
  if (typeof startHarness !== "function") window.startHarness = () => httpPost("http://127.0.0.1:8787/api/harness/start");
  if (typeof restartHarness !== "function") window.restartHarness = () => httpPost("http://127.0.0.1:8787/api/harness/restart");
  if (typeof openHarness !== "function") window.openHarness = () => window.open("http://127.0.0.1:3080");
  // ZCode/BOT:HTTP API(NiliX 主服务,原 Go 绑定 HTTP 化,重构不阉割功能)
  if (typeof startZCode !== "function") window.startZCode = () => httpPost("http://127.0.0.1:8787/api/zcode/start");
  if (typeof stopZCode !== "function") window.stopZCode = () => httpPost("http://127.0.0.1:8787/api/zcode/stop");
  if (typeof stopBot !== "function") window.stopBot = () => httpPost("http://127.0.0.1:8787/api/bot/stop");
  if (typeof restartBot !== "function") window.restartBot = () => httpPost("http://127.0.0.1:8787/api/bot/restart");

  /* ========== 灵动岛展开/收起 ========== */
  let expanded = false;
  let lastCollapseAt = 0; // 收起时间戳:防收起动画后鼠标仍在胶囊位置导致闪烁重开
  let collapseTimer = null; // 收起淡出阶段计时器(170ms 后切 collapsed)
  // 展开面板内容高度(#full 为自然高度,设置面板/行显隐变化后重新测量)
  function islandHeight() {
    const el = $("full");
    if (!el) return 380;
    return Math.max(1, el.offsetHeight);
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
      if (typeof setIsland === "function") setIsland(true, 380, islandHeight()); // 展开即时触发窗口动画,无延迟
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

    // DeepSeek Harness 服务状态(底部监控;启动/重启/访问按钮)
    const hs = s.harness || {},
      hDot = $("harness-dot"),
      hTxt = $("harness-txt"),
      hMeta = $("harness-meta"),
      hStart = $("harness-start"),
      hRestart = $("harness-restart"),
      hOpen = $("harness-open");
    if (hs.online) {
      hDot.className = "kb-dot on";
      hTxt.textContent = "在线";
      hMeta.textContent = ":3080" + (hs.version ? " · " + hs.version : "");
      hStart.classList.add("hidden");
      hRestart.classList.remove("hidden");
      hOpen.classList.remove("hidden");
      if (harnessStarting) {
        harnessStarting = false;
        hStart.textContent = "启动";
        hStart.disabled = false;
      }
    } else {
      hDot.className = "kb-dot off";
      hTxt.textContent = harnessStarting ? "启动中…" : "离线";
      hMeta.textContent = "";
      hStart.classList.remove("hidden");
      hRestart.classList.add("hidden");
      hOpen.classList.add("hidden");
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

  // DeepSeek Harness:启动 / 重启 / 访问(桥接 Go 注入函数;状态由 tick() 轮询刷新)
  let harnessStarting = false;
  const bindHarnessBtn = (id, fn, busyLabel) => {
    $(id).addEventListener("click", function () {
      if (typeof fn !== "function") return;
      const btn = this;
      const label = btn.textContent;
      btn.textContent = busyLabel;
      btn.disabled = true;
      fn()
        .then(() => {
          setTimeout(() => {
            btn.textContent = label;
            btn.disabled = false;
          }, 5000);
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
  $("harness-start").addEventListener("click", function () {
    if (typeof startHarness !== "function" || harnessStarting) return;
    const btn = this;
    harnessStarting = true;
    btn.textContent = "启动中…";
    btn.disabled = true;
    startHarness()
      .then(() => {
        setTimeout(() => {
          if (harnessStarting) {
            harnessStarting = false;
            btn.textContent = "启动";
            btn.disabled = false;
          }
        }, 30000);
      })
      .catch(() => {
        harnessStarting = false;
        btn.textContent = "失败";
        setTimeout(() => {
          btn.textContent = "启动";
          btn.disabled = false;
        }, 1500);
      });
  });
  bindHarnessBtn("harness-restart", () => (typeof restartHarness === "function" ? restartHarness() : Promise.reject()), "重启中…");
  $("harness-open").addEventListener("click", () => {
    if (typeof openHarness === "function") openHarness();
  });

  // 启动即查灵动岛启用状态(不等 5 次轮询):若系统设置已禁用,立即隐藏,避免黑底胶囊残留
  fetch("/api/island", { cache: "no-store" })
    .then((r) => r.json())
    .then((id) => {
      const on = !id || id.enabled !== false;
      islandEnabled = on;
      document.body.classList.toggle("island-disabled", !on);
    })
    .catch(() => {});

  tick();
  setInterval(tick, 1500);
  renderClock();
  setInterval(renderClock, 1000);
})();
