package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"quipster/internal/chunks"
	"quipster/internal/indexer"
	"quipster/internal/search"
)

func (d Deps) handleMediaList(w http.ResponseWriter, r *http.Request) {
	page := atoiQuery(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := atoiQuery(r.URL.Query().Get("perPage"))
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	res, err := d.St.ListMedia(perPage, (page-1)*perPage, strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("archived"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, res)
}

// handleMediaRestore re-activates an archived media row. Restoring a row
// queues an index job to re-add its Meili documents from the retained
// captions; the response reports whether that reindex was queued. A failed
// enqueue is surfaced as a warning field so the admin UI can offer a manual
// reindex instead of silently returning HTTP 200.
func (d Deps) handleMediaRestore(w http.ResponseWriter, r *http.Request) {
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
	if !m.Archived {
		writeJSON(w, map[string]any{"restored": false, "reindexQueued": false})
		return
	}
	needsReindex, _, err := d.St.RestoreMedia(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	reindexQueued := false
	warning := ""
	if needsReindex {
		payload := fmt.Sprintf(`{"mediaId":%d}`, id)
		if _, queued, qerr := d.St.EnqueueUniqueJob("index", payload, 1); qerr != nil {
			// The row is already re-activated; the index rebuild is covered by
			// boot-time reconciliation if this enqueue fails.
			log.Printf("[api] enqueue index job for restored media %d: %v", id, qerr)
			warning = "media restored but the reindex job could not be queued; run /api/admin/reindex if it does not reappear in search"
		} else {
			reindexQueued = queued
		}
	}
	if warning == "" {
		writeJSON(w, map[string]any{"restored": true, "reindexQueued": reindexQueued})
		return
	}
	writeJSON(w, map[string]any{"restored": true, "reindexQueued": reindexQueued, "warning": warning})
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
	mediaID, startIdx, endIdx, ok := chunks.ParseID(id)
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
