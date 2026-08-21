package api

// 漫剧管线 LLM 客户端(DeepSeek chat/completions,JSON 直出)
// 提示词模板移植自 h3-direct-render skill(v6,对齐 H3 官方 h3-prompt-writing)。

import (
	"bytes"
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
	req, err := http.NewRequest("POST", l.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.apiKey)
	// 审计 H12:瞬时失败(429/5xx/网络抖动)指数退避重试,单次抖动不再打崩整条管线
	// (此前零重试:plan 一次 429 即中断 AI 一条龙;修复师失败按原提示词白烧 GPU)
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 停止感知:用户点「停止」后立即放弃(LLM 请求最长 300s,卡住时停止必须生效)
		if l.stopped != nil && l.stopped() {
			return "", fmt.Errorf("已停止")
		}
		resp, err := l.client.Do(req)
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
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
		lastErr = fmt.Errorf("LLM 请求失败: %w", err)
		if attempt < maxAttempts {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
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
	if i := strings.Index(text, "{"); i > 0 {
		text = text[i:]
	}
	if i := strings.LastIndex(text, "}"); i >= 0 {
		text = text[:i+1]
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("LLM 输出非 JSON: %v (前 200 字: %s)", err, truncate(text, 200))
	}
	return out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- 知识库注入:config.knowledge 指向 zhishiku/AI漫剧生产 模板(与旧 direct_render.py 同款) ----

func manjuKBChunks(cfg map[string]any, keys []string) string {
	kb, _ := cfg["knowledge"].(map[string]any)
	if kb == nil {
		return ""
	}
	var out []string
	for _, k := range keys {
		arr, _ := kb[k].([]any)
		for _, x := range arr {
			rel := str(x)
			if rel == "" {
				continue
			}
			p := rel
			if !filepath.IsAbs(p) {
				p = filepath.Join(`C:\Mi\Ai\WorkBench\zhishiku\AI漫剧生产`, rel)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			txt := string(data)
			if r := []rune(txt); len(r) > 5000 {
				txt = string(r[:5000])
			}
			out = append(out, "【知识库模板《"+filepath.Base(rel)+"》要点(仅作设定/风格参考,必须贴合本剧世界观)】\n"+txt)
		}
	}
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
var manjuStyles = map[string]manjuStyleSpec{
	"2.5d":       {"2.5D anime, semi-realistic detailed anime", "The target video is in a 2.5D anime style, semi-realistic, detailed anime art, cinematic realistic lighting, high quality anime illustration", "[Shot 1] 2.5D anime style, semi-realistic, detailed anime art, cinematic realistic lighting"},
	"real":       {"photorealistic live-action, cinematic film still, real human", "The target video is in a cinematic, live-action style", "[Shot 1] Live-action, cinematic"},
	"3d":         {"3D CG render, detailed 3D animation", "The target video is in a high-quality 3D CG animation style, detailed rendering, cinematic lighting", "[Shot 1] 3D CG animation, detailed rendering, cinematic lighting"},
	"anime":      {"anime style, vibrant cel shading, detailed anime illustration", "The target video is in a vibrant anime style, cel shading, detailed anime illustration, cinematic lighting", "[Shot 1] Anime style, vibrant cel shading, detailed anime illustration"},
	"handdrawn":  {"hand-drawn illustration, organic sketch lines, storybook art", "The target video is in a hand-drawn illustration style, organic sketch lines, storybook art, cinematic lighting", "[Shot 1] Hand-drawn illustration style, organic sketch lines"},
	"papercraft": {"papercraft stop-motion, layered cut paper, tactile texture", "The target video is in a papercraft stop-motion style, layered cut paper, tactile texture, cinematic lighting", "[Shot 1] Papercraft stop-motion style, layered cut paper"},
	"clay":       {"claymation stop-motion, plasticine figures, tactile", "The target video is in a claymation stop-motion style, plasticine figures, tactile, cinematic lighting", "[Shot 1] Claymation stop-motion style, plasticine figures"},
	"ink":        {"Chinese ink wash painting, traditional brushwork, minimalist", "The target video is in a Chinese ink wash painting style, traditional brushwork, minimalist, cinematic lighting", "[Shot 1] Chinese ink wash painting style, traditional brushwork"},
}

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

// manjuStyleDesc 取风格措辞。
// 单个预设 key 直接查表;组合(预设+预设 / 预设+自定义词,以 + 分隔)逐段解析:
// 预设段取其 asset 核心措辞、自定义段原样保留,再拼成一句整体风格;其余视为自定义风格原样使用。
func manjuStyleDesc(style string) manjuStyleSpec {
	if s, ok := manjuStyles[style]; ok {
		return s
	}
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
			} else {
				core = append(core, p)
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
	}
	return manjuStyleSpec{asset: style, opening: style, shot1: style}
}

// manjuAssetStyle 返回给图片模型(定妆照/场景图)的风格措辞，与逐镜 H3 风格保持一致。
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

func scanNovelAssets(root string) manjuNovelAssets {
	var out manjuNovelAssets
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
	// 按 ## N、标题 切节
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
	var styleSec, negSec, charSec, sceneSec, masterSec string
	for name, body := range sections {
		switch {
		case strings.Contains(name, "一") && strings.Contains(name, "风格"):
			styleSec = body
		case strings.Contains(name, "二") && (strings.Contains(name, "负面") || strings.Contains(name, "负向")):
			negSec = body
		case strings.Contains(name, "三") && strings.Contains(name, "角色"):
			charSec = body
		case strings.Contains(name, "四") && strings.Contains(name, "场景"):
			sceneSec = body
		case strings.Contains(name, "五") && strings.Contains(name, "H3"):
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

func manjuDirectSystem(cfg map[string]any, style string) string {
	kbChar, kbScene, kbStory := manjuKnowledgeChunks(cfg)
	assetStyle := manjuAssetStyle(style)
	s := `你是 MiniMax H3 视频生成模型的导演兼提示词专家。基于给定的小说章节，直接输出完整漫剧渲染方案。

【内容纪律·最高优先】：分镜必须忠实还原小说原文，画面与小说对不上=废镜：
- 台词/旁白必须逐字引用小说原文（原词原句原标点，禁止改写/扩写/翻译/编造）
- 关键剧情事件（冲突/反转/打脸/名场面）必须有对应镜头，禁止跳事件、禁止张冠李戴
- 角色外观/服装/道具逐字从原文提炼，禁止自行增删设定
- 原文没有的台词和事件一律不许出现

【输出 JSON（严格）】：
{
  "episode_title": "集标题",
  "characters": [{"id": "角色名", "gender": "男/女", "age": "年龄段", "appearance": "完整外观（发型/脸型/五官/气质，逐字从原文提炼，具体到可渲染）", "costume": "完整服装描述", "image_prompt": "给图片模型的英文文生图提示词（半身立绘，` + assetStyle + ` 风格，正面正脸、头部完整居中（含发顶到下巴），含完整外观/服装/性别强化）", "views": {"front": "英文文生图提示词：正面特写（头肩构图，正脸居中含发顶到肩，面部五官/发型/领口细节清晰，` + assetStyle + ` 风格）", "full": "英文文生图提示词：全身立绘（完整头到脚，正面站姿，完整服装/鞋履/体态，` + assetStyle + ` 风格）", "side": "英文文生图提示词：侧面轮廓（侧脸 90 度，发型/脸型/服装侧面轮廓清晰，` + assetStyle + ` 风格）", "detail": "英文文生图提示词：细节特写（该角色最有辨识度的 1 个细节：饰品/花纹/发饰/疤痕等，大特写构图，` + assetStyle + ` 风格）"}}],
  "scenes": [{"id": "场景名（取自原文）", "description": "空间结构/材质/光线/氛围", "image_prompt": "给图片模型的英文文生图提示词（空场景无人物，明亮清晰，` + assetStyle + ` 风格）"}],
  "shots": [
    {
      "shot_id": 1,
      "scene": "场景id",
      "characters": ["角色id(该镜实际登场的全部角色:说话人+同时出镜者,缺一不可;未列出的角色一律不得入画)"],
      "shot_size": "特写/近景/中景/全景/远景",
      "camera": "运镜（类型+幅度+速度，如：缓慢推近）",
      "action": "画面动作描述",
      "dialogue": "角色:台词（逐字引用小说原文对白，禁止改写/扩写/编造；多句用换行分隔；无对白为空）。【说话人硬约束】\"角色:\"前缀必须是本镜 characters 中实际开口的角色，谁说的就是谁，禁止张冠李戴；角色说的话一律放 dialogue，禁止混入旁白",
      "narration": "旁白（仅原文叙述性文字/画外音，逐字引用）。【硬约束】旁白禁止包含任何角色的台词——角色说的每句话必须放进 dialogue 并标注对应角色；原文中\"XXX说\"的对白必须标为该角色 dialogue；无旁白则空；有台词时旁白留空避免重复",
      "duration": 5
    }
  ]
}
【时长硬约束】duration 由台词/动作量决定:中文台词约 4 字/秒(20 字台词≈5 秒;60 字≈12 秒),台词长于时长容纳量必须加时长(4-15)或拆镜;旁白同速折算。台词被截断=废镜。
【分镜纪律·强制】:
- shots[].characters 必须列全该镜实际在场的全部角色(说话人+同时出镜者,缺一不可);未列出的角色(长老/弟子/路人/群众)一律不得入画,如需氛围只允许无面部细节的远景虚化
- 【说话人纪律·强制】谁说的就是谁:原文对白按说话角色逐句标入对应 dialogue(前缀"角色:"),严禁把某角色说的话标成他人台词或塞进旁白;旁白只承载原文叙述,绝不含角色话语
- 有台词的说话人必须是该镜的视觉中心主体(景别/机位优先对准说话人),其他登场角色不得遮挡或抢占画面中心
- 同一角色在整集所有镜头中形象必须完全一致(外观/服装逐字复用其 characters 卡,禁止同角色换装/换写法)
【面容独特性纪律·强制(防跨剧撞脸)】:
- 每个角色的 image_prompt 必须给出**独一无二的面容锚点组合**:从 眼型(丹凤眼/桃花眼/狭长眼/圆眼)、眉型(剑眉/柳叶眉/浓眉/细眉)、鼻型(高挺/小巧/鹰钩)、唇形(薄唇/丰唇/唇珠)、脸型(瓜子/方圆/棱角/鹅蛋)、肤色(苍白/小麦/古铜)、气质 中选至少 4 个具体特征,并给 1 个独有印记(痣/疤/耳饰/发色挑染等);**禁止** generic 泛化词(sharp jawline/clear eyes/handsome/young man 单独出现都算,必须搭配具体特征)
- 同剧多角色面容必须**互不相同**(五官/发型/气质可辨认区分);不同剧的相同职位角色(如各剧主角)也必须是不同面容,禁止模板化雷同
- appearance 字段同步给出这些独特特征的中文描述(供提示词外观锁定引用)`
	if kbChar != "" {
		s += "\n\n【知识库角色模板参考（仅作设定参考，贴合本剧）】\n" + kbChar
	}
	if kbScene != "" {
		s += "\n\n【知识库场景模板参考（仅作设定参考，贴合本剧）】\n" + kbScene
	}
	if kbStory != "" {
		s += "\n\n【知识库分镜/运镜模板参考】\n" + kbStory
	}
	return s
}

// ---- 逐镜 H3 提示词直出(六段式 Ref2VA / 三段式 FL2VA) ----

func manjuStyleShot1(style string) string {
	return manjuStyleDesc(style).shot1
}

const manjuRef2vaTpl = `【Ref2VA 六段式(有角色,锁人物),严格此顺序】:
subject_definitions:
<Subject 1> is the character in <Picture 1> and <Picture 2> ... with [完整外观：逐字引用角色卡 appearance（发型/眼睛/疤痕/气质/道具等全部特征逐项覆盖，禁止省略/概括/编造）；服装 costume 全字段；【性别强化】女=feminine facial structure, soft delicate features, long hair（禁男性化），男=masculine jawline, strong brow, broad shoulders（禁女性化）]
[同一角色多视图:该角色有几个参考图就引用几张——<Subject 1> is the character in <Picture 1> (正面/正脸特写), <Picture 2> (全身/侧面/细节), ...;每张视图对应一个 <Picture N> 标签,顺序与 ref_available 该角色的视图顺序一致,全部引用后统一写 with [外观...]]
[多角色镜:每个登场角色一行 <Subject N> is the character in <Picture A> and <Picture B> ...,与参考图顺序一致(角色在前场景在后);画面里谁先出现谁 Subject 号靠前]
[参考图纪律·强制:ref_available 是「角色+视图」的平铺清单,顺序就是参考图传入顺序;<Picture 1..N> 严格对应清单第 1..N 项(同一角色多视图占多个 Picture 编号),Subject 编号与角色一一对应(Subject 1=清单第 1 个角色,依次),禁止调换/跳过/合并视图;清单外的登场角色(本镜参考图不足)写 <Subject N> is [角色名] with 外观描述(不引用任何 Picture),并保持与参考角色不串脸]
[外观锁定·强制:每个角色的外观只允许出现角色卡 appearance+costume 里的特征,且逐项覆盖(发型/眼睛/疤痕/服装/道具缺一不可);禁止 generic 泛化词(ordinary/plain/sturdy/average/young man 等),禁止编造角色卡没有的特征(白发/换装/错误年龄);多角色镜严禁把其他角色的特征写进本角色(谁的特征写谁)]
[场景编号·强制:场景的 Picture 编号 = 全部角色视图总数 + 1(如 2 角色各 2 视图 → 场景在 <Picture 5>);Subject 编号 = 角色数 + 1]
<Subject N+1> is the [场景名] environment in <Picture M>(M=角色视图总数+1), with [空间结构/材质/光线客观描述，引用场景卡]
[关键道具：<Subject M> is the [道具名] in <Picture M>, with 外观描述；说明与角色互动]

summary:
[reference generation] 本镜任务概述（1-2 句英文，说明目标视频与参考主体关系；任务前缀用官方固定值——参考生成为 reference generation，本管线恒用此值；只引用已定义标签，禁在 summary 引入新标签）

retention_analysis:
<Subject 1> (appears in [Shot 1]): fully_preserved - 面部/发型/服装与 <Picture 1> 完全一致
[多角色镜:每个角色一行 retention_analysis,全部 fully_preserved]
<Subject N+1> (appears in [Shot 1]): fully_preserved - 场景布局/光线/背景与 <Picture M>(场景编号,同 subject_definitions) 一致
[道具行同理]（标记只用官方固定四值：fully_preserved / partially_preserved / attribute_transfer / weak_reference；【官方规范】retention_analysis 内禁写 (Sx) 说话者 ID）

detailed_description:
{style}。[实体锁定句：The face, hairstyle, costume of <Subject 1> must remain exactly as in <Picture 1> throughout the shot; the scene layout of <Subject N+1> must match its reference.; 多角色镜加 Each character must keep their own identity from their own reference picture, never swap or blend identities.]
[Shot 1] [官方建议 350-500 英文词(对话密集优先完整台词时间线):开场构图→主体外观位置→动作状态变化→运镜(类型+幅度+速度,句内自然英语)→光影→台词/旁白→收尾；<Subject N> 标签在主体首次出现处插入,后续镜复用同标签不重定义；情感戏/对话优先近景/中景；末尾散文排除项 no subtitles, no text overlays, no watermark；【亮度护栏·强制】Dark mood is fine for atmosphere, but the subject's face and body must remain clearly visible and well-lit at all times - use a clear light source on the subject (candlelight, moonlight, torch, window light); never render the frame nearly black]

overall_soundscape:
环境底噪/动作音效（1-4 句英文连续段落，禁重复台词）

non_diegetic_music:
纯器乐配乐（1-3 句：乐器+速度+节奏+动态，禁抽象情绪词；无配乐写 N/A）`

const manjuFl2vaTpl = `【FL2VA 三段式（空镜/转场，无主角），严格此顺序】：
第一行对齐指令（两位小数）：How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video.（尾帧锚定时补 Picture 2 (from Shot N) aligns with the S.SS-second mark，S.SS=镜头时长两位小数）
空一行后：
integrated_multimodal_description:
{style} + 画面延续首帧（首帧锚定→动作展开→收尾）+ 动作/运镜/光影 + 台词/旁白 <d>[中文]原文</d>（H3 原生配音；时长严格=镜头秒数）+【亮度护栏】Subjects must remain clearly visible and adequately lit - keep readable exposure with visible faces and actions; avoid rendering the frame nearly black

overall_soundscape:
non_diegetic_music:`

const manjuShotWritingRules = `

【写作规范（官方强制，两种模式都遵守）】：
1. 首镜 [Shot 1] 无时间戳；多切点长镜后续镜 [Shot N] At MM:SS.mmm 严格递增切点时间（官方格式）
2. 说话者 (S1)(S2) 按实际发声顺序分配一次、跨镜复用同一 ID；首次出现给身份描述（年龄/性别/音色/语速/是否画内）；发声者写 <Subject N> (Sx)；【复合说话者】多人齐声/合唱写复合 ID 如 (S1,S2)（官方规范）；从不发声的角色不分配 ID；retention_analysis 中禁写 (Sx)
3. 台词 <d>[中文]原文</d> 逐字保留（原词原标点，句末以 。？！结束，不译不改写，H3 原生对白配音；听不清的片段写 [unclear] 不许猜写）。【说话人硬约束】分镜 dialogue 的每句台词必须由标注的对应角色开口说出：写该角色 <Subject N> (Sx) says: <d>…</d>——谁说的就是谁，禁止把台词安到别的角色头上、禁止把角色台词改写成旁白/画外音
4. 【旁白 = H3 原生画外音，不是 TTS，更不是角色台词】：只有分镜 narration 字段的内容才写 The narrator (S1) says in an off-screen voiceover: <d>[中文]旁白</d> while the on-screen characters' lips remain completely closed（旁白计入 (S1) 说话顺序；旁白与台词不同时出现；【硬约束】分镜 dialogue 里的角色台词禁止写成旁白——必须由对应角色开口，画面中该角色嘴唇在动）
5. 画外音台词也写 says in an off-screen voiceover ... while his/her lips remain completely closed
6. 【跨镜台词连续性（官方标签）】同一句台词跨越镜头切点时，两段接续处各写 <scenetrans> 并声明音频跨切点连续（continues seamlessly across the cut / carries over from the previous shot）；台词被视频结尾截断写 <cutoff>
7. 运镜三要素（类型+幅度+速度）写成句内自然英语（Push In/Pull Out/Pan Left/Pan Right/Truck/Tilt/Pedestal/Arc/Tracking/Static/POV/Roll/Shake；幅度 with small/large amplitude、速度 at slow/fast speed，中等默认省略——官方词表）
8. 排除项/可见文字用英文双引号原文；H3 为 CFG-distilled 无负面词，禁堆叠负面词。
   输入中的 negative_prompt(用户负面提示词)必须逐概念转译为正面排除句,合并写入 detailed_description 末尾散文(如 bad hands→no distorted hands;text→no text overlays, no watermark;flickering→no flickering frames);语义与既有排除项重复的合并,不重复罗列
9. 画面禁情绪化形容词（只写机位/光影/动作/构图），音效只写现场声，BGM 只写乐器与节奏（禁抽象情绪词，无配乐写 N/A——官方规范）
10. 输入中的 known_issues 是本项目历史高频审片问题:针对每个问题在 detailed_description 写一句正面规避描述(如 面部扭曲→face and hands anatomy must be natural and well-formed;近黑帧→主体带明确光源;水印文字→clean frame, no text overlays),不堆负面词不逐字罗列
11. 【角色纪律·强制】画面中只允许出现该镜 characters 列出的登场角色;未列出的角色(长老/弟子/路人/群众)一律不得入画——如需氛围只能以无面部细节的远景剪影/虚化背景出现,禁止特写/近景/中心构图
12. 【主角中心·强制】说话人/动作主角必须是该镜的视觉中心主体(居中/近景/构图优先),其他登场角色不得抢占画面中心或遮挡主角;主角形象与其参考图完全一致(face, hairstyle, costume)
13. 【跨镜外观锁定·强制】同一角色在本集所有镜头的 subject_definitions 外观描述必须逐字一致(以本集首镜写法为准,后续镜直接复用该写法,禁止每镜重新措辞);detailed_description 中对角色的外观/发型/服装描述也必须与该镜 subject_definitions 一致,禁止出现与 subject_definitions 矛盾的描述`

func manjuShotPromptSystem(hasChar bool, style string) string {
	sys := "你是 MiniMax H3 视频生成模型的提示词专家。基于给定镜头的分镜信息与角色/场景卡，直出该镜【完整】H3 提示词（英文主体、中文台词/旁白原文）。\n\n输出严格 JSON：{\"h3_prompt\": \"提示词全文\"}\n\n"
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
