package api

// 漫剧管线 Go 实现:方案(DeepSeek LLM 直出)→ 资产(SDXL/Z-Image)→ 预编码(Qwen3-VL 条件缓存)
// → 渲染(H3 + Turbo LoRA + MotionContext 接缝)→ 质检 + 合成(venv PyAV 辅助)。
// 与原 Python 管线的日志格式契约保持一致(━━━ 阶段 X ━━━ / [i/n] 镜头),前端进度/流程图无需改动。

import (
	"bufio"
	"crypto/md5"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nilix/internal/agent"
)

//go:embed scripts/manju_media.py
var manjuMediaPy string

// ---- 项目上下文 ----

type manjuCtx struct {
	configPath   string
	cfg          map[string]any
	style        string
	R            map[string]any
	P            map[string]any
	project      string // 项目名(目录名)
	episode      string
	chapters     string
	only         string
	novel        string // 实际使用的小说文件(前端覆盖优先)
	auto         bool   // 全本自动分集模式:该集章节由引擎按内容量切分(方案复用校验用)
	llm          *manjuLLM
	comfy        *comfyClient
	comfyOutput  string
	comfyInput   string
	sharedModels string
	assetsDir    string
	analysisDir  string
	clipsDir     string // <workdir>/clips
	workdir      string
	steps        int // 采样步数(turbo_lora 存在则用 turbo_steps)
	w, h         int
	fps          int
	seed         int
	minSec       int
	maxSec       int
	seedPolicy   string  // seed 重试策略:fixed(默认全剧固定)/increment(重试 seed+N)/random(重试换随机)
	resTier      string  // 分辨率档位(空/custom=手动宽高;draft/standard/fhd 见 manjuResTiers)
	draftJudge   bool    // 智能模式草稿预审:审片返工轮用缩放分辨率草稿,全部通过后全分辨率定稿重渲
	draftScale   float64 // 草稿缩放(0.2-0.95,默认 0.5;0.5 ≈ 1/4 像素量)
	forceAttempt int     // 定点返工等外部路径传入的重试序号(seed 策略用它换 seed;0=首渲)
	visionOnce   sync.Once
	vision       *agent.VisionClient // 每 run 共享(粘性降级状态跨镜头保留)
}

func manjuToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func newManjuCtx(configPath, episode, chapters, only, novel string) (*manjuCtx, error) {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return nil, err
	}
	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
	}
	ctx := &manjuCtx{
		configPath:   configPath,
		cfg:          cfg,
		style:        str(cfg["style"]),
		R:            R,
		P:            P,
		project:      filepath.Base(filepath.Dir(configPath)),
		episode:      orDefault(episode, str(R["episode"])),
		chapters:     orDefault(chapters, str(R["chapters"])),
		only:         only,
		novel:        str(P["novel"]),
		comfyOutput:  str(P["comfy_output"]),
		comfyInput:   str(P["comfy_input"]),
		sharedModels: filepath.Join(comfyShared, "models"),
		workdir:      str(P["workdir"]),
	}
	if ctx.episode == "" {
		ctx.episode = "EP01"
	}
	if novel != "" {
		if fileExists(novel) {
			ctx.novel = novel
		}
	}
	ctx.assetsDir = filepath.Join(ctx.workdir, "assets")
	ctx.analysisDir = filepath.Join(ctx.workdir, "analysis")
	ctx.clipsDir = filepath.Join(ctx.workdir, "clips")
	ctx.llm = manjuLLMFromCfg(cfg)
	ctx.llm.onUsage = func(model string, u agent.Usage) { manjuStatsAdd(ctx.project, model, u) }
	ctx.comfy = newComfyClient(str(R["comfy_url"]))
	if n, ok := manjuToInt(R["width"]); ok && n > 0 {
		ctx.w = n
	} else {
		ctx.w = 768
	}
	if n, ok := manjuToInt(R["height"]); ok && n > 0 {
		ctx.h = n
	} else {
		ctx.h = 1344
	}
	ctx.fps = 24
	if n, ok := manjuToInt(R["fps"]); ok && n > 0 {
		ctx.fps = n
	}
	ctx.seed = 1688
	if n, ok := manjuToInt(R["seed"]); ok {
		ctx.seed = n
	}
	ctx.minSec, ctx.maxSec = 4, 12
	if n, ok := manjuToInt(R["min_shot_seconds"]); ok && n > 0 {
		ctx.minSec = n
	}
	if n, ok := manjuToInt(R["max_shot_seconds"]); ok && n > 0 {
		ctx.maxSec = n
	}
	ctx.steps = 20
	if n, ok := manjuToInt(R["steps"]); ok && n > 0 {
		ctx.steps = n
	}
	if str(R["turbo_lora"]) != "" {
		// Turbo 步数:用户显式配置优先,缺省按 LoRA 类型参数表(旧系 8 步,Kijai 4 步版 4 步)
		if n, ok := manjuToInt(R["turbo_steps"]); ok && n > 0 {
			ctx.steps = n
		} else {
			ctx.steps = turboLoRASpecOf(str(R["turbo_lora"])).Steps
		}
	}
	// seed 重试策略(fixed 默认;非法值回退 fixed)
	ctx.seedPolicy = "fixed"
	if s := str(R["seed_policy"]); s == "increment" || s == "random" {
		ctx.seedPolicy = s
	}
	// 分辨率档位:非 custom 时覆盖手动宽高(等比缩放对齐 32)
	ctx.resTier = str(R["res_tier"])
	if t := ctx.resTier; t != "" && t != "custom" {
		if tw, th, ok := manjuResTierDims(t, ctx.w, ctx.h); ok {
			ctx.w, ctx.h = tw, th
		}
	}
	ctx.draftScale = 0.5
	if v, ok := manjuToFloat(R["draft_scale"]); ok && v >= 0.2 && v <= 0.95 {
		ctx.draftScale = v
	}
	ctx.draftJudge, _ = R["draft_judge"].(bool)
	return ctx, nil
}

// seedFor 重试 seed 策略:fixed=恒定(跨镜一致基线);increment=第 N 次重试 seed+N;
// random=重试换新随机(首渲仍用配置 seed 保持全剧基线)。attempt=0 表示首次渲染。
func (ctx *manjuCtx) seedFor(attempt int) int {
	switch ctx.seedPolicy {
	case "increment":
		return ctx.seed + attempt
	case "random":
		if attempt > 0 {
			return randSeed()
		}
	}
	return ctx.seed
}

// draftDims 草稿预审分辨率:定稿画幅 × draftScale 对齐 32(判分与分辨率弱相关,
// 0.5 缩放的像素量约为定稿 1/4,审片返工轮 GPU 时间等比下降)
func (ctx *manjuCtx) draftDims() (int, int) {
	sc := ctx.draftScale
	if sc <= 0 || sc >= 1 {
		sc = 0.5
	}
	return manjuAlign32(int(float64(ctx.w)*sc + 0.5)), manjuAlign32(int(float64(ctx.h)*sc + 0.5))
}

// draftDir 草稿预审产物目录(clips/<ep>/_draft,与定稿同集隔离;合成/集清单不读,
// 定稿轮完成后整目录清除)
func (ctx *manjuCtx) draftDir() string {
	return filepath.Join(ctx.clipsDir, ctx.episode, "_draft")
}

// ---- 运行日志(写入内存状态 + run.log,检测阶段切换通知) ----

type manjuLogger struct {
	state     *manjuTask
	file      *os.File
	mu        sync.Mutex
	lastStage string
	proj, ep  string
}

func newManjuLogger(state *manjuTask, file *os.File, proj, ep string) *manjuLogger {
	return &manjuLogger{state: state, file: file, proj: proj, ep: ep}
}

func (l *manjuLogger) logf(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.state
	state.mu.Lock()
	state.log += "\n" + line
	if len(state.log) > 300000 {
		state.log = state.log[len(state.log)-200000:]
	}
	state.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line + "\n")
	}
	if m := reManjuStage.FindStringSubmatch(line); m != nil {
		st := m[1]
		if st != l.lastStage {
			l.lastStage = st
			name := manjuStageName[st]
			if name == "" {
				name = st
			}
			manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · 进入阶段: %s", l.proj, l.ep, name))
		}
	}
}

func (l *manjuLogger) logStage(key string) {
	l.logf("━━━ 阶段 " + key + " ━━━")
}

func (l *manjuLogger) stopped() bool {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.stopped
}

// ---- 运行主循环(替代原 manjuWorker 的 Python 子进程) ----

// manjuPipelineRun 执行一个或多个阶段,返回退出码(0 成功)
func manjuPipelineRun(ctx *manjuCtx, phase string, lg *manjuLogger) int {
	stages := []string{"plan", "assets", "encode", "render", "qc", "assemble"}
	if phase != "all" {
		stages = []string{phase}
	}
	for _, st := range stages {
		if lg.stopped() {
			lg.logf("⏹ 任务已被手动停止。已完成产物保留,可直接再点同按钮续跑。")
			return 0
		}
		lg.logStage(st)
		var err error
		switch st {
		case "plan":
			err = stagePlan(ctx, lg)
		case "assets":
			err = stageAssets(ctx, lg)
		case "encode":
			err = stageEncode(ctx, lg)
		case "render":
			err = stageRender(ctx, lg)
		case "qc":
			err = stageQC(ctx, lg)
		case "assemble":
			err = stageAssemble(ctx, lg)
		}
		if err != nil {
			lg.logf("❌ 阶段 " + st + " 失败: " + err.Error())
			return 1
		}
	}
	return 0
}

