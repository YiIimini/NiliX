package manju
import (
	"nilix/internal/comfy"
	"nilix/internal/paths"
	"nilix/internal/util"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// ---- 漫剧工作台(manju) 本地服务编排层 ----
// 原生并入 kb-workbench:管线全部阶段(方案/资产/预编码/渲染/质检/合成/抽卡/建项目)由 Go 实现
// (见 manju_pipeline.go / manju_comfy.go / manju_llm.go),质检与合成复用 ComfyUI venv 的 PyAV
// (scripts/manju_media.py 内嵌,go:embed)。日志格式契约:━━━ 阶段 X ━━━ / [i/n] 镜头。

// manjuPipelinePath 旧管线目录(兼容遗留;自包含部署后由 paths.ManjuRootDir 决定)
func manjuPipelinePath() string { return filepath.Join(paths.ManjuRootDir, "direct_pipeline") }

// manjuEngineDir NiliX 服务项目目录 = exe 所在目录（随项目整体移动零成本，取不到时兜底 manju 根）。
var manjuEngineDir = func() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return filepath.Join(paths.ManjuRootDir, "NiliX")
}()

// manjuSettingsFile 漫剧默认 DeepSeek API Key（存 NiliX 项目目录，不再放 manju/server）。
var manjuSettingsFile = filepath.Join(manjuEngineDir, "server", "settings.json")

// manjuLegacySettingsFile 旧位置（kb-workbench 原版 manju/server/settings.json），仅用于迁移。
var manjuLegacySettingsFile = filepath.Join(paths.ManjuRootDir, "server", "settings.json")

// manjuPythonPath ComfyUI 的 python(管线依赖 PyAV/whisper 等);随 paths.ComfyRootDir 动态解析。
// 兼容两种安装形态:官方 portable(python_embeded)与 venv 安装(.venv)。

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
var manjuStageOrder = []string{"env", "plan", "assets", "encode", "render", "qc", "assemble", "upscale"}

var manjuPhases = map[string]bool{
	"all": true, "plan": true, "assets": true, "encode": true, "render": true, "qc": true, "assemble": true,
}

// manjuIntField 渲染参数整型字段 + 取值范围
type manjuIntField struct{ min, max int }

var manjuRenderIntFields = map[string]manjuIntField{
	"width": {128, 2048},
	"height": {128, 2048},
	"fps":                {8, 60},
	"steps":              {1, 60},
	"turbo_steps":        {1, 30},
	"min_shot_seconds":   {1, 15},
	"max_shot_seconds":   {1, 15},
	"shots_per_take":     {1, 3},
	"motion_audio_context": {1, 96},
}

// manjuRenderStrFields 渲染参数字符串字段(模型名/地址 + 运行参数)
var manjuRenderStrFields = []string{
	"comfy_url", "neg_prompt", "unet_fl2va", "unet_ref2va", "clip", "vae_video", "vae_audio",
	"z_image_unet", "z_image_clip", "z_image_vae", "turbo_lora", "turbo_lora_r2v", "animagine_ckpt",
	"krea2_unet", "krea2_clip", "krea2_vae", "char_engine",
	"chapters", "episode", "shots",
	"minimax_api_key", "minimax_base_url", "jianying_dir",
}

// manjuRenderBoolFields 渲染参数布尔字段(SageAttention 加速/草稿预审开关/FL2VA 双帧/字幕烧录)
// fl2va_end_frame(审计升级 P1):空镜镜头生成场景尾帧走 FL2VA 首尾双帧插值,场景内运动更稳;
// 默认关闭(场景图成本翻倍,节点缺失自动回退单图)
// subtitle(2026-08-23 用户反馈成片字幕位文字优化):合成时是否烧录对白字幕,默认 true(保持向后兼容)
// voiceover(2026-08-23 用户反馈 03/06 镜静音):合成前给旁白/画外音补 edge-tts 后期配音,默认 false(需显式开启)
// defreeze(2026-08-27 用户反馈"质检多数不合格/像PPT"):QC 段尾冻结自动截尾(H3 固有特性,
// 程序修优于换 seed 重渲),默认开;false 关闭
var manjuRenderBoolFields = []string{"sage_attention", "draft_judge", "fl2va_end_frame", "subtitle", "voiceover", "defreeze"}

// manjuRenderFloatFields 渲染参数浮点字段 + 取值范围 [min,max]
// chars_per_sec(2026-08-26):中文语音字速预算,台词+旁白总字数÷字速 ≤ 镜头时长;
// 默认 4(保守,防 H3 念一半切镜),爽文技能契约上限 5
var manjuRenderFloatFields = map[string][2]float64{
	"draft_scale":   {0.2, 0.95},
	"bgm_gain":      {0, 1},
	"bgm_duck":      {0, 1},
	"chars_per_sec": {2, 8},
}

// manjuSeedPolicies 合法 seed 重试策略
var manjuSeedPolicies = map[string]bool{"fixed": true, "increment": true, "random": true}

// manjuTransitions 合法镜头转场(cut=硬切 / fade=闪黑 / dissolve=叠化;seam 接缝镜恒硬切)
var manjuTransitions = map[string]bool{"cut": true, "fade": true, "dissolve": true}

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
	return filepath.Join(paths.ManjuRootDir, project, "run_state.json")
}

// manjuRunLogPath 项目运行日志:<项目目录>/run.log
func manjuRunLogPath(project string) string {
	return filepath.Join(paths.ManjuRootDir, project, "run.log")
}

