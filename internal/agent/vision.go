// 视觉模型客户端:OpenAI 兼容 chat/completions,content 为多模态数组(文本 + image_url base64)。
// 兼容 GLM-4.xV / Qwen-VL / GPT 系等一切 OpenAI 格式的视觉接口。
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

// VisionClient OpenAI 兼容视觉模型客户端
type VisionClient struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	client  *http.Client
}

// NewVisionClient 构造(超时缺省 180s:审片一次带多图,慢模型也要等得起)。
// baseURL 兼容三种写法:根地址 / 带 /chat/completions 的完整端点 / 带尾斜杠。
func NewVisionClient(baseURL, apiKey, model string, timeout time.Duration) *VisionClient {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	return &VisionClient{
		BaseURL: base,
		APIKey:  apiKey,
		Model:   model,
		Timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

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
// 返回 content 文本。响应不做 response_format 约束(部分视觉网关不支持 json_object,靠提示词约束 + 剥围栏)。
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
	body := map[string]any{
		"model":       v.Model,
		"temperature": temperature,
		"max_tokens":  4096,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": content},
		},
		"stream": false,
	}
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
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("视觉模型响应解析失败: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("视觉模型空响应")
	}
	return r.Choices[0].Message.Content, nil
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
