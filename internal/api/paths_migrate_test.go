package api

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-03 资产统一目录迁移:旧分立 char_lib/voice_lib → asset_lib/{characters,voices}
func TestMigrateAssetLibs(t *testing.T) {
	dir := t.TempDir()
	// 旧结构:char_lib/<角色>/card.json + voice_lib/audio/x.mp3
	oldChar := filepath.Join(dir, "char_lib", "季一星")
	_ = os.MkdirAll(oldChar, 0755)
	_ = os.WriteFile(filepath.Join(oldChar, "card.json"), []byte("{}"), 0644)
	oldVoice := filepath.Join(dir, "voice_lib", "audio")
	_ = os.MkdirAll(oldVoice, 0755)
	_ = os.WriteFile(filepath.Join(oldVoice, "a.mp3"), []byte("x"), 0644)
	migrateAssetLibs(dir)
	// 迁移后:内容在新位置,旧目录移除
	if !fileExists(filepath.Join(dir, "asset_lib", "characters", "季一星", "card.json")) {
		t.Fatal("角色库应迁入 asset_lib/characters")
	}
	if !fileExists(filepath.Join(dir, "asset_lib", "voices", "audio", "a.mp3")) {
		t.Fatal("音色库应迁入 asset_lib/voices")
	}
	if dirExists(filepath.Join(dir, "char_lib")) || dirExists(filepath.Join(dir, "voice_lib")) {
		t.Fatal("移空的旧目录应移除")
	}
	// 幂等 + 不覆盖:预置目标同名项,重跑迁移旧内容不覆盖
	_ = os.MkdirAll(filepath.Join(dir, "char_lib", "季一星"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "char_lib", "季一星", "card.json"), []byte(`{"new":1}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "asset_lib", "characters", "季一星", "card.json"), []byte(`{"old":1}`), 0644)
	migrateAssetLibs(dir)
	b, _ := os.ReadFile(filepath.Join(dir, "asset_lib", "characters", "季一星", "card.json"))
	if string(b) != `{"old":1}` {
		t.Fatalf("目标已存在不得覆盖: %s", b)
	}
}
