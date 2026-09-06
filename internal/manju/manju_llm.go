package manju

// 漫剧管线 LLM 客户端(DeepSeek chat/completions,JSON 直出)
// 提示词模板移植自 h3-direct-render skill(v6,对齐 H3 官方 h3-prompt-writing)。

import (
	"nilix/internal/util"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nilix/internal/agent"
)

type manjuLLM struct {
	apiKey      string
	baseURL     string
	model       string
	temperature float64
	maxTokens   int
	timeout     time.Duration
	client      *http.Client
	// onUsage 每次成功调用回抛 token 用量(项目级 llm_stats.json 记账;可为 nil)
	onUsage func(model string, u agent.Usage)
	// stopped 停止感知回调(用户点「停止」后 LLM 请求立即放弃,不再等 300s 超时;可为 nil)
	stopped func() bool
}

// SetStopped 注入停止感知回调(渲染管线 newManjuCtx 时设置;LLM 长请求是"停止无反应"的残留点)
func (l *manjuLLM) SetStopped(fn func() bool) {
	if l != nil {
		l.stopped = fn
	}
}

// manjuLLMFromCfg 从 config.json 构造 LLM 客户端(服务/模型缺省时回退到存为默认的服务,再回退 DeepSeek)
func manjuLLMFromCfg(cfg map[string]any) *manjuLLM {
	L, _ := cfg["llm"].(map[string]any)
	defBase, defModel := manjuDefaultLLMService()
	baseURL := strings.TrimRight(str(L["base_url"]), "/")
	if baseURL == "" {
		baseURL = defBase
	}
	model := str(L["model"])
	if model == "" {
		model = defModel
	}
	temp := 0.4
	if v, ok := manjuToFloat(L["temperature"]); ok {
		temp = v
	}
	maxTok := 8192
	if n, ok := manjuToInt(L["max_tokens"]); ok {
		maxTok = n
	}
	to := 300 * time.Second
	if n, ok := manjuToInt(L["request_timeout"]); ok && n > 0 {
		to = time.Duration(n) * time.Second
	}
	return &manjuLLM{
		apiKey:      str(L["api_key"]),
		baseURL:     baseURL,
		model:       model,
		temperature: temp,
		maxTokens:   maxTok,
		timeout:     to,
		client:      &http.Client{Timeout: to},
	}
}

// manjuDefaultLLMService 读「存为默认」的服务配置(settings.json),缺省回退 DeepSeek
func manjuDefaultLLMService() (baseURL, model string) {
	baseURL, model = "https://api.deepseek.com", "deepseek-chat"
	if b, err := os.ReadFile(manjuSettingsFile); err == nil {
		var def map[string]any
		if json.Unmarshal(b, &def) == nil {
			if u := strings.TrimRight(str(def["base_url"]), "/"); u != "" {
				baseURL = u
			}
			if m := str(def["model"]); m != "" {
				model = m
			}
		}
	}
	return baseURL, model
}

// errLLMTruncated LLM 输出达到 max_tokens 被截断(JSON 必然残缺,可精简重试)
var errLLMTruncated = fmt.Errorf("LLM 输出被截断(length)")

// chat 单次补全,返回 content 文本
func (l *manjuLLM) chat(system, user string, temp float64) (string, error) {
	if temp == 0 {
		temp = l.temperature
	}
	body := map[string]any{
		"model":       l.model,
		"temperature": temp,
		"max_tokens":  l.maxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]string{"type": "json_object"},
		"stream":          false,
	}
	b, _ := json.Marshal(body)
	// 审计 H12:瞬时失败(429/5xx/网络抖动)指数退避重试,单次抖动不再打崩整条管线
	// (此前零重试:plan 一次 429 即中断 AI 一条龙;修复师失败按原提示词白烧 GPU)
	// 修复:http.Request 的 Body 只能读一次——每次重试必须重建请求(新 bytes.Reader),
	// 否则第 2/3 次尝试发送空 body,退避重试实际失效(审查 P1)。
	// 审计 5.2:请求带 context——stopped 回调触发即 cancel,在飞 LLM 请求(最长 300s)
	// 立即中断,不再"停止后还要干等请求自然结束"(停止无反应的最后一个残留点)。
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 停止感知:用户点「停止」后立即放弃(LLM 请求最长 300s,卡住时停止必须生效)
		if l.stopped != nil && l.stopped() {
			return "", fmt.Errorf("已停止")
		}
		// 每次尝试独立 context:stopped 转 true 时取消在途请求
		reqCtx, reqCancel := context.WithCancel(context.Background())
		if l.stopped != nil {
			go func() {
				t := time.NewTicker(200 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-reqCtx.Done():
						return
					case <-t.C:
						if l.stopped() {
							reqCancel()
							return
						}
					}
				}
			}()
		}
		req, err := http.NewRequestWithContext(reqCtx, "POST", l.baseURL+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			reqCancel()
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+l.apiKey)
		resp, err := l.client.Do(req)
		if err != nil {
			reqCancel()
			// 停止触发导致 context 取消:报告为已停止(而非"请求失败")
			if l.stopped != nil && l.stopped() {
				return "", fmt.Errorf("已停止")
			}
			lastErr = fmt.Errorf("LLM 请求失败: %w", err)
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt*2) * time.Second)
			}
			continue
		}
		// 注意:不能在此立即 reqCancel()——响应头到达不代表 body 已读完,
		// context 取消会中断正在流式传输的响应体(chunked),ReadAll 读到空/截断
		// → json.Unmarshal "unexpected end of JSON input"(实测 bug)。
		// 读完 body 并关闭后再 cancel(同时让监听 goroutine 退出)。
		if resp.StatusCode == 200 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			reqCancel()
			var r struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
					Message      struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage agent.Usage `json:"usage"`
			}
			if err := json.Unmarshal(data, &r); err != nil {
				return "", fmt.Errorf("LLM 响应解析失败: %w", err)
			}
			if len(r.Choices) == 0 {
				return "", fmt.Errorf("LLM 空响应")
			}
			// 输出打满 max_tokens:JSON 被截断,直接判失败(调用方可精简重试)
			if r.Choices[0].FinishReason == "length" {
				return "", errLLMTruncated
			}
			if l.onUsage != nil && r.Usage.TotalTokens > 0 {
				l.onUsage(l.model, r.Usage)
			}
			return r.Choices[0].Message.Content, nil
		}
		// 429/5xx:退避重试
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		reqCancel()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt*2) * time.Second)
				continue
			}
			return "", lastErr
		}
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return "", lastErr
}

// chatJSON 请求 JSON 对象(剥离可能的 ```json 围栏)
func (l *manjuLLM) chatJSON(system, user string, temp float64) (map[string]any, error) {
	text, err := l.chat(system, user, temp)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	// 审计 5.3:先整体解析——输出正文若先出现 `{`(错误说明/示例),直接截取会切到
	// 错误位置;整体解析失败才做围栏/花括号剥离(容错启发式)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out, nil
	}
	// 剥 ```json 围栏
	if i := strings.Index(text, "```"); i >= 0 {
		if j := strings.Index(text[i+3:], "```"); j >= 0 {
			text = text[i+3 : i+3+j]
		} else {
			text = text[i+3:]
		}
		text = strings.TrimSpace(text)
		if err := json.Unmarshal([]byte(text), &out); err == nil {
			return out, nil
		}
	}
	// 最后兜底:截取首个 { 到末个 } 之间
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
			if err := json.Unmarshal([]byte(text), &out); err == nil {
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("LLM 输出非 JSON: %v (前 200 字: %s)", err, truncate(text, 200))
}

func truncate(s string, n int) string { return util.Truncate(s, n) }

// ---- 知识库注入:config.knowledge 指向 zhishiku/AI漫剧生产 模板(与旧 direct_render.py 同款) ----

// manjuKBRoots knowledge 模板候选根目录(相对路径依次尝试):
// 旧部署路径 zhishiku/AI漫剧生产 已废弃(2026-08-25 实测不存在),实际模板在 创作管理/AI漫剧 下。
// 逐根探测,首个存在的目录生效——新项目/迁移项目无需改 config,老 config 也能自动落到新路径。
var manjuKBRoots = []string{
	`C:\Mi\Ai\WorkBench\zhishiku\创作管理\AI漫剧`,
	`C:\Mi\Ai\WorkBench\zhishiku\创作管理\AI漫剧\提示词模板`,
	`C:\Mi\Ai\WorkBench\zhishiku\AI漫剧生产`, // 旧路径兜底(存在则仍可用)
}

// manjuKBLastMissing 最近一次 knowledge 加载缺失的模板清单(供 plan 阶段日志提示,防静默失效)。
var manjuKBLastMissing []string

func manjuKBChunks(cfg map[string]any, keys []string) string {
	kb, _ := cfg["knowledge"].(map[string]any)
	if kb == nil {
		return ""
	}
	var out []string
	missing := []string{}
	for _, k := range keys {
		arr, _ := kb[k].([]any)
		for _, x := range arr {
			rel := str(x)
			if rel == "" {
				continue
			}
			p := rel
			if !filepath.IsAbs(p) {
				// 相对路径:逐根尝试(模板分散在 AI漫剧 根目录与 提示词模板 子目录)
				found := ""
				for _, r := range manjuKBRoots {
					if st, err := os.Stat(r); err == nil && st.IsDir() {
						cand := filepath.Join(r, rel)
						if fileExists(cand) {
							found = cand
							break
						}
					}
				}
				if found == "" {
					missing = append(missing, rel)
					continue
				}
				p = found
			}
			data, err := os.ReadFile(p)
			if err != nil {
				missing = append(missing, rel)
				continue
			}
			txt := string(data)
			if r := []rune(txt); len(r) > 5000 {
				txt = string(r[:5000])
			}
			out = append(out, "【知识库模板《"+filepath.Base(rel)+"》要点(仅作设定/风格参考,必须贴合本剧世界观)】\n"+txt)
		}
	}
	manjuKBLastMissing = missing
	return strings.Join(out, "\n\n")
}

func manjuKnowledgeChunks(cfg map[string]any) (kbChar, kbScene, kbStory string) {
	kbChar = manjuKBChunks(cfg, []string{"characters", "character"})
	kbScene = manjuKBChunks(cfg, []string{"scenes", "scene"})
	kbStory = manjuKBChunks(cfg, []string{"storyboard", "story"})
	return
}

// ---- 方案直出(整体:人物/场景/分镜) ----

// manjuStyleSpec 一个渲染风格的三处措辞(方案 image_prompt / H3 六段式开头 / H3 空镜)。
type manjuStyleSpec struct {
	asset   string // 图片模型(定妆照/场景图)风格措辞
	opening string // H3 六段式 detailed_description 开头风格
	shot1   string // H3 空镜 [Shot 1] 风格
}

// manjuStyles 预设风格档位。style 值不在表内时视为「自定义风格」原样使用(英文风格描述)。
// 2026-08-24 用户规则升级:所有定妆照必须「写实拟动漫」——
// ①禁日漫(日本动漫脸):asset 措辞不用裸 "anime"(SDXL 对 anime 关键词高度敏感,正面拉向日漫),
//   改用「东方风格化插画/国风 CG」表述;②禁真人(侵权风险):不再写 photorealistic/real human,
//   统一「写实拟动漫:semi-realistic stylized illustration」——既非纯真人照片也非日漫脸。
// 防御分三层:这里(风格措辞不诱导) + manjuPortraitAnchor(正向锚强制) + manjuNegPrompt(负面排除)。
// 2026-08-24 升级(知识库「官方风格技能与漫剧优化」):风格措辞升级为官方风格签名段
// (Pixar 3D 动画/纸拼贴/纸艺定格/手绘实拍/极简产品)——官方 skills 目录 8 风格技能的可照抄签名词,
// 换风格只换美术字段(渲染媒介/色彩/灯光),不重写时间线;新增 minimal(极简产品)预设。
var manjuStyles = map[string]manjuStyleSpec{
	"2.5d":       {"2.5D stylized CG illustration, semi-realistic, East Asian art style, cinematic lighting", "The target video is in a 2.5D stylized CG illustration style, semi-realistic, East Asian art style, cinematic realistic lighting, high quality illustration", "[Shot 1] 2.5D stylized CG illustration, semi-realistic, East Asian art style, cinematic realistic lighting"},
	"real":       {"cinematic semi-realistic stylized illustration, film-like lighting, painterly realism", "The target video is in a cinematic semi-realistic stylized style, film-like lighting", "[Shot 1] Cinematic semi-realistic stylized, film-like lighting"},
	"3d":         {"Pixar-inspired 3D cartoon rendering, C4D + Octane look, warm subsurface scattering skin, sculpted hair clumps, non-realistic", "The target video is in a high-quality Pixar-inspired 3D cartoon animation style, C4D + Octane look, warm subsurface scattering skin, sculpted hair clumps, cinematic lighting", "[Shot 1] Pixar-inspired 3D cartoon animation, C4D + Octane look, warm subsurface scattering skin, sculpted hair clumps"},
	"anime":      {"stylized East Asian animation illustration, semi-realistic, painterly cel shading, cinematic lighting", "The target video is in a stylized East Asian animation illustration style, semi-realistic, painterly cel shading, cinematic lighting", "[Shot 1] Stylized East Asian animation illustration, semi-realistic, painterly cel shading"},
	"handdrawn":  {"hand-drawn illustration, crayon/chalk/colored pencil/pastel texture, slightly trembling lines, uneven smudging, rough edges, frame-by-frame redraw feel", "The target video is in a hand-drawn illustration style, crayon/chalk/colored pencil/pastel texture, slightly trembling lines, uneven smudging, rough edges, frame-by-frame redraw feel, cinematic lighting", "[Shot 1] Hand-drawn illustration style, crayon/chalk/colored pencil/pastel texture, trembling lines, rough edges"},
	"papercraft": {"handmade papercraft stop-motion, miniature diorama, layered cardboard cutouts, visible thickness, matte paper textures, real drop shadows, 2.5D parallax", "The target video is in a handmade papercraft stop-motion style, miniature diorama, layered cardboard cutouts, visible thickness, matte paper textures, real drop shadows, 2.5D parallax, cinematic lighting", "[Shot 1] Handmade papercraft stop-motion, miniature diorama, layered cardboard cutouts, matte paper textures, real drop shadows, 2.5D parallax"},
	"clay":       {"claymation stop-motion, plasticine figures, tactile", "The target video is in a claymation stop-motion style, plasticine figures, tactile, cinematic lighting", "[Shot 1] Claymation stop-motion style, plasticine figures"},
	"ink":        {"Chinese ink wash painting, traditional brushwork, minimalist", "The target video is in a Chinese ink wash painting style, traditional brushwork, minimalist, cinematic lighting", "[Shot 1] Chinese ink wash painting style, traditional brushwork"},
	"minimal":    {"minimalist product design, white-tech aesthetic with dark rim light, brand color field, light lifestyle scene", "The target video is in a minimalist product design style, white-tech aesthetic with dark rim light, brand color field, light lifestyle scene, cinematic lighting", "[Shot 1] Minimalist product design, white-tech aesthetic, dark rim light, brand color field"},
}

