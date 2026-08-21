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
	"runtime"
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
// 读写须持 manjuGlobalAgentMu(审计 S8:管线 goroutine 与保存设置 HTTP 并发读写)
var (
	manjuGlobalAgentMu sync.RWMutex
	manjuGlobalAgent   = agent.DefaultConfig()
)

// manjuSettingsStore 全局 settings.json 的读写入口(main 注入,「另存为全局默认」用)
var manjuSettingsStore *config.Store

// SetGlobalAgentCfg 由 main/保存设置后调用,刷新全局默认(settings.json agent 节 → agent.Config)
func SetGlobalAgentCfg(cfg *config.Settings) {
	// 入参不带 agent 节(如 PUT /api/settings 只改 LLM)时必须保留旧值——
	// 否则被重置为出厂默认,视觉模型/Key/及格线/返工轮数全部清空
	if cfg == nil || cfg.Agent == nil {
		return
	}
	a := agent.DefaultConfig()
	a.Enabled = cfg.Agent.Enabled
	a.VisionBaseURL = cfg.Agent.VisionBaseURL
	a.VisionAPIKey = cfg.Agent.VisionAPIKey
	a.VisionModel = cfg.Agent.VisionModel
	if cfg.Agent.PassScore > 0 {
		a.PassScore = cfg.Agent.PassScore
	}
	a.MaxRetries = cfg.Agent.MaxRetries
	if cfg.Agent.JudgeConcurrency >= 1 && cfg.Agent.JudgeConcurrency <= 4 {
		a.JudgeConcurrency = cfg.Agent.JudgeConcurrency
	}
	if cfg.Agent.AutoResolve != nil {
		a.AutoResolve = *cfg.Agent.AutoResolve
	}
	a.Normalize()
	manjuGlobalAgentMu.Lock()
	manjuGlobalAgent = a
	manjuGlobalAgentMu.Unlock()
}

// SetManjuSettingsStore 注入全局设置读写(main 调用,供「另存为全局默认」写 settings.json)
func SetManjuSettingsStore(st *config.Store) { manjuSettingsStore = st }

// loadAgentCfg 读取生效的智能体配置:全局默认打底,项目 agent 节非空字段覆盖
func loadAgentCfg(ctx *manjuCtx) agent.Config {
	manjuGlobalAgentMu.RLock()
	acfg := manjuGlobalAgent
	manjuGlobalAgentMu.RUnlock()
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
		if n, ok := manjuToInt(m["judge_concurrency"]); ok && n > 0 {
			acfg.JudgeConcurrency = n
		}
		if b, ok := m["auto_resolve"].(bool); ok {
			acfg.AutoResolve = b
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
		// 审计 M6:base_url 缺省回退 LLM 地址是常见配置坑——GLM 模型名打 DeepSeek 端点必 401,
		// 且难排查(体检只查 model 不查地址匹配)。回退时若模型名明显非 DeepSeek 系,
		// 直接返回 nil 降级跳过判分(不再静默用错端点空转返工)
		ml := strings.ToLower(strings.TrimSpace(acfg.VisionModel))
		if strings.Contains(ml, "glm") || strings.Contains(ml, "qwen-vl") ||
			strings.Contains(ml, "vision") || strings.Contains(ml, "gpt-4o") {
			return nil
		}
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
	if vc == nil {
		// 模型配置非法(拆分后无合法模型名):返回 nil,调用方降级跳过判分(审计 S2)
		return nil
	}
	vc.OnUsage = func(model string, u agent.Usage) { manjuStatsAdd(ctx.project, model, u) }
	return vc
}

// visionClientShared 每 run 共享一个视觉客户端:粘性降级状态跨镜头保留——
// 429 高峰一次降级成功后,后续镜头判分直接从备模型开始(此前每镜新建 client,
// 每镜都要在主模型上重烧 4/10/20s 退避才降级,高峰期审片被拖慢 34s/镜)。
func (ctx *manjuCtx) visionClientShared(acfg agent.Config) *agent.VisionClient {
	ctx.visionOnce.Do(func() { ctx.vision = ctx.visionClient(acfg) })
	return ctx.vision
}

// safeGo 带 panic 兜底的后台 goroutine(审计 S2):崩溃不崩进程——
// 与 startManjuRun 主管线 recover 同策略:记录 crash.log + 状态日志后降级继续。
// 所有管线/判分/ASR/预编码等子 goroutine 必须走此封装(Go 的 recover 只捕获同 goroutine,
// 主 run goroutine 的 defer 对子 goroutine panic 形同虚设)。
func safeGo(tag string, lg *manjuLogger, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 64<<10)
				n := runtime.Stack(buf, false)
				if lg != nil {
					lg.logf(fmt.Sprintf("💥 后台[%s]崩溃(panic): %v\n%s", tag, r, buf[:n]))
				}
				manjuWriteCrash(tag, fmt.Sprintf("%v", r), buf[:n])
			}
		}()
		fn()
	}()
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
	RunCount     int                `json:"runCount"`
	JudgedShots  int                `json:"judgedShots"`
	ReworkCount  int                `json:"reworkCount"`
	IssueStats   map[string]int     `json:"issueStats,omitempty"`
	ScoreTrend   []manjuScorePoint  `json:"scoreTrend,omitempty"`
	StyleChoices []manjuStyleChoice `json:"styleChoices,omitempty"`
	LastRunAt    int64              `json:"lastRunAt"`
	// LastSummarizedEp 最近一次汇总的集号(审计 M3:同一集只汇总一次,
	// 此前每次 qc/续跑都全量再累加一遍,返工计数/问题统计/趋势曲线虚高)
	LastSummarizedEp string `json:"lastSummarizedEp,omitempty"`
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
	Memory      manjuAgentMemory           `json:"memory,omitempty"`    // 学习记忆(跨次运行)
	LastError   *manjuAgentError           `json:"lastError,omitempty"` // 最近一次阶段失败诊断
	UpdatedAt   int64                      `json:"updatedAt"`
}

