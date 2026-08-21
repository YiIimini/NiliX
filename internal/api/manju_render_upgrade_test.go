package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nilix/internal/agent"
)

// TestResTierDims 分辨率档位换算:等比缩放 + 对齐 32 + 竖/横屏短边判定 + 无效档位回退
func TestResTierDims(t *testing.T) {
	cases := []struct {
		tier   string
		w, h   int
		ew, eh int
		ok     bool
	}{
		{"draft", 768, 1344, 416, 736, true}, // 竖屏 9:16 → 短边 416
		{"standard", 768, 1344, 768, 1344, true},
		{"fhd", 768, 1344, 1088, 1920, true},    // 1344*1088/768=1904 → 对齐 32 → 1920
		{"draft", 1344, 768, 736, 416, true},    // 横屏:高为短边
		{"custom", 768, 1344, 768, 1344, false}, // custom/未知档位 → 原值
		{"nope", 100, 200, 100, 200, false},
	}
	for _, c := range cases {
		w, h, ok := manjuResTierDims(c.tier, c.w, c.h)
		if w != c.ew || h != c.eh || ok != c.ok {
			t.Errorf("tier=%s %dx%d → %dx%d(ok=%v),期望 %dx%d(ok=%v)", c.tier, c.w, c.h, w, h, ok, c.ew, c.eh, c.ok)
		}
		if ok && (w%32 != 0 || h%32 != 0) {
			t.Errorf("tier=%s 结果未对齐 32: %dx%d", c.tier, w, h)
		}
	}
}

// TestDraftDims 草稿预审缩放:0.5 → 像素量约 1/4,两边对齐 32
func TestDraftDims(t *testing.T) {
	ctx := &manjuCtx{w: 768, h: 1344, draftScale: 0.5}
	dw, dh := ctx.draftDims()
	if dw != 384 || dh != 672 {
		t.Errorf("draftDims = %dx%d,期望 384x672", dw, dh)
	}
	// 缩放非法值回退 0.5
	ctx.draftScale = 0
	if dw2, _ := ctx.draftDims(); dw2 != 384 {
		t.Errorf("draftScale=0 未回退 0.5: %d", dw2)
	}
}

// TestSeedFor seed 重试策略:fixed 恒定 / increment 递增 / random 首渲仍用配置 seed
func TestSeedFor(t *testing.T) {
	ctx := &manjuCtx{seed: 1688, seedPolicy: "fixed"}
	if s := ctx.seedFor(0); s != 1688 {
		t.Errorf("fixed 首渲应等于配置 seed: %d", s)
	}
	if s := ctx.seedFor(3); s != 1691 {
		t.Errorf("fixed 重渲(attempt>0)应换 seed(+attempt),否则同 seed 同画面质检永远不过: %d", s)
	}
	ctx.seedPolicy = "increment"
	if s := ctx.seedFor(2); s != 1690 {
		t.Errorf("increment 策略第 2 次应 seed+2: %d", s)
	}
	if s := ctx.seedFor(0); s != 1688 {
		t.Errorf("increment 首渲应等于 seed: %d", s)
	}
	ctx.seedPolicy = "random"
	if s := ctx.seedFor(0); s != 1688 {
		t.Errorf("random 首渲应等于 seed: %d", s)
	}
	if s := ctx.seedFor(1); s == 1688 {
		t.Errorf("random 重试应换新随机(碰巧等于配置 seed 的概率可忽略)")
	}
}

// TestH3RenderWorkflowSage SageAttention 开关:开=插入 PatchSageAttentionKJ,关=无
func TestH3RenderWorkflowSage(t *testing.T) {
	base := map[string]any{
		"unet_ref2va": "u.safetensors", "unet_fl2va": "f.safetensors",
		"vae_video": "v.safetensors", "vae_audio": "a.safetensors",
	}
	hasPatch := func(wf map[string]any) bool {
		for _, n := range wf {
			if m, ok := n.(map[string]any); ok && m["class_type"] == "PatchSageAttentionKJ" {
				return true
			}
		}
		return false
	}
	on := map[string]any{}
	for k, v := range base {
		on[k] = v
	}
	on["sage_attention"] = true
	if wf := h3RenderWorkflow(on, 1, 768, 1344, 107, 8, "cache", false, false, 0, 1); !hasPatch(wf) {
		t.Errorf("sage_attention=true 未插入 PatchSageAttentionKJ")
	}
	if wf := h3RenderWorkflow(base, 1, 768, 1344, 107, 8, "cache", false, false, 0, 1); hasPatch(wf) {
		t.Errorf("未配置 sage_attention 不应插入补丁节点")
	}
}

