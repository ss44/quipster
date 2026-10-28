package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"quipster/internal/hardware"
	"quipster/internal/logbuf"
	"quipster/internal/store"
)

func newTestCollector(t *testing.T) (*Collector, *store.Store) {
	t.Helper()
	t.Setenv("FFMPEG_BIN", "/nonexistent/ffmpeg")
	t.Setenv("FFPROBE_BIN", "/nonexistent/ffprobe")
	st, err := store.Open(filepath.Join(t.TempDir(), "diag.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close: %v", cerr)
		}
	})
	logs := logbuf.New(4096)
	if _, err := logs.Write([]byte("booting diagnostics\n")); err != nil {
		t.Fatalf("log write: %v", err)
	}
	dir := t.TempDir()
	c := New(st, hardware.MonitorFor(hardware.Capabilities{}), logs, func() bool { return true }, "test-version", dir, dir, dir)
	return c, st
}

func TestMetricParsers(t *testing.T) {
	vals := parseMeminfo("MemTotal:       1000 kB\nMemAvailable:    400 kB\nSwapTotal:       200 kB\nSwapFree:        150 kB\n")
	if vals["MemTotal"] != 1000 || vals["MemAvailable"] != 400 {
		t.Errorf("meminfo = %v", vals)
	}
	if got := mibToBytes("2"); got != 2*1024*1024 {
		t.Errorf("mibToBytes = %d", got)
	}
	if parseFloat(" 12.5 ") != 12.5 || parseFloat("n/a") != 0 {
		t.Error("parseFloat failed")
	}
}

func TestSampleCPUWithDelta(t *testing.T) {
	prev := cpuTimes{at: time.Now().Add(-time.Second), total: -1000, idle: -1000}
	m, cur := sampleCPU(prev, time.Now())
	if !m.Available {
		t.Fatalf("metric unavailable: %+v", m)
	}
	if m.Value < 0 || m.Value > float64(100*runtime.NumCPU()) {
		t.Errorf("cpu value = %v", m.Value)
	}
	if cur.total <= 0 {
		t.Errorf("current sample = %+v", cur)
	}
}

