package api

import (
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"quipster/internal/clips"
	"quipster/internal/diagnostics"
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/nodes"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
)

// Deps bundles the collaborators and settings the HTTP router needs.
// Handlers receive it by value; every field is a shared service or
// read-only configuration.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Deps struct {
	WebFS      fs.FS
	St         *store.Store
	Search     *search.Service
	Indexer    *indexer.Indexer
	Clips      *clips.Service
	Thumbs     *thumbs.Service
	Events     *events.Collector
	Runner     *jobs.Runner
	ScanTicker func() // enqueues a scan job if none active; called for POST /scan
	MediaDir   string
	SubsDir    string
	APIToken   string
	// CacheMaxBytes is the configured clip-cache quota from CACHE_MAX_GB;
	// zero means unlimited and is reported as such by the admin config.
	CacheMaxBytes int64
	Diagnostics   *diagnostics.Collector
	MCP           http.Handler   // Model Context Protocol endpoint at /mcp; nil disables it
	Nodes         *nodes.Service // nil disables the worker-node endpoints
}

// streamingPaths are long-lived server-sent event endpoints. The request
// timeout middleware must not wrap them: it would sever every stream at the
// deadline, forcing browsers to reconnect and briefly showing stale state.
var streamingPaths = map[string]struct{}{
	"/api/admin/events":             {},
	"/api/admin/diagnostics/stream": {},
}

// timeoutExceptStreams applies d to ordinary requests but leaves streaming
// endpoints open for the lifetime of the client connection.
func timeoutExceptStreams(d time.Duration) func(http.Handler) http.Handler {
	timed := middleware.Timeout(d)
	return func(next http.Handler) http.Handler {
		limited := timed(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := streamingPaths[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			limited.ServeHTTP(w, r)
		})
	}
}

// Router builds the application HTTP handler: standard middleware, public
// read endpoints, token-guarded admin endpoints and, when a web filesystem is
// configured, the SPA fallback. middleware.RealIP is deliberately omitted
// because it is deprecated and spoofable via client-controlled headers.
func Router(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(timeoutExceptStreams(5 * time.Minute))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, req)
		})
	})

	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "meiliUp": d.Indexer.Healthy(), "semantic": d.Indexer.EmbedderEnabled()})
	})

	// Model Context Protocol endpoint for agent clients. It performs its own
	// scoped-token auth, so it mounts before the public REST routes with the
	// shared request-id/logging/recoverer middleware still applied.
	if d.MCP != nil {
		r.Handle("/mcp", d.MCP)
	}

	// Public read API
	r.Get("/api/suggest", d.handleSuggest)
	r.Get("/api/search", d.handleSearch)
	r.Get("/api/examples", d.handleExamples)
	r.Get("/api/media", d.handleMediaList)
	r.Get("/api/media/{id}", d.handleMedia)
	r.Get("/api/thumb/{mediaId}", d.handleThumb)
	r.Get("/api/chunks/{id}", d.handleChunk)
	r.Get("/api/quotes", d.handleQuotes)
	r.Get("/api/clips/stream", d.handleClipStream)

	// Admin (token-guarded when configured)
	r.Group(func(r chi.Router) {
		if d.APIToken != "" {
			r.Use(d.tokenAuth)
		}
		r.Post("/api/admin/scan", d.handleScan)
		r.Get("/api/admin/config", d.handleConfigGet)
		r.Put("/api/admin/config", d.handleConfigPut)
		r.Post("/api/admin/reindex", d.handleReindex)
		r.Post("/api/admin/reprobe", d.handleReprobe)
		r.Post("/api/admin/media/{id}/restore", d.handleMediaRestore)
		r.Post("/api/admin/queue/pause", d.handleQueuePause)
		r.Post("/api/admin/queue/resume", d.handleQueueResume)
		r.Post("/api/admin/queue/clear", d.handleQueueClear)
		r.Post("/api/admin/queue/stop", d.handleQueueStop)
		r.Get("/api/admin/status", d.handleStatus)
		r.Get("/api/admin/events", d.Events.Handler())
		r.Get("/api/admin/jobs", d.handleJobs)
		r.Get("/api/admin/diagnostics/system", d.handleDiagnosticsSystem)
		r.Get("/api/admin/diagnostics/metrics", d.handleDiagnosticsMetrics)
		r.Get("/api/admin/diagnostics/benchmarks", d.handleDiagnosticsBenchmarks)
		r.Get("/api/admin/diagnostics/media-optimization", d.handleDiagnosticsMediaOptimization)
		r.Get("/api/admin/diagnostics/stream", d.handleDiagnosticsStream)
		r.Post("/api/admin/diagnostics/probe", d.handleDiagnosticsProbe)
		r.Get("/api/admin/diagnostics/bundle", d.handleDiagnosticsBundle)
		r.Post("/api/admin/diagnostics/report", d.handleDiagnosticsReport)
		r.Get("/api/admin/logs", d.handleLogs)
	})

	// Worker-node cluster endpoints. Registration is open (approval gates
	// access); everything else requires the node token issued on approval.
	if d.Nodes != nil {
		r.Post("/api/nodes/register", d.handleNodeRegister)
		r.Get("/api/nodes/{id}/state", d.handleNodeState)
		r.Group(func(r chi.Router) {
			r.Use(d.nodeAuth)
			r.Post("/api/nodes/claim", d.handleNodeClaim)
			r.Post("/api/nodes/heartbeat", d.handleNodeHeartbeat)
			r.Post("/api/nodes/jobs/{id}/complete", d.handleNodeComplete)
			r.Post("/api/nodes/jobs/{id}/result", d.handleNodeResult)
		})
		// Node administration joins the token-guarded admin surface.
		r.Group(func(r chi.Router) {
			if d.APIToken != "" {
				r.Use(d.tokenAuth)
			}
			r.Get("/api/admin/nodes", d.handleNodesList)
			r.Post("/api/admin/nodes/{id}/approve", d.handleNodeApprove)
			r.Post("/api/admin/nodes/{id}/revoke", d.handleNodeRevoke)
			r.Delete("/api/admin/nodes/{id}", d.handleNodeDelete)
		})
	}

	if d.WebFS != nil {
		r.NotFound(d.spaHandler())
	}
	return r
}