// TestSaveRenderUpgradeFields 渲染参数新字段保存与校验:档位/seed策略/布尔/浮点 + 非法值拒绝
func TestSaveRenderUpgradeFields(t *testing.T) {
	proj := "zz_render_upgrade_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"render":{"width":768,"height":1344}}`), 0644)

	w, out := doReq(t, "POST", "/api/manju/render", map[string]any{
		"config": cfgPath, "res_tier": "draft", "seed_policy": "increment",
		"sage_attention": true, "draft_judge": true, "draft_scale": 0.6,
	})
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("保存 HTTP %d: %s", w.Code, w.Body.String())
	}
	cfg, _ := readManjuConfig(cfgPath)
	R, _ := cfg["render"].(map[string]any)
	if R["res_tier"] != "draft" || R["seed_policy"] != "increment" {
		t.Errorf("档位/策略未保存: %v", R)
	}
	if R["sage_attention"] != true || R["draft_judge"] != true {
		t.Errorf("布尔开关未保存: %v", R)
	}
	if ds, _ := manjuToFloat(R["draft_scale"]); ds != 0.6 {
		t.Errorf("draft_scale 未保存: %v", R["draft_scale"])
	}

	// 非法值拒绝
	for _, bad := range []map[string]any{
		{"config": cfgPath, "res_tier": "4k"},
		{"config": cfgPath, "seed_policy": "shuffle"},
		{"config": cfgPath, "draft_scale": 0.05},
		{"config": cfgPath, "draft_scale": 1.5},
	} {
		wb, _ := doReq(t, "POST", "/api/manju/render", bad)
		if wb.Code != 400 {
			t.Errorf("非法值 %v 应 400,得到 %d", bad["res_tier"], wb.Code)
		}
	}

	// 档位生效:newManjuCtx 后宽高等比缩放
	R["fps"] = 24
	_ = os.WriteFile(cfgPath, mustJSON(cfg), 0644)
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.w != 416 || ctx.h != 736 {
		t.Errorf("draft 档位未生效: %dx%d", ctx.w, ctx.h)
	}
	if ctx.seedPolicy != "increment" || !ctx.draftJudge {
		t.Errorf("策略/草稿开关未生效: %v %v", ctx.seedPolicy, ctx.draftJudge)
	}
}

