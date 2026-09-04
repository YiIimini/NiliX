package manju

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type dirFile struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
	Words int    `json:"words"`
	Dir   string `json:"dir"`
	No    int    `json:"no"`
	Kind  string `json:"kind"` // 创作输出分类: storyboard=分镜脚本 / prompt=提示词 / 空=其他附加(2026-08-23 用户规则)
}

func isImageFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".gif"
}

func coverPriority(name string) int {
	n := strings.ToLower(name)
	for i, kw := range []string{"主图", "正式版", "正式", "cover", "封面", "poster"} {
		if strings.Contains(n, kw) {
			return i
		}
	}
	return 99
}

func analyzeMedia(dir string) map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]any{"dir": dir, "err": err.Error()}
	}
	projects := make([]map[string]any, 0)
	for _, e := range entries {
		if !e.IsDir() || isJunkName(e.Name()) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		cats := map[string]*mediaCat{}
		order := []string{"characters", "scenes", "clips", "final", "tts", "score", "images", "videos", "audio", "docs"}
		for _, c := range order {
			cats[c] = &mediaCat{}
		}
		mtime := ""
		covers := []mediaItem{}
		walkMedia(full, full, cats, &mtime, &covers, 0)
		cover := pickMediaCover(covers)
		if cover == "" && cats["characters"].Count > 0 {
			cover = cats["characters"].Items[0].Path
		}
		proj := map[string]any{"name": e.Name(), "path": full, "mtime": mtime, "cover": cover, "covers": novelCoverPaths(full)}
		for _, c := range order {
			proj[c] = cats[c]
		}
		projects = append(projects, proj)
	}
	sort.Slice(projects, func(i, j int) bool {
		mi, _ := projects[i]["mtime"].(string)
		mj, _ := projects[j]["mtime"].(string)
		if mi != mj {
			return mi > mj
		}
		return projects[i]["name"].(string) < projects[j]["name"].(string)
	})
	return map[string]any{"dir": dir, "projects": projects}
}

func addFSRoot(p string) {
	if p = fsNormRoot(p); p == "" {
		return
	}
	fsRootsMu.Lock()
	defer fsRootsMu.Unlock()
	for _, r := range fsRoots {
		if strings.EqualFold(r, p) {
			return
		}
	}
	fsRoots = append(fsRoots, p)
}

