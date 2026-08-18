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

// wait 轮询执行完成;中断/错误返回错误(含异常信息,供调用方判断是否重试)
func (c *comfyClient) wait(promptID string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		entry := c.history(promptID)
		if entry != nil {
			if st, _ := entry["status"].(map[string]any); st != nil {
				if ss, _ := st["status_str"].(string); ss == "error" {
					return fmt.Errorf("ComfyUI 任务失败: %s", comfyErrMsg(st))
				}
				if done, _ := st["completed"].(bool); done {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ComfyUI 等待超时(%s)", timeout)
		}
		time.Sleep(poll)
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
func wfImage(workflow map[string]any, typ, prompt, neg string, seed, w, h, steps int, cfg float64, ckpt, unet, clipName, clipType, vae string, prefix string) string {
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
	if typ == "sdxl" {
		latentID = add("EmptyLatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	} else {
		latentID = add("EmptySD3LatentImage", map[string]any{"width": w, "height": h, "batch_size": 1})
	}
	samp := add("KSampler", map[string]any{
		"model": refOf(modelID), "positive": refOf(pos), "negative": refOf(negID),
		"latent_image": refOf(latentID), "seed": seed, "steps": steps, "cfg": cfg,
		"sampler_name": "euler", "scheduler": "normal", "denoise": 1.0,
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
func wfSDXL(prompt, ckpt string, seed, w, h int, prefix, neg string) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "sdxl", prompt, neg, seed, w, h, 25, 7.0, ckpt, "", "", "", "", prefix)
	return wf
}

// wfZImage 场景图/写实定妆照(8 步 turbo;neg 为负面提示词,缺省空串)
func wfZImage(prompt, unet, clipName, vae string, seed, w, h int, prefix, neg string) map[string]any {
	wf := map[string]any{}
	wfImage(wf, "zimage", prompt, neg, seed, w, h, 8, 1.0, "", unet, clipName, "qwen_image", vae, prefix)
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

// h3EncWorkflow 预编码工作流(只跑 Qwen3-VL,无 UNET;输出 CondSave 缓存 .pt)
// hasChar: 有角色 → MiniMaxH3ReferenceToVideo(角色+场景多参考);空镜 → MiniMaxH3ImageToVideo(场景首帧)
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
		inputs := map[string]any{
			"clip": refOf(clip), "vae": refOf(vae),
			"prompt": prompt, "width": w, "height": h, "length": length,
		}
		if sceneLoad != "" {
			inputs["first_frame"] = refOf(sceneLoad)
		}
		condID = wfAdd(wf, "MiniMaxH3ImageToVideo", inputs)
	}
	wfAdd(wf, "MiniMaxH3CondSave", map[string]any{"conditioning": refOf(condID), "cache_name": cacheName})
	return wf
}

// h3RenderWorkflow 采样渲染工作流:CondLoad 加载条件缓存(跳过重复 Qwen3-VL 编码)
// + EmptyMiniMaxH3LatentAV 空 AV latent + Turbo LoRA + 可选 MotionContext 接缝。
// 有角色用 ref2va 模型,空镜用 fl2va;接缝时 LoadLatent(prevIdx) → MotionContext → Trim,
// 无论是否接缝都 SaveLatent(curIdx),供下一镜续接。
func h3RenderWorkflow(R map[string]any, seed, w, h, length, steps int, cacheName string, hasChar, chained bool, prevIdx, curIdx int) map[string]any {
	wf := map[string]any{}
	unetName := str(R["unet_ref2va"])
	if !hasChar {
		unetName = str(R["unet_fl2va"])
	}
	model := wfAdd(wf, "UNETLoader", map[string]any{"unet_name": unetName, "weight_dtype": "default"})
	if lora := str(R["turbo_lora"]); lora != "" {
		model = wfAdd(wf, "LoraLoaderModelOnly", map[string]any{"model": refOf(model), "lora_name": lora, "strength_model": 0.8})
	}
	vae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_video"])})
	audioVae := wfAdd(wf, "VAELoader", map[string]any{"vae_name": str(R["vae_audio"])})

	condID := wfAdd(wf, "MiniMaxH3CondLoad", map[string]any{"cache_name": cacheName})
	latentID := wfAdd(wf, "EmptyMiniMaxH3LatentAV", map[string]any{"width": w, "height": h, "length": length})

	// 接缝:MotionContext(condLoad 条件 + 上一镜 latent)→ conditioning + trim_frames
	trimFramesID := ""
	if chained {
		latLoad := wfAdd(wf, "MiniMaxH3MotionContextLoadLatent", map[string]any{"latent_path": "h3_context", "clip_index": prevIdx})
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
	sampler := wfAdd(wf, "KSamplerSelect", map[string]any{"sampler_name": "res_multistep"})
	sched := wfAdd(wf, "BasicScheduler", map[string]any{"model": refOf(model), "scheduler": "simple", "steps": steps, "denoise": 1.0})
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

	// 保存当前镜 latent 供下一镜续接(总是保存,即使未接缝)
	wfAdd(wf, "MiniMaxH3MotionContextSaveLatent", map[string]any{
		"latent": refOf(samp), "filename_prefix": "h3_context/clip", "clip_index": curIdx,
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

// h3ContextLatentPath 上一镜 latent 落盘路径(output/h3_context/clip_%05d.safetensors)
func h3ContextLatentPath(comfyOutput string, idx int) string {
	return filepath.Join(comfyOutput, "h3_context", fmt.Sprintf("clip_%05d.safetensors", idx))
}

func h3CachePath(sharedModels, cacheName string) string {
	return filepath.Join(sharedModels, "conditioning", cacheName+".pt")
}

// randSeed 抽卡随机 seed
func randSeed() int {
	return rand.Intn(1<<31 - 1)
}

// fileExists 判断文件存在
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