var manjuAgentMu sync.Mutex

func manjuAgentStatePath(project string) string {
	return filepath.Join(ManjuRootDir, project, "agent_state.json")
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
	_ = atomicWriteJSON(manjuAgentStatePath(project), st)
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
	out["judgeConcurrency"] = acfg.JudgeConcurrency
	out["autoResolve"] = acfg.AutoResolve
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
			"arbiter": j.Arbiter,
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
		manjuSetStage(st) // 实时阶段推进:运行状态/气泡显示当前步骤(agent 模式同)
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
	if st.Episode != ctx.episode {
		// 审计 H9:换集翻页统一在 plan 阶段执行——此前 judgeShots/agentJudgeRemaining 的
		// 换集清空条件被这里提前写入的 Episode 破坏(条件恒假),EP01 旧判分/未解决升级
		// 残留混入 EP02,两集镜头号重叠时相互覆盖。这里集中翻页后,judgeShots 等处的
		// 同类判断因 Episode 已匹配而幂等跳过,不再重复清空
		st.Episode = ctx.episode
		st.Shots = map[string]*agent.Judgment{}
		st.Escalations = nil
	}
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
		} else if len([]rune(t)) <= 30 {
			key = t // 中文/其它语言自定义词原样保留(用户输入的题材词如 东方神话 直接作风格词,
			// 不再忽略——此前会被静默丢掉,「东方神话+东方修仙」输入无效)
		} else {
			notes = append(notes, truncate(t, 20)+"(过长已忽略)")
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

// manjuMergeStyle 合并用户基底风格与 LLM 补充风格:
// 用户基底(预设 key + 自定义词)强制保留且在前,LLM 推荐的新元素追加在后,去重。
// 用户主动选择「是」时绝不替换其预设+自定义风格,只在其上补充题材元素。
func manjuMergeStyle(base, recommend string) string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range strings.FieldsFunc(base+"+"+recommend, func(r rune) bool { return r == ',' || r == '+' || r == '、' || r == '/' || r == '|' || r == '，' }) {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return "2.5d"
	}
	return strings.Join(out, "+")
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
	sys := `你是漫剧(竖屏短剧)渲染风格与参数分析师,根据小说章节内容判断最匹配的渲染风格与渲染参数。
【铁律】用户当前风格是用户主动选择的基底,必须完整保留、绝不能替换或丢弃;你的任务是在此基底上补充题材元素词,让风格更贴合本章节。
【分析要点】题材类型(古装/现代/玄幻/科幻/都市/悬疑…)、叙事基调(热血/治愈/暗黑/甜宠…)、场景与美术特征、目标观众画风偏好、节奏密度(对话交锋/转场频率)。
【输出 JSON(严格)】{"style": "...", "reason": "...", "params": {...}}
style 取值规则(基底 + 补充,禁止替换基底):
- 开头必须原样包含用户当前基底风格(用户输入的全部元素词,一个不丢)
- 在其后追加 2-4 个题材元素词(取材于小说内容,英文短语):时代/文化氛围(如 ancient Chinese aesthetic / cyberpunk / steampunk)、美术质感(如 watercolor / oil painting / film grain)、光影气质(如 moody cinematic lighting / bright pastel);语义冲突的组合不要
- 整体用 + 连接(如 ink+ancient Chinese aesthetic+watercolor / 2.5d+cyberpunk+neon lighting),总元素不超过 7 个
- 所有新增题材元素词必须是英文(H3 提示词直接使用),中文风格词自行翻译
params 取值规则(渲染优化参数,按题材节奏判断,全部给出):
{"res_tier": "standard", "draft_judge": true, "seed_policy": "increment", "transition": "cut", "shots_per_take": 1}
- res_tier 分辨率档位:常规成片 standard;快速试片/预告优先 draft(约 1/3 像素量);高清大片质感 fhd
- draft_judge 草稿预审:审片返工轮半分辨率草稿、通过后全分辨率定稿(审片轮提速约 3/4),常规推荐 true
- seed_policy 返工 seed 策略:increment=每轮返工换 seed 更有效(推荐);fixed=全剧严格同 seed
- transition 镜头转场:快节奏打脸/爽点短剧 cut(硬切利落);连续叙事/情感递进 dissolve(叠化);古风/意境/回忆 fade(闪黑)
- shots_per_take 多切点长镜(实验特性):保守 1;同场景对话交锋密集、镜头多机位切换的可给 2
reason: 不超过 100 字中文,说明在用户基底风格上补充了哪些元素、与题材/基调的匹配理由。`
	baseDesc := old
	if strings.TrimSpace(old) == "" {
		baseDesc = "(未设置,直接按题材推荐 3-5 个元素)"
	}
	user := "用户当前基底风格(必须保留,为空则直接推荐): " + baseDesc + "\n需渲染章节: " + ctx.chapters + " / 集 " + ctx.episode + "\n\n小说章节内容(节选):\n" + truncate(text, 12000)
	out, err := ctx.llm.chatJSON(sys, user, 0.3)
	if err != nil {
		return nil, fmt.Errorf("深度分析失败: %w", err)
	}
	recommend, notes := manjuNormalizeStyle(str(out["style"]))
	if recommend == "" {
		return nil, fmt.Errorf("模型未给出有效风格,请重试")
	}
	reason := str(out["reason"])
	if notes != "" {
		if reason != "" {
			reason += ";"
		}
		reason += notes
	}
	// 合并:用户基底风格强制保留 + LLM 补充元素(去重,保持用户基底在前)
	style := manjuMergeStyle(old, recommend)
	ctx.cfg["style"] = style
	// 参数建议:合法值校验后写 render 节(AI 一条龙全权:渲染参数一并分析落库,启动日志明示)
	params := map[string]any{}
	if pm, ok := out["params"].(map[string]any); ok {
		RN, _ := ctx.cfg["render"].(map[string]any)
		if RN == nil {
			RN = map[string]any{}
			ctx.cfg["render"] = RN
		}
		if t := str(pm["res_tier"]); t != "" {
			if _, ok2 := manjuResTiers[t]; ok2 {
				RN["res_tier"] = t
				params["档位"] = t
			}
		}
		if v, ok2 := pm["draft_judge"].(bool); ok2 {
			RN["draft_judge"] = v
			if v {
				params["草稿预审"] = "开"
			} else {
				params["草稿预审"] = "关"
			}
		}
		if sp := str(pm["seed_policy"]); sp != "" && manjuSeedPolicies[sp] {
			RN["seed_policy"] = sp
			params["seed策略"] = sp
		}
		if tr := str(pm["transition"]); tr != "" && manjuTransitions[tr] {
			RN["transition"] = tr
			params["转场"] = tr
		}
		if n, ok2 := manjuToInt(pm["shots_per_take"]); ok2 && n >= 1 && n <= 3 {
			RN["shots_per_take"] = n
			if n > 1 {
				params["长镜"] = fmt.Sprintf("%d镜/组", n)
			}
		}
	}
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
	// added = 合并后新增的元素(基底中不存在的部分),便于前端明示"补充了哪些"
	added := styleAdded(old, style)
	return map[string]any{"ok": true, "style": style, "old": old, "added": added, "reason": reason, "params": params}, nil
}

