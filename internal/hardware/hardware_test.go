package hardware

import "testing"

func TestPipelinePerBackend(t *testing.T) {
	tests := []struct {
		name    string
		caps    Capabilities
		input   []string
		filter  string
		encoder string
		isHW    bool
	}{
		{
			name:    "none yields cpu plan",
			caps:    Capabilities{Backend: BackendNone},
			filter:  "scale=960:-2",
			encoder: "libx264",
		},
		{
			name:    "zero value yields cpu plan",
			caps:    Capabilities{},
			filter:  "scale=960:-2",
			encoder: "libx264",
		},
		{
			name:    "vaapi full gpu pipeline",
			caps:    Capabilities{Backend: BackendVaapi, Device: "/dev/dri/renderD128"},
			input:   []string{"-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"},
			filter:  "scale_vaapi=w=960:h=-2:format=nv12",
			encoder: "h264_vaapi",
			isHW:    true,
		},
		{
			name:    "nvenc full gpu pipeline",
			caps:    Capabilities{Backend: BackendNvenc},
			input:   []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
			filter:  "scale_cuda=w=960:h=-2:format=nv12",
			encoder: "h264_nvenc",
			isHW:    true,
		},
		{
			name:    "qsv decodes to system memory and scales on cpu",
			caps:    Capabilities{Backend: BackendQsv},
			input:   []string{"-hwaccel", "qsv"},
			filter:  "scale=960:-2",
			encoder: "h264_qsv",
			isHW:    true,
		},
		{
			name:    "videotoolbox decodes to system memory and scales on cpu",
			caps:    Capabilities{Backend: BackendVideotoolbox},
			input:   []string{"-hwaccel", "videotoolbox"},
			filter:  "scale=960:-2",
			encoder: "h264_videotoolbox",
			isHW:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caps.Available(); got != tc.isHW {
				t.Errorf("Available() = %v, want %v", got, tc.isHW)
			}
			p := tc.caps.Pipeline(960)
			assertArgs(t, "Pipeline.Input", p.Input, tc.input)
			if p.Filter != tc.filter {
				t.Errorf("Filter = %q, want %q", p.Filter, tc.filter)
			}
			if p.Encoder != tc.encoder {
				t.Errorf("Encoder = %q, want %q", p.Encoder, tc.encoder)
			}
			if p.IsHW != tc.isHW {
				t.Errorf("IsHW = %v, want %v", p.IsHW, tc.isHW)
			}
			// DecodeArgs always hands frames back to system memory, so
			// CPU filters/encoders can consume them.
			assertArgs(t, "DecodeArgs", tc.caps.DecodeArgs(), firstTwo(tc.input))
		})
	}
}

func firstTwo(args []string) []string {
	if len(args) < 2 {
		return nil
	}
	return args[:2]
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
