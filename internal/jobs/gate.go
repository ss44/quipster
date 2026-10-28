package jobs

import (
	"context"
	"log"
	"sync"

	"quipster/internal/store"
)

// ActiveCanceler cancels currently running jobs of one type. The runner
// implements it; tests can provide a fake.
type ActiveCanceler interface {
	CancelActiveType(jobType string) int
}

// Gate serializes metadata scans against probe work. Full-file subtitle
// extraction saturates the media share, which starves the scanner's stat and
// SQLite calls; a scan therefore runs exclusively and in-flight probes are
// canceled and requeued. Probes started while a scan is active wait for it to
// finish instead of consuming a network-storage slot.
type Gate struct {
	canceler   ActiveCanceler
	probesIdle chan struct{}
	scanDone   chan struct{}
	mu         sync.Mutex
	scanning   bool
	probes     int
}

// NewGate creates a gate. A nil canceler is allowed; it is useful in tests and
// in configurations that never register probe workers.
func NewGate(canceler ActiveCanceler) *Gate {
	return &Gate{canceler: canceler, probesIdle: make(chan struct{})}
}

// BeginScan acquires exclusive access for a scan. It cancels active probes so
// they release the media share, then waits for their handlers to return. The
// context bounds both waits, which lets queue pause/stop and the scan timeout
// unwind cleanly.
func (g *Gate) BeginScan(ctx context.Context) error {
	for {
		g.mu.Lock()
		if !g.scanning {
			g.scanning = true
			g.scanDone = make(chan struct{})
			g.mu.Unlock()
			break
		}
		done := g.scanDone
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}

	if g.canceler != nil {
		if n := g.canceler.CancelActiveType("probe"); n > 0 {
			log.Printf("[scan] canceled %d active probe job(s) to free storage I/O for the metadata walk", n)
		}
	}

	for {
		g.mu.Lock()
		if g.probes == 0 {
			g.mu.Unlock()
			return nil
		}
		idle := g.probesIdle
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.EndScan()
			return ctx.Err()
		case <-idle:
		}
	}
}

// EndScan releases exclusive access and wakes probes waiting for the scan.
func (g *Gate) EndScan() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.scanning {
		return
	}
	g.scanning = false
	if g.scanDone != nil {
		close(g.scanDone)
		g.scanDone = nil
	}
}

// BeginProbe acquires a shared probe slot, waiting while a scan is active.
func (g *Gate) BeginProbe(ctx context.Context) error {
	for {
		g.mu.Lock()
		if !g.scanning {
			g.probes++
			g.mu.Unlock()
			return nil
		}
		done := g.scanDone
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
}

// EndProbe releases a probe slot.
func (g *Gate) EndProbe() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.probes == 0 {
		return
	}
	g.probes--
	if g.probes == 0 {
		close(g.probesIdle)
		g.probesIdle = make(chan struct{})
	}
}

// Scan wraps a scan handler with exclusive gating.
func (g *Gate) Scan(handler func(context.Context, *store.Job, func(float64)) error) func(context.Context, *store.Job, func(float64)) error {
	return func(ctx context.Context, job *store.Job, progress func(float64)) error {
		if err := g.BeginScan(ctx); err != nil {
			return err
		}
		defer g.EndScan()
		return handler(ctx, job, progress)
	}
}

// Probe wraps a probe handler with shared gating.
func (g *Gate) Probe(handler func(context.Context, *store.Job, func(float64)) error) func(context.Context, *store.Job, func(float64)) error {
	return func(ctx context.Context, job *store.Job, progress func(float64)) error {
		if err := g.BeginProbe(ctx); err != nil {
			return err
		}
		defer g.EndProbe()
		return handler(ctx, job, progress)
	}
}
