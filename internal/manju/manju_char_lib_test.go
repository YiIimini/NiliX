package manju

import (
	"nilix/internal/paths"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCharLibFingerprint 形象指纹:提示词变化 → 指纹变化(同名不同形象不误复用)
func TestCharLibFingerprint(t *testing.T) {
	a := map[string]any{"id": "沈照", "image_prompt": "a 19-year-old with platinum-white hair", "q_form": "chibi", "species": "人", "gender": "男", "age": "19岁"}
	b := map[string]any{"id": "沈照", "image_prompt": "a 19-year-old with black hair", "q_form": "chibi", "species": "人", "gender": "男", "age": "19岁"}
	if manjuCharFingerprint(a) == manjuCharFingerprint(b) {
		t.Fatal("形象指纹:提示词不同应不同")
	}
	c := map[string]any{"id": "沈照", "image_prompt": "a 19-year-old with platinum-white hair", "q_form": "chibi", "species": "人", "gender": "男", "age": "19岁"}
	if manjuCharFingerprint(a) != manjuCharFingerprint(c) {
		t.Fatal("形象指纹:相同卡应一致")
	}
}

// TestCharLibStoreReuse 入库→复用全链路(2026-09-02):
// ①项目资产生成后入库(char_lib/<角色>/)
// ②同形象新项目直接复用(复制资产+asset_map 回写)
// ③形象变更不复用(指纹不匹配)
func TestCharLibStoreReuse(t *testing.T) {
	oldCharLib := paths.CharLibDir
	paths.CharLibDir = filepath.Join(t.TempDir(), "char_lib")
	defer func() { paths.CharLibDir = oldCharLib }()

	projDir := filepath.Join(t.TempDir(), "proj")
	assetsDir := filepath.Join(projDir, "assets", "characters")
	_ = os.MkdirAll(assetsDir, 0o755)
	card := map[string]any{
		"id": "沈照", "image_prompt": "a 19-year-old with platinum-white hair",
		"q_form": "chibi", "species": "人", "gender": "男", "age": "19岁",
	}
	ctx := &manjuCtx{workdir: projDir, assetsDir: filepath.Join(projDir, "assets")}
	// 生成假资产(主图+视图)
	_ = os.WriteFile(filepath.Join(assetsDir, "沈照.png"), []byte("main"), 0o644)
	_ = os.WriteFile(filepath.Join(assetsDir, "沈照_front.png"), []byte("front"), 0o644)
	_ = os.WriteFile(filepath.Join(assetsDir, "沈照_full.png"), []byte("full"), 0o644)
	// 入库
	if err := ctx.manjuCharLibStore(card); err != nil {
		t.Fatal(err)
	}
	libDir := manjuCharLibPath("沈照")
	if !fileExists(filepath.Join(libDir, "沈照.png")) {
		t.Fatalf("主图未入库: %s", libDir)
	}
	if !fileExists(manjuCharLibCardPath("沈照")) {
		t.Fatal("card.json 未入库")
	}
	// 清空项目资产(模拟新项目)
	_ = os.RemoveAll(assetsDir)
	_ = os.MkdirAll(assetsDir, 0o755)
	// 复用:同形象 → 复制
	cmap := map[string]any{}
	n := ctx.manjuCharLibReuse(card, cmap, nil)
	if n == 0 {
		t.Fatal("同形象应复用资产")
	}
	if !fileExists(filepath.Join(assetsDir, "沈照.png")) || !fileExists(filepath.Join(assetsDir, "沈照_front.png")) {
		t.Fatalf("复用后主图/正脸缺失")
	}
	if cmap["沈照"] == nil {
		t.Fatalf("复用应回写 asset_map")
	}
	// 形象变更 → 不复用
	changed := map[string]any{
		"id": "沈照", "image_prompt": "a 19-year-old with black hair",
		"q_form": "chibi", "species": "人", "gender": "男", "age": "19岁",
	}
	if n2 := ctx.manjuCharLibReuse(changed, map[string]any{}, nil); n2 != 0 {
		t.Fatal("形象变更不应复用")
	}
	// 清单 API 数据
	list := manjuCharLibList()
	if len(list) != 1 || str(list[0]["name"]) != "沈照" {
		t.Fatalf("清单应含沈照: %+v", list)
	}
	if files, ok := list[0]["files"].([]string); !ok || len(files) < 2 {
		t.Fatalf("清单文件数应 ≥2: %+v", list[0]["files"])
	}
	// 删除
	if err := manjuCharLibDelete("沈照"); err != nil {
		t.Fatal(err)
	}
	if len(manjuCharLibList()) != 0 {
		t.Fatal("删除后清单应为空")
	}
}

// TestCharLibDetailAsset 详情/预览 API(2026-09-02 统一管理):detail 返回完整
// 角色卡+文件清单+主图;asset 返回图片内容;非法参数 400。
func TestCharLibDetailAsset(t *testing.T) {
	oldCharLib := paths.CharLibDir
	paths.CharLibDir = filepath.Join(t.TempDir(), "char_lib")
	defer func() { paths.CharLibDir = oldCharLib }()

	projDir := filepath.Join(t.TempDir(), "proj")
	assetsDir := filepath.Join(projDir, "assets", "characters")
	_ = os.MkdirAll(assetsDir, 0o755)
	card := map[string]any{
		"id": "苏晚萤", "image_prompt": "a 22-year-old woman with long dark hair",
		"q_form": "chibi", "species": "人", "gender": "女", "age": "22岁",
	}
	ctx := &manjuCtx{workdir: projDir, assetsDir: filepath.Join(projDir, "assets")}
	_ = os.WriteFile(filepath.Join(assetsDir, "苏晚萤.png"), []byte("main-data"), 0o644)
	_ = os.WriteFile(filepath.Join(assetsDir, "苏晚萤_q.png"), []byte("q-data"), 0o644)
	if err := ctx.manjuCharLibStore(card); err != nil {
		t.Fatal(err)
	}
	// detail
	d, err := manjuCharLibDetail("苏晚萤")
	if err != nil {
		t.Fatal(err)
	}
	if str(d["name"]) != "苏晚萤" {
		t.Fatalf("detail name 错: %+v", d)
	}
	if str(d["main"]) != "苏晚萤.png" {
		t.Fatalf("main 应为苏晚萤.png: %+v", d["main"])
	}
	if files, ok := d["files"].([]string); !ok || len(files) != 2 {
		t.Fatalf("files 应为 2: %+v", d["files"])
	}
	cm, _ := d["card"].(map[string]any)
	if str(cm["image_prompt"]) == "" {
		t.Fatalf("detail 应含角色卡提示词")
	}
	// asset 预览
	b, err := os.ReadFile(filepath.Join(manjuCharLibPath("苏晚萤"), "苏晚萤.png"))
	if err != nil || string(b) != "main-data" {
		t.Fatalf("asset 内容不符")
	}
	// 非法参数
	if _, err := manjuCharLibDetail("../.."); err == nil {
		t.Fatal("非法角色名应报错")
	}
}

// TestCharLibImport 导入 API(2026-09-02 角色管理弹窗「从资产库导入」):
// 库中角色 → 导入当前项目:角色卡合并进 plan.characters + 资产复制 + asset_map 回写。
func TestCharLibImport(t *testing.T) {
	oldCharLib := paths.CharLibDir
	paths.CharLibDir = filepath.Join(t.TempDir(), "char_lib")
	defer func() { paths.CharLibDir = oldCharLib }()

	// 建库:先在一个"源项目"入库角色
	srcProj := filepath.Join(t.TempDir(), "src")
	srcAssets := filepath.Join(srcProj, "assets", "characters")
	_ = os.MkdirAll(srcAssets, 0o755)
	card := map[string]any{
		"id": "涂山杳杳", "image_prompt": "a fox spirit with nine tails", "q_form": "chibi",
		"species": "灵宠", "gender": "女", "age": "16岁",
	}
	srcCtx := &manjuCtx{workdir: srcProj, assetsDir: filepath.Join(srcProj, "assets")}
	_ = os.WriteFile(filepath.Join(srcAssets, "涂山杳杳.png"), []byte("m"), 0o644)
	_ = os.WriteFile(filepath.Join(srcAssets, "涂山杳杳_q.png"), []byte("q"), 0o644)
	if err := srcCtx.manjuCharLibStore(card); err != nil {
		t.Fatal(err)
	}
	// 目标项目:已有 plan(含一个旧角色)
	dstProj := filepath.Join(t.TempDir(), "dst")
	analysis := filepath.Join(dstProj, "analysis")
	assets := filepath.Join(dstProj, "assets")
	_ = os.MkdirAll(analysis, 0o755)
	_ = os.MkdirAll(filepath.Join(assets, "characters"), 0o755)
	plan := map[string]any{
		"chapters": "script", "script_parse_ver": manjuScriptParseVer,
		"characters": []any{map[string]any{"id": "旧角色", "image_prompt": "old"}},
		"scenes":     []any{},
		"shots":      []any{},
	}
	b, _ := json.Marshal(plan)
	_ = os.WriteFile(filepath.Join(analysis, "EP01_direct_plan.json"), b, 0o644)
	cfg := `{"style":"real","paths":{"workdir":"` + filepath.ToSlash(dstProj) + `","analysis":"` + filepath.ToSlash(analysis) + `","assets":"` + filepath.ToSlash(assets) + `"},"render":{"steps":8}}`
	cfgPath := filepath.Join(dstProj, "config.json")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o644)
	ctx, _ := newManjuCtx(filepath.ToSlash(cfgPath), "EP01", "", "", "")
	// 调导入
	detail, err := manjuCharLibDetail("涂山杳杳")
	if err != nil {
		t.Fatal(err)
	}
	cardM, _ := detail["card"].(map[string]any)
	cardM["id"] = "涂山杳杳"
	plan2, _, err := ctx.loadPlan()
	if err != nil {
		t.Fatal(err)
	}
	charsArr, _ := plan2["characters"].([]any)
	found := false
	for _, x := range charsArr {
		if m, ok := x.(map[string]any); ok && str(m["id"]) == "涂山杳杳" {
			found = true
		}
	}
	if !found {
		charsArr = append(charsArr, cardM)
		plan2["characters"] = charsArr
	}
	// 资产复制
	cmap := map[string]any{}
	n := ctx.manjuCharLibReuse(cardM, cmap, nil)
	if n != 2 {
		t.Fatalf("应复制 2 张资产, got %d", n)
	}
	if !fileExists(filepath.Join(assets, "characters", "涂山杳杳.png")) {
		t.Fatal("主图未复制到项目")
	}
	if err := ctx.writePlan(plan2); err != nil {
		t.Fatal(err)
	}
	// 验证 plan 已含新角色
	plan3, _, _ := ctx.loadPlan()
	chars3, _ := plan3["characters"].([]any)
	names := []string{}
	for _, x := range chars3 {
		if m, ok := x.(map[string]any); ok {
			names = append(names, str(m["id"]))
		}
	}
	hasName := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	if !hasName("涂山杳杳") || !hasName("旧角色") {
		t.Fatalf("导入后 plan 应含新旧角色: %v", names)
	}
}

