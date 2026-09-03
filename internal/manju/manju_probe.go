package manju

// manju_probe.go 新建项目目录探测(2026-08-30 用户需求:新建项目统一改为「选择目录后
// 系统自动解析目录下对应文件(提示词、分镜脚本等),未满足条件显示相关错误信息」)。
//
// 模块化设计:probeManjuDir 纯逻辑探测(独立可测,不依赖 HTTP),manjuProbeDir 只做
// 参数校验+JSON 出参。识别规则与既有管线同源:
//   - 分镜脚本识别与 scanStoryboardDir 同规则(文件名含「分镜」或内容含 [Shot N]/分镜表);
//   - 小说全本解析与 resolveNovelPath 同布局(<书>/全本/*.md);
//   - 提示词文件为爽文技能标准产物(素材/人物生成提示词.md、场景提示词.md、渲染提示词总集.md)。
// 判定:小说全本与分镜脚本至少其一 → ok;都缺 → errors 逐项列出期望路径。
// 库根(含多本书的父目录)→ 返回 books 列表让前端引导进入具体书目录,不误报错误。

import (
	"nilix/internal/paths"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manjuDirProbe 目录探测结果(前端「新建项目」解析面板数据源)
type manjuDirProbe struct {
	OK       bool     `json:"ok"`       // 满足创建条件(有全本或有分镜脚本)
	Dir      string   `json:"dir"`      // 归一化后的书根目录(输入为文件时取父目录)
	Name     string   `json:"name"`     // 建议剧名(书根目录名)
	NovelFile string  `json:"novelFile"`// 小说全本 md(空=无)
	Chapters int      `json:"chapters"` // 正文/卷/*/*.md 计数(信息)
	StoryDir string   `json:"storyDir"` // 分镜脚本目录(空=无)
	StoryN   int      `json:"storyN"`   // 分镜脚本数量
	Prompts  []string `json:"prompts"`  // 检测到的提示词文件(显示名,相对书根)
	HasPlan  bool     `json:"hasPlan"`  // 立项.json(创建时自动应用渲染规划)
	Books    []string `json:"books"`    // 库根场景:检测到的书目录名列表
	Errors   []string `json:"errors"`   // 未满足条件的具体错误
	Notes    []string `json:"notes"`    // 提示(可创建但有注意事项)
}

// probeStoryboards 轻量分镜脚本探测(只按文件名+内容线索,不读全文预览——
// probe 只需要数量与目录,preview 由既有 script/scan-dir 弹窗承担)
func probeStoryboards(dir string) (string, int) {
	cands := []string{
		filepath.Join(dir, "素材", "分镜脚本"),
		filepath.Join(dir, "分镜脚本"),
	}
	best := ""
	bestN := 0
	for _, sd := range cands {
		entries, err := os.ReadDir(sd)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".md" && ext != ".txt" && ext != ".markdown" {
				continue
			}
			full := filepath.Join(sd, e.Name())
			if strings.Contains(strings.ToLower(e.Name()), "分镜") || storyboardContentHint(full) {
				n++
			}
		}
		if n > bestN {
			best, bestN = sd, n
		}
	}
	return best, bestN
}

// probeBookRoot 判定一个目录是否像「书根」(有全本/正文/素材/设定集任一特征)
func probeBookRoot(dir string) bool {
	for _, sub := range []string{"全本", "正文", "素材", "设定集"} {
		if dirExists(filepath.Join(dir, sub)) {
			return true
		}
	}
	if _, n := probeStoryboards(dir); n > 0 {
		return true
	}
	return false
}

