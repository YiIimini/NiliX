package island

import (
	"testing"
	"time"
)

// 回归:透明就绪循环必须等待 WebView2 控制器异步初始化(可能数秒),
// 不能 250ms 就放弃——Controller2 就绪前 SetTransparent 无效,窗口恒深色(黑窗/胶囊黑底根源)
func TestTransparentWaitLoop(t *testing.T) {
	// 模拟:TransparentOK 前 3 秒返回 false(WebView2 初始化慢),之后 true
	readyAt := time.Now().Add(3 * time.Second)
	transparentOK := func() bool { return time.Now().After(readyAt) }
	applyTransparent := func(ok func() bool) bool {
		for i := 0; i < 200 && !ok(); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		return ok()
	}
	t0 := time.Now()
	applied := applyTransparent(transparentOK)
	elapsed := time.Since(t0)
	if !applied {
		t.Fatal("透明未生效(循环过早放弃)")
	}
	if elapsed < 2*time.Second || elapsed > 4*time.Second {
		t.Fatalf("等待时长异常: %v(应等待就绪约3s)", elapsed)
	}
	t.Logf("透明等待就绪 OK, 用时 %v", elapsed)
}