// manjuFinish 收尾:更新内存/磁盘状态(项目目录 run_state.json) + 结束通知
func manjuFinish(rc int) {
	state := manjuState
	state.mu.Lock()
	if state.stopped {
		state.log += "\n\n⏹ 任务已被手动停止。已完成镜头保留,可直接再点同按钮续跑。"
		rc = 0
	}
	state.rc = &rc
	state.done = true
	state.running = false
	state.cmd = nil
	proj, ep := state.project, state.episode
	logTail := state.log
	if len(logTail) > 30000 {
		logTail = logTail[len(logTail)-30000:]
	}
	elapsedSec := state.baseElapsed + int(time.Since(state.started).Seconds())
	state.elapsed = elapsedSec // 冻结总耗时(含续跑累加基数),供 status 接口在结束后展示
	info := parseManjuProgress(state.log, false)
	stopped := state.stopped
	state.mu.Unlock()
	writeManjuDiskState(proj, &manjuDiskState{
		Running: false, Stage: "", Done: true, RC: &rc, Stopped: stopped, StartedAt: 0, PID: 0,
		Episode: ep, CurrentStage: info.CurrentStage, StageIdx: info.StageIdx,
		ShotCur: info.ShotCur, ShotTotal: info.ShotTotal,
		ElapsedSec: elapsedSec, LogTail: logTail, UpdatedAt: time.Now().Unix(),
	})
	if stopped {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ⏹ 已手动停止", proj, ep))
	} else if rc == 0 {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ✅ 全部完成", proj, ep))
	} else {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ❌ 失败(退出码 %d)", proj, ep, rc))
	}
}

// ---- 章节 ----

// parseChapterSet 解析 "1-3" / "1,2,3" / "1-999"(全本=全部)
func parseChapterSet(s string, all []int) map[int]bool {
	out := map[int]bool{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			a, _ := strconv.Atoi(strings.TrimSpace(part[:i]))
			b, _ := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if a == 0 {
				a = 1
			}
			if b > 900 { // 全本
				for _, n := range all {
					out[n] = true
				}
				continue
			}
			if b < a {
				a, b = b, a
			}
			for n := a; n <= b; n++ {
				out[n] = true
			}
		} else if n, err := strconv.Atoi(part); err == nil {
			out[n] = true
		}
	}
	return out
}

// extractChapters 从小说文本抽取指定章节内容(^# 第N章 标题切分,与旧管线同款)
func extractChapters(novelText string, nums map[int]bool) []string {
	matches := reManjuChapter.FindAllStringSubmatchIndex(novelText, -1)
	var out []string
	for i, m := range matches {
		n, _ := strconv.Atoi(novelText[m[2]:m[3]])
		if !nums[n] {
			continue
		}
		end := len(novelText)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		out = append(out, novelText[m[0]:end])
	}
	return out
}

func allChapterNums(novelText string) []int {
	ms := reManjuChapter.FindAllStringSubmatch(novelText, -1)
	out := []int{}
	for _, m := range ms {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (ctx *manjuCtx) chapterText() (string, error) {
	data, err := os.ReadFile(ctx.novel)
	if err != nil {
		return "", fmt.Errorf("读小说失败: %w", err)
	}
	text := string(data)
	all := allChapterNums(text)
	if len(all) == 0 {
		// 无 # 第N章 结构(如 duanju 短剧全案):整篇作为单一素材直出方案
		return text, nil
	}
	nums := parseChapterSet(ctx.chapters, all)
	if len(nums) == 0 {
		return "", fmt.Errorf("章节范围无效: %s（该文件共 %d 章）", ctx.chapters, len(all))
	}
	chs := extractChapters(text, nums)
	if len(chs) == 0 {
		return "", fmt.Errorf("章节不存在: %s（该文件共 %d 章）", ctx.chapters, len(all))
	}
	return strings.Join(chs, "\n\n"), nil
}

// ---- 全本自动分段分集 ----

// chapterRangeFullBook 章节范围是否带"全本"标记(任一端点 >900,与 parseChapterSet 同语义)
func chapterRangeFullBook(s string) bool {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "-"); i > 0 {
			if b, err := strconv.Atoi(strings.TrimSpace(part[i+1:])); err == nil && b > 900 {
				return true
			}
		}
	}
	return false
}

// manjuEpSeg 自动分集的一段:集号 + 该集章节范围
type manjuEpSeg struct {
	Episode  string
	Chapters string
}

// manjuEpCharBudget 每集方案字数预算:低于方案生成的 20000 字截断,留余量(≈2-3 章)
const manjuEpCharBudget = 18000

// reChapterFile 分章文件名:第001章_标题.md
var reChapterFile = regexp.MustCompile(`第\s*(\d+)\s*章`)

// chapterNumFromName 从分章文件名解析章节号
func chapterNumFromName(name string) (int, bool) {
	m := reChapterFile.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// findChapterDir 在小说目录找 正文 分章子目录(01_正文/正文 等)
func findChapterDir(novelDir string) string {
	if novelDir == "" {
		return ""
	}
	entries, err := os.ReadDir(novelDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), "正文") {
			return filepath.Join(novelDir, e.Name())
		}
	}
	return ""
}

// reVolDir 卷目录名:卷一_逐出家门 / 卷12_xxx(卷号取 卷 后到下划线前)
var reVolDir = regexp.MustCompile(`^卷([一二三四五六七八九十百\d]+)[_\s]`)

// cnNumToInt 中文数字转整数(支持 一~十/百 及组合:十二=12、二十五=25)
func cnNumToInt(s string) int {
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10, '百': 100}
	total, cur := 0, 0
	for _, r := range s {
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		} else if r == '百' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 100
			cur = 0
		} else if d, ok := digits[r]; ok {
			cur = d
		}
	}
	return total + cur
}

// novelRootDir 小说项目根目录:优先 config 的 novel_dir;缺省时小说文件位于 全本/正文 子目录则向上取一级
func (ctx *manjuCtx) novelRootDir() string {
	if d := strings.TrimSpace(str(ctx.P["novel_dir"])); d != "" {
		return d
	}
	d := filepath.Dir(ctx.novel)
	base := strings.ToLower(filepath.Base(d))
	if base == "全本" || strings.Contains(base, "正文") {
		return filepath.Dir(d)
	}
	return d
}

// volumeEpisodes 按卷分集:正文/<卷X_标题>/ 卷目录下每卷一个集(卷内章节文件定章节范围,卷序定集号)。
// 返回 (集列表, 是否检测到卷结构);未检测到卷结构时回退字数打包。
func (ctx *manjuCtx) volumeEpisodes() ([]manjuEpSeg, bool) {
	dir := findChapterDir(ctx.novelRootDir())
	if dir == "" {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	type vol struct {
		no  int
		chs []int
	}
	var vols []vol
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := reVolDir.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		no := cnNumToInt(m[1])
		if no <= 0 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				no = n
			}
		}
		if no <= 0 {
			continue
		}
		var chs []int
		files, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
				continue
			}
			if n, ok := chapterNumFromName(f.Name()); ok {
				chs = append(chs, n)
			}
		}
		if len(chs) == 0 {
			continue
		}
		sort.Ints(chs)
		vols = append(vols, vol{no: no, chs: chs})
	}
	if len(vols) == 0 {
		return nil, false
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].no < vols[j].no })
	out := make([]manjuEpSeg, 0, len(vols))
	for i, v := range vols {
		a, b := v.chs[0], v.chs[len(v.chs)-1]
		r := strconv.Itoa(a)
		if b != a {
			r += "-" + strconv.Itoa(b)
		}
		out = append(out, manjuEpSeg{Episode: fmt.Sprintf("EP%02d", i+1), Chapters: r})
	}
	return out, true
}

// chapterEntries 收集小说全部章节(章节号+字数)。优先全本文本按 # 第N章 切分
// (与方案生成同源,字数统计最准);全本无章节结构时回退 01_正文 分章文件。
func (ctx *manjuCtx) chapterEntries() ([]struct{ n, chars int }, error) {
	out := []struct{ n, chars int }{}
	data, err := os.ReadFile(ctx.novel)
	if err != nil {
		return nil, err
	}
	text := string(data)
	ms := reManjuChapter.FindAllStringSubmatchIndex(text, -1)
	for i, m := range ms {
		n, _ := strconv.Atoi(text[m[2]:m[3]])
		end := len(text)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		out = append(out, struct{ n, chars int }{n: n, chars: len([]rune(text[m[0]:end]))})
	}
	if len(out) >= 2 {
		return out, nil
	}
	out = out[:0]
	if dir := findChapterDir(ctx.novelRootDir()); dir != "" {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					continue
				}
				n, ok := chapterNumFromName(e.Name())
				if !ok {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
					out = append(out, struct{ n, chars int }{n: n, chars: len([]rune(string(b)))})
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	}
	return out, nil
}

// manjuAutoEpisodes 全本时自动分段分集:
// 优先按卷划分(正文/<卷X_标题>/ 每卷一集,如吞天废子 8 卷 → EP01..EP08);
// 无卷结构时按内容量贪心打包(章节顺序,每集累计字数 ≤ 预算,至少 1 章)。
// 两种规则都是确定性分段,同一小说每次结果一致,可安全续跑。
func manjuAutoEpisodes(ctx *manjuCtx, chapters string) ([]manjuEpSeg, error) {
	if !chapterRangeFullBook(chapters) {
		return nil, nil
	}
	if segs, ok := ctx.volumeEpisodes(); ok {
		return segs, nil
	}
	chs, err := ctx.chapterEntries()
	if err != nil {
		return nil, err
	}
	if len(chs) == 0 {
		return nil, nil // 无章节结构:整篇一集
	}
	segFrom := func(idx int, seg []struct{ n, chars int }) manjuEpSeg {
		a, b := seg[0].n, seg[len(seg)-1].n
		r := strconv.Itoa(a)
		if b != a {
			r += "-" + strconv.Itoa(b)
		}
		return manjuEpSeg{Episode: fmt.Sprintf("EP%02d", idx+1), Chapters: r}
	}
	var out []manjuEpSeg
	curStart, curChars := 0, 0
	for i, c := range chs {
		if i > curStart && curChars+c.chars > manjuEpCharBudget {
			out = append(out, segFrom(len(out), chs[curStart:i]))
			curStart, curChars = i, 0
		}
		curChars += c.chars
	}
	if curStart < len(chs) {
		out = append(out, segFrom(len(out), chs[curStart:]))
	}
	return out, nil
}

// ---- 方案(LLM 直出人物/场景/分镜 + 逐镜 H3 提示词) ----

