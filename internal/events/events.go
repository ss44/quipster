package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"quipster/internal/store"
)

// Snapshot is the admin progress payload streamed over SSE each tick.
type Snapshot struct {
	Scan        *store.Scan      `json:"scan"`
	Queue       *store.JobStats  `json:"queue"`
	MediaCounts map[string]int64 `json:"mediaCounts"`
	CacheBytes  int64            `json:"cacheBytes"`
	Recent      []store.Job      `json:"recentJobs"`
	MeiliUp     bool             `json:"meiliUp"`
}

type Collector struct {
	St      *store.Store
	MeiliUp func() bool
}

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
	if jobs, err := c.St.ListJobs(8); err == nil {
		snap.Recent = jobs
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
			fmt.Fprintf(w, "event: tick\ndata: %s\n\n", b)
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
