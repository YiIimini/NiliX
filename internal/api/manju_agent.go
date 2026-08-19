package api

// 漫剧智能体调度层:审片官(视觉判分) + 修复师(提示词返工) + 剧本师复核 + 例外升级。
// 复用既有六阶段管线(stagePlan/Assets/Encode/Render/QC/Assemble),智能模式在 qc 阶段
// 之后插入「判分 → 修复 → 定点重渲染(≤maxRetries 轮) → 升级推送」闭环;
// 日志契约(━━━ 阶段 X ━━━ / [i/n])保持不变,前端进度解析不受影响。
// 审片维度对齐 MiniMax H3 官方能力边界,见 internal/agent/judge.go。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nilix/internal/agent"
	"nilix/internal/config"
)

// ---- manjuLLM 适配 agent.TextLLM(避免包循环依赖) ----

type manjuAgentLLM struct{ l *manjuLLM }

func (m manjuAgentLLM) ChatJSON(system, user string, temp float64) (map[string]any, error) {
	return m.l.chatJSON(system, user, temp)
}

// ---- 配置(项目 config.json 的 agent 节 + 全局默认两级) ----

// manjuGlobalAgent 全局默认智能体配置(settings.json 的 agent 节),main 注入;
// 项目 config.json 的 agent 节只覆盖非空字段,视觉模型配一次全局即可全项目生效。
var manjuGlobalAgent = agent.DefaultConfig()

// manjuSettingsStore 全局 settings.json 的读写入口(main 注入,「另存为全局默认」用)
var manjuSettingsStore *config.Store

// SetGlobalAgentCfg 由 main/保存设置后调用,刷新全局默认(settings.json agent 节 → agent.Config)
func SetGlobalAgentCfg(cfg *config.Settings) {
	a := agent.DefaultConfig()
	if cfg != nil && cfg.Agent != nil {
		a.Enabled = cfg.Agent.Enabled
		a.VisionBaseURL = cfg.Agent.VisionBaseURL
		a.VisionAPIKey = cfg.Agent.VisionAPIKey
		a.VisionModel = cfg.Agent.VisionModel
		if cfg.Agent.PassScore > 0 {
			a.PassScore = cfg.Agent.PassScore
		}
		a.MaxRetries = cfg.Agent.MaxRetries
		a.Normalize()
	}
	manjuGlobalAgent = a
}

// SetManjuSettingsStore 注入全局设置读写(main 调用,供「另存为全局默认」写 settings.json)
func SetManjuSettingsStore(st *config.Store) { manjuSettingsStore = st }

// loadAgentCfg 读取生效的智能体配置:全局默认打底,项目 agent 节非空字段覆盖
func loadAgentCfg(ctx *manjuCtx) agent.Config {
	acfg := manjuGlobalAgent
	if m, ok := ctx.cfg["agent"].(map[string]any); ok {
		if b, ok := m["enabled"].(bool); ok {
			acfg.Enabled = b
		}
		if s := str(m["vision_base_url"]); s != "" {
			acfg.VisionBaseURL = s
		}
		if s := str(m["vision_api_key"]); s != "" {
			acfg.VisionAPIKey = s
		}
		if s := str(m["vision_model"]); s != "" {
			acfg.VisionModel = s
		}
		if v, ok := manjuToFloat(m["pass_score"]); ok && v > 0 {
			acfg.PassScore = v
		}
		if n, ok := manjuToInt(m["max_retries"]); ok {
			acfg.MaxRetries = n
		}
		if n, ok := manjuToInt(m["frames_per_shot"]); ok && n > 0 {
			acfg.FramesPerShot = n
		}
	}
	acfg.Normalize()
	return acfg
}

// visionClient 按配置构造视觉客户端(地址/Key 缺省回退项目文本 LLM 的)。
// Key 解析顺序(glm-vision 技能):项目 agent 节 → 项目 LLM Key → 环境变量 GLM_VISION_API_KEY;
// 模型支持逗号链(如 "glm-4.6v-flash,glm-4v-flash"),单模型自动补内置降级链,429 重试耗尽自动降级。
func (ctx *manjuCtx) visionClient(acfg agent.Config) *agent.VisionClient {
	base := strings.TrimSpace(acfg.VisionBaseURL)
	if base == "" {
		base = ctx.llm.baseURL
	}
	key := strings.TrimSpace(acfg.VisionAPIKey)
	if key == "" {
		key = ctx.llm.apiKey
	}
	if key == "" {
		key = agent.EnvAPIKey() // 环境变量兜底(GLM_VISION_API_KEY)
	}
	vc := agent.NewVisionClient(base, key, strings.TrimSpace(acfg.VisionModel), 180*time.Second)
	vc.OnUsage = func(model string, u agent.Usage) { manjuStatsAdd(ctx.project, model, u) }
	return vc
}

// ---- 审片状态落盘(<项目>/agent_state.json,随项目目录删除) ----

type manjuAgentEscalation struct {
	EP       string  `json:"ep"`
	Shot     int     `json:"shot"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
	At       int64   `json:"at"`
	Resolved bool    `json:"resolved"`
	Action   string  `json:"action,omitempty"` // ignore / retry-passed / retry-rendered
}

type manjuAgentPlanReview struct {
	Score       float64  `json:"score"`
	Issues      []string `json:"issues,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
	At          int64    `json:"at"`
}

