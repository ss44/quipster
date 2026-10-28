package api

import (
	"net/http"
)

var candidateExamples = []string{
	"show me the money",
	"the city never sleeps",
	"i could have been a contender",
	"run, you fools",
}

func (d Deps) handleExamples(w http.ResponseWriter, _ *http.Request) {
	valid, err := d.St.ValidExamples(candidateExamples)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if valid == nil {
		valid = []string{}
	}
	writeJSON(w, map[string]any{"examples": valid})
}
