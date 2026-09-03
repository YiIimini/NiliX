package comfy

import (
	"net/http"

	"nilix/internal/paths"
	"nilix/internal/util"
)

// writeJSON/writeErr/maskKey 包装(2026-09-03 拆包:唯一定义在 util,包内别名零改动)
func writeJSON(w http.ResponseWriter, code int, v any) { util.WriteJSON(w, code, v) }

func writeErr(w http.ResponseWriter, code int, msg string) { util.WriteErr(w, code, msg) }

func maskKey(k string) string { return util.MaskKey(k) }
func dirExists(p string) bool { return util.DirExists(p) }
func fileExists(p string) bool { return util.FileExists(p) }
func truncate(s string, n int) string { return util.Truncate(s, n) }
func manjuPythonPath() string { return util.ManjuPythonPath(paths.ComfyRootDir) }
func copyTree(src, dst string) error { return util.CopyTree(src, dst) }
func isPidAlive(pid int) bool { return util.IsPidAlive(pid) }
func newComfyClient(base string) *Client { return NewClient(base) }
func str(v any) string                   { return util.Str(v) }
