package thumbs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/hardware"
	"quipster/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "thumbs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	return st
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg.sh")
	//nolint:gosec // the mock ffmpeg script must be executable in tests.
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

const successScript = `#!/bin/sh
for a in "$@"; do last="$a"; done
printf 'jpegdata' > "$last"
exit 0
`

const noOutputScript = `#!/bin/sh
exit 0
`

const failScript = `#!/bin/sh
echo "ffmpeg exploded" >&2
exit 1
`

const hwFailScript = `#!/bin/sh
case " $* " in
  *" -hwaccel "*) echo "hw unavailable" >&2; exit 1;;
esac
for a in "$@"; do last="$a"; done
printf 'jpegdata' > "$last"
exit 0
`

func addMedia(t *testing.T, st *store.Store, duration float64) int64 {
	t.Helper()
	src := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(src, []byte("media"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	id, _, err := st.UpsertMedia(src, "mp4", 5, 1000, "Video", 2020, 0, 0, "")
	if err != nil {
		t.Fatalf("upsert media: %v", err)
	}
	if duration > 0 {
		if err := st.UpdateMediaProbe(id, duration, 1920, 1080, "h264", "aac", "mp4", 0, 0, ""); err != nil {
			t.Fatalf("update probe: %v", err)
		}
	}
	return id
}

func TestBucket(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-5, 0},
		{0, 0},
		{4.9, 0},
		{5, 5},
		{7.2, 5},
		{10, 10},
		{14.99, 10},
	}
	for _, c := range cases {
		if got := Bucket(c.in); got != c.want {
			t.Errorf("Bucket(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNearestWidth(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{1, 320},
		{320, 320},
		{321, 640},
		{640, 640},
		{1920, 640},
	}
	for _, c := range cases {
		if got := NearestWidth(c.in); got != c.want {
			t.Errorf("NearestWidth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGetRendersThenCaches(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 0)

	first, err := s.Get(context.Background(), id, 7.2, 400)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if first.Kind != "thumb" {
		t.Errorf("kind = %q, want thumb", first.Kind)
	}
	if _, serr := os.Stat(first.Path); serr != nil {
		t.Fatalf("thumbnail file missing: %v", serr)
	}
	entry, err := st.GetCacheEntry(first.CacheKey)
	if err != nil || entry == nil {
		t.Fatalf("cache entry: %v %v", entry, err)
	}
	if entry.Hits != 0 {
		t.Errorf("hits = %d, want 0 before reuse", entry.Hits)
	}

	second, err := s.Get(context.Background(), id, 7.2, 400)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if second.CacheKey != first.CacheKey || second.Path != first.Path {
		t.Errorf("cache hit mismatch: %+v vs %+v", first, second)
	}
	entry, err = st.GetCacheEntry(first.CacheKey)
	if err != nil {
		t.Fatalf("cache entry after reuse: %v", err)
	}
	if entry == nil || entry.Hits != 1 {
		t.Errorf("hits after reuse = %+v, want 1", entry)
	}
}

func TestGetRegeneratesWhenFileMissing(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 0)

	first, err := s.Get(context.Background(), id, 1, 320)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if rerr := os.Remove(first.Path); rerr != nil {
		t.Fatalf("remove thumbnail: %v", rerr)
	}
	second, err := s.Get(context.Background(), id, 1, 320)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if second.CacheKey != first.CacheKey {
		t.Errorf("key changed after regeneration: %s vs %s", first.CacheKey, second.CacheKey)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Errorf("thumbnail not regenerated: %v", err)
	}
}

func TestGetTimeAndWidthBuckets(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 0)

	a, err := s.Get(context.Background(), id, 7.2, 400)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	b, err := s.Get(context.Background(), id, 5.1, 640)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if a.CacheKey != b.CacheKey {
		t.Errorf("expected same bucket key, got %s and %s", a.CacheKey, b.CacheKey)
	}

	c, err := s.Get(context.Background(), id, -3, 320)
	if err != nil {
		t.Fatalf("get c: %v", err)
	}
	d, err := s.Get(context.Background(), id, 0, 320)
	if err != nil {
		t.Fatalf("get d: %v", err)
	}
	if c.CacheKey != d.CacheKey {
		t.Errorf("negative time should clamp to zero: %s vs %s", c.CacheKey, d.CacheKey)
	}
}

func TestGetClampsToDuration(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 10)

	a, err := s.Get(context.Background(), id, 100, 320)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	b, err := s.Get(context.Background(), id, 5, 320)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if a.CacheKey != b.CacheKey {
		t.Errorf("duration clamp should bucket to 5s: %s vs %s", a.CacheKey, b.CacheKey)
	}
}

func TestGetMediaNotFound(t *testing.T) {
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	if _, err := s.Get(context.Background(), 9999, 0, 320); err == nil {
		t.Fatal("expected error for missing media")
	}
}

func TestGetStoreError(t *testing.T) {
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := s.Get(context.Background(), 1, 0, 320); err == nil {
		t.Fatal("expected error from closed store")
	}
}

func TestGetMkdirError(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}
	s := &Service{St: st, CacheDir: root}
	id := addMedia(t, st, 0)
	if _, err := s.Get(context.Background(), id, 0, 320); err == nil {
		t.Fatal("expected mkdir error")
	}
}

func TestGetRenderError(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, failScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 0)
	_, err := s.Get(context.Background(), id, 0, 320)
	if err == nil || !strings.Contains(err.Error(), "thumbnail failed") {
		t.Fatalf("err = %v, want thumbnail failed", err)
	}
}

func TestGetMissingOutput(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, noOutputScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir()}
	id := addMedia(t, st, 0)
	if _, err := s.Get(context.Background(), id, 0, 320); err == nil {
		t.Fatal("expected stat error when renderer writes nothing")
	}
}

func TestGetWithHardwareSuccess(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, successScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir(), Hw: hardware.MonitorFor(hardware.Capabilities{Backend: hardware.BackendVaapi})}
	id := addMedia(t, st, 0)
	if _, err := s.Get(context.Background(), id, 0, 320); err != nil {
		t.Fatalf("hardware get: %v", err)
	}
}

func TestGetFallsBackFromHardware(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, hwFailScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir(), Hw: hardware.MonitorFor(hardware.Capabilities{Backend: hardware.BackendVaapi})}
	id := addMedia(t, st, 0)
	e, err := s.Get(context.Background(), id, 0, 320)
	if err != nil {
		t.Fatalf("fallback get: %v", err)
	}
	if _, err := os.Stat(e.Path); err != nil {
		t.Errorf("fallback output missing: %v", err)
	}
}

func TestGetHardwareFallbackFailure(t *testing.T) {
	t.Setenv("FFMPEG_BIN", writeScript(t, failScript))
	st := openTestStore(t)
	s := &Service{St: st, CacheDir: t.TempDir(), Hw: hardware.MonitorFor(hardware.Capabilities{Backend: hardware.BackendVaapi})}
	id := addMedia(t, st, 0)
	_, err := s.Get(context.Background(), id, 0, 320)
	if err == nil || !strings.Contains(err.Error(), "thumbnail failed") {
		t.Fatalf("err = %v, want combined thumbnail failure", err)
	}
}
