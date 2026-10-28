package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("TEST_ENV_OR", "foo")

	if got := envOr("TEST_ENV_OR", "bar"); got != "foo" {
		t.Errorf("envOr() = %v, want foo", got)
	}
	if got := envOr("TEST_ENV_OR_MISSING", "bar"); got != "bar" {
		t.Errorf("envOr() = %v, want bar", got)
	}
}

func TestFfprobeBin(t *testing.T) {
	t.Setenv("FFPROBE_BIN", "/custom/ffprobe")
	if got := FfprobeBin(); got != "/custom/ffprobe" {
		t.Errorf("FfprobeBin() = %v, want /custom/ffprobe", got)
	}

	t.Setenv("FFPROBE_BIN", "   ")
	if got := FfprobeBin(); got != "ffprobe" {
		t.Errorf("FfprobeBin() = %v, want ffprobe", got)
	}
}

func TestFfmpegBin(t *testing.T) {
	t.Setenv("FFMPEG_BIN", "/custom/ffmpeg")
	if got := FfmpegBin(); got != "/custom/ffmpeg" {
		t.Errorf("FfmpegBin() = %v, want /custom/ffmpeg", got)
	}

	t.Setenv("FFMPEG_BIN", "")
	if got := FfmpegBin(); got != "ffmpeg" {
		t.Errorf("FfmpegBin() = %v, want ffmpeg", got)
	}
}

func TestNormalizeLang(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{" ENG ", "en"},
		{"en", "en"},
		{"fra", "fra"},
		{"", ""},
		{"Jpn", "jpn"},
	}
	for _, tc := range tests {
		if got := normalizeLang(tc.in); got != tc.want {
			t.Errorf("normalizeLang(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsTextSubtitle(t *testing.T) {
	tests := []struct {
		codec string
		want  bool
	}{
		{"subrip", true},
		{"srt", true},
		{"ass", true},
		{"pgs", false},
		{"vobsub", false},
		{"WEBVTT", true},
		{"unknown", false},
	}
	for _, tc := range tests {
		if got := IsTextSubtitle(tc.codec); got != tc.want {
			t.Errorf("IsTextSubtitle(%q) = %v, want %v", tc.codec, got, tc.want)
		}
	}
}

func TestFileDuration(t *testing.T) {
	tests := []struct {
		name string
		json string
		want float64
	}{
		{
			name: "format duration",
			json: `{"format": {"duration": "12.34"}}`,
			want: 12.34,
		},
		{
			name: "invalid format duration, video stream duration",
			json: `{"format": {"duration": "N/A"}, "streams": [{"codec_type": "video", "duration": "56.78"}]}`,
			want: 56.78,
		},
		{
			name: "no duration",
			json: `{"format": {}, "streams": []}`,
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var o ffprobeOut
			if err := json.Unmarshal([]byte(tc.json), &o); err != nil {
				t.Fatal(err)
			}
			if got := fileDuration(&o); got != tc.want {
				t.Errorf("fileDuration() = %v, want %v", got, tc.want)
			}
		})
	}
}

// writeFFprobeScript writes a mock ffprobe shell script and points
// FFPROBE_BIN at it.
func writeFFprobeScript(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // mock binary must stay executable
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_BIN", p)
}

const probeMockJSON = `{
	"format": {
		"format_name": "matroska",
		"duration": "10.5",
		"start_time": "1.2"
	},
	"streams": [
		{
			"index": 0,
			"codec_type": "video",
			"codec_name": "h264",
			"width": 1920,
			"height": 1080
		},
		{
			"index": 1,
			"codec_type": "audio",
			"codec_name": "aac",
			"channels": 2,
			"tags": {
				"language": "eng",
				"title": "Stereo"
			},
			"disposition": {
				"default": 1
			}
		},
		{
			"index": 2,
			"codec_type": "subtitle",
			"codec_name": "subrip",
			"tags": {
				"language": "eng"
			}
		}
	]
}`

func TestProbeSuccess(t *testing.T) {
	writeFFprobeScript(t, fmt.Sprintf("cat << 'EOF'\n%s\nEOF\n", probeMockJSON))

	res, err := Probe("dummy.mkv")
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	assertProbeSummary(t, res)
}

func assertProbeSummary(t *testing.T, res *Result) {
	t.Helper()
	if res.Container != "matroska" {
		t.Errorf("got container %q, want matroska", res.Container)
	}
	if res.Duration != 10.5 {
		t.Errorf("got duration %v, want 10.5", res.Duration)
	}
	if res.StartTime != 1.2 {
		t.Errorf("got start time %v, want 1.2", res.StartTime)
	}
	if res.VideoCodec != "h264" || res.Width != 1920 || res.Height != 1080 {
		t.Errorf("got video %q %dx%d, want h264 1920x1080", res.VideoCodec, res.Width, res.Height)
	}
	assertAudioSummary(t, res)
	assertSubsSummary(t, res)
}

func assertAudioSummary(t *testing.T, res *Result) {
	t.Helper()
	if len(res.Audio) != 1 || res.Audio[0].Codec != "aac" || res.Audio[0].Lang != "en" {
		t.Errorf("got audio %+v, want aac en", res.Audio)
	}
	if !res.Audio[0].Default || res.Audio[0].Channels != 2 {
		t.Errorf("got audio %+v, want default 2ch", res.Audio[0])
	}
	if res.AudioCodec != "aac" {
		t.Errorf("got AudioCodec %q, want aac", res.AudioCodec)
	}
}

func assertSubsSummary(t *testing.T, res *Result) {
	t.Helper()
	if len(res.Subs) != 1 || res.Subs[0].Codec != "subrip" || res.Subs[0].Lang != "en" {
		t.Errorf("got subs %+v, want subrip en", res.Subs)
	}
	if res.Subs[0].Ordinal != 0 || res.Subs[0].Index != 2 {
		t.Errorf("got sub ordinals %+v, want index 2 ordinal 0", res.Subs[0])
	}
}

func TestProbeExecError(t *testing.T) {
	writeFFprobeScript(t, "exit 1\n")

	if _, err := Probe("dummy.mkv"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestProbeInvalidJSON(t *testing.T) {
	writeFFprobeScript(t, "echo '{invalid'\n")

	if _, err := Probe("dummy.mkv"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestExtractSubtitle(t *testing.T) {
	ffmpegScript := filepath.Join(t.TempDir(), "ffmpeg")

	t.Run("success", func(t *testing.T) {
		if err := os.WriteFile(ffmpegScript, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // mock binary must stay executable
			t.Fatal(err)
		}
		t.Setenv("FFMPEG_BIN", ffmpegScript)
		if err := ExtractSubtitle(context.Background(), "dummy.mkv", 2, "out.srt"); err != nil {
			t.Fatalf("ExtractSubtitle failed: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		if err := os.WriteFile(ffmpegScript, []byte("#!/bin/sh\necho 'error extracting' >&2\nexit 1\n"), 0o755); err != nil { //nolint:gosec // mock binary must stay executable
			t.Fatal(err)
		}
		t.Setenv("FFMPEG_BIN", ffmpegScript)
		if err := ExtractSubtitle(context.Background(), "dummy.mkv", 2, "out.srt"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
