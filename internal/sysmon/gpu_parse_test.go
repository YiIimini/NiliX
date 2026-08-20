package sysmon

import (
	"testing"
)

// 回归:GPUInfo 解析 nvidia-smi 输出——部分驱动忽略 --nounits 输出带 %/MiB
// (如 "5 %"、"20987 MiB"),旧 strconv.ParseFloat 直接失败 → 温度/显存错乱("数据不对")。
func TestGPUInfoParseUnits(t *testing.T) {
	cases := map[string]float64{
		"5 %":        5,
		"20987 MiB":  20987,
		"46":         46,
		"12.5":       12.5,
		" 5 % ":      5,
		"24463 MiB ": 24463,
		"":           0,
		"abc":        0,
	}
	for in, want := range cases {
		if got := parseFloatClean(in); got != want {
			t.Errorf("parseFloatClean(%q) = %v, want %v", in, got, want)
		}
	}
}

// 回归:GPUInfo 实际查询(真实 nvidia-smi,验证温度字段正确且在合理范围)
func TestGPUInfoRealTemp(t *testing.T) {
	g := GPUInfo()
	if !g.Present {
		t.Skip("无 NVIDIA GPU")
	}
	if g.Temp <= 0 || g.Temp > 120 {
		t.Fatalf("温度异常: %.1f (usage=%.1f mem=%s/%s)", g.Temp, g.Usage, g.MemUsed, g.MemTotal)
	}
	if g.Usage < 0 || g.Usage > 100 {
		t.Fatalf("usage 越界: %.1f", g.Usage)
	}
}
