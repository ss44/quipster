package api

import (
	"log"
	"net/http"
	"time"

	"quipster/internal/diagnostics"
)

// diagnosticsUnavailable reports whether diagnostics are wired. Keeping the
// handlers nil-safe lets tests and role-split deployments construct a router
// without the collector.
func (d Deps) diagnosticsUnavailable(w http.ResponseWriter) bool {
	if d.Diagnostics == nil {
		writeErr(w, http.StatusServiceUnavailable, "diagnostics unavailable")
		return true
	}
	return false
}

// handleDiagnosticsSystem returns the cached environment report.
func (d Deps) handleDiagnosticsSystem(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	writeJSON(w, d.Diagnostics.System(r.Context()))
}

// handleDiagnosticsMetrics returns the current resource gauge snapshot.
func (d Deps) handleDiagnosticsMetrics(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	writeJSON(w, d.Diagnostics.Gauge(r.Context()))
}

// handleDiagnosticsBenchmarks aggregates completed job durations.
func (d Deps) handleDiagnosticsBenchmarks(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	writeJSON(w, d.Diagnostics.Benchmarks(r.URL.Query().Get("window")))
}

// handleDiagnosticsStream streams gauge snapshots over SSE.
func (d Deps) handleDiagnosticsStream(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	d.Diagnostics.StreamHandler().ServeHTTP(w, r)
}

// handleDiagnosticsProbe re-detects hardware and returns the new status.
func (d Deps) handleDiagnosticsProbe(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	writeJSON(w, map[string]any{"hardware": d.Diagnostics.RefreshHardware(r.Context())})
}

// handleDiagnosticsReport returns a Markdown report for bug reports.
func (d Deps) handleDiagnosticsReport(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := w.Write([]byte(d.Diagnostics.Report(r.Context()))); err != nil {
		log.Printf("[api] diagnostics report: %v", err)
	}
}

// handleDiagnosticsBundle streams a ZIP with redacted diagnostics.
func (d Deps) handleDiagnosticsBundle(w http.ResponseWriter, r *http.Request) {
	if d.diagnosticsUnavailable(w) {
		return
	}
	opts := diagnostics.BundleOptions{IncludePaths: r.URL.Query().Get("paths") == "1"}
	data, err := d.Diagnostics.Bundle(r.Context(), opts)
	if err != nil {
		log.Printf("[api] diagnostics bundle: %v", err)
		writeErr(w, http.StatusInternalServerError, "failed to build diagnostic bundle")
		return
	}
	name := "quipster-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if _, err := w.Write(data); err != nil {
		log.Printf("[api] diagnostics bundle write: %v", err)
	}
}

// handleLogs returns recent application log lines from the in-memory buffer.
func (d Deps) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := atoiQuery(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 5000 {
		limit = 500
	}
	lines := d.Diagnostics.Logs(limit)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, map[string]any{"lines": lines, "count": len(lines)})
}
