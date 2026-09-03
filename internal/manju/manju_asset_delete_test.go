package manju

// 资产管理删除清理回归(2026-09-02):
// ① output/delete scope=char:角色全部资产(主图/视图/正脸/候选)删除 + 前缀防误删
//    (张三≠张三丰)+ asset_map.characters 同步清理 + 幂等
// ② output/delete scope=scene:场景主图+尾帧删除 + asset_map.scenes 同步 + 前缀防误删
// ③ 运行中互斥(资产删除与渲染/资产阶段并发撕扯防护)
// ④ char_lib 批量删除(delete-batch:逐条+失败透出+all 清空)与来源项目字段(store 写入)

import (
	"nilix/internal/paths"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newAssetDeleteProj 构造带资产目录的临时项目,返回 (项目名, config路径)
func newAssetDeleteProj(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(paths.ManjuRootDir, name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "assets", "characters", "_gacha"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets", "scenes"), 0755); err != nil {
		t.Fatalf("MkdirAll scenes: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"style":"2.5d","render":{},"paths":{"workdir":"`+filepath.ToSlash(dir)+`"}}`), 0644)
	return name, cfg
}

func writeAssetFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("png"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestOutputDeleteCharScope(t *testing.T) {
	proj, cfg := newAssetDeleteProj(t, "zz_asset_del_test")
	charDir := filepath.Join(paths.ManjuRootDir, proj, "assets", "characters")
	for _, f := range []string{"张三.png", "张三_full.png", "张三_face.png", "张三_q.png", "张三丰.png"} {
		writeAssetFile(t, charDir, f)
	}
	writeAssetFile(t, filepath.Join(charDir, "_gacha"), "张三__s1.png")
	writeAssetFile(t, filepath.Join(charDir, "_gacha"), "张三丰__s2.png")
	amap := `{"characters":{"张三":"characters/张三.png","张三_face":"characters/张三_face.png","张三丰":"characters/张三丰.png"},"scenes":{}}`
	_ = os.WriteFile(filepath.Join(paths.ManjuRootDir, proj, "assets", "asset_map.json"), []byte(amap), 0644)

	w, out := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfg, "scope": "char", "path": "张三"})
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("HTTP %d %v", w.Code, out)
	}
	// 张三全部资产删除;张三丰不受影响(前缀防误删)
	for _, f := range []string{"张三.png", "张三_full.png", "张三_face.png", "张三_q.png"} {
		if fileExists(filepath.Join(charDir, f)) {
			t.Fatalf("未删除: %s", f)
		}
	}
	if !fileExists(filepath.Join(charDir, "张三丰.png")) {
		t.Fatal("前缀误删: 张三丰.png")
	}
	if fileExists(filepath.Join(charDir, "_gacha", "张三__s1.png")) {
		t.Fatal("抽卡候选未删")
	}
	if !fileExists(filepath.Join(charDir, "_gacha", "张三丰__s2.png")) {
		t.Fatal("候选前缀误删: 张三丰")
	}
	// asset_map.characters 同步清理
	b, _ := os.ReadFile(filepath.Join(paths.ManjuRootDir, proj, "assets", "asset_map.json"))
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("asset_map 解析失败")
	}
	cmap, _ := m["characters"].(map[string]any)
	if _, ok := cmap["张三"]; ok {
		t.Fatal("asset_map 未清 张三")
	}
	if _, ok := cmap["张三丰"]; !ok {
		t.Fatal("asset_map 误清 张三丰")
	}
	// 幂等:再删一次无资产不报错
	w2, out2 := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfg, "scope": "char", "path": "张三"})
	if w2.Code != 200 || len(anyArr(out2["removed"])) != 0 {
		t.Fatalf("幂等失败: %d %v", w2.Code, out2)
	}
}

