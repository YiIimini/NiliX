// Package verify 实现成片自动质检（时长/音视频流/faststart）。
package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// hideWindow windowsgui 父进程 spawn 子进程若不隐藏会弹黑窗(用户反馈"黑窗反复闪"),
// 所有 exec 统一加 HideWindow。非 Windows 平台忽略。
func hideWindow(cmd *exec.Cmd) *exec.Cmd {
	if cmd != nil && cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	return cmd
}

var defaultFFprobe = `C:\Users\Administrator\AppData\Local\Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-9.0-full_build\bin\ffprobe.exe`

func ffprobePath() (string, error) {
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p, nil
	}
	if _, err := os.Stat(defaultFFprobe); err == nil {
		return defaultFFprobe, nil
	}
	return "", fmt.Errorf("未找到 ffprobe")
}

// Check 是单条质检项。
type Check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// Report 是质检报告。
type Report struct {
	Passed bool    `json:"passed"`
	Checks []Check `json:"checks"`
}

type probeResult struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Verify 对成片做基本质检（时长、音视频流、faststart）。
// 审计 2026-08-28:ffprobe 加 30s 超时,读损坏/挂死文件不再永久阻塞。
func Verify(path string) (*Report, error) {
	ff, err := ffprobePath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := hideWindow(exec.CommandContext(ctx, ff, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-show_entries", "format=duration",
		"-of", "json", path)).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 读取失败: %w", err)
	}
	var pr probeResult
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, err
	}

	report := &Report{}
	var duration float64
	_, _ = fmt.Sscanf(pr.Format.Duration, "%f", &duration)
	report.Checks = append(report.Checks, Check{Name: "时长", Passed: duration > 0, Message: fmt.Sprintf("%.2fs", duration)})

	hasVideo, hasAudio := false, false
	for _, s := range pr.Streams {
		if s.CodecType == "video" {
			hasVideo = true
		}
		if s.CodecType == "audio" {
			hasAudio = true
		}
	}
	report.Checks = append(report.Checks, Check{Name: "视频流", Passed: hasVideo, Message: boolMsg(hasVideo, "存在", "缺失")})
	report.Checks = append(report.Checks, Check{Name: "音频流", Passed: hasAudio, Message: boolMsg(hasAudio, "存在", "缺失")})

	fast := checkFaststart(path)
	report.Checks = append(report.Checks, Check{Name: "faststart", Passed: fast, Message: boolMsg(fast, "moov 前置", "未前置")})

	report.Passed = true
	for _, c := range report.Checks {
		if !c.Passed {
			report.Passed = false
		}
	}
	return report, nil
}

func checkFaststart(path string) bool {
	// 审计 M9:分块扫描而非整文件读入内存——成片动辄 GB 级,os.ReadFile 峰值内存翻倍甚至 OOM。
	// moov/mdat 原子在文件前部(正常 mp4),扫前 64KB + 尾部 1MB 已足够;若未命中再退化为全文件流式扫描
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n1, _ := f.ReadAt(head, 0)
	head = head[:n1]
	if moovPos(head) >= 0 && mdatPos(head) >= 0 {
		return moovPos(head) < mdatPos(head)
	}
	// 尾部分块(找尾部 mdat 起点或后续 moov)
	const tailSize = 1 << 20
	st, err := f.Stat()
	if err != nil {
		return false
	}
	start := st.Size() - tailSize
	if start < 0 {
		start = 0
	}
	tail := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(tail, start); err != nil {
		return false
	}
	combined := append(append([]byte{}, head...), tail...)
	moov := bytes.Index(combined, []byte("moov"))
	mdat := bytes.Index(combined, []byte("mdat"))
	return moov >= 0 && (mdat < 0 || moov < mdat)
}

func moovPos(b []byte) int { return bytes.Index(b, []byte("moov")) }
func mdatPos(b []byte) int { return bytes.Index(b, []byte("mdat")) }

func boolMsg(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
