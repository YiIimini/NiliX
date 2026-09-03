package comfy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client ComfyUI HTTP 客户端(2026-09-03 拆包自 manju_comfy.go,导出面)
type Client struct {
	Base   string
	HTTP   *http.Client
}

func NewClient(base string) *Client {
	if base == "" {
		base = "http://127.0.0.1:8190"
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// hasNode 查询 ComfyUI 是否装有某自定义节点(/object_info/<name>,200=有)
func (c *Client) HasNode(name string) bool {
	resp, err := c.HTTP.Get(c.Base + "/object_info/" + name)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode == 200
}

func (c *Client) Online() (string, error) {
	resp, err := c.HTTP.Get(c.Base + "/system_stats")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		System struct {
			ComfyVersion string `json:"comfyui_version"`
		} `json:"system"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
	return r.System.ComfyVersion, nil
}

// submit 提交工作流(API 格式 {"N": {"class_type","inputs"}}),返回 prompt_id
func (c *Client) Submit(workflow map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{"prompt": workflow})
	resp, err := c.HTTP.Post(c.Base+"/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		// 连接被拒绝/主机不可达:提示 ComfyUI 未就绪,而非甩技术栈
		msg := err.Error()
		if strings.Contains(msg, "connection refused") || strings.Contains(msg, "connectex") || strings.Contains(msg, "no such host") {
			return "", fmt.Errorf("ComfyUI 未就绪(连接失败)——已尝试自动启动,若仍失败请到灵动岛/ComfyUI 页查看日志后手动启动")
		}
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ComfyUI /prompt HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var r struct {
		PromptID   string `json:"prompt_id"`
		Number     int    `json:"number"`
		NodeErrors any    `json:"node_errors"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("ComfyUI 响应解析失败: %s", truncate(string(data), 200))
	}
	if r.PromptID == "" {
		return "", fmt.Errorf("ComfyUI 未返回 prompt_id: %s", truncate(string(data), 200))
	}
	return r.PromptID, nil
}

// history 查询单个 prompt 的执行记录(未完成返回 nil)
func (c *Client) History(promptID string) map[string]any {
	resp, err := c.HTTP.Get(c.Base + "/history/" + promptID)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var h map[string]map[string]any
	if json.Unmarshal(data, &h) != nil {
		return nil
	}
	return h[promptID]
}

// inQueue 查询任务是否仍在 ComfyUI 队列(running 或 pending)——审计 M1:
// history 无记录可能是"还在长队列排队"而非"任务丢失";tryReclaim 据此避免
// 90s 误判后重新提交造成同一镜头双任务烧两遍 GPU
// 返回 (是否在队列, 查询是否成功):查询失败(网络/超时)= ComfyUI 忙,不是"不在队列"
// queueBusy ComfyUI 队列是否忙碌(有任务在跑或排队)。空闲自动释放显存用。
func (c *Client) QueueBusy() (bool, error) {
	resp, err := c.HTTP.Get(c.Base + "/queue")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var q struct {
		QueueRunning []any `json:"queue_running"`
		QueuePending []any `json:"queue_pending"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		return false, err
	}
	return len(q.QueueRunning) > 0 || len(q.QueuePending) > 0, nil
}

func (c *Client) InQueue(promptID string) (bool, error) {
	resp, err := c.HTTP.Get(c.Base + "/queue")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var q struct {
		QueueRunning []map[string]any `json:"queue_running"`
		QueuePending []map[string]any `json:"queue_pending"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		return false, err
	}
	for _, it := range q.QueueRunning {
		if id, _ := it["prompt_id"].(string); id == promptID {
			return true, nil
		}
	}
	for _, it := range q.QueuePending {
		if id, _ := it["prompt_id"].(string); id == promptID {
			return true, nil
		}
	}
	return false, nil
}
func (c *Client) ComfyReachable() bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(c.Base + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
func (c *Client) Wait(promptID string, timeout, poll time.Duration, stopped ...func() bool) error {
	isStopped := func() bool {
		for _, fn := range stopped {
			if fn != nil && fn() {
				return true
			}
		}
		return false
	}
	// 审计 P3/7.1:停止感知触发时主动 POST /interrupt——仅 return"已停止"会让
	// ComfyUI 继续把当前任务跑完(逐镜 10-70min 白烧 GPU),中断是停止的完整语义
	stoppedOnce := false
	interruptOnStop := func() bool {
		if isStopped() && !stoppedOnce {
			stoppedOnce = true
			req, err := http.NewRequest("POST", c.Base+"/interrupt", nil)
			if err == nil {
				if resp, err := c.HTTP.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}
		}
		return isStopped()
	}
	// 审计 7.1:任务丢失检测——ComfyUI 重启后 history 一直无记录且不在队列,
	// 继续死等满超时毫无意义。
	// 修复(用户实测"镜头 1 渲染失败: ComfyUI 任务丢失"误判):history 在任务
	// 未完成时本就无记录,靠 inQueue 判断是否还在队列;但 ComfyUI 加载大模型
	// (几十 GB)期间 HTTP 接口会超时,旧逻辑把"查询失败"当"不在队列",
	// 连续 5 次(~50s)即误判任务丢失。新逻辑:
	//   - 接口查询失败(网络/超时)= ComfyUI 忙,不算丢失,重置计数
	//   - 仅当 ComfyUI 可达(根路径 200)且明确不在队列时计 miss
	//   - 连续 30 次(~5min,轮询 10s)才判丢失,容忍模型加载/接口抖动
	missTicks := 0
	deadline := time.Now().Add(timeout)
	// 停止检查节拍:远小于 poll(停止响应不被长轮询拖慢),但也避免空转忙等
	stopTick := time.Duration(500 * time.Millisecond)
	if poll < stopTick {
		stopTick = poll
	}
	for {
		if interruptOnStop() {
			return fmt.Errorf("已停止")
		}
		entry := c.History(promptID)
		if entry != nil {
			missTicks = 0
			if st, _ := entry["status"].(map[string]any); st != nil {
				if ss, _ := st["status_str"].(string); ss == "error" {
					return fmt.Errorf("ComfyUI 任务失败: %s", ComfyErrMsg(st))
				}
				if done, _ := st["completed"].(bool); done {
					return nil
				}
			}
		} else {
			inQ, qerr := c.InQueue(promptID)
			if qerr != nil {
				missTicks = 0 // 查询失败=ComfyUI 忙(加载模型/高负载),非任务丢失
			} else if !inQ {
				if !c.ComfyReachable() {
					// 服务不可达(可能重启中):连续确认才判丢失
					missTicks++
					if missTicks >= 30 {
						return fmt.Errorf("ComfyUI 任务丢失(服务不可达或已重启): %s", promptID)
					}
				} else {
					// 可达但任务不在队列:任务可能已结束在写 history 的间隙,重置等待
					missTicks = 0
				}
			} else {
				missTicks = 0
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ComfyUI 等待超时(%s)", timeout)
		}
		// 细粒度停止感知:分段 sleep,期间持续检查 stopped
		waited := time.Duration(0)
		for waited < poll {
			if interruptOnStop() {
				return fmt.Errorf("已停止")
			}
			step := poll - waited
			if step > stopTick {
				step = stopTick
			}
			time.Sleep(step)
			waited += step
		}
	}
}

func ComfyErrMsg(st map[string]any) string {
	if arr, ok := st["messages"].([]any); ok {
		for _, m := range arr {
			if mm, ok := m.(map[string]any); ok {
				if d, ok := mm["data"].(map[string]any); ok {
					if em := str(d["exception_message"]); em != "" {
						return truncate(em, 300)
					}
				}
			}
		}
	}
	if em := str(st["exception_message"]); em != "" {
		return truncate(em, 300)
	}
	return "未知错误"
}
