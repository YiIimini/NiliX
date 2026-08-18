package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---- 漫剧工作台(manju) 本地服务编排层 ----
// 原生并入 kb-workbench:管线全部阶段(方案/资产/预编码/渲染/质检/合成/抽卡/建项目)由 Go 实现
// (见 manju_pipeline.go / manju_comfy.go / manju_llm.go),质检与合成复用 ComfyUI venv 的 PyAV
// (scripts/manju_media.py 内嵌,go:embed)。日志格式契约:━━━ 阶段 X ━━━ / [i/n] 镜头。

const manjuRoot = `C:\Mi\Ai\WorkBench\manju`
const manjuPipeline = manjuRoot + `\direct_pipeline`

// manjuEngineDir NiliX 服务项目目录 = exe 所在目录（随项目整体移动零成本，取不到时兜底 manju 根）。
var manjuEngineDir = func() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return manjuRoot + `\NiliX`
}()

// manjuSettingsFile 漫剧默认 DeepSeek API Key（存 NiliX 项目目录，不再放 manju/server）。
var manjuSettingsFile = filepath.Join(manjuEngineDir, "server", "settings.json")

// manjuLegacySettingsFile 旧位置（kb-workbench 原版 manju/server/settings.json），仅用于迁移。
const manjuLegacySettingsFile = manjuRoot + `\server\settings.json`

// manjuPython 复用 Comfy Desktop 内置 venv 的 python(管线依赖 PyAV 等)
var manjuPython = filepath.Join(comfyRoot, ".venv", "Scripts", "python.exe")

// manjuSkipDirs 非项目目录(即使内部有 config.json 也跳过,如 manju_pipeline)
var manjuSkipDirs = map[string]bool{
	"direct_pipeline": true, "manju_pipeline": true, "logs": true, "_archive": true,
	"tools": true, ".tools": true, "__pycache__": true, ".season": true,
	"assets": true, "analysis": true, "clips": true, "tts": true,
	"server": true, "workbench": true,
}

// reManjuChapter 章节号:^# 第N章(与 direct_render.py _extract_chapters 同款)
var reManjuChapter = regexp.MustCompile(`(?m)^#\s*第\s*(\d+)\s*章`)

// reManjuStage 从日志解析当前管线阶段(━━━ 阶段 X ━━━)
var reManjuStage = regexp.MustCompile(`━━━ 阶段 (\w+)`)

// reManjuShot 从日志解析镜头进度([3/15] = 正在处理第 3 镜,共 15)
var reManjuShot = regexp.MustCompile(`\[(\d+)/(\d+)\]`)

// manjuStageOrder 管线阶段顺序(与前端 FLOW 一致,用于计算总体进度)
var manjuStageOrder = []string{"env", "plan", "assets", "encode", "render", "qc", "assemble"}

var manjuPhases = map[string]bool{
	"all": true, "plan": true, "assets": true, "encode": true, "render": true, "qc": true, "assemble": true,
}

// manjuIntField 渲染参数整型字段 + 取值范围
type manjuIntField struct{ min, max int }

var manjuRenderIntFields = map[string]manjuIntField{
	"width":            {128, 2048},
	"height":           {128, 2048},
	"fps":              {8, 60},
	"steps":            {1, 60},
	"turbo_steps":      {1, 30},
	"min_shot_seconds": {1, 15},
	"max_shot_seconds": {1, 15},
}

// manjuRenderStrFields 渲染参数字符串字段(模型名/地址 + 运行参数)
var manjuRenderStrFields = []string{
	"comfy_url", "neg_prompt", "unet_fl2va", "unet_ref2va", "clip", "vae_video", "vae_audio",
	"z_image_unet", "z_image_clip", "z_image_vae", "turbo_lora", "animagine_ckpt",
	"chapters", "episode", "shots",
}

// ---- 任务状态(线程安全) ----

type manjuTask struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	running     bool
	stage       string
	log         string
	rc          *int // nil = 从未运行
	done        bool
	started     time.Time
	elapsed     int // 完成时的总耗时(秒);结束后不再随活时钟增长
	baseElapsed int // 续跑累加基数:上次被中断/失败的耗时,新任务从该值继续累计
	stopped     bool
	killedPID   int
	project     string // 当前项目名(通知文案用)
	episode     string // 当前集号
}

var manjuState = &manjuTask{}

// manjuDiskState 项目级落盘的运行状态(存对应项目目录 run_state.json,删除项目即随目录一起删除,
// 新项目/同名重建不会读到遗留数据)。任务完成/失败/停止/运行中都会更新本文件。
type manjuDiskState struct {
	Running      bool   `json:"running"`
	Stage        string `json:"stage"`
	Done         bool   `json:"done"`
	RC           *int   `json:"rc"`
	Stopped      bool   `json:"stopped"`
	StartedAt    int64  `json:"startedAt"`
	PID          int    `json:"pid"`
	Episode      string `json:"episode"`
	CurrentStage string `json:"currentStage"`
	StageIdx     int    `json:"stageIdx"`
	ShotCur      int    `json:"shotCur"`
	ShotTotal    int    `json:"shotTotal"`
	ElapsedSec   int    `json:"elapsedSec"`
	LogTail      string `json:"logTail"`
	UpdatedAt    int64  `json:"updatedAt"`
}

// manjuRunStatePath 项目运行状态文件:<项目目录>/run_state.json
func manjuRunStatePath(project string) string {
	return filepath.Join(manjuRoot, project, "run_state.json")
}

// manjuRunLogPath 项目运行日志:<项目目录>/run.log
func manjuRunLogPath(project string) string {
	return filepath.Join(manjuRoot, project, "run.log")
}

func writeManjuDiskState(project string, ds *manjuDiskState) {
	if project == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(manjuRunStatePath(project)), 0755)
	b, _ := json.Marshal(ds)
	_ = os.WriteFile(manjuRunStatePath(project), b, 0644)
}

func loadManjuDiskState(project string) *manjuDiskState {
	if project == "" {
		return nil
	}
	b, err := os.ReadFile(manjuRunStatePath(project))
	if err != nil {
		return nil
	}
	var ds manjuDiskState
	if json.Unmarshal(b, &ds) != nil {
		return nil
	}
	return &ds
}