// manjuPortraitAnchor 角色定妆照正向锚(2026-08-24 用户规则升级,强制附加到每个角色 image_prompt):
// 写实拟动漫 = 半写实风格化插画 + 东方/中式面孔 + 禁日漫 + 禁真人(防侵权)。
// 无论风格(写实/2.5D/动漫/水墨)一律执行——LLM 直出、脚本素材抽卡、视图派生全部走它。
// 2026-08-26 例外:次世代3D/BJD 风格(manjuStyleIs3D)改用 manju3DPortraitAnchor——
// 用户反馈"写实被干成动漫":painterly/illustration 措辞把 3D 渲染拉向 2D 插画;
// 3D 渲染虚拟人本身非真人照片,防侵权由 3D 锚内"not a real person"声明承担。
const manjuPortraitAnchor = "semi-realistic stylized illustration of an East Asian/Chinese character, subtly stylized painterly art, not a photorealistic photo of a real person, not a Japanese anime/manga style, avoid japanese-style facial features, avoid japanese anime eyes, avoid resembling any real person"

// manju3DPortraitAnchor 次世代3D/BJD 人偶风格人像锚(2026-08-26 用户新增):
// 次世代 3D 渲染 + 虚拟数字人 + BJD 人偶质感,替代插画风锚;不做 photorealistic→stylized 替换
// (3D 渲染语汇本身就是"非真人照片"的防侵权正解,替换反而把 3D 拉向 2D 插画/动漫)。
const manju3DPortraitAnchor = "next-generation 3D CGI render of a virtual digital human character, BJD doll aesthetic, porcelain-smooth skin with fine subsurface scattering, soft cinematic studio lighting, physically based rendering, clearly a stylized 3D rendered virtual character, not a real person, not a photograph of a real human, not a Japanese anime/manga style, avoid japanese-style facial features, avoid japanese anime eyes"

// manju3DSceneAnchor 次世代3D 风格场景锚(2026-08-26):3D 渲染虚拟场景,空场景无人保留。
const manju3DSceneAnchor = "3D rendered virtual environment, cinematic set design, physically based rendering, empty scene, no people"

// manjuPortraitFrontFace 正面人脸锚(2026-08-26 用户规则重申:人物角色提示词必须正面人脸,
// 禁侧面/半面视角):只加在主定妆照与正面全身视图;side 视图/角色板三视图(设计上含侧面)不加。
const manjuPortraitFrontFace = ", front-facing portrait, head facing the camera directly, symmetrical frontal face, both eyes evenly visible, no profile angle"

// manjuStyleIs3D 风格是否为次世代3D/BJD 人偶向(2026-08-26):token 含 3d/bjd/cg/unreal/
// blender/zbrush/virtual human/digital human 等。3D 向风格:定妆不替换写实措辞、不用插画风
// 人像锚,改用 manju3DPortraitAnchor + 正面人脸锚(用户反馈"选写实风却渲染成动漫形象"的根因
// 是旧锚把所有风格一刀切拉向 stylized illustration/painterly)。
func manjuStyleIs3D(style string) bool {
	for _, tok := range strings.Split(style, "+") {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "不要") {
			continue // 负面 token(如「不要3D游戏渲染」)不代表 3D 风格,2026-08-29
		}
		if strings.Contains(t, "3d") || strings.Contains(t, "bjd") || strings.Contains(t, "unreal") ||
			strings.Contains(t, "blender") || strings.Contains(t, "zbrush") ||
			strings.Contains(t, "virtual human") || strings.Contains(t, "digital human") ||
			strings.Contains(t, "virtual digital human") {
			return true
		}
		if t == "cg" || t == "cgi" {
			return true
		}
	}
	return false
}

// manjuStyleIsAnime 风格是否动漫/插画向(anime/2.5D/卡通/二次元/国漫/日漫/插画/漫画等
// token)→ 动漫书不做风格句写实化。注意「不要2D漫画线条」这类负面 token 以「不要」开头,
// 表示的是「不要动漫感」,必须跳过不判为动漫向(2026-08-29 写实化误判防线)。
func manjuStyleIsAnime(style string) bool {
	for _, tok := range strings.Split(style, "+") {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "不要") {
			continue
		}
		if strings.Contains(t, "anime") || strings.Contains(t, "2.5d") || strings.Contains(t, "2d") ||
			strings.Contains(t, "卡通") || strings.Contains(t, "二次元") || strings.Contains(t, "国漫") ||
			strings.Contains(t, "日漫") || strings.Contains(t, "插画") || strings.Contains(t, "漫画") ||
			strings.Contains(t, "painterly") || strings.Contains(t, "illustration") {
			return true
		}
	}
	return false
}

// manjuRealizeStyle 风格句写实化(2026-08-29 用户反馈「不是写实,怎么变成了卡通」):
// 分镜脚本风格句按立项 style 转写时常带「subtly anime-stylized semi-realistic characters」
// 等动漫化措辞(万怪之主 56 章 251 处),H3 出片角色即带 3D 动漫感;写实风格书在
// finalize 汇点统一把角色画风措辞替换为 photorealistic。幂等(替换后不含目标词);
// 动漫向书(manjuStyleIsAnime)与 3D 风格书(manjuStyleIs3D,用户规则「3D 就出 3D」)
// 不替换。场景词(如 low-poly game world 的题材设定)不碰。
func manjuRealizeStyle(hp string) string {
	if !strings.Contains(hp, "anime-stylized") && !strings.Contains(hp, "semi-realistic") {
		return hp
	}
	out := hp
	repl := [][2]string{
		// 2026-08-29 物种路由(审计 V6):替换词去 human——纯字符串替换不看镜内物种,
		// 物品独角镜/兽形镜也会被注入「photorealistic human characters」人形主体词
		{"subtly anime-stylized semi-realistic characters", "photorealistic cinematic characters and subjects with natural detailed textures"},
		{"anime-stylized semi-realistic characters", "photorealistic cinematic characters and subjects with natural detailed textures"},
		{"subtly anime-stylized", "photorealistic"},
		{"anime-stylized", "photorealistic"},
		{"semi-realistic", "realistic"},
	}
	for _, r := range repl {
		out = strings.ReplaceAll(out, r[0], r[1])
	}
	return out
}

// manjuSceneAnchor 场景图锚:场景无人脸,只需明亮清晰;不带人物锚(防误伤)。
const manjuSceneAnchor = "empty scene, no people"

// manjuStyleDesc 取风格措辞；预设外视为自定义风格，原样使用(用户直接输入英文风格描述)。
// manjuStyleHas 组合风格(以 + 分隔的 token)是否包含某预设元素,精确匹配避免子串误判。
func manjuStyleHas(style, key string) bool {
	for _, p := range strings.Split(style, "+") {
		if strings.TrimSpace(p) == key {
			return true
		}
	}
	return false
}

// manjuStyleStylized 风格组合是否含"风格化"元素(动漫/水墨/3D/手绘等非写实预设)。
// 2026-08-24 用户反馈:定妆照与视频角色画风割裂——组合风格(real+2.5d+ink)含 real 被一刀切
// 走 Z-Image 纯写实,而视频按 2.5D 动漫/水墨渲染,定妆照真人脸与视频动漫脸不一致。
// 修复:风格含这些风格化预设(即使组合里同时含 real)时,定妆照应走 SDXL checkpoint
// (Krea-2)渲染同风格;仅纯写实(real 或 real+自定义写实词)才走 Z-Image。
func manjuStyleStylized(style string) bool {
	for _, p := range strings.Split(style, "+") {
		switch strings.TrimSpace(p) {
		case "2.5d", "anime", "ink", "3d", "handdrawn", "papercraft", "clay":
			return true
		}
	}
	return false
}

// ---- 风格词净化(2026-08-25 用户反馈:角色/场景/视频与小说不符——污染根因之一) ----
//
// config.style 是 GUI 组合风格输入框的自由文本(以 + 分隔)。历史配置常把「非美术风格」的
// 规则性文字混进来(如 反派磕碜/Q版呆萌可爱小角色(用于对应角色的动态内心独白)/玄幻修仙/
// 真情实意/正能量…),这些中文指令被原样拼进英文 image_prompt 与 H3 提示词后:
//  - "Q版呆萌可爱小角色" → 把角色/场景往 Q 版萌物拉(实测阿铁全息屏被画成"呆"字)
//  - "玄幻修仙"         → 赛博朋克剧被拉向修仙古风
//  - 其余中文指令       → 对图像/视频模型是噪音 token,稀释有效语义
// 本净化器在 manjuStyleDesc 组装措辞前对每个自定义段执行:
//  1) 剥离括号及内容(括号里常是使用说明);
//  2) 黑名单包含匹配(角色塑造/内容价值观/运镜指令/跨书残留词) → 丢弃;
//  3) 中文白名单词 → 转英文措辞(图像模型对英文更可靠);未命中白名单的中文 → 丢弃;
//  4) 英文/数字自定义词 → 原样保留(用户自定义英文风格描述合法)。
// 被丢弃的词记入 manjuStyleLastDropped,由 plan 阶段日志提示用户,防静默。

// manjuStyleDropWords 非美术风格黑名单(子串匹配):角色塑造/内容价值观/运镜指令/跨书残留。
var manjuStyleDropWords = []string{
	"反派磕碜", "正角帅气或美丽", "配角谄媚", "真情实意", "正能量", "场景唯美",
	"电影级运镜", "电影动漫写实风格", "二次元动漫写实风", "电影感", "运镜",
	"系统绑定", "男主慵懒", "美女如云", "高燃爽文", "冷幽默", "逆袭", "打脸",
	"东方神话", "东方修仙", "东方玄幻", "玄幻修仙", "仙侠玄幻", "修真",
	"拟漫化", "禁日漫", "禁真人", "写实拟动漫", "Q版呆萌", "呆萌可爱",
	"内心独白", "角色", "主角", "配角", "反派", "正角",
}

// manjuStyleZhEn 中文美术风格白名单 → 英文措辞(仅这些中文词被接受并翻译,其余中文丢弃)。
var manjuStyleZhEn = map[string]string{
	"写实": "realistic", "动漫": "stylized anime", "水墨": "Chinese ink wash painting",
	"赛博朋克": "cyberpunk", "蒸汽朋克": "steampunk", "古风": "ancient Chinese aesthetic",
	"国潮": "Chinese retro wave", "科幻": "sci-fi", "奇幻": "fantasy",
	"热血": "passionate tone", "治愈": "healing cozy tone", "悬疑": "suspenseful tone",
	"都市": "urban modern", "Q版": "chibi cute style", "卡通": "cartoon",
	"像素": "pixel art", "油画": "oil painting", "水彩": "watercolor",
	"厚涂": "thick impasto", "平涂": "flat cel shading", "日系": "Japanese soft style",
	"玄幻": "xianxia fantasy", "仙侠": "xianxia immortal fantasy", "武侠": "wuxia martial arts",
	"西方": "western style", "暗黑": "dark moody", "胶片": "film grain aesthetic",
	"赛璐璐": "cel animation", "明亮": "bright airy", "低饱和": "low saturation", "高饱和": "high saturation",
}

// manjuStyleLastDropped 最近一次风格解析被净化的自定义词(供 plan 阶段日志提示)。
var manjuStyleLastDropped []string

// manjuSanitizeStyleToken 净化单个自定义风格词,返回(保留的英文措辞, 是否保留)。
func manjuSanitizeStyleToken(tok string) (string, bool) {
	// 1) 剥离括号及内容(括号里常是使用说明)
	if i := strings.IndexAny(tok, "（("); i >= 0 {
		tok = strings.TrimSpace(tok[:i])
	}
	if tok == "" {
		return "", false
	}
	// 2) 黑名单包含匹配
	for _, drop := range manjuStyleDropWords {
		if strings.Contains(tok, drop) {
			return "", false
		}
	}
	// 3) 中文白名单 → 英文措辞
	if en, ok := manjuStyleZhEn[tok]; ok {
		return en, true
	}
	// 4) 纯英文/数字自定义词保留(用户自定义英文风格描述)
	if isASCIIAlpha(tok) {
		return tok, true
	}
	return "", false
}

// isASCIIAlpha 是否全部为 ASCII 字母/数字/空格/常见分隔(英文风格词判定)。
func isASCIIAlpha(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// manjuStyleDesc 取风格措辞。
// 单个预设 key 直接查表;组合(预设+预设 / 预设+自定义词,以 + 分隔)逐段解析:
// 预设段取其 asset 核心措辞、自定义段经 manjuSanitizeStyleToken 净化后拼入;
// 净化丢弃的词记入 manjuStyleLastDropped(plan 阶段日志提示);全部被净化时退回空措辞。
func manjuStyleDesc(style string) manjuStyleSpec {
	if s, ok := manjuStyles[style]; ok {
		return s
	}
	manjuStyleLastDropped = nil
	parts := strings.Split(style, "+")
	if len(parts) > 1 {
		core := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if s, ok := manjuStyles[p]; ok {
				core = append(core, s.asset)
				continue
			}
			if keep, ok := manjuSanitizeStyleToken(p); ok {
				core = append(core, keep)
			} else {
				manjuStyleLastDropped = append(manjuStyleLastDropped, p)
			}
		}
		if len(core) > 0 {
			joined := strings.Join(core, ", ")
			return manjuStyleSpec{
				asset:   joined,
				opening: "The target video is in a " + joined + " style, cinematic realistic lighting",
				shot1:   "[Shot 1] " + joined + " style, cinematic realistic lighting",
			}
		}
		// 全部被净化:无有效风格措辞,回退默认写实措辞(比污染画面好)
		manjuStyleLastDropped = append(manjuStyleLastDropped, style)
		return manjuStyles["real"]
	}
	// 单个自定义 token(未用 + 组合)
	if keep, ok := manjuSanitizeStyleToken(style); ok {
		return manjuStyleSpec{asset: keep, opening: "The target video is in a " + keep + " style, cinematic realistic lighting", shot1: "[Shot 1] " + keep + " style, cinematic realistic lighting"}
	}
	manjuStyleLastDropped = append(manjuStyleLastDropped, style)
	return manjuStyles["real"]
}

