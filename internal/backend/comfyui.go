package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SystemStats 是 ComfyUI /system_stats 的节选。
type SystemStats struct {
	System struct {
		ComfyUIVersion string `json:"comfyui_version"`
		PythonVersion  string `json:"python_version"`
		PyTorchVersion string `json:"pytorch_version"`
	} `json:"system"`
	Devices []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		VRAMTotal int64  `json:"vram_total"`
		VRAMFree  int64  `json:"vram_free"`
	} `json:"devices"`
}

// PromptResponse 是 POST /prompt 的响应。
type PromptResponse struct {
	PromptID   string            `json:"prompt_id"`
	Number     int               `json:"number"`
	NodeErrors map[string]any    `json:"node_errors"`
	Error      map[string]any    `json:"error"`
}

// MediaFile 是输出媒体文件描述。
type MediaFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// HistoryOutput 是某节点的输出文件。
type HistoryOutput struct {
	Images []MediaFile `json:"images"`
	Videos []MediaFile `json:"videos"`
	Audio  []MediaFile `json:"audio"`
}

// HistoryEntry 是某 prompt 的执行历史。
type HistoryEntry struct {
	Outputs map[string]HistoryOutput `json:"outputs"`
	Status  struct {
		StatusStr string `json:"status_str"`
		Completed bool   `json:"completed"`
	} `json:"status"`
}

// ComfyUIClient 封装对 ComfyUI HTTP API 的调用。
type ComfyUIClient struct {
	BaseURL string
	client  *http.Client
	dl      *http.Client // 下载用，更长超时
}

// NewComfyUIClient 构造客户端。
func NewComfyUIClient(baseURL string) *ComfyUIClient {
	return &ComfyUIClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
		dl:      &http.Client{Timeout: 5 * time.Minute},
	}
}

// SystemStats 探测 ComfyUI 是否在线并返回系统信息。
func (c *ComfyUIClient) SystemStats(ctx context.Context) (*SystemStats, error) {
	raw, err := c.do(ctx, c.client, http.MethodGet, c.BaseURL+"/system_stats", nil)
	if err != nil {
		return nil, err
	}
	var ss SystemStats
	if err := json.Unmarshal(raw, &ss); err != nil {
		return nil, err
	}
	return &ss, nil
}

// SubmitPrompt 提交工作流，返回 prompt_id。
func (c *ComfyUIClient) SubmitPrompt(ctx context.Context, workflow map[string]any, clientID string) (*PromptResponse, error) {
	payload := map[string]any{"prompt": workflow, "client_id": clientID}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, c.client, http.MethodPost, c.BaseURL+"/prompt", body)
	if err != nil {
		return nil, err
	}
	var pr PromptResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("解析 /prompt 响应失败: %w", err)
	}
	if pr.NodeErrors != nil && len(pr.NodeErrors) > 0 {
		return nil, fmt.Errorf("ComfyUI 节点错误: %v", pr.NodeErrors)
	}
	return &pr, nil
}

// History 查询某 prompt 的执行历史；未完成时返回空 map。
func (c *ComfyUIClient) History(ctx context.Context, promptID string) (map[string]HistoryEntry, error) {
	raw, err := c.do(ctx, c.client, http.MethodGet, c.BaseURL+"/history/"+promptID, nil)
	if err != nil {
		return nil, err
	}
	var h map[string]HistoryEntry
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("解析 /history 响应失败: %w", err)
	}
	return h, nil
}

// Download 下载输出文件。
func (c *ComfyUIClient) Download(ctx context.Context, filename, subfolder, typ string) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", filename)
	q.Set("subfolder", subfolder)
	q.Set("type", typ)
	return c.do(ctx, c.dl, http.MethodGet, c.BaseURL+"/view?"+q.Encode(), nil)
}

// do 发送请求并返回响应体。
func (c *ComfyUIClient) do(ctx context.Context, client *http.Client, method, fullURL string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ComfyUI HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return raw, nil
}