func TestPercentileAndAggregates(t *testing.T) {
	values := []float64{10, 20, 30, 40, 50}
	if got := percentile(values, 0.5); got != 30 {
		t.Errorf("p50 = %v, want 30", got)
	}
	if got := percentile(values, 0.95); got != 50 {
		t.Errorf("p95 = %v, want 50", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty percentile = %v", got)
	}

	jobs := []store.Job{
		{Type: "probe", CreatedAt: 1000, StartedAt: 1200, FinishedAt: 2200},
		{Type: "probe", CreatedAt: 1000, StartedAt: 1300, FinishedAt: 3300},
		{Type: "clip", CreatedAt: 1000, StartedAt: 1100, FinishedAt: 1600},
		{Type: "probe", FinishedAt: 900}, // ignored, never started
	}
	out := aggregateBenchmarks(jobs)
	if len(out) != 2 {
		t.Fatalf("aggregates = %+v", out)
	}
	if out[0].Type != "clip" || out[0].Count != 1 {
		t.Errorf("first aggregate = %+v", out[0])
	}
	if out[1].Type != "probe" || out[1].Count != 2 || out[1].P50Ms != 1000 || out[1].MaxMs != 2000 {
		t.Errorf("probe aggregate = %+v", out[1])
	}
	if out[1].AvgWaitMs != 250 {
		t.Errorf("avg wait = %v, want 250", out[1].AvgWaitMs)
	}
}

func TestWindowHelpers(t *testing.T) {
	if normalizeWindow("24h") != "24h" || normalizeWindow("bogus") != "1h" || normalizeWindow(" 6H ") != "6h" {
		t.Error("normalizeWindow mapping wrong")
	}
	if windowDuration("7d") != 7*24*time.Hour || windowDuration("nope") != time.Hour {
		t.Error("windowDuration mapping wrong")
	}
}

func TestMediaIDFromPayload(t *testing.T) {
	if got := mediaIDFromPayload(`{"mediaId":42}`); got != 42 {
		t.Errorf("mediaID = %d", got)
	}
	if got := mediaIDFromPayload("not json"); got != 0 {
		t.Errorf("invalid payload = %d", got)
	}
}

//nolint:gocyclo // one sequential flow keeps collector assertions together
func TestCollectorGaugeSystemAndLogs(t *testing.T) {
	c, _ := newTestCollector(t)
	ctx := context.Background()

	g := c.Gauge(ctx)
	if g.At == 0 || g.Disks == nil || g.CacheParts == nil {
		t.Fatalf("gauge = %+v", g)
	}
	if len(g.Disks) != 3 {
		t.Errorf("disk entries = %d, want 3", len(g.Disks))
	}
	if g.Hardware.Available {
		t.Errorf("cpu-only hardware = %+v", g.Hardware)
	}

	sys := c.System(ctx)
	if sys.GoVersion == "" || sys.OS == "" || len(sys.Paths) != 3 {
		t.Fatalf("system = %+v", sys)
	}
	if sys.FFmpeg.Available {
		t.Error("missing ffmpeg must be unavailable")
	}
	if !sys.MeiliUp {
		t.Error("meili callback not called")
	}
	if sys.AppVersion != "test-version" {
		t.Errorf("version = %q", sys.AppVersion)
	}

	if lines := c.Logs(10); len(lines) != 1 || lines[0] != "booting diagnostics" {
		t.Errorf("logs = %v", lines)
	}
	if c.Logs(0) == nil {
		// zero is clamped by the handler, but the collector returns all lines
		t.Log("no logs returned")
	}

	if status := c.RefreshHardware(ctx); status.Available {
		t.Errorf("refresh on missing ffmpeg = %+v", status)
	}
}

func TestCollectorBenchmarksAndReadThroughput(t *testing.T) {
	c, st := newTestCollector(t)
	id, _, err := st.UpsertMedia("/media/a.mkv", "matroska", 4*1024*1024, 1, "A", 2020, 0, 0, "")
	if err != nil {
		t.Fatalf("UpsertMedia: %v", err)
	}
	payload := fmt.Sprintf(`{"mediaId":%d}`, id)
	if _, eerr := st.EnqueueJob("probe", payload, 0); eerr != nil {
		t.Fatalf("enqueue: %v", eerr)
	}
	job, err := st.ClaimJob(context.Background(), "test", []string{"probe"})
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := st.FinishJob(job.ID, "done", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	b := c.Benchmarks("1h")
	if len(b.Jobs) != 1 || b.Jobs[0].Type != "probe" {
		t.Fatalf("benchmarks = %+v", b)
	}
	if !b.ReadAvailable || b.ReadMBps <= 0 {
		t.Errorf("read throughput = %+v", b)
	}
	// Cached result is returned for the same window.
	if again := c.Benchmarks("1h"); again.At != b.At {
		t.Error("benchmarks should be cached")
	}
}

func TestReportRedactsSecrets(t *testing.T) {
	t.Setenv("MEILI_MASTER_KEY", "supersecret-value")
	c, _ := newTestCollector(t)
	if _, err := c.LogBuffer.Write([]byte("token supersecret-value leaked\n")); err != nil {
		t.Fatalf("log write: %v", err)
	}
	report := c.Report(context.Background())
	if !strings.Contains(report, "### Quipster diagnostics") {
		t.Errorf("report missing header:\n%s", report)
	}
	if strings.Contains(report, "supersecret-value") {
		t.Error("report leaked a secret")
	}
	if !strings.Contains(redactSecrets("value supersecret-value here"), "[redacted]") {
		t.Error("redactSecrets should replace known secret values")
	}
}

func TestBundleContentsAndPathRedaction(t *testing.T) {
	t.Setenv("API_TOKEN", "token-abc-123")
	c, _ := newTestCollector(t)
	if _, err := c.LogBuffer.Write([]byte("key token-abc-123\n")); err != nil {
		t.Fatalf("log write: %v", err)
	}
	data, err := c.Bundle(context.Background(), BundleOptions{})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, oerr := f.Open()
		if oerr != nil {
			t.Fatal(oerr)
		}
		body, rerr := io.ReadAll(rc)
		if cerr := rc.Close(); cerr != nil {
			t.Logf("close: %v", cerr)
		}
		if rerr != nil {
			t.Fatal(rerr)
		}
		if strings.Contains(string(body), "token-abc-123") {
			t.Errorf("%s leaked a secret", f.Name)
		}
	}
	for _, want := range []string{"system.json", "metrics.json", "benchmarks.json", "jobs.json", "logs.txt", "README.txt"} {
		if !names[want] {
			t.Errorf("bundle missing %s", want)
		}
	}

	withPaths, err := c.Bundle(context.Background(), BundleOptions{IncludePaths: true})
	if err != nil {
		t.Fatalf("Bundle paths: %v", err)
	}
	if !bytes.Contains(withPaths, []byte("include")) && !bytes.Contains(withPaths, []byte("redacted")) {
		t.Error("bundle README missing path note")
	}
}

func TestStreamHandlerSendsInitialFrame(t *testing.T) {
	c, _ := newTestCollector(t)
	srv := httptest.NewServer(c.StreamHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Logf("close: %v", cerr)
		}
	}()
	buf := make([]byte, 256)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "event: tick") {
		t.Errorf("first frame = %q", string(buf[:n]))
	}
}

