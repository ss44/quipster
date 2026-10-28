package hardware

import "testing"

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
