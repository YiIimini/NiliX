package api

// 渲染中间产物 API(2026-09-03 画布三期):镜头调试面板直查该镜中间态——
//   GET /api/manju/shot/intermediates?config=&episode=&shot=N
//     → 条件缓存(.pt 级别判定)/ 接缝 latent(上一镜+本镜)/ 成片信息 / 建议抽帧点
//   GET /api/manju/shot/frame?config=&episode=&shot=N&t=秒
//     → ffmpeg 抽帧 JPEG(磁盘缓存 .frames/,mp4 更新自动重抽)
// 排查渲染问题不再翻目录:缓存是纯文本还是含参考图编码、接缝 latent 是否在、
// 成片哪个时间点出问题,面板内一屏可见。

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// manjuIntermediateFile 中间产物文件态
type manjuIntermediateFile struct {
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
	Mtime  string `json:"mtime,omitempty"`
	Path   string `json:"path,omitempty"` // 相对展示(不含盘符,便于复现)
}

// manjuShotIntermediatesResp 中间产物响应
type manjuShotIntermediatesResp struct {
	ShotID  int    `json:"shot_id"`
	Episode string `json:"episode"`
	Cache   struct {
		Name  string                `json:"name"`
		Level string                `json:"level"` // rich=含参考图编码 / text=纯文本(63KB 级)
		Info  manjuIntermediateFile `json:"info"`
	} `json:"cache"`
	Latent struct {
		NS      string                `json:"ns"`
		Prev    manjuIntermediateFile `json:"prev"` // 上一镜 clip_(N-1):本镜能否接缝
		Current manjuIntermediateFile `json:"current"` // 本镜 clip_N:已渲染供下一镜续接
	} `json:"latent"`
	Clip manjuIntermediateFile `json:"clip"`
	Frames []float64           `json:"frames"` // 建议抽帧时间点(秒)
}

func fileIntermediateInfo(p string) manjuIntermediateFile {
	st, err := os.Stat(p)
	if err != nil {
		return manjuIntermediateFile{Exists: false}
	}
	return manjuIntermediateFile{Exists: true, Size: st.Size(),
		Mtime: st.ModTime().Format("01-02 15:04"), Path: filepath.ToSlash(p)}
}

// manjuShotIntermediatesHandler GET 中间产物
func manjuShotIntermediatesHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	episode := strings.TrimSpace(r.URL.Query().Get("episode"))
	shotN, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("shot")))
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if episode == "" {
		episode = ctx.episode
	}
	plan, _, err := ctx.loadPlan()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "方案未生成: " + err.Error()})
		return
	}
	shots, _ := planShots(plan)
	var target *manjuShot
	for i := range shots {
		if shots[i].ID == shotN {
			target = &shots[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("镜头 %d 不存在", shotN)})
		return
	}
	resp := manjuShotIntermediatesResp{ShotID: shotN, Episode: episode}
	// 条件缓存:级别按体积判定(纯文本≈63KB/3 tokens,含参考图编码≈21MB/5120 tokens)
	cacheName := ctx.shotCacheNameAt(*target, ctx.w, ctx.h)
	resp.Cache.Name = cacheName
	ci := fileIntermediateInfo(h3CachePath(ctx.sharedModels, cacheName))
	resp.Cache.Info = ci
	if ci.Exists {
		resp.Cache.Level = "text"
		if ci.Size > 1024*1024 {
			resp.Cache.Level = "rich"
		}
	}
	// 接缝 latent(命名空间同渲染:h3_context/<项目_集>/clip_%05d)
	ns := ctx.latentNS()
	resp.Latent.NS = ns
	if shotN > 1 {
		resp.Latent.Prev = fileIntermediateInfo(h3ContextLatentPath(ctx.comfyOutput, ns, shotN-1))
	}
	resp.Latent.Current = fileIntermediateInfo(h3ContextLatentPath(ctx.comfyOutput, ns, shotN))
	// 成片
	resp.Clip = fileIntermediateInfo(filepath.Join(ctx.clipsDir, episode, fmt.Sprintf("%02d.mp4", shotN)))
	// 建议抽帧点:首尾+均匀中间(相对 duration)
	dur := target.Duration
	if dur <= 0 {
		dur = 5
	}
	for _, ratio := range []float64{0.03, 0.2, 0.4, 0.6, 0.8, 0.97} {
		t := float64(dur) * ratio
		t = float64(int(t*100)) / 100
		resp.Frames = append(resp.Frames, t)
	}
	writeJSON(w, http.StatusOK, resp)
}

// manjuFFmpegBinFallback winget 安装路径兜底(与 internal/assemble defaultFFmpeg 同源)
const manjuFFmpegBinFallback = `C:\Users\Administrator\AppData\Local\Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-9.0-full_build\bin\ffmpeg.exe`

// manjuFFmpegPath ffmpeg 查找:ComfyUI venv(与渲染同源)→ winget 默认 → PATH
func manjuFFmpegPath() string {
	if ff := filepath.Join(ComfyRootDir, ".venv", "Scripts", "ffmpeg.exe"); fileExists(ff) {
		return ff
	}
	if fileExists(manjuFFmpegBinFallback) {
		return manjuFFmpegBinFallback
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// manjuShotFrameHandler GET 抽帧(磁盘缓存 .frames/,mp4 更新自动重抽)
func manjuShotFrameHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	episode := strings.TrimSpace(r.URL.Query().Get("episode"))
	shotN, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("shot")))
	tSec, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("t")), 64)
	idx := strings.TrimSpace(r.URL.Query().Get("i")) // 帧序(缓存名用,可空)
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if episode == "" {
		episode = ctx.episode
	}
	if idx == "" {
		idx = fmt.Sprintf("%.2f", tSec)
	}
	mp4 := filepath.Join(ctx.clipsDir, episode, fmt.Sprintf("%02d.mp4", shotN))
	st, err := os.Stat(mp4)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "镜头视频未渲染"})
		return
	}
	// 缓存命中:jpg 存在且新于 mp4
	cacheDir := filepath.Join(ctx.clipsDir, episode, ".frames")
	jpg := filepath.Join(cacheDir, fmt.Sprintf("%02d_f%s.jpg", shotN, idx))
	if jst, e := os.Stat(jpg); e == nil && jst.ModTime().After(st.ModTime()) {
		b, e := os.ReadFile(jpg)
		if e == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(b)
			return
		}
	}
	ff := manjuFFmpegPath()
	if ff == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "ffmpeg 未找到(ComfyUI venv / PATH)"})
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-ss", fmt.Sprintf("%.2f", tSec),
		"-i", mp4, "-frames:v", "1", "-q:v", "3", "-f", "image2", "-"}
	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, ff, args...)
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false,
			"error": fmt.Sprintf("抽帧失败 t=%.2fs: %v", tSec, err)})
		return
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.WriteFile(jpg, out.Bytes(), 0o644)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(out.Bytes())
}
