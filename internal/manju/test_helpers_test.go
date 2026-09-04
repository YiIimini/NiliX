package manju

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nilix/internal/paths"
)


func doReq(t *testing.T, method, url string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	apiMux().ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w, out
}

func verifyConfig(t *testing.T, llmKey string) (dir, cfgPath string) {
	t.Helper()
	// 建在 paths.ManjuRootDir 之下:审计 S1 的 config 白名单校验要求 config 归属项目根目录
	dir = filepath.Join(paths.ManjuRootDir, fmt.Sprintf("t-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfgPath = filepath.Join(dir, "config.json")
	novel := filepath.Join(dir, "novel.md")
	var novelContent string
	if llmKey != "" {
		// 深度分析用:≥200 字且带章节结构
		var b strings.Builder
		b.WriteString("# 第1章 开局\n")
		for i := 0; i < 40; i++ {
			b.WriteString("夜色下的古城灯火通明,主角穿过长街,衣袂随风而动,剑穗轻摇。\n")
		}
		novelContent = b.String()
	}
	if err := os.WriteFile(novel, []byte(novelContent), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"style": "",
		"llm": map[string]any{
			"api_key": llmKey, "base_url": "http://127.0.0.1:1", "model": "deepseek-chat",
		},
		"render": map[string]any{
			"steps": 30, "turbo_steps": 10, "turbo_lora": "turbo-lora.safetensors",
			"seed": 0, "fps": 100, "min_shot_seconds": 15, "max_shot_seconds": 4,
			"width": 768, "height": 1344, "comfy_url": "http://127.0.0.1:1",
		},
		"paths": map[string]any{
			"novel": novel, "workdir": dir,
			"comfy_input": filepath.Join(dir, "in"), "comfy_output": filepath.Join(dir, "out"),
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, b, 0644); err != nil {
		t.Fatal(err)
	}
	return dir, cfgPath
}

// apiMux 复用真实路由注册(agent 端点全部经此暴露)
func apiMux() *http.ServeMux {
	m := http.NewServeMux()
	RegisterRoutes(m)
	return m
}

func TestAgentNormalizeStyle(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"2.5D + 水墨, Cyberpunk", "2.5d+ink+Cyberpunk"},
		{"写实, 赛博朋克, 3D", "real+cyberpunk+3d"},
		{"2.5d+2.5d+写实", "2.5d+real"},
		{"水墨画、像素风、油画", "ink+pixel art+oil painting"},
		{"真实系图片", "真实系图片"}, // 中文自定义词 → 原样保留(2026-08-19 起不再忽略)
		{"anime + watercolor", "anime+watercolor"},
		{"手绘, 纸片拼贴, 粘土", "handdrawn+papercraft+clay"},
	}
	for _, c := range cases {
		got, _ := manjuNormalizeStyle(c.raw)
		if got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestAgentDiagnoseError(t *testing.T) {
	cases := []struct{ err, wantDiag string }{
		{"checkpoint not found: foo.safetensors", "模型缺失/不匹配"},
		{"CUDA out of memory. Tried to allocate 2.00 GiB", "显存不足(OOM)"},
		{"connection refused 127.0.0.1:8190", "ComfyUI 未连通"},
		{"HTTP 401 unauthorized", "API Key 无效"},
		{"context deadline exceeded (Client.Timeout)", "请求超时"},
		{"invalid character 'x' looking for beginning of value", "LLM 输出异常"},
		{"random weird failure", "未知错误"},
	}
	for _, c := range cases {
		diag, _ := manjuDiagnoseError("render", fmt.Errorf("%s", c.err))
		if diag != c.wantDiag {
			t.Errorf("diagnose(%q) = %q, want %q", c.err, diag, c.wantDiag)
		}
	}
}