// TestListManjuEpisodesSkipsWorkDirs 集清单排除 _draft / 2k 工作子目录
func TestListManjuEpisodesSkipsWorkDirs(t *testing.T) {
	clips := t.TempDir()
	for _, d := range []string{"EP01", "EP02", "_draft", "2k"} {
		_ = os.MkdirAll(filepath.Join(clips, d), 0755)
	}
	eps := listManjuEpisodes(map[string]any{"clips": clips})
	if len(eps) != 2 || eps[0] != "EP01" || eps[1] != "EP02" {
		t.Errorf("集清单应只有 EP01/EP02,得到 %v", eps)
	}
	_ = strings.TrimSpace("")
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

// TestLLMStats token 用量记账:分模型累计、落盘往返、零用量跳过
func TestLLMStats(t *testing.T) {
	proj := "zz_llm_stats_test"
	defer os.RemoveAll(filepath.Join(ManjuRootDir, proj))
	manjuStatsAdd(proj, "", agent.Usage{}) // 空 project/零用量:不落盘不崩
	if _, err := os.Stat(manjuStatsPath(proj)); err == nil {
		t.Errorf("空用量不应落盘")
	}
	manjuStatsAdd(proj, "deepseek-chat", agent.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})
	manjuStatsAdd(proj, "deepseek-chat", agent.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	manjuStatsAdd(proj, "glm-4.6v-flash", agent.Usage{PromptTokens: 2000, CompletionTokens: 300, TotalTokens: 2300})
	st := manjuStatsLoad(proj)
	if st.Calls != 3 || st.Total != 2465 {
		t.Errorf("总量异常: %v", st)
	}
	ds := st.ByModel["deepseek-chat"]
	if ds == nil || ds.Calls != 2 || ds.Prompt != 110 || ds.Completion != 55 {
		t.Errorf("deepseek 累计异常: %v", ds)
	}
	glm := st.ByModel["glm-4.6v-flash"]
	if glm == nil || glm.Total != 2300 {
		t.Errorf("glm 累计异常: %v", glm)
	}
	// status 摘要带出记账
	if sum := agentStatusSummary(filepath.Join(ManjuRootDir, proj, "config.json")); sum["llmStats"] == nil {
		t.Errorf("status 摘要未带 llmStats")
	}
}

// TestRenderCKRoundtrip 渲染检查点落盘往返:set/get/clear
func TestRenderCKRoundtrip(t *testing.T) {
	ctx := &manjuCtx{
		analysisDir: t.TempDir(),
		episode:     "EP01",
	}
	if ctx.renderCKGet("03") != "" {
		t.Fatalf("空检查点应返回空")
	}
	ctx.renderCKSet("03", "pid-abc")
	ctx.renderCKSet("03@d", "pid-draft")
	if ctx.renderCKGet("03") != "pid-abc" || ctx.renderCKGet("03@d") != "pid-draft" {
		t.Errorf("检查点读写不一致: %v", ctx.renderCKLoad().Shots)
	}
	ctx.renderCKClear("03")
	if ctx.renderCKGet("03") != "" || ctx.renderCKGet("03@d") != "pid-draft" {
		t.Errorf("clear 不应影响其它 key")
	}
}

// TestRenderCKReclaim 崩溃恢复三分支:已完成→收产物;失败→错误;丢失→false+nil
func TestRenderCKReclaim(t *testing.T) {
	lg := &manjuLogger{state: manjuState}
	outDir := t.TempDir()
	src := filepath.Join(outDir, "manju_00001.mp4")
	_ = os.WriteFile(src, []byte("video-bytes"), 0644)

	mkServer := func(entry string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/history/") {
				_, _ = w.Write([]byte(entry))
				return
			}
			_, _ = w.Write([]byte("{}"))
		}))
	}
	// 1. 已完成 → 收回产物
	srv := mkServer(`{"p1": {"status": {"status_str": "success", "completed": true},
		"outputs": {"9": {"video": [{"filename": "manju_00001.mp4", "subfolder": ""}]}}}}`)
	ctx := &manjuCtx{comfy: newComfyClient(srv.URL), comfyOutput: outDir, analysisDir: t.TempDir(), episode: "EP01"}
	dst := filepath.Join(t.TempDir(), "03.mp4")
	ok, err := ctx.tryReclaim("p1", dst, lg)
	if !ok || err != nil || !fileExists(dst) {
		t.Errorf("已完成任务应收回产物: ok=%v err=%v dst存在=%v", ok, err, fileExists(dst))
	}
	srv.Close()
	// 2. 失败 → 错误(调用方清检查点重新提交)
	srv2 := mkServer(`{"p2": {"status": {"status_str": "error", "messages": [{"data": {"exception_message": "OOM"}}]}}}`)
	ctx2 := &manjuCtx{comfy: newComfyClient(srv2.URL), comfyOutput: outDir, analysisDir: t.TempDir(), episode: "EP01"}
	if ok2, err2 := ctx2.tryReclaim("p2", dst, lg); ok2 || err2 == nil || !strings.Contains(err2.Error(), "OOM") {
		t.Errorf("失败任务应返回错误: ok=%v err=%v", ok2, err2)
	}
	srv2.Close()
	// 3. history 无记录(ComfyUI 重启)→ false+nil(丢失,重新提交)
	manjuReclaimLostWait = 200 * time.Millisecond
	defer func() { manjuReclaimLostWait = 90 * time.Second }()
	srv3 := mkServer(`{}`)
	ctx3 := &manjuCtx{comfy: newComfyClient(srv3.URL), comfyOutput: outDir, analysisDir: t.TempDir(), episode: "EP01"}
	if ok3, err3 := ctx3.tryReclaim("p3", dst, lg); ok3 || err3 != nil {
		t.Errorf("丢失任务应 false+nil: ok=%v err=%v", ok3, err3)
	}
	srv3.Close()
}

