package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quipster/internal/store"
)

func openWorkerTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	return st
}

func upsertTestMedia(t *testing.T, st *store.Store) int64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huge.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.UpsertMedia(path, "mkv", 1, 1, "Huge", 2026, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func probeJob(t *testing.T, id int64) *store.Job {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"mediaId": id})
	if err != nil {
		t.Fatal(err)
	}
	return &store.Job{ID: id, Type: "probe", Payload: string(payload)}
}

// A timed-out probe must kill ffprobe, record the skip on the media row, and
// finish the job so the queue keeps moving.
func TestJobHandlerSkipsTimedOutProbe(t *testing.T) {
	st := openWorkerTestStore(t)
	id := upsertTestMedia(t, st)
	writeFakeFfprobe(t, "while :; do :; done")

	p := &Pipeline{St: st, SubsCacheDir: t.TempDir(), ProbeTimeout: 100 * time.Millisecond}
	if err := p.JobHandler()(context.Background(), probeJob(t, id), func(float64) {}); err != nil {
		t.Fatalf("timed-out probe must finish the job, got %v", err)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "error" || !strings.Contains(m.Error, "timed out") {
		t.Fatalf("expected error status with timeout note, got %+v", m)
	}
}

// A canceled/failed job context (queue pause, shutdown) must requeue the job,
// not leave the media marked errored.
func TestJobHandlerRequeuesOnCanceledContext(t *testing.T) {
	st := openWorkerTestStore(t)
	id := upsertTestMedia(t, st)
	writeFakeFfprobe(t, "while :; do :; done")

	p := &Pipeline{St: st, SubsCacheDir: t.TempDir(), ProbeTimeout: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.JobHandler()(ctx, probeJob(t, id), func(float64) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled job context must surface for requeue, got %v", err)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "pending" {
		t.Fatalf("canceled job must not mark media errored, got %q", m.Status)
	}
}

func TestClampMin(t *testing.T) {
	if got := clampMin(5.0); got != 5.0 {
		t.Errorf("clampMin(5.0) = %v, want 5.0", got)
	}
	if got := clampMin(-1.2); got != 0 {
		t.Errorf("clampMin(-1.2) = %v, want 0", got)
	}
}

func TestDetectSidecarLang(t *testing.T) {
	if got := detectSidecarLang("some_file.srt"); got != "en" {
		t.Errorf("detectSidecarLang() = %v, want en", got)
	}
}

func TestPipelineSem(t *testing.T) {
	p := &Pipeline{ExtractConns: 3}
	s := p.sem()
	if cap(s) != 3 {
		t.Errorf("expected cap 3, got %d", cap(s))
	}

	// Default
	p2 := &Pipeline{}
	s2 := p2.sem()
	if cap(s2) != 2 {
		t.Errorf("expected default cap 2, got %d", cap(s2))
	}
}
