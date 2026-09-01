package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-01 二合一阶段一:workflow API 集成验证(节点链/参考图/覆盖/引擎)

func TestShotWorkflowAPI(t *testing.T) {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	assets := filepath.Join(dir, "assets")
	_ = os.MkdirAll(filepath.Join(assets, "characters"), 0o755)
	_ = os.MkdirAll(filepath.Join(assets, "scenes"), 0o755)
	_ = os.MkdirAll(analysis, 0o755)
	// 最小 plan(1 镜,带角色与场景)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{map[string]any{"id": "苏晚萤", "role": "女主", "image_prompt": "p"}},
		"scenes":     []any{map[string]any{"id": "广场", "description": "d", "image_prompt": "s"}},
		"shots": []any{map[string]any{
			"shot_id": 1, "scene": "广场", "characters": []any{"苏晚萤"}, "shot_size": "中景",
			"camera": "缓推", "action": "她站着", "dialogue": "", "narration": "", "duration": 5,
			"h3_prompt": "subject_definitions:\n<Subject 1> is Su Wanying in <Picture 1>.\n\ndetailed_description:\nThe scene.\n",
		}},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0o644)
	// 假资产文件
	_ = os.WriteFile(filepath.Join(assets, "characters", "苏晚萤_front.png"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(assets, "scenes", "广场.png"), []byte("x"), 0o644)
	// 配置
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(dir) + `","analysis":"` + filepath.ToSlash(analysis) + `","assets":"` + filepath.ToSlash(assets) + `"},"render":{"steps":8}}`
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)

	// GET workflow
	req := httptest.NewRequest(http.MethodGet, "/api/manju/shot/workflow?config="+filepath.ToSlash(cfgPath)+"&episode=EP01&shot=1", nil)
	rr := httptest.NewRecorder()
	manjuShotWorkflowHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("workflow API 状态 %d: %s", rr.Code, rr.Body.String())
	}
	var resp manjuShotWorkflowResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ShotID != 1 || resp.Scene != "广场" {
		t.Fatalf("shot 元数据错: %+v", resp)
	}
	if len(resp.Nodes) < 10 {
		t.Fatalf("节点链不完整: %d 个", len(resp.Nodes))
	}
	if resp.Nodes[0].Name != "UNETLoader" {
		t.Fatalf("首节点应为 UNETLoader: %s", resp.Nodes[0].Name)
	}
	last := resp.Nodes[len(resp.Nodes)-1]
	if last.Name != "SaveVideo" {
		t.Fatalf("末节点应为 SaveVideo: %s", last.Name)
	}
	if len(resp.Refs) < 2 {
		t.Fatalf("参考图应含角色+场景: %d", len(resp.Refs))
	}
	if resp.Cache == "" {
		t.Fatal("缓存名不应为空")
	}
	// 覆盖前指纹基线(此时无覆盖文件)
	mctx, _ := newManjuCtx(filepath.ToSlash(cfgPath), "EP01", "", "", "")
	shots, _ := planShots(plan)
	fpBase := mctx.shotRenderFingerprint(shots[0])
	// POST override
	ovBody := `{"config":"` + filepath.ToSlash(cfgPath) + `","episode":"EP01","shot":1,"override":{"seed":42,"steps":12,"note":"测试"}}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/manju/shot/override", strings.NewReader(ovBody))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	manjuShotOverrideHandler(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("override API 状态 %d: %s", rr2.Code, rr2.Body.String())
	}
	// 覆盖生效:重新 GET workflow 应带 override 且 seed 变化
	req3 := httptest.NewRequest(http.MethodGet, "/api/manju/shot/workflow?config="+filepath.ToSlash(cfgPath)+"&episode=EP01&shot=1", nil)
	rr3 := httptest.NewRecorder()
	manjuShotWorkflowHandler(rr3, req3)
	var resp3 manjuShotWorkflowResp
	_ = json.Unmarshal(rr3.Body.Bytes(), &resp3)
	if resp3.Override.Seed == nil || *resp3.Override.Seed != 42 {
		t.Fatalf("覆盖未生效: %+v", resp3.Override)
	}
	// 覆盖后指纹变化(manifest 链路)
	fp2 := mctx.shotRenderFingerprint(shots[0])
	if fpBase == fp2 {
		t.Fatal("覆盖后指纹应变化(触发该镜 stale 重渲)")
	}
}

