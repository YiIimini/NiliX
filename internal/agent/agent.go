// Package agent 漫剧智能体调度层:审片官(视觉判分)/修复师(提示词返工)/剧本师复核。
// 维度体系对齐 MiniMax H3 官方能力边界(参考保持/指令遵循/运镜语言/原生对白)
// 与已知失败模式(近黑帧/面部扭曲/文字水印),打分由 Go 侧加权计算,不信任模型自报总分。
package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---- 配置(项目 config.json 的 agent 节) ----

type Config struct {
	Enabled          bool    `json:"enabled"`           // 智能体调度总开关
	VisionBaseURL    string  `json:"vision_base_url"`   // 视觉模型 OpenAI 兼容地址(空=用项目 llm 地址)
	VisionAPIKey     string  `json:"vision_api_key"`    // 视觉模型 Key(空=用项目 llm key)
	VisionModel      string  `json:"vision_model"`      // 视觉模型名(空=审片官禁用,仅机械质检)
	PassScore        float64 `json:"pass_score"`        // 及格线(加权总分,0-100)
	MaxRetries       int     `json:"max_retries"`       // 自动返工轮数上限(每轮=修复提示词+重编码+重渲染)
	JudgeConcurrency int     `json:"judge_concurrency"` // 视觉判分 API 并发上限(1-4,默认 2;付费 Key 可调高提速)
	AutoResolve      bool    `json:"auto_resolve"`      // 预算耗尽 AI 终审自动拍板(接受最佳/从零重写一轮,不等人;默认开)
	FramesPerShot    int     // 抽帧数(缺省 3,不落盘到配置)
}

// DefaultConfig 缺省配置:75 分及格、最多 2 轮返工、终审自动拍板开(预算封顶,防无限重试烧 GPU)
func DefaultConfig() Config {
	return Config{PassScore: 75, MaxRetries: 2, JudgeConcurrency: 2, AutoResolve: true, FramesPerShot: 3}
}

// Normalize 兜底非法取值
func (c *Config) Normalize() {
	if c.PassScore <= 0 || c.PassScore > 100 {
		c.PassScore = 75
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.MaxRetries > 4 {
		c.MaxRetries = 4
	}
	if c.JudgeConcurrency <= 0 {
		c.JudgeConcurrency = 2
	}
	if c.JudgeConcurrency > 4 {
		c.JudgeConcurrency = 4
	}
	if c.FramesPerShot <= 0 {
		c.FramesPerShot = 3
	}
}

// VisionReady 审片官是否可用(视觉模型已配置)
func (c *Config) VisionReady() bool {
	return strings.TrimSpace(c.VisionModel) != ""
}

// ---- 打分维度(对齐 H3 官方能力) ----

// Dim 一个打分维度:key/中文名/权重
type Dim struct {
	Key   string
	Name  string
	Weight float64
}

// Dims 八维度及权重(合计 100):
// identity/scene 对应 Ref2VA 参考保持(官方 retention_analysis 体系);
// action/camera 对应官方"复杂多模态指令遵循"与运镜三要素语言;
// visibility 对应亮度护栏(近黑帧实测失败模式);tech 对应已知伪影(面部扭曲/闪烁/文字水印);
// style 对应风格句约束;lips 对应 <d> 原生对白口型(帧级观察可靠性低,低权重)。
var Dims = []Dim{
	{"identity", "主体一致性", 20},
	{"scene", "场景还原", 12},
	{"action", "动作符合", 15},
	{"camera", "运镜符合", 10},
	{"visibility", "主体可见性", 15},
	{"tech", "技术质量", 15},
	{"style", "风格统一", 8},
	{"lips", "口型对白", 5},
}

// dimNames key → 中文名
var dimNames = func() map[string]string {
	m := map[string]string{}
	for _, d := range Dims {
		m[d.Key] = d.Name
	}
	return m
}()

// DimName 维度中文名(未知 key 原样返回)
func DimName(k string) string {
	if n, ok := dimNames[k]; ok {
		return n
	}
	return k
}

// ---- 审片结论 ----

// Judgment 一个镜头的一次审片结论(状态落盘单位)
type Judgment struct {
	Status      string             `json:"status"`              // pass/fixed/failed/pending/accepted/skip
	Score       float64            `json:"score"`               // 加权总分(0-100,Go 计算)
	Dimensions  map[string]float64 `json:"dimensions,omitempty"` // 各维度 0-100
	Issues      []string           `json:"issues,omitempty"`    // 问题清单(中文,推送/展示用)
	Suggestion  string             `json:"suggestion,omitempty"` // 修复方向(喂给修复师)
	Retries     int                `json:"retries"`             // 已返工轮数
	Model       string             `json:"model,omitempty"`     // 审片用的视觉模型
	Fallback    bool               `json:"fallback,omitempty"`  // true=有维度缺失走兜底分
	QCFlags     []string           `json:"qcFlags,omitempty"`   // PyAV 机械质检问题(无音轨/近黑…)
	JudgedAt    int64              `json:"judgedAt"`
	Error       string             `json:"error,omitempty"`     // 审片失败原因(视觉模型不可用等)
	Arbiter     string             `json:"arbiter,omitempty"`   // 终审拍板记录(预算耗尽自动决策:接受/重写+理由)
}

// ToFloat 宽松数值解析(LLM 可能把数字输出成字符串,审计教训:漏维度记 0 会拉崩总分)
func ToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// clamp01hundred 夹到 [0,100]
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// WeightedScore 加权总分:缺失维度按 70 兜底并标记 fallback(缺失≠0,防单维拉崩)。
// 返回 (总分, 是否有兜底)。
func WeightedScore(dims map[string]float64) (float64, bool) {
	total, weight, fallback := 0.0, 0.0, false
	for _, d := range Dims {
		v, ok := dims[d.Key]
		if !ok {
			v, fallback = 70, true
		}
		total += clamp(v) * d.Weight
		weight += d.Weight
	}
	if weight == 0 {
		return 0, fallback
	}
	return clamp(total / weight), fallback
}

// WeakDims 列出低于阈值的维度(修复师聚焦用),按权重降序
func WeakDims(dims map[string]float64, threshold float64) []Dim {
	var out []Dim
	for _, d := range Dims {
		if v, ok := dims[d.Key]; ok && v < threshold {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// FormatScore 总分中文摘要(日志/推送用)
func FormatScore(score float64) string {
	return fmt.Sprintf("%.1f 分", score)
}
