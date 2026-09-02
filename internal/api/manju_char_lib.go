package api

// 角色资产独立库(2026-09-02 用户需求:技能侧角色输出可直接复用已有角色,
// 不用每次都重新渲染)。
//
// 背景:每本书的角色定妆照/视图/Q版都是 Krea-2 逐张渲染(分钟级成本),同一
// 角色形象(同样的 image_prompt 定妆)在不同项目/续作里反复重渲染纯属浪费;
// 且换 seed 重渲还会造成跨项目形象漂移。角色资产库把「已定妆的角色」按
// 形象指纹持久化到自包含根 char_lib/<角色名>/,新项目里同名同形象角色直接
// 复制复用,零渲染成本、形象跨项目锁定一致。
//
// 目录布局:
//
//	char_lib/<角色名>/
//	  card.json       角色卡(原样)+ 形象指纹字段(见 manjuCharLibCard)
//	  <角色名>.png           主定妆
//	  <角色名>_front.png     正脸特写
//	  <角色名>_full.png      全身视图
//	  <角色名>_side.png      侧面视图
//	  <角色名>_detail.png    细节特写
//	  <角色名>_q.png         Q版(正角)
//	  <角色名>_form2.png     真身形态(有 second_form 时)
//
// 复用判定:角色卡 image_prompt(+q_form+second_form)归一后哈希 == 库中
// card.json 的 fingerprint → 同形象,复制全部资产进项目 assets/characters/,
// 跳过渲染。形象有变(改过提示词)自动视为新形象,重渲染后覆盖入库。
//
// 与 voice_lib 同构:权威副本在自包含根,项目目录只是工作副本;项目清理/
// 重建不影响库。CharLibDir 未解析(测试/工具)时回退 ManjuRootDir/char_lib。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manjuCharLibDir 角色资产库根目录(CharLibDir 未解析时回退 ManjuRootDir/char_lib)
func manjuCharLibDir() string {
	if CharLibDir != "" {
		return CharLibDir
	}
	return filepath.Join(ManjuRootDir, "char_lib")
}

// manjuCharLibCard 库内角色卡(原角色卡 + 形象指纹)
type manjuCharLibCard struct {
	Card        map[string]any `json:"card"`        // 原角色卡(plan.characters 条目)
	Fingerprint string         `json:"fingerprint"` // 形象指纹(定妆相关字段哈希)
	CreatedAt   int64          `json:"created_at"`  // 入库时间(unix)
}

// charLibCardSuffixes 库内资产文件名后缀(与项目 assets/characters 命名一致)
var charLibCardSuffixes = []string{"", "_front", "_full", "_side", "_detail", "_q", "_form2"}