// 2026-09-03 跨名复用:名字不同但六字段形象一致(技能侧为配角采纳库内形象仅换名)
// → 二级形象指纹命中,复制资产按本项目角色名落地(宋明堂×裴照同脸实锤的根治)。
func TestCharLibCrossNameReuse(t *testing.T) {
	dir := t.TempDir()
	old := paths.CharLibDir
	paths.CharLibDir = filepath.Join(dir, "char_lib")
	t.Cleanup(func() { paths.CharLibDir = old })

	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "proj", "assets")}
	// 甲入库(完整六字段形象)
	jia := map[string]any{"id": "甲", "gender": "男", "age": "23岁", "species": "人",
		"image_prompt": "a 23-year-old male Chinese cultivator with jade hairpin", "q_form": "chibi cultivator"}
	os.MkdirAll(filepath.Join(ctx.assetsDir, "characters"), 0o755)
	os.WriteFile(filepath.Join(ctx.assetsDir, "characters", "甲.png"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(ctx.assetsDir, "characters", "甲_q.png"), []byte("png"), 0o644)
	if err := ctx.manjuCharLibStore(jia); err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	// 乙=不同名+六字段逐字一致(技能侧采纳库内形象仅换名)
	yi := map[string]any{"id": "乙", "gender": "男", "age": "23岁", "species": "人",
		"image_prompt": "a 23-year-old male Chinese cultivator with jade hairpin", "q_form": "chibi cultivator"}
	n := ctx.manjuCharLibReuse(yi, nil, &manjuLogger{state: manjuState})
	if n != 2 {
		t.Fatalf("跨名复用应复制 2 张, got %d", n)
	}
	for _, f := range []string{"乙.png", "乙_q.png"} {
		if !fileExists(filepath.Join(ctx.assetsDir, "characters", f)) {
			t.Errorf("应按本项目角色名落地 %s", f)
		}
	}
	// 丙=名字不同+形象有差(改一个词)→ 不复用
	bing := map[string]any{"id": "丙", "gender": "男", "age": "23岁", "species": "人",
		"image_prompt": "a 23-year-old male Chinese cultivator with golden hairpin", "q_form": "chibi cultivator"}
	if k := ctx.manjuCharLibReuse(bing, nil, nil); k != 0 {
		t.Errorf("形象有差(逐字不一致)不得跨名复用, got %d", k)
	}
	// 空卡(六字段全空)不参与跨名匹配
	empty := map[string]any{"id": "丁"}
	if _, lc := manjuCharLibMatch(empty); lc != "" {
		t.Errorf("空卡不得跨名匹配")
	}
}
