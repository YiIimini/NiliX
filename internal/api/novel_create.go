// Package api —— 网页版爽文小说创作(固化 shuangwen-novel 技能核心流程):
// 立项(LLM 生成人物/世界观/逐章大纲) → 逐章生成(≥1280 字,卷目录归档) → 落盘 Novel/<书名>/。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

var (
	novelTitleSan = regexp.MustCompile(`[\\/:*?"<>|]`)
	// reChapterNo 正文文件名章号(如 第001章_标题.md);提为包级,避免续写循环里每文件重编译
	reChapterNo = regexp.MustCompile(`第(\d{3})章`)
	// reNovelPlan 设定集「计划 N 章」标注
	reNovelPlan = regexp.MustCompile(`计划\s*(\d+)\s*章`)
)

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
	go renderNovelCover(proj, coverPrompt, cfg)
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
	res, err := writeNovelChapter(context.Background(), req.Title, req.No, cfg, "")
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
			m := reChapterNo.FindStringSubmatch(info.Name())
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
	// 创作档案:已审章节数 / 均分 / 高频问题
	reviewSummary := map[string]any{"count": 0, "avg": 0.0, "topIssue": ""}
	if len(st.Reviews) > 0 {
		avg, n := 0.0, 0
		issueCnt := map[string]int{}
		for _, rv := range st.Reviews {
			avg += rv.Score
			n++
			for _, is := range rv.Issues {
				issueCnt[is]++
			}
		}
		top, topN := "", 0
		for k, v := range issueCnt {
			if v > topN {
				top, topN = k, v
			}
		}
		reviewSummary["count"] = n
		reviewSummary["avg"] = avg / float64(n)
		reviewSummary["topIssue"] = top
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": proj, "hasOutline": hasOutline, "chapters": items,
		"state": st, "reviewSummary": reviewSummary})
}

// handleNovelAnalyze 立项前 AI 策划分析:题材定位/卖点/风格建议/开篇钩子/风险提醒。
// 纯分析不改任何文件,前端展示分析卡,用户可「采用风格」后立项。
func (s *Server) handleNovelAnalyze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Genre string `json:"genre"`
		Style string `json:"style"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	if cfg.LLM.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "未配置 LLM API key(顶栏设置 → 剧本模型)")
		return
	}
	llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.RequestTimeout)*time.Second)
	sys := "你是爆款网文策划分析师,只输出 JSON,不输出任何其它内容。"
	usr := fmt.Sprintf(`为一部「%s」题材、「%s」风格的小说做立项策划分析(题材/风格可留空,由你按市场爆款来补)。
严格输出 JSON:
{"position":"题材定位与目标读者,80字内","selling":"核心卖点与爽点节奏策略,100字内","style_advice":"与题材最匹配的叙事风格建议,40字内(如:热血燃向/轻松搞笑/悬疑烧脑/治愈温情)","hook":"开篇第一章钩子建议,60字内","risk":"常见翻车点与规避,60字内"}`,
		nvOrDefault(req.Genre, "玄幻逆袭"), nvOrDefault(req.Style, "热血爽文"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, err := llm.Chat(ctx, []backend.ChatMessage{
		{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 3000, 0.7)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "策划分析失败: "+err.Error())
		return
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &out); err != nil {
		writeErr(w, http.StatusBadGateway, "策划分析解析失败,请重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": out})
}

// handleNovelReview 章节审稿:8 维评分 + 问题清单 + 修改建议,结论写入创作档案(novel_state)。
func (s *Server) handleNovelReview(w http.ResponseWriter, r *http.Request) {
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
	proj := novelProjDir(req.Title)
	f, _ := findChapter(proj, req.No)
	if f == "" {
		writeErr(w, http.StatusBadRequest, "本章还没写,先生成章节再评章")
		return
	}
	content, err := os.ReadFile(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读章节失败: "+err.Error())
		return
	}
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	rv, rerr := reviewChapterCore(cfg, proj, req.Title, req.No, string(content))
	if rerr != nil {
		writeErr(w, http.StatusBadGateway, "评章失败: "+rerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": rv})
}

// reviewChapterCore 审稿核心(手动评章与续写自动返工共用):8 维打分 + 问题清单 + 建议,
// 结论写创作档案(novel_state.Reviews)。
func reviewChapterCore(cfg config.Settings, proj, title string, no int, content string) (novelChapterReview, error) {
	// 带上大纲该章条目作为评分上下文
	outline := ""
	if b, err := os.ReadFile(filepath.Join(proj, "设定集", "设定集与大纲.md")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, fmt.Sprintf("- 第%03d章", no)) {
				outline = strings.TrimPrefix(line, "- ")
				break
			}
		}
	}
	llm := backend.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.RequestTimeout)*time.Second)
	sys := "你是资深网文审稿编辑,对章节按 8 个维度打分(每项 0-100),只输出 JSON,不输出其它内容。"
	usr := fmt.Sprintf(`请审阅《%s》第%d章。
