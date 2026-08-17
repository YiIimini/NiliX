// Package backend 封装流水线依赖的外部服务客户端。
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatMessage 是 OpenAI 兼容的对话消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Thinking    thinkingParam `json:"thinking,omitempty"`
}

// thinkingParam 控制 DeepSeek V4 的思考模式。
type thinkingParam struct {
	Type string `json:"type"` // enabled / disabled
}

type chatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// LLMClient 是 OpenAI 兼容文本大模型客户端（当前用于 DeepSeek）。
type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

// NewLLMClient 构造客户端。
func NewLLMClient(baseURL, apiKey, model string, timeout time.Duration) *LLMClient {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &LLMClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *LLMClient) chatURL() string {
	if strings.HasSuffix(c.BaseURL, "/chat/completions") {
		return c.BaseURL
	}
	return c.BaseURL + "/chat/completions"
}

// Chat 发送一条对话请求并返回助手回复。默认关闭思考模式（直接产出，避免思考链耗尽 token）。
func (c *LLMClient) Chat(ctx context.Context, msgs []ChatMessage, maxTokens int, temperature float64) (string, error) {
	req := chatRequest{
		Model:       c.Model,
		Messages:    msgs,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Thinking:    thinkingParam{Type: "disabled"},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", err
	}
	if cr.Error != nil {
		return "", fmt.Errorf("LLM 错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("LLM 返回空 choices")
	}
	return cr.Choices[0].Message.Content, nil
}

// Test 做一次最小化请求以验证 key 与接口连通。
func (c *LLMClient) Test(ctx context.Context) error {
	_, err := c.Chat(ctx, []ChatMessage{{Role: "user", Content: "ping"}}, 1, 0)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
