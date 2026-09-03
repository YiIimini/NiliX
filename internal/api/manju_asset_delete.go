package api

// 项目内资产管理·删除清理(2026-09-02 用户需求「资产管理需要有删除清理功能」):
// 复用 POST /api/manju/output/delete 扩展两个 scope——
//   char  :删角色全部资产图(主图/正脸/视图/Q版/真身/角色板)+ _gacha 抽卡候选,
//          并同步清 asset_map.characters 条目;角色卡(plan)保留,回「未定妆」态,
//          可重新抽卡/上传/从资产库导入,下次资产阶段也可自动重出。
//   scene :删场景图(主图 + _end 尾帧等同名前缀产物),同步清 asset_map.scenes。
// 跨项目 char_lib 不动(库资产在「🎭 资产库」弹窗管理,含批量删除/清空)。
// 命名匹配防前缀误删:「张三」只匹配 张三.png / 张三_*.png,不碰「张三丰」。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// manjuCharAssetFiles 列出角色在项目内的全部资产文件(相对 characters 目录):
// <id>.png 主图 + <id>_*.png 视图/正脸/Q版/真身/角色板;候选在 _gacha/<id>_*。
// 返回 (正式资产, 抽卡候选)。
func manjuCharAssetFiles(charDir, cid string) (files []string, gacha []string) {
	if entries, err := os.ReadDir(charDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if manjuNameBelongsTo(e.Name(), cid) {
				files = append(files, e.Name())
			}
		}
	}
	if ge, err := os.ReadDir(filepath.Join(charDir, "_gacha")); err == nil {
		for _, e := range ge {
			if !e.IsDir() && strings.HasPrefix(e.Name(), cid+"_") {
				gacha = append(gacha, e.Name())
			}
		}
	}
	return files, gacha
}

// manjuNameBelongsTo 文件名是否属于角色 cid:<cid>.png 精确 或 <cid>_*.png 前缀。
func manjuNameBelongsTo(name, cid string) bool {
	return name == cid+".png" || (strings.HasPrefix(name, cid+"_") && strings.HasSuffix(name, ".png"))
}

// manjuDeleteCharAssets 删除角色全部资产并同步 asset_map.characters,返回删除文件名清单。
// 幂等:无资产时返回空清单(角色未定妆/已删过)。
func (ctx *manjuCtx) manjuDeleteCharAssets(cid string) []string {
	charDir := filepath.Join(ctx.assetsDir, "characters")
	files, gacha := manjuCharAssetFiles(charDir, cid)
	removed := []string{}
	for _, name := range files {
		if os.Remove(filepath.Join(charDir, name)) == nil {
			removed = append(removed, name)
		}
	}
	gDir := filepath.Join(charDir, "_gacha")
	for _, name := range gacha {
		if os.Remove(filepath.Join(gDir, name)) == nil {
			removed = append(removed, "_gacha/"+name)
		}
	}
	if len(removed) > 0 {
		ctx.manjuAssetMapClean("characters", cid)
	}
	return removed
}

// manjuDeleteSceneAssets 删除场景全部图(主图 + <sid>_*.png 尾帧等)并同步 asset_map.scenes。
func (ctx *manjuCtx) manjuDeleteSceneAssets(sid string) []string {
	sceneDir := filepath.Join(ctx.assetsDir, "scenes")
	removed := []string{}
	if entries, err := os.ReadDir(sceneDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
				continue
			}
			name := e.Name()
			if name == sid+".png" || strings.HasPrefix(name, sid+"_") {
				if os.Remove(filepath.Join(sceneDir, name)) == nil {
					removed = append(removed, name)
				}
			}
		}
	}
	if len(removed) > 0 {
		ctx.manjuAssetMapClean("scenes", sid)
	}
	return removed
}

// manjuAssetMapClean 从 asset_map.json 的 section(characters/scenes)里清掉 cid 及
// cid_ 前缀条目(删除资产后旧映射会让「资产就绪」计数虚高,且复用判定按文件存在为准)。
func (ctx *manjuCtx) manjuAssetMapClean(section, cid string) {
	p := filepath.Join(ctx.assetsDir, "asset_map.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var amap map[string]any
	if json.Unmarshal(b, &amap) != nil {
		return
	}
	sec, _ := amap[section].(map[string]any)
	if sec == nil {
		return
	}
	dirty := false
	for k := range sec {
		if k == cid || strings.HasPrefix(k, cid+"_") {
			delete(sec, k)
			dirty = true
		}
	}
	if !dirty {
		return
	}
	amap[section] = sec
	_ = atomicWriteJSON(p, amap)
}