大纲条目:%s
正文(%d字):
%s
严格输出 JSON:
{"dims":{"opening":开篇暴击,"conflict":冲突张力,"satisfy":爽点密度,"pace":节奏紧凑,"dialogue":台词质量,"hook":钩子设计,"shootable":可拍性,"consistency":一致性风险(越高=越偏离设定,扣分项)},"score":加权总分0-100(可拍性×1.5、一致性按(100-风险)×1.5、其余×1,求和后归一),保留1位小数,"issues":["具体到段落的问题,最多3条"],"suggestion":"修改建议,80字内"}`,
		title, no, nvOrDefault(outline, "无"), len([]rune(content)), clip(content, 6000))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, err := llm.Chat(ctx, []backend.ChatMessage{
		{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 3000, 0.3)
	if err != nil {
		return novelChapterReview{}, err
	}
	var out struct {
		Dims       map[string]float64 `json:"dims"`
		Score      float64            `json:"score"`
		Issues     []string           `json:"issues"`
		Suggestion string             `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &out); err != nil || len(out.Dims) == 0 {
		return novelChapterReview{}, fmt.Errorf("评章解析失败,请重试")
	}
	rv := novelChapterReview{Score: out.Score, Dims: out.Dims, Issues: out.Issues,
		Suggestion: out.Suggestion, At: time.Now().Format("2006-01-02 15:04")}
	st := loadNovelState(proj)
	st.Reviews[no] = rv
	saveNovelState(proj, st)
	return rv, nil
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
	// 剥 ```fence```(可带语言标记);未闭合时剥掉开头标记保留正文,不再误删尾部字符
	if i := strings.Index(s, "```"); i >= 0 {
		if j := strings.Index(s[i+3:], "```"); j >= 0 {
			s = s[i+3 : i+3+j]
		} else {
			s = s[:i] + s[i+3:]
		}
	}
	// 只保留第一个 { 到最后一个 } 之间的 JSON 主体
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
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

