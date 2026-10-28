package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"quipster/internal/clips"
	"quipster/internal/diagnostics"
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/nodes"
	"quipster/internal/scan"
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
	WebFS       fs.FS
	St          *store.Store
	Search      *search.Service
	Indexer     *indexer.Indexer
	Clips       *clips.Service
	Thumbs      *thumbs.Service
	Events      *events.Collector
	Runner      *jobs.Runner
	ScanTicker  func() // enqueues a scan job if none active; called for POST /scan
	MediaDir    string
	SubsDir     string
	APIToken    string
	Diagnostics *diagnostics.Collector
	MCP         http.Handler   // Model Context Protocol endpoint at /mcp; nil disables it
	Nodes       *nodes.Service // nil disables the worker-node endpoints
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
	r.Use(middleware.Timeout(5 * time.Minute))
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

func (d Deps) tokenAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-API-Token")
		if tok == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				tok = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if tok != d.APIToken {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "invalid or missing API token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON serializes v as JSON. Encode failures (disconnected client or an
// unsupported value) are logged rather than aborting the handler, since the
// response is already committed.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] write json: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": msg})
}

// bestEffort evaluates a best-effort lookup: a failure is logged and the zero
// value degrades the response payload instead of failing the whole request.
func bestEffort[T any](v T, err error, what string) T {
	if err != nil {
		log.Printf("[api] %s: %v", what, err)
	}
	return v
}

// atoiQuery parses an integer query parameter, returning 0 when the value is
// absent or malformed; handlers clamp the zero value into their own ranges.
func atoiQuery(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func (d Deps) handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, map[string]any{"suggestions": []any{}})
		return
	}
	sg, err := d.Search.Suggest(q, 8)
	if err != nil {
		log.Printf("[api] suggest: %v", err)
		writeErr(w, http.StatusServiceUnavailable, "search unavailable")
		return
	}
	writeJSON(w, map[string]any{"suggestions": sg})
}

func (d Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	show := r.URL.Query().Get("show")
	page := atoiQuery(r.URL.Query().Get("page"))
	perPage := atoiQuery(r.URL.Query().Get("perPage"))
	// mode picks keyword/hybrid/semantic retrieval; blank or unknown values
	// resolve server-side (hybrid when an embedder is configured).
	mode := r.URL.Query().Get("mode")
	res, err := d.Search.Search(q, show, page, perPage, mode)
	if err != nil {
		log.Printf("[api] search: %v", err)
		writeErr(w, http.StatusServiceUnavailable, "search unavailable")
		return
	}
	writeJSON(w, res)
}

func (d Deps) handleMediaList(w http.ResponseWriter, r *http.Request) {
	page := atoiQuery(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := atoiQuery(r.URL.Query().Get("perPage"))
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	res, err := d.St.ListMedia(perPage, (page-1)*perPage, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, res)
}

func (d Deps) handleMedia(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	m, err := d.St.GetMedia(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeErr(w, http.StatusNotFound, "media not found")
		return
	}
	// Track and cue enrichment is best-effort: a failed lookup degrades the
	// detail payload rather than failing the whole media response.
	tracks, terr := d.St.TracksForMedia(id)
	cueCount, cerr := d.St.CueCount(id)
	writeJSON(w, map[string]any{
		"media":    m,
		"tracks":   bestEffort(tracks, terr, "tracks"),
		"cueCount": bestEffort(cueCount, cerr, "cue count"),
	})
}

func (d Deps) handleThumb(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "mediaId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	t, terr := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if terr != nil {
		t = 0 // absent or malformed t defaults to the video start
	}
	width, werr := strconv.Atoi(r.URL.Query().Get("w"))
	if werr != nil {
		width = 0 // absent or malformed w lets the thumbs service decide
	}
	e, err := d.Thumbs.Get(r.Context(), id, t, width)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, e.Path)
}

// handleQuotes serves real dialog lines from the indexed library for the
// landing page.
func (d Deps) handleQuotes(w http.ResponseWriter, r *http.Request) {
	limit := atoiQuery(r.URL.Query().Get("limit"))
	quotes, err := d.St.RandomQuotes(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(quotes))
	for _, q := range quotes {
		out = append(out, map[string]any{
			"chunkId":    indexer.DocID(q.MediaFileID, q.StartCueIdx, q.EndCueIdx),
			"text":       q.Text,
			"mediaId":    q.MediaFileID,
			"mediaTitle": q.MediaTitle,
			"showTitle":  q.ShowTitle,
			"season":     q.Season,
			"episode":    q.Episode,
			"year":       q.Year,
			"startSec":   q.StartSec,
			"endSec":     q.EndSec,
			"thumbUrl":   fmt.Sprintf("/api/thumb/%d?t=%.1f&w=320", q.MediaFileID, q.StartSec),
		})
	}
	writeJSON(w, map[string]any{"quotes": out})
}

