package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManjuValidate2K 云端重生成预校验规则(starter CLI 同款,付费前 fail-loud)
func TestManjuValidate2K(t *testing.T) {
	good := &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 107, HasAudio: true, SizeBytes: 8 << 20}
	if bad := manjuValidate2K(good); len(bad) > 0 {
		t.Errorf("768×1344/24fps/107帧应通过: %v", bad)
	}
	good2 := &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 362, HasAudio: true, SizeBytes: 49 << 20}
	if bad := manjuValidate2K(good2); len(bad) > 0 {
		t.Errorf("362 帧上限应通过: %v", bad)
	}
	cases := []struct {
		name string
		info *manjuProbeInfo
		want string
	}{
		{"非32整除", &manjuProbeInfo{Width: 1080, Height: 1920, FPS: 24, Frames: 107, HasAudio: true}, "32"},
		{"面积超限", &manjuProbeInfo{Width: 1024, Height: 1824, FPS: 24, Frames: 107, HasAudio: true}, "面积"},
		{"帧率非24", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 23.976, Frames: 107, HasAudio: true}, "24fps"},
		{"帧数低于网格", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 96, HasAudio: true}, "网格"},
		{"帧数不在17网格", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 120, HasAudio: true}, "网格"},
		{"帧数超上限", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 379, HasAudio: true}, "网格"},
		{"无音轨", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 107, HasAudio: false}, "音轨"},
		{"超50MB", &manjuProbeInfo{Width: 768, Height: 1344, FPS: 24, Frames: 107, HasAudio: true, SizeBytes: 51 << 20}, "50MB"},
	}
	for _, c := range cases {
		bad := manjuValidate2K(c.info)
		if len(bad) == 0 || !strings.Contains(strings.Join(bad, ";"), c.want) {
			t.Errorf("%s: 应报「%s」,得到 %v", c.name, c.want, bad)
		}
	}
}

// TestManjuUpscaleSubmitPoll 提交+轮询(mock MiniMax v2 API):payload 结构、状态流转、下载
func TestManjuUpscaleSubmitPoll(t *testing.T) {
	// 准备源视频(内容无所谓,校验 base64 提交体非空)
	src := filepath.Join(t.TempDir(), "03.mp4")
	_ = os.WriteFile(src, []byte("fake-mp4"), 0644)

	var gotPayload map[string]any
	submitted := false
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("2k-video-bytes"))
	}))
	defer dlSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/video_regeneration"):
			_ = json.NewDecoder(r.Body).Decode(&gotPayload)
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("Bearer 鉴权异常: %s", got)
			}
			submitted = true
			_, _ = w.Write([]byte(`{"task_id":"task-2k-001"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/query/video_generation/"):
			if !submitted {
				t.Errorf("先查询后提交?!")
			}
			_, _ = w.Write([]byte(`{"task": {"status": "succeeded", "content": {"url": "` + dlSrv.URL + `/file.mp4"}}}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	taskID, err := manjuUpscaleSubmit(client, srv.URL, "test-key", "prompt-text", src, "2K")
	if err != nil || taskID != "task-2k-001" {
		t.Fatalf("提交失败: %v %s", err, taskID)
	}
	// payload 结构:model/resolution/content[text+base_video data URI]
	if gotPayload["model"] != "MiniMax-H3" || gotPayload["resolution"] != "2K" {
		t.Errorf("payload 模型/分辨率异常: %v", gotPayload)
	}
	content, _ := gotPayload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content 应为 text+base_video 两项: %v", content)
	}
	textItem, _ := content[0].(map[string]any)
	vidItem, _ := content[1].(map[string]any)
	if textItem["type"] != "text" || textItem["text"] != "prompt-text" {
		t.Errorf("text 项异常: %v", textItem)
	}
	if vidItem["role"] != "base_video" {
		t.Errorf("base_video role 缺失: %v", vidItem)
	}
	vu, _ := vidItem["video_url"].(map[string]any)
	if !strings.HasPrefix(vu["url"].(string), "data:video/mp4;base64,") {
		t.Errorf("video_url 非 data URI: %v", vu)
	}

	url, err := manjuUpscalePoll(client, srv.URL, "test-key", taskID, 5*time.Second, nil)
	if err != nil || !strings.HasPrefix(url, "https://") && !strings.Contains(url, "127.0.0.1") {
		// httptest 地址是 http://127.0.0.1,正式环境强制 https;此处校验能拿到地址即可
		if err != nil {
			t.Fatalf("轮询失败: %v", err)
		}
	}
	dst := filepath.Join(t.TempDir(), "2k", "03.mp4")
	if err := manjuUpscaleDownload(client, url, dst); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "2k-video-bytes" {
		t.Errorf("下载内容异常: %q", string(b))
	}
	// 失败状态 → 错误信息透出
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"task": {"status": "failed", "error": {"message": "content violation"}}}`))
	}))
	defer failSrv.Close()
	if _, err := manjuUpscalePoll(client, failSrv.URL, "k", "t", time.Second, nil); err == nil || !strings.Contains(err.Error(), "content violation") {
		t.Errorf("failed 状态应透出错误: %v", err)
	}
}

// TestManjuUpscaleRouteKeyMissing 未配置 Key:后台任务立即失败并给出指引
func TestManjuUpscaleRouteKeyMissing(t *testing.T) {
	proj := "zz_upscale_key_test"
	dir := filepath.Join(ManjuRootDir, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"render":{"minimax_api_key":""}}`), 0644)
	t.Setenv("MINIMAX_API_KEY", "")
	// settings.json 兜底也清掉(隔离)
	orig, _ := os.ReadFile(manjuSettingsFile)
	defer func() {
		if orig != nil {
			_ = os.WriteFile(manjuSettingsFile, orig, 0644)
		} else {
			_ = os.Remove(manjuSettingsFile)
		}
	}()
	_ = os.Remove(manjuSettingsFile)

	w, _ := doReq(t, "POST", "/api/manju/upscale2k", map[string]any{"config": cfgPath, "episode": "EP01"})
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	// 后台任务很快失败:等状态落定
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ds := loadManjuDiskState(proj); ds != nil && ds.Done && ds.RC != nil && *ds.RC != 0 {
			if !strings.Contains(ds.LogTail, "MiniMax API Key") {
				t.Errorf("应提示未配置 Key: %s", ds.LogTail)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("后台任务未按预期失败")
}
