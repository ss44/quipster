package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quipster/internal/diagnostics"
	"quipster/internal/events"
	"quipster/internal/logbuf"
	"quipster/internal/mcp"
)

func TestAppLogsRESTAndMCPShareSanitizedMemoryOnlyService(t *testing.T) {
	b := logbuf.New(0)
	c := &diagnostics.Collector{LogBuffer: b}
	if _, err := b.Write([]byte("healthy\nSQLITE_BUSY file=/mnt/private/movie.mkv\n")); err != nil {
		t.Fatal(err)
	}
	m := mcp.New(mcp.Deps{Diagnostics: c}, mcp.Config{Tokens: map[string]string{"tok": "admin"}, AllowAdmin: true})
	h := Router(Deps{Diagnostics: c, Events: &events.Collector{}, APIToken: "admin-secret", MCP: m.HTTPHandler()})
	unauthorized := do(t, h, http.MethodGet, "/api/admin/logs", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected logs: %d", unauthorized.Code)
	}
	headers := map[string]string{"Authorization": "Bearer admin-secret"}
	rest := do(t, h, http.MethodGet, "/api/admin/logs?contains=SQLITE_BUSY&limit=1", nil, headers)
	if rest.Code != http.StatusOK || strings.Contains(rest.Body.String(), "private") || !strings.Contains(rest.Body.String(), "SQLITE_BUSY") {
		t.Fatalf("REST result: %s", rest.Body.String())
	}
	body := decodeMap(t, rest)
	if body["count"] != float64(1) {
		t.Fatalf("REST count: %v", body)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_app_logs","arguments":{"contains":"SQLITE_BUSY","limit":1}}}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "SQLITE_BUSY") || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("MCP result: %s", rr.Body.String())
	}
	for _, query := range []string{"since=bad", "until=bad", "contains=" + strings.Repeat("x", 257)} {
		got := do(t, h, http.MethodGet, "/api/admin/logs?"+query, nil, headers)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("query %s: %d", query, got.Code)
		}
	}
}
