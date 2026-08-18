package render

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nilix/internal/assemble"
	"nilix/internal/backend"
	"nilix/internal/config"
	"nilix/internal/storyboard"
	"nilix/internal/verify"
)

// Job 是一个渲染任务（一部脚本的所有镜头）。
type Job struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Status  string    `json:"status"` // running / completed / failed
	Total   int       `json:"total"`
	Done    int       `json:"done"`
	Current string    `json:"current"`
	Outputs     []string       `json:"outputs"`
	FinalOutput string         `json:"final_output,omitempty"`
	QC          *verify.Report `json:"qc,omitempty"`
	Error       string         `json:"error,omitempty"`
	Started     time.Time      `json:"started"`
}

// Manager 管理渲染任务（异步逐镜执行）。
type Manager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	comfy *backend.ComfyUIClient
}

// NewManager 构造任务管理器。
func NewManager(comfy *backend.ComfyUIClient) *Manager {
	return &Manager{jobs: make(map[string]*Job), comfy: comfy}
}

// SetComfyURL 设置变化后同步 ComfyUI 客户端地址(设置页保存时由 api 层调用)
func (m *Manager) SetComfyURL(url string) {
	if url == "" {
		return
	}
	m.mu.Lock()
	m.comfy = backend.NewComfyUIClient(url)
	m.mu.Unlock()
}

// Submit 提交一部脚本的渲染任务并异步执行；每次提交用最新渲染配置。
func (m *Manager) Submit(script *storyboard.Script, seed int, cfg config.Settings, outDir string) *Job {
	job := &Job{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Title:   script.Title,
		Status:  "running",
		Total:   len(script.Shots),
		Started: time.Now(),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go m.run(job, script, seed, cfg, outDir)
	return job
}

func (m *Manager) run(job *Job, script *storyboard.Script, seed int, cfg config.Settings, outDir string) {
	renderer := NewRenderer(m.comfy, cfg.Render, outDir, cfg.Paths.ComfyInput)

	// 1. 资产生成：为每个角色生成参考图（用于 R2V 锁脸）。
	var refImages []string
	for _, ch := range script.Characters {
		prompt := characterPrompt(script.Style, ch.Description)
		img, err := renderer.generateImage(context.Background(), prompt, 768, 1024, seed, "char_"+safeName(ch.Name), cfg.Paths.ComfyInput)
		if err != nil {
			m.mu.Lock()
			job.Status = "failed"
			job.Error = fmt.Sprintf("角色 %s 参考图生成失败: %v", ch.Name, err)
			m.mu.Unlock()
			return
		}
		refImages = append(refImages, img)
	}

	// 2. 逐镜 R2V 渲染（参考图锁脸）。
	for i, sh := range script.Shots {
		m.mu.Lock()
		job.Current = sh.ID
		m.mu.Unlock()

		out, err := renderer.RenderShot(context.Background(), sh.H3Prompt, sh.DurationSec, seed+i, sh.ID, refImages)
		if err != nil {
			m.mu.Lock()
			job.Status = "failed"
			job.Error = fmt.Sprintf("镜头 %s: %v", sh.ID, err)
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		job.Outputs = append(job.Outputs, out)
		job.Done = i + 1
		m.mu.Unlock()
	}
	m.mu.Lock()
	job.Status = "completed"
	job.Current = ""
	m.mu.Unlock()

	// 渲染完成后自动合成成片。
	if len(job.Outputs) > 0 {
		final := filepath.Join(outDir, "final_"+job.ID+".mp4")
		if err := assemble.Assemble(job.Outputs, final); err != nil {
			m.mu.Lock()
			job.Error = "合成失败: " + err.Error()
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		job.FinalOutput = final
		m.mu.Unlock()

		// 自动质检。
		if rep, err := verify.Verify(final); err == nil {
			m.mu.Lock()
			job.QC = rep
			m.mu.Unlock()
		}
	}
}

// stylePrompt 把中文风格名映射成文生图提示词锚。
var stylePrompt = map[string]string{
	"写实电影级": "realistic cinematic photograph",
	"动漫二次元": "2D anime illustration style",
	"CG大片":   "3D CG render style",
}

func characterPrompt(style, desc string) string {
	s := stylePrompt[style]
	if s == "" {
		s = "anime style"
	}
	return fmt.Sprintf("%s, character reference sheet, %s, full body, clean plain background", s, desc)
}

func safeName(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return r.Replace(s)
}

// Status 返回任务状态。
func (m *Manager) Status(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List 返回所有任务（新在前）。
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out
}
