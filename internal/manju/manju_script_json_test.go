package manju

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 2026-08-31 JSON 分镜脚本全量解析验证:每个 .json 的 shots 数 = 解析器输出镜数。
// 2026-09-03 改动态扫书(不写死书名,删书/加书自适应)。
func TestJSONScriptParseAll(t *testing.T) {
	total := 0
	for _, book := range allBookDirs(t) {
		dir := filepath.Join(book, "素材", "分镜脚本")
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		if len(files) == 0 {
			continue // 该书无 JSON 分镜(未做分镜/旧 md 格式),不视为失败
		}
		for _, f := range files {
			text, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("读 %s: %v", f, err)
			}
			raws, _, err := parseScriptJSON(string(text))
			if err != nil {
				t.Fatalf("解析 %s: %v", f, err)
			}
			// shots 数 = 六段式块数(JSON 内 h3_prompt 非空计数)
			hasPrompt := 0
			for _, r := range raws {
				if strings.TrimSpace(r.H3Prompt) != "" {
					hasPrompt++
				}
			}
			if len(raws) == 0 {
				t.Fatalf("%s 解析后无镜头", f)
			}
			if hasPrompt != len(raws) {
				t.Fatalf("%s: %d 镜但仅 %d 镜有六段式", filepath.Base(f), len(raws), hasPrompt)
			}
			total += len(raws)
		}
	}
	t.Logf("全量 JSON 分镜脚本解析 OK,共 %d 镜", total)
}

// parseScriptJSON 的镜号唯一性(重复递增分配)与台词拆分
func TestParseScriptJSONBasic(t *testing.T) {
	j := `{"shots":[
	  {"shot_id":1,"shot_size":"特写","action":"【场景】A","dialogue":"(S1)甲:\"你好\"","duration":5,"h3_prompt":"subject_definitions:\nx"},
	  {"shot_id":2,"shot_size":"中景","action":"【场景】B","dialogue":"内心·甲:\"想什么。\"\n旁白：风声。","duration":6,"h3_prompt":"summary:\ny"}
	]}`
	raws, _, err := parseScriptJSON(j)
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) != 2 {
		t.Fatalf("镜数 = %d, want 2", len(raws))
	}
	if raws[0].Dialogue != "(S1)甲:\"你好\"" || raws[0].Narration != "" {
		t.Fatalf("镜1 台词拆分错: %+v", raws[0])
	}
	if raws[1].Dialogue != "" || !strings.Contains(raws[1].Narration, "内心·甲") || !strings.Contains(raws[1].Narration, "旁白") {
		t.Fatalf("镜2 内心/旁白拆分错: %+v", raws[1])
	}
	if raws[1].JSONChars != nil {
		t.Fatalf("未声明 characters 应为 nil")
	}
	// 重复镜号递增
	j2 := `{"shots":[{"shot_id":3,"action":"a","duration":5},{"shot_id":3,"action":"b","duration":5}]}`
	raws2, _, err := parseScriptJSON(j2)
	if err != nil || len(raws2) != 2 || raws2[0].ID == raws2[1].ID {
		t.Fatalf("重复镜号未递增分配: %+v err=%v", raws2, err)
	}
}

// md 表格断行修复回归:对话列含换行的行(旧格式 541 异常行)在 JSON 中多行合并
func TestJSONDialogueMultiLine(t *testing.T) {
	re := regexp.MustCompile(`\(S\d+\)[^：:]*[：:]`)
	j := `{"shots":[{"shot_id":1,"action":"a","dialogue":"(S1)甲:\"第一句\"\n(S1)甲:\"第二句\"","duration":5}]}`
	raws, _, err := parseScriptJSON(j)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(raws[0].Dialogue, "\n")
	if len(lines) != 2 || re.FindString(lines[0]) == "" {
		t.Fatalf("多行台词拆分错: %q", raws[0].Dialogue)
	}
	_ = strconv.Itoa
}

