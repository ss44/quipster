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

type Runner struct {
	st          *store.Store
	workerName  string
	maxAttempts int
	jobTimeout  time.Duration

	mu       sync.Mutex
	handlers map[string]Handler
	quits    map[string][]chan struct{}
	wg       sync.WaitGroup
}

func NewRunner(st *store.Store, workerName string) *Runner {
	return &Runner{
		st:          st,
		workerName:  workerName,
		maxAttempts: 3,
		jobTimeout:  3 * time.Hour,
		handlers:    map[string]Handler{},
		quits:       map[string][]chan struct{}{},
	}
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
		r.st.FinishJob(j.ID, "error", "no handler registered")
		return
	}
	last := time.Now()
	progress := func(p float64) {
		if time.Since(last) < 900*time.Millisecond && p < 1 {
			return
		}
		last = time.Now()
		r.st.SetJobProgress(j.ID, p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.jobTimeout)
	defer cancel()
	err := h(ctx, j, progress)
	if err == nil {
		if err := r.st.FinishJob(j.ID, "done", ""); err != nil {
			log.Printf("[jobs] finish error job %d: %v", j.ID, err)
		}
		return
	}
	if j.Attempts < r.maxAttempts {
		log.Printf("[jobs] job %d (%s) attempt %d failed, requeueing: %v", j.ID, j.Type, j.Attempts, err)
		r.st.RequeueJob(j.ID, err.Error())
		return
	}
	log.Printf("[jobs] job %d (%s) failed permanently after %d attempts: %v", j.ID, j.Type, j.Attempts, err)
	r.st.FinishJob(j.ID, "error", err.Error())
}

// Stop signals all workers and waits for the current job of each to wind down.
func (r *Runner) Stop() {
	r.mu.Lock()
	quits := make([]chan struct{}, 0, 8)
	for _, qs := range r.quits {
		quits = append(quits, qs...)
	}
	r.mu.Unlock()
	for _, q := range quits {
		close(q)
	}
	r.wg.Wait()
}
