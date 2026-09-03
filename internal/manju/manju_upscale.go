package manju

// 云端 2K 定稿(MiniMax /v2/video_regeneration):本地 ComfyUI 产物恰好满足云端重生成接口的
// 全部预校验(768×1344=面积上限且被32整除 / 恰好24fps / 17k+5 帧网格=官方 107~362 步长17 允许集 /
// QC 保证含音轨),把本地定稿镜头以 base_video 提交云端升 2K——本地 GPU 零负担,分辨率上限
// 突破本机显存约束。参考:ArcReel lib/video_backends/minimax.py(v2 API 客户端)、
// minimax-h3-starter tools/minimax_video_regenerate.py(校验规则+轮询+断点下载)。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// manjuMinimaxDefaults 云端重生成服务缺省(国内平台为 api.minimaxi.com,可在 config.render.minimax_base_url 覆盖)
// manjuUpscalePricePerSec 云端 2K 单价(元/秒,预估展示用;数据驱动,调整只改这里)
var manjuUpscalePricePerSec = 0.80

// manjuUpscaleEstimate 整集/指定镜头 2K 费用预估:总时长(秒)×单价
func (ctx *manjuCtx) manjuUpscaleEstimate(shots string) map[string]any {
	plan, _, err := ctx.loadPlan()
	totalSec := 0.0
	count := 0
	if err == nil {
		want := map[int]bool{}
		if shots != "" && shots != "all" {
			for _, p := range strings.Split(shots, ",") {
				if n, e := strconv.Atoi(strings.TrimSpace(p)); e == nil {
					want[n] = true
				}
			}
		}
		for _, x := range anyArr(plan["shots"]) {
			if m, ok := x.(map[string]any); ok {
				id, _ := manjuToInt(m["shot_id"])
				if len(want) > 0 && !want[id] {
					continue
				}
				d, _ := manjuToFloat(m["duration"])
				if d <= 0 {
					d = 5
				}
				totalSec += d
				count++
			}
		}
	}
	if count == 0 {
		if entries, e := os.ReadDir(filepath.Join(ctx.clipsDir, ctx.episode)); e == nil {
			for _, en := range entries {
				if !en.IsDir() && strings.HasSuffix(strings.ToLower(en.Name()), ".mp4") {
					count++
					totalSec += 8
				}
			}
		}
	}
	cost := totalSec * manjuUpscalePricePerSec
	return map[string]any{"shots": count, "durationSec": int(totalSec), "costCNY": round1(cost)}
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

const (
	manjuMinimaxDefaultBase = "https://api.minimax.io"
	manjuMinimaxModel       = "MiniMax-H3"
	manjuMinimaxResolution  = "2K"
)

// manjuProbeInfo 本地视频探测结果(probe 子命令输出)
type manjuProbeInfo struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       float64 `json:"fps"`
	Frames    int     `json:"frames"`
	HasAudio  bool    `json:"hasAudio"`
	SizeBytes int64   `json:"sizeBytes"`
}

// probeVideo 探测本地视频参数(venv PyAV)
func (ctx *manjuCtx) probeVideo(p string) (*manjuProbeInfo, error) {
	out, err := ctx.runMediaOut("probe", "--file", p)
	if err != nil {
		return nil, fmt.Errorf("探测失败: %w", err)
	}
	m := parseJSONLine(out)
	if m == nil {
		return nil, fmt.Errorf("探测输出无 JSON")
	}
	b, _ := json.Marshal(m)
	var info manjuProbeInfo
	if err := json.Unmarshal(b, &info); err != nil || info.Width == 0 {
		return nil, fmt.Errorf("探测 JSON 解析失败")
	}
	return &info, nil
}

// manjuValidate2K 云端重生成提交前本地拦截(starter CLI 同款规则,付费前 fail-loud):
// 宽高被32整除 / 面积≤768×1344 / 恰好24fps / 帧数∈107~362 且 ≡5(mod 17)(=NiliX 17k+5 网格)/
// 含音轨 / ≤50MB。返回违规清单(空=通过)。
func manjuValidate2K(info *manjuProbeInfo) []string {
	var bad []string
	if info.Width%32 != 0 || info.Height%32 != 0 {
		bad = append(bad, fmt.Sprintf("宽高须被 32 整除(%dx%d)", info.Width, info.Height))
	}
	if info.Width*info.Height > 768*1344 {
		bad = append(bad, fmt.Sprintf("面积超上限(%dx%d > 768x1344)", info.Width, info.Height))
	}
	if info.FPS != 24 {
		bad = append(bad, fmt.Sprintf("帧率须恰好 24fps(实际 %.3f)", info.FPS))
	}
	if info.Frames < 107 || info.Frames > 362 || (info.Frames-5)%17 != 0 {
		bad = append(bad, fmt.Sprintf("帧数 %d 不在允许网格(107~362,步长17,≡5 mod 17)", info.Frames))
	}
	if !info.HasAudio {
		bad = append(bad, "无音轨")
	}
	if info.SizeBytes > 50<<20 {
		bad = append(bad, fmt.Sprintf("文件 %.1fMB 超 50MB 上限", float64(info.SizeBytes)/(1<<20)))
	}
	return bad
}

