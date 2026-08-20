// 视觉模型客户端:OpenAI 兼容 chat/completions,content 为多模态数组(文本 + image_url base64)。
// 兼容 GLM-4.xV / Qwen-VL / GPT 系等一切 OpenAI 格式的视觉接口。
// 模型链兜底(glm-vision 技能逻辑):主模型 429/过载按 4s/10s/20s 退避重试,
// 仍失败自动降级链上备模型;key 解析顺序:配置 → 环境变量 GLM_VISION_API_KEY。
package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// visionFallbackChain 内置降级链(智谱免费档,glm-vision 技能同款:主模型高峰 429 常过载,
// 降级上一代免费 flash)。数据驱动,新增链只改这里。
var visionFallbackChain = map[string]string{
	"glm-4.6v-flash": "glm-4v-flash",
}

// visionBackoffs 429/过载退避节奏(glm-vision 技能:4s/10s/20s 三次)
var visionBackoffs = []time.Duration{4 * time.Second, 10 * time.Second, 20 * time.Second}

// Usage 一次调用的 token 用量(OpenAI 兼容响应的 usage 字段,缺失为 0)
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// VisionClient OpenAI 兼容视觉模型客户端(支持模型链)
type VisionClient struct {
	BaseURL string
	APIKey  string
	Model   string   // 主模型
	Models  []string // 模型链:主模型在前,失败降级依次尝试
	Timeout time.Duration
	client  *http.Client
	// LastUsedModel 最近一次成功调用实际使用的模型(降级时≠Model,Judgment 记录用)
	LastUsedModel string
	// OnUsage 每次成功调用回抛 token 用量(项目级记账用;可为 nil)
	OnUsage func(model string, u Usage)
	// 粘性降级(429 高峰):一次降级成功后,冷却窗内后续调用直接从备模型开始——
	// 不再每次先在主模型上烧完 4/10/20s 退避才降级;冷却结束自动回探主模型恢复。
	stickyMu    sync.Mutex
	stickyIdx   int
	stickyUntil time.Time
	// 整链熔断:全链 429(免费档高峰)后,熔断窗内直接快速失败不重试——
	// 否则每镜仍烧 4+10+20=34s 退避才失败,整轮审片空转;窗结束自动恢复。
	circuitMu    sync.Mutex
	circuitUntil time.Time
}

// visionCircuitCooldown 整链熔断窗(测试可缩短)
var visionCircuitCooldown = 3 * time.Minute

// visionStickyCooldown 粘性降级冷却窗(测试可缩短)
var visionStickyCooldown = 15 * time.Minute

