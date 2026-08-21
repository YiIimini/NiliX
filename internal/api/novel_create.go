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

// novelRoot 小说库根目录(审计 H13):一律读可配置的 NovelRootDir(main 按 settings/自包含
// 解析注入),不再硬编码 C:\Mi\Ai\WorkBench\Novel——此前设置改 novel_root 后网页小说仍写
// 旧目录,fs 白名单/书架/转剧本全线脱节
func novelRoot() string {
	if strings.TrimSpace(NovelRootDir) != "" {
		return NovelRootDir
	}
	return `C:\Mi\Ai\WorkBench\novel`
}

var (
	novelTitleSan = regexp.MustCompile(`[\\/:*?"<>|]`)
	// reChapterNo 正文文件名章号(如 第001章_标题.md);提为包级,避免续写循环里每文件重编译
	reChapterNo = regexp.MustCompile(`第(\d{3})章`)
	// reNovelPlan 设定集「计划 N 章」标注
	reNovelPlan = regexp.MustCompile(`计划\s*(\d+)\s*章`)
)

func novelProjDir(title string) string {
	safe := novelTitleSan.ReplaceAllString(strings.TrimSpace(title), "")
	// 审计 F5:过滤非法字符后仍可能残留 ".."——直接拼接会把项目建/写到小说库根之外
	// (title=".." → novelRoot()/.. = 父目录),必须拒绝
	if safe == "" || safe == "." || safe == ".." || strings.Contains(safe, "..") {
		return filepath.Join(novelRoot(), "_非法书名_")
	}
	return filepath.Join(novelRoot(), safe)
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
	usr := fmt.Sprintf(`为小说《%s》设计全本设定与逐章大纲(写足写细,本书质量的决定性步骤)。
题材:%s;风格:%s;总章数:%d(每 7 章一卷)。
爽点主线固定:开局被欺负 → 中期反转 → 后期打脸 → 结局封神。
严格输出 JSON:
{"logline":"一句话故事",
"characters":[{"name":"","age":"年龄/身份","looks":"具体外貌细节(发型/脸型/特征伤疤等实物记忆点)","persona":"一句话人设+判词","arc":"核心目标与成长弧线","color":"代表色","prop":"具名道具","habit":"动作习惯","desc":"功能位(主角/伪善反派/助攻/工具人)+性格关键词,60字内","img_prompt":"写实电影级英文生图提示词:Cinematic film still, photorealistic + 年龄/东方特征 + 3个具体外貌记忆点(服饰/发饰/伤痕实物) + 神态 + 环境光 + 85mm lens, shallow depth of field, ultra detailed, 8k, movie poster quality;禁日漫风"}],
"world":"时代背景/势力对立(≥2股,与主角恩怨挂钩)/核心规则;必含可量化灵力等级表(全书战力对表),200字内",
"goldenfinger":"金手指:规则铁律 + 代价(无代价的开挂是垃圾),60字内",
"volumes":[{"no":1,"title":"卷名"}],
"chapters":[{"no":1,"title":"章节名","premise":"本章事件,50字内","conflict":"冲突与爽点,40字内","foreshadow":"本章埋/收的伏笔,20字内(无则空)"}]}
要求:characters 必含主角+伪善型反派+助攻(下属/知己/神秘大佬)+工具人(两三笔立住),全部主要角色逐个填全字段;
chapters 必须恰好 %d 条,no 从 1 连续递增;卷数=%d;首卷埋线、末卷收线,每卷末章是卷末高潮,章章有钩子。`,
		req.Title, nvOrDefault(req.Genre, "玄幻逆袭"),
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
			Age       string `json:"age"`
			Looks     string `json:"looks"`
			Persona   string `json:"persona"`
			Arc       string `json:"arc"`
			Color     string `json:"color"`
			Prop      string `json:"prop"`
			Habit     string `json:"habit"`
			Desc      string `json:"desc"`
			ImgPrompt string `json:"img_prompt"`
		} `json:"characters"`
		World        string `json:"world"`
		GoldenFinger string `json:"goldenfinger"`
		Volumes      []struct {
			No    int    `json:"no"`
			Title string `json:"title"`
		} `json:"volumes"`
		Chapters []struct {
			No         int    `json:"no"`
			Title      string `json:"title"`
			Premise    string `json:"premise"`
			Conflict   string `json:"conflict"`
			Foreshadow string `json:"foreshadow"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &plan); err != nil || len(plan.Chapters) == 0 {
		writeErr(w, http.StatusBadGateway, "大纲解析失败(模型输出非预期 JSON),请重试")
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s —— 设定集与大纲\n\n> 题材:%s | 风格:%s | 计划 %d 章(7章/卷)\n\n## 一句话故事\n%s\n\n## 世界观\n%s\n",
		req.Title, nvOrDefault(req.Genre, "玄幻逆袭"), nvOrDefault(req.Style, "热血爽文"), len(plan.Chapters), plan.Logline, plan.World)
	if plan.GoldenFinger != "" {
		fmt.Fprintf(&sb, "\n## 金手指\n%s\n", plan.GoldenFinger)
	}
	sb.WriteString("\n## 人物(九要素档案)\n")
	for _, c := range plan.Characters {
		fmt.Fprintf(&sb, "- **%s**(%s):%s\n  人设:%s | 成长:%s | 代表色:%s | 道具:%s | 习惯:%s",
			c.Name, nvOrDefault(c.Age, "-"), c.Desc, nvOrDefault(c.Persona, "-"), nvOrDefault(c.Arc, "-"),
			nvOrDefault(c.Color, "-"), nvOrDefault(c.Prop, "-"), nvOrDefault(c.Habit, "-"))
		if c.Looks != "" {
			fmt.Fprintf(&sb, "\n  外貌:%s", c.Looks)
		}
		if c.ImgPrompt != "" {
			fmt.Fprintf(&sb, "\n  生图:%s", c.ImgPrompt)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n## 卷结构\n")
	for _, v := range plan.Volumes {
		fmt.Fprintf(&sb, "- 卷%02d %s\n", v.No, v.Title)
	}
	sb.WriteString("\n## 逐章大纲\n")
	for _, ch := range plan.Chapters {
		fmt.Fprintf(&sb, "- 第%03d章 %s:%s|%s", ch.No, ch.Title, ch.Premise, ch.Conflict)
		if ch.Foreshadow != "" {
			fmt.Fprintf(&sb, "|伏笔:%s", ch.Foreshadow)
		}
		sb.WriteString("\n")
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
	// 审计 S2:score 缺失/越界按"评审无效"处理——LLM 输出缺 score 字段时零值 0.0,
	// 若不拦截会被调用方 `<70 删稿重写` 误删刚写完并过 QA 的好章
	if out.Score <= 0 || out.Score > 100 {
		return novelChapterReview{}, fmt.Errorf("评章分数缺失或非法(%.2f),评审无效", out.Score)
	}
	rv := novelChapterReview{Score: out.Score, Dims: out.Dims, Issues: out.Issues,
		Suggestion: out.Suggestion, At: time.Now().Format("2006-01-02 15:04")}
	// 审计 S4:novel_state 读写并入按书互斥锁——此前 auto 卷间并行(4 goroutine)同时
	// read-modify-write novel_state.json,后写覆盖先写,审稿结论互相丢失
	unlock := novelTitleLock(title)
	unlock.Lock()
	st := loadNovelState(proj)
	st.Reviews[no] = rv
	saveNovelState(proj, st)
	unlock.Unlock()
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
	wf := wfZImage(prompt, cfg.Render.ZImageUnet, cfg.Render.ZImageClip, cfg.Render.ZImageVae, seed, 768, 1024, "novel_cover", neg, "")
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

// appendToFullBook 追加一章进全本,按章号定位:已存在同号段则替换(返工重写后全本与正文一致,
// 不会保留旧坏稿也不会同章重复——审计 S3 此前用整文件 Contains 判重,标题变化即重复追加);
// 不存在则纯追加。目录由 rebuildFullBookTOC 在续写完成/手动触发时重建。
func appendToFullBook(proj, title string, no int, chTitle, content string) {
	fullDir := filepath.Join(proj, "全本")
	_ = os.MkdirAll(fullDir, 0755)
	fp := filepath.Join(fullDir, novelTitleSan.ReplaceAllString(title, "")+"·全本.md")
	block := fmt.Sprintf("\n\n## 第%03d章 %s\n\n%s\n", no, chTitle, content)
	marker := fmt.Sprintf("## 第%03d章", no)
	if b, err := os.ReadFile(fp); err == nil {
		s := string(b)
		if i := strings.Index(s, marker); i >= 0 {
			// 定位该章段:起点=i,终点=下一 "\n## 第" 段起点(或文末)
			start := i
			next := strings.Index(s[start+len(marker):], "\n## 第")
			end := len(s)
			if next >= 0 {
				end = start + len(marker) + next
			}
			// 替换该段(含结尾换行:block 自带 \n\n 前导,去掉被替换段的旧结尾换行避免空行堆积)
			newS := s[:start] + block + s[end:]
			if strings.HasPrefix(newS, "\n\n") {
				newS = newS[2:]
			}
			_ = os.WriteFile(fp, []byte(newS), 0644)
			return
		}
	}
	// 全本不存在或该章未收录:追加(保留 O(1) 追加语义)
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		_, _ = f.WriteString(fmt.Sprintf("# %s(全本)\n\n> 爽文一条龙 · 每章 ≥1280 字\n", title))
	}
	_, _ = f.WriteString(block)
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
	QA      string `json:"qa,omitempty"`    // 全量 QA(qa_check.py)摘要,完成时展示
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
	if t.QA != "" {
		out["qa"] = t.QA
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
	// auto 后台任务:panic 兜底(审计 S2——原 _ = recover() 吞 panic 无日志,改用 safeGo 记 crash)
	safeGo("novel-auto", nil, func() {
		defer func() {
			novelAutoMu.Lock()
			t.Running = false
			novelAutoMu.Unlock()
		}()
		proj := novelProjDir(req.Title)
		// 技能阶段3 并行写卷:未写章按卷分组,卷内串行(保证卷内衔接),卷间并行(卷与卷只靠
		// 设定集+大纲耦合,天然可并行)。并发 4(技能为 8 代理;对 LLM API 限速更稳)。
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
		total := 0
		if b, err := os.ReadFile(filepath.Join(proj, "设定集", "设定集与大纲.md")); err == nil {
			if m := reNovelPlan.FindStringSubmatch(string(b)); len(m) > 1 {
				if v, e := strconv.Atoi(m[1]); e == nil && v > 0 {
					total = v
				}
			}
		}
		// 审计 H14:无「计划 N 章」标注时不再默认狂写 600 章——按已写最大章号 + 一卷(7) 收敛,
		// 硬上限 200(旧书/大纲格式变化的书不再触发大规模 LLM 消耗与 429)
		if total <= 0 {
			maxNo := 0
			for n := range have {
				if n > maxNo {
					maxNo = n
				}
			}
			total = maxNo + 7
			if total > 200 {
				total = 200
			}
			if total < 14 {
				total = 14 // 至少一卷
			}
		}
		groups := map[int][]int{}
		for n := 1; n <= total; n++ {
			if !have[n] {
				vol := (n + 6) / 7
				groups[vol] = append(groups[vol], n)
			}
		}
		if len(groups) == 0 {
			novelAutoMu.Lock()
			t.Done = true
			novelAutoMu.Unlock()
			rebuildFullBookTOC(proj, req.Title)
			return
		}
		writeOne := func(no int) bool { // 返回 false=停止/失败(该卷终止)
			select {
			case <-t.stop:
				return false
			default:
			}
			res, err := writeNovelChapter(t.ctx, req.Title, no, cfg, "")
			novelAutoMu.Lock()
			if err == nil && !res["exists"].(bool) && no > t.Current {
				t.Current = no
			}
			novelAutoMu.Unlock()
			if err != nil {
				novelAutoMu.Lock()
				t.Err = err.Error()
				novelAutoMu.Unlock()
				return false
			}
			// Agent 化续写:自动审稿,低于 70 分带意见删稿重写一轮,重写稿复审归档
			if res["exists"] == false && cfg.LLM.APIKey != "" {
				if f, _ := findChapter(proj, no); f != "" {
					if content, cerr := os.ReadFile(f); cerr == nil {
						if rv, rerr := reviewChapterCore(cfg, proj, req.Title, no, string(content)); rerr == nil {
							if rv.Score < 70 {
								note := strings.Join(rv.Issues, ";")
								if rv.Suggestion != "" {
									if note != "" {
										note += ";"
									}
									note += rv.Suggestion
								}
								// 审计 S3:先写后删——此前先 os.Remove 再写,LLM 失败/停止即静默丢章。
								// 改为:rename 原子移走旧稿 → 写新稿 → 成功删备份 / 失败恢复旧稿并显式报错
								backup := f + ".bak-rewrite"
								renamed := os.Rename(f, backup) == nil
								res2, err2 := writeNovelChapter(t.ctx, req.Title, no, cfg, note)
								if err2 == nil && res2["exists"] == false {
									// 新稿落盘成功:清理备份
									_ = os.Remove(backup)
									if f2, _ := findChapter(proj, no); f2 != "" {
										if c2, e2 := os.ReadFile(f2); e2 == nil {
											_, _ = reviewChapterCore(cfg, proj, req.Title, no, string(c2)) // 重写稿复审(归档)
										}
									}
								} else if renamed {
									// 重写失败且旧稿已移走:恢复旧稿,不再静默吞错误
									_ = os.Rename(backup, f)
									novelAutoMu.Lock()
									if t.Err == "" {
										t.Err = fmt.Sprintf("审稿返工重写失败(第 %d 章): %v", no, err2)
									}
									novelAutoMu.Unlock()
								}
								// renamed==false(旧稿未移走):旧稿仍在原处,重写失败亦无损失
							}
						}
					}
				}
			}
			select {
			case <-t.stop:
				return false
			case <-time.After(2 * time.Second): // 卷内限速,避免把 LLM 打爆
			}
			return true
		}
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, nos := range groups {
			wg.Add(1)
			safeGo("novel-vol", nil, func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				for _, no := range nos {
					if !writeOne(no) {
						return
					}
				}
			})
		}
		wg.Wait()
		select {
		case <-t.stop:
			return
		default:
		}
		novelAutoMu.Lock()
		t.Done = true
		novelAutoMu.Unlock()
		rebuildFullBookTOC(proj, req.Title) // 全本目录一次性重建(逐章纯追加不维护目录)
		// 技能阶段4 全量 QA:qa_check.py 校验文件数/每章字数/禁用词/违规词/加粗,摘要供 status 展示
		if qa := runNovelQACheck(proj); qa != "" {
			lines := strings.Split(strings.TrimSpace(qa), "\n")
			if len(lines) > 6 {
				lines = lines[len(lines)-6:]
			}
			novelAutoMu.Lock()
			t.QA = strings.Join(lines, " | ")
			novelAutoMu.Unlock()
		}
	})
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
	entries, err := os.ReadDir(novelRoot())
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
		proj := filepath.Join(novelRoot(), title)
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

// handleNovelDelete 彻底删除小说项目(设定集/正文/素材/封面/全本/创作档案)。
// 安全护栏:目录必须位于小说库根目录之下;同时清理该书的自动连载后台任务。
func (s *Server) handleNovelDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeErr(w, http.StatusBadRequest, "缺少 title(书名)")
		return
	}
	safe := novelTitleSan.ReplaceAllString(title, "")
	dir := filepath.Join(novelRoot(), safe)
	rootClean := filepath.Clean(novelRoot())
	if filepath.Clean(dir) == rootClean || !strings.HasPrefix(filepath.Clean(dir), rootClean+string(filepath.Separator)) {
		writeErr(w, http.StatusForbidden, "目标不在小说库根目录内,拒绝删除")
		return
	}
	if !dirExists(dir) {
		writeErr(w, http.StatusNotFound, "小说不存在: "+safe)
		return
	}
	// 清理自动连载任务(停止在途 LLM 调用)
	dirKey := filepath.Base(dir)
	novelAutoMu.Lock()
	if t, ok := novelAutoTasks[dirKey]; ok {
		if t.Running {
			t.Running = false
			t.cancel()
			close(t.stop)
		}
		delete(novelAutoTasks, dirKey)
	}
	novelAutoMu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": dir})
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
	// 原子写(审计 S4):temp+rename,崩溃不留半写文件
	tmp := novelStateFile(proj) + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err == nil {
		_ = os.Rename(tmp, novelStateFile(proj))
	} else {
		_ = os.Remove(tmp)
	}
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
		prevTail = "(上一章尚未生成(并行写卷中),按设定集与大纲衔接本卷剧情直接开写)"
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
	// shuangwen-novel 技能规范:章节写作规范全文 + 违规词库硬禁词(动笔前必读,写作/QA 两关都生效)
	sys := "你是爽文小说写手。要求:正文口语化短句、去AI味;场景/情绪具体;每章结尾留钩子;不要小标题、不要总结。只输出 JSON。"
	if spec := novelWritingSpec(); spec != "" {
		sys += "\n\n【章节写作规范(动笔前通读,逐条遵守)】\n" + spec
	}
	if hard := novelHardBanned(); len(hard) > 0 {
		sys += "\n\n【内容安全违规词(硬禁,正文中绝不出现)】" + strings.Join(hard, "、")
	}
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
	// 机械 QA(技能 qa_check 同口径):中文字数/去AI味禁用词/违规词硬禁/加粗;命中自动重写一轮
	parseOK := json.Unmarshal([]byte(stripJSONFence(raw)), &ch) == nil
	var qaProblems []string
	if !parseOK {
		qaProblems = []string{"输出解析失败(非预期 JSON)"}
	} else {
		qaProblems = novelChapterQA(ch.Content)
	}
	if len(qaProblems) > 0 {
		// 机械校验未过 → 自动重写一轮(意见与审稿意见合并注入);仍不过才报错人工重试
		note := "机械校验未过:" + strings.Join(qaProblems, ";")
		if reviewNote != "" {
			note = reviewNote + ";" + note
		}
		raw2, err2 := llm.Chat(ctx, []backend.ChatMessage{
			{Role: "system", Content: sys},
			{Role: "user", Content: usr + "\n\n【重写模式】上一稿未达标,意见如下,重写整章修正(直接输出修正后完整正文,不要提及审稿):\n" + note}}, 8000, 0.85)
		if err2 == nil {
			_ = json.Unmarshal([]byte(stripJSONFence(raw2)), &ch)
		}
		if novelChapterQA(ch.Content) != nil {
			return nil, fmt.Errorf("第%d章两轮均未过机械校验(%s),请手动重试", no, truncate(note, 120))
		}
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
				// 审计 H4:卷名消毒——LLM 卷名含 :?/<>|* 等 Windows 保留字符时
				// MkdirAll 整卷失败;此前章节名消毒了卷名漏网
				vt := novelTitleSan.ReplaceAllString(vtitle, "")
				if vt == "" {
					vt = "卷" + strconv.Itoa(vol)
				}
				volName = fmt.Sprintf("卷%02d_%s", vol, vt)
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
