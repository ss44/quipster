package diagnostics

import (
	"runtime"
	"time"
)

// ReadSnapshot is a fixed-size read-only diagnostic summary. It excludes
// configured paths, configuration maps, logs, database queries and probe output.
//
//nolint:govet // wire grouping favors readability over optimal alignment.
type ReadSnapshot struct {
	At          int64       `json:"at"`
	Available   bool        `json:"available"`
	Reason      string      `json:"reason,omitempty"`
	RAM         Metric      `json:"ram"`
	Swap        Metric      `json:"swap"`
	Load        LoadMetric  `json:"load"`
	SQLitePools sqlitePools `json:"sqlitePools"`
	System      ReadSystem  `json:"system"`
}

// ReadSystem contains only bounded build/runtime identity and uptime, not the
// slow System report (which can run commands and query SQLite).
//
//nolint:govet // wire grouping favors readability over optimal alignment.
type ReadSystem struct {
	AppVersion  string `json:"appVersion"`
	AppRevision string `json:"appRevision"`
	GoVersion   string `json:"goVersion"`
	OS          string `json:"goos"`
	Arch        string `json:"goarch"`
	CPUCores    int    `json:"cpuCores"`
	AppUptimeS  int64  `json:"appUptimeSec"`
	Goroutines  int    `json:"goroutines"`
}

// ReadOnlySnapshot gathers lightweight metrics without taking the Gauge/System
// locks: those paths may be waiting on SQLite, disk mounts or external tools.
// Only fixed local OS counters and in-memory DB pool stats are sampled. No
// benchmark, GPU/network probe, filesystem walk, full count or SQL is issued.
func (c *Collector) ReadOnlySnapshot() ReadSnapshot {
	now := time.Now()
	out := ReadSnapshot{At: now.UnixMilli(), SQLitePools: writerPoolStats(nil)}
	if c == nil {
		out.Reason = "collector_unavailable"
		return out
	}
	out.Available = true
	out.RAM, out.Swap = sampleMemory(now)
	out.Load = sampleLoad()
	out.SQLitePools = writerPoolStats(c.St)
	version := "[value omitted]"
	if len(c.Version) <= 256 {
		version = redactLog(c.Version)
	}
	out.System = ReadSystem{AppVersion: version, AppRevision: validRevision(c.Revision), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(), Goroutines: runtime.NumGoroutine()}
	if !c.StartedAt.IsZero() {
		out.System.AppUptimeS = max(0, int64(now.Sub(c.StartedAt).Seconds()))
	}
	return out
}
