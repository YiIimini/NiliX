// Package api 提供服务的 HTTP 接口（设置读写、连通测试、静态页）。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"nilix/internal/backend"
	"nilix/internal/config"
	"nilix/internal/kb_work"
	"nilix/internal/render"
	"nilix/internal/sysmon"
)

// Server 持有配置存储与内存态。
type Server struct {
	mu        sync.RWMutex
	store     *config.Store
	cfg       *config.Settings
	indexHTML []byte
	renderMgr *render.Manager
	sysmon    *sysmon.Collector
	kbStore   *kb_work.Store
	kbRoot    string
	kbFS      fs.FS
	islandFS  fs.FS
	outDir    string
}

// NewServer 构造服务。
func NewServer(store *config.Store, cfg *config.Settings, indexHTML []byte, renderMgr *render.Manager, sysmon *sysmon.Collector, kbStore *kb_work.Store, kbRoot string, kbFS, islandFS fs.FS, outDir string) *Server {
	return &Server{store: store, cfg: cfg, indexHTML: indexHTML, renderMgr: renderMgr, sysmon: sysmon, kbStore: kbStore, kbRoot: kbRoot, kbFS: kbFS, islandFS: islandFS, outDir: outDir}
}

// Routes 返回路由。
// sessionToken 会话令牌(启动随机生成,注入前端):所有非 GET 请求校验 X-NiliX-Token,
// 防本机浏览器跨站/恶意页面/顺手 curl 触发写操作(读操作 GET 不校验——真正的读保护由 fs 白名单承担)。
var sessionToken string

// SetSessionToken 注入会话令牌(main 启动时调用)
func SetSessionToken(t string) { sessionToken = t }

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("POST /api/settings/test", s.handleTest)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/page", s.handleKBPage)
	mux.HandleFunc("GET /api/asset", s.handleKBAsset)
	mux.HandleFunc("GET /api/comfy", s.handleComfy)
	mux.HandleFunc("POST /api/comfy/start", s.handleComfyStart)
	mux.HandleFunc("POST /api/comfy/stop", s.handleComfyStop)
	mux.HandleFunc("POST /api/comfy/install", comfyInstallStart)
	mux.HandleFunc("GET /api/comfy/install/status", comfyInstallStatus)
	mux.HandleFunc("POST /api/comfy/install/stop", comfyInstallStop)
	// DeepSeek Harness 服务(监控/启动/重启,灵动岛 + 应用内嵌窗口共用)
	mux.HandleFunc("GET /api/harness", s.handleHarness)
	mux.HandleFunc("POST /api/harness/start", s.handleHarnessStart)
	mux.HandleFunc("POST /api/harness/restart", s.handleHarnessRestart)
	// 灵动岛配置(系统设置弹窗开关 ↔ settings.json;灵动岛轮询自身显隐)
	mux.HandleFunc("GET /api/island", s.handleIslandGet)
	mux.HandleFunc("POST /api/island", s.handleIslandPost)
	mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	mux.HandleFunc("GET /api/fs/analyze", s.handleFSAnalyze)
	// 审计 M12:选目录弹系统对话框改 POST——GET 未鉴权且会启动阻塞式 STA 对话框,
	// 恶意网页 <img src=".../fs/select"> 即可在用户桌面弹窗骚扰/诱导选择目录
	mux.HandleFunc("POST /api/fs/select", s.handleFSSelect)
	mux.HandleFunc("GET /api/fs/read", s.handleFSRead)
	mux.HandleFunc("GET /api/fs/file", s.handleFSFile)
	mux.HandleFunc("GET /api/fs/media", s.handleFSMedia)
	mux.HandleFunc("POST /api/script/generate", s.handleGenerateScript)
	// 网页版爽文小说创作(shuangwen-novel 技能流程固化)
	mux.HandleFunc("POST /api/novel/create", s.handleNovelCreate)
	mux.HandleFunc("POST /api/novel/analyze", s.handleNovelAnalyze)
	mux.HandleFunc("POST /api/novel/review", s.handleNovelReview)
	mux.HandleFunc("POST /api/novel/chapter", s.handleNovelChapter)
	mux.HandleFunc("POST /api/novel/delete", s.handleNovelDelete)
	mux.HandleFunc("GET /api/novel/progress", s.handleNovelProgress)
	mux.HandleFunc("POST /api/novel/auto", s.handleNovelAuto)
	mux.HandleFunc("POST /api/novel/auto/stop", s.handleNovelAutoStop)
	mux.HandleFunc("GET /api/novel/auto/status", s.handleNovelAutoStatus)
	mux.HandleFunc("GET /api/novel/status/all", s.handleNovelStatusAll)
	mux.HandleFunc("GET /api/script/styles", s.handleScriptStyles)
	mux.HandleFunc("POST /api/render", s.handleRender)
	mux.HandleFunc("GET /api/render/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/render/jobs/{id}", s.handleJobStatus)
	mux.HandleFunc("GET /api/outputs", s.handleOutputs)
	mux.HandleFunc("GET /clips/{file}", s.handleClipFile)
	registerManjuRoutes(mux)
	s.registerZcodeRoutes(mux) // 胶囊服务按钮:ZCode 启停/Bot 停止(原 Go 绑定 HTTP 化)
	if s.islandFS != nil {
		// 灵动岛页面同样注入会话令牌(占位符 /*__NILIX_TOKEN__*/ → 真实 token):
		// island JS 的 httpPost(http://127.0.0.1:8787/api/comfy/start 等写请求)
		// 必须带 X-NiliX-Token,否则被 auth 拦成 401「会话失效」——重构不能阉割胶囊按钮
		mux.Handle("/island/", noCacheHTML(s.tokenInject(http.StripPrefix("/island/", http.FileServer(http.FS(s.islandFS))))))
	}
	mux.HandleFunc("/manage/", s.handleManage)
	if s.kbFS != nil {
		// 根路径(kb 工作台页)经 FileServer 直接输出静态文件,不会走 handleManage 的令牌替换——
		// 必须包 tokenInject 把会话令牌占位符替换进 index.html,否则前端 NILIX_TOKEN 是字面占位符,
		// 所有写请求带错误 token 被 auth 拦成 401「会话失效」
		mux.Handle("/", noCacheHTML(s.tokenInject(http.FileServer(http.FS(s.kbFS)))))
	}
	return s.localHostOnly(s.auth(mux))
}

