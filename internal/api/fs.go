package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ---- 目录管理 / 小说解析（从 kb-workbench 适配） ----

type dirFile struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
	Words int    `json:"words"`
	Dir   string `json:"dir"`
	No    int    `json:"no"`
}

type dirProject struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	IsDir     bool      `json:"isDir"`
	FileCount int       `json:"fileCount"`
	MdCount   int       `json:"mdCount"`
	TotalSize int64     `json:"totalSize"`
	Words     int       `json:"words"`
	Mtime     string    `json:"mtime"`
	Chapters  []dirFile `json:"chapters"`
	Extras    []dirFile `json:"extras"`
	Cover     string    `json:"cover"`
	Covers    []dirFile `json:"covers"`
	covers    []dirFile
	created   int64 // 目录创建时间(Unix秒),排序用
}

func isJunkName(name string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || strings.HasSuffix(name, "~") {
		return true
	}
	switch strings.ToLower(name) {
	case "logs", "__pycache__", "node_modules", "thumbs.db", "desktop.ini":
		return true
	}
	return false
}

var (
	reChapter = regexp.MustCompile(`第\s*(\d+)\s*章`)
	reLeadNum = regexp.MustCompile(`^\s*(\d+)`)
)

func chapterNo(name string) int {
	if m := reChapter.FindStringSubmatch(name); len(m) > 1 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := reLeadNum.FindStringSubmatch(name); len(m) > 1 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 1 << 30
}

func classifyTextFile(relDir, name string) (bool, int) {
	d := strings.ToLower(relDir)
	if strings.Contains(d, "正文") || strings.Contains(d, "content") || strings.Contains(d, "chapter") {
		return true, chapterNo(name)
	}
	if reChapter.MatchString(name) {
		return true, chapterNo(name)
	}
	return false, 0
}

func sortChapters(chs []dirFile) {
	sort.Slice(chs, func(i, j int) bool {
		if chs[i].No != chs[j].No {
			return chs[i].No < chs[j].No
		}
		return chs[i].Name < chs[j].Name
	})
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

func pickCover(cands []dirFile) string {
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		pi, pj := coverPriority(cands[i].Name), coverPriority(cands[j].Name)
		if pi != pj {
			return pi < pj
		}
		return cands[i].Name < cands[j].Name
	})
	return cands[0].Path
}

func analyzeDir(dir string) map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]any{"dir": dir, "err": err.Error()}
	}
	projects := make([]dirProject, 0, len(entries))
	total := struct {
		Projects  int   `json:"projects"`
		Files     int   `json:"files"`
		Words     int   `json:"words"`
		TotalSize int64 `json:"totalSize"`
	}{}
	for _, e := range entries {
		if isJunkName(e.Name()) || !e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		p := dirProject{Name: e.Name(), Path: full, IsDir: true}
		if fi, err := os.Stat(full); err == nil {
			p.created = fileCreateUnix(fi)
		}
		walkProject(full, full, &p, 0)
		sortChapters(p.Chapters)
		p.Cover = pickCover(p.covers)
		p.Covers = p.covers
		projects = append(projects, p)
		total.Projects++
		total.Files += p.FileCount
		total.Words += p.Words
		total.TotalSize += p.TotalSize
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].created != projects[j].created {
			return projects[i].created > projects[j].created // 最新创建的目录在前
		}
		return projects[i].Name < projects[j].Name
	})
	return map[string]any{"dir": dir, "name": filepath.Base(dir), "projects": projects, "total": total}
}

func walkProject(root, dir string, p *dirProject, depth int) {
	if depth > 4 {
		return
	}
	rel := ""
	if dir != root {
		rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(dir, root)), "/")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if isJunkName(e.Name()) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, ierr := e.Info()
		if e.IsDir() {
			walkProject(root, full, p, depth+1)
			continue
		}
		p.FileCount++
		sz, mt := int64(0), ""
		if ierr == nil {
			sz = info.Size()
			mt = info.ModTime().Format("2006-01-02 15:04")
		}
		p.TotalSize += sz
		if mt > p.Mtime {
			p.Mtime = mt
		}
		if isTextFile(e.Name()) {
			words := textWords(full)
			p.MdCount++
			p.Words += words
			df := dirFile{Name: e.Name(), Path: full, IsDir: false, Size: sz, Mtime: mt, Words: words, Dir: rel}
			if isCh, no := classifyTextFile(rel, e.Name()); isCh {
				df.No = no
				p.Chapters = append(p.Chapters, df)
			} else {
				p.Extras = append(p.Extras, df)
			}
		}
		if isImageFile(e.Name()) {
			dl := strings.ToLower(rel)
			if dl == "" || strings.Contains(dl, "封面") || strings.Contains(dl, "cover") {
				p.covers = append(p.covers, dirFile{Name: e.Name(), Path: full, Size: sz, Mtime: mt})
			}
		}
	}
}

func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".txt" || ext == ".markdown"
}

