package main

import "testing"

// TestMainWinStateValid 窗口记忆值可信度判定:
// 关闭/最小化瞬间的失效值(160x28、-32000,-32000)必须被拒,
// 真实可见尺寸必须放行(含贴屏幕边缘的负坐标)。
func TestMainWinStateValid(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		x, y int
		want bool
	}{
		{"关闭瞬间失效值", 160, 28, -32000, -32000, false}, // 实测污染值
		{"尺寸过小", 399, 300, 80, 40, false},
		{"高度过小", 1400, 299, 80, 40, false},
		{"位置屏幕外", 1400, 900, -32000, 40, false},
		{"正常值", 1400, 900, 80, 40, true},
		{"最小可视尺寸", 400, 300, 0, 0, true},
		{"负坐标但合法(多屏/负屏距)", 1200, 800, -500, -300, true},
	}
	for _, c := range cases {
		if got := mainWinStateValid(c.w, c.h, c.x, c.y); got != c.want {
			t.Errorf("%s: mainWinStateValid(%d,%d,%d,%d)=%v, want %v",
				c.name, c.w, c.h, c.x, c.y, got, c.want)
		}
	}
}
