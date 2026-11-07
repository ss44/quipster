// Package jobs provides the job runner that claims and executes queued work.
package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"quipster/internal/store"
)

// Handler executes a claimed job. progress reports 0..1 for long-running jobs.
type Handler func(ctx context.Context, j *store.Job, progress func(float64)) error

// Runner claims jobs from the store and executes them with registered handlers.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Runner struct {
	st                 runnerStore
	workerName         string
	maxAttempts        int
	jobTimeout         time.Duration
	persistenceTimeout time.Duration
	retryDelay         time.Duration

	mu       sync.Mutex
	handlers map[string]Handler
	quits    map[string][]chan struct{}
	wg       sync.WaitGroup

	activeMu sync.Mutex
	active   map[int64]activeJob
	stop     chan struct{}
	stopOnce sync.Once
}

// activeJob tracks the cancel function and type of an in-flight job so the
// scan gate can cancel only probes without disturbing a scan or clip job.
type activeJob struct {
	cancel  context.CancelFunc
	jobType string
}

// NewRunner creates a Runner and starts its pause watcher.
func NewRunner(st *store.Store, workerName string) *Runner {
	return newRunner(st, workerName)
}

func newRunner(st runnerStore, workerName string) *Runner {
	r := &Runner{
		st:                 st,
		workerName:         workerName,
		maxAttempts:        3,
		jobTimeout:         3 * time.Hour,
		persistenceTimeout: 5 * time.Second,
		retryDelay:         250 * time.Millisecond,
		handlers:           map[string]Handler{},
		quits:              map[string][]chan struct{}{},
		active:             map[int64]activeJob{},
		stop:               make(chan struct{}),
	}
	r.wg.Add(1)
	go r.pauseWatcher()
	return r
}

// SetJobTimeout bounds each job execution (0 keeps the default 3h).
func (r *Runner) SetJobTimeout(d time.Duration) {
	if d > 0 {
		r.jobTimeout = d
	}
}

// Register wires a job type to its handler and starts n workers for it.
func (r *Runner) Register(jobType string, n int, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.stop:
		return
	default:
	}
	r.handlers[jobType] = h
	for i := 0; i < n; i++ {
		quit := make(chan struct{})
		r.quits[jobType] = append(r.quits[jobType], quit)
		r.wg.Add(1)
		go r.loop(jobType, quit)
	}
}

func (r *Runner) loop(jobType string, quit chan struct{}) {
	defer r.wg.Done()
	for {
		select {
		case <-quit:
			return
		default:
		}
		if paused, err := r.st.GetQueuePaused(); err == nil && paused {
			if !waitForRetry(quit, 400*time.Millisecond) {
				return
			}
			continue
		}
		workerID := fmt.Sprintf("local-%s-%s-%s", r.workerName, jobType, rand.Text())
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-quit:
				cancel()
			case <-ctx.Done():
			}
		}()
		j, err := r.st.ClaimJob(ctx, workerID, []string{jobType})
		cancel()
		if err != nil {
			log.Printf("[jobs] claim error (%s): %v", jobType, err)
			if !r.reconcileClaim(workerID) || !waitForRetry(quit, time.Second) {
				return
			}
			continue
		}
		if j == nil {
			if !waitForRetry(quit, 750*time.Millisecond) {
				return
			}
			continue
		}
		j.WorkerID = workerID
		r.run(jobType, j)
	}
}

func (r *Runner) run(jobType string, j *store.Job) {
	claim := *j
	ctx, cancel := context.WithTimeout(context.Background(), r.jobTimeout)
	defer cancel()
	r.activeMu.Lock()
	r.active[j.ID] = activeJob{cancel: cancel, jobType: jobType}
	r.activeMu.Unlock()
	defer func() {
		r.activeMu.Lock()
		delete(r.active, claim.ID)
		r.activeMu.Unlock()
	}()
	select {
	case <-r.stop:
		cancel()
	default:
	}
	if err := ctx.Err(); err != nil {
		r.handleRunError(&claim, err)
		return
	}
	r.mu.Lock()
	h := r.handlers[jobType]
	r.mu.Unlock()
	if h == nil {
		log.Printf("[jobs] no handler for job type %s", jobType)
		r.persistResult(&claim, jobResult{status: "error", note: "no handler registered"})
		return
	}
	err := runRecover(ctx, h, j, r.progressReporter(ctx, cancel, &claim))
	cancel()
	if err == nil {
		r.persistResult(&claim, jobResult{status: "done"})
		return
	}
	r.handleRunError(&claim, err)
}

