package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"nilix/internal/storyboard"
)

type renderRequest struct {
	Script storyboard.Script `json:"script"`
	Seed   int               `json:"seed"`
}

// handleRender 提交一部脚本的逐镜渲染任务（异步）。
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	var req renderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if len(req.Script.Shots) == 0 {
		writeErr(w, http.StatusBadRequest, "脚本没有镜头")
		return
	}

	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()

	job := s.renderMgr.Submit(&req.Script, req.Seed, cfg, s.outDir)
	writeJSON(w, http.StatusOK, job)
}

// handleListJobs 列出所有渲染任务。
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.renderMgr.List())
}

// handleJobStatus 查询单个任务状态。
func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.renderMgr.Status(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleClipFile 提供渲染产物（镜头/成片）的下载与预览。
func (s *Server) handleClipFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		http.Error(w, "非法路径", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.outDir, name))
}

// handleOutputs 列出成品列表（clips 目录下的 final_*.mp4 成片）。
func (s *Server) handleOutputs(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.outDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	type output struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Size  int64  `json:"size"`
		Mtime string `json:"mtime"`
	}
	outs := make([]output, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "final_") {
			continue
		}
		info, _ := e.Info()
		sz, mt := int64(0), ""
		if info != nil {
			sz = info.Size()
			mt = info.ModTime().Format("2006-01-02 15:04")
		}
		outs = append(outs, output{Name: e.Name(), URL: "/clips/" + e.Name(), Size: sz, Mtime: mt})
	}
	writeJSON(w, http.StatusOK, outs)
}
