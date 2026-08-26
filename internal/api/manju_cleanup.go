package api

// 产物清理工具:一键清理 _gacha(抽卡候选)/ _frames(审片抽帧)/ 2k(云端 2K 产物)。
// 安全护栏:只清固定子目录,绝不触碰定妆照/场景图/镜头定稿/成片。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	// 运行中禁止清理(审计 S7:与渲染并发撕扯产物)
	if manjuStateRunningFor(cp) {
		writeErr(w, http.StatusConflict, "该项目正在渲染中,请先停止再清理")
		return
	}
	ctx, err := newManjuCtx(cp, "", "", "", "")
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

// removeTree 递归删除目录内容,返回 (成功文件数, 释放字节);目录本身保留。
// 2026-08-26:删除失败(ComfyUI/播放器占用句柄)不再静默——失败清单经 removeTreeEx 透出,
// 前端明示「X 个文件被占用」,否则用户以为清干净了实际残留(「扫帚清不干净」根因)。
func removeTree(dir string) (int, int64) {
	n, _, _ := removeTreeEx(dir, nil)
	return n, int64(0)
}

// removeTreeEx 带失败清单版本:failed 收集删除失败的文件名(上限 8 条,防刷屏)。
func removeTreeEx(dir string, failed *[]string) (int, int64, int) {
	files, bytes, fails := 0, int64(0), 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, 0
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			f, b, fl := removeTreeEx(full, failed)
			files += f
			bytes += b
			fails += fl
			_ = os.Remove(full) // 空子目录
			continue
		}
		if fi, err := e.Info(); err == nil {
			bytes += fi.Size()
		}
		if os.Remove(full) == nil {
			files++
		} else {
			fails++
			if failed != nil && len(*failed) < 8 {
				*failed = append(*failed, filepath.Base(full))
			}
		}
	}
	return files, bytes, fails
}

