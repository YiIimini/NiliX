package api

import (
	"encoding/json"
	"net/http"
	"time"

	"nilix/internal/backend"
	"nilix/internal/storyboard"
)

type generateScriptRequest struct {
	Novel string `json:"novel"`
	Style string `json:"style"`
}

// handleGenerateScript 输入小说，生成一集视频脚本。
func (s *Server) handleGenerateScript(w http.ResponseWriter, r *http.Request) {
	var req generateScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}

	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()

	if cfg.LLM.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "未配置 LLM API key，请先在设置页配置")
		return
	}

	llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.RequestTimeout)*time.Second)

	script, err := storyboard.Generate(r.Context(), llm, req.Novel, req.Style)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, script)
}

// handleScriptStyles 返回可选风格列表。
func (s *Server) handleScriptStyles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"styles": storyboard.Styles})
}
