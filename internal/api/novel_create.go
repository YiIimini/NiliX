// Package api —— 网页版爽文小说创作(固化 shuangwen-novel 技能核心流程):
// 立项(LLM 生成人物/世界观/逐章大纲) → 逐章生成(≥1280 字,卷目录归档) → 落盘 Novel/<书名>/。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"nilix/internal/backend"
)

const novelRootDir = `C:\Mi\Ai\WorkBench\Novel`

var novelTitleSan = regexp.MustCompile(`[\\/:*?"<>|]`)

func novelProjDir(title string) string {
	return filepath.Join(novelRootDir, novelTitleSan.ReplaceAllString(strings.TrimSpace(title), ""))
}

// handleNovelCreate 立项:生成设定集与大纲(同步,约 30-90s),幂等(已有大纲直接返回)。
func (s *Server) handleNovelCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Genre    string `json:"genre"`
		Style    string `json:"style"`
		Chapters int    `json:"chapters"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeErr(w, http.StatusBadRequest, "请填书名")
		return
	}
	if req.Chapters < 8 {
		req.Chapters = 56
	}
	if req.Chapters > 300 {
		req.Chapters = 300
	}
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	if cfg.LLM.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "未配置 LLM API key(顶栏设置 → 剧本模型)")
		return
	}

	proj := novelProjDir(req.Title)
	settingFile := filepath.Join(proj, "设定集", "设定集与大纲.md")
	if b, err := os.ReadFile(settingFile); err == nil && len(b) > 500 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exists": true, "dir": proj,
			"outline": string(b), "chapters": req.Chapters})
		return
	}

	llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.RequestTimeout)*time.Second)
	sys := "你是资深爽文小说架构师,只输出 JSON,不输出任何其它内容。"
	usr := fmt.Sprintf(`为小说《%s》设计全本设定与逐章大纲。
题材:%s;风格:%s;总章数:%d(每 7 章一卷)。
爽点主线固定:开局被欺负 → 中期反转 → 后期打脸 → 结局封神。
严格输出 JSON:
{"logline":"一句话故事","characters":[{"name":"","desc":"身份/性格/金手指,60字内"}],"world":"世界观与力量体系,150字内","volumes":[{"no":1,"title":"卷名"}],
"chapters":[{"no":1,"title":"章节名","premise":"本章事件,50字内","conflict":"冲突与爽点,40字内"}]}
chapters 必须恰好 %d 条,no 从 1 连续递增;卷数=%d。`, req.Title, nvOrDefault(req.Genre, "玄幻逆袭"),
		nvOrDefault(req.Style, "热血爽文"), req.Chapters, req.Chapters, (req.Chapters+6)/7)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, err := llm.Chat(ctx, []backend.ChatMessage{
		{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 16000, 0.8)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "大纲生成失败: "+err.Error())
		return
	}
	var plan struct {
		Logline    string `json:"logline"`
		Characters []struct {
			Name string `json:"name"`
			Desc string `json:"desc"`
		} `json:"characters"`
		World    string `json:"world"`
		Volumes  []struct {
			No    int    `json:"no"`
			Title string `json:"title"`
		} `json:"volumes"`
		Chapters []struct {
			No       int    `json:"no"`
			Title    string `json:"title"`
			Premise  string `json:"premise"`
			Conflict string `json:"conflict"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &plan); err != nil || len(plan.Chapters) == 0 {
		writeErr(w, http.StatusBadGateway, "大纲解析失败(模型输出非预期 JSON),请重试")
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s —— 设定集与大纲\n\n> 题材:%s | 风格:%s | 计划 %d 章(7章/卷)\n\n## 一句话故事\n%s\n\n## 世界观\n%s\n\n## 人物\n",
		req.Title, nvOrDefault(req.Genre, "玄幻逆袭"), nvOrDefault(req.Style, "热血爽文"), len(plan.Chapters), plan.Logline, plan.World)
	for _, c := range plan.Characters {
		fmt.Fprintf(&sb, "- **%s**:%s\n", c.Name, c.Desc)
	}
	sb.WriteString("\n## 逐章大纲\n")
	for _, ch := range plan.Chapters {
		fmt.Fprintf(&sb, "- 第%03d章 %s:%s|%s\n", ch.No, ch.Title, ch.Premise, ch.Conflict)
	}
	if err := os.MkdirAll(filepath.Join(proj, "设定集"), 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(settingFile, []byte(sb.String()), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dir": proj, "outline": sb.String(),
		"chapters": len(plan.Chapters)})
}

