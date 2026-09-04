package manju

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
// 重建不影响库。paths.CharLibDir 未解析(测试/工具)时回退 paths.ManjuRootDir/char_lib。

import (
	"nilix/internal/paths"
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

// manjuCharLibDir 角色资产库根目录(paths.CharLibDir 未解析时回退 paths.ManjuRootDir/char_lib)
func manjuCharLibDir() string {
	if paths.CharLibDir != "" {
		return paths.CharLibDir
	}
	return filepath.Join(paths.ManjuRootDir, "asset_lib", "characters")
}

// manjuCharLibCard 库内角色卡(原角色卡 + 形象指纹)
type manjuCharLibCard struct {
	Card         map[string]any `json:"card"`                    // 原角色卡(plan.characters 条目)
	Fingerprint  string         `json:"fingerprint"`             // 形象指纹(定妆相关字段哈希)
	CreatedAt    int64          `json:"created_at"`              // 入库时间(unix)
	SourceProject string        `json:"source_project,omitempty"` // 来源项目(2026-09-02:管理弹窗展示;老条目无此字段=—)
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

// manjuCharLookFingerprint 形象指纹(2026-09-03,跨名复用):同 manjuCharFingerprint
// 但不含角色名——名字不同而六字段(提示词/q版/真身/物种/性别/年龄)逐字一致=同一张脸。
// 用户实锤:宋明堂×裴照(两本各写一套近似提示词各渲一张脸)=资产库没发挥价值。
// 技能侧创作配角时主动采纳库内相似形象(逐字照抄七字段仅换名)→ 此处指纹命中
// → 零渲染复用。主角/独有印记角色的"是否采纳"由技能侧把守(平台只做确定性匹配)。
func manjuCharLookFingerprint(m map[string]any) string {
	parts := []string{
		strings.TrimSpace(str(m["image_prompt"])),
		strings.TrimSpace(str(m["q_form"])),
		strings.TrimSpace(str(m["second_form"])),
		strings.TrimSpace(str(m["species"])),
		strings.TrimSpace(str(m["gender"])),
		strings.TrimSpace(str(m["age"])),
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

// manjuCharLibMatch 查库两级匹配(2026-09-03 升级跨名复用):
// ①同名+形象指纹一致(既有语义:同名改形象=重渲覆盖入库);
// ②名字不同但形象指纹(六字段去名)逐字一致=同一张脸(技能侧为配角采纳库内形象、
//   仅换名创建时命中)→ 返回 (库目录, 库内角色名) 跨名复用,零渲染。
// 返回 ("", "") = 无可复用。
func manjuCharLibMatch(m map[string]any) (string, string) {
	cid := str(m["id"])
	if cid == "" {
		return "", ""
	}
	libDir := manjuCharLibPath(cid)
	cardPath := manjuCharLibCardPath(cid)
	if fileExists(cardPath) {
		if b, err := os.ReadFile(cardPath); err == nil {
			var card manjuCharLibCard
			if json.Unmarshal(b, &card) == nil && card.Fingerprint != "" && card.Fingerprint == manjuCharFingerprint(m) {
				// 库内至少要有主图才视为可用
				if fileExists(filepath.Join(libDir, cid+".png")) {
					return libDir, cid
				}
			}
		}
	}
	// 二级:形象指纹跨名匹配(遍历库目录,条目量级小;主图存在才候选)
	look := manjuCharLookFingerprint(m)
	if look == manjuCharLookFingerprint(nil) {
		return "", "" // 六字段全空:不做跨名匹配(防空卡乱命中)
	}
	ents, err := os.ReadDir(manjuCharLibDir())
	if err != nil {
		return "", ""
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		libCid := e.Name()
		if libCid == sanitizeFileName(cid) {
			continue // 同名已走一级
		}
		b, err := os.ReadFile(manjuCharLibCardPath(libCid))
		if err != nil {
			continue
		}
		var card manjuCharLibCard
		if json.Unmarshal(b, &card) != nil || card.Card == nil {
			continue
		}
		if manjuCharLookFingerprint(card.Card) != look {
			continue
		}
		dir := filepath.Join(manjuCharLibDir(), libCid)
		if fileExists(filepath.Join(dir, libCid+".png")) {
			return dir, libCid
		}
	}
	return "", ""
}

// manjuCharLibReuse 从库复制角色资产到项目 assets/characters(2026-09-02):
// 返回复制文件数;0 = 无可用资产。复制主图+正脸+视图+Q版+真身,并回写 asset_map。
// 调用时机:stageAssets 角色定妆前(库命中则跳过渲染)。
// 2026-09-03 跨名复用:库内文件名≠本项目角色名时按目标名重命名落地。
func (ctx *manjuCtx) manjuCharLibReuse(m map[string]any, cmap map[string]any, lg *manjuLogger) int {
	libDir, libCid := manjuCharLibMatch(m)
	if libDir == "" {
		return 0
	}
	cid := str(m["id"])
	dstDir := filepath.Join(ctx.assetsDir, "characters")
	_ = os.MkdirAll(dstDir, 0o755)
	n := 0
	for _, suf := range charLibCardSuffixes {
		src := filepath.Join(libDir, libCid+suf+".png")
		if !fileExists(src) {
			continue
		}
		name := cid + suf + ".png" // 跨名复用:按本项目角色名落地
		dst := filepath.Join(dstDir, name)
		// 2026-09-05 续跑全量重渲根因修复:内容相同不覆盖——此前无条件 copyFile,
		// 每次运行 mtime 刷新 → ensureFaceCrop 判正脸"早于主图"重裁 → 参考图指纹
		// 全 stale → 每次续跑整集从镜 1 重渲(当晚三次运行三次全量重渲实锤)。
		if fileExists(dst) && filesEqual(src, dst) {
			n++
		} else if err := copyFile(src, dst); err != nil {
			continue
		} else {
			n++
		}
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
		if libCid == cid {
			lg.logf(fmt.Sprintf("♻️ 角色 %s 复用资产库已有形象(%d 张,零渲染)——提示词变更后自动重渲覆盖入库", cid, n))
		} else {
			lg.logf(fmt.Sprintf("♻️ 角色 %s 形象与库内「%s」一致,跨名复用其资产(%d 张,零渲染)——同脸不同名,省一次定妆", cid, libCid, n))
		}
	}
	return n
}

// manjuCharLibStore 角色资产入库(2026-09-02):把项目 assets/characters 下的
// 该角色全部资产 + 角色卡复制到 char_lib/<角色名>/。调用时机:角色全部资产
// (主图+视图+Q版)生成完成后。幂等(重复入库覆盖同指纹)。
// 2026-09-04 同形象合并(回收员/地磅电子音/小蒋三套同脸资产实锤):六字段去名
// 形象指纹已存在于库(不同名同形象=同一人的不同称呼)→ 不新建条目直接跳过——
// 跨名复用走 look 指纹匹配自动命中已有条目,库里一种形象只存一份。
func (ctx *manjuCtx) manjuCharLibStore(m map[string]any) error {
	if paths.CharLibDir == "" && paths.ManjuRootDir == "" {
		return nil // 路径未解析(测试环境):跳过
	}
	cid := str(m["id"])
	if cid == "" {
		return nil
	}
	if _, libCid := manjuCharLibMatch(m); libCid != "" && libCid != sanitizeFileName(cid) {
		return nil // 同形象已在库(名字不同):复用已有条目,不新建重复条目
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
		Card:          m,
		Fingerprint:   manjuCharFingerprint(m),
		CreatedAt:     nowUnix(),
		SourceProject: ctx.project,
	}
	b, _ := json.MarshalIndent(card, "", "  ")
	_ = os.WriteFile(manjuCharLibCardPath(cid), b, 0o644)
	_ = rebuildCharLibIndex() // 汇总索引实时同步(2026-09-03)
	return nil
}

// manjuCharLibList 资产库清单(前端角色资产库弹窗数据源):
// [{name, files:[...], fingerprint 前8, created_at}] 按名字排序。
func manjuCharLibList() []map[string]any {
	// 2026-09-03 汇总索引版:读 asset_lib/characters/index.json 单文件(缺失惰性
	// 重建),免逐目录遍历;输出字段与旧遍历版兼容并扩充七字段摘要。
	return manjuCharLibListFromIndex()
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
	err := os.RemoveAll(libDir)
	_ = rebuildCharLibIndex() // 汇总索引实时同步(2026-09-03)
	return err
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

// manjuCharLibDeleteBatchHandler POST /api/manju/char-lib/delete-batch
// {names:[...]} 批量删除;{all:true} 清空全部(2026-09-02 资产管理批量清理)。
// 逐个走 manjuCharLibDelete(同名校验/护栏同款),失败项不阻断其余,结果逐条透出。
func manjuCharLibDeleteBatchHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
		All   bool     `json:"all"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	names := body.Names
	if body.All {
		names = nil
		for _, c := range manjuCharLibList() {
			names = append(names, str(c["name"]))
		}
	}
	if len(names) == 0 {
		writeErr(w, http.StatusBadRequest, "missing names(或 all=true 清空全部)")
		return
	}
	deleted, failed := []string{}, []map[string]string{}
	for _, n := range names {
		if err := manjuCharLibDelete(n); err != nil {
			failed = append(failed, map[string]string{"name": n, "error": err.Error()})
			continue
		}
		deleted = append(deleted, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted, "failed": failed})
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

func init() {
	// 路径迁移完成→资产汇总索引重建(2026-09-03 拆包:paths 零业务依赖,钩子注入)
	paths.OnPathsMigrated = func() {
		_ = rebuildCharLibIndex()
		_ = rebuildVoiceLibIndex()
	}
}
