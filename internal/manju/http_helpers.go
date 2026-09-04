package manju

import (
	"nilix/internal/comfy"
	"net/http"

	"nilix/internal/paths"
	"nilix/internal/util"
)

// writeJSON/writeErr/maskKey 包装(2026-09-03 拆包:唯一定义在 util,包内别名零改动)
func writeJSON(w http.ResponseWriter, code int, v any) { util.WriteJSON(w, code, v) }

func writeErr(w http.ResponseWriter, code int, msg string) { util.WriteErr(w, code, msg) }

func maskKey(k string) string { return util.MaskKey(k) }

// 文件名/章节正则别名(util 唯一定义)
var novelTitleSan = util.NovelTitleSan

// comfy 域符号包内别名(2026-09-03 拆包,包内调用点零改动)
func startComfy() error                  { return comfy.Start() }
func manjuPythonPath() string           { return util.ManjuPythonPath(paths.ComfyRootDir) }
func isPidAlive(pid int) bool           { return util.IsPidAlive(pid) }
func ComfyOnline() bool                  { return comfy.ComfyOnline() }
func comfyVersionSnapshot() map[string]any { return comfy.VersionSnapshot() }
func comfyLatestVerCached() string       { return comfy.LatestVerCached() }

// comfyClient 拆包别名(类型与方法转发)
type comfyClient = comfy.Client

func newComfyClient(base string) *comfyClient { return comfy.NewClient(base) }
func copyTree(src, dst string) error { return util.CopyTree(src, dst) }
func comfyErrMsg(st map[string]any) string { return comfy.ComfyErrMsg(st) }
func comfyParamsSnapshot() (url, in, out string) { return comfy.ComfyURL(), comfy.In(), comfy.Out() }
