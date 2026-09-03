package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-03 资产库汇总索引:重建/删除同步/惰性生成三向
func TestCharLibIndexLifecycle(t *testing.T) {
	dir := t.TempDir()
	old := ManjuRootDir
	ManjuRootDir = dir
	CharLibDir = filepath.Join(dir, "asset_lib", "characters")
	defer func() { ManjuRootDir = old; CharLibDir = "" }()

	// 造两个角色目录
	for _, name := range []string{"季一星", "阿影"} {
		d := filepath.Join(CharLibDir, name)
		_ = os.MkdirAll(d, 0o755)
		card := manjuCharLibCard{Card: map[string]any{
			"id": name, "gender": "男", "species": "人",
			"image_prompt": "a young man with black hair", "q_form": "chibi",
		}, Fingerprint: "abcd1234efgh", CreatedAt: 1693700000, SourceProject: "测试书"}
		b, _ := json.MarshalIndent(card, "", "  ")
		_ = os.WriteFile(filepath.Join(d, "card.json"), b, 0o644)
		_ = os.WriteFile(filepath.Join(d, name+".png"), []byte("x"), 0o644)
	}
	if err := rebuildCharLibIndex(); err != nil {
		t.Fatal(err)
	}
	// 索引内容:两角色,七字段+资产清单
	b, err := os.ReadFile(filepath.Join(CharLibDir, "index.json"))
	if err != nil {
		t.Fatal("index.json 应生成")
	}
	var idx struct {
		Count      int `json:"count"`
		Characters []struct {
			Name        string   `json:"name"`
			ImagePrompt string   `json:"image_prompt"`
			Species     string   `json:"species"`
			Assets      []string `json:"assets"`
		} `json:"characters"`
	}
	if json.Unmarshal(b, &idx) != nil || idx.Count != 2 {
		t.Fatalf("索引应为 2 角色: %s", b[:200])
	}
	found := false
	for _, c := range idx.Characters {
		if c.Name == "季一星" && c.ImagePrompt == "a young man with black hair" && len(c.Assets) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("索引应含七字段与资产清单")
	}
	// 删除同步:删一个角色后索引只剩 1
	if err := manjuCharLibDelete("阿影"); err != nil {
		t.Fatal(err)
	}
	if got := len(charLibIndexEntries()); got != 1 {
		t.Fatalf("删除后索引应剩 1, got %d", got)
	}
	// 惰性重建:删掉 index.json,读侧自动重建
	_ = os.Remove(filepath.Join(CharLibDir, "index.json"))
	if got := len(manjuCharLibList()); got != 1 {
		t.Fatalf("索引缺失应惰性重建, got %d", got)
	}
}

func TestVoiceLibIndexRebuild(t *testing.T) {
	dir := t.TempDir()
	old := VoiceLibDir
	VoiceLibDir = filepath.Join(dir, "voices")
	defer func() { VoiceLibDir = old }()
	_ = os.MkdirAll(filepath.Join(VoiceLibDir, "audio"), 0o755)
	_ = os.WriteFile(filepath.Join(VoiceLibDir, "audio", "male_sun.mp3"), []byte("x"), 0o644)
	if err := rebuildVoiceLibIndex(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(VoiceLibDir, "index.json"))
	if err != nil {
		t.Fatal("voices/index.json 应生成")
	}
	var idx struct {
		Count  int `json:"count"`
		Voices []struct {
			Key  string `json:"key"`
			File string `json:"file"`
		} `json:"voices"`
	}
	if json.Unmarshal(b, &idx) != nil || idx.Count != 1 || idx.Voices[0].Key != "male_sun" {
		t.Fatalf("音色索引不符: %s", b[:200])
	}
}
