package sysmon

// elevate.go 管理员状态检测与 UAC 提权重启。
// EC/WMI ACPI 通道与 LHM 核心温度都需要管理员令牌(雷神控制中心实际即以
// Administrator 运行);NiliX 平时无提权,由前端「设备控制」引导一键提权。

import (
	"syscall"
	"unsafe"
)

// IsAdmin 当前进程是否持有管理员令牌(UAC 提升或真实管理员)。
func IsAdmin() bool {
	var advapi = syscall.NewLazyDLL("advapi32.dll")
	var kernel32 = syscall.NewLazyDLL("kernel32.dll")
	pGetCurrent := kernel32.NewProc("GetCurrentProcess")
	pOpenToken := advapi.NewProc("OpenProcessToken")
	pGetInfo := advapi.NewProc("GetTokenInformation")

	var token uintptr
	hProc, _, _ := pGetCurrent.Call()
	r, _, _ := pOpenToken.Call(hProc, 0x0008, uintptr(unsafe.Pointer(&token))) // TOKEN_QUERY
	if r == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(token))

	var elevated uint32
	var retLen uint32
	r, _, _ = pGetInfo.Call(token, 20, uintptr(unsafe.Pointer(&elevated)), 4, uintptr(unsafe.Pointer(&retLen))) // TokenElevation
	return r != 0 && elevated != 0
}

// ElevateRestart 以 UAC 提权方式拉起一份新的自身进程(调用方随后应优雅退出当前实例)。
// 返回错误 = 提权被取消/失败;成功即已有新进程在启动。
func ElevateRestart(exePath, args string) error {
	var shell32 = syscall.NewLazyDLL("shell32.dll")
	pExec := shell32.NewProc("ShellExecuteW")
	// SW_SHOWNORMAL=1;"runas" 动词触发 UAC
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exePath)
	params, _ := syscall.UTF16PtrFromString(args)
	dir, _ := syscall.UTF16PtrFromString("")
	r, _, err := pExec.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)), uintptr(unsafe.Pointer(dir)), 1)
	if r <= 32 {
		return err
	}
	return nil
}
