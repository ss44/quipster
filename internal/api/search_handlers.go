package api

import (
	"log"
	"net/http"
	"strings"
)

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
