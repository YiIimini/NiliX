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
	resp, err := l.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
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
	// 素材/ 目录:按文件名语义归类
	if entries, err := os.ReadDir(filepath.Join(root, "素材")); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			low := strings.ToLower(e.Name())
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
  "characters": [{"id": "角色名", "gender": "男/女", "age": "年龄段", "appearance": "完整外观（发型/脸型/五官/气质，逐字从原文提炼，具体到可渲染）", "costume": "完整服装描述", "image_prompt": "给图片模型的英文文生图提示词（半身立绘，` + assetStyle + ` 风格，正面正脸、头部完整居中（含发顶到下巴），含完整外观/服装/性别强化）"}],
  "scenes": [{"id": "场景名（取自原文）", "description": "空间结构/材质/光线/氛围", "image_prompt": "给图片模型的英文文生图提示词（空场景无人物，明亮清晰，` + assetStyle + ` 风格）"}],
  "shots": [
    {
      "shot_id": 1,
      "scene": "场景id",
      "characters": ["角色id(该镜实际登场的全部角色:说话人+同时出镜者,缺一不可;未列出的角色一律不得入画)"],
      "shot_size": "特写/近景/中景/全景/远景",
      "camera": "运镜（类型+幅度+速度，如：缓慢推近）",
      "action": "画面动作描述",
      "dialogue": "角色:台词（逐字引用小说原文对白，禁止改写/扩写/编造；多句用换行分隔；无对白为空）",
      "narration": "旁白（逐字引用原文旁白，禁止改写；无则空；有台词时旁白留空避免重复）",
      "duration": 5
    }
  ]
}
【时长硬约束】duration 由台词/动作量决定:中文台词约 4 字/秒(20 字台词≈5 秒;60 字≈12 秒),台词长于时长容纳量必须加时长(4-15)或拆镜;旁白同速折算。台词被截断=废镜。
【分镜纪律·强制】:
- shots[].characters 必须列全该镜实际在场的全部角色(说话人+同时出镜者,缺一不可);未列出的角色(长老/弟子/路人/群众)一律不得入画,如需氛围只允许无面部细节的远景虚化
- 有台词的说话人必须是该镜的视觉中心主体(景别/机位优先对准说话人),其他登场角色不得遮挡或抢占画面中心
- 同一角色在整集所有镜头中形象必须完全一致(外观/服装逐字复用其 characters 卡,禁止同角色换装/换写法)`
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
<Subject 1> is the character in <Picture 1> with [完整外观：逐字引用角色卡 appearance；服装 costume 全字段；【性别强化】女=feminine facial structure, soft delicate features, long hair（禁男性化），男=masculine jawline, strong brow, broad shoulders（禁女性化）]
[多角色镜:每个登场角色一行 <Subject N> is the character in <Picture N>…,与参考图顺序一致(角色在前场景在后);画面里谁先出现谁 Subject 号靠前]
[参考图纪律·强制:只有输入中 ref_available 名单里的角色才写 in <Picture N>;名单外的登场角色(本镜参考图不足)写 <Subject N> is [角色名] with 外观描述(不引用任何 Picture),并保持与参考角色不串脸]
<Subject N+1> is the [场景名] environment in <Picture N+1>, with [空间结构/材质/光线客观描述，引用场景卡]
[关键道具：<Subject M> is the [道具名] in <Picture M>, with 外观描述；说明与角色互动]

summary:
[reference generation] 本镜任务概述（1-2 句英文，说明目标视频与参考主体关系）

retention_analysis:
<Subject 1> (appears in [Shot 1]): fully_preserved - 面部/发型/服装与 <Picture 1> 完全一致
[多角色镜:每个角色一行 retention_analysis,全部 fully_preserved]
<Subject N+1> (appears in [Shot 1]): fully_preserved - 场景布局/光线/背景与 <Picture N+1> 一致
[道具行同理]（标记只用官方四值：fully_preserved / partially_preserved / attribute_transfer / weak_reference）

detailed_description:
{style}。[实体锁定句：The face, hairstyle, costume of <Subject 1> must remain exactly as in <Picture 1> throughout the shot; the scene layout of <Subject N+1> must match its reference.; 多角色镜加 Each character must keep their own identity from their own reference picture, never swap or blend identities.]
[Shot 1] [300-500 词：开场构图→主体外观位置→动作状态变化→运镜（类型+幅度+速度）→光影→台词/旁白→收尾；<Subject N> 标签在主体首次出现处插入；情感戏/对话优先近景/中景；末尾散文排除项 no subtitles, no text overlays, no watermark；【亮度护栏·强制】Dark mood is fine for atmosphere, but the subject's face and body must remain clearly visible and well-lit at all times - use a clear light source on the subject (candlelight, moonlight, torch, window light); never render the frame nearly black]

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
1. 首镜 [Shot 1] 无时间戳
2. 说话者 (S1)(S2) 按发声顺序分配、跨镜一致；首次出现给身份描述（年龄/性别/音色/语速/是否画内）；发声者写 <Subject N> (Sx)
3. 台词 <d>[中文]原文</d> 逐字保留（原词原标点，句末以 。？！结束，不译不改写，H3 原生对白配音）
4. 【旁白 = H3 原生画外音，不是 TTS】：写 The narrator (S1) says in an off-screen voiceover: <d>[中文]旁白</d> while the on-screen characters' lips remain completely closed（旁白计入 (S1) 说话顺序；旁白与台词不同时出现）
5. 画外音台词也写 says in an off-screen voiceover ... while his/her lips remain completely closed
6. 运镜三要素（类型+幅度+速度）写成句内自然英语（Push In/Pull Out/Pan/Truck/Tilt/Pedestal/Arc/Tracking/Static/POV/Roll/Shake）
7. 排除项/可见文字用英文双引号原文；H3 为 CFG-distilled 无负面词，禁堆叠负面词。
   输入中的 negative_prompt(用户负面提示词)必须逐概念转译为正面排除句,合并写入 detailed_description 末尾散文(如 bad hands→no distorted hands;text→no text overlays, no watermark;flickering→no flickering frames);语义与既有排除项重复的合并,不重复罗列
8. 画面禁情绪化形容词（只写机位/光影/动作/构图），音效只写现场声，BGM 只写乐器与节奏
9. 输入中的 known_issues 是本项目历史高频审片问题:针对每个问题在 detailed_description 写一句正面规避描述(如 面部扭曲→face and hands anatomy must be natural and well-formed;近黑帧→主体带明确光源;水印文字→clean frame, no text overlays),不堆负面词不逐字罗列
10. 【角色纪律·强制】画面中只允许出现该镜 characters 列出的登场角色;未列出的角色(长老/弟子/路人/群众)一律不得入画——如需氛围只能以无面部细节的远景剪影/虚化背景出现,禁止特写/近景/中心构图
11. 【主角中心·强制】说话人/动作主角必须是该镜的视觉中心主体(居中/近景/构图优先),其他登场角色不得抢占画面中心或遮挡主角;主角形象与其参考图完全一致(face, hairstyle, costume)`

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
