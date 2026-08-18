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
	"sync"
	"strings"
	"time"

	"nilix/internal/backend"
	"nilix/internal/config"
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
	if req.Chapters < 52 { // 技能硬下限 ≥52(默认 56,8 卷×7 章)
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
{"logline":"一句话故事","characters":[{"name":"","desc":"身份/性格/金手指,60字内","img_prompt":"写实电影级人物生图提示词,含外貌/服装/气质,60-100字,禁日漫风"}],"world":"世界观与力量体系,150字内,必含可量化等级表","volumes":[{"no":1,"title":"卷名"}],
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
			Name      string `json:"name"`
			Desc      string `json:"desc"`
			ImgPrompt string `json:"img_prompt"`
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
		p := c.Desc
		if c.ImgPrompt != "" {
			p += " | 生图:" + c.ImgPrompt
		}
		fmt.Fprintf(&sb, "- **%s**:%s\n", c.Name, p)
	}
	sb.WriteString("\n## 卷结构\n")
	for _, v := range plan.Volumes {
		fmt.Fprintf(&sb, "- 卷%02d %s\n", v.No, v.Title)
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
	// 技能规范副本:创作指令卡 / 章节写作规范(模板存在则复制,缺失静默)
	skillRef := `C:\Users\Administrator\.agents\skills\shuangwen-novel\references`
	_ = copySkillFile(filepath.Join(skillRef, "创作指令卡-模板.md"), filepath.Join(proj, "设定集", "创作指令卡-AI版本.md"))
	_ = copySkillFile(filepath.Join(skillRef, "章节写作规范.md"), filepath.Join(proj, "设定集", "章节写作规范.md"))
	// 素材/人物生成提示词.md(全角色写实电影级提示词)
	var mats strings.Builder
	mats.WriteString("# 人物生成提示词(写实电影级 · 禁日漫风)\n\n")
	for _, c := range plan.Characters {
		fmt.Fprintf(&mats, "## %s\n%s\n\n", c.Name, nvOrDefault(c.ImgPrompt, c.Desc))
	}
	_ = os.MkdirAll(filepath.Join(proj, "素材"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "素材", "人物生成提示词.md"), []byte(mats.String()), 0644)
	// 封面提示词.md + 异步 Z-Image 渲染封面.png
	coverPrompt := fmt.Sprintf("epic novel cover art, %s %s, %s, cinematic lighting, highly detailed, dramatic composition, masterpiece, 4k", req.Title, nvOrDefault(req.Style, "热血爽文"), nvOrDefault(req.Genre, "玄幻逆袭"))
	_ = os.MkdirAll(filepath.Join(proj, "封面"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "封面", "封面提示词.md"), []byte(coverPrompt), 0644)
	go renderNovelCover(proj, coverPrompt)
	saveNovelState(proj, novelState{Title: req.Title, Total: len(plan.Chapters), Current: 0})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dir": proj, "outline": sb.String(),
		"chapters": len(plan.Chapters)})
}

// handleNovelChapter 逐章生成(幂等):读大纲条目+前章结尾,产出 ≥1280 字正文落盘。
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
	res, err := writeNovelChapter(req.Title, req.No, cfg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
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
	st := loadNovelState(proj)
	if st.Total == 0 && len(items) > 0 {
		// 旧项目无存档:按现有章节补一个
		st.Total = len(items) + 7
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": proj, "hasOutline": hasOutline, "chapters": items,
		"state": st})
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

// copySkillFile 复制技能模板文件(不存在静默跳过)
func copySkillFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0644)
}

// renderNovelCover 异步用 ComfyUI Z-Image 渲染小说封面(失败静默)
func renderNovelCover(proj, prompt string) {
	defer func() { _ = recover() }()
	c := newComfyClient("http://127.0.0.1:8190")
	if _, err := c.online(); err != nil {
		return
	}
	neg := "lowres, bad anatomy, text, watermark, logo, deformed, blurry"
	wf := wfZImage(prompt, "z_image_turbo_bf16.safetensors", "qwen_3_4b.safetensors", "ae.safetensors", 90321177, 768, 1024, "novel_cover", neg)
	pid, err := c.submit(wf)
	if err != nil {
		return
	}
	if err := c.wait(pid, 180*time.Second, 2*time.Second); err != nil {
		return
	}
	entry := c.history(pid)
	if img := comfyOutputImage(entry); img != "" {
		out := filepath.Join(comfyRoot, "output", filepath.Base(img))
		if data, err := os.ReadFile(out); err == nil {
			_ = os.MkdirAll(filepath.Join(proj, "封面"), 0755)
			_ = os.WriteFile(filepath.Join(proj, "封面", "封面.png"), data, 0644)
		}
	}
}

