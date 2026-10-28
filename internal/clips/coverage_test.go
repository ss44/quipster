package clips

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/store"
)

// mockEnv installs the committed variant mock as FFMPEG_BIN and sets the
// behavior switches. t.Setenv handles cleanup and forbids parallel tests.
func mockEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Setenv("FFMPEG_BIN", filepath.Join(cwd, "mock_ffmpeg_variants.sh"))
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func mockFailAlways(t *testing.T)   { mockEnv(t, map[string]string{"MOCK_FAIL_ALWAYS": "1"}) }
func mockFailMP4(t *testing.T)      { mockEnv(t, map[string]string{"MOCK_FAIL_MP4": "1"}) }
func mockCreateOutput(t *testing.T) { mockEnv(t, nil) }
func mockFailFirstThenCreate(t *testing.T) {
	mockEnv(t, map[string]string{"MOCK_FAIL_FIRST": "1", "MOCK_COUNTER_FILE": filepath.Join(t.TempDir(), "count")})
}
func mockSlowCreate(t *testing.T) {
	mockEnv(t, map[string]string{"MOCK_SLEEP": "0.5"})
}

// forceHW pins the vaapi probe result so tests are deterministic regardless
// of whether /dev/dri exists on the host.
func forceHW(s *Service, avail bool) {
	s.mu.Lock()
	s.hwprobed = true
	s.hwavail = avail
	s.mu.Unlock()
}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := setupTestStore(t)
	return New(st, Config{CacheDir: t.TempDir(), MaxClipSec: 30}), st
}

// seedMedia inserts a media row (extension-free path so output-arg matching in
// the mock is unambiguous) and fills in its probe data.
func seedMedia(t *testing.T, st *store.Store, path string, audioStream int) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia(path, "mkv", 1024, 0, "Fake Movie", 2026, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, st.UpdateMediaProbe(id, 600, 1920, 1080, "h264", "aac", "mkv", 0, audioStream, "eng"))
	return id
}

func TestAudioMapArgs(t *testing.T) {
	assert.Nil(t, audioMapArgs(-1))
	assert.Equal(t, []string{"-map", "0:a:2?"}, audioMapArgs(2))
}

// TestValidateSkipsRangeCheckWithoutDuration covers the Validate branch that
// skips start/end range checks when the media has no probed duration.
func TestValidateSkipsRangeCheckWithoutDuration(t *testing.T) {
	s, st := newTestService(t)
	id, _, err := st.UpsertMedia("/fake/noprobe.mkv", "mkv", 1024, 0, "NoProbe", 0, 0, 0, "")
	require.NoError(t, err)
	_, span, err := s.Validate(Request{MediaID: id, Start: -5, End: 20, Mode: "fast", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, 25.5, span)
}

// TestProduceFastMKVFallback covers the cutFast matroska fallback: the mock
// fails the mp4 remux but succeeds for mkv.
func TestProduceFastMKVFallback(t *testing.T) {
	mockFailMP4(t)
	s, st := newTestService(t)
	id := seedMedia(t, st, "/fake/source", 0)
	path, ext, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, "mkv", ext)
	require.FileExists(t, path)
}

// TestProduceFastBothFail covers the cutFast error path when every attempt fails.
func TestProduceFastBothFail(t *testing.T) {
	mockFailAlways(t)
	s, st := newTestService(t)
	forceHW(s, false)
	id := seedMedia(t, st, "/fake/source", 0)
	_, _, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fast cut failed")
}

// TestProducePreviewBothFail covers the cutPreview final error branch.
func TestProducePreviewBothFail(t *testing.T) {
	mockFailAlways(t)
	s, st := newTestService(t)
	forceHW(s, true)
	id := seedMedia(t, st, "/fake/source", 0)
	_, _, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preview cut failed")
}

// TestProduceAccurateBothFail covers the cutAccurate final error branch.
func TestProduceAccurateBothFail(t *testing.T) {
	mockFailAlways(t)
	s, st := newTestService(t)
	forceHW(s, true)
	id := seedMedia(t, st, "/fake/source", 0)
	_, _, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accurate cut failed")
}

// TestProduceRenderValidateError covers the render early-exit on invalid requests.
func TestProduceRenderValidateError(t *testing.T) {
	s, _ := newTestService(t)
	_, _, err := s.Produce(context.Background(), Request{MediaID: 1, Start: 0, End: 1, Mode: "bogus"})
	require.Error(t, err)
}

