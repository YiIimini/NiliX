// Package sysmon 提供系统监测（CPU/内存/GPU/磁盘/网络），从 sysmon-widget 的 stats 模块适配而来。
package sysmon

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// CPU CPU 指标。
type CPU struct {
	Usage   float64   `json:"usage"`
	Temp    float64   `json:"temp"`
	HasTemp bool      `json:"hasTemp"`
	Cores   []float64 `json:"cores"`
}

// Mem 内存指标。
type Mem struct {
	Percent   float64 `json:"percent"`
	Used      string  `json:"used"`
	Total     string  `json:"total"`
	Available string  `json:"available"`
	Temp      float64 `json:"temp"`
	HasTemp   bool    `json:"hasTemp"`
}

// GPU GPU 指标（nvidia-smi + Windows 性能计数器）。
type GPU struct {
	Present    bool    `json:"present"`
	Usage      float64 `json:"usage"`
	Temp       float64 `json:"temp"`
	MemUsed    string  `json:"memUsed"`
	MemTotal   string  `json:"memTotal"`
	MemPercent float64 `json:"memPercent"` // 显存使用率(0-100)
	SharedUsed string  `json:"sharedUsed"`
}

// Disk 磁盘指标（系统盘 C:）。
type Disk struct {
	ReadMBs  float64 `json:"readMBs"`
	WriteMBs float64 `json:"writeMBs"`
	Percent  float64 `json:"percent"`
	Used     string  `json:"used"`
	Total    string  `json:"total"`
}

// Net 网络指标。
type Net struct {
	RxRate  float64 `json:"rxRate"`
	TxRate  float64 `json:"txRate"`
	RxTotal string  `json:"rxTotal"`
	TxTotal string  `json:"txTotal"`
}

// Meta 系统元信息。
type Meta struct {
	Uptime string `json:"uptime"`
	Procs  int    `json:"procs"`
}

// Comfy ComfyUI 服务状态。
type Comfy struct {
	Online  bool   `json:"online"`
	Version string `json:"version"`
	Err     string `json:"err"`
}

// Harness DeepSeek Harness 服务状态。
type Harness struct {
	Online  bool   `json:"online"`
	Version string `json:"version"`
	Err     string `json:"err"`
}

// NiliX NiliX 主应用窗口状态(主窗口是否打开:控制端口 8799 可达即打开)。
type NiliX struct {
	WindowOpen bool   `json:"window_open"` // 主窗口是否打开(托盘可重建)
	Port       int    `json:"port"`
	Err        string `json:"err"`
}

// ZCode ZCode 桌面端状态（进程探测）。
type ZCode struct {
	Running bool `json:"running"`
	Pid     int  `json:"pid"`
	Count   int  `json:"count"`
}

// HW 设备控制中心(雷神 EC 同源通道 + NVAPI):风扇/性能模式/快速制冷/超频。
type HW struct {
	OK      bool   `json:"ok"`      // EC 通道可用(需管理员)
	Denied  bool   `json:"denied"`  // 权限被拒 → 引导一键提权
	Admin   bool   `json:"admin"`   // NiliX 自身是否管理员令牌
	CPUTemp float64 `json:"cpuTemp"` // EC CPU 温度(Snapshot 温度链首选;仅缓存读,绝不阻塞)
	GPUTemp float64 `json:"gpuTemp"`
	CPUFan  uint32 `json:"cpuFan"`  // RPM
	GPUFan  uint32 `json:"gpuFan"`  // RPM
	Mode    uint32 `json:"mode"`    // 0 轻效 / 1 进阶 / 2 巅峰
	ModeName string `json:"modeName"`
	ModeOK  bool   `json:"modeOK"`
	QuickCool bool `json:"quickCool"` // 快速制冷(风扇全速)
	Overclock bool `json:"overclock"` // 一键超频(NVAPI,免管理员)
}

// Bot ZCode bot 运行时状态（运行锁 owner.json 探测）。
type Bot struct {
	Online   bool   `json:"online"`
	Pid      int    `json:"pid"`
	Channels int    `json:"channels"`
	Err      string `json:"err"`
}