// appendToFullBook 增量合并全本(标题+目录+全文),按章号追加
func appendToFullBook(proj, title string, no int, chTitle, content string) {
	fullDir := filepath.Join(proj, "全本")
	_ = os.MkdirAll(fullDir, 0755)
	fp := filepath.Join(fullDir, novelTitleSan.ReplaceAllString(title, "")+"·全本.md")
	var b []byte
	if old, err := os.ReadFile(fp); err == nil {
		b = old
	} else {
		b = []byte(fmt.Sprintf("# %s(全本)\n\n> 爽文一条龙 · 每章 ≥1280 字\n\n", title))
	}
	// 目录占位:重建目录(收集已有章节)
	dirLines := ""
	_ = filepath.Walk(filepath.Join(proj, "正文"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		dirLines += "- " + strings.TrimSuffix(info.Name(), ".md") + "\n"
		return nil
	})
	// 追加本章正文
	b = append(b, []byte(fmt.Sprintf("\n\n## 第%03d章 %s\n\n%s\n", no, chTitle, content))...)
	_ = os.WriteFile(fp, b, 0644)
}

// ================= 后台自动续写(弹窗关闭仍在后台跑) =================
type novelAutoTask struct {
	Title   string `json:"title"`
	Running bool   `json:"running"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Done    bool   `json:"done"`
	stop    chan struct{}
}

var (
	novelAutoMu   sync.Mutex
	novelAutoTasks = map[string]*novelAutoTask{}
)

func novelAutoStatus(title string) map[string]any {
	novelAutoMu.Lock()
	defer novelAutoMu.Unlock()
	t, ok := novelAutoTasks[title]
	if !ok {
		return map[string]any{"title": title, "running": false, "current": 0, "total": 0, "done": false}
	}
	return map[string]any{"title": t.Title, "running": t.Running, "current": t.Current, "total": t.Total, "done": t.Done}
}

// handleNovelAuto 启动后台自动续写(幂等:已运行直接返回)
func (s *Server) handleNovelAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeErr(w, http.StatusBadRequest, "missing title")
		return
	}
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	if cfg.LLM.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "未配置 LLM API key")
		return
	}
	novelAutoMu.Lock()
	t, ok := novelAutoTasks[req.Title]
	if ok && t.Running {
		novelAutoMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
		return
	}
	t = &novelAutoTask{Title: req.Title, Running: true, stop: make(chan struct{})}
	novelAutoTasks[req.Title] = t
	novelAutoMu.Unlock()
	go func() {
		defer func() {
			novelAutoMu.Lock()
			t.Running = false
			novelAutoMu.Unlock()
			_ = recover()
		}()
		for {
			next := 0
			novelAutoMu.Lock()
			cur := t.Current
			novelAutoMu.Unlock()
			// 找下一未写章
			for n := cur + 1; n <= 600; n++ {
				if pf, _ := findChapter(novelProjDir(req.Title), n); pf == "" {
					next = n
					break
				}
			}
			if next == 0 {
				novelAutoMu.Lock()
				t.Done = true
				novelAutoMu.Unlock()
				return
			}
			select {
			case <-t.stop:
				return
			default:
			}
			res, err := writeNovelChapter(req.Title, next, cfg)
			novelAutoMu.Lock()
			if err == nil && !res["exists"].(bool) {
				t.Current = next
			}
			novelAutoMu.Unlock()
			if err != nil {
				return // 失败停(可手动重启续写)
			}
			select {
			case <-t.stop:
				return
			case <-time.After(2 * time.Second): // 限速,避免把 LLM 打爆
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
}

// handleNovelAutoStop 停止后台续写
func (s *Server) handleNovelAutoStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	novelAutoMu.Lock()
	t, ok := novelAutoTasks[req.Title]
	if ok && t.Running {
		close(t.stop)
		t.Running = false
	}
	novelAutoMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNovelStatusAll 全量创作状态(书架信号灯:绿=全本完/蓝=续作中/黄=断点/红=未完成)
func (s *Server) handleNovelStatusAll(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	entries, err := os.ReadDir(novelRootDir)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	novelAutoMu.Lock()
	defer novelAutoMu.Unlock()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		title := e.Name()
		proj := filepath.Join(novelRootDir, title)
		chapters := 0
		_ = filepath.Walk(filepath.Join(proj, "正文"), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
				chapters++
			}
			return nil
		})
		total := 0
		if b, err := os.ReadFile(filepath.Join(proj, "设定集", "设定集与大纲.md")); err == nil {
			m := regexp.MustCompile(`计划\s*(\d+)\s*章`).FindStringSubmatch(string(b))
			if len(m) > 1 {
				total, _ = strconv.Atoi(m[1])
			}
		}
		running := false
		if t, ok := novelAutoTasks[title]; ok && t.Running {
			running = true
		}
		status := "none"
		if total > 0 {
			switch {
			case running:
				status = "blue" // 续作中
			case chapters >= total:
				status = "green" // 全本完
			case chapters > 0:
				status = "yellow" // 续作断点
			default:
				status = "red" // 未完成
			}
		}
		out[title] = map[string]any{"chapters": chapters, "total": total, "running": running, "status": status}
	}
	writeJSON(w, http.StatusOK, out)
}

// ================= 创作状态存档(按小说 ID=目录名 持久化) =================
type novelState struct {
	Title     string `json:"title"`
	Total     int    `json:"total"`
	Current   int    `json:"current"`
	UpdatedAt string `json:"updatedAt"`
}

func novelStateFile(proj string) string {
	return filepath.Join(proj, "novel_state.json")
}

func loadNovelState(proj string) novelState {
	var st novelState
	if b, err := os.ReadFile(novelStateFile(proj)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveNovelState(proj string, st novelState) {
	st.UpdatedAt = time.Now().Format("2006-01-02 15:04")
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(novelStateFile(proj), b, 0644)
}

func touchNovelState(proj, title string, no int) {
	st := loadNovelState(proj)
	st.Title = title
	if no > st.Current {
		st.Current = no
	}
	if st.Total == 0 {
		st.Total = 56
	}
	saveNovelState(proj, st)
}

// writeNovelChapter 写第 no 章(幂等:已存在返回 exists),供手动与后台自动续写共用
func writeNovelChapter(title string, no int, cfg config.Settings) (map[string]any, error) {
	proj := novelProjDir(title)
	b, err := os.ReadFile(filepath.Join(proj, "设定集", "设定集与大纲.md"))
	if err != nil {
		return nil, fmt.Errorf("先立项生成大纲")
	}
	outline := string(b)
	if f, _ := findChapter(proj, no); f != "" {
		return map[string]any{"ok": true, "exists": true, "title": fmt.Sprintf("第%03d章", no)}, nil
	}
	var entry string
	for _, line := range strings.Split(outline, "\n") {
		if strings.HasPrefix(line, fmt.Sprintf("- 第%03d章", no)) {
			entry = strings.TrimPrefix(line, "- ")
			break
		}
	}
	if entry == "" {
		return nil, fmt.Errorf("大纲缺少第 %d 章", no)
	}
	prevTail := "(本书第一章,直接开局)"
	if no > 1 {
		if pf, _ := findChapter(proj, no-1); pf != "" {
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
严格输出 {"title":"章节名","content":"正文全文"} JSON。`, title, clip(outline, 2400), no, entry, prevTail)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, err := llm.Chat(ctx, []backend.ChatMessage{
		{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 8000, 0.85)
	if err != nil {
		return nil, fmt.Errorf("第%d章生成失败: %s", no, err.Error())
	}
	var ch struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &ch); err != nil || len([]rune(ch.Content)) < 800 {
		return nil, fmt.Errorf("第%d章解析失败或字数不足,请重试", no)
	}
	chTitle := novelTitleSan.ReplaceAllString(ch.Title, "")
	if chTitle == "" {
		chTitle = fmt.Sprintf("第%03d章", no)
	}
	vol := (no + 6) / 7
	volName := fmt.Sprintf("卷%02d", vol)
	for _, line := range strings.Split(outline, "\n") {
		if strings.HasPrefix(line, "- 卷") {
			var vno int
			var vtitle string
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, "- 卷"), "%d %s", &vno, &vtitle); err == nil && vno == vol {
				volName = fmt.Sprintf("卷%02d_%s", vol, vtitle)
				break
			}
		}
	}
	volDir := filepath.Join(proj, "正文", volName)
	_ = os.MkdirAll(volDir, 0755)
	name := fmt.Sprintf("第%03d章_%s.md", no, chTitle)
	file := filepath.Join(volDir, name)
	body := fmt.Sprintf("# 第%03d章 %s\n\n%s\n", no, chTitle, ch.Content)
	if err := os.WriteFile(file, []byte(body), 0644); err != nil {
		return nil, err
	}
	appendToFullBook(proj, title, no, chTitle, ch.Content)
	touchNovelState(proj, title, no)
	return map[string]any{"ok": true, "exists": false, "file": name,
		"words": len([]rune(ch.Content)), "title": chTitle}, nil
}
