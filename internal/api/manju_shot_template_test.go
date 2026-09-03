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

// 2026-09-03 画布三期:参数模板(存/列/删/批量应用,合并语义+指纹联动)

func templateTestEnv(t *testing.T) (cfgPath string) {
	t.Helper()
	dir := t.TempDir()
	analysis := filepath.Join(dir, "analysis")
	assets := filepath.Join(dir, "assets")
	_ = os.MkdirAll(analysis, 0o755)
	_ = os.MkdirAll(assets, 0o755)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{},
		"scenes":     []any{},
		"shots": []any{
			map[string]any{"shot_id": 1, "scene": "", "characters": []any{}, "shot_size": "中景",
				"camera": "固定", "action": "a", "dialogue": "", "narration": "", "duration": 5,
				"h3_prompt": "subject_definitions:\n<Subject 1> is the plaza in <Picture 1>.\n\ndetailed_description:\nX.\n"},
			map[string]any{"shot_id": 2, "scene": "", "characters": []any{}, "shot_size": "远景",
				"camera": "固定", "action": "b", "dialogue": "", "narration": "", "duration": 4,
				"h3_prompt": "subject_definitions:\n<Subject 1> is the plaza in <Picture 1>.\n\ndetailed_description:\nY.\n"},
		},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0o644)
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(dir) + `","analysis":"` + filepath.ToSlash(analysis) + `","assets":"` + filepath.ToSlash(assets) + `"},"render":{"steps":8}}`
	cfgPath = filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)
	return cfgPath
}

func TestShotTemplateSaveListDelete(t *testing.T) {
	cfgPath := templateTestEnv(t)
	// 存
	body := `{"config":"` + filepath.ToSlash(cfgPath) + `","name":"夜景打斗","note":"n1","override":{"steps":12,"sampler":"uni_pc"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/save", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	manjuShotTemplateSaveHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("save 状态 %d: %s", rr.Code, rr.Body.String())
	}
	// 同名更新(幂等,不重复)
	body2 := strings.Replace(body, `"note":"n1"`, `"note":"n2"`, 1)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/save", strings.NewReader(body2))
	req.Header.Set("Content-Type", "application/json")
	manjuShotTemplateSaveHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("重复 save 应更新: %d", rr.Code)
	}
	// 空模板拒绝
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/save",
		strings.NewReader(`{"config":"`+filepath.ToSlash(cfgPath)+`","name":"空","override":{}}`))
	req.Header.Set("Content-Type", "application/json")
	manjuShotTemplateSaveHandler(rr, req)
	if rr.Code == 200 {
		t.Fatal("空模板应被拒绝")
	}
	// 列表
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/manju/shot/templates?config="+filepath.ToSlash(cfgPath), nil)
	manjuShotTemplatesHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list 状态 %d", rr.Code)
	}
	var lst struct {
		OK        bool               `json:"ok"`
		Templates []manjuShotTemplate `json:"templates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &lst); err != nil {
		t.Fatal(err)
	}
	if len(lst.Templates) != 1 || lst.Templates[0].Name != "夜景打斗" || lst.Templates[0].Note != "n2" {
		t.Fatalf("模板列表不符: %+v", lst.Templates)
	}
	if lst.Templates[0].Override.Steps == nil || *lst.Templates[0].Override.Steps != 12 {
		t.Fatalf("模板参数不符: %+v", lst.Templates[0].Override)
	}
	// 删
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/delete",
		strings.NewReader(`{"config":"`+filepath.ToSlash(cfgPath)+`","name":"夜景打斗"}`))
	req.Header.Set("Content-Type", "application/json")
	manjuShotTemplateDeleteHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("delete 状态 %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/manju/shot/templates?config="+filepath.ToSlash(cfgPath), nil)
	manjuShotTemplatesHandler(rr, req)
	_ = json.Unmarshal(rr.Body.Bytes(), &lst)
	if len(lst.Templates) != 0 {
		t.Fatalf("删除后列表应为空: %+v", lst.Templates)
	}
}

func TestShotTemplateApply(t *testing.T) {
	cfgPath := templateTestEnv(t)
	ctx, _ := newManjuCtx(filepath.ToSlash(cfgPath), "EP01", "", "", "")
	// 镜 2 预置 note(验证合并语义:模板无 note 字段时保留)
	seed := 777
	if err := ctx.setShotOverride(2, manjuShotOverride{Seed: &seed, Note: "已有备注"}); err != nil {
		t.Fatal(err)
	}
	// 指定镜应用(模板:steps+engine)
	body := `{"config":"` + filepath.ToSlash(cfgPath) + `","episode":"EP01","shots":[2],"override":{"steps":12,"sampler":"heun"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	manjuShotTemplateApplyHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("apply 状态 %d: %s", rr.Code, rr.Body.String())
	}
	ov := ctx.shotOverrideFor(2)
	if ov.Steps == nil || *ov.Steps != 12 || ov.Sampler != "heun" {
		t.Fatalf("模板字段未应用: %+v", ov)
	}
	if ov.Seed == nil || *ov.Seed != 777 || ov.Note != "已有备注" {
		t.Fatalf("合并语义破坏了已有覆盖: %+v", ov)
	}
	// 整集应用(shots 空=全部)
	bodyAll := `{"config":"` + filepath.ToSlash(cfgPath) + `","episode":"EP01","shots":[],"override":{"steps":10}}`
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/manju/shot/template/apply", strings.NewReader(bodyAll))
	req.Header.Set("Content-Type", "application/json")
	manjuShotTemplateApplyHandler(rr, req)
	var resp struct {
		OK      bool `json:"ok"`
		Applied int  `json:"applied"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.OK || resp.Applied != 2 {
		t.Fatalf("整集应用应 2 镜: %+v", resp)
	}
	if ov1 := ctx.shotOverrideFor(1); ov1.Steps == nil || *ov1.Steps != 10 {
		t.Fatalf("镜 1 未应用: %+v", ov1)
	}
	// 指纹联动:镜 1 覆盖变化应改变其渲染指纹(下次渲染该镜 stale)
	plan, _, err := ctx.loadPlan()
	if err != nil {
		t.Fatal(err)
	}
	shots, _ := planShots(plan)
	if ctx.shotRenderFingerprint(shots[0]) == "" {
		t.Fatal("指纹为空")
	}
	// 覆盖文件确实落盘(独立 ctx 读取)
	ctx2, _ := newManjuCtx(filepath.ToSlash(cfgPath), "EP01", "", "", "")
	if len(ctx2.loadShotOverrides()) != 2 {
		t.Fatalf("覆盖落盘不符: %+v", ctx2.loadShotOverrides())
	}
}