// manjuAssetStyle 返回给图片模型(定妆照/场景图)的风格措辞，与逐镜 H3 风格保持一致。
// manjuStyleDescQuiet 展示用解析:不留净化痕迹(2026-09-06 误报实锤:UI 画风
// 查询污染全局 manjuStyleLastDropped,plan 日志错报「已从 image_prompt 剔除」
// 17 项画风词,实际卡内全英文从未剥离)。
func manjuStyleDescQuiet(style string) manjuStyleSpec {
	defer func() { manjuStyleLastDropped = nil }()
	return manjuStyleDesc(style)
}

func manjuAssetStyle(style string) string {
	return manjuStyleDesc(style).asset
}

// manjuNovelAssets 完整解析小说目录素材(数据驱动,命名约定只改候选表):
//  渲染提示词总集.md(优先,统一单文件) → 风格/负面/角色/场景/H3母版全量
//  素材/人物生成提示词.md(含 角色/人物 的 md) → CharPrompt  角色定妆照提示词
//  素材/场景*.md(含 场景 的 md)              → ScenePrompt 场景图提示词
//  素材/其它 md                              → ExtraPrompt 道具/氛围等素材
//  设定集/*.md(全部合并)                     → Setting     世界观/大纲/创作规范
//  封面/封面提示词.md                        → CoverPrompt 封面提示词(参考)
// 各段截断 8000 字,总注入受调用方总量控制。
type manjuNovelAssets struct {
	CharPrompt  string
	ScenePrompt string
	ExtraPrompt string
	Setting     string
	CoverPrompt string
	StylePrompt string // 渲染风格提示词(总集一节,注入每镜 detailed_description 前缀)
	NegPrompt   string // 全局负面提示词(总集二节,转 H3 正面约束 / 本地生图直接使用)
	Files       []string // 发现的素材文件清单(日志展示)
}

func scanNovelAssets(root string) manjuNovelAssets {	var out manjuNovelAssets
	if root == "" {
		return out
	}
	readTrunc := func(p string) string {
		if !fileExists(p) {
			return ""
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		txt := string(b)
		if r := []rune(txt); len(r) > 8000 {
			txt = string(r[:8000])
		}
		return txt
	}
	add := func(name, content string) {
		if content != "" {
			out.Files = append(out.Files, name)
		}
	}
	// 渲染提示词总集.md:统一单文件,优先解析(存在则角色/场景/风格/负面都从这里取,
	// 不再依赖分散的 人物生成提示词/场景提示词;分散文件仍兼容回退)。
	if c := readTrunc(filepath.Join(root, "素材", "渲染提示词总集.md")); c != "" {
		if a := parseRenderPromptMaster(c); a != nil {
			out.StylePrompt = a.StylePrompt
			out.NegPrompt = a.NegPrompt
			out.CharPrompt = a.CharPrompt
			out.ScenePrompt = a.ScenePrompt
			out.ExtraPrompt = a.ExtraPrompt
			out.Files = append(out.Files, "素材/渲染提示词总集.md(统一单文件)")
		}
	}
	// 素材/ 目录:按文件名语义归类(总集未覆盖的段/或兼容旧项目)
	if entries, err := os.ReadDir(filepath.Join(root, "素材")); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			low := strings.ToLower(e.Name())
			if strings.Contains(low, "渲染提示词总集") {
				continue // 已解析
			}
			content := readTrunc(filepath.Join(root, "素材", e.Name()))
			switch {
			case strings.Contains(low, "人物") || strings.Contains(low, "角色"):
				if out.CharPrompt == "" {
					out.CharPrompt = content
				}
				add(e.Name(), content)
			case strings.Contains(low, "场景") || strings.Contains(low, "背景"):
				if out.ScenePrompt == "" {
					out.ScenePrompt = content
				}
				add(e.Name(), content)
			default:
				if out.ExtraPrompt == "" {
					out.ExtraPrompt = content
				}
				add(e.Name(), content)
			}
		}
	}
	// 设定集/ 目录:全部合并(世界观/大纲/创作规范)
	if entries, err := os.ReadDir(filepath.Join(root, "设定集")); err == nil {
		var parts []string
		total := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			c := readTrunc(filepath.Join(root, "设定集", e.Name()))
			if c == "" {
				continue
			}
			parts = append(parts, "【"+e.Name()+"】\n"+c)
			total += len([]rune(c))
			out.Files = append(out.Files, "设定集/"+e.Name())
			if total > 20000 { // 设定集总截断
				break
			}
		}
		if len(parts) > 0 {
			out.Setting = strings.Join(parts, "\n\n")
		}
	}
	// 封面提示词(参考,用于项目封面一致性)
	if c := readTrunc(filepath.Join(root, "封面", "封面提示词.md")); c != "" {
		out.CoverPrompt = c
		out.Files = append(out.Files, "封面/封面提示词.md")
	}
	return out
}

// parseRenderPromptMaster 解析「渲染提示词总集.md」统一单文件(用户规则:提示词集中单文件,渲染管线直接识别):
//  一、渲染风格提示词(代码块全文)           → StylePrompt(注入每镜 detailed_description 前缀)
//  二、全局负面提示词(代码块全文)           → NegPrompt(生图直接使用;H3 转正面约束由调用方处理)
//  三、角色提示词(### 3.N 标题 + 代码块)    → CharPrompt(角色定妆照参考)
//  四、场景提示词(表格 场景|提示词)         → ScenePrompt(场景图参考)
//  五、H3 Ref2VA 六段式母版(代码块)        → ExtraPrompt(附到素材供 H3 提示词生成参考)
// 返回 nil 表示文件无有效节(调用方回退分散文件)。
func parseRenderPromptMaster(content string) *manjuNovelAssets {
	a := &manjuNovelAssets{}
	code := func(sec string) string { // 提取节内第一个 ``` 代码块
		idx := strings.Index(sec, "```")
		if idx < 0 {
			return ""
		}
		rest := sec[idx+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(rest[:end])
	}
	// 按 ## N、标题 切节(兼容中文数字「一、二、三…」与阿拉伯数字「1. 2. 3.」两种编号——
	// 技能阶段6 生成的 渲染提示词总集.md 用阿拉伯数字节标题,2026-08-24 实测修复)
	sections := map[string]string{}
	lines := strings.Split(content, "\n")
	cur := ""
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			cur = ln
			sections[cur] = ""
		} else if cur != "" {
			sections[cur] += ln + "\n"
		}
	}
	// 节编号归一:阿拉伯数字 → 中文数字(1→一,2→二,3→三,4→四,5→五,6→六)
	cnOf := func(n string) string {
		switch strings.TrimSpace(n) {
		case "1":
			return "一"
		case "2":
			return "二"
		case "3":
			return "三"
		case "4":
			return "四"
		case "5":
			return "五"
		case "6":
			return "六"
		}
		return ""
	}
	// 中文数字必须是显式集合(Unicode 码点不连续:'四' U+56DB > '六' U+516D,区间判断会漏)
	isCN := func(r rune) bool {
		return strings.ContainsRune("一二三四五六七八九十", r)
	}
	var styleSec, negSec, charSec, sceneSec, masterSec string
	for name, body := range sections {
		title := strings.TrimSpace(strings.TrimPrefix(name, "##"))
		// 提取标题开头的数字编号(中文或阿拉伯),只取编号本身用于匹配
		no := ""
		for _, r := range title {
			if (r >= '0' && r <= '9') || isCN(r) {
				no += string(r)
			} else {
				break
			}
		}
		if no == "" {
			continue
		}
		if len(no) == 1 && no >= "0" && no <= "9" {
			no = cnOf(no)
		}
		switch {
		case strings.Contains(no, "一") && strings.Contains(title, "风格"):
			styleSec = body
		case strings.Contains(no, "二") && (strings.Contains(title, "负面") || strings.Contains(title, "负向")):
			negSec = body
		case strings.Contains(no, "三") && strings.Contains(title, "角色"):
			charSec = body
		case strings.Contains(no, "四") && strings.Contains(title, "场景"):
			sceneSec = body
		case strings.Contains(no, "五") && strings.Contains(title, "H3"):
			masterSec = body
		}
	}
	if styleSec == "" && charSec == "" && sceneSec == "" {
		return nil // 无有效节
	}
	a.StylePrompt = code(styleSec)
	a.NegPrompt = code(negSec)
	// 三、角色:整节作为 CharPrompt(### 角色名 + 代码块提示词原样,LLM 从中提炼)
	if charSec != "" {
		a.CharPrompt = strings.TrimSpace(charSec)
	}
	// 四、场景:表格 场景|提示词 → 原样注入(LLM 读表格)
	if sceneSec != "" {
		a.ScenePrompt = strings.TrimSpace(sceneSec)
	}
	// 五、H3 母版:附到 ExtraPrompt(供逐镜 H3 提示词生成参考)
	if masterSec != "" {
		a.ExtraPrompt = strings.TrimSpace(masterSec)
	}
	return a
}

// manjuDurationRule 时长/语音预算/拆镜密度规则段(2026-08-26):min_shot_seconds/max_shot_seconds
// 与 chars_per_sec 从渲染配置读取注入系统提示词——此前硬编码「4-15s/4 字每秒」,立项.render
// 规划的时长区间是死配置从未生效。direct/script 两个系统提示词共用。
func manjuDurationRule(cfg map[string]any) string {
	R, _ := cfg["render"].(map[string]any)
	lo, hi := 4, 12
	if n, ok := manjuToInt(R["min_shot_seconds"]); ok && n > 0 {
		lo = n
	}
	if n, ok := manjuToInt(R["max_shot_seconds"]); ok && n > 0 {
		hi = n
	}
	cps := 4.0
	if f, ok := manjuToFloat(R["chars_per_sec"]); ok && f >= 2 && f <= 8 {
		cps = f
	}
	return fmt.Sprintf("【拆镜密度·内容完整优先·强制】(2026-08-30 用户规则:分镜可以多,保证小说内容完整表达)按正文/脚本逐节拍完整拆镜:约每 80-130 字一镜,覆盖全部情节/对白/动作/情绪/细节,禁止为控制镜数合并节拍或挑关键点压缩剧情——**内容表达完整优先,宁多镜不压缩,拆镜数不设上限**;一个节拍放不下就拆两镜,长对白拆多句、多动作拆多镜,保证每镜单一清晰画面;每镜登场角色 ≤3(H3 参考图上限,超员必须拆镜),每句对白 ≤20 字(超长对白拆成多句对话或旁白承接)。\n【时长硬约束】duration 由台词/动作量决定:中文语音约 %.0f 字/秒(20 字≈5 秒;60 字≈12 秒),台词+旁白总字数 ÷ %.0f 不得超过时长(%d-%d 秒);超预算必须加时长或拆镜,旁白同速折算计入。台词被截断=废镜。", cps, cps, lo, hi)
}