// manjuAppendRunLog 新一次运行开始:不清空历史日志,追加分隔线让用户看到进度累积
// ("上次跑到镜头 X,本次从哪续")。历史超上限(512KB)截断保留尾部(256KB)防无限增长。
func manjuAppendRunLog(project string) {
	p := manjuRunLogPath(project)
	const maxSize = 512 << 10
	const keepSize = 256 << 10
	if st, err := os.Stat(p); err == nil && st.Size() > maxSize {
		if b, err := os.ReadFile(p); err == nil && len(b) > keepSize {
			_ = os.WriteFile(p, b[len(b)-keepSize:], 0644)
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	sep := "\n\n══════════ " + time.Now().Format("01-02 15:04:05") + " 新一次运行(续跑) ══════════\n"
	_, _ = f.WriteString(sep)
}

func writeManjuDiskState(project string, ds *manjuDiskState) {
	if project == "" {
		return
	}
	_ = atomicWriteJSON(manjuRunStatePath(project), ds)
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


// ---- 运行状态已并入项目目录 run_state.json(见 manjuDiskState),删除项目即删除状态 ----

// ---- 漫剧阶段切换通知(推送到微信,多渠道可选) ----

var manjuNotifyFile = filepath.Join(paths.ManjuRootDir, "logs", "notify.json")

// manjuStageName 阶段 key → 中文名(通知文案)
var manjuStageName = map[string]string{
	"env": "环境自检", "plan": "方案", "assets": "资产", "encode": "编码",
	"render": "渲染", "qc": "质检", "assemble": "合成", "upscale": "云端2K",
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
	// 审计 SSRF:endpoint 仅允许 https 公网地址,拒绝 http/内网/回环——防止
	// webhook 地址被配置指向本机服务(盲打内网)
	safeEndpoint := func(u string) bool {
		p, err := neturl.Parse(u)
		if err != nil || p.Scheme != "https" || p.Host == "" {
			return false
		}
		h := p.Hostname()
		if h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0" {
			return false
		}
		if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
			return false
		}
		return true
	}
	switch n.Channel {
	case "serverchan":
		manjuPostForm("https://sctapi.ftqq.com/"+n.Token+".send", map[string]string{"title": msg, "desp": msg})
	case "pushplus":
		manjuPostJSON("https://www.pushplus.plus/send", "", map[string]any{
			"token": n.Token, "title": msg, "content": msg, "template": "txt",
		})
	case "wecom":
		if n.Endpoint == "" || !safeEndpoint(n.Endpoint) {
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
		if n.Endpoint == "" || !safeEndpoint(n.Endpoint) {
			return
		}
		manjuPostJSON(n.Endpoint, n.Token, map[string]any{
			"msgtype": "text", "text": map[string]any{"content": msg},
		})
	}
}

func str(v any) string { return util.Str(v) }

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
	// 2026-08-23 优化:容忍 UTF-8 BOM(PS/部分编辑器写 JSON 会带 BOM,json.Unmarshal 报
	// "invalid character 'ï'";外部工具改 config 后 NiliX 不至于崩)
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func writeManjuConfig(path string, cfg map[string]any) error {
	return atomicWriteJSON(path, cfg)
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
	entries, err := os.ReadDir(paths.ManjuRootDir)
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
		cfg := filepath.Join(paths.ManjuRootDir, name, "config.json")
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
	return atomicWriteJSON(manjuSettingsFile, s)
}

// ---- 原子落盘(崩溃半写防线) ----
// 状态/配置类 JSON 统一「同目录临时文件 + os.Rename」原子替换:进程崩溃/断电不会留下半写文件,
// 此前 os.WriteFile 直写,崩溃时 run_state/agent_state/manifest/render_ck 等可能损坏——
// 读取方大多"静默当空"(检查点丢失 → 重复提交重复烧 GPU)。

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	// 审计 2026-08-28:调用方误传目录(如 "..")时 base 落成 "..",临时文件以 "...tmp*" 泄漏
	// 到调用方目录(实测 409 个残留)——此处拒绝非文件名路径,从源头掐断。
	if base == "" || base == "." || base == ".." {
		return fmt.Errorf("atomicWrite: 非法路径 %q", path)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp%d", base, time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Rename 失败(目标被占用/跨设备等)必须回收临时文件,否则 .tmp 泄漏堆积
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
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
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := ""
	if P, ok := cfg["paths"].(map[string]any); ok {
		name = filepath.Base(str(P["workdir"]))
	}
	// llm 节密钥掩码(前端保存以 **** 往返表示"未修改",与 handlePutSettings 同协议)
	llmOut := map[string]any{}
	if L, ok := cfg["llm"].(map[string]any); ok {
		for k, v := range L {
			if k == "api_key" {
				llmOut[k] = maskKey(str(v))
			} else {
				llmOut[k] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       name,
		"style":      cfg["style"],
		"llm":        llmOut,
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
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	// 审计 P8:config read-modify-write 并发互斥(与 qcAcceptClear 共用 per-config 锁,
	// 防快速连点保存/停止横幅决策并发覆盖丢更新)
	cfgLock := manjuConfigLock(configPath)
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	// 集数特殊处理:空/0 → 删除 config.render.episode(自动模式,由运行时按章节数计算);
	// 纯数字 N → 存 EPxx(与内部标识一致)
	if v, present := body["episode"]; present {
		es := strings.TrimSpace(str(v))
		if es == "" || es == "0" {
			delete(R, "episode")
		} else if n, err := strconv.Atoi(es); err == nil && n > 0 {
			R["episode"] = fmt.Sprintf("EP%02d", n)
		} else if es != "" {
			R["episode"] = es // 非数字(旧 EP01 输入)原样
		}
	}
	// 分辨率档位(空=手动宽高;custom 或档位表内取值)
	if v, present := body["res_tier"]; present {
		s := strings.TrimSpace(str(v))
		if s != "" && s != "custom" {
			if _, ok := manjuResTiers[s]; !ok {
				http.Error(w, `{"error":"res_tier 非法(可选 draft/standard/fhd 或留空手动)"}`, http.StatusBadRequest)
				return
			}
		}
		R["res_tier"] = s
	}
	// 定妆引擎合法值(2026-08-24 用户规则:SDXL 已禁用,观感差):仅 zimage/krea2,非法回退 zimage
	if v, present := body["char_engine"]; present {
		s := strings.TrimSpace(str(v))
		if s == "" {
			s = "zimage"
		}
		switch s {
		case "zimage", "krea2":
		default:
			s = "zimage"
		}
		R["char_engine"] = s
	}
	// seed 重试策略
	if v, present := body["seed_policy"]; present {
		s := strings.TrimSpace(str(v))
		if !manjuSeedPolicies[s] {
			http.Error(w, `{"error":"seed_policy 非法(可选 fixed/increment/random)"}`, http.StatusBadRequest)
			return
		}
		R["seed_policy"] = s
	}
	// 转场(空=硬切)
	if v, present := body["transition"]; present {
		s := strings.TrimSpace(str(v))
		if !manjuTransitions[s] {
			http.Error(w, `{"error":"transition 非法(可选 cut/fade/dissolve)"}`, http.StatusBadRequest)
			return
		}
		R["transition"] = s
	}
	// BGM 路径(空=清除)
	if v, present := body["bgm"]; present {
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				delete(R, "bgm")
			} else {
				R["bgm"] = s
			}
		}
	}
	// 布尔字段(SageAttention/草稿预审开关)
	for _, k := range manjuRenderBoolFields {
		if v, present := body[k]; present {
			if b, ok := v.(bool); ok {
				R[k] = b
			}
		}
	}
	// 浮点字段(草稿缩放等)
	for k, lim := range manjuRenderFloatFields {
		v, present := body[k]
		if !present || !manjuHas(v) {
			continue
		}
		f, ok := manjuToFloat(v)
		if !ok || f < lim[0] || f > lim[1] {
			http.Error(w, fmt.Sprintf(`{"error":"字段 %s 非法(需 %g-%g)"}`, k, lim[0], lim[1]), http.StatusBadRequest)
			return
		}
		R[k] = f
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

	// 用户手动保存渲染配置 = 接管风格/负面(清除总集自动注入标记,
	// 下次 newManjuCtx 不再自动覆盖用户修改后的 style/neg_prompt)
	if R != nil {
		delete(R, "_prompt_master_synced")
	}

	if err := writeManjuConfig(configPath, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
	// 2026-08-24 用户要求:选目录即自动填充剧名(前端已自动填,后端兜底防手动清空/绕过前端)
	if name == "" && novel != "" {
		base := strings.TrimRight(novel, `/\`)
		if strings.HasSuffix(strings.ToLower(base), ".md") || strings.HasSuffix(strings.ToLower(base), ".txt") {
			base = filepath.Dir(base)
		}
		name = filepath.Base(base)
	}
	if name == "" {
		http.Error(w, `{"error":"请填剧名（小说可选：留空=视频脚本直出模式）"}`, http.StatusBadRequest)
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
		"scriptMode": novel == "",
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
	// 默认服务(地址/模型,供设置弹窗回填)
	if b, err := os.ReadFile(manjuSettingsFile); err == nil {
		var def map[string]any
		if json.Unmarshal(b, &def) == nil {
			res["defaultBaseUrl"] = str(def["base_url"])
			res["defaultModel"] = str(def["model"])
		}
	}
	// 附带当前项目的 key/服务状态(设置弹窗展示,提示是否会导致 LLM 401)
	// 审计 2026-08-28:config 参数此前不过 guard,可探测任意路径 JSON——补归属校验
	if cfgPath := r.URL.Query().Get("config"); cfgPath != "" {
		guarded, gerr := manjuGuardConfig(cfgPath)
		if gerr != nil {
			writeErr(w, http.StatusBadRequest, gerr.Error())
			return
		}
		if cfg, err := readManjuConfig(guarded); err == nil {
			if L, ok := cfg["llm"].(map[string]any); ok {
				res["projectBaseUrl"] = str(L["base_url"])
				res["projectModel"] = str(L["model"])
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
	apiKey := strings.TrimSpace(str(body["apiKey"]))
	baseURL := strings.TrimSpace(str(body["baseUrl"]))
	model := strings.TrimSpace(str(body["model"]))
	if apiKey != "" || baseURL != "" || model != "" {
		// 存为默认:API Key + 服务地址/模型(项目未配置时 manjuLLMFromCfg 回退使用)。
		// settings.json 同时是全局智能体默认(agent 节)的存放处,须合并写入,不能整体覆盖
		def := map[string]any{}
		if b, err := os.ReadFile(manjuSettingsFile); err == nil {
			_ = json.Unmarshal(b, &def)
		}
		if apiKey != "" {
			def["api_key"] = apiKey
		}
		if baseURL != "" {
			def["base_url"] = baseURL
		}
		if model != "" {
			def["model"] = model
		}
		_ = writeManjuSettings(def)
		// 应用到指定项目:写项目 config.llm(api_key/base_url/model,非空覆盖)
		if cfgPath := str(body["config"]); cfgPath != "" {
			cp, gerr := manjuGuardConfig(cfgPath)
			if gerr != nil {
				writeErr(w, http.StatusForbidden, gerr.Error())
				return
			}
			cfgPath = cp
			// 审计 P8:config read-modify-write 并发互斥(与 manjuSaveRender 同锁)
			cfgLock := manjuConfigLock(cfgPath)
			cfgLock.Lock()
			if cfg, err := readManjuConfig(cfgPath); err == nil {
				L, _ := cfg["llm"].(map[string]any)
				if L == nil {
					L = map[string]any{}
				}
				if apiKey != "" {
					L["api_key"] = apiKey
				}
				if baseURL != "" {
					L["base_url"] = baseURL
				}
				if model != "" {
					L["model"] = model
				}
				cfg["llm"] = L
				_ = writeManjuConfig(cfgPath, cfg)
			}
			cfgLock.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "nothing to save"})
}

func manjuNovelInfo(w http.ResponseWriter, r *http.Request) {
	novel := r.URL.Query().Get("novel")
	configPath := r.URL.Query().Get("config")
	if configPath != "" {
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		configPath = cp
	}
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
	// novel 归属校验:禁止读取小说库根目录之外的任意文件(防任意读 + 大文件整读 OOM)
	if _, gerr := manjuGuardNovel(novel); gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	// 大文件护栏:超过 64MB 的小说拒绝整读(章节统计无需读全量)
	if fi, err := os.Stat(novel); err == nil && fi.Size() > 64<<20 {
		writeErr(w, http.StatusBadRequest, "小说文件过大(>64MB),请直接使用 全本/<书名>·全本.md")
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
	// 审计 F5:标题消毒后仍可能残留 "..",直接拼接会把小说写到小说库根之外
	safeTitle := novelTitleSan.ReplaceAllString(title, "")
	if safeTitle == "" || safeTitle == "." || safeTitle == ".." || strings.Contains(safeTitle, "..") {
		writeErr(w, http.StatusBadRequest, "非法书名")
		return
	}
	dir := filepath.Join(paths.NovelRootDir, safeTitle)
	if filepath.Clean(dir) == filepath.Clean(paths.NovelRootDir) ||
		!strings.HasPrefix(filepath.Clean(dir), filepath.Clean(paths.NovelRootDir)+string(filepath.Separator)) {
		writeErr(w, http.StatusForbidden, "目标不在小说库根目录内")
		return
	}
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

// ---- 视频脚本直出模式(H3 官方格式分镜脚本 md,与小说解析并存二选一) ----

// manjuScriptTarget 解析项目 + 集号 → 脚本文件路径(workdir/script/<ep>.md)与 configPath
func manjuScriptTarget(project, episode string) (configPath, scriptPath, workdir string, err error) {
	if project == "" {
		return "", "", "", fmt.Errorf("缺少项目名")
	}
	configPath = filepath.Join(paths.ManjuRootDir, project, "config.json")
	if !fileExists(configPath) {
		return "", "", "", fmt.Errorf("项目不存在: %s", project)
	}
	cfg, cerr := readManjuConfig(configPath)
	if cerr != nil {
		return "", "", "", cerr
	}
	P, _ := cfg["paths"].(map[string]any)
	workdir = str(P["workdir"])
	if workdir == "" {
		return "", "", "", fmt.Errorf("项目 workdir 未配置")
	}
	ep := normalizeEpisode(orDefault(episode, "EP01"))
	return configPath, filepath.Join(workdir, "script", ep+".md"), workdir, nil
}

// manjuScriptSave 保存视频渲染脚本 md(官方 H3 分镜格式)并启用脚本直出模式:
// 写 <workdir>/script/<ep>.md + config paths.script 指向它。脚本文件指纹变化
// (size/mtime)会使旧方案过期自动重新生成,无需手动清产物。
func manjuScriptSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Episode string `json:"episode"`
		Text    string `json:"text"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, `{"error":"脚本内容为空"}`, http.StatusBadRequest)
		return
	}
	configPath, scriptPath, _, err := manjuScriptTarget(body.Project, body.Episode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(scriptPath, []byte(text), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 审计 2026-08-28:config 读-改-写统一走 per-config 锁(与 manjuSaveRender 一致,防并发丢更新)
	cfgLock := manjuConfigLock(configPath)
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
		cfg["paths"] = P
	}
	P["script"] = scriptPath
	if err := writeManjuConfig(configPath, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": scriptPath, "script_mode": true})
}

// manjuScriptClear 清除视频脚本,项目回到小说解析模式
func manjuScriptClear(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		http.Error(w, `{"error":"缺少项目名"}`, http.StatusBadRequest)
		return
	}
	configPath := filepath.Join(paths.ManjuRootDir, project, "config.json")
	if !fileExists(configPath) {
		writeErr(w, http.StatusBadRequest, "项目不存在: "+project)
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 审计 2026-08-28:读-改-写同加 per-config 锁
	cfgLock := manjuConfigLock(configPath)
	cfgLock.Lock()
	defer cfgLock.Unlock()
	P, _ := cfg["paths"].(map[string]any)
	removed := ""
	if P != nil {
		if sp := strings.TrimSpace(str(P["script"])); sp != "" {
			// 审计 2026-08-28:sp 来自 config.json 可被手工编辑,删除前必须确认目标在项目
			// workdir 内(与 manju_delete.go 同款护栏),防 RemoveAll 递归删到项目外
			projDir := filepath.Join(paths.ManjuRootDir, project)
			scriptDir := filepath.Clean(filepath.Dir(sp))
			if scriptDir != projDir && !strings.HasPrefix(scriptDir, projDir+string(filepath.Separator)) {
				writeErr(w, http.StatusBadRequest, "脚本目录不在项目内,拒绝清除")
				return
			}
			_ = os.RemoveAll(scriptDir) // 删 script/ 目录(含各集脚本)
			removed = sp
		}
		delete(P, "script")
	}
	if err := writeManjuConfig(configPath, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed, "script_mode": false})
}

// manjuScriptImportFromNovel 从小说项目的「素材/分镜脚本/」目录导入某集分镜脚本
// (爽文技能阶段6 产物:第NNN章_章节名_分镜脚本.md,分镜表 8 字段 + 每镜 H3 六段式,
// 符合 H3 官方直通渲染格式)并启用脚本直出模式:复制到 <workdir>/script/<ep>.md
// + config paths.script 指向它(指纹变化自动使旧方案过期重生成)。
// 集号→章号映射:EP01→第001章。
func manjuScriptImportFromNovel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Episode string `json:"episode"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	configPath, scriptPath, _, err := manjuScriptTarget(body.Project, body.Episode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
		cfg["paths"] = P
	}
	novel := strings.TrimSpace(str(P["novel"]))
	if novel == "" {
		writeErr(w, http.StatusBadRequest, "项目未配置小说目录(paths.novel),无法定位分镜脚本")
		return
	}
	// 优先用 paths.novel_dir(小说根目录);旧配置只有全本文件路径时上溯一级
	novelDir := strings.TrimSpace(str(P["novel_dir"]))
	if novelDir == "" {
		novelDir = filepath.Dir(novel)
		if strings.EqualFold(filepath.Base(novelDir), "全本") {
			novelDir = filepath.Dir(novelDir)
		}
	}
	ep := normalizeEpisode(orDefault(body.Episode, "EP01"))
	var chap string
	if n, aerr := strconv.Atoi(strings.TrimPrefix(ep, "EP")); aerr == nil && n > 0 {
		chap = fmt.Sprintf("%03d", n)
	} else {
		writeErr(w, http.StatusBadRequest, "集号无效: "+ep+"(应为 EP01/EP02…或数字)")
		return
	}
	dir := filepath.Join(novelDir, "素材", "分镜脚本")
	matches, _ := filepath.Glob(filepath.Join(dir, "第"+chap+"章*.md"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "EP"+strings.TrimPrefix(ep, "EP")+".md"))
	}
	if len(matches) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("小说分镜脚本目录(%s)未找到「第%s章*_分镜脚本.json/.md」;请确认已按爽文技能分镜派发模板生成", dir, chap))
		return
	}
	src := pickStoryboardMatch(matches) // 同章号多文件择优(排除备份,取最新)
	text, rerr := os.ReadFile(src)
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, "读取分镜脚本失败: "+rerr.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(scriptPath, text, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	P["script"] = scriptPath
	if err := writeManjuConfig(configPath, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": scriptPath, "source": src, "chapter": chap, "script_mode": true})
}

// manjuScriptEnable 落盘脚本并启用脚本直出模式:写 <workdir>/script/<ep>.md
// + config paths.script 指向它(脚本文件指纹变化自动使旧方案过期重生成)。
func manjuScriptEnable(configPath, scriptPath string, cfg map[string]any, src string, text []byte) (map[string]any, error) {
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(scriptPath, text, 0o644); err != nil {
		return nil, err
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
		cfg["paths"] = P
	}
	P["script"] = scriptPath
	if err := writeManjuConfig(configPath, cfg); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "path": scriptPath, "source": src, "script_mode": true}, nil
}

// sbItem 分镜脚本扫描项
type sbItem struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Preview  string `json:"preview,omitempty"`
	Chapter  string `json:"chapter,omitempty"`
	Episode  string `json:"episode,omitempty"`
	Selected bool   `json:"selected"`
}

// scanStoryboardDir 扫描目录(含 素材/分镜脚本 子目录)检测分镜脚本:
// 文件名含"分镜脚本"/"分镜" 或内容含 H3 分镜特征([Shot / 分镜表)即视为分镜脚本;
// 按集号(EP01→第001章)标记 Selected,排序按章号数字升序(第1章<第2章<第10章)。
// preview/内容统一走 readTextFileUTF8(容忍 BOM/GBK,防乱码)。
func scanStoryboardDir(dir, episode string) ([]sbItem, string) {
	ep := normalizeEpisode(orDefault(episode, "EP01"))
	chap := ""
	if n, aerr := strconv.Atoi(strings.TrimPrefix(ep, "EP")); aerr == nil && n > 0 {
		chap = fmt.Sprintf("%03d", n)
	}
	var items []sbItem
	seen := map[string]bool{}
	scanDirs := []string{dir}
	if sd := filepath.Join(dir, "素材", "分镜脚本"); dirExists(sd) {
		scanDirs = append(scanDirs, sd)
	}
	// 2026-08-29 递归一层子目录(书父目录检测):用户选小说库根(如 D:\Ai\NiliX\novel,内含
	// 多本书)时,各书分镜脚本在 <书>/素材/分镜脚本 下——只扫两层会报「未检测到」。
	// 子目录各自再扫「本层 + 素材/分镜脚本」(书的结构),不无限递归(防全盘扫)。
	entries0, rerr0 := os.ReadDir(dir)
	if rerr0 == nil {
		for _, e := range entries0 {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if strings.EqualFold(e.Name(), "素材") {
				continue // 已单独处理
			}
			scanDirs = append(scanDirs, sub)
			if sd := filepath.Join(sub, "素材", "分镜脚本"); dirExists(sd) {
				scanDirs = append(scanDirs, sd)
			}
		}
	}
	for _, sd := range scanDirs {
		entries, rerr := os.ReadDir(sd)
		if rerr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			full := filepath.Join(sd, name)
			if seen[full] {
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".md" && ext != ".txt" && ext != ".markdown" {
				continue
			}
			if !strings.Contains(strings.ToLower(name), "分镜") && !storyboardContentHint(full) {
				continue
			}
			seen[full] = true
			info, ierr := e.Info()
			sz := int64(0)
			if ierr == nil {
				sz = info.Size()
			}
			item := sbItem{Path: full, Name: name, Size: sz}
			if pb, perr := readTextFileUTF8(full); perr == nil {
				pv := strings.TrimSpace(string(pb))
				if len(pv) > 220 {
					pv = pv[:220] + "…"
				}
				item.Preview = pv
			}
			if m := reChapter.FindStringSubmatch(name); m != nil && len(m) > 1 {
				item.Chapter = m[1]
				if cn, aerr := strconv.Atoi(item.Chapter); aerr == nil {
					item.Episode = fmt.Sprintf("EP%02d", cn)
				}
			}
			item.Selected = chap != "" && item.Chapter == chap
			items = append(items, item)
		}
	}
	// 排序:本集(选中)优先,其余按章号数字升序(兼容未补零),无章号按名称
	sort.Slice(items, func(i, j int) bool {
		if items[i].Selected != items[j].Selected {
			return items[i].Selected
		}
		ni := chapterNumber(items[i].Chapter)
		nj := chapterNumber(items[j].Chapter)
		if ni != nj {
			return ni < nj
		}
		return items[i].Name < items[j].Name
	})
	return items, ep
}

// manjuScriptScanDir 手动选择目录 → 检测分镜脚本
// (hasStoryboard=false 时前端引导把目录当小说源走 LLM 直出)
func manjuScriptScanDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir     string `json:"dir"`
		Episode string `json:"episode"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	dir := strings.TrimSpace(body.Dir)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "缺少目录")
		return
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		writeErr(w, http.StatusBadRequest, "目录不存在: "+dir)
		return
	}
	items, ep := scanStoryboardDir(dir, body.Episode)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"dir":           dir,
		"episode":       ep,
		"hasStoryboard": len(items) > 0,
		"storyboards":   items,
	})
}

// manjuScriptImportAllDir 批量导入目录检测到的全部分镜脚本(2026-08-24 用户要求):
// 每个分镜脚本按章号 → <workdir>/script/EPxx.md(第001章→EP01),一次全部导入;
// config paths.script 指向当前集(episode)脚本;源目录 素材/(人物生成提示词.md 等)
// 复制到 workdir/素材/ 供脚本模式定妆照参考。内容统一编码转换(防 GBK 乱码)。
func manjuScriptImportAllDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Episode string `json:"episode"`
		Dir     string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := strings.TrimSpace(body.Dir)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "缺少目录")
		return
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		writeErr(w, http.StatusBadRequest, "目录不存在: "+dir)
		return
	}
	configPath, _, workdir, err := manjuScriptTarget(body.Project, body.Episode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, ep := scanStoryboardDir(dir, body.Episode)
	if len(items) == 0 {
		writeErr(w, http.StatusNotFound, "目录未检测到分镜脚本")
		return
	}
	// 复制源目录 素材/ → workdir/素材/(人物提示词文档,定妆照参考;内容有变则覆盖更新)
	assetsUpdated := copyNovelAssetsToWorkdir(dir, workdir)
	// 逐个导入:章号 → EPxx.md
	results := make([]map[string]any, 0, len(items))
	for _, it := range items {
		epFile := it.Episode
		if epFile == "" { // 无章号的文件按当前集兜底
			epFile = ep
		}
		text, rerr := readTextFileUTF8(it.Path)
		if rerr != nil {
			continue
		}
		scriptPath := filepath.Join(workdir, "script", epFile+".md")
		if _, werr := manjuScriptEnable(configPath, scriptPath, cfg, it.Path, text); werr != nil {
			continue
		}
		results = append(results, map[string]any{
			"path": scriptPath, "source": it.Path, "episode": epFile, "chapter": it.Chapter,
		})
	}
	if len(results) == 0 {
		writeErr(w, http.StatusInternalServerError, "全部脚本导入失败")
		return
	}
	// paths.script 指向当前集脚本(渲染按集号选文件,见 manjuScriptTarget 的 script 目录逻辑)
	curFile := filepath.Join(workdir, "script", ep+".md")
	if !fileExists(curFile) {
		curFile = str(results[0]["path"])
	}
	P, _ := cfg["paths"].(map[string]any)
	if P == nil {
		P = map[string]any{}
		cfg["paths"] = P
	}
	P["script"] = curFile
	if err := writeManjuConfig(configPath, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "imported": len(results), "total": len(items), "script_mode": true,
		"path": curFile, "dir": dir, "assets_updated": assetsUpdated, "results": results,
	})
}

