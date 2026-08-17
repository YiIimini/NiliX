// Package config 负责服务的全局设置：结构定义、JSON 持久化与 API key 加密存储。
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const encPrefix = "enc:"

// Settings 是服务全局设置，结构与既有的漫剧 config.json 对齐。
type Settings struct {
	LLM    LLMSettings    `json:"llm"`
	Render RenderSettings `json:"render"`
	Paths  PathSettings   `json:"paths"`
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
	ZImageUnet     string            `json:"z_image_unet"`
	ZImageClip     string            `json:"z_image_clip"`
	ZImageVae      string            `json:"z_image_vae"`
	CharModels     map[string]string `json:"char_models"`
}

// PathSettings 与 ComfyUI 共享的输入/输出目录。
type PathSettings struct {
	ComfyInput  string `json:"comfy_input"`
	ComfyOutput string `json:"comfy_output"`
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
			VaeVideo:       "minimax_h3_video_vae_fp16.safetensors",
			VaeAudio:       "minimax_h3_audio_vae_fp32.safetensors",
			TurboLora:      "minimax_h3_turbo_4step_ema.safetensors",
			ZImageUnet:     "z_image_turbo_bf16.safetensors",
			ZImageClip:     "qwen_3_4b.safetensors",
			ZImageVae:      "ae.safetensors",
			CharModels:     map[string]string{"女": "animagine-xl-3.1.safetensors", "男": "sd_xl_base_1.0.safetensors"},
		},
		Paths: PathSettings{
			ComfyInput:  `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared\input`,
			ComfyOutput: `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared\output`,
		},
	}
}

// Load 读取并解密设置；文件不存在时返回默认值。
func (s *Store) Load() (*Settings, error) {
	key, err := s.loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	s.key = key

	cfg := Default()
	if b, err := os.ReadFile(s.Path); err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	}
	if err := openAPIKey(key, &cfg.LLM); err != nil {
		return nil, err
	}
	return cfg, nil
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
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, b, 0600)
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

func sealAPIKey(key []byte, llm *LLMSettings) error {
	if llm.APIKey == "" || strings.HasPrefix(llm.APIKey, encPrefix) {
		return nil
	}
	ct, err := encrypt(key, []byte(llm.APIKey))
	if err != nil {
		return err
	}
	llm.APIKey = encPrefix + ct
	return nil
}

func openAPIKey(key []byte, llm *LLMSettings) error {
	if !strings.HasPrefix(llm.APIKey, encPrefix) {
		return nil // 明文（旧格式），保持原样，下次保存时迁移加密
	}
	pt, err := decrypt(key, llm.APIKey[len(encPrefix):])
	if err != nil {
		return errors.New("解密 API key 失败：主密钥可能已变更")
	}
	llm.APIKey = pt
	return nil
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