// manjuMinimaxKey 云端 Key 解析:项目 render.minimax_api_key → settings.json minimax_api_key → 环境变量
func manjuMinimaxKey(ctx *manjuCtx) string {
	if k := strings.TrimSpace(str(ctx.R["minimax_api_key"])); k != "" {
		return k
	}
	if b, err := os.ReadFile(manjuSettingsFile); err == nil {
		var def map[string]any
		if json.Unmarshal(b, &def) == nil {
			if k := strings.TrimSpace(str(def["minimax_api_key"])); k != "" {
				return k
			}
		}
	}
	return strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
}

func manjuMinimaxBase(ctx *manjuCtx) string {
	if u := strings.TrimRight(strings.TrimSpace(str(ctx.R["minimax_base_url"])), "/"); u != "" {
		return u
	}
	return manjuMinimaxDefaultBase
}

// manjuUpscaleSubmit 提交重生成任务:content = [text 提示词, video_url base_video(本地 mp4 base64 data URI)]
func manjuUpscaleSubmit(client *http.Client, base, key, prompt, videoPath, resolution string) (string, error) {
	data, err := os.ReadFile(videoPath)
	if err != nil {
		return "", fmt.Errorf("读视频失败: %w", err)
	}
	if prompt == "" {
		prompt = "Upscale the base video to high resolution. Keep all motion, camera work, speech and audio exactly identical to the base video."
	}
	payload := map[string]any{
		"model":      manjuMinimaxModel,
		"resolution": orDefault(resolution, manjuMinimaxResolution),
		"content": []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "video_url", "video_url": map[string]string{
				"url": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data),
			}, "role": "base_video"},
		},
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", base+"/v2/video_regeneration", strings.NewReader(string(b)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("提交失败: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("提交 HTTP %d: %s", resp.StatusCode, truncate(string(rb), 300))
	}
	var r struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if json.Unmarshal(rb, &r) != nil || (r.TaskID == "" && r.ID == "") {
		return "", fmt.Errorf("提交响应无 task_id: %s", truncate(string(rb), 200))
	}
	return orDefault(r.TaskID, r.ID), nil
}

// manjuUpscalePoll 轮询重生成任务直至出片,返回下载 URL(https 强校验)
func manjuUpscalePoll(client *http.Client, base, key, taskID string, timeout time.Duration, stopped func() bool) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if stopped != nil && stopped() {
			return "", fmt.Errorf("已停止")
		}
		req, err := http.NewRequest("GET", base+"/v2/query/video_generation/"+taskID, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("查询失败: %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("查询 HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
		}
		var r struct {
			Task struct {
				Status  string `json:"status"`
				Content struct {
					URL string `json:"url"`
				} `json:"content"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"task"`
		}
		if json.Unmarshal(data, &r) == nil {
			switch r.Task.Status {
			case "succeeded", "success", "done":
				// 下载地址强制 https(回环地址豁免:本地 mock/代理场景)
				if !strings.HasPrefix(r.Task.Content.URL, "https://") && !strings.Contains(r.Task.Content.URL, "127.0.0.1") && !strings.Contains(r.Task.Content.URL, "localhost") {
					return "", fmt.Errorf("下载地址非法: %s", truncate(r.Task.Content.URL, 80))
				}
				return r.Task.Content.URL, nil
			case "failed", "cancel", "cancelled":
				msg := orDefault(r.Task.Error.Message, "unknown")
				return "", fmt.Errorf("云端任务失败: %s", truncate(msg, 200))
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("云端任务超时(%s),task_id=%s 可稍后手动查询", timeout, taskID)
		}
		time.Sleep(10 * time.Second)
	}
}

// manjuUpscaleDownload 流式下载到 .part 临时文件后原子落盘
func manjuUpscaleDownload(client *http.Client, url, dst string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(part)
		return err
	}
	f.Close()
	return os.Rename(part, dst)
}

// manjuUpscale2kDir 云端 2K 产物目录(clips/<ep>/2k,与本地定稿同集隔离)
func (ctx *manjuCtx) manjuUpscale2kDir() string {
	return filepath.Join(ctx.clipsDir, ctx.episode, "2k")
}

