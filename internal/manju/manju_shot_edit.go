package manju

// 分镜单镜编辑 API(2026-09-03 视频管理弹窗):
//   POST /api/manju/shot/edit {config, episode, shot, fields:{...}, h3_prompt?}
// 字段白名单(scene/shot_size/camera/action/dialogue/narration/duration/characters)写回
// 该集 plan 的对应镜头;台词变化时把旧台词逐句确定性替换进 h3_prompt(无 LLM),
// 提示词/时长/场景/角色变化经 finalizeAlignedPrompt 纳入渲染指纹→该镜自动 stale 重渲。
// 显式传 h3_prompt(非空)=用户手改提示词,跳过自动同步直接落盘。

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// manjuShotEditFields 可编辑字段白名单(空指针=不改;值空串=清空该字段)
type manjuShotEditFields struct {
	Scene     *string   `json:"scene"`
	ShotSize  *string   `json:"shot_size"`
	Camera    *string   `json:"camera"`
	Action    *string   `json:"action"`
	Dialogue  *string   `json:"dialogue"`
	Narration *string   `json:"narration"`
	Duration  *int      `json:"duration"`
	Characters *[]string `json:"characters"`
	H3Prompt  *string   `json:"h3_prompt"` // 显式全文(手改提示词)
}

// syncDialogueIntoPrompt 台词逐句确定性同步:dialogue 为"角色:台词"多行,
// h3_prompt 的 Audio 行内嵌 `<d>[Chinese]台词</d>`(技能侧产出)。逐行取说词,
// 在提示词中全文替换(旧→新);找不到旧句(已被对齐层改写)跳过并计数。
// 返回 (新提示词, 成功句数, 需同步句数)。
func syncDialogueIntoPrompt(hp, oldDlg, newDlg string) (string, int, int) {
	sayOf := func(line string) string {
		line = strings.TrimSpace(line)
		for _, sep := range []string{":", ":"} {
			if i := strings.Index(line, sep); i >= 0 {
				return strings.TrimSpace(line[i+len(sep):])
			}
		}
		return line
	}
	oldSays := []string{}
	for _, l := range strings.Split(oldDlg, "\n") {
		if s := sayOf(l); s != "" {
			oldSays = append(oldSays, s)
		}
	}
	newSays := []string{}
	for _, l := range strings.Split(newDlg, "\n") {
		if s := sayOf(l); s != "" {
			newSays = append(newSays, s)
		}
	}
	if len(oldSays) == 0 || len(oldSays) != len(newSays) {
		// 句数不匹配(增删台词)无法确定性逐句替换——交给用户手改提示词
		return hp, 0, len(oldSays)
	}
	done := 0
	for i := range oldSays {
		if oldSays[i] == newSays[i] {
			done++
			continue
		}
		if strings.Contains(hp, oldSays[i]) {
			hp = strings.ReplaceAll(hp, oldSays[i], newSays[i])
			done++
		}
	}
	return hp, done, len(oldSays)
}

// manjuShotEditHandler POST 单镜编辑
func manjuShotEditHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config  string               `json:"config"`
		Episode string               `json:"episode"`
		Shot    int                  `json:"shot"`
		Fields  manjuShotEditFields  `json:"fields"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	cp, gerr := manjuGuardConfig(body.Config)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	body.Config = cp
	if body.Episode == "" || body.Shot <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "episode/shot 必填"})
		return
	}
	cfg, err := readManjuConfig(body.Config)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	planPath := manjuFindPlanDir(str(P["analysis"]), body.Episode)
	if planPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "该集方案不存在: " + body.Episode})
		return
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var plan map[string]any
	if err := json.Unmarshal(data, &plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "方案解析失败"})
		return
	}
	arr, _ := plan["shots"].([]any)
	var target map[string]any
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			if id, ok2 := manjuToInt(m["shot_id"]); ok2 && id == body.Shot {
				target = m
				break
			}
		}
	}
	if target == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": strconv.Itoa(body.Shot) + " 号镜头不存在"})
		return
	}
	f := body.Fields
	updated := []string{}
	if f.Scene != nil {
		target["scene"] = *f.Scene
		updated = append(updated, "scene")
	}
	if f.ShotSize != nil {
		target["shot_size"] = *f.ShotSize
		updated = append(updated, "shot_size")
	}
	if f.Camera != nil {
		target["camera"] = *f.Camera
		updated = append(updated, "camera")
	}
	if f.Action != nil {
		target["action"] = *f.Action
		updated = append(updated, "action")
	}
	oldDlg := str(target["dialogue"])
	if f.Dialogue != nil {
		target["dialogue"] = *f.Dialogue
		updated = append(updated, "dialogue")
	}
	if f.Narration != nil {
		target["narration"] = *f.Narration
		updated = append(updated, "narration")
	}
	if f.Duration != nil {
		if *f.Duration < 1 || *f.Duration > 60 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "时长需 1~60 秒"})
			return
		}
		target["duration"] = *f.Duration
		updated = append(updated, "duration")
	}
	if f.Characters != nil {
		target["characters"] = toAnySlice(*f.Characters)
		updated = append(updated, "characters")
	}
	// 提示词:显式手改 > 台词逐句自动同步 > 不动
	promptSynced, promptSyncMiss := 0, 0
	if f.H3Prompt != nil && strings.TrimSpace(*f.H3Prompt) != "" {
		target["h3_prompt"] = *f.H3Prompt
		updated = append(updated, "h3_prompt")
	} else if f.Dialogue != nil && *f.Dialogue != oldDlg {
		hp, done, total := syncDialogueIntoPrompt(str(target["h3_prompt"]), oldDlg, *f.Dialogue)
		if done > 0 {
			target["h3_prompt"] = hp
			updated = append(updated, "h3_prompt(台词同步)")
		}
		promptSynced, promptSyncMiss = done, total-done
	}
	if len(updated) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无可更新字段"})
		return
	}
	if err := atomicWriteJSON(planPath, plan); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "方案写回失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "updated": updated,
		"prompt_synced": promptSynced, "prompt_sync_miss": promptSyncMiss,
		"note": "时长/场景/角色/提示词变化将纳入渲染指纹,该镜下次渲染自动重出",
	})
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