func (d Deps) handleChunk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mediaID, startIdx, endIdx, ok := parseChunkID(id)
	if !ok {
		writeErr(w, http.StatusBadRequest, "chunk id must be mediaId_startCueIdx-endCueIdx")
		return
	}
	ch, err := d.St.GetChunkByKey(mediaID, startIdx, endIdx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, "chunk not found")
		return
	}
	m, err := d.St.GetMedia(mediaID)
	if err != nil || m == nil {
		writeErr(w, http.StatusNotFound, "media not found")
		return
	}
	span, before, after, err := d.St.CuesInRange(mediaID, startIdx, endIdx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"chunk":         ch,
		"media":         m,
		"cues":          search.CueDTOs(span),
		"contextBefore": search.CueDTOs(before),
		"contextAfter":  search.CueDTOs(after),
	})
}

func parseChunkID(id string) (int64, int, int, bool) {
	parts := strings.SplitN(id, "_", 2)
	if len(parts) != 2 {
		return 0, 0, 0, false
	}
	mediaID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	parts2 := strings.SplitN(parts[1], "-", 2)
	if len(parts2) != 2 {
		return 0, 0, 0, false
	}
	s, err1 := strconv.Atoi(parts2[0])
	e, err2 := strconv.Atoi(parts2[1])
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	return mediaID, s, e, true
}

// parseClipStreamRequest builds a clips.Request from the stream endpoint's
// query parameters, applying the service defaults for absent mode and padMs.
func parseClipStreamRequest(q url.Values, defaultPadMs int) (clips.Request, error) {
	mediaID, err := strconv.ParseInt(q.Get("mediaId"), 10, 64)
	if err != nil {
		return clips.Request{}, errors.New("mediaId required")
	}
	start, err1 := strconv.ParseFloat(q.Get("start"), 64)
	end, err2 := strconv.ParseFloat(q.Get("end"), 64)
	if err1 != nil || err2 != nil {
		return clips.Request{}, errors.New("start and end required")
	}
	mode := q.Get("mode")
	if mode == "" {
		mode = "fast"
	}
	padMs := defaultPadMs
	if p := q.Get("padMs"); p != "" {
		padMs, err = strconv.Atoi(p)
		if err != nil || padMs < 0 || padMs > 5000 {
			return clips.Request{}, errors.New("padMs out of range")
		}
	}
	return clips.Request{MediaID: mediaID, Start: start, End: end, PadMs: padMs, Mode: mode}, nil
}

func (d Deps) handleClipStream(w http.ResponseWriter, r *http.Request) {
	req, err := parseClipStreamRequest(r.URL.Query(), d.Clips.Config().DefaultPadMs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, _, verr := d.Clips.Validate(req)
	if verr != nil {
		writeErr(w, http.StatusBadRequest, verr.Error())
		return
	}
	key := d.Clips.CacheKey(req)

	if e, cerr := d.Clips.Cached(key); cerr != nil {
		writeErr(w, http.StatusInternalServerError, cerr.Error())
		return
	} else if e != nil {
		d.Clips.Touch(key)
		d.serveClip(w, r, e.Path, m, req)
		return
	}

	// Long accurate cuts run as background jobs with progress.
	span := req.End - req.Start
	if req.Mode == "accurate" && span > d.Clips.Config().LongCutSec {
		jobID, newlyQueued, qerr := d.St.EnqueueUniqueJob("clip", clips.AsyncJobPayload(req), 1)
		if qerr != nil {
			writeErr(w, http.StatusInternalServerError, qerr.Error())
			return
		}
		if newlyQueued || jobID > 0 { // always return accepted if it's async
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"status": "queued", "jobId": jobID, "cacheKey": key})
			return
		}
	}

	path, _, perr := d.Clips.Produce(r.Context(), req)
	if perr != nil {
		if r.Context().Err() != nil {
			return
		}
		log.Printf("[api] clip produce: %v", perr)
		writeErr(w, http.StatusInternalServerError, "clip render failed")
		return
	}
	d.serveClip(w, r, path, m, req)
}

