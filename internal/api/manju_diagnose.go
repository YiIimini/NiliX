package api

// 一键诊断导出:打包项目运行日志/状态文件/配置(Key 打码)/环境自检为 zip,
// 用户反馈问题时直接贴包,免去手动翻多个 JSON。

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// manjuDiagnoseZip 收集项目诊断信息打包 zip,直接写响应流
func manjuDiagnoseZip(w http.ResponseWriter, configPath string) {
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	proj := ctx.project
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-diagnose.zip"`, proj))
	zw := zip.NewWriter(w)
	defer zw.Close()

	addFile := func(name, path string, maxSize int64) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if maxSize > 0 && int64(len(b)) > maxSize {
			b = b[len(b)-int(maxSize):]
		}
		f, _ := zw.Create(name)
		_, _ = f.Write(b)
	}
	// 配置 Key 打码后进包
	if b, err := os.ReadFile(configPath); err == nil {
		var cfg map[string]any
		if json.Unmarshal(b, &cfg) == nil {
			maskKeys(cfg)
			if bb, err := json.MarshalIndent(cfg, "", "  "); err == nil {
				f, _ := zw.Create("config.json")
				_, _ = f.Write(bb)
			}
		}
	}
	// 运行/审片/记账/清单/检查点/日志
	addFile("run_state.json", manjuRunStatePath(proj), 0)
	addFile("agent_state.json", manjuAgentStatePath(proj), 2<<20)
	addFile("llm_stats.json", manjuStatsPath(proj), 0)
	addFile("manifest.json", ctx.manifestPath(), 2<<20)
	addFile("render_ck.json", ctx.renderCKPath(), 0)
	addFile("run.log", manjuRunLogPath(proj), 512<<10)
	// 环境自检 + 版本说明
	env := manjuEnvCheck(configPath)
	f, _ := zw.Create("env_check.txt")
	_, _ = f.Write([]byte(env))
	ver := "NiliX diagnose 2026-08-19 (审计批次:安全/性能/体验/工程化)\n"
	f2, _ := zw.Create("README.txt")
	_, _ = f2.Write([]byte(ver + "包含:config(Key 打码)/run_state/agent_state/llm_stats/manifest/render_ck/run.log/env_check\n"))
}

// maskKeys 递归把 api_key / vision_api_key / minimax_api_key 打码
func maskKeys(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if strings.Contains(strings.ToLower(k), "api_key") || strings.Contains(strings.ToLower(k), "apikey") {
				if s, ok := val.(string); ok && s != "" {
					x[k] = maskKey(s)
				}
			} else {
				maskKeys(val)
			}
		}
	case []any:
		for _, item := range x {
			maskKeys(item)
		}
	}
}

// registerDiagnoseRoute 诊断导出路由
func registerDiagnoseRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/manju/diagnose", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		if configPath == "" {
			writeErr(w, http.StatusBadRequest, "missing config")
			return
		}
		manjuDiagnoseZip(w, configPath)
	})
}