func textWords(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range string(b) {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			n++
		}
	}
	return n
}

func pickDir() (string, error) {
	ps := `
Add-Type -AssemblyName System.Windows.Forms
$f = New-Object System.Windows.Forms.FolderBrowserDialog
$f.Description = "选择管理目录"
$f.ShowNewFolderButton = $false
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  Write-Output $f.SelectedPath
}`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errors.New("cancelled")
	}
	return p, nil
}

type mediaItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type mediaCat struct {
	Items []mediaItem `json:"items"`
	Count int         `json:"count"`
}

func mediaCatOf(relDir, name string) string {
	d := strings.ToLower(relDir)
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".gif":
		if strings.Contains(d, "character") || strings.Contains(d, "人物") {
			return "characters"
		}
		if strings.Contains(d, "scene") || strings.Contains(d, "场景") {
			return "scenes"
		}
		return "images"
	case ext == ".mp4" || ext == ".mov" || ext == ".webm":
		if strings.Contains(d, "final") || strings.Contains(d, "render") || strings.Contains(d, "output") || strings.Contains(d, "成片") {
			return "final"
		}
		if strings.Contains(d, "clip") || strings.Contains(d, "分镜") || strings.Contains(d, "shot") {
			return "clips"
		}
		return "videos"
	case ext == ".wav" || ext == ".mp3" || ext == ".m4a":
		if strings.Contains(d, "tts") || strings.Contains(d, "voice") || strings.Contains(d, "配音") {
			return "tts"
		}
		if strings.Contains(d, "score") || strings.Contains(d, "music") || strings.Contains(d, "配乐") {
			return "score"
		}
		return "audio"
	default:
		return "docs"
	}
}

// novelCoverPaths 读项目 config 的小说目录,按书架同规则收集封面图(02_封面/ 等含"封面"目录或目录根图片)
func novelCoverPaths(projectDir string) []map[string]any {
	b, err := os.ReadFile(filepath.Join(projectDir, "config.json"))
	if err != nil {
		return nil
	}
	var cfg struct {
		Paths struct {
			NovelDir string `json:"novel_dir"`
		} `json:"paths"`
	}
	if json.Unmarshal(b, &cfg) != nil || cfg.Paths.NovelDir == "" {
		return nil
	}
	root := cfg.Paths.NovelDir
	out := []map[string]any{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if isJunkName(e.Name()) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(full, depth+1)
				continue
			}
			if !isImageFile(e.Name()) {
				continue
			}
			rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(dir, root)), "/")
			dl := strings.ToLower(rel)
			if dl == "" || strings.Contains(dl, "封面") || strings.Contains(dl, "cover") {
				out = append(out, map[string]any{"name": e.Name(), "path": full})
			}
		}
	}
	walk(root, 0)
	return out
}

func pickMediaCover(cands []mediaItem) string {
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		pi, pj := coverPriority(cands[i].Name), coverPriority(cands[j].Name)
		if pi != pj {
			return pi < pj
		}
		return cands[i].Name < cands[j].Name
	})
	return cands[0].Path
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

func walkMedia(root, dir string, cats map[string]*mediaCat, mtime *string, covers *[]mediaItem, depth int) {
	if depth > 5 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if isJunkName(e.Name()) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walkMedia(root, full, cats, mtime, covers, depth+1)
			continue
		}
		rel := strings.TrimPrefix(dir, root)
		cat := mediaCatOf(rel, e.Name())
		if mc, ok := cats[cat]; ok {
			info, _ := e.Info()
			sz := int64(0)
			if info != nil {
				sz = info.Size()
				if mt := info.ModTime().Format("2006-01-02 15:04"); mt > *mtime {
					*mtime = mt
				}
			}
			mc.Items = append(mc.Items, mediaItem{Name: e.Name(), Path: full, Size: sz})
			mc.Count++
		}
		if isImageFile(e.Name()) {
			dl := strings.ToLower(rel)
			if dl == "" || strings.Contains(dl, "封面") || strings.Contains(dl, "cover") || strings.Contains(dl, "poster") {
				*covers = append(*covers, mediaItem{Name: e.Name(), Path: full})
			}
		}
	}
}

// ---- fs 根目录白名单(安全护栏) ----
// 所有 /api/fs/* 的 path/dir 必须位于已注册根目录(或其子路径)内——
// 此前任意绝对路径可读(.secret.key / server/settings.json 明文 key 实测可读),必须收敛。
// 注册源:启动注入(知识库/漫剧/小说/Comfy 目录)+ 运行时动态(用户目录选择器选中的目录、
// 项目 config.json paths 里的目录),见 SetFSRoots / addFSRoot / registerCtxRoots。

var (
	fsRootsMu sync.RWMutex
	fsRoots   []string
)

// fsNormRoot 规范化允许根目录:解析符号链接/junction 后的真实路径(审计 M11),
// 与 fsPathAllowed 的 EvalSymlinks 结果保持一致(Windows 长路径/符号链接格式差异)
func fsNormRoot(r string) string {
	r = filepath.Clean(strings.TrimSpace(r))
	if ev, err := filepath.EvalSymlinks(r); err == nil {
		r = filepath.Clean(ev)
	}
	return r
}