// renderNovelCover 异步用 ComfyUI Z-Image 渲染小说封面(失败打日志,不静默)。
// 地址/模型/输出目录全部取自 settings.json(与 Comfy 页面同一数据源),换配置即生效。
func renderNovelCover(proj, prompt string, cfg config.Settings) {
	defer func() { _ = recover() }()
	c := newComfyClient(cfg.Render.ComfyURL)
	if _, err := c.online(); err != nil {
		log.Printf("封面渲染跳过(ComfyUI 离线 %s): %v", cfg.Render.ComfyURL, err)
		return
	}
	neg := "lowres, bad anatomy, text, watermark, logo, deformed, blurry"
	seed := cfg.Render.Seed
	if seed == 0 {
		seed = 1688
	}
	wf := wfZImage(prompt, cfg.Render.ZImageUnet, cfg.Render.ZImageClip, cfg.Render.ZImageVae, seed, 768, 1024, "novel_cover", neg)
	pid, err := c.submit(wf)
	if err != nil {
		log.Printf("封面渲染提交失败: %v", err)
		return
	}
	if err := c.wait(pid, 180*time.Second, 2*time.Second); err != nil {
		log.Printf("封面渲染失败: %v", err)
		return
	}
	entry := c.history(pid)
	if img := comfyOutputImage(entry); img != "" {
		// 产物目录=配置的 ComfyOutput(经 NiliX 启动的 ComfyUI 用 --output-directory 指向这里)
		out := filepath.Join(cfg.Paths.ComfyOutput, filepath.Base(img))
		if data, err := os.ReadFile(out); err == nil {
			_ = os.MkdirAll(filepath.Join(proj, "封面"), 0755)
			_ = os.WriteFile(filepath.Join(proj, "封面", "封面.png"), data, 0644)
			log.Printf("✅ 封面已生成: %s", filepath.Join(proj, "封面", "封面.png"))
		} else {
			log.Printf("封面产物读取失败 %s: %v", out, err)
		}
	} else {
		log.Printf("封面任务完成但未找到图片输出")
	}
}

// appendToFullBook 纯追加一章进全本(O(1):不重读旧文、不重扫目录——600 章续写原来是
// 每章整读整写+全树 Walk 的 O(n²));目录由 rebuildFullBookTOC 在续写完成/手动触发时重建。
// 追加前去重:全本已含同章号标题则跳过(手动与自动并发写同章的护栏,写锁之外的二道防线)。
func appendToFullBook(proj, title string, no int, chTitle, content string) {
	fullDir := filepath.Join(proj, "全本")
	_ = os.MkdirAll(fullDir, 0755)
	fp := filepath.Join(fullDir, novelTitleSan.ReplaceAllString(title, "")+"·全本.md")
	head := fmt.Sprintf("第%03d章 %s", no, chTitle)
	if b, err := os.ReadFile(fp); err == nil && strings.Contains(string(b), "\n## "+head) {
		return // 已收录(并发/重复调用护栏)
	}
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		_, _ = f.WriteString(fmt.Sprintf("# %s(全本)\n\n> 爽文一条龙 · 每章 ≥1280 字\n", title))
	}
	_, _ = f.WriteString(fmt.Sprintf("\n\n## 第%03d章 %s\n\n%s\n", no, chTitle, content))
}

// rebuildFullBookTOC 重建全本目录(头部目录区):续写完成/手动生成后调用一次。
func rebuildFullBookTOC(proj, title string) {
	fp := filepath.Join(proj, "全本", novelTitleSan.ReplaceAllString(title, "")+"·全本.md")
	b, err := os.ReadFile(fp)
	if err != nil {
		return
	}
	var ids []string
	_ = filepath.Walk(filepath.Join(proj, "正文"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			ids = append(ids, strings.TrimSuffix(info.Name(), ".md"))
		}
		return nil
	})
	sort.Strings(ids)
	var b2 strings.Builder
	b2.WriteString(fmt.Sprintf("# %s(全本)\n\n> 爽文一条龙 · 每章 ≥1280 字 · 目录 %d 章\n\n", title, len(ids)))
	for _, id := range ids {
		b2.WriteString("- " + id + "\n")
	}
	// 正文区 = 第一个 "\n\n## 第001章" 起
	if i := strings.Index(string(b), "\n## 第"); i >= 0 {
		b2.WriteString(string(b)[i:])
	}
	_ = os.WriteFile(fp, []byte(b2.String()), 0644)
}

