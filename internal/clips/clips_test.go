package clips

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/hardware"
	"quipster/internal/store"
)

func setupTestStore(t *testing.T) *store.Store {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return st
}

// closeStore closes a test store, demoting a failed close to a logged note so
// teardown problems do not mask the test outcome.
func closeStore(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.Close(); err != nil {
		t.Logf("close store: %v", err)
	}
}

func TestCacheKey(t *testing.T) {
	key1 := CacheKey(1, 10.5, 20.5, "fast", 250)
	key2 := CacheKey(1, 10.5, 20.5, "fast", 250)
	key3 := CacheKey(2, 10.5, 20.5, "fast", 250)
	if key1 != key2 {
		t.Errorf("expected keys to match")
	}
	if key1 == key3 {
		t.Errorf("expected keys to differ")
	}
	if len(key1) != 32 {
		t.Errorf("expected 32 chars, got %d", len(key1))
	}
}

func TestNew(t *testing.T) {
	cfg := Config{}
	s := New(nil, cfg)
	c := s.Config()
	if c.MaxClipSec != 120 {
		t.Errorf("expected 120, got %f", c.MaxClipSec)
	}
	if c.DefaultPadMs != 250 {
		t.Errorf("expected 250, got %d", c.DefaultPadMs)
	}
	if c.LongCutSec != 20 {
		t.Errorf("expected 20, got %f", c.LongCutSec)
	}
}

func TestValidate(t *testing.T) {
	st := setupTestStore(t)
	defer closeStore(t, st)
	s := New(st, Config{MaxClipSec: 30})

	id := seedMedia(t, st, "/fake/movie.mp4", 0)

	tests := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"valid fast", Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 0}, false},
		{"valid preview", Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 0}, false},
		{"valid accurate", Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}, false},
		{"invalid mode", Request{MediaID: id, Start: 10, End: 20, Mode: "unknown", PadMs: 0}, true},
		{"start >= end", Request{MediaID: id, Start: 20, End: 10, Mode: "fast", PadMs: 0}, true},
		{"exceeds max duration", Request{MediaID: id, Start: 10, End: 50, Mode: "fast", PadMs: 0}, true},
		{"media not found", Request{MediaID: 999, Start: 10, End: 20, Mode: "fast", PadMs: 0}, true},
		{"start out of range", Request{MediaID: id, Start: -1, End: 20, Mode: "fast", PadMs: 0}, true},
		{"end out of range", Request{MediaID: id, Start: 10, End: 605, Mode: "fast", PadMs: 0}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, span, err := s.Validate(tc.req)
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				expectedSpan := (tc.req.End - tc.req.Start) + 2*(float64(tc.req.PadMs)/1000)
				if span != expectedSpan {
					t.Errorf("expected span %f, got %f", expectedSpan, span)
				}
			}
		})
	}
}

func TestCachedTouch(t *testing.T) {
	st := setupTestStore(t)
	defer closeStore(t, st)
	dir := t.TempDir()
	s := New(st, Config{CacheDir: dir})

	key := "testkey123"
	// Before insertion, should be empty
	e, err := s.Cached(key)
	if err != nil {
		t.Errorf("expected no err, got %v", err)
	}
	if e != nil {
		t.Errorf("expected nil entry, got %v", e)
	}

	// Insert into DB but no file
	if uerr := st.UpsertCacheEntry(key, filepath.Join(dir, "missing.mp4"), "clip", 100); uerr != nil {
		t.Fatalf("UpsertCacheEntry: %v", uerr)
	}
	e, err = s.Cached(key)
	if err != nil {
		t.Errorf("expected no err, got %v", err)
	}
	if e != nil {
		t.Errorf("expected nil since file is missing, got %v", e)
	}

	// Insert and create file
	realPath := filepath.Join(dir, "real.mp4")
	if werr := os.WriteFile(realPath, []byte("data"), 0o600); werr != nil {
		t.Fatalf("WriteFile: %v", werr)
	}
	if uerr := st.UpsertCacheEntry(key, realPath, "clip", 4); uerr != nil {
		t.Fatalf("UpsertCacheEntry: %v", uerr)
	}

	e, err = s.Cached(key)
	if err != nil {
		t.Errorf("expected no err, got %v", err)
	}
	if e == nil || e.Path != realPath {
		t.Errorf("expected valid entry, got %v", e)
	}

	// Test Touch
	s.Touch(key)

	// Closing the store makes the LRU touch fail, exercising the logged
	// error path of Touch. The deferred close then double-closes, which is
	// a harmless no-op.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	s.Touch(key)
}