func manjuDirectSystem(cfg map[string]any, style string) string {
	kbChar, kbScene, kbStory := manjuKnowledgeChunks(cfg)
	assetStyle := manjuAssetStyle(style)
	// 2026-08-26:min/max_shot_seconds 与 chars_per_sec 从渲染配置注入提示词——此前硬编码
	// 4-15s/4 字每秒,立项.render 规划的时长区间从未生效(死配置激活的一部分)
	durationRule := manjuDurationRule(cfg)
	s := `你是 MiniMax H3 视频生成模型的导演兼提示词专家。基于给定的小说章节，直接输出完整漫剧渲染方案。

【内容纪律·最高优先】：分镜必须忠实还原小说原文，画面与小说对不上=废镜：
- 台词/旁白必须逐字引用小说原文（原词原句原标点，禁止改写/扩写/翻译/编造）
- 关键剧情事件（冲突/反转/打脸/名场面）必须有对应镜头，禁止跳事件、禁止张冠李戴
- 角色外观/服装/道具逐字从原文提炼，禁止自行增删设定
- 原文没有的台词和事件一律不许出现

【输出 JSON（严格）】：
{
  "episode_title": "集标题",
  "characters": [{"id": "角色名", "role": "正角|反派|功能配角（按剧情阵营判定:主角/女主/正派灵宠/重要正派助攻=正角;主要反派=反派;次要反派/下属/炮灰/龙套=功能配角。【Q版纪律·2026-09-03 用户规则再升级】q_form 提示词全员必填不得为空;渲染端 Q 版资产全员生成(含配角/群演)。内心戏画面路由不变:正角内心用 Q 版形象,反派/功能配角内心=写实正脸+画外音）", "gender": "男/女", "species": "人|妖兽|灵宠|神兽|精怪|鬼物|机械|物品（种族:人=人类角色;妖兽/灵宠/神兽/精怪/鬼物=非人形兽类/生灵,image_prompt 画的是兽形本体(毛色/种族特征)而非人形,严禁把兽类当人画;物品=有意识的器物/植物/法宝/灵植(如会说话的剑/盆栽精灵/古镜),image_prompt 画的是物品/植物本体(材质/形制/纹理/标志性细节),严禁画人形/人脸/人衣;角色是兽类或物品必须标对应非人类,不得标精怪/机械 兜底）", "age": "年龄段", "appearance": "完整外观（发型/脸型/五官/气质，逐字从原文提炼，具体到可渲染）", "costume": "完整服装描述", "color_palette": "角色配色板(5色HEX,如 #1E2A33 #31414D #53606D #D8D1C6 #A89B8B,顺序=主色/辅色/点缀色/肤色/发色;角色板与所有视图严格用它统一配色)", "expressions": "表情神态(4-6个该角色关键情绪,中文逗号分隔,如 清冷/沉思/轻笑/审视/惊疑)", "board_prompt": "给图片模型的英文文生图提示词:角色板/角色资料卡整合图（【角色板结构·强制,2026-08-25 即梦角色版教程】①三视图整合:正面/侧面/背面全身并排;②细节特写模块:脸部/发型/服装纹样/配饰各一小格;③服饰分层展示:外袍/内衬/腰带,纹样刺绣细节;④表情神态 4-6 格(用 expressions);⑤配色板:5 色 HEX 色块一行标注;⑥人设文字:身份/气质/视觉标志一行中文。整图竖版网格排版、浅色底、清晰分区、所有分格同一人同一服装;拟漫化半写实东方风格,禁日漫禁真人;` + assetStyle + ` 风格;外观/服装逐字引用 appearance/costume,配色严格用 color_palette）", "image_prompt": "给图片模型的英文文生图提示词（【按物种分档·2026-08-29 审计V3/V5根治】人类:full body, head to toe, 自然 7 头身正常比例, 禁止大头小身/半身/头像/portrait;兽类(species≠人):画兽形本体(毛色/种族特征),不套人形立绘措辞;物品(species=物品):画物品/植物本体 the item/plant itself(材质/形制/纹理/标志性细节),不写 full body/7头身/服装/人脸,严禁任何人类形象与叶子拼脸;` + assetStyle + ` 风格;【禁违禁物品·2026-09-01 用户规则】人物角色禁止携带烟、酒等违规物品——image_prompt/q_form 禁止 cigarette/smoking/cigar/alcohol/beer/wine/liquor/烟/酒 等词(渲染端负面词+正向剥离双保险,素材侧写干净);【拟动漫硬规则·2026-08-24 用户规则】人类角色:semi-realistic stylized illustration of an East Asian/Chinese character, 禁日漫(not a Japanese anime/manga style, avoid japanese-style facial features, japanese anime eyes), 禁真人(not a photorealistic photo of a real person, avoid resembling any real person)——写实风格同样按拟动漫渲染,不输出真人照片;兽类/物品角色:不用 East Asian/Chinese character 措辞,改用 semi-realistic stylized rendering of the creature/item itself;含完整外观/服装/性别强化仅适用于人类）", "voice": "音色+念白性格(可选,2026-09-03 配音去AI味升级):年龄段+性别+音色质感+**这个角色怎么说话**——念白习惯/标志性语气/情绪习惯(示例:中年男声,低沉不紧不慢,说话像掂量每个字/少年男声,话赶话往外蹦,憨气十足/老年女声,絮叨里全是心疼,句尾像哄睡——禁纯静态描述如「清亮稳定」「沉稳男声」,静态词=H3 平板播报腔;每角色念白性格互异),或热门方言(东北/陕西/四川/河南/广西/湖南/粤语/台普,人设契合才配,主角/正派默认普通话;渲染端 dialectVoiceFor 优先使用,同剧角色音色互异)", "views": {"front": "英文文生图提示词：正面全身立绘（full body 正面, 头到脚完整, 脸部五官清晰占画面合理比例, ` + assetStyle + ` 风格）", "full": "英文文生图提示词：全身立绘（完整头到脚，正面站姿，自然 7 头身比例，完整服装/鞋履/体态，` + assetStyle + ` 风格）", "side": "英文文生图提示词：侧面全身（侧身 90 度完整头到脚，发型/脸型/服装侧面轮廓清晰，自然比例，` + assetStyle + ` 风格）", "detail": "英文文生图提示词：细节特写（该角色最有辨识度的 1 个细节：饰品/花纹/发饰/疤痕等，大特写构图，` + assetStyle + ` 风格）", "q": "英文文生图提示词：Q版形象（【Q版纪律·2026-09-03 用户规则再升级】①q_form 全员必填不得为空,渲染端全员生成 Q 版资产;②Q版面容随角色本人——脸型/眼型/发型/年龄感/胡须按该角色具体面容,禁止统一做成通用宝宝脸(老人要有老相、有胡须者保留胡须、少年保持少年相;【年龄措辞分物种·2026-08-30】人形老年写 aged face with wrinkles/眼角纹/白发;兽形老年写口鼻眼周毛色渐灰(greying muzzle)的老兽相,严禁给兽写人类皱纹;物品无年龄语义不写年龄词);③species≠人 的妖兽/灵宠/神兽:Q版=该妖兽本体的萌化小兽形(保留种族/毛色/特征),禁止画成人形或人类宝宝;④人类按性别与年龄:女性一律无胡须、年轻男性(少年/青年/男孩)一律无胡须,仅成年/老年男性可有胡须且 Q版必须保留;⑤Q版渲染以该角色正面定妆照为参考图生成(同一人,禁止形象大变)。chibi cute style, 短手短脚, 保留该角色标志特征[发型/瞳色/服饰/印记/胡须], 呆萌可爱表情, 内心独白/心理活动渲染用 Q 版形象表现, ` + assetStyle + ` 风格）"}}],
  "scenes": [{"id": "场景名（取自原文;只收剧情实际发生的具体场景——素材里的全局配置段(通用负向词/统一风格前缀/质量后缀/色锚系统等)是作画排除词不是场景,禁止列为 scenes）", "description": "空间结构/材质/光线/氛围", "image_prompt": "给图片模型的英文文生图提示词（空场景无人物，明亮清晰，` + assetStyle + ` 风格）"}],
  "shots": [
    {
      "shot_id": 1,
      "scene": "场景id",
      "characters": ["角色id(该镜实际登场的全部角色:说话人+同时出镜者,缺一不可;未列出的角色一律不得入画)"],
      "shot_size": "特写/近景/中景/全景/远景",
      "camera": "运镜（类型+幅度+速度，如：缓慢推近）",
      "action": "画面动作描述",
      "style": "该镜渲染风格(2026-08-23 多风格并用:可省略=继承全局风格;需要差异化时给,如写实对话镜=real、奇幻特效镜=real+magical realism、回忆/梦境镜=ink+watercolor、赛博镜=cyberpunk;可 + 组合多个元素,总元素≤4;风格需贴合该镜情绪/内容)",
      "dialogue": "角色:台词（逐字引用小说原文对白，禁止改写/扩写/编造；多句用换行分隔；无对白为空）。【说话人硬约束】\"角色:\"前缀必须是本镜 characters 中实际开口的角色，谁说的就是谁，禁止张冠李戴；角色说的话一律放 dialogue，禁止混入旁白",
      "narration": "旁白（仅原文叙述性文字/画外音，逐字引用）。【硬约束】旁白禁止包含任何角色的台词——角色说的每句话必须放进 dialogue 并标注对应角色；原文中\"XXX说\"的对白必须标为该角色 dialogue；无旁白则空；有台词时旁白留空避免重复。【内心独白标记·强制】原文角色内心/心理活动(心想/暗道/嘀咕/盘算等)写 内心·角色名:原文内心内容(渲染时画面用该角色 Q 版呆萌形象+画外音,2026-08-23 用户规则;【Q版纪律·2026-09-03 用户规则再升级】Q 版资产全员生成;画面路由:正角内心用 Q 版形象渲染,反派/功能配角/群演内心=该角色写实正脸图+画外音),与客观旁白区分",
      "duration": 5
    }
  ],
  "directing": {
    "time": "线性|倒叙|循环|平行切|单时刻切片",
    "pov": "全知|跟随单人|监控·仪器|物的视角|缺席",
    "tempo": "匀速|加速爆发|前紧后松|两次呼吸|全片凝滞",
    "audio": "BGM通铺|对白驱动|音效驱动|环境声|完全静音|声画错位",
    "ending": "空景收|回到首镜|硬切黑|悬而未决|日常化",
    "peak_device": "全片情绪最高点使用的手法(一句话,写手法不写题材,如'摘掉遮脸物+近乎全黑')",
    "climax_pattern": "高潮段镜头组织方式(如'6×特写连打'/'单镜长时凝滞')"
  }
}
{MANJU_DURATION_RULE}
【节奏模型·强制】(2026-08-24 知识库「官方风格技能与漫剧优化」节奏模型整合)每镜内部必须有多拍节奏,禁止一镜一个动作平铺直叙:5 秒镜=3-4 个 beat(建立→动作→收尾);10 秒镜=5-7 个 beat 且含 1-2 个峰值+1-2 个刹车(静止/空拍);15 秒镜=6-9 个 beat 且含 2-3 个峰值+安静刹车。节奏意图词:setup(建立)/establish(定位)/prepare(蓄势)/impact(冲击)/brake(刹车)/settle(落定)——每镜 action 按 beat 组织,峰值镜前必有蓄势镜,高潮后必接刹车,禁止高潮镜直接切下一镜无缓冲
【近景补偿·强制】(2026-08-24 知识库「H3长镜连续与工作室实战」人脸 token 数学整合)H3 VisualVAE 32× 空间下采样,中景人脸仅约 2 token、眼睛约 0.28 token——拉近景比加大画幅更有效。情感戏/对白戏/表情戏(哭/怒/恐惧/心动/内心挣扎)一律强制近景或特写(shot_size=近景/特写,机位对准面部),禁止用中景/全景拍情绪;中景起步 ≥1024×576;脸部特写是该角色情绪演出的主要载体,表演层细节(情绪三层拆解/五维微表情/哭戏梯度)写在特写镜里
【防同质化变量表·强制(每集必填)】directing 五维必须逐项选择并**贯彻到分镜**(镜头时长分布/景别/收尾镜/声音设计对应取值):禁止默认组合「线性+全知+匀速+BGM通铺+空景收」(历史最高频重复)。tempo 取值对照时长分布:匀速=各镜等长;加速爆发=逐段加快末段最密;前紧后松=开头密逐渐拉开;全片凝滞=全部取上限时长。ending 取值对照末 1-3 镜:空景收=拉大远景空镜;回到首镜=末镜与首镜同机位同景别;硬切黑=高潮中途切黑;悬而未决=停在一个动作中间;日常化=回落到极普通日常场景。peak_device 写手法本身(摘面具/脱帽/亮武器),不要写题材(防化服/武侠)。
【分镜纪律·强制】:
- shots[].characters 必须列全该镜实际在场的全部角色(说话人+同时出镜者,缺一不可);未列出的角色(长老/弟子/路人/群众)一律不得入画,如需氛围只允许无面部细节的远景虚化
- 【说话人纪律·强制】谁说的就是谁:原文对白按说话角色逐句标入对应 dialogue(前缀"角色:"),严禁把某角色说的话标成他人台词或塞进旁白;旁白只承载原文叙述,绝不含角色话语
- 有台词的说话人必须是该镜的视觉中心主体(景别/机位优先对准说话人),其他登场角色不得遮挡或抢占画面中心
- 同一角色在整集所有镜头中形象必须完全一致(外观/服装逐字复用其 characters 卡,禁止同角色换装/换写法)
- 【位置锚定·强制】(2026-08-30 ver14,官方 Reference Anchors)shots[].action 中每个登场角色首次出现处写**屏幕位置**(画面左/中/右+前景/中景/背景)+**朝向**(面对镜头/朝左/朝右);同一场景地标(门/窗/桌/柜台)写屏幕相对位置跨镜沿用;非首镜开头人物位置与上一镜收尾一致,需要换位写明确换位动作过渡,禁止无过渡的左右翻转
- 【跨镜衔接·强制】(2026-08-30 ver14,H3 MotionContext 接缝契约)每镜 action 开头承接上一镜收尾(人物位置/姿势/情绪,气闸 1-2 秒保持上镜收尾构图再发展本镜内容——与 pinned 帧矛盾的排布会被渲染成 union 多出人脸);承接段写微小可见动作(呼吸/重心转移/视线变化),禁止静止 hold(渲染成字面冻结);接缝镜动作节拍按早 0.92s 预算(pin 头 22 帧)
- 【内心戏·强制】(2026-08-30 ver14)narration 中「内心·角色名:」前缀的内心独白:该角色为正角时画面=其 Q 版形象(逐镜 h3_prompt 里 Q 版单独定义 Subject 并引用 Q 版参考图),Q 版动作必须**具象演绎内心内容语义**(数数/盘算→掰指头,思考→托腮歪头,惊讶→瞪眼捂嘴,担忧→抱膝揪衣角),禁止动作与内心内容语义脱节;内心内容走 off-screen voiceover 画外音;反派/功能配角内心=写实正脸图+画外音
【面容独特性纪律·强制(防跨剧撞脸)】:
- 每个角色的 image_prompt 必须给出**独一无二的面容锚点组合**:从 眼型(丹凤眼/桃花眼/狭长眼/圆眼)、眉型(剑眉/柳叶眉/浓眉/细眉)、鼻型(高挺/小巧/鹰钩)、唇形(薄唇/丰唇/唇珠)、脸型(瓜子/方圆/棱角/鹅蛋)、肤色(苍白/小麦/古铜)、气质 中选至少 4 个具体特征,并给 1 个独有印记(痣/疤/耳饰/发色挑染等);**禁止** generic 泛化词(sharp jawline/clear eyes/handsome/young man 单独出现都算,必须搭配具体特征)
- 同剧多角色面容必须**互不相同**(五官/发型/气质可辨认区分);不同剧的相同职位角色(如各剧主角)也必须是不同面容,禁止模板化雷同
- appearance 字段同步给出这些独特特征的中文描述(供提示词外观锁定引用)
【种族与胡须纪律·强制】(2026-08-25 用户规则):
- 角色是妖兽/灵宠/神兽/精怪等非人形种族时:species 必须填非人;image_prompt/views 一律画兽形本体(毛色/种族特征),严禁画成人形或人头兽身;必须是该兽独自一兽的独照,严禁出现任何人类/主人/弟子/骑肩/趴怀等人物互动描述(人与兽同框属镜头渲染层,角色资产图里写了人=渲染必出人,2026-08-28 小貔实证);该角色 Q 版=萌化小兽形,禁止人类宝宝
- 女性角色一律无胡须/胡子(appearance/image_prompt/views/Q版全部禁止出现胡须描写)
- 年轻男性(少年/青少年/青年/男孩/孩童)一律无胡须;胡须仅限成年/老年男性角色
- 有胡须的成年/老年男性:appearance 必须写明胡须形态(如花白络腮胡/山羊胡),image_prompt 与 Q 版提示词必须保留胡须
- Q 版形象面容必须跟随角色本人(老人显老/有胡须保留/少年清俊),禁止全部做成统一萌娃脸
【判停清单·模型做不到的六类,换写法不要重抽】(整合 ai-film-skills dialogue-drama 判停清单实测):
- 机械开合(舱盖掀开/抽屉拉出/门开): 拆成「关着的空镜 硬切 开着的空镜」,不写开合过程
- 群体连锁反应(一排人依次回头): 改单人反应 + 画外声补足"一片骚动"
- 走位(走进来/走出画/走到某处): 直接画「已到位」的构图——模型做不出位移,但**原地姿态变化**(起立/坐下/转身/弯腰)可以,别过度保守
- 画内文字: 只允许大号阿拉伯数字(中文/小字必糊),否则画面里干脆不要文字载体(招牌/菜单/路牌/书封)
- 多人互动(A 给 B 戴上某物/交接): 拆成单人镜,用视线缝起来
- 手部纹理级特征(掌纹/指节细节): 改成姿势级特征(握/指/摊开)
【题材真实度判据】(整合 ai-film-skills prompt-craft 实测): 戏剧强度越高越假——火山喷发/冰川崩塌/闪电劈荒原/巨兽正面亮相是 AI 过拟合区,一出必带 AI 味;普通瞬间才真。高危画面优先改拍「痕迹/后果」(如地面炸裂+上方压下的阴影,不正面拍本体);题材选型先问:这个世界里的东西,模型见过真的吗?
【表演层·强制】(2026-08-24 知识库「H3提示词优化5层结构方法论」+「AIGC人物微表情设计指南」整合,专治蜡像脸)情绪镜禁止写情绪形容词,必须按三层物理细节拆解:①外部动作(转身/握拳/低头/咬唇)②生理反应(瞳孔收缩/喉结滚动/下眼睑微红/鼻翼轻颤)③量化指标(眉心上聚2mm/单侧嘴角下沉0.5°/振幅<1mm)。表情=眉眼/嘴角/肌肉/呼吸/光影五维组合;哭戏按四梯度写(强忍泪水=泪锁睫毛边缘不落→无声落泪→抽泣=肩胸起伏→崩溃大哭);非对称(只让半边脸动)+克制中断(动作启动后在第10°突然减速停止)去 AI 感;每条约束写成可见终态("第8秒时她仍是长黑发蓝开衫圆框眼镜"),不写"保持一致"`
	if kbChar != "" {
		s += "\n\n【知识库角色模板参考（仅作设定参考，贴合本剧）】\n" + kbChar
	}
	s = strings.Replace(s, "{MANJU_DURATION_RULE}", durationRule, 1)
	if kbScene != "" {
		s += "\n\n【知识库场景模板参考（仅作设定参考，贴合本剧）】\n" + kbScene
	}
	if kbStory != "" {
		s += "\n\n【知识库分镜/运镜模板参考】\n" + kbStory
	}
	return s
}

