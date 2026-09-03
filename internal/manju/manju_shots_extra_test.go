package manju

import (
	"nilix/internal/paths"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestShotsOverrideVideoFields 二期:镜头列表 API 补 override/qc_failed/video 字段
func TestShotsOverrideVideoFields(t *testing.T) {
	// config 必须位于 paths.ManjuRootDir/<项目> 下(manjuGuardConfig 护栏)
	dir := filepath.Join(paths.ManjuRootDir, "shots-extra-test")
	_ = os.RemoveAll(dir)
	analysis := filepath.Join(dir, "analysis")
	assets := filepath.Join(dir, "assets")
	clips := filepath.Join(dir, "clips")
	_ = os.MkdirAll(analysis, 0o755)
	_ = os.MkdirAll(assets, 0o755)
	_ = os.MkdirAll(clips, 0o755)
	_ = os.MkdirAll(filepath.Join(clips, "EP01"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "workdir"), 0o755)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{},
		"scenes":     []any{},
		"shots": []any{
			map[string]any{"shot_id": 1, "scene": "", "characters": []any{}, "shot_size": "中景",
				"camera": "固定", "action": "a", "dialogue": "", "narration": "", "duration": 5,
				"h3_prompt": "detailed_description:\nX\n"},
			map[string]any{"shot_id": 2, "scene": "", "characters": []any{}, "shot_size": "中景",
				"camera": "固定", "action": "b", "dialogue": "", "narration": "", "duration": 5,
				"h3_prompt": "detailed_description:\nY\n"},
		},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0o644)
	// 镜1 已渲染 + 有覆盖;镜2 未渲染
	_ = os.WriteFile(filepath.Join(clips, "EP01", "01.mp4"), []byte("v"), 0o644)
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(filepath.Join(dir, "workdir")) + `","analysis":"` + filepath.ToSlash(analysis) + `","assets":"` + filepath.ToSlash(assets) + `","clips":"` + filepath.ToSlash(clips) + `"},"render":{"steps":8}}`
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)
	mctx, _ := newManjuCtx(filepath.ToSlash(cfgPath), "EP01", "", "", "")
	_ = mctx.setShotOverride(1, manjuShotOverride{Seed: intPtr(7)})
	req := httptest.NewRequest(http.MethodGet, "/api/manju/shots?config="+filepath.ToSlash(cfgPath)+"&episode=EP01", nil)
	rr := httptest.NewRecorder()
	manjuShots(rr, req)
	if rr.Code != 200 {
		t.Fatalf("shots API 状态 %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Episodes []struct {
			Episode string `json:"episode"`
			Shots   []struct {
				ID       int    `json:"id"`
				Rendered bool   `json:"rendered"`
				Override bool   `json:"override"`
				QcFailed bool   `json:"qc_failed"`
				Video    string `json:"video"`
			} `json:"shots"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Episodes) == 0 || len(resp.Episodes[0].Shots) != 2 {
		t.Fatalf("应返回 2 镜: %s", rr.Body.String())
	}
	s1 := resp.Episodes[0].Shots[0]
	s2 := resp.Episodes[0].Shots[1]
	if !s1.Rendered || !s1.Override {
		t.Fatalf("镜1 应 rendered+override: %+v", s1)
	}
	if s1.Video == "" {
		t.Fatalf("镜1 已渲染应带 video 路径: %+v", s1)
	}
	if s2.Rendered || s2.Override || s2.Video != "" {
		t.Fatalf("镜2 应未渲染无覆盖无视频: %+v", s2)
	}
}

func intPtr(n int) *int { return &n }
