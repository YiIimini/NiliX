package api

// Agent 全流程核验测试:体检 / 一键修复 / 聊天意图路由 / 风格规整 / 失败诊断 /
// 记忆汇总 / 智能体设置 / 升级处理 / 深度分析(LLM mock 全链路)。
// 用临时目录构造项目 config.json,不碰真实项目;agent_state 落在 manjuRoot 下独立
// 项目名,测试结束清理。

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

	"nilix/internal/agent"
	"nilix/internal/config"
)

const verifyProj = "zz_agent_verify"

// verifyConfig 构造临时项目:cfgPath 的目录名即项目名(agent_state 落盘用)
func verifyConfig(t *testing.T, llmKey string) (dir, cfgPath string) {
	t.Helper()
	dir = t.TempDir()
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
	registerManjuRoutes(m)
	return m
}

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

func TestAgentStyleLabelCN(t *testing.T) {
	if got := styleLabelCN("2.5d+ink+cyberpunk"); got != "2.5D 动漫 + 水墨 + cyberpunk" {
		t.Errorf("label = %q", got)
	}
	if got := styleLabelCN(""); got != "未设置" {
		t.Errorf("empty label = %q", got)
	}
}

func TestAgentChatRoutes(t *testing.T) {
	_, cfgPath := verifyConfig(t, "")
	// 确定性指令:修复(后端直接执行,reply 必须带结果)
	w, out := doReq(t, "POST", "/api/manju/agent/chat", map[string]any{"config": cfgPath, "text": "修复"})
	if w.Code != 200 {
		t.Fatalf("chat fix HTTP %d", w.Code)
	}
	if str(out["action"]) != "fixall" {
		t.Errorf("action = %v", out["action"])
	}
	if !strings.Contains(str(out["reply"]), "已自动修复 4 项") {
		t.Errorf("修复应直接执行并返回结果, got %q", str(out["reply"]))
	}
	// 修复后 config 已写回
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	R := cfg["render"].(map[string]any)
	if n, _ := manjuToInt(R["fps"]); n != 24 {
		t.Errorf("fps=%v, want 24", R["fps"])
	}
	// 再说修复:没有可修项,回复明确
	_, out2 := doReq(t, "POST", "/api/manju/agent/chat", map[string]any{"config": cfgPath, "text": "把问题都修了"})
	if !strings.Contains(str(out2["reply"]), "没有可自动修复") {
		t.Errorf("二次修复应提示无项可修, got %q", str(out2["reply"]))
	}
	cases := []struct{ text, wantAction, wantContain string }{
		{"帮我体检一下这个项目", "health", ""},
		{"分析项目", "health", ""},
		{"看看什么画风合适", "style", "失败"}, // 无 LLM key 时代码里走 manjuStyleAnalyzeRun 报"未配置 LLM"
		{"审片报告", "", "还没有审片记录"},
		{"总结一下学习情况", "", "学习记录"},
		{"记忆和趋势", "", "学习记录"},
		{"优化调整升级一下", "fixall", "没有可自动修复"},
		{"你好你是谁", "", ""}, // 未知短语:无 Key → 回退固定指令提示
	}
	for _, c := range cases {
		w, out := doReq(t, "POST", "/api/manju/agent/chat", map[string]any{"config": cfgPath, "text": c.text})
		if w.Code != 200 {
			t.Errorf("chat(%q) HTTP %d", c.text, w.Code)
			continue
		}
		if got := str(out["action"]); got != c.wantAction {
			t.Errorf("chat(%q) action = %q, want %q", c.text, got, c.wantAction)
		}
		if c.wantContain != "" && !strings.Contains(str(out["reply"]), c.wantContain) {
			t.Errorf("chat(%q) reply 不含 %q: %q", c.text, c.wantContain, str(out["reply"]))
		}
	}
	// 无 Key 自由对话回退:未配 Key 的项目问任意问题 → 提示配 Key(而非"没听懂")
	_, out3 := doReq(t, "POST", "/api/manju/agent/chat", map[string]any{"config": cfgPath, "text": "画面太暗了怎么办"})
	if !strings.Contains(str(out3["reply"]), "固定指令") || !strings.Contains(str(out3["reply"]), "DeepSeek") {
		t.Errorf("无 Key 自由对话应提示配 Key: %q", str(out3["reply"]))
	}
}

