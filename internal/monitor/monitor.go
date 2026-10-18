// Package monitor reads GPU, RAM, disk and SoC metrics from the local machine.
package monitor

import (
	"context"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Metrics is the wire shape used by /api/system/metrics and /ws/metrics.
type Metrics struct {
	GPUUtilization     int     `json:"gpu_utilization"`
	GPUMemoryUsedMB    int     `json:"gpu_memory_used_mb"`
	GPUMemoryTotalMB   int     `json:"gpu_memory_total_mb"`
	GPUTemperature     int     `json:"gpu_temperature"`
	GPUTempHeadroom    *int    `json:"gpu_temp_headroom"`
	GPUThermalSlowdown bool    `json:"gpu_thermal_slowdown"`
	SOCTemperature     *int    `json:"soc_temperature"`
	SOCCritical        *int    `json:"soc_critical"`
	GPUName            string  `json:"gpu_name"`
	RAMUsedGB          float64 `json:"ram_used_gb"`
	RAMTotalGB         float64 `json:"ram_total_gb"`
	DiskUsedGB         float64 `json:"disk_used_gb"`
	DiskTotalGB        float64 `json:"disk_total_gb"`
	DiskFreeGB         float64 `json:"disk_free_gb"`
}

// Read gathers metrics from hardware. Any source that fails is silently
// skipped and its fields keep the zero values, matching upstream behaviour.
func Read(ctx context.Context) Metrics {
	var m Metrics
	// nvidia-smi is a subprocess; cap it so a wedged driver cannot hang the
	// metrics endpoint (or the 1 Hz websocket loop) forever.
	gpuCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	g, ok := readGPU(gpuCtx)
	cancel()
	if ok {
		m = g
	}
	m.SOCTemperature, m.SOCCritical = readSOCTemperature()
	if total, avail, ok := readMeminfo("/proc/meminfo"); ok {
		m.RAMTotalGB = round1(float64(total) / 1048576.0)
		m.RAMUsedGB = round1(float64(total-avail) / 1048576.0)
	}
	if total, used, free, ok := readDisk("/home"); ok {
		m.DiskTotalGB = round1(total / (1024 * 1024 * 1024))
		m.DiskUsedGB = round1(used / (1024 * 1024 * 1024))
		m.DiskFreeGB = round1(free / (1024 * 1024 * 1024))
	}
	return m
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func readGPU(ctx context.Context) (Metrics, bool) {
	args := []string{
		"--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,name," +
			"temperature.gpu.tlimit,clocks_event_reasons.sw_thermal_slowdown," +
			"clocks_event_reasons.hw_thermal_slowdown",
		"--format=csv,noheader,nounits",
	}
	cmd := exec.CommandContext(ctx, "nvidia-smi", args...)
	out, err := cmd.Output()
	if err != nil {
		return Metrics{}, false
	}
	return parseNvidiaSmi(string(out))
}

func parseNvidiaSmi(out string) (Metrics, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return Metrics{}, false
	}
	parts := splitCSV(lines[0])
	if len(parts) < 5 {
		return Metrics{}, false
	}
	m := Metrics{
		GPUUtilization:   atoi(parts[0]),
		GPUMemoryUsedMB:  atoi(parts[1]),
		GPUMemoryTotalMB: atoi(parts[2]),
		GPUTemperature:   atoi(parts[3]),
		GPUName:          parts[4],
	}
	if len(parts) > 7 {
		if isDigits(parts[5]) {
			h := atoi(parts[5])
			m.GPUTempHeadroom = &h
		}
		m.GPUThermalSlowdown = strings.Contains(parts[6], "Active") || strings.Contains(parts[7], "Active")
	}
	return m, true
}

func splitCSV(line string) []string {
	var parts []string
	for _, p := range strings.Split(line, ",") {
		parts = append(parts, strings.TrimSpace(p))
	}
	return parts
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func isDigits(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readDisk returns total, used and free bytes for path, computed exactly like
// upstream's shutil.disk_usage: used comes from f_bfree (not f_bavail), so
// total is not necessarily used+free once the root-reserved blocks are in play.
func readDisk(path string) (total, used, free float64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, false
	}
	bsize := float64(st.Bsize)
	total = bsize * float64(st.Blocks)
	free = bsize * float64(st.Bavail)
	used = bsize * float64(st.Blocks-st.Bfree)
	return total, used, free, true
}
