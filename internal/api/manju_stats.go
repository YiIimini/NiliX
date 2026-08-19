package api

// LLM/VLM token 用量记账(ArcReel ApiCall 逐次记账的桌面单机版):
// manjuLLM.chat 与 agent.VisionClient 每次成功调用回抛 usage → 按项目累计到
// <项目>/llm_stats.json(分模型),status 接口带给前端展示「一次一条龙烧了多少 token」。
// 本地 GPU 渲染无 API 费用,计账对象只有文本/视觉两类外部调用;累计值跨次运行保留
// (与学习记忆同生命周期,删除项目即随目录删除)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nilix/internal/agent"
)

// manjuStatEntry 单模型累计
type manjuStatEntry struct {
	Calls       int `json:"calls"`
	Prompt      int `json:"promptTokens"`
	Completion  int `json:"completionTokens"`
	Total       int `json:"totalTokens"`
}

// manjuLLMStats 项目级 token 记账
type manjuLLMStats struct {
	Calls     int                       `json:"calls"`
	Total     int                       `json:"totalTokens"`
	ByModel   map[string]*manjuStatEntry `json:"byModel"`
	UpdatedAt int64                     `json:"updatedAt"`
}

var manjuStatsMu sync.Mutex

func manjuStatsPath(project string) string {
	return filepath.Join(manjuRoot, project, "llm_stats.json")
}

// manjuStatsAdd 累计一次调用(usage 为空/项目名为空时静默跳过)
func manjuStatsAdd(project, model string, u agent.Usage) {
	if project == "" || u.TotalTokens == 0 {
		return
	}
	if model == "" {
		model = "(unknown)"
	}
	manjuStatsMu.Lock()
	defer manjuStatsMu.Unlock()
	st := manjuStatsLoadLocked(project)
	if st.ByModel == nil {
		st.ByModel = map[string]*manjuStatEntry{}
	}
	e := st.ByModel[model]
	if e == nil {
		e = &manjuStatEntry{}
		st.ByModel[model] = e
	}
	e.Calls++
	e.Prompt += u.PromptTokens
	e.Completion += u.CompletionTokens
	e.Total += u.TotalTokens
	st.Calls++
	st.Total += u.TotalTokens
	st.UpdatedAt = time.Now().Unix()
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(filepath.Dir(manjuStatsPath(project)), 0755)
	_ = os.WriteFile(manjuStatsPath(project), b, 0644)
}

// manjuStatsLoad 读项目记账(无文件返回空结构)
func manjuStatsLoad(project string) *manjuLLMStats {
	manjuStatsMu.Lock()
	defer manjuStatsMu.Unlock()
	return manjuStatsLoadLocked(project)
}

func manjuStatsLoadLocked(project string) *manjuLLMStats {
	st := &manjuLLMStats{ByModel: map[string]*manjuStatEntry{}}
	if project == "" {
		return st
	}
	b, err := os.ReadFile(manjuStatsPath(project))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, st)
	if st.ByModel == nil {
		st.ByModel = map[string]*manjuStatEntry{}
	}
	return st
}