type manjuShot struct {
	ID         int
	Scene      string
	Characters []string
	ShotSize   string
	Camera     string
	Action     string
	Dialogue   string
	Narration  string
	Duration   int
	H3Prompt   string
	TakeTail   bool        // 多切点长镜的内镜:不独立渲染,由组头一次生成覆盖
	TakeGroup  []manjuShot // 多切点长镜组头携带整组(含自身;单镜为空)
}

// loadPlan 读取 analysis/<ep>_direct_plan.json,规范化镜头字段
func (ctx *manjuCtx) loadPlan() (map[string]any, []manjuShot, error) {
	p := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	var plan map[string]any
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, nil, fmt.Errorf("方案解析失败: %w", err)
	}
	shots, err := planShots(plan)
	if err != nil {
		return nil, nil, err
	}
	return plan, shots, nil
}

func planShots(plan map[string]any) ([]manjuShot, error) {
	var out []manjuShot
	arr, _ := plan["shots"].([]any)
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		s := manjuShot{
			Scene:     str(m["scene"]),
			ShotSize:  str(m["shot_size"]),
			Camera:    str(m["camera"]),
			Action:    str(m["action"]),
			Dialogue:  str(m["dialogue"]),
			Narration: str(m["narration"]),
			H3Prompt:  str(m["h3_prompt"]),
		}
		s.ID, _ = manjuToInt(m["shot_id"])
		s.Duration = 5
		if n, ok := manjuToInt(m["duration"]); ok && n > 0 {
			s.Duration = n
		}
		if arr2, ok := m["characters"].([]any); ok {
			for _, c := range arr2 {
				if cs := str(c); cs != "" {
					s.Characters = append(s.Characters, cs)
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// manjuConciseSuffix 方案输出超长被截断时的精简约束(仅重试时追加,不改变正常生成)
const manjuConciseSuffix = `

【输出体积硬约束(前次输出被截断,本次必须精简)】:
- characters 不超过 4 个、scenes 不超过 4 个、shots 不超过 16 个
- appearance/costume/image_prompt 每项不超过 40 字,scene 的 description 不超过 30 字
- 所有描述压缩到"可渲染"即可,禁止铺陈展开;整个 JSON 输出控制在 6000 tokens 以内`

// ensurePlan 保证方案存在(有则复用,无则 LLM 直出),同时写 _characters.json(抽卡用)
// 复用校验:方案记录了章节范围(plan.chapters)且与本次请求一致才复用;
// 旧方案(无分段标记)在普通模式复用(兼容不重渲),在全本自动分集模式视为过期重新生成;
// 章节范围变化导致重新生成时,清空该集旧镜头/缓存,避免按旧内容复用。
// 生成失败(输出被截断/非 JSON)时追加精简约束重试一次。
func (ctx *manjuCtx) ensurePlan(lg *manjuLogger) (map[string]any, error) {
	plan, _, err := ctx.loadPlan()
	if err == nil {
		planC := str(plan["chapters"])
		legacy := planC == ""                               // 旧方案无分段标记
		reuse := planC == ctx.chapters                      // 范围一致
		reuse = reuse || (legacy && !ctx.auto)              // 旧方案普通模式兼容复用
		reuse = reuse || chapterRangeFullBook(ctx.chapters) // 全本请求:沿用该集既有方案
		// 小说内容指纹:改过正文必须重新生成方案,否则渲染的还是旧剧情
		// (「改了小说但视频对不上」的头号原因);旧方案无指纹记录则不强制,兼容老项目
		fp := ctx.novelFingerprint()
		planFp := str(plan["novel_fp"])
		if reuse && fp != "" && planFp != "" && planFp != fp {
			lg.logf("⚠️ 小说正文已修改(" + planFp + " → " + fp + ")，方案过期，重新生成并清空该集旧产物")
			reuse = false
			ctx.clearEpisodeArtifacts(lg)
		}
		if reuse {
			ctx.writeCharactersJSON(plan)
			lg.logf("♻️  复用方案: " + filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"))
			return plan, nil
		}
		if legacy {
			lg.logf("⚠️ 集 " + ctx.episode + " 旧方案无分集标记,按自动分集重新生成并清空该集旧产物")
		} else {
			lg.logf("⚠️ 集 " + ctx.episode + " 方案章节范围已变(" + planC + " → " + ctx.chapters + "),重新生成并清空该集旧产物")
		}
		ctx.clearEpisodeArtifacts(lg)
	}
	chapterText, cerr := ctx.chapterText()
	if cerr != nil {
		return nil, cerr
	}
	runes := len([]rune(chapterText))
	lg.logf("📖 章节 " + ctx.chapters + "（" + strconv.Itoa(runes) + " 字）")
	if runes > 20000 {
		// 超长静默截断会让超出部分的剧情根本没进方案,视频自然对不上——必须明示
		lg.logf("  ⚠️ 内容 " + strconv.Itoa(runes) + " 字超出 20000 字上限,超出部分可能未被方案覆盖(建议缩小章节范围或分集)")
	}
	lg.logf("🤖 大模型直出 人物/场景/分镜...")
	sys := manjuDirectSystem(ctx.cfg, ctx.style)
	// 生成并校验:输出被截断/非 JSON/无镜头都视为无效,追加精简约束重试一次
	plan, err = ctx.llm.chatJSON(sys, truncate(chapterText, 20000), 0.4)
	invalid := err != nil || len(anyArr(plan["shots"])) == 0
	if invalid {
		lg.logf("  ⚠️ 方案生成无效(输出过长/非 JSON/无镜头)，追加精简约束重试一次...")
		plan, err = ctx.llm.chatJSON(sys+manjuConciseSuffix, truncate(chapterText, 20000), 0.4)
		if err != nil || len(anyArr(plan["shots"])) == 0 {
			if err == nil {
				err = fmt.Errorf("方案生成后仍无镜头")
			}
			return nil, fmt.Errorf("方案生成失败(重试后): %w", err)
		}
		lg.logf("  ✅ 精简重试成功")
	}
	if err := ctx.writePlan(plan); err != nil {
		return nil, err
	}
	ctx.writeCharactersJSON(plan)
	chars := len(anyArr(plan["characters"]))
	scenes := len(anyArr(plan["scenes"]))
	shots, _ := planShots(plan)
	lg.logf(fmt.Sprintf("  ✅ %d 角色 / %d 场景 / %d 镜头", chars, scenes, len(shots)))
	return plan, nil
}

// novelFingerprint 小说正文指纹(大小+mtime):方案复用校验的依据,
// 正文变化后旧方案视为过期,强制重新生成,保证视频内容与小说同步。
func (ctx *manjuCtx) novelFingerprint() string {
	if ctx.novel == "" {
		return ""
	}
	st, err := os.Stat(ctx.novel)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d|%d", st.Size(), st.ModTime().Unix())
}

func anyArr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// manjuClearProject 重跑模式:清空项目全部渲染产物(方案/镜头/成片/条件缓存),资产(定妆照/场景图)保留。
// 缓存按项目名前缀匹配,不误删其他项目。
func manjuClearProject(ctx *manjuCtx, lg *manjuLogger) {
	// 方案与逐镜提示词(全部集)
	if entries, err := os.ReadDir(ctx.analysisDir); err == nil {
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Remove(filepath.Join(ctx.analysisDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空方案文件 " + strconv.Itoa(n) + " 个")
		}
	}
	// 镜头与成片
	removed := 0
	if entries, err := os.ReadDir(ctx.clipsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				epDir := filepath.Join(ctx.clipsDir, e.Name())
				if fs, err := os.ReadDir(epDir); err == nil {
					for _, f := range fs {
						if !f.IsDir() {
							_ = os.Remove(filepath.Join(epDir, f.Name()))
							removed++
						}
					}
				}
			} else {
				_ = os.Remove(filepath.Join(ctx.clipsDir, e.Name()))
				removed++
			}
		}
	}
	if entries, err := os.ReadDir(ctx.workdir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), "_成片.mp4") {
				_ = os.Remove(filepath.Join(ctx.workdir, e.Name()))
				removed++
			}
		}
	}
	if removed > 0 {
		lg.logf("  🧹 已清空镜头/成片 " + strconv.Itoa(removed) + " 个")
	}
	// 条件缓存(项目前缀)
	prefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_"
	condDir := filepath.Join(ctx.sharedModels, "conditioning")
	if entries, err := os.ReadDir(condDir); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				_ = os.Remove(filepath.Join(condDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空条件缓存 " + strconv.Itoa(n) + " 个")
		}
	}
}

// clearEpisodeArtifacts 清空该集旧产物:镜头 mp4 + 条件缓存(缓存名带项目_集号前缀,不误删其他项目)。
// 方案章节范围变化后旧镜头/缓存与新方案内容不符,必须清除让渲染按新方案重做。
func (ctx *manjuCtx) clearEpisodeArtifacts(lg *manjuLogger) {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if entries, err := os.ReadDir(clipsEp); err == nil {
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Remove(filepath.Join(clipsEp, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空该集旧镜头 " + strconv.Itoa(n) + " 个: " + clipsEp)
		}
	}
	prefix := reNonWord.ReplaceAllString(ctx.project, "_") + "_" + reNonWord.ReplaceAllString(ctx.episode, "_") + "_a" + ctx.assetsFingerprint() + "_s"
	condDir := filepath.Join(ctx.sharedModels, "conditioning")
	if entries, err := os.ReadDir(condDir); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				_ = os.Remove(filepath.Join(condDir, e.Name()))
				n++
			}
		}
		if n > 0 {
			lg.logf("  🧹 已清空该集旧条件缓存 " + strconv.Itoa(n) + " 个(将自动重新编码)")
		}
	}
}