// TestProduceFlightDedup covers the Produce flight table branches: a waiter
// joining a finished flight, and a canceled context while one is in flight.
func TestProduceFlightDedup(t *testing.T) {
	s, _ := newTestService(t)

	key := CacheKey(7, 10, 20, "fast", 0)
	fake := &flight{done: make(chan struct{}), path: "/fake/done.mp4", ext: "mp4"}
	close(fake.done)
	s.sf[key] = fake
	path, ext, err := s.Produce(context.Background(), Request{MediaID: 7, Start: 10, End: 20, Mode: "fast"})
	require.NoError(t, err)
	assert.Equal(t, "/fake/done.mp4", path)
	assert.Equal(t, "mp4", ext)

	key2 := CacheKey(8, 10, 20, "fast", 0)
	s.sf[key2] = &flight{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.Produce(ctx, Request{MediaID: 8, Start: 10, End: 20, Mode: "fast"})
	require.ErrorIs(t, err, context.Canceled)
}

// TestProduceConcurrentDedup verifies two concurrent calls with the same key
// share one render flight.
func TestProduceConcurrentDedup(t *testing.T) {
	mockSlowCreate(t)
	s, st := newTestService(t)
	forceHW(s, false)
	id := seedMedia(t, st, "/fake/source", 0)
	req := Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250}

	var wg sync.WaitGroup
	var secondPath string
	var secondErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		secondPath, _, secondErr = s.Produce(context.Background(), req)
	}()
	time.Sleep(100 * time.Millisecond) // let the first flight start
	firstPath, _, firstErr := s.Produce(context.Background(), req)
	wg.Wait()
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, firstPath, secondPath)
}

// TestRunWithProgressFastDelegates covers the non-accurate delegation branch.
func TestRunWithProgressFastDelegates(t *testing.T) {
	mockCreateOutput(t)
	s, st := newTestService(t)
	id := seedMedia(t, st, "/fake/source", 0)
	path, ext, err := s.RunWithProgress(context.Background(),
		Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250}, nil)
	require.NoError(t, err)
	assert.Equal(t, "mp4", ext)
	require.FileExists(t, path)
}

// TestRunWithProgressCached covers the cached-result branch: the failing mock
// proves ffmpeg never runs when the cache entry is valid.
func TestRunWithProgressCached(t *testing.T) {
	mockFailAlways(t)
	s, st := newTestService(t)
	forceHW(s, false)
	id := seedMedia(t, st, "/fake/source", 0)
	realPath := filepath.Join(t.TempDir(), "cached.mp4")
	require.NoError(t, os.WriteFile(realPath, []byte("x"), 0o600))
	key := CacheKey(id, 10, 20, "accurate", 250)
	require.NoError(t, st.UpsertCacheEntry(key, realPath, "clip", 1))

	path, ext, err := s.RunWithProgress(context.Background(),
		Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}, func(float64) {})
	require.NoError(t, err)
	assert.Equal(t, "mp4", ext)
	assert.Equal(t, realPath, path)
}

// TestRunWithProgressVariants covers the vaapi success run, the vaapi→cpu
// retry failure, the plain cpu failure and the binary-start failure.
func TestRunWithProgressVariants(t *testing.T) {
	req := func(id int64) Request {
		return Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}
	}
	t.Run("hw success", func(t *testing.T) {
		mockCreateOutput(t)
		s, st := newTestService(t)
		forceHW(s, true)
		id := seedMedia(t, st, "/fake/source", 0)
		path, ext, err := s.RunWithProgress(context.Background(), req(id), func(float64) {})
		require.NoError(t, err)
		assert.Equal(t, "mp4", ext)
		require.FileExists(t, path)
	})
	t.Run("hw retry exhausted", func(t *testing.T) {
		mockFailAlways(t)
		s, st := newTestService(t)
		forceHW(s, true)
		id := seedMedia(t, st, "/fake/source", 0)
		_, _, err := s.RunWithProgress(context.Background(), req(id), func(float64) {})
		require.Error(t, err)
	})
	t.Run("cpu fail", func(t *testing.T) {
		mockFailAlways(t)
		s, st := newTestService(t)
		forceHW(s, false)
		id := seedMedia(t, st, "/fake/source", 0)
		_, _, err := s.RunWithProgress(context.Background(), req(id), func(float64) {})
		require.Error(t, err)
	})
	t.Run("start error", func(t *testing.T) {
		t.Setenv("FFMPEG_BIN", "/nonexistent/ffmpeg-missing")
		s, st := newTestService(t)
		forceHW(s, false)
		id := seedMedia(t, st, "/fake/source", 0)
		_, _, err := s.RunWithProgress(context.Background(), req(id), func(float64) {})
		require.Error(t, err)
	})
}

// TestProduceHWPreviewAndAccurateFallback covers the vaapi-failure fallback of
// cutPreview and cutAccurate: the first attempt fails, the CPU retry succeeds.
// Each mode gets a fresh failure counter so both hardware attempts fail.
func TestProduceHWPreviewAndAccurateFallback(t *testing.T) {
	s, st := newTestService(t)
	forceHW(s, true)
	id := seedMedia(t, st, "/fake/source", 0)

	mockFailFirstThenCreate(t)
	path, ext, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, "mp4", ext)
	require.FileExists(t, path)

	mockFailFirstThenCreate(t)
	path, ext, err = s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, "mp4", ext)
	require.FileExists(t, path)
}