// readManjuRunLogTail 读项目日志尾部(重启恢复时补全日志展示)
func readManjuRunLogTail(project string) string {
	b, err := os.ReadFile(manjuRunLogPath(project))
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 30000 {
		s = s[len(s)-30000:]
	}
	return s
}

func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf("%d", pid))
}

// ---- 运行状态已并入项目目录 run_state.json(见 manjuDiskState),删除项目即删除状态 ----

// ---- 漫剧阶段切换通知(推送到微信,多渠道可选) ----

const manjuNotifyFile = manjuRoot + `\logs\notify.json`

// manjuStageName 阶段 key → 中文名(通知文案)
var manjuStageName = map[string]string{
	"env": "环境自检", "plan": "方案", "assets": "资产", "encode": "编码",
	"render": "渲染", "qc": "质检", "assemble": "合成",
}

// manjuNotify 通知配置(落盘 logs\notify.json)
// Channel: serverchan(Server酱) / pushplus(PushPlus) / wecom(企业微信群机器人) / wxpusher(WxPusher) / custom(自定义 webhook)
type manjuNotify struct {
	Enabled  bool   `json:"enabled"`
	Channel  string `json:"channel"`
	Endpoint string `json:"endpoint"` // wecom/custom: 完整 webhook URL
	Token    string `json:"token"`    // serverchan: SendKey / pushplus: token / wxpusher: appToken / custom: 鉴权
	UID      string `json:"uid"`      // wxpusher: 目标 uid(多个用逗号分隔)
}

func loadManjuNotify() manjuNotify {
	n := manjuNotify{Channel: "serverchan"}
	b, err := os.ReadFile(manjuNotifyFile)
	if err == nil {
		_ = json.Unmarshal(b, &n)
	}
	return n
}

func saveManjuNotify(n manjuNotify) {
	_ = os.MkdirAll(filepath.Dir(manjuNotifyFile), 0755)
	b, _ := json.MarshalIndent(n, "", "  ")
	_ = os.WriteFile(manjuNotifyFile, b, 0644)
}

// manjuPostJSON 异步 POST JSON(失败静默)
func manjuPostJSON(target, bearer string, payload any) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", target, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	go func() { _, _ = client.Do(req) }()
}

// manjuPostForm 异步 POST 表单(失败静默)
func manjuPostForm(target string, values map[string]string) {
	form := neturl.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	req, err := http.NewRequest("POST", target, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second}
	go func() { _, _ = client.Do(req) }()
}

// manjuNotifySend 异步推送一条文本通知(失败静默,不影响渲染主流程)
func manjuNotifySend(msg string) {
	n := loadManjuNotify()
	if !n.Enabled {
		return
	}
	switch n.Channel {
	case "serverchan":
		manjuPostForm("https://sctapi.ftqq.com/"+n.Token+".send", map[string]string{"title": msg, "desp": msg})
	case "pushplus":
		manjuPostJSON("https://www.pushplus.plus/send", "", map[string]any{
			"token": n.Token, "title": msg, "content": msg, "template": "txt",
		})
	case "wecom":
		if n.Endpoint == "" {
			return
		}
		manjuPostJSON(n.Endpoint, "", map[string]any{
			"msgtype": "text", "text": map[string]any{"content": msg},
		})
	case "wxpusher":
		uids := []string{}
		for _, u := range strings.Split(n.UID, ",") {
			if u = strings.TrimSpace(u); u != "" {
				uids = append(uids, u)
			}
		}
		manjuPostJSON("http://wxpusher.zjiecode.com/api/send/message", "", map[string]any{
			"appToken": n.Token, "content": msg, "summary": msg, "contentType": 1, "uids": uids,
		})
	default: // custom: 通用 webhook(企业微信机器人格式 + 可选 Bearer 鉴权)
		if n.Endpoint == "" {
			return
		}
		manjuPostJSON(n.Endpoint, n.Token, map[string]any{
			"msgtype": "text", "text": map[string]any{"content": msg},
		})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func manjuHas(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x) != ""
	case float64, int, json.Number:
		return true
	}
	return false
}

func manjuToInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// ---- config.json 读写(map 保留未知字段,往返不丢) ----

func readManjuConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func writeManjuConfig(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// writeManjuRunParams 把章节/集号/镜头写入 config.render(新管线从 render 读取运行参数)
func writeManjuRunParams(configPath, chapters, episode, shots string) error {
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		return err
	}
	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
		cfg["render"] = R
	}
	if chapters != "" {
		R["chapters"] = chapters
	}
	if episode != "" {
		R["episode"] = episode
	}
	if shots != "" {
		R["shots"] = shots
	} else {
		delete(R, "shots") // 镜头清空时删除旧值
	}
	return writeManjuConfig(configPath, cfg)
}

func listManjuProjects() []map[string]any {
	out := []map[string]any{}
	entries, err := os.ReadDir(manjuRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || manjuSkipDirs[name] {
			continue
		}
		cfg := filepath.Join(manjuRoot, name, "config.json")
		if _, err := os.Stat(cfg); err == nil {
			out = append(out, map[string]any{"name": name, "configPath": cfg})
		}
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["name"]) < str(out[j]["name"]) })
	return out
}

// ---- settings(默认 API Key) ----

// readManjuAPIKeyFile 读指定 settings 文件的 api_key。
func readManjuAPIKeyFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s map[string]any
	if json.Unmarshal(data, &s) != nil {
		return ""
	}
	return str(s["api_key"])
}

func manjuDefaultAPIKey() string {
	if k := readManjuAPIKeyFile(manjuSettingsFile); k != "" {
		return k
	}
	// 迁移:旧位置(manju/server/settings.json)有 key 时搬到新位置,并删除旧文件。
	if k := readManjuAPIKeyFile(manjuLegacySettingsFile); k != "" {
		_ = writeManjuSettings(map[string]any{"api_key": k})
		_ = os.Remove(manjuLegacySettingsFile)
		return k
	}
	return ""
}

