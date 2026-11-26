package clips

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/hardware"
	"quipster/internal/store"
)

func TestNormalizeSubtitleStyle(t *testing.T) {
	if got, err := NormalizeSubtitleStyle(SubtitleStyle{Enabled: false}); err != nil || got.Enabled {
		t.Fatalf("disabled style = %+v, err=%v", got, err)
	}

	got, err := NormalizeSubtitleStyle(SubtitleStyle{
		Enabled:      true,
		Font:         "Montserrat",
		Color:        "#FFD166",
		Outline:      2.46,
		OutlineColor: "112233",
		Shadow:       1.44,
		BgOpacity:    0.556,
		BgColor:      "#000000",
		Position:     "TOP",
		MarginV:      40,
		Bold:         true,
		Italic:       true,
		Uppercase:    true,
	})
	if err != nil {
		t.Fatalf("NormalizeSubtitleStyle valid: %v", err)
	}
	if got.Font != "montserrat" || got.FontSize != 40 || got.Color != "ffd166" ||
		got.Outline != 2.5 || got.Shadow != 1.4 || got.BgOpacity != 0.56 || got.Position != "top" {
		t.Errorf("unexpected normalized style: %+v", got)
	}

	bad := []SubtitleStyle{
		{Enabled: true, Font: "comic-sans"},
		{Enabled: true, FontSize: 10},
		{Enabled: true, FontSize: 120},
		{Enabled: true, Color: "zzz"},
		{Enabled: true, OutlineColor: "12345"},
		{Enabled: true, BgColor: "12345g"},
		{Enabled: true, Outline: -1},
		{Enabled: true, Outline: 9},
		{Enabled: true, Shadow: -0.5},
		{Enabled: true, BgOpacity: 1.5},
		{Enabled: true, MarginV: -1},
		{Enabled: true, MarginV: 300},
		{Enabled: true, Position: "left"},
	}
	for i, tc := range bad {
		if _, err := NormalizeSubtitleStyle(tc); err == nil {
			t.Errorf("case %d (%+v): expected validation error", i, tc)
		}
	}
}

func TestSubtitleCacheKey(t *testing.T) {
	r := NewRenderer(Config{})
	baseReq := Request{MediaID: 5, Start: 10, End: 15, Mode: "accurate", PadMs: 250}
	disabledReq := baseReq
	disabledReq.Subtitles = &SubtitleStyle{Enabled: false}
	if r.CacheKey(baseReq) != r.CacheKey(disabledReq) {
		t.Fatal("disabled subtitles must not change cache key")
	}

	st1 := DefaultSubtitleStyle()
	subReq1 := baseReq
	subReq1.Subtitles = &st1
	if r.CacheKey(subReq1) == r.CacheKey(baseReq) {
		t.Fatal("enabled subtitles must change cache key")
	}

	st2 := DefaultSubtitleStyle()
	st2.Font = "INTER"
	st2.Color = "#FFFFFF"
	subReq2 := baseReq
	subReq2.Subtitles = &st2
	if r.CacheKey(subReq1) != r.CacheKey(subReq2) {
		t.Fatal("equivalent subtitle styles must hash identically")
	}

	st3 := DefaultSubtitleStyle()
	st3.FontSize = 52
	subReq3 := baseReq
	subReq3.Subtitles = &st3
	if r.CacheKey(subReq1) == r.CacheKey(subReq3) {
		t.Fatal("different font size must produce a distinct cache key")
	}
}

func TestBuildASS(t *testing.T) {
	cues := []SubtitleCue{
		{Start: 8, End: 9, Text: "before clip"},
		{Start: 9.5, End: 12.25, Text: "Hello {world}\\nline2\nnext"},
		{Start: 13, End: 25, Text: "clamped end"},
		{Start: 15, End: 15, Text: "   "},
	}

	st := DefaultSubtitleStyle()
	st.Font = "oswald"
	st.FontSize = 48
	st.Color = "ffd166"
	st.Bold = true
	st.Italic = true
	st.Uppercase = true
	st.Position = "top"
	st.MarginV = 42

	ass := BuildASS(st, cues, 10, 5, 1920, 800)
	for _, want := range []string{
		"PlayResX: 1728",
		"PlayResY: 720",
		"Style: Default,Oswald,48,&H0066D1FF,",
		",-1,-1,0,0,100,100,0,0,1,2.5,1.5,8,48,48,42,1",
		//nolint:misspell // ASS subtitle format requires the literal "Dialogue:" event keyword.
		`Dialogue: 0,0:00:00.00,0:00:02.25,Default,,0,0,0,,HELLO \{WORLD\}\\NLINE2\NNEXT`,
		//nolint:misspell // ASS subtitle format requires the literal "Dialogue:" event keyword.
		"Dialogue: 0,0:00:03.00,0:00:05.00,Default,,0,0,0,,CLAMPED END",
	} {
		if !strings.Contains(ass, want) {
			t.Errorf("ASS missing %q\nfull ASS:\n%s", want, ass)
		}
	}
	if strings.Contains(ass, "BEFORE CLIP") {
		t.Errorf("out-of-range cue must be skipped:\n%s", ass)
	}

	// Boxed mode uses BorderStyle=3 and semi-transparent OutlineColour.
	boxed := DefaultSubtitleStyle()
	boxed.BgOpacity = 0.5
	boxed.BgColor = "102030"
	boxed.Position = "middle"
	assBox := BuildASS(boxed, cues[1:2], 10, 5, 0, 0)
	if !strings.Contains(assBox, "PlayResX: 1280") {
		t.Errorf("unknown dims should default PlayResX to 1280:\n%s", assBox)
	}
	if !strings.Contains(assBox, "&H80302010,&HFF000000,0,0,0,0,100,100,0,0,3,10.0,0.0,5,") {
		t.Errorf("boxed ASS style mismatch:\n%s", assBox)
	}
}