// Snapshot 一次采集快照。
type Snapshot struct {
	CPU   CPU   `json:"cpu"`
	Mem   Mem   `json:"mem"`
	GPU   GPU   `json:"gpu"`
	HW    HW    `json:"hw"`
	Disk  Disk  `json:"disk"`
	Net   Net   `json:"net"`
	ZCode ZCode `json:"zcode"`
	Bot   Bot   `json:"bot"`
	Comfy Comfy `json:"comfy"`
	// Harness DeepSeek Harness 服务状态(灵动岛 HUD 底部监控;探测独立于 api 包避免循环依赖)
	Harness Harness `json:"harness"`
	Meta    Meta    `json:"meta"`
	Ts      string  `json:"ts"`
}

// Collector 定时采集器（含差值/缓存）。
type Collector struct {
	mu        sync.Mutex
	prevNetRx uint64
	prevNetTx uint64
	prevDiskR uint64
	prevDiskW uint64
	// 磁盘/网络速率各自的基准时刻(审计 M1:此前共用 prevTime,diskRate 先跑把基准置为
	// 当前时刻,netRate 的 elapsed≈0 恒返回 0——灵动岛网络速率永远显示 0 KB/s)
	prevDiskTime time.Time
	prevNetTime  time.Time
	firstDisk    bool
	firstNet     bool

	gpuCache   GPU
	gpuAt      time.Time
	sharedMem  uint64
	sharedAt   time.Time // 共享显存独立缓存:PowerShell CIM 查询单次约 1.2s,高频调用会拖垮 /api/stats
	cfyCache   Comfy
	cfyAt      time.Time
	cfyFail    int // 连续探测失败计数(粘滞:连续失败才翻转,防状态灯抖动)
	hCache     Harness
	hAt        time.Time
	zcodeCache ZCode
	zcodeAt    time.Time
	botCache   Bot
	botAt      time.Time
	procsCount int
	procsAt    time.Time
	lhm        *LHM
	ec         *ECHW // 雷神同源 EC/WMI 通道(风扇/模式/温度)
	hwCache    HW
	hwAt       time.Time
}

// NewCollector 构造采集器。
func NewCollector() *Collector {
	c := &Collector{firstDisk: true, firstNet: true, lhm: NewLHM(), ec: NewECHW()}
	go c.ecLoop() // EC 采样是 PowerShell 进程(数百 ms),异步协程预热缓存,不阻塞 /api/stats
	_, _ = cpu.Percent(0, false)
	return c
}

// EC 设备控制中心句柄(供 API 控制端点调用)。
func (c *Collector) EC() *ECHW { return c.ec }

// LHM lhmsensor 句柄(服务优雅退出时主动 Kill,防子进程残留)。
func (c *Collector) LHM() *LHM { return c.lhm }

// ecLoop 后台轮询 EC 状态(3s)。数据源优先级(2026-08-26 定稿):
//   - 主服务自身管理员 → 直接 PS 采样(原通道);
//   - 否则读提权助手 logs/hw/hw_state.json(UAC 一次授权后常驻);助手未跑=未授权态,
//     由 AutoEnsureHWAgent / CTL 按钮点击拉起,采样循环自身绝不弹 UAC。
func (c *Collector) ecLoop() {
	for {
		c.refreshHWOnce()
		time.Sleep(3 * time.Second)
	}
}

// refreshHWOnce 采一轮 EC 状态入缓存(ecLoop 与 RefreshHW 共用)。
func (c *Collector) refreshHWOnce() {
	var s ECHWSample
	if IsAdmin() {
		s = c.ec.Sample()
	} else if st, ok := ReadHWAgentState(); ok {
		s = st
	}
	c.mu.Lock()
	c.hwCache = HW{
		OK: s.OK, Denied: s.Denied, Admin: IsAdmin(),
		CPUTemp: s.CPUT, GPUTemp: s.GPUT,
		CPUFan: s.CPUFan, GPUFan: s.GPUFan,
		Mode: s.Mode, ModeName: ECModeName(s.Mode), ModeOK: s.ModeOK,
		QuickCool: s.QuickCool,
		Overclock: NVOverclockState(),
	}
	c.hwAt = time.Now()
	c.mu.Unlock()
}

// RefreshHW 控制操作成功后立即刷新缓存(不等 ecLoop 3s 周期),前端下轮拉 stats 即新值。
func (c *Collector) RefreshHW() { c.refreshHWOnce() }