// manjuScriptSystem 视频脚本直出系统提示词(与小说解析并存输入模式):
// 输入是一段符合 MiniMax H3 官方规范(VIDEO_PROMPT_WRITING_GUIDE)的视频渲染脚本 md
// (分镜脚本:镜头/时间码/画面/台词/音效/环境声/配乐),LLM 一步直出完整渲染方案
// (characters/scenes/shots/directing,shots 每镜直接带 h3_prompt——genShotPrompts
// 自动跳过已有提示词的镜,即「脚本 md → 完整方案」零中间步骤)。
// 官方格式规定(2026-08 本地副本:manju-production 技能 references/官方提示词_官方指南原文/
// base-en.txt + ref-en.txt)逐条内嵌:对齐指令/三核心字段/[Shot N] 时间码/运镜三要素/
// 说话者 (Sx)/<d>…</d> 中文台词/画外音措辞/<scenetrans>/可见文字双引号。
func manjuScriptSystem(cfg map[string]any, style string) string {
	assetStyle := manjuAssetStyle(style)
	s := `你是 MiniMax H3 视频生成模型的导演兼提示词专家。基于给定的【视频渲染脚本 md】(符合 H3 官方分镜规范),直接输出完整漫剧渲染方案——一步直出,每个镜头同时给出完整 H3 提示词(h3_prompt)。

【输入说明】视频渲染脚本 md 是镜头级的分镜脚本,可能包含:镜头编号与时间码([Shot N] At MM:SS.mmm)、景别/机位/运镜、画面动作、台词(标注说话人)、音效、环境声(overall_soundscape)、配乐提示(non_diegetic_music)。脚本可能是官方三段式/六段式 prompt 形态,也可能是结构化分镜表形态——两种都要吸收为方案。

【输出 JSON（严格）】:
{
  "episode_title": "集标题",
  "characters": [{"id": "角色名", "role": "正角|反派|功能配角（按剧情阵营判定:主角/女主/正派灵宠/重要正派助攻=正角;主要反派=反派;次要反派/下属/炮灰/龙套=功能配角。【Q版纪律·2026-09-03 用户规则再升级】q_form 提示词全员必填不得为空;渲染端 Q 版资产全员生成(含配角/群演)。内心戏画面路由不变:正角内心用 Q 版形象,反派/功能配角内心=写实正脸+画外音）", "gender": "男/女", "species": "人|妖兽|灵宠|神兽|精怪|鬼物|机械|物品（种族:人=人类角色;妖兽/灵宠/神兽/精怪/鬼物=非人形兽类/生灵,image_prompt 画的是兽形本体(毛色/种族特征)而非人形,严禁把兽类当人画;物品=有意识的器物/植物/法宝/灵植(如会说话的剑/盆栽精灵/古镜),image_prompt 画的是物品/植物本体(材质/形制/纹理/标志性细节),严禁画人形/人脸/人衣;角色是兽类或物品必须标对应非人类,不得标精怪/机械 兜底）", "age": "年龄段", "appearance": "完整外观（发型/脸型/五官/气质，从脚本提取并补全，具体到可渲染）", "costume": "完整服装描述", "color_palette": "角色配色板(5色HEX,如 #1E2A33 #31414D #53606D #D8D1C6 #A89B8B,顺序=主色/辅色/点缀色/肤色/发色;角色板与所有视图严格用它统一配色)", "expressions": "表情神态(4-6个该角色关键情绪,中文逗号分隔,如 清冷/沉思/轻笑/审视/惊疑)", "board_prompt": "给图片模型的英文文生图提示词:角色板/角色资料卡整合图（【角色板结构·强制,2026-08-25 即梦角色版教程】①三视图整合:正面/侧面/背面全身并排;②细节特写模块:脸部/发型/服装纹样/配饰各一小格;③服饰分层展示:外袍/内衬/腰带,纹样刺绣细节;④表情神态 4-6 格(用 expressions);⑤配色板:5 色 HEX 色块一行标注;⑥人设文字:身份/气质/视觉标志一行中文。整图竖版网格排版、浅色底、清晰分区、所有分格同一人同一服装;拟漫化半写实东方风格,禁日漫禁真人;` + assetStyle + ` 风格;外观/服装逐字引用 appearance/costume,配色严格用 color_palette）", "image_prompt": "给图片模型的英文文生图提示词（【按物种分档·2026-08-29 审计V3/V5根治】人类:full body, head to toe, 自然 7 头身正常比例, 禁止大头小身/半身/头像/portrait;兽类(species≠人):画兽形本体(毛色/种族特征),不套人形立绘措辞;物品(species=物品):画物品/植物本体 the item/plant itself(材质/形制/纹理/标志性细节),不写 full body/7头身/服装/人脸,严禁任何人类形象与叶子拼脸;` + assetStyle + ` 风格;【拟动漫硬规则·2026-08-24 用户规则】人类角色:semi-realistic stylized illustration of an East Asian/Chinese character, 禁日漫(not a Japanese anime/manga style, avoid japanese-style facial features, japanese anime eyes), 禁真人(not a photorealistic photo of a real person, avoid resembling any real person)——写实风格同样按拟动漫渲染,不输出真人照片;兽类/物品角色:不用 East Asian/Chinese character 措辞,改用 semi-realistic stylized rendering of the creature/item itself;含完整外观/服装/性别强化仅适用于人类）", "voice": "音色+念白性格(可选,2026-09-03 配音去AI味升级):年龄段+性别+音色质感+**这个角色怎么说话**——念白习惯/标志性语气/情绪习惯(示例:中年男声,低沉不紧不慢,说话像掂量每个字/少年男声,话赶话往外蹦,憨气十足/老年女声,絮叨里全是心疼,句尾像哄睡——禁纯静态描述如「清亮稳定」「沉稳男声」,静态词=H3 平板播报腔;每角色念白性格互异),或热门方言(东北/陕西/四川/河南/广西/湖南/粤语/台普,人设契合才配,主角/正派默认普通话;渲染端 dialectVoiceFor 优先使用,同剧角色音色互异)", "views": {"front": "英文文生图提示词：正面全身立绘（full body 正面, 头到脚完整, 脸部五官清晰占画面合理比例, ` + assetStyle + ` 风格）", "full": "英文文生图提示词：全身立绘（完整头到脚，正面站姿，自然 7 头身比例，完整服装/鞋履/体态，` + assetStyle + ` 风格）", "side": "英文文生图提示词：侧面全身（侧身 90 度完整头到脚，发型/脸型/服装侧面轮廓清晰，自然比例，` + assetStyle + ` 风格）", "detail": "英文文生图提示词：细节特写（该角色最有辨识度的 1 个细节：饰品/花纹/发饰/疤痕等，大特写构图，` + assetStyle + ` 风格）", "q": "英文文生图提示词：Q版形象（【Q版纪律·2026-09-03 用户规则再升级】①q_form 全员必填不得为空,渲染端全员生成 Q 版资产;②Q版面容随角色本人——脸型/眼型/发型/年龄感/胡须按该角色具体面容,禁止统一做成通用宝宝脸(老人要有老相、有胡须者保留胡须、少年保持少年相;【年龄措辞分物种·2026-08-30】人形老年写 aged face with wrinkles/眼角纹/白发;兽形老年写口鼻眼周毛色渐灰(greying muzzle)的老兽相,严禁给兽写人类皱纹;物品无年龄语义不写年龄词);③species≠人 的妖兽/灵宠/神兽:Q版=该妖兽本体的萌化小兽形(保留种族/毛色/特征),禁止画成人形或人类宝宝;④人类按性别与年龄:女性一律无胡须、年轻男性(少年/青年/男孩)一律无胡须,仅成年/老年男性可有胡须且 Q版必须保留;⑤Q版渲染以该角色正面定妆照为参考图生成(同一人,禁止形象大变)。chibi cute style, 短手短脚, 保留该角色标志特征[发型/瞳色/服饰/印记/胡须], 呆萌可爱表情, 内心独白/心理活动渲染用 Q 版形象表现, ` + assetStyle + ` 风格）"}}],
  "scenes": [{"id": "场景名（取自脚本;只收剧情实际发生的具体场景——素材里的全局配置段(通用负向词/统一风格前缀/质量后缀/色锚系统等)是作画排除词不是场景,禁止列为 scenes）", "description": "空间结构/材质/光线/氛围", "image_prompt": "给图片模型的英文文生图提示词（空场景无人物，明亮清晰，` + assetStyle + ` 风格）"}],
  "shots": [
    {
      "shot_id": 1,
      "scene": "场景id",
      "characters": ["角色id(该镜实际登场的全部角色:说话人+同时出镜者,缺一不可;未列出的角色一律不得入画)"],
      "shot_size": "特写/近景/中景/全景/远景",
      "camera": "运镜（类型+幅度+速度，如：缓慢推近）",
      "action": "画面动作描述",
      "style": "该镜渲染风格(2026-08-23 多风格并用:可省略=继承全局风格;需要差异化时给,如写实对话镜=real、奇幻特效镜=real+magical realism、回忆/梦境镜=ink+watercolor;可 + 组合多个元素,总元素≤4;风格贴合该镜情绪/内容)",
      "dialogue": "角色:台词（脚本台词逐字引用，禁止改写/扩写/编造；多句用换行分隔；无对白为空）。【说话人硬约束】"角色:"前缀必须是本镜 characters 中实际开口的角色，谁说的就是谁",
      "narration": "旁白（画外音，脚本原文；无则空；有台词时旁白留空避免重复）。【内心独白标记·强制】脚本原文角色内心/心理活动写 内心·角色名:内容(渲染时画面用该角色 Q 版呆萌形象+画外音,2026-08-23 用户规则;【Q版纪律·2026-09-03 用户规则再升级】Q 版资产全员生成;画面路由:正角内心用 Q 版形象渲染,反派/功能配角/群演内心=该角色写实正脸图+画外音),与客观旁白区分",
      "duration": 5,
      "h3_prompt": "该镜完整 H3 提示词（英文主体、中文台词/旁白原文；见下方官方格式硬规定）"
    }
  ],
  "directing": {
    "time": "线性|倒叙|循环|平行切|单时刻切片",
    "pov": "全知|跟随单人|监控·仪器|物的视角|缺席",
    "tempo": "匀速|加速爆发|前紧后松|两次呼吸|全片凝滞",
    "audio": "BGM通铺|对白驱动|音效驱动|环境声|完全静音|声画错位",
    "ending": "空景收|回到首镜|硬切黑|悬而未决|日常化",
    "peak_device": "全片情绪最高点使用的手法(一句话,写手法不写题材)",
    "climax_pattern": "高潮段镜头组织方式"
  }
}

【脚本→方案映射规则·强制】:
- 脚本的每个镜头/段落 → 一个 shots 条目;脚本标注的时间码/时长优先,未标注时按台词量折算(语音预算与时长区间见下方【时长硬约束】)
- 脚本出现的角色/场景 → characters/scenes 卡片,外观/服装从脚本逐字提取,脚本未给的细节按上下文补全(补全项不得与脚本矛盾)
- 脚本的台词逐字进 dialogue(标注说话人),旁白进 narration;脚本里已有的 H3 提示词片段直接吸收进 h3_prompt,缺失部分按下方官方格式补全
- 脚本与小说不同:没有"原文事件必须逐字"约束——脚本本身就是镜头指令,直接执行;但台词仍逐字保留

【H3 官方格式硬规定·h3_prompt 必须遵守】(官方 VIDEO_PROMPT_WRITING_GUIDE base-en/ref-en):
- 有角色的镜用 Ref2VA 六段式(subject_definitions/summary/retention_analysis/detailed_description/overall_soundscape/non_diegetic_music),无角色的空镜用 FL2VA 三段式(首行对齐指令 + integrated_multimodal_description/overall_soundscape/non_diegetic_music)——模板见下方
- 【时码 clip-local·强制】(2026-08-30 官方源码核验:H3 官方要求切点时码"falls within the video duration",且每镜独立编码渲染、时轴从 0 起)h3_prompt 的时码一律用【本镜内时间轴】:首段 [Shot 1] 无时间戳;后续切点 [Shot N] At MM:SS.mmm 从 0 严格递增且必须小于本镜 duration——严禁写全片累计时间(如前镜共 15 秒时本镜写 At 00:15.000 是越界错误,应写本镜内实际切点如 At 00:04.000);多切点长镜直接引用输入 take_shots[].cut_at(已是镜内时轴)
- 运镜三要素(类型+幅度+速度)写成句内自然英语(Push In/Pull Out/Pan/Truck/Tilt/Pedestal/Arc/Tracking/Static/POV/Roll/Shake;with small/large amplitude;at slow/fast speed)
- 说话者稳定 ID (S1)(S2),首次出现给身份描述,发声者写 <Subject N> (Sx);【镜内编号·强制】(2026-08-30 官方源码核验:每镜是独立 clip 独立编码,模型看不到其它镜头——跨镜全局编号是悬空引用)说话者 ID 在【本镜内】按实际发声顺序从 S1 连续分配:第一个开口的是 S1、第二个是 S2,严禁跳号、严禁沿用全片序号(如本镜只有两人对话却出现 S6);台词写 <d>[Chinese] 台词中文原文</d>——【官方语言标签·强制】(2026-08-30 官方 base-en §4.4 核验:d 标签内必须带语言标签,官方写法 <d>[English] ...</d>;中文台词即写 <d>[Chinese]陈默？</d>,标签词用英文 Chinese——2026-08-28 的 <d>[中文]…</d> 被逐字念出事故根因是标签用了中文词而非标签不该存在,裸 <d>中文</d> 无标签会让模型猜配音语言,两者都禁止);画外音写 says in an off-screen voiceover ... while his/her lips remain completely closed
- 【画外音/旁白措辞·硬禁中文】(2026-08-23 实测:直出的 h3_prompt 用中文「画外音/旁白/嘴唇完全闭合」H3 无法识别对白驱动→该镜静音 rms≈0.005;d 标签内同样禁止写中文占位说明字样——2026-08-28 实测 <d>[中文]…</d> 中文标签词被逐字抄进台词,2026-08-30 官方核验正解=标签用英文 Chinese 而非剥掉标签):detailed_description 里画外音/旁白一律用英文指令句——旁白写 The narrator (Sx) says in an off-screen voiceover: <d>[Chinese] …</d>(d 标签内=[Chinese] 标签+分镜 narration 的中文原文)while the on-screen characters' lips remain completely closed;画外音台词写 (Sx) says in an off-screen voiceover: <d>[Chinese] …</d>(d 标签内=[Chinese] 标签+台词中文原文)while his/her lips remain completely closed;禁止出现中文「画外音」「旁白」「嘴唇闭合」字样
- 台词跨切点写 <scenetrans>,被结尾截断写 <cutoff>
- 画面可见文字(招牌/字幕/霓虹)用英文双引号原文
- overall_soundscape 1-4 句英文连续段落(环境/动作/非语言人声,不重复台词);non_diegetic_music 1-3 句(乐器+速度+节奏+动态,禁抽象情绪词,无配乐写 N/A);【动作拆小·2026-09-01 知识库五步导演法】复杂动作拆 3-6 个连续可观察子动作,大幅移动+说话+复杂运镜+场景变化同镜=难度爆炸须拆镜或改画外;【单一运镜】每镜一种主要镜头运动,禁堆叠冲突运动词;【特效锚定·2026-09-01 知识库仙侠打戏工作流】特效必须附着载体(武器/肢体/地面破坏点),写清载体→路径→落点,禁凭空漂浮;【场景服务动作】战斗镜场景写功能要素(可蹬踏柱/可借力壁/破坏承接点);【地面材质】战斗/近景镜写地面材质细节(裂纹/碎石/切割痕/烧蚀)
- H3 为 CFG-distilled 无负面词:负面概念一律转正面排除句写进散文,禁堆叠负面词

{MANJU_DURATION_RULE}
【分镜纪律·强制】:
- shots[].characters 必须列全该镜实际在场角色(说话人+同时出镜者);未列出角色不得入画,如需氛围只允许无面部细节的远景虚化
- 谁说的就是谁:脚本台词按说话角色逐句标入对应 dialogue,严禁张冠李戴或塞进旁白
- 有台词的说话人必须是该镜视觉中心主体,其他角色不得遮挡抢中心
- 同一角色整集所有镜头形象完全一致(外观/服装逐字复用其 characters 卡,禁止换装/换写法)
【面容独特性纪律·强制(防跨剧撞脸)】:
- 每个角色 image_prompt 给出独一无二面容锚点组合(眼型/眉型/鼻型/唇形/脸型/肤色/气质 至少 4 个具体特征 + 1 个独有印记);禁止 generic 泛化词;同剧多角色面容互不相同
【种族与胡须纪律·强制】(2026-08-25 用户规则):
- 角色是妖兽/灵宠/神兽/精怪等非人形种族时:species 必须填非人;image_prompt/views 一律画兽形本体(毛色/种族特征),严禁画成人形或人头兽身;必须是该兽独自一兽的独照,严禁出现任何人类/主人/弟子/骑肩/趴怀等人物互动描述(人与兽同框属镜头渲染层,角色资产图里写了人=渲染必出人,2026-08-28 小貔实证);该角色 Q 版=萌化小兽形,禁止人类宝宝
- 女性角色一律无胡须/胡子(appearance/image_prompt/views/Q版全部禁止出现胡须描写)
- 年轻男性(少年/青少年/青年/男孩/孩童)一律无胡须;胡须仅限成年/老年男性角色
- 有胡须的成年/老年男性:appearance 必须写明胡须形态(如花白络腮胡/山羊胡),image_prompt 与 Q 版提示词必须保留胡须
- Q 版形象面容必须跟随角色本人(老人显老/有胡须保留/少年清俊),禁止全部做成统一萌娃脸
【判停清单·模型做不到的六类,换写法不要重抽】:
- 机械开合→「关着的空镜 硬切 开着的空镜」;群体连锁反应→单人反应+画外声;走位→直接画「已到位」(原地姿态变化可以);画内文字→只留大号阿拉伯数字;多人互动→拆单人镜视线缝合;手部纹理级→姿势级
【题材真实度判据】戏剧强度越高越假(火山/冰川/闪电/巨兽正面是 AI 过拟合区);高危画面优先改拍「痕迹/后果」
【节奏模型·强制】(2026-08-24 知识库「官方风格技能与漫剧优化」整合)每镜内部多拍节奏:5 秒镜=3-4 beat、10 秒镜=5-7 beat(含 1-2 峰值+1-2 刹车)、15 秒镜=6-9 beat(含 2-3 峰值+安静刹车);节奏意图词 setup/establish/prepare/impact/brake/settle,峰值镜前必有蓄势,高潮后必接刹车
【近景补偿·强制】(2026-08-24 知识库「H3长镜连续与工作室实战」整合)情感戏/对白戏/表情戏一律近景或特写(shot_size=近景/特写,机位对准面部),禁止中景/全景拍情绪;脸部特写是情绪演出主要载体
【防同质化变量表·强制】directing 五维必须逐项选择并贯彻到分镜,禁止默认组合「线性+全知+匀速+BGM通铺+空景收」;peak_device 写手法不写题材
【人物比例/物品清单/动作流畅·强制】(2026-08-23 用户反馈大头/乱入/不流畅)角色一律 full body 全身 7 头身,禁止大头小身/portrait 头像;detailed_description 每物品写数量/位置/关系,不需要的物品写排除句,同镜物品≤3;每镜单一主导动作+小幅+慢速,走位写「已到位」,连续动作拆镜
【日本人物形象·禁止·强制】(2026-08-23 用户规则)无论风格(含 anime/2.5d)人物一律中式/东方面孔,写 East Asian/Chinese facial features,正面排除 avoid japanese-style facial features, japanese anime eyes;anime 风格词保留,日本式脸型禁止
【内心戏 Q 版化·强制·仅正角】(2026-08-23 用户规则 + 2026-08-24 限定:Q 版仅限正角)narration 若为角色内心独白(前缀 内心·角色名),该镜 detailed_description 画面主体=该角色 Q 版呆萌形象(引用其 Q 版参考图);【Q版纪律·2026-08-24 用户规则】role=反派/功能配角 或无 Q 版参考图的角色内心戏**禁止用 Q 版**,画面主体=该角色写实正脸图;画外音一律 The narrator says in an off-screen voiceover 念内心;非内心旁白保持原画面
【动物禁人脸·强制】(2026-08-23 用户规则)动物/萌宠/妖兽保持动物形态(物种特征),写 animal form + species-specific features + no human face;禁止人脸/人形化(拟人化角色除外)
【表演层·强制】(2026-08-24 知识库整合,专治蜡像脸)情绪镜禁止情绪形容词,按三层物理细节拆解:①外部动作(转身/握拳/低头/咬唇)②生理反应(瞳孔收缩/喉结滚动/下眼睑微红/鼻翼轻颤)③量化指标(眉心上聚2mm/单侧嘴角下沉0.5°/振幅<1mm)。表情=眉眼/嘴角/肌肉/呼吸/光影五维组合;哭戏四梯度(强忍→无声→抽泣→崩溃);非对称+克制中断去 AI 感;约束写可见终态不写"保持一致"`
	// 拼接官方六段式/三段式模板与逐镜写作规范,保证直出的 h3_prompt 格式与逐镜生成完全一致
	opening := manjuStyleDesc(style).opening
	if opening == "" {
		opening = manjuStyleDesc("real").opening
	}
	s += "\n\n" + strings.ReplaceAll(manjuRef2vaTpl, "{style}", opening) + manjuShotWritingRules
	s += "\n\n" + strings.ReplaceAll(manjuFl2vaTpl, "{style}", manjuStyleShot1(style))
	s = strings.Replace(s, "{MANJU_DURATION_RULE}", manjuDurationRule(cfg), 1)
	return s
}

