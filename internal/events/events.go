// Package events streams admin progress snapshots to browsers over
// server-sent events.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"quipster/internal/store"
)

// Snapshot is the admin progress payload streamed over SSE each tick.
// Fields are ordered to group pointer-carrying data for compact layout;
// JSON decoding is name-based so order is not significant on the wire.
type Snapshot struct {
	Scan        *store.Scan      `json:"scan"`
	Queue       *store.JobStats  `json:"queue"`
	MediaCounts map[string]int64 `json:"mediaCounts"`
	Lines       *store.LineStats `json:"lines"`
	Recent      []store.Job      `json:"recentJobs"`
	Running     []store.Job      `json:"runningJobs"`
	CacheBytes  int64            `json:"cacheBytes"`
	MeiliUp     bool             `json:"meiliUp"`
}

// Collector gathers a Snapshot from the store on demand and serves it as SSE.
type Collector struct {
	St       *store.Store
	MeiliUp  func() bool
	notifyCh chan struct{}
	state    []byte
	lines    lineStatsCache
	mu       sync.Mutex
	users    int
	running  bool
}

const (
	lineStatsRefreshInterval = 30 * time.Second
	lineStatsMaxAge          = 2 * time.Minute
	lineStatsTimeout         = time.Minute
)

type lineStatsCache struct {
	value      *store.LineStats
	at         time.Time
	next       time.Time
	mu         sync.Mutex
	refreshing bool
}

func (c *lineStatsCache) get(now time.Time, load func(context.Context) (*store.LineStats, error)) *store.LineStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.refreshing && !now.Before(c.next) {
		c.refreshing = true
		c.next = now.Add(lineStatsRefreshInterval)
		go c.refresh(now, load)
	}
	if c.value == nil || now.Sub(c.at) >= lineStatsMaxAge {
		return nil
	}
	value := *c.value
	return &value
}

func (c *lineStatsCache) refresh(at time.Time, load func(context.Context) (*store.LineStats, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), lineStatsTimeout)
	defer cancel()
	value, err := load(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && value != nil {
		valueCopy := *value
		c.value = &valueCopy
		c.at = at
	}
	if next := time.Now().Add(lineStatsRefreshInterval); next.After(c.next) {
		c.next = next
	}
	c.refreshing = false
}

// Collect builds a snapshot of current scan/queue/media state. Individual
// store reads are best-effort: a failing read leaves the corresponding
// snapshot field nil rather than failing the whole stream.
func (c *Collector) Collect() *Snapshot {
	snap := &Snapshot{}
	if sc, err := c.St.RunningScan(); err == nil {
		snap.Scan = sc
	}
	if qs, err := c.St.JobStats(); err == nil {
		snap.Queue = qs
	}
	if mc, err := c.St.MediaStatusCounts(); err == nil {
		snap.MediaCounts = mc
	}
	if cb, err := c.St.CacheBytes("clip", "thumb", "sub"); err == nil {
		snap.CacheBytes = cb
	}
	snap.Lines = c.lines.get(time.Now(), c.St.LineStatsContext)
	if jobs, err := c.St.ListJobs(50); err == nil {
		snap.Recent = jobs
	}
	if running, err := c.St.RunningJobs(12); err == nil {
		snap.Running = running
	}
	if c.MeiliUp != nil {
		snap.MeiliUp = c.MeiliUp()
	}
	return snap
}

func (c *Collector) run() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		c.mu.Lock()
		if c.users == 0 {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		snap := c.Collect()
		b, err := json.Marshal(snap)
		if err != nil {
			continue
		}

		c.mu.Lock()
		if c.users == 0 {
			c.running = false
			c.mu.Unlock()
			return
		}
		if !bytes.Equal(c.state, b) {
			c.state = b
			if c.notifyCh != nil {
				close(c.notifyCh)
			}
			c.notifyCh = make(chan struct{})
		}
		c.mu.Unlock()
	}
}

func (c *Collector) subscribe() ([]byte, chan struct{}) {
	c.mu.Lock()
	c.users++
	if c.notifyCh == nil {
		c.notifyCh = make(chan struct{})
	}
	if !c.running {
		c.running = true
		go c.run()
	}
	if c.state == nil {
		c.mu.Unlock()
		b, err := json.Marshal(c.Collect())
		c.mu.Lock()
		if err == nil && c.state == nil {
			c.state = b
		}
	}
	state, notifyCh := c.state, c.notifyCh
	c.mu.Unlock()
	return state, notifyCh
}

func (c *Collector) unsubscribe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users--
	if c.users == 0 {
		c.state = nil
	}
}

// Handler streams snapshots as SSE.
func (c *Collector) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		state, notifyCh := c.subscribe()
		defer c.unsubscribe()

		if state != nil {
			// A write error here almost always means the client disconnected;
			// stop sending rather than logging on every tick.
			if _, werr := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", state); werr != nil {
				return
			}
			fl.Flush()
		}

		ctx := r.Context()
		keepAlive := time.NewTicker(15 * time.Second)
		defer keepAlive.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-notifyCh:
				c.mu.Lock()
				state = c.state
				notifyCh = c.notifyCh
				c.mu.Unlock()

				if _, werr := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", state); werr != nil {
					return
				}
				fl.Flush()
			case <-keepAlive.C:
				if _, werr := fmt.Fprintf(w, "event: ping\ndata: \n\n"); werr != nil {
					return
				}
				fl.Flush()
			}
		}
	}
}
