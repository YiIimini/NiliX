package api

// 灵动岛配置接口:系统设置弹窗「灵动岛」开关 ↔ settings.json island.enabled。
// 灵动岛 WebView 轮询此接口决定自身显示/隐藏(禁用后隐藏悬浮胶囊)。

import (
	"encoding/json"
	"net/http"
)

// islandEnabled 读取灵动岛启用状态(默认启用)
func islandEnabled() bool {
	if manjuSettingsStore != nil {
		if cfg, err := manjuSettingsStore.Load(); err == nil {
			if cfg.Island.Enabled != nil {
				return *cfg.Island.Enabled
			}
		}
	}
	return true
}

func (s *Server) handleIslandGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": islandEnabled()})
}

func (s *Server) handleIslandPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "缺少 enabled")
		return
	}
	if manjuSettingsStore == nil {
		writeErr(w, http.StatusInternalServerError, "设置存储未就绪")
		return
	}
	cfg, err := manjuSettingsStore.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取设置失败: "+err.Error())
		return
	}
	cfg.Island.Enabled = body.Enabled
	if err := manjuSettingsStore.Save(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
}
