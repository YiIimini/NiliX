package manju

// 2026-08-26 升级:QC 视觉抽检——对质检(音轨/亮度/冻结/OCR)通过的镜头抽 1 帧,
// 用本地视觉模型判断画面崩坏/花屏/畸形/五官错乱等硬伤,命中写回 QC 报告
// (追加 flag 且 ok=false,触发渲染阶段自动删旧重渲,与现有 QC 行为一致)。
// 未配置视觉模型(agent.vision_model / GLM_VISION_API_KEY)时自动跳过,不阻塞管线。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// qcVisionEnabled QC 视觉抽检开关(render.qc_vision,缺省开启)。
func qcVisionEnabled(R map[string]any) bool {
	if v, ok := R["qc_vision"].(bool); ok {
		return v
	}
	return true
}

const qcVisionSystem = `你是视频质检员。判断这一帧视频画面是否有明显质量问题。
质量硬伤包括:画面崩坏/扭曲/花屏/色块、五官错乱或畸形、多余或残缺肢体、明显伪影与撕裂、全黑或全白画面、大面积噪点。
轻微瑕疵(轻微模糊/轻微噪点/构图问题)不算质量问题。
只输出 JSON:{"ok": true 或 false, "issue": "问题描述(ok=true 时为空字符串)"}`

const qcVisionUser = `请判断这张视频帧是否有画面质量问题。`

// qcVisualCheck 对质检通过的镜头做视觉抽检,命中写回 QC 报告。
// failed 为已判定失败的镜头集合(跳过不抽)。无视觉模型/抽帧失败/视觉调用失败均静默跳过。
func (ctx *manjuCtx) qcVisualCheck(lg *manjuLogger, reportPath string, failed map[int]bool) {
	if !qcVisionEnabled(ctx.R) {
		return
	}
	acfg := loadAgentCfg(ctx)
	vc := ctx.visionClientShared(acfg)
	if vc == nil {
		lg.logf("  ⏭️ 未配置视觉模型(agent.vision_model 或 GLM_VISION_API_KEY),跳过 QC 视觉抽检")
		return
	}
	clipsDir := filepath.Join(ctx.clipsDir, ctx.episode)
	clips, err := os.ReadDir(clipsDir)
	if err != nil {
		return
	}
	tmpDir, err := os.MkdirTemp("", "qcvision")
	if err != nil {
		return
	}
	defer os.RemoveAll(tmpDir)

	total, hit := 0, 0
	for _, e := range clips {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".mp4"))
		if err != nil || failed[id] {
			continue
		}
		total++
		out, err := ctx.runMediaOut("frames", "--video", filepath.Join(clipsDir, e.Name()), "--count", "1", "--out-dir", tmpDir)
		if err != nil {
			continue
		}
		var fr struct {
			Frames []string `json:"frames"`
		}
		for _, line := range strings.Split(out, "\n") {
			if json.Unmarshal([]byte(line), &fr) == nil && len(fr.Frames) > 0 {
				break
			}
		}
		if len(fr.Frames) == 0 {
			continue
		}
		res, err := vc.ChatJSON(qcVisionSystem, qcVisionUser, fr.Frames, 0.2)
		if err != nil {
			continue
		}
		if ok, _ := res["ok"].(bool); !ok {
			issue := strings.TrimSpace(str(res["issue"]))
			if issue == "" {
				issue = "视觉抽检未通过"
			}
			lg.logf(fmt.Sprintf("  👁️ 视觉抽检 镜 %d: %s(将触发重渲)", id, issue))
			appendQCVisualFlag(reportPath, e.Name(), issue)
			hit++
		}
	}
	if total > 0 {
		lg.logf(fmt.Sprintf("  👁️ QC 视觉抽检 %d 镜,命中 %d 镜", total, hit))
	}
}

// appendQCVisualFlag 在 QC 报告里给指定镜头追加视觉 flag 并置 ok=false(保留其余字段)。
func appendQCVisualFlag(reportPath, name, issue string) {
	b, err := os.ReadFile(reportPath)
	if err != nil {
		return
	}
	var rep map[string]any
	if json.Unmarshal(b, &rep) != nil {
		return
	}
	shots, ok := rep["shots"].(map[string]any)
	if !ok {
		shots = map[string]any{}
		rep["shots"] = shots
	}
	sh, ok := shots[name].(map[string]any)
	if !ok {
		sh = map[string]any{}
		shots[name] = sh
	}
	sh["ok"] = false
	if flags, ok := sh["flags"].([]any); ok {
		sh["flags"] = append(flags, "视觉:"+issue)
	} else {
		sh["flags"] = []any{"视觉:" + issue}
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile(reportPath, out, 0644)
}
