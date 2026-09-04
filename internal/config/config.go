// Package config 负责服务的全局设置：结构定义、JSON 持久化与 API key 加密存储。
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const encPrefix = "enc:"

// Settings 是服务全局设置，结构与既有的漫剧 config.json 对齐。
type Settings struct {
	LLM    LLMSettings     `json:"llm"`
	Render RenderSettings  `json:"render"`
	Paths  PathSettings    `json:"paths"`
	// Agent 智能体全局默认配置(视觉模型/判分参数):项目 config.json 的 agent 节可覆盖。
	// 指针+omitempty:前端顶栏设置表单不带此字段时不会被零值清空。
	Agent *AgentSettings `json:"agent,omitempty"`
	// Window 桌面主窗口记忆(用户调整后持久化,下次启动直接加载;0=未记忆按 16:9 默认)
	Window WindowSettings `json:"window,omitempty"`
	// Island 灵动岛悬浮胶囊配置(启用/禁用;默认启用)
	Island IslandSettings `json:"island,omitempty"`
	// Cleanup 每日维护:凌晨清空 ComfyUI 共享 input/output(接替原 Windows 计划任务,
	// 其护栏锁旧 AppData 路径,自包含迁移后对新位置自动失效;默认启用)。
	Cleanup CleanupSettings `json:"cleanup,omitempty"`
}

// CleanupSettings 每日清理配置。
type CleanupSettings struct {
	Daily *bool `json:"daily,omitempty"` // nil=未设置(默认启用)
}

// DailyEnabled 是否启用每日清理(缺省启用)。
func (c CleanupSettings) DailyEnabled() bool {
	return c.Daily == nil || *c.Daily
}

// IslandSettings 灵动岛 HUD 悬浮胶囊配置。
type IslandSettings struct {
	Enabled *bool `json:"enabled,omitempty"` // nil=未设置(默认启用)
}

// WindowSettings 主窗口尺寸记忆(用户手动调整窗口大小后落盘,下次启动直接恢复)
type WindowSettings struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	Max    bool `json:"max,omitempty"` // 是否最大化
}

// AgentSettings 智能体全局默认(视觉模型地址/Key/模型/及格线/返工轮数)。
type AgentSettings struct {
	Enabled          bool    `json:"enabled,omitempty"`
	VisionBaseURL    string  `json:"vision_base_url,omitempty"`
	VisionAPIKey     string  `json:"vision_api_key,omitempty"`
	VisionModel      string  `json:"vision_model,omitempty"`
	PassScore        float64 `json:"pass_score,omitempty"`
	MaxRetries       int     `json:"max_retries,omitempty"`
	JudgeConcurrency int     `json:"judge_concurrency,omitempty"` // 视觉判分并发上限(1-4,0=默认)
	// AutoResolve 预算耗尽 AI 终审自动拍板(nil=未设置,用 agent 包默认 true)
	AutoResolve *bool `json:"auto_resolve,omitempty"`
}

// LLMSettings 剧本引擎所用的大模型（OpenAI 兼容接口，当前为 DeepSeek）。
type LLMSettings struct {
	APIKey         string  `json:"api_key"`
	BaseURL        string  `json:"base_url"`
	Model          string  `json:"model"`
	MaxTokens      int     `json:"max_tokens"`
	Temperature    float64 `json:"temperature"`
	RequestTimeout int     `json:"request_timeout"` // 秒
}

