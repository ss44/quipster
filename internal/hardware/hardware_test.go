package hardware

import (
	"strings"
	"testing"
)

func TestPipelinePerBackend(t *testing.T) {
	//nolint:govet // field grouping favors readability over optimal alignment.
	tests := []struct {
		name        string
		caps        Capabilities
		wantInput   []string
		wantDecode  []string
		wantFilter  string
		wantEncoder string
		wantHW      bool
	}{
		{
			name:        "none yields cpu plan",
			caps:        Capabilities{Backend: BackendNone},
			wantFilter:  "scale=1280x720",
			wantEncoder: "libx264",
		},
		{
			name:        "zero value yields cpu plan",
			caps:        Capabilities{},
			wantFilter:  "scale=1280x720",
			wantEncoder: "libx264",
		},
		{
			name:        "vaapi full gpu pipeline",
			caps:        Capabilities{Backend: BackendVaapi, Device: "/dev/dri/renderD128"},
			wantInput:   []string{"-vaapi_device", "/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"},
			wantDecode:  []string{"-hwaccel", "vaapi"},
			wantFilter:  "scale_vaapi=1280x720:format=nv12",
			wantEncoder: "h264_vaapi",
			wantHW:      true,
		},
		{
			name:        "vaapi without detected device uses default node",
			caps:        Capabilities{Backend: BackendVaapi},
			wantInput:   []string{"-vaapi_device", "/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"},
			wantDecode:  []string{"-hwaccel", "vaapi"},
			wantFilter:  "scale_vaapi=1280x720:format=nv12",
			wantEncoder: "h264_vaapi",
			wantHW:      true,
		},
		{
			name:        "nvenc full gpu pipeline",
			caps:        Capabilities{Backend: BackendNvenc},
			wantInput:   []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
			wantDecode:  []string{"-hwaccel", "cuda"},
			wantFilter:  "scale_cuda=1280x720:format=nv12",
			wantEncoder: "h264_nvenc",
			wantHW:      true,
		},
		{
			name:        "qsv decodes to system memory and scales on cpu",
			caps:        Capabilities{Backend: BackendQsv},
			wantInput:   []string{"-hwaccel", "qsv"},
			wantDecode:  []string{"-hwaccel", "qsv"},
			wantFilter:  "scale=1280x720",
			wantEncoder: "h264_qsv",
			wantHW:      true,
		},
		{
			name:        "videotoolbox decodes to system memory and scales on cpu",
			caps:        Capabilities{Backend: BackendVideotoolbox},
			wantInput:   []string{"-hwaccel", "videotoolbox"},
			wantDecode:  []string{"-hwaccel", "videotoolbox"},
			wantFilter:  "scale=1280x720",
			wantEncoder: "h264_videotoolbox",
			wantHW:      true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caps.Available(); got != tc.wantHW {
				t.Errorf("Available() = %v, want %v", got, tc.wantHW)
			}
			p := tc.caps.Pipeline("1280x720")
			assertArgs(t, "Pipeline.Input", p.Input, tc.wantInput)
			if p.Filter != tc.wantFilter {
				t.Errorf("Filter = %q, want %q", p.Filter, tc.wantFilter)
			}
			if p.Encoder != tc.wantEncoder {
				t.Errorf("Encoder = %q, want %q", p.Encoder, tc.wantEncoder)
			}
			if p.IsHW != tc.wantHW {
				t.Errorf("IsHW = %v, want %v", p.IsHW, tc.wantHW)
			}
			// DecodeArgs always hands frames back to system memory, so
			// CPU filters/encoders can consume them.
			assertArgs(t, "DecodeArgs", tc.caps.DecodeArgs(), tc.wantDecode)
		})
	}
}

func assertArgs(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", name, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
	}
}

func TestDetectDisabled(t *testing.T) {
	for _, v := range []string{"none", "off", "cpu", "false", "0"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("HWACCEL", v)
			if c := Detect("/nonexistent/ffmpeg"); c.Available() {
				t.Errorf("HWACCEL=%s: got backend %q, want cpu-only", v, c.Backend)
			}
		})
	}
}

func TestDetectForcedUnavailable(t *testing.T) {
	t.Setenv("HWACCEL", "videotoolbox")
	// ffmpeg binary does not exist, so probing must fail and fall back to cpu.
	if c := Detect("/nonexistent/ffmpeg"); c.Available() {
		t.Errorf("forced unavailable backend: got %q, want cpu-only", c.Backend)
	}
}

func TestDetectMissingFfmpeg(t *testing.T) {
	t.Setenv("HWACCEL", "auto")
	if c := Detect("/nonexistent/ffmpeg"); c.Available() {
		t.Errorf("missing ffmpeg: got %q, want cpu-only", c.Backend)
	}
}

