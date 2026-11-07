// Package api exercises the HTTP router and its handlers.
package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/mcp"
)

func TestHealth(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/health", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header = %q", got)
	}
	body := decodeMap(t, rr)
	if body["ok"] != true || body["meiliUp"] != true {
		t.Errorf("health body = %v", body)
	}
}

func TestHealthMeiliDown(t *testing.T) {
	st := newStore(t)
	deps := buildDeps(t, st, newMeili(t, http.StatusInternalServerError, nil))
	rr := do(t, Router(deps), http.MethodGet, "/api/health", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if body := decodeMap(t, rr); body["meiliUp"] != false {
		t.Errorf("meiliUp = %v, want false", body["meiliUp"])
	}
}

func TestAPIToken(t *testing.T) {
	_, deps := newEnv(t)
	deps.APIToken = "secret"
	h := Router(deps)

	cases := []struct {
		headers map[string]string
		name    string
		want    int
	}{
		{nil, "missing", http.StatusUnauthorized},
		{map[string]string{"X-API-Token": "nope"}, "wrong header", http.StatusUnauthorized},
		{map[string]string{"Authorization": "Bearer nope"}, "wrong bearer", http.StatusUnauthorized},
		{map[string]string{"Authorization": "Basic abc"}, "basic auth", http.StatusUnauthorized},
		{map[string]string{"X-API-Token": "secret"}, "header", http.StatusOK},
		{map[string]string{"Authorization": "Bearer secret"}, "bearer", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, h, http.MethodGet, "/api/admin/config", nil, tc.headers)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}

	// Public routes stay reachable without a token.
	if rr := do(t, h, http.MethodGet, "/api/health", nil, nil); rr.Code != http.StatusOK {
		t.Errorf("health status = %d", rr.Code)
	}
}

func TestWebServing(t *testing.T) {
	_, deps := newEnv(t)
	h := Router(deps)

	cases := []struct {
		target string
		want   string
	}{
		{"/", "<html>index</html>"},
		{"/app.js", "console.log(1)"},
		{"/some/client/route", "<html>index</html>"},
		{"/site.webmanifest", "{}"},
	}
	for _, tc := range cases {
		rr := do(t, h, http.MethodGet, tc.target, nil, nil)
		if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
			t.Errorf("%s status=%d body=%q", tc.target, rr.Code, rr.Body.String())
		}
	}

	rr := do(t, h, http.MethodGet, "/site.webmanifest", nil, nil)
	if got := rr.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("manifest Content-Type = %q, want application/manifest+json", got)
	}

	deps.WebFS = nil
	rr = do(t, Router(deps), http.MethodGet, "/missing", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("nil WebFS status = %d, want 404", rr.Code)
	}
}

func TestStoreErrorPaths(t *testing.T) {
	st, deps := newEnv(t)
	if err := os.Mkdir(filepath.Join(deps.MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	cases := []struct {
		body   io.Reader
		method string
		target string
	}{
		{nil, http.MethodGet, "/api/media"},
		{nil, http.MethodGet, "/api/media/1"},
		{nil, http.MethodGet, "/api/chunks/1_0-1"},
		{nil, http.MethodGet, "/api/thumb/1"},
		{nil, http.MethodGet, "/api/admin/config"},
		{strings.NewReader(`{"folders":["Shows"]}`), http.MethodPut, "/api/admin/config"},
		{nil, http.MethodPost, "/api/admin/scan"},
		{nil, http.MethodPost, "/api/admin/scan?root=Shows"},
		{nil, http.MethodPost, "/api/admin/reindex"},
		{nil, http.MethodPost, "/api/admin/reprobe"},
		{nil, http.MethodPost, "/api/admin/queue/pause"},
		{nil, http.MethodPost, "/api/admin/queue/resume"},
		{nil, http.MethodPost, "/api/admin/queue/clear"},
		{nil, http.MethodPost, "/api/admin/queue/stop"},
		{nil, http.MethodGet, "/api/admin/jobs"},
	}
	for _, tc := range cases {
		rr := do(t, h, tc.method, tc.target, tc.body, nil)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s %s status = %d, want 500", tc.method, tc.target, rr.Code)
		}
	}
}

func TestMCPEndpoint(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	deps.MCP = mcp.New(mcp.Deps{
		St: st, Search: deps.Search, Indexer: deps.Indexer, Clips: deps.Clips,
		Thumbs: deps.Thumbs, Events: deps.Events, Runner: deps.Runner,
		MediaDir: deps.MediaDir, ClipWorkers: 1,
	}, mcp.Config{Tokens: map[string]string{"agent": "read+clip"}}).HTTPHandler()
	h := Router(deps)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	jsonHeader := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	if rr := do(t, h, http.MethodPost, "/mcp", strings.NewReader(body), jsonHeader); rr.Code != http.StatusUnauthorized {
		t.Errorf("missing token status = %d, want 401", rr.Code)
	}
	wrong := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Authorization": "Bearer nope"}
	if rr := do(t, h, http.MethodPost, "/mcp", strings.NewReader(body), wrong); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", rr.Code)
	}

	authed := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Authorization": "Bearer agent"}
	rr := do(t, h, http.MethodPost, "/mcp", strings.NewReader(body), authed)
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "search_quotes") {
		t.Errorf("tools/list body missing search_quotes: %s", rr.Body.String())
	}

	// Calling a tool end to end resolves real library data through the MCP
	// adapter mounted on the shared router middleware.
	call := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_media","arguments":{"mediaId":%d}}}`, id)
	rr = do(t, h, http.MethodPost, "/mcp", strings.NewReader(call), authed)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Pilot") {
		t.Fatalf("tools/call status = %d body=%s", rr.Code, rr.Body.String())
	}
}