// ================= 后台自动续写(弹窗关闭仍在后台跑) =================
type novelAutoTask struct {
	Title   string `json:"title"`
	Running bool   `json:"running"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Done    bool   `json:"done"`
	Err     string `json:"error,omitempty"` // 失败原因(LLM 报错/字数不足等),前端展示并指向断点续写
	stop    chan struct{}
	ctx     context.Context // 停止续写时 cancel:中断在途 LLM 调用,不再烧 token
	cancel  context.CancelFunc
}

var (
	novelAutoMu    sync.Mutex
	novelAutoTasks = map[string]*novelAutoTask{} // key=项目目录名(与书架/statusAll 一致)
)

func novelAutoStatus(title string) map[string]any {
	dirKey := filepath.Base(novelProjDir(title))
	novelAutoMu.Lock()
	defer novelAutoMu.Unlock()
	t, ok := novelAutoTasks[dirKey]
	if !ok {
		return map[string]any{"title": title, "running": false, "current": 0, "total": 0, "done": false}
	}
	out := map[string]any{"title": t.Title, "running": t.Running, "current": t.Current, "total": t.Total, "done": t.Done}
	if t.Err != "" {
		out["error"] = t.Err
	}
	return out
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
	dirKey := filepath.Base(novelProjDir(req.Title))
	novelAutoMu.Lock()
	t, ok := novelAutoTasks[dirKey]
	if ok && t.Running {
		novelAutoMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t = &novelAutoTask{Title: req.Title, Running: true, stop: make(chan struct{}), ctx: ctx, cancel: cancel}
	novelAutoTasks[dirKey] = t
	novelAutoMu.Unlock()
	go func() {
		defer func() {
			novelAutoMu.Lock()
			t.Running = false
			novelAutoMu.Unlock()
			_ = recover()
		}()
		proj := novelProjDir(req.Title)
		for {
			next := 0
			novelAutoMu.Lock()
			cur := t.Current
			novelAutoMu.Unlock()
			// 找下一未写章:一次目录扫描建章号集合(原来每章最多 600 次全树 Walk,书越厚越慢)
			have := map[int]bool{}
			_ = filepath.Walk(proj, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if m := reChapterNo.FindStringSubmatch(info.Name()); m != nil {
					if n, e := strconv.Atoi(m[1]); e == nil {
						have[n] = true
					}
				}
				return nil
			})
			for n := cur + 1; n <= 600; n++ {
				if !have[n] {
					next = n
					break
				}
			}
			if next == 0 {
				novelAutoMu.Lock()
				t.Done = true
				novelAutoMu.Unlock()
				rebuildFullBookTOC(proj, req.Title) // 全本目录一次性重建(逐章纯追加不维护目录)
				return
			}
			select {
			case <-t.stop:
				return
			default:
			}
			res, err := writeNovelChapter(t.ctx, req.Title, next, cfg, "")
			novelAutoMu.Lock()
			if err == nil && !res["exists"].(bool) {
				t.Current = next
			}
			novelAutoMu.Unlock()
			if err != nil {
				novelAutoMu.Lock()
				t.Err = err.Error()
				novelAutoMu.Unlock()
				return // 失败停(可手动重启续写;原因经 status 接口展示)
			}
			// Agent 化续写:自动审稿,低于 70 分带意见删稿重写一轮,重写稿复审归档
			if res["exists"] == false && cfg.LLM.APIKey != "" {
				if f, _ := findChapter(proj, next); f != "" {
					if content, cerr := os.ReadFile(f); cerr == nil {
						if rv, rerr := reviewChapterCore(cfg, proj, req.Title, next, string(content)); rerr == nil {
							if rv.Score < 70 {
								note := strings.Join(rv.Issues, ";")
								if rv.Suggestion != "" {
									if note != "" {
										note += ";"
									}
									note += rv.Suggestion
								}
								_ = os.Remove(f)
								if res2, err2 := writeNovelChapter(t.ctx, req.Title, next, cfg, note); err2 == nil && res2["exists"] == false {
									if f2, _ := findChapter(proj, next); f2 != "" {
										if c2, e2 := os.ReadFile(f2); e2 == nil {
											_, _ = reviewChapterCore(cfg, proj, req.Title, next, string(c2)) // 重写稿复审(归档)
										}
									}
								}
							}
						}
					}
				}
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

// handleNovelAutoStop 停止后台续写:取消在途 LLM 调用(不再烧 token),并等 goroutine 退出
func (s *Server) handleNovelAutoStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	dirKey := filepath.Base(novelProjDir(req.Title))
	novelAutoMu.Lock()
	t, ok := novelAutoTasks[dirKey]
	if ok && t.Running {
		t.Running = false
		t.cancel() // 中断在途 LLM 调用
		close(t.stop)
	}
	novelAutoMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNovelAutoStatus 查询单书续写进度(前端轮询:当前章/总章/运行中/已完成)
func (s *Server) handleNovelAutoStatus(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		writeErr(w, http.StatusBadRequest, "missing title")
		return
	}
	writeJSON(w, http.StatusOK, novelAutoStatus(title))
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
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // 跳过文件与隐藏杂物目录(.tools/.git 等)
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
			if m := reNovelPlan.FindStringSubmatch(string(b)); len(m) > 1 {
				total, _ = strconv.Atoi(m[1])
			}
		}
		running := false
		if t, ok := novelAutoTasks[title]; ok && t.Running {
			running = true
		}
		status := "none"
		if total == 0 && chapters > 0 {
			// 旧书无"计划 N 章"标注:有正文默认视为已完成全本
			total = chapters
		}
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
// novelChapterReview 单章审稿结论(8 维 + 加权总分 + 问题清单)
type novelChapterReview struct {
	Score      float64            `json:"score"`
	Dims       map[string]float64 `json:"dims"`
	Issues     []string           `json:"issues"`
	Suggestion string             `json:"suggestion"`
	At         string             `json:"at"`
}

type novelState struct {
	Title        string                          `json:"title"`
	Total        int                             `json:"total"`
	Current      int                             `json:"current"`
	UpdatedAt    string                          `json:"updatedAt"`
	Reviews      map[int]novelChapterReview      `json:"reviews,omitempty"`      // 章节号 → 审稿结论(创作档案)
	StyleChoices []map[string]string             `json:"styleChoices,omitempty"` // 策划分析采纳的风格记录
}

func novelStateFile(proj string) string {
	return filepath.Join(proj, "novel_state.json")
}

func loadNovelState(proj string) novelState {
	var st novelState
	if b, err := os.ReadFile(novelStateFile(proj)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Reviews == nil {
		st.Reviews = map[int]novelChapterReview{}
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

// ---- 按书写锁:手动生成与后台续写并发写同一本书时的护栏(检查→生成→落盘→全本 整段互斥) ----
var (
	novelWriteMu    sync.Mutex
	novelWriteLocks = map[string]*sync.Mutex{}
)

func novelTitleLock(title string) *sync.Mutex {
	key := filepath.Base(novelProjDir(title))
	novelWriteMu.Lock()
	defer novelWriteMu.Unlock()
	m, ok := novelWriteLocks[key]
	if !ok {
		m = &sync.Mutex{}
		novelWriteLocks[key] = m
	}
	return m
}

// writeNovelChapter 写第 no 章(幂等:已存在返回 exists),供手动与后台自动续写共用。
// ctx 传入调用方上下文:自动续写停止时 cancel,在途 LLM 调用立即中断,不再烧 token/落盘。
func writeNovelChapter(ctx context.Context, title string, no int, cfg config.Settings, reviewNote string) (map[string]any, error) {
	unlock := novelTitleLock(title)
	unlock.Lock()
	defer unlock.Unlock()
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
				// rune 截断(字节截断会从汉字中间切断,发给 LLM 的是乱码)
				tr := []rune(t)
				if len(tr) > 120 {
					t = string(tr[len(tr)-120:])
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
	if reviewNote != "" {
		usr += "\n\n【重写模式】上一稿审稿未达标,意见如下,重写整章修正(直接输出修正后的完整正文,不要提及审稿):\n" + reviewNote
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
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
	// 字数校验与硬性要求一致(≥1280 字);低于要求按失败处理,避免残章混进正文
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &ch); err != nil || len([]rune(ch.Content)) < 1280 {
		return nil, fmt.Errorf("第%d章解析失败或字数不足(需≥1280字),请重试", no)
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
