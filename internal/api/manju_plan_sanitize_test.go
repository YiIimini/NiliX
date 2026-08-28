package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 回归(2026-08-28「别动我的宿主」):LLM 直出方案的角色 id 含英文双引号(王鹏飞"王胖"),
// id 直接拼落盘路径 characters/<id>.png → Windows 报「文件名语法不正确」,assets 阶段整集失败。
// 角色/场景 id 与镜头引用必须在 loadPlan/writePlan 同一口径下清洗。
func TestManjuSanitizePlanIDsQuotedChar(t *testing.T) {
	plan := map[string]any{
		"characters": []any{
			map[string]any{"id": `王鹏飞"王胖"`, "name": "王胖"},
			map[string]any{"id": "陈鱼"},
		},
		"scenes": []any{
			map[string]any{"id": `废墟:大门`},
		},
		"shots": []any{
			map[string]any{"shot_id": 1, "scene": `废墟:大门`, "characters": []any{`王鹏飞"王胖"`, "陈鱼"}},
		},
	}
	manjuSanitizePlanIDs(plan)

	c0 := anyArr(plan["characters"])[0].(map[string]any)
	if got := str(c0["id"]); got != `王鹏飞_王胖_` {
		t.Fatalf("角色 id 应清洗双引号, got %q", got)
	}
	s0 := anyArr(plan["scenes"])[0].(map[string]any)
	if got := str(s0["id"]); got != "废墟_大门" {
		t.Fatalf("场景 id 应清洗冒号, got %q", got)
	}
	sh := anyArr(plan["shots"])[0].(map[string]any)
	if got := str(sh["scene"]); got != "废墟_大门" {
		t.Fatalf("镜头 scene 引用应同步改写, got %q", got)
	}
	carr := sh["characters"].([]any)
	if got := carr[0].(string); got != `王鹏飞_王胖_` {
		t.Fatalf("镜头角色引用应同步改写, got %q", got)
	}
	if got := carr[1].(string); got != "陈鱼" {
		t.Fatalf("合法 id 不得被改动, got %q", got)
	}

	// 幂等:二次调用结果不变
	manjuSanitizePlanIDs(plan)
	if got := str(c0["id"]); got != `王鹏飞_王胖_` {
		t.Fatalf("清洗应幂等, got %q", got)
	}
}

// 清洗后撞名:两个不同 raw id 清洗为同一安全名时,后者追加下划线保唯一(不得互相覆盖)
func TestManjuSanitizePlanIDsCollision(t *testing.T) {
	plan := map[string]any{
		"characters": []any{
			map[string]any{"id": `a"b`},
			map[string]any{"id": `a/b`},
		},
	}
	manjuSanitizePlanIDs(plan)
	arr := anyArr(plan["characters"])
	id1 := str(arr[0].(map[string]any)["id"])
	id2 := str(arr[1].(map[string]any)["id"])
	if id1 != "a_b" {
		t.Fatalf("第一个 id 应取基础安全名, got %q", id1)
	}
	if id2 != "a_b_" {
		t.Fatalf("第二个 id 撞名应追加下划线保唯一, got %q", id2)
	}
}

// 存量方案口径:磁盘上已落盘的含非法字符方案,loadPlan 读入后 id 与镜头引用已清洗,
// 后续 assets/render 拿到的 id 可安全拼路径
func TestManjuLoadPlanSanitizesLegacyIDs(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: dir, episode: "EP01"}
	raw := `{"chapters":"1-1","characters":[{"id":"王鹏飞\"王胖\""}],"scenes":[{"id":"废墟:大门"}],` +
		`"shots":[{"shot_id":1,"scene":"废墟:大门","characters":["王鹏飞\"王胖\""]}]}`
	if err := os.WriteFile(filepath.Join(dir, "EP01_direct_plan.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	plan, shots, err := ctx.loadPlan()
	if err != nil {
		t.Fatal(err)
	}
	c0 := anyArr(plan["characters"])[0].(map[string]any)
	if got := str(c0["id"]); got != `王鹏飞_王胖_` {
		t.Fatalf("读盘后角色 id 应已清洗, got %q", got)
	}
	if len(shots) != 1 || shots[0].Characters[0] != `王鹏飞_王胖_` || shots[0].Scene != "废墟_大门" {
		t.Fatalf("读盘后镜头引用应已清洗: %+v", shots[0])
	}
}

// writePlan 落盘口径:含非法字符 id 的方案写盘后,磁盘 JSON 里的 id 已清洗;
// 内存同一 map 同步清洗(调用方拿到即安全)
func TestManjuWritePlanSanitizesIDsOnDisk(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{analysisDir: dir, episode: "EP01"}
	plan := map[string]any{
		"characters": []any{map[string]any{"id": `王鹏飞"王胖"`}},
	}
	if err := ctx.writePlan(plan); err != nil {
		t.Fatal(err)
	}
	if got := str(anyArr(plan["characters"])[0].(map[string]any)["id"]); got != `王鹏飞_王胖_` {
		t.Fatalf("writePlan 应同步清洗内存 plan, got %q", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, "EP01_direct_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `王鹏飞\"王胖\"`) {
		t.Fatalf("落盘 JSON 仍含未清洗 id: %s", b)
	}
	if !strings.Contains(string(b), `王鹏飞_王胖_`) {
		t.Fatalf("落盘 JSON 应为清洗后 id: %s", b)
	}
}
