package api

import (
	"net/http"
)

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