func TestEscapeFilterPath(t *testing.T) {
	got := escapeFilterPath(`/tmp/a'b:c[1],d;e\f.ass`)
	want := `/tmp/a\'b\:c\[1\]\,d\;e\\f.ass`
	if got != want {
		t.Errorf("escapeFilterPath = %q, want %q", got, want)
	}
}

func TestValidateSubtitles(t *testing.T) {
	st := setupTestStore(t)
	defer closeStore(t, st)
	s := New(st, Config{MaxClipSec: 30})
	id := seedMedia(t, st, "/fake/movie.mp4", 0)

	sub := DefaultSubtitleStyle()
	if _, _, err := s.Validate(Request{MediaID: id, Start: 10, End: 15, Mode: "fast", Subtitles: &sub}); err == nil {
		t.Fatal("expected error for fast mode with burned-in subtitles")
	}
	if _, _, err := s.Validate(Request{MediaID: id, Start: 10, End: 15, Mode: "accurate", Subtitles: &sub}); err != nil {
		t.Fatalf("accurate with subtitles should validate: %v", err)
	}
	if _, _, err := s.Validate(Request{MediaID: id, Start: 10, End: 15, Mode: "gif", Subtitles: &sub}); err != nil {
		t.Fatalf("gif with subtitles should validate: %v", err)
	}
	bad := DefaultSubtitleStyle()
	bad.Font = "nope"
	if _, _, err := s.Validate(Request{MediaID: id, Start: 10, End: 15, Mode: "accurate", Subtitles: &bad}); err == nil {
		t.Fatal("expected error for unknown subtitle font")
	}
}

func seedSubtitledClipFixture(t *testing.T) (*Service, int64, string, string) {
	t.Helper()
	t.Setenv("FFMPEG_BIN", writeMockFFmpeg(t))
	t.Setenv("MOCK_COUNT", filepath.Join(t.TempDir(), "count"))
	argsDir := t.TempDir()
	t.Setenv("MOCK_ARGS_DIR", argsDir)

	st := setupTestStore(t)
	t.Cleanup(func() { closeStore(t, st) })
	cacheDir := t.TempDir()
	s := New(st, Config{CacheDir: cacheDir, MaxClipSec: 30, Hw: hardware.MonitorFor(hardware.Capabilities{})})

	id := seedMedia(t, st, "/fake/movie.mp4", 0)
	trackID, err := st.ReplaceTrack(id, &store.SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatalf("ReplaceTrack: %v", err)
	}
	if rerr := st.ReplaceCues(id, trackID, []store.Cue{
		{Idx: 0, StartSec: 10, EndSec: 12, Text: "Hello there"},
		{Idx: 1, StartSec: 12.5, EndSec: 14.5, Text: "General Kenobi"},
	}); rerr != nil {
		t.Fatalf("ReplaceCues: %v", rerr)
	}
	return s, id, cacheDir, argsDir
}