// styleAdded 返回合并后相对基底新增的元素(按 + 分段,基底中已含的不计)。
func styleAdded(base, merged string) string {
	have := map[string]bool{}
	for _, t := range strings.FieldsFunc(base, func(r rune) bool { return r == '+' || r == ',' || r == '、' }) {
		t = strings.TrimSpace(t)
		if t != "" {
			have[strings.ToLower(t)] = true
		}
	}
	out := []string{}
	for _, t := range strings.FieldsFunc(merged, func(r rune) bool { return r == '+' || r == ',' || r == '、' }) {
		t = strings.TrimSpace(t)
		if t != "" && !have[strings.ToLower(t)] {
			out = append(out, t)
		}
	}
	return strings.Join(out, "+")
}

// manjuAgentStyleAnalyze 深度分析小说章节 → LLM 推荐渲染风格与参数 → 写入 config.json。
// 返回旧/新风格、推荐理由与参数变更,由前端决定是否继续走 AI 一条龙。
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
	// 审计 F4:深度分析会写回 config.json,config 必须过 guard
	if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	} else {
		configPath = cp
	}
	res, err := manjuStyleAnalyzeRun(configPath, str(body["episode"]), str(body["chapters"]), str(body["novel"]))
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "深度分析失败") || strings.Contains(err.Error(), "未给出有效风格") ||
			strings.Contains(err.Error(), "写入渲染配置失败") {
			code = http.StatusInternalServerError
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- 机械质检(JSON 报告) ----

// whisperModelReady faster-whisper small 是否已缓存(HuggingFace hub 目录)
func whisperModelReady() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, pat := range []string{"faster-whisper-small", "faster-whisper-base"} {
		p := filepath.Join(home, ".cache", "huggingface", "hub", "models--Systran--"+pat)
		if fileExists(filepath.Join(p, "refs", "main")) {
			return true
		}
		entries, err := os.ReadDir(p)
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

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
	// whisper 首次下载提示:模型未缓存时明确告知(460MB 需几分钟且无进度),避免用户以为卡死
	if !whisperModelReady() {
		lg.logf(fmt.Sprintf("🎤 ASR 台词核对: %d 个有台词镜头——⚠️ faster-whisper small 模型尚未下载(约 460MB,首次运行需几分钟,下载期间无进度条,请耐心等待;之后全离线)", len(spoken)))
	} else {
		lg.logf(fmt.Sprintf("🎤 ASR 台词核对: %d 个有台词镜头(whisper small 已就绪)", len(spoken)))
	}
	ids := make([]string, 0, len(spoken))
	for _, s := range spoken {
		ids = append(ids, strconv.Itoa(s.ID))
	}
	out, err := ctx.runMediaOutStop([]string{"asr", "--dir", clipsEp, "--plan", planPath, "--shots", strings.Join(ids, ",")}, lg.stopped)
	if err != nil {
		// 停止触发的返回不告警(正常流程);其他错误提示后忽略继续
		if !strings.Contains(err.Error(), "已停止") {
			lg.logf("  ⚠️ ASR 核对不可用(忽略,继续视觉判分): " + truncate(err.Error(), 120))
		}
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
	return ctx.runMediaOutStop(args, nil)
}

// runMediaOutStop 同 runMediaOut,但支持停止感知(stopped 回调非空时,用户点「停止」立即杀子进程,
// 不再等 25 分钟超时——ASR whisper 转写/质检 PyAV 卡住时停止必须有效)
func (ctx *manjuCtx) runMediaOutStop(args []string, stopped func() bool) (string, error) {
	script := ensureMediaHelper()
	cmd := exec.Command(manjuPythonPath(), append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// 审计 M2:子进程超时——此前 cmd.Wait 无限阻塞,whisper 首次下载/PyAV 坏文件/ffmpeg 死锁
	// 时整条 AI 一条龙永久挂死,停止也无效(只能杀进程);停止感知:500ms 粒度检查,立即杀
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timeout := time.NewTimer(25 * time.Minute)
	defer timeout.Stop()
	stopTick := time.NewTicker(500 * time.Millisecond)
	defer stopTick.Stop()
	for {
		select {
		case err := <-done:
			return out.String(), err
		case <-timeout.C:
			_ = cmd.Process.Kill()
			<-done
			return out.String(), fmt.Errorf("媒体子进程超时(>25 分钟),已终止")
		case <-stopTick.C:
			if stopped != nil && stopped() {
				_ = cmd.Process.Kill()
				<-done
				return out.String(), fmt.Errorf("已停止")
			}
		}
	}
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
	if _, err := ctx.runMediaOutStop(args, lg.stopped); err != nil {
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

// inspectShot 审片单进程:一遍解码同时产出机械质检报告+抽帧 JPEG(替代原先 qc+frames
// 两个独立进程、同一文件解两遍)。返回 (frames, qcFlags, err)。
// 抽帧缓存复用:同一镜头产物未变(比对源 mp4 的 mtime/大小标记文件)时直接复用上次抽帧,
// 手动重审/终检重审不再重复解码视频(每镜省一次完整解码;产物变化自动失效重抽)。
func (ctx *manjuCtx) inspectShot(lg *manjuLogger, clip string, shotID, count int) ([]string, []string, error) {
	dir := ctx.shotFramesDir(shotID)
	mark := filepath.Join(dir, "_src.meta")
	// 缓存命中条件:标记文件存在且记录的源 mp4 指纹(大小+mtime)与当前一致 → 复用已有帧
	if b, err := os.ReadFile(mark); err == nil && fileExists(clip) {
		if fi, err2 := os.Stat(clip); err2 == nil {
			cur := fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())
			if string(b) == cur {
				var frames []string
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					if strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
						frames = append(frames, filepath.Join(dir, e.Name()))
					}
				}
				if len(frames) > 0 {
					return frames, nil, nil
				}
			}
		}
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, nil, err
	}
	out, err := ctx.runMediaOutStop([]string{"inspect", "--file", clip, "--out-dir", dir, "--count", strconv.Itoa(count)}, lg.stopped)
	if err != nil {
		return nil, nil, fmt.Errorf("审片检测失败: %w", err)
	}
	m := parseJSONLine(out)
	var frames []string
	var flags []string
	if m != nil {
		if arr, ok := m["frames"].([]any); ok {
			for _, x := range arr {
				if f := str(x); f != "" {
					frames = append(frames, f)
				}
			}
		}
		if qc, ok := m["qc"].(map[string]any); ok {
			if ok2, _ := qc["ok"].(bool); !ok2 {
				for _, fl := range anyArr(qc["flags"]) {
					flags = append(flags, str(fl))
				}
			}
		}
	}
	if len(frames) == 0 {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
				frames = append(frames, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(frames) == 0 {
		return nil, flags, fmt.Errorf("抽帧 0 张")
	}
	// 记录源 mp4 指纹,下次同产物审片直接复用抽帧(免重复解码)
	if fi, err := os.Stat(clip); err == nil {
		_ = os.WriteFile(mark, []byte(fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())), 0644)
	}
	return frames, flags, nil
}

// extractFrames 抽帧(媒体辅助脚本),返回 JPEG 路径列表
func (ctx *manjuCtx) extractFrames(lg *manjuLogger, clip string, shotID, count int) ([]string, error) {
	dir := ctx.shotFramesDir(shotID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	out, err := ctx.runMediaOutStop([]string{"frames", "--video", clip, "--out-dir", dir, "--count", strconv.Itoa(count)}, lg.stopped)
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

// refImagesFor 镜头参考图:R2V=全部登场角色多视图(正脸优先+全身/细节,≤9 预算),FL2VA=场景图
func (ctx *manjuCtx) refImagesFor(s manjuShot) []string {
	var out []string
	n := len(s.Characters)
	if n > 3 {
		n = 3
	}
	for i, cid := range s.Characters {
		if i >= 3 {
			break
		}
		for _, rel := range ctx.charViewRels(cid, i, n) {
			p := filepath.Join(ctx.assetsDir, rel)
			if fileExists(p) {
				out = append(out, p)
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
	vc := ctx.visionClientShared(acfg)
	if vc == nil {
		// 审计 M6:视觉客户端构造失败(模型名与 base_url 服务商不匹配/模型名为空)→ 降级跳过判分,
		// 机械质检问题照常升级,不空转返工
		lg.logf("🤖 视觉模型配置异常(模型名与 base_url 服务商不匹配或模型名为空),跳过判分")
		var failed []int
		for id := range qcBad {
			failed = append(failed, id)
		}
		return failed
	}
	charMap, sceneMap := planCharSceneMaps(plan)
	styleDesc := manjuStyleDesc(ctx.style).asset
	primaryModel := strings.Split(strings.TrimSpace(acfg.VisionModel), ",")[0]
	var failed []int
	for _, s := range shots {
		clip := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
		if !fileExists(clip) {
			continue
		}
		// 审计 H8:断点续跑判分幂等——该镜已判分通过(pass/fixed/accepted)且产物未变(stale 检查)
		// 且无机械质检问题则跳过,不重复扣 VLM 费/重建抽帧(此前每次续跑整集重判)
		if len(qcBad[s.ID]) == 0 {
			if prev := loadAgentStateShot(project, s.ID); prev != nil &&
				(prev.Status == "pass" || prev.Status == "fixed" || prev.Status == "accepted") {
				if ctx.shotManifestStatus(s) != "stale" {
					lg.logf(fmt.Sprintf("   ⏭ 镜头 %d 已判分通过(%s)且产物未变,跳过审片", s.ID, prev.Status))
					continue
				}
			}
		}
		jd := &agent.Judgment{Status: "pending", JudgedAt: time.Now().Unix(), Model: acfg.VisionModel}
		jd.QCFlags = qcBad[s.ID]
		frames, qcFlags, ferr := ctx.inspectShot(lg, clip, s.ID, acfg.FramesPerShot)
		if ferr != nil {
			jd.Error = ferr.Error()
			lg.logf("🤖 审片 镜头 " + strconv.Itoa(s.ID) + " 抽帧失败: " + ferr.Error())
		} else {
			// inspect 单进程已含机械质检(替代 runQCJSON 独立进程);外部传入的 qcBad 合并去重
			if len(qcFlags) > 0 {
				seen := map[string]bool{}
				for _, f := range append(append([]string{}, jd.QCFlags...), qcBad[s.ID]...) {
					seen[f] = true
				}
				for _, f := range qcFlags {
					if !seen[f] {
						jd.QCFlags = append(jd.QCFlags, f)
						seen[f] = true
					}
				}
				merged := append([]string{}, jd.QCFlags...)
				qcBad[s.ID] = merged
			}
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
				// 降级标注:实际模型 ≠ 配置主模型 → 明示已降级(粘性窗口内直连备模型)
				degrade := ""
				if jd.Model != "" && jd.Model != primaryModel {
					degrade = " ⤵️已降级 " + jd.Model
				}
				lg.logf(fmt.Sprintf("🤖 审片 镜头 %d: %.1f 分 %s %s%s", s.ID, jd.Score, mark, weakStr, degrade))
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
	// 检查点一并清除:定点重渲/质检自愈/agent 返工删产物后,残留 ck 会让 tryReclaim 收回旧产物
	ctx.renderCKClear(strconv.Itoa(s.ID))
	ctx.renderCKClear(strconv.Itoa(s.ID) + "@d")
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

// agentRenderPipeline AI 一条龙 render 阶段主体(替代整段渲完再统一审):
// 逐镜「渲染→机械质检+ASR 台词核对+视觉判分」;不合格镜头当场由修复师改写 H3 提示词,
// 删产物进重渲队列(每镜预算 acfg.MaxRetries 轮);预算耗尽升级待人拍板;全部通过后
// 由 qc 阶段收尾汇总、assemble 统一合成成片。
func agentRenderPipeline(ctx *manjuCtx, lg *manjuLogger, acfg agent.Config) error {
	// ComfyUI 未运行自动拉起(渲染前兜底)
	if err := ctx.ensureComfyReady(lg); err != nil {
		return err
	}
	// 渲染/编码前释放显存(assets 阶段 ZImage/Lumina 常驻,不腾空间 Qwen3-VL/H3 UNET 加载
	// 会因显存不足阻塞 → 提交后 ComfyUI 挂起、显卡没动静)
	ctx.freeComfyModels(lg)
	// SageAttn 节点缺失提前降级,使「⚙️ 生效参数」总览展示真实生效值(renderShotTo 内兜底)
	ctx.sageAttnGuard(lg)
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
	lg.logf(fmt.Sprintf("🤖 Agent 流水线启动: %d 镜 · 渲染与审片并行(渲完即后台判分,ASR 与审片并行) · 不合格修复提示词排队重渲(预算 %d 轮)%s",
		len(selected), acfg.MaxRetries, func() string {
			if draftMode {
				return fmt.Sprintf("\n📐 草稿预审: 审片轮 %d×%d 草稿 → 落定后 %d×%d 定稿重渲", jw2, jh2, ctx.w, ctx.h)
			}
			return ""
		}()))
	// 生效参数总览(渲染配置全量可见:档位/步数/seed策略/转场/BGM/长镜/SageAttn)
	bgmDesc := "无"
	if b := strings.TrimSpace(str(ctx.R["bgm"])); b != "" {
		bgmDesc = filepath.Base(b)
	}
	sageDesc := ""
	if b, _ := ctx.R["sage_attention"].(bool); b {
		sageDesc = " · ⚡SageAttn"
	}
	seedPolicyCN := map[string]string{"fixed": "固定", "increment": "重试递增", "random": "重试随机"}[orDefault(ctx.seedPolicy, "fixed")]
	lg.logf(fmt.Sprintf("⚙️ 生效参数: 档位 %s(%d×%d@%dfps) · %d 步 · seed %d(%s) · 转场 %s · BGM %s · 长镜 %d 镜/组%s",
		orDefault(ctx.resTier, "手动"), ctx.w, ctx.h, ctx.fps, ctx.steps, ctx.seed, seedPolicyCN,
		orDefault(str(ctx.R["transition"]), "cut"), bgmDesc, ctx.shotsPerTake(), sageDesc))

	queue := selected
	queueIsFirst := true
	passed := 0
	escCount := 0
	autoAccept, autoRegen := 0, 0 // 终审自动拍板:接受/重写计数
	planPath := manjuFindPlanDir(ctx.analysisDir, ctx.episode)
	for len(queue) > 0 && !lg.stopped() {
		// 本轮三路并行:①逐镜渲染,渲完立即后台并发审片(渲染不空等——审片期间 GPU 继续渲下一镜);
		// ②渲染一结束即启动批量 ASR(只依赖产物文件,不需判分结论)——与视觉审片并行,
		//   省掉原先「等全部判分完 → 再串行跑 ASR」的整段(whisper 模型加载+全轮转写);
		// ③轮末汇总失败镜头走修复/升级
		var jw sync.WaitGroup
		judgeSem := make(chan struct{}, acfg.JudgeConcurrency) // 视觉判分 API 并发上限(可配,默认 2;免费档调高易 429)
		for qi, s := range queue {
			if lg.stopped() {
				jw.Wait()
				return fmt.Errorf("已停止")
			}
			// 预编码重叠:渲染当前镜期间后台预提交下一镜的 Qwen3-VL 编码(Qwen3-VL 无 UNET,
			// 与 H3 采样可在 ComfyUI 队列并行,整集省掉每镜「编码+渲染」串行的空窗)
			var preWg sync.WaitGroup
			var preErr error
			if qi+1 < len(queue) {
				next := queue[qi+1]
				nextDst := filepath.Join(judgeDir, fmt.Sprintf("%02d.mp4", next.ID))
				need := !fileExists(nextDst)
				if draftMode && fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", next.ID))) {
					need = false // 已有定稿产物,草稿轮无需预编码
				}
				if need {
					preWg.Add(1)
					safeGo("preencode", lg, func() {
						defer preWg.Done()
						if e := ctx.ensureEncodedAt(next, ctx.shotCacheNameAt(next, jw2, jh2), jw2, jh2, lg); e != nil {
							preErr = e
						}
					})
				}
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
					preWg.Wait()
					jw.Wait()
					return err
				}
			}
			preWg.Wait() // 等下一镜预编码(通常渲染期间早已完成;失败由下一镜串行重试兜底)
			if preErr != nil {
				lg.logf("  ⚠️ 下一镜预编码失败(将串行重试): " + truncate(preErr.Error(), 100))
			}
			// 后台即时审片:inspect 单进程已含机械质检+抽帧(无需再跑 runQCJSON 独立进程),
			// 结论落 agent_state;ASR 与审片并行
			jw.Add(1)
			safeGo("judge", lg, func() {
				defer jw.Done()
				judgeSem <- struct{}{}
				defer func() { <-judgeSem }()
				t0 := time.Now()
				lg.logf(fmt.Sprintf("🤖 审片官接管镜头 %d ...", s.ID))
				ctx.judgeShots(lg, acfg, plan, []manjuShot{s}, nil, judgeDir)
				lg.logf(fmt.Sprintf("    ⏱ 镜头 %d 审片 %.1fs(质检+抽帧单进程,含判分)", s.ID, time.Since(t0).Seconds()))
			})
		}
		// ASR 与视觉审片并行开跑(ASR 只需产物文件;渲染循环已结束,全部镜头就绪)
		var asrWg sync.WaitGroup
		var asrBad map[int][]string
		asrWg.Add(1)
		safeGo("asr", lg, func() {
			defer asrWg.Done()
			asrBad = ctx.runASRCheck(lg, judgeDir, queue, planPath)
		})
		jw.Wait()    // 等本轮全部审片落定
		asrWg.Wait() // 等批量 ASR(与审片并行,通常早已完成)
		if lg.stopped() {
			return fmt.Errorf("已停止")
		}

		// 汇总失败镜头(视觉判分未过 / 判分调用出错 / ASR 台词不符)→ 升级或修复重渲
		var redo []manjuShot
		for _, s := range queue {
			jd := loadAgentStateShot(ctx.project, s.ID)
			asrFlags, asrHit := asrBad[s.ID]
			// 审计 S5:判分调用失败(pending+Error,视觉服务不可用/超时/熔断)与判分不合格(failed)分流——
			// 前者无判分依据,重渲只会空烧 GPU/LLM/VLM,直接升级待恢复后人工重试;后者走修复师返工
			judgeErr := jd.Status == "pending" && jd.Error != ""
			judgeFailed := jd.Status == "failed"
			if !judgeFailed && !judgeErr && !asrHit {
				passed++
				continue
			}
			// 判分服务不可用:不返工不重渲,直接升级(熔断窗恢复后用户可「重试」该升级)
			if judgeErr {
				escalateShot(ctx, lg, s.ID, jd.Score, "判分调用失败(视觉服务不可用/超时),未重渲: "+jd.Error, acfg)
				escCount++
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
				// AI 终审自动拍板(auto_resolve 默认开):接受该镜最佳结果,或按分镜原文从零重写
				// 提示词独立重渲一轮后接受——AI 一条龙不把决策丢给人(终审失败兜底接受,绝不阻塞)
				if acfg.AutoResolve && acfg.VisionReady() {
					if ctx.arbiterResolve(plan, s, jd, acfg, lg, judgeDir, jw2, jh2) == "regenerate" {
						autoRegen++
					} else {
						autoAccept++
					}
					continue
				}
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
		// 审计 H10:升级通知——此前智能模式的升级推送只在死代码 agentJudgeAndRework 里,
		// 实际运行从不触发,坏镜升级无人知晓(阶段切换/完成通知已有,唯独升级缺失)
		manjuNotifySend(fmt.Sprintf("漫剧《%s》%s · 🚨 %d 个镜头升级待拍板(工作台「审片报告」可重试/忽略)", ctx.project, ctx.episode, escCount))
	} else if autoAccept+autoRegen == 0 {
		lg.logf(fmt.Sprintf("🎉 审片全部通过:%d 镜(含返工通过)", passed))
	}
	if n := autoAccept + autoRegen; n > 0 {
		lg.logf(fmt.Sprintf("🤖 终审拍板 %d 镜:接受 %d · 重写 %d——AI 全权决策,无需人工介入(审片报告可事后重试)", n, autoAccept, autoRegen))
		if float64(autoAccept) > 0.3*float64(len(selected)) {
			lg.logf(fmt.Sprintf("  💡 接受率偏高:及格线 %.0f 分可能偏严或视觉模型评分尺度偏紧,可在设置调整", acfg.PassScore))
		}
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
		for si, s := range shots2 {
			if lg.stopped() {
				return fmt.Errorf("已停止")
			}
			if !selSet[s.ID] {
				continue
			}
			done++
			// 定稿轮同样预编码重叠:渲染当前镜期间后台预编码下一镜(全集顺序,接缝依赖 latent 不并行渲染)
			var preWg sync.WaitGroup
			var preErr error
			for ni := si + 1; ni < len(shots2); ni++ {
				nx := shots2[ni]
				if !selSet[nx.ID] {
					continue
				}
				if !fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", nx.ID))) {
					preWg.Add(1)
					safeGo("rework-preencode", lg, func() {
						defer preWg.Done()
						if e := ctx.ensureEncodedAt(nx, ctx.shotCacheNameAt(nx, ctx.w, ctx.h), ctx.w, ctx.h, lg); e != nil {
							preErr = e
						}
					})
				}
				break // 只预编码最近下一个待渲镜头
			}
			dst := filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))
			if fileExists(dst) && ctx.shotManifestStatus(s) == "stale" {
				lg.logf(fmt.Sprintf("⚠️ 镜头 %d 定稿已过期(输入已变),删旧重渲", s.ID))
				ctx.clearShotArtifacts(s)
			}
			if fileExists(dst) {
				preWg.Wait()
				lg.logf(fmt.Sprintf("  跳过（已定稿）: %s", dst))
				continue
			}
			lg.logf(fmt.Sprintf("[%d/%d] 定稿 镜头 %d: [%s] %s", done, len(selected), s.ID, s.Scene, s.Camera))
			if err := ctx.renderShotTo(s, idxOf[s.ID], false, clipsEp, ctx.w, ctx.h, 0, lg); err != nil {
				preWg.Wait()
				return err
			}
			preWg.Wait()
			if preErr != nil {
				lg.logf("  ⚠️ 下一镜定稿预编码失败(将串行重试): " + truncate(preErr.Error(), 100))
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
	out, _ := ctx.runMediaOutStop([]string{"qc", "--file", final}, lg.stopped)
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
	// 性能护栏:全部镜头已判分通过且无 stale 时,产物未被替换(流水线刚审过),跳过全目录解码——
	// 否则每次续跑 qc 阶段都把整集视频再解一遍(纯浪费)。产物变更/未判分镜头存在时才全目录终检。
	st := loadAgentState(ctx.project)
	allPassed := true
	for _, s := range selected {
		if !fileExists(filepath.Join(clipsEp, fmt.Sprintf("%02d.mp4", s.ID))) {
			continue
		}
		j := st.Shots[strconv.Itoa(s.ID)]
		if j == nil || (j.Status != "pass" && j.Status != "fixed" && j.Status != "accepted") {
			allPassed = false
			break
		}
		if ctx.shotManifestStatus(s) == "stale" {
			allPassed = false
			break
		}
	}
	var finalBad map[int][]string
	if allPassed {
		lg.logf("  ⏭ 终检跳过:全部镜头已判分通过且产物未变(无替换/变更风险)")
	} else {
		finalBad, _ = ctx.runQCJSON(lg, clipsEp, "")
	}
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
		planPath := manjuFindPlanDir(ctx.analysisDir, ctx.episode)
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
	// 审计 M3:同一集只汇总一次——续跑/重复 qc 不再把全量返工轮数与问题再累加一遍
	if stm.Memory.LastSummarizedEp == ctx.episode {
		return nil
	}
	stm.Memory.LastSummarizedEp = ctx.episode
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
		type kv struct {
			k string
			v int
		}
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

// arbiterResolve 预算耗尽的 AI 终审拍板(auto_resolve 开启时不把决策丢给人):
// accept=接受当前最佳(判分状态 accepted+决策记录,升级不入列);
// regenerate=按分镜原文从零重写提示词(非增量修复),独立重渲一轮+复审后接受结果。
// 返回决策("accept"/"regenerate");终审调用失败兜底 accept。
func (ctx *manjuCtx) arbiterResolve(plan map[string]any, s manjuShot, jd *agent.Judgment, acfg agent.Config, lg *manjuLogger, dir string, w, h int) string {
	charMap, sceneMap := planCharSceneMaps(plan)
	decision, reason, err := agent.ArbiterDecide(manjuAgentLLM{ctx.llm}, shotMetaFromPlan(s, charMap, sceneMap, manjuStyleDesc(ctx.style).asset), jd, acfg.PassScore, acfg.MaxRetries)
	if err != nil {
		decision, reason = "accept", "终审调用失败,兜底接受("+truncate(err.Error(), 60)+")"
	}
	decisionCN := map[string]string{"accept": "自动接受", "regenerate": "从零重写"}[decision]
	if decision == "regenerate" {
		lg.logf(fmt.Sprintf("🤖 终审镜头 %d:%s(%s)→ 按分镜原文重写提示词,独立重渲一轮", s.ID, decisionCN, reason))
		if np, ferr := ctx.genShotPrompt(s, charMap, sceneMap); ferr == nil {
			if uerr := ctx.updateShotPrompt(s, np); uerr == nil {
				s.H3Prompt = np
			}
		} else {
			lg.logf("  ⚠️ 重写失败,接受当前产物: " + truncate(ferr.Error(), 80))
		}
		ctx.clearShotArtifacts(s)
		bumpAgentRetries(ctx.project, s.ID)
		if rerr := ctx.renderShotTo(s, 0, true, dir, w, h, loadAgentStateShot(ctx.project, s.ID).Retries, lg); rerr != nil {
			lg.logf("  ⚠️ 终审重渲失败,保留原产物: " + truncate(rerr.Error(), 80))
		} else {
			ctx.judgeShots(lg, acfg, plan, []manjuShot{s}, nil, dir)
			if nd := loadAgentStateShot(ctx.project, s.ID); nd != nil {
				reason = fmt.Sprintf("重写后 %.0f 分,接受", nd.Score)
			}
		}
		ctx.markAutoAccepted(s.ID, fmt.Sprintf("终审:从零重写(%s)", reason))
		lg.logf(fmt.Sprintf("  ✅ 镜头 %d 终审重写完成,结果接受进成片", s.ID))
		return "regenerate"
	}
	ctx.markAutoAccepted(s.ID, fmt.Sprintf("终审:%s(%s)", decisionCN, reason))
	lg.logf(fmt.Sprintf("🤖 终审镜头 %d:%s——当前 %.0f 分已是该镜可达最佳,重渲收益低,接受进成片", s.ID, decisionCN, jd.Score))
	return "accept"
}

// markAutoAccepted 终审接受:判分状态 accepted + 决策记录入 Judgment.Arbiter(审片报告
// 可见),并自动解除该镜既有未处理升级(人工仍可事后点「重试此镜」覆盖)
func (ctx *manjuCtx) markAutoAccepted(shotID int, note string) {
	manjuAgentMu.Lock()
	defer manjuAgentMu.Unlock()
	st := loadAgentStateLocked(ctx.project)
	if j := st.Shots[strconv.Itoa(shotID)]; j != nil {
		j.Status = "accepted"
		j.Arbiter = note
	}
	for i := range st.Escalations {
		e := &st.Escalations[i]
		if e.EP == ctx.episode && e.Shot == shotID && !e.Resolved {
			e.Resolved = true
			e.Action = "auto-accept"
		}
	}
	saveAgentStateLocked(ctx.project, st)
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

	// 定点返工后台任务:panic 兜底(审计 S2——此前无 recover,内部任何 panic 直接崩进程)
	safeGo("rework", nil, func() {
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
	})
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
	// 深度分析小说内容 → 推荐并更新渲染风格(AI 一条龙「是」分支)
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
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		configPath = cp
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
			manjuGlobalAgentMu.RLock()
			res["projectMissing"] = true
			res["visionBaseUrl"] = manjuGlobalAgent.VisionBaseURL
			res["agentEnabled"] = manjuGlobalAgent.Enabled
			manjuGlobalAgentMu.RUnlock()
		}
		// 全局默认(settings.json agent 节):前端展示"项目未配置时使用全局默认"(项目缺失时也返回,避免整块视觉区空白)
		manjuGlobalAgentMu.RLock()
		res["globalDefaults"] = map[string]any{
			"enabled": manjuGlobalAgent.Enabled, "visionModel": manjuGlobalAgent.VisionModel,
			"visionBaseUrl": manjuGlobalAgent.VisionBaseURL,
			"passScore":     manjuGlobalAgent.PassScore, "maxRetries": manjuGlobalAgent.MaxRetries,
			"hasVisionKey": manjuGlobalAgent.VisionAPIKey != "",
		}
		manjuGlobalAgentMu.RUnlock()
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
			if n, ok := manjuToInt(m["judge_concurrency"]); ok && n >= 1 && n <= 4 {
				A["judge_concurrency"] = n
			}
			if b, ok := m["auto_resolve"].(bool); ok {
				A["auto_resolve"] = b
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
			if b, ok := A["auto_resolve"].(bool); ok {
				ga.AutoResolve = &b
			}
			if n, ok := manjuToInt(A["judge_concurrency"]); ok && n >= 1 && n <= 4 {
				ga.JudgeConcurrency = n
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
		// 审计 F3:config 必须归属项目根目录(否则可对任意 JSON 文件读改写)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
		}
		cfg, err := readManjuConfig(configPath)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
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
		// 审计 F4:config 归属校验(judge)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
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
		// 审计 F4:config 归属校验(resolve)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
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
		// 审计 F4:config 归属校验(output/delete;workdir 取自 config,越界可删任意文件)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
		}
		ctx, err := newManjuCtx(configPath, str(body["episode"]), "", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
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
		// 审计 F4:config 归属校验(trailer)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
		}
		ctx, err := newManjuCtx(configPath, episode, "", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
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
			"--plan", manjuFindPlanDir(ctx.analysisDir, ctx.episode)}
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
		// 审计 F4:config 归属校验(vision-test)
		if cp, gerr := manjuGuardConfig(configPath); gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		} else {
			configPath = cp
		}
		ctx, err := newManjuCtx(configPath, "", "", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
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