// manjuStyleChoice 一次风格更新记忆
type manjuStyleChoice struct {
	At     int64  `json:"at"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Reason string `json:"reason"`
}

// manjuScorePoint 一轮审片的均分节点(分数趋势)
type manjuScorePoint struct {
	Episode string  `json:"episode"`
	Score   float64 `json:"score"`
	Count   int     `json:"count"`
	At      int64   `json:"at"`
}

// manjuAgentMemory 智能体跨次运行的学习记忆
type manjuAgentMemory struct {
	RunCount    int                `json:"runCount"`
	JudgedShots int                `json:"judgedShots"`
	ReworkCount int                `json:"reworkCount"`
	IssueStats  map[string]int     `json:"issueStats,omitempty"`
	ScoreTrend  []manjuScorePoint  `json:"scoreTrend,omitempty"`
	StyleChoices []manjuStyleChoice `json:"styleChoices,omitempty"`
	LastRunAt   int64              `json:"lastRunAt"`
}

// manjuAgentError 阶段失败诊断记录
type manjuAgentError struct {
	Stage      string `json:"stage"`
	Message    string `json:"message"`
	Diagnosis  string `json:"diagnosis"`
	Suggestion string `json:"suggestion"`
	At         int64  `json:"at"`
}

type manjuAgentState struct {
	Episode     string                     `json:"episode"`
	PassScore   float64                    `json:"passScore"`
	MaxRetries  int                        `json:"maxRetries"`
	VisionModel string                     `json:"visionModel,omitempty"`
	PlanReview  *manjuAgentPlanReview      `json:"planReview,omitempty"`
	Shots       map[string]*agent.Judgment `json:"shots"` // 镜头ID → 最新结论(当前集)
	Escalations []manjuAgentEscalation     `json:"escalations,omitempty"`
	Memory      manjuAgentMemory           `json:"memory,omitempty"`  // 学习记忆(跨次运行)
	LastError   *manjuAgentError           `json:"lastError,omitempty"` // 最近一次阶段失败诊断
	UpdatedAt   int64                      `json:"updatedAt"`
}

var manjuAgentMu sync.Mutex

func manjuAgentStatePath(project string) string {
	return filepath.Join(manjuRoot, project, "agent_state.json")
}

func loadAgentState(project string) *manjuAgentState {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	return loadAgentStateLocked(project)
}

func loadAgentStateLocked(project string) *manjuAgentState {
	st := &manjuAgentState{Shots: map[string]*agent.Judgment{}}
	b, err := os.ReadFile(manjuAgentStatePath(project))
	if err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Shots == nil {
		st.Shots = map[string]*agent.Judgment{}
	}
	if st.Memory.IssueStats == nil {
		st.Memory.IssueStats = map[string]int{}
	}
	if st.Memory.ScoreTrend == nil {
		st.Memory.ScoreTrend = []manjuScorePoint{}
	}
	if st.Memory.StyleChoices == nil {
		st.Memory.StyleChoices = []manjuStyleChoice{}
	}
	return st
}

func saveAgentStateLocked(project string, st *manjuAgentState) {
	st.UpdatedAt = time.Now().Unix()
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(filepath.Dir(manjuAgentStatePath(project)), 0755)
	_ = os.WriteFile(manjuAgentStatePath(project), b, 0644)
}

// agentStatusSummary status 接口附带的审片摘要(前端审片报告面板数据源)
func agentStatusSummary(configPath string) map[string]any {
	out := map[string]any{"configured": false, "shots": []any{}, "escalations": []any{}}
	if configPath == "" {
		return out
	}
	project := filepath.Base(filepath.Dir(configPath))
	// token 记账不依赖项目 config(项目目录在即可读)
	out["llmStats"] = manjuStatsLoad(project)
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		return out
	}
	acfg := loadAgentCfg(ctx)
	out["configured"] = acfg.Enabled
	out["visionModel"] = acfg.VisionModel
	out["passScore"] = acfg.PassScore
	out["maxRetries"] = acfg.MaxRetries
	st := loadAgentState(project)
	out["episode"] = st.Episode
	if st.PlanReview != nil {
		out["planReview"] = st.PlanReview
	}
	ids := make([]string, 0, len(st.Shots))
	for k := range st.Shots {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, ea := strconv.Atoi(ids[i])
		b, eb := strconv.Atoi(ids[j])
		if ea == nil && eb == nil {
			return a < b
		}
		return ids[i] < ids[j]
	})
	shotsOut := make([]any, 0, len(ids))
	for _, id := range ids {
		j := st.Shots[id]
		if j == nil {
			continue
		}
		shotsOut = append(shotsOut, map[string]any{
			"id": id, "status": j.Status, "score": j.Score, "retries": j.Retries,
			"issues": j.Issues, "dimensions": j.Dimensions, "fallback": j.Fallback,
			"qcFlags": j.QCFlags, "error": j.Error, "judgedAt": j.JudgedAt,
		})
	}
	out["shots"] = shotsOut
	esc := []any{}
	for _, e := range st.Escalations {
		if !e.Resolved {
			esc = append(esc, e)
		}
	}
	out["escalations"] = esc
	out["escalationCount"] = len(esc)
	out["memory"] = st.Memory
	out["lastError"] = st.LastError
	// token 用量记账(文本+视觉全部外部调用,分模型累计)
	return out
}

// ---- 智能体主管线(替代 manjuPipelineRun 的调度壳,阶段函数全部复用) ----

// manjuAgentPipelineRun 智能模式执行:与 manjuPipelineRun 同签名同日志契约;
// plan 后加剧本师复核,qc 阶段扩展为「机械质检 + 审片 + 返工闭环 + 升级」。
func manjuAgentPipelineRun(ctx *manjuCtx, phase string, lg *manjuLogger) int {
	acfg := loadAgentCfg(ctx)
	stages := []string{"plan", "assets", "encode", "render", "qc", "assemble"}
	if phase != "all" {
		stages = []string{phase}
	}
	// 学习记忆:记录本次运行
	manjuAgentMu.Lock()
	st0 := loadAgentStateLocked(ctx.project)
	st0.Memory.RunCount++
	st0.Memory.LastRunAt = time.Now().Unix()
	saveAgentStateLocked(ctx.project, st0)
	manjuAgentMu.Unlock()
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
			if err == nil && acfg.Enabled {
				agentPlanReview(ctx, lg, acfg)
			}
		case "assets":
			err = stageAssets(ctx, lg)
		case "encode":
			err = stageEncode(ctx, lg)
		case "render":
			// Agent 全权流水线:单镜渲完即审片,不合格当场修提示词排队重渲(预算内),
			// 渲染+审片+返工在 render 阶段一体完成;qc 阶段只剩统一收尾核对
			err = agentRenderPipeline(ctx, lg, acfg)
		case "qc":
			err = agentJudgeRemaining(ctx, lg, acfg)
		case "assemble":
			err = stageAssemble(ctx, lg)
			if err == nil {
				agentAssembleCheck(ctx, lg) // 成片终检:时长/黑屏/静音(报告性质,不阻断)
			}
		}
		if err != nil {
			lg.logf("❌ 阶段 " + st + " 失败: " + err.Error())
			// 智能诊断:识别错误模式,结论与建议落 agent_state 供工作台展示
			diag, sugg := manjuDiagnoseError(st, err)
			lg.logf("🤖 智能诊断[" + diag + "]: " + sugg)
			manjuAgentMu.Lock()
			ste := loadAgentStateLocked(ctx.project)
			ste.LastError = &manjuAgentError{Stage: st, Message: err.Error(), Diagnosis: diag, Suggestion: sugg, At: time.Now().Unix()}
			saveAgentStateLocked(ctx.project, ste)
			manjuAgentMu.Unlock()
			return 1
		}
	}
	return 0
}

// agentPlanReview 剧本师复核(advisory:只报告不改动,结论落 agent_state 供工作台展示)
func agentPlanReview(ctx *manjuCtx, lg *manjuLogger, acfg agent.Config) {
	plan, shots, err := ctx.loadPlan()
	if err != nil {
		return
	}
	var b strings.Builder
	b.WriteString("剧名: " + ctx.project + " / 集: " + ctx.episode)
	if t := str(plan["episode_title"]); t != "" {
		b.WriteString("《" + t + "》")
	}
	b.WriteString(fmt.Sprintf("\n角色 %d / 场景 %d / 镜头 %d\n", len(anyArr(plan["characters"])), len(anyArr(plan["scenes"])), len(shots)))
	for i, s := range shots {
		if i >= 24 {
			b.WriteString(fmt.Sprintf("…(共 %d 镜,余略)\n", len(shots)))
			break
		}
		b.WriteString(fmt.Sprintf("镜%d[%s·%s·%ds] %s | 台词:%s 旁白:%s\n", s.ID, s.Scene, s.ShotSize, s.Duration,
			truncate(s.Action, 40), truncate(s.Dialogue, 30), truncate(s.Narration, 30)))
	}
	score, issues, sugg, err := agent.PlanReview(manjuAgentLLM{ctx.llm}, b.String())
	if err != nil {
		lg.logf("📖 剧本师复核跳过: " + err.Error())
		return
	}
	lg.logf(fmt.Sprintf("📖 剧本师复核: %.0f 分", score))
	for _, is := range issues {
		lg.logf("    ⚠️ " + is)
	}
	manjuAgentMu.Lock()
	st := loadAgentStateLocked(ctx.project)
	st.Episode = ctx.episode
	st.PlanReview = &manjuAgentPlanReview{Score: score, Issues: issues, Suggestions: sugg, At: time.Now().Unix()}
	st.PassScore, st.MaxRetries, st.VisionModel = acfg.PassScore, acfg.MaxRetries, acfg.VisionModel
	saveAgentStateLocked(ctx.project, st)
	manjuAgentMu.Unlock()
	if score < 60 {
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · 📖 剧本复核 %.0f 分偏低,建议先看工作台审片报告再继续渲染", ctx.project, ctx.episode, score))
	}
}

// ---- 深度分析:小说内容 → 推荐并更新渲染风格 ----

// manjuStyleCN 中文风格叫法 → 预设 key(LLM 可能输出中文名,映射保证落库合法;key 已去空格小写)
var manjuStyleCN = map[string]string{
	"2.5d动漫": "2.5d", "动漫": "2.5d", "2.5d半写实": "2.5d",
	"写实": "real", "真人": "real", "实拍": "real", "真人实拍": "real", "电影感": "real",
	"3dcg": "3d", "3d动画": "3d", "三维": "3d", "三维cg": "3d",
	"二次元": "anime", "日漫": "anime", "日系": "anime", "动漫插画": "anime",
	"手绘": "handdrawn", "手绘插画": "handdrawn", "插画": "handdrawn",
	"纸艺": "papercraft", "剪纸": "papercraft", "纸片拼贴": "papercraft", "拼贴": "papercraft",
	"粘土": "clay", "黏土": "clay", "泥塑": "clay", "橡皮泥": "clay",
	"水墨": "ink", "水墨画": "ink", "国画": "ink", "写意": "ink",
}

// manjuStyleCNEN 中文风格词 → 英文自定义措辞(不进预设,原样拼入提示词)
var manjuStyleCNEN = map[string]string{
	"赛博朋克": "cyberpunk", "蒸汽朋克": "steampunk", "水彩": "watercolor", "水彩画": "watercolor",
	"像素": "pixel art", "像素风": "pixel art", "油画": "oil painting",
	"科幻": "sci-fi", "古风": "ancient Chinese aesthetic", "国潮": "Chinese retro wave",
	"暗黑": "dark fantasy", "哥特": "gothic", "浮世绘": "ukiyo-e", "版画": "woodblock print",
	"蜡笔": "crayon", "素描": "sketch", "铅笔": "pencil sketch", "胶片": "film grain", "复古": "vintage",
}

// isASCIIWord 是否为纯英文自定义词(含空格/-/. 等,长度受限;中文会被滤掉防污染英文提示词)
func isASCIIWord(s string) bool {
	if s == "" || len(s) > 60 {
		return false
	}
	for _, r := range s {
		if r >= 128 {
			return false
		}
	}
	return true
}

// manjuNormalizeStyle 把 LLM 返回的风格描述规整为合法 style(预设 key / key+key 组合 / 英文自定义词):
// 预设 key 大小写不敏感、中文叫法映射到 key、中文风格词转英文措辞、非法片段丢弃。
func manjuNormalizeStyle(raw string) (string, string) {
	notes := []string{}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '+' || r == '、' || r == '/' || r == '|' || r == '，' }) {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		key := ""
		low := strings.ToLower(t)
		flat := strings.ReplaceAll(low, " ", "")
		if _, ok := manjuStyles[low]; ok {
			key = low // 预设 key(大小写不敏感)
		} else if k, ok := manjuStyleCN[flat]; ok {
			key = k // 中文叫法 → 预设 key
		} else if k, ok := manjuStyleCNEN[flat]; ok {
			key = k // 中文风格词 → 英文措辞
		} else if isASCIIWord(t) {
			key = t // 英文自定义词原样保留
		} else {
			notes = append(notes, t+"(已忽略)")
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return strings.Join(out, "+"), strings.Join(notes, "; ")
}

// manjuStyleAnalyzeRun 深度分析小说章节 → LLM 推荐渲染风格 → 写入 config.json + 记忆。
// HTTP handler 与聊天「推荐风格」共用(聊天入口用 config 内的章节/集号缺省)。
func manjuStyleAnalyzeRun(configPath, episode, chapters, novel string) (map[string]any, error) {
	ctx, err := newManjuCtx(configPath, episode, chapters, "", novel)
	if err != nil {
		return nil, err
	}
	if ctx.llm == nil || ctx.llm.apiKey == "" {
		return nil, fmt.Errorf("未配置 LLM(设置 → 智能体),无法深度分析")
	}
	text, err := ctx.chapterText()
	if err != nil {
		return nil, fmt.Errorf("读小说失败: %w", err)
	}
	if len([]rune(text)) < 200 {
		return nil, fmt.Errorf("小说内容太少,无法深度分析")
	}
	old := str(ctx.cfg["style"])
	sys := `你是漫剧(竖屏短剧)渲染风格分析师,根据小说章节内容判断最匹配的渲染风格。
【分析要点】题材类型(古装/现代/玄幻/科幻/都市/悬疑…)、叙事基调(热血/治愈/暗黑/甜宠…)、场景与美术特征、目标观众画风偏好。
【输出 JSON(严格)】{"style": "...", "reason": "..."}
style 取值规则(多维组合,禁止只给单一预设):
- 主体画风:预设 key 2.5d(2.5D动漫半写实) / real(写实真人电影) / 3d(3D CG) / anime(二次元) / handdrawn(手绘) / papercraft(纸艺) / clay(粘土) / ink(水墨),最多 2 个
- 累加题材元素词(取材于小说内容,英文短语):时代/文化氛围(如 ancient Chinese aesthetic / cyberpunk / steampunk)、美术质感(如 watercolor / oil painting / film grain)、光影气质(如 moody cinematic lighting / bright pastel);2-3 个
- 整体用 + 连接(如 ink+ancient Chinese aesthetic+watercolor / 2.5d+cyberpunk+neon lighting),总元素 3-5 个,语义冲突的组合不要
- 所有题材元素词必须是英文(H3 提示词直接使用),中文风格词自行翻译
reason: 不超过 100 字中文,说明题材/基调与各风格元素的匹配理由。`
	user := "当前渲染风格: " + old + "\n需渲染章节: " + ctx.chapters + " / 集 " + ctx.episode + "\n\n小说章节内容(节选):\n" + truncate(text, 12000)
	out, err := ctx.llm.chatJSON(sys, user, 0.3)
	if err != nil {
		return nil, fmt.Errorf("深度分析失败: %w", err)
	}
	style, notes := manjuNormalizeStyle(str(out["style"]))
	if style == "" {
		return nil, fmt.Errorf("模型未给出有效风格,请重试")
	}
	reason := str(out["reason"])
	if notes != "" {
		if reason != "" {
			reason += ";"
		}
		reason += notes
	}
	ctx.cfg["style"] = style
	if err := writeManjuConfig(configPath, ctx.cfg); err != nil {
		return nil, fmt.Errorf("写入渲染配置失败: %w", err)
	}
	// 学习记忆:记录风格选择(保留最近 10 次)
	manjuAgentMu.Lock()
	stc := loadAgentStateLocked(ctx.project)
	stc.Memory.StyleChoices = append(stc.Memory.StyleChoices, manjuStyleChoice{At: time.Now().Unix(), Old: old, New: style, Reason: reason})
	if len(stc.Memory.StyleChoices) > 10 {
		stc.Memory.StyleChoices = stc.Memory.StyleChoices[len(stc.Memory.StyleChoices)-10:]
	}
	saveAgentStateLocked(ctx.project, stc)
	manjuAgentMu.Unlock()
	return map[string]any{"ok": true, "style": style, "old": old, "reason": reason}, nil
}

// manjuAgentStyleAnalyze 深度分析小说章节 → LLM 推荐渲染风格 → 写入 config.json(主要调整风格项)。
// 返回旧/新风格与推荐理由,由前端决定是否继续走智能一条龙。
func manjuAgentStyleAnalyze(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	configPath := str(body["config"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	res, err := manjuStyleAnalyzeRun(configPath, str(body["episode"]), str(body["chapters"]), str(body["novel"]))
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "深度分析失败") || strings.Contains(err.Error(), "未给出有效风格") ||
			strings.Contains(err.Error(), "写入渲染配置失败") {
			code = http.StatusInternalServerError
		}
		http.Error(w, `{"error":"`+err.Error()+`"}`, code)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- 机械质检(JSON 报告) ----

// runASRCheck ASR 台词核对(本地 faster-whisper):有台词镜头转写比对,不符 → 汇入 qcBad 触发返工。
// 全本地 CPU small 模型;无台词镜头跳过;脚本/模型不可用时静默降级(不影响判分流程)。
func (ctx *manjuCtx) runASRCheck(lg *manjuLogger, clipsEp string, shots []manjuShot, planPath string) map[int][]string {
	var spoken []manjuShot
	for _, s := range shots {
		if strings.TrimSpace(s.Dialogue) != "" && fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))) {
			spoken = append(spoken, s)
		}
	}
	if len(spoken) == 0 {
		return nil
	}
	lg.logf(fmt.Sprintf("🎤 ASR 台词核对: %d 个有台词镜头(本地 small 模型,首次运行需下载)", len(spoken)))
	ids := make([]string, 0, len(spoken))
	for _, s := range spoken {
		ids = append(ids, strconv.Itoa(s.ID))
	}
	out, err := ctx.runMediaOut("asr", "--dir", clipsEp, "--plan", planPath, "--shots", strings.Join(ids, ","))
	if err != nil {
		lg.logf("  ⚠️ ASR 核对不可用(忽略,继续视觉判分): " + truncate(err.Error(), 120))
		return nil
	}
	m := parseJSONLine(out)
	if m == nil {
		return nil
	}
	shotsMap, _ := m["shots"].(map[string]any)
	bad := map[int][]string{}
	for id, v := range shotsMap {
		r, ok := v.(map[string]any)
		if !ok || r["ok"] == true {
			continue
		}
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		reason := "台词与分镜不符"
		if e := str(r["error"]); e != "" {
			reason = "ASR 失败: " + truncate(e, 60)
		} else if sp, ex := str(r["spoken"]), str(r["expected"]); sp != "" || ex != "" {
			reason = fmt.Sprintf("台词不符(实听「%s」vs 分镜「%s」)", truncate(sp, 24), truncate(ex, 24))
		}
		bad[n] = append(bad[n], reason)
		lg.logf(fmt.Sprintf("  ❌ 镜头 %d %s", n, reason))
	}
	if len(bad) == 0 {
		lg.logf("  ✅ 有台词镜头转写全部与分镜一致")
	}
	return bad
}

// runMediaOut 跑媒体辅助脚本并捕获完整 stdout(不写运行日志,供 JSON 解析)
func (ctx *manjuCtx) runMediaOut(args ...string) (string, error) {
	script := ensureMediaHelper()
	cmd := exec.Command(manjuPython, append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if err := cmd.Wait(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// runQCJSON 机械质检并返回问题镜头报告 {镜头ID: flags}
// runQCJSON 机械质检并返回问题镜头报告 {镜头ID: flags};onlyShots 非空时只检指定镜(如 "3"/"1,3")
func (ctx *manjuCtx) runQCJSON(lg *manjuLogger, clipsEp, onlyShots string) (map[int][]string, error) {
	jsonPath := filepath.Join(os.TempDir(), fmt.Sprintf("manju_qc_%d.json", time.Now().UnixNano()))
	defer os.Remove(jsonPath)
	args := []string{"qc", "--dir", clipsEp, "--json", jsonPath}
	if onlyShots != "" {
		args = append(args, "--shots", onlyShots)
	}
	if _, err := ctx.runMediaOut(args...); err != nil {
		// 质检脚本自身失败(如目录空):不影响判分流程,视为无机械问题
		lg.logf("  ⚠️ 机械质检异常(忽略,继续审片): " + err.Error())
		return map[int][]string{}, nil
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		return map[int][]string{}, nil
	}
	var report struct {
		Shots map[string]struct {
			OK    bool     `json:"ok"`
			Flags []string `json:"flags"`
		} `json:"shots"`
	}
	if json.Unmarshal(b, &report) != nil {
		return map[int][]string{}, nil
	}
	out := map[int][]string{}
	for name, r := range report.Shots {
		id, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(name), ".mp4"))
		if err != nil || r.OK {
			continue
		}
		out[id] = r.Flags
	}
	return out, nil
}

// ---- 审片 + 返工闭环 ----

// shotFramesDir 抽帧输出目录 analysis/_frames/<ep>/<NN>/
func (ctx *manjuCtx) shotFramesDir(shotID int) string {
	return filepath.Join(ctx.analysisDir, "_frames", ctx.episode, fmt.Sprintf("%02d", shotID))
}

// extractFrames 抽帧(媒体辅助脚本),返回 JPEG 路径列表
func (ctx *manjuCtx) extractFrames(lg *manjuLogger, clip string, shotID, count int) ([]string, error) {
	dir := ctx.shotFramesDir(shotID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	out, err := ctx.runMediaOut("frames", "--video", clip, "--out-dir", dir, "--count", strconv.Itoa(count))
	if err != nil {
		return nil, fmt.Errorf("抽帧失败: %w", err)
	}
	m := parseJSONLine(out)
	var frames []string
	if m != nil {
		if arr, ok := m["frames"].([]any); ok {
			for _, x := range arr {
				if s := str(x); s != "" {
					frames = append(frames, s)
				}
			}
		}
	}
	if len(frames) == 0 {
		// 兜底:目录里有什么 jpg 用什么
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
				frames = append(frames, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("抽帧 0 张")
	}
	return frames, nil
}

// shotMetaFromPlan 从方案组装审片输入元数据
func shotMetaFromPlan(s manjuShot, charMap, sceneMap map[string]map[string]any, styleDesc string) agent.ShotMeta {
	meta := agent.ShotMeta{
		ShotID: s.ID, Scene: s.Scene, Characters: s.Characters, ShotSize: s.ShotSize,
		Camera: s.Camera, Action: s.Action, Dialogue: s.Dialogue, Narration: s.Narration,
		StyleDesc: styleDesc, HasChar: len(s.Characters) > 0,
	}
	if sc, ok := sceneMap[s.Scene]; ok {
		meta.SceneDesc = str(sc["description"])
	}
	if meta.HasChar {
		if c, ok := charMap[s.Characters[0]]; ok {
			meta.CharDesc = strings.TrimSpace(str(c["appearance"]) + ";" + str(c["costume"]))
		}
	}
	return meta
}

// refImagesFor 镜头参考图:R2V=全部登场角色定妆照(正脸优先,最多 3 个),FL2VA=场景图
func (ctx *manjuCtx) refImagesFor(s manjuShot) []string {
	var out []string
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		for _, rel := range []string{"characters/" + cid + "_face.png", "characters/" + cid + ".png"} {
			p := filepath.Join(ctx.assetsDir, rel)
			if fileExists(p) {
				out = append(out, p)
				break
			}
		}
	}
	if s.Scene != "" {
		p := filepath.Join(ctx.assetsDir, "scenes", s.Scene+".png")
		if fileExists(p) {
			out = append(out, p)
		}
	}
	return out
}

// judgeShots 对给定镜头逐个审片(已有 mp4 才判),写状态并返回本轮失败的镜头ID。
// clipsDir 为镜头产物目录(常规=定稿目录;草稿预审=clips/<ep>/_draft)。
func (ctx *manjuCtx) judgeShots(lg *manjuLogger, acfg agent.Config, plan map[string]any, shots []manjuShot, qcBad map[int][]string, clipsDir string) []int {
	project := ctx.project
	clipsEp := clipsDir
	if !acfg.VisionReady() {
		// 审片官未配置:机械质检问题直接进失败集(升级用),不打分不返工
		var failed []int
		for id, flags := range qcBad {
			lg.logf(fmt.Sprintf("🤖 镜头 %d 机械质检未过: %s(视觉模型未配置,无法判分返工)", id, strings.Join(flags, "、")))
			failed = append(failed, id)
		}
		return failed
	}
	vc := ctx.visionClient(acfg)
	charMap, sceneMap := planCharSceneMaps(plan)
	styleDesc := manjuStyleDesc(ctx.style).asset
	var failed []int
	for _, s := range shots {
		clip := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
		if !fileExists(clip) {
			continue
		}
		jd := &agent.Judgment{Status: "pending", JudgedAt: time.Now().Unix(), Model: acfg.VisionModel}
		jd.QCFlags = qcBad[s.ID]
		frames, ferr := ctx.extractFrames(lg, clip, s.ID, acfg.FramesPerShot)
		if ferr != nil {
			jd.Error = ferr.Error()
			lg.logf("🤖 审片 镜头 " + strconv.Itoa(s.ID) + " 抽帧失败: " + ferr.Error())
		} else {
			meta := shotMetaFromPlan(s, charMap, sceneMap, styleDesc)
			if j, err := agent.Judge(vc, meta, frames, ctx.refImagesFor(s), acfg.PassScore); err != nil {
				jd.Error = err.Error()
				lg.logf("🤖 审片 镜头 " + strconv.Itoa(s.ID) + " 调用失败: " + truncate(err.Error(), 160))
			} else {
				jd = j
				jd.QCFlags = qcBad[s.ID]
				mark := "✅"
				if jd.Status != "pass" {
					mark = "❌"
					failed = append(failed, s.ID)
				}
				weak := agent.WeakDims(jd.Dimensions, 60)
				weakStr := ""
				if len(weak) > 0 {
					parts := make([]string, 0, len(weak))
					for _, d := range weak {
						parts = append(parts, fmt.Sprintf("%s%.0f", d.Name, jd.Dimensions[d.Key]))
					}
					weakStr = "(弱项:" + strings.Join(parts, "/") + ")"
				}
				lg.logf(fmt.Sprintf("🤖 审片 镜头 %d: %.1f 分 %s %s", s.ID, jd.Score, mark, weakStr))
				for _, is := range jd.Issues {
					lg.logf("      · " + is)
				}
			}
		}
		// 机械质检 bad 的镜头无论判分如何都进失败集(黑屏/无声必须返工)
		if len(jd.QCFlags) > 0 {
			found := false
			for _, id := range failed {
				if id == s.ID {
					found = true
					break
				}
			}
			if !found {
				failed = append(failed, s.ID)
			}
		}
		manjuAgentMu.Lock()
		st := loadAgentStateLocked(project)
		if st.Episode != ctx.episode {
			// 换集:审片报告翻页(旧集升级记录随报告清空,成片/日志仍在)
			st.Episode = ctx.episode
			st.Shots = map[string]*agent.Judgment{}
			st.Escalations = nil
		}
		if prev := st.Shots[strconv.Itoa(s.ID)]; prev != nil {
			jd.Retries = prev.Retries
		}
		if jd.Status == "pass" && len(jd.QCFlags) == 0 && jd.Retries > 0 {
			jd.Status = "fixed"
		}
		st.Shots[strconv.Itoa(s.ID)] = jd
		st.PassScore, st.MaxRetries, st.VisionModel = acfg.PassScore, acfg.MaxRetries, acfg.VisionModel
		saveAgentStateLocked(project, st)
		manjuAgentMu.Unlock()
	}
	return failed
}

func planCharSceneMaps(plan map[string]any) (charMap, sceneMap map[string]map[string]any) {
	charMap = map[string]map[string]any{}
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok {
			charMap[str(m["id"])] = m
		}
	}
	sceneMap = map[string]map[string]any{}
	for _, s := range anyArr(plan["scenes"]) {
		if m, ok := s.(map[string]any); ok {
			sceneMap[str(m["id"])] = m
		}
	}
	return
}

// clearShotArtifacts 删镜头 mp4 + 条件缓存(.pt)+ 清单记录,让定点重渲染真正重做
// (提示词改动只在重新编码时生效;缓存名不含提示词指纹,必须显式删)。
func (ctx *manjuCtx) clearShotArtifacts(s manjuShot) {
	_ = os.Remove(filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", s.ID)))
	_ = os.Remove(h3CachePath(ctx.sharedModels, ctx.shotCacheName(s)))
	ctx.manifestRemove(s.ID)
}

// updateShotPrompt 把修复师的新提示词写回方案 json + 逐镜提示词缓存
func (ctx *manjuCtx) updateShotPrompt(s manjuShot, newPrompt string) error {
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return err
	}
	shotObjs, _ := plan["shots"].([]any)
	for _, x := range shotObjs {
		if m, ok := x.(map[string]any); ok {
			if n, ok := manjuToInt(m["shot_id"]); ok && n == s.ID {
				m["h3_prompt"] = newPrompt
				break
			}
		}
	}
	if err := ctx.writePlan(plan); err != nil {
		return err
	}
	promptsPath := filepath.Join(ctx.analysisDir, ctx.episode+"_shots_prompts.json")
	prompts := map[string]any{}
	if b, err := os.ReadFile(promptsPath); err == nil {
		_ = json.Unmarshal(b, &prompts)
	}
	prompts[strconv.Itoa(s.ID)] = newPrompt
	if b, err := json.MarshalIndent(prompts, "", "  "); err == nil {
		_ = os.WriteFile(promptsPath, b, 0644)
	}
	return nil
}

// ---- Agent 全权流水线:单镜渲染 → 即时审片 → 不合格修复提示词排队重渲 → 统一收尾 ----

// topAgentIssues 项目历史高频审片问题 topN(学习记忆 → 提示词生成反哺;无记录返回空)
func topAgentIssues(project string, n int) []string {
	st := loadAgentState(project)
	type kv struct {
		k string
		v int
	}
	var arr []kv
	for k, v := range st.Memory.IssueStats {
		if k = strings.TrimSpace(k); k != "" && v >= 2 {
			arr = append(arr, kv{k, v})
		}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
	if len(arr) > n {
		arr = arr[:n]
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		out = append(out, fmt.Sprintf("%s×%d", it.k, it.v))
	}
	return out
}

// agentRenderPipeline 智能一条龙 render 阶段主体(替代整段渲完再统一审):
// 逐镜「渲染→机械质检+ASR 台词核对+视觉判分」;不合格镜头当场由修复师改写 H3 提示词,
// 删产物进重渲队列(每镜预算 acfg.MaxRetries 轮);预算耗尽升级待人拍板;全部通过后
// 由 qc 阶段收尾汇总、assemble 统一合成成片。
func agentRenderPipeline(ctx *manjuCtx, lg *manjuLogger, acfg agent.Config) error {
	plan, shots, err := ctx.ensurePlanAndPrompts(lg)
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
	idxOf := map[int]int{}
	for i, s := range shots {
		idxOf[s.ID] = i + 1
	}
	// 草稿预审(可配):审片返工轮用缩放分辨率草稿(判分与分辨率弱相关,GPU 时间约按像素量等比下降),
	// 全部通过/升级落定后按全集顺序全分辨率定稿重渲(提示词已审定,定稿零返工)。
	draftMode := acfg.VisionReady() && ctx.draftJudge
	judgeDir := clipsEp
	jw2, jh2 := ctx.w, ctx.h
	if draftMode {
		judgeDir = ctx.draftDir()
		jw2, jh2 = ctx.draftDims()
		_ = os.MkdirAll(judgeDir, 0755)
	}
	lg.logf(fmt.Sprintf("🤖 Agent 流水线启动: %d 镜 · 渲染与审片并行(渲完即后台判分,ASR 按轮批量) · 不合格修复提示词排队重渲(预算 %d 轮)%s",
		len(selected), acfg.MaxRetries, func() string {
			if draftMode {
				return fmt.Sprintf("\n📐 草稿预审: 审片轮 %d×%d 草稿 → 落定后 %d×%d 定稿重渲", jw2, jh2, ctx.w, ctx.h)
			}
			return ""
		}()))

	queue := selected
	queueIsFirst := true
	passed := 0
	escCount := 0
	planPath := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")
	for len(queue) > 0 && !lg.stopped() {
		// 本轮两阶段:①逐镜渲染,渲完立即后台并发审片(渲染不空等——审片期间 GPU 继续渲下一镜);
		// ②轮末批量 ASR 台词核对(whisper 模型整轮只加载一次,原来每镜加载一次是主要变慢原因)
		var jw sync.WaitGroup
		judgeSem := make(chan struct{}, 2) // 视觉判分 API 并发上限
		for _, s := range queue {
			if lg.stopped() {
				jw.Wait()
				return fmt.Errorf("已停止")
			}
			dst := filepath.Join(judgeDir, fmt.Sprintf("%02d.mp4", s.ID))
			finalP := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
			firstRound := queueIsFirst // 首轮=正常渲(接缝);返工轮=独立生成(不接缝,防旧 latent 污染)
			if !draftMode && fileExists(dst) && ctx.shotManifestStatus(s) == "stale" {
				lg.logf(fmt.Sprintf("⚠️ 镜头 %d 产物已过期(输入已变),删旧重渲", s.ID))
				ctx.clearShotArtifacts(s)
			} else if draftMode && fileExists(finalP) && ctx.shotManifestStatus(s) == "stale" {
				lg.logf(fmt.Sprintf("⚠️ 镜头 %d 定稿已过期(输入已变),删旧走草稿重审", s.ID))
				ctx.clearShotArtifacts(s)
			}
			if !draftMode && fileExists(dst) {
				lg.logf(fmt.Sprintf("♻️ 镜头 %d 已有产物,直接进入审片", s.ID))
			} else if draftMode && fileExists(finalP) {
				lg.logf(fmt.Sprintf("✅ 镜头 %d 已有定稿产物,跳过草稿与审片", s.ID))
				continue
			} else if fileExists(dst) {
				lg.logf(fmt.Sprintf("♻️ 镜头 %d 已有草稿产物,直接进入审片", s.ID))
			} else {
				// 返工轮按该镜返工序号换 seed(seed 策略非 fixed 时)
				attempt := 0
				if !firstRound {
					if jd := loadAgentStateShot(ctx.project, s.ID); jd.Retries > 0 {
						attempt = jd.Retries
					}
				}
				if err := ctx.renderShotTo(s, idxOf[s.ID], !firstRound, judgeDir, jw2, jh2, attempt, lg); err != nil {
					jw.Wait()
					return err
				}
			}
			// 后台即时审片(qc 单镜 + 视觉判分;ASR 轮末批量),结论落 agent_state
			jw.Add(1)
			go func(s manjuShot) {
				defer jw.Done()
				judgeSem <- struct{}{}
				defer func() { <-judgeSem }()
				lg.logf(fmt.Sprintf("🤖 审片官接管镜头 %d ...", s.ID))
				qcBad, _ := ctx.runQCJSON(lg, judgeDir, strconv.Itoa(s.ID))
				ctx.judgeShots(lg, acfg, plan, []manjuShot{s}, qcBad, judgeDir)
			}(s)
		}
		jw.Wait() // 等本轮全部审片落定
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}
		// 批量 ASR:本轮全部有台词镜头一次转写(一次模型加载),结果并入失败集
		asrBad := ctx.runASRCheck(lg, judgeDir, queue, planPath)

		// 汇总失败镜头(视觉判分未过 / 判分调用出错 / ASR 台词不符)→ 升级或修复重渲
		var redo []manjuShot
		for _, s := range queue {
			jd := loadAgentStateShot(ctx.project, s.ID)
			asrFlags, asrHit := asrBad[s.ID]
			visualFailed := jd.Status == "failed" || (jd.Status == "pending" && jd.Error != "")
			if !visualFailed && !asrHit {
				passed++
				continue
			}
			if asrHit { // ASR 意见并入该镜 QCFlags(升级原因与面板可见)
				manjuAgentMu.Lock()
				st := loadAgentStateLocked(ctx.project)
				if j := st.Shots[strconv.Itoa(s.ID)]; j != nil {
					j.QCFlags = append(j.QCFlags, asrFlags...)
					st.Shots[strconv.Itoa(s.ID)] = j
				}
				saveAgentStateLocked(ctx.project, st)
				manjuAgentMu.Unlock()
				jd = loadAgentStateShot(ctx.project, s.ID)
			}
			// 失败处置:无视觉模型或预算耗尽 → 升级;否则修复师改提示词排队重渲
			if !acfg.VisionReady() {
				reason := strings.Join(append(jd.QCFlags, "机械质检/台词未过(未配置视觉模型,不判分)"), ";")
				escalateShot(ctx, lg, s.ID, jd.Score, reason, acfg)
				escCount++
				continue
			}
			if jd.Retries >= acfg.MaxRetries {
				reason := strings.Join(jd.Issues, ";")
				if reason == "" {
					reason = strings.Join(jd.QCFlags, ";")
				}
				if reason == "" {
					reason = fmt.Sprintf("判分 %.0f 未达 %.0f(重渲 %d 轮耗尽)", jd.Score, acfg.PassScore, jd.Retries)
				}
				escalateShot(ctx, lg, s.ID, jd.Score, reason, acfg)
				lg.logf(fmt.Sprintf("🚨 镜头 %d 预算耗尽,升级待人拍板", s.ID))
				escCount++
				continue
			}
			// 修复师:按审片意见改写 H3 提示词 → 删旧产物排队重渲
			charMap, sceneMap := planCharSceneMaps(plan)
			np, ferr := agent.FixPrompt(manjuAgentLLM{ctx.llm}, shotMetaFromPlan(s, charMap, sceneMap, manjuStyleDesc(ctx.style).asset), s.H3Prompt, jd)
			if ferr == nil {
				if uerr := ctx.updateShotPrompt(s, np); uerr == nil {
					s.H3Prompt = np
					lg.logf(fmt.Sprintf("  ✏️ 镜头 %d 提示词已按审片意见修复(%d 字)", s.ID, len([]rune(np))))
				} else {
					lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 提示词回写失败(按原提示词重渲): " + uerr.Error())
				}
			} else {
				lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 修复师失败(按原提示词重渲染): " + truncate(ferr.Error(), 120))
			}
			ctx.clearShotArtifacts(s)
			bumpAgentRetries(ctx.project, s.ID)
			redo = append(redo, s)
			lg.logf(fmt.Sprintf("  🔁 镜头 %d 排队重渲(第 %d/%d 轮)", s.ID, jd.Retries+1, acfg.MaxRetries))
		}
		queue = redo
		queueIsFirst = false
	}
	if lg.stopped() {
		return fmt.Errorf("已停止")
	}
	if escCount > 0 {
		lg.logf(fmt.Sprintf("⚠️ %d 个镜头升级待拍板(工作台「审片报告」可重试/忽略),成片继续合成", escCount))
	} else {
		lg.logf(fmt.Sprintf("🎉 审片全部通过:%d 镜(含返工通过)", passed))
	}
	// 定稿轮(草稿预审):以审定后的提示词按全集顺序全分辨率重渲(保 MotionContext 接缝),
	// 已有定稿产物的镜头跳过(中断续跑幂等);完成后清草稿目录与草稿条件缓存。
	if draftMode {
		_, shots2, err := ctx.loadPlan() // 重读:返工轮改写的提示词已回写方案(只需镜头顺序与最新提示词)
		if err != nil {
			return err
		}
		selSet := map[int]bool{}
		for _, s := range selected {
			selSet[s.ID] = true
		}
		lg.logf(fmt.Sprintf("📐 定稿轮: %d 镜 × %d×%d 全分辨率渲染(提示词已审定,零返工)", len(selected), ctx.w, ctx.h))
		done := 0
		for _, s := range shots2 {
			if lg.stopped() {
				return fmt.Errorf("已停止")
			}
			if !selSet[s.ID] {
				continue
			}
			done++
			dst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
			if fileExists(dst) && ctx.shotManifestStatus(s) == "stale" {
				lg.logf(fmt.Sprintf("⚠️ 镜头 %d 定稿已过期(输入已变),删旧重渲", s.ID))
				ctx.clearShotArtifacts(s)
			}
			if fileExists(dst) {
				lg.logf(fmt.Sprintf("  跳过（已定稿）: %s", dst))
				continue
			}
			lg.logf(fmt.Sprintf("[%d/%d] 定稿 镜头 %d: [%s] %s", done, len(selected), s.ID, s.Scene, s.Camera))
			if err := ctx.renderShotTo(s, idxOf[s.ID], false, clipsEp, ctx.w, ctx.h, 0, lg); err != nil {
				return err
			}
			// 草稿条件缓存清理(定稿缓存另名共存,草稿 .pt 不再需要)
			_ = os.Remove(h3CachePath(ctx.sharedModels, ctx.shotCacheNameAt(s, jw2, jh2)))
		}
		_ = os.RemoveAll(ctx.draftDir())
		lg.logf("🎉 定稿轮完成,草稿目录已清理")
	}
	return nil
}

// agentAssembleCheck 成片终检:对合成后的成片跑单文件机械质检(时长/近黑帧/静音/音轨),
// 结果写日志(镜头级问题已由流水线逐镜审过,此处为成片级兜底报告)。
func agentAssembleCheck(ctx *manjuCtx, lg *manjuLogger) {
	final := filepath.Join(ctx.workdir, ctx.episode+"_成片.mp4")
	if !fileExists(final) {
		return
	}
	// qc 发现问题时 exit 1(不等于执行失败),stdout 仍带完整报告——只看输出内容
	out, _ := ctx.runMediaOut("qc", "--file", final)
	for _, ln := range strings.Split(out, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "质检") {
			continue
		}
		if strings.Contains(t, "✅") {
			lg.logf("🎞 成片终检: " + t)
		} else if strings.Contains(t, "⚠️") || strings.Contains(t, "❌") {
			lg.logf("⚠️ 成片终检发现问题: " + t)
		}
	}
}


// agentJudgeRemaining qc 阶段收尾:补审漏网镜头(中断续跑等场景)+ 学习记忆汇总。
func agentJudgeRemaining(ctx *manjuCtx, lg *manjuLogger, acfg agent.Config) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if entries, err := os.ReadDir(clipsEp); err != nil || len(entries) == 0 {
		lg.logf("  ⏭ 该集无镜头可审片，跳过")
		return nil
	}
	plan, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	// 合成前终检:全目录机械质检兜底(流水线逐镜审过,此处抓漏网:产物被手动替换/ASR 库中途不可用等)
	// 发现"有产物、有判分记录、但最新记录未含当前质检问题"的镜头 → 重审一次;仍失败直接升级(收尾阶段不再返工)
	finalBad, _ := ctx.runQCJSON(lg, clipsEp, "")
	if len(finalBad) > 0 {
		var needRejudge []manjuShot
		for _, s := range selected {
			flags, hit := finalBad[s.ID]
			if !hit || !fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))) {
				continue
			}
			jd := loadAgentStateShot(ctx.project, s.ID)
			known := strings.Join(jd.QCFlags, ";")
			newIssue := false
			for _, f := range flags {
				if !strings.Contains(known, f) {
					newIssue = true
					break
				}
			}
			if newIssue {
				needRejudge = append(needRejudge, s)
			}
		}
		if len(needRejudge) > 0 {
			lg.logf(fmt.Sprintf("🔍 终检发现 %d 个镜头有新机械质检问题,重审", len(needRejudge)))
			failed := ctx.judgeShots(lg, acfg, plan, needRejudge, finalBad, clipsEp)
			for _, id := range failed {
				jd := loadAgentStateShot(ctx.project, id)
				reason := strings.Join(append(jd.QCFlags, jd.Issues...), ";")
				escalateShot(ctx, lg, id, jd.Score, orDefault(reason, "终检未过"), acfg)
			}
			if len(failed) > 0 {
				lg.logf(fmt.Sprintf("🚨 终检 %d 镜未过,已升级待人拍板", len(failed)))
			}
		}
	}
	// 补审:有产物但无判分记录的镜头(流水线被中断后续跑)
	st := loadAgentState(ctx.project)
	var missed []manjuShot
	for _, s := range selected {
		if !fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))) {
			continue
		}
		if st.Shots[strconv.Itoa(s.ID)] == nil {
			missed = append(missed, s)
		}
	}
	if st.Episode != ctx.episode {
		// 换集:翻页(审片状态由流水线重写,此处只兜底)
		st.Episode = ctx.episode
	}
	if len(missed) > 0 {
		lg.logf(fmt.Sprintf("🤖 补审 %d 个漏审镜头(中断续跑)", len(missed)))
		qcBad, _ := ctx.runQCJSON(lg, clipsEp, "")
		planPath := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")
		if asrBad := ctx.runASRCheck(lg, clipsEp, missed, planPath); len(asrBad) > 0 {
			for id, flags := range asrBad {
				qcBad[id] = append(qcBad[id], flags...)
			}
		}
		failed := ctx.judgeShots(lg, acfg, plan, missed, qcBad, clipsEp)
		if len(failed) > 0 && acfg.VisionReady() {
			// 补审未过的镜头走一轮定点修复重渲(与流水线同规则)
			var redo []manjuShot
			for _, s := range missed {
				for _, id := range failed {
					if s.ID == id {
						redo = append(redo, s)
					}
				}
			}
			for _, s := range redo {
				jd := loadAgentStateShot(ctx.project, s.ID)
				charMap, sceneMap := planCharSceneMaps(plan)
				if np, ferr := agent.FixPrompt(manjuAgentLLM{ctx.llm}, shotMetaFromPlan(s, charMap, sceneMap, manjuStyleDesc(ctx.style).asset), s.H3Prompt, jd); ferr == nil {
					if uerr := ctx.updateShotPrompt(s, np); uerr == nil {
						s.H3Prompt = np
					}
				}
				ctx.clearShotArtifacts(s)
				bumpAgentRetries(ctx.project, s.ID)
				attempt := loadAgentStateShot(ctx.project, s.ID).Retries
				if err := ctx.renderShotTo(s, 0, true, clipsEp, ctx.w, ctx.h, attempt, lg); err != nil {
					lg.logf("  ⚠️ 补审重渲失败: " + err.Error())
					continue
				}
				ctx.judgeShots(lg, acfg, plan, []manjuShot{s}, nil, clipsEp)
			}
		}
	}
	return agentSummarizeJudging(ctx, lg)
}

// agentSummarizeJudging 学习记忆汇总:返工轮数/审片均分趋势/高频问题统计(落 agent_state)
func agentSummarizeJudging(ctx *manjuCtx, lg *manjuLogger) error {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	stm := loadAgentStateLocked(ctx.project)
	total, cnt := 0.0, 0
	for _, j := range stm.Shots {
		if j.Score > 0 {
			total += j.Score
			cnt++
		}
		stm.Memory.ReworkCount += j.Retries // 本集各镜返工轮数累计
		for _, is := range j.Issues {
			is = strings.TrimSpace(is)
			if is != "" {
				stm.Memory.IssueStats[is]++
			}
		}
	}
	if cnt > 0 {
		stm.Memory.ScoreTrend = append(stm.Memory.ScoreTrend, manjuScorePoint{
			Episode: ctx.episode, Score: total / float64(cnt), Count: cnt, At: time.Now().Unix()})
		if len(stm.Memory.ScoreTrend) > 30 {
			stm.Memory.ScoreTrend = stm.Memory.ScoreTrend[len(stm.Memory.ScoreTrend)-30:]
		}
		stm.Memory.JudgedShots += cnt
	}
	// IssueStats 防膨胀:只保留 top 60
	if len(stm.Memory.IssueStats) > 60 {
		type kv struct{ k string; v int }
		var arr []kv
		for k, v := range stm.Memory.IssueStats {
			arr = append(arr, kv{k, v})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
		nw := map[string]int{}
		for i, it := range arr {
			if i >= 60 {
				break
			}
			nw[it.k] = it.v
		}
		stm.Memory.IssueStats = nw
	}
	saveAgentStateLocked(ctx.project, stm)
	if cnt > 0 {
		lg.logf(fmt.Sprintf("🧠 本集审片汇总:%d 镜 · 均分 %.0f(学习档案已更新)", cnt, total/float64(cnt)))
	}
	return nil
}

// agentJudgeAndRework 智能模式 qc 阶段主体:机械质检 → 审片 → 返工闭环 → 升级。
// 永不因个别镜头失败中断整集(例外升级给人,成片继续合成)。
func agentJudgeAndRework(ctx *manjuCtx, lg *manjuLogger, acfg agent.Config) error {
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if entries, err := os.ReadDir(clipsEp); err != nil || len(entries) == 0 {
		lg.logf("  ⏭ 该集无镜头可审片，跳过")
		return nil
	}
	plan, shots, err := ctx.ensurePlanAndPrompts(lg)
	if err != nil {
		return err
	}
	selected := ctx.selectedShots(shots)
	if len(selected) == 0 {
		lg.logf("  ⏭ 没有需要处理的镜头")
		return nil
	}
	qcBad, _ := ctx.runQCJSON(lg, clipsEp, "")
	// ASR 台词核对:有台词镜头本地转写比对(不符进失败集触发返工;不可用自动降级)
	planPath := filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")
	if asrBad := ctx.runASRCheck(lg, clipsEp, selected, planPath); len(asrBad) > 0 {
		if qcBad == nil {
			qcBad = asrBad
		} else {
			for id, flags := range asrBad {
				qcBad[id] = append(qcBad[id], flags...)
			}
		}
	}
	lg.logf(fmt.Sprintf("🤖 审片官开始判分: %d 镜(视觉模型 %s)", len(selected), orDefault(acfg.VisionModel, "未配置→仅机械质检")))
	failed := ctx.judgeShots(lg, acfg, plan, selected, qcBad, clipsEp)

	// 返工闭环:修复提示词 → 删旧产物 → 定点重编码重渲染 → 复审(预算封顶)
	round := 0
	for len(failed) > 0 && round < acfg.MaxRetries && acfg.VisionReady() && !lg.stopped() {
		round++
		ids := make([]string, 0, len(failed))
		var fixTargets []manjuShot
		for _, s := range selected {
			for _, id := range failed {
				if s.ID == id {
					fixTargets = append(fixTargets, s)
					ids = append(ids, strconv.Itoa(s.ID))
				}
			}
		}
		lg.logf(fmt.Sprintf("🔧 返工第 %d/%d 轮: 镜头 %s", round, acfg.MaxRetries, strings.Join(ids, ",")))
		charMap, sceneMap := planCharSceneMaps(plan)
		for _, s := range fixTargets {
			jd := loadAgentStateShot(ctx.project, s.ID)
			np, ferr := agent.FixPrompt(manjuAgentLLM{ctx.llm}, shotMetaFromPlan(s, charMap, sceneMap, manjuStyleDesc(ctx.style).asset), s.H3Prompt, jd)
			if ferr != nil {
				lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 修复师失败(按原提示词重渲染): " + truncate(ferr.Error(), 120))
			} else {
				if err := ctx.updateShotPrompt(s, np); err != nil {
					lg.logf("  ⚠️ 镜头 " + strconv.Itoa(s.ID) + " 提示词回写失败: " + err.Error())
				} else {
					s.H3Prompt = np
					lg.logf(fmt.Sprintf("  ✏️ 镜头 %d 提示词已修复(%d 字)", s.ID, len([]rune(np))))
				}
			}
			ctx.clearShotArtifacts(s)
			bumpAgentRetries(ctx.project, s.ID)
		}
		// 定点重跑编码+渲染(临时收窄 only)
		prevOnly := ctx.only
		ctx.only = strings.Join(ids, ",")
		lg.logStage("encode")
		if err := stageEncode(ctx, lg); err != nil {
			ctx.only = prevOnly
			return fmt.Errorf("返工预编码失败: %w", err)
		}
		lg.logStage("render")
		if err := stageRender(ctx, lg); err != nil {
			ctx.only = prevOnly
			return fmt.Errorf("返工渲染失败: %w", err)
		}
		ctx.only = prevOnly
		// 复审(只审本轮重做的镜头)
		plan2, shots2, err := ctx.ensurePlanAndPrompts(lg)
		if err != nil {
			return err
		}
		var redoShots []manjuShot
		for _, s := range shots2 {
			for _, id := range failed {
				if s.ID == id {
					redoShots = append(redoShots, s)
				}
			}
		}
		qcBad2, _ := ctx.runQCJSON(lg, clipsEp, "")
		failed = ctx.judgeShots(lg, acfg, plan2, redoShots, qcBad2, clipsEp)
	}

	// 升级:预算耗尽仍未通过的镜头 → 推送 + 状态记录(成片照常合成,人再拍板)
	if len(failed) > 0 {
		sort.Ints(failed)
		parts := make([]string, 0, len(failed))
		for _, id := range failed {
			jd := loadAgentStateShot(ctx.project, id)
			reason := strings.Join(jd.Issues, ";")
			if reason == "" {
				reason = strings.Join(jd.QCFlags, ";")
			}
			if reason == "" {
				reason = "判分未达标"
			}
			escalateShot(ctx, lg, id, jd.Score, reason, acfg)
			parts = append(parts, fmt.Sprintf("镜%d(%.0f分)", id, jd.Score))
		}
		lg.logf("⚠️ 已升级待人拍板: " + strings.Join(parts, "、") + "(工作台「审片报告」可重试/忽略)")
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · ⚠️ %d 个镜头审片未达标已升级: %s(重试 %d 轮耗尽)",
			ctx.project, ctx.episode, len(failed), strings.Join(parts, "、"), acfg.MaxRetries))
	} else {
		lg.logf("🎉 审片全部通过")
	}
	// 学习记忆:本轮判分 → 高频问题统计 + 均分趋势 + 返工次数
	manjuAgentMu.Lock()
	stm := loadAgentStateLocked(ctx.project)
	stm.Memory.ReworkCount += round
	total, cnt := 0.0, 0
	for _, j := range stm.Shots {
		if j.Score > 0 {
			total += j.Score
			cnt++
		}
		for _, is := range j.Issues {
			is = strings.TrimSpace(is)
			if is != "" {
				stm.Memory.IssueStats[is]++
			}
		}
	}
	if cnt > 0 {
		stm.Memory.ScoreTrend = append(stm.Memory.ScoreTrend, manjuScorePoint{
			Episode: ctx.episode, Score: total / float64(cnt), Count: cnt, At: time.Now().Unix()})
		if len(stm.Memory.ScoreTrend) > 30 {
			stm.Memory.ScoreTrend = stm.Memory.ScoreTrend[len(stm.Memory.ScoreTrend)-30:]
		}
		stm.Memory.JudgedShots += cnt
	}
	// IssueStats 防膨胀:只保留 top 60
	if len(stm.Memory.IssueStats) > 60 {
		type kv struct{ k string; v int }
		var arr []kv
		for k, v := range stm.Memory.IssueStats {
			arr = append(arr, kv{k, v})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
		nw := map[string]int{}
		for i, it := range arr {
			if i >= 60 {
				break
			}
			nw[it.k] = it.v
		}
		stm.Memory.IssueStats = nw
	}
	saveAgentStateLocked(ctx.project, stm)
	manjuAgentMu.Unlock()
	return nil
}

// loadAgentStateShot 读某镜头最新结论(无则空 Judgment)
func loadAgentStateShot(project string, shotID int) *agent.Judgment {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	st := loadAgentStateLocked(project)
	if j := st.Shots[strconv.Itoa(shotID)]; j != nil {
		cp := *j
		return &cp
	}
	return &agent.Judgment{}
}

// bumpAgentRetries 返工轮数 +1
func bumpAgentRetries(project string, shotID int) {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	st := loadAgentStateLocked(project)
	if j := st.Shots[strconv.Itoa(shotID)]; j != nil {
		j.Retries++
		saveAgentStateLocked(project, st)
	}
}

// escalateShot 记录升级(同镜未解决的升级只更新不重复)
func escalateShot(ctx *manjuCtx, lg *manjuLogger, shotID int, score float64, reason string, acfg agent.Config) {
	manjuAgentMu.Lock()
	st := loadAgentStateLocked(ctx.project)
	st.Episode = ctx.episode
	found := false
	for i := range st.Escalations {
		e := &st.Escalations[i]
		if e.EP == ctx.episode && e.Shot == shotID && !e.Resolved {
			e.Score, e.Reason, e.At = score, reason, time.Now().Unix()
			found = true
			break
		}
	}
	if !found {
		st.Escalations = append(st.Escalations, manjuAgentEscalation{
			EP: ctx.episode, Shot: shotID, Score: score, Reason: reason, At: time.Now().Unix(),
		})
	}
	saveAgentStateLocked(ctx.project, st)
	manjuAgentMu.Unlock()
}

// ---- 手动操作:重审 / 升级处理 ----

// manjuAgentJudgeOne 手动重审一个已有镜头(同步,前端等结果)
func manjuAgentJudgeOne(configPath, episode string, shotID int) (*agent.Judgment, error) {
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		return nil, err
	}
	acfg := loadAgentCfg(ctx)
	if !acfg.VisionReady() {
		return nil, fmt.Errorf("未配置视觉模型(设置弹窗「智能体」里填写)")
	}
	clip := filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", shotID))
	if !fileExists(clip) {
		return nil, fmt.Errorf("镜头 %d 尚未渲染", shotID)
	}
	plan, shots, err := ctx.ensurePlanAndPrompts(&manjuLogger{state: manjuState})
	if err != nil {
		return nil, err
	}
	var target []manjuShot
	for _, s := range shots {
		if s.ID == shotID {
			target = append(target, s)
		}
	}
	if len(target) == 0 {
		return nil, fmt.Errorf("方案中无镜头 %d", shotID)
	}
	lg := &manjuLogger{state: manjuState, proj: ctx.project, ep: ctx.episode}
	_ = ctx.judgeShots(lg, acfg, plan, target, nil, filepath.Join(ctx.clipsDir, ctx.episode))
	return loadAgentStateShot(ctx.project, shotID), nil
}

// manjuAgentReworkRun 定点返工一个镜头(升级卡「重试」触发,后台任务)
// 流程:修复提示词 → 重编码重渲染 → 复审;通过自动解除升级。
func manjuAgentReworkRun(w http.ResponseWriter, configPath, episode string, shotID int) {
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"已有任务运行中，先停止"}`, http.StatusConflict)
		return
	}
	projName := filepath.Base(filepath.Dir(configPath))
	manjuState.running = true
	manjuState.stage = "render"
	manjuState.log = ""
	manjuState.rc = nil
	manjuState.done = false
	manjuState.started = time.Now()
	manjuState.baseElapsed = 0
	manjuState.stopped = false
	manjuState.project = projName
	manjuState.episode = episode
	manjuState.mu.Unlock()
	writeManjuDiskState(projName, &manjuDiskState{Running: true, Stage: "render", StartedAt: time.Now().Unix(), Episode: episode})
	_ = os.WriteFile(manjuRunLogPath(projName), nil, 0644)

	go func() {
		rc := 1
		runFile, _ := os.OpenFile(manjuRunLogPath(projName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		defer func() {
			if runFile != nil {
				_ = runFile.Close()
			}
			manjuFinish(rc)
		}()
		ctx, err := newManjuCtx(configPath, episode, "", strconv.Itoa(shotID), "")
		if err != nil {
			return
		}
		lg := newManjuLogger(manjuState, runFile, ctx.project, ctx.episode)
		acfg := loadAgentCfg(ctx)
		// 返工序号进 seed 策略(increment/random 时定点返工也换 seed)
		ctx.forceAttempt = loadAgentStateShot(ctx.project, shotID).Retries + 1
		lg.logf(fmt.Sprintf("🤖 定点返工: 镜头 %d(修复提示词 → 重编码 → 重渲染 → 复审)", shotID))
		plan, shots, err := ctx.ensurePlanAndPrompts(lg)
		if err != nil {
			lg.logf("❌ 读取方案失败: " + err.Error())
			return
		}
		var target manjuShot
		found := false
		for _, s := range shots {
			if s.ID == shotID {
				target, found = s, true
			}
		}
		if !found {
			lg.logf("❌ 方案中无镜头 " + strconv.Itoa(shotID))
			return
		}
		jd := loadAgentStateShot(ctx.project, shotID)
		if strings.TrimSpace(target.H3Prompt) != "" && acfg.VisionReady() {
			charMap, sceneMap := planCharSceneMaps(plan)
			if np, ferr := agent.FixPrompt(manjuAgentLLM{ctx.llm}, shotMetaFromPlan(target, charMap, sceneMap, manjuStyleDesc(ctx.style).asset), target.H3Prompt, jd); ferr == nil {
				if err := ctx.updateShotPrompt(target, np); err == nil {
					lg.logf("  ✏️ 提示词已按审片意见修复")
				}
			}
		}
		ctx.clearShotArtifacts(target)
		bumpAgentRetries(ctx.project, shotID)
		lg.logStage("encode")
		if err := stageEncode(ctx, lg); err != nil {
			lg.logf("❌ 预编码失败: " + err.Error())
			return
		}
		lg.logStage("render")
		if err := stageRender(ctx, lg); err != nil {
			lg.logf("❌ 渲染失败: " + err.Error())
			return
		}
		if acfg.VisionReady() {
			plan2, shots2, _ := ctx.ensurePlanAndPrompts(lg)
			var redo []manjuShot
			for _, s := range shots2 {
				if s.ID == shotID {
					redo = append(redo, s)
				}
			}
			if failed := ctx.judgeShots(lg, acfg, plan2, redo, nil, filepath.Join(ctx.clipsDir, ctx.episode)); len(failed) == 0 {
				resolveEscalation(ctx.project, ctx.episode, shotID, "retry-passed")
				lg.logf("🎉 镜头 " + strconv.Itoa(shotID) + " 返工通过,升级已解除")
			} else {
				escalateShot(ctx, lg, shotID, loadAgentStateShot(ctx.project, shotID).Score, "定点返工仍未达标", acfg)
			}
		} else {
			resolveEscalation(ctx.project, ctx.episode, shotID, "retry-rendered")
			lg.logf("✅ 镜头 " + strconv.Itoa(shotID) + " 已重渲染(未配置视觉模型,跳过复审)")
		}
		rc = 0
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// resolveEscalation 解除一条升级
func resolveEscalation(project, episode string, shotID int, action string) {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	st := loadAgentStateLocked(project)
	for i := range st.Escalations {
		e := &st.Escalations[i]
		if e.EP == episode && e.Shot == shotID && !e.Resolved {
			e.Resolved = true
			e.Action = action
		}
	}
	if j := st.Shots[strconv.Itoa(shotID)]; j != nil && action == "ignore" {
		j.Status = "accepted"
	}
	saveAgentStateLocked(project, st)
}

// ---- HTTP 端点 ----

func registerAgentRoutes(mux *http.ServeMux) {
	// 深度分析小说内容 → 推荐并更新渲染风格(智能一条龙「是」分支)
	mux.HandleFunc("POST /api/manju/agent/style", manjuAgentStyleAnalyze)
	// 项目体检 / 一键修复 / 自然语言指令
	mux.HandleFunc("GET /api/manju/agent/health", manjuHealth)
	mux.HandleFunc("POST /api/manju/agent/health/fix", manjuHealthFix)
	mux.HandleFunc("POST /api/manju/agent/chat", manjuAgentChat)
	// 审片状态 + 配置读取(key 打码)
	mux.HandleFunc("GET /api/manju/agent", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		if configPath == "" {
			http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
			return
		}
			res := agentStatusSummary(configPath)
			if ctx, err := newManjuCtx(configPath, "", "", "", ""); err == nil {
				acfg := loadAgentCfg(ctx)
				masked := ""
				if len(acfg.VisionAPIKey) > 9 {
					masked = acfg.VisionAPIKey[:5] + "…" + acfg.VisionAPIKey[len(acfg.VisionAPIKey)-4:]
				}
				res["hasVisionKey"] = acfg.VisionAPIKey != ""
				res["visionKeyMasked"] = masked
				res["visionBaseUrl"] = acfg.VisionBaseURL
				res["agentEnabled"] = acfg.Enabled
				// 云端 2K Key(项目 render 节优先,回退全局 server/settings.json;掩码展示)
				if mmKey := manjuMinimaxKey(ctx); mmKey != "" {
					res["hasMinimaxKey"] = true
					res["minimaxKeyMasked"] = "已配置"
					if len(mmKey) > 9 {
						res["minimaxKeyMasked"] = mmKey[:5] + "…" + mmKey[len(mmKey)-4:]
					}
				}
			} else {
				// 项目缺失(目录被删/未创建):不整体失败——仍返回全局默认,前端展示"项目缺失,按全局配置"
				res["projectMissing"] = true
				res["visionBaseUrl"] = manjuGlobalAgent.VisionBaseURL
				res["agentEnabled"] = manjuGlobalAgent.Enabled
			}
			// 全局默认(settings.json agent 节):前端展示"项目未配置时使用全局默认"(项目缺失时也返回,避免整块视觉区空白)
			res["globalDefaults"] = map[string]any{
				"enabled": manjuGlobalAgent.Enabled, "visionModel": manjuGlobalAgent.VisionModel,
				"visionBaseUrl": manjuGlobalAgent.VisionBaseURL,
				"passScore":     manjuGlobalAgent.PassScore, "maxRetries": manjuGlobalAgent.MaxRetries,
				"hasVisionKey": manjuGlobalAgent.VisionAPIKey != "",
			}
			writeJSON(w, http.StatusOK, res)
	})

		// 保存智能体配置:默认写入项目 config.json 的 agent 节;global=true 时另存为全局默认
		// (settings.json agent 节,所有项目共用,项目未配置时生效)
		mux.HandleFunc("POST /api/manju/agent/settings", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			configPath := str(body["config"])
			m, _ := body["agent"].(map[string]any)
			// 表单值 → agent 节(非空覆盖;全局分支用同一套提取,不依赖项目 config)
			fillAgentFields := func(A map[string]any) {
				if m == nil {
					return
				}
				if b, ok := m["enabled"].(bool); ok {
					A["enabled"] = b
				}
				for _, k := range []string{"vision_base_url", "vision_api_key", "vision_model"} {
					if v := strings.TrimSpace(str(m[k])); v != "" {
						A[k] = v
					}
				}
				if v, ok := manjuToFloat(m["pass_score"]); ok && v > 0 && v <= 100 {
					A["pass_score"] = v
				}
				if n, ok := manjuToInt(m["max_retries"]); ok && n >= 0 && n <= 4 {
					A["max_retries"] = n
				}
			}
			if str(body["global"]) == "true" {
				// 另存为全局默认:写 settings.json 的 agent 节(Key 加密存储),并刷新内存默认。
				// 不依赖项目 config——项目目录缺失/未创建时也能另存为全局默认(之前会 400,导致"全局默认是摆设")
				if manjuSettingsStore == nil {
					http.Error(w, `{"error":"全局设置存储不可用"}`, http.StatusInternalServerError)
					return
				}
				A := map[string]any{}
				fillAgentFields(A)
				g, err := manjuSettingsStore.Load()
				if err != nil {
					http.Error(w, `{"error":"读取全局设置失败: `+err.Error()+`"}`, http.StatusInternalServerError)
					return
				}
				ga := &config.AgentSettings{
					Enabled:       A["enabled"] == true,
					VisionBaseURL: str(A["vision_base_url"]),
					VisionModel:   str(A["vision_model"]),
				}
				if v, ok := manjuToFloat(A["pass_score"]); ok && v > 0 {
					ga.PassScore = v
				}
				if n, ok := manjuToInt(A["max_retries"]); ok {
					ga.MaxRetries = n
				}
				// Key:项目已填则同步为全局默认;否则保留全局旧值(避免空值清掉已存的默认 Key)
				if k := strings.TrimSpace(str(m["vision_api_key"])); k != "" {
					ga.VisionAPIKey = k
				} else if g.Agent != nil {
					ga.VisionAPIKey = g.Agent.VisionAPIKey
				}
				g.Agent = ga
				if err := manjuSettingsStore.Save(g); err != nil {
					http.Error(w, `{"error":"保存全局默认失败: `+err.Error()+`"}`, http.StatusInternalServerError)
					return
				}
				SetGlobalAgentCfg(g)
				// 云端 2K Key 全局默认:与 DeepSeek 默认 Key 同处(server/settings.json 明文,须合并写不覆盖)
				if k := strings.TrimSpace(str(body["minimax_api_key"])); k != "" {
					def := map[string]any{}
					if b, err := os.ReadFile(manjuSettingsFile); err == nil {
						_ = json.Unmarshal(b, &def)
					}
					def["minimax_api_key"] = k
					_ = writeManjuSettings(def)
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "global": true})
				return
			}
			if configPath == "" {
				http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
				return
			}
			cfg, err := readManjuConfig(configPath)
			if err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
				return
			}
			A, _ := cfg["agent"].(map[string]any)
			if A == nil {
				A = map[string]any{}
			}
			fillAgentFields(A)
			cfg["agent"] = A
			// 云端 2K Key(项目级):非空才写 render 节(空值不清已有 Key)
			if k := strings.TrimSpace(str(body["minimax_api_key"])); k != "" {
				RN, _ := cfg["render"].(map[string]any)
				if RN == nil {
					RN = map[string]any{}
					cfg["render"] = RN
				}
				RN["minimax_api_key"] = k
			}
			if err := writeManjuConfig(configPath, cfg); err != nil {
				http.Error(w, `{"error":"保存失败: `+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		})

	// 手动重审一个镜头(同步返回结论)
	mux.HandleFunc("POST /api/manju/agent/judge", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		episode := orDefault(str(body["episode"]), "EP01")
		shotID, ok := manjuToInt(body["shot"])
		if configPath == "" || !ok {
			http.Error(w, `{"error":"missing config/shot"}`, http.StatusBadRequest)
			return
		}
		manjuState.mu.Lock()
		running := manjuState.running
		manjuState.mu.Unlock()
		if running {
			http.Error(w, `{"error":"任务运行中,结束后再重审"}`, http.StatusConflict)
			return
		}
		jd, err := manjuAgentJudgeOne(configPath, episode, shotID)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "judgment": jd})
	})

	// 升级处理:retry(定点返工,后台任务) / ignore(接受现状)
	mux.HandleFunc("POST /api/manju/agent/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		episode := orDefault(str(body["episode"]), "EP01")
		shotID, ok := manjuToInt(body["shot"])
		action := str(body["action"])
		if configPath == "" || !ok || (action != "retry" && action != "ignore") {
			http.Error(w, `{"error":"missing config/shot/action(retry|ignore)"}`, http.StatusBadRequest)
			return
		}
		project := filepath.Base(filepath.Dir(configPath))
		if action == "ignore" {
			resolveEscalation(project, episode, shotID, "ignore")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		manjuAgentReworkRun(w, configPath, episode, shotID)
	})

	// 产物删除:file=删单个文件(成片/镜头 mp4);episode=删整集目录(镜头目录+成片)。
	// 安全护栏:目标必须位于该项目工作目录之内,拒绝删工作目录本身。
	mux.HandleFunc("POST /api/manju/output/delete", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		scope := str(body["scope"])
		if configPath == "" || (scope != "file" && scope != "episode") {
			http.Error(w, `{"error":"missing config/scope(file|episode)"}`, http.StatusBadRequest)
			return
		}
		ctx, err := newManjuCtx(configPath, str(body["episode"]), "", "", "")
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		wd := filepath.Clean(ctx.workdir)
		removed := []string{}
		if scope == "file" {
			p := filepath.Clean(str(body["path"]))
			if p == "" || !strings.HasPrefix(p, wd+string(filepath.Separator)) {
				http.Error(w, `{"error":"路径不在项目工作目录内,拒绝删除"}`, http.StatusBadRequest)
				return
			}
			if err := os.Remove(p); err != nil {
				http.Error(w, `{"error":"删除失败: `+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			removed = append(removed, filepath.Base(p))
		} else {
			ep := orDefault(ctx.episode, "EP01")
			epDir := filepath.Join(ctx.clipsDir, ep)
			if filepath.Clean(epDir) != wd {
				if err := os.RemoveAll(epDir); err == nil {
					removed = append(removed, epDir)
				} else {
					http.Error(w, `{"error":"删除集目录失败: `+err.Error()+`"}`, http.StatusInternalServerError)
					return
				}
			}
			final := filepath.Join(ctx.workdir, ep+"_成片.mp4")
			if fileExists(final) {
				_ = os.Remove(final)
				removed = append(removed, filepath.Base(final))
			}
			trailer := filepath.Join(ctx.workdir, ep+"_预告片.mp4")
			if fileExists(trailer) {
				_ = os.Remove(trailer)
				removed = append(removed, filepath.Base(trailer))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	})

	// 预告片自动剪辑:审片分数选镜头(高分优先+剧本关键位),本地合成 30s 预告
	mux.HandleFunc("POST /api/manju/trailer", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		episode := orDefault(str(body["episode"]), "EP01")
		target := 30.0
		if v, ok := manjuToFloat(body["target"]); ok && v >= 10 && v <= 120 {
			target = v
		}
		if configPath == "" {
			http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
			return
		}
		ctx, err := newManjuCtx(configPath, episode, "", "", "")
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
		if entries, err := os.ReadDir(clipsEp); err != nil || len(entries) == 0 {
			http.Error(w, `{"error":"该集没有已渲染镜头,先跑渲染"}`, http.StatusBadRequest)
			return
		}
		// 选镜:审片分数降序;无审片数据时按剧本位置(开场/中段/结尾各取)
		st := loadAgentState(ctx.project)
		type cand struct {
			name  string
			score float64
		}
		var cands []cand
		if entries, err := os.ReadDir(clipsEp); err == nil {
			for _, e := range entries {
				n := e.Name()
				if !strings.HasSuffix(strings.ToLower(n), ".mp4") {
					continue
				}
				idStr := strings.TrimSuffix(n, ".mp4")
				id, err := strconv.Atoi(idStr)
				if err != nil {
					continue
				}
				score := 0.0
				if j := st.Shots[strconv.Itoa(id)]; j != nil && j.Score > 0 {
					score = j.Score
				}
				cands = append(cands, cand{n, score})
			}
		}
		if len(cands) == 0 {
			http.Error(w, `{"error":"无可用镜头"}`, http.StatusBadRequest)
			return
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
		// 预告片镜头数:目标 30s/每镜 4s ≈ 8 镜上限
		maxN := int(target/4) + 1
		if maxN > len(cands) {
			maxN = len(cands)
		}
		listPath := filepath.Join(os.TempDir(), fmt.Sprintf("manju_trailer_%d.txt", time.Now().UnixNano()))
		defer os.Remove(listPath)
		var lb strings.Builder
		for i := 0; i < maxN; i++ {
			fmt.Fprintf(&lb, "%s %.0f\n", cands[i].name, cands[i].score)
		}
		if err := os.WriteFile(listPath, []byte(lb.String()), 0644); err != nil {
			http.Error(w, `{"error":"写清单失败: `+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		out := filepath.Join(ctx.workdir, ctx.episode+"_预告片.mp4")
		args := []string{"trailer", "--clips-dir", clipsEp, "--out", out, "--list", listPath,
			"--fps", strconv.Itoa(ctx.fps), "--target", fmt.Sprintf("%.0f", target),
			"--plan", filepath.Join(ctx.analysisDir, ctx.episode+"_direct_plan.json")}
		if _, err := ctx.runMediaOut(args...); err != nil {
			http.Error(w, `{"error":"预告片合成失败: `+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": out, "shots": maxN})
	})

	// 视觉模型连通测试(拿项目第一张定妆照问一句话)
	mux.HandleFunc("POST /api/manju/agent/vision-test", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		if configPath == "" {
			http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
			return
		}
		ctx, err := newManjuCtx(configPath, "", "", "", "")
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		acfg := loadAgentCfg(ctx)
		if !acfg.VisionReady() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "未填写视觉模型名"})
			return
		}
		vc := ctx.visionClient(acfg)
		testImg := ""
		entries, _ := os.ReadDir(filepath.Join(ctx.assetsDir, "characters"))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") && !strings.Contains(e.Name(), "_face") {
				testImg = filepath.Join(ctx.assetsDir, "characters", e.Name())
				break
			}
		}
		if testImg == "" {
			// 没有定妆照也能测连通:生成一张合成测试图(深底白 N)
			p, ierr := syntheticVisionTestImage()
			if ierr != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "生成测试图失败: " + ierr.Error()})
				return
			}
			defer os.Remove(p)
			testImg = p
		}
		out, err := vc.ChatJSON("你是连通测试助手,只输出 JSON,不要输出其它内容。",
			`描述这张图,严格输出 {"desc":"一句话描述"} 格式的 JSON。`, []string{testImg}, 0.1)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		reply := ""
		if len(out) > 0 {
			reply = fmt.Sprint(out)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "visionModel": acfg.VisionModel, "reply": reply})
	})
}

// syntheticVisionTestImage 生成 224×224 深底白 N 测试图(项目无角色定妆照时的连通测试兜底)
func syntheticVisionTestImage() (string, error) {
	const s = 224
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{19, 25, 38, 255}}, image.Point{}, draw.Src)
	white := &image.Uniform{color.RGBA{233, 237, 245, 255}}
	// 三笔构成 N:左竖线 + 对角线 + 右竖线,线宽 18
	thick := func(x0, y0, x1, y1, w int) {
		steps := int(math.Max(math.Abs(float64(x1-x0)), math.Abs(float64(y1-y0)))) * 2
		for i := 0; i <= steps; i++ {
			x := x0 + (x1-x0)*i/steps
			y := y0 + (y1-y0)*i/steps
			draw.Draw(img, image.Rect(x-w/2, y-w/2, x+w/2+1, y+w/2+1), white, image.Point{}, draw.Src)
		}
	}
	thick(70, 60, 70, 164, 18)
	thick(70, 164, 154, 60, 18)
	thick(154, 60, 154, 164, 18)
	f, err := os.CreateTemp("", "nilix_vision_test_*.jpg")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 88}); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
