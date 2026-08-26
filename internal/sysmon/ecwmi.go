package sysmon

// ecwmi.go 雷神控制中心(Thunderobot Control Center)同款硬件通道。
//
// 数据来源逆向自 C:\Mi\Apps\雷神控制中心\Central.Wmi.dll(ILSpy 反编译,2026-08-26):
//   - BIOS 在 root\wmi 暴露 ACPIMethod 类,方法 MemIO/DoMethod 走 IData[24] 字节协议(魔数 "BYDL"),
//     普通用户权限即可调用——无需 LHM 的管理员/ring0,是本机最权威的 EC 通道;
//   - EC RAM 读写经 MemIO 端口 0x300(索引口 0x90/0x91/0x92/0x93/0x94,数据口 0xA0);
//   - 关键偏移:CPU温度=10 核心温度=12 CPU风扇=8 GPU风扇=9 风扇Max=6/7;
//   - 性能模式/风扇全速经端口 0x302(模式=idx 0x60,锁=idx 0x70,全速态=idx 0x66);
//   - GPU 温度 = DoMethod cmd=4;写后通知 EC = ECWrite(191,120)。
//
// 实现选型:go-ole 无公开 byte-SafeArray 构造,故采样走单次 PowerShell 进程
// (一次 COM 连接完成整轮只读采样,2.5s 缓存);控制写为独立单次进程。
// EC 读序列严格按雷神原版逐步复刻,只读不越权。

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ECHWSample 一轮 EC 采样结果(原始值;0xFFFFFFFF=无效)。
type ECHWSample struct {
	OK         bool    `json:"ok"`
	Denied     bool    `json:"denied"` // root\wmi 拒绝访问 → 需以管理员运行 NiliX 解锁
	CPUT       float64 `json:"cpuT"`
	CoreT      float64 `json:"coreT"`
	GPUT       float64 `json:"gpuT"`
	CPUFan     uint32  `json:"cpuFan"`
	GPUFan     uint32  `json:"gpuFan"`
	CPUFanMax  uint32  `json:"cpuFanMax"`
	GPUFanMax  uint32  `json:"gpuFanMax"`
	Mode       uint32  `json:"mode"`      // 0 轻效 / 1 进阶 / 2 巅峰
	ModeOK     bool    `json:"modeOK"`    // 端口 0x302 通道可用
	QuickCool  bool    `json:"quickCool"` // 风扇全速(快速制冷)开启
	QuickOK    bool    `json:"quickOK"`
}

const ecInvalid = 0xFFFFFFFF

// ecPSCommon PS 侧共用头:建 WMI 连接 + IO/ECRead/ECWrite 函数(照抄雷神字节协议)。
// catch 输出 denied 标记:root\wmi 实例枚举被拒 = 进程非管理员(雷神 CC 实际以 Administrator 运行)。
const ecPSCommon = `
$ErrorActionPreference='Stop'
function Main {
  try {
    $s = New-Object System.Management.ManagementScope('\\.\root\wmi')
    $s.Options.EnablePrivileges = $true
    $s.Connect()
    $sr = New-Object System.Management.ManagementObjectSearcher($s, (New-Object System.Management.ObjectQuery('SELECT * FROM ACPIMethod')))
    $inst = $null
    foreach ($o in $sr.Get()) { $inst = $o; break }
    if ($null -eq $inst) { return '{"ok":false}' }
    function WmiCall([string]$m, [byte[]]$d) {
      $in = $inst.GetMethodParameters($m); $in['IData'] = $d
      $out = $inst.InvokeMethod($m, $in, $null)
      return [uint32]$out['OData']
    }
    function IO([uint32]$addr, [int]$rw, [byte]$idx, [byte]$dat) {
      $b = New-Object byte[] 24; $b[0]=66; $b[1]=89; $b[2]=68; $b[3]=76
      $b[4] = [byte]($addr -band 0xFF); $b[5] = [byte](($addr -shr 8) -band 0xFF)
      $b[12]=1; $b[13]=[byte]$rw; $b[14]=8; $b[15]=$idx; $b[16]=$dat
      return WmiCall 'MemIO' $b
    }
    function ECRead([byte]$a) {
      [void](IO 0x300 1 148 0)
      [void](IO 0x300 1 146 1)
      [void](IO 0x300 1 144 0)
      [void](IO 0x300 1 145 $a)
      [void](IO 0x300 1 147 160)
      [System.Threading.Thread]::Sleep(5)
      return IO 0x300 0 160 0
    }
    function ECWrite([byte]$a, [byte]$v) {
      [void](IO 0x300 1 148 0)
      [void](IO 0x300 1 145 0)
      [void](IO 0x300 1 146 0)
      [void](IO 0x300 1 146 1)
      [void](IO 0x300 1 144 0)
      [void](IO 0x300 1 145 $a)
      [void](IO 0x300 1 160 $v)
      [void](IO 0x300 1 147 161)
    }
    return & $script:body
  } catch { return '{"ok":false,"denied":true}' }
}
`