// Snapshot 采集一次全量指标。
func (c *Collector) Snapshot() *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	s := &Snapshot{Ts: now.Format("15:04:05")}

	perCore, _ := cpu.Percent(0, true)
	if len(perCore) > 0 {
		sum := 0.0
		for _, v := range perCore {
			sum += v
		}
		s.CPU.Usage = sum / float64(len(perCore))
		s.CPU.Cores = perCore
	} else {
		if u, _ := cpu.Percent(0, false); len(u) > 0 {
			s.CPU.Usage = u[0]
		}
	}
	// CPU 核心温度优先级:雷神同源 EC(最准,需管理员;只读 ecLoop 预热缓存,绝不阻塞)
	// → LHM → N/A(宁缺毋假,不用 ACPI 热区)。
	if c.hwCache.CPUTemp > 0 && c.hwCache.CPUTemp < 120 {
		s.CPU.Temp, s.CPU.HasTemp = c.hwCache.CPUTemp, true
	} else if t, ok := c.lhm.CPUTemp(); ok {
		s.CPU.Temp, s.CPU.HasTemp = t, true
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		s.Mem.Percent = vm.UsedPercent
		s.Mem.Used = FormatBytes(vm.Used)
		s.Mem.Total = FormatBytes(vm.Total)
		s.Mem.Available = FormatBytes(vm.Available)
	}
	if t, ok := c.lhm.MemTemp(); ok {
		s.Mem.Temp, s.Mem.HasTemp = t, true
	}

	s.GPU = c.gpuCached(now)
	// GPU 温度优先雷神同源 EC 值(与控制中心显示一致),无则 nvidia-smi,再无则 LHM 兜底
	if c.hwCache.GPUTemp > 0 && c.hwCache.GPUTemp < 120 {
		s.GPU.Temp = c.hwCache.GPUTemp
	}
	s.Comfy = c.comfyCached(now)
	s.Harness = c.harnessCached(now)
	s.ZCode = c.zcodeCached(now)
	s.Bot = c.botCached(now)
	if time.Since(c.hwAt) > 0 {
		s.HW = c.hwCache
	}

	if du, err := disk.Usage(`C:\`); err == nil {
		s.Disk.Percent = du.UsedPercent
		s.Disk.Used = FormatBytes(du.Used)
		s.Disk.Total = FormatBytes(du.Total)
	}
	s.Disk.ReadMBs, s.Disk.WriteMBs = c.diskRate(`C:`)

	s.Net.RxRate, s.Net.TxRate, s.Net.RxTotal, s.Net.TxTotal = c.netRate()

	if up, err := host.Uptime(); err == nil {
		s.Meta.Uptime = FormatUptime(up)
	}
	if now.Sub(c.procsAt) > 5*time.Second {
		if pids, err := process.Pids(); err == nil {
			c.procsCount = len(pids)
		}
		c.procsAt = now
	}
	s.Meta.Procs = c.procsCount

	return s
}

func (c *Collector) gpuCached(now time.Time) GPU {
	// GPU 利用率/温度 5 秒缓存(无需 2s 精度;nvidia-smi 单次约 50ms 可接受)
	if now.Sub(c.gpuAt) < 5*time.Second {
		return c.gpuCache
	}
	g := GPUInfo()
	// 温度兜底:nvidia-smi 偶发 [N/A]/驱动忙时无温度,LHM 有值则补上(2026-08-26)
	if g.Present && g.Temp <= 0 {
		if t, ok := c.lhm.GPUTemp(); ok {
			g.Temp = t
		}
	}
	g.SharedUsed = c.sharedMemCached(now)
	c.gpuCache = g
	c.gpuAt = now
	return c.gpuCache
}

// sharedMemCached 共享显存 30 秒缓存:PowerShell CIM 查询单次约 1.2s,
// 若随 GPU 每次刷新执行,前端 2s 轮询 /api/stats 会被它拖到 1.2s+ 响应——系统监测几乎永远转圈。
func (c *Collector) sharedMemCached(now time.Time) string {
	if now.Sub(c.sharedAt) < 30*time.Second {
		return FormatBytes(c.sharedMem)
	}
	c.sharedMem = SharedGPUMem()
	c.sharedAt = now
	return FormatBytes(c.sharedMem)
}

// ComfyURL ComfyUI 服务地址。
const ComfyURL = "http://127.0.0.1:8190"

func (c *Collector) comfyCached(now time.Time) Comfy {
	if now.Sub(c.cfyAt) < 5*time.Second {
		return c.cfyCache
	}
	cur := pollComfy()
	if cur.Online {
		c.cfyFail = 0
		c.cfyCache = cur
	} else if c.cfyFail >= 2 {
		// 连续 3 次失败才判离线(ComfyUI 加载模型/GPU 忙时响应慢会单次超时,
		// 立即翻转会让状态灯「运行中/已停止」来回跳)
		c.cfyCache = cur
	} else {
		c.cfyFail++
		// 未达阈值:保留上次状态,不抖动
	}
	c.cfyAt = now
	return c.cfyCache
}

func pollComfy() Comfy {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ComfyURL + "/system_stats")
	if err != nil {
		return Comfy{Err: "offline"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Comfy{Err: "http " + http.StatusText(resp.StatusCode)}
	}
	var s struct {
		System struct {
			ComfyUIVersion string `json:"comfyui_version"`
		} `json:"system"`
	}
	if json.NewDecoder(resp.Body).Decode(&s) != nil {
		return Comfy{Err: "parse"}
	}
	return Comfy{Online: true, Version: s.System.ComfyUIVersion}
}

// ---- DeepSeek Harness ----

// HarnessURL DeepSeek Harness 服务地址(DSH Web,与 api 包 harness.go 一致)。
const HarnessURL = "http://127.0.0.1:3080"

func (c *Collector) harnessCached(now time.Time) Harness {
	if now.Sub(c.hAt) < 5*time.Second {
		return c.hCache
	}
	cur := pollHarness()
	if cur.Online {
		c.hCache = cur
	} else if cur.Err != "" && c.hCache.Online {
		// 一次失败不立即翻转(DSH 启动慢/重载中),保底由下次轮询确认
		c.hAt = now
		return c.hCache
	}
	c.hCache = cur
	c.hAt = now
	return c.hCache
}

func pollHarness() Harness {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(HarnessURL + "/")
	if err != nil {
		return Harness{Err: "offline"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Harness{Err: "http " + http.StatusText(resp.StatusCode)}
	}
	// 版本号:优先读 DSH npm 包 package.json(比页面 <title> 可靠,SPA 页面无版本);
	// 兜底从页面标题提取。
	ver := dshPkgVersion()
	if ver == "" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if m := harnessTitleRe.FindSubmatch(body); len(m) > 1 {
			ver = strings.TrimSpace(string(m[1]))
		}
	}
	return Harness{Online: true, Version: ver}
}

// dshPkgVersion 读 DSH npm 包版本(@deepseek-ai/dsh/package.json),与 api 包 harnessRoot 一致。
func dshPkgVersion() string {
	p := `C:\Mi\Ai\DeepSeekHarness\node_modules\@deepseek-ai\dsh\package.json`
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &m) == nil && m.Version != "" {
		return m.Version
	}
	return ""
}

// harnessTitleRe 从 Harness 页面标题提取版本号(与 api 包 harness.go 同正则)。
var harnessTitleRe = regexp.MustCompile(`(?is)<title[^>]*>\s*([^<]*?)\s*</title>`)

// ---- ZCode / Bot ----

// zcodeExe ZCode 桌面端可执行文件名（多进程架构，探测同名进程）。
const zcodeExe = "ZCode.exe"

// botLocksDir ZCode bot 运行时锁目录（owner.json 记录托管进程 pid）。
const botLocksDir = `C:\Users\Administrator\.zcode\v2\bots-runtime-locks`

func (c *Collector) zcodeCached(now time.Time) ZCode {
	if now.Sub(c.zcodeAt) < 5*time.Second {
		return c.zcodeCache
	}
	c.zcodeCache = probeZCode()
	c.zcodeAt = now
	return c.zcodeCache
}

// probeZCode 探测 ZCode 桌面端进程。
func probeZCode() ZCode {
	procs, err := process.Processes()
	if err != nil {
		return ZCode{}
	}
	best := 0
	count := 0
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || !strings.EqualFold(name, zcodeExe) {
			continue
		}
		count++
		if best == 0 || int(p.Pid) < best {
			best = int(p.Pid)
		}
	}
	return ZCode{Running: count > 0, Pid: best, Count: count}
}

func (c *Collector) botCached(now time.Time) Bot {
	if now.Sub(c.botAt) < 5*time.Second {
		return c.botCache
	}
	c.botCache = pollBot()
	c.botAt = now
	return c.botCache
}

// pollBot 读运行锁 owner.json，检查 bot 托管进程是否存活。
// 锁结构:bots-runtime-locks/{feishu-websocket,weixin-polling}/<hash>.lock/owner.json
func pollBot() Bot {
	matches, err := filepath.Glob(filepath.Join(botLocksDir, "*", "*", "owner.json"))
	if err != nil || len(matches) == 0 {
		return Bot{Err: "no lock"}
	}
	alive := map[int]bool{}
	for _, f := range matches {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var o struct {
			Pid int `json:"pid"`
		}
		if json.Unmarshal(b, &o) != nil || o.Pid == 0 {
			continue
		}
		if ok, err := process.PidExists(int32(o.Pid)); err == nil && ok {
			alive[o.Pid] = true
		}
	}
	if len(alive) == 0 {
		return Bot{Err: "stopped", Channels: len(matches)}
	}
	min := 0
	for pid := range alive {
		if min == 0 || pid < min {
			min = pid
		}
	}
	return Bot{Online: true, Pid: min, Channels: len(matches)}
}

// ZCodeRunning ZCode 桌面端是否在运行（启动前防重复）。
func ZCodeRunning() bool { return probeZCode().Running }

// ZCodePID 当前 ZCode 桌面端最小存活 pid(0=未运行),供停止用——
// 审计 2026-08-28:按 PID 杀而非按镜像名杀,避免误杀用户手动开的第二个实例。
func ZCodePID() int { return probeZCode().Pid }

// BotPID 当前存活的 bot 运行时 pid（0=无），供停止/重启使用。
func BotPID() int { return pollBot().Pid }

func (c *Collector) diskRate(name string) (float64, float64) {
	ics, err := disk.IOCounters(name)
	if err != nil {
		return 0, 0
	}
	ic, ok := ics[name]
	if !ok {
		for _, v := range ics {
			ic = v
			break
		}
	}
	elapsed := time.Since(c.prevDiskTime).Seconds()
	if c.firstDisk || elapsed <= 0 {
		c.prevDiskR, c.prevDiskW = ic.ReadBytes, ic.WriteBytes
		c.prevDiskTime = time.Now()
		c.firstDisk = false
		return 0, 0
	}
	rMB := float64(ic.ReadBytes-c.prevDiskR) / elapsed / 1024 / 1024
	wMB := float64(ic.WriteBytes-c.prevDiskW) / elapsed / 1024 / 1024
	c.prevDiskR, c.prevDiskW = ic.ReadBytes, ic.WriteBytes
	c.prevDiskTime = time.Now()
	if rMB < 0 {
		rMB = 0
	}
	if wMB < 0 {
		wMB = 0
	}
	return rMB, wMB
}

func (c *Collector) netRate() (rxRate, txRate float64, rxTotal, txTotal string) {
	ics, err := net.IOCounters(true)
	if err != nil {
		return 0, 0, "0B", "0B"
	}
	var rx, tx uint64
	for _, ic := range ics {
		name := strings.ToLower(ic.Name)
		if strings.Contains(name, "loopback") || strings.Contains(name, "pseudo") {
			continue
		}
		rx += ic.BytesRecv
		tx += ic.BytesSent
	}
	elapsed := time.Since(c.prevNetTime).Seconds()
	if c.firstNet || elapsed <= 0 {
		c.prevNetRx, c.prevNetTx = rx, tx
		c.prevNetTime = time.Now()
		c.firstNet = false
		return 0, 0, FormatBytes(rx), FormatBytes(tx)
	}
	rate := func(cur, prev uint64) float64 {
		v := float64(cur-prev) / elapsed / 1024 / 1024
		if v < 0 || v > 4096 {
			return 0
		}
		return v
	}
	rxRate = rate(rx, c.prevNetRx)
	txRate = rate(tx, c.prevNetTx)
	c.prevNetRx, c.prevNetTx = rx, tx
	c.prevNetTime = time.Now()
	return rxRate, txRate, FormatBytes(rx), FormatBytes(tx)
}