func writeManjuSettings(s map[string]any) error {
	_ = os.MkdirAll(filepath.Dir(manjuSettingsFile), 0755)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manjuSettingsFile, data, 0644)
}

// ---- 同步子进程(create / env 自检) ----

func runManjuSync(argv []string, cwd string, timeout time.Duration, extraEnv map[string]string) (int, string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	env := append(os.Environ(), "PYTHONIOENCODING=utf-8")
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return -1, err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		rc := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				rc = ee.ExitCode()
			} else {
				rc = -1
			}
		}
		return rc, out.String()
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return -1, "超时(" + timeout.String() + ")"
	}
}

// ---- 各端点处理器 ----

func manjuProject(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	name := ""
	if P, ok := cfg["paths"].(map[string]any); ok {
		name = filepath.Base(str(P["workdir"]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       name,
		"style":      cfg["style"],
		"llm":        cfg["llm"],
		"render":     cfg["render"],
		"moderation": cfg["moderation"],
		"paths":      cfg["paths"],
	})
}

// manjuStyleInfo 解析当前风格(预设/组合/自定义均可):返回注入各处的英文措辞,供前端「?」弹窗详细说明
func manjuStyleInfo(w http.ResponseWriter, r *http.Request) {
	style := strings.TrimSpace(r.URL.Query().Get("style"))
	if style == "" {
		style = "2.5d"
	}
	spec := manjuStyleDesc(style)
	writeJSON(w, http.StatusOK, map[string]any{
		"style":   style,
		"asset":   spec.asset,
		"opening": spec.opening,
		"shot1":   spec.shot1,
	})
}

func manjuSaveRender(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	configPath := str(body["config"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 渲染风格(顶层 style):单个预设 key / 多元素组合(预设+预设,以 + 分隔) / 自定义英文描述均可;
	// 组合与自定义由 manjuStyleDesc 原样拼入提示词,不在此枚举硬编码
	if v, ok := body["style"]; ok && manjuHas(v) {
		s := str(v)
		if strings.TrimSpace(s) == "" {
			http.Error(w, `{"error":"style 不能为空"}`, http.StatusBadRequest)
			return
		}
		cfg["style"] = s
	}

	R, _ := cfg["render"].(map[string]any)
	if R == nil {
		R = map[string]any{}
		cfg["render"] = R
	}

	// 整型字段(范围校验)
	for k, lim := range manjuRenderIntFields {
		v, present := body[k]
		if !present || !manjuHas(v) {
			continue
		}
		n, ok := manjuToInt(v)
		if !ok || n < lim.min || n > lim.max {
			http.Error(w, `{"error":"字段 `+k+` 非法(需 `+strconv.Itoa(lim.min)+`-`+strconv.Itoa(lim.max)+` 整数)"}`, http.StatusBadRequest)
			return
		}
		R[k] = n
	}
	// seed(整型,无范围限制)
	if v, present := body["seed"]; present && manjuHas(v) {
		n, ok := manjuToInt(v)
		if !ok {
			http.Error(w, `{"error":"seed 非法"}`, http.StatusBadRequest)
			return
		}
		R["seed"] = n
	}
	// 字符串字段(模型名/地址)
	for _, k := range manjuRenderStrFields {
		if v, present := body[k]; present {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				R[k] = s
			}
		}
	}
	// 人物一致性:char_models(男/女 checkpoint,非写实风格定妆照用)
	setCharModel := func(key, g string) {
		if v, present := body[key]; present {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				cm, _ := R["char_models"].(map[string]any)
				if cm == nil {
					cm = map[string]any{}
					R["char_models"] = cm
				}
				cm[g] = s
			}
		}
	}
	setCharModel("charModelMale", "男")
	setCharModel("charModelFemale", "女")

	// 内容审核:违禁词 + 违规画面打码
	MOD, _ := cfg["moderation"].(map[string]any)
	if MOD == nil {
		MOD = map[string]any{}
	}
	if v, present := body["banned_words"]; present {
		if arr, ok := v.([]any); ok {
			words := make([]string, 0, len(arr))
			for _, it := range arr {
				if s := strings.TrimSpace(str(it)); s != "" {
					words = append(words, s)
				}
			}
			MOD["banned_words"] = words
		}
	}
	if v, present := body["mosaic_enabled"]; present {
		if b, ok := v.(bool); ok {
			MOD["mosaic_enabled"] = b
		}
	}
	if v, present := body["mosaic_level"]; present {
		n, ok := manjuToInt(v)
		if !ok || n < 2 || n > 64 {
			http.Error(w, `{"error":"mosaic_level 需 2-64 整数(马赛克块边长)"}`, http.StatusBadRequest)
			return
		}
		MOD["mosaic_level"] = n
	}
	cfg["moderation"] = MOD

	if err := writeManjuConfig(configPath, cfg); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "render": R, "style": cfg["style"], "moderation": cfg["moderation"]})
}

