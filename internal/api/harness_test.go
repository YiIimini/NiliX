package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"nilix/internal/sysmon"
)

// 回归:Harness 服务探测接口——路由存在且返回结构完整(探测独立于 ComfyUI,
// 灵动岛 HUD 底部监控 + 应用内嵌 Harness 窗口共用)
func TestHarnessRoute(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/api/harness", nil)
	w := httptest.NewRecorder()
	s.handleHarness(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var st HarnessStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, w.Body.String())
	}
	if st.URL != "http://127.0.0.1:3080" {
		t.Fatalf("URL = %q", st.URL)
	}
	if st.Startup.Port != "3080" {
		t.Fatalf("Startup.Port = %q", st.Startup.Port)
	}
	if st.Startup.Node == "" || st.Startup.Binary == "" {
		t.Fatalf("Startup 信息不完整: %+v", st.Startup)
	}
	// 本机 Harness 可能在运行也可能不在——不强制 online,但探测不 panic
	t.Logf("harness online=%v err=%q", st.Online, st.Err)
}

// 回归:sysmon 快照含 harness 字段(灵动岛轮询 /api/stats 用)
func TestSysmonSnapshotHasHarness(t *testing.T) {
	col := sysmon.NewCollector()
	s := &Server{sysmon: col}
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	s.handleStats(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if _, ok := body["harness"]; !ok {
		t.Fatalf("stats 响应缺少 harness 字段: %s", w.Body.String())
	}
}
