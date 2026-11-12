package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/indexer"
	"quipster/internal/logbuf"
	"quipster/internal/store"
)

func TestAssemblySharesApplicationLogsWithMCP(t *testing.T) {
	clearConfigEnv(t)
	cfg := assemblyConfig(t)
	cfg.role = "api"
	cfg.mcpEnabled = true
	cfg.mcpAllowAdmin = true
	cfg.mcpTokens = "tok:admin"
	cfg.logs = logbuf.New(0)
	if _, err := cfg.logs.Write([]byte("application-log-marker\n")); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.dataDir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	a := assembleApp(context.Background(), cfg, st, indexer.New(st, cfg.meiliURL, ""))
	defer a.Stop()
	if a.Deps.Diagnostics.LogBuffer != cfg.logs {
		t.Fatal("REST must use original application buffer")
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_app_logs","arguments":{"contains":"application-log-marker"}}}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	a.Deps.MCP.ServeHTTP(rr, req)
	if a.logDone == nil {
		t.Fatal("log expiry goroutine not started")
	}
	select {
	case <-a.logDone:
		t.Fatal("log expiry stopped before app shutdown")
	default:
	}
	a.Stop()
	select {
	case <-a.logDone:
	default:
		t.Fatal("shutdown did not join log expiry goroutine")
	}
	if !strings.Contains(rr.Body.String(), "application-log-marker") {
		t.Fatalf("MCP not wired to original buffer: %s", rr.Body.String())
	}
}
