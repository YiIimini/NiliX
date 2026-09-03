package api

// 2026-09-03 QC 幽灵人声检测(修仙界EP01 镜1 实锤;同日 ASR 升级):无台词镜(无对白/
// 无旁白且 h3 无 <d> 台词标签)的渲染产物检出人声 → 写回 QC 报告 ok=false,走既有
// 自动删旧重渲链(与视觉抽检同机制)。诱因:H3 对无台词镜会自发生成画外人声
// (镜1 实证:whisper 转写出 0-4.32s 连续中文人声,avg_logprob -0.56,渲染输入
// 无任何台词)。
// 检测(2026-09-03 二轮升级):初版声学双特征(语音节奏+带通节奏保留)对「人声+
// 连续环境音叠加」失效——钢琴/风扇谐波连续填充 200-3400Hz 人声频段,带通后零
// 静音段,镜1 漏检放行。升级为 faster-whisper ASR 主判(vad_filter=True 先切语音
// 段防音乐幻觉 + avg_logprob>-1.0 置信度阈值 + 有效文本≥2 字):批量一次模型
// 加载逐镜转写。ASR 不可用(python/模型缺失)退回旧声学双特征(保守兜底)。
// 工具缺失/执行失败一律静默跳过,不阻塞管线。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	reSilenceStart = regexp.MustCompile(`silence_start:\s*([0-9.]+)`)
	reSilenceEnd   = regexp.MustCompile(`silence_end:\s*([0-9.]+)`)
)

// ghostVoiceEligible 该镜是否参与检测:无对白(空或「无」占位)、无旁白、h3 无真实
// <d> 台词(AUDIO & LIP DISCIPLINE 纪律段引用的 "<d> tags" 字样剔除后再判)。
func ghostVoiceEligible(dialogue, narration, h3 string) bool {
	d := strings.TrimSpace(dialogue)
	if d != "" && d != "无" {
		return false
	}
	if strings.TrimSpace(narration) != "" {
		return false
	}
	if i := strings.Index(h3, "AUDIO & LIP DISCIPLINE"); i >= 0 {
		h3 = h3[:i]
	}
	return !strings.Contains(h3, "<d>")
}

// ffVoiceSegments 特征①:统计 0.15~2.5s 的独立发声段数(被 ≥0.25s 静音分隔)
func ffVoiceSegments(ff, mp4 string, dur float64) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-i", mp4,
		"-af", "silencedetect=noise=-35dB:d=0.25", "-f", "null", "-")
	var out bytes.Buffer
	cmd.Stderr = &out
	if cmd.Run() != nil {
		return 0
	}
	return countVoiceSegments(out.String(), dur)
}

// shotHasGhostVoice 双特征判定;返回(命中, 描述)
// 特征②采用「带通后节奏保留」而非频段能量比:钢琴中音/合成器泛音同样落在
// 300-3400Hz,能量占比会把配乐误判(修仙界镜1 实测占比仅 14.5% 但人声确凿);
// 带通滤掉低频嗡鸣后若语音节奏仍保留 ≥2 段,说明中频确有结构化发声(人声)。
func shotHasGhostVoice(ff, mp4 string, durSec float64) (bool, string) {
	if durSec <= 0 {
		durSec = 5
	}
	segs := ffVoiceSegments(ff, mp4, durSec)
	if segs < 2 {
		return false, ""
	}
	bandSegs := ffVoiceSegmentsFiltered(ff, mp4, durSec, "highpass=f=200,lowpass=f=3400")
	if bandSegs < 2 {
		return false, ""
	}
	return true, fmt.Sprintf("检出疑似人声(%d 段语音节奏,滤除低频后仍保留 %d 段)", segs, bandSegs)
}

// ffVoiceSegmentsFiltered 带滤波版语音节奏统计(特征②)
func ffVoiceSegmentsFiltered(ff, mp4 string, dur float64, filter string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-i", mp4,
		"-af", filter+",silencedetect=noise=-35dB:d=0.25", "-f", "null", "-")
	var out bytes.Buffer
	cmd.Stderr = &out
	if cmd.Run() != nil {
		return 0
	}
	return countVoiceSegments(out.String(), dur)
}

// countVoiceSegments 从 silencedetect 输出统计 0.15~2.5s 独立发声段
func countVoiceSegments(stderrText string, dur float64) int {
	type span struct{ s, e float64 }
	var silences []span
	cur := -1.0
	for _, line := range strings.Split(stderrText, "\n") {
		if m := reSilenceStart.FindStringSubmatch(line); m != nil {
			cur, _ = strconv.ParseFloat(m[1], 64)
		} else if m := reSilenceEnd.FindStringSubmatch(line); m != nil && cur >= 0 {
			v, _ := strconv.ParseFloat(m[1], 64)
			silences = append(silences, span{cur, v})
			cur = -1
		}
	}
	voices, pos := 0, 0.0
	for _, s := range silences {
		if s.s > pos {
			if l := s.s - pos; l >= 0.15 && l <= 2.5 {
				voices++
			}
		}
		if s.e > pos {
			pos = s.e
		}
	}
	if dur > pos {
		if l := dur - pos; l >= 0.15 && l <= 2.5 {
			voices++
		}
	}
	return voices
}

// ghostCand 待检镜头(文件路径+时长)
type ghostCand struct {
	id   int
	path string
	dur  float64
}

