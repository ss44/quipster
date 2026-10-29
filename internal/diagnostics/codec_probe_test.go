package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeExecutableStub writes a shell script with the given body and marks it
// executable, standing in for a vendor probe binary.
func writeExecutableStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	//nolint:gosec // the stub must be executable so exec can run it as a fake probe binary.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod stub %s: %v", name, err)
	}
	return path
}

// TestToolVersionUnavailable covers the degraded VersionInfo reported when a
// probe binary cannot be executed.
func TestToolVersionUnavailable(t *testing.T) {
	info := toolVersion(context.Background(), "quipster-missing-binary")
	if info.Available || info.Reason == "" {
		t.Fatalf("toolVersion(missing) = %+v, want unavailable with a reason", info)
	}
}

// TestToolVersionReportsFirstLine covers successful invocations whose output
// spans multiple lines: only the first line is the version banner.
func TestToolVersionReportsFirstLine(t *testing.T) {
	bin := writeExecutableStub(t, t.TempDir(), "probe-tool", "#!/bin/sh\necho 'tool 1.2.3'\necho 'usage line'\n")
	info := toolVersion(context.Background(), bin)
	if !info.Available || info.Version != "tool 1.2.3" {
		t.Fatalf("toolVersion(stub) = %+v, want first line of output", info)
	}
}

// TestFFmpegListingWithStub drives ffmpegList and ffmpegCodecs against a stub
// binary, covering both the unavailable-binary degradation and the parsing of
// realistic `-encoders` output (header lines, blank lines, separators and
// uninteresting codec rows).
func TestFFmpegListingWithStub(t *testing.T) {
	ctx := context.Background()

	// A missing binary degrades to empty listings rather than errors.
	t.Setenv("FFMPEG_BIN", filepath.Join(t.TempDir(), "missing-ffmpeg"))
	if got := ffmpegList(ctx, "-filters"); got != nil {
		t.Fatalf("ffmpegList(missing) = %v, want nil", got)
	}
	if got := ffmpegCodecs(ctx, "-encoders"); got != nil {
		t.Fatalf("ffmpegCodecs(missing) = %v, want nil", got)
	}

	script := `#!/bin/sh
echo "ffmpeg stub output:"
echo ""
echo " V....D av1        AV1"
echo " A....D aac        AAC (aac)"
echo " V....D h264       libx264 H.264"
echo " = "
echo " -something"
echo "short"
echo " V -weird"
`
	bin := writeExecutableStub(t, t.TempDir(), "ffmpeg", script)
	t.Setenv("FFMPEG_BIN", bin)

	list := ffmpegList(ctx, "-encoders")
	want := []string{
		"V....D av1        AV1",
		"A....D aac        AAC (aac)",
		"V....D h264       libx264 H.264",
		"=",
		"-something",
		"short",
		"V -weird",
	}
	if len(list) != len(want) {
		t.Fatalf("ffmpegList(stub) = %q, want %q", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("ffmpegList(stub)[%d] = %q, want %q", i, list[i], want[i])
		}
	}

	codecs := ffmpegCodecs(ctx, "-encoders")
	wantCodecs := []string{"av1", "aac", "h264"}
	if len(codecs) != len(wantCodecs) {
		t.Fatalf("ffmpegCodecs(stub) = %q, want %q", codecs, wantCodecs)
	}
	for i := range wantCodecs {
		if codecs[i] != wantCodecs[i] {
			t.Fatalf("ffmpegCodecs(stub)[%d] = %q, want %q", i, codecs[i], wantCodecs[i])
		}
	}
}
