// 审片官:对渲染完成的镜头逐镜判分。
// 维度体系严格对齐 MiniMax H3 官方说明(README + h3-prompt-writing skill):
//   - Ref2VA「全参考模式」的 retention_analysis 体系 → 主体/场景一致性
//   - 官方宣称的「复杂多模态指令遵循」→ 动作/运镜符合度(运镜=类型+幅度+速度)
//   - <d>[中文]台词</d> 原生对白 + 说话者 (Sx) → 口型/对白状态(帧级代理,低权重)
//   - 社区已知失败模式(近黑帧/面部扭曲/闪烁/文字水印)→ 可见性/技术质量
// 打分只取模型各维度 0-100 分值,加权总分由 Go 计算(防模型自报虚高)。
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ShotMeta 审片输入的镜头元数据(由管线从 direct_plan 组装)
type ShotMeta struct {
	ShotID     int
	Scene      string
	SceneDesc  string
	Characters []string
	CharDesc   string // 主要角色外观(方案 appearance/costume 拼接)
	ShotSize   string
	Camera     string
	Action     string
	Dialogue   string
	Narration  string
	StyleDesc  string // 风格英文描述(与渲染时同款措辞)
	HasChar    bool   // 有角色=R2V(参考定妆照);空镜=FL2VA(参考场景图)
}

// judgeSystem 审片官系统提示词
const judgeSystem = `你是 AI 漫剧生产的资深审片官,专审 MiniMax H3(参考生成模式 R2V/空镜 FL2VA)渲染出的镜头。
你将看到:该镜头的若干抽取帧(按时间顺序)、角色参考图(定妆照/正脸,R2V 的 <Picture 1>)、场景参考图(<Picture 2>),以及该镜的分镜要求。

请严格按以下八个维度逐项打 0-100 分(整数),对照依据为 MiniMax H3 官方能力与已知缺陷:
1. identity 主体一致性:主体面部/发型/服装与角色参考图的保持度。H3 官方 Ref2VA 以 retention_analysis 承诺参考保持(fully_preserved);人脸 token 少、身份漂移是最常见失败。空镜无角色时给 90。
2. scene 场景还原:画面环境与场景参考图的空间布局/材质/光线一致度。
3. action 动作符合:画面呈现的动作/事件与分镜 action 描述的相符程度(H3 官方核心能力「复杂多模态指令遵循」)。
4. camera 运镜符合:从帧序列推断的机位/景别变化是否符合分镜 camera 要求(推拉摇移/幅度/速度)。无法推断时按景别正确性给分。
5. visibility 主体可见性:主体是否清晰可见、曝光是否正常。H3 已知失败模式是暗场景渲染成近全黑(整帧平均亮度极低);主体淹没/大面积死黑必须重罚。
6. tech 技术质量:解剖畸变(手指/面部扭曲)、闪烁、残影、乱入文字/水印/字幕等伪影(H3 无负面词机制,此类问题只能靠重渲染解决)。
7. style 风格统一:与指定风格描述(如 2.5D 动漫/写实/水墨)的贴合度,以及作为剧集一员的观感统一。
8. lips 口型对白:有台词的镜头,说话者嘴部应有开合动作、且非画外音式紧闭;纯旁白/空镜镜头嘴部应闭合。有台词/旁白的镜头,画面人物说话内容必须与分镜台词/旁白一致(画面内容与分镜对不上=废镜,重罚)。仅凭帧判断可靠性有限,从严只降不升。

【情节符合度·最高优先思考项】你审的不是"一张好看的图",而是"这一镜是否忠实演绎了分镜要求的剧情"。逐条对照分镜的 narration(旁白)/action(动作)/dialogue(台词)检查画面:
- 画面里出现的情节/角色/道具,必须是分镜里明确要求的;出现分镜没有的人物、物件、事件=幻觉内容,必须重罚。
- 画面演的事件必须与分镜 action 一致(谁、对谁、做了什么、结果如何);张冠李戴(把 A 的戏演成了 B 的戏)、事件顺序错乱=重罚。
- 台词与口型:有 dialogue 的镜头,画面人物应处于"正在说话"状态;旁白镜头人物不应开口。
- 凡因"画面与剧情脱节"扣分,必须在 issues 里用中文写明"第几帧出现什么幻觉/偏差,分镜要求是什么"。
- 此类问题一律把 action(必要时连带 scene)打到 40 分以下,杜绝带病进入成片。

评分纪律:
- 90+ 优秀专业可播出;75-89 合格;60-74 有瑕疵可接受;40-59 明显问题需返工;<40 严重失败。
- issues 用中文短句列出具体问题(每条一句话,指出哪一帧/哪个主体);suggestion 给出针对提示词的修复方向(如加强光照描述/收紧动作描述),中文。
- 只依据可见证据打分,不臆测;参考图与帧中都看不清的项目给 70(中性),不要给 0。

输出严格 JSON(不要任何其他文字):
{"dimensions":{"identity":90,"scene":85,"action":80,"camera":75,"visibility":90,"tech":85,"style":88,"lips":70},"issues":["第2帧主角面部与参考图发型不符"],"suggestion":"在提示词 detailed_description 中强化发型与光照描述"}`

