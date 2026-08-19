// 视觉模型客户端:OpenAI 兼容 chat/completions,content 为多模态数组(文本 + image_url base64)。
// 兼容 GLM-4.xV / Qwen-VL / GPT 系等一切 OpenAI 格式的视觉接口。
// 模型链兜底(glm-vision 技能逻辑):主模型 429/过载按 4s/10s/20s 退避重试,
// 仍失败自动降级链上备模型;key 解析顺序:配置 → 环境变量 GLM_VISION_API_KEY。
package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// visionFallbackChain 内置降级链(智谱免费档,glm-vision 技能同款:主模型高峰 429 常过载,
// 降级上一代免费 flash)。数据驱动,新增链只改这里。
var visionFallbackChain = map[string]string{
	"glm-4.6v-flash": "glm-4v-flash",
}

// visionBackoffs 429/过载退避节奏(glm-vision 技能:4s/10s/20s 三次)
var visionBackoffs = []time.Duration{4 * time.Second, 10 * time.Second, 20 * time.Second}

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
}

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
		BaseURL:  base,
		APIKey:   apiKey,
		Model:    models[0],
		Models:   models,
		Timeout:  timeout,
		client:   &http.Client{Timeout: timeout},
	}
	vc.LastUsedModel = vc.Model
	return vc
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
// 备模型则自动降级继续;全链失败返回聚合错误(不再空转——免费档整链过载时明确告知稍后再试)。
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
	var errs []string
	for mi, model := range v.Models {
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
				v.LastUsedModel = model
				if mi > 0 {
					// 降级成功:记录在案(Judgment.Model 呈现实际模型)
					return out, nil
				}
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
				Content string `json:"message"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("视觉模型响应解析失败: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("视觉模型空响应")
	}
	return r.Choices[0].Message.Content, nil
}

// retryable 判断错误是否可重试(可重试=可降级):HTTP 429/5xx、网络错误、连接类错误;
// 4xx(除429)与解析类错误不可重试。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "HTTP 429") || strings.Contains(msg, "HTTP 5") ||
		strings.Contains(msg, "请求失败") || strings.Contains(msg, "connection") ||
		strings.Contains(msg, "EOF") || strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") {
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