// localHostOnly DNS rebinding 防线(审计 H1):只接受本机 Host。
// 恶意域名 A 记录指向 127.0.0.1 时,浏览器同源策略把它视为同源,服务端渲染进 HTML 的
// 会话 token 会被攻击者页面读到——Host 头校验是第一道闸,拒绝一切非本机来源。
func (s *Server) localHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validLocalHost(r.Host) {
			writeErr(w, http.StatusForbidden, "非法 Host,仅接受本机访问")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validLocalHost 判断 Host 头是否为本机地址(兼容带/不带端口)
func validLocalHost(hostPort string) bool {
	h := hostPort
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		h = host
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// tokenInject 对 HTML 响应替换会话令牌占位符 /*__NILIX_TOKEN__*/ → 真实 token。
// 仅 index.html(路径 / 或 *.html)走缓冲替换;媒体/脚本直接透传(不整文件缓冲)。
func (s *Server) tokenInject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isHTML := r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, "/index.html") ||
			strings.HasSuffix(r.URL.Path, "/island/") || r.URL.Path == "/island"
		if isHTML {
			// 审计 H6:CSP——本地页面含用户可控 md 内容,XSS 后果放大;frame-ancestors 防嵌入。
			// frame-src 必须放行 ComfyUI(默认 127.0.0.1:8190,可自定义端口/地址)——此前缺
			// frame-src 回退 default-src 'self',跨源 iframe(ComfyUI)被浏览器阻止
			// "已阻止此内容。请与网站所有者联系以解决此问题。"
			// connect-src 必须放行本地控制端口 8799(主窗口按钮)/8788(胶囊)——此前缺
			// connect-src 回退 default-src 'self',跨端口 fetch 被 CSP 拦截,
			// 最小化/最大化/关闭按钮点击无效(页面 JS 静默失败,按钮无功能)。
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob:; media-src 'self' blob:; "+
					"connect-src 'self' http://127.0.0.1:* http://localhost:* ws://127.0.0.1:* ws://localhost:*; "+
					"frame-src 'self' http://127.0.0.1:* http://localhost:* ws://127.0.0.1:* ws://localhost:*; "+
					"frame-ancestors 'none'; base-uri 'self'")
			w.Header().Set("X-Frame-Options", "DENY")
		}
		if sessionToken == "" || !isHTML {
			next.ServeHTTP(w, r)
			return
		}
		rr := httptest.NewRecorder()
		next.ServeHTTP(rr, r)
		body := rr.Body.Bytes()
		if strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
			body = bytes.Replace(body, []byte("/*__NILIX_TOKEN__*/"), []byte(sessionToken), 1)
			// 实际根路径注入(JSON 编码转义反斜杠):前端 dirview 不再硬编码 C:\Mi\Ai\WorkBench\manju,
			// 自包含部署后 manju/novel 在 exe 目录旁,硬编码路径不存在 → /api/fs/analyze 非 2xx → "load failed"
			if mj, err := json.Marshal(ManjuRootDir); err == nil {
				body = bytes.Replace(body, []byte("/*__NILIX_MANJU_ROOT__*/"), mj, 1)
			}
			if nv, err := json.Marshal(NovelRootDir); err == nil {
				body = bytes.Replace(body, []byte("/*__NILIX_NOVEL_ROOT__*/"), nv, 1)
			}
		}
		for k, vs := range rr.Header() {
			// Content-Length 必须丢弃:占位符(21B)替换成 token(32B)后长度已变,
			// 保留旧值会让浏览器按旧长度截断 HTML → 页面 JS 缺失 → 窗口一片黑(刚踩的坑)
			if k == "Content-Length" {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rr.Code)
		_, _ = w.Write(body)
	})
}

