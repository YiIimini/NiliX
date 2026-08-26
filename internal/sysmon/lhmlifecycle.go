package sysmon

// lhmlifecycle.go lhmsensor 子进程生命周期治理(2026-08-26)。
// 教训:服务被 taskkill 时子进程不随行,每轮重启漏一个 lhmsensor(60MB/个,曾堆积 8 个)。
//   - killOrphanLHM:启动前清理非本进程所属的 lhmsensor(孤儿);
//   - bindJobKillOnParentExit:Job Object KILL_ON_JOB_CLOSE,父进程(含被强杀)退出时
//     OS 自动终结 job 内子进程——根治。

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
)

func killOrphanLHM() {
	procs, err := process.Processes()
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || !strings.EqualFold(name, lhmExe) {
			continue
		}
		ppid, err := p.Ppid()
		if err != nil {
			continue
		}
		if int(ppid) == self {
			continue // 本进程已拉的实例
		}
		// parent 已死(孤儿)或 parent 是别的旧上下文:本产品独占 lhmsensor,统一回收
		_ = p.Kill()
	}
}

var jobOnce sync.Once

// bindJobKillOnParentExit 把子进程装入 Job Object,父进程退出时自动终结。
func bindJobKillOnParentExit(proc *os.Process) {
	if proc == nil {
		return
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	// JobObjectExtendedLimitInformation = 9
	type basicLimit struct {
		perProcUserTime int64
		perJobUserTime  int64
		limitFlags      uint32
		minWs, maxWs    uintptr
		activeProcLimit uint32
		affinity        uintptr
		priorityClass   uint32
		schedClass      uint32
	}
	type extendedLimit struct {
		basic       basicLimit
		ioRead      int64
		ioWrite     int64
		procMemLim  uintptr
		jobMemLim   uintptr
		peakProcMem uintptr
		peakJobMem  uintptr
	}
	jobOnce.Do(func() {
		// *os.Process 在 windows 实现上带 Handle() uintptr,经 unsafe 提取
		type handler interface{ Handle() uintptr }
		hp, ok := any(proc).(handler)
		if !ok {
			return
		}
		cj := k32.NewProc("CreateJobObjectW")
		si := k32.NewProc("SetInformationJobObject")
		ap := k32.NewProc("AssignProcessToJobObject")
		h, _, _ := cj.Call(0, 0)
		if h == 0 {
			return
		}
		var info extendedLimit
		info.basic.limitFlags = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		si.Call(h, 9, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		ap.Call(h, hp.Handle())
		// handle 故意不关:保持 job 存活至本进程结束;进程退出 OS 关句柄→job 内子进程终结
	})
}