// SetFSRoots 全量设置允许根目录(启动时注入)
func SetFSRoots(roots ...string) {
	fsRootsMu.Lock()
	defer fsRootsMu.Unlock()
	seen := map[string]bool{}
	fsRoots = fsRoots[:0]
	for _, r := range roots {
		if r = fsNormRoot(r); r != "" && !seen[strings.ToLower(r)] {
			seen[strings.ToLower(r)] = true
			fsRoots = append(fsRoots, r)
		}
	}
	// 漫剧项目根恒允许(api 包内部)
	if r := fsNormRoot(ManjuRootDir); r != "" && !seen[strings.ToLower(r)] {
		fsRoots = append(fsRoots, r)
	}
}

// addFSRoot 动态注册一个允许根(去重)
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

// fsPathAllowed path 必须位于某已注册根目录内(含根本身)
func fsPathAllowed(p string) bool {
	if p = filepath.Clean(strings.TrimSpace(p)); p == "" {
		return false
	}
	// 审计 M11:解析符号链接/junction 后再比对——根内若有目录 junction 指向根外
	// (如 manju/<项目>/secret → C:\Users\.ssh),字符串前缀比对会放行越界读取。
	// EvalSymlinks 对不存在路径返回错误,此时回退原路径(浏览未创建文件场景)
	if ev, err := filepath.EvalSymlinks(p); err == nil {
		p = filepath.Clean(ev)
	}
	fsRootsMu.RLock()
	defer fsRootsMu.RUnlock()
	lp := strings.ToLower(p)
	sep := string(filepath.Separator)
	for _, r := range fsRoots {
		lr := strings.ToLower(r)
		if lp == lr || strings.HasPrefix(lp, lr+sep) {
			return true
		}
	}
	return false
}

// ---- fs API handlers ----

// fileCreateUnix 返回文件/目录创建时间(Unix 秒);Windows 从文件属性取创建时间,失败回退修改时间
func fileCreateUnix(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		// FILETIME 从 1601-01-01 起 100ns 单位, 转 Unix 秒(偏移 11644473600s)
		return st.CreationTime.Nanoseconds()/1e9 - 11644473600
	}
	return info.ModTime().Unix()
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "missing dir")
		return
	}
	if !fsPathAllowed(dir) {
		writeErr(w, http.StatusForbidden, "目录不在允许的管理范围内")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	type item struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
		Mtime string `json:"mtime"`
		ctime int64  // 创建时间(Unix秒),仅排序用
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		info, _ := e.Info()
		sz, mt, ct := int64(0), "", int64(0)
		if info != nil {
			sz = info.Size()
			mt = info.ModTime().Format("2006-01-02 15:04")
			ct = fileCreateUnix(info)
		}
		items = append(items, item{Name: e.Name(), IsDir: e.IsDir(), Size: sz, Mtime: mt, ctime: ct})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ctime != items[j].ctime {
			return items[i].ctime > items[j].ctime // 最新创建的排最前
		}
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "items": items})
}

func (s *Server) handleFSAnalyze(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "missing dir")
		return
	}
	if !fsPathAllowed(dir) {
		writeErr(w, http.StatusForbidden, "目录不在允许的管理范围内")
		return
	}
	writeJSON(w, http.StatusOK, analyzeDir(dir))
}

func (s *Server) handleFSSelect(w http.ResponseWriter, r *http.Request) {
	p, err := pickDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addFSRoot(p) // 用户主动选择的管理目录动态放行
	writeJSON(w, http.StatusOK, map[string]any{"dir": p})
}

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "missing path")
		return
	}
	if !fsPathAllowed(p) {
		writeErr(w, http.StatusForbidden, "文件不在允许的管理范围内")
		return
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() > 2<<20 {
		writeErr(w, http.StatusBadRequest, "invalid or too large")
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": filepath.Base(p), "ext": strings.ToLower(filepath.Ext(p)), "content": string(b)})
}

func (s *Server) handleFSFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if !fsPathAllowed(p) {
		http.Error(w, "文件不在允许的管理范围内", http.StatusForbidden)
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	allowed := false
	for _, e := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".mp4", ".mov", ".webm", ".wav", ".mp3", ".m4a", ".json", ".md", ".txt"} {
		if ext == e {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "not allowed", http.StatusBadRequest)
		return
	}
	// 素材文件(如定妆照)会被采纳/覆盖更新:禁止长缓存,浏览器每次回源校验,采纳后立即显示新图
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}

func (s *Server) handleFSMedia(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "missing dir")
		return
	}
	if !fsPathAllowed(dir) {
		writeErr(w, http.StatusForbidden, "目录不在允许的管理范围内")
		return
	}
	writeJSON(w, http.StatusOK, analyzeMedia(dir))
}
