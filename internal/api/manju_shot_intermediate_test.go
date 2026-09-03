package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-03 画布三期:中间产物(缓存/接缝 latent/成片/建议抽帧点)

func TestShotIntermediates(t *testing.T) {
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	assets := filepath.Join(dir, "assets")
	clips := filepath.Join(dir, "clips", "EP01")
	_ = os.MkdirAll(analysis, 0o755)
	_ = os.MkdirAll(assets, 0o755)
	_ = os.MkdirAll(clips, 0o755)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{},
		"scenes":     []any{},
		"shots": []any{map[string]any{
			"shot_id": 2, "scene": "", "characters": []any{}, "shot_size": "中景",
			"camera": "固定", "action": "a", "dialogue": "", "narration": "", "duration": 8,
			"h3_prompt": "subject_definitions:\n<Subject 1> is x in <Picture 1>.\n\ndetailed_description:\nX.\n",
		}},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0o644)
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(dir) + `","analysis":"` + filepath.ToSlash(analysis) + `","assets":"` + filepath.ToSlash(assets) + `","clips":"` + filepath.ToSlash(filepath.Join(dir, "clips")) + `"},"render":{"steps":8}}`
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/manju/shot/intermediates?config="+filepath.ToSlash(cfgPath)+"&episode=EP01&shot=2", nil)
	rr := httptest.NewRecorder()
	manjuShotIntermediatesHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("intermediates 状态 %d: %s", rr.Code, rr.Body.String())
	}
	var resp manjuShotIntermediatesResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Cache.Name == "" {
		t.Fatal("缓存名不应为空")
	}
	if resp.Cache.Info.Exists {
		t.Fatal("无缓存文件应为 exists=false")
	}
	if resp.Latent.Prev.Exists {
		t.Fatal("无 latent 文件应为 exists=false")
	}
	if len(resp.Frames) != 6 {
		t.Fatalf("建议抽帧点应 6 个: %v", resp.Frames)
	}
	// duration=8:首帧点≈0.24(0.03×8),尾帧点≈7.76
	if resp.Frames[0] > 0.5 || resp.Frames[5] < 7 {
		t.Fatalf("抽帧点未按 duration 均布: %v", resp.Frames)
	}
}

func TestShotFrameMissingVideo(t *testing.T) {
	dir := t.TempDir()
	clips := filepath.Join(dir, "clips")
	_ = os.MkdirAll(clips, 0o755)
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(dir) + `","clips":"` + filepath.ToSlash(clips) + `"},"render":{"steps":8}}`
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)
	req := httptest.NewRequest(http.MethodGet, "/api/manju/shot/frame?config="+filepath.ToSlash(cfgPath)+"&episode=EP01&shot=1&t=0.5", nil)
	rr := httptest.NewRecorder()
	manjuShotFrameHandler(rr, req)
	if rr.Code != 404 {
		t.Fatalf("未渲染镜头应 404, got %d", rr.Code)
	}
}