// NewVisionClient 构造(超时缺省 180s:审片一次带多图,慢模型也要等得起)。
// baseURL 兼容三种写法:根地址 / 带 /chat/completions 的完整端点 / 带尾斜杠。
// model 支持"主模型,备模型"逗号链(自定义降级);单模型自动查内置降级表补链。
func NewVisionClient(baseURL, apiKey, model string, timeout time.Duration) *VisionClient {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	var models []string
	seen := map[string]bool{}
	for _, m := range strings.Split(model, ",") {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	// 空模型(如 vision_model="," 拆分后无合法名):返回 nil,调用方降级跳过判分——
	// 此前直接 models[0] 越界 panic 崩掉整个服务(审计 S2)
	if len(models) == 0 {
		return nil
	}
	if len(models) == 1 {
		// 单模型:自动补内置降级链(如 glm-4.6v-flash → glm-4v-flash)
		cur := models[0]
		for {
			next, ok := visionFallbackChain[cur]
			if !ok || seen[next] {
				break
			}
			seen[next] = true
			models = append(models, next)
			cur = next
		}
	}
	vc := &VisionClient{
		BaseURL: base,
		APIKey:  apiKey,
		Model:   models[0],
		Models:  models,
		Timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
	vc.LastUsedModel = vc.Model
	return vc
}

// LastUsed 线程安全读取实际使用模型(判分并发时与写入互斥)
func (v *VisionClient) LastUsed() string {
	v.stickyMu.Lock()
	defer v.stickyMu.Unlock()
	return v.LastUsedModel
}

// EnvAPIKey 环境变量兜底 key(glm-vision 技能:GLM_VISION_API_KEY)
func EnvAPIKey() string { return strings.TrimSpace(os.Getenv("GLM_VISION_API_KEY")) }

// imageDataURI 读图片文件转 base64 data URI(JPEG/PNG 均可)
func imageDataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读图片失败 %s: %w", filepath.Base(path), err)
	}
	mime := "image/jpeg"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		mime = "image/png"
	case ".webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// chatImage 多模态单轮:system + user 文本 + 图片(imagePaths 按顺序附加)。
// 模型链逐个尝试:每个模型按 4s/10s/20s 退避重试 429/网络类错误,重试耗尽且链上有
// 备模型则自动降级继续;粘性降级——降级成功后冷却窗内直接从备模型开始(省每镜 34s
// 主模型退避),冷却结束回探主模型;全链失败返回聚合错误(不再空转)。
func (v *VisionClient) chatImage(system, user string, imagePaths []string, temperature float64) (string, error) {
	content := []map[string]any{{"type": "text", "text": user}}
	for _, p := range imagePaths {
		uri, err := imageDataURI(p)
		if err != nil {
			return "", err
		}
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": uri},
		})
	}
	// 整链熔断:免费档高峰 429 后冷却窗内快速失败,不空转退避
	v.circuitMu.Lock()
	cb := v.circuitUntil
	v.circuitMu.Unlock()
	if time.Now().Before(cb) {
		return "", fmt.Errorf("视觉模型整链熔断中(高峰过载),约 %s 后自动恢复——本镜判分跳过",
			time.Until(cb).Round(time.Second).String())
	}
	// 起始模型:粘性窗口内从上次降级成功的备模型直连
	start := 0
	v.stickyMu.Lock()
	if v.stickyIdx > 0 && time.Now().Before(v.stickyUntil) {
		start = v.stickyIdx
	}
	v.stickyMu.Unlock()
	var errs []string
	for mi := start; mi < len(v.Models); mi++ {
		model := v.Models[mi]
		body := map[string]any{
			"model":       model,
			"temperature": temperature,
			"max_tokens":  4096,
			"messages": []map[string]any{
				{"role": "system", "content": system},
				{"role": "user", "content": content},
			},
			"stream": false,
		}
		var lastErr error
		for attempt := 0; attempt <= len(visionBackoffs); attempt++ {
			if attempt > 0 {
				time.Sleep(visionBackoffs[attempt-1])
			}
			out, err := v.doChatOnce(body)
			if err == nil {
				// 粘性记账 + 实际使用模型:一并入锁(LastUsedModel 被判分并发 goroutine 共享读写,
				// 裸字段在 -race 下必炸,审片报告模型归属也会错乱)
				v.stickyMu.Lock()
				v.LastUsedModel = model
				if mi > 0 {
					v.stickyIdx, v.stickyUntil = mi, time.Now().Add(visionStickyCooldown)
				} else {
					v.stickyIdx, v.stickyUntil = 0, time.Time{}
				}
				v.stickyMu.Unlock()
				return out, nil
			}
			lastErr = err
			if !retryable(err) {
				break // 4xx(除429)/解析类错误:换模型也救不了,但换链无妨——直接跳出重试
			}
		}
		errs = append(errs, model+": "+lastErr.Error())
		if !retryable(lastErr) || mi == len(v.Models)-1 {
			break
		}
		// 可重试类错误且链上还有备模型 → 降级继续
	}
	if len(errs) == 0 {
		errs = append(errs, "无可用视觉模型")
	}
	joined := strings.Join(errs, " | ")
	if strings.Contains(joined, "429") {
		joined += "(免费档高峰整链过载,建议稍后再试或更换视觉模型)"
	}
	// 全链都失败 → 熔断窗(下一次调用快速失败,不每镜重烧 34s 退避)
	v.circuitMu.Lock()
	v.circuitUntil = time.Now().Add(visionCircuitCooldown)
	v.circuitMu.Unlock()
	return "", fmt.Errorf("视觉模型链失败: %s", truncateStr(joined, 400))
}

// doChatOnce 单次视觉请求(无重试)
func (v *VisionClient) doChatOnce(body map[string]any) (string, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", v.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.APIKey)
	resp, err := v.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("视觉模型请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("视觉模型 HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))
	}
	var r struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("视觉模型响应解析失败: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("视觉模型空响应")
	}
	if v.OnUsage != nil && r.Usage.TotalTokens > 0 {
		model, _ := body["model"].(string)
		v.OnUsage(model, r.Usage)
	}
	return r.Choices[0].Message.Content, nil
}

// retryable 判断错误是否可重试(可重试=可降级):HTTP 429/5xx、网络错误、连接类错误;
// 4xx(除429)与解析类错误不可重试。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	// 审计 M1:显式识别 context.DeadlineExceeded;错误文本先小写化再匹配——
	// net/http 超时错误是 "Client.Timeout exceeded"(大写 T),大小写敏感匹配不到会
	// 跳过退避/降级直接整链失败+熔断,高峰期每镜一次瞬时超时即停摆
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "http 429") || strings.Contains(msg, "http 5") ||
		strings.Contains(msg, "请求失败") || strings.Contains(msg, "connection") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded") {
		return true
	}
	return false
}

// ChatJSON 多模态请求 + 解析 JSON 对象(剥 ```json 围栏,取首个 { 到末个 })
func (v *VisionClient) ChatJSON(system, user string, imagePaths []string, temperature float64) (map[string]any, error) {
	text, err := v.chatImage(system, user, imagePaths, temperature)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i > 0 {
		text = text[i:]
	}
	if i := strings.LastIndex(text, "}"); i >= 0 {
		text = text[:i+1]
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("视觉模型输出非 JSON: %v (前 200 字: %s)", err, truncateStr(text, 200))
	}
	return out, nil
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