func TestOutputDeleteSceneScope(t *testing.T) {
	proj, cfg := newAssetDeleteProj(t, "zz_scene_del_test")
	sceneDir := filepath.Join(paths.ManjuRootDir, proj, "assets", "scenes")
	for _, f := range []string{"客厅.png", "客厅_end.png", "客厅2.png"} {
		writeAssetFile(t, sceneDir, f)
	}
	amap := `{"characters":{},"scenes":{"客厅":"scenes/客厅.png","客厅2":"scenes/客厅2.png"}}`
	_ = os.WriteFile(filepath.Join(paths.ManjuRootDir, proj, "assets", "asset_map.json"), []byte(amap), 0644)

	w, out := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfg, "scope": "scene", "path": "客厅"})
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("HTTP %d %v", w.Code, out)
	}
	if fileExists(filepath.Join(sceneDir, "客厅.png")) || fileExists(filepath.Join(sceneDir, "客厅_end.png")) {
		t.Fatal("场景主图/尾帧未删")
	}
	if !fileExists(filepath.Join(sceneDir, "客厅2.png")) {
		t.Fatal("前缀误删: 客厅2.png")
	}
	b, _ := os.ReadFile(filepath.Join(paths.ManjuRootDir, proj, "assets", "asset_map.json"))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	smap, _ := m["scenes"].(map[string]any)
	if _, ok := smap["客厅"]; ok {
		t.Fatal("asset_map.scenes 未清 客厅")
	}
	if _, ok := smap["客厅2"]; !ok {
		t.Fatal("asset_map.scenes 误清 客厅2")
	}
}

func TestOutputDeleteAssetRunningGuard(t *testing.T) {
	proj, cfg := newAssetDeleteProj(t, "zz_asset_guard_test")
	charDir := filepath.Join(paths.ManjuRootDir, proj, "assets", "characters")
	writeAssetFile(t, charDir, "王五.png")
	fakeRunning(t, proj)
	for _, scope := range []string{"char", "scene"} {
		w, _ := doReq(t, "POST", "/api/manju/output/delete", map[string]any{"config": cfg, "scope": scope, "path": "王五"})
		if w.Code != 409 {
			t.Fatalf("scope=%s 运行中应 409 拒绝, got %d", scope, w.Code)
		}
	}
	if !fileExists(filepath.Join(charDir, "王五.png")) {
		t.Fatal("运行中删除未被拦住")
	}
}

func TestCharLibDeleteBatchAndSource(t *testing.T) {
	// paths.CharLibDir 全局临时替换(先例:全局路径局部赋值+defer 恢复,不污染真实库)
	oldLib := paths.CharLibDir
	paths.CharLibDir = filepath.Join(t.TempDir(), "char_lib")
	defer func() { paths.CharLibDir = oldLib }()

	// 手工构造两个库角色
	for _, n := range []string{"甲", "乙"} {
		d := manjuCharLibPath(n)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		writeAssetFile(t, d, n+".png")
		card, _ := json.Marshal(manjuCharLibCard{Card: map[string]any{"id": n}, Fingerprint: "fp_" + n})
		_ = os.WriteFile(manjuCharLibCardPath(n), card, 0644)
	}

	// 来源项目字段:store 入库时写入 ctx.project
	proj, cfg := newAssetDeleteProj(t, "zz_clib_store_test")
	ctx, err := newManjuCtx(cfg, "", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	writeAssetFile(t, filepath.Join(paths.ManjuRootDir, proj, "assets", "characters"), "李四.png")
	if err := ctx.manjuCharLibStore(map[string]any{"id": "李四", "image_prompt": "a man"}); err != nil {
		t.Fatalf("store: %v", err)
	}
	b, _ := os.ReadFile(manjuCharLibCardPath("李四"))
	var card manjuCharLibCard
	if json.Unmarshal(b, &card) != nil || card.SourceProject != proj {
		t.Fatalf("来源项目未写入: %s", string(b))
	}
	found := false
	for _, c := range manjuCharLibList() {
		if str(c["name"]) == "李四" && str(c["source_project"]) == proj {
			found = true
		}
	}
	if !found {
		t.Fatal("list 未透出 source_project")
	}

	// 批量删除:1 个存在 + 1 个不存在 → 逐条结果,失败不阻断
	w, out := doReq(t, "POST", "/api/manju/char-lib/delete-batch", map[string]any{"names": []string{"甲", "不存在"}})
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("HTTP %d %v", w.Code, out)
	}
	if d := anyArr(out["deleted"]); len(d) != 1 || str(d[0]) != "甲" {
		t.Fatalf("deleted 应只含 甲: %v", d)
	}
	if f := anyArr(out["failed"]); len(f) != 1 {
		t.Fatalf("failed 应含 1 项: %v", f)
	}
	if !fileExists(manjuCharLibCardPath("乙")) {
		t.Fatal("批量删除误伤未选中的 乙")
	}

	// all=true 清空全部
	w2, out2 := doReq(t, "POST", "/api/manju/char-lib/delete-batch", map[string]any{"all": true})
	if w2.Code != 200 || len(anyArr(out2["deleted"])) != 2 {
		t.Fatalf("清空失败: %d %v", w2.Code, out2)
	}
	if len(manjuCharLibList()) != 0 {
		t.Fatal("清空后库应为空")
	}
}
