package api

// 漫剧管线 ComfyUI 客户端 + 工作流构建
// 节点图重建依据:ComfyUI /object_info 节点 schema + 官方 MiniMaxH3 模板(video_minimax_h3_r2v.json)
//   + ComfyUI-H3-Motion-Context 示例工作流 + ComfyUI-H3-ConditioningCache 源码。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type comfyClient struct {
	base   string
	client *http.Client
}

func newComfyClient(base string) *comfyClient {
	if base == "" {
		base = "http://127.0.0.1:8190"
	}
	return &comfyClient{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}

// hasNode 查询 ComfyUI 是否装有某自定义节点(/object_info/<name>,200=有)
func (c *comfyClient) hasNode(name string) bool {
	resp, err := c.client.Get(c.base + "/object_info/" + name)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode == 200
}

func (c *comfyClient) online() (string, error) {
	resp, err := c.client.Get(c.base + "/system_stats")
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
func (c *comfyClient) submit(workflow map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{"prompt": workflow})
	resp, err := c.client.Post(c.base+"/prompt", "application/json", bytes.NewReader(body))
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
func (c *comfyClient) history(promptID string) map[string]any {
	resp, err := c.client.Get(c.base + "/history/" + promptID)
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
func (c *comfyClient) inQueue(promptID string) bool {
	resp, err := c.client.Get(c.base + "/queue")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var q struct {
		QueueRunning []map[string]any `json:"queue_running"`
		QueuePending []map[string]any `json:"queue_pending"`
	}
	if json.Unmarshal(data, &q) != nil {
		return false
	}
	for _, it := range q.QueueRunning {
		if id, _ := it["prompt_id"].(string); id == promptID {
			return true
		}
	}
	for _, it := range q.QueuePending {
		if id, _ := it["prompt_id"].(string); id == promptID {
			return true
		}
	}
	return false
}

// wait 轮询执行完成;中断/错误返回错误(含异常信息,供调用方判断是否重试)
// wait 轮询执行完成;中断/错误返回错误(含异常信息,供调用方判断是否重试)。
// stopped 为可选停止感知回调:用户点「停止」后立即返回"已停止"错误,
// 不再死等 ComfyUI(尤其卡在模型加载/排队的任务,/interrupt 无法中断它们)。
// 停止感知用 500ms 细粒度轮询(不随 poll 间隔变慢——poll 可能 10s,停止要立即生效)。
func (c *comfyClient) wait(promptID string, timeout, poll time.Duration, stopped ...func() bool) error {
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
			req, err := http.NewRequest("POST", c.base+"/interrupt", nil)
			if err == nil {
				if resp, err := c.client.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}
		}
		return isStopped()
	}
	// 审计 7.1:任务丢失检测——ComfyUI 重启后 history 一直无记录且不在队列,
	// 继续死等满超时毫无意义;连续 5 次轮询(约 5×poll)既无 history 又不在队列即判丢失
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
		entry := c.history(promptID)
		if entry != nil {
			missTicks = 0
			if st, _ := entry["status"].(map[string]any); st != nil {
				if ss, _ := st["status_str"].(string); ss == "error" {
					return fmt.Errorf("ComfyUI 任务失败: %s", comfyErrMsg(st))
				}
				if done, _ := st["completed"].(bool); done {
					return nil
				}
			}
		} else if !c.inQueue(promptID) {
			missTicks++
			if missTicks >= 5 {
				return fmt.Errorf("ComfyUI 任务丢失(可能服务已重启): %s", promptID)
			}
		} else {
			missTicks = 0
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

// comfyErrMsg 从 status 提取最可读的异常信息(优先 exception_message)
func comfyErrMsg(st map[string]any) string {
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

// 提取执行结果里的媒体文件(遍历所有节点输出)
func comfyOutputFiles(entry map[string]any, key string) []string {
	var out []string
	outputs, _ := entry["outputs"].(map[string]any)
	for _, v := range outputs {
		nm, _ := v.(map[string]any)
		arr, _ := nm[key].([]any)
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				fn, sub := str(m["filename"]), str(m["subfolder"])
				out = append(out, filepath.ToSlash(filepath.Join(sub, fn)))
			}
		}
	}
	return out
}

func comfyOutputVideo(entry map[string]any) string {
	files := comfyOutputFiles(entry, "video")
	if len(files) > 0 {
		return files[0]
	}
	// SaveVideo 在 history 里以 images 键上报(animated=true)
	files = comfyOutputFiles(entry, "images")
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

func comfyOutputImage(entry map[string]any) string {
	files := comfyOutputFiles(entry, "images")
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// ---- 工作流构建 ----

// h3Length 镜头时长(秒) → 帧数(H3 17k+5 网格 @24fps,与官方模板 ComfyMathExpression 同公式)
// 注意 Go 的 % 对负数取模与 Python 不同,须归一为非负(否则长度会少 17 的倍数)
func h3Length(seconds, fps int) int {
	base := max(5, (seconds*fps+1)/2*2)
	delta := (5 - base%17) % 17
	if delta < 0 {
		delta += 17
	}
	return base + delta
}

// wfImage 通用图生图工作流(SDXL checkpoint 或 Z-Image unet),返回 SaveImage 节点 id
// initImage 非空时走 img2img:主图作 latent 起点(VAEEncode),denoise 0.6 保留身份、
// 按提示词重绘视角/构图(定妆照多视图与主图保持同一人,防"侧面/全身变成不相干新角色")
// 审计升级:denoise 0.6 对 Z-Image(turbo 8 步)重绘量太小,四视图全变正面——
// 视图换视角需要更高 denoise(0.8),用 initStrength 参数(缺省 0.6)
func wfImage(workflow map[string]any, typ, prompt, neg string, seed, w, h, steps int, cfg float64, ckpt, unet, clipName, clipType, vae, prefix, initImage string, initStrength float64) string {
	n := len(workflow) + 1
	add := func(classType string, inputs map[string]any) string {
		id := itoa(n)
		n++
		workflow[id] = map[string]any{"class_type": classType, "inputs": inputs}
		return id
	}
	var modelID, clipID, vaeID string
	if typ == "sdxl" {
		cid := add("CheckpointLoaderSimple", map[string]any{"ckpt_name": ckpt})
		modelID, clipID, vaeID = cid+"[0]", cid+"[1]", cid+"[2]"
	} else {
		mid := add("UNETLoader", map[string]any{"unet_name": unet, "weight_dtype": "default"})
		clipID = add("CLIPLoader", map[string]any{"clip_name": clipName, "type": clipType})
		vaeID = add("VAELoader", map[string]any{"vae_name": vae})
		modelID = mid + "[0]"
	}
	pos := add("CLIPTextEncode", map[string]any{"clip": refOf(clipID), "text": prompt})
	negID := add("CLIPTextEncode", map[string]any{"clip": refOf(clipID), "text": neg})
	var latentID string
	denoise := 1.0
	if initImage != "" {
		load := add("LoadImage", map[string]any{"image": initImage})
		latentID = add("VAEEncode", map[string]any{"pixels": refOf(load), "vae": refOf(vaeID)})
		if initStrength > 0 && initStrength < 1 {
			denoise = initStrength
		} else {
			denoise = 0.6
		}
	} else if typ == "sdxl" {
		latentID = add("EmptyLatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	} else {
		latentID = add("EmptySD3LatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	}
	samp := add("KSampler", map[string]any{
		"model": refOf(modelID), "positive": refOf(pos), "negative": refOf(negID),
		"latent_image": refOf(latentID), "seed": seed, "steps": steps, "cfg": cfg,
		"sampler_name": "euler", "scheduler": "normal", "denoise": denoise,
	})
	dec := add("VAEDecode", map[string]any{"samples": refOf(samp), "vae": refOf(vaeID)})
	return add("SaveImage", map[string]any{"images": refOf(dec), "filename_prefix": prefix})
}

// refOf 把一个 "N" 或 "N[0]" 形式的引用转为 API 引用值
func refOf(id string) any {
	if i := strings.Index(id, "["); i > 0 {
		return []any{id[:i], mustAtoi(id[i+1 : len(id)-1])}
	}
	return []any{id, 0}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
func mustAtoi(s string) int {
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

// wfSDXL 人物定妆照/抽卡(SDXL checkpoint;写实风格用 Z-Image 时走 wfZImage)
// initImage 非空 → img2img(保留身份重绘视角;视图生成用);initStrength 0-1(缺省 0.6)
func wfSDXL(prompt, ckpt string, seed, w, h int, prefix, neg, initImage string, initStrength float64) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "sdxl", prompt, neg, seed, w, h, 25, 7.0, ckpt, "", "", "", "", prefix, initImage, initStrength)
	return wf
}

// wfZImage 场景图/写实定妆照(8 步 turbo;neg 为负面提示词,缺省空串)
// initImage 非空 → img2img(保留身份重绘视角;视图生成用);initStrength 0-1(缺省 0.6)
func wfZImage(prompt, unet, clipName, vae string, seed, w, h int, prefix, neg, initImage string, initStrength float64) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "zimage", prompt, neg, seed, w, h, 8, 1.0, "", unet, clipName, "qwen_image", vae, prefix, initImage, initStrength)
	return wf
}

// manjuNegPrompt 内置默认负面提示词(render.neg_prompt 未配置/为空时的兜底)
const manjuNegPrompt = "lowres, bad anatomy, bad hands, text, error, extra digit, no text, no watermark, no deformed hands, flickering frames, temporal discontinuity, inconsistent lighting"

// manjuModelRefs 载入 H3 三件套(clip / vae_video / vae_audio),返回 [clip, vae, audioVae]
func h3Loaders(workflow map[string]any, R map[string]any) (clip, vae, audioVae string) {
	clip = wfAdd(workflow, "CLIPLoader", map[string]any{"clip_name": str(R["clip"]), "type": "minimax"})
	vae = wfAdd(workflow, "VAELoader", map[string]any{"vae_name": str(R["vae_video"])})
	audioVae = wfAdd(workflow, "VAELoader", map[string]any{"vae_name": str(R["vae_audio"])})
	return
}

func wfAdd(workflow map[string]any, classType string, inputs map[string]any) string {
	id := itoa(len(workflow) + 1)
	workflow[id] = map[string]any{"class_type": classType, "inputs": inputs}
	return id
}

// fl2vaNodeOK FL2VA 双帧节点可用性探测(审计升级 P1:空镜可选首尾双图插值,官方
// FL2VA 单镜连续更稳;节点缺失自动回退单图,零风险)。带缓存,进程生命周期内探测一次。
var (
	fl2vaNodeOnce sync.Once
	fl2vaNodeOK   bool
)

func fl2vaNodeAvailable() bool {
	fl2vaNodeOnce.Do(func() {
		c := newComfyClient(comfyParams().url)
		resp, err := c.client.Get(c.base + "/object_info/MiniMaxH3Fl2VA")
		if err == nil {
			defer resp.Body.Close()
			fl2vaNodeOK = resp.StatusCode == 200
		}
	})
	return fl2vaNodeOK
}

// h3EncWorkflow 预编码工作流(只跑 Qwen3-VL,无 UNET;输出 CondSave 缓存 .pt)
// hasChar: 有角色 → MiniMaxH3ReferenceToVideo(角色+场景多参考);空镜 → MiniMaxH3ImageToVideo(场景首帧),
// 若 R["_scene_end"] 提供尾帧且节点可用 → MiniMaxH3Fl2VA 首尾双帧插值(审计升级 P1)
// charRefs: 全部登场角色的参考图(正脸优先),多角色同镜逐一传入锁身份
func h3EncWorkflow(R map[string]any, prompt string, w, h, length int, charRefs []string, sceneRef, cacheName string, hasChar bool) map[string]any {
	wf := map[string]any{}
	clip, vae, audioVae := h3Loaders(wf, R)
	var condID string
	if hasChar {
		inputs := map[string]any{
			"clip": refOf(clip), "vae": refOf(vae), "audio_vae": refOf(audioVae),
			"prompt": prompt, "width": w, "height": h, "length": length, "ref_image_size": "match",
		}
		var refs []any
		for _, cr := range charRefs { // 多角色:每个登场角色一张参考图(正脸优先)
			if cr == "" {
				continue
			}
			refs = append(refs, refOf(wfAdd(wf, "LoadImage", map[string]any{"image": cr})))
		}
		if sceneRef != "" {
			refs = append(refs, refOf(wfAdd(wf, "LoadImage", map[string]any{"image": sceneRef})))
		}
		if len(refs) > 0 {
			inputs["ref_images"] = refs
		}
		condID = wfAdd(wf, "MiniMaxH3ReferenceToVideo", inputs)
	} else {
		var sceneLoad string
		if sceneRef != "" {
			sceneLoad = wfAdd(wf, "LoadImage", map[string]any{"image": sceneRef})
		}
		// FL2VA 双帧:尾帧图存在且节点可用 → 首尾双图插值;否则回退单图 I2VA
		// (审计 4.2:尾帧已由调用方复制进 comfyInput 并传文件名,此处仅判非空)
		if end := strings.TrimSpace(str(R["_scene_end"])); end != "" && fl2vaNodeAvailable() {
			endLoad := wfAdd(wf, "LoadImage", map[string]any{"image": end})
			inputs := map[string]any{
				"clip": refOf(clip), "vae": refOf(vae),
				"prompt": prompt, "width": w, "height": h, "length": length,
			}
			if sceneLoad != "" {
				inputs["first_frame"] = refOf(sceneLoad)
			}
			inputs["last_frame"] = refOf(endLoad)
			condID = wfAdd(wf, "MiniMaxH3Fl2VA", inputs)
		} else {
			inputs := map[string]any{
				"clip": refOf(clip), "vae": refOf(vae),
				"prompt": prompt, "width": w, "height": h, "length": length,
			}
			if sceneLoad != "" {
				inputs["first_frame"] = refOf(sceneLoad)
			}
			condID = wfAdd(wf, "MiniMaxH3ImageToVideo", inputs)
		}
	}
	wfAdd(wf, "MiniMaxH3CondSave", map[string]any{"conditioning": refOf(condID), "cache_name": cacheName})
	return wf
}

// turboLoRASpec 不同 Turbo LoRA 的最优参数(按文件名识别,数据驱动可扩展):
// 采样器/强度/推荐步数不兼容会明显劣化画质甚至出废片,换 LoRA 无需改代码。
type turboLoRASpec struct {
	Strength  float64
	Sampler   string
	Scheduler string
	Steps     int
	FL2VOnly  bool // FL2V 专用蒸馏版(Kijai LightX2V):R2V 镜头不挂,自动回退全步数
}

func turboLoRASpecOf(name string) turboLoRASpec {
	n := strings.ToLower(name)
	if strings.Contains(n, "lightx2v") || strings.Contains(n, "kijai") {
		// Kijai LightX2V 4 步蒸馏版(HuggingFace Kijai/MiniMax-H3_comfy,ComfyUI 原生适配):
		// 强度 0.75 + 采样器 sa_solver + 4 步(er_sde 亦可);音频质量比其它 4 步方案好。
		// 注意:该版为 FL2V 蒸馏,R2V(角色镜头)不兼容——h3RenderWorkflow 会对 R2V 自动摘除
		return turboLoRASpec{Strength: 0.75, Sampler: "sa_solver", Scheduler: "simple", Steps: 4, FL2VOnly: true}
	}
	// larryvrh 系 4step EMA 等旧默认(FL2V/R2V 通用)
	return turboLoRASpec{Strength: 0.8, Sampler: "res_multistep", Scheduler: "simple", Steps: 8}
}

// manjuResTiers 数据驱动分辨率档位:tier → 短边像素(对齐 minimax-h3-starter 的 416P/768P 分层理念)。
// 档位按配置画幅等比换算,两边都对齐 32(H3 VAE 32× 下采样网格;1080 非 32 倍数,fhd 用 1088);
// 未配置/custom = 直接用 width×height 手动值。新增档位只改这张表(数据驱动,不加代码分支)。
var manjuResTiers = map[string]int{"draft": 416, "standard": 768, "fhd": 1088}

// manjuAlign32 对齐到最近的 32 倍数(至少 32)
func manjuAlign32(n int) int {
	if n < 32 {
		return 32
	}
	return (n + 16) / 32 * 32
}

// manjuResTierDims 档位 × 画幅 → 实际宽高(保持宽高比,短边=档位值,另一边对齐 32);
// tier 无效返回原值 + false(调用方保持手动宽高)。
func manjuResTierDims(tier string, w, h int) (int, int, bool) {
	short, ok := manjuResTiers[tier]
	if !ok || w <= 0 || h <= 0 {
		return w, h, false
	}
	if w <= h { // 竖屏:宽为短边
		return manjuAlign32(short), manjuAlign32(h * short / w), true
	}
	return manjuAlign32(w * short / h), manjuAlign32(short), true
}

// h3RenderWorkflow 采样渲染工作流:CondLoad 加载条件缓存(跳过重复 Qwen3-VL 编码)
// + EmptyMiniMaxH3LatentAV 空 AV latent + Turbo LoRA + 可选 MotionContext 接缝。
// 有角色用 ref2va 模型,空镜用 fl2va;接缝时 LoadLatent(prevIdx) → MotionContext → Trim,
// 无论是否接缝都 SaveLatent(curIdx),供下一镜续接。
// latent 命名空间取自 R["_latent_ns"](审计 S6):output/h3_context/<ns>/clip_NNNNN,
// 防跨项目/跨方案/草稿定稿分辨率互相串接;缺省回退 default(测试兼容)。
func h3RenderWorkflow(R map[string]any, seed, w, h, length, steps int, cacheName string, hasChar, chained bool, prevIdx, curIdx int) map[string]any {
	wf := map[string]any{}
	latentNS := strings.TrimSpace(str(R["_latent_ns"]))
	if latentNS == "" {
		latentNS = "default"
	}
	unetName := str(R["unet_ref2va"])
	if !hasChar {
		unetName = str(R["unet_fl2va"])
	}
	model := wfAdd(wf, "UNETLoader", map[string]any{"unet_name": unetName, "weight_dtype": "default"})
	// Turbo LoRA 选择与参数自动适配:
	// - R2V(角色镜)优先 turbo_lora_r2v(未配置沿用 turbo_lora);
	//   若解析到 FL2V 专用蒸馏版(Kijai LightX2V)则 R2V 自动摘除并回退全步数,防不兼容劣化
	// - FL2V(空镜)用 turbo_lora;强度/采样器按 LoRA 类型参数表
	loraName := str(R["turbo_lora"])
	if hasChar {
		if r2v := str(R["turbo_lora_r2v"]); r2v != "" {
			loraName = r2v
		}
	}
	spec := turboLoRASpecOf(loraName)
	if hasChar && spec.FL2VOnly && str(R["turbo_lora_r2v"]) == "" {
		// FL2V 专用 LoRA 不挂 R2V:LoRA/采样器/步数全部回退默认,保证角色镜质量
		loraName = ""
		spec = turboLoRASpecOf("")
		if n, ok := manjuToInt(R["steps"]); ok && n > 0 {
			steps = n
		}
	}
	if loraName != "" {
		model = wfAdd(wf, "LoraLoaderModelOnly", map[string]any{"model": refOf(model), "lora_name": loraName, "strength_model": spec.Strength})
	}
	// SageAttention 加速补丁(KJNodes,starter 官方工作流同款):
	// 长序列注意力量化加速,RTX 50 系白捡提速。默认关。
	// 节点名用 sageAttnGuard 探测到的实际注册名——KJNodes 上游把类名拼错为
	// PathchSageAttentionKJ(非 Patch),用错名字 ComfyUI 会报 missing_node_type 400。
	if b, _ := R["sage_attention"].(bool); b {
		nodeName := "PatchSageAttentionKJ"
		if n := str(R["sage_node_name"]); n != "" {
			nodeName = n
		}
		model = wfAdd(wf, nodeName, map[string]any{
			"model": refOf(model), "sage_attention": "auto", "allow_compile": false,
		})
	}
	vae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_video"])})
	audioVae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_audio"])})

	condID := wfAdd(wf, "MiniMaxH3CondLoad", map[string]any{"cache_name": cacheName})
	latentID := wfAdd(wf, "EmptyMiniMaxH3LatentAV", map[string]any{"width": w, "height": h, "length": length})

	// 接缝:MotionContext(condLoad 条件 + 上一镜 latent)→ conditioning + trim_frames
	trimFramesID := ""
	if chained {
		latLoad := wfAdd(wf, "MiniMaxH3MotionContextLoadLatent", map[string]any{"latent_path": "h3_context/" + latentNS, "clip_index": prevIdx})
		mc := wfAdd(wf, "MiniMaxH3MotionContext", map[string]any{
			"conditioning": refOf(condID), "vae": refOf(vae), "latent": refOf(latentID),
			"context_length": "22", "audio_context_length": 24,
			"context_latent": refOf(latLoad),
		})
		// MotionContext 输出 0=conditioning, 1=trim_frames
		condID = mc + "[0]"
		trimFramesID = mc + "[1]"
	}

	guider := wfAdd(wf, "BasicGuider", map[string]any{"model": refOf(model), "conditioning": refOf(condID)})
	noise := wfAdd(wf, "RandomNoise", map[string]any{"noise_seed": seed})
	// 采样器随 Turbo LoRA 类型:Kijai LightX2V 4步版必须 sa_solver(er_sde 亦可),旧 larryvrh 系用 res_multistep
	sampler := wfAdd(wf, "KSamplerSelect", map[string]any{"sampler_name": spec.Sampler})
	sched := wfAdd(wf, "BasicScheduler", map[string]any{"model": refOf(model), "scheduler": spec.Scheduler, "steps": steps, "denoise": 1.0})
	samp := wfAdd(wf, "SamplerCustomAdvanced", map[string]any{
		"noise": refOf(noise), "guider": refOf(guider), "sampler": refOf(sampler),
		"sigmas": refOf(sched), "latent_image": refOf(latentID),
	})

	imgID := wfAdd(wf, "VAEDecode", map[string]any{"samples": refOf(samp), "vae": refOf(vae)})
	audID := wfAdd(wf, "VAEDecodeAudio", map[string]any{"samples": refOf(samp), "vae": refOf(audioVae)})

	// 接缝裁掉 burn-in 前缀(视频/音频),否则直接用解码输出
	if chained {
		trim := wfAdd(wf, "MiniMaxH3MotionContextTrim", map[string]any{
			"images": refOf(imgID), "trim_frames": refOf(trimFramesID), "audio": refOf(audID),
			"fps": float64(h3Fps(R)), "match_tail": true,
		})
		imgID = trim + "[0]"
		audID = trim + "[1]"
	}
	video := wfAdd(wf, "CreateVideo", map[string]any{"images": refOf(imgID), "fps": float64(h3Fps(R)), "audio": refOf(audID)})
	wfAdd(wf, "SaveVideo", map[string]any{"video": refOf(video), "filename_prefix": "manju", "format": "auto", "codec": "auto"})

	// 保存当前镜 latent 供下一镜续接(总是保存,即使未接缝);按命名空间隔离(审计 S6)
	wfAdd(wf, "MiniMaxH3MotionContextSaveLatent", map[string]any{
		"latent": refOf(samp), "filename_prefix": "h3_context/" + latentNS + "/clip", "clip_index": curIdx,
	})
	return wf
}

func h3Fps(R map[string]any) int {
	fps := 24
	if n, ok := manjuToInt(R["fps"]); ok && n > 0 {
		fps = n
	}
	return fps
}

// h3ContextLatentPath 上一镜 latent 落盘路径(output/h3_context/<ns>/clip_%05d.safetensors);
// ns 为命名空间(项目_集,审计 S6 防跨项目串接)
func h3ContextLatentPath(comfyOutput, ns string, idx int) string {
	return filepath.Join(comfyOutput, "h3_context", ns, fmt.Sprintf("clip_%05d.safetensors", idx))
}

func h3CachePath(sharedModels, cacheName string) string {
	return filepath.Join(sharedModels, "conditioning", cacheName+".pt")
}

// randSeed 抽卡随机 seed
func randSeed() int {
	return rand.Intn(1<<31 - 1)
}

// dirExists 判断目录存在
func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// fileExists 判断文件存在
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
