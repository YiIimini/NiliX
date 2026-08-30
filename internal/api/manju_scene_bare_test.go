package api

import (
	"strings"
	"testing"
)

// 2026-08-29 绿萝全书实锤回归:场景提示词第三代格式——纯名字标题「## 星海大厦外景」
// (无编号无括号)。旧解析只认「场景N·名字(括号)」/「编号. 名字(括号)」两代格式,
// 32 张卡一张不匹配 → scenes=0,25 镜全无场景图,同场景跨镜长相漂移;文件里唯二带括号
// 的标题是两张重复的封面备用卡,匹配后又被封面过滤拦掉,日志只剩「过滤 2 张」极具迷惑性。
func TestParseSceneCardsBareTitles(t *testing.T) {
	md := "# 《别惹这盆绿萝》场景提示词(写实电影级 · 卡名=分镜【场景名】逐字锚定)\n\n" +
		"# 统一风格(所有场景共用)\n\nshared style line\n\n" +
		"# 场景卡\n\n" +
		"## 星海大厦外景\n```\na modern 38-story glass skyscraper at dusk, cold blue glass curtain wall, Cinematic\n```\n> 冷蓝玻璃×暖色天光,全书主舞台标志。\n\n" +
		"## 走廊绿萝角\n```\na long office corridor at night, fluorescent lights half dimmed, Cinematic\n```\n> 阿碧的\"家\";走廊尽头=信息交汇点。\n\n" +
		"## 封面备用卡·绿萝与巨影(仅封面用,禁入正片)\n```\na giant pothos shadow over the city, Cinematic\n```\n\n" +
		"## 备忘\n这里没有提示词代码块,不应被收进卡池。\n"
	cards := parseSceneCards(md, "", false)
	if len(cards) != 3 {
		ids := make([]string, 0, len(cards))
		for _, c := range cards {
			id, _ := c["id"].(string)
			ids = append(ids, id)
		}
		t.Fatalf("纯名字卡应解析 3 张(两张正片+一张封面备用),实收 %d:%v", len(cards), ids)
	}
	want := map[string]bool{"星海大厦外景": false, "走廊绿萝角": false}
	dropped := false
	for _, c := range cards {
		id, _ := c["id"].(string)
		if _, ok := want[id]; ok {
			want[id] = true
		}
		desc, _ := c["description"].(string)
		if manjuSceneDropped(id, desc) {
			dropped = true
		}
		if strings.Contains(id, "备忘") {
			t.Errorf("无提示词代码块的纯名字节被误收进卡池:%s", id)
		}
	}
	for id, got := range want {
		if !got {
			t.Errorf("第三代纯名字场景卡 %q 未被解析", id)
		}
	}
	if !dropped {
		t.Errorf("封面备用卡未被 manjuSceneDropped 拦截(usableScenes 过滤层失效)")
	}
	// 前两代格式回归:带括号编号卡仍走原路径,不受兜底分支影响
	old := "## 1. 晚膳小馆（主角主场）\n```\na small restaurant, Cinematic\n```\n"
	oc := parseSceneCards(old, "", false)
	if len(oc) != 1 {
		t.Fatalf("第二代编号格式解析退化:%d 张", len(oc))
	}
	if id, _ := oc[0]["id"].(string); id != "晚膳小馆" {
		t.Errorf("第二代卡名解析错误:%q", id)
	}
}

// 预期槽位纯函数规则(2026-08-29 槽位时机修复):plan 汇点在 assets 阶段之前,不能
// 探测文件——实测恒 0 会把全部 <Picture N> 引用剥光并回写固化进 plan。
func TestManjuExpectPicSlots(t *testing.T) {
	cases := []struct {
		name  string
		chars []string
		scene string
		want  int
	}{
		{"空镜无场景", nil, "", 0},
		{"单角色", []string{"苏苗苗"}, "", 3},    // front/full/detail
		{"双角色", []string{"苏苗苗", "小圆"}, "", 4}, // 各 front+full
		{"三角色", []string{"a", "b", "c"}, "", 4},  // 主角 front+full+其余各 front
		{"四角色截断", []string{"a", "b", "c", "d"}, "", 4},
		{"单角色+场景图", []string{"苏苗苗"}, "走廊绿萝角", 4},
		{"纯空镜+场景图", nil, "走廊绿萝角", 1},
	}
	for _, c := range cases {
		got := manjuExpectPicSlots(manjuShot{Characters: c.chars, Scene: c.scene})
		if got != c.want {
			t.Errorf("%s: expect %d, got %d", c.name, c.want, got)
		}
	}
}

// plan 汇点用预期槽位 finalize 时,人物图引用必须保留(绿萝 EP01 25 镜 Picture 引用
// 在 plan 阶段被实测 0 槽剥光的根因回归);0 槽剥除逻辑本身保持(超界引用仍须剥)。
func TestPlanStageFinalizeKeepsPicRefs(t *testing.T) {
	hp := "subject_definitions:\n" +
		"<Subject 1> is Su Miaomiao in <Picture 1>, a young horticulture worker in a denim apron.\n" +
		"<Subject 2> is the receptionist Xiao Yuan in <Picture 2>, holding a cup of water.\n"
	s := manjuShot{ID: 9, Characters: []string{"苏苗苗", "小圆"}}
	out := manjuFinalizePromptPure(hp, true, manjuExpectPicSlots(s))
	if !strings.Contains(out, "<Picture 1>") || !strings.Contains(out, "<Picture 2>") {
		t.Fatalf("plan 汇点(预期槽位=4)不应剥人物图引用:\n%s", out)
	}
	// 病灶对照:实测 0 槽(旧汇点行为)会把引用全部剥除
	old := manjuFinalizePromptPure(hp, true, 0)
	if strings.Contains(old, "<Picture") {
		t.Fatalf("0 槽应剥除全部引用(超界剥除逻辑本身保持正确)")
	}
}
