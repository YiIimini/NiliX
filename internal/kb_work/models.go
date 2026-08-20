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
