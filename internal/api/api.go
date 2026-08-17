// Package api 提供服务的 HTTP 接口（设置读写、连通测试、静态页）。
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
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
	kbGraphMu     sync.RWMutex
	kbGraphJSON   []byte
	kbGraphGen    time.Time
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
	mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	mux.HandleFunc("GET /api/fs/analyze", s.handleFSAnalyze)
	mux.HandleFunc("GET /api/fs/select", s.handleFSSelect)
	mux.HandleFunc("GET /api/fs/read", s.handleFSRead)
	mux.HandleFunc("GET /api/fs/file", s.handleFSFile)
	mux.HandleFunc("GET /api/fs/media", s.handleFSMedia)
	mux.HandleFunc("POST /api/script/generate", s.handleGenerateScript)
	mux.HandleFunc("GET /api/script/styles", s.handleScriptStyles)
	mux.HandleFunc("POST /api/render", s.handleRender)
	mux.HandleFunc("GET /api/render/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/render/jobs/{id}", s.handleJobStatus)
	mux.HandleFunc("GET /api/outputs", s.handleOutputs)
	mux.HandleFunc("GET /clips/{file}", s.handleClipFile)
	registerManjuRoutes(mux)
	if s.islandFS != nil {
		mux.Handle("/island/", http.StripPrefix("/island/", http.FileServer(http.FS(s.islandFS))))
	}
	mux.HandleFunc("/manage/", s.handleManage)
	if s.kbFS != nil {
		mux.Handle("/", noCacheHTML(http.FileServer(http.FS(s.kbFS))))
	}
	return mux
}

// noCacheHTML 给 HTML 入口加 no-cache:HTML 内的静态资源带 ?v= 版本号,
// HTML 本身必须每次回源校验,否则浏览器缓存旧 HTML → 整站旧资源,出现"修了但看不到"。
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || strings.HasSuffix(p, ".html") {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
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
	_, _ = w.Write(s.indexHTML)
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
