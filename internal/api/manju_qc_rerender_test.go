package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandShotList(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"   ":     "",
		"7":       "7",
		"1,2":     "1,2",
		"1-3":     "1,2,3",
		"1-3,5":   "1,2,3,5",
		"3-1":     "1,2,3", // 倒序自动矫正
		"1,3-5,9": "1,3,4,5,9",
		"a,b":     "",
	}
	for in, want := range cases {
		if got := expandShotList(in); got != want {
			t.Errorf("expandShotList(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQCReportFailedShots(t *testing.T) {
	dir := t.TempDir()
	ctx := &manjuCtx{
		workdir: dir,
		episode: "EP01",
	}
	path := ctx.qcReportPath()
	if got := ctx.qcFailedShots(); got != nil {
		t.Fatalf("无报告应返回 nil, got %v", got)
	}
	// 写入混合报告:07 失败、02 通过、无扩展名记录忽略
	rep := map[string]any{"shots": map[string]any{
		"01.mp4": map[string]any{"ok": true},
		"02.mp4": map[string]any{"ok": false, "flags": []string{"近黑帧60%"}},
		"07.mp4": map[string]any{"ok": false},
	}}
	data, _ := json.Marshal(rep)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	got := ctx.qcFailedShots()
	if len(got) != 2 || !got[2] || !got[7] || got[1] {
		t.Fatalf("失败镜头应为 {2,7}, got %v", got)
	}
	// 清除后为空
	ctx.qcReportClear()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("清除后报告应被删除: %v", err)
	}
	// 损坏报告返回 nil 不 panic
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.WriteFile(path, []byte("{bad json"), 0644)
	if got := ctx.qcFailedShots(); got != nil {
		t.Fatalf("损坏报告应返回 nil, got %v", got)
	}
}

func TestQCEscapeHatch(t *testing.T) {
	dir := t.TempDir()
	R := map[string]any{"qc_skip_shots": "3, 7"}
	cfg := map[string]any{"render": R}
	ctx := &manjuCtx{cfg: cfg, R: R, workdir: dir, episode: "EP01"}

	// qcSkipSet 解析
	skip := ctx.qcSkipSet()
	if len(skip) != 2 || !skip[3] || !skip[7] {
		t.Fatalf("qcSkipSet 应为 {3,7}, got %v", skip)
	}
	// qcSkipList 排序输出
	if l := ctx.qcSkipList(); l != "3,7" {
		t.Fatalf("qcSkipList = %q, want 3,7", l)
	}
	// qcAccept 默认 false
	if ctx.qcAccept() {
		t.Fatal("qc_accept 未设置时应为 false")
	}
	// qcAcceptClear 无标记时不报错
	ctx.qcAcceptClear()

	// 写 qc_accept 后 accept 为 true
	R["qc_accept"] = true
	if !ctx.qcAccept() {
		t.Fatal("qc_accept=true 时应为 true")
	}
	ctx.qcAcceptClear()
	if ctx.qcAccept() {
		t.Fatal("qcAcceptClear 后应清除标记")
	}
	if _, ok := R["qc_accept"]; ok {
		t.Fatal("qcAcceptClear 应从 render 删除 qc_accept")
	}
}

func TestSageAttnGuard(t *testing.T) {
	state := &manjuTask{}
	lg := newManjuLogger(state, nil, "p", "EP01")
	// 1. 开关关闭:不探测不降级(sageChecked 保持 false)
	// (sageEnabled 缺省开启的语义变更后,「关闭」须显式 false——空 R 现按开启处理)
	ctx := &manjuCtx{R: map[string]any{"sage_attention": false}}
	ctx.sageAttnGuard(lg)
	if ctx.sageChecked {
		t.Fatal("sage_attention 关闭时不应探测节点")
	}
	// 2. 开关开 + 节点缺失(连接不上的地址 → hasNode false):自动降级
	// (审计 3.3:降级结果在 ctx 字段,applySageToR 注入 R 副本——不直接写共享 R)
	ctx2 := &manjuCtx{R: map[string]any{"sage_attention": true}, comfy: newComfyClient("http://127.0.0.1:1")}
	ctx2.sageAttnGuard(lg)
	if !ctx2.sageChecked {
		t.Fatal("开关开启时应探测一次")
	}
	if ctx2.sageOK || ctx2.sageNodeName != "" {
		t.Fatal("节点缺失时 sageOK 应为 false")
	}
	rCopy := map[string]any{"sage_attention": true}
	ctx2.applySageToR(rCopy)
	if b, _ := rCopy["sage_attention"].(bool); b {
		t.Fatal("applySageToR 应在副本上关闭 sage_attention")
	}
	// 3. 已探测过:不再重复探测(再次调用不改变状态)
	ctx2.sageAttnGuard(lg)
	if !ctx2.sageChecked || ctx2.sageOK {
		t.Fatal("已探测后不应再次探测")
	}
}
