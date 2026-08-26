package sysmon

// nvapi.go NVIDIA nvapi64.dll 薄封装:一键超频(GetPstates20→改 delta→SetPstates20)。
//
// 协议与结构布局逆向自雷神控制中心(NvAPIWrapper.dll + ControlCenter.Hardware.dll):
//   - SetClocks(core=200MHz, mem=1000MHz) → Graphics/Memory 时钟 delta(kHz),关闭=0/0;
//   - NV_GPU_PSTATES20 结构:头20B + 16×pstate(456B)+ OverVolting(100B)= 7416B(V3);
//   - pstate = id/flags(8B) + clocks[8](44B each) + voltages[4](24B each)。
// 布局经 GetPstates20 返回的 version 低16位(size)运行时自校验,不匹配即拒绝写入。

import (
	"sync"
	"syscall"
	"unsafe"
)

// NVAPI 函数 ID(十进制,来源 NvAPIWrapper FunctionId 枚举)。
const (
	nvAPIInitialize      = 22079528  // 0x0150E828
	nvAPIEnumPhysicalGPU = 3853292063 // 0xE5AC921F
	nvAPIGetPstates20    = 1878528531
	nvAPISetPstates20    = 256749163
)

// 时钟域(PublicClockDomain)。
const (
	nvClockGraphics = 0
	nvClockMemory   = 4
)

type nvDelta struct{ val, min, max int32 } // 12B

type nvClockEntry struct {
	domain, typ, flags uint32
	delta              nvDelta // @12
	freq               [5]uint32 // @24 union: Single([0]) / Range(minF,maxF,voltDom,minV,maxV)
} // 44B

type nvVoltEntry struct {
	dom, flags, value uint32
	delta             nvDelta
} // 24B

type nvPstate struct {
	id, flags uint32
	clocks    [8]nvClockEntry
	volts     [4]nvVoltEntry
} // 456B

type nvOverVolt struct {
	n    uint32
	volts [4]nvVoltEntry
} // 100B

type nvPstates20 struct {
	version, flags, numPs, numClk, numVolt uint32
	ps                                     [16]nvPstate
	ov                                     nvOverVolt
} // 7416B(V3)

// nvapiOnce / nvapiReady:Initialize 只做一次。
var (
	nvapiOnce sync.Once
	nvapiOK   bool
	nvGPU     uintptr
	nvSizeOK  bool
)

func nvapiInit() {
	nvapiOnce.Do(func() {
		mod := syscall.NewLazyDLL("nvapi64.dll")
		if mod.Load() != nil {
			return
		}
		q := mod.NewProc("nvapi_QueryInterface")
		if q.Find() != nil {
			return
		}
		initAddr, _, _ := q.Call(nvAPIInitialize)
		if initAddr == 0 {
			return
		}
		if st, _, _ := syscall.SyscallN(initAddr); st != 0 {
			return
		}
		enumAddr, _, _ := q.Call(nvAPIEnumPhysicalGPU)
		if enumAddr == 0 {
			return
		}
		var handles [64]uintptr
		var n uint32
		if st, _, _ := syscall.SyscallN(enumAddr, uintptr(unsafe.Pointer(&handles[0])), uintptr(unsafe.Pointer(&n))); st != 0 || n == 0 {
			return
		}
		nvGPU = handles[0]
		nvapiOK = true
	})
}

// nvPstatesAddr QueryInterface 拿 Get/Set 函数地址。
func nvPstatesAddr(id uintptr) uintptr {
	mod := syscall.NewLazyDLL("nvapi64.dll")
	q := mod.NewProc("nvapi_QueryInterface")
	a, _, _ := q.Call(id)
	return a
}

// nvReadPstates 读全部性能状态;并校验驱动返回的 size 与本地结构一致。
func nvReadPstates() (*nvPstates20, bool) {
	nvapiInit()
	if !nvapiOK {
		return nil, false
	}
	get := nvPstatesAddr(nvAPIGetPstates20)
	if get == 0 {
		return nil, false
	}
	var ps nvPstates20
	ps.version = uint32(unsafe.Sizeof(ps)) | (3 << 16) // V3
	if st, _, _ := syscall.SyscallN(get, nvGPU, uintptr(unsafe.Pointer(&ps))); st != 0 {
		return nil, false
	}
	if int(ps.version&0xFFFF) != int(unsafe.Sizeof(ps)) {
		return nil, false // 驱动版本布局不一致,拒绝使用
	}
	return &ps, true
}

// NVSetOverclock 一键超频:core/mem 为 MHz 偏移(雷神同款 开=200/1000,关=0/0)。
// 只改可编辑 pstate 中 Graphics/Memory 域的 delta,电压与其余域保持不动。
func NVSetOverclock(coreMHz, memMHz int) bool {
	ps, ok := nvReadPstates()
	if !ok {
		return false
	}
	coreK, memK := int32(coreMHz*1000), int32(memMHz*1000)
	n := int(ps.numPs)
	if n > 16 {
		n = 16
	}
	for i := 0; i < n; i++ {
		st := &ps.ps[i]
		if st.flags&1 == 0 { // 不可编辑
			continue
		}
		c := int(ps.numClk)
		if c > 8 {
			c = 8
		}
		for j := 0; j < c; j++ {
			ck := &st.clocks[j]
			if ck.flags&1 == 0 {
				continue
			}
			switch ck.domain {
			case nvClockGraphics:
				ck.delta.val = coreK
			case nvClockMemory:
				ck.delta.val = memK
			}
		}
	}
	set := nvPstatesAddr(nvAPISetPstates20)
	if set == 0 {
		return false
	}
	st, _, _ := syscall.SyscallN(set, nvGPU, uintptr(unsafe.Pointer(ps)))
	return st == 0
}

// NVOverclockState 当前超频状态:任一可编辑 Graphics 域 delta>0 视为开启。
func NVOverclockState() bool {
	ps, ok := nvReadPstates()
	if !ok {
		return false
	}
	n := int(ps.numPs)
	if n > 16 {
		n = 16
	}
	for i := 0; i < n; i++ {
		st := &ps.ps[i]
		if st.flags&1 == 0 {
			continue
		}
		c := int(ps.numClk)
		if c > 8 {
			c = 8
		}
		for j := 0; j < c; j++ {
			ck := &st.clocks[j]
			if ck.domain == nvClockGraphics && ck.delta.val > 0 {
				return true
			}
		}
	}
	return false
}