// RenderSettings 渲染后端（ComfyUI）与 H3 模型配置。
type RenderSettings struct {
	ComfyURL       string            `json:"comfy_url"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	FPS            int               `json:"fps"`
	Steps          int               `json:"steps"`
	TurboSteps     int               `json:"turbo_steps"`
	MinShotSeconds int               `json:"min_shot_seconds"`
	MaxShotSeconds int               `json:"max_shot_seconds"`
	Seed           int               `json:"seed"`
	UnetFL2VA      string            `json:"unet_fl2va"`
	UnetRef2VA     string            `json:"unet_ref2va"`
	Clip           string            `json:"clip"`
	VaeVideo       string            `json:"vae_video"`
	VaeAudio       string            `json:"vae_audio"`
	TurboLora      string            `json:"turbo_lora"`
	// TurboLoraR2V 角色镜(Ref2VA)专用 Turbo LoRA(lightx2v ref2v;空=R2V 沿用 turbo_lora)。
	// 2026-08-26 教训:该字段缺位时,启动 Load→Save 循环会把 settings.json 里手写的
	// turbo_lora_r2v 静默抹掉,角色镜永远吃不上专用 LoRA——强类型字段必须与管线键对齐。
	TurboLoraR2V   string            `json:"turbo_lora_r2v,omitempty"`
	// RefImageSize 参考图编码分辨率(match=缩到生成分辨率再编码,max=保留最高 2048
	// 短边,官方文档明示 max 身份保真更强;2026-09-04 画质升级默认 max)。
	RefImageSize   string            `json:"ref_image_size,omitempty"`
	ZImageUnet     string            `json:"z_image_unet"`
	ZImageClip     string            `json:"z_image_clip"`
	ZImageVae      string            `json:"z_image_vae"`
	CharModels     map[string]string `json:"char_models"`
	// LanAccess 是否允许局域网设备访问 ComfyUI(默认 false=仅本机 127.0.0.1)。
	// 审计 2026-08-28:ComfyUI 默认无鉴权,监听 0.0.0.0 时局域网任意设备可提交任务烧 GPU/
	// 读产物——安全默认收紧为仅本机,确需手机/平板访问时再在设置里开启。
	LanAccess bool `json:"lan_access,omitempty"`
}

// PathSettings 与 ComfyUI 共享的输入/输出目录 + 自包含部署目录。
// 新增的根目录字段(manju_root/novel_root/comfy_root/comfy_shared/novel_skill):
// 空 = 自动解析(exe 目录自包含子目录存在时用之,否则回退旧硬编码路径),显式填写则优先。
type PathSettings struct {
	ComfyInput  string `json:"comfy_input"`
	ComfyOutput string `json:"comfy_output"`
	ManjuRoot   string `json:"manju_root,omitempty"`
	NovelRoot   string `json:"novel_root,omitempty"`
	ComfyRoot   string `json:"comfy_root,omitempty"`
	ComfyShared string `json:"comfy_shared,omitempty"`
	NovelSkill  string `json:"novel_skill,omitempty"`
}

// Store 管理设置的加载、保存与主密钥。
type Store struct {
	Path    string
	keyPath string
	key     []byte
}

// NewStore 以 settings.json 的路径构造 Store。
func NewStore(path string) *Store {
	return &Store{Path: path, keyPath: filepath.Join(filepath.Dir(path), ".secret.key")}
}

// cleanupOldBackups 删除 30 天前的 .corrupt / .bad 备份(供排查的窗口期足够,之后纯占位)。
// 审计 2026-08-28:recoverKey/损坏自愈每次触发都留一份备份,长时间运行会累积。
func (s *Store) cleanupOldBackups() {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for _, suffix := range []string{".corrupt", ".bad"} {
		if st, err := os.Stat(s.Path + suffix); err == nil && st.ModTime().Before(cutoff) {
			_ = os.Remove(s.Path + suffix)
		}
	}
}

// Default 返回带合理默认值的设置。
func Default() *Settings {
	return &Settings{
		LLM: LLMSettings{
			BaseURL:        "https://api.deepseek.com",
			Model:          "deepseek-v4-flash",
			MaxTokens:      8192,
			Temperature:    0.4,
			RequestTimeout: 300,
		},
		Render: RenderSettings{
			ComfyURL:       "http://127.0.0.1:8190",
			Width:          768,
			Height:         1344,
			FPS:            24,
			Steps:          20,
			TurboSteps:     8,
			MinShotSeconds: 4,
			MaxShotSeconds: 12,
			Seed:           1688,
			UnetFL2VA:      "MiniMax_H3_fl2va_pruned_int8_convrot.safetensors",
			UnetRef2VA:     "MiniMax_H3_ref2va_pruned_int8_convrot.safetensors",
			Clip:           "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
			// 2026-09-04 画质升级:fp16 VAE + PDD 8step LoRA(官方最佳实践对齐,
			// turbo 蒸馏降音频/运动质量,4 步低于质量甜点;详见 manjuDefaultConfig 注释)
			VaeVideo:       "minimax_h3_video_vae_fp16.safetensors",
			VaeAudio:       "minimax_h3_audio_vae_fp32.safetensors",
			TurboLora:      "minimax_h3_fl2va_pdd_acc_8step_comfyui.safetensors",
			TurboLoraR2V:   "minimax_h3_ref2va_pdd_acc_8step_comfyui.safetensors",
			RefImageSize:   "max",
			ZImageUnet:     "z_image_turbo_bf16.safetensors",
			ZImageClip:     "qwen_3_4b.safetensors",
			ZImageVae:      "ae.safetensors",
			CharModels:     map[string]string{}, // SDXL/animagine 已 2026-08-26 清理(渲染观感差,定妆照统一 Krea-2/Z-Image)
		},
		Paths: PathSettings{
			// 输入/输出缺省跟随共享目录(自包含),不再内置 AppData 绝对路径——
			// Load 以 defaults 打底,留着旧绝对路径会在 settings.json 缺省时"复活",
			// 新机上直接指向不存在的目录
		},
	}
}

// Load 读取并解密设置；文件不存在时返回默认值。
func (s *Store) Load() (*Settings, error) {
	s.cleanupOldBackups() // 审计 2026-08-28:顺带清 30 天前的损坏备份,防 .corrupt/.bad 无限累积
	key, err := s.loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	s.key = key

	cfg := Default()
	if b, err := os.ReadFile(s.Path); err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			// 配置文件损坏自愈:备份坏文件(.bad),以默认配置继续——此前直接 return err,
			// main log.Fatalf 整个服务起不来;其余设置靠用户重填,坏文件保留供排查
			_ = os.WriteFile(s.Path+".bad", b, 0600)
			_ = os.Remove(s.Path)
			cfg = Default()
		}
	}
	// 解密失败(主密钥丢失/损坏/被清理工具删除):备份原文件、重建密钥、以空 key 启动,
	// 避免整个服务起不来;用户重填 Key 即恢复,其余设置保留,原文件在 settings.json.bak
	if err := openAPIKey(key, &cfg.LLM); err != nil {
		return nil, s.recoverKey(cfg, err)
	}
	if cfg.Agent != nil {
		if err := openStr(key, &cfg.Agent.VisionAPIKey); err != nil {
			return nil, s.recoverKey(cfg, err)
		}
	}
	return cfg, nil
}

// recoverKey 主密钥失效自愈(审计 M4):先短暂重试一次——杀软锁文件/IO 抖动这类瞬时读错误
// 会误判"密钥损坏",此前直接删 settings.json+.secret.key 重建,用户配置静默清零。
// 确认真损坏后:坏密文保留为 .corrupt(不删除,供排查)、重建密钥、以空 key 配置返回。
func (s *Store) recoverKey(cfg *Settings, cause error) error {
	// 瞬时错误重试(200ms 后重新加载 key 并尝试解密)
	time.Sleep(200 * time.Millisecond)
	if k2, err := s.loadOrCreateKey(); err == nil {
		c2 := *cfg
		if e1 := openAPIKey(k2, &c2.LLM); e1 == nil {
			if c2.Agent == nil {
				s.key = k2
				*cfg = c2
				return nil
			}
			if e2 := openStr(k2, &c2.Agent.VisionAPIKey); e2 == nil {
				s.key = k2
				*cfg = c2
				return nil
			}
		}
	}
	if rb, rerr := os.ReadFile(s.Path); rerr == nil {
		_ = os.WriteFile(s.Path+".corrupt", rb, 0600)
		// 审计 2026-08-28:settings.json 本体不再删除——非密文设置(路径/模型/渲染参数)
		// 全是明文 JSON,此前整文件删除把用户全部配置清零;改为仅清掉损坏的密文 key 字段,
		// 其余配置原样保留(sealStr 对 enc: 前缀跳过,坏密文不主动清会永久残留、每次启动重复恢复)
		var doc map[string]any
		if json.Unmarshal(rb, &doc) == nil {
			if llm, ok := doc["llm"].(map[string]any); ok {
				llm["api_key"] = ""
			}
			if ag, ok := doc["agent"].(map[string]any); ok {
				ag["vision_api_key"] = ""
			}
			if nb, merr := json.MarshalIndent(doc, "", "  "); merr == nil {
				_ = os.WriteFile(s.Path, nb, 0600)
			}
		}
	}
	_ = os.Remove(s.keyPath)
	k, kerr := s.loadOrCreateKey()
	if kerr != nil {
		return fmt.Errorf("重建加密密钥失败: %w", kerr)
	}
	s.key = k
	cfg.LLM.APIKey = ""
	if cfg.Agent != nil {
		cfg.Agent.VisionAPIKey = ""
	}
	_ = cause
	return nil
}

// Save 加密 API key 后写回文件。
func (s *Store) Save(cfg *Settings) error {
	key := s.key
	if key == nil {
		k, err := s.loadOrCreateKey()
		if err != nil {
			return err
		}
		key = k
	}
	cp := *cfg
	if err := sealAPIKey(key, &cp.LLM); err != nil {
		return err
	}
	if cp.Agent != nil {
		// 深拷贝:加密写的是副本,调用方持有的 Agent 保持明文(浅拷贝会连带改坏调用方数据)
		ag := *cfg.Agent
		cp.Agent = &ag
		if err := sealStr(key, &cp.Agent.VisionAPIKey); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	// 原子写:同目录临时文件 + rename,防保存途中崩溃留下半写配置(损坏配置会让服务起不来)
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// loadOrCreateKey 读取或生成 32 字节主密钥。
func (s *Store) loadOrCreateKey() ([]byte, error) {
	if b, err := os.ReadFile(s.keyPath); err == nil && len(b) == 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.keyPath, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// sealStr 通用加密单个字符串字段(空串/已加密跳过),LLM Key 与视觉模型 Key 共用
func sealStr(key []byte, s *string) error {
	if *s == "" || strings.HasPrefix(*s, encPrefix) {
		return nil
	}
	ct, err := encrypt(key, []byte(*s))
	if err != nil {
		return err
	}
	*s = encPrefix + ct
	return nil
}

// openStr 通用解密单个字符串字段(明文旧格式保持原样,下次保存时迁移加密)
func openStr(key []byte, s *string) error {
	if !strings.HasPrefix(*s, encPrefix) {
		return nil
	}
	pt, err := decrypt(key, (*s)[len(encPrefix):])
	if err != nil {
		return err
	}
	*s = pt
	return nil
}

func sealAPIKey(key []byte, llm *LLMSettings) error {
	return sealStr(key, &llm.APIKey)
}

func openAPIKey(key []byte, llm *LLMSettings) error {
	return openStr(key, &llm.APIKey)
}

func encrypt(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decrypt(key []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文过短")
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