func TestAgentChatLLMFree(t *testing.T) {
	// 自由对话:mock LLM 带上下文回答 + 可触发动作
	var gotUser string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &req)
		for _, m := range req.Messages {
			if m.Role == "user" {
				gotUser = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"reply\":\"画面暗可以把步数提到 20,并在提示词里强化光源描述\",\"action\":\"\"}"},"finish_reason":"stop"}]}`))
	}))
	defer llm.Close()
	_, cfgPath := verifyConfig(t, "sk-chat")
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	L := cfg["llm"].(map[string]any)
	L["base_url"] = strings.TrimSuffix(llm.URL, "/")
	b, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)
	defer os.RemoveAll(filepath.Dir(manjuAgentStatePath(filepath.Base(filepath.Dir(cfgPath)))))

	w, out := doReq(t, "POST", "/api/manju/agent/chat", map[string]any{"config": cfgPath, "text": "画面太暗了怎么办"})
	if w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	if !strings.Contains(str(out["reply"]), "步数") {
		t.Errorf("LLM 自由回答未透传: %q", str(out["reply"]))
	}
	if !strings.Contains(gotUser, "【项目上下文】") || !strings.Contains(gotUser, "画面太暗了怎么办") {
		t.Errorf("上下文未注入, user=%q", truncate(gotUser, 80))
	}
}

func TestAgentHealthAndFix(t *testing.T) {
	_, cfgPath := verifyConfig(t, "")
	// 体检:应出现 ≥4 个可修复异常(步数/种子/帧率/时长)
	w, out := doReq(t, "GET", "/api/manju/agent/health?config="+cfgPath, nil)
	if w.Code != 200 {
		t.Fatalf("health HTTP %d", w.Code)
	}
	items := anyArr(out["items"])
	if len(items) < 7 {
		t.Fatalf("体检项太少: %d", len(items))
	}
	fixable := 0
	for _, it := range items {
		m := it.(map[string]any)
		if m["fixable"].(bool) && m["status"] != "ok" {
			fixable++
		}
	}
	if fixable < 4 {
		t.Errorf("可修复项 %d,期望 ≥4", fixable)
	}
	// 逐项一键修复
	for _, key := range []string{"render_steps", "render_seed", "render_fps", "render_dur"} {
		w2, out2 := doReq(t, "POST", "/api/manju/agent/health/fix", map[string]any{"config": cfgPath, "key": key})
		if w2.Code != 200 || !out2["ok"].(bool) {
			t.Fatalf("fix %s HTTP %d: %s", key, w2.Code, w2.Body.String())
		}
	}
	// 校验写回结果
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	R := cfg["render"].(map[string]any)
	if n, _ := manjuToInt(R["steps"]); n != 10 {
		t.Errorf("steps=%v, want 10", R["steps"])
	}
	if n, _ := manjuToInt(R["seed"]); n != 1688 {
		t.Errorf("seed=%v, want 1688", R["seed"])
	}
	if n, _ := manjuToInt(R["fps"]); n != 24 {
		t.Errorf("fps=%v, want 24", R["fps"])
	}
	if n, _ := manjuToInt(R["min_shot_seconds"]); n != 4 {
		t.Errorf("min=%v, want 4", R["min_shot_seconds"])
	}
	// 修复后重检:4 项不再异常
	_, out3 := doReq(t, "GET", "/api/manju/agent/health?config="+cfgPath, nil)
	for _, it := range anyArr(out3["items"]) {
		m := it.(map[string]any)
		if m["fixable"].(bool) && m["status"] != "ok" {
			t.Errorf("修复后 %s 仍异常: %v", m["key"], m["status"])
		}
	}
}

func TestAgentSettingsAndStatus(t *testing.T) {
	_, cfgPath := verifyConfig(t, "")
	w, out := doReq(t, "POST", "/api/manju/agent/settings", map[string]any{
		"config": cfgPath,
		"agent":  map[string]any{"enabled": true, "vision_model": "qwen-vl-max", "pass_score": 80, "max_retries": 3},
	})
	if w.Code != 200 || !out["ok"].(bool) {
		t.Fatalf("settings HTTP %d %s", w.Code, w.Body.String())
	}
	w2, out2 := doReq(t, "GET", "/api/manju/agent?config="+cfgPath, nil)
	if w2.Code != 200 {
		t.Fatalf("agent status HTTP %d", w2.Code)
	}
	if out2["agentEnabled"] != true {
		t.Errorf("agentEnabled = %v", out2["agentEnabled"])
	}
	if str(out2["visionModel"]) != "qwen-vl-max" {
		t.Errorf("visionModel = %v", out2["visionModel"])
	}
	if v, _ := manjuToFloat(out2["passScore"]); v != 80 {
		t.Errorf("passScore = %v", out2["passScore"])
	}
	if n, _ := manjuToInt(out2["maxRetries"]); n != 3 {
		t.Errorf("maxRetries = %v", out2["maxRetries"])
	}
}

func TestAgentResolveIgnore(t *testing.T) {
	dir, cfgPath := verifyConfig(t, "")
	project := filepath.Base(dir) // TempDir 名即项目名
	statePath := manjuAgentStatePath(project)
	_ = os.MkdirAll(filepath.Dir(statePath), 0755)
	st := &manjuAgentState{
		Episode: "EP01",
		Shots:   map[string]*agent.Judgment{"1": {Status: "failed", Score: 50}},
		Escalations: []manjuAgentEscalation{
			{EP: "EP01", Shot: 1, Score: 50, Reason: "判分未达标"},
		},
	}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(statePath, b, 0644)
	defer os.RemoveAll(filepath.Dir(statePath))

	w, out := doReq(t, "POST", "/api/manju/agent/resolve", map[string]any{
		"config": cfgPath, "episode": "EP01", "shot": 1, "action": "ignore",
	})
	if w.Code != 200 || !out["ok"].(bool) {
		t.Fatalf("resolve ignore HTTP %d %s", w.Code, w.Body.String())
	}
	st2 := loadAgentState(project)
	if len(st2.Escalations) != 1 || !st2.Escalations[0].Resolved {
		t.Errorf("升级未解除: %+v", st2.Escalations)
	}
	if st2.Shots["1"].Status != "accepted" {
		t.Errorf("镜头状态 = %s, want accepted", st2.Shots["1"].Status)
	}
	// 非法 action → 400
	w3, _ := doReq(t, "POST", "/api/manju/agent/resolve", map[string]any{
		"config": cfgPath, "episode": "EP01", "shot": 1, "action": "bad",
	})
	if w3.Code != 400 {
		t.Errorf("非法 action 应 400, got %d", w3.Code)
	}
}

func TestAgentStyleAnalyzeFullFlow(t *testing.T) {
	// mock LLM(OpenAI 兼容):返回 2.5d+水墨
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"style\":\"2.5d, 水墨\",\"reason\":\"古风题材匹配\"}"},"finish_reason":"stop"}]}`))
	}))
	defer llm.Close()

	_, cfgPath := verifyConfig(t, "sk-test")
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	L := cfg["llm"].(map[string]any)
	L["base_url"] = strings.TrimSuffix(llm.URL, "/")
	b, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)

	project := filepath.Base(filepath.Dir(cfgPath))
	stateDir := filepath.Dir(manjuAgentStatePath(project))
	defer os.RemoveAll(stateDir)

	w, out := doReq(t, "POST", "/api/manju/agent/style", map[string]any{
		"config": cfgPath, "chapters": "1", "episode": "EP01",
	})
	if w.Code != 200 {
		t.Fatalf("style analyze HTTP %d: %s", w.Code, w.Body.String())
	}
	if str(out["style"]) != "2.5d+ink" {
		t.Errorf("style = %q, want 2.5d+ink", str(out["style"]))
	}
	if str(out["old"]) != "" {
		t.Errorf("old = %q, want 空", str(out["old"]))
	}
	if !strings.Contains(str(out["reason"]), "古风") {
		t.Errorf("reason = %q", str(out["reason"]))
	}
	// 配置已写回
	var cfg2 map[string]any
	b2, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b2, &cfg2)
	if str(cfg2["style"]) != "2.5d+ink" {
		t.Errorf("config.style = %q", str(cfg2["style"]))
	}
	// 记忆已记录
	st := loadAgentState(project)
	if len(st.Memory.StyleChoices) != 1 || st.Memory.StyleChoices[0].New != "2.5d+ink" {
		t.Errorf("StyleChoices = %+v", st.Memory.StyleChoices)
	}
}

