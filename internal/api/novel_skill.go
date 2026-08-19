package api

// shuangwen-novel 技能资产接入:词库/写作规范按文件加载注入(数据驱动,增删词库文件即生效),
// 单章机械 QA 与技能 qa_check.py 同口径;文件缺失时静默降级(不阻塞创作)。

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// NovelSkillDir 技能目录(违规词库/负面提示词库/章节写作规范/qa 脚本);见 paths.go

// novelHardBanned 违规词库硬禁词(## [硬禁] 分类下词条;增删词库即生效)
func novelHardBanned() []string {
	b, err := os.ReadFile(filepath.Join(NovelSkillDir, "wordbank", "违规词库.md"))
	if err != nil {
		return nil
	}
	var words []string
	hard := false
	for _, ln := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## ") {
			hard = strings.Contains(t, "[硬禁]")
			continue
		}
		if hard && strings.HasPrefix(t, "- ") {
			if w := strings.TrimSpace(t[2:]); w != "" {
				words = append(words, w)
			}
		}
	}
	return words
}

// novelBannedAI 章节写作规范的去AI味禁用词(与技能 qa_check.py BANNED 同源)
var novelBannedAI = []string{"此外", "与此同时", "众所周知", "值得一提的是", "毫无疑问", "综上所述",
	"总的来说", "让我们", "值得注意的是", "至关重要", "彰显", "诠释", "凸显", "交织", "蜕变",
	"绽放", "谱写", "深邃", "无疑", "不禁"}

// novelWritingSpec 章节写作规范.md 全文(注入写章 system,等价技能"代理动笔前读规范")
func novelWritingSpec() string {
	b, err := os.ReadFile(filepath.Join(NovelSkillDir, "references", "章节写作规范.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

// novelChineseCount 中文字数(中文+中文标点;技能口径——len 会把英文/数字算入导致虚高)
func novelChineseCount(s string) int {
	n := 0
	for _, r := range s {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			n++
		}
	}
	return n
}

// novelChapterQA 单章机械校验:中文字数/去AI味禁用词/违规词库硬禁词/正文加粗。
// 返回问题清单(空=通过);写章后即时把关,与 LLM 审稿主观关互补(技能阶段4 的在线版)。
func novelChapterQA(content string) []string {
	var problems []string
	if n := novelChineseCount(content); n < 1280 {
		problems = append(problems, fmt.Sprintf("中文字数 %d 不足 1280(需补写至 1400-1550)", n))
	}
	for _, w := range novelBannedAI {
		if strings.Contains(content, w) {
			problems = append(problems, "去AI味禁用词:"+w)
		}
	}
	for _, w := range novelHardBanned() {
		if strings.Contains(content, w) {
			problems = append(problems, "违规词(硬禁):"+w)
		}
	}
	if strings.Contains(content, "**") {
		problems = append(problems, "正文含加粗(禁止 markdown 加粗)")
	}
	return problems
}

// runNovelQACheck 技能阶段4 全量 QA:qa_check.py 校验文件数/每章字数/禁用词/违规词/加粗。
// 纯标准库脚本(venv python 可跑);脚本或技能目录缺失时返回空(不阻塞)。
func runNovelQACheck(proj string) string {
	script := filepath.Join(NovelSkillDir, "scripts", "qa_check.py")
	if !fileExists(script) {
		return ""
	}
	cmd := exec.Command(manjuPythonPath(), script, "--root", proj)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// manjuSkillUpdate 小说续作技能 git 同步(技能目录是 git 仓库时 git pull;失败透出原因)。
func manjuSkillUpdate(w http.ResponseWriter, r *http.Request) {
	if !dirExists(filepath.Join(NovelSkillDir, ".git")) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "技能目录不是 git 仓库(先按 github 仓库克隆到目录)"})
		return
	}
	cmd := exec.Command("git", "-C", NovelSkillDir, "pull", "--ff-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "git pull 失败: " + err.Error(), "output": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": msg})
}
