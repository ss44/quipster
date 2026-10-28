package jobs

import (
	"context"
	"testing"
	"time"

	"quipster/internal/store"
)

func TestRunnerCancelActiveType(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	started := make(chan string, 2)
	block := func(kind string) Handler {
		return func(ctx context.Context, _ *store.Job, _ func(float64)) error {
			started <- kind
			<-ctx.Done()
			return ctx.Err()
		}
	}
	r.Register("probe", 1, block("probe"))
	r.Register("scan", 1, block("scan"))

	probeID, err := st.EnqueueJob("probe", `{"mediaId":1}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	scanID, err := st.EnqueueJob("scan", `{}`, 3)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case kind := <-started:
			seen[kind] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("handlers did not both start; seen=%v", seen)
		}
	}

	if n := r.CancelActiveType("probe"); n != 1 {
		t.Fatalf("CancelActiveType(probe) = %d, want 1", n)
	}
	// The probe must be requeued by the scan suspension path without spending
	// an attempt while the scan keeps running.
	waitFor(t, "probe suspended by scan", func() bool {
		j := jobByID(t, st, probeID)
		return j != nil && j.Error == "scan-active" && j.Attempts == 1
	})
	if j := jobByID(t, st, scanID); j == nil || j.Status != "running" {
		t.Fatalf("scan must not be canceled by CancelActiveType(probe): %+v", j)
	}
	r.CancelActive()
}

func TestRunnerRecoversPanic(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	r.Register("panic", 1, func(context.Context, *store.Job, func(float64)) error {
		panic("intentional test panic")
	})
	id, err := st.EnqueueJob("panic", `{}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	waitJobStatus(t, st, id, "error")

	// The runner must still serve other job types after a panic.
	r.Register("ok", 1, func(context.Context, *store.Job, func(float64)) error { return nil })
	okID, err := st.EnqueueJob("ok", `{}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	waitJobStatus(t, st, okID, "done")
}

func TestRunnerStopCancelsBlockedJob(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	started := make(chan struct{})
	r.Register("blocked", 1, func(ctx context.Context, _ *store.Job, _ func(float64)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := st.EnqueueJob("blocked", `{}`, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked job never started")
	}
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not cancel the blocked job")
	}
}
