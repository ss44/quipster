package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"quipster/internal/clips"
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
)

type Deps struct {
	St         *store.Store
	Search     *search.Service
	Indexer    *indexer.Indexer
	Clips      *clips.Service
	Thumbs     *thumbs.Service
	Events     *events.Collector
	MediaDir   string
	SubsDir    string
	WebFS      fs.FS
	APIToken   string
	ScanTicker func() // enqueues a scan job if none active; called for POST /scan
}

func Router(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, req)
		})
	})

	r.Get("/api/health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "meiliUp": d.Indexer.Healthy()})
	})

	// Public read API
	r.Get("/api/suggest", d.handleSuggest)
	r.Get("/api/search", d.handleSearch)
	r.Get("/api/media", d.handleMediaList)
	r.Get("/api/media/{id}", d.handleMedia)
	r.Get("/api/thumb/{mediaId}", d.handleThumb)
	r.Get("/api/chunks/{id}", d.handleChunk)
	r.Get("/api/clips/stream", d.handleClipStream)

	// Admin (token-guarded when configured)
	r.Group(func(r chi.Router) {
		if d.APIToken != "" {
			r.Use(d.tokenAuth)
		}
		r.Post("/api/admin/scan", d.handleScan)
		r.Post("/api/admin/reindex", d.handleReindex)
		r.Get("/api/admin/status", d.handleStatus)
		r.Get("/api/admin/events", d.Events.Handler())
		r.Get("/api/admin/jobs", d.handleJobs)
	})

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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": msg})
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
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	res, err := d.Search.Search(q, show, page, perPage)
	if err != nil {
		log.Printf("[api] search: %v", err)
		writeErr(w, http.StatusServiceUnavailable, "search unavailable")
		return
	}
	writeJSON(w, res)
}

func (d Deps) handleMediaList(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
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
	tracks, _ := d.St.TracksForMedia(id)
	cueCount, _ := d.St.CueCount(id)
	writeJSON(w, map[string]any{"media": m, "tracks": tracks, "cueCount": cueCount})
}

func (d Deps) handleThumb(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "mediaId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	e, err := d.Thumbs.Get(r.Context(), id, t, width)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, e.Path)
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
		"cues":          span,
		"contextBefore": before,
		"contextAfter":  after,
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

func (d Deps) handleClipStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mediaID, err := strconv.ParseInt(q.Get("mediaId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "mediaId required")
		return
	}
	start, err1 := strconv.ParseFloat(q.Get("start"), 64)
	end, err2 := strconv.ParseFloat(q.Get("end"), 64)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, "start and end required")
		return
	}
	mode := q.Get("mode")
	if mode == "" {
		mode = "fast"
	}
	padMs := d.Clips.Config().DefaultPadMs
	if p := q.Get("padMs"); p != "" {
		padMs, err = strconv.Atoi(p)
		if err != nil || padMs < 0 || padMs > 5000 {
			writeErr(w, http.StatusBadRequest, "padMs out of range")
			return
		}
	}
	req := clips.Request{MediaID: mediaID, Start: start, End: end, PadMs: padMs, Mode: mode}
	m, _, err := d.Clips.Validate(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	key := clips.CacheKey(req.MediaID, req.Start, req.End, req.Mode, req.PadMs)

	if e, err := d.Clips.Cached(key); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if e != nil {
		d.Clips.Touch(key)
		d.serveClip(w, r, e.Path, m, req)
		return
	}

	// Long accurate cuts run as background jobs with progress.
	span := req.End - req.Start
	if mode == "accurate" && span > d.Clips.Config().LongCutSec {
		jobID, queued, err := d.enqueueClipJob(req)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if queued {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"status": "queued", "jobId": jobID, "cacheKey": key})
			return
		}
	}

	path, ext, err := d.Clips.Produce(r.Context(), req)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		log.Printf("[api] clip produce: %v", err)
		writeErr(w, http.StatusInternalServerError, "clip render failed")
		return
	}
	_ = ext
	d.serveClip(w, r, path, m, req)
}

func (d Deps) enqueueClipJob(req clips.Request) (int64, bool, error) {
	payload := clips.AsyncJobPayload(req)
	jobs, err := d.St.ListJobs(200)
	if err != nil {
		return 0, false, err
	}
	for _, j := range jobs {
		if j.Type == "clip" && j.Payload == payload && (j.Status == "queued" || j.Status == "running") {
			return j.ID, true, nil
		}
	}
	id, err := d.St.EnqueueJob("clip", payload, 1)
	return id, true, err
}

func (d Deps) serveClip(w http.ResponseWriter, r *http.Request, path string, m *store.MediaFile, req clips.Request) {
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "clip file unreadable")
		return
	}
	defer f.Close()
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

func (d Deps) handleScan(w http.ResponseWriter, r *http.Request) {
	if active, err := d.St.HasActiveScanJob(); err == nil && active {
		writeJSON(w, map[string]any{"status": "already-running"})
		return
	}
	payload := map[string]any{}
	if root := strings.TrimSpace(r.URL.Query().Get("root")); root != "" {
		payload["root"] = root
	}
	pb, _ := json.Marshal(payload)
	if _, err := d.St.EnqueueJob("scan", string(pb), 3); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": "started"})
}

func (d Deps) handleReindex(w http.ResponseWriter, r *http.Request) {
	n, err := d.Indexer.ReindexAll()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"enqueued": n})
}

func (d Deps) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, d.Events.Collect())
}

func (d Deps) handleJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
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
		fileServer.ServeHTTP(w, r)
	}
}
