// 2026-08-30 五问整改回归测试:内心配音重复 / 配音分不清人物乱对嘴型 /
// 站位不清 / 运镜垃圾 / 动作僵硬——机械修复层全部用例。
package manju
import (
	"strings"
	"testing"
)

// ---- 问题1:内心活动配音重复 ----

// stripNarrationPrefix:剥「内心·角色名:」「旁白:」(Sx)与引号,取纯内容
func TestStripNarrationPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"内心·阿影:\"当影子第三年。\"。", "当影子第三年。"},
		{"内心·阿影:\"直起身子。\"。", "直起身子。"},
		{"旁白：雨夜的风声掠过屋顶。", "雨夜的风声掠过屋顶。"},
		{"旁白:两人对峙。", "两人对峙。"},
		{"内容(S1)。", "内容。"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := stripNarrationPrefix(c.in); got != c.want {
			t.Fatalf("stripNarrationPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 补写去重:LLM 已剥离前缀正确写法时,「内心·」narration 不再补出带字面量标签的
// 重复画外音(EP01 镜4/5/6/14 实锤场景);未同步时才补写干净内容(不带前缀)。
func TestScriptValidateInnerNoDup(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	raws := []scriptShotRaw{
		{ID: 1, Duration: 6,
			Narration: "内心·阿影:\"当影子第三年。\"\"好处是不用吃饭,坏处是,看得见所有人吃饭。\"。",
			H3Prompt: "subject_definitions:\n<Subject 1> is Shen Zhao.\n\ndetailed_description:\n[Shot 1] Shen Zhao stands silent. The narrator says in an off-screen voiceover: <d>[Chinese] 当影子第三年。</d> and then adds: <d>[Chinese] 好处是不用吃饭,坏处是,看得见所有人吃饭。</d>\n\noverall_soundscape:\nquiet."},
	}
	scriptValidateShots(raws, lg)
	p := raws[0].H3Prompt
	if strings.Contains(p, "内心·阿影") {
		t.Fatalf("不应补出带「内心·阿影:」字面量的重复句: %s", p)
	}
	if strings.Count(p, "当影子第三年") != 1 {
		t.Fatalf("内心独白只应出现一次(LLM 已写,补写必须跳过): %s", p)
	}
	// 幂等:再跑一遍仍不补
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	scriptValidateShots(raws, lg)
	if strings.Count(raws[0].H3Prompt, "当影子第三年") != 1 {
		t.Fatalf("复核后不应重复补写: %s", raws[0].H3Prompt)
	}
	// 真缺失场景:补写干净内容,不带「内心·」前缀与引号
	raws2 := []scriptShotRaw{
		{ID: 2, Duration: 5,
			Narration: "内心·赵德柱:\"冷柜有动静。\"。",
			H3Prompt:  "detailed_description:\n[Shot 1] a dark room.\n\noverall_soundscape:\nquiet."},
	}
	scriptValidateShots(raws2, lg)
	p2 := raws2[0].H3Prompt
	if !strings.Contains(p2, "<d>冷柜有动静。</d>") {
		t.Fatalf("缺失的内心句应补写干净内容: %s", p2)
	}
	if strings.Contains(p2, "内心·赵德柱") {
		t.Fatalf("补写内容禁止带「内心·赵德柱:」前缀: %s", p2)
	}
}

// ---- 问题2:配音分不清人物 ----

// offscreenVoiceKeyFor 单一事实源:内心戏描述 → 角色音色(beast_cute),不再掉
// 默认 male_mag;旁白 → 叙述音色;路人 → 关键词猜测回退
func TestOffscreenVoiceKeyForInner(t *testing.T) {
	ctx := &manjuCtx{charInfo: map[string]map[string]any{
		"阿影": {"id": "阿影", "species": "兽", "gender": "女", "age": "8岁"},
	}}
	c := manjuRefContract{Chars: []manjuCharSlot{{ID: "阿影"}}}
	// EP01 实锤形态:注入侧写呆萌兽音,挂载侧此前猜成 male_mag
	if k := ctx.offscreenVoiceKeyFor("the quiet inner voice of 阿影, a cute, playful creature voice, bright and bubbly", c); k != "beast_cute" {
		t.Fatalf("内心戏应挂角色音色 beast_cute, got %q", k)
	}
	if k := ctx.offscreenVoiceKeyFor("the narrator with a calm, neutral storytelling voice", c); k != "male_narrator" {
		t.Fatalf("旁白应挂叙述音色 male_narrator, got %q", k)
	}
	if k := ctx.offscreenVoiceKeyFor("a cocky young male netizen voice", c); k != "boy_teen" {
		t.Fatalf("路人按关键词回退应挂 boy_teen, got %q", k)
	}
}

// ensureVoiceBindings 逐角色补写:存在 1 条内心 Audio 定义时,同镜登场角色缺失
// 定义仍补写(此前整体 bail=沈照无音色);编号接续 maxN+1;幂等
func TestEnsureVoiceBindingsPerChar(t *testing.T) {
	hp := "subject_definitions:\n<Subject 1> is Shen Zhao.\n<Subject 2> is the chibi shadow.\n\n<Audio 1> is the voice-timbre reference for the quiet inner voice of 阿影, a cute, playful creature voice, containing a spoken voiceover.\n\nsummary:\nThe video shows Shen Zhao standing.\n\ndetailed_description:\n[Shot 1] ...\n\noverall_soundscape:\nquiet.\n\nnon_diegetic_music:\nN/A"
	bindings := []voiceBinding{
		{CharID: "沈照", Audio: "<Audio 1>", SubN: 1},
		{CharID: "阿影", Audio: "<Audio 2>", SubN: 2},
	}
	c := manjuRefContract{Chars: []manjuCharSlot{{ID: "沈照"}, {ID: "阿影"}}}
	out := ensureVoiceBindings(hp, bindings, c)
	if !strings.Contains(out, "<Audio 2> is the voice-timbre reference for <Subject 1> (S2), containing a spoken voiceover.") {
		t.Fatalf("缺定义的登场角色沈照应补写 Audio 定义(编号接续 2): %s", out)
	}
	// 阿影内心定义保留不重复(定义目标非 <Subject 2> 形态,但能解析为阿影)
	if strings.Count(out, "for the quiet inner voice of 阿影") != 1 {
		t.Fatalf("阿影内心定义不应被重复/覆盖: %s", out)
	}
	// 幂等
	out2 := ensureVoiceBindings(out, bindings, c)
	if out2 != out {
		t.Fatalf("复核后应完全不变(幂等):\n%s\n---\n%s", out, out2)
	}
}

// markOffscreenSays:裸 (Sx) says 画外句 → off-screen voiceover + lips closed;
// 画面角色 <Subject N> (Sx) says 不动;已标注不动;幂等
func TestMarkOffscreenSays(t *testing.T) {
	hp := "detailed_description:\nThe challenger's voice, thick and jeering (S1), says: <d>[Chinese] 就这?</d> and then adds louder: <d>[Chinese] 这位子你占三年了!</d> Shen Zhao's eyes flick down.\n\noverall_soundscape:\nquiet."
	out := markOffscreenSays(hp)
	if !strings.Contains(out, "says in an off-screen voiceover:") {
		t.Fatalf("裸 (S1) says 应改写为 off-screen voiceover: %s", out)
	}
	if !strings.Contains(out, "这位子你占三年了!</d> while the on-screen characters' lips remain completely closed.") {
		t.Fatalf("最后 </d> 后应补 lips-closed 从句: %s", out)
	}
	// 幂等:改写后形态不再命中,输出稳定
	if out2 := markOffscreenSays(out); out2 != out {
		t.Fatalf("复核后应完全不变(幂等):\n%s\n---\n%s", out, out2)
	}
	// 画面角色 <Subject N> (Sx) says 不动
	hp2 := "detailed_description:\n<Subject 2> (S1) says: <d>[Chinese] 两份——</d> he begins.\n\noverall_soundscape:\nquiet."
	out2 := markOffscreenSays(hp2)
	if !strings.Contains(out2, "<Subject 2> (S1) says:") || strings.Contains(out2, "off-screen voiceover") {
		t.Fatalf("画面角色台词禁止改写为画外: %s", out2)
	}
}

// charIDMatch 剥括号:分镜表「挑战者程野(剪影)」归一为登场角色「程野」
func TestCharIDMatchBracket(t *testing.T) {
	c := manjuRefContract{Chars: []manjuCharSlot{{ID: "程野"}, {ID: "沈照"}}}
	if got := charIDMatch("挑战者程野(剪影)", c); got != "程野" {
		t.Fatalf("剥括号后应匹配程野, got %q", got)
	}
	if got := charIDMatch("程野", c); got != "程野" {
		t.Fatalf("精确名应匹配, got %q", got)
	}
	if got := charIDMatch("路人甲", c); got != "" {
		t.Fatalf("无卡角色不应误匹配, got %q", got)
	}
}

// ---- 问题3/4:站位不清 / 运镜垃圾 ----

// manjuCameraPhrase:括号英文直取 / 固定 / 中文映射兜底 / 空
// 2026-09-03 电影级升级:中文映射短语升级为电影术语(cinematic dolly/tracking/
// orbital),断言同步;「固定（Static）」类裸静态英文归一为 canonical 短语。
func TestManjuCameraPhrase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"缓推（Push In, small, slow）", "Push In, small, slow"},
		{"跟移（Track, medium, slow）", "Track, medium, slow"},
		{"固定", "static locked-off camera"},
		{"固定（Static）", "static locked-off camera"},
		{"横移", "cinematic lateral dolly truck with medium amplitude"},
		{"左摇", "smooth pan to the left"},
		{"左横移", "cinematic dolly truck to the left with medium amplitude"},
		{"跟拍", "steady cinematic tracking shot following the subject at matching speed"},
		{"低机位微推", "low-angle shot"},
		{"慢升（Rise, small, slow）", "Rise, small, slow"},
		{"", ""},
		{"无意义", ""},
	}
	for _, c := range cases {
		if got := manjuCameraPhrase(c.in); got != c.want {
			t.Fatalf("manjuCameraPhrase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// injectCameraDiscipline/injectPositionDiscipline:注入 + 幂等锚
// 2026-09-02 语义更新:站位纪律按「有 subject_definitions(有人物)」判定注入,
// fixture 带主体;运镜/站位纪律插 detailed_description 段前(先删后插自愈)。
func TestInjectDisciplines(t *testing.T) {
	hp := "subject_definitions:\n<Subject 1> is Shen Zhao.\n\ndetailed_description:\n[Shot 1] Shen Zhao stands.\n\noverall_soundscape:\nquiet."
	out := injectCameraDiscipline(hp, "缓推（Push In, small, slow）")
	if !strings.Contains(out, "CAMERA DISCIPLINE: this shot's camera performs Push In, small, slow") {
		t.Fatalf("应注入运镜纪律: %s", out)
	}
	if injectCameraDiscipline(out, "缓推（Push In, small, slow）") != out {
		t.Fatalf("运镜纪律复核应不变(幂等)")
	}
	outS := injectCameraDiscipline(hp, "固定")
	if !strings.Contains(outS, "static locked-off camera") {
		t.Fatalf("固定镜应注入 static: %s", outS)
	}
	outP := injectPositionDiscipline(hp)
	if !strings.Contains(outP, "POSITION DISCIPLINE") {
		t.Fatalf("应注入站位纪律: %s", outP)
	}
	if injectPositionDiscipline(outP) != outP {
		t.Fatalf("站位纪律复核应不变(幂等)")
	}
}

// scriptValidateShots 站位/运镜 WARN:无位置词触发告警;有位置词安静
func TestScriptValidatePositionWarn(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	raws := []scriptShotRaw{
		{ID: 1, Duration: 5, Camera: "固定",
			Dialogue: "沈照:两份——",
			H3Prompt: "detailed_description:\n[Shot 1] Shen Zhao is in the light, the camera view is wide.\n\noverall_soundscape:\nquiet."},
		{ID: 2, Duration: 6, Camera: "缓推（Push In, small, slow）",
			H3Prompt: "detailed_description:\n[Shot 1] Shen Zhao stands at the center of the frame, midground, facing camera.\n\noverall_soundscape:\nquiet."},
	}
	scriptValidateShots(raws, lg)
	manjuState.mu.Lock()
	logged := manjuState.log
	manjuState.mu.Unlock()
	if !strings.Contains(logged, "镜头 1 画面描述无任何人物位置/朝向词") {
		t.Fatalf("镜 1 缺站位应 WARN: %s", logged)
	}
	if !strings.Contains(logged, "镜头 1 有台词但画面描述无动作/表情词") {
		t.Fatalf("镜 1 有台词无动作应 WARN: %s", logged)
	}
	if strings.Contains(logged, "镜头 2 画面描述无任何人物位置/朝向词") {
		t.Fatalf("镜 2 有站位不应 WARN: %s", logged)
	}
}
