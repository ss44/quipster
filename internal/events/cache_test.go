package events

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quipster/internal/store"
)

func waitForLineStats(t *testing.T, c *lineStatsCache) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		refreshing := c.refreshing
		c.mu.Unlock()
		if !refreshing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("line statistics refresh did not finish")
}

func TestLineStatsCacheConcurrentRefreshAndExpiry(t *testing.T) {
	var c lineStatsCache
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (*store.LineStats, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return &store.LineStats{Cues: 7, Chunks: 4, ChunksIndexed: 3}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	now := time.Now()
	if got := c.get(now, load); got != nil {
		t.Fatalf("cold cache = %+v", got)
	}
	<-started
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if got := c.get(now, load); got != nil {
				t.Errorf("in-flight cache = %+v", got)
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("concurrent calls = %d, want 1", n)
	}
	close(release)
	waitForLineStats(t, &c)
	got := c.get(now, load)
	if got == nil || *got != (store.LineStats{Cues: 7, Chunks: 4, ChunksIndexed: 3}) {
		t.Fatalf("cached value = %+v", got)
	}
	got.Cues = -1
	if got := c.get(now, load); got.Cues != 7 {
		t.Fatalf("caller changed shared value: %+v", got)
	}

	testLineStatsRefreshExpiry(t, &c, &calls, now)
}

func testLineStatsRefreshExpiry(t *testing.T, c *lineStatsCache, calls *atomic.Int32, now time.Time) {
	t.Helper()
	nextStarted := make(chan struct{})
	nextRelease := make(chan struct{})
	nextLoad := func(ctx context.Context) (*store.LineStats, error) {
		calls.Add(1)
		close(nextStarted)
		select {
		case <-nextRelease:
			return &store.LineStats{Cues: 10, Chunks: 5, ChunksIndexed: 5}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	refreshAt := now.Add(lineStatsRefreshInterval + time.Second)
	if got := c.get(refreshAt, nextLoad); got == nil || got.Cues != 7 {
		t.Fatalf("during refresh = %+v", got)
	}
	<-nextStarted
	if got := c.get(now.Add(lineStatsMaxAge), nextLoad); got != nil {
		t.Fatalf("expired value = %+v", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("overlapping refresh calls = %d, want 2", n)
	}
	close(nextRelease)
	waitForLineStats(t, c)
	if got := c.get(refreshAt, nextLoad); got == nil || *got != (store.LineStats{Cues: 10, Chunks: 5, ChunksIndexed: 5}) {
		t.Fatalf("refreshed value = %+v", got)
	}
}

func TestLineStatsCacheFailureAndRecovery(t *testing.T) {
	var c lineStatsCache
	var calls atomic.Int32
	now := time.Now()
	ok := func(context.Context) (*store.LineStats, error) {
		calls.Add(1)
		return &store.LineStats{Cues: 42}, nil
	}
	c.get(now, ok)
	waitForLineStats(t, &c)
	failed := func(context.Context) (*store.LineStats, error) {
		calls.Add(1)
		return nil, errors.New("unavailable")
	}
	failureAt := now.Add(lineStatsRefreshInterval + time.Second)
	c.get(failureAt, failed)
	waitForLineStats(t, &c)
	if got := c.get(failureAt.Add(time.Second), failed); got == nil || got.Cues != 42 {
		t.Fatalf("fresh value after failure = %+v", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("failure was not throttled: %d calls", n)
	}
	expiredAt := now.Add(lineStatsMaxAge)
	if got := c.get(expiredAt, ok); got != nil {
		t.Fatalf("expired value after failure = %+v", got)
	}
	waitForLineStats(t, &c)
	if got := c.get(expiredAt, ok); got == nil || got.Cues != 42 {
		t.Fatalf("recovered value = %+v", got)
	}
}

func TestLineStatsContextCountsAndCancellation(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	id, _, err := st.UpsertMedia("/media/a.mkv", "matroska", 100, 1, "A", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	track, err := st.ReplaceTrack(id, &store.SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if cueErr := st.ReplaceCues(id, track, []store.Cue{{Idx: 0, Text: "one"}, {Idx: 1, Text: "two"}}); cueErr != nil {
		t.Fatal(cueErr)
	}
	if chunkErr := st.ReplaceChunks(id, []store.Chunk{{StartCueIdx: 0, EndCueIdx: 0, Text: "one"}, {StartCueIdx: 1, EndCueIdx: 1, Text: "two"}}); chunkErr != nil {
		t.Fatal(chunkErr)
	}
	chunks, err := st.ChunksForMedia(id)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks = %+v, error = %v", chunks, err)
	}
	if indexErr := st.MarkChunksIndexed([]int64{chunks[0].ID}, 1); indexErr != nil {
		t.Fatal(indexErr)
	}
	got, err := st.LineStatsContext(context.Background())
	if err != nil || got == nil || *got != (store.LineStats{Cues: 2, Chunks: 2, ChunksIndexed: 1}) {
		t.Fatalf("statistics = %+v, error = %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.LineStatsContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query error = %v", err)
	}
}

func TestCollectorSharesLineStatsAndKeepsOtherFieldsCurrent(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	c := &Collector{St: st}
	c.Collect()
	waitForLineStats(t, &c.lines)
	if got := c.Collect(); got.Lines == nil || got.Lines.Cues != 0 || got.Queue == nil {
		t.Fatalf("initial snapshot = %+v", got)
	}
	if _, err := st.EnqueueJob("scan", "payload", 1); err != nil {
		t.Fatal(err)
	}
	if got := c.Collect(); got.Lines == nil || got.Lines.Cues != 0 || got.Queue == nil || got.Queue.Queued != 1 {
		t.Fatalf("subsequent snapshot = %+v", got)
	}
}

func TestHandlerClearsStateAndStopsWhenIdle(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	c := &Collector{St: st}
	for iteration := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		c.Handler()(rec, httptest.NewRequest("GET", "/events", nil).WithContext(ctx))
		if !strings.Contains(rec.Body.String(), "event: tick") {
			t.Fatalf("iteration %d missing initial tick", iteration)
		}
		if iteration == 0 {
			if _, err := st.EnqueueJob("scan", "payload", 1); err != nil {
				t.Fatal(err)
			}
		} else if !strings.Contains(rec.Body.String(), `"queued":1`) {
			t.Fatalf("reconnect received stale state: %s", rec.Body.String())
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		running, state := c.running, c.state
		c.mu.Unlock()
		if !running && state == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("collector remained active after clients left")
}
