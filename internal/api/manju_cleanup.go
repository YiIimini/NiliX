package api

// 产物清理工具:一键清理 _gacha(抽卡候选)/ _frames(审片抽帧)/ 2k(云端 2K 产物)。
// 安全护栏:只清固定子目录,绝不触碰定妆照/场景图/镜头定稿/成片。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// manjuCleanupRun 清理指定产物类别,返回每类删除文件数与释放字节
func manjuCleanupRun(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	if configPath == "" {
		writeErr(w, http.StatusBadRequest, "missing config")
		return
	}
	targets, _ := body["targets"].([]any)
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "missing targets(gacha/frames/2k)")
		return
	}
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cleaned := []map[string]any{}
	for _, t := range targets {
		key := str(t)
		var dir string
		switch key {
		case "gacha":
			dir = filepath.Join(ctx.assetsDir, "characters", "_gacha")
		case "frames":
			dir = filepath.Join(ctx.analysisDir, "_frames")
		case "2k":
			dir = filepath.Join(ctx.clipsDir, ctx.episode, "2k")
		default:
			writeErr(w, http.StatusBadRequest, "未知清理目标: "+key)
			return
		}
		files, bytes := removeTree(dir)
		if files > 0 {
			cleaned = append(cleaned, map[string]any{"target": key, "files": files, "bytes": bytes, "dir": dir})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleaned": cleaned})
}

// removeTree 递归删除目录内容,返回 (文件数, 释放字节);目录本身保留
func removeTree(dir string) (int, int64) {
	files, bytes := 0, int64(0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			f, b := removeTree(full)
			files += f
			bytes += b
			_ = os.Remove(full) // 空子目录
			continue
		}
		if fi, err := e.Info(); err == nil {
			bytes += fi.Size()
		}
		if os.Remove(full) == nil {
			files++
		}
	}
	return files, bytes
}

// registerCleanupRoute 产物清理路由
func registerCleanupRoute(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/manju/cleanup", manjuCleanupRun)
	// 清理目标目录占用(前端展示用,可选)
	mux.HandleFunc("GET /api/manju/cleanup/sizes", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		if configPath == "" {
			writeErr(w, http.StatusBadRequest, "missing config")
			return
		}
		ctx, err := newManjuCtx(configPath, "", "", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out := map[string]any{}
		for _, t := range []struct{ key, dir string }{
			{"gacha", filepath.Join(ctx.assetsDir, "characters", "_gacha")},
			{"frames", filepath.Join(ctx.analysisDir, "_frames")},
			{"2k", filepath.Join(ctx.clipsDir, ctx.episode, "2k")},
		} {
			out[t.key] = dirSize(t.dir)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func dirSize(dir string) map[string]any {
	files, bytes := 0, int64(0)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			bytes += fi.Size()
			files++
		}
		return nil
	})
	return map[string]any{"files": files, "bytes": bytes}
}
