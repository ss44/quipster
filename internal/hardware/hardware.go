// Package hardware detects FFmpeg hardware-accelerated decode/encode backends
// (NVENC, Intel QuickSync, VAAPI, Apple VideoToolbox) and builds the ffmpeg
// flag pipelines to use them. When no backend is available the CPU software
// pipeline is used instead.
package hardware

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Backend names an FFmpeg hardware acceleration backend.
type Backend string

// Supported hardware backends, in probe priority order.
const (
	BackendNone         Backend = "none"         // no usable hardware backend; CPU pipeline
	BackendNvenc        Backend = "nvenc"        // NVIDIA NVDEC/NVENC (cuda)
	BackendQsv          Backend = "qsv"          // Intel QuickSync
	BackendVaapi        Backend = "vaapi"        // Intel/AMD iGPU on Linux (/dev/dri)
	BackendVideotoolbox Backend = "videotoolbox" // Apple VideoToolbox
)

// Pipeline is one complete transcode plan: input-side decode flags, the video
// filter, and the H.264 encoder. Filter and Encoder are never empty; a CPU
// plan decodes in software, scales with "scale", and encodes with libx264.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Pipeline struct {
	Input   []string // input-side flags to insert before -i; nil for CPU
	Filter  string   // -vf value
	Encoder string   // -c:v value
	IsHW    bool     // true when Encoder is a hardware encoder
}

// Capabilities describes a detected hardware acceleration backend. The zero
// value is the CPU-only configuration.
type Capabilities struct {
	Backend Backend
	Device  string // DRM render node for vaapi, e.g. /dev/dri/renderD128
}

// Available reports whether a hardware backend was detected.
func (c Capabilities) Available() bool { return c.Backend != BackendNone && c.Backend != "" }

// backendSpec is everything that varies between backends.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type backendSpec struct {
	hwaccel    string   // -hwaccel decoder name
	gpuFrames  bool     // pipeline can keep frames in GPU memory end to end
	gpuScale   string   // GPU scale filter template (%s = resolution); "" = CPU scale
	encoder    string   // hardware H.264 encoder
	probe      []string // synthetic encode args verifying the backend works
	needDevice bool     // probe requires a DRM render node (vaapi)
}

var specs = map[Backend]backendSpec{
	BackendNvenc: {
		hwaccel: "cuda", gpuFrames: true,
		gpuScale: "scale_cuda=%s:format=nv12", encoder: "h264_nvenc",
		probe: []string{"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2",
			"-c:v", "h264_nvenc", "-f", "null", "-"},
	},
	BackendQsv: {
		hwaccel: "qsv", encoder: "h264_qsv",
		probe: []string{"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2",
			"-c:v", "h264_qsv", "-f", "null", "-"},
	},
	BackendVaapi: {
		hwaccel: "vaapi", gpuFrames: true, needDevice: true,
		gpuScale: "scale_vaapi=%s:format=nv12", encoder: "h264_vaapi",
		probe: []string{"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2",
			"-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi", "-f", "null", "-"},
	},
	BackendVideotoolbox: {
		hwaccel: "videotoolbox", encoder: "h264_videotoolbox",
		probe: []string{"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2",
			"-c:v", "h264_videotoolbox", "-f", "null", "-"},
	},
}

// probeOrder is the priority in which backends are probed when HWACCEL=auto.
// Probes for backends the ffmpeg binary was not built with fail fast, so the
// order only matters on machines with several usable GPUs.
var probeOrder = []Backend{BackendNvenc, BackendQsv, BackendVaapi, BackendVideotoolbox}

// DecodeArgs returns input-side flags that decode with hardware acceleration
// while handing frames back to system memory, so CPU filters and encoders
// taking software frames can consume them. nil when no backend is available.
func (c Capabilities) DecodeArgs() []string {
	if spec, ok := specs[c.Backend]; ok {
		return []string{"-hwaccel", spec.hwaccel}
	}
	return nil
}

// Pipeline returns a usable transcode plan for `res` (a ffmpeg resolution
// string like "960x540" or "1280x720"). Backends that keep frames in GPU
// memory (vaapi, nvenc) use their GPU scaler; backends that decode into
// system memory (qsv, videotoolbox) scale on the CPU and let the hardware
// encoder upload. Unavailable backends yield the CPU plan.
func (c Capabilities) Pipeline(res string) Pipeline {
	p := Pipeline{Filter: "scale=" + res, Encoder: "libx264"}
	spec, ok := specs[c.Backend]
	if !ok {
		return p
	}
	p.Encoder = spec.encoder
	p.IsHW = true
	p.Input = c.DecodeArgs()
	if spec.gpuFrames {
		if c.Backend == BackendVaapi {
			// -vaapi_device makes the DRM render node explicit; without it
			// ffmpeg would pick the first default node, which can differ
			// from the probed device on multi-GPU machines.
			p.Input = append([]string{"-vaapi_device", c.renderNode()}, p.Input...)
		}
		p.Input = append(p.Input, "-hwaccel_output_format", spec.hwaccel)
		p.Filter = fmt.Sprintf(spec.gpuScale, res)
	}
	return p
}

