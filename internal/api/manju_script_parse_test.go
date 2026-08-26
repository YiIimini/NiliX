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
`, "real")
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
`, "real")
	if len(chars) == 0 || str(chars[0]["second_form"]) == "" {
		t.Fatalf("双代码块真身应解析出 second_form, got %+v", chars)
	}
	// 无标注:不产生 second_form
	chars = parseCharCards(`# 人物
## 3. 阿拾（女主，19岁）
- 生图提示词：Cinematic film still, a young woman
`, "real")
	if len(chars) == 0 || str(chars[0]["second_form"]) != "" {
		t.Fatalf("无真身标注不应有 second_form, got %+v", chars)
	}
}
