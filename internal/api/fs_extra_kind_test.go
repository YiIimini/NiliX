package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-08-23 用户规则:创作输出(分镜脚本/提示词)在小说详情弹窗需独立一栏。
// 分镜脚本文件名常含"第N章"(如 第001章_一碗馊面_分镜脚本.md),必须:
//  1. 不被 classifyTextFile 误判为正文章节
//  2. extraKind 打标 storyboard / prompt
func TestExtraKindClassification(t *testing.T) {
	cases := []struct {
		relDir, name, want string
	}{
		{"素材/分镜脚本", "第001章_一碗馊面_分镜脚本.md", "storyboard"},
		{"素材/分镜脚本", "第012章_卷末_分镜脚本.md", "storyboard"},
		{"素材", "人物生成提示词.md", "prompt"},
		{"素材", "场景提示词.md", "prompt"},
		{"封面", "封面提示词.md", "prompt"},
		{"素材", "渲染提示词总集.md", "prompt"},
		{"设定集", "设定集与大纲.md", ""},
		{"设定集", "创作指令卡-AI版本.md", ""},
		{"正文/卷一_蒙冤", "第001章_一碗馊面.md", ""}, // 纯正文文件名不会被打 kind
	}
	for _, c := range cases {
		if got := extraKind(c.relDir, c.name); got != c.want {
			t.Errorf("extraKind(%q,%q) = %q, want %q", c.relDir, c.name, got, c.want)
		}
	}
}

// 分镜脚本文件名含"第N章"也不得被 classifyTextFile 判为正文
func TestClassifyTextFileSkipsStoryboard(t *testing.T) {
	cases := []struct {
		relDir, name string
		wantCh       bool
	}{
		{"素材/分镜脚本", "第001章_一碗馊面_分镜脚本.md", false},
		{"素材/分镜脚本", "第003章_第一碗面_分镜脚本.md", false},
		{"正文/卷一_蒙冤", "第001章_一碗馊面.md", true},
		{"素材", "人物生成提示词.md", false},
	}
	for _, c := range cases {
		if isCh, _ := classifyTextFile(c.relDir, c.name); isCh != c.wantCh {
			t.Errorf("classifyTextFile(%q,%q) isCh = %v, want %v", c.relDir, c.name, isCh, c.wantCh)
		}
	}
}