// manjuFindByNovel 按小说目录/文件查找已有项目(阅读器「漫剧制作」入口)。
// 匹配 = 项目 paths.novel_dir 与目标目录相同,或 paths.novel 文件相同(大小写/分隔符不敏感);
// 仅同名但小说目录不同的项目不算同一本书,返回 nameTaken 提示避免误匹配。
func manjuFindByNovel(w http.ResponseWriter, r *http.Request) {
	novel := strings.TrimSpace(r.URL.Query().Get("novel"))
	if novel == "" {
		http.Error(w, `{"error":"missing novel"}`, http.StatusBadRequest)
		return
	}
	norm := normNovelPath(novel)
	bookName := strings.TrimRight(filepath.Base(strings.ReplaceAll(novel, "\\", "/")), "/")
	nameTaken := ""
	for _, p := range listManjuProjects() {
		cfg, err := readManjuConfig(str(p["configPath"]))
		if err != nil {
			continue
		}
		P, _ := cfg["paths"].(map[string]any)
		pd := normNovelPath(str(P["novel_dir"]))
		pn := normNovelPath(str(P["novel"]))
		if nameTaken == "" && bookName != "" && strings.EqualFold(str(p["name"]), bookName) {
			nameTaken = str(p["name"])
		}
		// 小说目录指向相同目录 或 小说文件相同 → 同一本书
		sameBook := (pd != "" && pd == norm) || (pn != "" && pn == norm)
		// 兜底:项目小说文件位于目标目录内(项目是选小说文件建的,nove_dir 可能指向 全本/ 子目录,
		// 而阅读器传入书根目录) → 仍是同一本书
		if !sameBook && pn != "" && strings.HasPrefix(pn, norm+"/") {
			sameBook = true
		}
		if sameBook {
			R, _ := cfg["render"].(map[string]any)
			writeJSON(w, http.StatusOK, map[string]any{
				"exists": true, "name": str(p["name"]), "configPath": str(p["configPath"]),
				"chapters": str(R["chapters"]), "episode": str(R["episode"]),
				"novel": str(P["novel"]), "novelDir": str(P["novel_dir"]),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": false, "nameTaken": nameTaken})
}

// normNovelPath 小说路径归一化:统一分隔符/小写/去尾斜杠,用于目录身份比对
func normNovelPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\\", "/")
	return strings.ToLower(strings.TrimRight(s, "/"))
}

func manjuCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(str(body["name"]))
	novel := strings.TrimSpace(str(body["novel"]))
	apiKey := strings.TrimSpace(str(body["apiKey"]))
	if name == "" || novel == "" {
		http.Error(w, `{"error":"请填剧名与小说路径"}`, http.StatusBadRequest)
		return
	}
	if apiKey == "" {
		apiKey = manjuDefaultAPIKey()
	}
	out, configPath, ok := manjuCreateProject(name, novel, apiKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         ok,
		"exitCode":   boolExitCode(ok),
		"output":     out,
		"configPath": configPath,
	})
}

func boolExitCode(ok bool) int {
	if ok {
		return 0
	}
	return 1
}

func manjuSettingsGet(w http.ResponseWriter, r *http.Request) {
	key := manjuDefaultAPIKey()
	masked := ""
	if len(key) > 9 {
		masked = key[:5] + "…" + key[len(key)-4:]
	}
	res := map[string]any{"hasKey": key != "", "masked": masked}
	// 附带当前项目的 key 状态(设置弹窗展示,提示是否会导致 LLM 401)
	if cfgPath := r.URL.Query().Get("config"); cfgPath != "" {
		if cfg, err := readManjuConfig(cfgPath); err == nil {
			if L, ok := cfg["llm"].(map[string]any); ok {
				if pk := str(L["api_key"]); pk != "" {
					pm := pk
					if len(pm) > 9 {
						pm = pm[:5] + "…" + pm[len(pm)-4:]
					}
					res["projectKey"] = true
					res["projectMasked"] = pm
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func manjuSettingsPost(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if clear, _ := body["clear"].(string); clear == "1" {
		_ = writeManjuSettings(map[string]any{})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": true})
		return
	}
	if apiKey := strings.TrimSpace(str(body["apiKey"])); apiKey != "" {
		_ = writeManjuSettings(map[string]any{"api_key": apiKey})
		// 可选:同时把 key 写入指定项目的 config(设置弹窗「应用到此项目」)
		if cfgPath := str(body["config"]); cfgPath != "" {
			if cfg, err := readManjuConfig(cfgPath); err == nil {
				L, _ := cfg["llm"].(map[string]any)
				if L == nil {
					L = map[string]any{}
				}
				L["api_key"] = apiKey
				cfg["llm"] = L
				_ = writeManjuConfig(cfgPath, cfg)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "nothing to save"})
}

func manjuNovelInfo(w http.ResponseWriter, r *http.Request) {
	novel := r.URL.Query().Get("novel")
	configPath := r.URL.Query().Get("config")
	if novel == "" && configPath != "" {
		if cfg, err := readManjuConfig(configPath); err == nil {
			if P, ok := cfg["paths"].(map[string]any); ok {
				novel = str(P["novel"])
			}
		}
	}
	if novel == "" {
		http.Error(w, `{"error":"missing novel"}`, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(novel)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error(), "path": novel})
		return
	}
	text := string(data)
	nums := reManjuChapter.FindAllStringSubmatch(text, -1)
	count := len(nums)
	first, last := 0, 0
	if count > 0 {
		first, _ = strconv.Atoi(nums[0][1])
		last, _ = strconv.Atoi(nums[count-1][1])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": novel, "chars": len([]rune(text)), "count": count,
		"first": first, "last": last,
	})
}

// manjuNovelSave 把粘贴的小说文章保存为 .md 文件，供「小说来源」管线使用。
func manjuNovelSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, `{"error":"文章内容为空"}`, http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "未命名小说"
	}
	dir := `C:\Mi\Ai\WorkBench\novel`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	safe := regexp.MustCompile(`[\\/:*?"<>|]`).ReplaceAllString(title, "_")
	path := filepath.Join(dir, safe+".md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// manjuModelDirs 各模型字段对应的 ComfyUI 模型子目录（依次查找，取第一个非空）。
var manjuModelDirs = map[string][]string{
	"unet_fl2va":   {"diffusion_models", "unet"},
	"unet_ref2va":  {"diffusion_models", "unet"},
	"z_image_unet": {"diffusion_models", "unet"},
	"clip":         {"text_encoders", "clip"},
	"z_image_clip": {"text_encoders", "clip"},
	"vae_video":    {"vae"},
	"vae_audio":    {"vae"},
	"z_image_vae":  {"vae"},
	"turbo_lora":   {"loras"},
	"char_male":    {"checkpoints"},
	"char_female":  {"checkpoints"},
	"animagine":    {"checkpoints"},
}

// manjuModels 返回各模型字段的可选模型列表(从 ComfyUI 模型目录读取),供前端下拉选择。
func manjuModels(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for field, dirs := range manjuModelDirs {
		names := []string{}
		for _, d := range dirs {
			dir := filepath.Join(comfyShared, "models", d)
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext == ".safetensors" || ext == ".ckpt" || ext == ".pt" || ext == ".bin" {
					names = append(names, e.Name())
				}
			}
			if len(names) > 0 {
				break
			}
		}
		sort.Strings(names)
		out[field] = names
	}
	writeJSON(w, http.StatusOK, out)
}

func manjuEnv(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	if configPath == "" {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		configPath = str(body["config"])
	}
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	out := manjuEnvCheck(configPath)
	rc := 0
	if i := strings.LastIndex(out, "[exit "); i >= 0 {
		rc, _ = strconv.Atoi(strings.TrimSuffix(out[i+len("[exit "):], "]"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": rc == 0, "exitCode": rc, "output": out})
}

func manjuRun(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	chapters := orDefault(str(body["chapters"]), "1-3")
	episode := orDefault(str(body["episode"]), "EP01")
	phase := str(body["phase"])
	only := str(body["only"])
	novel := str(body["novel"])
	fresh, _ := body["fresh"].(bool) // 重跑:先清空项目旧产物(方案/镜头/成片/缓存)
	agentMode, _ := body["agent"].(bool) // 智能体调度:剧本复核 + 审片官判分 + 自动返工 + 例外升级

	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	if phase != "" && !manjuPhases[phase] {
		http.Error(w, `{"error":"未知阶段: `+phase+`(可用 all/plan/assets/encode/render/qc/assemble)"}`, http.StatusBadRequest)
		return
	}

	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"已有任务运行中，先停止"}`, http.StatusConflict)
		return
	}
	// 续跑耗时累加:上次运行被手动停止或失败时,总耗时从上次冻结值继续累计(不重新计时);
	// 上次成功完成则视为全新一轮,从 0 开始
	projName := filepath.Base(filepath.Dir(configPath))
	baseElapsed := 0
	if ps := loadManjuDiskState(projName); ps != nil && (ps.Stopped || (ps.RC != nil && *ps.RC != 0)) {
		baseElapsed = ps.ElapsedSec
	}
	manjuState.running = true
	manjuState.stage = orDefault(phase, "all")
	manjuState.log = ""
	manjuState.rc = nil
	manjuState.done = false
	manjuState.started = time.Now()
	manjuState.baseElapsed = baseElapsed
	manjuState.stopped = false
	manjuState.killedPID = 0
	manjuState.project = projName
	manjuState.episode = episode
	manjuState.mu.Unlock()

	// 落盘到对应项目目录:清空上次日志 + 写入"运行中"状态(服务重启后按项目恢复,删项目即删状态)
	_ = os.MkdirAll(filepath.Dir(manjuRunStatePath(projName)), 0755)
	_ = os.WriteFile(manjuRunLogPath(projName), nil, 0644)
	writeManjuDiskState(projName, &manjuDiskState{Running: true, Stage: orDefault(phase, "all"), StartedAt: time.Now().Unix(), Episode: episode})

	// 新管线:章节/集号/镜头从 config.render 读取(render.chapters/episode/shots),先写入再启动
	if err := writeManjuRunParams(configPath, chapters, episode, only); err != nil {
		manjuState.mu.Lock()
		manjuState.running = false
		rc := -1
		manjuState.rc = &rc
		manjuState.done = true
		manjuState.mu.Unlock()
		writeManjuDiskState(projName, &manjuDiskState{Running: false, Stage: manjuState.stage, Done: true, RC: &rc, StartedAt: manjuState.started.Unix(), Episode: episode})
		http.Error(w, `{"error":"写入渲染参数失败: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	// Go 管线:全部阶段在本进程内实现(替代 Python 子进程 spawn)
	ctx, err := newManjuCtx(configPath, episode, chapters, only, novel)
	if err != nil {
		manjuState.mu.Lock()
		manjuState.running = false
		rc := -1
		manjuState.rc = &rc
		manjuState.done = true
		manjuState.mu.Unlock()
		writeManjuDiskState(projName, &manjuDiskState{Running: false, Stage: manjuState.stage, Done: true, RC: &rc, StartedAt: manjuState.started.Unix(), Episode: episode})
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	runFile, _ := os.OpenFile(manjuRunLogPath(projName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	lg := newManjuLogger(manjuState, runFile, ctx.project, ctx.episode)
	phaseName := orDefault(phase, "all")

	// 全本自动分段分集:把整本书按字数预算切成多集,逐集跑管线,免去用户手动换集号
	autoEps, aerr := manjuAutoEpisodes(ctx, chapters)
	if aerr != nil {
		manjuState.mu.Lock()
		manjuState.log += "\n⚠️ 自动分集失败: " + aerr.Error() + "，按单集运行"
		manjuState.mu.Unlock()
		autoEps = nil
	}
	if len(autoEps) > 0 {
		label := autoEps[0].Episode
		if len(autoEps) > 1 {
			label += "-" + autoEps[len(autoEps)-1].Episode
		}
		manjuState.mu.Lock()
		manjuState.episode = label
		manjuState.mu.Unlock()
		lg.logf("📺 全本自动分集 → " + label + "（共 " + strconv.Itoa(len(autoEps)) + " 集，每集约 1.8 万字）")
	}
	go func() {
		rc := 0
		// 智能模式走 agent 调度壳(阶段函数复用,qc 扩展为审片+返工闭环),普通模式原样
		runPipeline := func(c *manjuCtx, l *manjuLogger) int {
			if agentMode {
				return manjuAgentPipelineRun(c, phaseName, l)
			}
			return manjuPipelineRun(c, phaseName, l)
		}
		// 重跑模式:先清空项目旧产物(方案/镜头/成片/条件缓存,资产保留),再从头跑
		if fresh {
			lg.logf("🔁 重跑模式:清空项目旧产物(方案/镜头/成片/缓存)，资产(定妆照/场景图)保留")
			manjuClearProject(ctx, lg)
		}
		if agentMode {
			lg.logf("🤖 智能体调度模式:剧本师复核 + 审片官判分 + 自动返工(预算 " + strconv.Itoa(loadAgentCfg(ctx).MaxRetries) + " 轮) + 例外升级")
		}
		if len(autoEps) == 0 {
			rc = runPipeline(ctx, lg)
		} else {
			for i, seg := range autoEps {
				if lg.stopped() {
					lg.logf("⏹ 任务已被手动停止。已完成集保留，可直接续跑。")
					rc = 0
					break
				}
				epCtx, eerr := newManjuCtx(configPath, seg.Episode, seg.Chapters, only, novel)
				if eerr != nil {
					lg.logf("❌ 自动分集 " + seg.Episode + " 初始化失败: " + eerr.Error())
					rc = 1
					break
				}
				epCtx.auto = true
				epLg := newManjuLogger(manjuState, runFile, epCtx.project, seg.Episode)
				lg.logf("🎬 第 " + strconv.Itoa(i+1) + "/" + strconv.Itoa(len(autoEps)) + " 集 " + seg.Episode + "（章节 " + seg.Chapters + "）")
				rc = runPipeline(epCtx, epLg)
				if rc != 0 {
					break
				}
			}
		}
		if runFile != nil {
			_ = runFile.Close()
		}
		manjuFinish(rc)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stage": phaseName})
}

func manjuKill(w http.ResponseWriter, r *http.Request) {
	manjuState.mu.Lock()
	if !manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"无运行任务"}`, http.StatusConflict)
		return
	}
	manjuState.stopped = true
	manjuState.mu.Unlock()
	// 中断 ComfyUI 正在执行的任务,让等待快速返回(Go 管线在下一检查点收尾)
	client := &http.Client{Timeout: 8 * time.Second}
	if resp, err := client.Post("http://127.0.0.1:8190/interrupt", "application/json", nil); err == nil {
		_ = resp.Body.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pid": 0})
}

// manjuProgressInfo 从日志解析出的进度信息
type manjuProgressInfo struct {
	CurrentStage string
	ShotCur      int
	ShotTotal    int
	StageIdx     int
	Progress     float64
}

// parseManjuProgress 从日志解析当前阶段(━━━ 阶段 X)与镜头进度([3/15])并计算总体进度
func parseManjuProgress(log string, running bool) manjuProgressInfo {
	info := manjuProgressInfo{StageIdx: -1}
	if m := reManjuStage.FindAllStringSubmatch(log, -1); len(m) > 0 {
		info.CurrentStage = m[len(m)-1][1]
	}
	if m := reManjuShot.FindAllStringSubmatch(log, -1); len(m) > 0 {
		info.ShotCur, _ = strconv.Atoi(m[len(m)-1][1])
		info.ShotTotal, _ = strconv.Atoi(m[len(m)-1][2])
	}
	for i, k := range manjuStageOrder {
		if k == info.CurrentStage {
			info.StageIdx = i
			break
		}
	}
	if running && info.StageIdx >= 0 {
		n := float64(len(manjuStageOrder))
		if (info.CurrentStage == "render" || info.CurrentStage == "encode") && info.ShotTotal > 0 {
			info.Progress = (float64(info.StageIdx) + float64(info.ShotCur)/float64(info.ShotTotal)) / n * 100
		} else {
			info.Progress = (float64(info.StageIdx) + 0.5) / n * 100
		}
		if info.Progress < 1 {
			info.Progress = 1
		}
		if info.Progress > 99 {
			info.Progress = 99
		}
	}
	return info
}

func manjuStatus() map[string]any {
	return manjuStatusFor("")
}

// manjuDiskStatus 从项目落盘状态构造 status 响应(无状态文件 → 空闲)
func manjuDiskStatus(project string, ds *manjuDiskState) map[string]any {
	if ds == nil {
		return map[string]any{
			"running": false, "stage": "", "currentStage": "", "stageIdx": -1,
			"stageTotal": len(manjuStageOrder), "shotCur": 0, "shotTotal": 0, "progress": 0,
			"done": false, "rc": nil, "stopped": false, "elapsedSec": 0, "logTail": "",
		}
	}
	running := ds.Running
	done := ds.Done
	stopped := ds.Stopped
	// 落盘说运行中但管线进程已死(服务重启/硬杀):纠正为被中断
	if running && !isPidAlive(ds.PID) {
		running = false
		done = true
		stopped = true
	}
	elapsed := ds.ElapsedSec
	if running && ds.StartedAt > 0 {
		elapsed = int(time.Now().Unix() - ds.StartedAt)
	}
	logStr := ds.LogTail
	if logStr == "" && project != "" {
		logStr = readManjuRunLogTail(project)
	}
	info := parseManjuProgress(logStr, running)
	return map[string]any{
		"running":      running,
		"stage":        ds.Episode,
		"currentStage": info.CurrentStage,
		"stageIdx":     info.StageIdx,
		"stageTotal":   len(manjuStageOrder),
		"shotCur":      info.ShotCur,
		"shotTotal":    info.ShotTotal,
		"progress":     info.Progress,
		"done":         done,
		"rc":           ds.RC,
		"stopped":      stopped,
		"elapsedSec":   elapsed,
		"logTail":      logStr,
	}
}

// manjuStatusFor 查询指定项目的运行状态:内存任务属于该项目 → 实时状态;
// 否则读该项目目录 run_state.json(重启恢复/其他项目,删项目即无状态 → 空闲)
func manjuStatusFor(config string) map[string]any {
	manjuState.mu.Lock()
	running := manjuState.running
	stage := manjuState.stage
	done := manjuState.done
	rc := manjuState.rc
	stopped := manjuState.stopped
	started := manjuState.started
	frozenElapsed := manjuState.elapsed
	baseElapsed := manjuState.baseElapsed
	memLog := manjuState.log
	liveProject := manjuState.project
	authoritative := running || done || rc != nil || memLog != "" || !started.IsZero()
	manjuState.mu.Unlock()

	cfgName := ""
	if config != "" {
		cfgName = filepath.Base(filepath.Dir(config))
	}

	// 内存任务属于该项目 → 实时状态(运行中活时钟 + 累加基数;结束冻结值)
	if authoritative && cfgName != "" && liveProject == cfgName {
		elapsed := baseElapsed + int(time.Since(started).Seconds())
		if done && !running && frozenElapsed > 0 {
			elapsed = frozenElapsed
		}
		logStr := memLog
		if logStr == "" {
			logStr = readManjuRunLogTail(cfgName)
		}
		info := parseManjuProgress(logStr, running)
		return map[string]any{
			"running": running, "stage": stage, "currentStage": info.CurrentStage,
			"stageIdx": info.StageIdx, "stageTotal": len(manjuStageOrder),
			"shotCur": info.ShotCur, "shotTotal": info.ShotTotal, "progress": info.Progress,
			"done": done, "rc": rc, "stopped": stopped, "elapsedSec": elapsed, "logTail": logStr,
		}
	}
	// 其余情况:该项目目录的落盘状态(无 → 空闲)
	if cfgName != "" {
		return manjuDiskStatus(cfgName, loadManjuDiskState(cfgName))
	}
	return manjuDiskStatus("", nil)
}

// listManjuMedia 列出目录内指定扩展名的媒体文件(name/path/size),按名称排序
func listManjuMedia(dir string, exts map[string]bool) []map[string]any {
	out := []map[string]any{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !exts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, map[string]any{
				"name": e.Name(),
				"path": filepath.Join(dir, e.Name()),
				"size": info.Size(),
				"v":    info.ModTime().Unix(), // 文件版本(采纳覆盖后变化,前端用于缓存失效)
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["name"]) < str(out[j]["name"]) })
	return out
}

// reManjuEpFile 集产物文件名:EP01_direct_plan.json / EP01_成片.mp4(集号取第一个下划线前段)
var reManjuEpFile = regexp.MustCompile(`^([^_]+)_(direct_plan|shots_prompts|characters)\.json$|^([^_]+)_成片\.mp4$`)

// listManjuEpisodes 检测项目里存在的集号:镜头目录(clips/<ep>)∪ 方案文件 ∪ 成片文件,按集号排序
func listManjuEpisodes(P map[string]any) []string {
	set := map[string]bool{}
	add := func(s string) {
		if s != "" {
			set[s] = true
		}
	}
	if entries, err := os.ReadDir(filepath.Join(str(P["clips"]))); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(e.Name())
			}
		}
	}
	for _, dir := range []string{str(P["analysis"]), str(P["workdir"])} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if m := reManjuEpFile.FindStringSubmatch(e.Name()); m != nil {
				if m[1] != "" {
					add(m[1])
				} else if m[3] != "" {
					add(m[3])
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// manjuEpisodeOutputs 单个集的产物:镜头 + 成片 + 方案文件
func manjuEpisodeOutputs(P map[string]any, episode string) map[string]any {
	vidExts := map[string]bool{".mp4": true, ".mov": true, ".webm": true}
	clips := listManjuMedia(filepath.Join(str(P["clips"]), episode), vidExts)
	var final any
	if info, err := os.Stat(filepath.Join(str(P["workdir"]), episode+"_成片.mp4")); err == nil {
		final = map[string]any{"name": episode + "_成片.mp4", "path": filepath.Join(str(P["workdir"]), episode+"_成片.mp4"), "size": info.Size()}
	}
	artifacts := map[string]any{}
	for _, f := range []string{episode + "_direct_plan.json", episode + "_shots_prompts.json"} {
		if info, err := os.Stat(filepath.Join(str(P["analysis"]), f)); err == nil {
			artifacts[f] = info.Size()
		}
	}
	return map[string]any{"episode": episode, "clips": clips, "final": final, "artifacts": artifacts}
}

// manjuOutputs 产物列表:人物/场景为全项目共享,视频/方案/成片按集区分(episode 参数缺省=全部集)
func manjuOutputs(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := r.URL.Query().Get("episode") // 空 = 返回全部集
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"characters": []any{}, "scenes": []any{}, "episodes": []any{}, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)

	imgExts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	characters := listManjuMedia(filepath.Join(str(P["assets"]), "characters"), imgExts)
	scenes := listManjuMedia(filepath.Join(str(P["assets"]), "scenes"), imgExts)

	// 人物标注采纳状态(adopted.json 标记的 char → 正式定妆照)
	adoptedMap := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(str(P["assets"]), "characters", "adopted.json")); err == nil {
		_ = json.Unmarshal(b, &adoptedMap)
	}
	for _, c := range characters {
		base := str(c["name"])
		base = strings.TrimSuffix(base, filepath.Ext(base))
		c["adopted"] = adoptedMap[base] != ""
	}

	// 按集区分:全部集或单集
	eps := listManjuEpisodes(P)
	if episode != "" && episode != "all" {
		eps = []string{episode}
	}
	episodes := make([]any, 0, len(eps))
	for _, e := range eps {
		episodes = append(episodes, manjuEpisodeOutputs(P, e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"characters": characters, "scenes": scenes, "episodes": episodes})
}

// parseJSONLine 从子进程输出里解析最后一个 JSON 对象(抽卡脚本返回单行 JSON)
func parseJSONLine(s string) map[string]any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			return m
		}
	}
	return nil
}

// manjuPlan 读取方案 JSON,返回角色列表(供角色抽卡)
func manjuPlan(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := orDefault(r.URL.Query().Get("episode"), "EP01")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	// 角色方案优先读 _characters.json(角色抽卡前置阶段产物),回退旧 _direct_plan.json
	var planPath string
	var dataBytes []byte
	for _, name := range []string{episode + "_characters.json", episode + "_direct_plan.json"} {
		p := filepath.Join(str(P["analysis"]), name)
		if b, err := os.ReadFile(p); err == nil {
			planPath, dataBytes = p, b
			break
		}
	}
	if planPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "error": "角色方案未生成，请先抽卡生成角色方案"})
		return
	}
	var plan map[string]any
	if json.Unmarshal(dataBytes, &plan) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "error": "方案解析失败"})
		return
	}
	chars := []map[string]any{}
	if arr, ok := plan["characters"].([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				chars = append(chars, map[string]any{
					"id": m["id"], "gender": m["gender"], "age": m["age"],
					"appearance": m["appearance"], "costume": m["costume"],
				})
			}
		}
	}
	shots := 0
	// 镜头数单独从 _direct_plan.json 读(角色方案 _characters.json 只有角色/场景,无 shots)
	if b, err := os.ReadFile(filepath.Join(str(P["analysis"]), episode+"_direct_plan.json")); err == nil {
		var sp map[string]any
		if json.Unmarshal(b, &sp) == nil {
			if arr, ok := sp["shots"].([]any); ok {
				shots = len(arr)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": true, "episode_title": plan["episode_title"], "characters": chars, "shots": shots})
}

// manjuGacha 角色抽卡:生成一张定妆照候选(随机 seed)
func manjuGacha(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := str(body["char"])
	if configPath == "" || char == "" {
		http.Error(w, `{"error":"missing config or char"}`, http.StatusBadRequest)
		return
	}
	res, _, err := manjuGachaDraw(configPath, episode, char)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "生成失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// manjuGachaAdopt 采纳抽卡候选为正式定妆照
func manjuGachaAdopt(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := str(body["char"])
	image := str(body["image"])
	if configPath == "" || char == "" || image == "" {
		http.Error(w, `{"error":"missing config/char/image"}`, http.StatusBadRequest)
		return
	}
	if err := manjuAdoptGacha(configPath, episode, char, image); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "采纳失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sanitizeFileName 文件名安全化:替换 Windows 非法字符(角色名等用于落盘文件名)
func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	bad := map[rune]bool{'\\': true, '/': true, ':': true, '*': true, '?': true, '"': true, '<': true, '>': true, '|': true}
	return strings.Map(func(r rune) rune {
		if bad[r] {
			return '_'
		}
		return r
	}, s)
}

// manjuGachaUpload 上传角色图并直接采纳为正式定妆照(覆盖 → 指纹失效 → 后续渲染以它为身份参考)
func manjuGachaUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "解析上传失败: "+err.Error())
		return
	}
	configPath := r.FormValue("config")
	episode := orDefault(r.FormValue("episode"), "EP01")
	char := strings.TrimSpace(r.FormValue("char"))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少上传文件")
		return
	}
	defer file.Close()
	if configPath == "" || char == "" {
		writeErr(w, http.StatusBadRequest, "missing config or char")
		return
	}
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
	default:
		writeErr(w, http.StatusBadRequest, "仅支持 png/jpg/jpeg/webp 图片")
		return
	}
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, _, err := ctx.loadPlan()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "角色方案未生成，请先「生成方案」")
		return
	}
	found := false
	for _, c := range anyArr(plan["characters"]) {
		if m, ok := c.(map[string]any); ok && str(m["id"]) == char {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusBadRequest, "角色「"+char+"」不在方案中")
		return
	}
	// 先落盘到 _gacha 候选区,再走正式采纳(与抽卡候选同一路径)
	gachaDir := filepath.Join(ctx.assetsDir, "characters", "_gacha")
	if err := os.MkdirAll(gachaDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved := filepath.Join(gachaDir, sanitizeFileName(char)+"_upload_"+time.Now().Format("20060102150405")+ext)
	out, err := os.Create(saved)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Close()
	if err := manjuAdoptGacha(configPath, episode, char, saved); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "采纳失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "image": filepath.Join(ctx.assetsDir, "characters", char+".png"),
	})
}

// manjuGachaPlan 角色方案生成(LLM 直出角色/场景 → analysis/<集>_characters.json)
func manjuGachaPlan(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	chapters := str(body["chapters"])
	episode := str(body["episode"])
	shots := str(body["shots"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	// 角色方案生成也读 render.chapters/episode,先写入
	if err := writeManjuRunParams(configPath, chapters, episode, shots); err != nil {
		http.Error(w, `{"error":"写入参数失败: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	if err := manjuPlanCharacters(configPath, episode); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "生成角色方案失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// registerManjuRoutes 注册漫剧工作台全部端点
func registerManjuRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/manju/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"projects": listManjuProjects()})
	})
	mux.HandleFunc("GET /api/manju/find", manjuFindByNovel)
	mux.HandleFunc("GET /api/manju/project", manjuProject)
	mux.HandleFunc("POST /api/manju/render", manjuSaveRender)
	mux.HandleFunc("GET /api/manju/style", manjuStyleInfo)
	mux.HandleFunc("POST /api/manju/create", manjuCreate)
	mux.HandleFunc("GET /api/manju/settings", manjuSettingsGet)
	mux.HandleFunc("POST /api/manju/settings", manjuSettingsPost)
	mux.HandleFunc("GET /api/manju/novel", manjuNovelInfo)
	mux.HandleFunc("POST /api/manju/novel/save", manjuNovelSave)
	mux.HandleFunc("GET /api/manju/models", manjuModels)
	mux.HandleFunc("POST /api/manju/env", manjuEnv)
	mux.HandleFunc("POST /api/manju/run", manjuRun)
	mux.HandleFunc("GET /api/manju/status", func(w http.ResponseWriter, r *http.Request) {
		res := manjuStatusFor(r.URL.Query().Get("config"))
		// 附带审片报告摘要(智能体模式数据源,前端复用同一轮询)
		res["agent"] = agentStatusSummary(r.URL.Query().Get("config"))
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/manju/kill", manjuKill)
	mux.HandleFunc("GET /api/manju/outputs", manjuOutputs)
	mux.HandleFunc("GET /api/manju/plan", manjuPlan)
	mux.HandleFunc("POST /api/manju/gacha", manjuGacha)
	mux.HandleFunc("POST /api/manju/gacha/upload", manjuGachaUpload)
	mux.HandleFunc("POST /api/manju/gacha/adopt", manjuGachaAdopt)
	mux.HandleFunc("POST /api/manju/gacha/plan", manjuGachaPlan)
	mux.HandleFunc("GET /api/manju/notify", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, loadManjuNotify())
	})
	mux.HandleFunc("POST /api/manju/notify", func(w http.ResponseWriter, r *http.Request) {
		var n manjuNotify
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		n.Channel = strings.TrimSpace(n.Channel)
		n.Endpoint = strings.TrimSpace(n.Endpoint)
		n.Token = strings.TrimSpace(n.Token)
		n.UID = strings.TrimSpace(n.UID)
		if n.Channel == "" {
			n.Channel = "serverchan"
		}
		saveManjuNotify(n)
		writeJSON(w, http.StatusOK, n)
	})
	mux.HandleFunc("POST /api/manju/notify/test", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msg := str(body["message"])
		if msg == "" {
			msg = "漫剧通知测试(来自 kb-workbench)"
		}
		manjuNotifySend(msg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	registerAgentRoutes(mux)
}