// manjuModeTag 方案输入模式日志标记(脚本直出/小说解析)
func manjuModeTag(scriptMode bool) string {
	if scriptMode {
		return "(脚本直出,逐镜 H3 提示词一步到位)"
	}
	return "(小说解析)"
}

// ---- 逐镜 H3 提示词直出(六段式 Ref2VA / 三段式 FL2VA) ----

func manjuStyleShot1(style string) string {
	return manjuStyleDesc(style).shot1
}

const manjuRef2vaTpl = `【Ref2VA 六段式(有角色,锁人物),严格此顺序】:
subject_definitions:
<Subject 1> is the character in <Picture 1> and <Picture 2> ... with [完整外观：逐字引用角色卡 appearance（发型/眼睛/疤痕/气质/道具等全部特征逐项覆盖，禁止省略/概括/编造）；服装 costume 全字段；【性别强化·仅人类】(species=人)女=feminine facial structure, soft delicate features, long hair（禁男性化），男=masculine jawline, strong brow, broad shoulders（禁女性化）；【物种分档·2026-08-29 审计修复】species=物品 的角色(器物/植物/法宝/灵植)主体写 the item/plant itself + 本体特征(材质/形制/纹理/标志性细节),严禁写人脸/发型/服装/人形身体;兽类主体写兽形本体特征]
[同一角色多视图:该角色有几个参考图就引用几张——<Subject 1> is the character in <Picture 1> (正面/正脸特写), <Picture 2> (全身/侧面/细节), ...;每张视图对应一个 <Picture N> 标签,顺序与 ref_available 该角色的视图顺序一致,全部引用后统一写 with [外观...]]
[多角色镜:每个登场角色一行 <Subject N> is the character in <Picture A> and <Picture B> ...,与参考图顺序一致(角色在前场景在后);画面里谁先出现谁 Subject 号靠前]
[群像镜纪律·强制(2026-08-27 用户反馈:分镜 4 两名牢卒未定义 Subject,模型自由发挥时从参考图复制了白发管事的形象,画面出现重复人物):动作/画面中出现的每一个人物——包括无名群演(牢卒/士兵/侍卫/侍女/随从/路人/仆役)都必须 subject_definitions 逐一定义:有参考图引用 <Picture N>,无参考图写 <Subject N> is [群演身份] with 独立外观描述(年龄/体型/服装颜色,不引用任何 Picture);严禁省略群演、严禁把多人写成复数笼统词(如 two jailers 必须拆成 <Subject N> 与 <Subject N+1> 两个独立个体);每个 Subject 是独立个体,严禁复用/复制其他 Subject 或参考图人物的外观与脸]
[参考图纪律·强制:ref_available 是「角色+视图」的平铺清单,顺序就是参考图传入顺序;<Picture 1..N> 严格对应清单第 1..N 项(同一角色多视图占多个 Picture 编号),Subject 编号与角色一一对应(Subject 1=清单第 1 个角色,依次),禁止调换/跳过/合并视图;清单外的登场角色(本镜参考图不足)写 <Subject N> is [角色名] with 外观描述(不引用任何 Picture),并保持与参考角色不串脸。【官方机制·2026-08-30 源码核验:H3 文本编码器把参考图按挂载顺序自动标为 Picture 1/2/3…列在提示词之前——<Picture N> 是对第 N 张挂载图的指认,编号错位=模型拿错图(场景图当人脸/别人脸当本角色),subject_definitions 编号必须与 ref_available 清单逐一对齐,这是硬性输入契约不是修辞偏好】]
[外观锁定·强制:每个角色的外观只允许出现角色卡 appearance+costume 里的特征,且逐项覆盖(发型/眼睛/疤痕/服装/道具缺一不可);禁止 generic 泛化词(ordinary/plain/sturdy/average/young man 等),禁止编造角色卡没有的特征(白发/换装/错误年龄);多角色镜严禁把其他角色的特征写进本角色(谁的特征写谁)]
[拟漫化硬规则·2026-08-24 用户强制,2026-08-29 物种分档:人类角色均为 semi-realistic stylized illustration of an East Asian/Chinese character——参考图已拟漫化,subject_definitions 必须延续此画风;兽类/物品角色不套 East Asian/Chinese character 措辞——兽类=semi-realistic stylized rendering of the creature itself,物品=semi-realistic stylized rendering of the item/plant itself,严禁给兽/物品写人脸/人形/人衣;禁止写 photorealistic/realistic photo/real human(真人脸=侵权);禁止写 japanese anime/manga style, japanese-style facial features, japanese anime eyes(禁日漫);角色外形以参考图为准逐字保留,画风恒定拟漫]
[场景编号·强制:场景的 Picture 编号 = 全部角色视图总数 + 1(如 2 角色各 2 视图 → 场景在 <Picture 5>);Subject 编号 = 角色数 + 1]
<Subject N+1> is the [场景名] environment in <Picture M>(M=角色视图总数+1), with [空间结构/材质/光线客观描述，引用场景卡]
[关键道具：<Subject M> is the [道具名] in <Picture M>, with 外观描述；说明与角色互动]

summary:
[reference generation] 本镜任务概述（1-2 句英文，说明目标视频与参考主体关系；任务前缀用官方固定值——参考生成为 reference generation，本管线恒用此值；只引用已定义标签，禁在 summary 引入新标签）

retention_analysis:
<Subject 1> (appears in [Shot 1]): fully_preserved - 面部/发型/服装与 <Picture 1> 完全一致(兽类=毛色/体型/种族特征一致;物品=本体形态/材质/标志性细节一致)
[多角色镜:每个角色一行 retention_analysis,全部 fully_preserved]
<Subject N+1> (appears in [Shot 1]): fully_preserved - 场景布局/光线/背景与 <Picture M>(场景编号,同 subject_definitions) 一致
[道具行同理]（标记只用官方固定四值：fully_preserved / partially_preserved / attribute_transfer / weak_reference；【官方规范】retention_analysis 内禁写 (Sx) 说话者 ID）

detailed_description:
{style}。[实体锁定句：The face, hairstyle, costume of <Subject 1> must remain exactly as in <Picture 1> throughout the shot; the scene layout of <Subject N+1> must match its reference.; 多角色镜加 Each character must keep their own identity from their own reference picture, never swap or blend identities.]
[拟漫化锚句·强制(人类角色):All characters appear as semi-realistic stylized illustrations, East Asian/Chinese facial features, not photorealistic photos, not Japanese anime style — keep the stylized look consistent with the reference character.;本镜含兽类/物品角色时改用:The creatures/items appear as semi-realistic stylized renderings of themselves, no human face, no human form, consistent with their reference pictures.]
[Shot 1] [官方建议 350-500 英文词(对话密集优先完整台词时间线):开场构图→主体外观位置→动作状态变化→运镜(类型+幅度+速度,句内自然英语)→光影→台词/旁白→收尾；<Subject N> 标签在主体首次出现处插入,后续镜复用同标签不重定义；情感戏/对话优先近景/中景；末尾散文排除项 no subtitles, no text overlays, no watermark；【亮度护栏·强制】Dark mood is fine for atmosphere, but the subject's face and body must remain clearly visible and well-lit at all times - use a clear light source on the subject (candlelight, moonlight, torch, window light); never render the frame nearly black]

overall_soundscape:
环境底噪/动作音效（1-4 句英文连续段落，禁重复台词）

non_diegetic_music:
纯器乐配乐（1-3 句：乐器+速度+节奏+动态，禁抽象情绪词；无配乐写 N/A）【BGM 定向文案·强制】(2026-08-24 知识库整合)按题材文化贴合选乐器:古风/仙侠/武侠→古筝、竹笛、琵琶、箫;热血/战斗→鼓组+弦乐;都市/现代→钢琴、合成器;悬疑/惊悚→低音提琴拨弦、钟琴;治愈/温馨→木琴、竖琴;对白下 ducking 压到对白之下`

