package api

// 诊断快照自动落盘:任务结束时把项目诊断信息(config Key 打码 + 各状态文件 + 环境自检)
// 统一汇总写到固定目录 manju/logs/diagnose/<项目>_diagnose.json——本地服务无需导出 zip,
// 反馈问题时直接提供该文件即可。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// manjuDiagnoseDir 诊断快照固定目录(与 notify.json 同级)
func manjuDiagnoseDir() string {
	return filepath.Join(ManjuRootDir, "logs", "diagnose")
}

// manjuDiagnosePath 某项目诊断快照路径
func manjuDiagnosePath(project string) string {
	return filepath.Join(manjuDiagnoseDir(), project+"_diagnose.json")
}

// diagnoseSnapshot 收集项目诊断信息为单个 JSON(全部 Key 打码)
func (ctx *manjuCtx) diagnoseSnapshot() map[string]any {
	snap := map[string]any{
		"project":     ctx.project,
		"episode":     ctx.episode,
		"generatedAt": time.Now().Format("2006-01-02 15:04:05"),
	}
	// config(Key 打码)
	if b, err := os.ReadFile(ctx.configPath); err == nil {
		var cfg map[string]any
		if json.Unmarshal(b, &cfg) == nil {
			maskKeys(cfg)
			snap["config"] = cfg
		}
	}
	// 各状态文件(原样并入,若损坏则跳过)
	load := func(path string) any {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var v any
		if json.Unmarshal(b, &v) != nil {
			return string(b) // 非 JSON(如损坏/文本):原样字符串
		}
		return v
	}
	snap["runState"] = load(manjuRunStatePath(ctx.project))
	snap["agentState"] = load(manjuAgentStatePath(ctx.project))
	snap["llmStats"] = load(manjuStatsPath(ctx.project))
	snap["manifest"] = load(ctx.manifestPath())
	snap["renderCk"] = load(ctx.renderCKPath())
	// 环境自检文本
	snap["envCheck"] = manjuEnvCheck(ctx.configPath)
	return snap
}

// manjuWriteDiagnoseSnapshot 任务结束时自动写快照(固定目录,覆盖旧快照保留最近一次)
func manjuWriteDiagnoseSnapshot(project, episode string) {
	if project == "" {
		return
	}
	cfgPath := filepath.Join(ManjuRootDir, project, "config.json")
	ctx, err := newManjuCtx(cfgPath, episode, "", "", "")
	if err != nil {
		return
	}
	_ = atomicWriteJSON(manjuDiagnosePath(project), ctx.diagnoseSnapshot())
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