func TestProduceWithSubtitles(t *testing.T) {
	s, id, cacheDir, argsDir := seedSubtitledClipFixture(t)
	sub := DefaultSubtitleStyle()
	sub.Font = "montserrat"
	sub.Uppercase = true

	req := Request{MediaID: id, Start: 10, End: 15, Mode: "accurate", PadMs: 250, Subtitles: &sub}
	path, ext, err := s.Produce(context.Background(), req)
	if err != nil || ext != "mp4" {
		t.Fatalf("Produce accurate with subtitles: ext=%q err=%v", ext, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("output missing: %v", statErr)
	}

	gifReq := Request{MediaID: id, Start: 10, End: 14, Mode: "gif", PadMs: 250, Subtitles: &sub}
	if _, gext, gerr := s.Produce(context.Background(), gifReq); gerr != nil || gext != "gif" {
		t.Fatalf("Produce gif with subtitles: ext=%q err=%v", gext, gerr)
	}

	call1, err := os.ReadFile(filepath.Join(argsDir, "call-1.txt")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("read call-1: %v", err)
	}
	if !strings.Contains(string(call1), "ass=filename=") || !strings.Contains(string(call1), "fontsdir=") {
		t.Errorf("accurate ffmpeg call missing ass filter:\n%s", string(call1))
	}
	call2, err := os.ReadFile(filepath.Join(argsDir, "call-2.txt")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("read call-2: %v", err)
	}
	if !strings.Contains(string(call2), "ass=filename=") || !strings.Contains(string(call2), "palettegen") {
		t.Errorf("gif ffmpeg call missing ass+palettegen filter:\n%s", string(call2))
	}

	matches, globErr := filepath.Glob(filepath.Join(cacheDir, "clips", "subs-*.ass"))
	if globErr != nil {
		t.Fatalf("glob temp ass files: %v", globErr)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temp ass files: %v", matches)
	}
}

func TestJobHandlerWithSubtitles(t *testing.T) {
	s, id, _, _ := seedSubtitledClipFixture(t)
	sub := DefaultSubtitleStyle()
	sub.Font = "oswald"
	sub.Bold = true
	req := Request{MediaID: id, Start: 10, End: 15, Mode: "accurate", PadMs: 250, Subtitles: &sub}

	payload := AsyncJobPayload(req)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := decoded["subtitles"]; !ok {
		t.Fatalf("AsyncJobPayload missing subtitles: %s", payload)
	}
	handler := s.JobHandler()
	if err := handler(context.Background(), &store.Job{Payload: payload}, func(float64) {}); err != nil {
		t.Fatalf("JobHandler with subtitles: %v", err)
	}
}

func TestRealFFmpegSubtitleBurnIn(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	t.Setenv("FFMPEG_BIN", ffmpegBin)

	tmp := t.TempDir()
	src := filepath.Join(tmp, "sample.mp4")
	// Synthesize a 1-second 320x180 test clip so libass + fontconfig + embedded
	// Google Fonts are verified end-to-end against a real FFmpeg binary.
	cmd := exec.Command(ffmpegBin, "-v", "error", "-y", //nolint:gosec // invokes verified ffmpeg on PATH with static test args
		"-f", "lavfi", "-i", "testsrc2=s=320x180:d=1:r=12",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src)
	if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
		t.Fatalf("generate test clip: %v (%s)", cmdErr, string(out))
	}

	renderer := NewRenderer(Config{
		CacheDir:    filepath.Join(tmp, "cache"),
		AccurateRes: "320:-2",
		GifRes:      "240:-2",
		GifFPS:      10,
		CPUPreset:   "ultrafast",
		Hw:          hardware.MonitorFor(hardware.Capabilities{}),
	})
	sub := DefaultSubtitleStyle()
	sub.Font = "montserrat"
	sub.Bold = true
	sub.Uppercase = true
	cues := []SubtitleCue{{Start: 0.1, End: 0.9, Text: "Hello Quipster!"}}

	mp4Path, ext, err := renderer.RenderLocalWithSubtitles(context.Background(), src, -1, 320, 180,
		Request{MediaID: 1, Start: 0, End: 1, Mode: "accurate", PadMs: 0, Subtitles: &sub}, cues, nil)
	if err != nil || ext != "mp4" {
		t.Fatalf("real ffmpeg mp4 burn-in: ext=%q err=%v", ext, err)
	}
	if st, statErr := os.Stat(mp4Path); statErr != nil || st.Size() < 500 {
		t.Fatalf("mp4 output invalid: st=%v err=%v", st, statErr)
	}

	sub.BgOpacity = 0.65
	sub.Font = "jetbrains-mono"
	gifPath, gext, err := renderer.RenderLocalWithSubtitles(context.Background(), src, -1, 320, 180,
		Request{MediaID: 1, Start: 0, End: 1, Mode: "gif", PadMs: 0, Subtitles: &sub}, cues, nil)
	if err != nil || gext != "gif" {
		t.Fatalf("real ffmpeg gif burn-in: ext=%q err=%v", gext, err)
	}
	if st, statErr := os.Stat(gifPath); statErr != nil || st.Size() < 500 {
		t.Fatalf("gif output invalid: st=%v err=%v", st, statErr)
	}
}
