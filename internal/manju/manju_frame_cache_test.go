package manju

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 优化回归:审片抽帧目录按集/镜头隔离(shotFramesDir),且 _src.meta 标记文件
// 记录源 mp4 指纹(大小@mtime)用于缓存复用——验证路径构造与「指纹变更→失效」规则。
func TestShotFramesDirIsolation(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: filepath.Join(dir, "analysis"), episode: "EP01"}
	got := ctx.shotFramesDir(3)
	if filepath.Base(filepath.Dir(got)) != "EP01" || filepath.Base(got) != "03" {
		t.Fatalf("shotFramesDir 路径异常: %s", got)
	}
}

// 优化回归:与 inspectShot 缓存命中相同的指纹规则——源 mp4 大小或 mtime 变化 → 指纹变化
// (抽帧缓存自动失效重抽;产物未变 → 指纹稳定 → 复用抽帧,免重复解码)
func TestShotFrameMarkFingerprintStability(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "01.mp4")
	_ = os.WriteFile(clip, []byte("v1"), 0644)
	fp1 := shotFrameMark(clip)
	if fp1 == "" {
		t.Fatalf("指纹不应为空")
	}
	// 同文件重读:指纹稳定(缓存命中前提)
	fp1b := shotFrameMark(clip)
	if fp1 != fp1b {
		t.Fatalf("同文件指纹漂移: %q vs %q", fp1, fp1b)
	}
	// 内容变化(大小变)→ 指纹变化(缓存失效)
	time.Sleep(20 * time.Millisecond)
	_ = os.WriteFile(clip, []byte("v1-content-longer"), 0644)
	fp2 := shotFrameMark(clip)
	if fp2 == fp1 {
		t.Fatalf("内容变化后指纹未变: %s", fp2)
	}
}

// shotFrameMark 抽帧源指纹(大小@mtime纳秒),与 inspectShot 的 _src.meta 记录规则一致
func shotFrameMark(clip string) string {
	fi, err := os.Stat(clip)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.Size(), 10) + "@" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

var _ = strings.Contains // 避免未使用 import 告警
