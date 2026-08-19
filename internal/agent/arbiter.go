package agent

// 终审官(Arbiter):返工预算耗尽后的自动拍板——AI 一条龙不把决策丢给人。
// 输入该镜判分/弱项/修复轮数,决策二选一:
//   accept      接受当前最佳结果进成片(轻微偏差/镜头本身难,重渲期望收益低于成本)
//   regenerate  增量修复已证明无效(结构性跑偏/机械质检硬伤反复),按分镜原文从零重写再试一轮
// 终审调用失败兜底 accept(绝不阻塞成片);决策与理由记入 Judgment.Arbiter 供审片报告展示。

import (
	"encoding/json"
	"fmt"
	"strings"
)

const arbiterSystem = `你是漫剧成片终审官。一个镜头经过多轮返工(修复师改写提示词+重渲染)后仍未达标,现在必须拍板:接受它进成片,还是换思路重写?

【决策标准】
- accept:问题属于轻微偏差(运镜略偏/风格细微不一致/个别维度略低于线),重渲的期望收益低于时间成本;或问题源于镜头本身难度(复杂动作/多角色同镜),重写也难显著改善。
- regenerate:审片反馈指向提示词结构性跑偏(主体错乱/场景完全不符/动作与分镜相反/近黑帧或无音轨等硬伤反复未修),增量修复已证明无效,必须从分镜原文重写。

【输出严格 JSON】{"decision": "accept" 或 "regenerate", "reason": "不超过 60 字中文"}`

// ArbiterDecide 终审拍板一个预算耗尽的镜头
func ArbiterDecide(llm TextLLM, meta ShotMeta, jd *Judgment, passScore float64, maxRetries int) (decision, reason string, err error) {
	weak := WeakDims(jd.Dimensions, 60)
	weakNames := make([]string, 0, len(weak))
	for _, d := range weak {
		if v, ok := jd.Dimensions[d.Key]; ok {
			weakNames = append(weakNames, fmt.Sprintf("%s %.0f 分", d.Name, v))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"shot": map[string]any{
			"shot_id": meta.ShotID, "scene": meta.Scene, "camera": meta.Camera,
			"action": meta.Action, "dialogue": meta.Dialogue,
			"mode": map[bool]string{true: "Ref2VA", false: "FL2VA"}[meta.HasChar],
		},
		"pass_score":      passScore,
		"total_score":     jd.Score,
		"weak_dimensions": weakNames,
		"issues":          jd.Issues,
		"qc_flags":        jd.QCFlags,
		"suggestion":      jd.Suggestion,
		"retries_used":    jd.Retries,
		"max_retries":     maxRetries,
	})
	out, err := llm.ChatJSON(arbiterSystem, string(payload), 0.2)
	if err != nil {
		return "", "", fmt.Errorf("终审官调用失败: %w", err)
	}
	d := strings.ToLower(strings.TrimSpace(stringOf(out["decision"])))
	if d != "accept" && d != "regenerate" {
		return "", "", fmt.Errorf("终审决策非法: %q", truncateArb(d, 40))
	}
	return d, strings.TrimSpace(stringOf(out["reason"])), nil
}

func truncateArb(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
