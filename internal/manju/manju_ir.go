package manju

// 官方 Context IR 提示词扩写(MiniMax POST /v2/h3_context_ir):
// 本接口只返回增强后的视频提示词,不创建视频生成任务——与 NiliX「提示词增强」语义完全对齐。
// 输出为三段式 integrated_multimodal_description / overall_soundscape / non_diegetic_music,
// 与 NiliX 空镜(FL2VA)提示词格式一致,直接替换空镜 h3_prompt。
// 角色镜(Ref2VA 六段式含 <Picture N> 引用)暂不扩写:IR 文本模式输出不含参考图引用,
// 强行替换会丢失 <Subject>/<Picture> 主体映射(参考图与提示词的对应关系)。
// 鉴权/Base 复用 2K 的 manjuMinimaxKey/manjuMinimaxBase(同一把 MiniMax API Key)。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	manjuIRModel       = "MiniMax-H3"
	manjuIRTimeout     = 6 * time.Minute // 单镜 IR 任务轮询上限(IR 通常几十秒,留足余量)
	manjuIRConcurrency = 2               // 空镜并发扩写上限(云端限流友好)
)

// manjuIRSubmit 提交 Context IR 任务(text 模式,无参考图)。返回 task_id。
func manjuIRSubmit(client *http.Client, base, key, prompt string, duration int, ratio string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("IR 输入提示词为空")
	}
	if ratio == "" {
		ratio = "9:16"
	}
	payload := map[string]any{
		"model":    manjuIRModel,
		"content":  []map[string]any{{"type": "text", "text": prompt}},
		"duration": duration,
		"ratio":    ratio,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", base+"/v2/h3_context_ir", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("IR 提交失败: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("IR 提交 HTTP %d: %s", resp.StatusCode, truncate(string(rb), 300))
	}
	var r struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if json.Unmarshal(rb, &r) != nil || (r.TaskID == "" && r.ID == "") {
		return "", fmt.Errorf("IR 提交响应无 task_id: %s", truncate(string(rb), 200))
	}
	return orDefault(r.TaskID, r.ID), nil
}