// ecSampleScript 整轮只读采样 → JSON。
const ecSampleScript = ecPSCommon + `
$script:body = {
  $cpuT  = ECRead 10; $coreT = ECRead 12
  $cpuF  = ECRead 8;  $gpuF  = ECRead 9
  $cpuFM = ECRead 6;  $gpuFM = ECRead 7
  $mode  = IO 0x302 0 96 0
  $cool  = IO 0x302 0 102 0
  $b2 = New-Object byte[] 24; $b2[0]=66; $b2[1]=89; $b2[2]=68; $b2[3]=76; $b2[4]=4
  $gpuT = WmiCall 'DoMethod' $b2
  [pscustomobject]@{
    ok=[bool]1; cpuT=$cpuT; coreT=$coreT; gpuT=$gpuT
    cpuFan=$cpuF; gpuFan=$gpuF; cpuFanMax=$cpuFM; gpuFanMax=$gpuFM
    mode=$mode; modeOK=($mode -ne 4294967295)
    quickCool=(($cool -band 1) -eq 1); quickOK=($cool -ne 4294967295)
  } | ConvertTo-Json -Compress
}
Main
`

// ECHW EC/WMI 通道封装:采样缓存 + 控制写。
type ECHW struct {
	mu     sync.Mutex
	cached ECHWSample
	at     time.Time
}

// NewECHW 构造(采样惰性触发)。
func NewECHW() *ECHW { return &ECHW{} }

// Sample 取一轮采样(2.5s 缓存;失败样本不缓存,下轮重试)。
func (e *ECHW) Sample() ECHWSample {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.at) < 2500*time.Millisecond && e.cached.OK {
		return e.cached
	}
	s := e.runPS(ecSampleScript)
	if s.OK {
		e.cached, e.at = s, time.Now()
	}
	return s
}

// runPS 跑一段 EC PS 脚本并解析 JSON 输出。
func (e *ECHW) runPS(script string) ECHWSample {
	cmd := hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ECHWSample{}
	}
	line := strings.TrimSpace(string(out))
	if i := strings.LastIndex(line, "\n"); i >= 0 { // PS 杂讯容错:取最后一个 JSON 对象行
		line = strings.TrimSpace(line[i+1:])
	}
	if !strings.HasPrefix(line, "{") {
		return ECHWSample{}
	}
	var s ECHWSample
	if json.Unmarshal([]byte(line), &s) != nil {
		return ECHWSample{}
	}
	// uint32(-1) 失败值归零
	if s.CPUT == float64(ecInvalid) || s.CPUT > 120 {
		s.CPUT = 0
	}
	if s.CoreT == float64(ecInvalid) || s.CoreT > 120 {
		s.CoreT = 0
	}
	if s.GPUT == float64(ecInvalid) || s.GPUT > 120 {
		s.GPUT = 0
	}
	if s.CPUFan == ecInvalid {
		s.CPUFan = 0
	}
	if s.GPUFan == ecInvalid {
		s.GPUFan = 0
	}
	if s.CPUFanMax == ecInvalid {
		s.CPUFanMax = 0
	}
	if s.GPUFanMax == ecInvalid {
		s.GPUFanMax = 0
	}
	return s
}

// SetMode 性能模式切换(0 轻效 / 1 进阶 / 2 巅峰):解锁→写模式→通知 EC。
func (e *ECHW) SetMode(mode uint32) error {
	if mode > 3 {
		return fmt.Errorf("非法性能模式: %d", mode)
	}
	script := ecPSCommon + fmt.Sprintf(`
$script:body = {
  $b = IO 0x302 0 112 0
  [void](IO 0x302 1 112 ([byte]($b -bor 1)))
  [void](IO 0x302 1 96 %d)
  ECWrite 191 120
  'OK'
}
Main
`, mode)
	cmd := hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("模式切换执行失败: %w", err)
	}
	res := strings.TrimSpace(string(out))
	switch {
	case res == "OK":
	case strings.Contains(res, "denied"):
		return fmt.Errorf("需要管理员权限:请以管理员身份运行 NiliX(灵动岛「设备控制」可一键提权)")
	default:
		return fmt.Errorf("模式切换未生效(root\\wmi ACPIMethod 不可用)")
	}
	e.mu.Lock()
	e.at = time.Time{} // 失效缓存,下轮立即重采样
	e.mu.Unlock()
	return nil
}

// SetQuickCool 快速制冷(风扇全速)。阈值序列照抄雷神 SetFanFullMode,按当前模式取档。
func (e *ECHW) SetQuickCool(on bool) error {
	body := `
$script:body = {
  $m = IO 0x302 0 96 0
`
	if on {
		body += `
  [void](ECWrite 65 1)
  if ($m -eq 0)      { [void](ECWrite 76 73);   [void](ECWrite 77 96) }
  elseif ($m -eq 1)  { [void](ECWrite 76 104);  [void](ECWrite 77 140) }
  else               { [void](ECWrite 76 144);  [void](ECWrite 77 183) }
`
	} else {
		body += `
  [void](ECWrite 65 0)
`
	}
	body += `
  'OK'
}
Main
`
	script := ecPSCommon + body
	cmd := hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("快速制冷执行失败: %w", err)
	}
	res := strings.TrimSpace(string(out))
	switch {
	case res == "OK":
	case strings.Contains(res, "denied"):
		return fmt.Errorf("需要管理员权限:请以管理员身份运行 NiliX(灵动岛「设备控制」可一键提权)")
	default:
		return fmt.Errorf("快速制冷未生效(root\\wmi ACPIMethod 不可用)")
	}
	e.mu.Lock()
	e.at = time.Time{}
	e.mu.Unlock()
	return nil
}

// ecModeNames 性能模式名(UI 展示用)。
var ecModeNames = map[uint32]string{0: "轻效", 1: "进阶", 2: "巅峰", 3: "巅峰"}

// ECModeName 模式值 → 中文名(未知值原样返回)。
func ECModeName(m uint32) string {
	if n, ok := ecModeNames[m]; ok {
		return n
	}
	return strconv.FormatUint(uint64(m), 10)
}