// auth 非 GET 请求校验会话 token(静态资源/读接口 GET 放行)
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && sessionToken != "" {
			if r.Header.Get("X-NiliX-Token") != sessionToken {
				http.Error(w, `{"error":"会话失效,请刷新页面"}`, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// noCacheHTML 全部本地静态资源 no-cache(回源校验,ETag 未变走 304):
// 本地文件小、回源零成本,杜绝"改完代码但浏览器/WebView2 命中旧缓存,
// 修了却看不到/旧 JS 报错"的经典问题。HTML 内 ?v= 版本号保留作双保险。
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

type getSettingsResponse struct {
	Settings  config.Settings `json:"settings"`
	APIKeySet bool            `json:"api_key_set"`
}

type testItem struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type testResponse struct {
	LLM     testItem `json:"llm"`
	ComfyUI testItem `json:"comfyui"`
}

func (s *Server) handleManage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	out := s.indexHTML
	// 注入会话令牌(前端 window.NILIX_TOKEN + localStorage),供所有 API 写请求带 X-NiliX-Token
	if sessionToken != "" {
		out = bytes.Replace(out, []byte("/*__NILIX_TOKEN__*/"), []byte(sessionToken), 1)
	}
	// 实际根路径注入(与 tokenInject 同步):前端 dirview 读取真实 manju/novel 根
	if mj, err := json.Marshal(ManjuRootDir); err == nil {
		out = bytes.Replace(out, []byte("/*__NILIX_MANJU_ROOT__*/"), mj, 1)
	}
	if nv, err := json.Marshal(NovelRootDir); err == nil {
		out = bytes.Replace(out, []byte("/*__NILIX_NOVEL_ROOT__*/"), nv, 1)
	}
	_, _ = w.Write(out)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	view := *s.cfg
	set := view.LLM.APIKey != ""
	view.LLM.APIKey = maskKey(view.LLM.APIKey)
	if view.Agent != nil {
		// 深拷贝 Agent 节：浅拷贝下 view.Agent 仍指向共享配置，掩码会写回原值
		ag := *view.Agent
		ag.VisionAPIKey = maskKey(ag.VisionAPIKey)
		view.Agent = &ag
	}
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, getSettingsResponse{Settings: view, APIKeySet: set})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in config.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}

	s.mu.Lock()
	// 掩码表示"未修改"，沿用已有 key；空串表示清空。
	if strings.Contains(in.LLM.APIKey, "****") {
		in.LLM.APIKey = s.cfg.LLM.APIKey
	}
	if err := s.store.Save(&in); err != nil {
		s.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	s.cfg = &in
	s.mu.Unlock()

	// ComfyUI 启动参数单一数据源同步:改 comfy_url / input / output 立即生效
	// (否则 start 用新参数、stop/probe 用旧参数,自相矛盾)
	SetComfyParams(in.Render.ComfyURL, in.Paths.ComfyInput, in.Paths.ComfyOutput)
	s.renderMgr.SetComfyURL(in.Render.ComfyURL)
	// 全局智能体默认同步(settings 表单不带 agent 字段时保留旧值,指针+omitempty 已保证)
	SetGlobalAgentCfg(&in)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	res := testResponse{}

	if cfg.LLM.APIKey == "" {
		res.LLM = testItem{OK: false, Message: "未配置 API key"}
	} else {
		llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
			time.Duration(cfg.LLM.RequestTimeout)*time.Second)
		if err := llm.Test(ctx); err != nil {
			res.LLM = testItem{OK: false, Message: err.Error()}
		} else {
			res.LLM = testItem{OK: true, Message: "连通正常（" + cfg.LLM.Model + "）"}
		}
	}

	cui := backend.NewComfyUIClient(cfg.Render.ComfyURL)
	if ss, err := cui.SystemStats(ctx); err != nil {
		res.ComfyUI = testItem{OK: false, Message: err.Error()}
	} else {
		res.ComfyUI = testItem{OK: true, Message: "在线，ComfyUI " + ss.System.ComfyUIVersion}
	}

	writeJSON(w, http.StatusOK, res)
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	// 全部 API 响应禁缓存:设置/agent 等配置接口若被 WebView2 启发式缓存,
	// 保存后重开弹窗会显示旧值,表现为"配置被清空/没保存上"
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
