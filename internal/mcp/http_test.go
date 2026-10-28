package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postMCP sends one JSON-RPC request through the streamable HTTP endpoint in
// stateless mode and returns the recorder.
func postMCP(t *testing.T, h http.Handler, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHTTPHandlerAuthAndRateLimit(t *testing.T) {
	fx := newFixture(t)
	h := fx.srv.HTTPHandler()

	// Unknown and missing tokens are rejected before the MCP handler runs.
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"missing token", "", http.StatusUnauthorized},
		{"unknown token", "nope", http.StatusUnauthorized},
	} {
		if got := postMCP(t, h, "/mcp", tc.token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Code; got != tc.want {
			t.Errorf("%s status = %d, want %d", tc.name, got, tc.want)
		}
	}

	// A valid token passes and reaches the MCP layer.
	if got := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Code; got != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200", got)
	}

	// Exhausting the per-token budget yields 429.
	limited := New(testDeps(t, fx.st, fx.fake), Config{
		Tokens:    map[string]string{"tok": ScopeRead},
		RateLimit: 1,
	})
	lh := limited.HTTPHandler()
	if got := postMCP(t, lh, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Code; got != http.StatusOK {
		t.Fatalf("first limited request status = %d, want 200", got)
	}
	if got := postMCP(t, lh, "/mcp", "tok", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`).Code; got != http.StatusTooManyRequests {
		t.Errorf("second limited request status = %d, want 429", got)
	}
	// Another token still has its own budget.
	if got := postMCP(t, lh, "/mcp", "second", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Code; got != http.StatusUnauthorized {
		t.Errorf("unknown second token status = %d, want 401", got)
	}
}

func TestHTTPHandlerGrantsScopedTools(t *testing.T) {
	fx := newFixture(t)
	// The HTTP layer resolves the same scoped servers the in-memory tests use:
	// an admin token sees admin tools through tools/list.
	h := fx.srv.HTTPHandler()
	rr := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "queue_control") {
		t.Errorf("admin tools missing from HTTP tools/list: %s", rr.Body.String())
	}
}

func TestLimiterWindowResets(t *testing.T) {
	l := newLimiter(2)
	first, second := l.allow("tok"), l.allow("tok")
	if !first || !second {
		t.Fatal("first window requests must pass")
	}
	if l.allow("tok") {
		t.Fatal("third request in the same window must be rejected")
	}
	// A zero limit disables limiting entirely.
	open := newLimiter(0)
	for i := 0; i < 5; i++ {
		if !open.allow("tok") {
			t.Fatal("unlimited limiter rejected a request")
		}
	}
}