func (ctx *manjuCtx) writePlan(plan map[string]any) error {
	if err := os.MkdirAll(ctx.analysisDir, 0755); err != nil {
		return err
	}
	// 记录方案章节范围(复用校验依据);旧方案复用时不覆盖已有标记,保持其来源
	if str(plan["chapters"]) == "" {
		plan["chapters"] = ctx.chapters
	}
	if str(plan["episode"]) == "" {
		plan["episode"] = ctx.episode
	}
	// 记录小说正文指纹(复用校验依据:改过正文 → 方案过期强制重生成)
	if str(plan["novel_fp"]) == "" {
		if fp := ctx.novelFingerprint(); fp != "" {
			plan["novel_fp"] = fp
		}
	}
	data, _ := json.MarshalIndent(plan, "", "  ")
	return os.WriteFile(filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"), data, 0644)
}

// writeCharactersJSON 抽卡/主页角色列表(无 shots)
func (ctx *manjuCtx) writeCharactersJSON(plan map[string]any) {
	_ = os.MkdirAll(ctx.analysisDir, 0755)
	chars, _ := plan["characters"].([]any)
	scenes, _ := plan["scenes"].([]any)
	out := map[string]any{"characters": chars, "scenes": scenes}
	data, _ := json.MarshalIndent(out, "", "  ")
	_ = os.WriteFile(filepath.Join(ctx.analysisDir, ctx.episode+"_characters.json"), data, 0644)
}

// genShotPrompts 逐镜补全 h3_prompt(缺失才生成,进度落盘 _shots_prompts.json)
func (ctx *manjuCtx) genShotPrompts(plan map[string]any, shots []manjuShot, lg *manjuLogger) error {
	promptsPath := filepath.Join(ctx.analysisDir, ctx.episode+"_shots_prompts.json")
	prompts := map[string]string{}
	if b, err := os.ReadFile(promptsPath); err == nil {
		var pm map[string]any
		if json.Unmarshal(b, &pm) == nil {
			for k, v := range pm {
				prompts[k] = str(v)
			}
		}
	}
	need := false
	for _, s := range shots {
		if s.TakeTail {
			continue // 长镜内镜不独立生成提示词(由组头多切点提示词覆盖)
		}
		if s.H3Prompt == "" && prompts[strconv.Itoa(s.ID)] == "" {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	lg.logf("🤖 逐镜直出完整 H3 提示词（六段式/三段式）...")
	chars, _ := plan["characters"].([]any)
	scenes, _ := plan["scenes"].([]any)
	charMap := map[string]map[string]any{}
	for _, c := range chars {
		if m, ok := c.(map[string]any); ok {
			charMap[str(m["id"])] = m
		}
	}
	sceneMap := map[string]map[string]any{}
	for _, s := range scenes {
		if m, ok := s.(map[string]any); ok {
			sceneMap[str(m["id"])] = m
		}
	}
	// 回写 h3_prompt 到 plan(保持 shot 顺序引用)
	shotObjs, _ := plan["shots"].([]any)
	for i, s := range shots {
		if s.TakeTail {
			continue // 内镜由组头承载
		}
		if s.H3Prompt != "" || prompts[strconv.Itoa(s.ID)] != "" {
			continue
		}
		lg.logf(fmt.Sprintf("  ▶ 镜头 %d [%s] %s %s", s.ID, s.Scene, s.ShotSize, s.Camera))
		hp, err := ctx.genShotPrompt(s, charMap, sceneMap)
		if err != nil {
			return fmt.Errorf("镜头 %d 提示词失败: %w", s.ID, err)
		}
		prompts[strconv.Itoa(s.ID)] = hp
		if i < len(shotObjs) {
			if m, ok := shotObjs[i].(map[string]any); ok {
				m["h3_prompt"] = hp
			}
		}
		lg.logf(fmt.Sprintf("    ✅ 镜头 %d 提示词就绪（%d 字）", s.ID, len([]rune(hp))))
	}
	if err := os.MkdirAll(ctx.analysisDir, 0755); err != nil {
		return err
	}
	pm := map[string]any{}
	for k, v := range prompts {
		pm[k] = v
	}
	if b, err := json.MarshalIndent(pm, "", "  "); err == nil {
		_ = os.WriteFile(promptsPath, b, 0644)
	}
	return ctx.writePlan(plan)
}

func (ctx *manjuCtx) genShotPrompt(s manjuShot, charMap, sceneMap map[string]map[string]any) (string, error) {
	hasChar := len(s.Characters) > 0
	sys := manjuShotPromptSystem(hasChar, ctx.style)
	shotObj := map[string]any{
		"shot_id": s.ID, "shot_size": s.ShotSize, "camera": s.Camera, "action": s.Action,
		"dialogue": s.Dialogue, "narration": s.Narration, "duration": s.Duration,
		"scene": s.Scene, "characters": s.Characters,
	}
	chars := map[string]any{}
	for _, cid := range s.Characters {
		if m, ok := charMap[cid]; ok {
			chars[cid] = m
		}
	}
	data := map[string]any{
		"shot": shotObj, "characters": chars, "scene": sceneMap[s.Scene],
		"negative_prompt": ctx.negPrompt(),
		"known_issues":    topAgentIssues(ctx.project, 3),
	}
	// 多切点长镜:附加规范 + 组内各镜字段与切点时间(take_shots 供 LLM 直引,不必自算)
	if len(s.TakeGroup) > 1 {
		sys += manjuMultiCutAddon
		var group []any
		cum := 0.0
		for _, g := range s.TakeGroup {
			gm := map[string]any{
				"shot_id": g.ID, "shot_size": g.ShotSize, "camera": g.Camera, "action": g.Action,
				"dialogue": g.Dialogue, "narration": g.Narration, "duration": g.Duration,
				"scene": g.Scene, "characters": g.Characters, "cut_at": manjuTimecode(cum),
			}
			group = append(group, gm)
			cum += float64(g.Duration)
		}
		data["take_shots"] = group
	}
	ctxData, _ := json.Marshal(data)
	out, err := ctx.llm.chatJSON(sys, string(ctxData), 0.3)
	if err != nil {
		return "", err
	}
	hp := str(out["h3_prompt"])
	if hp == "" {
		return "", fmt.Errorf("LLM 未返回 h3_prompt")
	}
	return hp, nil
}

// manjuTimecode 秒 → MM:SS.mmm(H3 官方多切点时间戳格式)
func manjuTimecode(sec float64) string {
	total := int(sec * 1000)
	ms := total % 1000
	ss := (total / 1000) % 60
	mm := total / 60000
	return fmt.Sprintf("%02d:%02d.%03d", mm, ss, ms)
}

// ---- 资产(定妆照 SDXL + 场景图 Z-Image,已存在复用) ----

// negPrompt 负面提示词:配置 render.neg_prompt 缺省/为空时用内置默认(角色/场景图生成)
func (ctx *manjuCtx) negPrompt() string {
	if s := strings.TrimSpace(str(ctx.R["neg_prompt"])); s != "" {
		return s
	}
	return manjuNegPrompt
}

// characterCkpt 角色定妆照 checkpoint(按性别,兜底 animagine/sd_xl_base)
func (ctx *manjuCtx) characterCkpt(char map[string]any) string {
	cm, _ := ctx.R["char_models"].(map[string]any)
	g := str(char["gender"])
	if g == "男" {
		if s := str(cm["男"]); s != "" {
			return s
		}
	}
	if g == "女" {
		if s := str(cm["女"]); s != "" {
			return s
		}
	}
	if s := str(ctx.R["animagine_ckpt"]); s != "" {
		return s
	}
	return "sd_xl_base_1.0.safetensors"
}

// portraitWF 定妆照工作流按风格分流:含写实元素用 Z-Image(真人级),其余用 SDXL checkpoint
func (ctx *manjuCtx) portraitWF(prompt string, seed, w, h int, prefix string, char map[string]any) map[string]any {
	if manjuStyleHas(ctx.style, "real") {
		return wfZImage(prompt, str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), seed, w, h, prefix, ctx.negPrompt())
	}
	return wfSDXL(prompt, ctx.characterCkpt(char), seed, w, h, prefix, ctx.negPrompt())
}

// comfyGenImage 提交图片工作流并复制结果到 dst,返回输出文件相对路径
func (ctx *manjuCtx) comfyGenImage(wf map[string]any, dst string, lg *manjuLogger, what string) error {
	pid, err := ctx.comfy.submit(wf)
	if err != nil {
		return err
	}
	lg.logf("  🎨 提交 " + what + " " + pid[:8] + "...")
	if err := ctx.comfy.wait(pid, 900*time.Second, 5*time.Second); err != nil {
		return err
	}
	entry := ctx.comfy.history(pid)
	rel := comfyOutputImage(entry)
	if rel == "" {
		return fmt.Errorf("图片任务完成但未找到输出")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return copyFile(filepath.Join(ctx.comfyOutput, rel), dst)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func stageAssets(ctx *manjuCtx, lg *manjuLogger) error {
	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		return err
	}
	amap := map[string]any{"characters": map[string]any{}, "scenes": map[string]any{}}
	if b, err := os.ReadFile(filepath.Join(ctx.assetsDir, "asset_map.json")); err == nil {
		_ = json.Unmarshal(b, &amap)
	}
	cmap, _ := amap["characters"].(map[string]any)
	smap, _ := amap["scenes"].(map[string]any)
	if cmap == nil {
		cmap = map[string]any{}
	}
	if smap == nil {
		smap = map[string]any{}
	}
	chars, _ := plan["characters"].([]any)
	for i, c := range chars {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cid := str(m["id"])
		if cid == "" {
			continue
		}
		dst := filepath.Join(ctx.assetsDir, "characters", cid+".png")
		if !fileExists(dst) {
			lg.logf("🎨 角色定妆照: " + cid + " ...")
			wf := ctx.portraitWF(str(m["image_prompt"]), 7000+i, ctx.w, ctx.h, "manju_asset", m)
			if err := ctx.comfyGenImage(wf, dst, lg, "角色 "+cid); err != nil {
				return fmt.Errorf("角色 %s 定妆照失败: %w", cid, err)
			}
		}
		cmap[cid] = "characters/" + cid + ".png"
		cmap[cid+"_face"] = "characters/" + cid + "_face.png"
		if err := ctx.ensureFaceCrop(cid, lg); err != nil {
			return err
		}
	}
	scenes, _ := plan["scenes"].([]any)
	for i, sc := range scenes {
		m, ok := sc.(map[string]any)
		if !ok {
			continue
		}
		sid := str(m["id"])
		if sid == "" {
			continue
		}
		dst := filepath.Join(ctx.assetsDir, "scenes", sid+".png")
		if !fileExists(dst) {
			lg.logf("🎨 场景图: " + sid + " ...")
			wf := wfZImage(str(m["image_prompt"]), str(ctx.R["z_image_unet"]), str(ctx.R["z_image_clip"]), str(ctx.R["z_image_vae"]), 8000+i, ctx.w, ctx.h, "manju_asset", ctx.negPrompt())
			if err := ctx.comfyGenImage(wf, dst, lg, "场景 "+sid); err != nil {
				return fmt.Errorf("场景 %s 失败: %w", sid, err)
			}
		}
		smap[sid] = "scenes/" + sid + ".png"
	}
	if b, err := json.MarshalIndent(amap, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(ctx.assetsDir, "asset_map.json"), b, 0644)
	}
	lg.logf(fmt.Sprintf("  ✅ 资产就绪: %d 角色 / %d 场景", len(cmap)/2, len(smap)))
	return nil
}

// ---- 条件缓存指纹 + 缓存名 ----

// assetsFingerprint 对 assets/characters + assets/scenes 下 png 的「文件名+mtime」做 MD5 取 8 位
func (ctx *manjuCtx) assetsFingerprint() string {
	var parts []string
	for _, dir := range []string{"characters", "scenes"} {
		entries, err := os.ReadDir(filepath.Join(ctx.assetsDir, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			parts = append(parts, e.Name()+"@"+strconv.FormatInt(info.ModTime().Unix(), 10))
		}
	}
	sort.Strings(parts)
	sum := md5Hex(strings.Join(parts, "|"))
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return sum
}

// shotCacheName 条件缓存名:项目_v版本_内容指纹。
// 内容指纹 = md5(提示词 + 角色列表 + 场景 + 画幅 + 帧数):同条件镜头共享同一份缓存,
// Qwen3-VL 只编码一次(原来每镜头独立缓存,重复编码浪费;相同条件的后续镜头直接复用)。
// 版本号进缓存名:逻辑升级(如多角色参考图)后旧缓存自动失效重编。
const manjuCacheVer = "v2"

func (ctx *manjuCtx) shotCacheName(s manjuShot) string {
	return ctx.shotCacheNameAt(s, ctx.w, ctx.h)
}

// shotCacheNameAt 指定宽高的缓存名(草稿/定稿分辨率各自独立缓存,互不挤占)
func (ctx *manjuCtx) shotCacheNameAt(s manjuShot, w, h int) string {
	proj := reNonWord.ReplaceAllString(ctx.project, "_")
	return fmt.Sprintf("%s_%s_c%s", proj, manjuCacheVer, ctx.shotCondFingerprintAt(s, w, h))
}

// shotCondFingerprint 镜头条件指纹:决定缓存是否可复用的全部输入
func (ctx *manjuCtx) shotCondFingerprint(s manjuShot) string {
	return ctx.shotCondFingerprintAt(s, ctx.w, ctx.h)
}

// shotCondFingerprintAt 指定宽高的条件指纹(草稿/定稿分开记账)
func (ctx *manjuCtx) shotCondFingerprintAt(s manjuShot, w, h int) string {
	hh := md5.New()
	fmt.Fprintf(hh, "p=%s|w=%d|h=%d|len=%d|chars=%s|scene=%s|prompt=%s",
		s.H3Prompt, w, h, h3Length(s.Duration, ctx.fps),
		strings.Join(s.Characters, ","), s.Scene, s.H3Prompt)
	sum := fmt.Sprintf("%x", hh.Sum(nil))
	if len(sum) > 10 {
		sum = sum[:10]
	}
	return sum
}

// ---- 预编码 ----

func (ctx *manjuCtx) ensureEncoded(s manjuShot, cacheName string, lg *manjuLogger) error {
	return ctx.ensureEncodedAt(s, cacheName, ctx.w, ctx.h, lg)
}

// ensureEncodedAt 指定宽高的预编码(草稿/定稿各自的条件缓存)
func (ctx *manjuCtx) ensureEncodedAt(s manjuShot, cacheName string, w, h int, lg *manjuLogger) error {
	if fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
		return nil
	}
	lg.logf("  预编码提交...")
	wf := h3EncWorkflow(ctx.R, s.H3Prompt, w, h, h3Length(s.Duration, ctx.fps),
		ctx.charRefNames(s), ctx.sceneRefName(s), cacheName, len(s.Characters) > 0)
	pid, err := ctx.comfy.submit(wf)
	if err != nil {
		return err
	}
	if err := ctx.comfy.wait(pid, 1800*time.Second, 10*time.Second); err != nil {
		return err
	}
	lg.logf("  ✅ 镜头 " + strconv.Itoa(s.ID) + " 条件缓存完成 -> " + cacheName + ".pt")
	return nil
}

func stageEncode(ctx *manjuCtx, lg *manjuLogger) error {
	_, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	if len(selected) == 0 {
		lg.logf("  ⏭ 没有需要处理的镜头")
		return nil
	}
	for i, s := range selected {
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}
		cacheName := ctx.shotCacheName(s)
		if fileExists(h3CachePath(ctx.sharedModels, cacheName)) {
			lg.logf("  跳过（缓存已存在）: " + cacheName + ".pt")
			continue
		}
		lg.logf(fmt.Sprintf("[%d/%d] 镜头 %d: [%s] %s", i+1, len(selected), s.ID, s.Scene, s.Camera))
		if err := ctx.ensureEncoded(s, cacheName, lg); err != nil {
			return fmt.Errorf("镜头 %d 预编码失败: %w", s.ID, err)
		}
	}
	lg.logf("🎉 预编码完成")
	return nil
}

// ensurePlanAndPrompts 方案 + 多切点分组 + 逐镜提示词(渲染/预编码前置)
func (ctx *manjuCtx) ensurePlanAndPrompts(lg *manjuLogger) (map[string]any, []manjuShot, error) {
	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		return nil, nil, err
	}
	shots, _ := planShots(plan)
	ctx.ensureTakes(plan, shots, lg)   // 多切点长镜分组(experimental,默认关)
	shots = applyTakes(plan, shots)    // 组头时长=总和,内镜标 TakeTail
	if err := ctx.genShotPrompts(plan, shots, lg); err != nil {
		return nil, nil, err
	}
	plan, shots, err = ctx.loadPlan() // 重读(提示词已回写)
	if err != nil {
		return nil, nil, err
	}
	return plan, applyTakes(plan, shots), nil
}

