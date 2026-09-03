package manju

// 资产库汇总索引(2026-09-03 用户需求:角色库与音色库各一个汇总 index.json,
// 写操作实时重建,平台侧/技能侧单文件直读,免逐目录遍历)。
//   asset_lib/characters/index.json —— 全部角色的七字段(技能侧查库复用逐字比对
//     依据)+ 指纹 + 资产清单 + 来源项目;
//   asset_lib/voices/index.json      —— 全部音色参考音频清单(key/文件/大小/mtime)。
// 写侧:入库/删除/音色同步后全量重建(85 目录毫秒级,永不漂移);读侧:索引缺失
// 或损坏时惰性重建。技能侧契约改为直读 index.json(禁止缓存原则不变)。

import (
	"nilix/internal/paths"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// assetIndexEntry 角色库索引条目(七字段与 card.json 的 card 同源)
type assetIndexEntry struct {
	Name          string   `json:"name"`
	Fingerprint   string   `json:"fingerprint"`
	SourceProject string   `json:"source_project,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	Gender        string   `json:"gender,omitempty"`
	Age           string   `json:"age,omitempty"`
	Species       string   `json:"species,omitempty"`
	ImagePrompt   string   `json:"image_prompt,omitempty"`
	QForm         string   `json:"q_form,omitempty"`
	SecondForm    string   `json:"second_form,omitempty"`
	Assets        []string `json:"assets"`
}

// rebuildCharLibIndex 全量重建角色库索引(写操作后调用;幂等)
func rebuildCharLibIndex() error {
	root := manjuCharLibDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var list []assetIndexEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		card := manjuCharLibCard{}
		if b, err := os.ReadFile(filepath.Join(root, name, "card.json")); err == nil {
			_ = json.Unmarshal(b, &card)
		}
		var assets []string
		if fe, err := os.ReadDir(filepath.Join(root, name)); err == nil {
			for _, f := range fe {
				if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".png") {
					assets = append(assets, f.Name())
				}
			}
		}
		sort.Strings(assets)
		list = append(list, assetIndexEntry{
			Name:          name,
			Fingerprint:   card.Fingerprint,
			SourceProject: card.SourceProject,
			CreatedAt:     card.CreatedAt,
			Gender:        str(card.Card["gender"]),
			Age:           str(card.Card["age"]),
			Species:       str(card.Card["species"]),
			ImagePrompt:   str(card.Card["image_prompt"]),
			QForm:         str(card.Card["q_form"]),
			SecondForm:    str(card.Card["second_form"]),
			Assets:        assets,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	idx := map[string]any{
		"updated_at": nowUnix(),
		"count":      len(list),
		"characters": list,
	}
	b, _ := json.MarshalIndent(idx, "", "  ")
	return os.WriteFile(filepath.Join(root, "index.json"), b, 0o644)
}

// charLibIndexEntries 读侧:索引缺失/损坏时惰性重建;返回条目列表
func charLibIndexEntries() []assetIndexEntry {
	root := manjuCharLibDir()
	p := filepath.Join(root, "index.json")
	if b, err := os.ReadFile(p); err == nil {
		var idx struct {
			Characters []assetIndexEntry `json:"characters"`
		}
		if json.Unmarshal(b, &idx) == nil && idx.Characters != nil {
			return idx.Characters
		}
	}
	_ = rebuildCharLibIndex()
	if b, err := os.ReadFile(p); err == nil {
		var idx struct {
			Characters []assetIndexEntry `json:"characters"`
		}
		if json.Unmarshal(b, &idx) == nil {
			return idx.Characters
		}
	}
	return nil
}

// manjuCharLibListFromIndex 索引版资产库清单(平台侧列表数据源,与旧逐目录
// 遍历版输出字段兼容并扩充七字段摘要)
func manjuCharLibListFromIndex() []map[string]any {
	var out []map[string]any
	for _, e := range charLibIndexEntries() {
		fp := ""
		if len(e.Fingerprint) >= 8 {
			fp = e.Fingerprint[:8]
		}
		main := ""
		for _, f := range e.Assets {
			if f == e.Name+".png" {
				main = f
				break
			}
		}
		if main == "" && len(e.Assets) > 0 {
			main = e.Assets[0]
		}
		out = append(out, map[string]any{
			"name":           e.Name,
			"files":          e.Assets,
			"fingerprint":    fp,
			"created_at":     e.CreatedAt,
			"id":             e.Name,
			"main":           main,
			"source_project": e.SourceProject,
			"gender":         e.Gender,
			"age":            e.Age,
			"species":        e.Species,
			"image_prompt":   e.ImagePrompt,
			"q_form":         e.QForm,
			"second_form":    e.SecondForm,
		})
	}
	return out
}

// rebuildVoiceLibIndex 全量重建音色库索引(音色写/删后调用;幂等)
func rebuildVoiceLibIndex() error {
	if paths.VoiceLibDir == "" {
		return nil
	}
	if err := os.MkdirAll(paths.VoiceLibDir, 0o755); err != nil {
		return err
	}
	var list []map[string]any
	_ = filepath.WalkDir(paths.VoiceLibDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".mp3") && !strings.HasSuffix(strings.ToLower(p), ".wav") {
			return nil
		}
		rel, _ := filepath.Rel(paths.VoiceLibDir, p)
		key := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		info, _ := d.Info()
		list = append(list, map[string]any{
			"key":        key,
			"file":       filepath.ToSlash(rel),
			"size_bytes": info.Size(),
			"updated_at": nowUnix(),
		})
		return nil
	})
	idx := map[string]any{
		"updated_at": nowUnix(),
		"count":      len(list),
		"voices":     list,
	}
	b, _ := json.MarshalIndent(idx, "", "  ")
	return os.WriteFile(filepath.Join(paths.VoiceLibDir, "index.json"), b, 0o644)
}