// TestShotManifestLifecycle 产物时效清单:mark→current、改提示词→stale、删产物→missing、
// 无记录→unknown 兼容、clearShotArtifacts 清记录
func TestShotManifestLifecycle(t *testing.T) {
	proj := "zz_manifest_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	workdir := dir
	_ = os.MkdirAll(filepath.Join(workdir, "assets", "characters"), 0755)
	_ = os.MkdirAll(filepath.Join(workdir, "assets", "scenes"), 0755)
	_ = os.MkdirAll(filepath.Join(workdir, "clips", "EP01"), 0755)
	_ = os.MkdirAll(filepath.Join(workdir, "analysis"), 0755)
	_ = os.WriteFile(filepath.Join(workdir, "config.json"), []byte(`{"paths":{"workdir":"`+filepath.ToSlash(workdir)+`"}}`), 0644)
	ctx, err := newManjuCtx(filepath.Join(dir, "config.json"), "EP01", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := manjuShot{ID: 3, Scene: "s1", H3Prompt: "p1", Duration: 5, Characters: []string{"c1"}}
	mp4 := filepath.Join(ctx.clipsDir, "EP01", "03.mp4")
	_ = os.WriteFile(mp4, []byte("v"), 0644)

	if st := ctx.shotManifestStatus(s); st != "unknown" {
		t.Errorf("无记录应 unknown: %s", st)
	}
	ctx.manifestMark(s, true)
	if st := ctx.shotManifestStatus(s); st != "current" {
		t.Errorf("mark 后应 current: %s", st)
	}
	// 改提示词 → stale
	s.H3Prompt = "p2"
	if st := ctx.shotManifestStatus(s); st != "stale" {
		t.Errorf("改提示词应 stale: %s", st)
	}
	s.H3Prompt = "p1"
	// 改资产(定妆照 mtime/新增)→ stale
	_ = os.WriteFile(filepath.Join(workdir, "assets", "characters", "c1.png"), []byte("x"), 0644)
	if st := ctx.shotManifestStatus(s); st != "stale" {
		t.Errorf("换定妆照应 stale(条件缓存隐性失效防线): %s", st)
	}
	// 删产物 → missing
	_ = os.Remove(mp4)
	if st := ctx.shotManifestStatus(s); st != "missing" {
		t.Errorf("删产物应 missing: %s", st)
	}
	// clearShotArtifacts:mp4 与清单记录一起清
	_ = os.WriteFile(mp4, []byte("v"), 0644)
	ctx.manifestMark(s, false)
	ctx.clearShotArtifacts(s)
	if st := ctx.shotManifestStatus(s); st != "missing" {
		t.Errorf("clearShotArtifacts 后应 missing(产物已删): %s", st)
	}
	if e := ctx.manifestLoad().Shots["3"]; e != nil {
		t.Errorf("clearShotArtifacts 应清清单记录: %v", e)
	}
	// seam 记录(转场硬切依据)
	ctx.manifestMark(s, true)
	m := ctx.manifestLoad()
	if e := m.Shots["3"]; e == nil || !e.Seam {
		t.Errorf("seam 未记录: %v", e)
	}
}

// TestApplyTakes 多切点长镜:组头时长=总和(clamp 15)、内镜 TakeTail、selectedShots 过滤
func TestApplyTakes(t *testing.T) {
	plan := map[string]any{
		"takes": []any{[]any{1, 2, 3}, []any{5, 6}},
	}
	shots := []manjuShot{
		{ID: 1, Scene: "a", Duration: 5}, {ID: 2, Scene: "a", Duration: 4},
		{ID: 3, Scene: "a", Duration: 4}, {ID: 4, Scene: "b", Duration: 6},
		{ID: 5, Scene: "b", Duration: 8}, {ID: 6, Scene: "b", Duration: 9},
	}
	out := applyTakes(plan, shots)
	if out[0].Duration != 13 || len(out[0].TakeGroup) != 3 {
		t.Errorf("组头(1)应 Duration=13 且携带整组: %d %v", out[0].Duration, len(out[0].TakeGroup))
	}
	if out[1].TakeTail != true || out[2].TakeTail != true {
		t.Errorf("内镜(2,3)应标 TakeTail")
	}
	if out[3].TakeTail || len(out[3].TakeGroup) != 0 {
		t.Errorf("独立镜(4)不应受影响")
	}
	if out[4].Duration != 15 || !out[5].TakeTail {
		t.Errorf("组(5,6)时长应 clamp 15: %d", out[4].Duration)
	}
	// selectedShots:内镜被过滤
	ctx := &manjuCtx{}
	sel := ctx.selectedShots(out)
	ids := []int{}
	for _, s := range sel {
		ids = append(ids, s.ID)
	}
	if fmt.Sprint(ids) != "[1 4 5]" {
		t.Errorf("selectedShots 应只剩组头与独立镜: %v", ids)
	}
	// only 过滤同时生效:only=2(内镜)→ 空
	ctx2 := &manjuCtx{only: "2"}
	if sel2 := ctx2.selectedShots(out); len(sel2) != 0 {
		t.Errorf("only=内镜应无渲染目标: %v", sel2)
	}
}

// TestEnsureTakes 分组贪心:同场景相邻、组内数量/时长上限、确定性只生成一次
func TestEnsureTakes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"render":{"shots_per_take":3}}`), 0644)
	// workdir 指向临时目录(newManjuCtx 只需 render 节)
	ctx := &manjuCtx{R: map[string]any{"shots_per_take": 3}}
	shots := []manjuShot{
		{ID: 1, Scene: "a", Duration: 5}, {ID: 2, Scene: "a", Duration: 5},
		{ID: 3, Scene: "a", Duration: 5}, {ID: 4, Scene: "a", Duration: 5}, // 15s 上限,4 不可并入
		{ID: 5, Scene: "b", Duration: 3}, // 换场景另起
	}
	plan := map[string]any{"shots": []any{}}
	lg := &manjuLogger{state: manjuState}
	ctx.ensureTakes(plan, shots, lg)
	takes := anyArr(plan["takes"])
	if len(takes) != 1 {
		t.Fatalf("应只分出 1 组(1-3;4 超时长另起单镜,5 换场景单镜): %v", takes)
	}
	g := anyArr(takes[0])
	if len(g) != 3 || fmt.Sprint(g[0]) != "1" {
		t.Errorf("组应为 [1 2 3]: %v", g)
	}
	// 已有 takes 不重算
	ctx.ensureTakes(plan, shots, lg)
	if len(anyArr(plan["takes"])) != 1 {
		t.Errorf("已有分组应复用不重算")
	}
	// 关闭(默认 1)不分组
	plan2 := map[string]any{"shots": []any{}}
	ctx2 := &manjuCtx{R: map[string]any{}}
	ctx2.ensureTakes(plan2, shots, lg)
	if plan2["takes"] != nil {
		t.Errorf("shots_per_take<2 不应分组")
	}
}

// TestManjuTimecode 多切点时间戳格式 MM:SS.mmm
func TestManjuTimecode(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{{0, "00:00.000"}, {3.5, "00:03.500"}, {63.256, "01:03.256"}, {15, "00:15.000"}}
	for _, c := range cases {
		if got := manjuTimecode(c.in); got != c.want {
			t.Errorf("timecode(%v)=%s,期望 %s", c.in, got, c.want)
		}
	}
}

// TestNormalizeEpisode 集数 → 集号:1→EP01、12→EP12、0/空原样、旧 EP01 输入兼容
func TestNormalizeEpisode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1", "EP01"}, {"12", "EP12"}, {"0", "0"}, {"", ""},
		{"EP01", "EP01"}, {"ep03", "ep03"}, {" 5 ", "EP05"},
	}
	for _, c := range cases {
		if got := normalizeEpisode(c.in); got != c.want {
			t.Errorf("normalizeEpisode(%q)=%q,期望 %q", c.in, got, c.want)
		}
	}
}

// TestManjuChapterEpisodes 集数=0 自动模式:每章一集(第 N 章 = 第 N 集)
func TestManjuChapterEpisodes(t *testing.T) {
	proj := "zz_ep_auto_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	novel := filepath.Join(dir, "book.md")
	_ = os.WriteFile(novel, []byte("# 第1章 开篇\n内容一\n# 第3章 转折\n内容三\n# 第5章 高潮\n内容五\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"paths":{"novel":"`+filepath.ToSlash(novel)+`","workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	ctx, err := newManjuCtx(filepath.Join(dir, "config.json"), "0", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	segs := manjuChapterEpisodes(ctx, "1-999")
	if len(segs) != 3 {
		t.Fatalf("应每章一集共 3 集: %v", segs)
	}
	if segs[0].Episode != "EP01" || segs[0].Chapters != "1-1" {
		t.Errorf("第 1 集应为 EP01/1-1: %v", segs[0])
	}
	if segs[1].Episode != "EP02" || segs[1].Chapters != "3-3" {
		t.Errorf("第 2 集应为 EP02/3-3(按章节序): %v", segs[1])
	}
	if segs[2].Episode != "EP03" || segs[2].Chapters != "5-5" {
		t.Errorf("第 3 集应为 EP03/5-5: %v", segs[2])
	}
}

// TestManjuSaveRenderEpisode 保存集数:3→config.render.episode=EP03;0→删除(自动模式)
func TestManjuSaveRenderEpisode(t *testing.T) {
	proj := "zz_ep_save_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"render":{"width":768,"height":1344,"episode":"EP02"}}`), 0644)

	w, _ := doReq(t, "POST", "/api/manju/render", map[string]any{"config": cfgPath, "episode": "3"})
	if w.Code != 200 {
		t.Fatalf("保存 HTTP %d: %s", w.Code, w.Body.String())
	}
	cfg, _ := readManjuConfig(cfgPath)
	R, _ := cfg["render"].(map[string]any)
	if R["episode"] != "EP03" {
		t.Errorf("集数 3 应存 EP03: %v", R["episode"])
	}
	// 0 → 删除
	w2, _ := doReq(t, "POST", "/api/manju/render", map[string]any{"config": cfgPath, "episode": "0"})
	if w2.Code != 200 {
		t.Fatalf("保存0 HTTP %d", w2.Code)
	}
	cfg2, _ := readManjuConfig(cfgPath)
	R2, _ := cfg2["render"].(map[string]any)
	if _, ok := R2["episode"]; ok {
		t.Errorf("集数 0 应删除 episode: %v", R2["episode"])
	}
}

