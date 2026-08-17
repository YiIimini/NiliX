package kb_work

import (
	"sort"
	"strings"
)

// BuildGraph 构建关系图谱
func BuildGraph(s *Store) *Graph {
	pages := s.Snapshot()
	indexID := s.IndexID()

	// 节点
	nodes := make([]GraphNode, 0, len(pages)+8)
	catCount := map[string]int{}
	catColor := map[string]string{}
	for _, p := range pages {
		if p.Category == "" {
			continue // 索引页(README)不计入分类
		}
		catCount[p.Category]++
		if p.Color != "" {
			catColor[p.Category] = p.Color
		}
	}
	// 索引节点
	if indexID != "" {
		nodes = append(nodes, GraphNode{ID: indexID, Name: "索引", Category: "索引", Color: indexColor, Size: len(pages), IsIndex: true})
	}
	// 分类枢纽节点
	catNames := make([]string, 0, len(catCount))
	for c := range catCount {
		catNames = append(catNames, c)
	}
	sort.Strings(catNames)
	for _, c := range catNames {
		nodes = append(nodes, GraphNode{
			ID: "cat:" + c, Name: c, Category: c,
			Color: catColor[c], Size: catCount[c]*2, IsHub: true,
		})
	}
	// 页面节点(跳过索引页,避免与索引节点重复)
	for _, p := range pages {
		if p.ID == indexID {
			continue
		}
		nodes = append(nodes, GraphNode{
			ID: p.ID, Name: p.Title, Category: p.Category,
			Color: p.Color, Size: len(p.Links) + 1, Mtime: p.Mtime,
		})
	}

	// 边:双链(去重,双向成对只保留一条)+ 分类枢纽→页面
	linkMap := map[string]bool{}
	links := make([]GraphLink, 0)
	addLink := func(a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		key := a + "\x00" + b
		if a > b {
			key = b + "\x00" + a
		}
		if !linkMap[key] {
			linkMap[key] = true
			links = append(links, GraphLink{Source: a, Target: b})
		}
	}
	for _, p := range pages {
		for _, t := range p.Links {
			addLink(p.ID, t)
		}
		if p.Category != "" {
			addLink("cat:"+p.Category, p.ID)
		}
		if indexID != "" && p.ID != indexID {
			addLink(indexID, p.ID)
		}
	}

	return &Graph{Nodes: nodes, Links: links}
}

// BuildOverview 构建最新总览
func BuildOverview(s *Store) *Overview {
	pages := s.Snapshot()
	stats := Stats{Categories: len(s.Categories()), LastUpdate: s.loadedAtStr()}

	byCat := map[string][]*Page{}
	for _, p := range pages {
		if p.ID == s.IndexID() {
			continue // 索引不进分类分组
		}
		byCat[p.Category] = append(byCat[p.Category], p)
		stats.Pages++
		stats.Links += len(p.Links)
		stats.Words += p.Words
	}
	catColor := map[string]string{}
	parentOf := map[string]string{}
	for _, c := range s.Categories() {
		catColor[c.Name] = c.Color
		parentOf[c.Name] = c.Parent
	}

	catNames := make([]string, 0, len(byCat))
	for c := range byCat {
		catNames = append(catNames, c)
	}
	sort.Strings(catNames)

	groups := make([]CatGroup, 0, len(catNames))
	for _, c := range catNames {
		list := byCat[c]
		sort.Slice(list, func(i, j int) bool { return list[i].Mtime > list[j].Mtime })
		g := CatGroup{Name: c, Color: catColor[c], Parent: parentOf[c], Pages: make([]PageBrief, 0, len(list))}
		for _, p := range list {
			g.Pages = append(g.Pages, brief(p))
		}
		groups = append(groups, g)
	}

	// 最近更新:全量按时间排序
	all := make([]*Page, 0, len(pages))
	for _, p := range pages {
		if p.ID == s.IndexID() {
			continue
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Mtime > all[j].Mtime })
	recent := make([]PageBrief, 0, len(all))
	for _, p := range all {
		recent = append(recent, brief(p))
	}

	stats.LastUpdate = s.loadedAtStr()
	if len(all) > 0 {
		stats.LastUpdate = all[0].Mtime
	}

	return &Overview{Stats: stats, Categories: groups, Recent: recent}
}

// Meta 站点元信息(方法形式)
func (s *Store) Meta() *Meta { return BuildMeta(s) }

// BuildMeta 构建站点元信息
func BuildMeta(s *Store) *Meta {
	cats := s.Categories()
	out := make([]Category, len(cats))
	for i, c := range cats {
		out[i] = c
	}
	return &Meta{Categories: out, PageCount: len(s.pagesNoLock()), IndexID: s.IndexID()}
}

func (s *Store) pagesNoLock() []*Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pages
}

func (s *Store) loadedAtStr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedAt.UTC().Format("2006-01-02T15:04:05Z")
}

func brief(p *Page) PageBrief {
	return PageBrief{
		ID: p.ID, Title: p.Title, Category: p.Category, Color: p.Color,
		Mtime: p.Mtime, Links: len(p.Links), Words: p.Words, Snippet: p.Snippet,
	}
}

// IDOf 兼容辅助:文件名去扩展名
func IDOf(name string) string {
	return strings.TrimSuffix(name, ".md")
}
