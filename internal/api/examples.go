package api

import "net/http"

// candidateExamples are the famous lines the landing page offers as search
// shortcuts; PopularExamples validates them against live indexed content so
// removed or re-probed media can no longer produce dead-end results.
var candidateExamples = []string{
	"show me the money",
	"the city never sleeps",
	"i could have been a contender",
	"run, you fools",
}

// handleExamples serves the landing-page example queries, ranked by popularity.
// An empty list means the library has no usable examples and the UI hides the
// block instead of rendering broken shortcuts.
func (d Deps) handleExamples(w http.ResponseWriter, _ *http.Request) {
	examples, err := d.St.PopularExamples(candidateExamples)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"examples": examples})
}