// shotsPerTake 多切点长镜每组镜头数(1=关闭;render.shots_per_take 2-3)
func (ctx *manjuCtx) shotsPerTake() int {
	if n, ok := manjuToInt(ctx.R["shots_per_take"]); ok && n >= 2 && n <= 3 {
		return n
	}
	return 1
}

// ensureTakes 多切点长镜分组(experimental):相邻、同场景镜头贪心打包(组内时长和 ≤15s、
// 数量 ≤shots_per_take)。分组确定性且只生成一次写回 plan.takes(提示词缓存稳定性依赖)。
func (ctx *manjuCtx) ensureTakes(plan map[string]any, shots []manjuShot, lg *manjuLogger) {
	if ctx.shotsPerTake() < 2 {
		return
	}
	if plan["takes"] != nil {
		return // 已有分组复用(方案重生成时 takes 随旧方案一起消失,自动重分组)
	}
	var takes []any
	var cur []manjuShot
	flush := func() {
		defer func() { cur = nil }()
		if len(cur) < 2 {
			return
		}
		ids := make([]any, 0, len(cur))
		for _, s := range cur {
			ids = append(ids, s.ID)
		}
		takes = append(takes, ids)
	}
	for _, s := range shots {
		if len(cur) > 0 {
			last := cur[len(cur)-1]
			sum := 0
			for _, c := range cur {
				sum += c.Duration
			}
			// 新镜加入的约束:同场景 + 组内数量/时长上限
			if s.Scene != last.Scene || len(cur) >= ctx.shotsPerTake() || sum+s.Duration > 15 {
				flush()
			}
		}
		cur = append(cur, s)
	}
	flush()
	plan["takes"] = takes // 空也写(标记已分组,避免每次运行重复计算)
	if err := ctx.writePlan(plan); err == nil && len(takes) > 0 {
		n := 0
		for _, t := range takes {
			n += len(anyArr(t))
		}
		lg.logf(fmt.Sprintf("🎥 多切点长镜: %d 组(覆盖 %d 镜,组内 [Shot N] 切点一次生成)", len(takes), n))
	}
}

// applyTakes 解析 plan.takes:组头 Duration=组内总和(clamp 15s,渲染一次),
// 内镜标 TakeTail(渲染阶段跳过;字幕/ASR 由组头文件按 takes 合并承载)。
func applyTakes(plan map[string]any, shots []manjuShot) []manjuShot {
	byID := map[int]*manjuShot{}
	for i := range shots {
		byID[shots[i].ID] = &shots[i]
	}
	for _, g := range anyArr(plan["takes"]) {
		var ids []int
		for _, x := range anyArr(g) {
			if n, ok := manjuToInt(x); ok {
				ids = append(ids, n)
			}
		}
		if len(ids) < 2 {
			continue
		}
		head := byID[ids[0]]
		if head == nil {
			continue
		}
		sum := 0
		var group []manjuShot
		for _, id := range ids {
			s := byID[id]
			if s == nil {
				continue
			}
			group = append(group, *s)
			sum += s.Duration
			if id != ids[0] {
				s.TakeTail = true
			}
		}
		if sum > 15 {
			sum = 15 // API 时长上限 clamp
		}
		head.Duration = sum
		head.TakeGroup = group
	}
	return shots
}

// stagePlan 方案阶段:LLM 直出人物/场景/分镜 + 逐镜 H3 提示词
func stagePlan(ctx *manjuCtx, lg *manjuLogger) error {
	_, _, err := ctx.ensurePlanAndPrompts(lg)
	return err
}