// TestProduceCPUTranscodeFailure covers the cpu-only failure path of
// cutPreview and cutAccurate, where no hardware attempt is made at all.
func TestProduceCPUTranscodeFailure(t *testing.T) {
	mockFailAlways(t)
	s, st := newTestService(t)
	forceHW(s, false)
	id := seedMedia(t, st, "/fake/source", 0)

	_, _, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preview cut failed")

	_, _, err = s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accurate cut failed")
}

// TestProduceHWPreviewSuccess covers the vaapi success path of cutPreview and
// the audio-map with a negative ordinal.
func TestProduceHWPreviewSuccess(t *testing.T) {
	mockCreateOutput(t)
	s, st := newTestService(t)
	forceHW(s, true)
	id := seedMedia(t, st, "/fake/source", -1) // no audio mapped
	path, ext, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "preview", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, "mp4", ext)
	require.FileExists(t, path)
}

// TestProduceWithClosedStore covers the Cached error branches of Produce and
// RunWithProgress.
func TestProduceWithClosedStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	require.NoError(t, err)
	require.NoError(t, st.Close())
	s := New(st, Config{CacheDir: t.TempDir(), MaxClipSec: 30})
	req := Request{MediaID: 1, Start: 10, End: 20, Mode: "fast", PadMs: 250}

	_, _, err = s.Produce(context.Background(), req)
	require.Error(t, err)

	_, _, err = s.RunWithProgress(context.Background(),
		Request{MediaID: 1, Start: 10, End: 20, Mode: "accurate", PadMs: 250}, func(float64) {})
	require.Error(t, err)
}

// TestCutFastCleanupError covers the removePartial branch that logs a failed
// cleanup: a non-empty directory at the mp4 output path makes os.Remove fail
// after the successful matroska fallback.
func TestCutFastCleanupError(t *testing.T) {
	mockFailMP4(t)
	s, st := newTestService(t)
	id := seedMedia(t, st, "/fake/source", 0)
	key := CacheKey(id, 10, 20, "fast", 250)

	// Plant a non-empty directory where the mp4 output would be written.
	junk := filepath.Join(s.cfg.CacheDir, "clips", key+".mp4", "occupied")
	require.NoError(t, os.MkdirAll(junk, 0o750))

	path, ext, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250})
	require.NoError(t, err)
	assert.Equal(t, "mkv", ext)
	require.FileExists(t, path)
}

// TestProduceMkdirError covers the clip cache directory creation failure in
// render and RunWithProgress: pointing CacheDir at a regular file makes
// MkdirAll fail with ENOTDIR.
func TestProduceMkdirError(t *testing.T) {
	mockCreateOutput(t)
	_, st := newTestService(t)
	id := seedMedia(t, st, "/fake/source", 0)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	s := New(st, Config{CacheDir: blocker, MaxClipSec: 30})

	_, _, err := s.Produce(context.Background(), Request{MediaID: id, Start: 10, End: 20, Mode: "fast", PadMs: 250})
	require.Error(t, err)

	_, _, err = s.RunWithProgress(context.Background(),
		Request{MediaID: id, Start: 10, End: 20, Mode: "accurate", PadMs: 250}, func(float64) {})
	require.Error(t, err)
}

// TestJobHandler covers the async job rendering path: a valid accurate payload
// with progress reporting, the fast delegation, field defaults and malformed
// payloads.
func TestJobHandler(t *testing.T) {
	mockCreateOutput(t)
	s, st := newTestService(t)
	forceHW(s, false)
	id := seedMedia(t, st, "/fake/source", 0)
	handler := s.JobHandler()
	validMedia := `{"mediaId":` + strconv.FormatInt(id, 10) + ``

	var reported []float64
	err := handler(context.Background(),
		&store.Job{Payload: validMedia + `,"start":10,"end":20,"padMs":100,"mode":"accurate"}`},
		func(p float64) { reported = append(reported, p) })
	require.NoError(t, err)
	require.Greater(t, len(reported), 1)

	// Explicit fast payload delegates to the sync produce path.
	err = handler(context.Background(),
		&store.Job{Payload: validMedia + `,"start":10,"end":20,"padMs":0,"mode":"fast"}`},
		func(float64) {})
	require.NoError(t, err)

	// Minimal payload: mode and padMs default, mediaId 0 is missing →
	// validation fails with "media 0 not found".
	err = handler(context.Background(), &store.Job{Payload: `{"start":0,"end":5}`}, func(float64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media 0 not found")

	// Malformed payload is rejected before any ffmpeg work.
	err = handler(context.Background(), &store.Job{Payload: `{invalid`}, func(float64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad payload")
}
