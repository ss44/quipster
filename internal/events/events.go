// Package events streams admin progress snapshots to browsers over
// server-sent events.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	St      *store.Store
	MeiliUp func() bool
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
	if ls, err := c.St.LineStats(); err == nil {
		snap.Lines = ls
	}
	if jobs, err := c.St.ListJobs(8); err == nil {
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

// Handler streams snapshots as SSE with a 1s tick.
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
		ctx := r.Context()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		send := func() {
			snap := c.Collect()
			b, err := json.Marshal(snap)
			if err != nil {
				return
			}
			// A write error here almost always means the client disconnected;
			// stop sending rather than logging on every tick.
			if _, werr := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", b); werr != nil {
				return
			}
			fl.Flush()
		}
		send()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				send()
			}
		}
	}
}