// 2026-08-31 JSON 素材卡解析:人物生成提示词.json / 场景提示词.json
func TestParseCardsJSON(t *testing.T) {
	chars := parseCharCardsJSON(`[
	  {"id":"金珠","gender":"女","age":"24岁","role":"女主","appearance":"背锅账房","voice":"清亮女声","image_prompt":"Cinematic ..."},
	  {"id":"云鹤章","gender":"男","age":"58岁","role":"反派","image_prompt":"Cinematic ..."}
	]`)
	if len(chars) != 2 || chars[0]["id"] != "金珠" || chars[0]["role"] != "女主" {
		t.Fatalf("角色 JSON 解析错: %+v", chars)
	}
	if chars[1]["age"] != "58岁" || chars[0]["voice"] != "清亮女声" {
		t.Fatalf("角色字段错: %+v", chars[1])
	}
	scenes := parseSceneCardsJSON(`[
	  {"id":"深夜工位","description":"唯显示器亮着","image_prompt":"Cinematic office ..."}
	]`)
	if len(scenes) != 1 || scenes[0]["id"] != "深夜工位" || scenes[0]["image_prompt"] == "" {
		t.Fatalf("场景 JSON 解析错: %+v", scenes)
	}
}

// 存量转换 JSON 素材卡全量可解析(id 非空、image_prompt 非空占比)
func TestCardsJSONAllBooks(t *testing.T) {
	total, withPrompt := 0, 0
	for _, book := range allBookDirs(t) {
		dir := filepath.Join(book, "素材")
		bts, err := os.ReadFile(filepath.Join(dir, "人物生成提示词.json"))
		if err != nil {
			continue // 该书无角色卡素材,跳过
		}
		for _, c := range parseCharCardsJSON(string(bts)) {
			total++
			if str(c["image_prompt"]) != "" {
				withPrompt++
			}
		}
	}
	if total == 0 {
		t.Skip("书库中无角色卡素材可扫")
	}
	t.Logf("全库 JSON 角色卡 %d 个,含 image_prompt %d 个", total, withPrompt)
	if withPrompt < total/2 {
		t.Fatalf("角色卡转换质量不足: %d/%d 有提示词", withPrompt, total)
	}
}

// 2026-08-31 用户实测「有 JSON 脚本却走 LLM 直出」根因修复:autoStoryboardForEpisode
// 匹配分镜脚本 .json 优先(md 兼容)——JSON 化后 .md 删除,旧匹配 0 命中回退 LLM
func TestAutoStoryboardFindsJSON(t *testing.T) {
	dir := t.TempDir()
	sbDir := filepath.Join(dir, "素材", "分镜脚本")
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 只放 JSON(模拟 JSON 化后的书)
	if err := os.WriteFile(filepath.Join(sbDir, "第1章_天才榜第七_分镜脚本.json"),
		[]byte(`{"shots":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "第5章_破影者来了_分镜脚本.json"),
		[]byte(`{"shots":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := &manjuCtx{P: map[string]any{"novel_dir": dir}, R: map[string]any{"episode": "EP01"}}
	got := ctx.autoStoryboardForEpisode(nil)
	if got == "" || !strings.HasSuffix(got, "第1章_天才榜第七_分镜脚本.json") {
		t.Fatalf("EP01 应命中第1章 JSON 脚本, got %q", got)
	}
	ctx2 := &manjuCtx{P: map[string]any{"novel_dir": dir}, R: map[string]any{"episode": "EP05"}}
	got2 := ctx2.autoStoryboardForEpisode(nil)
	if got2 == "" || !strings.HasSuffix(got2, "第5章_破影者来了_分镜脚本.json") {
		t.Fatalf("EP05 应命中第5章 JSON 脚本, got %q", got2)
	}
}

// 存量容错回归(2026-09-03 拆包动态扫书实锤):递葫芦47章 light/sound 数组形态、
// 被论斤18章字符串内裸控制字符——技能侧产出形态漂移不得让解析层直接拒稿。
func TestParseScriptJSONTolerantForms(t *testing.T) {
	j := `{"shots":[{"shot_id":1,"action":"a","light":["火把光明灭"],"sound":["欢呼如潮","压住的呼吸"],"duration":5,"h3_prompt":"x"}]}`
	raws, _, err := parseScriptJSON(j)
	if err != nil {
		t.Fatal(err)
	}
	if raws[0].Light != "火把光明灭" || raws[0].Sound != "欢呼如潮／压住的呼吸" {
		t.Fatalf("数组形态兼容错: %q / %q", raws[0].Light, raws[0].Sound)
	}
	// 字符串字面量内裸换行(JSON 标准禁止)转空格宽容解析;token 间换行本合法
	j2 := "{\n\"shots\":[{\"shot_id\":1,\"action\":\"甲说\n继续\",\"duration\":5}]}"
	raws2, _, err := parseScriptJSON(j2)
	if err != nil {
		t.Fatalf("裸控制字符应被宽容解析: %v", err)
	}
	if raws2[0].Action != "甲说 继续" {
		t.Fatalf("裸换行应转空格: %q", raws2[0].Action)
	}
}
