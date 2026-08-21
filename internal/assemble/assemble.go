// Package assemble 实现 FFmpeg 合成：把逐镜片段拼接成完整成片。
package assemble

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// hideWindow windowsgui 父进程 spawn 子进程若不隐藏会弹黑窗,统一加 HideWindow
func hideWindow(cmd *exec.Cmd) *exec.Cmd {
	if cmd != nil && cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	return cmd
}

// defaultFFmpeg 是 winget 安装的默认路径（LookPath 失败时的兜底）。
var defaultFFmpeg = `C:\Users\Administrator\AppData\Local\Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-9.0-full_build\bin\ffmpeg.exe`

// ffmpegPath 返回可用的 ffmpeg 路径。
func ffmpegPath() (string, error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	if _, err := os.Stat(defaultFFmpeg); err == nil {
		return defaultFFmpeg, nil
	}
	return "", fmt.Errorf("未找到 ffmpeg，请先安装或配置")
}

// Assemble 把镜头片段按顺序拼接成成片（重编码 + 响度归一 + faststart）。
func Assemble(clips []string, output string) error {
	if len(clips) == 0 {
		return fmt.Errorf("无镜头可合成")
	}
	ff, err := ffmpegPath()
	if err != nil {
		return err
	}

	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	for _, c := range clips {
		args = append(args, "-i", c)
	}

	videoArgs := []string{"-c:v", "libx264", "-crf", "18", "-pix_fmt", "yuv420p"}
	audioArgs := []string{"-c:a", "aac", "-b:a", "128k"}
	faststart := []string{"-movflags", "+faststart"}

	if len(clips) == 1 {
		// 单镜头：直接重编码 + 响度归一 + faststart。
		args = append(args,
			"-af", "aresample=48000,loudnorm=I=-16:TP=-1.5:LRA=11",
		)
		args = append(args, videoArgs...)
		args = append(args, audioArgs...)
		args = append(args, faststart...)
		args = append(args, output)
	} else {
		// 多镜头：concat filter + 响度归一。
		var fc strings.Builder
		for i := range clips {
			fc.WriteString(fmt.Sprintf("[%d:v][%d:a]", i, i))
		}
		fc.WriteString(fmt.Sprintf("concat=n=%d:v=1:a=1[v][a]", len(clips)))
		fc.WriteString(";[a]aresample=48000,loudnorm=I=-16:TP=-1.5:LRA=11[aout]")
		args = append(args, "-filter_complex", fc.String())
		args = append(args, "-map", "[v]", "-map", "[aout]")
		args = append(args, videoArgs...)
		args = append(args, audioArgs...)
		args = append(args, faststart...)
		args = append(args, output)
	}

	out, err := hideWindow(exec.Command(ff, args...)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 合成失败: %w（%s）", err, truncate(string(out), 300))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
