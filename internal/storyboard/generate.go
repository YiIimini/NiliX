package storyboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"nilix/internal/backend"
)

// Styles 是可选的三套风格模板。
var Styles = []string{"写实电影级", "动漫二次元", "CG大片"}

// styleAnchor 把中文风格名映射成 H3 提示词里的英文风格锚。
var styleAnchor = map[string]string{
	"写实电影级": "Live-action, cinematic, realistic film style",
	"动漫二次元": "2D-animated, cel-shaded anime style",
	"CG大片":   "3D CG, cinematic blockbuster style",
}

const scriptPromptTemplate = `你是资深 AI 视频导演。根据用户提供的小说内容，生成一集用于 MiniMax H3 视频生成的分镜脚本。

必须只输出一个合法 JSON（不要 markdown 代码块、不要任何解释文字），结构如下：
{
  "title": "本集标题",
  "characters": [
    {"name": "角色名", "role": "主角/反派/配角", "description": "年龄、性别、发型、瞳色、脸型、服装、性格，逐项明确以锁定一致性"}
  ],
  "scenes": [
    {"name": "场景名（必须取自原文）", "description": "环境、光线、地标、氛围"}
  ],
  "shots": [
    {
      "id": "S01",
      "duration_sec": 6,
      "hook": "setup/reveal/reversal/suspense/tender/climax 之一",
      "continuity": "一句话：如何承接上一镜、铺垫下一镜",
      "description": "镜头描述：景别、运镜、人物动作、每秒关键变化",
      "h3_prompt": "该镜头的完整 H3 提示词（英文，官方语法）"
    }
  ]
}

硬性规则：
1. 单集 3-8 个镜头；每镜 duration_sec 取 4-12 的整数。
2. h3_prompt 用英文写，遵循 H3 官方语法，必须包含三段：
   - integrated_multimodal_description：以 [Shot 1] 开头，多镜头用 [Shot N] At MM:SS.mmm 标切点；
   - overall_soundscape：环境音/动作音总结；
   - non_diegetic_music：背景音乐（无则写 N/A）。
3. 对白写法：说话人加稳定 ID，如 The young woman with a soft voice (S1) says: <d>[中文]台词</d>；台词保留原文中文。
4. 场景名必须取自原文，不得杜撰。
5. 视觉风格统一为：{anchor}

小说内容：
{novel}`

// Generate 调用 LLM 把小说生成一集视频脚本。
func Generate(ctx context.Context, llm *backend.LLMClient, novel, style string) (*Script, error) {
	if strings.TrimSpace(novel) == "" {
		return nil, errors.New("小说内容为空")
	}
	if style == "" {
		style = Styles[0]
	}
	anchor := styleAnchor[style]
	if anchor == "" {
		anchor = style
	}

	// 审计 H16:小说内容超长时截断——整本 120 万字直接灌单条 prompt 必超上下文/巨额 token,
	// 服务端收口(此前无任何长度控制)。保留开头(通常含主角设定与开局),提示按章分段更稳
	const novelBudget = 12000
	if rn := []rune(novel); len(rn) > novelBudget {
		novel = string(rn[:novelBudget]) + "\n\n[注:原文过长已截断,如需完整章节请按章分段生成]"
	}
	prompt := strings.ReplaceAll(scriptPromptTemplate, "{anchor}", anchor)
	prompt = strings.ReplaceAll(prompt, "{novel}", novel)

	raw, err := llm.Chat(ctx, []backend.ChatMessage{{Role: "user", Content: prompt}}, 16384, 0.4)
	if err != nil {
		return nil, err
	}

	var s Script
	if err := json.Unmarshal([]byte(extractJSON(raw)), &s); err != nil {
		return nil, fmt.Errorf("解析 LLM 输出失败: %w（输出前 200 字: %s）", err, truncate(raw, 200))
	}
	if len(s.Shots) == 0 {
		return nil, errors.New("LLM 未生成任何镜头")
	}
	s.Style = style
	return &s, nil
}

// extractJSON 从 LLM 输出中提取 JSON（清理 markdown 代码块包裹与前后杂文）。
func extractJSON(s string) string {
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if n := strings.Index(s, "\n"); n >= 0 && strings.TrimSpace(s[:n]) == "json" {
			s = s[n+1:]
		}
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return s
}

func truncate(s string, n int) string {
	// rune 截断(审计:字节截断会切断 UTF-8 多字节字符出乱码)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