func TestAgentStyleAnalyzeValidation(t *testing.T) {
	// 无 LLM Key → 明确报错
	_, cfgPath := verifyConfig(t, "")
	w, _ := doReq(t, "POST", "/api/manju/agent/style", map[string]any{"config": cfgPath, "chapters": "1"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "未配置 LLM") {
		t.Errorf("无 Key: HTTP %d %s", w.Code, w.Body.String())
	}
	// 小说内容太少(空正文) → 明确报错
	_, cfgPath2 := verifyConfig(t, "sk-test")
	_ = os.WriteFile(filepath.Join(filepath.Dir(cfgPath2), "novel.md"), []byte("第一章 只有一句很短的内容"), 0644)
	w2, _ := doReq(t, "POST", "/api/manju/agent/style", map[string]any{"config": cfgPath2, "chapters": "1"})
	if w2.Code != 400 || !strings.Contains(w2.Body.String(), "内容太少") {
		t.Errorf("内容太少: HTTP %d %s", w2.Code, w2.Body.String())
	}
}

func TestAgentGlobalDefaults(t *testing.T) {
	// 全局默认:settings.json agent 节 → 全项目共用;项目未配置时生效,项目配置覆盖
	store := config.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	SetManjuSettingsStore(store)
	SetGlobalAgentCfg(config.Default())
	defer SetManjuSettingsStore(nil)

	// 另存为全局默认(global=true)
	_, cfgPath := verifyConfig(t, "")
	w, out := doReq(t, "POST", "/api/manju/agent/settings", map[string]any{
		"config": cfgPath, "global": "true",
		"agent": map[string]any{"enabled": true, "vision_model": "glm-4.6v-flash",
			"vision_api_key": "sk-global-test", "pass_score": 80, "max_retries": 3},
	})
	if w.Code != 200 || !out["ok"].(bool) {
		t.Fatalf("global save HTTP %d %s", w.Code, w.Body.String())
	}
	// settings.json 已写入且 Key 加密
	g, err := store.Load()
	if err != nil {
		t.Fatalf("settings load: %v", err)
	}
	if g.Agent == nil || g.Agent.VisionModel != "glm-4.6v-flash" {
		t.Fatalf("global agent = %+v", g.Agent)
	}
	if g.Agent.VisionAPIKey != "sk-global-test" {
		t.Fatalf("Key 应解密为明文, got %q", g.Agent.VisionAPIKey)
	}
	// 项目未配置 agent 节 → 全局默认生效
	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	acfg := loadAgentCfg(ctx)
	if acfg.VisionModel != "glm-4.6v-flash" || !acfg.Enabled || acfg.PassScore != 80 || acfg.MaxRetries != 3 {
		t.Errorf("全局默认未生效: %+v", acfg)
	}
	if acfg.VisionAPIKey != "sk-global-test" {
		t.Errorf("全局 Key 未生效: %q", acfg.VisionAPIKey)
	}
	// 项目配置覆盖全局
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	cfg["agent"] = map[string]any{"vision_model": "qwen-vl-max", "vision_api_key": "sk-proj"}
	b, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0644)
	ctx2, _ := newManjuCtx(cfgPath, "", "", "", "")
	acfg2 := loadAgentCfg(ctx2)
	if acfg2.VisionModel != "qwen-vl-max" || acfg2.VisionAPIKey != "sk-proj" {
		t.Errorf("项目覆盖失败: %+v", acfg2)
	}
	if acfg2.PassScore != 80 {
		t.Errorf("未覆盖字段应沿用全局默认, passScore=%v", acfg2.PassScore)
	}
	// GET 接口带全局默认展示
	w2, out2 := doReq(t, "GET", "/api/manju/agent?config="+cfgPath, nil)
	if w2.Code != 200 {
		t.Fatalf("agent status HTTP %d", w2.Code)
	}
	gd, _ := out2["globalDefaults"].(map[string]any)
	if gd == nil || str(gd["visionModel"]) != "glm-4.6v-flash" {
		t.Errorf("globalDefaults = %+v", gd)
	}
}

