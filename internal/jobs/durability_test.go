package jobs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"quipster/internal/store"
)

type faultStore struct {
	*store.Store
	finalize func(context.Context, int64, string, int, string, string, bool) (bool, error)
	claim    func(context.Context, string, []string) (*store.Job, error)
	running  func(context.Context, string) (*store.Job, error)
}

func (s *faultStore) FinalizeOwnedJob(ctx context.Context, id int64, workerID string, attempts int, status, note string, soft bool) (bool, error) {
	if s.finalize != nil {
		return s.finalize(ctx, id, workerID, attempts, status, note, soft)
	}
	return s.Store.FinalizeOwnedJob(ctx, id, workerID, attempts, status, note, soft)
}

func (s *faultStore) ClaimJob(ctx context.Context, workerID string, types []string) (*store.Job, error) {
	if s.claim != nil {
		return s.claim(ctx, workerID, types)
	}
	return s.Store.ClaimJob(ctx, workerID, types)
}

func (s *faultStore) RunningJobByWorker(ctx context.Context, workerID string) (*store.Job, error) {
	if s.running != nil {
		return s.running(ctx, workerID)
	}
	return s.Store.RunningJobByWorker(ctx, workerID)
}

func durabilityRunner(t *testing.T, st runnerStore) *Runner {
	t.Helper()
	r := newRunner(st, "w")
	r.persistenceTimeout = 50 * time.Millisecond
	r.retryDelay = 5 * time.Millisecond
	t.Cleanup(r.Stop)
	return r
}

func activeCount(r *Runner) int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return len(r.active)
}

func TestRunnerRetainsSuccessfulClaimUntilDurable(t *testing.T) {
	st := openStore(t)
	var blocked atomic.Bool
	blocked.Store(true)
	var writes atomic.Int32
	fs := &faultStore{Store: st}
	fs.finalize = func(ctx context.Context, id int64, owner string, attempt int, status, note string, soft bool) (bool, error) {
		writes.Add(1)
		if blocked.Load() {
			return false, errors.New("SQLITE_BUSY")
		}
		return st.FinalizeOwnedJob(ctx, id, owner, attempt, status, note, soft)
	}
	r := durabilityRunner(t, fs)
	first, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	second, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	owners := make(chan string, 2)
	r.Register("demo", 1, func(_ context.Context, j *store.Job, _ func(float64)) error {
		owners <- j.WorkerID
		return nil
	})
	waitFor(t, "completion retries", func() bool { return writes.Load() >= 3 })
	require.Equal(t, 1, activeCount(r))
	require.Equal(t, "running", jobByID(t, st, first).Status)
	require.Equal(t, "queued", jobByID(t, st, second).Status)
	require.Len(t, owners, 1)
	blocked.Store(false)
	waitJobStatus(t, st, first, "done")
	waitJobStatus(t, st, second, "done")
	firstOwner, secondOwner := <-owners, <-owners
	require.NotEqual(t, firstOwner, secondOwner)
	require.True(t, strings.HasPrefix(firstOwner, "local-w-demo-"))
	require.Equal(t, 1, jobByID(t, st, first).Attempts)
	require.Equal(t, 1, jobByID(t, st, second).Attempts)
}

func TestRunnerPersistenceRetriesPreserveFailureBudget(t *testing.T) {
	st := openStore(t)
	var writes atomic.Int32
	fs := &faultStore{Store: st}
	fs.finalize = func(ctx context.Context, id int64, owner string, attempt int, status, note string, soft bool) (bool, error) {
		if writes.Add(1)%2 == 1 {
			return false, errors.New("SQLITE_BUSY")
		}
		return st.FinalizeOwnedJob(ctx, id, owner, attempt, status, note, soft)
	}
	r := durabilityRunner(t, fs)
	id, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	var executions atomic.Int32
	r.Register("demo", 1, func(context.Context, *store.Job, func(float64)) error {
		executions.Add(1)
		return errors.New("failed")
	})
	j := waitJobStatus(t, st, id, "error")
	require.Equal(t, int32(3), executions.Load())
	require.Equal(t, int32(6), writes.Load())
	require.Equal(t, 3, j.Attempts)
	require.Equal(t, "failed", j.Error)
}

