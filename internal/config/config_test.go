package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreEncryptRoundtrip 加密往返:Save 后 Key 带 enc: 前缀,Load 解密回明文
func TestStoreEncryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "settings.json"))
	cfg := Default()
	cfg.LLM.APIKey = "sk-secret-123"
	cfg.Agent = &AgentSettings{VisionAPIKey: "vis-secret-456"}
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	// 落盘:Key 加密(带 enc:),不出现明文
	raw, _ := os.ReadFile(store.Path)
	if !strings.Contains(string(raw), "enc:") {
		t.Fatalf("Key 未加密落盘: %s", raw)
	}
	if strings.Contains(string(raw), "sk-secret-123") || strings.Contains(string(raw), "vis-secret-456") {
		t.Fatalf("明文 Key 泄露落盘")
	}
	// 回读解密
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.APIKey != "sk-secret-123" || got.Agent.VisionAPIKey != "vis-secret-456" {
		t.Fatalf("解密回读异常: %q %q", got.LLM.APIKey, got.Agent.VisionAPIKey)
	}
}

// TestStoreCorruptSelfHeal 配置损坏自愈:坏 JSON → 备份 .bad + 默认配置继续(服务不挂)
func TestStoreCorruptSelfHeal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(p, []byte(`{"llm": {bad json`), 0600)
	store := NewStore(p)
	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("损坏配置应自愈不报错: %v", err)
	}
	if cfg.LLM.BaseURL == "" {
		t.Fatalf("自愈后应回到默认配置")
	}
	// 坏文件已备份
	if _, e := os.Stat(p + ".bad"); e != nil {
		t.Fatalf("坏文件未备份: %v", e)
	}
}

// TestStoreSaveAtomic 原子写:Save 后无 .tmp 残留
func TestStoreSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "settings.json"))
	if err := store.Save(Default()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}
}