func TestAgentMemorySummary(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Base(dir)
	stateDir := filepath.Dir(manjuAgentStatePath(project))
	defer os.RemoveAll(stateDir)
	// 空记忆
	s := manjuMemorySummary(project)
	if !strings.Contains(s, "没有学习记录") {
		t.Errorf("空记忆回复 = %q", s)
	}
	// 填充记忆
	_ = os.MkdirAll(stateDir, 0755)
	st := &manjuAgentState{
		Shots: map[string]*agent.Judgment{},
		Memory: manjuAgentMemory{
			RunCount: 3, JudgedShots: 42, ReworkCount: 2,
			IssueStats:   map[string]int{"面部扭曲": 5, "近黑帧": 3, "指令遵循差": 2},
			ScoreTrend:   []manjuScorePoint{{Episode: "EP01", Score: 70, Count: 20, At: 1}, {Episode: "EP02", Score: 78, Count: 22, At: 2}},
			StyleChoices: []manjuStyleChoice{{Old: "", New: "2.5d+ink", Reason: "古风"}},
		},
	}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(manjuAgentStatePath(project), b, 0644)
	s = manjuMemorySummary(project)
	for _, want := range []string{"3 次", "42 镜", "2 次", "↑ 提升 8 分", "面部扭曲×5", "2.5D 动漫 + 水墨"} {
		if !strings.Contains(s, want) {
			t.Errorf("记忆汇总缺 %q:\n%s", want, s)
		}
	}
}

