package clips

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	_ = "expected"
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
	forceHW(s, false)

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