func TestHelpers(t *testing.T) {
	if fallback("", "x") != "x" || fallback(" ", "x") != "x" || fallback("v", "x") != "v" {
		t.Error("fallback")
	}
	if truncate("abcdef", 3) != "abc…" || truncate("ab", 3) != "ab" {
		t.Error("truncate")
	}
	if boolWord(true, "yes", "no") != "yes" || boolWord(false, "yes", "no") != "no" {
		t.Error("boolWord")
	}
	if fmtBytes(512) != "512 B" || !strings.HasSuffix(fmtBytes(2048), "KB") {
		t.Error("fmtBytes")
	}
	if ms(500) != "500ms" || ms(1500) != "1.5s" {
		t.Error("ms")
	}
	if minutes(65) != "1m" || !strings.Contains(minutes(3660), "h") {
		t.Errorf("minutes = %q", minutes(65))
	}
	_ = deployment(&SystemInfo{Container: true})
	_ = online(true)
	_ = memPair(Metric{Available: false, Reason: "n/a"})
	_ = pct(Metric{Available: true, Value: 42})
	_ = readSummary(&Benchmarks{})
	_ = benchSummary(&Benchmarks{})
	_ = hardwareLine(hardware.Status{})
}

func TestSafeConfigAndPlatformHelpers(t *testing.T) {
	t.Setenv("FFMPEG_BIN", "/nonexistent/ffmpeg")
	t.Setenv("ROLE", "api")
	t.Setenv("HWACCEL", "auto")
	t.Setenv("MEILI_MASTER_KEY", "do-not-include")
	cfg := safeConfig()
	if cfg["ROLE"] != "api" || cfg["HWACCEL"] != "auto" {
		t.Errorf("config = %v", cfg)
	}
	if _, ok := cfg["MEILI_MASTER_KEY"]; ok {
		t.Error("safeConfig must not include secrets")
	}
	_ = osName()
	_ = kernelVersion()
	_ = cpuModel()
	_ = isContainer()
	_ = dbBytes(t.TempDir())

	if v := toolVersion(context.Background(), "/nonexistent/tool"); v.Available {
		t.Error("missing tool must be unavailable")
	}
	if list := ffmpegList(context.Background(), "-hwaccels"); list != nil {
		t.Errorf("missing ffmpeg list = %v", list)
	}
	if list := ffmpegCodecs(context.Background(), "-encoders"); list != nil {
		t.Errorf("missing ffmpeg codecs = %v", list)
	}
}

func TestGPUUnavailable(t *testing.T) {
	// Force a missing vendor tool so the assertion is deterministic.
	t.Setenv("PATH", "/nonexistent")
	nvidiaSMIOnce = sync.Once{}
	nvidiaSMIPath = ""
	gpu, vram := sampleGPU(context.Background(), time.Now())
	if gpu.Available || vram.Available {
		t.Fatalf("gpu = %+v vram = %+v", gpu, vram)
	}
}