// selectedShots 按 only 过滤(内镜恒过滤:由组头一次渲染覆盖)
func (ctx *manjuCtx) selectedShots(shots []manjuShot) []manjuShot {
	sel := map[int]bool{}
	all := ctx.only == ""
	if !all {
		for _, p := range strings.Split(ctx.only, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if i := strings.Index(p, "-"); i > 0 {
				a, _ := strconv.Atoi(p[:i])
				b, _ := strconv.Atoi(p[i+1:])
				for n := a; n <= b; n++ {
					sel[n] = true
				}
			} else if n, err := strconv.Atoi(p); err == nil {
				sel[n] = true
			}
		}
	}
	var out []manjuShot
	for _, s := range shots {
		if s.TakeTail {
			continue
		}
		if all || sel[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// ---- 参考图(复制到 ComfyUI input,LoadImage 直接按名读取) ----

// charRefNames 全部登场角色的参考图:每个角色优先正脸特写 <cid>_face.png(身份锁定强),
// 缺省回退全身定妆照;最多前 3 个角色(参考图过多稀释 token)。多角色同镜逐一传图锁身份。
func (ctx *manjuCtx) charRefNames(s manjuShot) []string {
	var out []string
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		rel := "characters/" + cid + "_face.png"
		if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
			rel = "characters/" + cid + ".png"
			if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
				continue
			}
		}
		name := fmt.Sprintf("dir_char_%d_%d.png", s.ID, i)
		_ = copyFile(filepath.Join(ctx.assetsDir, rel), filepath.Join(ctx.comfyInput, name))
		out = append(out, name)
	}
	return out
}

func (ctx *manjuCtx) sceneRefName(s manjuShot) string {
	if s.Scene == "" {
		return ""
	}
	rel := "scenes/" + s.Scene + ".png"
	if !fileExists(filepath.Join(ctx.assetsDir, rel)) {
		return ""
	}
	name := fmt.Sprintf("dir_scene_%d.png", s.ID)
	_ = copyFile(filepath.Join(ctx.assetsDir, rel), filepath.Join(ctx.comfyInput, name))
	return name
}

// ---- 渲染 ----

func stageRender(ctx *manjuCtx, lg *manjuLogger) error {
	_, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	if len(selected) == 0 {
		lg.logf("  ⏭ 没有需要处理的镜头")
		return nil
	}
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if err := os.MkdirAll(clipsEp, 0755); err != nil {
		return err
	}
	// 目标集镜头全集(仅用于进度分母与接缝序号:接缝按全集序号)
	allShots := shots
	idxOf := map[int]int{}
	for i, s := range allShots {
		idxOf[s.ID] = i + 1
	}
	for i, s := range selected {
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}
		lg.logf(fmt.Sprintf("[%d/%d] 镜头 %d: [%s] %s", i+1, len(selected), s.ID, s.Scene, s.Camera))
		dst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
		if fileExists(dst) {
			if fi, err := os.Stat(dst); err == nil && fi.Size() == 0 {
				// 中断残留的 0 字节文件:跳过会让坏产物混进成片,QC 阶段才暴露,直接删除重渲
				_ = os.Remove(dst)
				lg.logf("  ⚠️ 发现 0 字节残留,删除重渲: " + dst)
			} else if ctx.shotManifestStatus(s) == "stale" {
				// 产物过期(提示词/定妆照/场景图/画幅已变):旧镜头会被跳过复用,必须删旧重渲
				lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 产物已过期(输入已变),删旧重渲(含条件缓存)")
				ctx.clearShotArtifacts(s)
			} else {
				lg.logf("  跳过（已存在）: " + dst)
				continue
			}
		}
		// 流水线预编码:当前镜渲染等待期间,后台预提交下一镜的 Qwen3-VL 编码
		// (GPU 渲染与文本编码可并行,镜头间空窗从「编码+渲染」串行缩短为约一帧渲染时长)
		var preErr error
		var preWg sync.WaitGroup
		if i+1 < len(selected) {
			next := selected[i+1]
			nextDst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", next.ID))
			if !fileExists(nextDst) {
				preWg.Add(1)
				go func() {
					defer preWg.Done()
					preErr = ctx.ensureEncoded(next, ctx.shotCacheName(next), lg)
				}()
			}
		}
		if err := ctx.renderSingleShot(s, idxOf[s.ID], false, lg); err != nil {
			preWg.Wait()
			return err
		}
		// 等预编码收尾(通常渲染期间早已完成);失败在下一镜自己的 ensureEncoded 处暴露
		preWg.Wait()
		if preErr != nil {
			lg.logf("  ⚠️ 下一镜预编码失败(下一镜将串行重试): " + truncate(preErr.Error(), 100))
		}
	}
	lg.logf("🎉 渲染完成 -> " + clipsEp)
	return nil
}

// renderSingleShot 渲染单个镜头到定稿目录(定稿分辨率)。
func (ctx *manjuCtx) renderSingleShot(s manjuShot, idx int, fresh bool, lg *manjuLogger) error {
	return ctx.renderShotTo(s, idx, fresh, filepath.Join(ctx.clipsDir, ctx.episode), ctx.w, ctx.h, ctx.forceAttempt, lg)
}

// renderShotTo 渲染单个镜头(编码→提交→等待→取回;中断自动重试一次)。
// stageRender 与 Agent 流水线(单镜渲完即审)共用;dstDir/w/h/attempt 支持
// 草稿预审(半分辨率草稿)与 seed 重试策略(非 fixed 策略按 attempt 换 seed)。
// fresh=true 时独立生成不接缝(返工重渲镜:其首渲的接缝 latent 已被本次覆盖,
// 且下游镜基于旧 latent,再接缝只会放大跳变)。
func (ctx *manjuCtx) renderShotTo(s manjuShot, idx int, fresh bool, dstDir string, w, h, attempt int, lg *manjuLogger) error {
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	dst := filepath.Join(dstDir, fmt.Sprintf("%02d.mp4", s.ID))
	cacheName := ctx.shotCacheNameAt(s, w, h)
	if err := ctx.ensureEncodedAt(s, cacheName, w, h, lg); err != nil {
		return fmt.Errorf("镜头 %d 预编码失败: %w", s.ID, err)
	}
	// 崩溃恢复:上次「已提交未收产物」的任务先尝试收回(已完成免重渲/在跑的等完再收/丢失的重新提交)
	ckKey := strconv.Itoa(s.ID)
	if dstDir == ctx.draftDir() {
		ckKey += "@d"
	}
	if old := ctx.renderCKGet(ckKey); old != "" {
		if ok2, rerr := ctx.tryReclaim(old, dst, lg); ok2 {
			ctx.renderCKClear(ckKey)
			return nil
		} else if rerr != nil {
			lg.logf("  ⚠️ 上次未收产物的任务无法恢复(" + truncate(rerr.Error(), 120) + "),重新提交")
			ctx.renderCKClear(ckKey)
		} else {
			lg.logf("  ⚠️ 上次任务已丢失(ComfyUI 重启),重新提交")
			ctx.renderCKClear(ckKey)
		}
	}
	chained := !fresh && idx > 1 && fileExists(h3ContextLatentPath(ctx.comfyOutput, idx-1))
	if fresh {
		lg.logf("  ♻️ 镜头 " + strconv.Itoa(s.ID) + " 返工重渲:独立生成(不接缝)")
	}
	submit := func() (string, error) {
		seed := ctx.seedFor(attempt)
		if attempt > 0 && ctx.seedPolicy != "fixed" {
			lg.logf(fmt.Sprintf("  🎲 镜头 %d 第 %d 次尝试 seed=%d(策略 %s)", s.ID, attempt+1, seed, ctx.seedPolicy))
		}
		wf := h3RenderWorkflow(ctx.R, seed, w, h, h3Length(s.Duration, ctx.fps),
			ctx.steps, cacheName, len(s.Characters) > 0, chained, idx-1, idx)
		return ctx.comfy.submit(wf)
	}
	pid, err := submit()
	if err != nil {
		return fmt.Errorf("镜头 %d 提交失败: %w", s.ID, err)
	}
	ctx.renderCKSet(ckKey, pid) // 提交即落盘:崩溃后可按 prompt_id 收回,绝不重复烧 GPU
	lg.logf("  渲染提交 " + pid[:8] + "...")
	t0 := time.Now()
	if err := ctx.comfy.wait(pid, 3600*time.Second, 10*time.Second); err != nil {
		// 中断(网页取消/重启 ComfyUI)自动重试一次(同 attempt 同 seed:任务未完成,重抽无意义)
		if strings.Contains(err.Error(), "interrupt") || strings.Contains(err.Error(), "中断") {
			lg.logf("  ⚠️ 渲染被中断,5 秒后自动重试...")
			time.Sleep(5 * time.Second)
			if lg.stopped() {
				return fmt.Errorf("已停止")
			}
			pid, err = submit()
			if err != nil {
				return fmt.Errorf("镜头 %d 重试提交失败: %w", s.ID, err)
			}
			if err = ctx.comfy.wait(pid, 3600*time.Second, 10*time.Second); err != nil {
				return fmt.Errorf("镜头 %d 渲染失败(重试后): %w", s.ID, err)
			}
		} else {
			return fmt.Errorf("镜头 %d 渲染失败: %w", s.ID, err)
		}
	}
	entry := ctx.comfy.history(pid)
	rel := comfyOutputVideo(entry)
	if rel == "" {
		return fmt.Errorf("镜头 %d 任务完成但未找到视频输出", s.ID)
	}
	if err := copyFile(filepath.Join(ctx.comfyOutput, rel), dst); err != nil {
		return fmt.Errorf("镜头 %d 复制视频失败: %w", s.ID, err)
	}
	ctx.renderCKClear(ckKey) // 产物已收,检查点使命完成
	if dstDir == filepath.Join(ctx.clipsDir, ctx.episode) {
		ctx.manifestMark(s, chained) // 定稿产物入清单(时效追踪;草稿不入)
	}
	lg.logf(fmt.Sprintf("  ✅ 镜头 %d 完成（%.1f 分）-> %s", s.ID, time.Since(t0).Minutes(), dst))
	return nil
}

// ---- 质检(venv PyAV) ----

