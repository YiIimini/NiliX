package sysmon

import (
	"strconv"
	"strings"
)

// parseFloatClean 解析 nvidia-smi 数值字段:剥离单位/百分号等非数字残留
// (部分驱动忽略 --nounits,输出 "5 %" / "20987 MiB";直接 ParseFloat 会失败 → 温度/显存错乱)
func parseFloatClean(s string) float64 {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	v, _ := strconv.ParseFloat(b.String(), 64)
	return v
}

// GPUInfo 通过 nvidia-smi 查询 GPU 利用率/温度/显存。
func GPUInfo() GPU {
	g := GPU{Present: false}
	out, err := hiddenCmd("nvidia-smi",
		"--query-gpu=utilization.gpu,temperature.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return g
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	parts := strings.Split(line, ",")
	if len(parts) < 4 {
		return g
	}
	g.Present = true
	g.Usage = parseFloatClean(parts[0])
	g.Temp = parseFloatClean(parts[1])
	usedMB := parseFloatClean(parts[2])
	totalMB := parseFloatClean(parts[3])
	g.MemUsed = FormatBytes(uint64(usedMB) * 1024 * 1024)
	g.MemTotal = FormatBytes(uint64(totalMB) * 1024 * 1024)
	// 显存使用率(used/total 百分比):灵动岛/工作台据此显示"占用 20.5/23.9 GB (86%)"
	if totalMB > 0 {
		g.MemPercent = usedMB / totalMB * 100
	}
	return g
}

// SharedGPUMem 读取共享显存（系统内存借用给 GPU）的当前占用，单位字节。
func SharedGPUMem() uint64 {
	cmd := hiddenCmd("powershell", "-NoProfile", "-Command",
		"Get-CimInstance -Namespace root/cimv2 -ClassName Win32_PerfFormattedData_GPUPerformanceCounters_GPUAdapterMemory | Where-Object { $_.DedicatedUsage -gt 0 } | Select-Object -First 1 -ExpandProperty SharedUsage")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// ACPI 热区/MSAcpi 温度链已于 2026-08-26 移除:热区值与 CPU 核心温度脱节(恒 45℃ 上下),
// 属「温度不准」假源;核心温度唯一来源 = LHM(需管理员),读不到时前端显示 N/A。
