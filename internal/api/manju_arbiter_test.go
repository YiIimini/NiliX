package api

import (
	"os"
	"path/filepath"
	"testing"

	"nilix/internal/agent"
)

// fakeArbiterLLM 伪终审 LLM(固定 decision/reason)
type fakeArbiterLLM struct{ decision, reason string }

func (f fakeArbiterLLM) ChatJSON(system, user string, temperature float64) (map[string]any, error) {
	return map[string]any{"decision": f.decision, "reason": f.reason}, nil
}

// TestArbiterDecide 终审决策解析:accept/regenerate/非法值
func TestArbiterDecide(t *testing.T) {
	jd := &agent.Judgment{
		Score: 68, Retries: 2,
		Dimensions: map[string]float64{"identity": 82, "camera": 55, "action": 61},
		Issues:     []string{"运镜与分镜不符", "动作幅度偏小"},
	}
	meta := agent.ShotMeta{ShotID: 3, Scene: "书院", Camera: "缓慢推近", HasChar: true}
	if d, r, err := agent.ArbiterDecide(fakeArbiterLLM{"accept", "轻微偏差重渲收益低"}, meta, jd, 75, 2); err != nil || d != "accept" || r != "轻微偏差重渲收益低" {
		t.Errorf("accept 决策异常: %v %s %s", err, d, r)
	}
	if d, _, err := agent.ArbiterDecide(fakeArbiterLLM{"regenerate", "主体错乱需重写"}, meta, jd, 75, 2); err != nil || d != "regenerate" {
		t.Errorf("regenerate 决策异常: %v %s", err, d)
	}
	if _, _, err := agent.ArbiterDecide(fakeArbiterLLM{"maybe", "?"}, meta, jd, 75, 2); err == nil {
		t.Errorf("非法决策应报错")
	}
}

// TestMarkAutoAccepted 终审接受落盘:状态 accepted + Arbiter 记录 + 既有升级自动解除
func TestMarkAutoAccepted(t *testing.T) {
	proj := "zz_arbiter_test"
	dir := filepath.Join(manjuRoot, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"render":{"width":768,"height":1344}}`), 0644)
	ctx := &manjuCtx{project: proj, episode: "EP01"}
	// 预置:判分 failed + 未解决升级
	manjuAgentMu.Lock()
	st := loadAgentStateLocked(proj)
	st.Episode = "EP01"
	st.Shots["3"] = &agent.Judgment{Status: "failed", Score: 66, Retries: 2}
	st.Escalations = []manjuAgentEscalation{{EP: "EP01", Shot: 3, Score: 66, Reason: "判分 66 未达 75"}}
	saveAgentStateLocked(proj, st)
	manjuAgentMu.Unlock()

	ctx.markAutoAccepted(3, "终审:自动接受(轻微偏差重渲收益低)")

	st2 := loadAgentState(proj)
	j := st2.Shots["3"]
	if j == nil || j.Status != "accepted" || j.Arbiter == "" {
		t.Fatalf("终审接受未落盘: %+v", j)
	}
	if len(st2.Escalations) != 1 || !st2.Escalations[0].Resolved || st2.Escalations[0].Action != "auto-accept" {
		t.Fatalf("既有升级应自动解除: %+v", st2.Escalations)
	}
	// 摘要不再把该镜列为待处理升级
	sum := agentStatusSummary(filepath.Join(dir, "config.json"))
	if sum["escalationCount"].(int) != 0 {
		t.Errorf("升级计数应归零: %v", sum["escalationCount"])
	}
}

// TestAgentAutoResolveConfig auto_resolve 配置链:默认开 → 项目关 → 摘要回读
func TestAgentAutoResolveConfig(t *testing.T) {
	proj := "zz_arb_cfg_test"
	dir := filepath.Join(manjuRoot, proj)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"render":{"width":768,"height":1344}}`), 0644)

	// 默认开
	ctx, _ := newManjuCtx(cfgPath, "EP01", "", "", "")
	if !loadAgentCfg(ctx).AutoResolve {
		t.Errorf("auto_resolve 默认应开启")
	}
	// 项目关闭
	w, _ := doReq(t, "POST", "/api/manju/agent/settings", map[string]any{
		"config": cfgPath, "agent": map[string]any{"enabled": true, "auto_resolve": false},
	})
	if w.Code != 200 {
		t.Fatalf("保存 HTTP %d: %s", w.Code, w.Body.String())
	}
	ctx2, _ := newManjuCtx(cfgPath, "EP01", "", "", "")
	if loadAgentCfg(ctx2).AutoResolve {
		t.Errorf("项目显式关闭未生效")
	}
	// 摘要回读(供设置开关回显)
	if sum := agentStatusSummary(cfgPath); sum["autoResolve"] != false {
		t.Errorf("摘要未回读 autoResolve: %v", sum["autoResolve"])
	}
}
