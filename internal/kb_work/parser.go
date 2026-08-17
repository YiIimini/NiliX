package kb_work

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 默认配色(README 图例缺失时的兜底)
var defaultColors = map[string]string{
	"系统优化": "#4A90D9",
	"系统美化": "#9B59B6",
	"开发工具": "#27AE60",
	"版本控制": "#E67E22",
	"知识库规范": "#F1C40F",
	"文档技能": "#E74C3C",
}

const indexColor = "#5DADE2" // 索引节点(README)专用色

var (
	linkRe  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	titleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	legendRe = regexp.MustCompile(`^\|\s*([^|]+?)\s*\|\s*[^|]*\` + "`" + `#([0-9A-Fa-f]{6})\` + "`" + `\s*\|\s*`)
	codeRe  = regexp.MustCompile("```[^`]*```|`[^`]*`")
	mdStripper = regexp.MustCompile(`[#>*_\[\]|(){}!-]`)
)

// Store 解析结果缓存(线程安全)
type Store struct {
	mu         sync.RWMutex
	root       string
	pages      []*Page
	byID       map[string]*Page
	categories []Category
	indexID    string
	loadedAt   time.Time
}

func NewStore(root string) *Store {
	s := &Store{root: root}
	s.Reload()
	return s
}

func (s *Store) Reload() error {
	pages, cats, indexID, err := scan(s.root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = pages
	s.categories = cats
	s.indexID = indexID
	s.byID = make(map[string]*Page, len(pages))
	for _, p := range pages {
		s.byID[p.ID] = p
	}
	s.loadedAt = time.Now()
	return nil
}

// Snapshot 返回页面深拷贝(避免外部并发读写)
func (s *Store) Snapshot() []*Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Page, len(s.pages))
	for i, p := range s.pages {
		cp := *p
		cp.Links = append([]string(nil), p.Links...)
		cp.RawLinks = append([]string(nil), p.RawLinks...)
		out[i] = &cp
	}
	return out
}

func (s *Store) Categories() []Category {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Category(nil), s.categories...)
}

func (s *Store) IndexID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexID
}

func (s *Store) Page(id string) (*Page, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.Links = append([]string(nil), p.Links...)
	return &cp, true
}

// scan 扫描知识库目录（两级：顶层大类 + 大类下的子分类）
func scan(root string) ([]*Page, []Category, string, error) {
	colors := readLegend(root)

	cats := make([]Category, 0)
	pages := make([]*Page, 0)
	indexID := ""

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			if e.Name() == "README.md" || strings.EqualFold(e.Name(), "readme.md") {
				p, err := parsePage(root, "README.md", "", indexColor)
				if err == nil {
					p.ID = strings.TrimSuffix(e.Name(), ".md")
					indexID = p.ID
					pages = append(pages, p)
				}
			}
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		catDir := e.Name()
		cat := Category{Name: catDir, Dir: catDir + "/", Color: colors[catDir]}
		if cat.Color == "" {
			cat.Color = fallbackColor(catDir)
		}
		cats = append(cats, cat)
		scanCatDir(root, catDir, catDir, cat.Color, cat, &cats, &pages)
	}

	// 解析双链:按文件名(去 .md)解析
	byBase := map[string]*Page{}
	for _, p := range pages {
		byBase[strings.ToLower(p.ID)] = p
	}
	for _, p := range pages {
		for _, raw := range p.RawLinks {
			target := strings.TrimSpace(strings.SplitN(raw, "|", 2)[0])
			if target == "" || target == p.ID {
				continue
			}
			if tp, ok := byBase[strings.ToLower(target)]; ok {
				p.Links = append(p.Links, tp.ID)
			}
		}
		p.Links = dedupe(p.Links)
	}

	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })
	sort.Slice(pages, func(i, j int) bool { return pages[i].Mtime > pages[j].Mtime })
	return pages, cats, indexID, nil
}

// scanCatDir 递归扫描分类目录：直接 .md 归当前分类(catName)，子目录作为子分类(Parent=父分类)。
func scanCatDir(root, relDir, catName, catColor string, cat Category, cats *[]Category, pages *[]*Page) {
	entries, err := os.ReadDir(filepath.Join(root, relDir))
	if err != nil {
		return
	}
	for _, f := range entries {
		if f.IsDir() {
			name := f.Name()
			// 跳过点开头目录与资源目录(assets 等)
			if strings.HasPrefix(name, ".") || name == "assets" {
				continue
			}
			subCat := Category{Name: name, Dir: relDir + "/" + name + "/", Color: catColor, Parent: cat.Name}
			*cats = append(*cats, subCat)
			scanCatDir(root, relDir+"/"+name, name, catColor, subCat, cats, pages)
			continue
		}
		if !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		rel := relDir + "/" + f.Name()
		p, err := parsePage(root, rel, catName, catColor)
		if err != nil {
			continue
		}
		*pages = append(*pages, p)
	}
}

// parsePage 解析单个 md 文件
func parsePage(root, relPath, category, color string) (*Page, error) {
	full := filepath.Join(root, relPath)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	text := string(data)
	id := strings.TrimSuffix(filepath.Base(relPath), ".md")

	title := id
	if m := titleRe.FindStringSubmatch(text); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}

	rawLinks := make([]string, 0)
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		rawLinks = append(rawLinks, m[1])
	}

	fi, _ := os.Stat(full)
	mtime := time.Time{}
	if fi != nil {
		mtime = fi.ModTime()
	}

	return &Page{
		ID:       id,
		Title:    title,
		Category: category,
		Color:    color,
		RawLinks: rawLinks,
		Words:    countWords(text),
		Mtime:    mtime.UTC().Format(time.RFC3339),
		RelPath:  relPath,
		Snippet:  snippet(text),
		Markdown: text,
	}, nil
}

// readLegend 从 README.md 解析"分类与颜色图例"表格
func readLegend(root string) map[string]string {
	colors := map[string]string{}
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return colors
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := legendRe.FindStringSubmatch(line); len(m) > 1 {
			name := strings.TrimSpace(strings.Split(m[1], " ")[0])
			if name != "" {
				colors[name] = "#" + strings.ToUpper(m[2])
			}
		}
	}
	return colors
}

func fallbackColor(name string) string {
	if c, ok := defaultColors[name]; ok {
		return c
	}
	return "#5DADE2"
}

// countWords 统计有效字数(去代码块/标记后的可见字符数)
func countWords(md string) int {
	md = codeRe.ReplaceAllString(md, "")
	md = mdStripper.ReplaceAllString(md, " ")
	return utf8.RuneCountInString(strings.Join(strings.Fields(md), " "))
}

// snippet 提取首个非标题/非引用正文行,截断 100 字符
func snippet(md string) string {
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") {
			continue
		}
		t = linkRe.ReplaceAllString(t, "$1")
		r := []rune(t)
		if len(r) > 100 {
			t = string(r[:100]) + "…"
		}
		return t
	}
	return ""
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