// TestAgentSettingsProjectMissing 项目目录缺失时:GET agent 仍返回全局默认(不空白),另存为全局默认不依赖项目 config
func TestAgentSettingsProjectMissing(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	SetManjuSettingsStore(store)
	defer SetManjuSettingsStore(nil)
	missing := filepath.Join(manjuRoot, "zz_missing_proj", "config.json")
	// GET:项目缺失 → projectMissing + globalDefaults 齐全
	w, out := doReq(t, "GET", "/api/manju/agent?config="+filepath.ToSlash(missing), nil)
	if w.Code != 200 {
		t.Fatalf("项目缺失 GET agent HTTP %d", w.Code)
	}
	if out["projectMissing"] != true {
		t.Errorf("应标记 projectMissing: %v", out)
	}
	gd, ok := out["globalDefaults"].(map[string]any)
	if !ok || gd["visionModel"] == nil {
		t.Errorf("项目缺失应返回 globalDefaults: %v", out)
	}
	// POST:另存为全局默认(项目不存在)应成功,并写入 settings.json agent 节
	w2, out2 := doReq(t, "POST", "/api/manju/agent/settings", map[string]any{
		"config": filepath.ToSlash(missing), "global": "true",
		"agent": map[string]any{"enabled": true, "vision_model": "glm-4.6v-flash", "pass_score": 80},
	})
	if w2.Code != 200 || out2["global"] != true {
		t.Fatalf("项目缺失另存为全局默认应成功, HTTP %d %s", w2.Code, w2.Body.String())
	}
	if manjuGlobalAgent.VisionModel != "glm-4.6v-flash" || manjuGlobalAgent.PassScore != 80 {
		t.Errorf("全局默认未刷新: %+v", manjuGlobalAgent)
	}
	// 保存后 GET(项目仍缺失)应回填刚存的全局默认
	w3, out3 := doReq(t, "GET", "/api/manju/agent?config="+filepath.ToSlash(missing), nil)
	if w3.Code != 200 {
		t.Fatalf("HTTP %d", w3.Code)
	}
	gd3, _ := out3["globalDefaults"].(map[string]any)
	if gd3["visionModel"] != "glm-4.6v-flash" {
		t.Errorf("保存后全局默认未回填: %v", out3)
	}
}

// TestManjuNormalizeStyleCN 中文自定义词保留:输入 东方神话+东方修仙 应原样保留(此前被静默忽略)
func TestManjuNormalizeStyleCN(t *testing.T) {
	style, notes := manjuNormalizeStyle("东方神话+东方修仙+2.5d")
	if notes != "" {
		t.Fatalf("中文词不应被忽略: notes=%q", notes)
	}
	if style != "东方神话+东方修仙+2.5d" {
		t.Fatalf("中文词应原样保留: %q", style)
	}
	// 预设中文叫法仍映射
	if s, _ := manjuNormalizeStyle("水墨+赛博朋克"); s != "ink+cyberpunk" {
		t.Fatalf("预设/映射词应转换: %q", s)
	}
	// 超长词忽略
	if s, n := manjuNormalizeStyle("这是一个非常非常非常非常非常非常非常非常非常非常非常非常非常长的自定义风格词测试"); n == "" || s != "" {
		t.Fatalf("超长词应忽略: style=%q notes=%q", s, n)
	}
}