func (ctx *manjuCtx) runMedia(lg *manjuLogger, args ...string) error {
	script := ensureMediaHelper()
	argv := append([]string{script}, args...)
	cmd := exec.Command(manjuPython, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lg.logf(sc.Text())
	}
	return cmd.Wait()
}

// hasTopLevelClips 集目录是否有顶层镜头 mp4(排除 _draft/2k 等工作子目录)
func hasTopLevelClips(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			return true
		}
	}
	return false
}

func stageQC(ctx *manjuCtx, lg *manjuLogger) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if !hasTopLevelClips(clipsEp) {
		// 该集无镜头(如全本自动分集下尚未渲染的集):无事可检,跳过而非报错
		lg.logf("  ⏭ 该集无镜头可质检，跳过")
		return nil
	}
	if err := ctx.runMedia(lg, "qc", "--dir", clipsEp); err != nil {
		return fmt.Errorf("质检未通过: %w", err)
	}
	return nil
}

// ---- 合成 ----

func stageAssemble(ctx *manjuCtx, lg *manjuLogger) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if !hasTopLevelClips(clipsEp) {
		// 该集无镜头(如全本自动分集下尚未渲染的集):无事可合成,跳过
		lg.logf("  ⏭ 该集无镜头可合成，跳过")
		return nil
	}
	out := filepath.Join(ctx.workdir, ctx.episode+"_成片.mp4")
	mosaic := 0
	if MOD, ok := ctx.cfg["moderation"].(map[string]any); ok {
		if enabled, _ := MOD["mosaic_enabled"].(bool); enabled {
			mosaic = 16
			if n, ok := manjuToInt(MOD["mosaic_level"]); ok {
				mosaic = n
			}
		}
	}
	args := []string{"assemble", "--clips-dir", clipsEp, "--out", out, "--episode", ctx.episode, "--fps", strconv.Itoa(ctx.fps)}
	if mosaic > 0 {
		args = append(args, "--mosaic", strconv.Itoa(mosaic))
	}
	args = append(args, "--plan", filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json"))
	// 转场 + BGM(数据驱动配置;seam 接缝镜清单传给脚本强制硬切,叠化重影防线)
	trans := orDefault(str(ctx.R["transition"]), "cut")
	if !manjuTransitions[trans] {
		trans = "cut"
	}
	args = append(args, "--transition", trans)
	if hc := ctx.seamHardCuts(); hc != "" {
		args = append(args, "--hard-cuts", hc)
	}
	if bgm := strings.TrimSpace(str(ctx.R["bgm"])); bgm != "" {
		args = append(args, "--bgm", bgm)
		if g, ok := manjuToFloat(ctx.R["bgm_gain"]); ok && g > 0 {
			args = append(args, "--bgm-gain", strconv.FormatFloat(g, 'g', -1, 64))
		}
		if d, ok := manjuToFloat(ctx.R["bgm_duck"]); ok && d > 0 {
			args = append(args, "--bgm-duck", strconv.FormatFloat(d, 'g', -1, 64))
		}
	}
	if err := ctx.runMedia(lg, args...); err != nil {
		return fmt.Errorf("合成失败: %w", err)
	}
	return nil
}

// seamHardCuts 接缝镜头号列表(MotionContext 渲染的镜头,其起始边界画面连续,
// 合成转场必须硬切;来源 manifest 的 seam 标记)
func (ctx *manjuCtx) seamHardCuts() string {
	manjuManifestMu.Lock()
	m := ctx.manifestLoad()
	manjuManifestMu.Unlock()
	ids := []int{}
	for k, e := range m.Shots {
		if e != nil && e.Seam {
			if n, err := strconv.Atoi(k); err == nil {
				ids = append(ids, n)
			}
		}
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, n := range ids {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// ---- 媒体辅助脚本 ----

func ensureMediaHelper() string {
	dir := filepath.Join(manjuRoot, "logs", "media")
	_ = os.MkdirAll(dir, 0755)
	p := filepath.Join(dir, "manju_media.py")
	// 总是覆盖提取(内嵌脚本随 exe 版本更新)
	_ = os.WriteFile(p, []byte(manjuMediaPy), 0644)
	return p
}

// filesEqual 判断两文件内容是否相同(整文件 MD5,用于识别"_face 是主图副本"的旧状态)
func filesEqual(a, b string) bool {
	ha := fileMD5Hex(a)
	if ha == "" {
		return false
	}
	return ha == fileMD5Hex(b)
}

func fileMD5Hex(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := md5.Sum(data)
	return fmt.Sprintf("%x", h)
}

// ensureFaceCrop 确保角色有正脸特写参考(缺失或仍是主图副本时用 PIL 裁剪生成)
func (ctx *manjuCtx) ensureFaceCrop(cid string, lg *manjuLogger) error {
	mainP := filepath.Join(ctx.assetsDir, "characters", cid+".png")
	faceP := filepath.Join(ctx.assetsDir, "characters", cid+"_face.png")
	if !fileExists(mainP) {
		return nil
	}
	if fileExists(faceP) && !filesEqual(faceP, mainP) {
		return nil // 已有独立正脸
	}
	if err := ctx.runMedia(lg, "facecrop", "--src", mainP, "--dst", faceP); err != nil {
		return fmt.Errorf("角色 %s 正脸裁剪失败: %w", cid, err)
	}
	return nil
}

// ---- 环境自检 ----

func manjuEnvCheck(configPath string) string {
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		return "❌ config 读取失败: " + err.Error() + "\n[exit 1]"
	}
	var b strings.Builder
	ok := true
	b.WriteString("📁 项目: " + ctx.project + "\n")
	if fileExists(configPath) {
		b.WriteString("  ✅ config.json\n")
	} else {
		b.WriteString("  ❌ config.json 缺失\n")
		ok = false
	}
	b.WriteString("📖 小说: " + ctx.novel + "\n")
	if fileExists(ctx.novel) {
		data, _ := os.ReadFile(ctx.novel)
		b.WriteString(fmt.Sprintf("  ✅ 共 %d 章 / %d 字\n", len(allChapterNums(string(data))), len([]rune(string(data)))))
	} else {
		b.WriteString("  ❌ 小说文件不存在\n")
		ok = false
	}
	b.WriteString("🖥 ComfyUI: " + ctx.comfy.base + "\n")
	if v, err := ctx.comfy.online(); err == nil {
		b.WriteString("  ✅ 在线 v" + v + "\n")
	} else {
		b.WriteString("  ❌ 离线: " + err.Error() + "\n")
		ok = false
	}
	check := func(label, name string) string {
		p := filepath.Join(ctx.sharedModels, label, name)
		if fileExists(p) {
			return "  ✅ " + name + "\n"
		}
		ok = false
		return "  ❌ " + name + "（未找到）\n"
	}
	b.WriteString("🧠 H3 模型:\n")
	b.WriteString(check("diffusion_models", str(ctx.R["unet_ref2va"])))
	b.WriteString(check("diffusion_models", str(ctx.R["unet_fl2va"])))
	b.WriteString(check("text_encoders", str(ctx.R["clip"])))
	b.WriteString(check("vae", str(ctx.R["vae_video"])))
	b.WriteString(check("vae", str(ctx.R["vae_audio"])))
	if l := str(ctx.R["turbo_lora"]); l != "" {
		b.WriteString(check("loras", l))
	}
	if b2, _ := ctx.R["sage_attention"].(bool); b2 {
		b.WriteString("⚡ SageAttention 加速(已开启):\n")
		if ctx.comfy.hasNode("PatchSageAttentionKJ") {
			b.WriteString("  ✅ ComfyUI 节点 PatchSageAttentionKJ 可用(KJNodes)\n")
		} else {
			b.WriteString("  ❌ ComfyUI 缺少 PatchSageAttentionKJ 节点(安装 ComfyUI-KJNodes,或在渲染参数里关闭 SageAttention)\n")
			ok = false
		}
	}
	b.WriteString(check("diffusion_models", str(ctx.R["z_image_unet"])))
	b.WriteString(check("text_encoders", str(ctx.R["z_image_clip"])))
	b.WriteString(check("vae", str(ctx.R["z_image_vae"])))
	b.WriteString("🖼 SDXL checkpoint(非写实风格定妆照):\n")
	b.WriteString(check("checkpoints", "sd_xl_base_1.0.safetensors"))
	b.WriteString(check("checkpoints", "animagine-xl-3.1.safetensors"))
	b.WriteString("🐍 PyAV(质检/合成): " + manjuPython + "\n")
	if fileExists(manjuPython) {
		b.WriteString("  ✅ venv python\n")
	} else {
		b.WriteString("  ❌ venv python 缺失\n")
		ok = false
	}
	rc := 0
	if !ok {
		rc = 1
	}
	return strings.TrimRight(b.String(), "\n") + "\n[exit " + strconv.Itoa(rc) + "]"
}

// ---- 新建项目(new_project 等价:小说目录自动识别 + 默认 config) ----

func manjuCreateProject(name, novel, apiKey string) (string, string, bool) {
	var out strings.Builder
	bad := map[rune]bool{'\\': true, '/': true, ':': true, '*': true, '?': true, '"': true, '<': true, '>': true, '|': true}
	clean := strings.Map(func(r rune) rune {
		if bad[r] {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if clean == "" || clean == "." {
		return "❌ 剧名无效\n[exit 1]", "", false
	}
	novelFile, novelDir := resolveNovelPath(novel)
	if novelFile == "" {
		out.WriteString("❌ 小说路径无效或未找到正文文件: " + novel + "\n[exit 1]")
		return out.String(), "", false
	}
	projDir := filepath.Join(manjuRoot, clean)
	if _, err := os.Stat(projDir); err == nil {
		out.WriteString("❌ 项目已存在: " + clean + "\n[exit 1]")
		return out.String(), "", false
	}
	for _, d := range []string{"", "analysis", "assets/characters", "assets/scenes", "clips"} {
		if err := os.MkdirAll(filepath.Join(projDir, d), 0755); err != nil {
			return "❌ 创建目录失败: " + err.Error() + "\n[exit 1]", "", false
		}
	}
	if apiKey == "" {
		apiKey = manjuDefaultAPIKey()
	}
	cfg := manjuDefaultConfig(clean, novelFile, novelDir, apiKey)
	if err := writeManjuConfig(filepath.Join(projDir, "config.json"), cfg); err != nil {
		return "❌ 写 config 失败: " + err.Error() + "\n[exit 1]", "", false
	}
	// 从小说目录检索封面图复制到项目 assets/，供左侧栏海报展示
	if cover := copyProjectCover(novelDir, projDir); cover != "" {
		out.WriteString("  ✅ 封面已检索: " + filepath.Base(cover) + "\n")
	}
	out.WriteString("📁 项目已创建: " + projDir + "\n")
	out.WriteString("📖 小说: " + novelFile + "\n")
	out.WriteString("  ✅ config.json 已生成\n")
	return out.String(), filepath.Join(projDir, "config.json"), true
}

// copyProjectCover 从小说目录检索封面图片(文件名含 主图/封面/cover/poster 优先)复制到项目 assets/，
// 返回复制后的绝对路径(未找到返回空)。文件名保留 "cover" 关键字供左侧栏 analyzeMedia 识别为海报封面。
func copyProjectCover(novelDir, projDir string) string {
	entries, err := os.ReadDir(novelDir)
	if err != nil {
		return ""
	}
	var cands []dirFile
	for _, e := range entries {
		if e.IsDir() || !isImageFile(e.Name()) {
			continue
		}
		cands = append(cands, dirFile{Name: e.Name(), Path: filepath.Join(novelDir, e.Name())})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		pi, pj := coverPriority(cands[i].Name), coverPriority(cands[j].Name)
		if pi != pj {
			return pi < pj
		}
		return cands[i].Name < cands[j].Name
	})
	src := cands[0].Path
	dst := filepath.Join(projDir, "assets", "cover"+strings.ToLower(filepath.Ext(src)))
	if copyFile(src, dst) != nil {
		return ""
	}
	return dst
}

// firstMdByPattern 在目录内找文件名含 pat 的第一个 .md
func firstMdByPattern(dir, pat string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		if strings.Contains(e.Name(), pat) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// resolveNovelPath 小说路径识别:文件直接用;目录则在其中找 正文/全本 类 md
// (支持 全本/xxx.md、正文/xxx.md 等子目录结构,如吞天废子:全本/吞天废子·全本.md)
func resolveNovelPath(novel string) (file, dir string) {
	novel = strings.Trim(novel, `" `)
	if novel == "" {
		return "", ""
	}
	if st, err := os.Stat(novel); err == nil {
		if !st.IsDir() {
			return novel, filepath.Dir(novel)
		}
		dir = novel
		// 优先 全本 类(完整正文),其次 正文 类;先查根目录,再查同名子目录
		for _, pat := range []string{"全本", "正文"} {
			if f := firstMdByPattern(dir, pat); f != "" {
				return f, dir
			}
			subs, _ := os.ReadDir(dir)
			for _, s := range subs {
				if !s.IsDir() || !strings.Contains(s.Name(), pat) {
					continue
				}
				if f := firstMdByPattern(filepath.Join(dir, s.Name()), pat); f != "" {
					return f, dir
				}
			}
		}
		// 兜底:目录里第一个 .md(排除 设定/大纲)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			if strings.Contains(e.Name(), "设定") || strings.Contains(e.Name(), "大纲") {
				continue
			}
			return filepath.Join(dir, e.Name()), dir
		}
	}
	return "", ""
}

func manjuDefaultConfig(name, novelFile, novelDir, apiKey string) map[string]any {
	return map[string]any{
		"style": "2.5d",
		"llm": map[string]any{
			"api_key": apiKey, "base_url": "https://api.deepseek.com",
			"model": "deepseek-chat", "temperature": 0.4, "max_tokens": 8192, "request_timeout": 300,
		},
		"render": map[string]any{
			"width": 768, "height": 1344, "fps": 24, "steps": 20, "turbo_steps": 8, "seed": 1688,
			"min_shot_seconds": 4, "max_shot_seconds": 12,
			"comfy_url":      "http://127.0.0.1:8190",
			"neg_prompt":     "lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting",
			"unet_fl2va":     "MiniMax_H3_fl2va_pruned_int8_convrot.safetensors",
			"unet_ref2va":    "MiniMax_H3_ref2va_pruned_int8_convrot.safetensors",
			"clip":           "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
			"vae_video":      "minimax_h3_video_vae_fp16.safetensors",
			"vae_audio":      "minimax_h3_audio_vae_fp32.safetensors",
			"z_image_unet":   "z_image_turbo_bf16.safetensors",
			"z_image_clip":   "qwen_3_4b.safetensors",
			"z_image_vae":    "ae.safetensors",
			"turbo_lora":     "minimax_h3_turbo_4step_ema.safetensors",
			"animagine_ckpt": "animagine-xl-3.1.safetensors",
			"char_models":    map[string]any{"男": "sd_xl_base_1.0.safetensors", "女": "animagine-xl-3.1.safetensors"},
			"chapters":       "1-3", "episode": "EP01",
		},
		"moderation": map[string]any{"banned_words": []any{}, "mosaic_enabled": false, "mosaic_level": 16},
		"knowledge": map[string]any{
			"characters": []any{"古风男性角色设计板.md", "女频短剧女主提示词模板.md", "古风角色发型提示词模板.md", "古风男主发型提示词模板.md"},
			"scenes":     []any{"仙侠场景提示词模板.md", "天庭场景提示词模板.md"},
			"storyboard": []any{"AI视频运镜提示词模板.md", "漫剧创作规范.md"},
		},
		"paths": map[string]any{
			"workdir":      filepath.Join(manjuRoot, name),
			"novel":        novelFile,
			"novel_dir":    novelDir,
			"analysis":     filepath.Join(manjuRoot, name, "analysis"),
			"assets":       filepath.Join(manjuRoot, name, "assets"),
			"clips":        filepath.Join(manjuRoot, name, "clips"),
			"comfy_input":  `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared\input`,
			"comfy_output": `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared\output`,
			"outline":      "", "setting": "",
		},
	}
}

// ---- 角色抽卡 ----

// gachaCharGender 角色性别(按性别选 SDXL checkpoint;写实风格走 Z-Image 无需性别)
func (ctx *manjuCtx) gachaCharGender(char string) string {
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return ""
	}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
			return str(m["gender"])
		}
	}
	return ""
}