func (r *Runner) progressReporter(ctx context.Context, cancel context.CancelFunc, j *store.Job) func(float64) {
	var mu sync.Mutex
	last := time.Now()
	return func(p float64) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil || (time.Since(last) < 900*time.Millisecond && p < 1) {
			return
		}
		last = time.Now()
		progressCtx, progressCancel := context.WithTimeout(ctx, r.persistenceTimeout)
		defer progressCancel()
		matched, err := r.st.SetOwnedJobProgress(progressCtx, j.ID, j.WorkerID, j.Attempts, p)
		if err != nil {
			log.Printf("[jobs] progress error job %d: %v", j.ID, err)
		} else if !matched {
			cancel()
		}
	}
}

// handleRunError applies the retry policy: queue pauses and scan suspensions
// requeue softly, ordinary failures consume an attempt, and exhausted jobs are
// finished as errors.
func (r *Runner) handleRunError(j *store.Job, err error) {
	if paused, perr := r.st.GetQueuePaused(); perr == nil && paused {
		log.Printf("[jobs] job %d (%s) interrupted by queue pause, requeueing", j.ID, j.Type)
		r.persistResult(j, jobResult{status: "queued", note: "paused", soft: true})
		return
	}
	// The scan gate cancels in-flight probes to free storage I/O. Those are
	// suspensions, not failures: requeue them without spending an attempt so
	// repeated manual scans cannot exhaust a probe's retry budget.
	if j.Type == "probe" && errors.Is(err, context.Canceled) {
		if active, serr := r.st.HasActiveScanJob(); serr == nil && active {
			log.Printf("[jobs] job %d (probe) suspended by active scan, requeueing without an attempt", j.ID)
			r.persistResult(j, jobResult{status: "queued", note: "scan-active", soft: true})
			return
		}
	}
	// Any job canceled by pause, stop or shutdown is a suspension, not a
	// failure: requeue it without spending an attempt. DeadlineExceeded is
	// excluded because a job that exhausted its own timeout is a real failure.
	if errors.Is(err, context.Canceled) {
		log.Printf("[jobs] job %d (%s) canceled, requeueing without an attempt", j.ID, j.Type)
		r.persistResult(j, jobResult{status: "queued", note: "canceled", soft: true})
		return
	}
	if j.Attempts < r.maxAttempts {
		log.Printf("[jobs] job %d (%s) attempt %d failed, requeueing: %v", j.ID, j.Type, j.Attempts, err)
		r.persistResult(j, jobResult{status: "queued", note: err.Error()})
		return
	}
	log.Printf("[jobs] job %d (%s) failed permanently after %d attempts: %v", j.ID, j.Type, j.Attempts, err)
	r.persistResult(j, jobResult{status: "error", note: err.Error()})
}

// runRecover executes a handler and converts a panic into a job error. A
// panic in one media handler must not take down the whole process and leave
// its scan row (and every other worker) stuck until a restart.
func runRecover(ctx context.Context, h Handler, j *store.Job, progress func(float64)) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[jobs] job %d (%s) panic: %v\n%s", j.ID, j.Type, rec, debug.Stack())
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	return h(ctx, j, progress)
}

// Stop signals all workers and waits for the current job of each to wind down.
// In-flight handlers are canceled first; otherwise a handler blocked on slow
// media I/O would hold shutdown open indefinitely.
func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		close(r.stop)
		for _, qs := range r.quits {
			for _, q := range qs {
				close(q)
			}
		}
		r.mu.Unlock()
		r.CancelActive()
		r.wg.Wait()
	})
}

// CancelActive interrupts all in-flight jobs (used by queue pause/stop).
func (r *Runner) CancelActive() int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	for _, job := range r.active {
		job.cancel()
	}
	return len(r.active)
}

// CancelActiveType interrupts only jobs of the given type and returns how many
// were canceled. The scan gate uses it to make room on the media share.
func (r *Runner) CancelActiveType(jobType string) int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	n := 0
	for _, job := range r.active {
		if job.jobType == jobType {
			job.cancel()
			n++
		}
	}
	return n
}

// pauseWatcher reacts to the persisted pause flag: workers in any role stop
// their in-flight jobs within a second of a pause request.
func (r *Runner) pauseWatcher() {
	defer r.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			if paused, err := r.st.GetQueuePaused(); err == nil && paused {
				r.CancelActive()
			}
		}
	}
}