// handleNovelChapter 逐章生成:读大纲对应条目+前章结尾,产出 ≥1280 字正文落盘(幂等,已存在直接返回)。
func (s *Server) handleNovelChapter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		No    int    `json:"no"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || req.No < 1 {
		writeErr(w, http.StatusBadRequest, "参数缺失")
		return
	}
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	if cfg.LLM.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "未配置 LLM API key")
		return
	}
	proj := novelProjDir(req.Title)
	b, err := os.ReadFile(filepath.Join(proj, "设定集", "设定集与大纲.md"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "先立项生成大纲")
		return
	}
	outline := string(b)

	// 已有该章(按 第NNN章 前缀)→ 幂等返回
	if f, name := findChapter(proj, req.No); f != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exists": true, "file": name})
		return
	}

	// 大纲条目行
	var entry string
	for _, line := range strings.Split(outline, "\n") {
		if strings.HasPrefix(line, fmt.Sprintf("- 第%03d章", req.No)) {
			entry = strings.TrimPrefix(line, "- ")
			break
		}
	}
	if entry == "" {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("大纲缺少第 %d 章", req.No))
		return
	}
	// 前一章结尾(承接)
	prevTail := "(本书第一章,直接开局)"
	if req.No > 1 {
		if pf, _ := findChapter(proj, req.No-1); pf != "" {
			if pb, err := os.ReadFile(pf); err == nil {
				t := strings.TrimSpace(string(pb))
				if len(t) > 120 {
					t = t[len(t)-120:]
				}
				prevTail = t
			}
		}
	}

	llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.RequestTimeout)*time.Second)
	sys := "你是爽文小说写手。要求:正文口语化短句、去AI味;场景/情绪具体;每章结尾留钩子;不要小标题、不要总结。只输出 JSON。"
	usr := fmt.Sprintf(`小说《%s》设定与大纲如下(节选):
%s

本次写第 %d 章。大纲条目:%s
上一章结尾(承接,不要复述):%s

硬性要求:正文 ≥1280 字(2000 字左右最佳);推进大纲事件;至少一个爽点或冲突升级。
严格输出 {"title":"章节名","content":"正文全文"} JSON。`, req.Title, clip(outline, 2400), req.No, entry, prevTail)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, err := llm.Chat(ctx, []backend.ChatMessage{
		{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 8000, 0.85)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "第"+strconv.Itoa(req.No)+"章生成失败: "+err.Error())
		return
	}
	var ch struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &ch); err != nil || len([]rune(ch.Content)) < 800 {
		writeErr(w, http.StatusBadGateway, "第"+strconv.Itoa(req.No)+"章解析失败或字数不足,请重试")
		return
	}
	chTitle := novelTitleSan.ReplaceAllString(ch.Title, "")
	if chTitle == "" {
		chTitle = fmt.Sprintf("第%03d章", req.No)
	}
	vol := (req.No + 6) / 7
	volDir := filepath.Join(proj, "正文", fmt.Sprintf("卷%02d", vol))
	_ = os.MkdirAll(volDir, 0755)
	name := fmt.Sprintf("第%03d章_%s.md", req.No, chTitle)
	file := filepath.Join(volDir, name)
	body := fmt.Sprintf("# 第%03d章 %s\n\n%s\n", req.No, chTitle, ch.Content)
	if err := os.WriteFile(file, []byte(body), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": name,
		"words": len([]rune(ch.Content)), "title": chTitle})
}

// handleNovelProgress 查询某书已有章节(断点续写)。
func (s *Server) handleNovelProgress(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		writeErr(w, http.StatusBadRequest, "missing title")
		return
	}
	proj := novelProjDir(title)
	type item struct {
		No   int    `json:"no"`
		File string `json:"file"`
	}
	var items []item
	_ = filepath.Walk(filepath.Join(proj, "正文"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		m := regexp.MustCompile(`第(\d{3})章`).FindStringSubmatch(info.Name())
		if m == nil {
			return nil
		}
		no, _ := strconv.Atoi(m[1])
		items = append(items, item{No: no, File: info.Name()})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].No < items[j].No })
	_, hasOutline := func() (bool, bool) {
		_, e := os.Stat(filepath.Join(proj, "设定集", "设定集与大纲.md"))
		return true, e == nil
	}()
	writeJSON(w, http.StatusOK, map[string]any{"dir": proj, "hasOutline": hasOutline, "chapters": items})
}

func findChapter(proj string, no int) (string, string) {
	var found, name string
	_ = filepath.Walk(filepath.Join(proj, "正文"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.HasPrefix(info.Name(), fmt.Sprintf("第%03d章", no)) {
			found, name = p, info.Name()
		}
		return nil
	})
	return found, name
}

func nvOrDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		if j := strings.Index(s[i:], "\n"); j >= 0 {
			s = s[i+j+1:]
		}
		if k := strings.LastIndex(s, "```"); k >= 0 {
			s = s[:k]
		}
	}
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 {
		s = s[:j+1]
	}
	return strings.TrimSpace(s)
}
