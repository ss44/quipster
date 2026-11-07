package jobs

import (
	"context"
	"log"
	"time"

	"quipster/internal/store"
)

type runnerStore interface {
	ClaimJob(context.Context, string, []string) (*store.Job, error)
	GetQueuePaused() (bool, error)
	HasActiveScanJob() (bool, error)
	FinalizeOwnedJob(context.Context, int64, string, int, string, string, bool) (bool, error)
	RunningJobByWorker(context.Context, string) (*store.Job, error)
	SetOwnedJobProgress(context.Context, int64, string, int, float64) (bool, error)
}

type jobResult struct {
	status string
	note   string
	soft   bool
}

func (r *Runner) persistResult(j *store.Job, result jobResult) {
	r.retryPersistence(func(ctx context.Context) error {
		matched, err := r.st.FinalizeOwnedJob(ctx, j.ID, j.WorkerID, j.Attempts, result.status, result.note, result.soft)
		if err == nil && !matched {
			log.Printf("[jobs] job %d ownership ended before %s persistence", j.ID, result.status)
		}
		return err
	})
}

func (r *Runner) reconcileClaim(workerID string) bool {
	return r.retryPersistence(func(ctx context.Context) error {
		j, err := r.st.RunningJobByWorker(ctx, workerID)
		if err != nil || j == nil {
			return err
		}
		_, err = r.st.FinalizeOwnedJob(ctx, j.ID, workerID, j.Attempts, "queued", "claim-interrupted", true)
		return err
	})
}

func (r *Runner) retryPersistence(operation func(context.Context) error) bool {
	delay := r.retryDelay
	for {
		ctx, cancel := context.WithTimeout(context.Background(), r.persistenceTimeout)
		err := operation(ctx)
		cancel()
		if err == nil {
			return true
		}
		log.Printf("[jobs] persistence error; retaining claim until reconciled: %v", err)
		if !waitForRetry(r.stop, delay) {
			log.Printf("[jobs] shutdown with unresolved persistence: %v", err)
			return false
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func waitForRetry(stop <-chan struct{}, delay time.Duration) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
