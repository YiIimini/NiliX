package comfy

import "testing"

// 安装资源清单完整性:必须覆盖 portable/tool/node/model 四类,模型有 HF 相对路径。
func TestComfyInstallResources(t *testing.T) {
	has := map[string]bool{}
	for _, r := range comfyDefaultResources {
		has[r.Kind] = true
		if r.Name == "" || r.Rel == "" || r.URL == "" {
			t.Errorf("资源缺字段: %+v", r)
		}
	}
	for _, k := range []string{"portable", "tool", "node", "model"} {
		if !has[k] {
			t.Errorf("清单缺少 %s 类资源", k)
		}
	}
	// portable 与 tool 必须是完整 URL,model 是 HF 相对路径
	for _, r := range comfyDefaultResources {
		if r.Kind == "model" && (len(r.URL) < 10 || !containsStr(r.URL, "resolve/main/")) {
			t.Errorf("模型 URL 应为 HF 相对路径: %+v", r)
		}
		if r.Kind != "model" && !containsStr(r.URL, "http") {
			t.Errorf("非模型资源应为完整 URL: %+v", r)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
