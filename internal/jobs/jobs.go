// Package jobs provides the job runner that claims and executes queued work.
package jobs

import (
	"context"
	"fmt"
	"log"
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
	active   map[int64]context.CancelFunc
	stop     chan struct{}
	stopOnce sync.Once
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
		active:      map[int64]context.CancelFunc{},
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
	r.active[j.ID] = cancel
	r.activeMu.Unlock()
	defer func() {
		r.activeMu.Lock()
		delete(r.active, j.ID)
		r.activeMu.Unlock()
	}()

	err := h(ctx, j, progress)
	if err == nil {
		if ferr := r.st.FinishJob(j.ID, "done", ""); ferr != nil {
			log.Printf("[jobs] finish error job %d: %v", j.ID, ferr)
		}
		return
	}
	if paused, perr := r.st.GetQueuePaused(); perr == nil && paused {
		log.Printf("[jobs] job %d (%s) interrupted by queue pause, requeueing", j.ID, j.Type)
		if rerr := r.st.RequeueJobSoft(j.ID, "paused"); rerr != nil {
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

// Stop signals all workers and waits for the current job of each to wind down.
func (r *Runner) Stop() {
	r.mu.Lock()
	quits := make([]chan struct{}, 0, 8)
	for _, qs := range r.quits {
		quits = append(quits, qs...)
	}
	r.mu.Unlock()
	r.stopOnce.Do(func() { close(r.stop) })
	for _, q := range quits {
		close(q)
	}
	r.wg.Wait()
}

// CancelActive interrupts all in-flight jobs (used by queue pause/stop).
func (r *Runner) CancelActive() int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	for _, cancel := range r.active {
		cancel()
	}
	return len(r.active)
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
