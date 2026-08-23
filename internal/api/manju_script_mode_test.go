package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 新建项目:novel 留空 = 视频脚本直出模式(config 无 paths.novel,输出提示脚本模式)
func TestCreateProjectScriptMode(t *testing.T) {
	name := "test_script_proj_" + fmt.Sprint(time.Now().UnixNano())
	out, configPath, ok := manjuCreateProject(name, "", "sk-test")
	if !ok {
		t.Fatalf("脚本模式建项目失败: %s", out)
	}
	defer os.RemoveAll(filepath.Join(ManjuRootDir, name))
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		t.Fatalf("读 config: %v", err)
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		t.Fatalf("config 无 paths")
	}
	if str(P["novel"]) != "" {
		t.Fatalf("脚本模式 config 不应有 paths.novel, got %q", str(P["novel"]))
	}
	if str(P["workdir"]) == "" {
		t.Fatalf("脚本模式 config 应有 workdir")
	}
	if !strings.Contains(out, "视频脚本直出模式") {
		t.Fatalf("输出应提示脚本直出模式: %s", out)
	}
}

// 项目体检:脚本模式识别脚本文件而非小说
func TestHealthCheckScriptMode(t *testing.T) {
	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	script := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(script, []byte("[Shot 1] 中景,缓慢推近。青年坐在桌边。\n台词:苏白:\"来了。\""), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"2.5d","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(script)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	items := manjuHealthCheck(ctx)
	for _, it := range items {
		if it.Key == "novel" {
			if it.Label != "视频脚本" || it.Status != "ok" {
				t.Fatalf("脚本模式体检项错误: %+v", it)
			}
			return
		}
	}
	t.Fatalf("体检缺少输入源项")
}

// 脚本直出系统提示词:官方格式规定关键点必须齐全
func TestManjuScriptSystemOfficialFormat(t *testing.T) {
	sys := manjuScriptSystem(map[string]any{}, "2.5d")
	for _, want := range []string{
		"integrated_multimodal_description", // 三核心字段
		"overall_soundscape",
		"non_diegetic_music",
		"[Shot 1]",            // 镜头时间码语法
		"At MM:SS.mmm",        // 切点时间
		"<d>[中文]原文</d>",     // 台词逐字保留
		"off-screen voiceover", // 画外音措辞
		"lips remain completely closed",
		"Ref2VA 六段式",   // 六段式模板
		"FL2VA 三段式",    // 三段式模板
		"h3_prompt",     // 直出字段
		"directing",     // 防同质化五维
		"判停清单",         // 判停纪律
		"subject_definitions", // 参考标签体系
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuScriptSystem 缺少官方格式规定 %q", want)
		}
	}
}

// newManjuCtx 脚本模式检测:config paths.script 存在 → scriptMode + novel=脚本 + chapters 固定
func TestNewManjuCtxScriptMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	script := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(script, []byte("[Shot 1] ..."), 0o644)
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(script)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	if !ctx.scriptMode {
		t.Fatalf("应进入脚本模式")
	}
	if ctx.novel != filepath.ToSlash(script) {
		t.Fatalf("ctx.novel 应指向脚本文件, got %s", ctx.novel)
	}
	if ctx.chapters != "script" {
		t.Fatalf("脚本模式 chapters 应固定 script, got %s", ctx.chapters)
	}
}

// 无脚本配置 → 小说模式(章节归一化照常)
func TestNewManjuCtxNovelMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	novel := filepath.Join(dir, "book.md")
	_ = os.WriteFile(novel, []byte("# 第一章\n正文"), 0o644)
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"workdir":"`+filepath.ToSlash(dir)+`","novel":"`+filepath.ToSlash(novel)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	if ctx.scriptMode {
		t.Fatalf("无脚本配置不应进入脚本模式")
	}
	if ctx.novel != filepath.ToSlash(novel) {
		t.Fatalf("ctx.novel 应指向小说, got %s", ctx.novel)
	}
}

