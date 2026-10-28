package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quipster/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"))
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

func jobByID(t *testing.T, st *store.Store, id int64) *store.Job {
	t.Helper()
	jobs, err := st.ListJobs(100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for i := range jobs {
		if jobs[i].ID == id {
			return &jobs[i]
		}
	}
	return nil
}

func waitJobStatus(t *testing.T, st *store.Store, id int64, status string) *store.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j := jobByID(t, st, id)
		if j != nil && j.Status == status {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j := jobByID(t, st, id)
	t.Fatalf("job %d did not reach status %q, last: %+v", id, status, j)
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunnerProcessesJob(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	var progressCalls atomic.Int32
	r.Register("demo", 1, func(_ context.Context, _ *store.Job, progress func(float64)) error {
		// The first report is written; the second is throttled because it is
		// below 1.0 and arrives within the 900ms reporting window.
		progress(1)
		progress(0.25)
		progressCalls.Add(2)
		return nil
	})

	id, err := st.EnqueueJob("demo", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j := waitJobStatus(t, st, id, "done")
	if j.Progress != 1 {
		t.Errorf("progress = %v, want 1", j.Progress)
	}
	if progressCalls.Load() != 2 {
		t.Errorf("handler not invoked as expected: %d", progressCalls.Load())
	}
}

func TestRunnerRequeuesAndFailsPermanently(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	var attempts atomic.Int32
	r.Register("flaky", 1, func(context.Context, *store.Job, func(float64)) error {
		attempts.Add(1)
		return errors.New("boom")
	})

	id, err := st.EnqueueJob("flaky", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j := waitJobStatus(t, st, id, "error")
	if j.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", j.Attempts)
	}
	if j.Error != "boom" {
		t.Errorf("error = %q, want boom", j.Error)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("handler invocations = %d, want 3", got)
	}
}

func TestRunnerMissingHandler(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	id, err := st.EnqueueJob("orphan", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j, err := st.ClaimJob(context.Background(), "manual", []string{"orphan"})
	if err != nil || j == nil {
		t.Fatalf("claim: job=%v err=%v", j, err)
	}
	r.run("orphan", j)

	got := jobByID(t, st, id)
	if got == nil || got.Status != "error" {
		t.Fatalf("job = %+v, want error status", got)
	}
	if !strings.Contains(got.Error, "no handler") {
		t.Errorf("error = %q, want no handler message", got.Error)
	}
}

func TestRunnerFinishJobErrorPath(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	_, err := st.EnqueueJob("x", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j, err := st.ClaimJob(context.Background(), "manual", []string{"x"})
	if err != nil || j == nil {
		t.Fatalf("claim: job=%v err=%v", j, err)
	}
	r.mu.Lock()
	r.handlers["x"] = func(context.Context, *store.Job, func(float64)) error { return nil }
	r.mu.Unlock()

	// Closing the store forces FinishJob to fail; run must swallow the error
	// rather than panic or retry the job.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	r.run("x", j)
}

func TestRunnerHandlesClaimError(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	r.Register("idle", 1, func(context.Context, *store.Job, func(float64)) error { return nil })
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}

func TestRunnerTimeout(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)
	r.SetJobTimeout(50 * time.Millisecond)

	r.Register("slow", 1, func(ctx context.Context, _ *store.Job, _ func(float64)) error {
		<-ctx.Done()
		return ctx.Err()
	})

	id, err := st.EnqueueJob("slow", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j := waitJobStatus(t, st, id, "error")
	if !strings.Contains(j.Error, context.DeadlineExceeded.Error()) {
		t.Errorf("error = %q, want deadline exceeded", j.Error)
	}
}

func TestRunnerCancelActive(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	release := make(chan struct{})
	defer func() {
		close(release)
		r.Stop()
	}()

	started := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	r.Register("cancelable", 1, func(ctx context.Context, _ *store.Job, _ func(float64)) error {
		calls.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})

	_, err := st.EnqueueJob("cancelable", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("job never started")
	}
	if n := r.CancelActive(); n != 1 {
		t.Errorf("CancelActive() = %d, want 1", n)
	}
	// The canceled job must be requeued and picked up again.
	waitFor(t, "job retry", func() bool { return calls.Load() >= 2 })
}

func TestRunnerCancelActiveEmpty(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)
	if n := r.CancelActive(); n != 0 {
		t.Errorf("CancelActive() = %d, want 0", n)
	}
}

func TestRunnerPauseRequeuesSoftly(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	t.Cleanup(r.Stop)

	started := make(chan struct{})
	var once sync.Once
	r.Register("pausable", 1, func(ctx context.Context, _ *store.Job, _ func(float64)) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	})

	id, err := st.EnqueueJob("pausable", `{}`, 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("job never started")
	}
	// The pause watcher notices within a second, cancels the active job, and
	// run() requeues it without spending an attempt.
	if err := st.SetQueuePaused(true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	j := waitJobStatus(t, st, id, "queued")
	if j.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 after soft requeue", j.Attempts)
	}
	if j.Error != "paused" {
		t.Errorf("error = %q, want paused", j.Error)
	}
}

func TestStopWithoutJobs(t *testing.T) {
	st := openStore(t)
	r := NewRunner(st, "w")
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
}
