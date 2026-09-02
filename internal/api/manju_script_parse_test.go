package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tplBacktick = "```"

// 脚本直出程序化解析:用爽文技能阶段6分镜脚本真实样本验证——
// 分镜表行 → 镜头元数据、六段式代码块 → h3_prompt 逐字保留、素材 → 角色/场景卡。
func TestScriptParsePlanRealStoryboard(t *testing.T) {
	// 模拟《仙厨小饭馆》第001章分镜脚本(节选:头部 + 分镜表 2 行 + 每镜六段式 2 段)
	script := `# 《仙厨小饭馆》分镜脚本 · 第001章_一碗馊面（第1集）

> 剧名：《仙厨小饭馆》｜集号：第1集
> 主要角色：云晚（琥珀杏眼/左耳垂痣/旧木簪/青灰围裙/旧银镯/豁口锅铲）；沈玉衡（油头/月白西装银勺胸针/转银筷）
> 说话者 ID（本集固定，跨镜一致）：S1 沈玉衡｜S2 云晚
> 镜数：2 镜｜估算时长：2 × 5s ≈ 11s

## 一、分镜表（正文→镜头，每镜可渲染）

| 镜号 | 景别 | 运镜 | 画面内容（可渲染·光声味） | 台词/旁白（带 ID） | 光影 | 音效 | 时长 |
|---|---|---|---|---|---|---|---|
| 01 | 中景（小馆全景偏中） | 慢推（small, slow） | 傍晚灶台白汽滚起；云晚站在灶台后捞面（画面右侧），沈玉衡月白西装堵门口（画面左侧）；暖金灶光 vs 冷青门光对冲 | 旁白：临江老街，晚膳小馆。(S1)沈玉衡："晚老板，久仰。" | 暖金灶火内光 × 冷青门光 | 汤锅咕嘟/门轴响 | 5s |
| 02 | 中景 | 过肩固定微推 | 沈玉衡落座转银筷，云晚端面上前放桌 | (S1)沈玉衡："这汤底，放了多少天没换的？" | 冷青顶光 | 银筷碰碗沿 | 6s |

## 二、每镜 H3 提示词（六段式 Ref2VA，渲染直用）

### Shot 01 —— 中景·建立（暖金 vs 冷青对冲）
` + tplBacktick + `
subject_definitions: <Subject 1> is 云晚 / <Subject 4> is 沈玉衡 / <Picture 1> is 晚膳小馆
summary: [reference generation] 傍晚晚膳小馆，云晚在灶台后捞面，沈玉衡推门而入。
detailed_description: Cinematic film still, photorealistic. [Shot 1] 云晚站在画面右侧灶台后，画面左侧沈玉衡立于门口，冷青门光与暖金灶光相撞。0-5s 节拍：起点=云晚低头捞面，变化=沈玉衡站定开口。运镜：缓慢前推 small/slow。
overall_soundscape: 汤锅咕嘟冒泡声、门轴吱呀声。
non_diegetic_music: 暖调 BGM 起，门开瞬间铜管上挑。
` + tplBacktick + `

### Shot 02 —— 中景·过肩
` + tplBacktick + `
subject_definitions: <Subject 4> is 沈玉衡 / <Subject 1> is 云晚 / <Picture 1> is 晚膳小馆
summary: [reference generation] 沈玉衡落座转银筷质问汤底。
detailed_description: Cinematic film still, photorealistic. [Shot 2] At 00:05.000 肩后机位拍沈玉衡，前景云晚虚化肩背。0-6s 节拍：起点=落座转筷，变化=划汤三圈开口质问。运镜：固定微推 small/slow。
overall_soundscape: 银筷碰碗沿脆响、蒸汽嘶声。
non_diegetic_music: 弦乐低音悬疑铺垫。
` + tplBacktick + `

## 三、分镜验收清单（出片前自查）
- [x] 五维差异化：时间结构=傍晚→夜线性；视点=云晚受害视角；节奏=压迫递增；声音=人声鼎沸砸碗；结尾=反派冷青车镜钩子
`

	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	sp := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(sp, []byte(script), 0o644)
	// 素材(人物/场景提示词)
	matDir := filepath.Join(dir, "素材")
	_ = os.MkdirAll(matDir, 0o755)
	_ = os.WriteFile(filepath.Join(matDir, "人物生成提示词.md"), []byte(`# 人物
## 1. 云晚（女主，22岁，灵厨）
- 辨识度三件套：琥珀杏眼+左耳垂小痣 / 旧木簪挽低丸子 / 青灰围裙+旧银镯
- 正向提示词：
`+tplBacktick+`
Cinematic film still, photorealistic, a pretty 22-year-old East Asian woman, apricot almond eyes, black hair in a low bun, grey-blue apron, worn silver bracelet, movie poster quality
`+tplBacktick+`
## 2. 沈玉衡（反派少主，24岁）
- 辨识度三件套：发胶三七分油头 / 月白西装+银勺胸针 / 手里常转银筷
- 正向提示词：
`+tplBacktick+`
Cinematic film still, photorealistic, a pale 24-year-old East Asian man, slicked-back hair, white suit with silver spoon lapel pin, holding a silver chopstick, movie poster quality
`+tplBacktick+`
`), 0o644)
	_ = os.WriteFile(filepath.Join(matDir, "场景提示词.md"), []byte(`# 场景
## 1. 晚膳小馆（主角主场·临江老街）
- 视觉锚定物：旧灶台上一口豁边铁锅、窗台一盆蒜苗
- 光线：灶台暖金炉火为主光
`+tplBacktick+`
Cinematic film still, photorealistic, a small old noodle shop, worn iron wok on a brick stove, warm golden interior light, movie poster quality
`+tplBacktick+`
`), 0o644)

	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"real+2.5d+ink","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)

	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	// 镜头数
	shots, _ := planShots(plan)
	if len(shots) != 2 {
		t.Fatalf("应 2 镜, got %d", len(shots))
	}
	// 六段式逐字保留(含站位/运镜)
	s1 := shots[0]
	if !strings.Contains(s1.H3Prompt, "subject_definitions") || !strings.Contains(s1.H3Prompt, "detailed_description") || !strings.Contains(s1.H3Prompt, "overall_soundscape") {
		t.Fatalf("shot1 h3_prompt 应为完整六段式, got: %q", s1.H3Prompt[:min(80, len(s1.H3Prompt))])
	}
	if !strings.Contains(s1.H3Prompt, "画面右侧") || !strings.Contains(s1.H3Prompt, "画面左侧") {
		t.Fatalf("shot1 站位(左/右侧)丢失: %q", s1.H3Prompt)
	}
	if !strings.Contains(s1.H3Prompt, "缓慢前推 small/slow") {
		t.Fatalf("shot1 运镜丢失: %q", s1.H3Prompt)
	}
	if s1.Duration != 5 {
		t.Fatalf("shot1 时长应为 5, got %d", s1.Duration)
	}
	if s1.ShotSize == "" || !strings.Contains(s1.ShotSize, "中景") {
		t.Fatalf("shot1 景别解析失败: %q", s1.ShotSize)
	}
	if s1.Camera == "" || !strings.Contains(s1.Camera, "慢推") {
		t.Fatalf("shot1 运镜字段解析失败: %q", s1.Camera)
	}
	// 台词/旁白拆分
	if s1.Dialogue == "" || !strings.Contains(s1.Dialogue, "沈玉衡") || !strings.Contains(s1.Dialogue, "晚老板") {
		t.Fatalf("shot1 台词解析失败: %q", s1.Dialogue)
	}
	if s1.Narration == "" || !strings.Contains(s1.Narration, "临江老街") {
		t.Fatalf("shot1 旁白解析失败: %q", s1.Narration)
	}
	// 角色匹配(画面内容含云晚/沈玉衡)
	if len(s1.Characters) != 2 {
		t.Fatalf("shot1 登场角色应含 云晚/沈玉衡, got %v", s1.Characters)
	}
	// 角色卡:素材解析 + 风格措辞
	chars := anyArr(plan["characters"])
	if len(chars) != 2 {
		t.Fatalf("角色卡应 2 个, got %d", len(chars))
	}
	c0 := chars[0].(map[string]any)
	if str(c0["id"]) != "云晚" || str(c0["gender"]) != "女" {
		t.Fatalf("角色卡0 应为云晚/女, got %v", c0)
	}
	ip := str(c0["image_prompt"])
	if !strings.Contains(ip, "22-year-old") || !strings.Contains(ip, "apricot almond eyes") {
		t.Fatalf("云晚 image_prompt 应为素材英文, got: %q", ip)
	}
	// 场景卡
	scenes := anyArr(plan["scenes"])
	if len(scenes) != 1 {
		t.Fatalf("场景卡应 1 个, got %d", len(scenes))
	}
	// directing 五维
	d, _ := plan["directing"].(map[string]any)
	if d == nil || str(d["time"]) == "" {
		t.Fatalf("directing 缺失: %+v", d)
	}
}