const manjuFl2vaTpl = `【FL2VA 三段式（空镜/转场，无主角），严格此顺序】：
第一行对齐指令（两位小数）：How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video.（尾帧锚定时补 Picture 2 (from Shot N) aligns with the S.SS-second mark，S.SS=镜头时长两位小数）
空一行后：
integrated_multimodal_description:
{style} + 画面延续首帧（首帧锚定→动作展开→收尾）+ 动作/运镜/光影 + 台词/旁白 <d>…</d>（d 标签内=中文原文,H3 原生配音;时长严格=镜头秒数）+【亮度护栏】Subjects must remain clearly visible and adequately lit - keep readable exposure with visible faces and actions; avoid rendering the frame nearly black

overall_soundscape:
non_diegetic_music:【BGM 定向文案·强制】(2026-08-24 知识库「官方风格技能与漫剧优化」整合)配乐写乐器+速度+节奏+动态,并按题材文化贴合选乐器:古风/仙侠/武侠→古筝、竹笛、琵琶、箫;热血/战斗→鼓组+弦乐齐奏;都市/现代→钢琴、合成器、电吉他;悬疑/惊悚→低音提琴拨弦、钟琴、不安的脉冲;治愈/温馨→木琴、竖琴、轻快的拨弦;祭典/节庆→锣鼓、唢呐、民族打击乐。对白下配乐自动 ducking(压到对白之下),无配乐写 N/A,禁写"欢快/悲伤/激昂"等抽象情绪词——只写乐器与节奏(如 guzheng plucking at 90 BPM, sparse and delicate)`

