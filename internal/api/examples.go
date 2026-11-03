package api

import (
	"net/http"
	"sort"

	"quipster/internal/search"
)

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
	type candidateHit struct {
		text string
		hits int64
	}
	var hits []candidateHit
	for _, c := range candidateExamples {
		res, err := d.Search.Search(c, "", 1, 1, search.ModeKeyword)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if res.TotalHits > 0 {
			hits = append(hits, candidateHit{c, res.TotalHits})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].hits == hits[j].hits {
			return hits[i].text < hits[j].text
		}
		return hits[i].hits > hits[j].hits
	})
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.text)
	}
	writeJSON(w, map[string]any{"examples": out})
}
