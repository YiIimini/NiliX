package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"nilix/internal/kb_work"
)

// handleKBMeta 知识库元信息。
func (s *Server) handleKBMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.kbStore.Meta())
}

// handleKBGraph 知识图谱(预序列化字节缓存:同一代扫描直接回放,~0ms;
// /api/reload 触发新扫描后按 LoadedAt 指纹自动重建——等价 git 版本键,且免去每次跑 git status 的开销)。
func (s *Server) handleKBGraph(w http.ResponseWriter, r *http.Request) {
	gen := s.kbStore.LoadedAt()
	s.kbGraphMu.RLock()
	hit := s.kbGraphGen.Equal(gen) && len(s.kbGraphJSON) > 0
	b := s.kbGraphJSON
	s.kbGraphMu.RUnlock()
	if !hit {
		var err error
		b, err = json.Marshal(kb_work.BuildGraph(s.kbStore))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "graph marshal: "+err.Error())
			return
		}
		s.kbGraphMu.Lock()
		s.kbGraphJSON, s.kbGraphGen = b, gen
		s.kbGraphMu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

// handleKBPage 单个知识页。
func (s *Server) handleKBPage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	p, ok := s.kbStore.Page(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "page not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleKBAsset 知识库图片资源（详情页 Markdown 相对路径 assets/* 经此路由加载）。
func (s *Server) handleKBAsset(w http.ResponseWriter, r *http.Request) {
	cat := strings.TrimSpace(r.URL.Query().Get("cat"))
	p := r.URL.Query().Get("p")
	if cat == "" || p == "" {
		writeErr(w, http.StatusBadRequest, "missing cat or p")
		return
	}
	// cat 必须是知识库根目录下的单段子目录名:拒绝 .. / 空 / 含路径分隔符,
	// 否则 filepath.Base("..") 会把 base 抬到 kbRoot 的父目录,越权读任意文件
	if cat == "." || cat == ".." || strings.Contains(cat, "..") || strings.ContainsAny(cat, `/\`) {
		writeErr(w, http.StatusBadRequest, "invalid cat")
		return
	}
	clean := filepath.Clean("/" + p)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") || strings.Contains(clean, "..") {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	base := filepath.Join(s.kbRoot, cat)
	fp := filepath.Join(base, clean)
	// 双重护栏:产物必须落在 <kbRoot>/<cat>/ 之下(防 cat 与 p 组合逃逸)
	rootClean := filepath.Clean(s.kbRoot)
	if !strings.HasPrefix(filepath.Clean(fp), rootClean+string(filepath.Separator)) {
		writeErr(w, http.StatusBadRequest, "path outside kb")
		return
	}
	http.ServeFile(w, r, fp)
}

// handleKBReload 重新扫描知识库。
func (s *Server) handleKBReload(w http.ResponseWriter, r *http.Request) {
	if err := s.kbStore.Reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pages": s.kbStore.Meta().PageCount})
}
