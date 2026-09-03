// Package util 跨模块共享的纯工具函数(2026-09-03 模块化拆包配套):
// internal/api → internal/manju / internal/comfy 拆分时,原 api 包内散落的
// 通用小函数集中于此(唯一定义);各业务包内保留同名非导出包装
// (func str(...) { return util.Str(...) }),包内上百处调用点零改动。
package util

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Str map 取字符串(类型不符返回空串)
func Str(v any) string {
	s, _ := v.(string)
	return s
}

// MustAtoi 宽松 atoi(解析失败返回 0)
func MustAtoi(s string) int {
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

// Itoa 整数转字符串
func Itoa(n int) string { return fmt.Sprintf("%d", n) }

// DirExists 目录存在
func DirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// FileExists 文件存在(非目录)
func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// NowUnix 当前 unix 秒
func NowUnix() int64 { return time.Now().Unix() }

// Truncate rune 级截断(超长加省略号)
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// CopyFile 复制文件(小文件全量读写的简单实现,资产图/音频均在此量级)
func CopyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// MaskKey API key 打码(头 4 + **** + 尾 4)
func MaskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// WriteJSON 统一 JSON 响应(全 API 禁缓存:WebView2 启发式缓存会把保存后的配置
// 显示回旧值,表现为"配置被清空/没保存上")
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteErr 统一错误响应
func WriteErr(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// NovelTitleSan 文件名非法字符(Windows:半角冒号触发 NTFS ADS 陷阱等)
var NovelTitleSan = regexp.MustCompile(`[\/:*?"<>|]`)

// ReChapter 章节文件名「第N章」提取
var ReChapter = regexp.MustCompile(`第\s*(\d+)\s*章`)

func ManjuPythonPath(root string) string {
	for _, rel := range []string{
		filepath.Join(".venv", "Scripts", "python.exe"),
		filepath.Join("python_embeded", "python.exe"),
	} {
		if p := filepath.Join(root, rel); FileExists(p) {
			return p
		}
	}
	return filepath.Join(root, ".venv", "Scripts", "python.exe")
}

func IsPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 黑窗防护:托盘 3s 轮询高频调用
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf("%d", pid))
}

// CopyTree 递归复制目录
func CopyTree(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return CopyFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := CopyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
