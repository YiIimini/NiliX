package manju

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"nilix/internal/paths"
)

func manjuSkillUpdate(w http.ResponseWriter, r *http.Request) {
	if !dirExists(filepath.Join(paths.NovelSkillDir, ".git")) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "技能目录不是 git 仓库(先按 github 仓库克隆到目录)"})
		return
	}
	cmd := exec.Command("git", "-C", paths.NovelSkillDir, "pull", "--ff-only")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "git pull 失败: " + err.Error(), "output": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": msg})
}