// manjuCharFingerprint 角色形象指纹:定妆相关字段(提示词/q版/真身/物种/性别)归一
// 哈希——同名角色不同形象(改过提示词)指纹不同,不误复用。
func manjuCharFingerprint(m map[string]any) string {
	parts := []string{
		str(m["id"]),
		str(m["image_prompt"]),
		str(m["q_form"]),
		str(m["second_form"]),
		str(m["species"]),
		str(m["gender"]),
		str(m["age"]),
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

// manjuCharLibPath 角色在库中的目录
func manjuCharLibPath(cid string) string {
	return filepath.Join(manjuCharLibDir(), sanitizeFileName(cid))
}

// manjuCharLibCardPath 角色库卡文件路径
func manjuCharLibCardPath(cid string) string {
	return filepath.Join(manjuCharLibPath(cid), "card.json")
}

// manjuCharLibMatch 查库:角色在库中存在且形象指纹匹配 → 返回库目录;否则 ""
func manjuCharLibMatch(m map[string]any) string {
	cid := str(m["id"])
	if cid == "" {
		return ""
	}
	libDir := manjuCharLibPath(cid)
	cardPath := manjuCharLibCardPath(cid)
	if !fileExists(cardPath) {
		return ""
	}
	b, err := os.ReadFile(cardPath)
	if err != nil {
		return ""
	}
	var card manjuCharLibCard
	if json.Unmarshal(b, &card) != nil || card.Fingerprint == "" {
		return ""
	}
	if card.Fingerprint != manjuCharFingerprint(m) {
		return "" // 形象已变:不复用,重新渲染后覆盖入库
	}
	// 库内至少要有主图才视为可用
	if !fileExists(filepath.Join(libDir, cid+".png")) {
		return ""
	}
	return libDir
}

// manjuCharLibReuse 从库复制角色资产到项目 assets/characters(2026-09-02):
// 返回复制文件数;0 = 无可用资产。复制主图+正脸+视图+Q版+真身,并回写 asset_map。
// 调用时机:stageAssets 角色定妆前(库命中则跳过渲染)。
func (ctx *manjuCtx) manjuCharLibReuse(m map[string]any, cmap map[string]any, lg *manjuLogger) int {
	libDir := manjuCharLibMatch(m)
	if libDir == "" {
		return 0
	}
	cid := str(m["id"])
	dstDir := filepath.Join(ctx.assetsDir, "characters")
	_ = os.MkdirAll(dstDir, 0o755)
	n := 0
	for _, suf := range charLibCardSuffixes {
		name := cid + suf + ".png"
		src := filepath.Join(libDir, name)
		if !fileExists(src) {
			continue
		}
		if err := copyFile(src, filepath.Join(dstDir, name)); err != nil {
			continue
		}
		n++
		if cmap != nil {
			rel := "characters/" + name
			key := cid
			if suf != "" {
				key = cid + suf
			}
			cmap[key] = rel
		}
	}
	if n > 0 && lg != nil {
		lg.logf(fmt.Sprintf("♻️ 角色 %s 复用资产库已有形象(%d 张,零渲染)——提示词变更后自动重渲覆盖入库", cid, n))
	}
	return n
}

// manjuCharLibStore 角色资产入库(2026-09-02):把项目 assets/characters 下的
// 该角色全部资产 + 角色卡复制到 char_lib/<角色名>/。调用时机:角色全部资产
// (主图+视图+Q版)生成完成后。幂等(重复入库覆盖同指纹)。
func (ctx *manjuCtx) manjuCharLibStore(m map[string]any) error {
	if CharLibDir == "" && ManjuRootDir == "" {
		return nil // 路径未解析(测试环境):跳过
	}
	cid := str(m["id"])
	if cid == "" {
		return nil
	}
	libDir := manjuCharLibPath(cid)
	_ = os.MkdirAll(libDir, 0o755)
	srcDir := filepath.Join(ctx.assetsDir, "characters")
	n := 0
	for _, suf := range charLibCardSuffixes {
		name := cid + suf + ".png"
		src := filepath.Join(srcDir, name)
		if !fileExists(src) {
			continue
		}
		if err := copyFile(src, filepath.Join(libDir, name)); err == nil {
			n++
		}
	}
	if n == 0 {
		return nil // 无资产可入库
	}
	card := manjuCharLibCard{
		Card:        m,
		Fingerprint: manjuCharFingerprint(m),
		CreatedAt:   nowUnix(),
	}
	b, _ := json.MarshalIndent(card, "", "  ")
	_ = os.WriteFile(manjuCharLibCardPath(cid), b, 0o644)
	return nil
}

// manjuCharLibList 资产库清单(前端角色资产库弹窗数据源):
// [{name, files:[...], fingerprint 前8, created_at}] 按名字排序。
func manjuCharLibList() []map[string]any {
	root := manjuCharLibDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return []map[string]any{}
	}
	var out []map[string]any
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		cardPath := filepath.Join(root, name, "card.json")
		card := manjuCharLibCard{}
		if b, err := os.ReadFile(cardPath); err == nil {
			_ = json.Unmarshal(b, &card)
		}
		var files []string
		if fe, err := os.ReadDir(filepath.Join(root, name)); err == nil {
			for _, f := range fe {
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".png") {
					files = append(files, f.Name())
				}
			}
		}
		sort.Strings(files)
		fp := ""
		if len(card.Fingerprint) >= 8 {
			fp = card.Fingerprint[:8]
		}
		// 主图缩略图(卡片封面;无主图取首个文件)
		main := ""
		for _, f := range files {
			if f == name+".png" {
				main = f
				break
			}
		}
		if main == "" && len(files) > 0 {
			main = files[0]
		}
		out = append(out, map[string]any{
			"name":       name,
			"files":      files,
			"fingerprint": fp,
			"created_at": card.CreatedAt,
			"id":         str(card.Card["id"]),
			"main":       main,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return str(out[i]["name"]) < str(out[j]["name"])
	})
	return out
}

// manjuCharLibDelete 删除库中角色(2026-09-02 前端「从资产库删除」)
func manjuCharLibDelete(cid string) error {
	if cid == "" || strings.ContainsAny(cid, `\/`) {
		return fmt.Errorf("非法角色名")
	}
	libDir := manjuCharLibPath(cid)
	if !fileExists(filepath.Join(libDir, "card.json")) {
		return fmt.Errorf("资产库无此角色: %s", cid)
	}
	return os.RemoveAll(libDir)
}

