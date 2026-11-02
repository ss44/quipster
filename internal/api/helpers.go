package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

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
