package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"nilix/internal/config"
)

// 回归:灵动岛配置接口——系统设置弹窗开关读/写 settings.json island.enabled,
// 灵动岛轮询 GET 决定自身显隐(禁用后隐藏悬浮胶囊)
func TestIslandConfig(t *testing.T) {
	// 用临时 settings 存储
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "settings.json"))
	old := manjuSettingsStore
	manjuSettingsStore = store
	defer func() { manjuSettingsStore = old }()

	s := &Server{}
	// 默认启用(nil → true)
	req := httptest.NewRequest("GET", "/api/island", nil)
	w := httptest.NewRecorder()
	s.handleIslandGet(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["enabled"] != true {
		t.Fatalf("默认应启用, got %v", out)
	}

	// 写禁用
	body, _ := json.Marshal(map[string]bool{"enabled": false})
	req2 := httptest.NewRequest("POST", "/api/island", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	s.handleIslandPost(w2, req2)
	_ = json.Unmarshal(w2.Body.Bytes(), &out)
	if out["ok"] != true {
		t.Fatalf("写禁用失败: %v", w2.Body.String())
	}

	// 读回禁用
	req3 := httptest.NewRequest("GET", "/api/island", nil)
	w3 := httptest.NewRecorder()
	s.handleIslandGet(w3, req3)
	_ = json.Unmarshal(w3.Body.Bytes(), &out)
	if out["enabled"] != false {
		t.Fatalf("读回应为禁用, got %v", out)
	}
}
