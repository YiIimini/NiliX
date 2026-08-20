package sysmon

import (
	"testing"
	"time"
)

// 回归:系统监测 /api/stats 响应必须快——SharedGPUMem(PowerShell CIM)单次约 1.2s,
// 若随每次 GPU 刷新执行,前端 2s 轮询会被拖到 1s+ 响应(系统监测永远转圈)。
// 这里验证缓存语义:连续 Snapshot 命中 GPU/共享显存缓存,总耗时远小于单次慢查询。
func TestSnapshotFastOnCacheHit(t *testing.T) {
	c := NewCollector()
	_ = c.Snapshot() // 首次(含慢查询,仅做初始化)
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		_ = c.Snapshot()
	}
	elapsed := time.Since(t0)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("缓存命中 Snapshot 过慢: %v(>500ms),共享显存查询可能未独立缓存", elapsed)
	}
}
