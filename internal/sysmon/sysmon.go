// Package sysmon 提供系统监测（CPU/内存/GPU/磁盘/网络），从 sysmon-widget 的 stats 模块适配而来。
package sysmon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// KB 结构注释更新：KB 是 NiliX 内置知识库(kb_work)状态。
type KB struct {
	Online     bool   `json:"online"`
	Pages      int    `json:"pages"`
	Categories int    `json:"categories"`
	Err        string `json:"err"`
}

// ZCode ZCode 桌面端状态（进程探测）。
type ZCode struct {
	Running bool `json:"running"`
	Pid     int  `json:"pid"`
	Count   int  `json:"count"`
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
	Disk  Disk  `json:"disk"`
	Net   Net   `json:"net"`
	KB    KB    `json:"kb"`
	ZCode ZCode `json:"zcode"`
	Bot   Bot   `json:"bot"`
	Comfy Comfy `json:"comfy"`
	Meta  Meta  `json:"meta"`
	Ts    string `json:"ts"`
}

// Collector 定时采集器（含差值/缓存）。
type Collector struct {
	mu        sync.Mutex
	prevNetRx uint64
	prevNetTx uint64
	prevDiskR uint64
	prevDiskW uint64
	prevTime  time.Time
	first     bool

	cpuTemp    float64
	cpuTempOK  bool
	cpuTempAt  time.Time
	gpuCache   GPU
	gpuAt      time.Time
	cfyCache   Comfy
	cfyAt      time.Time
	cfyFail    int // 连续探测失败计数(粘滞:连续失败才翻转,防状态灯抖动)
	kbCache    KB
	kbAt       time.Time
	zcodeCache ZCode
	zcodeAt    time.Time
	botCache   Bot
	botAt      time.Time
	procsCount int
	procsAt    time.Time
	lhm        *LHM
}

// NewCollector 构造采集器。
func NewCollector() *Collector {
	c := &Collector{first: true, lhm: NewLHM()}
	_, _ = cpu.Percent(0, false)
	return c
}

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
	if t, ok := c.lhm.CPUTemp(); ok {
		s.CPU.Temp, s.CPU.HasTemp = t, true
	} else {
		s.CPU.Temp, s.CPU.HasTemp = c.cpuTempCached(now)
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
	s.Comfy = c.comfyCached(now)
	s.KB = c.kbCached(now)
	s.ZCode = c.zcodeCached(now)
	s.Bot = c.botCached(now)

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

func (c *Collector) cpuTempCached(now time.Time) (float64, bool) {
	if now.Sub(c.cpuTempAt) < 10*time.Second {
		return c.cpuTemp, c.cpuTempOK
	}
	t, ok := CPUTemp()
	c.cpuTemp, c.cpuTempOK, c.cpuTempAt = t, ok, now
	return t, ok
}

func (c *Collector) gpuCached(now time.Time) GPU {
	if now.Sub(c.gpuAt) < 2*time.Second {
		return c.gpuCache
	}
	g := GPUInfo()
	g.SharedUsed = FormatBytes(SharedGPUMem())
	c.gpuCache = g
	c.gpuAt = now
	return c.gpuCache
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

// KBURL NiliX 内置知识库（kb_work，zhishiku）的 API。
const KBURL = "http://127.0.0.1:8787/api/meta"

func (c *Collector) kbCached(now time.Time) KB {
	if now.Sub(c.kbAt) < 5*time.Second {
		return c.kbCache
	}
	c.kbCache = pollKB()
	c.kbAt = now
	return c.kbCache
}

// pollKB 探测 NiliX 内置知识库（kb_work，zhishiku）的 /api/meta。
func pollKB() KB {
	client := http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(KBURL)
	if err != nil {
		return KB{Err: "offline"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return KB{Err: "http " + http.StatusText(resp.StatusCode)}
	}
	var m struct {
		PageCount  int               `json:"pageCount"`
		Categories []json.RawMessage `json:"categories"`
	}
	if json.NewDecoder(resp.Body).Decode(&m) != nil {
		return KB{Err: "parse"}
	}
	return KB{Online: true, Pages: m.PageCount, Categories: len(m.Categories)}
}

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
	elapsed := time.Since(c.prevTime).Seconds()
	if c.first || elapsed <= 0 {
		c.prevDiskR, c.prevDiskW = ic.ReadBytes, ic.WriteBytes
		c.prevTime = time.Now()
		c.first = false
		return 0, 0
	}
	rMB := float64(ic.ReadBytes-c.prevDiskR) / elapsed / 1024 / 1024
	wMB := float64(ic.WriteBytes-c.prevDiskW) / elapsed / 1024 / 1024
	c.prevDiskR, c.prevDiskW = ic.ReadBytes, ic.WriteBytes
	c.prevTime = time.Now()
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
	elapsed := time.Since(c.prevTime).Seconds()
	if c.first || elapsed <= 0 {
		c.prevNetRx, c.prevNetTx = rx, tx
		c.prevTime = time.Now()
		c.first = false
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
	c.prevTime = time.Now()
	return rxRate, txRate, FormatBytes(rx), FormatBytes(tx)
}