func TestRunnerStaleSoftRequeueCannotAffectNewClaim(t *testing.T) {
	st := openStore(t)
	id, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	j, err := st.ClaimJob(t.Context(), "old-owner", []string{"demo"})
	require.NoError(t, err)
	require.NotNil(t, j)
	j.WorkerID = "old-owner"
	var writes atomic.Int32
	fs := &faultStore{Store: st}
	fs.finalize = func(ctx context.Context, id int64, owner string, attempt int, status, note string, soft bool) (bool, error) {
		matched, ferr := st.FinalizeOwnedJob(ctx, id, owner, attempt, status, note, soft)
		if ferr != nil || writes.Add(1) != 1 {
			return matched, ferr
		}
		next, cerr := st.ClaimJob(ctx, "new-owner", []string{"demo"})
		if cerr != nil {
			return false, cerr
		}
		if next == nil {
			return false, errors.New("requeued job was not claimable")
		}
		_, perr := st.SetOwnedJobProgress(ctx, id, "new-owner", next.Attempts, 0.65)
		if perr != nil {
			return false, perr
		}
		return false, errors.New("uncertain finalization acknowledgement")
	}
	r := durabilityRunner(t, fs)
	r.Register("demo", 0, func(context.Context, *store.Job, func(float64)) error { return context.Canceled })
	r.run("demo", j)
	got := jobByID(t, st, id)
	require.Equal(t, "running", got.Status)
	require.Equal(t, "new-owner", got.WorkerID)
	require.Equal(t, 1, got.Attempts)
	require.Equal(t, 0.65, got.Progress)
	require.Equal(t, int32(2), writes.Load())
}

func TestRunnerRecoversOnlyItsUncertainClaim(t *testing.T) {
	st := openStore(t)
	foreign := make([]int64, 0, 2)
	for _, owner := range []string{"local-another-live-worker", store.NodeWorkerID(1)} {
		id, err := st.EnqueueJob("foreign", `{}`, 0)
		require.NoError(t, err)
		_, err = st.ClaimJob(t.Context(), owner, []string{"foreign"})
		require.NoError(t, err)
		foreign = append(foreign, id)
	}
	id, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	fs := &faultStore{Store: st}
	var claims, reads atomic.Int32
	fs.claim = func(ctx context.Context, owner string, types []string) (*store.Job, error) {
		j, cerr := st.ClaimJob(ctx, owner, types)
		if cerr == nil && claims.Add(1) == 1 {
			return nil, errors.New("uncertain claim commit")
		}
		return j, cerr
	}
	fs.running = func(ctx context.Context, owner string) (*store.Job, error) {
		if reads.Add(1) < 3 {
			return nil, errors.New("temporary read failure")
		}
		return st.RunningJobByWorker(ctx, owner)
	}
	r := durabilityRunner(t, fs)
	var executions atomic.Int32
	r.Register("demo", 1, func(context.Context, *store.Job, func(float64)) error {
		executions.Add(1)
		return nil
	})
	j := waitJobStatus(t, st, id, "done")
	require.Equal(t, int32(1), executions.Load())
	require.Equal(t, 1, j.Attempts)
	require.GreaterOrEqual(t, reads.Load(), int32(3))
	for _, foreignID := range foreign {
		require.Equal(t, "running", jobByID(t, st, foreignID).Status)
	}
}

func TestRunnerShutdownBoundsUnresolvedPersistence(t *testing.T) {
	st := openStore(t)
	fs := &faultStore{Store: st}
	var writes atomic.Int32
	fs.finalize = func(ctx context.Context, _ int64, _ string, _ int, _, _ string, _ bool) (bool, error) {
		writes.Add(1)
		<-ctx.Done()
		return false, ctx.Err()
	}
	r := durabilityRunner(t, fs)
	id, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	var executions atomic.Int32
	r.Register("demo", 1, func(context.Context, *store.Job, func(float64)) error {
		executions.Add(1)
		return nil
	})
	waitFor(t, "pending persistence", func() bool { return writes.Load() >= 2 })
	require.Equal(t, 1, activeCount(r))
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited indefinitely for failed persistence")
	}
	require.Equal(t, "running", jobByID(t, st, id).Status)
	require.Equal(t, int32(1), executions.Load())
}

func TestRunnerStopBetweenClaimAndHandler(t *testing.T) {
	st := openStore(t)
	id, err := st.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	claimed := make(chan struct{})
	fs := &faultStore{Store: st}
	fs.claim = func(ctx context.Context, owner string, types []string) (*store.Job, error) {
		j, cerr := st.ClaimJob(ctx, owner, types)
		if cerr == nil && j != nil {
			close(claimed)
			<-ctx.Done()
		}
		return j, cerr
	}
	r := durabilityRunner(t, fs)
	var executions atomic.Int32
	r.Register("demo", 1, func(context.Context, *store.Job, func(float64)) error {
		executions.Add(1)
		return nil
	})
	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("claim did not complete")
	}
	r.Stop()
	j := jobByID(t, st, id)
	require.Equal(t, "queued", j.Status)
	require.Equal(t, 0, j.Attempts)
	require.Equal(t, int32(0), executions.Load())
}
