package kb_work

// Category 知识库分类(对应目录,配色来自 README.md 图例)。
// 支持两级：顶层大类(Parent 为空) + 大类下的子分类(Parent=大类名)。
type Category struct {
	Name   string `json:"name"`
	Color  string `json:"color"`
	Count  int    `json:"count"`
	Dir    string `json:"dir"`
	Parent string `json:"parent"` // 父分类名；空=顶层大类
}

// Page 单个知识页
type Page struct {
	ID       string   `json:"id"`       // 文件名(不含 .md)
	Title    string   `json:"title"`    // 首个 H1
	Category string   `json:"category"` // 分类名
	Color    string   `json:"color"`
	Links    []string `json:"links"`    // 已解析的 [[目标]]
	RawLinks []string `json:"rawLinks"` // 全部 [[...]] 原文
	Words    int      `json:"words"`    // 字数
	Mtime    string   `json:"mtime"`    // RFC3339
	RelPath  string   `json:"relPath"`  // 相对路径,如 系统优化/系统清理.md
	Snippet  string   `json:"snippet"`  // 摘要(首个正文行)
	Markdown string   `json:"markdown"` // 原始内容(仅 /api/page 序列化)
}

// PageBrief 总览用精简页
type PageBrief struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Mtime    string `json:"mtime"`
	Links    int    `json:"links"`
	Words    int    `json:"words"`
	Snippet  string `json:"snippet"`
}

// GraphNode 图谱节点
type GraphNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Size     int    `json:"size"`
	Mtime    string `json:"mtime"`   // RFC3339(页面节点;枢纽/索引节点为空)
	IsHub    bool   `json:"isHub"`   // 分类枢纽节点
	IsIndex  bool   `json:"isIndex"` // 索引节点(README)
}

// GraphLink 图谱边
type GraphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// Graph 图谱数据
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Links []GraphLink `json:"links"`
}

// Stats 统计
type Stats struct {
	Pages      int    `json:"pages"`
	Categories int    `json:"categories"`
	Links      int    `json:"links"`
	Words      int    `json:"words"`
	LastUpdate string `json:"lastUpdate"`
}

// CatGroup 分类分组(总览用)
type CatGroup struct {
	Name   string      `json:"name"`
	Color  string      `json:"color"`
	Parent string      `json:"parent"` // 父分类名；空=顶层大类
	Pages  []PageBrief `json:"pages"`
}

// Overview 最新总览
type Overview struct {
	Stats      Stats      `json:"stats"`
	Categories []CatGroup `json:"categories"`
	Recent     []PageBrief `json:"recent"`
}

// Meta 站点元信息
type Meta struct {
	Categories []Category `json:"categories"`
	PageCount  int        `json:"pageCount"`
	IndexID    string     `json:"indexId"` // README 节点 id
}
