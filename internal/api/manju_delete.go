package api

// 删除项目:删除当前项目整个完整目录(manjuRoot/<项目>/,含 config/方案/镜头/资产/agent_state 等)。
// 安全护栏:项目名必须解析自 configPath(不接受任意路径),目标必须位于 manjuRoot 之下。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func manjuDeleteProject(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	configPath := str(body["config"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	proj := filepath.Base(filepath.Dir(configPath))
	if proj == "" || proj == "." || proj == ".." || manjuSkipDirs[proj] || strings.ContainsAny(proj, `\/`) {
		http.Error(w, `{"error":"非法项目名"}`, http.StatusBadRequest)
		return
	}
	dir := filepath.Join(manjuRoot, proj)
	rootClean := filepath.Clean(manjuRoot)
	if filepath.Clean(dir) == rootClean || !strings.HasPrefix(filepath.Clean(dir), rootClean+string(filepath.Separator)) {
		http.Error(w, `{"error":"目标不在项目根目录内,拒绝删除"}`, http.StatusBadRequest)
		return
	}
	if !fileExists(filepath.Join(dir, "config.json")) {
		http.Error(w, `{"error":"项目目录不存在或缺少 config.json"}`, http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, `{"error":"删除失败: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": dir})
}
