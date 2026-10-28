package diagnostics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// StreamHandler serves resource gauges as server-sent events. Samples are
// shared across viewers by Collector.Gauge's short cache, so multiple tabs do
// not multiply vendor tool invocations.
func (c *Collector) StreamHandler() http.HandlerFunc {
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

		writeFrame := func(snap *GaugeSnapshot) bool {
			data, err := json.Marshal(snap)
			if err != nil {
				return true
			}
			if _, werr := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", data); werr != nil {
				return false
			}
			fl.Flush()
			return true
		}

		if !writeFrame(c.Gauge(r.Context())) {
			return
		}

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		keepAlive := time.NewTicker(15 * time.Second)
		defer keepAlive.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if !writeFrame(c.Gauge(r.Context())) {
					return
				}
			case <-keepAlive.C:
				if _, err := fmt.Fprint(w, "event: ping\ndata: \n\n"); err != nil {
					return
				}
				fl.Flush()
			}
		}
	}
}
