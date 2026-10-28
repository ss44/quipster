package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"quipster/internal/hardware"
	"quipster/internal/store"
)

// BundleOptions controls optional sensitive content in an exported bundle.
type BundleOptions struct {
	IncludePaths bool
}

// Report renders a compact Markdown diagnostics report suitable for pasting
// into a bug report. Secrets are redacted; media paths are omitted.
func (c *Collector) Report(ctx context.Context) string {
	sys := c.System(ctx)
	gauge := c.Gauge(ctx)
	bench := c.Benchmarks("1h")

	var b strings.Builder
	b.WriteString("### Quipster diagnostics\n")
	fmt.Fprintf(&b, "Version: %s (%s, %s/%s)\n", fallback(sys.AppVersion, "dev"), sys.GoVersion, sys.OS, sys.Arch)
	fmt.Fprintf(&b, "Deployment: %s · kernel %s · uptime %s\n", deployment(sys), fallback(sys.Kernel, "unknown"), minutes(sys.AppUptimeS))
	fmt.Fprintf(&b, "Hardware: %s\n", hardwareLine(sys.Hardware))
	fmt.Fprintf(&b, "Runtime: %d HW renders · %d CPU renders · %d fallbacks (last: %s)\n",
		sys.Hardware.Stats.HWEncodes, sys.Hardware.Stats.CPURenders, sys.Hardware.Stats.Fallbacks,
		fallback(sys.Hardware.Stats.LastFallbackNote, "none"))
	fmt.Fprintf(&b, "Resources: CPU %s (proc %s) · RAM %s · VRAM %s · GPU %s\n",
		pct(gauge.CPU), pct(gauge.ProcessCPU), memPair(gauge.RAM), memPair(gauge.VRAM), pct(gauge.GPU))
	fmt.Fprintf(&b, "Performance (1h): %s · throughput %.2f jobs/min · read %s\n",
		benchSummary(bench), bench.ThroughputPerMin, readSummary(bench))
	if c.St != nil {
		if counts, err := c.St.MediaStatusCounts(); err == nil {
			var total int64
			for _, n := range counts {
				total += n
			}
			fmt.Fprintf(&b, "Library: %d files · %d indexed · %d errors\n", total, counts["indexed"], counts["error"])
		}
	}
	fmt.Fprintf(&b, "Search: %s\n", online(sys.MeiliUp))
	fmt.Fprintf(&b, "Cache: %s\n", fmtBytes(gauge.CacheBytes))

	if errs := c.recentErrors(); len(errs) > 0 {
		b.WriteString("Last errors:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  - %s: %s\n", e.Type, truncate(e.Error, 160))
		}
	}
	return redactSecrets(b.String())
}

