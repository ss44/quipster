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
