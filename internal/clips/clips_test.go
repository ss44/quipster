package clips

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/hardware"
)

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
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// runTranscodeTest runs one transcode with the mock ffmpeg and returns the
// recorded args of each successful call.
func runTranscodeTest(t *testing.T, hw hardware.Capabilities, failTimes int) (string, [][]string) {
	t.Helper()
	t.Setenv("FFMPEG_BIN", writeMockFFmpeg(t))
	t.Setenv("MOCK_COUNT", filepath.Join(t.TempDir(), "count"))
	argsDir := t.TempDir()
	t.Setenv("MOCK_ARGS_DIR", argsDir)
	t.Setenv("MOCK_FAIL_TIMES", fmt.Sprint(failTimes))

	s := &Service{cfg: Config{Hw: hw}}
	out := filepath.Join(t.TempDir(), "out.mp4")
	err := s.runTranscode(context.Background(), "in.mp4", out, 5, 10, 1, previewProfile, nil)
	if err != nil {
		t.Fatalf("runTranscode: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not created: %v", err)
	}

	var calls [][]string
	for n := 1; ; n++ {
		b, err := os.ReadFile(filepath.Join(argsDir, fmt.Sprintf("call-%d.txt", n)))
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
	_, calls := runTranscodeTest(t, hardware.Capabilities{Backend: hardware.BackendVaapi}, 0)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1 (hw succeeds, no fallback)", len(calls))
	}
	a := calls[0]
	if !has(a, "-hwaccel", "vaapi") || !has(a, "-hwaccel_output_format", "vaapi") {
		t.Errorf("missing vaapi gpu decode flags: %v", a)
	}
	if !has(a, "-vf", "scale_vaapi=w=960:h=-2:format=nv12") {
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
	if !has(cpu, "-vf", "scale=960:-2") || !has(cpu, "-c:v", "libx264") ||
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
	if !has(a, "-vf", "scale=960:-2") {
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
	if !has(a, "-vf", "scale=960:-2") || !has(a, "-c:v", "libx264") {
		t.Errorf("missing cpu encoder args: %v", a)
	}
}
