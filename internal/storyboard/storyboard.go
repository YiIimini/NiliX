// Package storyboard 实现剧本引擎：把小说文本生成结构化视频分镜脚本。
package storyboard

// Script 是一集小说生成的完整视频脚本。
type Script struct {
	Title      string          `json:"title"`
	Style      string          `json:"style"`
	Characters []CharacterCard `json:"characters"`
	Scenes     []SceneCard     `json:"scenes"`
	Shots      []Shot          `json:"shots"`
}

// CharacterCard 角色卡，用于锁定角色一致性（Ref2VA 参考图来源）。
type CharacterCard struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description"`
}

// SceneCard 场景卡，空镜/场景参考来源。
type SceneCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Shot 单个镜头，是后续 H3 渲染与调度的最小单元。
type Shot struct {
	ID          string  `json:"id"`
	DurationSec float64 `json:"duration_sec"`
	Hook        string  `json:"hook"`
	Continuity  string  `json:"continuity"`
	Description string  `json:"description"`
	H3Prompt    string  `json:"h3_prompt"`
}
