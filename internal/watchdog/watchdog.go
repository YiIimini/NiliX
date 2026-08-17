// Package watchdog 提供进程单实例保护（命名互斥体看门狗），防止重复启动。
//
// 主进程与所有子进程都应通过本包确保单实例：各自用唯一的名字调用
// SingleInstance，避免同一个进程被重复拉起。
package watchdog

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrAlreadyRunning 表示已存在同名实例。
var ErrAlreadyRunning = errors.New("已有实例在运行")

const errorAlreadyExists = 183 // ERROR_ALREADY_EXISTS

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	user32           = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW  = user32.NewProc("MessageBoxW")
)

// Guard 持有单实例互斥体句柄。
type Guard struct {
	handle windows.Handle
}

// SingleInstance 确保当前进程是唯一实例。
// name 建议用进程唯一名（如 "NiliX"）；子进程用各自的名字复用本机制。
func SingleInstance(name string) (*Guard, error) {
	namePtr, err := windows.UTF16PtrFromString("Local\\" + name)
	if err != nil {
		return nil, err
	}
	r, _, e1 := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if r == 0 {
		return nil, e1
	}
	h := windows.Handle(r)
	if errno, ok := e1.(syscall.Errno); ok && errno == errorAlreadyExists {
		windows.CloseHandle(h)
		return nil, ErrAlreadyRunning
	}
	return &Guard{handle: h}, nil
}

// Release 释放互斥体。
func (g *Guard) Release() {
	if g != nil && g.handle != 0 {
		windows.CloseHandle(g.handle)
		g.handle = 0
	}
}

// Alert 弹出消息框（GUI 无窗口时的用户可见提示）。
func Alert(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	// MB_ICONINFORMATION
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x00000040)
}