// Judge 审片一个镜头。frames=抽帧 JPEG 路径;refImages=参考图路径(角色定妆照/场景图,可空)。
// 返回 Judgment(不含重试计数,由调用方维护)。视觉调用失败返回 error(调用方降级为机械质检)。
func Judge(vc *VisionClient, meta ShotMeta, frames, refImages []string, passScore float64) (*Judgment, error) {
	var b strings.Builder
	b.WriteString("【分镜要求】\n")
	metaJSON, _ := json.MarshalIndent(map[string]any{
		"shot_id": meta.ShotID, "scene": meta.Scene, "scene_description": meta.SceneDesc,
		"characters": meta.Characters, "character_appearance": meta.CharDesc,
		"shot_size": meta.ShotSize, "camera": meta.Camera, "action": meta.Action,
		"dialogue": meta.Dialogue, "narration": meta.Narration,
		"style": meta.StyleDesc, "mode": map[bool]string{true: "Ref2VA(有角色,参考图1=角色定妆照)", false: "FL2VA(空镜,参考图=场景图)"}[meta.HasChar],
	}, "", "  ")
	b.Write(metaJSON)
	b.WriteString("\n\n【图片顺序说明】前 " + fmt.Sprint(len(frames)) + " 张为镜头抽取帧(按时间顺序),其后为参考图(角色定妆照/场景图)。")
	b.WriteString("\n请逐维度打分并输出 JSON。")

	out, err := vc.ChatJSON(judgeSystem, b.String(), append(append([]string{}, frames...), refImages...), 0.1)
	if err != nil {
		return nil, err
	}
	j := &Judgment{Model: vc.LastUsed(), JudgedAt: time.Now().Unix()} // 记实际使用模型(链降级时为备模型)
	dims := map[string]float64{}
	if raw, ok := out["dimensions"].(map[string]any); ok {
		for _, d := range Dims {
			if v, ok := ToFloat(raw[d.Key]); ok {
				dims[d.Key] = clamp(v)
			}
		}
	}
	if len(dims) == 0 {
		return nil, fmt.Errorf("审片输出缺少 dimensions")
	}
	j.Dimensions = dims
	score, fb := WeightedScore(dims)
	j.Score = score
	j.Fallback = fb
	if arr, ok := out["issues"].([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				j.Issues = append(j.Issues, strings.TrimSpace(s))
			}
		}
	}
	j.Suggestion = strings.TrimSpace(stringOf(out["suggestion"]))
	j.Status = "failed"
	if score >= passScore {
		j.Status = "pass"
	}
	return j, nil
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