// manjuUpscaleOne 单镜头云端 2K:探测→校验→提交→轮询→下载到 2k/NN.mp4
func (ctx *manjuCtx) manjuUpscaleOne(shotID int, prompt, key string, lg *manjuLogger) error {
	src := filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", shotID))
	dst := filepath.Join(ctx.manjuUpscale2kDir(), fmt.Sprintf("%02d.mp4", shotID))
	if fileExists(dst) {
		lg.logf(fmt.Sprintf("  跳过（已有 2K 产物）: %s", dst))
		return nil
	}
	info, err := ctx.probeVideo(src)
	if err != nil {
		return fmt.Errorf("镜头 %d %w", shotID, err)
	}
	if bad := manjuValidate2K(info); len(bad) > 0 {
		return fmt.Errorf("镜头 %d 不满足云端重生成条件: %s", shotID, strings.Join(bad, "、"))
	}
	client := &http.Client{Timeout: 120 * time.Second}
	lg.logf(fmt.Sprintf("  ☁️ 镜头 %d 提交云端重生成(%dx%d %d帧 %.1fMB)...", shotID, info.Width, info.Height, info.Frames, float64(info.SizeBytes)/(1<<20)))
	taskID, err := manjuUpscaleSubmit(client, manjuMinimaxBase(ctx), key, prompt, src, manjuMinimaxResolution)
	if err != nil {
		return fmt.Errorf("镜头 %d %w", shotID, err)
	}
	lg.logf("  ⏳ 云端任务 " + taskID[:8] + " 生成中(2K,约 2-8 分钟,每 10s 查询)...")
	url, err := manjuUpscalePoll(client, manjuMinimaxBase(ctx), key, taskID, 30*time.Minute, lg.stopped)
	if err != nil {
		return fmt.Errorf("镜头 %d %w", shotID, err)
	}
	dl := &http.Client{Timeout: 10 * time.Minute}
	if err := manjuUpscaleDownload(dl, url, dst); err != nil {
		return fmt.Errorf("镜头 %d 下载失败: %w", shotID, err)
	}
	lg.logf(fmt.Sprintf("  ✅ 镜头 %d 云端 2K 完成 -> %s", shotID, dst))
	return nil
}