func (d Deps) serveClip(w http.ResponseWriter, r *http.Request, path string, m *store.MediaFile, req clips.Request) {
	f, err := os.Open(path) //nolint:gosec // path is a clips-service cache entry (cache dir + hash key), not raw user input
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "clip file unreadable")
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("[api] close clip file: %v", cerr)
		}
	}()
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	ct := "video/mp4"
	if ext == "mkv" {
		ct = "video/x-matroska"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	if r.URL.Query().Get("download") == "1" {
		name := clipName(m, req) + "." + ext
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat failed")
		return
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

func clipName(m *store.MediaFile, req clips.Request) string {
	title := m.Title
	if m.ShowTitle != "" {
		title = m.ShowTitle
		if m.Season > 0 && m.Episode > 0 {
			title += fmt.Sprintf(" S%02dE%02d", m.Season, m.Episode)
		}
	}
	if title == "" {
		title = fmt.Sprintf("clip-%d", m.ID)
	}
	var b strings.Builder
	for _, rn := range title {
		switch {
		case rn >= 'a' && rn <= 'z', rn >= 'A' && rn <= 'Z', rn >= '0' && rn <= '9', rn == ' ', rn == '-', rn == '_':
			b.WriteRune(rn)
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = fmt.Sprintf("clip-%d", m.ID)
	}
	return fmt.Sprintf("%s [%d-%d]", name, int(req.Start*1000), int(req.End*1000))
}

// handleScan enqueues a scan of the configured library folders. An explicit
// ?root=subfolder scans just that folder without pruning outside it.
func (d Deps) handleScan(w http.ResponseWriter, r *http.Request) {
	if active, err := d.St.HasActiveScanJob(); err == nil && active {
		writeJSON(w, map[string]any{"status": "already-running"})
		return
	}
	payload := map[string]any{}
	if root := strings.TrimSpace(r.URL.Query().Get("root")); root != "" {
		roots, err := scan.CleanRoots([]string{root})
		if err != nil || len(roots) == 0 {
			writeErr(w, http.StatusBadRequest, "invalid scan root")
			return
		}
		payload["roots"] = roots
		payload["prune"] = false
	} else {
		roots, err := d.St.GetLibraryPaths()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		payload["roots"] = roots
		payload["prune"] = true
	}
	pb, _ := json.Marshal(payload) //nolint:errcheck // the map holds only strings and a bool, so marshaling cannot fail
	if _, err := d.St.EnqueueJob("scan", string(pb), 3); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": "started"})
}

// handleConfigGet returns the media root, configured folders and the top-level
// folders available to choose from.
func (d Deps) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	folders, err := d.St.GetLibraryPaths()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if folders == nil {
		folders = []string{}
	}
	available := []string{}
	if entries, err := os.ReadDir(d.MediaDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				available = append(available, e.Name())
			}
		}
	}
	writeJSON(w, map[string]any{
		"mediaDir":  d.MediaDir,
		"folders":   folders,
		"available": available,
	})
}

// handleConfigPut validates and persists the library folders to scan.
func (d Deps) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folders []string `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	folders, err := scan.CleanRoots(body.Folders)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, rel := range folders {
		full := filepath.Join(d.MediaDir, filepath.FromSlash(rel))
		if info, err := os.Stat(full); err != nil || !info.IsDir() {
			writeErr(w, http.StatusBadRequest, "folder not found: "+rel)
			return
		}
	}
	if err := d.St.SetLibraryPaths(folders); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"folders": folders})
}

func (d Deps) handleReindex(w http.ResponseWriter, _ *http.Request) {
	n, err := d.Indexer.ReindexAll()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"enqueued": n})
}

func (d Deps) handleQueuePause(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"paused": true})
}

func (d Deps) handleQueueResume(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"paused": false})
}

// handleQueueClear drops queued jobs; running jobs keep running unless paused.
func (d Deps) handleQueueClear(w http.ResponseWriter, _ *http.Request) {
	n, err := d.St.ClearQueuedJobs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"cleared": n})
}

// handleReprobe re-runs probe (metadata/audio/subtitle selection) for media
// that already finished, e.g. after a pipeline improvement.
func (d Deps) handleReprobe(w http.ResponseWriter, _ *http.Request) {
	ids, err := d.St.MediaIDsByStatus("indexed", "no_subs", "error")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := 0
	for _, id := range ids {
		payload, _ := json.Marshal(map[string]any{"mediaId": id}) //nolint:errcheck // the map holds only an int64, so marshaling cannot fail
		if _, err := d.St.EnqueueJob("probe", string(payload), 2); err == nil {
			n++
		}
	}
	writeJSON(w, map[string]any{"enqueued": n})
}

// handleQueueStop pauses the queue, aborts in-flight jobs and clears queued work.
func (d Deps) handleQueueStop(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	canceled := 0
	if d.Runner != nil {
		canceled = d.Runner.CancelActive()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := d.St.JobStats()
		if err != nil || stats.Running == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cleared, err := d.St.ClearQueuedJobs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The wire key keeps the historical spelling; the web UI reads it.
	writeJSON(w, map[string]any{"paused": true, "cancelled": canceled, "cleared": cleared}) //nolint:misspell // legacy API key consumed by web/src/api.ts
}

func (d Deps) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, d.Events.Collect())
}

func (d Deps) handleJobs(w http.ResponseWriter, r *http.Request) {
	limit := atoiQuery(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 50
	}
	jobs, err := d.St.ListJobs(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := d.St.JobStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"jobs": jobs, "stats": st})
}

// spaHandler serves the embedded SPA with index.html fallback.
func (d Deps) spaHandler() http.HandlerFunc {
	fileServer := http.FileServer(http.FS(d.WebFS))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(d.WebFS, p); err != nil {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		// Go's MIME table has no entry for .webmanifest, and a PWA manifest
		// must be served as application/manifest+json for browsers to accept it.
		if strings.HasSuffix(p, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		fileServer.ServeHTTP(w, r)
	}
}
