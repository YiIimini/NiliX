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

// 2026-09-03 视频管理:单镜编辑(字段白名单写回 + 台词逐句同步进提示词)与场景列表

func newEditProj(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(ManjuRootDir, name)
	_ = os.RemoveAll(dir)
	for _, d := range []string{"analysis", filepath.Join("assets", "scenes")} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"style":"real","render":{},"paths":{"workdir":"`+filepath.ToSlash(dir)+`",
		"analysis":"`+filepath.ToSlash(filepath.Join(dir, "analysis"))+`",
		"assets":"`+filepath.ToSlash(filepath.Join(dir, "assets"))+`"}}`), 0o644)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{},
		"scenes":     []any{map[string]any{"id": "大厅", "description": "金碧大厅", "image_prompt": "a grand hall"}},
		"shots": []any{
			map[string]any{"shot_id": 1, "scene": "大厅", "characters": []any{"林澈"}, "shot_size": "中景",
				"camera": "固定", "action": "他走进大厅", "dialogue": "林澈:你们来了。",
				"narration": "", "duration": 5,
				"h3_prompt": "subject_definitions:\n<Subject 1> is Lin Che in <Picture 1>.\n\naudio:\n林澈 says <d>[Chinese]你们来了。</d>\n\ndetailed_description:\nHe enters.\n"},
			map[string]any{"shot_id": 2, "scene": "大厅", "characters": []any{}, "shot_size": "远景",
				"camera": "固定", "action": "全景收尾", "dialogue": "", "narration": "夜色渐深。", "duration": 4,
				"h3_prompt": "subject_definitions:\n<Subject 1> is the hall in <Picture 1>.\n\ndetailed_description:\nThe hall.\n"},
		},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(dir, "analysis", "EP01_direct_plan.json"), b, 0o644)
	return name, cfg
}

func postEdit(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/manju/shot/edit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	manjuShotEditHandler(rr, req)
	return rr
}

func TestShotEditDialogueSync(t *testing.T) {
	_, cfg := newEditProj(t, "zz_shot_edit_test")
	// 改台词(单句)→ 自动逐句同步进 h3_prompt
	rr := postEdit(t, `{"config":"`+filepath.ToSlash(cfg)+`","episode":"EP01","shot":1,
		"fields":{"dialogue":"林澈：都退下吧。","duration":6}}`)
	if rr.Code != 200 {
		t.Fatalf("edit 状态 %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK           bool     `json:"ok"`
		Updated      []string `json:"updated"`
		PromptSynced int      `json:"prompt_synced"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.OK || resp.PromptSynced != 1 {
		t.Fatalf("台词同步未生效: %+v", resp)
	}
	// plan 落盘验证:dialogue/duration 更新 + h3_prompt 内嵌新台词、旧台词消失
	b, _ := os.ReadFile(filepath.Join(ManjuRootDir, "zz_shot_edit_test", "analysis", "EP01_direct_plan.json"))
	var plan map[string]any
	_ = json.Unmarshal(b, &plan)
	arr := plan["shots"].([]any)
	m := arr[0].(map[string]any)
	if m["dialogue"] != "林澈：都退下吧。" {
		t.Fatalf("dialogue 未写回: %v", m["dialogue"])
	}
	if n, _ := manjuToInt(m["duration"]); n != 6 {
		t.Fatalf("duration 未写回: %v", m["duration"])
	}
	hp := str(m["h3_prompt"])
	if !strings.Contains(hp, "都退下吧。") || strings.Contains(hp, "你们来了。") {
		t.Fatalf("h3_prompt 台词未同步: %s", hp)
	}
}

func TestShotEditPromptManualAndGuard(t *testing.T) {
	_, cfg := newEditProj(t, "zz_shot_edit_test2")
	// 显式 h3_prompt 手改优先(即使台词也变了,不做自动同步)
	rr := postEdit(t, `{"config":"`+filepath.ToSlash(cfg)+`","episode":"EP01","shot":1,
		"fields":{"dialogue":"林澈:新台词。","h3_prompt":"manual prompt"}}`)
	if rr.Code != 200 {
		t.Fatalf("edit 状态 %d: %s", rr.Code, rr.Body.String())
	}
	b, _ := os.ReadFile(filepath.Join(ManjuRootDir, "zz_shot_edit_test2", "analysis", "EP01_direct_plan.json"))
	var plan map[string]any
	_ = json.Unmarshal(b, &plan)
	m := plan["shots"].([]any)[0].(map[string]any)
	if m["h3_prompt"] != "manual prompt" {
		t.Fatalf("显式 h3_prompt 未落盘: %v", m["h3_prompt"])
	}
	// 时长越界拒绝
	if rr2 := postEdit(t, `{"config":"`+filepath.ToSlash(cfg)+`","episode":"EP01","shot":1,"fields":{"duration":0}}`); rr2.Code == 200 {
		t.Fatal("时长 0 应被拒绝")
	}
	// 不存在的镜头
	if rr3 := postEdit(t, `{"config":"`+filepath.ToSlash(cfg)+`","episode":"EP01","shot":9,"fields":{"action":"x"}}`); rr3.Code == 200 {
		t.Fatal("镜 9 不存在应报错")
	}
}

func TestSyncDialogueMismatch(t *testing.T) {
	hp, done, total := syncDialogueIntoPrompt("a <d>[Chinese]你好。</d> b", "甲:你好。", "甲:你好。\n乙:再见。")
	if done != 0 || total != 1 {
		t.Fatalf("句数不匹配应不同步: done=%d total=%d", done, total)
	}
	if strings.Contains(hp, "再见") {
		t.Fatal("句数不匹配时提示词不应被改动")
	}
	// 找不到旧句(提示词里没有)→ 跳过
	hp2, done2, total2 := syncDialogueIntoPrompt("no dialogue here", "甲:你好。", "甲:哈喽。")
	if done2 != 0 || total2 != 1 || hp2 != "no dialogue here" {
		t.Fatalf("未命中旧句应跳过: %d/%d", done2, total2)
	}
}

func TestScenesListAPI(t *testing.T) {
	_, cfg := newEditProj(t, "zz_scenes_test")
	// 场景图:大厅.png 存在,_end 不存在
	_ = os.WriteFile(filepath.Join(ManjuRootDir, "zz_scenes_test", "assets", "scenes", "大厅.png"), []byte("png"), 0o644)
	req := httptest.NewRequest(http.MethodGet, "/api/manju/scenes?config="+filepath.ToSlash(cfg), nil)
	rr := httptest.NewRecorder()
	manjuScenesHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("scenes 状态 %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Scenes []map[string]any `json:"scenes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Scenes) != 1 {
		t.Fatalf("场景应 1 项: %+v", resp.Scenes)
	}
	s := resp.Scenes[0]
	if s["id"] != "大厅" || s["has_image"] != true || s["has_end"] != false {
		t.Fatalf("场景状态不符: %+v", s)
	}
	if n, _ := manjuToInt(s["shots"]); n != 2 {
		t.Fatalf("大厅应被 2 镜引用: %v", s["shots"])
	}
	if s["description"] != "金碧大厅" {
		t.Fatalf("描述不符: %v", s["description"])
	}
}
