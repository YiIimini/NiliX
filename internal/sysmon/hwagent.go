package sysmon

// hwagent.go 提权硬件助手(--hwagent 子进程)。
//
// 体验目标(2026-08-26 用户定调):点 CTL 按钮/启动应用时弹一次 UAC 授权即可,
// 不重启服务、不搞「解锁」概念。普通权限的主服务无法读写 root\wmi EC(雷神控制中心
// 实际即以 Administrator 运行),故由本助手以管理员常驻:
//   - 采样循环:1.2s 一轮 EC 采样 → logs/hw/hw_state.json(主服务只读该文件展示);
//   - 命令轮询:400ms 读 logs/hw/hw_cmd.json,序号变化即执行(模式/制冷)→ hw_result.json。
// UAC 仅在助手被拉起时出现一次;助手存活期间(状态文件 mtime<6s)不再打扰。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const hwAgentDir = "logs/hw"

func hwStatePath() string { return filepath.Join(hwAgentDir, "hw_state.json") }
func hwCmdPath() string   { return filepath.Join(hwAgentDir, "hw_cmd.json") }
func hwResultPath() string { return filepath.Join(hwAgentDir, "hw_result.json") }

// hwCmdFile / hwResultFile 文件协议结构。
type hwCmdFile struct {
	ID  uint64      `json:"id"`
	Act string      `json:"act"`
	Val interface{} `json:"val,omitempty"`
}

type hwResultFile struct {
	ID  uint64 `json:"id"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// hwAgentMutex 单实例互斥(防重复拉起多个助手)。
func hwAgentMutex() (uintptr, bool) {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	p := k32.NewProc("CreateMutexW")
	name, _ := syscall.UTF16PtrFromString("Global\\NiliX.hwagent")
	h, _, err := p.Call(0, 0, uintptr(unsafe.Pointer(name)))
	// ERROR_ALREADY_EXISTS=183
	if err != nil && err.(syscall.Errno) == 183 {
		return 0, false
	}
	return h, true
}

// RunHWAgent --hwagent 入口:管理员权限常驻,采样+执行控制命令。返回退出码。
func RunHWAgent() int {
	h, first := hwAgentMutex()
	if !first {
		return 0 // 已有助手在跑
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	_ = os.MkdirAll(hwAgentDir, 0o755)
	e := NewECHW()
	var lastID uint64
	for {
		s := e.runPS(ecSampleScript)
		writeHWJSON(hwStatePath(), map[string]interface{}{
			"ts": time.Now().UnixMilli(), "sample": s,
		})
		// 命令轮询(每轮采样间穿插短间隔检查,交互响应 <1s)
		// 执行完命令立即 break 进入下一轮采样——状态文件即刻反映新状态,
		// 否则要等完剩余 sleep+整轮周期(前端观感「点击后几秒才同步」,2026-08-26)
		for i := 0; i < 3; i++ {
			if c, ok := readHWCMD(); ok && c.ID != lastID {
				lastID = c.ID
				if c.Act == "exit" {
					// 主服务退出时优雅关停(管理员进程外部杀不动,文件命令自退)
					writeHWCMDResult(c, nil)
					_ = os.Remove(hwStatePath()) // 清状态文件,防主服务误判助手健康
					os.Exit(0)
				}
				writeHWCMDResult(c, execHWCMD(e, c))
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
	}
}

// execHWCMD 执行一条控制命令。
func execHWCMD(e *ECHW, c hwCmdFile) error {
	switch c.Act {
	case "mode":
		v, _ := toUint(c.Val)
		return e.SetMode(v)
	case "cool":
		v, _ := toBool(c.Val)
		return e.SetQuickCool(v)
	default:
		return fmt.Errorf("hwagent 未知命令: %s", c.Act)
	}
}

func toUint(v interface{}) (uint32, bool) {
	switch x := v.(type) {
	case float64:
		return uint32(x), true
	case uint32:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return uint32(n), err == nil
	}
	return 0, false
}

func toBool(v interface{}) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	}
	return false, false
}

func writeHWCMDResult(c hwCmdFile, err error) {
	r := hwResultFile{ID: c.ID, OK: err == nil}
	if err != nil {
		r.Err = err.Error()
	}
	writeHWJSON(hwResultPath(), r)
}

func writeHWJSON(path string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func readHWCMD() (hwCmdFile, bool) {
	var c hwCmdFile
	b, err := os.ReadFile(hwCmdPath())
	if err != nil || len(b) == 0 {
		return c, false
	}
	if json.Unmarshal(b, &c) != nil || c.ID == 0 {
		return c, false
	}
	return c, true
}

// ---- 主服务侧:助手客户端 ----

var hwCmdSeq uint64

// ReadHWAgentState 读助手采样(健康=状态文件 6s 内更新);主服务展示数据唯一入口。
func ReadHWAgentState() (ECHWSample, bool) {
	var wrap struct {
		Ts     int64      `json:"ts"`
		Sample ECHWSample `json:"sample"`
	}
	b, err := os.ReadFile(hwStatePath())
	if err != nil || json.Unmarshal(b, &wrap) != nil {
		return ECHWSample{}, false
	}
	if time.Since(time.UnixMilli(wrap.Ts)) > 6*time.Second {
		return ECHWSample{}, false
	}
	return wrap.Sample, true
}

// SendHWCmd 经文件通道下发控制命令并等结果(助手须已在运行)。
func SendHWCmd(act string, val interface{}) error {
	id := uint64(time.Now().UnixNano())
	writeHWJSON(hwCmdPath(), hwCmdFile{ID: id, Act: act, Val: val})
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		var r hwResultFile
		b, err := os.ReadFile(hwResultPath())
		if err == nil && json.Unmarshal(b, &r) == nil && r.ID == id {
			if !r.OK {
				return fmt.Errorf("%s", r.Err)
			}
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("硬件助手未响应(状态文件无结果)")
}

// ShutdownHWAgent 请求提权助手退出(exit 文件命令,助手自退;权限无关)。
// 旧版助手(<2026-08-26 exit 命令)不认识该命令会忽略——需任务管理器手动结束一次完成换代。
func ShutdownHWAgent() {
	writeHWJSON(hwCmdPath(), hwCmdFile{ID: uint64(time.Now().UnixNano()), Act: "exit"})
}

// EnsureHWAgent 拉起提权助手(UAC 弹窗一次);返回错误=用户取消/失败。
func EnsureHWAgent() error {
	if _, ok := ReadHWAgentState(); ok {
		return nil // 已健康,无需打扰
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return ElevateRestart(exe, "--hwagent")
}

var hwAutoPull atomic.Bool

// AutoEnsureHWAgent 启动后自动请求一次授权(用户点否后不再骚扰;CTL 按钮仍可再次触发)。
func AutoEnsureHWAgent() {
	if hwAutoPull.Load() {
		return
	}
	hwAutoPull.Store(true)
	time.Sleep(3 * time.Second) // 等服务稳定再弹,避免抢开机焦点
	if _, ok := ReadHWAgentState(); ok {
		return
	}
	_ = EnsureHWAgent()
}
