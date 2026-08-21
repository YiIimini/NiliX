package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 审计 P2 回归:条件缓存指纹必须包含 FL2VA 尾帧维度——
// fl2va_end_frame 开关切换 / 尾帧图变更后指纹变化(旧缓存失效,不串用双帧/单帧模式)
func TestShotFingerprintIncludesFl2vaEndFrame(t *testing.T) {
	dir := t.TempDir()
	scenes := filepath.Join(dir, "assets", "scenes")
	_ = os.MkdirAll(scenes, 0755)
	_ = os.WriteFile(filepath.Join(scenes, "s.png"), []byte("scene"), 0644)
	_ = os.WriteFile(filepath.Join(scenes, "s_end.png"), []byte("end1"), 0644)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets"), w: 768, h: 1344, fps: 24}

	shot := manjuShot{Scene: "s", Duration: 4, H3Prompt: "p"}
	// 开关关闭
	ctx.R = map[string]any{"fl2va_end_frame": false}
	fpOff := ctx.shotCondFingerprintAt(shot, 768, 1344)
	// 开关开启(空镜 + 有尾帧)
	ctx.R = map[string]any{"fl2va_end_frame": true}
	fpOn := ctx.shotCondFingerprintAt(shot, 768, 1344)
	if fpOff == fpOn {
		t.Fatalf("fl2va_end_frame 开关应改变指纹: off=%s on=%s", fpOff, fpOn)
	}
	// 尾帧图内容变化 → 指纹变化(用不同大小内容,规避同 mtime 粒度下 size 相同)
	_ = os.WriteFile(filepath.Join(scenes, "s_end.png"), []byte("end2-with-different-length"), 0644)
	fpOn2 := ctx.shotCondFingerprintAt(shot, 768, 1344)
	if fpOn == fpOn2 {
		t.Fatalf("尾帧图变更应改变指纹: %s", fpOn)
	}
	// 有角色镜头:不启用尾帧(Ref2VA 无此概念),指纹不含尾帧维度
	shot2 := manjuShot{Scene: "s", Characters: []string{"甲"}, Duration: 4, H3Prompt: "p"}
	ctx.R = map[string]any{"fl2va_end_frame": true}
	fpChar := ctx.shotCondFingerprintAt(shot2, 768, 1344)
	ctx.R = map[string]any{"fl2va_end_frame": false}
	fpCharOff := ctx.shotCondFingerprintAt(shot2, 768, 1344)
	if fpChar != fpCharOff {
		t.Fatalf("有角色镜头不应受尾帧开关影响: %s vs %s", fpChar, fpCharOff)
	}
}

// 审计 F1 回归:newManjuCtx 白名单扩张不再把小说库根外的 novel 目录注册进 fs 白名单
func TestNewManjuCtxSkipsOutsideNovelFSRoot(t *testing.T) {
	oldNovel := NovelRootDir
	root := t.TempDir()
	NovelRootDir = root
	defer func() { NovelRootDir = oldNovel }()

	dir := t.TempDir()
	// 越界 novel 放在工作目录之外(否则被 workdir 白名单覆盖,测不出 novel 自身注册逻辑)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.md")
	_ = os.WriteFile(outside, []byte("x"), 0644)
	cfgPath := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"paths": map[string]any{
			"workdir": dir, "novel": outside,
		},
	}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, b, 0644)

	ctx, err := newManjuCtx(cfgPath, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 越界 novel 不得被加入 fs 白名单
	if fsPathAllowed(outside) {
		t.Fatalf("小说库根外的 novel 不应进入 fs 白名单: %s", outside)
	}
	_ = ctx
}
