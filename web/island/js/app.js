/* SysMon 灵动岛:顶部居中胶囊,点击展开为完整监测面板 */
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);

  /* ========== 灵动岛展开/收起 ========== */
  let expanded = false;
  let lastCollapseAt = 0; // 收起时间戳:防收起动画后鼠标仍在胶囊位置导致闪烁重开
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
    expanded = on;
    document.body.classList.toggle("island-collapsed", !on);
    document.body.classList.toggle("island-expanded", on);
    if (!on) lastCollapseAt = Date.now();
    if (typeof setIsland === "function") {
      if (on) setIsland(true, 380, islandHeight()); // 展开即时触发窗口动画,无延迟
      else setIsland(false, 0, 0); // Go 侧窗口尺寸动画 + 圆角裁剪
    }
  }
  // 悬停胶囊展开;展开后鼠标落在面板内,离开面板才缩回
  $("pill").addEventListener("mouseenter", () => setExpanded(true));
  $("full").addEventListener("mouseleave", () => setExpanded(false));
  // 顶部已无收起/关闭按钮(右上角仅状态灯);收起仍由鼠标移出面板触发
  const btnCollapse = $("btn-collapse");
  if (btnCollapse) btnCollapse.addEventListener("click", () => setExpanded(false));
  const btnClose = $("btn-close");
  if (btnClose)
    btnClose.addEventListener("click", () => {
      if (typeof closeWin === "function") closeWin();
    });

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
    $("set-kb").checked = localStorage.getItem("sysmon-show-kb") !== "0";
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
  $("set-kb").addEventListener("change", () => {
    const show = $("set-kb").checked;
    localStorage.setItem("sysmon-show-kb", show ? "1" : "0");
    $("kb-row").style.display = show ? "" : "none";
    syncIslandSize(); // 行显隐改变内容高度,展开态下自适应窗口
  });
  $("set-fx").addEventListener("change", () => {
    const on = $("set-fx").checked;
    localStorage.setItem("sysmon-effects", on ? "1" : "0");
    document.body.classList.toggle("no-effects", !on);
  });

  // 初始化:主题 / KB 行显示 / 特效
  applySysTheme(localStorage.getItem("sysmon-theme") === "light");
  $("kb-row").style.display = localStorage.getItem("sysmon-show-kb") !== "0" ? "" : "none";
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
      ct.textContent = "";
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
    setPill("pi-gpu", "pill-gpu", g0 && g0.present ? g0.usage : 0);

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

    // GPU
    const g = s.gpu;
    if (g && g.present) {
      setMetric($("gpu-fill"), $("gpu-val"), g.usage, 0);
      const gt = $("gpu-temp");
      gt.textContent = Math.round(g.temp) + "°C";
      gt.className = "temp mono " + tempClass(g.temp);
      $("gpu-mem").textContent =
        "显存 " + g.memUsed + " / " + g.memTotal + (g.sharedUsed ? " · 共享 " + g.sharedUsed : "");
    } else {
      $("gpu-fill").className = "fill";
      $("gpu-val").className = "val mono";
      $("gpu-val").textContent = "N/A";
      $("gpu-temp").textContent = "";
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

    // KB 服务状态（NiliX 内置知识库，随服务常驻在线，仅提供"访问"）
    const kb = s.kb,
      dot = $("kb-dot"),
      txt = $("kb-txt"),
      meta = $("kb-meta"),
      btnOpen = $("kb-open");
    if (kb.online) {
      dot.className = "kb-dot on";
      txt.textContent = "在线";
      meta.textContent = kb.pages + " 页 · " + kb.categories + " 类";
      btnOpen.classList.remove("hidden");
    } else {
      dot.className = "kb-dot off";
      txt.textContent = "离线";
      meta.textContent = "";
      btnOpen.classList.add("hidden");
    }

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

  // KB 访问按钮:默认浏览器打开知识库
  $("kb-open").addEventListener("click", () => {
    if (typeof openKB === "function") openKB();
  });

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

  tick();
  setInterval(tick, 1500);
  renderClock();
  setInterval(renderClock, 1000);
})();
