package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 审计 5.2 回归:stopped 回调触发后,在飞 LLM 请求被 context 取消立即返回
// (旧实现等满 300s 超时——"停止无反应"残留点)
func TestManjuLLMStopCancelsInFlight(t *testing.T) {
	// 慢服务器:收到请求后挂起,不响应(模拟 LLM 卡住/慢响应)
	var started atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Store(true)
		time.Sleep(30 * time.Second) // 长挂起
	}))
	defer srv.Close()

	llm := &manjuLLM{
		baseURL: srv.URL, model: "test", apiKey: "k",
		temperature: 0.4, maxTokens: 100, timeout: 60 * time.Second,
		client: &http.Client{Timeout: 60 * time.Second},
	}
	// 停止回调:立即返回 true(模拟用户已点停止)
	llm.SetStopped(func() bool { return true })

	done := make(chan error, 1)
	go func() {
		_, err := llm.chat("sys", "user", 0)
		done <- err
	}()
	// 应在几秒内返回"已停止"(不能等 30s 服务器响应/60s 超时)
	select {
	case err := <-done:
		if err == nil || err.Error() != "已停止" {
			t.Fatalf("应返回已停止,得到: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("stopped 后请求未被取消,仍在等待(停止无反应)")
	}
}

// 回归:LLM 200 响应 body 正常读取(修复 5.2 误伤——读完 body 前 cancel context
// 会中断 chunked 流式传输,ReadAll 读到空 → "unexpected end of JSON input",
// 用户实测「深度分析失败: LLM 响应解析失败: unexpected end of JSON input」)
func TestManjuLLMReadsChunkedBody(t *testing.T) {
	payload := `{"choices":[{"finish_reason":"stop","message":{"content":"{\"style\":\"2.5d+ink\",\"reason\":\"ok\"}"}}],"usage":{"total_tokens":10}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 分块写入并 flush:模拟服务端 chunked transfer-encoding(响应头先到、body 后到)
		fl, _ := w.(http.Flusher)
		for i := 0; i < len(payload); i += 16 {
			_, _ = w.Write([]byte(payload[i:min(i+16, len(payload))]))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	llm := &manjuLLM{
		baseURL: srv.URL, model: "test", apiKey: "k",
		temperature: 0.4, maxTokens: 100, timeout: 30 * time.Second,
		client: &http.Client{Timeout: 30 * time.Second},
	}
	text, err := llm.chat("sys", "user", 0)
	if err != nil {
		t.Fatalf("chat 失败(body 被截断): %v", err)
	}
	if !strings.Contains(text, "2.5d+ink") {
		t.Fatalf("content 未完整读取: %q", text)
	}
}

// 回归(用户规则 2026-08):普通执行管线/一条龙 不解析小说总集——渲染配置参数
// 一律用用户配置的 style/neg_prompt,不被总集自动覆盖;仅 AI 一条龙分析时读总集。
// 验证:newManjuCtx 构建的 ctx 使用用户配置(总集存在也不改 style/neg_prompt)。
func TestNewManjuCtxKeepsUserConfig(t *testing.T) {
	// 构造含总集的小说目录 + 用户已配置 style/neg_prompt 的 config
	dir := t.TempDir()
	novelDir := filepath.Join(dir, "素材")
	_ = os.MkdirAll(novelDir, 0755)
	master := "# 渲染提示词总集\n\n## 一、漫剧渲染风格提示词\n\n```\nCinematic film still, live-action, photorealistic\n```\n\n## 二、全局负面提示词\n\n```\nanime, cartoon, illustration\n```\n"
	_ = os.WriteFile(filepath.Join(novelDir, "渲染提示词总集.md"), []byte(master), 0644)

	projDir := filepath.Join(ManjuRootDir, "zz_keepcfg_test")
	_ = os.RemoveAll(projDir)
	defer os.RemoveAll(projDir)
	_ = os.MkdirAll(projDir, 0755)
	cfgPath := filepath.Join(projDir, "config.json")
	cfg := map[string]any{
		"style": "2.5d+real+ink", // 用户配置的风格
		"paths": map[string]any{
			"workdir": projDir, "novel_dir": dir, "novel": filepath.Join(dir, "book.md"),
		},
		"render": map[string]any{"neg_prompt": "user custom negative", "width": 768, "height": 1344},
	}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, b, 0644)
	_ = os.WriteFile(filepath.Join(dir, "book.md"), []byte("x"), 0644)

	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 普通管线:总集存在但 style/neg_prompt 保持用户配置,不被覆盖
	if str(ctx.cfg["style"]) != "2.5d+real+ink" {
		t.Fatalf("普通管线 style 被覆盖: %q", ctx.cfg["style"])
	}
	if str(ctx.R["neg_prompt"]) != "user custom negative" {
		t.Fatalf("普通管线 neg_prompt 被覆盖: %q", ctx.R["neg_prompt"])
	}
	// 磁盘 config 未被改写(无 _prompt_master_synced 标记)
	if b2, _ := os.ReadFile(cfgPath); strings.Contains(string(b2), "_prompt_master_synced") {
		t.Fatal("普通管线不应写回总集注入标记")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 审计 P2 回归:条件缓存指纹必须包含 FL2VA 尾帧维度——
// fl2va_end_frame 开关切换 / 尾帧图变更后指纹变化(旧缓存失效,不串用双帧/单帧模式)
func TestShotFingerprintIncludesFl2vaEndFrame(t *testing.T) {
	dir := t.TempDir()
	scenes := filepath.Join(dir, "assets", "scenes")
	_ = os.MkdirAll(scenes, 0755)
	_ = os.WriteFile(filepath.Join(scenes, "s.png"), []byte("scene"), 0644)
	_ = os.WriteFile(filepath.Join(scenes, "s_end.png"), []byte("end1"), 0644)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets"), w: 768, h: 1344, fps: 24}

	shot := manjuShot{Scene: "s", Duration: 4, H3Prompt: "p"}
	// 开关关闭
	ctx.R = map[string]any{"fl2va_end_frame": false}
	fpOff := ctx.shotCondFingerprintAt(shot, 768, 1344)
	// 开关开启(空镜 + 有尾帧)
	ctx.R = map[string]any{"fl2va_end_frame": true}
	fpOn := ctx.shotCondFingerprintAt(shot, 768, 1344)
	if fpOff == fpOn {
		t.Fatalf("fl2va_end_frame 开关应改变指纹: off=%s on=%s", fpOff, fpOn)
	}
	// 尾帧图内容变化 → 指纹变化(用不同大小内容,规避同 mtime 粒度下 size 相同)
	_ = os.WriteFile(filepath.Join(scenes, "s_end.png"), []byte("end2-with-different-length"), 0644)
	fpOn2 := ctx.shotCondFingerprintAt(shot, 768, 1344)
	if fpOn == fpOn2 {
		t.Fatalf("尾帧图变更应改变指纹: %s", fpOn)
	}
	// 有角色镜头:不启用尾帧(Ref2VA 无此概念),指纹不含尾帧维度
	shot2 := manjuShot{Scene: "s", Characters: []string{"甲"}, Duration: 4, H3Prompt: "p"}
	ctx.R = map[string]any{"fl2va_end_frame": true}
	fpChar := ctx.shotCondFingerprintAt(shot2, 768, 1344)
	ctx.R = map[string]any{"fl2va_end_frame": false}
	fpCharOff := ctx.shotCondFingerprintAt(shot2, 768, 1344)
	if fpChar != fpCharOff {
		t.Fatalf("有角色镜头不应受尾帧开关影响: %s vs %s", fpChar, fpCharOff)
	}
}

// 审计 F1 回归:newManjuCtx 白名单扩张不再把小说库根外的 novel 目录注册进 fs 白名单
func TestNewManjuCtxSkipsOutsideNovelFSRoot(t *testing.T) {
	oldNovel := NovelRootDir
	root := t.TempDir()
	NovelRootDir = root
	defer func() { NovelRootDir = oldNovel }()

	dir := t.TempDir()
	// 越界 novel 放在工作目录之外(否则被 workdir 白名单覆盖,测不出 novel 自身注册逻辑)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.md")
	_ = os.WriteFile(outside, []byte("x"), 0644)
	cfgPath := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"paths": map[string]any{
			"workdir": dir, "novel": outside,
		},
	}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, b, 0644)

	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 越界 novel 不得被加入 fs 白名单
	if fsPathAllowed(outside) {
		t.Fatalf("小说库根外的 novel 不应进入 fs 白名单: %s", outside)
	}
	_ = ctx
}

// 回归:空镜双帧(FL2VA)走核心节点 MiniMaxH3ImageToVideo 的 last_frame 参数,
// 不再依赖不存在的自定义节点 MiniMaxH3Fl2VA(用户实测 400 missing_node_type)
func TestEncWorkflowDualFrameUsesCoreNode(t *testing.T) {
	// 真实 h3EncWorkflow:空镜 + 场景首图 + _scene_end 尾帧 → MiniMaxH3ImageToVideo 含 first+last_frame
	wf := h3EncWorkflow(map[string]any{
		"unet_fl2va": "f.safetensors", "unet_ref2va": "r.safetensors",
		"clip": "c.safetensors", "vae_video": "v.safetensors", "vae_audio": "a.safetensors",
		"_scene_end": "dir_scene_1_end.png",
	}, "prompt", 768, 1344, 145, nil, "dir_scene_1.png", "cache", false)

	foundDual := false
	for _, n := range wf {
		m, _ := n.(map[string]any)
		if m == nil {
			continue
		}
		ct, _ := m["class_type"].(string)
		if ct == "MiniMaxH3Fl2VA" {
			t.Fatal("工作流不应使用不存在的 MiniMaxH3Fl2VA 节点")
		}
		if ct == "MiniMaxH3ImageToVideo" {
			ins, _ := m["inputs"].(map[string]any)
			if ins["last_frame"] != nil {
				foundDual = true
			}
			if ins["first_frame"] == nil {
				t.Fatal("空镜首帧缺失:first_frame 应为场景首图")
			}
		}
	}
	if !foundDual {
		t.Fatal("双帧应通过 MiniMaxH3ImageToVideo.last_frame 实现,实际未找到")
	}
}