// copyNovelAssetsToWorkdir 源目录 素材/(人物生成提示词.md/场景提示词.md/渲染提示词总集.md)
// 复制到 workdir/素材/,供脚本直出模式定妆照/场景参考。
// 2026-08-26 修复:旧版「已存在不覆盖」→ 源素材更新后重新导入分镜,workdir 仍用旧角色/场景卡
// (脚本直出的角色卡来源 workdir 优先),定妆照/场景图全按旧设定生成。改为内容比对后覆盖,
// 返回更新文件数。
func copyNovelAssetsToWorkdir(srcDir, workdir string) int {
	src := filepath.Join(srcDir, "素材")
	if !dirExists(src) {
		return 0
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	updated := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dstF := filepath.Join(workdir, "素材", e.Name())
		b, rerr := readTextFileUTF8(filepath.Join(src, e.Name()))
		if rerr != nil || len(b) == 0 {
			continue
		}
		if old, oerr := readTextFileUTF8(dstF); oerr == nil && bytes.Equal(old, b) {
			continue // 内容一致,无需覆盖
		}
		if mkerr := os.MkdirAll(filepath.Dir(dstF), 0o755); mkerr != nil {
			continue
		}
		if os.WriteFile(dstF, b, 0o644) == nil {
			updated++
		}
	}
	return updated
}

// readTextFileUTF8 读文本文件并统一为 UTF-8(容忍 UTF-8 BOM;GBK/GB18030 自动转码,防乱码)
func readTextFileUTF8(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return toUTF8(b), nil
}

// toUTF8 字节 → UTF-8:去 UTF-8 BOM;合法 UTF-8 原样;否则按 GBK/GB18030 解码
// (Windows 中文文件/旧编辑器产物常见 GBK,直接 UTF-8 读会乱码——2026-08-24 用户反馈)
func toUTF8(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(b) {
		return b
	}
	if dec := simplifiedchinese.GBK.NewDecoder(); dec != nil {
		if out, derr := dec.Bytes(b); derr == nil {
			return out
		}
	}
	if dec := simplifiedchinese.GB18030.NewDecoder(); dec != nil {
		if out, derr := dec.Bytes(b); derr == nil {
			return out
		}
	}
	return b // 无法识别:原样返回
}