const manjuShotWritingRules = `

【写作规范（官方强制，两种模式都遵守）】：
1. 【时码 clip-local·强制】(2026-08-30 官方核验:每镜独立渲染时轴从 0 起,切点必须落在本镜时长内)首段 [Shot 1] 无时间戳;后续切点 [Shot N] At MM:SS.mmm 在本镜内严格递增且 < duration;多切点长镜直接引用输入 take_shots[].cut_at(镜内时轴);严禁全片累计时间码
2. 说话者 (S1)(S2) 在【本镜内】按实际发声顺序从 S1 连续分配(2026-08-30 ver14:渲染端会按全集首次发声顺序把有音色绑定角色重写为全局 (Sx) 跨镜稳定——LLM 侧按镜内序写即可,机械层自动对齐;同一角色同一镜内必须同一 ID,禁止镜内换号);【音色身份短语·强制】说话者首次出现必须写身份描述——年龄段+性别+音高/音色质感+语速(官方示例 "the middle-aged baker with a calm, slightly raspy voice (S1)"、ref-en 示例 "the same clear youthful voice",如 a young man with a clear steady voice / an elderly woman with a warm crackly voice / a little girl with a high bright voice),同一角色跨镜复用同一短语(in the same ... voice),禁止逐镜换措辞——这是声线前后漂移的根源;发声者写 <Subject N> (Sx)；【复合说话者】多人齐声/合唱写复合 ID 如 (S1,S2)（官方规范）；从不发声的角色不分配 ID；retention_analysis 中禁写 (Sx)
3. 台词写 <d>[Chinese] 中文原文</d>（【官方语言标签·2026-08-30 官方 base-en §4.4 核验】d 标签内=语言标签+原话:标签词必须用英文 Chinese(历史 <d>[中文]…</d> 被逐字念出=标签语种用错,不是标签不该存在);原词原标点，句末以 。？！结束，不译不改写,H3 原生对白配音;裸 <d>中文</d> 无标签禁止——模型会猜错配音语言;听不清的片段写 [unclear] 不许猜写）。【说话人硬约束】分镜 dialogue 的每句台词必须由标注的对应角色开口说出：写该角色 <Subject N> (Sx) says: <d>[Chinese] …</d>——谁说的就是谁，禁止把台词安到别的角色头上、禁止把角色台词改写成旁白/画外音；同一句台词在整条提示词中只能出现一次，禁止重复贴原文（重复句会被 H3 念两遍，2026-08-30 五问整改）
4. 【旁白 = H3 原生画外音，不是 TTS，更不是角色台词】：只有分镜 narration 字段的内容才写 The narrator (Sx) says in an off-screen voiceover: <d>[Chinese] …</d>(d 标签内=narration 中文原文+官方语言标签)while the on-screen characters' lips remain completely closed（旁白按镜内发声顺序计入 (Sx)；旁白与台词不同时出现；【硬约束】分镜 dialogue 里的角色台词禁止写成旁白——必须由对应角色开口，画面中该角色嘴唇在动）。【禁止前缀/重复·强制】(2026-08-30 五问整改：内心配音重复根治) d 标签内只写台词/旁白/内心独白的中文原文，禁止带「内心·角色名:」「旁白:」前缀、禁止用中文引号包裹原文（引号会被逐字念出）；同一句台词/旁白/内心独白在整条提示词中只能出现一次——禁止把 narration 既写成 off-screen voiceover 句又在别处重复贴原文（重复句会被 H3 念两遍）；旁白与内心同镜并存时各写一条 off-screen 句、内容互斥不重复
5. 画外音台词也写 says in an off-screen voiceover ... while his/her lips remain completely closed
6. 【跨镜台词连续性（官方标签）】同一句台词跨越镜头切点时，两段接续处各写 <scenetrans> 并声明音频跨切点连续（continues seamlessly across the cut / carries over from the previous shot）；台词被视频结尾截断写 <cutoff>
7. 运镜三要素（类型+幅度+速度）写成句内自然英语（Push In/Pull Out/Pan Left/Pan Right/Truck/Tilt/Pedestal/Arc/Tracking/Static/POV/Roll/Shake；幅度 with small/large amplitude、速度 at slow/fast speed，中等默认省略——官方词表）。【景别↔运镜匹配·强制】(2026-08-30 五问整改：运镜垃圾) 运镜必须与景别匹配：特写/近景→小幅推近/缓摇/固定（情绪聚焦）；中景→推/拉/横移/跟移（对话与动作皆宜）；全景→横移/跟移/升降（交代空间）；远景/大远景→升降/缓推/航拍（建立场景）；对峙/对话镜→固定机位+缓慢微摇（手持感），禁止纯静止无微动的固定——固定机位镜必须以画面内角色动作或环境动效（飘动的发丝/衣角/尘埃/光斑）持续撑住每一帧；运镜方向与主体运动方向一致（跟移追主体、推近主体动作），禁止方向相悖的运镜
8. 排除项/可见文字用英文双引号原文；H3 为 CFG-distilled 无负面词，禁堆叠负面词。
   输入中的 negative_prompt(用户负面提示词)必须逐概念转译为正面排除句,合并写入 detailed_description 末尾散文(如 bad hands→no distorted hands;text→no text overlays, no watermark;flickering→no flickering frames);语义与既有排除项重复的合并,不重复罗列
9. 画面禁情绪化形容词（只写机位/光影/动作/构图），音效只写现场声，BGM 只写乐器与节奏（禁抽象情绪词，无配乐写 N/A——官方规范）
10. 输入中的 known_issues 是本项目历史高频审片问题:针对每个问题在 detailed_description 写一句正面规避描述(如 面部扭曲→face and hands anatomy must be natural and well-formed;近黑帧→主体带明确光源;水印文字→clean frame, no text overlays),不堆负面词不逐字罗列
11. 【角色纪律·强制】画面中只允许出现该镜 characters 列出的登场角色;未列出的角色(长老/弟子/路人/群众)一律不得入画——如需氛围只能以无面部细节的远景剪影/虚化背景出现,禁止特写/近景/中心构图
12. 【主角中心·强制】说话人/动作主角必须是该镜的视觉中心主体(居中/近景/构图优先),其他登场角色不得抢占画面中心或遮挡主角;主角形象与其参考图完全一致(face, hairstyle, costume)
13. 【跨镜外观锁定·强制】同一角色在本集所有镜头的 subject_definitions 外观描述必须逐字一致(以本集首镜写法为准,后续镜直接复用该写法,禁止每镜重新措辞);detailed_description 中对角色的外观/发型/服装描述也必须与该镜 subject_definitions 一致,禁止出现与 subject_definitions 矛盾的描述
14. 【导演备注·硬规则祈使句·强制】(整合 ai-film-skills 实测)详细描述末尾单独一段写编号祈使句硬规则(不是描述句),模型会遵守:
   1) 切点卡在动作进行中段,禁止动作做完再切;相邻镜头景别必须不同
   2) 场景/光线/服装全程不变,只变机位与姿势;同一镜头内保持单机位连续性
   3) 每个有台词镜头人物嘴唇运动必须与台词同步;无台词镜头 mouth firmly closed
   4) 人物手部/肢体必须有自然动作,禁止呆立定格(治愈留白镜除外:写"停住/保持这个眼神/谁都没动")
   5) 主体必须清晰受光,禁止整帧近黑(暗调氛围可以,脸和身体必须可见)
15. 【状态变化·画"变化之前"·强制】(整合 ai-film-skills dialogue-drama 3.1 实测)同一镜头内部要演出的状态变化(涟漪扩散/裂缝亮起/火焰点燃/印记留下/屏幕亮起),首帧关键帧一律描述「变化之前」的静止状态,变化过程交给动作描述;严禁把变化终点写成首帧(模型只能倒着演回起点)。例:要"石头裂开→橘光涌出",写"暗色石头完好无损 → 裂纹扩散 → 橘光涌出稳住",不要写"石头已裂开发光"
16. 【关系感·物理动作·强制】(整合 ai-film-skills shot-list-prompt ② 实测)角色之间的关系一律写成「距离 + 朝向镜头的物理动作」,禁止抽象关系描述(如"她的头靠在镜头的肩上""两人并肩走远"这类会失败);模型理解"镜头在哪、人朝镜头做什么",不理解"你和她的关系"
17. 【描述零数字·强制】(整合 ai-film-skills chain-consistency ⑤/pitfalls ⑫ 实测)角色/场景描述中禁止出现任何数字(年龄/身高/数量/年份)——会被模型画成画面文字(字幕位);一律改用形容词(如"二十五六岁"→"青年","五十上下"→"年长");需要尺寸写绝对量或占比,禁止"像一枚硬币那么大"类类比(喻体会被画成实物)
18. 【同机位状态镜·接戏规则】(整合 ai-film-skills dialogue-drama 3.1 配套)跨镜同机位状态变化(门开/抽屉/屏幕明灭)用硬切+状态对比描述;接戏靠下一镜动作接切,禁止用首尾同图(last_frame 锁死)钉住终点姿势——首尾同图会把动作整个锁死。【收尾动作·强制】(2026-08-30 五问整改:动作僵硬)每镜结尾必须落在明确的完成态动作上(落座/转身/收拳/抬头/垂目/握紧/停步),禁止以静止定格姿势收尾——H3 长镜运动衰减,静止收尾=成片末段冻结(实测 freeze_ratio 1.0)
19. 【缺席表达·否定句列全·强制】(整合 ai-film-skills prompt-craft 三-C 实测)凡是「某物不在场但留下痕迹」的镜头(空椅子凹陷/无人会议室/只有痕迹),模型必然把那个「某物」画出来——否定句必须逐项列全(「椅子上没有人,画面里没有任何人体、四肢或衣物」「房间空无一人,没有人影,没有身体,只有空椅子」),只写「没有人」无效;写缺席类镜头前自问:那句话里那个不该出现的东西,堵死了吗
20. 【构图指令·权重之争】(整合 ai-film-skills prompt-craft 六 实测)构图指令写了不执行,是权重之争不是措辞问题——指令句被几百字描述稀释,画面塌回默认构图(正面/居中/中景)。两条有效杠杆:①夹句:同一句核心指令在提示词**最前面和最后面各放一遍**,常量夹中间;②画幅裁切:把构图写成物理上塞不下别的东西(「上三分之一只有天,下三分之一只有地,他整个人装在中间那条带子里」),而不是形容它该长什么样。堆形容词/写长写狠无效;水平左右位置(人在画左/画右)提示词基本治不好,需要时改机位/景别设计,不要在措辞里绕
21. 【概念盲区判据·强制】(整合 ai-film-skills prompt-craft 三 实测)某镜头若重渲后错误方向与上次**一致**(不是随机差异),判定为模型概念盲区:禁止继续加词/堆否定,直接改主体或换镜头设计(例:洞螈→洞穴盲鱼;抽象"兽瞳"→具体物种;本体拍不了→拍痕迹/后果);同镜重抽上限后仍不对,改分镜比再抽划算
22. 【人物比例·强制】(2026-08-23 用户反馈大头)角色 image_prompt/detailed_description 人物一律「full body 全身、自然 7 头身比例、禁止大头小身/半身/portrait 头像」;视频人物比例由定妆参考图决定,参考图必须全身
23. 【画面物品清单·强制】(2026-08-23 用户反馈乱入物品)detailed_description 里每个出现的物品写明数量/位置/与主体的关系(「他手里握着缺角镜子,桌面没有其他物品」);禁止笼统场景描述让模型自由发挥补物品;不需要的物品写排除句(no other objects in frame / only XX on the table);同一镜物品数≤3,超过拆镜
24. 【动作流畅·幅度分级·强制】(2026-08-23 用户反馈人物镜头不流畅;2026-08-30 五问整改：动作僵硬) 每镜**单一主导动作**,动作幅度按情绪强度与景别分级,禁止一律「小幅+慢速」(旧规则一刀切小幅慢速=僵硬源头,与 EXECUTION DISCIPLINE 要求的大幅可感知动作互相矛盾):①爆发镜(冲突/追赶/崩溃/爆发情绪)→ 大幅快速动作,幅度 clearly visible/plainly perceptible,动作带惯性(起步-发力-收势);②常态镜(对话/行走/日常)→ 中幅自然动作,手势/转身/点头幅度自然可感,禁止微不可见;③情绪镜(内心戏/哀伤/对峙)→ 微表情三层物理拆解(规则29),肢体静止但呼吸/眼神/手指微动持续。走位:一步以内的小范围位移(转身/跨步/俯身/侧移)直接写,模型可渲染;跨屏长距离位移才写「已到位」+到达后姿态;连续动作拆成 2 镜或静态+微动;禁止一镜内多个不相干动作堆叠
25. 【日本人物形象·禁止·强制】(2026-08-23 用户规则:动漫渲染也禁止日本人物形象)无论渲染风格(含 anime/2.5d/动漫),所有人物一律**中式/东方面孔**——detailed_description 人物镜写 East Asian/Chinese facial features(自然眼型,非日漫大眼),正面排除句 avoid japanese-style facial features, japanese anime eyes, big sparkly anime eyes, sharp anime chin;禁止出现日本式脸型/日式动漫大眼/日本风格面容;anime/cartoon **风格词保留**(风格可动漫,脸必须中式东方)
26. 【内心戏 Q 版化·强制·仅正角】(2026-08-23 用户规则 + 2026-08-24 限定:Q 版仅限正角;2026-08-30 ver14 Q版动作演绎)narration 若为角色内心独白(前缀 内心·角色名,如「内心·阿拾:…」):①若该角色 role=正角(有 Q 版参考图),subject_definitions 为该 Q 版形象**单独定义一行 Subject**(<Subject N> is the chibi version of 角色名 in <Picture M>, with a big head, round face, short limbs, keeping the character's signature features 发型/瞳色/服饰),引用其 Q 版参考图 <Picture>(编号按参考图挂载清单);detailed_description 画面主体=该 Q 版形象,带 stylized Q-version proportions 风格签名句;【Q版动作演绎内心·强制】Q 版的动作/表情必须**具象演绎内心内容语义**——数数/盘算类→掰手指头,思考/疑惑类→托腮歪头,惊讶→瞪眼捂嘴,担忧→抱膝揪衣角,得意→叉腰晃头,禁止 Q 版动作与内心内容无关(如掰指头配"冷柜有动静"这类语义脱节);内心内容按逐秒表演指令铺满时长(节拍+微表情);画外音 The narrator (S1) says in an off-screen voiceover 念内心内容 while lips closed(2026-08-30 ver14:内心戏画外音由渲染端绑定该角色音色,与客观旁白区分);②若该角色 role=反派/功能配角(无 Q 版图),**禁止用 Q 版**——画面主体=该角色写实正脸图+画外音念内心(off-screen voiceover, lips closed);③角色无 role 字段时按有无 Q 版参考图判断:有则 Q 版,无则写实+画外音;非内心客观旁白保持原画面+画外音
27. 【人物微动漫写实·强制】(2026-08-23 用户规则:避免写实人物侵权)写实电影级渲染时,人物形象**微动漫化**——detailed_description 人物写 subtly anime-stylized semi-realistic character, stylized East Asian features(略带动漫风格化:适度圆润/线条化,避免与任何真人肖像高度相似);场景/光影/镜头保持写实电影级(人物微动漫,场景写实);Q 版内心形象不受此限(本就呆萌)
28. 【动物禁人脸·强制】(2026-08-23 用户规则:动物别乱入人脸)动物/萌宠/妖兽/兽类角色一律保持**动物形态**(物种特征:毛皮/鳞甲/兽瞳/喙/爪/尾/角),禁止人脸/人形化/拟人过头;detailed_description 动物镜写 animal form, species-specific features(如 round ink-black blob spirit with golden bead eyes),并明确 no human face;定妆 image_prompt 动物角色加 animal form 约束;穿衣服的拟人化角色(设定明确)除外
29. 【表演层·情绪三层拆解·强制】(2026-08-24 知识库「H3提示词优化5层结构方法论」整合:专治蜡像脸/假表情)H3 把抽象情绪形容词当低质量指令——禁止直接写"她很伤心/愤怒/害怕"(模型只会出呆滞假脸),必须把情绪翻译成**三层物理细节**:①外部动作(可观察:转身/握拳/低头/咬唇);②生理反应(不可控真相:瞳孔收缩/喉结滚动/下眼睑微红/鼻翼轻颤);③量化指标(振幅<1mm/时长1.5s/眉心上聚2mm/单侧嘴角下沉0.5°)。detailed_description 人物镜按此三层写表演,禁止情绪形容词单独出现
30. 【微表情五维拆解·强制】(2026-08-24 知识库「AIGC人物微表情设计指南」整合)表情=眉眼/嘴角/面部肌肉/呼吸节奏/光影质感五要素组合,不是单个情绪词。人物特写/近景镜至少覆盖 3 个维度:眉眼状态(眉位高低/眉形收放/眼部张力/视线聚焦)、嘴角唇部(上扬下压幅度/唇部紧绷/嘴型张合)、面部肌肉(额部/下颌线/鼻唇沟的收紧松弛颤动)、呼吸节奏(平稳/短促/屏息/抽泣停顿)、光影配合(侧光勾情绪/顶光压氛围)。同一情绪分克制/爆发双档(愤怒克制版=眉心紧锁+眼白微露+嘴角紧绷后张开;爆发版=眼裂放大+瞳孔收缩+面部肌肉强烈)
31. 【哭戏四梯度·强制】(2026-08-24 知识库「AIGC人物微表情设计指南」整合:哭戏的情绪刻度表)哭戏按强度分四档写,禁止笼统"哭了":①强忍泪水(隐忍哭)=眉尾下垂+眼睑轻颤+下眼睑泛红+鼻翼微颤+泪水不落;②无声落泪(安静哭)=泪珠缓慢滑落+眼尾泛红+嘴角微下垂;③抽泣哭(压抑哭)=肩部胸廓起伏+鼻翼煽动+嘴角抽搐;④崩溃大哭(爆发哭)=眼裂放大+泪水滚落+面部张力强。变体:哽咽哭(喉结滚动/泪珠挂睫毛/说不出话)、喜极而泣(嘴角带笑+泪珠眼尾滑落)、委屈哭(下唇轻突+眼睑微颤)。情绪越深越要"少一点更准"(隐忍心动=目光轻回+嘴角极轻上扬+耳尖微红)
32. 【非对称与克制中断·强制】(2026-08-24 知识库「H3提示词优化5层结构方法论」第三层整合:去 AI 感两手法)①非对称:情绪只让半边脸动,明确"眼部不参与/左脸不动"——全脸同步动=AI 味;②克制与中断:动作启动后写中断点,不写"摇头否认",写"摇头启动后在第 10° 突然减速停止"。情绪演出带肌肉层次和克制(泪锁在睫毛边缘不滑落/笑到一半收住),比写满更真
33. 【原子需求台账·强制】(2026-08-24 知识库「H3提示词优化5层结构方法论」第四层整合:每条约束必须可验收)每镜详细描述按台账四栏自查并落实:必须出现(核心主体/关键动作/关键道具)/必须保持(发型/瞳色/服装/疤痕/场景布局——写"第 N 秒时仍是长黑发、蓝开衫、圆框眼镜"这类可见终态,不写"保持一致")/允许变化(表情/光线/镜头内可动元素)/禁止出现(无关人物/多余物品/画面文字/水印)。"保持一致"=没写,必须改写成可被结果检查的可见终态;多素材任务显式写清每张图各自负责什么,不让两份素材抢同一核心身份
34. 【配音音色绑定·条件强制】(2026-08-26 用户需求;2026-08-30 官方格式对齐:Audio 定义必须绑 <Subject M> (Sx)——绑中文名时模型无法把音色与画面里的英文名角色关联=音色随机分配的根源;2026-08-30 ver14 音色指纹:Audio 定义行必须带该角色年龄段+音色质感短语,模型按身份短语分配声线——无年龄信息=音色与角色年龄无关。voice_bindings 非空时生效;空则完全忽略本条)输入 voice_bindings 是本镜绑定配音音色的角色编号表([{"char_id": 角色名, "audio": "<Audio 1>"}...],按登场顺序编号,audio 编号严禁改动):①subject_definitions 中为每个绑定角色追加一行音色定义——<Audio N> is the voice-timbre reference for <Subject M> (Sx), with <该角色年龄段+音色质感短语(与角色卡 age/gender 一致,如 an elderly woman with a warm crackly voice / a young man with a clear steady voice)>, containing a spoken voiceover.(N=voice_bindings 的 audio 编号,M=该角色 Subject 编号=登场序,Sx=该角色在【本镜内】的说话者 ID——按本镜发声顺序从 S1 连续分配,与第 2 条同源);②detailed_description 中该角色说台词处写 with voice timbre referencing <Audio N>(放在 (Sx) 之后、<d> 之前,并入句内自然英语,不另起句);③旁白/画外音不绑定(voice_bindings 只含登场角色);④禁止把 <Audio N> 绑定到别的角色、禁止改写编号、禁止编造 voice_bindings 里没有的 <Audio>
35. 【链式衔接·气闸原则·强制】(2026-08-27 官方 MotionContext README 核验转化;2026-08-30 ver14 注入上一镜收尾)非首镜的镜头是接上一镜续写的(pinned 头),输入 prev_shot 是上一镜收尾分镜数据,本镜开头画面必须承接它:①detailed_description 开头 1-2 句用官方延续句式承接上一镜收尾(continues seamlessly from the previous shot / carries over from the previous shot,写人物位置/姿势/情绪/景别的承接),再写本镜内容;②气闸:开头先保持上一镜收尾构图约 1-2 秒(同一构图/人物位置,无新主体无台词),再发展本镜内容——官方实测这种"气闸"衔接比直接硬切更紧;③开头画面的人物安排必须与上一镜收尾一致:提示词若与 pinned 帧矛盾(上一镜结尾 A 特写、本镜开头写 B+C 双人),模型不二选一而是**全部渲染(union)**——这正是"多出不相干人脸"的深层根源;④承接段也要有微小可见动作(a breath/a weight shift/an eyeline change/fabric or hair movement),官方:"静止的 hold 渲染成字面冻结";⑤接缝时间预算:渲染出片比采样短 0.92s(pin 头 22 帧),动作节拍按早 0.92s 预算(写给 4.0s 的节拍成品在 3.08s)
36. 【位置锚定纪律·强制】(2026-08-30 ver14,H3 官方 Reference Anchors:人物点位错乱的根治面——官方 3d-animation 技能逐镜必填「屏幕相对位置+朝向」)①每个登场角色在 detailed_description 首次清晰出现处,必须写**屏幕位置**(画面左/中/右 + 前景/中景/背景)+**朝向**(facing camera / facing left / facing right,背对时写 turned away),禁止只写动作不写位置;②场景固定地标(门/窗/桌/柜台)写屏幕相对位置(如 door-frame at the right third of the frame),同一场景跨镜沿用,位置变化写明确连续说明;③非首镜开头人物位置必须与上一镜收尾一致,需要换位时写明确换位动作过渡(如 she steps from the left side to the center),禁止无过渡的左右翻转;④同一镜内人物相对位置(谁在左谁在右)一旦确定,镜内不得翻转`


func manjuShotPromptSystem(hasChar bool, style string) string {
	sys := "你是 MiniMax H3 视频生成模型的提示词专家。基于给定镜头的分镜信息与角色/场景卡，直出该镜【完整】H3 提示词（英文主体、中文台词/旁白原文）。\n\n输出严格 JSON：{\"h3_prompt\": \"提示词全文\"}\n\n"
	sys += "【输出体积硬约束·2026-09-04 官方对齐扩容(官方要求 detailed_description as detailed and explicit as possible,正文信息密度直接决定画面细节;旧 150-220 词约束压掉了构图/材质/光影细节,画面空洞)】:\n"
	sys += "- 六段式必须完整(字段齐全);subject_definitions 逐项列角色/场景(不展开;每个 <Subject> 一行);\n"
	sys += "- detailed_description 控制在 250-350 英文词(开场构图→主体外观+屏幕位置朝向→动作状态变化逐步展开→运镜句内自然英语→光影材质→台词逐字→收尾动作;每个主体首次出现处写屏幕位置+朝向;对话密集优先完整台词时间线);\n"
	sys += "- overall_soundscape 1-2 句、non_diegetic_music 1 句、summary/retention_analysis 各 1 句;\n"
	sys += "- 整个 h3_prompt 控制在 2200 tokens 以内(约 900 英文词);正文宁详勿略,禁止背景铺陈注水;\n"
	sys += "- 台词/旁白逐字保留(中文原文),时长=输入 duration。\n\n"
	if hasChar {
		opening := manjuStyleDesc(style).opening
		if opening == "" {
			opening = manjuStyleDesc("real").opening
		}
		sys += strings.ReplaceAll(manjuRef2vaTpl, "{style}", opening) + manjuShotWritingRules
	} else {
		sys += strings.ReplaceAll(manjuFl2vaTpl, "{style}", manjuStyleShot1(style)) + manjuShotWritingRules
	}
	return sys
}

// manjuMultiCutAddon 多切点长镜附加规范(experimental;官方 [Shot N] At MM:SS.mmm 语法,
// 切点时间由输入 take_shots[].cut_at 直供,避免 LLM 自算出错)
const manjuMultiCutAddon = `

【多切点长镜头(Multi-Cut Long Take,单次生成内多镜头切换)附加规范】:
- 本提示词覆盖输入 take_shots 里的一组连续镜头,单次生成内含多个机位切点:
  [Shot 1] 开场段无时间戳;每个后续切点写 [Shot N] At MM:SS.mmm(直接引用该镜输入的 cut_at,时间严格递增)
- 每个 [Shot N] 段完整交代:景别/机位/主体动作/台词(如有,逐字保留);切点处画面与声音同步切换,干净利落
- 整段一气呵成的连续调度感;detailed_description/integrated_multimodal_description 按切点分段组织
- total duration = 输入 shot.duration(组内时长和);最后一段自然收尾`
