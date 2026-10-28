package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

//nolint:gocyclo // one sequential flow keeps endpoint assertions together
func TestDiagnosticsEndpoints(t *testing.T) {
	_, deps := newEnv(t)
	h := Router(deps)

	sys := do(t, h, http.MethodGet, "/api/admin/diagnostics/system", nil, nil)
	if sys.Code != http.StatusOK {
		t.Fatalf("system status = %d: %s", sys.Code, sys.Body.String())
	}
	body := decodeMap(t, sys)
	if body["goVersion"] == nil || body["goos"] == nil {
		t.Errorf("system body = %v", body)
	}

	metrics := do(t, h, http.MethodGet, "/api/admin/diagnostics/metrics", nil, nil)
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", metrics.Code)
	}
	if got := decodeMap(t, metrics); got["at"] == nil {
		t.Errorf("metrics body = %v", got)
	}

	bench := do(t, h, http.MethodGet, "/api/admin/diagnostics/benchmarks?window=24h", nil, nil)
	if bench.Code != http.StatusOK {
		t.Fatalf("benchmarks status = %d", bench.Code)
	}
	if got := decodeMap(t, bench); got["window"] != "24h" {
		t.Errorf("benchmark window = %v", got["window"])
	}

	probe := do(t, h, http.MethodPost, "/api/admin/diagnostics/probe", nil, nil)
	if probe.Code != http.StatusOK {
		t.Fatalf("probe status = %d", probe.Code)
	}
	if got := decodeMap(t, probe); got["hardware"] == nil {
		t.Errorf("probe body = %v", got)
	}

	report := do(t, h, http.MethodPost, "/api/admin/diagnostics/report", nil, nil)
	if report.Code != http.StatusOK || !strings.Contains(report.Body.String(), "### Quipster diagnostics") {
		t.Errorf("report status=%d body=%q", report.Code, report.Body.String())
	}
	if ct := report.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Errorf("report content type = %q", ct)
	}

	bundle := do(t, h, http.MethodGet, "/api/admin/diagnostics/bundle", nil, nil)
	if bundle.Code != http.StatusOK {
		t.Fatalf("bundle status = %d", bundle.Code)
	}
	if ct := bundle.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("bundle content type = %q", ct)
	}
	if !strings.Contains(bundle.Header().Get("Content-Disposition"), "quipster-diagnostics-") {
		t.Errorf("bundle disposition = %q", bundle.Header().Get("Content-Disposition"))
	}
	if !strings.HasPrefix(bundle.Body.String(), "PK") {
		t.Error("bundle is not a zip archive")
	}

	logs := do(t, h, http.MethodGet, "/api/admin/logs?limit=10", nil, nil)
	if logs.Code != http.StatusOK {
		t.Fatalf("logs status = %d", logs.Code)
	}
	if got := decodeMap(t, logs); got["lines"] == nil {
		t.Errorf("logs body = %v", got)
	}
}

func TestDiagnosticsStream(t *testing.T) {
	_, deps := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics/stream", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	deps.Diagnostics.StreamHandler()(rr, req)
	if !strings.Contains(rr.Body.String(), "event: tick") {
		t.Errorf("stream body = %q", rr.Body.String())
	}
}

func TestDiagnosticsUnavailable(t *testing.T) {
	deps := Deps{}
	h := Router(deps)
	for _, target := range []string{
		"/api/admin/diagnostics/system",
		"/api/admin/diagnostics/metrics",
		"/api/admin/diagnostics/benchmarks",
		"/api/admin/diagnostics/stream",
	} {
		if rr := do(t, h, http.MethodGet, target, nil, nil); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", target, rr.Code)
		}
	}
	for _, target := range []string{
		"/api/admin/diagnostics/probe",
		"/api/admin/diagnostics/report",
	} {
		if rr := do(t, h, http.MethodPost, target, nil, nil); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", target, rr.Code)
		}
	}
	if rr := do(t, h, http.MethodGet, "/api/admin/diagnostics/bundle", nil, nil); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("bundle status = %d, want 503", rr.Code)
	}
	// Logs are nil-safe and report an empty list instead of failing.
	if rr := do(t, h, http.MethodGet, "/api/admin/logs", nil, nil); rr.Code != http.StatusOK {
		t.Errorf("logs status = %d, want 200", rr.Code)
	}
}