// chapterNumber 章号字符串转数字(第001章→1;解析失败返回大数排最后)
func chapterNumber(s string) int {
	if s == "" {
		return int(^uint(0) >> 1) // 最大 int
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return int(^uint(0) >> 1)
}

// storyboardContentHint 读文件头部检测 H3 分镜格式特征([Shot N] / 分镜表)
func storyboardContentHint(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	s := string(buf[:n])
	return strings.Contains(s, "[Shot ") || strings.Contains(s, "分镜表") || strings.Contains(s, "## 一、分镜表")
}

// manjuScriptImportDir 导入手动选择的目录里检测到的分镜脚本文件 → 启用脚本直出
// (复制到 <workdir>/script/<ep>.md + config paths.script,指纹变化自动使旧方案过期)
func manjuScriptImportDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Episode string `json:"episode"`
		File    string `json:"file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	file := strings.TrimSpace(body.File)
	if file == "" {
		writeErr(w, http.StatusBadRequest, "缺少分镜脚本文件路径")
		return
	}
	text, err := readTextFileUTF8(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取分镜脚本失败: "+err.Error())
		return
	}
	configPath, scriptPath, _, err := manjuScriptTarget(body.Project, body.Episode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := manjuScriptEnable(configPath, scriptPath, cfg, file, text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// manjuScriptStatus 脚本直出模式状态(active/path/preview,供前端显示与切换)
func manjuScriptStatus(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	configPath := filepath.Join(paths.ManjuRootDir, project, "config.json")
	if !fileExists(configPath) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	sp := strings.TrimSpace(str(P["script"]))
	active := sp != "" && fileExists(sp)
	res := map[string]any{"active": active, "path": sp, "script_mode": active}
	if active {
		if b, rerr := os.ReadFile(sp); rerr == nil {
			runes := []rune(string(b))
			res["bytes"] = len(runes)
			if len(runes) > 500 {
				res["preview"] = string(runes[:500]) + "…"
			} else {
				res["preview"] = string(runes)
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// manjuModelDirs 各模型字段对应的 ComfyUI 模型子目录（依次查找，取第一个非空）。
var manjuModelDirs = map[string][]string{
	"unet_fl2va":     {"diffusion_models", "unet"},
	"unet_ref2va":    {"diffusion_models", "unet"},
	"z_image_unet":   {"diffusion_models", "unet"},
	"clip":           {"text_encoders", "clip"},
	"z_image_clip":   {"text_encoders", "clip"},
	"vae_video":      {"vae"},
	"vae_audio":      {"vae"},
	"z_image_vae":    {"vae"},
	// 2026-09-04:PDD Acc 单文件在 pdd_acc 目录,与 loras 合并列出(下拉可选)
	"turbo_lora":     {"loras", "pdd_acc"},
	"turbo_lora_r2v": {"loras", "pdd_acc"},
	"char_male":      {"checkpoints"},
	"char_female":    {"checkpoints"},
	"animagine":      {"checkpoints"},
}

// manjuModels 返回各模型字段的可选模型列表(从 ComfyUI 模型目录读取),供前端下拉选择。
func manjuModels(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for field, dirs := range manjuModelDirs {
		names := []string{}
		// turbo_* 字段=合并模式(loras+pdd_acc 都列出;PDD 单文件在 pdd_acc 目录);
		// 其余=互斥备选目录,取第一个非空(unet/diffusion_models 类)
		merge := strings.HasPrefix(field, "turbo_")
		for _, d := range dirs {
			dir := filepath.Join(paths.ComfySharedDir, "models", d)
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
			if len(names) > 0 && !merge {
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
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	out := manjuEnvCheck(cp)
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
	if configPath != "" {
		cp, gerr := manjuGuardConfig(configPath)
		if gerr != nil {
			writeErr(w, http.StatusForbidden, gerr.Error())
			return
		}
		configPath = cp
	}
	chapters := str(body["chapters"])
	// 章节 0(默认)= 解析小说总章数作为实际值(全书范围,如 1-56)
	if chapters == "" || chapters == "0" {
		if n := manjuChapterTotal(configPath); n > 0 {
			chapters = fmt.Sprintf("1-%d", n)
		} else {
			chapters = "1-3" // 兜底:小说不可读时用默认范围
		}
	}
	episode := str(body["episode"])
	phase := str(body["phase"])
	// 集数语义:纯数字 N>0 → 第 N 集(第 N 章,EPxx);0 → 按章节数自动分集(每章一集);
	// 非数字(旧 EP01 输入)原样兼容
	// 审计 P6:episode 缺省(空串)≠ "0"——空串沿用 config.render.episode(EP01),
	// 不能触发"每章一集"全本分集(56 章书被切成 56 集逐集渲染,每集一次完整 LLM+GPU)
	epStr := strings.TrimSpace(episode)
	epNum, _ := strconv.Atoi(epStr)
	autoByChapter := false
	switch {
	case epNum > 0:
		episode = fmt.Sprintf("EP%02d", epNum)
		// 集数决定集:第 N 集 = 第 N 章(1 章 1 集),无条件覆盖章节范围——
		// 章节框(如 1-56)是全书章节编号范围,不是渲染范围;否则显式填 1-56 会被
		// 当成一次渲染 56 章 9 万字(超 20000 上限误报)。集数 0 才走每章一集全渲染。
		chapters = fmt.Sprintf("%d-%d", epNum, epNum)
	case epStr == "0":
		autoByChapter = true // 集数 0:按小说章节数计算
	default:
		episode = orDefault(episode, "EP01")
	}
	only := str(body["only"])
	novel := str(body["novel"])
	fresh, _ := body["fresh"].(bool)     // 重跑:先清空项目旧产物(方案/镜头/成片/缓存)
	agentMode, _ := body["agent"].(bool) // 智能体调度:剧本复核 + 审片官判分 + 自动返工 + 例外升级

	if err := startManjuRun(configPath, chapters, episode, phase, only, novel, autoByChapter, fresh, agentMode); err != nil {
		if strings.Contains(err.Error(), "运行中") {
			http.Error(w, `{"error":"已有任务运行中，先停止"}`, http.StatusConflict)
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stage": orDefault(phase, "all")})
}

// startManjuRun 启动渲染管线(manjuRun HTTP 与崩溃自动恢复共用):
// 校验、初始化运行状态、启动后台 goroutine。返回 error(参数非法/已有任务/初始化失败)。
func startManjuRun(configPath, chapters, episode, phase, only, novel string, autoByChapter, fresh, agentMode bool) error {
	if configPath == "" {
		return fmt.Errorf("missing config")
	}
	if phase != "" && !manjuPhases[phase] {
		return fmt.Errorf("未知阶段: %s(可用 all/plan/assets/encode/render/qc/assemble)", phase)
	}

	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		return fmt.Errorf("已有任务运行中，先停止")
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

	// 落盘到对应项目目录:保留上次日志(追加分隔线,让用户看到"上次跑到哪、本次从哪续"——
	// 此前每次续跑日志被清空,界面永远从 [1/19] 开始,用户误判卡死反复退出) + 写入"运行中"状态
	_ = os.MkdirAll(filepath.Dir(manjuRunStatePath(projName)), 0755)
	manjuAppendRunLog(projName)
	writeManjuDiskState(projName, &manjuDiskState{Running: true, Stage: orDefault(phase, "all"), StartedAt: time.Now().Unix(), Episode: episode, PID: os.Getpid()})

	// 新管线:章节/集号/镜头从 config.render 读取(render.chapters/episode/shots),先写入再启动
	// 立项.render 变更同步(2026-08-26):立项.json 更新后自动重新合并(幂等,显式配置优先)
	if msg := syncLixiRenderPlan(configPath); msg != "" {
		manjuState.mu.Lock()
		manjuState.log += msg + "\n"
		manjuState.mu.Unlock()
	}
	if err := writeManjuRunParams(configPath, chapters, episode, only); err != nil {
		manjuState.mu.Lock()
		manjuState.running = false
		rc := -1
		manjuState.rc = &rc
		manjuState.done = true
		manjuState.mu.Unlock()
		writeManjuDiskState(projName, &manjuDiskState{Running: false, Stage: manjuState.stage, Done: true, RC: &rc, StartedAt: manjuState.started.Unix(), Episode: episode})
		return fmt.Errorf("写入渲染参数失败: %w", err)
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
		return err
	}
	runFile, _ := os.OpenFile(manjuRunLogPath(projName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	lg := newManjuLogger(manjuState, runFile, ctx.project, ctx.episode)
	phaseName := orDefault(phase, "all")
	// 2026-09-01 用户规则:AI 一条龙=全走 LLM+Agent,跳过脚本检测强制 LLM 直出
	ctx.agentMode = agentMode

	// 全本自动分段分集:把整本书切成多集,逐集跑管线,免去用户手动换集号
	// 集数 0 = 每章一集(第 N 章 = 第 N 集);否则沿用 按卷/字数打包 的自动分集
	autoEps, aerr := manjuAutoEpisodes(ctx, chapters)
	if autoByChapter {
		autoEps = manjuChapterEpisodes(ctx, chapters) // 章节 0 已解析为 1-总章数;具体范围(如 1-10)则范围内每章一集
		if len(autoEps) == 0 {
			autoEps, aerr = manjuAutoEpisodes(ctx, chapters) // 保底:无章节结构回退打包
		}
	}
	if aerr != nil {
		manjuState.mu.Lock()
		manjuState.log += "\n[" + time.Now().Format("15:04:05") + "] ⚠️ 自动分集失败: " + aerr.Error() + "，按单集运行"
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
		// 管线崩溃保护:渲染/质检等 goroutine panic 会直接崩掉整个主进程(无日志、无状态落盘,
		// 表现为"应用莫名退出")。这里兜底:记录崩溃堆栈到 logs/crash.log、状态落盘为失败、不崩进程。
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 64<<10)
				n := runtime.Stack(buf, false)
				lg.logf(fmt.Sprintf("💥 渲染管线崩溃(panic): %v\n%s", r, buf[:n]))
				manjuWriteCrash("pipeline", fmt.Sprintf("%v", r), buf[:n])
				if runFile != nil {
					_ = runFile.Close()
				}
				manjuFinish(1) // 落盘失败态,用户可续跑
			}
		}()
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
				epCtx.agentMode = agentMode // AI 一条龙:跳过脚本检测,全 LLM+Agent
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
	return nil
}

// AutoRecoverRendering 启动后自动恢复"渲染中异常退出"的任务(崩溃恢复续跑):
// 磁盘 run_state 显示 running 但进程已死(崩溃残留——异常退出不走 onExit,磁盘仍为运行中),
// 且上次阶段在渲染链路(编码/渲染/质检),且环境就绪(ComfyUI 可拉起、项目可读)
// → 自动续跑:幂等跳过已完成阶段、渲染检查点自动收回上次未收产物,绝不重复烧 GPU。
// 用户主动停止(磁盘 Stopped=true,run_state 会落盘 stopped)与已完成任务不自动恢复。
func AutoRecoverRendering() {
	// 恢复扫描自身兜底:任何 panic 不静默失效,记录后继续
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 64<<10)
			n := runtime.Stack(buf, false)
			manjuWriteCrash("autorecover", fmt.Sprintf("%v", r), buf[:n])
		}
	}()
	entries, err := os.ReadDir(paths.ManjuRootDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		ds := loadManjuDiskState(name)
		if ds == nil || !ds.Running || isPidAlive(ds.PID) {
			continue // 未在运行或进程仍存活
		}
		// 崩溃残留:running 但进程已死。只在渲染链路阶段自动恢复(plan/assets 无 GPU 消耗,用户按需重跑)
		stage := ds.Stage
		if stage == "" {
			stage = ds.CurrentStage
		}
		// 渲染链路阶段才自动恢复。注意:前端一条龙/续跑都发 phase="all",磁盘 Stage 落盘恒为 "all",
		// 运行期不更新磁盘阶段——白名单必须含 "all",否则一条龙崩溃后永不自动恢复(实测发现的坑)。
		if stage != "all" && stage != "encode" && stage != "render" && stage != "qc" {
			continue
		}
		cfgPath := filepath.Join(paths.ManjuRootDir, name, "config.json")
		if !fileExists(cfgPath) {
			continue
		}
		// 环境判断:ComfyUI 不在线先拉起,拉不起则不自动跑(避免空转)
		if !ComfyOnline() {
			if err := startComfy(); err != nil {
				log.Printf("♻️ 自动恢复 %s 跳过: ComfyUI 无法启动(%v)", name, err)
				continue
			}
		}
		episode := orDefault(ds.Episode, "EP01")
		// 定点参数回读(2026-08-29):writeManjuRunParams 把 only 落盘到 config.render.shots,
		// 恢复时必须带回,否则定点重渲(only=4)崩溃后恢复成全量重渲——实测浪费整轮 GPU。
		resumeOnly := ""
		if cfg0, cerr := readManjuConfig(cfgPath); cerr == nil {
			if R, ok := cfg0["render"].(map[string]any); ok {
				resumeOnly = str(R["shots"])
			}
		}
		// 审计 P5:自动分集(episode=0)崩溃恢复——磁盘 Episode 落的是 "0",
		// 若当作单集续跑会生成 analysis/0_*、clips/0/ 幽灵集并一次塞入全书;
		// 恢复时识别 "0" → 按原 autoByChapter 语义重建分集
		autoByChapter := ds.Episode == "0"
		log.Printf("♻️ 检测到 %s 渲染中断于「%s」阶段,自动续跑…(定点镜头:%s)", name, stage, orDefault(resumeOnly, "全部"))
		if err := startManjuRun(cfgPath, "", episode, "all", resumeOnly, "", autoByChapter, false, false); err != nil {
			log.Printf("♻️ 自动恢复 %s 失败: %v", name, err)
		}
		// 注意:startManjuRun 遇"已有任务运行中"返回错误——这是已恢复第一个任务后的正常状态,
		// 不能 break,其余项目留待下次启动(或用户手动续跑)
	}
}

func manjuKill(w http.ResponseWriter, r *http.Request) {
	manjuState.mu.Lock()
	if !manjuState.running {
		manjuState.mu.Unlock()
		http.Error(w, `{"error":"无运行任务"}`, http.StatusConflict)
		return
	}
	manjuState.stopped = true
	proj := manjuState.project
	manjuState.mu.Unlock()
	// 中断 ComfyUI 正在执行的任务:优先用任务项目的 comfy_url(自定义端口/地址也生效,
	// 不再硬编码 8190——审计 H1/S9)
	base := "http://127.0.0.1:8190"
	if proj != "" {
		if cfg, err := readManjuConfig(filepath.Join(paths.ManjuRootDir, proj, "config.json")); err == nil {
			if R, ok := cfg["render"].(map[string]any); ok {
				if u := str(R["comfy_url"]); u != "" {
					base = strings.TrimRight(u, "/")
				}
			}
		}
	}
	client := &http.Client{Timeout: 8 * time.Second}
	// 中断当前采样 + 清空排队任务:只 interrupt 会停当前镜,已提交排队的后续镜头仍会继续跑
	// (普通一条龙逐镜提交、agent 流水线多镜预提交——停止后队列里还剩一堆,GPU 继续烧)
	// ComfyUI:中断 = POST /interrupt;清空 pending 队列 = POST /queue {"clear":true}(实测 200)
	if req, err := http.NewRequest("POST", base+"/interrupt", nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	if req, err := http.NewRequest("POST", base+"/queue", bytes.NewBufferString(`{"clear": true}`)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	// 短暂等待管线感知 stopped(最大 3s:comfy.wait/runMedia 停止感知秒级生效;不阻塞 HTTP 太久,
	// 前端 poll 会持续轮询状态——剩余未停的由停止感知逐点退出)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		manjuState.mu.Lock()
		done := manjuState.done
		running := manjuState.running
		manjuState.mu.Unlock()
		if done || !running {
			break
		}
		time.Sleep(150 * time.Millisecond)
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
			"running": false, "stage": "", "currentStage": "", "episode": "", "stageIdx": -1,
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
	// 2026-08-23 用户要求:完整运行日志(产物收起也完整展开到底)——返回 run.log 全文
	// (限 500KB 防前端卡;前端按阶段分组折叠展示)
	logFull := ""
	if project != "" {
		if b, rerr := os.ReadFile(manjuRunLogPath(project)); rerr == nil {
			logFull = string(b)
			if len(logFull) > 500*1024 {
				logFull = logFull[len(logFull)-500*1024:]
			}
		}
	}
	info := parseManjuProgress(logStr, running)
	return map[string]any{
		"running":      running,
		// 审计 M7:stage 恒为阶段名(与内存态一致),集号单独用 episode 字段——
		// 此前磁盘态 stage 存集号(EP01),前端 stageCN 显示错乱/「中断于 EP01」
		"stage":        info.CurrentStage,
		"episode":      ds.Episode,
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
		"logFull":      logFull,
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
	liveEpisode := manjuState.episode
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
		// 2026-08-23:完整日志(run.log 全文,限 500KB)
		logFull := ""
		if cfgName != "" {
			if b, rerr := os.ReadFile(manjuRunLogPath(cfgName)); rerr == nil {
				logFull = string(b)
				if len(logFull) > 500*1024 {
					logFull = logFull[len(logFull)-500*1024:]
				}
			}
		}
		return map[string]any{
			"running": running, "stage": stage, "currentStage": info.CurrentStage,
			"episode": liveEpisode, "stageIdx": info.StageIdx, "stageTotal": len(manjuStageOrder),
			"shotCur": info.ShotCur, "shotTotal": info.ShotTotal, "progress": info.Progress,
			"done": done, "rc": rc, "stopped": stopped, "elapsedSec": elapsed, "logTail": logStr, "logFull": logFull,
		}
	}
	// 其余情况:该项目目录的落盘状态(无 → 空闲)
	if cfgName != "" {
		return manjuDiskStatus(cfgName, loadManjuDiskState(cfgName))
	}
	return manjuDiskStatus("", nil)
}

// manjuFlowCheck 检测项目是否已有渲染流程产物(方案/镜头/成片)。
// 前端点「一条龙」时据此询问「从头重渲 or 续跑」:有产物 → 弹窗询问;
// 无 → 直接启动。资产(定妆照/场景图)不算流程,避免误报。
func manjuFlowCheck(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	if configPath == "" {
		writeErr(w, http.StatusBadRequest, "missing config")
		return
	}
	cp, err := manjuGuardConfig(configPath)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	cfg, err := readManjuConfig(cp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res := map[string]any{"hasFlow": false, "plans": 0, "clips": 0, "finals": 0, "detail": ""}
	P, _ := cfg["paths"].(map[string]any)
	workdir := str(P["workdir"])
	if workdir == "" {
		writeJSON(w, http.StatusOK, res)
		return
	}
	// 方案文件(analysis/*.json)
	plans := 0
	if entries, err := os.ReadDir(filepath.Join(workdir, "analysis")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				plans++
			}
		}
	}
	// 镜头(clips/<EP>/*.mp4 或 clips/*.mp4)
	clips := 0
	if entries, err := os.ReadDir(filepath.Join(workdir, "clips")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				if fs, err := os.ReadDir(filepath.Join(workdir, "clips", e.Name())); err == nil {
					for _, f := range fs {
						if !f.IsDir() && strings.HasSuffix(f.Name(), ".mp4") {
							clips++
						}
					}
				}
			} else if strings.HasSuffix(e.Name(), ".mp4") {
				clips++
			}
		}
	}
	// 成片(workdir/*_成片.mp4)
	finals := 0
	if entries, err := os.ReadDir(workdir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), "_成片.mp4") {
				finals++
			}
		}
	}
	parts := []string{}
	if plans > 0 {
		parts = append(parts, fmt.Sprintf("方案 %d 个", plans))
	}
	if clips > 0 {
		parts = append(parts, fmt.Sprintf("镜头 %d 个", clips))
	}
	if finals > 0 {
		parts = append(parts, fmt.Sprintf("成片 %d 个", finals))
	}
	res["plans"], res["clips"], res["finals"] = plans, clips, finals
	res["hasFlow"] = len(parts) > 0
	res["detail"] = strings.Join(parts, "、")
	writeJSON(w, http.StatusOK, res)
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

// reEpisodeSafe 集号参数白名单(审计 4.1):EPxx 或纯数字——拒绝 ..、路径分隔符等穿越
var reEpisodeSafe = regexp.MustCompile(`^[A-Za-z]{0,4}\d{1,3}$`)

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
			// 跳过非集目录:草稿预审 _draft / 云端 2K 定稿 2k(产物工作区,不是集)
			if e.IsDir() && !strings.HasPrefix(e.Name(), "_") && e.Name() != "2k" {
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

// manjuEpisodeOutputs 单个集的产物:镜头 + 云端2K定稿 + 成片 + 方案文件
func manjuEpisodeOutputs(P map[string]any, episode string) map[string]any {
	vidExts := map[string]bool{".mp4": true, ".mov": true, ".webm": true}
	clips := listManjuMedia(filepath.Join(str(P["clips"]), episode), vidExts)
	upscaled := listManjuMedia(filepath.Join(str(P["clips"]), episode, "2k"), vidExts)
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
	return map[string]any{"episode": episode, "clips": clips, "upscaled": upscaled, "final": final, "artifacts": artifacts}
}

// manjuOutputs 产物列表:人物/场景为全项目共享,视频/方案/成片按集区分(episode 参数缺省=全部集)
func manjuOutputs(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := r.URL.Query().Get("episode") // 空 = 返回全部集
	// 审计 4.1:episode 拼进文件路径,必须消毒——`..`/分隔符可跨界列目录
	if episode != "" && episode != "all" {
		if !reEpisodeSafe.MatchString(episode) {
			writeErr(w, http.StatusBadRequest, "非法 episode 参数")
			return
		}
	}
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
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
	// 产物时效角标:提示词/定妆照/画幅已变但旧产物仍在的镜头标 stale(前端「已过期」徽标,
	// 下次渲染自动删旧重渲)。manifest 无记录的产物同样视为 stale(不可信,2026-08-26 语义)。
	if mctx, err := newManjuCtx(configPath, "", "", "", ""); err == nil {
		for _, epAny := range episodes {
			ep, _ := epAny.(map[string]any)
			if ep == nil {
				continue
			}
			mctx.episode = str(ep["episode"])
			plan, shots, err := mctx.loadPlan()
			if err != nil {
				continue
			}
			_ = plan
			clips, _ := ep["clips"].([]map[string]any)
			for _, c := range clips {
				idStr := strings.TrimSuffix(str(c["name"]), filepath.Ext(str(c["name"])))
				for _, s := range shots {
					if strconv.Itoa(s.ID) == idStr {
						if mctx.shotManifestStatus(s) == "stale" {
							c["stale"] = true
						}
						break
					}
				}
			}
		}
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

// manjuEpNum 提取集号中的数字(EP01→1, EP01-EP56→1),用于方案文件集号排序
func manjuEpNum(ep string) int {
	m := regexp.MustCompile(`\d+`).FindString(ep)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}

// manjuScanMin 扫描 analysis 目录,返回后缀匹配且集号最小的现有方案文件路径(如 EP01_*);
// 同集多后缀时按 suffs 传入顺序优先(如 direct_plan 优先于 characters)。
func manjuScanMin(analysisDir string, suffs ...string) string {
	best, bestEp, bestIdx := "", "", -1
	entries, _ := os.ReadDir(analysisDir)
	for _, e := range entries {
		n := e.Name()
		cand, idx := "", -1
		for i, s := range suffs {
			if strings.HasSuffix(n, s) {
				cand, idx = strings.TrimSuffix(n, s), i
				break
			}
		}
		if cand == "" {
			continue
		}
		if best == "" ||
			manjuEpNum(cand) < manjuEpNum(bestEp) ||
			(manjuEpNum(cand) == manjuEpNum(bestEp) && idx < bestIdx) {
			best, bestEp, bestIdx = filepath.Join(analysisDir, n), cand, idx
		}
	}
	return best
}

// manjuFindPlanFile 找角色方案文件(characters 优先回退 direct_plan):
// 指定集号(非 0/空)按 <ep>_characters.json → <ep>_direct_plan.json 找;
// 集号 0/空/该集未生成时,扫描 analysis 取集号最小的现有方案(如 EP01)。
func manjuFindPlanFile(analysisDir, episode string) string {
	if episode != "" && episode != "0" {
		for _, name := range []string{episode + "_characters.json", episode + "_direct_plan.json"} {
			if p := filepath.Join(analysisDir, name); fileExists(p) {
				return p
			}
		}
	}
	return manjuScanMin(analysisDir, "_characters.json", "_direct_plan.json")
}

// manjuFindPlanDir 找完整分镜方案(direct_plan 优先,需 shots 时用):
// 指定集号(非 0/空)按 <ep>_direct_plan.json → <ep>_characters.json 找;
// 集号 0/空/该集未生成时,扫描 analysis 取集号最小的现有方案。
func manjuFindPlanDir(analysisDir, episode string) string {
	if episode != "" && episode != "0" {
		for _, name := range []string{episode + "_direct_plan.json", episode + "_characters.json"} {
			if p := filepath.Join(analysisDir, name); fileExists(p) {
				return p
			}
		}
	}
	return manjuScanMin(analysisDir, "_direct_plan.json", "_characters.json")
}

// manjuPlan 读取方案 JSON,返回角色列表(供角色抽卡)
// manjuShots 渲染镜头管理列表(2026-08-29 用户需求:「渲染」按钮弹窗的镜头数据源):
// 按集分项返回每镜(编号/场景/景别/运镜/时长/有无台词/是否已渲染/是否过期),
// 供前端弹窗做单镜「重新渲染」/「开始渲染」/「全部重做」交互。
// episode 空 = 全部集;EP01 等 = 单集。仅返回已生成方案(direct_plan)的集。
func manjuShots(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := r.URL.Query().Get("episode")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"episodes": []any{}, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	eps := listManjuEpisodes(P)
	if episode != "" && episode != "all" {
		eps = []string{episode}
	}
	mctx, _ := newManjuCtx(configPath, "", "", "", "")
	episodes := make([]any, 0, len(eps))
	for _, e := range eps {
		planPath := manjuFindPlanDir(str(P["analysis"]), e)
		if planPath == "" {
			continue
		}
		dataBytes, rerr := os.ReadFile(planPath)
		if rerr != nil {
			continue
		}
		var plan map[string]any
		if json.Unmarshal(dataBytes, &plan) != nil {
			continue
		}
		shotsArr, _ := plan["shots"].([]any)
		shots := make([]any, 0, len(shotsArr))
		renderedN, staleN := 0, 0
		epEp := strings.TrimSuffix(filepath.Base(planPath), "_direct_plan.json")
		if mctx != nil {
			mctx.episode = epEp
		}
		// 2026-09-02 二期:质检失败集读一次(逐镜读文件太浪费)
		qcFailedSet := map[int]bool{}
		if mctx != nil {
			qcFailedSet = mctx.qcFailedShots()
		}
		for _, x := range shotsArr {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			id, _ := manjuToInt(m["shot_id"])
			clip := filepath.Join(str(P["clips"]), epEp, fmt.Sprintf("%02d.mp4", id))
			rendered := fileExists(clip)
			stale := false
			if rendered && mctx != nil {
				s := manjuShot{ID: id, Scene: str(m["scene"]), Duration: 5}
				if n, ok := manjuToInt(m["duration"]); ok && n > 0 {
					s.Duration = n
				}
				s.H3Prompt = str(m["h3_prompt"])
				if mctx.shotManifestStatus(s) == "stale" {
					stale = true
				}
			}
			if rendered {
				renderedN++
			}
			if stale {
				staleN++
			}
			// 2026-09-02 二期:镜头卡片补覆盖标记 + 视频预览 URL + 质检失败标记
			ov := manjuShotOverride{}
			if mctx != nil {
				mctx.episode = epEp
				ov = mctx.shotOverrideFor(id)
			}
			hasOv := ov.Seed != nil || ov.Steps != nil || ov.Sampler != "" ||
				ov.TurboLora != "" || ov.NegPrompt != "" || ov.PDD != nil || ov.Sage != nil
			qcFailed := qcFailedSet[id] && rendered
			video := ""
			if rendered {
				video = "clips/" + epEp + "/" + fmt.Sprintf("%02d.mp4", id)
			}
			shots = append(shots, map[string]any{
				"id":          id,
				"scene":       str(m["scene"]),
				"shot_size":   str(m["shot_size"]),
				"camera":      str(m["camera"]),
				"duration":    m["duration"],
				"characters":  m["characters"],  // 登场角色(中文详情,2026-09-03 视频管理弹窗)
				"action":      str(m["action"]), // 动作描述(中文详情)
				"dialogue":    str(m["dialogue"]),
				"narration":   str(m["narration"]),
				"h3_prompt":   str(m["h3_prompt"]), // 原始提示词(编辑用;最终化版走 shot/workflow)
				"has_dialogue": str(m["dialogue"]) != "" || strings.Contains(str(m["h3_prompt"]), "<d>"),
				"rendered":    rendered,
				"stale":       stale,
				"override":    hasOv,
				"qc_failed":   qcFailed,
				"video":       video,
			})
		}
		episodes = append(episodes, map[string]any{
			"episode":   epEp,
			"shots":     shots,
			"total":     len(shots),
			"rendered":  renderedN,
			"stale":     staleN,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": episodes})
}

// manjuShotsClear 清空指定集全部镜头渲染产物(mp4 + manifest 记录 + 条件缓存;
// 方案/定妆照/场景图保留)——「全部重做」的产物侧操作,随后由前端触发 render 全量重渲。
func manjuShotsClear(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := r.URL.Query().Get("episode")
	if configPath == "" || episode == "" {
		http.Error(w, `{"error":"missing config/episode"}`, http.StatusBadRequest)
		return
	}
	if !reEpisodeSafe.MatchString(episode) {
		writeErr(w, http.StatusBadRequest, "非法 episode 参数")
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	if manjuStateRunningFor(cp) {
		writeErr(w, http.StatusConflict, "渲染运行中,请先停止")
		return
	}
	configPath = cp
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "创建上下文失败: " + err.Error()})
		return
	}
	_, shots, err := ctx.loadPlan()
	if err != nil || len(shots) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "该集无方案/镜头,无需清理"})
		return
	}
	n := 0
	for _, s := range shots {
		ctx.clearShotArtifacts(s) // 删 mp4 + manifest + 检查点;条件缓存含指纹不显式删
		n++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": n, "episode": episode})
}

func manjuPlan(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config")
	episode := r.URL.Query().Get("episode")
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	cfg, err := readManjuConfig(configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "error": err.Error()})
		return
	}
	P, _ := cfg["paths"].(map[string]any)
	planPath := manjuFindPlanFile(str(P["analysis"]), episode)
	if planPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "error": "角色方案未生成，请先抽卡生成角色方案"})
		return
	}
	dataBytes, _ := os.ReadFile(planPath)
	planEp := strings.TrimSuffix(filepath.Base(planPath), "_characters.json")
	planEp = strings.TrimSuffix(planEp, "_direct_plan.json")
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
					"voice_ref": m["voice_ref"], "voice_name": m["voice_name"],
					"minor": m["minor"], // 群演轻量卡(2026-08-27):角色管理弹窗标「群演」
				})
			}
		}
	}
	shots := 0
	// 镜头数单独从同集 _direct_plan.json 读(角色方案 _characters.json 只有角色/场景,无 shots)
	if b, err := os.ReadFile(filepath.Join(str(P["analysis"]), planEp+"_direct_plan.json")); err == nil {
		var sp map[string]any
		if json.Unmarshal(b, &sp) == nil {
			if arr, ok := sp["shots"].([]any); ok {
				shots = len(arr)
			}
		}
	}
	// 附加磁盘抽卡历史:候选落盘于 _gacha/<char>[_<view>]_s<seed>.png 且不覆盖,弹窗重开仍可回看对比;
	// 每个角色按视图分组返回候选(front 主视图无后缀,full/side/detail 带 _<view>),前端视图 tab 展示
	if ctx, err := newManjuCtx(configPath, episode, "", "", ""); err == nil {
		gdir := filepath.Join(ctx.assetsDir, "characters", "_gacha")
		entries, _ := os.ReadDir(gdir)
		for i := range chars {
			id := str(chars[i]["id"])
			if id == "" {
				continue
			}
			safe := sanitizeFileName(id)
			views := []map[string]any{}
			for _, view := range append([]string{""}, manjuViewOrder[1:]...) {
				type cand struct {
					path string
					seed int
					mod  time.Time
				}
				list := []cand{}
				prefix := safe + "_s" // front 主视图: <char>_s<seed>.png
				if view != "" {
					prefix = safe + "_" + view + "_s"
				}
				for _, e := range entries {
					name := e.Name()
					if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".png") {
						continue
					}
					seed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".png"))
					if err != nil { // 其它角色前缀撞车/非 seed 文件,过滤
						continue
					}
					if fi, err := e.Info(); err == nil {
						list = append(list, cand{path: filepath.Join(gdir, name), seed: seed, mod: fi.ModTime()})
					}
				}
				sort.Slice(list, func(a, b int) bool { return list[a].mod.After(list[b].mod) })
				if len(list) > 12 {
					list = list[:12]
				}
				gacha := []map[string]any{}
				for _, c := range list {
					gacha = append(gacha, map[string]any{"image": c.path, "seed": c.seed})
				}
				// 采纳状态:主视图=主图存在;其余视图=对应视图文件存在
				ready := fileExists(filepath.Join(ctx.assetsDir, "characters", safe+".png"))
				if view != "" {
					ready = fileExists(filepath.Join(ctx.assetsDir, "characters", safe+"_"+view+".png"))
				}
				views = append(views, map[string]any{"view": view, "gacha": gacha, "ready": ready})
			}
			chars[i]["views"] = views
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": true, "episode_title": plan["episode_title"], "characters": chars, "shots": shots})
}

// manjuGacha 角色抽卡:一次连抽 count 张候选(随机 seed,前端可反复抽累积对比)
// view 可选:空=正面主视图,full/side/detail=对应视图(角色卡 views 提示词)
func manjuGacha(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := str(body["char"])
	view := str(body["view"])
	count := 1
	if v, ok := body["count"].(float64); ok {
		count = int(v)
	}
	if configPath == "" || char == "" {
		http.Error(w, `{"error":"missing config or char"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	imgs, err := manjuGachaDraw(cp, episode, char, view, count)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "生成失败: " + err.Error()})
		return
	}
	res := map[string]any{"ok": true, "images": imgs}
	if len(imgs) > 0 { // 兼容单抽旧字段
		res["image"] = imgs[0]["image"]
		res["seed"] = imgs[0]["seed"]
	}
	writeJSON(w, http.StatusOK, res)
}

