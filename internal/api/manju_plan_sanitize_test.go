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

// 伪场景过滤回归(2026-08-28:素材全局段「通用负向词」被 LLM 当场景输出,
// scenes[0]=通用负向词 白烧一张 GPU 生成无意义场景图)
func TestManjuSanitizePlanIDsDropsPseudoScenes(t *testing.T) {
	plan := map[string]any{
		"scenes": []any{
			map[string]any{"id": "通用负向词"},
			map[string]any{"id": "开放工位区"},
			map[string]any{"id": "统一风格前缀"},
			map[string]any{"id": "通用仓库"}, // 真场景名含「通用」,不得误删
		},
		"characters": []any{map[string]any{"id": "陈默"}},
	}
	manjuSanitizePlanIDs(plan)
	ids := []string{}
	for _, x := range anyArr(plan["scenes"]) {
		ids = append(ids, str(x.(map[string]any)["id"]))
	}
	for _, bad := range []string{"通用负向词", "统一风格前缀"} {
		for _, id := range ids {
			if id == bad {
				t.Fatalf("伪场景 %q 应被过滤, got scenes: %v", bad, ids)
			}
		}
	}
	for _, want := range []string{"开放工位区", "通用仓库"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("真场景 %q 不得误删, got scenes: %v", want, ids)
		}
	}
}

// 回归(2026-08-28 EP01 事故):场景卡「城市夜景大远景」desc 写「封面备用·开篇/终章」,
// id 干净但实为封面卡——混进正片场景池后被别名匹配+最高频兜底传染全片(办公室戏 18 镜
// 全挂城市大远景参考图)。封面卡必须整条删,且引用它的镜头 scene 置空(不挂错图)。
func TestManjuSanitizePlanIDsDropsCoverScene(t *testing.T) {
	plan := map[string]any{
		"scenes": []any{
			map[string]any{"id": "城市夜景大远景", "description": "封面备用·开篇/终章;说明:凌晨的城市,一栋写字楼里唯一亮着的窗"},
			map[string]any{"id": "深夜工位", "description": "说明:深夜亮着显示器的工位"},
		},
		"shots": []any{
			map[string]any{"shot_id": 1, "scene": "城市夜景大远景"},
			map[string]any{"shot_id": 2, "scene": "深夜工位"},
		},
		"characters": []any{map[string]any{"id": "陈默"}},
	}
	manjuSanitizePlanIDs(plan)
	for _, x := range anyArr(plan["scenes"]) {
		if str(x.(map[string]any)["id"]) == "城市夜景大远景" {
			t.Fatal("封面备用卡(desc 含「封面」)应被过滤")
		}
	}
	for _, x := range anyArr(plan["shots"]) {
		m := x.(map[string]any)
		if m["shot_id"] == 1 && str(m["scene"]) != "" {
			t.Fatalf("引用被删封面卡的镜头 scene 应置空, got %q", str(m["scene"]))
		}
		if m["shot_id"] == 2 && str(m["scene"]) != "深夜工位" {
			t.Fatalf("正常场景引用不得误清, got %q", str(m["scene"]))
		}
	}
}

