package sysmon

import (
	"testing"
)

// 回归:GPU 显存使用率(memPercent)——渲染时显存常满而算力利用率波动,
// 前端以此显示"占用 X/Y GB (N%)",缺失时用户只见占用无百分比
func TestGPUInfoMemPercent(t *testing.T) {
	g := GPUInfo()
	if !g.Present {
		t.Skip("无 NVIDIA GPU,跳过")
	}
	if g.MemPercent <= 0 || g.MemPercent > 100 {
		t.Fatalf("memPercent 异常: %.1f (used=%s total=%s)", g.MemPercent, g.MemUsed, g.MemTotal)
	}
	// 显存占用>0 时百分比必须 >0
	if g.MemUsed != "0 B" && g.MemPercent == 0 {
		t.Fatalf("显存已占用但 memPercent=0")
	}
}

// 回归:Snapshot 的 GPU 字段含 memPercent(灵动岛/工作台 /api/stats 数据源)
func TestSnapshotGPUMemPercentField(t *testing.T) {
	c := NewCollector()
	s := c.Snapshot()
	if !s.GPU.Present {
		t.Skip("无 NVIDIA GPU,跳过")
	}
	if s.GPU.MemPercent <= 0 || s.GPU.MemPercent > 100 {
		t.Fatalf("Snapshot.memPercent 异常: %.1f", s.GPU.MemPercent)
	}
}