// probeManjuDir 目录探测纯逻辑(独立可测):
// 输入任意路径(书根/库根/全本 md 文件),输出结构化探测结果。
func probeManjuDir(raw string) manjuDirProbe {
	p := manjuDirProbe{Dir: strings.TrimSpace(raw)}
	if p.Dir == "" {
		p.Errors = append(p.Errors, "请选择或输入项目目录")
		return p
	}
	// 文件输入(全本 md 等)→ 父目录;引号容错与 manjuCreateProject 同款
	p.Dir = strings.Trim(p.Dir, `" `)
	if fi, err := os.Stat(p.Dir); err == nil && !fi.IsDir() {
		p.Dir = filepath.Dir(p.Dir)
	}
	p.Dir = filepath.Clean(p.Dir)
	// 布局子目录输入(全本/正文/素材/分镜脚本等)→ 上提到书根(最多三层,防极端嵌套)
	for i := 0; i < 3; i++ {
		base := filepath.Base(p.Dir)
		if base == "." || base == string(filepath.Separator) {
			break
		}
		isLayout := false
		for _, sub := range []string{"全本", "正文", "素材", "分镜脚本", "设定集", "封面"} {
			if strings.EqualFold(base, sub) {
				isLayout = true
				break
			}
		}
		if !isLayout {
			break
		}
		p.Dir = filepath.Dir(p.Dir)
	}
	p.Name = filepath.Base(p.Dir)

	// 库根场景:直接子目录里有多本书 → 列书引导,不误报
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		p.Errors = append(p.Errors, "目录不存在或不可访问: "+p.Dir)
		return p
	}
	if entries, err := os.ReadDir(p.Dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if probeBookRoot(filepath.Join(p.Dir, e.Name())) {
				p.Books = append(p.Books, e.Name())
			}
		}
	}
	// 自身就是书根时,Books 含子目录书但优先按本目录解析;只有当本目录
	// 既非书根(无全本/无分镜)且检出多本书时,才进入「库根引导」分支
	selfBook := dirExists(filepath.Join(p.Dir, "全本")) || dirExists(filepath.Join(p.Dir, "正文"))
	sd, sn := probeStoryboards(p.Dir)
	if !selfBook && sn == 0 && len(p.Books) > 0 {
		sort.Strings(p.Books)
		p.Notes = append(p.Notes, "该目录是小说库根(检测到 "+itoa(len(p.Books))+" 本书),请进入具体某本书的目录再创建项目")
		p.Errors = append(p.Errors, "所选目录是书库根目录,不是某一本书的目录(检测到书: "+strings.Join(p.Books, "、")+")")
		return p
	}
	p.Books = nil // 自身是书根:Books 无意义,清空防前端误判

	// 全本
	if entries, err := os.ReadDir(filepath.Join(p.Dir, "全本")); err == nil {
		var best string
		var bestSz int64
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".md" && ext != ".txt" && ext != ".markdown" {
				continue
			}
			sz := int64(0)
			if info, ie := e.Info(); ie == nil {
				sz = info.Size()
			}
			if sz > bestSz {
				best, bestSz = filepath.Join(p.Dir, "全本", e.Name()), sz
			}
		}
		p.NovelFile = best
	}
	// 正文分章计数(信息项)
	if entries, err := os.ReadDir(filepath.Join(p.Dir, "正文")); err == nil {
		for _, vol := range entries {
			if !vol.IsDir() {
				continue
			}
			if ch, err := os.ReadDir(filepath.Join(p.Dir, "正文", vol.Name())); err == nil {
				for _, c := range ch {
					if !c.IsDir() && strings.EqualFold(filepath.Ext(c.Name()), ".md") {
						p.Chapters++
					}
				}
			}
		}
	}
	// 分镜脚本
	p.StoryDir, p.StoryN = sd, sn
	// 提示词文件(存在即列出,缺了不报错——仅提示)
	for _, pf := range []struct{ rel, label string }{
		{filepath.Join("素材", "人物生成提示词.md"), "人物生成提示词"},
		{filepath.Join("素材", "场景提示词.md"), "场景提示词"},
		{filepath.Join("素材", "渲染提示词总集.md"), "渲染提示词总集"},
		{filepath.Join("封面", "封面提示词.md"), "封面提示词"},
	} {
		if fileExists(filepath.Join(p.Dir, filepath.FromSlash(pf.rel))) {
			p.Prompts = append(p.Prompts, pf.label)
		}
	}
	p.HasPlan = fileExists(filepath.Join(p.Dir, "立项.json"))

	// 条件判定与具体错误
	switch {
	case p.NovelFile != "" && p.StoryN > 0:
		p.OK = true
		p.Notes = append(p.Notes, "小说全本与分镜脚本均检测到:默认小说解析,分镜脚本创建后自动按章导入(每章一集)")
	case p.NovelFile != "":
		p.OK = true
		p.Notes = append(p.Notes, "未检测到分镜脚本:将走 LLM 直出(小说解析)生成渲染方案")
	case p.StoryN > 0:
		p.OK = true
		p.Notes = append(p.Notes, "未检测到小说全本:将启用视频脚本直出模式(分镜脚本创建后自动按章导入)")
	default:
		p.Errors = append(p.Errors, "未找到小说全本(期望 <书目录>/全本/*.md)")
		p.Errors = append(p.Errors, "未找到分镜脚本(期望 <书目录>/素材/分镜脚本/ 下文件名含「分镜」的 .json/.md)")
		p.Errors = append(p.Errors, "至少需要小说全本或分镜脚本之一才能创建项目;请检查所选目录是否为某一本书的根目录")
	}
	return p
}

// manjuProbeDir POST /api/manju/probe-dir {dir} — 新建项目目录探测
// (前端选目录后调用,解析结果驱动创建面板与错误提示)。
// 白名单与创建同款(小说库根内);guard 不通过时也返回 200+结构化 errors,
// 让前端统一在解析面板显示具体错误与库根引导(而非裸 400)。
func manjuProbeDir(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := strings.TrimSpace(str(body["dir"]))
	if dir == "" {
		writeJSON(w, http.StatusOK, probeManjuDir(""))
		return
	}
	if _, gerr := manjuGuardNovel(dir); gerr != nil {
		p := manjuDirProbe{Dir: dir, Name: filepath.Base(filepath.Clean(dir))}
		p.Errors = append(p.Errors, gerr.Error())
		p.Errors = append(p.Errors, "请选择小说库内的书籍目录(库根: "+paths.NovelRootDir+");库外目录不支持作为项目源")
		writeJSON(w, http.StatusOK, p)
		return
	}
	writeJSON(w, http.StatusOK, probeManjuDir(dir))
}