// manjuGachaAdopt 采纳抽卡候选为正式定妆照(view 可选,同 manjuGacha)
func manjuGachaAdopt(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := str(body["char"])
	view := str(body["view"])
	image := str(body["image"])
	if configPath == "" || char == "" || image == "" {
		http.Error(w, `{"error":"missing config/char/image"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	// 角色名 sanitize:防路径穿越(审计 H7;upload 已有方案成员校验,adopt 也需)
	if err := manjuAdoptGacha(configPath, episode, char, view, image); err != nil {
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

// manjuCharPromptGet 角色形态图提示词(2026-08-27 角色管理「复制提示词」按钮):
// 按 角色名+视图+口径 重建该图生成时的最终提示词(manjuCharImagePrompt,与生成链同一套
// 构建函数),前端复制到剪贴板供外部使用/迭代。
func manjuCharPromptGet(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := str(body["char"])
	view := str(body["view"])
	kind := str(body["kind"]) // gacha=抽卡候选(默认);official=正式视图资产
	if configPath == "" || char == "" {
		http.Error(w, `{"error":"missing config or char"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	ctx, err := newManjuCtx(cp, episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	m := ctx.gachaCharInfo(char)
	if len(m) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "角色不在方案中: " + char})
		return
	}
	pos, neg := manjuCharImagePrompt(ctx, m, char, view, kind)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "prompt": pos, "negative": neg})
}

