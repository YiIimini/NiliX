package comfy

// 自包含部署自愈(2026-08-26 ComfyUI 整合进 NiliX):
// NiliX 目录整体拷贝到新电脑(路径/用户名变化)后,首次启动自动校准三处路径绑定,
// 无需手工改任何配置。每次拉起 ComfyUI 前执行,幂等。
//  1. extra_model_paths.yaml:base_path 重写为当前 Shared 目录(绝对路径)
//  2. .venv/pyvenv.cfg:home 解析失败时重写为 paths.ComfyRootDir 旁的 standalone-env
//  3. NILIX_COND_CACHE 环境变量:conditioning 缓存目录注入(H3-ConditioningCache 首选)

import (
	"nilix/internal/paths"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var basePathRe = regexp.MustCompile(`(?m)^(\s*base_path:\s*)(\S.*)$`)

// comfySelfHeal 拉起 ComfyUI 前的路径自愈,返回需注入的环境变量。
func comfySelfHeal() []string {
	healExtraModelPaths()
	healPyvenvCfg()
	var env []string
	if d := filepath.Join(paths.ComfySharedDir, "models", "conditioning"); dirExists(d) {
		env = append(env, "NILIX_COND_CACHE="+d)
	}
	return env
}

// healExtraModelPaths 校准 extra_model_paths.yaml 的 base_path 指向当前 Shared。
// 文件缺失时按共享目录的标准映射重建(checkpoints/loras/vae 等 28 类)。
func healExtraModelPaths() {
	if paths.ComfySharedDir == "" {
		return
	}
	path := filepath.Join(paths.ComfyRootDir, "extra_model_paths.yaml")
	want := filepath.ToSlash(paths.ComfySharedDir) + "/"
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[自愈] extra_model_paths.yaml 缺失,重建指向 %s", want)
		if werr := os.WriteFile(path, []byte(defaultExtraModelPaths(want)), 0644); werr != nil {
			log.Printf("[自愈] 重建失败(忽略): %v", werr)
		}
		return
	}
	txt := string(b)
	m := basePathRe.FindStringSubmatch(txt)
	if m != nil && strings.TrimRight(strings.Trim(m[2], `/"`), "/") == strings.TrimRight(want, "/") {
		return // 已指向当前 Shared
	}
	ntxt := basePathRe.ReplaceAllString(txt, "${1}"+want)
	if ntxt == txt && !strings.Contains(txt, "base_path") {
		ntxt = defaultExtraModelPaths(want) // 空文件/无 base_path:整体重建
	}
	if err := os.WriteFile(path, []byte(ntxt), 0644); err != nil {
		log.Printf("[自愈] base_path 重写失败(忽略): %v", err)
	} else {
		log.Printf("[自愈] extra_model_paths.yaml base_path → %s", want)
	}
}

// defaultExtraModelPaths 生成标准共享映射(与 2026-08 起旧安装的 yaml 同构)。
func defaultExtraModelPaths(base string) string {
	subs := []string{
		"checkpoints", "clip", "clip_vision", "configs", "controlnet", "embeddings",
		"loras", "vae", "vae_approx", "upscale_models", "diffusion_models", "text_encoders",
		"audio_encoders", "style_models", "hypernetworks", "photomaker", "ipadapter",
		"unet", "gguf", "reference", "reactor", "insightface", "animatediff_models",
		"animatediff_motion_models", "sam", "grounding", "LLM", "custom_nodes",
	}
	var b strings.Builder
	b.WriteString("shared:\n    base_path: " + base + "\n")
	for _, s := range subs {
		b.WriteString("    " + s + ": models/" + s + "/\n")
	}
	return b.String()
}

// healPyvenvCfg 校准 .venv/pyvenv.cfg 的 home(base Python 位置)。
// home 支持绝对或相对(相对 pyvenv.cfg 所在的 .venv 目录解析);解析结果
// 无 python.exe 时重写为 paths.ComfyRootDir 旁的 standalone-env(自包含布局)。
func healPyvenvCfg() {
	venvDir := filepath.Join(paths.ComfyRootDir, ".venv")
	cfgPath := filepath.Join(venvDir, "pyvenv.cfg")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return // 无 venv(非自包含部署),交给 startComfy 的 python.exe 预检报错
	}
	if home := pyvenvHome(string(b)); home != "" {
		p := home
		if !filepath.IsAbs(p) {
			p = filepath.Join(venvDir, p)
		}
		if fileExists(filepath.Join(p, "python.exe")) {
			return // home 可解析,无需修复
		}
	}
	want := filepath.Join(filepath.Dir(paths.ComfyRootDir), "standalone-env")
	if !fileExists(filepath.Join(want, "python.exe")) {
		return // 旁边也没有 base python,写了也起不来;交给启动报错可见
	}
	nb := rewritePyvenvHome(string(b), filepath.ToSlash(want))
	if err := os.WriteFile(cfgPath, []byte(nb), 0644); err != nil {
		log.Printf("[自愈] pyvenv.cfg home 重写失败(忽略): %v", err)
	} else {
		log.Printf("[自愈] pyvenv.cfg home → %s", want)
	}
}

// pyvenvHome 提取 pyvenv.cfg 的 home 值(无则空串)。
func pyvenvHome(cfg string) string {
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "home") && strings.Contains(line, "=") {
			return strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
		}
	}
	return ""
}

// rewritePyvenvHome 只替换 home 行,其余原样保留。
func rewritePyvenvHome(cfg, newHome string) string {
	var b strings.Builder
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(strings.TrimRight(line, "\r"), "home") && strings.Contains(line, "=") {
			b.WriteString("home = " + newHome)
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
