package diagnostics

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sampleMemory parses /proc/meminfo into RAM and swap metrics.
func sampleMemory(now time.Time) (Metric, Metric) {
	ram := Metric{UpdatedAt: now.UnixMilli(), Unit: "bytes"}
	swap := Metric{UpdatedAt: now.UnixMilli(), Unit: "bytes"}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		ram.Reason = "/proc/meminfo unavailable"
		swap.Reason = ram.Reason
		return ram, swap
	}
	vals := parseMeminfo(string(b))
	total := vals["MemTotal"] * 1024
	avail := vals["MemAvailable"] * 1024
	if avail <= 0 {
		avail = (vals["MemFree"] + vals["Buffers"] + vals["Cached"]) * 1024
	}
	if total > 0 {
		ram.Available = true
		ram.Total = total
		ram.Value = total - avail
	} else {
		ram.Reason = "MemTotal missing"
	}
	if st := vals["SwapTotal"] * 1024; st > 0 {
		swap.Available = true
		swap.Total = st
		swap.Value = st - vals["SwapFree"]*1024
	} else {
		swap.Reason = "no swap configured"
	}
	return ram, swap
}

// parseMeminfo turns /proc/meminfo lines into numeric kB values.
func parseMeminfo(raw string) map[string]float64 {
	out := map[string]float64{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		out[key] = v
	}
	return out
}

// sampleLoad parses /proc/loadavg.
func sampleLoad() LoadMetric {
	m := LoadMetric{}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		m.Reason = "/proc/loadavg unavailable"
		return m
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		m.Reason = "unexpected /proc/loadavg format"
		return m
	}
	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64) //nolint:errcheck // malformed load averages degrade to zero
		return v
	}
	m.One, m.Five, m.Fifteen = parse(fields[0]), parse(fields[1]), parse(fields[2])
	m.Available = true
	return m
}

// nvidiaSMI resolves the vendor tool once so a missing GPU does not cost a
// filesystem lookup on every tick.
var (
	nvidiaSMIOnce sync.Once
	nvidiaSMIPath string
)

// resolveNvidiaSMI locates nvidia-smi, caching a negative result.
func resolveNvidiaSMI() string {
	nvidiaSMIOnce.Do(func() {
		path, err := exec.LookPath("nvidia-smi")
		if err == nil {
			nvidiaSMIPath = path
		}
	})
	return nvidiaSMIPath
}

// queryNvidia returns vendor GPU inventory and utilization.
func queryNvidia(ctx context.Context) GPUSummary {
	out := GPUSummary{}
	bin := resolveNvidiaSMI()
	if bin == "" {
		out.Reason = "nvidia-smi not available"
		return out
	}
	fields := []string{"name", "driver_version", "memory.total", "memory.used", "utilization.gpu"}
	raw, err := runCommand(ctx, time.Second, bin,
		"--query-gpu="+strings.Join(fields, ","), "--format=csv,noheader,nounits")
	if err != nil || raw == "" {
		out.Reason = "nvidia-smi query failed"
		return out
	}
	// Multiple GPUs are possible; report the first row.
	line := strings.SplitN(raw, "\n", 2)[0]
	parts := strings.Split(line, ",")
	if len(parts) < len(fields) {
		out.Reason = "unexpected nvidia-smi output"
		return out
	}
	out.Name = strings.TrimSpace(parts[0])
	out.DriverVersion = strings.TrimSpace(parts[1])
	out.MemoryTotal = mibToBytes(parts[2])
	out.MemoryUsed = mibToBytes(parts[3])
	out.UtilizationPct = parseFloat(parts[4])
	out.Available = true
	return out
}

// sampleGPU converts the GPU inventory into gauge metrics.
func sampleGPU(ctx context.Context, now time.Time) (Metric, Metric) {
	gpu := Metric{UpdatedAt: now.UnixMilli(), Unit: "percent"}
	vram := Metric{UpdatedAt: now.UnixMilli(), Unit: "bytes"}
	info := queryNvidia(ctx)
	if !info.Available {
		gpu.Reason = info.Reason
		vram.Reason = info.Reason
		return gpu, vram
	}
	gpu.Available = true
	gpu.Value = info.UtilizationPct
	if info.MemoryTotal > 0 {
		vram.Available = true
		vram.Total = float64(info.MemoryTotal)
		vram.Value = float64(info.MemoryUsed)
	} else {
		vram.Reason = "VRAM total not reported"
	}
	return gpu, vram
}

// mibToBytes parses an nvidia-smi MiB value into bytes.
func mibToBytes(s string) int64 {
	return int64(parseFloat(s) * 1024 * 1024)
}

// parseFloat parses a trimmed vendor value, yielding zero on malformed input.
func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