// 端到端:analyzeDir 扫描一个模拟小说目录,分镜脚本进 Extras(storyboard) 不进 Chapters
func TestAnalyzeDirStoryboardSeparated(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "测试书")
	mustMkdir(t, filepath.Join(book, "正文", "卷一_蒙冤"))
	mustMkdir(t, filepath.Join(book, "素材", "分镜脚本"))
	mustMkdir(t, filepath.Join(book, "设定集"))
	mustWrite(t, filepath.Join(book, "正文", "卷一_蒙冤", "第001章_一碗馊面.md"), "第一章正文内容")
	mustWrite(t, filepath.Join(book, "正文", "卷一_蒙冤", "第002章_阿财开口.md"), "第二章正文内容")
	mustWrite(t, filepath.Join(book, "素材", "分镜脚本", "第001章_一碗馊面_分镜脚本.md"), "## 分镜\n- 镜头1")
	mustWrite(t, filepath.Join(book, "素材", "分镜脚本", "第002章_阿财开口_分镜脚本.md"), "## 分镜\n- 镜头1")
	mustWrite(t, filepath.Join(book, "素材", "人物生成提示词.md"), "人物:xxx")
	mustWrite(t, filepath.Join(book, "设定集", "设定集与大纲.md"), "大纲")

	res := analyzeDir(dir)
	projs, ok := res["projects"].([]dirProject)
	if !ok || len(projs) != 1 {
		t.Fatalf("projects = %#v, want 1", res["projects"])
	}
	p := projs[0]
	if len(p.Chapters) != 2 {
		t.Fatalf("Chapters = %d, want 2 (分镜脚本不得混入正文): %#v", len(p.Chapters), p.Chapters)
	}
	var sb, prompt, other int
	for _, e := range p.Extras {
		switch e.Kind {
		case "storyboard":
			sb++
		case "prompt":
			prompt++
		default:
			other++
		}
	}
	if sb != 2 {
		t.Errorf("Extras storyboard = %d, want 2", sb)
	}
	if prompt != 1 {
		t.Errorf("Extras prompt = %d, want 1", prompt)
	}
	if other != 1 {
		t.Errorf("Extras other = %d, want 1 (设定集与大纲)", other)
	}
	for _, e := range p.Extras {
		if e.Kind == "storyboard" && !strings.Contains(strings.ToLower(e.Name), "分镜") {
			t.Errorf("storyboard 文件命名异常: %s", e.Name)
		}
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// 分镜脚本列表按章节号增序(2026-08-26 用户反馈:详情页右侧栏「第10章」被字典序
// 排在「第1章」「第2章」之间)。sortExtras 后 storyboard 组内必须 1→2→10。
func TestSortExtrasChapterAsc(t *testing.T) {
	exs := []dirFile{
		{Name: "第2章_阿财开口_分镜脚本.md", Kind: "storyboard"},
		{Name: "第10章_卷末_分镜脚本.md", Kind: "storyboard"},
		{Name: "第1章_一碗馊面_分镜脚本.md", Kind: "storyboard"},
		{Name: "人物生成提示词.md", Kind: "prompt"},
		{Name: "第1章_分镜脚本草稿.md", Kind: "storyboard"},
	}
	sortExtras(exs)
	var sbNames []string
	for _, e := range exs {
		if e.Kind == "storyboard" {
			sbNames = append(sbNames, e.Name)
		}
	}
	want := []string{"第1章_一碗馊面_分镜脚本.md", "第1章_分镜脚本草稿.md", "第2章_阿财开口_分镜脚本.md", "第10章_卷末_分镜脚本.md"}
	if strings.Join(sbNames, "|") != strings.Join(want, "|") {
		t.Fatalf("storyboard 组内应按章号增序:\n got %v\nwant %v", sbNames, want)
	}
}

// 端到端:analyzeDir 输出的 Extras 中 storyboard 保持章号增序(10+ 章不乱)
func TestAnalyzeDirStoryboardOrder(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "排序书")
	sbDir := filepath.Join(book, "素材", "分镜脚本")
	mustMkdir(t, sbDir)
	// os.ReadDir 按字典序返回:第10章 会排在 第2章 前——修复后必须被纠正
	for _, n := range []string{"第1章_启程_分镜脚本.md", "第2章_遇袭_分镜脚本.md", "第10章_终局_分镜脚本.md"} {
		mustWrite(t, filepath.Join(sbDir, n), "## 分镜")
	}
	res := analyzeDir(dir)
	projs, _ := res["projects"].([]dirProject)
	if len(projs) != 1 {
		t.Fatalf("projects = %d, want 1", len(projs))
	}
	var got []string
	for _, e := range projs[0].Extras {
		if e.Kind == "storyboard" {
			got = append(got, e.Name)
		}
	}
	wantOrder := []string{"第1章_启程_分镜脚本.md", "第2章_遇袭_分镜脚本.md", "第10章_终局_分镜脚本.md"}
	if strings.Join(got, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("分镜脚本应按章号增序:\n got %v\nwant %v", got, wantOrder)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 2026-09-03 用户要求:详情弹窗显示分镜脚本列表——技能侧分镜是 .json
// (素材/分镜脚本/第NNN章_标题_分镜脚本.json),必须进 Extras(storyboard)、
// 不混入正文、不污染全书字数统计;无关 json(如 立项.json)不进 Extras。
func TestAnalyzeDirStoryboardJSON(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "JSON书")
	mustMkdir(t, filepath.Join(book, "正文"))
	mustMkdir(t, filepath.Join(book, "素材", "分镜脚本"))
	mustWrite(t, filepath.Join(book, "正文", "第001章_开篇.md"), "第一章正文")
	mustWrite(t, filepath.Join(book, "素材", "分镜脚本", "第022章_捏碎的测试仪_分镜脚本.json"),
		`{"book":"JSON书","shots":[{"shot_id":1,"action":"a"}]}`)
	mustWrite(t, filepath.Join(book, "素材", "分镜脚本", "第023章_豆豆失踪_分镜脚本.json"), `{"shots":[]}`)
	mustWrite(t, filepath.Join(book, "立项.json"), `{"title":"x"}`)

	res := analyzeDir(dir)
	projs, ok := res["projects"].([]dirProject)
	if !ok || len(projs) != 1 {
		t.Fatalf("projects = %#v, want 1", res["projects"])
	}
	p := projs[0]
	if len(p.Chapters) != 1 {
		t.Fatalf("Chapters = %d, want 1(json 分镜不得混入正文)", len(p.Chapters))
	}
	var sb int
	for _, e := range p.Extras {
		switch {
		case e.Kind == "storyboard":
			sb++
			if filepath.Ext(e.Name) != ".json" {
				t.Errorf("storyboard 应为 json: %s", e.Name)
			}
		case e.Name == "立项.json":
			t.Error("无关 json 不应进 Extras")
		}
	}
	if sb != 2 {
		t.Errorf("Extras storyboard json = %d, want 2", sb)
	}
	if p.Words != len([]rune("第一章正文")) {
		t.Errorf("字数统计被 json 污染: %d", p.Words)
	}
}
