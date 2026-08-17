package sysmon

import (
	"strconv"
	"strings"
)

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
	g.Usage, _ = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	g.Temp, _ = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	usedMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	totalMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	g.MemUsed = FormatBytes(uint64(usedMB) * 1024 * 1024)
	g.MemTotal = FormatBytes(uint64(totalMB) * 1024 * 1024)
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

// ThermalZoneTemps 读取全部 ACPI 热区温度（℃）。
func ThermalZoneTemps() ([]float64, bool) {
	cmd := hiddenCmd("powershell", "-NoProfile", "-Command",
		"Get-CimInstance -Namespace root/cimv2 -ClassName Win32_PerfFormattedData_Counters_ThermalZoneInformation | Select-Object -ExpandProperty Temperature")
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var temps []float64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			continue
		}
		c := v - 273.15
		if c < 0 || c > 120 {
			continue
		}
		temps = append(temps, c)
	}
	return temps, len(temps) > 0
}

// CPUTemp 读取 CPU 温度：取热区最高值；回退 MSAcpi。
func CPUTemp() (float64, bool) {
	if temps, ok := ThermalZoneTemps(); ok {
		max := temps[0]
		for _, t := range temps {
			if t > max {
				max = t
			}
		}
		return max, true
	}
	cmd := hiddenCmd("powershell", "-NoProfile", "-Command",
		"Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature | Measure-Object -Property CurrentTemperature -Maximum | Select-Object -ExpandProperty Maximum")
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v/10 - 273.15, true
}