// manjuCacheClear 一键清理项目旧缓存(2026-08-25 用户要求:NiliX 导航栏右侧「扫帚」按钮):
// 清 analysis 方案缓存(<ep>_direct_plan.json/<ep>_characters.json/<ep>_shots_prompts.json
// /manifest/render_ck/指纹) + 镜头 mp4(clips/<ep>/) + 条件缓存(conditioning/<项目>_*) +
// 接缝 latent(comfyOutput/h3_context/<ns>/)。
// 用途:分镜脚本修复后旧 plan 里的错误时长/缺失台词会让修复不生效;新项目也常被旧项目缓存
// (项目前缀不匹配的一般不会,但条件缓存/接缝 latent 复用旧方案)影响——一键清空让渲染按新方案重做。
// 安全护栏:只清上述缓存,绝不触碰 定妆照(characters/*.png)/场景图(scenes)/成片(_成片.mp4)/2k 产物。
func manjuCacheClear(w http.ResponseWriter, r *http.Request) {
	// 兼容两种传参:Query ?config=... 或 JSON body {config: ...}(前端 post 用 body)
	advanced := false
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	if configPath == "" {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath = strings.TrimSpace(str(body["config"]))
		if b, ok := body["advanced"].(bool); ok {
			advanced = b
		}
	}
	if q := r.URL.Query().Get("advanced"); q == "true" || q == "1" {
		advanced = true
	}
	if configPath == "" {
		writeErr(w, http.StatusBadRequest, "missing config")
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	// 运行中禁止清理(与渲染并发撕扯产物)
	if manjuStateRunningFor(cp) {
		writeErr(w, http.StatusConflict, "该项目正在渲染中,请先停止再清理")
		return
	}
	ctx, err := newManjuCtx(cp, "", "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 1) analysis 方案缓存:本集 + 全部集(清理按钮=全项目缓存清空)
	clean := []map[string]any{}
	// 方案 JSON 是单个文件不是目录:按 analysis 目录里的方案类文件删(保留 _frames/_gacha 子目录产物)
	if files, bytes := removePlanJSONs(ctx.analysisDir); files > 0 {
		clean = append(clean, map[string]any{"target": "plan", "files": files, "bytes": bytes, "dir": ctx.analysisDir})
	}
	// 2) 镜头 mp4:clips/<ep>/ 全清(清理按钮=全部集)
	var failed []string
	totalFails := 0
	if files, bytes, fl := removeTreeEx(ctx.clipsDir, &failed); files > 0 || fl > 0 {
		totalFails += fl
		clean = append(clean, map[string]any{"target": "clips", "files": files, "bytes": bytes, "dir": ctx.clipsDir})
	}
	// 2b) 运行状态残留(2026-08-26 用户反馈「清不干净」):非运行态下 run_state.json 是
	// 崩溃/中断残留——不清会让页面继续弹「检测到已有任务」横幅,像没清干净;agent_state 同理
	for _, state := range []string{"run_state.json", "agent_state.json"} {
		sp := filepath.Join(ManjuRootDir, ctx.project, state)
		if _, err := os.Stat(sp); err == nil && os.Remove(sp) == nil {
			clean = append(clean, map[string]any{"target": "state", "files": 1, "bytes": 0, "dir": sp})
		}
	}
	// 3) 条件缓存:conditioning/<项目>_* 前缀(与 clearEpisodeArtifacts 同口径,不误删其他项目)
	prefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_"
	condDir := filepath.Join(ctx.sharedModels, "conditioning")
	if entries, err := os.ReadDir(condDir); err == nil {
		n, b := 0, int64(0)
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			full := filepath.Join(condDir, e.Name())
			if fi, _ := e.Info(); fi != nil {
				b += fi.Size()
			}
			if os.Remove(full) == nil {
				n++
			}
		}
		if n > 0 {
			clean = append(clean, map[string]any{"target": "conditioning", "files": n, "bytes": b, "dir": condDir})
		}
	}
	// 4) 接缝 latent:comfyOutput/h3_context/<项目>_* 前缀子目录(审计 S6 命名空间:
	// latent 目录名 = <项目>_<集>,只清当前项目,不误删其他项目的 latent——h3_context 是
	// ComfyUI 共享 output 下的全局目录,全清会波及别的项目)
	// 同时清 config 的 comfy_output 与运行时 comfyParams 两处(两者可能不同但都是合法输出目录)
	latentPrefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_"
	for _, outDir := range []string{str(ctx.P["comfy_output"]), ctx.comfyOutput} {
		if outDir == "" {
			continue
		}
		latentRoot := filepath.Join(outDir, "h3_context")
		if entries, err := os.ReadDir(latentRoot); err == nil {
			n, b := 0, int64(0)
			for _, e := range entries {
				if !e.IsDir() || !strings.HasPrefix(e.Name(), latentPrefix) {
					continue
				}
				full := filepath.Join(latentRoot, e.Name())
				f, bb := removeTree(full)
				n += f
				b += bb
				_ = os.Remove(full)
			}
			if n > 0 {
				clean = append(clean, map[string]any{"target": "latent", "files": n, "bytes": b, "dir": latentRoot})
			}
		}
	}
	// 5) 高级清理(2026-08-25 用户要求:「高级」按钮):额外清空 ComfyUI 共享 input/output 目录
	// 里的全部产物(含其他项目的图片/视频/中间产物)。彻底清场用,慎用。
	// 安全护栏:只清 ComfyUI 共享目录(input/output)内的产物,目录本身保留;
	// 模型权重(../models)绝不触碰。目录来自 ComfySharedDir(与 comfyParams 同源)。
	if advanced {
		for _, sub := range []string{"input", "output"} {
			dir := filepath.Join(ComfySharedDir, sub)
			if files, bytes, fl := removeTreeEx(dir, &failed); files > 0 || fl > 0 {
				totalFails += fl
				clean = append(clean, map[string]any{"target": "comfy_"+sub, "files": files, "bytes": bytes, "dir": dir})
			}
		}
		// 6) 运行日志(2026-08-26 用户要求:高级清理同时清运行日志数据):
		// 内存日志(清空日志按钮同款)+ 全部项目 run.log 落盘截断(manju/logs 平台日志保留)
		manjuState.mu.Lock()
		manjuState.log = ""
		manjuState.mu.Unlock()
		n, b := int64(0), int64(0)
		if entries, err := os.ReadDir(ManjuRootDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				p := filepath.Join(ManjuRootDir, e.Name(), "run.log")
				if fi, err := os.Stat(p); err == nil {
					b += fi.Size()
					if os.Truncate(p, 0) == nil {
						n++
					}
				}
			}
		}
		if n > 0 {
			clean = append(clean, map[string]any{"target": "runlog", "files": n, "bytes": b, "dir": filepath.Join(ManjuRootDir, "<项目>/run.log")})
		}
	}
	resp := map[string]any{"ok": true, "advanced": advanced, "cleaned": clean}
	if totalFails > 0 {
		resp["failed"] = totalFails
		resp["failedFiles"] = failed
	}
	writeJSON(w, http.StatusOK, resp)
}

// removePlanJSONs 删除 analysis 目录里的方案类 JSON 缓存文件
// (<ep>_direct_plan.json / <ep>_characters.json / <ep>_shots_prompts.json /
// <ep>_manifest.json / <ep>_render_ck.json / fingerprints.jsonl),
// 保留 _frames/_gacha 等子目录与目录本身。返回 (文件数, 字节)。
func removePlanJSONs(dir string) (int, int64) {
	files, bytes := 0, int64(0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !(strings.HasSuffix(name, "_direct_plan.json") ||
			strings.HasSuffix(name, "_characters.json") ||
			strings.HasSuffix(name, "_shots_prompts.json") ||
			strings.HasSuffix(name, "_manifest.json") ||
			strings.HasSuffix(name, "_render_ck.json") ||
			name == "fingerprints.jsonl") {
			continue
		}
		full := filepath.Join(dir, name)
		if fi, _ := e.Info(); fi != nil {
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
	// 2026-08-25 用户要求:导航栏右侧「扫帚」按钮 → 一键清理旧缓存
	mux.HandleFunc("POST /api/manju/cache/clear", manjuCacheClear)
	// 清理目标目录占用(前端展示用,可选)
	mux.HandleFunc("GET /api/manju/cleanup/sizes", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		if configPath == "" {
			writeErr(w, http.StatusBadRequest, "missing config")
			return
		}
		// 审计 F4:GET 免 token 且 newManjuCtx 会扩 fs 白名单,config 必须过 guard
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		ctx, err := newManjuCtx(cp, "", "", "", "")
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