// TestScriptParse9ColDuration 回归测试(H1 修复):9 列分镜表(音效与时长间插风格列)时长必须正确解析,
// 不得因列错位回退成 5s——否则长台词镜被压进 5s 渲染导致台词截断/内容丢失。
func TestScriptParse9ColDuration(t *testing.T) {
	script := `# 《测试》分镜脚本 · 第001章（第1集）

## 一、分镜表（正文→镜头）

| 镜号 | 景别 | 运镜 | 画面内容（可渲染·光声味） | 台词/旁白（带 ID） | 光影 | 音效 | 风格（可选） | 时长 |
|---|---|---|---|---|---|---|---|---|
| 01 | 近景 | 固定（Static） | 主角抬眼 | (S1)主角："这句台词很长需要九秒才说得完，一字一字数得清清楚楚。" | 冷光 | 风声 | real | 9s |
| 02 | 中景 | 慢推（Push In, small, slow） | 反派冷笑 | (S2)反派："哼。" | 暖光 | 门轴 | real | 7s |

## 二、每镜 H3 提示词（六段式 Ref2VA）

### Shot 01 —— 近景
` + tplBacktick + `
subject_definitions: <Subject 1> is 主角
summary: [reference generation] 主角抬眼
detailed_description: Cinematic film still, photorealistic. [Shot 1] 主角抬眼。
overall_soundscape: 风声
non_diegetic_music: 低弦
` + tplBacktick + `

### Shot 02 —— 中景
` + tplBacktick + `
subject_definitions: <Subject 1> is 主角 / <Subject 2> is 反派
summary: [reference generation] 反派冷笑
detailed_description: Cinematic film still, photorealistic. [Shot 2] 反派冷笑。
overall_soundscape: 门轴
non_diegetic_music: 低弦
` + tplBacktick + `
`

	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	sp := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(sp, []byte(script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"real","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)

	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots, _ := planShots(plan)
	if len(shots) != 2 {
		t.Fatalf("应 2 镜, got %d", len(shots))
	}
	if shots[0].Duration != 9 {
		t.Fatalf("shot1 时长应为 9s(9列风格列后), got %d —— H1 时长列错位回归!", shots[0].Duration)
	}
	if shots[1].Duration != 7 {
		t.Fatalf("shot2 时长应为 7s, got %d", shots[1].Duration)
	}
	// 长台词完整保留(不截断)
	if !strings.Contains(shots[0].Dialogue, "这句台词很长需要九秒才说得完") {
		t.Fatalf("shot1 长台词丢失: %q", shots[0].Dialogue)
	}
	// 六段式逐字保留
	if !strings.Contains(shots[0].H3Prompt, "Cinematic film still") {
		t.Fatalf("shot1 h3_prompt 应为六段式, got: %q", shots[0].H3Prompt[:min(80, len(shots[0].H3Prompt))])
	}
}

// TestScriptParseFormatB 回归测试(H2 修复):Format B 脚本(六段式写在 2 列表格、无 ### Shot 代码块,
// 混沌灵根/镇厄奶团等老脚本格式)必须从表格提取 detailed_description 作为 h3_prompt,
// 不得全部走 LLM 重生成导致导演设计/台词被改写。
func TestScriptParseFormatB(t *testing.T) {
	script := `# 《混沌灵根》分镜脚本 · 第001章_重生前夜（第1集）

> 剧名：《混沌灵根》｜集号：第1集
> 镜数：2 镜

## 一、分镜表（正文内容→镜头，每镜可渲染）

| 镜号 | 景别 | 运镜 | 画面内容（正文原意提炼） | 台词/旁白（带 ID，逐字） | 光影 | 音效 | 时长 |
|---|---|---|---|---|---|---|---|
| 01 | 中景 | 固定（Static） | 苏念睁开 | 旁白：苏念睁开眼的时候 | 常规光影 | 风声山鸣 | 6s |
| 02 | 特写 | 缓推（Push In, small, slow） | 苏念盯着 | (S1)苏念："苏薇。" | 夜色冷光 | 心跳声 | 6s |

## 二、每镜 H3 提示词（六段式，紧凑）

| 镜 | detailed_description（Cinematic film still, photorealistic 开头） |
|---|---|
| 01 | [Shot 1] Cinematic film still, photorealistic, 苏念睁眼看见房梁。<常规光影>。<运镜：固定（Static）>。<音效：风声山鸣> |
| 02 | [Shot 2] Cinematic film still, photorealistic, 苏念盯着苏薇。<夜色冷光>。<运镜：缓推（Push In, small, slow）>。<音效：心跳声> |
`

	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	sp := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(sp, []byte(script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"real","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)

	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots, _ := planShots(plan)
	if len(shots) != 2 {
		t.Fatalf("应 2 镜, got %d", len(shots))
	}
	// Format B:detailed_description 从 2 列表格提取,不得为空
	if shots[0].H3Prompt == "" || !strings.Contains(shots[0].H3Prompt, "Cinematic film still") {
		t.Fatalf("Format B shot1 h3_prompt 应为表格提取的 detailed_description, got: %q", shots[0].H3Prompt)
	}
	if shots[0].H3Prompt != "" && !strings.Contains(shots[0].H3Prompt, "苏念睁眼看见房梁") {
		t.Fatalf("Format B shot1 内容丢失: %q", shots[0].H3Prompt)
	}
	if shots[0].Duration != 6 {
		t.Fatalf("shot1 时长应为 6s, got %d", shots[0].Duration)
	}
	if !strings.Contains(shots[1].Dialogue, "苏薇") {
		t.Fatalf("shot2 台词丢失: %q", shots[1].Dialogue)
	}
}

// TestScriptAutoDurationCompensate 语音预算自动补偿(2026-08-26):台词+旁白总字数 ÷ 字速
// 超出脚本时长 → 自动延长(clamp 15s),防 H3 念一半切镜;时长充足不改动。
func TestScriptAutoDurationCompensate(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "EP01.md")
	// 镜1:4s 镜 + 41 字台词(41÷4≈10.25 → 需 11s);镜2:6s 镜 + 短台词(不动)
	script := `# 《测》分镜脚本 · 第001章_测试（第1集）

| 镜号 | 景别 | 运镜 | 画面内容 | 台词/旁白（带 ID） | 光影 | 音效 | 时长 |
|---|---|---|---|---|---|---|---|
| 01 | 中景 | 固定 | 甲站在大堂 | (S1)甲：` + strings.Repeat("今", 20) + `，` + strings.Repeat("日", 20) + `。 | 暖光 | 人声 | 4s |
| 02 | 近景 | 推 | 甲皱眉 | (S1)甲："不行。" | 冷光 | 风声 | 6s |

### Shot 01
` + tplBacktick + `
detailed_description: Cinematic. [Shot 1] 甲站在大堂。
` + tplBacktick + `

### Shot 02
` + tplBacktick + `
detailed_description: Cinematic. [Shot 2] 甲皱眉。
` + tplBacktick + `
`
	_ = os.WriteFile(sp, []byte(script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"paths":{"script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)
	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots, _ := planShots(plan)
	// 41 字符含 1 逗号+1 句号,有效发音字 40 → 40÷4=10s(标点不占语音时长)
	if shots[0].Duration != 10 {
		t.Fatalf("镜1 40 有效字 ÷4字每秒 应补偿到 10s, got %d", shots[0].Duration)
	}
	if shots[1].Duration != 6 {
		t.Fatalf("镜2 时长不应改动, got %d", shots[1].Duration)
	}
}

// TestScriptValidateShots 机械质检三项(2026-08-26):时间戳递减 / 时长全同 / 台词未同步六段式。
func TestScriptValidateShots(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	raws := []scriptShotRaw{
		{ID: 1, Duration: 5, Dialogue: "甲:你敢动她试试", H3Prompt: "detailed_description: [Shot 1] At 00:10.000 甲开口 <d>[中文]你敢动她试试</d>"},
		{ID: 2, Duration: 5, Dialogue: "甲:这句没进提示词", H3Prompt: "detailed_description: [Shot 2] At 00:05.000 别的内容"},
		{ID: 3, Duration: 5, H3Prompt: "detailed_description: [Shot 3] At 00:12.000"},
		{ID: 4, Duration: 5},
		{ID: 5, Duration: 5},
		{ID: 6, Duration: 5},
	}
	scriptValidateShots(raws, lg)
	manjuState.mu.Lock()
	logged := manjuState.log
	manjuState.mu.Unlock()
	if !strings.Contains(logged, "未严格晚于上一处") {
		t.Fatalf("镜2 时间戳 00:05 < 镜1 00:10 应报递减, log: %s", logged)
	}
	if !strings.Contains(logged, "节奏单一") {
		t.Fatalf("6 镜全 5s 应报时长全同, log: %s", logged)
	}
	if !strings.Contains(logged, "未同步进六段式") {
		t.Fatalf("镜2 台词不在提示词应报未同步, log: %s", logged)
	}
	// 镜1 台词在提示词里,不应出现在未同步告警对象中(以内容前缀判断)
	if strings.Contains(logged, "你敢动她试试") {
		t.Fatalf("已同步台词不应误报, log: %s", logged)
	}
	// 合规输入零告警
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	raws = []scriptShotRaw{
		{ID: 1, Duration: 4, Dialogue: "甲:好的", H3Prompt: "At 00:04.000 <d>[中文]好的</d>"},
		{ID: 2, Duration: 6, H3Prompt: "At 00:10.000"},
	}
	scriptValidateShots(raws, lg)
	manjuState.mu.Lock()
	logged = manjuState.log
	manjuState.mu.Unlock()
	if strings.Contains(logged, "机械质检") {
		t.Fatalf("合规输入应零机械质检告警, log: %s", logged)
	}
}

// TestSecondFormParse 双形态素材解析(2026-08-26):「真身提示词:」行内标注 / 双代码块标注。
func TestSecondFormParse(t *testing.T) {
	// ①行内标注
	chars := parseCharCards(`# 人物
## 1. 小白（灵宠，雪狐）
- 辨识度三件套：通体雪白 / 蓝瞳 / 尾尖一点朱红
- 生图提示词：Cinematic film still, a small white fox with blue eyes
- 真身提示词：Cinematic film still, a giant divine white fox true form with lightning marks
`, "real", false)
	if len(chars) == 0 {
		t.Fatalf("应解析出角色卡")
	}
	c := chars[0]
	if str(c["id"]) != "小白" {
		t.Fatalf("角色名应小白, got %q", str(c["id"]))
	}
	f2 := str(c["second_form"])
	if !strings.Contains(f2, "true form") {
		t.Fatalf("second_form 应含真身提示词(行内标注), got %q", f2)
	}
	if !strings.Contains(str(c["image_prompt"]), "small white fox") {
		t.Fatalf("主形象不应被真身覆盖, got %q", str(c["image_prompt"]))
	}

	// ②双代码块(第二块上方含「真身」字样)
	chars = parseCharCards(`# 人物
## 2. 小白（灵宠）
- 正向提示词：
`+tplBacktick+`
Cinematic film still, a small white fox
`+tplBacktick+`
- 真身（神话真身版）：
`+tplBacktick+`
Cinematic film still, a giant divine fox true form with storm fur
`+tplBacktick+`
`, "real", false)
	if len(chars) == 0 || str(chars[0]["second_form"]) == "" {
		t.Fatalf("双代码块真身应解析出 second_form, got %+v", chars)
	}
	// 无标注:不产生 second_form
	chars = parseCharCards(`# 人物
## 3. 阿拾（女主，19岁）
- 生图提示词：Cinematic film still, a young woman
`, "real", false)
	if len(chars) == 0 || str(chars[0]["second_form"]) != "" {
		t.Fatalf("无真身标注不应有 second_form, got %+v", chars)
	}
}

// TestParseCharCardsGlobalSection 回归测试(2026-08-26 用户实测):顶流遗产把
// 「统一风格前缀/统一质量后缀/通用负向词」三个全局共用字段写成二级标题,reMdTitle
// 匹配成角色卡 → 伪角色+伪视图资产。黑名单过滤后只出真角色,全局段不得进角色卡。
func TestParseCharCardsGlobalSection(t *testing.T) {
	text := `# 《顶流遗产》人物生成提示词（次世代3D渲染·BJD数字人风格）

> 全书统一视觉（用户指定）：次世代 3D 渲染 + 半写实国漫 AI 漫剧 + BJD 人偶质感

## 统一风格前缀（所有角色共用）
` + tplBacktick + `
Next-generation 3D render, semi-realistic Chinese anime CGI drama, virtual digital human, BJD doll aesthetic, cinematic film still
` + tplBacktick + `

## 统一质量后缀（所有角色共用）
` + tplBacktick + `
realistic skin texture with fine pores, soft facial lighting, cinematic lighting, 8K ultra detailed
` + tplBacktick + `

## 通用负向词（所有角色共用）
` + tplBacktick + `
hand-drawn, thick paint, japanese anime face, watermark, deformed, extra fingers
` + tplBacktick + `

---

## 1. 沈曜（主角·正角男=帅/冷峻）
**记忆点**：①右眼尾泪痣 ②黑曜石十二星芒戒指 ③黑金撞色穿搭

` + tplBacktick + `
Next-generation 3D render, a 23-year-old East Asian young man, teardrop mole at the corner of his right eye, black tactical jacket over white t-shirt
` + tplBacktick + `
**专属负向**：通用负向 + old face

## 2. 江晚吟（女主·正角女=美/冷艳）
**记忆点**：①发梢银灰挑染 ②左耳星形耳钉 ③黑白双面穿搭

` + tplBacktick + `
Next-generation 3D render, a 21-year-old East Asian young woman, phoenix eyes, long straight black hair with silver-grey dyed tips
` + tplBacktick + `

## 3. 顾承儒（主反派·伪善=磕碜阴鸷，禁帅美词）
**记忆点**：①鹰钩鼻+金丝眼镜三白眼 ②背头僵笑 ③三件套西装

` + tplBacktick + `
Next-generation 3D render, a 45-year-old East Asian man, hawk nose and cold eyes behind gold-rimmed glasses
` + tplBacktick + `
`
	chars := parseCharCards(text, "real", false)
	if len(chars) != 3 {
		t.Fatalf("应仅 3 个真角色(全局段不得当角色), got %d: %+v", len(chars), chars)
	}
	got := map[string]bool{}
	for _, c := range chars {
		id := str(c["id"])
		got[id] = true
		if reManjuGlobalSection.MatchString(id) {
			t.Fatalf("全局段「%s」不得进入角色卡", id)
		}
	}
	for _, want := range []string{"沈曜", "江晚吟", "顾承儒"} {
		if !got[want] {
			t.Fatalf("缺角色 %s, got %v", want, got)
		}
	}
}

// TestParseCharCardsQuoteGlobalNoNumber 回归测试:通用负向词写在 `>` 引用行(混沌灵根/
// 我死于第七集写法)不得产生伪角色;无编号角色标题(我死于第七集「## 阮棠(女主)」)必须正常解析。
func TestParseCharCardsQuoteGlobalNoNumber(t *testing.T) {
	text := `# 《我死于第七集》人物生成提示词(写实电影级·人物微动漫写实)

> 用途:漫剧定妆(NiliX 自动读取)。
> 通用负向词(所有角色共用,拼在各自负面词后):

` + tplBacktick + `
japanese anime face, japanese manga face, anime eyes, watermark
` + tplBacktick + `

---

## 阮棠(女主)
**记忆点**：①右耳垂痣 ②旧木簪 ③青灰围裙

` + tplBacktick + `
Cinematic film still, a 20-year-old East Asian woman, tiny mole on right earlobe, grey-blue apron
` + tplBacktick + `

## 阿铁(灵宠·剑灵)
**记忆点**：①通体雪白 ②蓝瞳 ③尾尖朱红

` + tplBacktick + `
Cinematic film still, a small white sword-spirit fox with blue eyes
` + tplBacktick + `
`
	chars := parseCharCards(text, "real", false)
	if len(chars) != 2 {
		t.Fatalf("引用行全局段不得产生伪角色,应 2 个角色, got %d: %+v", len(chars), chars)
	}
	if str(chars[0]["id"]) != "阮棠" || str(chars[1]["id"]) != "阿铁" {
		t.Fatalf("无编号角色标题应正常解析, got %v / %v", str(chars[0]["id"]), str(chars[1]["id"]))
	}
}

// TestScriptValidateAutoPatch 未同步台词自动补写(2026-08-26):分镜表有、六段式无 →
// 程序直接补 <d>/画外音,渲染含此句配音;重复校验不二次追加(幂等)。
func TestScriptValidateAutoPatch(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	raws := []scriptShotRaw{
		{ID: 1, Duration: 6, Dialogue: "苏薇:你听不到我的声音吗", Narration: "旁白：雨夜的风声掠过屋顶。",
			H3Prompt: "detailed_description: [Shot 1] At 00:06.000 已有的画面描述 <d>[中文]既有台词</d>"},
	}
	scriptValidateShots(raws, lg)
	manjuState.mu.Lock()
	logged := manjuState.log
	manjuState.mu.Unlock()
	if !strings.Contains(logged, "已自动补写") {
		t.Fatalf("未同步台词应触发自动补写, log: %s", logged)
	}
	p := raws[0].H3Prompt
	if !strings.Contains(p, "<d>你听不到我的声音吗</d>") {
		t.Fatalf("对白应补进 <d>: %s", p)
	}
	if !strings.Contains(p, "off-screen voiceover: <d>雨夜的风声掠过屋顶。</d>") {
		t.Fatalf("旁白应补画外音: %s", p)
	}
	if !strings.Contains(p, "<d>[中文]既有台词</d>") {
		t.Fatalf("既有对白不应被破坏: %s", p)
	}
	// 幂等:补后再校验,不再重复追加
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	scriptValidateShots(raws, lg)
	manjuState.mu.Lock()
	logged = manjuState.log
	manjuState.mu.Unlock()
	if strings.Contains(logged, "已自动补写") {
		t.Fatalf("补写后复核应零补写(幂等), log: %s", logged)
	}
	if strings.Count(raws[0].H3Prompt, "你听不到我的声音吗") != 1 {
		t.Fatalf("不应重复补写: %s", raws[0].H3Prompt)
	}
}

// 2026-08-30 ver14:内心·独白识别——「内心·角色名:内容」进 narration 保留「内心·」
// 前缀(渲染端 Q 版挂载/画外音音色差异化依赖该前缀),不再被当说话人建幽灵群演卡;
// 裸写(无 S 号)不再整句丢弃;光影/音效/风格列保留(此前解析后丢弃)。
func TestScriptParseInnerMonologueAndCols(t *testing.T) {
	script := `# 《测试》分镜脚本 · 第001章（第1集）

## 一、分镜表（正文→镜头）

| 镜号 | 景别 | 运镜 | 画面内容（可渲染） | 台词/旁白（带 ID） | 光影 | 音效 | 风格（可选） | 时长 |
|---|---|---|---|---|---|---|---|---|
| 01 | 特写 | 固定 | 【安步堂】化妆笔蘸粉,林九手腕悬空扫过嘴角 | 内心·林九:躺了一辈子,最后一次体面。 | 暖灯侧光 | 粉扑轻响 | real+magical | 4s |
| 02 | 近景 | 固定 | 【安步堂】林九低头补色 | (S1)林九:"大娘,您别动。" | 暖灯,镜面反光 | 调色刀当啷落盘 | real | 5s |

## 二、每镜 H3 提示词（六段式 Ref2VA）

### Shot 01 —— 特写
` + tplBacktick + `
subject_definitions: <Subject 1> is Lin Jiu in <Picture 1>.
summary: [reference generation] test.
detailed_description: [Shot 1] test.
overall_soundscape: test.
non_diegetic_music: N/A.
` + tplBacktick + `

### Shot 02 —— 近景
` + tplBacktick + `
subject_definitions: <Subject 1> is Lin Jiu in <Picture 1>.
summary: [reference generation] test.
detailed_description: [Shot 1] test.
overall_soundscape: test.
non_diegetic_music: N/A.
` + tplBacktick + `
`
	dir := t.TempDir()
	scriptDir := filepath.Join(dir, "script")
	_ = os.MkdirAll(scriptDir, 0o755)
	sp := filepath.Join(scriptDir, "EP01.md")
	_ = os.WriteFile(sp, []byte(script), 0o644)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"style":"real","paths":{"workdir":"`+filepath.ToSlash(dir)+`","script":"`+filepath.ToSlash(sp)+`"},"render":{}}`), 0o644)

	ctx, err := newManjuCtx(cfgPath, "EP01", "", "", "")
	if err != nil {
		t.Fatalf("newManjuCtx: %v", err)
	}
	lg := &manjuLogger{state: manjuState}
	plan, err := ctx.scriptParsePlan(lg)
	if err != nil {
		t.Fatalf("scriptParsePlan: %v", err)
	}
	shots, _ := planShots(plan)
	if len(shots) < 2 {
		t.Fatalf("镜头数不足: %d", len(shots))
	}
	// 内心·→ narration 保留前缀(渲染端 Q 版切换条件),不进 dialogue
	if !strings.Contains(shots[0].Narration, "内心·林九:躺了一辈子,最后一次体面") {
		t.Fatalf("内心·应进 narration 且保留前缀: %q", shots[0].Narration)
	}
	if strings.Contains(shots[0].Narration, "旁白") {
		t.Fatalf("内心不应被当旁白: %q", shots[0].Narration)
	}
	if shots[0].Dialogue != "" {
		t.Fatalf("内心不应进 dialogue: %q", shots[0].Dialogue)
	}
	// 镜2 对白正常解析
	if !strings.Contains(shots[1].Dialogue, "林九:大娘,您别动") {
		t.Fatalf("镜2 对白解析失败: %q", shots[1].Dialogue)
	}
	// 不建幽灵群演卡(「内心·林九」不得进 characters)
	planChars, _ := plan["characters"].([]any)
	for _, c := range planChars {
		if m, ok := c.(map[string]any); ok {
			if strings.Contains(str(m["id"]), "内心·") {
				t.Fatalf("内心·不应建幽灵群演卡: %s", str(m["id"]))
			}
		}
	}
	// 光影/音效/风格三列保留(ver14,此前正则捕获后丢弃)
	if shots[0].Light != "暖灯侧光" || shots[0].Sound != "粉扑轻响" {
		t.Fatalf("光影/音效列未保留: light=%q sound=%q", shots[0].Light, shots[0].Sound)
	}
	if shots[0].Style != "real+magical" {
		t.Fatalf("9 列风格列未保留: %q", shots[0].Style)
	}
	if shots[1].Style != "real" {
		t.Fatalf("镜2 风格列未保留: %q", shots[1].Style)
	}
}

// TestScriptDedupShotLines 跨镜串句/镜内重复去重(2026-09-01 配音重复根治):
//  ① 总览镜串句:镜1 h3 写完整对话(含镜2/3 台词)→ 删除镜1 中属镜2/3 的 <d> 句,
//     连带引导语,不残留悬空英文;
//  ② 本镜权威台词保留(镜1 自己的台词不误删);
//  ③ 即兴台词(任何镜台词列都没有)→ 保留;
//  ④ 镜内半角/全角双版本 → 只留第一处;
//  ⑤ 短词「无」(归一化后 1 字)→ 不参与判定,保留;
//  ⑥ 幂等:二次执行无变化。
func TestScriptDedupShotLines(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	raws := []scriptShotRaw{
		{ID: 1, Dialogue: "(S1)老赵:\"今儿大比,你磨它干啥。\"",
			H3Prompt: "detailed_description: Old Zhao speaks, his voice rough and low: <d>[Chinese]今儿大比,你磨它干啥。</d> Jiang Que does not pause his sharpening. He replies without looking up, his tone flat: <d>[Chinese]测完灵,下午还有三十捆柴。</d> He then stops the whetstone, lifts his head, and adds calmly: <d>[Chinese]天塌下来,柴还是要劈的。</d> Old Zhao's bowl pauses mid-air."},
		{ID: 2, Dialogue: "(S2)姜缺:\"测完灵,下午还有三十捆柴。\"",
			H3Prompt: "detailed_description: <d>[Chinese]测完灵,下午还有三十捆柴。</d>"},
		{ID: 3, Dialogue: "(S2)姜缺:\"天塌下来,柴还是要劈的。\"",
			H3Prompt: "detailed_description: He adds calmly: <d>[Chinese]天塌下来,柴还是要劈的。</d>"},
	}
	scriptDedupShotLines(raws, lg)
	// ① 镜1 串句删除(两句都删)
	if strings.Contains(raws[0].H3Prompt, "测完灵") {
		t.Fatalf("镜1 应删除属镜2 的串句, got: %s", raws[0].H3Prompt)
	}
	if strings.Contains(raws[0].H3Prompt, "天塌下来") {
		t.Fatalf("镜1 应删除属镜3 的串句, got: %s", raws[0].H3Prompt)
	}
	// ② 镜1 自己的台词保留
	if !strings.Contains(raws[0].H3Prompt, "今儿大比") {
		t.Fatalf("镜1 自己的台词应保留, got: %s", raws[0].H3Prompt)
	}
	// 引导语不残留("He replies" 应随串句一起删)
	if strings.Contains(raws[0].H3Prompt, "He replies without looking up") {
		t.Fatalf("串句引导语应一并删除, got: %s", raws[0].H3Prompt)
	}
	// ② 镜2/3 自己的台词保留
	if !strings.Contains(raws[1].H3Prompt, "测完灵") || !strings.Contains(raws[2].H3Prompt, "天塌下来") {
		t.Fatalf("镜2/3 权威台词应保留")
	}
	// 日志有串句删除记录
	manjuState.mu.Lock()
	logged := manjuState.log
	manjuState.mu.Unlock()
	if !strings.Contains(logged, "配音去重") {
		t.Fatalf("应有去重日志, log: %s", logged)
	}
}

func TestScriptDedupShotLinesEdge(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.log = ""
	manjuState.mu.Unlock()
	lg := &manjuLogger{state: manjuState}
	// ④ 镜内半角/全角双版本 + ⑤ 短词「无」保留 + ⑥ 幂等
	raws := []scriptShotRaw{
		{ID: 1, Dialogue: "(S1)解说:\"壹万号一拳!\"",
			H3Prompt: "detailed_description: 解说喊: <d>[Chinese]壹万号一拳!</d> 全场欢呼: <d>[Chinese]壹万号一拳!</d> 又喊 <d>[Chinese]壹万号一拳!</d>"},
		{ID: 2, Dialogue: "(S1)姜缺:\"无\"\n(S1)姜缺:\"无\"",
			H3Prompt: "detailed_description: 姜缺答: <d>[Chinese]无</d> <d>[Chinese]无</d>"},
		{ID: 3, Dialogue: "",
			H3Prompt: "detailed_description: 群众呼喊: <d>[Chinese]哎哟…疼…</d>"},
	}
	scriptDedupShotLines(raws, lg)
	// ④ 镜内重复只留第一处
	if n := strings.Count(raws[0].H3Prompt, "壹万号一拳"); n != 1 {
		t.Fatalf("镜1 同镜重复应只留 1 处, got %d: %s", n, raws[0].H3Prompt)
	}
	// ⑤ 短词「无」:台词列声明过 → 镜内去重只留第一处(dialogue 有两行「无」,h3 两处
	// 都算本镜权威,但镜内重复仍去重);归一化 1 字不参与跨镜,不会误删其它镜
	if n := strings.Count(raws[1].H3Prompt, "<d>[Chinese]无</d>"); n != 1 {
		t.Fatalf("镜2 同镜重复「无」应只留 1 处, got %d: %s", n, raws[1].H3Prompt)
	}
	// ③ 即兴台词保留
	if !strings.Contains(raws[2].H3Prompt, "哎哟") {
		t.Fatalf("即兴台词不应删除, got: %s", raws[2].H3Prompt)
	}
	// ⑥ 幂等:再次执行无变化(与首次执行后的结果一致)
	first := append([]scriptShotRaw{}, raws...)
	scriptDedupShotLines(raws, lg)
	for i := range raws {
		if raws[i].H3Prompt != first[i].H3Prompt {
			t.Fatalf("幂等失败(镜 %d): %q != %q", i+1, raws[i].H3Prompt, first[i].H3Prompt)
		}
	}
}

// TestParseInnerMultiLine 内心段多行延续(2026-09-02 王牌三岁半镜11 实锤):
// 「内心·棠棠:"豆豆…,"\n"怎么只有一个冰凉的环。"」→ 两行都归 narration,
// 不把无前缀延续行错拆进 dialogue。
func TestParseInnerMultiLine(t *testing.T) {
	text := `{
  "book":"王牌三岁半","episode":1,"chapter_title":"第1章","global_style":"s",
  "shots":[{
    "shot_id":11,"shot_size":"中景","camera":"固定","action":"棠棠站在台上",
    "dialogue":"内心·棠棠：\"豆豆明明说测试台上有糖发，\"\n\"怎么只有一个冰凉的环。\"",
    "light":"","sound":"","duration":6,"characters":["棠棠"],
    "h3_prompt":"subject_definitions:\n<Subject 1> is Tangtang in <Picture 1>.\n\ndetailed_description:\nX\n"
  }]
}`
	raws, err := parseScriptJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) != 1 {
		t.Fatalf("应 1 镜, got %d", len(raws))
	}
	r := raws[0]
	if !strings.Contains(r.Narration, "豆豆明明说") || !strings.Contains(r.Narration, "怎么只有一个冰凉的环") {
		t.Fatalf("内心两行都应进 narration: %q", r.Narration)
	}
	if strings.Contains(r.Narration, "内心·棠棠：\n\"怎么") {
		// 允许:两行各自保留,但都必须带内心语义(前缀只在首行)
	}
	if r.Dialogue != "" {
		t.Fatalf("内心延续行不应进 dialogue: %q", r.Dialogue)
	}
}
