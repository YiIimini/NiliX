package manju

import (
	"nilix/internal/paths"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestManjuLLMSettings 验证「LLM 服务预设 + 自定义」:存默认(合并保留 agent 节)、应用到项目(写 llm 三字段)、默认服务回退
func TestManjuLLMSettings(t *testing.T) {
	// 隔离测试:备份原 settings.json,测试后恢复
	orig, _ := os.ReadFile(manjuSettingsFile)
	defer func() {
		if orig != nil {
			_ = os.WriteFile(manjuSettingsFile, orig, 0644)
		} else {
			_ = os.Remove(manjuSettingsFile)
		}
	}()

	proj := "zz_llm_settings_test"
	dir := filepath.Join(paths.ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"llm":{"api_key":"old"}}`), 0644)

	// 1. settings.json 预置全局 agent 节(模拟「另存为全局默认」写过的状态)
	_ = os.MkdirAll(filepath.Dir(manjuSettingsFile), 0755)
	_ = os.WriteFile(manjuSettingsFile, []byte(`{"agent":{"enabled":true,"vision_model":"glm-4.6v-flash"},"api_key":"default-old"}`), 0644)

	// 2. 存为默认(仅 Key):须合并保留 agent 节
	w, out := doReq(t, "POST", "/api/manju/settings", map[string]any{"apiKey": "sk-new-default"})
	if w.Code != 200 || !out["ok"].(bool) {
		t.Fatalf("存默认 HTTP %d %s", w.Code, w.Body.String())
	}
	var saved map[string]any
	b, _ := os.ReadFile(manjuSettingsFile)
	_ = json.Unmarshal(b, &saved)
	if saved["api_key"] != "sk-new-default" {
		t.Errorf("默认 key 未保存: %v", saved)
	}
	if _, ok := saved["agent"].(map[string]any); !ok {
		t.Fatalf("存默认覆盖写清掉了全局 agent 节: %s", string(b))
	}

	// 3. 应用到项目(自定义服务):写项目 llm 三字段,旧 key 被替换
	w2, out2 := doReq(t, "POST", "/api/manju/settings", map[string]any{
		"apiKey": "sk-proj", "baseUrl": "https://my.ollama.local/v1/", "model": "qwen2.5:14b",
		"config": cfgPath,
	})
	if w2.Code != 200 || !out2["ok"].(bool) {
		t.Fatalf("应用项目 HTTP %d %s", w2.Code, w2.Body.String())
	}
	cfg, err := readManjuConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	L, _ := cfg["llm"].(map[string]any)
	if L["api_key"] != "sk-proj" || L["base_url"] != "https://my.ollama.local/v1/" || L["model"] != "qwen2.5:14b" {
		t.Errorf("项目 llm 配置未正确写入: %v", L)
	}

	// 4. 默认服务回退:项目未配服务 → 用「存为默认」的地址/模型;全缺省 → DeepSeek
	llm := manjuLLMFromCfg(cfg)
	if llm.baseURL != "https://my.ollama.local/v1" || llm.model != "qwen2.5:14b" {
		t.Errorf("项目自定义服务未生效: %s / %s", llm.baseURL, llm.model)
	}
	_ = os.WriteFile(manjuSettingsFile, []byte(`{"api_key":"k","base_url":"https://api.deepseek.com/","model":"deepseek-chat"}`), 0644)
	llm2 := manjuLLMFromCfg(map[string]any{"llm": map[string]any{"api_key": "proj-key"}})
	if llm2.apiKey != "proj-key" || llm2.baseURL != "https://api.deepseek.com" || llm2.model != "deepseek-chat" {
		t.Errorf("默认服务回退异常: %v", llm2)
	}
	_ = os.WriteFile(manjuSettingsFile, []byte(`{"api_key":"k"}`), 0644)
	llm3 := manjuLLMFromCfg(map[string]any{})
	if llm3.baseURL != "https://api.deepseek.com" || llm3.model != "deepseek-chat" {
		t.Errorf("全缺省未回退 DeepSeek: %s / %s", llm3.baseURL, llm3.model)
	}

	// 5. GET settings:返回默认服务 + 项目服务,供弹窗回填
	_ = os.WriteFile(manjuSettingsFile, []byte(`{"api_key":"k","base_url":"https://api.deepseek.com/","model":"deepseek-chat"}`), 0644)
	w3, res := doReq(t, "GET", "/api/manju/settings?config="+filepath.ToSlash(cfgPath), nil)
	if w3.Code != 200 {
		t.Fatalf("GET settings HTTP %d", w3.Code)
	}
	if res["defaultBaseUrl"] != "https://api.deepseek.com/" || res["defaultModel"] != "deepseek-chat" {
		t.Errorf("默认服务未返回: %v", res)
	}
	if res["projectBaseUrl"] != "https://my.ollama.local/v1/" || res["projectModel"] != "qwen2.5:14b" {
		t.Errorf("项目服务未返回: %v", res)
	}
	if res["projectMasked"] != "sk-proj" { // 长度≤9 不掩码
		t.Errorf("项目 key 掩码异常: %v", res["projectMasked"])
	}

	// 6. 空表单提交:不报错,不写任何东西
	_, out4 := doReq(t, "POST", "/api/manju/settings", map[string]any{})
	if out4["ok"] != false {
		t.Errorf("空表单应返回 ok=false: %v", out4)
	}
}