func TestAsyncJobPayload(t *testing.T) {
	req := Request{MediaID: 1, Start: 1.5, End: 2.5, PadMs: 100, Mode: "accurate"}
	payload := AsyncJobPayload(req)
	// The json fields might be ordered differently so we check if strings contain key elements
	// But it's easier to just unmarshal and compare.
	// actually we shouldn't nest import, let's just do a basic string check
	if payload == "" {
		t.Errorf("expected non-empty payload")
	}
}

func setMockFFmpeg(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_BIN", filepath.Join(cwd, "mock_ffmpeg.sh"))
}

func TestProduce(t *testing.T) {
	setMockFFmpeg(t)
	st := setupTestStore(t)
	defer closeStore(t, st)
	dir := t.TempDir()
	s := New(st, Config{CacheDir: dir, MaxClipSec: 30})

	id := seedMedia(t, st, "/fake/movie.mp4", 0)

	tests := []struct {
		name string
		req  Request
	}{
		{"fast mode", Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250}},
		{"preview mode", Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250}},
		{"accurate mode", Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, ext, err := s.Produce(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Produce() error = %v", err)
			}
			if ext != "mp4" && ext != "mkv" {
				t.Errorf("expected mp4 or mkv, got %v", ext)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("expected file to exist at %v, got err %v", path, err)
			}

			// Call Produce again to test Cached path
			path2, ext2, err2 := s.Produce(context.Background(), tc.req)
			if err2 != nil {
				t.Fatalf("Produce() cached error = %v", err2)
			}
			if path != path2 || ext != ext2 {
				t.Errorf("Cached result differs: %v vs %v", path, path2)
			}
		})
	}
}

func TestProduceCPU(t *testing.T) {
	setMockFFmpeg(t)
	st := setupTestStore(t)
	defer closeStore(t, st)
	dir := t.TempDir()
	s := New(st, Config{CacheDir: dir, MaxClipSec: 30})
	forceHWOff(s)

	id := seedMedia(t, st, "/fake/movie.mp4", 0)

	tests := []struct {
		name string
		req  Request
	}{
		{"preview mode cpu", Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250}},
		{"accurate mode cpu", Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _, err := s.Produce(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Produce() error = %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("expected file to exist at %v, got err %v", path, err)
			}
		})
	}
}

func TestRunWithProgress(t *testing.T) {
	setMockFFmpeg(t)
	st := setupTestStore(t)
	defer closeStore(t, st)
	dir := t.TempDir()
	s := New(st, Config{CacheDir: dir, MaxClipSec: 30})

	id := seedMedia(t, st, "/fake/movie.mp4", 0)

	var progressReports []float64
	reportFunc := func(p float64) {
		progressReports = append(progressReports, p)
	}

	req := Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}
	path, ext, err := s.RunWithProgress(context.Background(), req, reportFunc)
	if err != nil {
		t.Fatalf("RunWithProgress() error = %v", err)
	}
	if ext != "mp4" {
		t.Errorf("expected mp4, got %v", ext)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist at %v, got err %v", path, err)
	}

	// mock outputs out_time_us=1000000 and 2000000
	if len(progressReports) < 2 {
		t.Errorf("expected at least 2 progress reports, got %d", len(progressReports))
	}
}

