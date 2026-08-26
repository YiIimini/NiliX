package sysmon

// 真机通道验证(雷神 EC/WMI + NVAPI):go test -v -run Live ./internal/sysmon/
// 只读,不写硬件;CI/异机型自动跳过。

import (
	"testing"
	"unsafe"
)

func TestLiveNVStructureSize(t *testing.T) {
	if got := unsafe.Sizeof(nvPstates20{}); got != 7416 {
		t.Fatalf("nvPstates20 布局错位: %d != 7416", got)
	}
	if got := unsafe.Sizeof(nvPstate{}); got != 456 {
		t.Fatalf("nvPstate 布局错位: %d != 456", got)
	}
	if got := unsafe.Sizeof(nvClockEntry{}); got != 44 {
		t.Fatalf("nvClockEntry 布局错位: %d != 44", got)
	}
}

func TestLiveECSample(t *testing.T) {
	e := NewECHW()
	s := e.Sample()
	t.Logf("EC 样本: %+v", s)
	if !s.OK {
		t.Skip("root\\wmi ACPIMethod 不可用(非雷神同源机型)")
	}
	if s.CPUT <= 0 || s.CPUT > 120 {
		t.Errorf("CPU 温度异常: %v", s.CPUT)
	}
	t.Logf("CPU=%v℃ 核心=%v℃ GPU=%v℃ 风扇 cpu=%d gpu=%d (max %d/%d) 模式=%d(%s)",
		s.CPUT, s.CoreT, s.GPUT, s.CPUFan, s.GPUFan, s.CPUFanMax, s.GPUFanMax, s.Mode, ECModeName(s.Mode))
}

func TestLiveNVRead(t *testing.T) {
	ps, ok := nvReadPstates()
	if !ok {
		t.Skip("NVAPI 不可用(无 N 卡或驱动拒绝)")
	}
	t.Logf("Pstates20: numPs=%d numClk=%d numVolt=%d", ps.numPs, ps.numClk, ps.numVolt)
	n := int(ps.numPs)
	if n > 16 {
		n = 16
	}
	for i := 0; i < n; i++ {
		st := &ps.ps[i]
		c := int(ps.numClk)
		if c > 8 {
			c = 8
		}
		for j := 0; j < c; j++ {
			ck := &st.clocks[j]
			if ck.domain == nvClockGraphics || ck.domain == nvClockMemory {
				t.Logf("pstate[%d] id=%d editable=%v domain=%d delta=%d kHz",
					i, st.id, st.flags&1 == 1, ck.domain, ck.delta.val)
			}
		}
	}
	t.Logf("当前超频态: %v", NVOverclockState())
}
