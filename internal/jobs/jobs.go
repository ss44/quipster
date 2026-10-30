// Package jobs provides the job runner that claims and executes queued work.
package jobs

import (
	"context"
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
	st          *store.Store
	workerName  string
	maxAttempts int
	jobTimeout  time.Duration

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
	r := &Runner{
		st:          st,
		workerName:  workerName,
		maxAttempts: 3,
		jobTimeout:  3 * time.Hour,
		handlers:    map[string]Handler{},
		quits:       map[string][]chan struct{}{},
		active:      map[int64]activeJob{},
		stop:        make(chan struct{}),
	}
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
	r.handlers[jobType] = h
	for i := 0; i < n; i++ {
		quit := make(chan struct{})
		r.quits[jobType] = append(r.quits[jobType], quit)
		r.wg.Add(1)
		go r.loop(jobType, fmt.Sprintf("%s-%s-%d", r.workerName, jobType, i), quit)
	}
}

func (r *Runner) loop(jobType, workerID string, quit chan struct{}) {
	defer r.wg.Done()
	for {
		select {
		case <-quit:
			return
		default:
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-quit:
				cancel()
			case <-ctx.Done():
			}
		}()
		if paused, err := r.st.GetQueuePaused(); err == nil && paused {
			select {
			case <-quit:
				return
			case <-time.After(400 * time.Millisecond):
			}
			continue
		}
		j, err := r.st.ClaimJob(ctx, workerID, []string{jobType})
		cancel()
		if err != nil {
			log.Printf("[jobs] claim error (%s): %v", jobType, err)
			time.Sleep(time.Second)
			continue
		}
		if j == nil {
			select {
			case <-quit:
				return
			case <-time.After(750 * time.Millisecond):
			}
			continue
		}
		r.run(jobType, j)
	}
}

func (r *Runner) run(jobType string, j *store.Job) {
	r.mu.Lock()
	h := r.handlers[jobType]
	r.mu.Unlock()
	if h == nil {
		log.Printf("[jobs] no handler for job type %s", jobType)
		if ferr := r.st.FinishJob(j.ID, "error", "no handler registered"); ferr != nil {
			log.Printf("[jobs] finish error job %d: %v", j.ID, ferr)
		}
		return
	}
	last := time.Now()
	progress := func(p float64) {
		if time.Since(last) < 900*time.Millisecond && p < 1 {
			return
		}
		last = time.Now()
		if perr := r.st.SetJobProgress(j.ID, p); perr != nil {
			log.Printf("[jobs] progress error job %d: %v", j.ID, perr)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.jobTimeout)
	defer cancel()
	r.activeMu.Lock()
	r.active[j.ID] = activeJob{cancel: cancel, jobType: jobType}
	r.activeMu.Unlock()
	defer func() {
		r.activeMu.Lock()
		delete(r.active, j.ID)
		r.activeMu.Unlock()
	}()

	err := runRecover(ctx, h, j, progress)
	if err == nil {
		if ferr := r.st.FinishJob(j.ID, "done", ""); ferr != nil {
			log.Printf("[jobs] finish error job %d: %v", j.ID, ferr)
		}
		return
	}
	r.handleRunError(j, err)
}

// handleRunError applies the retry policy: queue pauses and scan suspensions
// requeue softly, ordinary failures consume an attempt, and exhausted jobs are
// finished as errors.
func (r *Runner) handleRunError(j *store.Job, err error) {
	if paused, perr := r.st.GetQueuePaused(); perr == nil && paused {
		log.Printf("[jobs] job %d (%s) interrupted by queue pause, requeueing", j.ID, j.Type)
		if rerr := r.st.RequeueJobSoft(j.ID, "paused"); rerr != nil {
			log.Printf("[jobs] soft requeue error job %d: %v", j.ID, rerr)
		}
		return
	}
	// The scan gate cancels in-flight probes to free storage I/O. Those are
	// suspensions, not failures: requeue them without spending an attempt so
	// repeated manual scans cannot exhaust a probe's retry budget.
	if j.Type == "probe" && errors.Is(err, context.Canceled) {
		if active, serr := r.st.HasActiveScanJob(); serr == nil && active {
			log.Printf("[jobs] job %d (probe) suspended by active scan, requeueing without an attempt", j.ID)
			if rerr := r.st.RequeueJobSoft(j.ID, "scan-active"); rerr != nil {
				log.Printf("[jobs] soft requeue error job %d: %v", j.ID, rerr)
			}
			return
		}
	}
	// Any job canceled by pause, stop or shutdown is a suspension, not a
	// failure: requeue it without spending an attempt. DeadlineExceeded is
	// excluded because a job that exhausted its own timeout is a real failure.
	if errors.Is(err, context.Canceled) {
		log.Printf("[jobs] job %d (%s) canceled, requeueing without an attempt", j.ID, j.Type)
		if rerr := r.st.RequeueJobSoft(j.ID, "canceled"); rerr != nil {
			log.Printf("[jobs] soft requeue error job %d: %v", j.ID, rerr)
		}
		return
	}
	if j.Attempts < r.maxAttempts {
		log.Printf("[jobs] job %d (%s) attempt %d failed, requeueing: %v", j.ID, j.Type, j.Attempts, err)
		if rerr := r.st.RequeueJob(j.ID, err.Error()); rerr != nil {
			log.Printf("[jobs] requeue error job %d: %v", j.ID, rerr)
		}
		return
	}
	log.Printf("[jobs] job %d (%s) failed permanently after %d attempts: %v", j.ID, j.Type, j.Attempts, err)
	if ferr := r.st.FinishJob(j.ID, "error", err.Error()); ferr != nil {
		log.Printf("[jobs] finish error job %d: %v", j.ID, ferr)
	}
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
		quits := make([]chan struct{}, 0, 8)
		for _, qs := range r.quits {
			quits = append(quits, qs...)
		}
		r.mu.Unlock()
		close(r.stop)
		for _, q := range quits {
			close(q)
		}
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