// renderNode returns the DRM render node to use for vaapi pipelines: the
// detected device, or ffmpeg's default first render node when detection ran
// without one (forced backend, device not reported).
func (c Capabilities) renderNode() string {
	if c.Device != "" {
		return c.Device
	}
	return "/dev/dri/renderD128"
}

// Detect probes supported backends in priority order and returns the first
// available one. The HWACCEL env var overrides selection:
//
//	"auto" (default): probe everything in priority order
//	"none" / "off" / "cpu": disable hardware acceleration
//	a backend name ("nvenc", "qsv", "vaapi", "videotoolbox"): force that
//	backend; if its probe fails, fall back to the CPU pipeline
func Detect(ffmpegBin string) Capabilities {
	force := strings.ToLower(strings.TrimSpace(os.Getenv("HWACCEL")))
	switch force {
	case "", "auto":
		for _, b := range probeOrder {
			if c, ok := probeBackend(ffmpegBin, b); ok {
				return c
			}
		}
	case "none", "off", "cpu", "false", "0":
	case "nvenc", "qsv", "vaapi", "videotoolbox":
		if c, ok := probeBackend(ffmpegBin, Backend(force)); ok {
			return c
		}
		log.Printf("[hw] forced backend %q is not available, falling back to cpu", force) //nolint:gosec // G706: force comes from the operator's own env var
	default:
		log.Printf("[hw] unknown HWACCEL value %q, probing all backends", force) //nolint:gosec // G706: force comes from the operator's own env var
		for _, b := range probeOrder {
			if c, ok := probeBackend(ffmpegBin, b); ok {
				return c
			}
		}
	}
	return Capabilities{}
}

// probeBackend runs a short synthetic encode through the backend's encoder to
// verify the ffmpeg build and the driver actually work.
func probeBackend(ffmpegBin string, b Backend) (Capabilities, bool) {
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return Capabilities{}, false // ffmpeg missing, always CPU
	}
	spec, ok := specs[b]
	if !ok {
		return Capabilities{}, false
	}
	dev := ""
	args := []string{"-v", "error", "-y"}
	if spec.needDevice {
		dev = vaapiDevice()
		if dev == "" {
			return Capabilities{}, false
		}
		args = append(args, "-init_hw_device", "vaapi=va:"+dev)
	}
	args = append(args, spec.probe...)
	if exec.Command(ffmpegBin, args...).Run() != nil {
		return Capabilities{}, false
	}
	return Capabilities{Backend: b, Device: dev}, true
}

// vaapiDevice returns the first available GPU render node, or "".
func vaapiDevice() string {
	for _, dev := range []string{"/dev/dri/renderD128", "/dev/dri/renderD129", "/dev/dri/renderD130"} {
		if _, err := os.Stat(dev); err == nil {
			return dev
		}
	}
	return ""
}

// Selection records how the active backend was chosen, so diagnostics can
// distinguish an intentional CPU configuration from an unexpected fallback.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Selection struct {
	Requested string `json:"requested"` // normalized HWACCEL value: auto | none | nvenc | qsv | vaapi | videotoolbox
	Backend   string `json:"backend"`   // detected backend, empty when CPU
	Device    string `json:"device"`    // DRM render node for vaapi
	Encoder   string `json:"encoder"`   // active H.264 encoder (hardware or libx264)
	Decoder   string `json:"decoder"`   // -hwaccel decoder name, empty for CPU
	Available bool   `json:"available"`
	Reason    string `json:"reason"` // why the active mode was chosen, especially fallbacks
}

// StatsSnapshot is a point-in-time copy of the runtime encode counters.
type StatsSnapshot struct {
	LastFallbackNote string `json:"lastFallbackNote"`
	HWEncodes        int64  `json:"hwEncodes"`
	CPURenders       int64  `json:"cpuRenders"`
	Fallbacks        int64  `json:"fallbacks"`
	LastFallbackAt   int64  `json:"lastFallbackAt"`
}

// Stats tracks how often the hardware pipeline succeeded or fell back. The
// counters are process-lifetime totals; diagnostics derives rates from them.
type Stats struct {
	lastNote   atomic.Value // string
	hwEncodes  atomic.Int64
	cpuRenders atomic.Int64
	fallbacks  atomic.Int64
	lastBackAt atomic.Int64
}

// RecordHW notes one successful hardware render.
func (s *Stats) RecordHW() {
	if s == nil {
		return
	}
	s.hwEncodes.Add(1)
}

// RecordCPU notes one render that ran on the software pipeline by design.
func (s *Stats) RecordCPU() {
	if s == nil {
		return
	}
	s.cpuRenders.Add(1)
}

