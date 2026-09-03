package api

// 场景管理 API(2026-09-03 视频管理封面 ⋮ 菜单 → 场景管理弹窗):
//   GET /api/manju/scenes?config=&episode= → 场景卡清单(名称/描述/提示词/主图/尾帧状态)
// 数据源:该集 plan(_direct_plan.json 优先,兼容 _characters.json)的 scenes 卡 +
// assets/scenes/<场景>.png 与 _end.png 磁盘状态;episode 空=自动取第一个有方案的集。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func manjuScenesHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	episode := strings.TrimSpace(r.URL.Query().Get("episode"))
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	analysis := str(P["analysis"])
	if episode == "" {
		for _, e := range listManjuEpisodes(P) {
			if p := manjuFindPlanDir(analysis, e); p != "" {
				episode = e
				break
			}
		}
	}
	planPath := manjuFindPlanDir(analysis, episode)
	if planPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "episode": episode, "scenes": []any{}})
		return
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var plan map[string]any
	if err := json.Unmarshal(data, &plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "方案解析失败"})
		return
	}
	ctx, cerr := newManjuCtx(configPath, episode, "", "", "")
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": cerr.Error()})
		return
	}
	// 场景引用计数:哪些场景在本集分镜里实际登场
	refCount := map[string]int{}
	if arr, ok := plan["shots"].([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				refCount[str(m["scene"])]++
			}
		}
	}
	scenes := []map[string]any{}
	if arr, ok := plan["scenes"].([]any); ok {
		for _, x := range arr {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			id := str(m["id"])
			if id == "" {
				continue
			}
			safe := sanitizeFileName(id)
			img := filepath.Join(ctx.assetsDir, "scenes", safe+".png")
			end := filepath.Join(ctx.assetsDir, "scenes", safe+"_end.png")
			item := map[string]any{
				"id":           id,
				"description":  str(m["description"]),
				"image_prompt": str(m["image_prompt"]),
				"has_image":    fileExists(img),
				"has_end":      fileExists(end),
				"shots":        refCount[id], // 本集登场镜数(0=已出卡未用)
			}
			if item["has_image"].(bool) {
				item["image"] = filepath.ToSlash(img) // 前端 /api/fs/file 预览用
			}
			if item["has_end"].(bool) {
				item["end_image"] = filepath.ToSlash(end)
			}
			scenes = append(scenes, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "episode": episode, "scenes": scenes})
}
