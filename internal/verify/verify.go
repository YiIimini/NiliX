// Package verify 实现成片自动质检（时长/音视频流/faststart）。
package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

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
func Verify(path string) (*Report, error) {
	ff, err := ffprobePath()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(ff, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-show_entries", "format=duration",
		"-of", "json", path).Output()
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
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	moov := bytes.Index(data, []byte("moov"))
	mdat := bytes.Index(data, []byte("mdat"))
	return moov >= 0 && (mdat < 0 || moov < mdat)
}

func boolMsg(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