// TestManjuChapterTotal 章节 0 默认值:从 config 小说文件解析总章数
func TestManjuChapterTotal(t *testing.T) {
	proj := "zz_ch_total_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	novel := filepath.Join(dir, "book.md")
	_ = os.WriteFile(novel, []byte("# 第1章 a\n# 第2章 b\n# 第3章 c\n# 第4章 d\n# 第5章 e\n"), 0644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"novel":"`+filepath.ToSlash(novel)+`"}}`), 0644)
	if n := manjuChapterTotal(cfgPath); n != 5 {
		t.Fatalf("总章数应 5: %d", n)
	}
}

// TestManjuChapterEpisodesRange 每章一集 + 章节范围过滤:1-2 → 只生成前 2 章两集
func TestManjuChapterEpisodesRange(t *testing.T) {
	proj := "zz_ep_range_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	novel := filepath.Join(dir, "book.md")
	_ = os.WriteFile(novel, []byte("# 第1章 a\n# 第2章 b\n# 第3章 c\n# 第4章 d\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"paths":{"novel":"`+filepath.ToSlash(novel)+`","workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	ctx, _ := newManjuCtx(filepath.Join(dir, "config.json"), "0", "1-2", "", "")
	segs := manjuChapterEpisodes(ctx, "1-2")
	if len(segs) != 2 || segs[0].Episode != "EP01" || segs[0].Chapters != "1-1" || segs[1].Chapters != "2-2" {
		t.Fatalf("范围 1-2 应生成 EP01/1-1、EP02/2-2: %v", segs)
	}
	// 全书(章节 0 已解析 1-N):全部 4 集
	segsAll := manjuChapterEpisodes(ctx, "1-4")
	if len(segsAll) != 4 {
		t.Fatalf("全书应 4 集: %v", segsAll)
	}
	// 全本(1-999):不过滤
	segsFull := manjuChapterEpisodes(ctx, "1-999")
	if len(segsFull) != 4 {
		t.Fatalf("全本应 4 集: %v", segsFull)
	}
}

// TestParseChapterRange 范围解析
func TestParseChapterRange(t *testing.T) {
	if lo, hi, ok := parseChapterRange("1-56"); !ok || lo != 1 || hi != 56 {
		t.Fatalf("1-56 解析异常: %d %d %v", lo, hi, ok)
	}
	if _, _, ok := parseChapterRange("1-999"); ok {
		t.Fatalf("全本应不过滤")
	}
	if _, _, ok := parseChapterRange("0"); ok {
		t.Fatalf("0 应不过滤")
	}
	if _, _, ok := parseChapterRange("abc"); ok {
		t.Fatalf("非法应不过滤")
	}
}

// TestManjuNovelAssetPrompt 小说素材提示词读取:素材/人物生成提示词.md 命中并截断;无素材返回空
// TestScanNovelAssets 小说素材完整解析:素材/人物/场景分类、设定集合并、封面提示词、文件清单
func TestScanNovelAssets(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "素材"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "设定集"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "封面"), 0755)
	// 无素材 → 空清单
	if a := scanNovelAssets(dir); len(a.Files) != 0 {
		t.Fatalf("无素材应空: %v", a.Files)
	}
	// 素材分类:人物/场景/其它
	_ = os.WriteFile(filepath.Join(dir, "素材", "人物生成提示词.md"), []byte("人物:陈鱼,死鱼眼"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "素材", "场景提示词.md"), []byte("场景:青云宗山门"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "素材", "道具提示词.md"), []byte("道具:草绳"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "设定集", "设定集与大纲.md"), []byte("世界观:修仙界"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "设定集", "章节写作规范.md"), []byte("文风:轻松"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "封面", "封面提示词.md"), []byte("封面:写实电影级"), 0644)
	a := scanNovelAssets(dir)
	if !strings.Contains(a.CharPrompt, "陈鱼") || !strings.Contains(a.ScenePrompt, "山门") || !strings.Contains(a.ExtraPrompt, "草绳") {
		t.Fatalf("素材分类异常: char=%q scene=%q extra=%q", a.CharPrompt, a.ScenePrompt, a.ExtraPrompt)
	}
	if !strings.Contains(a.Setting, "世界观") || !strings.Contains(a.Setting, "文风") {
		t.Fatalf("设定集合并异常: %q", a.Setting)
	}
	if !strings.Contains(a.CoverPrompt, "写实") {
		t.Fatalf("封面提示词异常: %q", a.CoverPrompt)
	}
	if len(a.Files) != 6 {
		t.Fatalf("文件清单应 6 项: %v", a.Files)
	}
}

// TestNovelFingerprintIncludesAssets 方案指纹必须含素材:素材文件变更 → 指纹变化
// (修复「素材/人物生成提示词.md 准备了却不生效」——旧方案被 ensurePlan 复用,素材白准备)
func TestNovelFingerprintIncludesAssets(t *testing.T) {
	dir := t.TempDir()
	// 正文(全本) + 素材
	_ = os.MkdirAll(filepath.Join(dir, "素材"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "正文", "卷一"), 0755)
	novelPath := filepath.Join(dir, "全本", "书·全本.md")
	_ = os.MkdirAll(filepath.Dir(novelPath), 0755)
	_ = os.WriteFile(novelPath, []byte("第一章 内容"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "素材", "人物生成提示词.md"), []byte("人物:陈鱼,死鱼眼"), 0644)

	ctx := &manjuCtx{novel: novelPath}
	f1 := ctx.novelFingerprint()
	if f1 == "" {
		t.Fatalf("指纹为空")
	}
	// 改素材文件 → 指纹必须变化
	time.Sleep(1100 * time.Millisecond) // 确保 mtime 秒级变化
	_ = os.WriteFile(filepath.Join(dir, "素材", "人物生成提示词.md"), []byte("人物:陈鱼,死鱼眼,泪痣"), 0644)
	f2 := ctx.novelFingerprint()
	if f2 == f1 {
		t.Fatalf("素材变更后指纹未变化: %s", f1)
	}
	// 新增素材文件 → 指纹变化
	_ = os.WriteFile(filepath.Join(dir, "素材", "场景提示词.md"), []byte("场景:山门"), 0644)
	f3 := ctx.novelFingerprint()
	if f3 == f2 {
		t.Fatalf("新增素材后指纹未变化")
	}
	// 正文无关子目录(如 封面/全本外)不干扰:仍含正文指纹
	if !strings.Contains(f3, novelPath) {
		t.Fatalf("指纹应含正文路径: %s", f3)
	}
}