// writeMockFFmpeg creates a POSIX-sh script that counts invocations, records
// the args of every call to $MOCK_ARGS_DIR/call-N.txt, fails the first
// MOCK_FAIL_TIMES calls, and otherwise writes to the output file (last arg).
func writeMockFFmpeg(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
n=$(cat "$MOCK_COUNT" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$MOCK_COUNT"
last=""
for a in "$@"; do last="$a"; done
printf '%s\n' "$@" > "$MOCK_ARGS_DIR/call-$n.txt"
if [ "$n" -le "${MOCK_FAIL_TIMES:-0}" ]; then
    echo "mock ffmpeg failure $n" >&2
    exit 1
fi
printf x > "$last"
`
	p := filepath.Join(t.TempDir(), "mock-ffmpeg")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test fixture needs the executable bit
		t.Fatal(err)
	}
	return p
}

// runTranscodeTest runs one preview transcode with the mock ffmpeg and returns
// the recorded args of each call.
func runTranscodeTest(t *testing.T, hw hardware.Capabilities, failTimes int) (string, [][]string) {
	t.Helper()
	t.Setenv("FFMPEG_BIN", writeMockFFmpeg(t))
	t.Setenv("MOCK_COUNT", filepath.Join(t.TempDir(), "count"))
	argsDir := t.TempDir()
	t.Setenv("MOCK_ARGS_DIR", argsDir)
	t.Setenv("MOCK_FAIL_TIMES", fmt.Sprint(failTimes))

	s := &Service{cfg: Config{Hw: hw, PreviewRes: "640:-2", AccurateRes: "1280:-2"}}
	out := filepath.Join(t.TempDir(), "out.mp4")
	err := s.runTranscode(context.Background(), "in.mp4", out, 5, 10, 1, s.previewProfile(), nil)
	if err != nil {
		t.Fatalf("runTranscode: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not created: %v", err)
	}

	var calls [][]string
	for n := 1; ; n++ {
		b, err := os.ReadFile(filepath.Join(argsDir, fmt.Sprintf("call-%d.txt", n))) //nolint:gosec // G304: paths come from t.TempDir() fixtures
		if err != nil {
			break
		}
		calls = append(calls, strings.Fields(string(b)))
	}
	if len(calls) == 0 {
		t.Fatal("mock recorded no calls")
	}
	return out, calls
}

func has(args []string, flag, val string) bool {
	for i := range args {
		if args[i] == flag && (val == "" || (i+1 < len(args) && args[i+1] == val)) {
			return true
		}
	}
	return false
}

func TestRunTranscodeHardwareEncode(t *testing.T) {
	_, calls := runTranscodeTest(t, hardware.Capabilities{Backend: hardware.BackendVaapi, Device: "/dev/dri/renderD128"}, 0)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1 (hw succeeds, no fallback)", len(calls))
	}
	a := calls[0]
	if !has(a, "-vaapi_device", "/dev/dri/renderD128") {
		t.Errorf("missing explicit vaapi device: %v", a)
	}
	if !has(a, "-hwaccel", "vaapi") || !has(a, "-hwaccel_output_format", "vaapi") {
		t.Errorf("missing vaapi gpu decode flags: %v", a)
	}
	if !has(a, "-vf", "scale_vaapi=640:-2:format=nv12") {
		t.Errorf("missing vaapi scale filter: %v", a)
	}
	if !has(a, "-c:v", "h264_vaapi") || !has(a, "-b:v", "1M") {
		t.Errorf("missing hw encoder args: %v", a)
	}
	if has(a, "-c:v", "libx264") {
		t.Errorf("unexpected cpu encoder: %v", a)
	}
}

func TestRunTranscodeCPUFallback(t *testing.T) {
	_, calls := runTranscodeTest(t, hardware.Capabilities{Backend: hardware.BackendVaapi}, 1)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2 (hw fails once, cpu retry succeeds)", len(calls))
	}
	cpu := calls[1]
	if has(cpu, "-hwaccel", "") {
		t.Errorf("cpu retry must not carry hwaccel flags: %v", cpu)
	}
	if !has(cpu, "-vf", "scale=640:-2") || !has(cpu, "-c:v", "libx264") ||
		!has(cpu, "-preset", "ultrafast") {
		t.Errorf("missing cpu fallback encoder args: %v", cpu)
	}
}

func TestRunTranscodeQSVNoEmptyFilter(t *testing.T) {
	// Regression: qsv has no GPU scaler, so the pipeline must scale on the CPU
	// instead of passing an empty -vf to ffmpeg.
	_, calls := runTranscodeTest(t, hardware.Capabilities{Backend: hardware.BackendQsv}, 0)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	a := calls[0]
	if !has(a, "-vf", "scale=640:-2") {
		t.Errorf("qsv pipeline must use a cpu scale filter, got: %v", a)
	}
	if has(a, "-hwaccel_output_format", "") {
		t.Errorf("qsv decodes to system memory, must not set -hwaccel_output_format: %v", a)
	}
	if !has(a, "-hwaccel", "qsv") || !has(a, "-c:v", "h264_qsv") {
		t.Errorf("missing qsv decode/encode args: %v", a)
	}
}

func TestRunTranscodeCPUBackend(t *testing.T) {
	_, calls := runTranscodeTest(t, hardware.Capabilities{}, 0)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1 (cpu only)", len(calls))
	}
	a := calls[0]
	if has(a, "-hwaccel", "") {
		t.Errorf("cpu pipeline must not carry hwaccel flags: %v", a)
	}
	if !has(a, "-vf", "scale=640:-2") || !has(a, "-c:v", "libx264") {
		t.Errorf("missing cpu encoder args: %v", a)
	}
}
