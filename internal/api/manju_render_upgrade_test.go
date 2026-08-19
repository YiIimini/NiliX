package api

import (
	"encoding/json"
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
	if s := ctx.seedFor(3); s != 1688 {
		t.Errorf("fixed 策略应恒定: %d", s)
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
	dir := filepath.Join(manjuRoot, proj)
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
	defer os.RemoveAll(filepath.Join(manjuRoot, proj))
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
	if sum := agentStatusSummary(filepath.Join(manjuRoot, proj, "config.json")); sum["llmStats"] == nil {
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
	dir := filepath.Join(manjuRoot, proj)
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
