package manju
import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 角色一致性纪律:逐镜提示词禁止未登场角色入画、说话人必须视觉中心、Subject 只能引用有参考图的角色。
func TestShotPromptRoleDiscipline(t *testing.T) {
	sys := manjuShotPromptSystem(true, "2.5d+real+ink")
	for _, want := range []string{"角色纪律", "主角中心", "ref_available"} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuShotPromptSystem 缺少纪律 %q", want)
		}
	}
	if !strings.Contains(sys, "in <Picture 1>") || !strings.Contains(sys, "清单外的登场角色") {
		t.Errorf("Ref2VA 模板缺少参考图纪律")
	}
}

func TestDirectSystemRoleDiscipline(t *testing.T) {
	sys := manjuDirectSystem(map[string]any{}, "2.5d+real")
	for _, want := range []string{"分镜纪律", "必须列全", "视觉中心主体"} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuDirectSystem 缺少分镜纪律 %q", want)
		}
	}
}

// shotRefRoles:只返回有参考图(正脸优先,回退全身)的角色,≤3 与 charRefNames 同限。
func TestShotRefRoles(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(assets, 0755)
	// 陈鱼 有正脸;柳如烟 只有全身;葛长老 无资产;守山弟子 排第 4 被上限截断
	_ = os.WriteFile(filepath.Join(assets, "陈鱼_face.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "柳如烟.png"), []byte("f"), 0644)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets")}
	s := manjuShot{Characters: []string{"陈鱼", "柳如烟", "葛长老", "守山弟子"}}
	got := ctx.shotRefRoles(s)
	want := []string{"陈鱼", "柳如烟"}
	if len(got) != len(want) {
		t.Fatalf("shotRefRoles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shotRefRoles = %v, want %v", got, want)
		}
	}
}

// TestShotPromptKeepsUserConfig 普通执行管线/一条龙(用户规则 2026-08):逐镜 H3
// 提示词**不**注入总集的风格/负面——渲染风格/负面一律用用户配置(config.style /
// render.neg_prompt)。仅 AI 一条龙分析时读总集并写回配置。
func TestShotPromptKeepsUserConfig(t *testing.T) {
	dir := t.TempDir()
	// 小说目录 + 总集(风格/负面/角色)
	_ = os.MkdirAll(filepath.Join(dir, "素材"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "全本"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "素材", "渲染提示词总集.md"), []byte(
		"## 一、漫剧渲染风格提示词\n```\nCinematic film still, photorealistic, xianxia, cinematic lighting, 8k\n```\n\n"+
			"## 二、全局负面提示词\n```\nanime, cartoon, watermark, text, blurry\n```\n\n"+
			"## 三、角色提示词\n### 3.1 陈鱼\n```\nChenYu master prompt\n```\n"), 0644)
	novelPath := filepath.Join(dir, "全本", "书.md")
	_ = os.WriteFile(novelPath, []byte("第一章 内容足够长"), 0644)

	// mock LLM:捕获系统提示词,返回合法 h3_prompt
	var gotSys string
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				gotSys = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"h3_prompt\":\"Cinematic film still. [Shot 1] scene.\"}"},"finish_reason":"stop"}]}`))
	}))
	defer llmSrv.Close()

	ctx := &manjuCtx{
		novel:       novelPath,
		style:       "2.5d",
		analysisDir: filepath.Join(dir, "analysis"),
		llm: &manjuLLM{
			apiKey:  "sk-test",
			baseURL: strings.TrimSuffix(llmSrv.URL, "/"),
			model:   "deepseek-chat",
			client:  &http.Client{Timeout: 10 * time.Second},
		},
	}
	_ = os.MkdirAll(ctx.analysisDir, 0755)
	hp, err := ctx.genShotPromptRaw(manjuShot{ID: 1, Characters: []string{"陈鱼"}}, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("genShotPromptRaw: %v", err)
	}
	if hp == "" {
		t.Fatalf("h3_prompt 为空")
	}
	// 普通管线:系统提示词**不得**含总集注入段(风格/负面用用户配置,不自动解析总集)
	// 注:内置默认负面(manjuNegPrompt)含 anime 等词,此处只查总集特有的段落标记
	for _, forbid := range []string{"全局渲染风格提示词", "全局负面提示词(总集", "渲染提示词总集", "总集·强制"} {
		if strings.Contains(gotSys, forbid) {
			t.Errorf("普通管线不应注入总集内容,系统提示词含 %q", forbid)
		}
	}
	// 总集风格提示词不应整体进入
	if strings.Contains(gotSys, "Cinematic film still, photorealistic, xianxia") {
		t.Errorf("普通管线不应注入总集风格提示词")
	}
	// 用户配置的风格应体现在系统提示词(2.5d 风格措辞)
	if !strings.Contains(gotSys, "2.5D") && !strings.Contains(gotSys, "2.5d") {
		t.Errorf("系统提示词缺用户配置风格(2.5d): %q", gotSys[:min(200, len(gotSys))])
	}
}
