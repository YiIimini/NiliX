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

// 回归:渲染提示词总集 → 渲染配置注入(用户反馈:总集风格/负面未进渲染配置,
// 界面显示旧值;本地生图用内置默认负面而非总集负面)
// 总集存在时:style 为空→写总集风格;neg_prompt 空/内置默认→写总集负面;用户自定义不覆盖
func TestManjuInjectPromptMaster(t *testing.T) {
	// 构造含总集的小说目录
	dir := t.TempDir()
	novelDir := filepath.Join(dir, "素材")
	_ = os.MkdirAll(novelDir, 0755)
	master := "# 渲染提示词总集\n\n## 一、漫剧渲染风格提示词（全剧风格统一，注入每镜）\n\n```\nCinematic film still, live-action, photorealistic, modern urban xianxia fantasy aesthetic, cinematic color grading, epic cinematic quality, volumetric lighting, ultra detailed, 8k, HDR\n```\n\n## 二、全局负面提示词（所有画面统一追加）\n\n```\nanime, cartoon, illustration, manga, chibi, doll, 3d render, plastic skin, flat lighting, watermark, text, letters, logo, signature, deformed, extra fingers, extra limbs, low quality, blurry, oversaturated, heavy makeup\n```\n"
	_ = os.WriteFile(filepath.Join(novelDir, "渲染提示词总集.md"), []byte(master), 0644)

	// 1. style 空 + neg 空 → 都注入
	cfg := map[string]any{}
	R := map[string]any{}
	P := map[string]any{"novel_dir": dir}
	if !manjuInjectPromptMaster(cfg, R, P) {
		t.Fatal("总集存在时应发生注入")
	}
	if !strings.Contains(str(cfg["style"]), "photorealistic") || !strings.Contains(str(cfg["style"]), "HDR") {
		t.Fatalf("style 未注入总集风格: %q", cfg["style"])
	}
	if !strings.Contains(str(R["neg_prompt"]), "anime") || !strings.Contains(str(R["neg_prompt"]), "heavy makeup") {
		t.Fatalf("neg_prompt 未注入总集负面: %q", R["neg_prompt"])
	}

	// 2. 用户 style 已含总集特征词(Cinematic)→ 不覆盖;用户自定义 neg → 不覆盖
	cfg2 := map[string]any{"style": "Cinematic film still, custom user style"}
	R2 := map[string]any{"neg_prompt": "user custom negative"}
	if manjuInjectPromptMaster(cfg2, R2, P) {
		t.Fatal("style 已含总集特征词/neg 已自定义时不应覆盖")
	}
	if cfg2["style"] != "Cinematic film still, custom user style" || R2["neg_prompt"] != "user custom negative" {
		t.Fatalf("用户配置被覆盖: style=%q neg=%q", cfg2["style"], R2["neg_prompt"])
	}

	// 2b. 旧 style 不含总集特征词(如 AI 分析的预设组合)→ 注入总集风格
	cfg2b := map[string]any{"style": "2.5d+real+ink+anime+urban fantasy+neon noir+film grain"}
	R2b := map[string]any{}
	if !manjuInjectPromptMaster(cfg2b, R2b, P) {
		t.Fatal("旧 style 不含总集特征词时应注入总集风格")
	}
	if !strings.Contains(str(cfg2b["style"]), "Cinematic") {
		t.Fatalf("style 未替换为总集风格: %q", cfg2b["style"])
	}
	if str(R2b["_prompt_master_synced"]) != "true" && R2b["_prompt_master_synced"] != true {
		t.Fatalf("注入后应置 _prompt_master_synced 标记: %v", R2b["_prompt_master_synced"])
	}

	// 3. neg 为内置默认 → 覆盖为总集负面(用户未真正自定义)
	cfg3 := map[string]any{}
	R3 := map[string]any{"neg_prompt": manjuNegPrompt}
	if !manjuInjectPromptMaster(cfg3, R3, P) {
		t.Fatal("neg 为内置默认时应注入总集负面")
	}
	if strings.Contains(str(R3["neg_prompt"]), "lowres") {
		t.Fatalf("内置默认未被总集负面替换: %q", R3["neg_prompt"])
	}
	if !strings.Contains(str(R3["neg_prompt"]), "anime") {
		t.Fatalf("neg_prompt 未注入总集负面: %q", R3["neg_prompt"])
	}

	// 4. 无总集 → 不注入
	emptyDir := t.TempDir()
	P4 := map[string]any{"novel_dir": emptyDir}
	cfg4 := map[string]any{}
	R4 := map[string]any{}
	if manjuInjectPromptMaster(cfg4, R4, P4) {
		t.Fatal("无总集不应注入")
	}
	if cfg4["style"] != nil || R4["neg_prompt"] != nil {
		t.Fatalf("无总集仍写入了: style=%v neg=%v", cfg4["style"], R4["neg_prompt"])
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