// ensurePlan 脚本模式端到端:脚本 → LLM 直出方案(带 h3_prompt),逐镜生成跳过
func TestEnsurePlanScriptMode(t *testing.T) {
	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	script := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(script, []byte(`# 视频渲染脚本 EP01
[Shot 1] 客栈内景,中景,缓慢推近。青年(苏白)坐在桌边,低声说:"来了。"环境:雨声,木地板吱呀。配乐:古琴慢板。
[Shot 2] At 00:04.000,切特写。女子推门而入,雨水顺着斗笠滴落。`), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"2.5d","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(script)+`"},"render":{}}`), 0o644)

	// mock LLM:捕获系统提示词与输入,返回带 h3_prompt 的完整方案
	var gotSys, gotInput string
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				gotSys = m.Content
			} else if m.Role == "user" {
				gotInput = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(string(mustJSON(map[string]any{
			"episode_title": "客栈夜雨",
			"characters": []any{map[string]any{
				"id": "苏白", "gender": "男", "age": "青年",
				"appearance": "黑色长发束起,剑眉,狭长眼,薄唇,面容清冷",
				"costume":    "深青色劲装",
				"image_prompt": "portrait",
				"views": map[string]any{"front": "f", "full": "fu", "side": "s", "detail": "d"},
			}},
			"scenes": []any{map[string]any{
				"id": "客栈", "description": "木质结构,雨夜,烛光", "image_prompt": "scene",
			}},
			"shots": []any{map[string]any{
				"shot_id": 1, "scene": "客栈", "characters": []any{"苏白"},
				"shot_size": "中景", "camera": "缓慢推近", "action": "苏白坐在桌边",
				"dialogue": "苏白:来了。", "narration": "", "duration": 5,
				"h3_prompt": "subject_definitions:\n...\n\nintegrated_multimodal_description: [Shot 1] ...",
			}, map[string]any{
				"shot_id": 2, "scene": "客栈", "characters": []any{},
				"shot_size": "特写", "camera": "硬切", "action": "女子推门而入",
				"dialogue": "", "narration": "", "duration": 4,
				"h3_prompt": "How the reference pictures align...\n\nintegrated_multimodal_description: [Shot 2] At 00:04.000 ...",
			}},
			"directing": map[string]any{
				"time": "线性", "pov": "全知", "tempo": "前紧后松",
				"audio": "对白驱动", "ending": "悬而未决",
				"peak_device": "推门定格", "climax_pattern": "特写收束",
			},
		}))) + `},"finish_reason":"stop"}]}`))
	}))
	defer llmSrv.Close()

	lg := &manjuLogger{state: manjuState}
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	ctx.llm = &manjuLLM{
		apiKey:  "sk-test",
		baseURL: strings.TrimSuffix(llmSrv.URL, "/"),
		model:   "deepseek-chat",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	plan, err := ctx.ensurePlan(lg)
	if err != nil {
		t.Fatalf("ensurePlan: %v", err)
	}
	// 系统提示词应走脚本直出(含官方格式),输入应是脚本全文
	if !strings.Contains(gotSys, "视频渲染脚本") || !strings.Contains(gotSys, "h3_prompt") {
		t.Fatalf("脚本模式系统提示词不对: %q", gotSys[:min(120, len(gotSys))])
	}
	if !strings.Contains(gotInput, "[Shot 1]") {
		t.Fatalf("输入应为脚本全文, got: %q", gotInput[:min(120, len(gotInput))])
	}
	// 方案落盘且含 h3_prompt(直出)
	planPath := filepath.Join(ctx.analysisDir, "EP01_direct_plan.json")
	if !fileExists(planPath) {
		t.Fatalf("方案未落盘")
	}
	shots, _ := planShots(plan)
	if len(shots) != 2 {
		t.Fatalf("应 2 镜, got %d", len(shots))
	}
	if shots[0].H3Prompt == "" {
		t.Fatalf("直出的 h3_prompt 丢失")
	}
	// 逐镜提示词生成应跳过(全部已有 h3_prompt)
	if err := ctx.genShotPrompts(plan, shots, lg); err != nil {
		t.Fatalf("genShotPrompts: %v", err)
	}
	promptsPath := filepath.Join(ctx.analysisDir, "EP01_shots_prompts.json")
	if b, rerr := os.ReadFile(promptsPath); rerr == nil && len(b) > 0 {
		var pm map[string]string
		_ = json.Unmarshal(b, &pm)
		if len(pm) != 0 {
			t.Fatalf("已有 h3_prompt 的镜不应再逐镜生成, got %d 条", len(pm))
		}
	}
	// 指纹已记录
	if recs := manjuFingerprintLoad(manjuFingerprintPath(ctx.analysisDir)); len(recs) != 1 || recs[0].ID != "EP01" {
		t.Fatalf("脚本模式指纹应记录 EP01, got %+v", recs)
	}
}

// 从小说项目 素材/分镜脚本/(爽文技能阶段6 产物)导入分镜脚本 → 脚本直出模式
func TestManjuScriptImportFromNovel(t *testing.T) {
	name := "test_imp_proj_" + fmt.Sprint(time.Now().UnixNano())
	_, configPath, ok := manjuCreateProject(name, "", "sk-test")
	if !ok {
		t.Fatalf("建项目失败")
	}
	defer os.RemoveAll(filepath.Join(ManjuRootDir, name))
	cfg, _ := readManjuConfig(configPath)
	P, _ := cfg["paths"].(map[string]any)
	workdir := str(P["workdir"])
	novelRoot := filepath.Join(workdir, "novel")
	sbDir := filepath.Join(novelRoot, "素材", "分镜脚本")
	_ = os.MkdirAll(sbDir, 0o755)
	src := filepath.Join(sbDir, "第001章_被扔掉的人_分镜脚本.md")
	_ = os.WriteFile(src, []byte("# 《人间回收站》分镜脚本 · 第001章\n\n## 一、分镜表\n| 镜号 | 景别 | ... |\n\n## 二、每镜 H3 提示词\n[Shot 1] At 00:00.000 中景,慢推。"), 0o644)
	P["novel"] = filepath.ToSlash(novelRoot)
	if err := writeManjuConfig(configPath, cfg); err != nil {
		t.Fatalf("写 config: %v", err)
	}

	// EP01 → 第001章 导入成功
	req := httptest.NewRequest("POST", "/api/manju/script/import-from-novel", strings.NewReader(`{"project":"`+name+`","episode":"EP01"}`))
	w := httptest.NewRecorder()
	manjuScriptImportFromNovel(w, req)
	if w.Code != 200 {
		t.Fatalf("import 应 200, got %d: %s", w.Code, w.Body.String())
	}
	cfg2, _ := readManjuConfig(configPath)
	P2, _ := cfg2["paths"].(map[string]any)
	sp := str(P2["script"])
	if sp == "" || !fileExists(sp) {
		t.Fatalf("config paths.script 未设置或文件不存在: %q", sp)
	}
	if b, _ := os.ReadFile(sp); !strings.Contains(string(b), "第001章") {
		t.Fatalf("导入内容应为分镜脚本全文")
	}

	// 集号无效 → 400
	req2 := httptest.NewRequest("POST", "/api/manju/script/import-from-novel", strings.NewReader(`{"project":"`+name+`","episode":"bad"}`))
	w2 := httptest.NewRecorder()
	manjuScriptImportFromNovel(w2, req2)
	if w2.Code != 400 {
		t.Fatalf("无效集号应 400, got %d", w2.Code)
	}

	// 无分镜脚本目录 → 404
	_ = os.RemoveAll(filepath.Join(novelRoot, "素材"))
	req3 := httptest.NewRequest("POST", "/api/manju/script/import-from-novel", strings.NewReader(`{"project":"`+name+`","episode":"EP02"}`))
	w3 := httptest.NewRecorder()
	manjuScriptImportFromNovel(w3, req3)
	if w3.Code != 404 {
		t.Fatalf("无分镜脚本应 404, got %d", w3.Code)
	}
}