// manjuIRPoll 轮询 IR 任务,成功返回增强后的提示词(取自 task.content.prompt)。
func manjuIRPoll(client *http.Client, base, key, taskID string, timeout time.Duration, stopped func() bool) (string, error) {
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
			return "", fmt.Errorf("IR 查询失败: %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("IR 查询 HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
		}
		var r struct {
			Task struct {
				Status  string `json:"status"`
				Content struct {
					Prompt string `json:"prompt"`
				} `json:"content"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"task"`
		}
		if json.Unmarshal(data, &r) == nil {
			switch r.Task.Status {
			case "succeeded", "success", "done":
				if strings.TrimSpace(r.Task.Content.Prompt) == "" {
					return "", fmt.Errorf("IR 任务成功但无增强提示词(content.prompt 为空)")
				}
				return r.Task.Content.Prompt, nil
			case "failed", "cancel", "cancelled":
				return "", fmt.Errorf("IR 云端任务失败: %s", truncate(orDefault(r.Task.Error.Message, "unknown"), 200))
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("IR 云端任务超时(%s),task_id=%s", timeout, taskID)
		}
		time.Sleep(5 * time.Second)
	}
}

// irExpandEnabled 是否启用官方 Context IR 扩写(render.ir_expand,缺省开启;
// 未配置 MiniMax API Key 时在调用方自动跳过)。
func irExpandEnabled(R map[string]any) bool {
	if v, ok := R["ir_expand"].(bool); ok {
		return v
	}
	return true
}

// manjuShotIRRatio 由画布尺寸推导 ratio 参数(768×1344 → 9:16;其余按短边归约)。
func manjuShotIRRatio(w, h int) string {
	if w == 0 || h == 0 {
		return "9:16"
	}
	if w == h {
		return "1:1"
	}
	if w > h {
		return "16:9"
	}
	return "9:16"
}

// expandShotsWithIR 对给定镜的提示词做官方 Context IR 扩写(仅空镜,无 <Picture> 引用),
// 成功后更新 prompts 与 plan shots 的 h3_prompt。无 Key / 全为空镜数 0 / 全部失败时返回错误,
// 调用方降级继续(本地提示词兜底,不阻塞管线)。
func (ctx *manjuCtx) expandShotsWithIR(prompts map[string]string, shots map[int]map[string]any, lg *manjuLogger) error {
	key := manjuMinimaxKey(ctx)
	if key == "" {
		lg.logf("  ⏭️ 未配置 MiniMax API Key,跳过官方 Context IR 扩写(提示词保持本地生成)")
		return nil
	}
	var ids []string
	for id, p := range prompts {
		if !strings.Contains(p, "<Picture") {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		lg.logf("  ⏭️ 本集无空镜(全为角色镜),跳过 Context IR 扩写")
		return nil
	}
	lg.logf(fmt.Sprintf("  ☁️ 官方 Context IR 扩写 %d 个空镜提示词(并发 %d)...", len(ids), manjuIRConcurrency))
	client := &http.Client{Timeout: 120 * time.Second}
	sem := make(chan struct{}, manjuIRConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	done := 0
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nid, _ := strconv.Atoi(id)
			p := prompts[id]
			dur := 5
			if m, ok := shots[nid]; ok {
				if d, ok := manjuToFloat(m["duration"]); ok && d > 0 {
					dur = int(d)
				}
			}
			if dur < 4 {
				dur = 4
			}
			if dur > 15 {
				dur = 15
			}
			taskID, err := manjuIRSubmit(client, manjuMinimaxBase(ctx), key, p, dur, manjuShotIRRatio(ctx.w, ctx.h))
			if err == nil {
				var out string
				out, err = manjuIRPoll(client, manjuMinimaxBase(ctx), key, taskID, manjuIRTimeout, nil)
				if err == nil && strings.TrimSpace(out) != "" {
					mu.Lock()
					prompts[id] = out
					if m, ok := shots[nid]; ok {
						m["h3_prompt"] = out
					}
					done++
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	lg.logf(fmt.Sprintf("  ✅ Context IR 完成 %d/%d 个空镜", done, len(ids)))
	if done == 0 && firstErr != nil {
		return fmt.Errorf("Context IR 全部失败(已保留本地提示词): %w", firstErr)
	}
	return nil
}

// manjuIRRun 手动触发 Context IR 扩写(后台任务):读取 plan,对空镜逐镜 IR,更新并写盘。
// 用于改完提示词/脚本后重跑官方扩写;失败镜头保留原提示词,不阻塞。
func manjuIRRun(w http.ResponseWriter, configPath, episode string) {
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"已有任务运行中，先停止"}`, http.StatusConflict)
		return
	}
	projName := filepath.Base(filepath.Dir(configPath))
	manjuState.running = true
	manjuState.stage = "ir_expand"
	manjuState.log = ""
	manjuState.rc = nil
	manjuState.done = false
	manjuState.started = time.Now()
	manjuState.baseElapsed = 0
	manjuState.stopped = false
	manjuState.project = projName
	manjuState.episode = episode
	manjuState.mu.Unlock()
	writeManjuDiskState(projName, &manjuDiskState{Running: true, Stage: "ir_expand", StartedAt: time.Now().Unix(), Episode: episode})
	_ = os.WriteFile(manjuRunLogPath(projName), nil, 0644)

	safeGo("ir-expand", nil, func() {
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
		plan, _, perr := ctx.loadPlan()
		if perr != nil {
			lg.logf("❌ 方案读取失败: " + perr.Error())
			return
		}
		// 收集空镜 h3_prompt(无 <Picture> 引用)
		prompts := map[string]string{}
		shots := map[int]map[string]any{}
		for _, x := range anyArr(plan["shots"]) {
			if m, ok := x.(map[string]any); ok {
				id, _ := manjuToInt(m["shot_id"])
				hp := str(m["h3_prompt"])
				if id > 0 && hp != "" && !strings.Contains(hp, "<Picture") {
					prompts[strconv.Itoa(id)] = hp
					shots[id] = m
				}
			}
		}
		if len(prompts) == 0 {
			lg.logf("⏭️ 本集没有可扩写的空镜提示词(全为角色镜或提示词为空)")
			rc = 0
			return
		}
		if err := ctx.expandShotsWithIR(prompts, shots, lg); err != nil {
			lg.logf("⚠️ Context IR 扩写部分失败(已保留本地提示词): " + err.Error())
		}
		// 写回 plan
		if err := ctx.writePlan(plan); err != nil {
			lg.logf("❌ 写回 plan 失败: " + err.Error())
			return
		}
		lg.logf("🎉 Context IR 扩写完成")
		rc = 0
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// registerIRRoutes Context IR 扩写路由
func registerIRRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/manju/ir_expand", func(w http.ResponseWriter, r *http.Request) {
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
		if _, err := readManjuConfig(cp); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		manjuIRRun(w, cp, episode)
	})
}