// Bundle builds a ZIP containing the system report, metrics, recent jobs,
// logs, and a short description. Paths are redacted unless explicitly allowed.
func (c *Collector) Bundle(ctx context.Context, opts BundleOptions) ([]byte, error) {
	sys := *c.System(ctx)
	if !opts.IncludePaths {
		for i := range sys.Paths {
			sys.Paths[i].Path = "<redacted>"
		}
	}
	gauge := c.Gauge(ctx)
	bench := c.Benchmarks("24h")

	var jobs []store.Job
	if c.St != nil {
		if list, err := c.St.ListJobs(200); err == nil {
			jobs = list
		}
	}
	if !opts.IncludePaths {
		for i := range jobs {
			jobs[i].MediaPath = "<redacted>"
		}
	}
	logs := redactLines(c.Logs(500))

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	//nolint:govet // field grouping favors readability over optimal alignment.
	files := []struct {
		name string
		v    any
	}{
		{"system.json", sys},
		{"metrics.json", gauge},
		{"benchmarks.json", bench},
		{"jobs.json", jobs},
	}
	for _, f := range files {
		data, err := json.MarshalIndent(f.v, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", f.name, err)
		}
		if err := addZipFile(zw, f.name, data); err != nil {
			return nil, err
		}
	}
	if err := addZipFile(zw, "logs.txt", []byte(strings.Join(logs, "\n"))); err != nil {
		return nil, err
	}
	readme := "Quipster diagnostic bundle\n\n" +
		"system.json: environment, hardware selection and effective settings\n" +
		"metrics.json: latest resource snapshot\n" +
		"benchmarks.json: 24h job performance aggregates\n" +
		"jobs.json: most recent 200 jobs\n" +
		"logs.txt: most recent application log lines\n\n" +
		"Secrets are redacted. Media paths are " + boolWord(opts.IncludePaths, "included", "redacted") + ".\n"
	if err := addZipFile(zw, "README.txt", []byte(readme)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// addZipFile writes one file into the archive.
func addZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// recentErrors returns up to five recent failed jobs.
func (c *Collector) recentErrors() []store.Job {
	if c.St == nil {
		return nil
	}
	jobs, err := c.St.ListJobs(100)
	if err != nil {
		return nil
	}
	out := make([]store.Job, 0, 5)
	for _, j := range jobs {
		if j.Error != "" {
			out = append(out, j)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

// redactLines applies secret redaction to every log line.
func redactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = redactSecrets(line)
	}
	return out
}

// secretValues returns non-trivial secret environment values to scrub.
func secretValues() []string {
	vals := make([]string, 0, 3)
	for _, key := range []string{"MEILI_MASTER_KEY", "API_TOKEN", "MEILI_KEY"} {
		if v := os.Getenv(key); len(strings.TrimSpace(v)) >= 4 {
			vals = append(vals, v)
		}
	}
	return vals
}

// redactSecrets replaces known secret values in text.
func redactSecrets(text string) string {
	for _, v := range secretValues() {
		text = strings.ReplaceAll(text, v, "[redacted]")
	}
	return text
}

// hardwareLine renders the hardware status for the report.
func hardwareLine(s hardware.Status) string {
	if s.Available {
		return fmt.Sprintf("HARDWARE · %s · %s · device %s · %s", strings.ToUpper(s.Backend), s.Encoder, fallback(s.Device, "default"), strings.ToUpper(s.Requested))
	}
	return fmt.Sprintf("SOFTWARE · CPU · libx264 · %s", s.Reason)
}

// deployment describes the runtime environment.
func deployment(sys *SystemInfo) string {
	if sys.Container {
		return "docker"
	}
	return "host"
}

// benchSummary renders the first few job benchmarks.
func benchSummary(b *Benchmarks) string {
	if len(b.Jobs) == 0 {
		return "no completed jobs"
	}
	parts := make([]string, 0, len(b.Jobs))
	for _, j := range b.Jobs {
		parts = append(parts, fmt.Sprintf("%s p50 %s / p95 %s", j.Type, ms(j.P50Ms), ms(j.P95Ms)))
	}
	return strings.Join(parts[:min(len(parts), 3)], " · ")
}

// readSummary renders derived read throughput.
func readSummary(b *Benchmarks) string {
	if !b.ReadAvailable {
		return "n/a (" + fallback(b.ReadReason, "no data") + ")"
	}
	return fmt.Sprintf("%.1f MB/s", b.ReadMBps)
}

// memPair renders used/total for a byte metric.
func memPair(m Metric) string {
	if !m.Available {
		return "n/a (" + fallback(m.Reason, "unavailable") + ")"
	}
	if m.Total > 0 {
		return fmt.Sprintf("%s / %s", fmtBytes(int64(m.Value)), fmtBytes(int64(m.Total)))
	}
	return fmtBytes(int64(m.Value))
}

// pct renders a percentage metric.
func pct(m Metric) string {
	if !m.Available {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", m.Value)
}

// online renders a health boolean.
func online(up bool) string {
	if up {
		return "online"
	}
	return "offline"
}

// ms formats milliseconds as a compact duration.
func ms(v float64) string {
	if v < 1000 {
		return fmt.Sprintf("%.0fms", v)
	}
	return fmt.Sprintf("%.1fs", v/1000)
}

// minutes formats seconds as a compact uptime string.
func minutes(sec int64) string {
	d := time.Duration(sec) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// fmtBytes formats bytes with binary units.
func fmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// fallback returns v or def when v is empty.
func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// truncate shortens s to n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// boolWord selects one of two words.
func boolWord(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
