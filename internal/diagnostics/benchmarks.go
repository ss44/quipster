package diagnostics

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"quipster/internal/store"
)

// JobBench summarizes one job type over the selected window.
type JobBench struct {
	Type      string  `json:"type"`
	Count     int     `json:"count"`
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	MaxMs     float64 `json:"maxMs"`
	AvgWaitMs float64 `json:"avgWaitMs"`
}

// Benchmarks is the aggregated performance report.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Benchmarks struct {
	At               int64      `json:"at"`
	Window           string     `json:"window"`
	Since            int64      `json:"since"`
	ReadReason       string     `json:"readReason,omitempty"`
	Jobs             []JobBench `json:"jobs"`
	ThroughputPerMin float64    `json:"throughputPerMin"`
	ReadMBps         float64    `json:"readMBps"`
	ReadAvailable    bool       `json:"readAvailable"`
}

// benchCache is one cached benchmark response.
type benchCache struct {
	at time.Time
	b  *Benchmarks
}

// Benchmarks aggregates completed job durations for the requested window. The
// result is cached for 30 seconds so polling clients do not re-run SQL.
func (c *Collector) Benchmarks(window string) *Benchmarks {
	if c == nil || c.St == nil {
		return &Benchmarks{At: time.Now().UnixMilli(), Window: window}
	}
	window = normalizeWindow(window)
	now := time.Now()
	c.mu.Lock()
	if c.bench == nil {
		c.bench = map[string]benchCache{}
	}
	if entry, ok := c.bench[window]; ok && now.Sub(entry.at) < 30*time.Second {
		c.mu.Unlock()
		return entry.b
	}
	c.mu.Unlock()

	b := c.collectBenchmarks(window, now)

	c.mu.Lock()
	c.bench[window] = benchCache{at: now, b: b}
	c.mu.Unlock()
	return b
}

// collectBenchmarks performs the bounded aggregation.
func (c *Collector) collectBenchmarks(window string, now time.Time) *Benchmarks {
	d := windowDuration(window)
	since := now.Add(-d).UnixMilli()
	b := &Benchmarks{At: now.UnixMilli(), Window: window, Since: since}
	jobs, err := c.St.FinishedJobsSince(since, 20000)
	if err != nil {
		b.ReadReason = "job history unavailable"
		return b
	}
	b.Jobs = aggregateBenchmarks(jobs)
	if minutes := d.Minutes(); minutes > 0 {
		b.ThroughputPerMin = float64(len(jobs)) / minutes
	}
	b.ReadMBps, b.ReadAvailable, b.ReadReason = c.readThroughput(jobs)
	return b
}

// aggregateBenchmarks groups finished jobs by type and computes latency
// percentiles plus average queue wait.
func aggregateBenchmarks(jobs []store.Job) []JobBench {
	byType := map[string][]float64{}
	waits := map[string][]float64{}
	for _, j := range jobs {
		if j.StartedAt <= 0 || j.FinishedAt < j.StartedAt {
			continue
		}
		byType[j.Type] = append(byType[j.Type], float64(j.FinishedAt-j.StartedAt))
		if j.CreatedAt > 0 && j.StartedAt >= j.CreatedAt {
			waits[j.Type] = append(waits[j.Type], float64(j.StartedAt-j.CreatedAt))
		}
	}
	out := make([]JobBench, 0, len(byType))
	for typ, durations := range byType {
		sort.Float64s(durations)
		jb := JobBench{
			Type:  typ,
			Count: len(durations),
			P50Ms: percentile(durations, 0.50),
			P95Ms: percentile(durations, 0.95),
			MaxMs: durations[len(durations)-1],
		}
		if w := waits[typ]; len(w) > 0 {
			var sum float64
			for _, v := range w {
				sum += v
			}
			jb.AvgWaitMs = sum / float64(len(w))
		}
		out = append(out, jb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// percentile returns the nearest-rank percentile of a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted))*p+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// readThroughput derives source-read throughput from probe job duration and
// media size. It is explicitly an estimate, not a live disk counter.
func (c *Collector) readThroughput(jobs []store.Job) (float64, bool, string) {
	type sample struct {
		mediaID int64
		millis  int64
	}
	samples := make([]sample, 0, len(jobs))
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		if j.Type != "probe" || j.StartedAt <= 0 || j.FinishedAt <= j.StartedAt {
			continue
		}
		id := mediaIDFromPayload(j.Payload)
		if id <= 0 {
			continue
		}
		samples = append(samples, sample{mediaID: id, millis: j.FinishedAt - j.StartedAt})
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(samples) == 0 {
		return 0, false, "no completed probe jobs in this window"
	}
	sizes, err := c.St.MediaSizes(ids)
	if err != nil {
		return 0, false, "media sizes unavailable"
	}
	var bytes, millis int64
	for _, s := range samples {
		if size := sizes[s.mediaID]; size > 0 {
			bytes += size
			millis += s.millis
		}
	}
	if millis <= 0 || bytes <= 0 {
		return 0, false, "no media sizes for completed probes"
	}
	mbps := float64(bytes) / (float64(millis) / 1000) / (1024 * 1024)
	return mbps, true, ""
}

// mediaIDFromPayload extracts mediaId from a job payload.
func mediaIDFromPayload(payload string) int64 {
	var p struct {
		MediaID int64 `json:"mediaId"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return 0
	}
	return p.MediaID
}

// normalizeWindow accepts 1h/6h/24h/7d and defaults to 1h.
func normalizeWindow(w string) string {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "1h", "6h", "24h", "7d":
		return strings.ToLower(strings.TrimSpace(w))
	default:
		return "1h"
	}
}

// windowDuration maps a normalized window label to a duration.
func windowDuration(w string) time.Duration {
	switch w {
	case "6h":
		return 6 * time.Hour
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return time.Hour
	}
}
