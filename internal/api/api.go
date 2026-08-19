// Package api 提供服务的 HTTP 接口（设置读写、连通测试、静态页）。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
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
	mu          sync.RWMutex
	store       *config.Store
	cfg         *config.Settings
	indexHTML   []byte
	renderMgr   *render.Manager
	sysmon      *sysmon.Collector
	kbStore     *kb_work.Store
	kbGraphMu   sync.RWMutex
	kbGraphJSON []byte
	kbGraphGen  time.Time
	kbRoot      string
	kbFS        fs.FS
	islandFS    fs.FS
	outDir      string
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
	mux.HandleFunc("GET /api/meta", s.handleKBMeta)
	mux.HandleFunc("GET /api/graph", s.handleKBGraph)
	mux.HandleFunc("GET /api/page", s.handleKBPage)
	mux.HandleFunc("GET /api/asset", s.handleKBAsset)
	mux.HandleFunc("POST /api/reload", s.handleKBReload)
	mux.HandleFunc("GET /api/comfy", s.handleComfy)
	mux.HandleFunc("POST /api/comfy/start", s.handleComfyStart)
	mux.HandleFunc("POST /api/comfy/stop", s.handleComfyStop)
	mux.HandleFunc("POST /api/comfy/install", comfyInstallStart)
	mux.HandleFunc("GET /api/comfy/install/status", comfyInstallStatus)
	mux.HandleFunc("POST /api/comfy/install/stop", comfyInstallStop)
	mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	mux.HandleFunc("GET /api/fs/analyze", s.handleFSAnalyze)
	mux.HandleFunc("GET /api/fs/select", s.handleFSSelect)
	mux.HandleFunc("GET /api/fs/read", s.handleFSRead)
	mux.HandleFunc("GET /api/fs/file", s.handleFSFile)
	mux.HandleFunc("GET /api/fs/media", s.handleFSMedia)
	mux.HandleFunc("POST /api/script/generate", s.handleGenerateScript)
	// 网页版爽文小说创作(shuangwen-novel 技能流程固化)
	mux.HandleFunc("POST /api/novel/create", s.handleNovelCreate)
	mux.HandleFunc("POST /api/novel/analyze", s.handleNovelAnalyze)
	mux.HandleFunc("POST /api/novel/review", s.handleNovelReview)
	mux.HandleFunc("POST /api/novel/chapter", s.handleNovelChapter)
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
	if s.islandFS != nil {
		mux.Handle("/island/", noCacheHTML(http.StripPrefix("/island/", http.FileServer(http.FS(s.islandFS)))))
	}
	mux.HandleFunc("/manage/", s.handleManage)
	if s.kbFS != nil {
		// 根路径(kb 工作台页)经 FileServer 直接输出静态文件,不会走 handleManage 的令牌替换——
		// 必须包 tokenInject 把会话令牌占位符替换进 index.html,否则前端 NILIX_TOKEN 是字面占位符,
		// 所有写请求带错误 token 被 auth 拦成 401「会话失效」
		mux.Handle("/", noCacheHTML(s.tokenInject(http.FileServer(http.FS(s.kbFS)))))
	}
	return s.auth(mux)
}

// tokenInject 对 HTML 响应替换会话令牌占位符 /*__NILIX_TOKEN__*/ → 真实 token。
// 仅 index.html(路径 / 或 *.html)走缓冲替换;媒体/脚本直接透传(不整文件缓冲)。
func (s *Server) tokenInject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isHTML := r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, "/index.html")
		if sessionToken == "" || !isHTML {
			next.ServeHTTP(w, r)
			return
		}
		rr := httptest.NewRecorder()
		next.ServeHTTP(rr, r)
		body := rr.Body.Bytes()
		if strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
			body = bytes.Replace(body, []byte("/*__NILIX_TOKEN__*/"), []byte(sessionToken), 1)
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
	_, _ = w.Write(out)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	view := *s.cfg
	set := view.LLM.APIKey != ""
	view.LLM.APIKey = maskKey(view.LLM.APIKey)
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