// manjuGachaDraw 生成一张抽卡候选(随机 seed)→ assets/characters/_gacha/<char>_s<seed>.png
func manjuGachaDraw(configPath, episode, char string) (map[string]any, int, error) {
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return nil, -1, err
	}
	if _, _, err := ctx.loadPlan(); err != nil {
		return nil, -1, fmt.Errorf("角色方案未生成,请先「生成方案」")
	}
	seed := randSeed()
	// 抽卡候选与正式定妆照同款模型:写实→Z-Image,其余→SDXL checkpoint
	charInfo := map[string]any{"gender": ctx.gachaCharGender(char)}
	wf := ctx.portraitWF(charPromptFor(ctx, char), seed, ctx.w, ctx.h, "manju_gacha", charInfo)
	dst := filepath.Join(ctx.assetsDir, "characters", "_gacha", fmt.Sprintf("%s_s%d.png", char, seed))
	lg := &manjuLogger{state: manjuState}
	if err := ctx.comfyGenImage(wf, dst, lg, "角色 "+char); err != nil {
		return nil, -1, err
	}
	return map[string]any{"ok": true, "image": dst, "seed": seed}, seed, nil
}

// charPromptFor 抽卡提示词:优先角色卡 image_prompt,兜底占位
func charPromptFor(ctx *manjuCtx, char string) string {
	plan, _, err := ctx.loadPlan()
	if err == nil {
		for _, c := range anyArr(plan["characters"]) {
			if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
				if p := str(m["image_prompt"]); p != "" {
					return p
				}
			}
		}
	}
	return "portrait of " + char + ", " + manjuAssetStyle(ctx.style) + ", upper body, detailed face, clean background"
}

// manjuAdoptGacha 采纳抽卡候选为正式定妆照(覆盖 → mtime 变化 → 缓存指纹失效),并切正脸参考
func manjuAdoptGacha(configPath, episode, char, image string) error {
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return err
	}
	if !fileExists(image) {
		return fmt.Errorf("候选图不存在: %s", image)
	}
	dst := filepath.Join(ctx.assetsDir, "characters", char+".png")
	if err := copyFile(image, dst); err != nil {
		return err
	}
	manjuMarkAdopted(ctx, char, dst)
	// 正脸特写参考(替换旧的全身副本),供 R2V 身份锁定
	lg := &manjuLogger{state: manjuState}
	return ctx.ensureFaceCrop(char, lg)
}

// manjuAdoptedFilePath 采纳标记:assets/characters/adopted.json,char → 正式定妆照路径
func manjuAdoptedFilePath(ctx *manjuCtx) string {
	return filepath.Join(ctx.assetsDir, "characters", "adopted.json")
}

// manjuMarkAdopted 记录角色已被采纳(产物-人物 据此展示采纳定妆照并标注)
func manjuMarkAdopted(ctx *manjuCtx, char, image string) {
	m := manjuAdoptedMap(ctx)
	m[char] = image
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(filepath.Dir(manjuAdoptedFilePath(ctx)), 0755)
	_ = os.WriteFile(manjuAdoptedFilePath(ctx), b, 0644)
}

// manjuAdoptedMap 读取采纳标记(文件不存在/损坏时为空 map)
func manjuAdoptedMap(ctx *manjuCtx) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(manjuAdoptedFilePath(ctx)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// manjuPlanCharacters 抽卡前置:生成角色/场景方案(复用已有,保证与渲染方案一致)
func manjuPlanCharacters(configPath, episode string) error {
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return err
	}
	lg := &manjuLogger{state: manjuState}
	if _, err := ctx.ensurePlan(lg); err != nil {
		return err
	}
	return nil
}

// ---- 小工具 ----

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

// reNonWord 缓存名清洗:保留中文/字母/数字/_/-,其余替换为 _ (与旧 Python \w unicode 行为一致)
var reNonWord = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)
