package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTimeoutExceptStreams keeps the request timeout on ordinary routes while
// allowing SSE endpoints to outlive it. A timed-out stream would make every
// browser reconnect and briefly render a stale snapshot.
func TestTimeoutExceptStreams(t *testing.T) {
	mw := timeoutExceptStreams(20 * time.Millisecond)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusGatewayTimeout)
		case <-time.After(60 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		}
	}))

	cases := []struct {
		path string
		want int
	}{
		{"/api/admin/events", http.StatusOK},
		{"/api/admin/diagnostics/stream", http.StatusOK},
		{"/api/search", http.StatusGatewayTimeout},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rr.Code != c.want {
			t.Errorf("%s status = %d, want %d", c.path, rr.Code, c.want)
		}
	}
}
