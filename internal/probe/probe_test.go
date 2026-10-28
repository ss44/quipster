package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPickEnglishAudio(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "eac3", Lang: "en", Default: true},
		{Ordinal: 1, Codec: "ac3", Lang: "fr"},
		{Ordinal: 2, Codec: "aac", Lang: "en", Title: "Commentary"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected English track 0, got %+v", got)
	}
}

func TestPickEnglishAudioSkipsCommentary(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "eac3", Lang: "en", Title: "Director Commentary"},
		{Ordinal: 1, Codec: "ac3", Lang: "eng"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected non-commentary English track 1, got %+v", got)
	}
}

func TestPickEnglishAudioPrefersDefaultEnglish(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "en"},
		{Ordinal: 1, Codec: "eac3", Lang: "en", Default: true},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected default English track 1, got %+v", got)
	}
}

func TestPickEnglishAudioFallsBackToDefaultTrack(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "ja"},
		{Ordinal: 1, Codec: "eac3", Lang: "fr", Default: true},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected default fallback track 1, got %+v", got)
	}
}

func TestPickEnglishAudioFirstTrackWhenUntagged(t *testing.T) {
	tracks := []AudioStream{{Ordinal: 0, Codec: "aac", Lang: ""}}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected sole untagged track 0, got %+v", got)
	}
}

func TestPickEnglishAudioEmpty(t *testing.T) {
	if got := PickEnglishAudio(nil); got != nil {
		t.Fatalf("expected nil for empty tracks, got %+v", got)
	}
}

// flagValue returns the value following flag in an argv-style list.
func flagValue(args []string, flag string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func TestProbeArgs(t *testing.T) {
	args := probeArgs("/media/movie.mkv")
	for _, flag := range []string{"-analyzeduration", "-probesize"} {
		v, ok := flagValue(args, flag)
		if !ok {
			t.Fatalf("missing %s in %v", flag, args)
		}
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			t.Fatalf("expected positive integer for %s, got %q", flag, v)
		}
	}
	if args[len(args)-1] != "/media/movie.mkv" {
		t.Fatalf("expected path as final arg, got %v", args)
	}
}

func writeFakeFfprobe(t *testing.T, body string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ffprobe.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // mock binary must stay executable
		t.Fatalf("write fake ffprobe: %v", err)
	}
	t.Setenv("FFPROBE_BIN", bin)
}

func TestProbeTimeout(t *testing.T) {
	writeFakeFfprobe(t, "while :; do :; done")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Probe(ctx, "/media/huge.mkv")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("probe did not stop at the deadline, took %s", elapsed)
	}
}

func TestOptimizationFlags(t *testing.T) {
	result := &Result{
		VideoCodec: "hevc",
		AudioCodec: "ac3",
		metadata: optimizationMetadata{
			videoBitRate: highVideoBitRate,
			avgFrameRate: 30,
			frameRate:    24,
			maxBFrames:   maxLowLatencyBFrames + 1,
		},
	}
	got := optimizationFlags(result)
	want := []string{
		"video_codec_compatibility",
		"audio_codec_compatibility",
		"high_video_bitrate",
		"suspected_variable_frame_rate",
		"deep_b_frame_reordering",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("optimizationFlags = %v, want %v", got, want)
	}
	below := &Result{
		VideoCodec: "h264",
		AudioCodec: "aac",
		metadata: optimizationMetadata{
			avgFrameRate: 24,
			frameRate:    25,
			maxBFrames:   4,
			videoBitRate: highVideoBitRate - 1,
		},
	}
	if got := optimizationFlags(below); len(got) != 0 {
		t.Fatalf("below-threshold result produced flags: %v", got)
	}
	audioOnly := &Result{AudioCodec: "flac", metadata: optimizationMetadata{formatBitRate: highVideoBitRate}}
	if got := optimizationFlags(audioOnly); !reflect.DeepEqual(got, []string{"audio_codec_compatibility"}) {
		t.Fatalf("audio-only result flags = %v", got)
	}
	if got := optimizationFlags(nil); got != nil {
		t.Fatalf("nil result flags = %v, want nil", got)
	}
}

func TestProbeParsesOptimizationMetadata(t *testing.T) {
	writeFakeFfprobe(t, `cat <<'EOF'
{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"bit_rate":"45000000","avg_frame_rate":"30000/1001","r_frame_rate":"30/1","has_b_frames":5}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"46000000"}}
EOF`)
	res, err := Probe(context.Background(), "/media/movie.mp4")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.metadata.videoBitRate != 45_000_000 || res.metadata.formatBitRate != 46_000_000 || res.metadata.avgFrameRate < 29.9 || res.metadata.frameRate != 30 || res.metadata.maxBFrames != 5 {
		t.Fatalf("unexpected optimization metrics: %+v", res)
	}
}

func TestProbeParsesStreams(t *testing.T) {
	writeFakeFfprobe(t, `cat <<'EOF'
{
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "bit_rate": "45000000", "avg_frame_rate": "30000/1001", "r_frame_rate": "30/1", "has_b_frames": 5},
    {"index": 2, "codec_type": "audio", "codec_name": "aac", "channels": 2,
     "disposition": {"default": 1}, "tags": {"language": "eng"}},
    {"index": 3, "codec_type": "subtitle", "codec_name": "subrip", "tags": {"language": "eng"}}
  ],
  "format": {"format_name": "matroska,webm", "duration": "120.5", "start_time": "1.25", "bit_rate": "46000000"}
}
EOF`)
	res, err := Probe(context.Background(), "/media/movie.mkv")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Duration != 120.5 || res.StartTime != 1.25 {
		t.Fatalf("unexpected times: %+v", res)
	}
	if res.VideoCodec != "h264" || res.Width != 1920 || res.Height != 1080 {
		t.Fatalf("unexpected video: %+v", res)
	}
	if res.AudioCodec != "aac" || len(res.Audio) != 1 || res.Audio[0].Index != 2 || res.Audio[0].Lang != "en" {
		t.Fatalf("unexpected audio: %+v", res.Audio)
	}
	if len(res.Subs) != 1 || res.Subs[0].Codec != "subrip" || res.Subs[0].Lang != "en" {
		t.Fatalf("unexpected subs: %+v", res.Subs)
	}
	if res.Container != "matroska,webm" {
		t.Fatalf("unexpected container: %q", res.Container)
	}
}

func TestProbeSurfacesStderr(t *testing.T) {
	writeFakeFfprobe(t, `echo "moov atom not found" >&2; exit 1`)
	_, err := Probe(context.Background(), "/media/broken.mp4")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "moov atom not found") {
		t.Fatalf("expected stderr in error, got %v", err)
	}
}

func TestPickEnglishAudioAllUnusable(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "en", Title: "Commentary"},
		{Ordinal: 1, Codec: "ac3", Lang: "en", Title: "Audio Description"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected raw first track when every track is commentary/description, got %+v", got)
	}
}