// RecordFallback notes a hardware render that failed and was retried on CPU.
func (s *Stats) RecordFallback(note string, atMillis int64) {
	if s == nil {
		return
	}
	s.fallbacks.Add(1)
	s.lastBackAt.Store(atMillis)
	if note != "" {
		s.lastNote.Store(note)
	}
}

// Snapshot returns a race-free copy of the counters.
func (s *Stats) Snapshot() StatsSnapshot {
	if s == nil {
		return StatsSnapshot{}
	}
	snap := StatsSnapshot{
		HWEncodes:      s.hwEncodes.Load(),
		CPURenders:     s.cpuRenders.Load(),
		Fallbacks:      s.fallbacks.Load(),
		LastFallbackAt: s.lastBackAt.Load(),
	}
	if v, ok := s.lastNote.Load().(string); ok {
		snap.LastFallbackNote = v
	}
	return snap
}

// Status is the serializable hardware state surfaced by the diagnostics API.
type Status struct {
	Selection
	Stats StatsSnapshot `json:"stats"`
}

// Monitor owns the active backend and its runtime counters. It is safe for
// concurrent use and supports re-probing after a driver or container change.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Monitor struct {
	mu        sync.RWMutex
	caps      Capabilities
	sel       Selection
	stats     Stats
	ffmpegBin string
}

// NewMonitor probes the given ffmpeg binary and records how the backend was
// selected. A missing binary yields the CPU plan rather than an error.
func NewMonitor(ffmpegBin string) *Monitor {
	caps, sel := DetectWithSelection(ffmpegBin)
	return &Monitor{caps: caps, sel: sel, ffmpegBin: ffmpegBin}
}

// MonitorFor wraps an already-detected capability set, primarily for tests and
// callers that supply their own probe result.
func MonitorFor(caps Capabilities) *Monitor {
	return &Monitor{caps: caps, sel: selectionFor(caps, normalizedRequested(os.Getenv("HWACCEL")))}
}

// Capabilities returns the current backend. The nil receiver yields the CPU
// plan so services can be constructed without hardware support in tests.
func (m *Monitor) Capabilities() Capabilities {
	if m == nil {
		return Capabilities{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caps
}

// Status returns the current selection plus runtime counters.
func (m *Monitor) Status() Status {
	if m == nil {
		return Status{Selection: Selection{Requested: "auto", Encoder: "libx264", Reason: "hardware monitor unavailable"}}
	}
	m.mu.RLock()
	sel := m.sel
	m.mu.RUnlock()
	return Status{Selection: sel, Stats: m.stats.Snapshot()}
}

// Refresh re-runs backend probing and replaces the active selection.
func (m *Monitor) Refresh() Status {
	if m == nil {
		return Status{}
	}
	caps, sel := DetectWithSelection(m.ffmpegBin)
	m.mu.Lock()
	m.caps = caps
	m.sel = sel
	m.mu.Unlock()
	return m.Status()
}

// RecordHW notes a successful hardware render.
func (m *Monitor) RecordHW() {
	if m == nil {
		return
	}
	m.stats.RecordHW()
}

// RecordCPU notes a software render.
func (m *Monitor) RecordCPU() {
	if m == nil {
		return
	}
	m.stats.RecordCPU()
}

// RecordFallback notes a hardware render that fell back to software.
func (m *Monitor) RecordFallback(note string, atMillis int64) {
	if m == nil {
		return
	}
	m.stats.RecordFallback(note, atMillis)
}

// DetectWithSelection probes backends like Detect and also reports how the
// result relates to the requested HWACCEL setting.
func DetectWithSelection(ffmpegBin string) (Capabilities, Selection) {
	requested := normalizedRequested(os.Getenv("HWACCEL"))
	caps := Detect(ffmpegBin)
	return caps, selectionFor(caps, requested)
}

// normalizedRequested maps the operator's HWACCEL value onto the selection
// vocabulary. Empty means auto.
func normalizedRequested(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", "auto":
		return "auto"
	case "none", "off", "cpu", "false", "0":
		return "none"
	default:
		return v
	}
}

// selectionFor builds the user-facing selection for a probe result.
func selectionFor(caps Capabilities, requested string) Selection {
	sel := Selection{Requested: requested, Available: caps.Available()}
	if caps.Available() {
		spec := specs[caps.Backend]
		sel.Backend = string(caps.Backend)
		sel.Device = caps.Device
		sel.Encoder = spec.encoder
		sel.Decoder = spec.hwaccel
		return sel
	}
	sel.Encoder = "libx264"
	switch {
	case requested == "none":
		sel.Reason = "hardware acceleration disabled via HWACCEL"
	case requested == "auto":
		sel.Reason = "no usable hardware backend detected"
	default:
		sel.Reason = fmt.Sprintf("requested %s but the probe failed; using CPU", requested)
	}
	return sel
}
