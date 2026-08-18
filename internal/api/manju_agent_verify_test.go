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
		{"真实系图片", ""}, // 中文未收录词 → 忽略
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
	cases := []struct{ text, wantAction, wantContain string }{
		{"帮我体检一下这个项目", "health", ""},
		{"分析项目", "health", ""},
		{"推荐风格吧", "style", ""},
		{"看看什么画风合适", "style", ""},
		{"审片报告", "", "还没有审片记录"},
		{"总结一下学习情况", "", "学习记录"},
		{"记忆和趋势", "", "学习记录"},
		{"把问题都修了", "fixall", ""},
		{"优化调整升级一下", "fixall", ""},
		{"你好你是谁", "", "漫剧智能体"},
		{"天气怎么样", "", "没听懂"},
	}
	_, cfgPath := verifyConfig(t, "")
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
