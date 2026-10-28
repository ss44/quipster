// Package diagnostics collects hardware, resource, benchmark, and environment
// telemetry for the admin diagnostics view. Collection is best-effort: every
// unavailable metric carries a reason instead of failing the whole snapshot.
package diagnostics

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"quipster/internal/hardware"
	"quipster/internal/logbuf"
	"quipster/internal/probe"
	"quipster/internal/store"
)

// Metric is one sampled gauge. Total is set for capacities (RAM, VRAM, disk).
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Metric struct {
	UpdatedAt int64   `json:"updatedAt"`
	Value     float64 `json:"value"`
	Total     float64 `json:"total,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Available bool    `json:"available"`
	Stale     bool    `json:"stale,omitempty"`
}

// LoadMetric carries the three standard load averages.
type LoadMetric struct {
	Reason    string  `json:"reason,omitempty"`
	One       float64 `json:"one"`
	Five      float64 `json:"five"`
	Fifteen   float64 `json:"fifteen"`
	Available bool    `json:"available"`
}

// DiskMetric describes free space for one configured directory.
type DiskMetric struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Reason     string `json:"reason,omitempty"`
	FreeBytes  int64  `json:"freeBytes"`
	TotalBytes int64  `json:"totalBytes"`
	Available  bool   `json:"available"`
}

// GaugeSnapshot is the one-second telemetry frame streamed to the UI.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type GaugeSnapshot struct {
	At         int64                 `json:"at"`
	CacheParts map[string]int64      `json:"cacheParts"`
	Disks      map[string]DiskMetric `json:"disks"`
	CPU        Metric                `json:"cpu"`
	ProcessCPU Metric                `json:"processCpu"`
	RAM        Metric                `json:"ram"`
	Swap       Metric                `json:"swap"`
	Load       LoadMetric            `json:"load"`
	GPU        Metric                `json:"gpu"`
	VRAM       Metric                `json:"vram"`
	Hardware   hardware.Status       `json:"hardware"`
	CacheBytes int64                 `json:"cacheBytes"`
	Queue      *store.JobStats       `json:"queue,omitempty"`
}

// VersionInfo is a resolved external tool version.
type VersionInfo struct {
	Version   string `json:"version"`
	Reason    string `json:"reason,omitempty"`
	Available bool   `json:"available"`
}

// GPUSummary is vendor-reported accelerator information.
type GPUSummary struct {
	Name           string  `json:"name"`
	DriverVersion  string  `json:"driverVersion"`
	Reason         string  `json:"reason,omitempty"`
	MemoryTotal    int64   `json:"memoryTotalBytes"`
	MemoryUsed     int64   `json:"memoryUsedBytes"`
	UtilizationPct float64 `json:"utilizationPct"`
	Available      bool    `json:"available"`
}

// PathInfo is one configured directory plus its current free space.
type PathInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Reason     string `json:"reason,omitempty"`
	FreeBytes  int64  `json:"freeBytes"`
	TotalBytes int64  `json:"totalBytes"`
	Available  bool   `json:"available"`
}

// SystemInfo is the slow-changing environment report.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type SystemInfo struct {
	GeneratedAt int64             `json:"generatedAt"`
	Config      map[string]string `json:"config"`
	Hardware    hardware.Status   `json:"hardware"`
	GPU         GPUSummary        `json:"gpu"`
	OSName      string            `json:"osName"`
	Kernel      string            `json:"kernel"`
	AppVersion  string            `json:"appVersion"`
	GoVersion   string            `json:"goVersion"`
	Arch        string            `json:"goarch"`
	OS          string            `json:"goos"`
	CPUModel    string            `json:"cpuModel"`
	FFmpeg      VersionInfo       `json:"ffmpeg"`
	FFprobe     VersionInfo       `json:"ffprobe"`
	Paths       []PathInfo        `json:"paths"`
	HWAccels    []string          `json:"hwaccels"`
	Encoders    []string          `json:"encoders"`
	Decoders    []string          `json:"decoders"`
	CPUCores    int               `json:"cpuCores"`
	AppUptimeS  int64             `json:"appUptimeSec"`
	DBBytes     int64             `json:"dbBytes"`
	MeiliUp     bool              `json:"meiliUp"`
	Container   bool              `json:"container"`
}

// cpuTimes is a raw /proc/stat sample used to compute a CPU percentage delta.
type cpuTimes struct {
	at    time.Time
	total float64
	idle  float64
}

// Collector gathers and caches diagnostics snapshots for the API. It is safe
// for concurrent use; expensive reads are cached briefly so multiple viewers
// share one sample.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Collector struct {
	St        *store.Store
	Hw        *hardware.Monitor
	LogBuffer *logbuf.Buffer
	MeiliUp   func() bool
	StartedAt time.Time
	Version   string
	DataDir   string
	CacheDir  string
	MediaDir  string

	mu           sync.Mutex
	lastGauge    *GaugeSnapshot
	lastGaugeAt  time.Time
	lastSystem   *SystemInfo
	lastSystemAt time.Time
	prevCPU      cpuTimes
	prevProcSec  float64
	prevProcAt   time.Time
	bench        map[string]benchCache
}

// New constructs a collector and records the process start time for uptime.
func New(st *store.Store, hw *hardware.Monitor, logs *logbuf.Buffer, meiliUp func() bool, version, dataDir, cacheDir, mediaDir string) *Collector {
	return &Collector{
		St:        st,
		Hw:        hw,
		LogBuffer: logs,
		MeiliUp:   meiliUp,
		StartedAt: time.Now(),
		Version:   version,
		DataDir:   dataDir,
		CacheDir:  cacheDir,
		MediaDir:  mediaDir,
	}
}

// Gauge returns a cached resource snapshot, refreshing it at most every 750ms.
func (c *Collector) Gauge(ctx context.Context) *GaugeSnapshot {
	if c == nil {
		return &GaugeSnapshot{At: time.Now().UnixMilli()}
	}
	c.mu.Lock()
	if c.lastGauge != nil && time.Since(c.lastGaugeAt) < 750*time.Millisecond {
		snap := c.lastGauge
		c.mu.Unlock()
		return snap
	}
	snap := c.sampleGauge(ctx)
	c.lastGauge = snap
	c.lastGaugeAt = time.Now()
	c.mu.Unlock()
	return snap
}

// sampleGauge reads one resource frame. Callers must hold c.mu.
func (c *Collector) sampleGauge(ctx context.Context) *GaugeSnapshot {
	now := time.Now()
	snap := &GaugeSnapshot{At: now.UnixMilli()}

	cpu, prev := sampleCPU(c.prevCPU, now)
	snap.CPU = cpu
	c.prevCPU = prev
	snap.ProcessCPU, c.prevProcSec, c.prevProcAt = sampleProcessCPU(c.prevProcSec, c.prevProcAt, now)
	snap.RAM, snap.Swap = sampleMemory(now)
	snap.Load = sampleLoad()
	snap.GPU, snap.VRAM = sampleGPU(ctx, now)
	snap.Disks = c.sampleDisks()
	snap.CacheParts, snap.CacheBytes = c.sampleCache()
	if qs, err := c.St.JobStats(); err == nil {
		snap.Queue = qs
	}
	if c.Hw != nil {
		snap.Hardware = c.Hw.Status()
	}
	return snap
}

// System returns cached environment information, refreshing after five minutes.
func (c *Collector) System(ctx context.Context) *SystemInfo {
	if c == nil {
		return &SystemInfo{GeneratedAt: time.Now().UnixMilli()}
	}
	c.mu.Lock()
	if c.lastSystem != nil && time.Since(c.lastSystemAt) < 5*time.Minute {
		sys := c.lastSystem
		c.mu.Unlock()
		return sys
	}
	c.mu.Unlock()

	sys := c.collectSystem(ctx)

	c.mu.Lock()
	c.lastSystem = sys
	c.lastSystemAt = time.Now()
	c.mu.Unlock()
	return sys
}

// RefreshHardware re-probes the backend and invalidates the system cache so the
// next read reports the new selection.
func (c *Collector) RefreshHardware(ctx context.Context) hardware.Status {
	if c == nil {
		return hardware.Status{}
	}
	var status hardware.Status
	if c.Hw != nil {
		status = c.Hw.Refresh()
	}
	c.mu.Lock()
	c.lastSystem = nil
	c.lastSystemAt = time.Time{}
	c.mu.Unlock()
	// Rebuild immediately so the caller gets the refreshed environment too.
	c.System(ctx)
	return status
}

// Logs returns up to limit recent log lines, oldest first.
func (c *Collector) Logs(limit int) []string {
	if c == nil || c.LogBuffer == nil {
		return nil
	}
	return c.LogBuffer.Lines(limit)
}

// collectSystem gathers the slow-changing environment report.
func (c *Collector) collectSystem(ctx context.Context) *SystemInfo {
	now := time.Now()
	sys := &SystemInfo{
		GeneratedAt: now.UnixMilli(),
		AppVersion:  c.Version,
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUCores:    runtime.NumCPU(),
		Container:   isContainer(),
		OSName:      osName(),
		Kernel:      kernelVersion(),
		CPUModel:    cpuModel(),
		FFmpeg:      toolVersion(ctx, probe.FfmpegBin()),
		FFprobe:     toolVersion(ctx, probe.FfprobeBin()),
		HWAccels:    ffmpegList(ctx, "-hwaccels"),
		Encoders:    ffmpegCodecs(ctx, "-encoders"),
		Decoders:    ffmpegCodecs(ctx, "-decoders"),
		Config:      safeConfig(),
		DBBytes:     dbBytes(c.DataDir),
		Paths:       c.paths(),
	}
	if c.MeiliUp != nil {
		sys.MeiliUp = c.MeiliUp()
	}
	if !c.StartedAt.IsZero() {
		sys.AppUptimeS = int64(time.Since(c.StartedAt).Seconds())
	}
	if c.Hw != nil {
		sys.Hardware = c.Hw.Status()
	}
	sys.GPU = queryNvidia(ctx)
	if !sys.GPU.Available && sys.Hardware.Available {
		sys.GPU.Reason = "vendor GPU tooling not available (nvidia-smi missing)"
	}
	return sys
}

// paths lists configured directories with free space.
func (c *Collector) paths() []PathInfo {
	dirs := []struct{ name, path string }{
		{"data", c.DataDir},
		{"cache", c.CacheDir},
		{"media", c.MediaDir},
	}
	out := make([]PathInfo, 0, len(dirs))
	for _, d := range dirs {
		p := PathInfo{Name: d.name, Path: d.path}
		if d.path == "" {
			p.Reason = "not configured"
			out = append(out, p)
			continue
		}
		free, total, err := diskFree(d.path)
		if err != nil {
			p.Reason = err.Error()
			out = append(out, p)
			continue
		}
		p.FreeBytes, p.TotalBytes, p.Available = free, total, true
		out = append(out, p)
	}
	return out
}

// sampleDisks builds the gauge disk map from the configured paths.
func (c *Collector) sampleDisks() map[string]DiskMetric {
	out := map[string]DiskMetric{}
	for _, p := range c.paths() {
		out[p.Name] = DiskMetric{
			Name:       p.Name,
			Path:       p.Path,
			FreeBytes:  p.FreeBytes,
			TotalBytes: p.TotalBytes,
			Available:  p.Available,
			Reason:     p.Reason,
		}
	}
	return out
}

// sampleCache reads cache byte totals by kind.
func (c *Collector) sampleCache() (map[string]int64, int64) {
	parts := map[string]int64{}
	var total int64
	if c.St == nil {
		return parts, total
	}
	for _, kind := range []string{"clip", "thumb", "sub"} {
		if n, err := c.St.CacheBytes(kind); err == nil {
			parts[kind] = n
			total += n
		}
	}
	return parts, total
}

// runCommand runs a command with a short timeout and returns trimmed output.
func runCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	//nolint:gosec // binary and args come from fixed internal lists, not user input
	out, err := exec.CommandContext(runCtx, name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// toolVersion returns the first line of `tool -version`.
func toolVersion(ctx context.Context, bin string) VersionInfo {
	out, err := runCommand(ctx, 2*time.Second, bin, "-version")
	if err != nil {
		return VersionInfo{Reason: "not available: " + err.Error()}
	}
	line := out
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return VersionInfo{Available: true, Version: line}
}

// ffmpegList returns non-header lines from an informational ffmpeg flag.
func ffmpegList(ctx context.Context, flag string) []string {
	out, err := runCommand(ctx, 2*time.Second, probe.FfmpegBin(), "-hide_banner", flag)
	if err != nil {
		return nil
	}
	list := make([]string, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		list = append(list, line)
	}
	return list
}

// ffmpegCodecs returns codec names of interest from `-encoders` or `-decoders`.
func ffmpegCodecs(ctx context.Context, flag string) []string {
	out, err := runCommand(ctx, 2*time.Second, probe.FfmpegBin(), "-hide_banner", flag)
	if err != nil {
		return nil
	}
	list := make([]string, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		if name == "=" || strings.HasPrefix(name, "-") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "h264") || strings.Contains(lower, "hevc") || strings.Contains(lower, "av1") || strings.Contains(lower, "aac") {
			list = append(list, name)
		}
	}
	return list
}

// safeConfig exposes non-secret operator settings. Secrets are never read, so
// they cannot leak into a report or bundle.
func safeConfig() map[string]string {
	keys := []string{
		"ROLE", "ADDR", "HWACCEL", "CLIP_PREVIEW_RES", "CLIP_ACCURATE_RES",
		"CLIP_CPU_PRESET", "SUBTITLE_WORKERS", "INDEX_WORKERS", "CLIP_WORKERS",
		"CACHE_MAX_GB", "MAX_CLIP_SEC", "PROBE_TIMEOUT", "SCAN_INTERVAL",
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			out[k] = v
		}
	}
	// HWACCEL defaults to auto when unset; showing that makes the diagnostic
	// report interpretable without requiring the operator to know the default.
	if _, ok := out["HWACCEL"]; !ok {
		out["HWACCEL"] = "auto"
	}
	return out
}

// osName reads PRETTY_NAME from /etc/os-release, falling back to the Go OS.
func osName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

// kernelVersion reads the kernel release from /proc/sys/kernel/osrelease.
func kernelVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// cpuModel reads the model name from /proc/cpuinfo.
func cpuModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") {
			if idx := strings.IndexByte(line, ':'); idx >= 0 {
				return strings.TrimSpace(line[idx+1:])
			}
		}
	}
	return ""
}

// isContainer reports whether the process appears to run in a Docker container.
func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	return err == nil && strings.Contains(string(b), "docker")
}

// dbBytes reports the SQLite database size including WAL/SHM sidecars.
func dbBytes(dataDir string) int64 {
	if dataDir == "" {
		return 0
	}
	var total int64
	for _, name := range []string{"quipster.db", "quipster.db-wal", "quipster.db-shm"} {
		if info, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			total += info.Size()
		}
	}
	return total
}

// sampleCPU computes system CPU utilization from two /proc/stat samples.
func sampleCPU(prev cpuTimes, now time.Time) (Metric, cpuTimes) {
	m := Metric{UpdatedAt: now.UnixMilli(), Unit: "percent"}
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		m.Reason = "/proc/stat unavailable"
		return m, cpuTimes{}
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		m.Reason = "unexpected /proc/stat format"
		return m, cpuTimes{}
	}
	var total, idle float64
	for i, f := range fields[1:] {
		v, perr := strconv.ParseFloat(f, 64)
		if perr != nil {
			continue
		}
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	cur := cpuTimes{at: now, total: total, idle: idle}
	if prev.at.IsZero() || total <= prev.total {
		m.Reason = "warming up"
		return m, cur
	}
	dtotal := total - prev.total
	didle := idle - prev.idle
	if dtotal <= 0 {
		m.Reason = "no CPU delta"
		return m, cur
	}
	m.Available = true
	m.Value = clampPct(100 * (dtotal - didle) / dtotal)
	return m, cur
}

// sampleProcessCPU computes process CPU percent from getrusage deltas. It
// returns the metric plus the new sample for the next call.
func sampleProcessCPU(prev float64, prevAt, now time.Time) (Metric, float64, time.Time) {
	m := Metric{UpdatedAt: now.UnixMilli(), Unit: "percent"}
	secs, ok := processCPUSeconds()
	if !ok {
		m.Reason = "process CPU counter unavailable"
		return m, prev, prevAt
	}
	if prevAt.IsZero() {
		m.Reason = "warming up"
		return m, secs, now
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		m.Reason = "warming up"
		return m, secs, now
	}
	m.Available = true
	m.Value = clampPct(100 * (secs - prev) / elapsed)
	return m, secs, now
}

// clampPct bounds a percentage to [0, 100*cores] so transient counter jumps
// cannot render nonsensically.
func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	cpuMax := float64(runtime.NumCPU()) * 100
	if v > cpuMax {
		return cpuMax
	}
	return v
}
