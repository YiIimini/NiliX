package api

import (
	"net/http"
)

// handleStats 返回系统监测快照（CPU/内存/GPU/磁盘/网络）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.sysmon.Snapshot())
}