// manjuUpscaleRun 云端 2K 定稿后台任务(整集或指定镜头;shotts=""/"all"=本集全部)
func manjuUpscaleRun(w http.ResponseWriter, configPath, episode, shots string) {
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"已有任务运行中，先停止"}`, http.StatusConflict)
		return
	}
	projName := filepath.Base(filepath.Dir(configPath))
	manjuState.running = true
	manjuState.stage = "upscale"
	manjuState.log = ""
	manjuState.rc = nil
	manjuState.done = false
	manjuState.started = time.Now()
	manjuState.baseElapsed = 0
	manjuState.stopped = false
	manjuState.project = projName
	manjuState.episode = episode
	manjuState.mu.Unlock()
	writeManjuDiskState(projName, &manjuDiskState{Running: true, Stage: "upscale", StartedAt: time.Now().Unix(), Episode: episode})
	_ = os.WriteFile(manjuRunLogPath(projName), nil, 0644)

	// 云端 2K 后台任务:panic 兜底(审计 S2——网络/JSON 处理 panic 不崩进程)
	safeGo("upscale2k", nil, func() {
		rc := 1
		runFile, _ := os.OpenFile(manjuRunLogPath(projName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		defer func() {
			if runFile != nil {
				_ = runFile.Close()
			}
			manjuFinish(rc)
		}()
		ctx, err := newManjuCtx(configPath, episode, "", "", "")
		if err != nil {
			return
		}
		lg := newManjuLogger(manjuState, runFile, ctx.project, ctx.episode)
		key := manjuMinimaxKey(ctx)
		if key == "" {
			lg.logf("❌ 未配置 MiniMax API Key(设置 → 智能体调度 → MiniMax Key,或项目 config.render.minimax_api_key / 环境变量 MINIMAX_API_KEY)")
			return
		}
		plan, _, perr := ctx.loadPlan()
		if perr != nil {
			lg.logf("⚠️ 方案读取失败,提示词回退保真升格: " + perr.Error())
		}
		promptOf := func(id int) string {
			for _, x := range anyArr(plan["shots"]) {
				if m, ok := x.(map[string]any); ok {
					if n, ok := manjuToInt(m["shot_id"]); ok && n == id {
						return str(m["h3_prompt"])
					}
				}
			}
			return ""
		}
		// 目标镜头:指定(如 "3"/"1,3")或本集全部顶层镜头
		var ids []int
		if shots != "" && shots != "all" {
			for _, p := range strings.Split(shots, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
					ids = append(ids, n)
				}
			}
		}
		if len(ids) == 0 {
			entries, _ := os.ReadDir(filepath.Join(ctx.clipsDir, ctx.episode))
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
					continue
				}
				if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".mp4")); err == nil {
					ids = append(ids, n)
				}
			}
		}
		if len(ids) == 0 {
			lg.logf("❌ 本集没有已渲染镜头,先跑渲染")
			return
		}
		lg.logf(fmt.Sprintf("☁️ 云端 2K 定稿: %d 镜(本地草稿已通过审片,云端只做升格;产物 → clips/%s/2k/)", len(ids), ctx.episode))
		failed := 0
		for i, id := range ids {
			if lg.stopped() {
				lg.logf("⏹ 已停止(已完成 2K 保留,可续跑)")
				rc = 0
				return
			}
			lg.logf(fmt.Sprintf("[%d/%d] 镜头 %d", i+1, len(ids), id))
			if err := ctx.manjuUpscaleOne(id, promptOf(id), key, lg); err != nil {
				lg.logf("  ❌ " + err.Error())
				failed++
			}
		}
		if failed > 0 {
			lg.logf(fmt.Sprintf("⚠️ %d/%d 镜头 2K 失败(详见上方日志;已成功的保留)", failed, len(ids)))
			return
		}
		lg.logf("🎉 云端 2K 定稿全部完成")
		rc = 0
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// registerUpscaleRoutes 云端 2K + 剪映导出路由
func registerUpscaleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/manju/jianying", manjuJianyingExport)
	mux.HandleFunc("GET /api/manju/upscale2k/estimate", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		episode := orDefault(r.URL.Query().Get("episode"), "EP01")
		shots := r.URL.Query().Get("shots")
		if configPath == "" {
			writeErr(w, http.StatusBadRequest, "missing config")
			return
		}
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		ctx, err := newManjuCtx(cp, episode, "", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ctx.manjuUpscaleEstimate(shots))
	})
	mux.HandleFunc("POST /api/manju/upscale2k", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath := str(body["config"])
		episode := orDefault(str(body["episode"]), "EP01")
		shots := str(body["shots"])
		if configPath == "" {
			http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
			return
		}
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		if _, err := readManjuConfig(cp); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		manjuUpscaleRun(w, cp, episode, shots)
	})
}

// manjuJianyingExport 剪映草稿导出(同步,无 GPU 依赖):视频轨+字幕轨(不烧录,可继续编辑)。
// 依赖 venv 安装 pyJianYingDraft(未装时脚本 exit 2 + 安装指引,本接口原样透出)。
// render.jianying_dir 配置剪映草稿目录时导出后自动复制进去(数据驱动,可选)。
func manjuJianyingExport(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	ctx, err := newManjuCtx(cp, episode, "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	if !hasTopLevelClips(clipsEp) {
		http.Error(w, `{"error":"该集没有已渲染镜头,先跑渲染"}`, http.StatusBadRequest)
		return
	}
	outDir := filepath.Join(ctx.workdir, "剪映草稿")
	name := ctx.episode + "_NiliX"
	args := []string{"jianying", "--clips-dir", clipsEp, "--out-dir", outDir, "--name", name,
		"--fps", strconv.Itoa(ctx.fps), "--plan", manjuFindPlanDir(ctx.analysisDir, ctx.episode)}
	if trans := orDefault(str(ctx.R["transition"]), "cut"); manjuTransitions[trans] {
		args = append(args, "--transition", trans)
		if hc := ctx.seamHardCuts(); hc != "" {
			args = append(args, "--hard-cuts", hc)
		}
	}
	out, err := ctx.runMediaOut(args...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if i := strings.LastIndex(msg, "\n"); i > 0 && len(msg)-i < 400 {
			msg = msg[i+1:] // 末行通常是最要紧的指引
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": truncate(orDefault(msg, err.Error()), 400)})
		return
	}
	res := parseJSONLine(out)
	draft := ""
	if res != nil {
		draft = str(res["draft"])
	}
	// 自动复制进剪映草稿目录(可选配置)
	copied := ""
	if jyDir := strings.TrimSpace(str(ctx.R["jianying_dir"])); jyDir != "" && draft != "" {
		if err := copyTree(draft, filepath.Join(jyDir, filepath.Base(draft))); err == nil {
			copied = filepath.Join(jyDir, filepath.Base(draft))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft": draft, "copiedTo": copied})
}

// copyTree 递归复制目录(剪映草稿自动入库用)