func TestNormalizedRequested(t *testing.T) {
	cases := map[string]string{
		"": "auto", "auto": "auto", "NONE": "none", "off": "none",
		"cpu": "none", "false": "none", "0": "none", "nvenc": "nvenc",
		"VAAPI": "vaapi", " qsv ": "qsv",
	}
	for in, want := range cases {
		if got := normalizedRequested(in); got != want {
			t.Errorf("normalizedRequested(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectionForReasons(t *testing.T) {
	hw := selectionFor(Capabilities{Backend: BackendNvenc, Device: "/dev/nvidia0"}, "auto")
	if !hw.Available || hw.Encoder != "h264_nvenc" || hw.Decoder != "cuda" || hw.Backend != "nvenc" {
		t.Errorf("hardware selection = %+v", hw)
	}
	disabled := selectionFor(Capabilities{}, "none")
	if disabled.Available || disabled.Encoder != "libx264" || disabled.Reason == "" {
		t.Errorf("disabled selection = %+v", disabled)
	}
	forced := selectionFor(Capabilities{}, "qsv")
	if forced.Available || forced.Encoder != "libx264" || forced.Reason == "" {
		t.Errorf("forced fallback selection = %+v", forced)
	}
	auto := selectionFor(Capabilities{}, "auto")
	if auto.Reason != "no usable hardware backend detected" {
		t.Errorf("auto reason = %q", auto.Reason)
	}
}

func TestStatsAndMonitor(t *testing.T) {
	m := MonitorFor(Capabilities{Backend: BackendQsv})
	if !m.Capabilities().Available() {
		t.Fatal("monitor must expose wrapped capabilities")
	}
	m.RecordHW()
	m.RecordHW()
	m.RecordCPU()
	m.RecordFallback("driver hiccup", 1234)
	st := m.Status()
	if st.Stats.HWEncodes != 2 || st.Stats.CPURenders != 1 || st.Stats.Fallbacks != 1 {
		t.Fatalf("stats = %+v", st.Stats)
	}
	if st.Stats.LastFallbackNote != "driver hiccup" || st.Stats.LastFallbackAt != 1234 {
		t.Errorf("fallback details = %+v", st.Stats)
	}

	var nilMonitor *Monitor
	if nilMonitor.Capabilities().Available() {
		t.Error("nil monitor must report cpu")
	}
	nilMonitor.RecordHW()
	nilMonitor.RecordCPU()
	nilMonitor.RecordFallback("x", 1)
	if nilMonitor.Refresh().Available {
		t.Error("nil monitor refresh must be empty")
	}
	if nilMonitor.Status().Reason == "" {
		t.Error("nil monitor status must explain unavailability")
	}
}

func TestRefreshWithMissingBinary(t *testing.T) {
	t.Setenv("HWACCEL", "auto")
	m := NewMonitor("/nonexistent/ffmpeg")
	status := m.Refresh()
	if status.Available || status.Encoder != "libx264" {
		t.Errorf("refresh status = %+v", status)
	}
}

func TestDetectWithSelectionDisabled(t *testing.T) {
	t.Setenv("HWACCEL", "none")
	caps, sel := DetectWithSelection("/nonexistent/ffmpeg")
	if caps.Available() || sel.Available || sel.Requested != "none" {
		t.Errorf("disabled detect = %+v %+v", caps, sel)
	}
}

func TestDetectUnknownHWACCELFallsBackToProbing(t *testing.T) {
	t.Setenv("HWACCEL", "bogus")
	// An unrecognized value must not be treated as a forced backend: probing
	// all backends runs and, with no ffmpeg, ends on the CPU pipeline.
	if c := Detect("/nonexistent/ffmpeg"); c.Available() {
		t.Errorf("unknown HWACCEL: got %q, want cpu-only", c.Backend)
	}
}

func TestProbeBackendMissingBinary(t *testing.T) {
	if _, ok := probeBackend("/nonexistent/ffmpeg", BackendNvenc); ok {
		t.Error("probe must fail when ffmpeg is missing")
	}
}

func TestProbeBackendVaapiNeedsRenderNode(t *testing.T) {
	dev := vaapiDevice()
	if dev != "" && !strings.HasPrefix(dev, "/dev/dri/renderD") {
		t.Errorf("vaapiDevice = %q, want a /dev/dri render node", dev)
	}
	// The vaapi probe requires a render node, so without one it must fail
	// before invoking ffmpeg at all. Skip the assertion on machines that do
	// expose a GPU node, where the synthetic encode decides the outcome.
	if dev == "" {
		if _, ok := probeBackend("ffmpeg", BackendVaapi); ok {
			t.Error("vaapi probe must fail without a render node")
		}
	}
}

func TestStatsNilReceiversAndCounters(t *testing.T) {
	var s Stats
	s.RecordHW()
	s.RecordCPU()
	s.RecordFallback("", 0)
	snap := s.Snapshot()
	if snap.HWEncodes != 1 || snap.CPURenders != 1 || snap.Fallbacks != 1 {
		t.Errorf("stats = %+v", snap)
	}
	if snap.LastFallbackNote != "" {
		t.Errorf("empty note must not overwrite lastFallbackNote: %q", snap.LastFallbackNote)
	}

	var nilStats *Stats
	nilStats.RecordHW()
	nilStats.RecordCPU()
	nilStats.RecordFallback("x", 5)
	if got := nilStats.Snapshot(); got != (StatsSnapshot{}) {
		t.Errorf("nil stats snapshot = %+v, want zero", got)
	}
}