// manjuCharLibDetail 单角色详情(2026-09-02 独立资产库管理:点击卡片预览详情):
// 返回完整角色卡(card.json)+ 资产文件清单(相对库路径,前端预览用)
func manjuCharLibDetail(cid string) (map[string]any, error) {
	if cid == "" || strings.ContainsAny(cid, `\/`) {
		return nil, fmt.Errorf("非法角色名")
	}
	libDir := manjuCharLibPath(cid)
	cardPath := manjuCharLibCardPath(cid)
	if !fileExists(cardPath) {
		return nil, fmt.Errorf("资产库无此角色: %s", cid)
	}
	b, err := os.ReadFile(cardPath)
	if err != nil {
		return nil, err
	}
	var card manjuCharLibCard
	if err := json.Unmarshal(b, &card); err != nil {
		return nil, err
	}
	var files []string
	if fe, err := os.ReadDir(libDir); err == nil {
		for _, f := range fe {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".png") {
				files = append(files, f.Name())
			}
		}
	}
	sort.Strings(files)
	// 主图优先(详情弹窗大图)
	main := ""
	for _, f := range files {
		if f == cid+".png" {
			main = f
			break
		}
	}
	if main == "" && len(files) > 0 {
		main = files[0]
	}
	return map[string]any{
		"name":        cid,
		"card":        card.Card,
		"fingerprint": card.Fingerprint,
		"created_at":  card.CreatedAt,
		"files":       files,
		"main":        main,
	}, nil
}

// manjuCharLibAssetHandler 库内资产图片预览(2026-09-02):
// GET /api/manju/char-lib/asset?name=<角色>&file=<文件名>
func manjuCharLibAssetHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	if name == "" || strings.ContainsAny(name, `\/`) || file == "" || strings.Contains(file, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	p := filepath.Join(manjuCharLibPath(name), filepath.Base(file))
	b, err := os.ReadFile(p)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "文件不存在"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// manjuCharLibHandler GET /api/manju/char-lib/list → 资产库清单(全局,跨项目)
func manjuCharLibHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"chars": manjuCharLibList()})
}

// manjuCharLibDetailHandler GET /api/manju/char-lib/detail?name= → 单角色详情
func manjuCharLibDetailHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	d, err := manjuCharLibDetail(name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// manjuCharLibDeleteHandler POST /api/manju/char-lib/delete {name}
func manjuCharLibDeleteHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := manjuCharLibDelete(body.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// manjuCharLibImportHandler POST /api/manju/char-lib/import
// {config, name} → 把资产库角色导入当前项目:
//  ① 角色卡合并进 plan.characters(同名替换,新名追加);
//  ② 复制库内全部资产到项目 assets/characters(asset_map 回写);
//  ③ 落盘 plan + characters JSON。
// 之后角色管理弹窗/渲染管线直接可用(零渲染)。
func manjuCharLibImportHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config string `json:"config"`
		Name   string `json:"name"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, err := newManjuCtx(body.Config, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if body.Name == "" || strings.ContainsAny(body.Name, `\/`) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "非法角色名"})
		return
	}
	// 库内角色卡
	detail, err := manjuCharLibDetail(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	card, _ := detail["card"].(map[string]any)
	if card == nil || str(card["id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "资产库角色卡缺失"})
		return
	}
	card["id"] = body.Name // 库目录名=角色 id(防 card.id 与目录不一致)
	// 读当前 plan
	plan, _, err := ctx.loadPlan()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "方案未生成: " + err.Error()})
		return
	}
	// 合并角色卡(同名替换,新名追加;保持其它卡不动)
	charsArr, _ := plan["characters"].([]any)
	found := false
	for _, x := range charsArr {
		if m, ok := x.(map[string]any); ok && str(m["id"]) == body.Name {
			m = card
			found = true
			break
		}
	}
	if !found {
		charsArr = append(charsArr, card)
		plan["characters"] = charsArr
	}
	// 复制资产到项目 + 回写 asset_map
	amap := map[string]any{"characters": map[string]any{}, "scenes": map[string]any{}}
	if b, err := os.ReadFile(filepath.Join(ctx.assetsDir, "asset_map.json")); err == nil {
		_ = json.Unmarshal(b, &amap)
	}
	cmap, _ := amap["characters"].(map[string]any)
	if cmap == nil {
		cmap = map[string]any{}
	}
	n := ctx.manjuCharLibReuse(card, cmap, nil)
	if n == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "资产复制失败(库内无资产?)"})
		return
	}
	amap["characters"] = cmap
	if b, err := json.MarshalIndent(amap, "", "  "); err == nil {
		_ = os.MkdirAll(ctx.assetsDir, 0o755)
		_ = os.WriteFile(filepath.Join(ctx.assetsDir, "asset_map.json"), b, 0o644)
	}
	// 落盘 plan + characters JSON(渲染管线/角色管理直接可用)
	if err := ctx.writePlan(plan); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx.writeCharactersJSON(plan)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": body.Name, "files": n})
}