// 回归(2026-08-28 EP01 事故):旧 matchSceneByAlias 是美食书硬编码别名表,跨书污染——
// 本书镜12 pool 含「门口」→ 旧规则 need「夜景」命中封面卡「城市夜景大远景」→ 1 票最高频
// 兜底传染全片。重写为场景卡动态特征词滑窗匹配后:工位词命中工位卡,「门口」不再命中
// 封面卡(封面卡已不入匹配池),多卡平局取 id 短者(更专一)。
func TestManjuMatchSceneByAliasDynamic(t *testing.T) {
	cards := []map[string]any{
		{"id": "开放工位区", "description": "说明:整层开放的工位隔断区"},
		{"id": "深夜工位", "description": "说明:深夜唯一亮着的工位"},
		{"id": "城市夜景大远景", "description": "封面备用·开篇/终章;说明:凌晨的城市,一栋写字楼里唯一亮着的窗"},
		{"id": "数据机房", "description": "说明:服务器机柜走廊"},
	}
	cases := []struct {
		name    string
		pool    string
		want    string
		wantAny []string
	}{
		{"工位隔断叙述命中工位卡", "门禁读卡器亮起绿点,皮鞋声拐过工位隔断,赵德柱停在亮屏前", "",
			[]string{"深夜工位", "开放工位区"}},
		{"旧污染词「门口」不再命中封面卡", "他站在门口迟疑了一下,推开玻璃门走进来", "", nil},
		{"服务器机柜命中机房", "两人穿过成排的服务器机柜,蓝灯连成一线", "数据机房", nil},
		{"城市/夜景泛词+封面卡词不构成场景身份", "俯瞰城市,夜景璀璨,写字楼林立", "", nil},
	}
	for _, c := range cases {
		got := matchSceneByAlias(c.pool, cards)
		if c.wantAny != nil {
			ok := false
			for _, w := range c.wantAny {
				if got == w {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s: pool=%q got %q, want one of %v", c.name, c.pool, got, c.wantAny)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: pool=%q got %q, want %q", c.name, c.pool, got, c.want)
		}
	}
}

// 特征词滑窗:长 id 无分词也能拆出实体词(「深夜工位」→「工位」),泛词表滤除镜头语言词
func TestManjuSceneFeatureWords(t *testing.T) {
	ws := manjuSceneFeatureWords("城市夜景大远景 封面备用·开篇/终章;说明:凌晨的城市,一栋写字楼里唯一亮着的窗")
	has := func(w string) bool {
		for _, x := range ws {
			if x == w {
				return true
			}
		}
		return false
	}
	for _, generic := range []string{"城市", "夜景", "大远景", "凌晨", "封面", "备用", "开篇", "终章", "说明"} {
		if has(generic) {
			t.Errorf("泛词 %q 应被滤除", generic)
		}
	}
	if !has("写字楼") {
		t.Errorf("实体词「写字楼」应保留为特征词, got %v", ws)
	}
}

// 回归(2026-08-28 EP01 配音事故):爽文技能规范示例「台词 <d>[中文]原文</d>」被 LLM 字面
// 抄袭,EP01 六段式台词段直出 <d>[中文]陈默？</d>——H3 把「中文」二字当台词念/对白驱动
// 失效,有台词的镜全部近静默。最终化汇点必须剥掉 d 标签内的 [中文]/[chinese] 占位前缀。
func TestManjuFinalizePromptStripsDialogueTagNoise(t *testing.T) {
	in := "The engineer (S1) says: <d>[中文]陈默？</d> while typing.\nThe narrator says: <d>[Chinese] 凌晨两点,写字楼还亮着一盏灯。</d>"
	out := manjuFinalizePromptPure(in, true, 3)
	if strings.Contains(out, "[中文]") || strings.Contains(out, "[Chinese]") {
		t.Fatalf("d 标签占位前缀应被剥除, got: %s", out)
	}
	if !strings.Contains(out, "<d>陈默？</d>") {
		t.Errorf("台词本体应保留, want <d>陈默？</d>, got: %s", out)
	}
	if !strings.Contains(out, "<d>凌晨两点,写字楼还亮着一盏灯。</d>") {
		t.Errorf("旁白本体应保留, got: %s", out)
	}
}

// 回归(2026-08-28 EP01 人物不一致):脚本直出 h3 的 <Picture N> 编号按叙述假设,与渲染端
// 实际提交(人物图前+场景图后)错位——镜3 chars=[] 却写 <Picture 1> is Chen Mo,唯一挂的
// 城市夜景图被 H3 当陈默长相参考。最终化必须:①超界引用剥除;②无人物图镜的人物主体句
// 引用剥除(场景句保留);③残留连接词清理成通顺英文。
func TestManjuStripDanglingPictureRefs(t *testing.T) {
	// 镜3 真实形态:无人物图(picSlots=1 场景图),人物句引用 Picture 1/2 全剥,环境句保留
	in := "subject_definitions:\n<Subject 1> is the living form of Chen Mo in <Picture 1>, a lean engineer with black-framed glasses.\n<Subject 2> is the dark reflective monitor face in <Picture 2>, glowing cold white.\n<Subject 3> is the office floor environment in <Picture 1>, rows of cubicles.\n"
	out := manjuFinalizePromptPure(in, false, 1)
	if strings.Contains(out, "Chen Mo in <Picture") {
		t.Errorf("无人物图镜的人物句 Picture 引用应剥除, got: %s", out)
	}
	if strings.Contains(out, "monitor face in <Picture") {
		t.Errorf("超界引用(Picture 2 > 1 槽)应剥除, got: %s", out)
	}
	if !strings.Contains(out, "environment in <Picture 1>") {
		t.Errorf("环境句的场景槽引用应保留, got: %s", out)
	}
	if !strings.Contains(out, "black-framed glasses") {
		t.Errorf("剥引用不得丢人物描述本体, got: %s", out)
	}
	// 有人物图镜:槽内引用保留,仅超界剥除
	in2 := "<Subject 1> is Zhao Dezhu in <Picture 1> with gold watch.\n<Subject 2> is a prop badge in <Picture 4>, worn lanyard.\n"
	out2 := manjuFinalizePromptPure(in2, true, 2)
	if !strings.Contains(out2, "Zhao Dezhu in <Picture 1>") {
		t.Errorf("槽内人物引用应保留, got: %s", out2)
	}
	if strings.Contains(out2, "<Picture 4>") {
		t.Errorf("超界道具引用应剥除, got: %s", out2)
	}
}

// 回归(2026-08-28 过捞修复):h3 捞人第一版用「卡内特征词重叠≥3」,角色卡共享模板词
// (cinematic/photorealistic/doll/aesthetic)让镜3 把 12 个角色全部捞进 chars(渲染挂图
// 与人物纪律全乱)。收紧为卡间独有词(仅 1 卡有的词)重叠≥2 + 主体句限定 subject_
// definitions 区:镜3 只捞回陈默(plaid/flannel/black-framed glasses 独有),赵德柱不捞。
func TestManjuCharsFromH3UniqueWords(t *testing.T) {
	cards := []map[string]any{
		{"id": "陈默", "image_prompt": "Cinematic film still, photorealistic, virtual digital human, a lean 32-year-old software engineer with short messy black hair, dark circles, black-framed glasses, dark-gray plaid flannel shirt"},
		{"id": "赵德柱", "image_prompt": "Cinematic film still, photorealistic, virtual digital human, a middle-aged manager with comb-over hair, navy polo shirt, gold watch on wrist"},
		{"id": "林小满", "image_prompt": "Cinematic film still, photorealistic, virtual digital human, a young woman with long ponytail, bright orange hoodie, canvas sneakers"},
	}
	ids := []string{"陈默", "赵德柱", "林小满"}
	h3 := "subject_definitions:\n<Subject 1> is the living form of Chen Mo in <Picture 1>, a lean engineer with short messy black hair, dark circles, black-framed glasses, plaid flannel shirt with sleeves rolled to the elbows.\n<Subject 2> is the dark reflective monitor face in <Picture 2>, glowing cold white.\n\nsummary:\n[reference generation] test.\n\ndetailed_description:\nThe navy polo manager walks in with his gold watch and comb-over."
	got := manjuCharsFromH3(h3, cards, ids)
	if len(got) != 1 || got[0] != "陈默" {
		t.Fatalf("镜3 应只捞回陈默(独有词 plaid/flannel/glasses), got %v", got)
	}
	// detailed_description 提到赵德柱外观也不能捞(主体句限定 subject_definitions 区)
	for _, c := range got {
		if c == "赵德柱" {
			t.Fatal("主体区外的描述词不得触发捞人")
		}
	}
}

// 回归(2026-08-28 误杀修复):「深夜工位」是主场景,素材标题括号写「（主场景·夜·全书
// 视觉锚）」并入 desc 后被伪卡词「全书」整卡误杀——EP01 整本书丢主场景。伪配置组
// 只查 id;封面组仍查 id+desc(封面备用卡的标记在 desc 里)。
func TestManjuSceneDroppedGrouping(t *testing.T) {
	if manjuSceneDropped("深夜工位", "主场景·夜·全书视觉锚;说明:唯一亮着的工位,暖台灯孤岛×冷蓝夜色对比") {
		t.Fatal("主场景 desc 含合法标注「全书视觉锚」不得误杀(伪卡词只查 id)")
	}
	if !manjuSceneDropped("城市夜景大远景", "封面备用·开篇/终章;说明:凌晨的城市,一栋写字楼里唯一亮着的窗") {
		t.Fatal("封面备用卡(desc 含「封面」)必须 drop")
	}
	if !manjuSceneDropped("通用负向词", "") {
		t.Fatal("伪配置卡(id 含「负向」)必须 drop")
	}
	if !manjuSceneDropped("统一风格前缀", "") {
		t.Fatal("伪配置卡(id 含「统一风格」)必须 drop")
	}
	if manjuSceneDropped("劳动仲裁庭", "说明:庄重的仲裁庭") {
		t.Fatal("正常场景卡不得误杀")
	}
}