// manjuGachaUpload 上传角色图并直接采纳为正式定妆照(覆盖 → 指纹失效 → 后续渲染以它为身份参考)
func manjuGachaUpload(w http.ResponseWriter, r *http.Request) {
	// 真上限:MaxBytesReader 限制整个请求体(此前 64MB 只是 ParseMultipartForm 内存阈值,
	// 更大的文件会 spool 落盘,io.Copy 无限制可被灌满磁盘)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传过大或解析失败: "+err.Error())
		return
	}
	configPath := r.FormValue("config")
	episode := orDefault(r.FormValue("episode"), "EP01")
	char := strings.TrimSpace(r.FormValue("char"))
	view := strings.TrimSpace(r.FormValue("view"))
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
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
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
	// 单文件 20MB 上限(LimitReader+1 探测越界)
	n, err := io.Copy(out, io.LimitReader(file, 20<<20+1))
	if err != nil {
		out.Close()
		_ = os.Remove(saved)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Close()
	if n > 20<<20 {
		_ = os.Remove(saved)
		writeErr(w, http.StatusBadRequest, "文件超过 20MB 上限")
		return
	}
	if err := manjuAdoptGacha(configPath, episode, char, view, saved); err != nil {
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
	// 审计 F2:config 必须归属项目根目录(否则可对任意 JSON 文件读改写)
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	// 角色方案生成也读 render.chapters/episode,先写入(审计 P8:写 config 加 per-config 锁)
	cfgLock := manjuConfigLock(configPath)
	cfgLock.Lock()
	err := writeManjuRunParams(configPath, chapters, episode, shots)
	cfgLock.Unlock()
	if err != nil {
		http.Error(w, `{"error":"写入参数失败: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	if err := manjuPlanCharacters(configPath, episode); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "生成角色方案失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// manjuVoiceLibItem 预置风格音色库条目(2026-08-27 用户需求:预置多风格参考音频,按角色人设自动选择;
// 同日矩阵化升级:用户反馈音色不太友好,须按年龄×性别细分)。
// Key=稳定标识(参考音频文件名 stem,lib_<Key>.mp3,绑定/回显/生成共用);
// Name=edge-tts 音色名(实际发音引擎);Pitch/Rate=edge-tts 相对调节(童声=拔高加速,
// 老年=压低放缓——edge-tts 无真童声/老年声,用基音偏移派生,H3 只取 timbre 故有效)。
// ⚠️ 音色名经过 edge-tts 实机验证:2026-08-27 实测存活=Xiaoxiao/Xiaoyi/Yunjian/Yunxi/
// Yunxia/Yunyang+方言区域组;Xiaochen/Xiaomo/Xiaoshuang/Xiaoyou/Xiaohan/Xiaoxuan 已下线
// (NoAudioReceived/列表除名),勿加回;新增音色必须先实测可用。
// Gender/Age 是自动匹配标签(autoVoiceFor 矩阵),Vibe 仅供 UI 展示。
type manjuVoiceLibItem struct {
	Key   string // 稳定标识(文件名/绑定值,勿改——改了存量绑定断链)
	Name  string // edge-tts 音色名
	Label string
	Gender string // 男/女/童/通用
	Age    string // 儿童/少年/青年/中年/老年/方言…
	Vibe   string // 风格描述
	Pitch  string // edge-tts 相对音调,如 "+20Hz"/"-15Hz"(空=原生)
	Rate   string // edge-tts 相对语速,如 "+8%"/"-10%"(空=原生)
}

// manjuVoiceLib 内置风格音色库(2026-08-27 矩阵化):年龄×性别全覆盖 + 反派/兽类特化 +
// 方言/区域手动组。基音偏移派生童声/老年声(edge-tts 无原生童声老年声)。
// 2026-08-30 ver15 差异化变体:同档位多角色(如两个中年男)按登场序分配
// _2/_3 变体(不同声源或音高/语速偏移)——根治「同档位角色共用一音色分不清」;
// narrator 叙述音色为旁白专属(与所有角色档位区分,见 manjuOffscreenBindings)。
var manjuVoiceLib = []manjuVoiceLibItem{
	// ---- 自动匹配矩阵(性别×年龄) ----
	{"child_boy", "zh-CN-YunxiaNeural", "童声 · 小男孩", "童", "儿童", "清脆", "+20Hz", "+6%"},
	{"child_boy_2", "zh-CN-YunxiaNeural", "童声 · 小男孩·亮", "童", "儿童", "亮堂", "+26Hz", "+9%"},
	{"child_girl", "zh-CN-XiaoyiNeural", "童声 · 小女孩", "童", "儿童", "娇萌", "+25Hz", "+8%"},
	{"child_girl_2", "zh-CN-XiaoyiNeural", "童声 · 小女孩·甜", "童", "儿童", "甜糯", "+28Hz", "+10%"},
	{"boy_teen", "zh-CN-YunxiaNeural", "少年 · 元气男声", "男", "少年", "元气", "", ""},
	{"boy_teen_2", "zh-CN-YunxiaNeural", "少年 · 清亮男声", "男", "少年", "清亮", "-3Hz", "+5%"},
	{"girl_lively", "zh-CN-XiaoyiNeural", "少女 · 活泼女声", "女", "少女", "活泼", "", ""},
	{"girl_lively_2", "zh-CN-XiaoyiNeural", "少女 · 甜美女声", "女", "少女", "甜美", "+8Hz", "+3%"},
	{"male_sun", "zh-CN-YunxiNeural", "青年 · 阳光男声", "男", "青年", "阳光", "", ""},
	{"male_sun_2", "zh-CN-YunxiNeural", "青年 · 清爽男声", "男", "青年", "清爽", "-3Hz", "+4%"},
	{"female_warm", "zh-CN-XiaoxiaoNeural", "青年 · 温柔女声", "女", "青年", "温柔", "", ""},
	{"female_warm_2", "zh-CN-XiaoxiaoNeural", "青年 · 知性女声", "女", "青年", "知性", "-2Hz", "0%"},
	{"male_mag", "zh-CN-YunjianNeural", "中年 · 磁性男声", "男", "中年", "磁性", "", ""},
	{"male_mag_2", "zh-CN-YunyangNeural", "中年 · 沉稳男声", "男", "中年", "沉稳", "", ""},
	{"male_mag_3", "zh-CN-YunxiNeural", "中年 · 粗犷男声", "男", "中年", "粗犷", "-8Hz", "-6%"},
	{"female_mature", "zh-CN-XiaoxiaoNeural", "中年 · 知性女声", "女", "中年", "知性", "-4Hz", "-5%"},
	{"female_mature_2", "zh-CN-XiaoxiaoNeural", "中年 · 温和女声", "女", "中年", "温和", "-6Hz", "-3%"},
	{"male_elder", "zh-CN-YunjianNeural", "老年 · 沧桑男声", "男", "老年", "沧桑", "-15Hz", "-12%"},
	{"male_elder_2", "zh-CN-YunyangNeural", "老年 · 厚重男声", "男", "老年", "厚重", "-12Hz", "-8%"},
	{"female_elder", "zh-CN-XiaoxiaoNeural", "老年 · 沉稳女声", "女", "老年", "沉稳", "-10Hz", "-15%"},
	{"female_elder_2", "zh-CN-XiaoxiaoNeural", "老年 · 温和女声", "女", "老年", "温和", "-14Hz", "-12%"},
	// ---- 特化(反派/兽类,autoVoiceFor 优先于年龄矩阵) ----
	{"male_deep", "zh-CN-YunjianNeural", "反派 · 低沉男声", "男", "通用", "威压", "-8Hz", "-8%"},
	{"male_deep_2", "zh-CN-YunxiNeural", "反派 · 沙哑男声", "男", "通用", "沙哑", "-10Hz", "-10%"},
	{"female_deep", "zh-CN-XiaoxiaoNeural", "反派 · 冷冽女声", "女", "通用", "冷冽", "-6Hz", "-8%"},
	{"female_deep_2", "zh-CN-XiaoxiaoNeural", "反派 · 肃杀女声", "女", "通用", "肃杀", "-8Hz", "-10%"},
	// 2026-09-04 神谕档(九霄·主脑投影实配):无面光体/天道/系统主脑类"空旷神谕感"
	// 专用——外部音源(钟离基底 ffmpeg 空旷化:微降调+大空间回声+低通),edge-tts
	// 参数仅为兜底占位(有 .src 外部音源时不会用到)
	{"male_oracle", "zh-CN-YunjianNeural", "神谕 · 空旷男声", "男", "通用", "空旷神谕", "-12Hz", "-14%"},
	{"beast_cute", "zh-CN-XiaoyiNeural", "萌系 · 灵宠兽类", "通用", "通用", "呆萌", "+12Hz", "+8%"},
	{"beast_cute_2", "zh-CN-XiaoyiNeural", "萌系 · 奶音兽类", "通用", "通用", "奶音", "+16Hz", "+10%"},
	// ---- 叙述(旁白专属,autoVoiceFor 不返回;manjuOffscreenBindings 绑定客观旁白) ----
	{"male_narrator", "zh-CN-YunyangNeural", "叙述 · 沉稳男声", "男", "叙述", "中立", "-2Hz", "0%"},
	{"female_narrator", "zh-CN-XiaoxiaoNeural", "叙述 · 冷静女声", "女", "叙述", "中立", "-2Hz", "-2%"},
	// ---- 方言/区域(手动选择/技能侧「音色」字段配置;东北/陕西/粤/台=edge-tts 原生
	// 方言声源;四川/河南/广西/湖南 edge-tts 无方言声源——用不同普通话声源做基底,
	// 口音由 Audio 定义行描述驱动 H3 生成(manjuVoicePhraseFor 带 accent 描述)) ----
	{"cn_dongbei", "zh-CN-liaoning-XiaobeiNeural", "方言 · 东北女声", "女", "方言", "爽朗", "", ""},
	{"cn_shaanxi", "zh-CN-shaanxi-XiaoniNeural", "方言 · 陕西女声", "女", "方言", "亮堂", "", ""},
	{"cn_sichuan", "zh-CN-YunxiNeural", "方言 · 四川男声", "男", "方言", "麻辣", "+2Hz", "0%"},
	{"cn_henan", "zh-CN-YunxiaNeural", "方言 · 河南男声", "男", "方言", "质朴", "-2Hz", "0%"},
	{"cn_guangxi", "zh-CN-YunyangNeural", "方言 · 广西男声", "男", "方言", "温吞", "0%", "-4%"},
	{"cn_hunan", "zh-CN-YunjianNeural", "方言 · 湖南男声", "男", "方言", "热辣", "-4Hz", "0%"},
	{"hk_female", "zh-HK-HiuMaanNeural", "区域 · 粤语女声", "女", "区域", "港风", "", ""},
	{"hk_male", "zh-HK-WanLungNeural", "区域 · 粤语男声", "男", "区域", "港风", "", ""},
	{"tw_female", "zh-TW-HsiaoChenNeural", "区域 · 台湾女声", "女", "区域", "台普", "", ""},
	{"tw_male", "zh-TW-YunJheNeural", "区域 · 台湾男声", "男", "区域", "台普", "", ""},
}

// manjuVoiceLibFor 按 Key(优先)或旧版 edge 音色名(存量绑定兼容)查库条目;未命中返回 nil
func manjuVoiceLibFor(id string) *manjuVoiceLibItem {
	for i := range manjuVoiceLib {
		if manjuVoiceLib[i].Key == id {
			return &manjuVoiceLib[i]
		}
	}
	for i := range manjuVoiceLib {
		if manjuVoiceLib[i].Name == id {
			return &manjuVoiceLib[i]
		}
	}
	return nil
}

// manjuVoicePackPrefix 音色包绑定值前缀:voice=<prefix><文件名> 表示直接复制自备音频(不走 TTS)
const manjuVoicePackPrefix = "pack:"

// manjuVoicePacksDir 自备音色包目录(ComfyUI input/audio/voicepacks):用户把取得授权的
// 参考音频(mp3/wav)放进来即可在角色管理下拉选用——「全网热门音色」的合规入口。
func manjuVoicePacksDir() string {
	in := comfy.In()
	if in == "" {
		in = filepath.Join(paths.ComfySharedDir, "input")
	}
	return filepath.Join(in, "audio", "voicepacks")
}

// manjuVoicePacks 扫描自备音色包(按名排序,前端下拉稳定)
func manjuVoicePacks() []string {
	entries, err := os.ReadDir(manjuVoicePacksDir())
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".mp3" || ext == ".wav" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// manjuVoiceLibList 兼容视图(前端音色下拉数据源,2026-08-26 起)
func manjuVoiceLibList() []map[string]any {
	out := make([]map[string]any, 0, len(manjuVoiceLib))
	for _, v := range manjuVoiceLib {
		out = append(out, map[string]any{
			"key": v.Key, "name": v.Name, "label": v.Label, "gender": v.Gender, "age": v.Age, "vibe": v.Vibe,
		})
	}
	return out
}

// manjuVoiceGenText 音色参考音频的兜底参考文本(H3 只引用 timbre,内容不限)
const manjuVoiceGenText = "今天天气不错,我们一起去公园走走吧。"

// manjuVoiceGenTextFor 按音色 key 返回情感化参考文本(2026-09-03 配音去 AI 味):
// 旧版全部音色念同一句中性句「今天天气不错…」——edge-tts 以最平的播报腔念出,
// 平板韵律随参考音频传给 H3 克隆,是「配音 AI 味」的直接来源之一。
// 每档音色按人设配 2-3 个情绪短句(感叹/疑问/停顿),edge-tts 对标点与语气词
// 敏感,情感文本能带出真实起伏;未列出的 key(变体/新增)兜底中性句。
func manjuVoiceGenTextFor(key string) string {
	texts := map[string]string{
		"child_boy":       "哼,我才不怕呢!看我的——冲呀!",
		"child_girl":      "哇,快看呀!是蝴蝶哎——等等我来啦!",
		"boy_teen":        "这有什么难的?交给我,保证给你办好!",
		"girl_lively":     "真的吗?太好啦!走走走,这回你可别想溜哦。",
		"male_sun":        "没事,有我在。放心,这点事难不倒咱们。",
		"female_warm":     "别急,慢慢来。你看,这不是挺好的嘛。",
		"male_mag":        "嗯……此事,没那么简单。你先退下,容我想想。",
		"female_mature":   "哦?说说看,到底怎么回事——别慌,慢慢讲。",
		"male_elder":      "唉,几十年喽……想当年啊,老夫也是一把好手。",
		"female_elder":    "哎哟,我的儿……来来来,坐下,我慢慢跟你讲。",
		"male_deep":       "呵……你以为,你还有得选么?",
		"female_deep":     "说吧——你最好,别让我问第二遍。",
		"beast_cute":      "嘿嘿!好吃的?给我给我,我最喜欢啦!",
		"male_narrator":   "那一夜,雨下得很急。谁也没想到,故事,从这里开始了。",
		"female_narrator": "那一夜,雨下得很急。谁也没想到,故事,从这里开始了。",
		"cn_dongbei":      "哎呀,这可太好了!来来来,咱好好唠唠。",
		"cn_shaanxi":      "咋嘛?这事情,有啥捋不顺的!坐下说。",
		"cn_sichuan":      "要得要得!这个事情嘛,包在我身上,巴适得很!",
		"cn_henan":        "中!这事儿管保给你弄妥帖,恁就瞧好吧!",
		"cn_guangxi":      "莫急莫急,慢慢来,事情总会搞定的啦。",
		"cn_hunan":        "要得!咯硬是好事咧,快跟我讲讲看!",
		"hk_male":         "唔……呢件事,你点睇啊?讲来听听。",
		"hk_female":       "哇,真系好正啊!快啲话俾我知发生咩事啦!",
		"tw_male":         "齁~你这样不行喔!来来来,我教你啦。",
		"tw_female":       "真的假的?太夸张了吧!快点跟我说清楚喔。",
	}
	if t, ok := texts[key]; ok {
		return t
	}
	return manjuVoiceGenText
}

// manjuVoiceList 可用配音音色列表(前端音色下拉数据源;2026-08-27 增自备音色包)
func manjuVoiceList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"voices": manjuVoiceLibList(), "packs": manjuVoicePacks()})
}

// manjuVoicePrepare 预生成风格音色库中缺失的参考音频(edge-tts → input/audio/lib_<name>.mp3)。
// 渲染前也会按需自动补齐(autoVoiceFor 命中但文件缺失时);本接口供 UI 一键预生成全部。
func manjuVoicePrepare(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	if configPath == "" {
		http.Error(w, `{"error":"missing config"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	ctx, err := newManjuCtx(cp, "EP01", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "创建上下文失败: " + err.Error()})
		return
	}
	done, failed := ctx.ensureVoiceLib()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generated": done, "failed": failed})
}

// manjuVoiceGen 生成角色配音音色参考音频(edge-tts → ComfyUI input/audio/),并写入方案角色绑定。
// voice 传空 = 解绑(清除该角色音色)。音色文件确定性命名 voice_<项目>_<角色ID>.mp3,
// 渲染端按同名查找挂载 ref_audios(见 charVoiceRef),plan 只存绑定记录供前端回显。
func manjuVoiceGen(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	configPath := str(body["config"])
	episode := orDefault(str(body["episode"]), "EP01")
	char := sanitizeFileName(str(body["char"]))
	voice := strings.TrimSpace(str(body["voice"]))
	if configPath == "" || char == "" {
		http.Error(w, `{"error":"missing config/char"}`, http.StatusBadRequest)
		return
	}
	cp, gerr := manjuGuardConfig(configPath)
	if gerr != nil {
		writeErr(w, http.StatusForbidden, gerr.Error())
		return
	}
	configPath = cp
	ctx, err := newManjuCtx(configPath, episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "创建上下文失败: " + err.Error()})
		return
	}
	plan, _, err := ctx.loadPlan()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读取方案失败: " + err.Error()})
		return
	}
	// 角色必须存在于方案(防任意写 plan)
	found := false
	if arr, ok := plan["characters"].([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok && str(m["id"]) == char {
				found = true
				break
			}
		}
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "角色「" + char + "」不在方案中"})
		return
	}
	rel := "audio/voice_" + ctx.project + "_" + char + ".mp3"
	out := filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))
	if voice == "" {
		// 解绑:删音频(权威副本+input 副本) + 清 plan 绑定
		_ = os.Remove(out)
		if paths.VoiceLibDir != "" {
			_ = os.Remove(filepath.Join(paths.VoiceLibDir, filepath.FromSlash(rel)))
			_ = rebuildVoiceLibIndex()
		}
		setVoiceBinding(plan, char, "", "")
		if err := ctx.writePlan(plan); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "保存方案失败: " + err.Error()})
			return
		}
		ctx.writeCharactersJSON(plan)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bound": false})
		return
	}
	// 音色包绑定(2026-08-27):voice="pack:<文件名>" → 直接复制自备音频为角色音色参考,
	// 不走 TTS(用户取得授权的参考音频入口);文件名校验防路径穿越
	if strings.HasPrefix(voice, manjuVoicePackPrefix) {
		pf := filepath.Base(strings.TrimPrefix(voice, manjuVoicePackPrefix))
		ext := strings.ToLower(filepath.Ext(pf))
		if ext != ".mp3" && ext != ".wav" || strings.ContainsAny(pf, `/\`) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "音色包文件仅支持 mp3/wav: " + pf})
			return
		}
		src := filepath.Join(manjuVoicePacksDir(), pf)
		if !fileExists(src) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "音色包不存在(放入 input/audio/voicepacks/ 后刷新): " + pf})
			return
		}
		if err := copyFile(src, out); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "音色包复制失败: " + err.Error()})
			return
		}
		ctx.syncVoiceBindingAuthoritative(rel)
		setVoiceBinding(plan, char, filepath.ToSlash(rel), voice)
		if err := ctx.writePlan(plan); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "保存方案失败: " + err.Error()})
			return
		}
		ctx.writeCharactersJSON(plan)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bound": true, "voice_ref": filepath.ToSlash(rel), "voice_name": voice})
		return
	}
	// 库音色绑定:Key(新版)/旧 edge 音色名(存量兼容)→ 查表生成(带基音偏移派生参数)
	item := manjuVoiceLibFor(voice)
	if item == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "未知音色: " + voice})
		return
	}
	args := []string{"voice-gen", "--text", manjuVoiceGenTextFor(item.Key), "--voice", item.Name, "--out", out}
	if item.Pitch != "" {
		args = append(args, "--pitch="+item.Pitch) // 等号形式:负值防 argparse 误判为选项
	}
	if item.Rate != "" {
		args = append(args, "--rate="+item.Rate)
	}
	if _, err := ctx.runMediaOut(args...); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "音色生成失败: " + err.Error()})
		return
	}
	// 0 字节防护(不支持的音色 edge-tts 不报错但产出空文件,LoadAudio 会 400)
	if fi, serr := os.Stat(out); serr != nil || fi.Size() == 0 {
		_ = os.Remove(out)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "音色生成产出空文件(音色不可用?): " + item.Name})
		return
	}
	ctx.syncVoiceBindingAuthoritative(rel)
	setVoiceBinding(plan, char, filepath.ToSlash(rel), item.Key)
	if err := ctx.writePlan(plan); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "保存方案失败: " + err.Error()})
		return
	}
	ctx.writeCharactersJSON(plan)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bound": true, "voice_ref": filepath.ToSlash(rel), "voice_name": item.Key})
}

// syncVoiceBindingAuthoritative 角色显式绑定音频同步权威副本(voice_lib/audio/…,
// 2026-08-29 稳定化:ComfyUI input 副本被误删时可由权威目录重建)
func (ctx *manjuCtx) syncVoiceBindingAuthoritative(rel string) {
	if paths.VoiceLibDir == "" {
		return
	}
	src := filepath.Join(ctx.comfyInput, filepath.FromSlash(rel))
	if !fileExists(src) {
		return
	}
	dst := filepath.Join(paths.VoiceLibDir, filepath.FromSlash(rel))
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	if copyFile(src, dst) == nil {
		_ = rebuildVoiceLibIndex() // 汇总索引实时同步(2026-09-03)
	}
}

// setVoiceBinding 写角色音色绑定到方案 characters(voice 为空清绑定)
func setVoiceBinding(plan map[string]any, char, voiceRef, voiceName string) {
	arr, _ := plan["characters"].([]any)
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok || str(m["id"]) != char {
			continue
		}
		if voiceRef == "" {
			delete(m, "voice_ref")
			delete(m, "voice_name")
		} else {
			m["voice_ref"] = voiceRef
			m["voice_name"] = voiceName
		}
	}
}

// registerManjuRoutes 注册漫剧工作台全部端点
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/manju/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"projects": listManjuProjects()})
	})
	mux.HandleFunc("GET /api/manju/find", manjuFindByNovel)
	mux.HandleFunc("GET /api/manju/project", manjuProject)
	mux.HandleFunc("POST /api/manju/render", manjuSaveRender)
	mux.HandleFunc("GET /api/manju/style", manjuStyleInfo)
	mux.HandleFunc("POST /api/manju/create", manjuCreate)
	mux.HandleFunc("POST /api/manju/probe-dir", manjuProbeDir)
	mux.HandleFunc("POST /api/manju/delete", manjuDeleteProject)
	mux.HandleFunc("GET /api/manju/settings", manjuSettingsGet)
	mux.HandleFunc("POST /api/manju/settings", manjuSettingsPost)
	mux.HandleFunc("GET /api/manju/novel", manjuNovelInfo)
	mux.HandleFunc("POST /api/manju/novel/save", manjuNovelSave)
	mux.HandleFunc("GET /api/manju/script", manjuScriptStatus)
	mux.HandleFunc("POST /api/manju/script/save", manjuScriptSave)
	mux.HandleFunc("POST /api/manju/script/clear", manjuScriptClear)
	mux.HandleFunc("POST /api/manju/script/import-from-novel", manjuScriptImportFromNovel)
	mux.HandleFunc("POST /api/manju/script/scan-dir", manjuScriptScanDir)
	mux.HandleFunc("POST /api/manju/script/import-dir", manjuScriptImportDir)
	mux.HandleFunc("POST /api/manju/script/import-all-dir", manjuScriptImportAllDir)
	mux.HandleFunc("GET /api/manju/models", manjuModels)
	mux.HandleFunc("POST /api/manju/env", manjuEnv)
	mux.HandleFunc("POST /api/manju/run", manjuRun)
	mux.HandleFunc("GET /api/manju/status", func(w http.ResponseWriter, r *http.Request) {
		configPath := r.URL.Query().Get("config")
		if configPath != "" {
			cp, gerr := manjuGuardConfig(configPath)
			if gerr != nil {
				writeErr(w, http.StatusForbidden, gerr.Error())
				return
			}
			configPath = cp
		}
		res := manjuStatusFor(configPath)
		// 附带审片报告摘要(智能体模式数据源,前端复用同一轮询)
		res["agent"] = agentStatusSummary(configPath)
		writeJSON(w, http.StatusOK, res)
	})
	// 清空运行日志(2026-08-26 用户反馈:清空日志按钮只在前端覆盖一条「(就绪)」,
	// 后端 manjuState.log 未清,2 秒后轮询又把旧日志拉回来)。
	// 2026-08-28 二修(用户再反馈「清空日志按钮无效」):只清内存仍无效——空闲时轮询的
	// logTail 来源是磁盘(run_state.json 的 logTail 字段 + run.log 文件),必须三清:
	// ①内存 manjuState.log ②run.log 截断 ③run_state.json 的 logTail 置空。
	// 运行中允许清(后续日志继续追加);排障证据由 logs/ 级别文件保留,run.log 属展示层。
	mux.HandleFunc("POST /api/manju/log/clear", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		manjuState.mu.Lock()
		manjuState.log = ""
		manjuState.mu.Unlock()
		out := map[string]any{"ok": true}
		if configPath := strings.TrimSpace(str(body["config"])); configPath != "" {
			if cp, gerr := manjuGuardConfig(configPath); gerr == nil {
				project := filepath.Base(filepath.Dir(cp))
				if p := manjuRunLogPath(project); fileExists(p) {
					_ = os.Truncate(p, 0)
					out["runLog"] = true
				}
				if ds := loadManjuDiskState(project); ds != nil && ds.LogTail != "" {
					ds.LogTail = ""
					writeManjuDiskState(project, ds)
					out["runState"] = true
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /api/manju/flow", manjuFlowCheck)
	mux.HandleFunc("POST /api/manju/kill", manjuKill)
	mux.HandleFunc("GET /api/manju/outputs", manjuOutputs)
	mux.HandleFunc("GET /api/manju/plan", manjuPlan)
	mux.HandleFunc("GET /api/manju/shots", manjuShots)
	mux.HandleFunc("POST /api/manju/shots/clear", manjuShotsClear)
	// 2026-09-01 二合一阶段一:镜头级调试(工作流可视化/参数覆盖)
	mux.HandleFunc("GET /api/manju/shot/workflow", manjuShotWorkflowHandler)
	mux.HandleFunc("POST /api/manju/shot/override", manjuShotOverrideHandler)
	mux.HandleFunc("GET /api/manju/shot/overrides", manjuShotOverridesHandler)
	mux.HandleFunc("GET /api/manju/shot/asset", manjuShotDebugStatic)
	mux.HandleFunc("GET /api/manju/shot/video", manjuShotVideoHandler)
	// 2026-09-03 画布三期:参数模板(批量应用)+ 渲染中间产物(缓存/latent/抽帧)
	mux.HandleFunc("GET /api/manju/shot/templates", manjuShotTemplatesHandler)
	mux.HandleFunc("POST /api/manju/shot/template/save", manjuShotTemplateSaveHandler)
	mux.HandleFunc("POST /api/manju/shot/template/delete", manjuShotTemplateDeleteHandler)
	mux.HandleFunc("POST /api/manju/shot/template/apply", manjuShotTemplateApplyHandler)
	mux.HandleFunc("GET /api/manju/shot/intermediates", manjuShotIntermediatesHandler)
	mux.HandleFunc("GET /api/manju/shot/frame", manjuShotFrameHandler)
	// 2026-09-03 视频管理三弹窗:单镜编辑(分镜中文详情)/场景管理列表
	mux.HandleFunc("POST /api/manju/shot/edit", manjuShotEditHandler)
	mux.HandleFunc("GET /api/manju/scenes", manjuScenesHandler)
	// 2026-09-02 角色资产库(跨项目复用)
	mux.HandleFunc("GET /api/manju/char-lib/list", manjuCharLibHandler)
	mux.HandleFunc("GET /api/manju/char-lib/detail", manjuCharLibDetailHandler)
	mux.HandleFunc("GET /api/manju/char-lib/asset", manjuCharLibAssetHandler)
	mux.HandleFunc("POST /api/manju/char-lib/import", manjuCharLibImportHandler)
	mux.HandleFunc("POST /api/manju/char-lib/delete", manjuCharLibDeleteHandler)
	mux.HandleFunc("POST /api/manju/char-lib/delete-batch", manjuCharLibDeleteBatchHandler)
	mux.HandleFunc("GET /api/manju/notes", manjuNotesGet)
	mux.HandleFunc("POST /api/manju/notes", manjuNotesPost)
	mux.HandleFunc("GET /api/manju/paths", manjuPathsGet)
	mux.HandleFunc("POST /api/manju/paths", manjuPathsPost)
	mux.HandleFunc("POST /api/manju/skill/update", manjuSkillUpdate)
	mux.HandleFunc("POST /api/manju/gacha", manjuGacha)
	mux.HandleFunc("POST /api/manju/gacha/upload", manjuGachaUpload)
	mux.HandleFunc("POST /api/manju/gacha/adopt", manjuGachaAdopt)
	mux.HandleFunc("POST /api/manju/gacha/plan", manjuGachaPlan)
	mux.HandleFunc("POST /api/manju/char/prompt", manjuCharPromptGet)
	mux.HandleFunc("GET /api/manju/voice/list", manjuVoiceList)
	mux.HandleFunc("POST /api/manju/voice/gen", manjuVoiceGen)
	mux.HandleFunc("POST /api/manju/voice/prepare", manjuVoicePrepare)
	mux.HandleFunc("GET /api/manju/notify", func(w http.ResponseWriter, r *http.Request) {
		// 审计 F11:Token 明文返回泄漏(serverchan/pushplus/wxpusher 均为密钥)——掩码展示
		n := loadManjuNotify()
		n.Endpoint = maskKey(n.Endpoint)
		n.Token = maskKey(n.Token)
		n.UID = maskKey(n.UID)
		writeJSON(w, http.StatusOK, n)
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
		// 掩码回写协议:GET 返回的是掩码值(maskKey),保存时若仍是掩码(=未修改)保留原值
		old := loadManjuNotify()
		if strings.Contains(n.Token, "****") {
			n.Token = old.Token
		}
		if strings.Contains(n.Endpoint, "****") {
			n.Endpoint = old.Endpoint
		}
		if strings.Contains(n.UID, "****") {
			n.UID = old.UID
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
	registerUpscaleRoutes(mux)
	registerIRRoutes(mux)
	registerAgentRoutes(mux)
	registerCleanupRoute(mux)
	registerNovelRoutes(mux)
}

// novel 域路由(2026-09-03 拆包:小说创作 handler 随 manju 域走)
func registerNovelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/novel/create", handleNovelCreate)
	mux.HandleFunc("POST /api/novel/analyze", handleNovelAnalyze)
	mux.HandleFunc("POST /api/novel/review", handleNovelReview)
	mux.HandleFunc("POST /api/novel/chapter", handleNovelChapter)
	mux.HandleFunc("POST /api/novel/delete", handleNovelDelete)
	mux.HandleFunc("GET /api/novel/progress", handleNovelProgress)
	mux.HandleFunc("POST /api/novel/auto", handleNovelAuto)
	mux.HandleFunc("POST /api/novel/auto/stop", handleNovelAutoStop)
	mux.HandleFunc("GET /api/novel/auto/status", handleNovelAutoStatus)
}