// asrGhostPy 批量 ASR 转写脚本(stdin=mp4 绝对路径逐行,stdout=@@GHOST@@JSON):
// vad_filter 先切语音段(纯音乐多数被 VAD 滤除),逐段带起止/置信度/文本返回。
const asrGhostPy = `import sys, json
from faster_whisper import WhisperModel
m = WhisperModel("small", device="cpu", compute_type="int8")
res = {}
for line in sys.stdin:
    p = line.strip()
    if not p:
        continue
    try:
        segs, _ = m.transcribe(p, language="zh", vad_filter=True)
        res[p] = [{"s": round(x.start, 2), "e": round(x.end, 2), "lp": round(x.avg_logprob, 2), "t": x.text.strip()} for x in segs]
    except Exception as ex:
        res[p] = {"error": str(ex)}
print("@@GHOST@@" + json.dumps(res, ensure_ascii=False))`

// asrGhostVoiceBatch 批量 ASR 幽灵人声检测:返回 path→命中描述;nil=ASR 不可用。
// 判定:存在段 时长≥0.5s 且 avg_logprob>-1.0(高置信,滤音乐幻觉)且文本≥2 字。
// 一次进程批量转写(模型加载一次);超时 10 分钟按一集全部无台词镜估。
func (ctx *manjuCtx) asrGhostVoiceBatch(cands []ghostCand) map[string]string {
	py := manjuPythonPath()
	if _, err := os.Stat(py); err != nil {
		return nil
	}
	cmd := exec.Command(py, "-X", "utf8", "-c", asrGhostPy)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		return nil
	}
	for _, c := range cands {
		_, _ = io.WriteString(stdin, c.path+"\n")
	}
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		_ = cmd.Process.Kill()
		return nil
	}
	var payload string
	for _, line := range strings.Split(out.String(), "\n") {
		if i := strings.Index(line, "@@GHOST@@"); i >= 0 {
			payload = line[i+len("@@GHOST@@"):]
		}
	}
	if payload == "" {
		return nil // faster-whisper 缺失等:stderr 有原因,不阻塞管线
	}
	var raw map[string]any
	if json.Unmarshal([]byte(payload), &raw) != nil {
		return nil
	}
	hits := map[string]string{}
	for _, c := range cands {
		arr, ok := raw[c.path].([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			seg, ok := it.(map[string]any)
			if !ok {
				continue
			}
			lp, _ := seg["lp"].(float64)
			txt := strings.TrimSpace(str(seg["t"]))
			s, _ := seg["s"].(float64)
			e, _ := seg["e"].(float64)
			runeLen := len([]rune(txt))
			if e-s >= 0.5 && lp > -1.0 && runeLen >= 2 {
				clip := txt
				if runeLen > 18 {
					clip = string([]rune(txt)[:18]) + "…"
				}
				hits[c.path] = fmt.Sprintf("ASR 检出人声(%.1fs, 置信 %.2f: 「%s」)", e-s, lp, clip)
				break
			}
		}
	}
	return hits
}

// qcGhostVoiceCheck 无台词镜幽灵人声检测,命中写回 QC 报告(appendQCFlag,与
// 视觉抽检同款机制:追加 flag 且 ok=false → 渲染阶段自动删旧重渲)。
func (ctx *manjuCtx) qcGhostVoiceCheck(lg *manjuLogger, reportPath string, failed map[int]bool) {
	_, shots, err := ctx.loadPlan()
	if err != nil {
		return
	}
	eligible := map[int]float64{}
	for _, s := range shots {
		if ghostVoiceEligible(s.Dialogue, s.Narration, s.H3Prompt) {
			eligible[s.ID] = float64(s.Duration)
		}
	}
	if len(eligible) == 0 {
		return
	}
	clipsEp := filepath.Join(ctx.clipsDir, ctx.episode)
	entries, err := os.ReadDir(clipsEp)
	if err != nil {
		return
	}
	var cands []ghostCand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".mp4"))
		if err != nil || failed[id] {
			continue
		}
		if dur, ok := eligible[id]; ok {
			cands = append(cands, ghostCand{id: id, path: filepath.Join(clipsEp, e.Name()), dur: dur})
		}
	}
	if len(cands) == 0 {
		return
	}
	// ASR 主判(2026-09-03):声学双特征对「人声+连续环境音」实测漏检(镜1 带通后
	// 零静音段放行),whisper VAD+置信度双阈值稳定检出。
	hits := ctx.asrGhostVoiceBatch(cands)
	total, hit := len(cands), 0
	if hits == nil {
		// ASR 不可用:退回旧声学双特征(保守兜底,漏检风险自担)
		ff := manjuFFmpegPath()
		if ff == "" {
			return
		}
		for _, c := range cands {
			if bad, why := shotHasGhostVoice(ff, c.path, c.dur); bad {
				lg.logf(fmt.Sprintf("  👻 幽灵人声 镜 %d: %s(将触发重渲)", c.id, why))
				appendQCFlag(reportPath, fmt.Sprintf("%d.mp4", c.id), "幽灵人声:"+why)
				hit++
			}
		}
	} else {
		for _, c := range cands {
			if why, ok := hits[c.path]; ok {
				lg.logf(fmt.Sprintf("  👻 幽灵人声 镜 %d: %s(将触发重渲)", c.id, why))
				appendQCFlag(reportPath, fmt.Sprintf("%d.mp4", c.id), "幽灵人声:"+why)
				hit++
			}
		}
	}
	if total > 0 {
		lg.logf(fmt.Sprintf("  👻 幽灵人声检测 %d 镜(无台词镜, ASR),命中 %d 镜", total, hit))
	}
}

// appendQCFlag 在 QC 报告给镜头追加 flag 并置 ok=false(泛化自视觉抽检)
func appendQCFlag(reportPath, name, issue string) {
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
		sh["flags"] = append(flags, issue)
	} else {
		sh["flags"] = []any{issue}
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile(reportPath, out, 0o644)
}
