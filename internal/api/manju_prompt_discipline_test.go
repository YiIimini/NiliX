package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 角色一致性纪律:逐镜提示词禁止未登场角色入画、说话人必须视觉中心、Subject 只能引用有参考图的角色。
func TestShotPromptRoleDiscipline(t *testing.T) {
	sys := manjuShotPromptSystem(true, "2.5d+real+ink")
	for _, want := range []string{"角色纪律", "主角中心", "ref_available"} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuShotPromptSystem 缺少纪律 %q", want)
		}
	}
	if !strings.Contains(sys, "in <Picture 1>") || !strings.Contains(sys, "清单外的登场角色") {
		t.Errorf("Ref2VA 模板缺少参考图纪律")
	}
}

func TestDirectSystemRoleDiscipline(t *testing.T) {
	sys := manjuDirectSystem(map[string]any{}, "2.5d+real")
	for _, want := range []string{"分镜纪律", "必须列全", "视觉中心主体"} {
		if !strings.Contains(sys, want) {
			t.Errorf("manjuDirectSystem 缺少分镜纪律 %q", want)
		}
	}
}

// shotRefRoles:只返回有参考图(正脸优先,回退全身)的角色,≤3 与 charRefNames 同限。
func TestShotRefRoles(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets", "characters")
	_ = os.MkdirAll(assets, 0755)
	// 陈鱼 有正脸;柳如烟 只有全身;葛长老 无资产;守山弟子 排第 4 被上限截断
	_ = os.WriteFile(filepath.Join(assets, "陈鱼_face.png"), []byte("f"), 0644)
	_ = os.WriteFile(filepath.Join(assets, "柳如烟.png"), []byte("f"), 0644)
	ctx := &manjuCtx{assetsDir: filepath.Join(dir, "assets")}
	s := manjuShot{Characters: []string{"陈鱼", "柳如烟", "葛长老", "守山弟子"}}
	got := ctx.shotRefRoles(s)
	want := []string{"陈鱼", "柳如烟"}
	if len(got) != len(want) {
		t.Fatalf("shotRefRoles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shotRefRoles = %v, want %v", got, want)
		}
	}
}
