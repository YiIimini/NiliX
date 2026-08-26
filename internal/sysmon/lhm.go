package sysmon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LHM LibreHardwareMonitor 传感器读取（可选温度源）。
// 常驻运行 lhmsensor.exe，解析 stdout 每 2s 一行 {cpu,mem,gpu}(℃)。
// 复用 sysmon-widget 已部署的 lhmsensor；找不到则静默降级（用 nvidia-smi + ACPI）。
type LHM struct {
	mu  sync.Mutex
	cpu *float64
	mem *float64
	gpu *float64
}

func NewLHM() *LHM {
	l := &LHM{}
	go l.run()
	return l
}

const lhmExe = "lhmsensor.exe"

// lhmExePath 定位 lhmsensor.exe：优先项目内 lhmsensor/（随服务分发，main 已 Chdir 到 exe 目录），
// 兜底旧 sysmon-widget 部署路径；都找不到返回空（静默降级）。
func lhmExePath() string {
	cands := []string{
		filepath.Join("lhmsensor", lhmExe),
		`C:\Mi\Ai\WorkBench\sysmon-widget\lhmsensor\bin\` + lhmExe,
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// lhmsensor 只读传感器、双开无冲突;外部已跑的实例 stdout 我们读不到,
// 绝不能因「进程已存在」而放弃自启——那会让温度永远回退 ACPI 热区假值(2026-08-26 修复)。
func (l *LHM) run() {
	exe := lhmExePath()
	if exe == "" {
		return
	}
	cmd := hiddenCmd(exe)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var d struct {
			CPU *float64 `json:"cpu"`
			MEM *float64 `json:"mem"`
			GPU *float64 `json:"gpu"`
		}
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		l.mu.Lock()
		l.cpu = validTemp(d.CPU, 120)
		l.mem = validTemp(d.MEM, 120)
		l.gpu = validTemp(d.GPU, 100)
		l.mu.Unlock()
	}
	_ = cmd.Wait()
}

func validTemp(v *float64, max float64) *float64 {
	if v == nil || *v <= 0 || *v > max {
		return nil
	}
	return v
}

func (l *LHM) CPUTemp() (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cpu == nil {
		return 0, false
	}
	return *l.cpu, true
}

func (l *LHM) MemTemp() (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mem == nil {
		return 0, false
	}
	return *l.mem, true
}

// GPUTemp LHM 的 GPU 温度(仅作 nvidia-smi 失效时的兜底源)。
func (l *LHM) GPUTemp() (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gpu == nil {
		return 0, false
	}
	return *l.gpu, true
}
