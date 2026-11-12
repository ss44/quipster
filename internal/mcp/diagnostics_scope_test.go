package mcp

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/diagnostics"
	"quipster/internal/logbuf"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsToolIsLightweightAndSanitized(t *testing.T) {
	t.Setenv("API_TOKEN", "snapshot-secret")
	c := &diagnostics.Collector{Version: "snapshot-secret /private/build", DataDir: "/private/data", MediaDir: "/private/media", CacheDir: "/private/cache", MeiliUp: func() bool { t.Error("diagnostics ran network probe"); return false }, LogBuffer: logbuf.New(0)}
	if _, err := c.LogBuffer.Write([]byte("2026/11/12 password=othersecret file=C:\\private\\movie.mkv\n")); err != nil {
		t.Fatal(err)
	}
	for _, collector := range []*diagnostics.Collector{nil, c, {Version: strings.Repeat("x", 1<<20)}} {
		s := New(Deps{Diagnostics: collector}, Config{Tokens: map[string]string{"tok": "diagnostics"}})
		assertDiagnosticSurface(t, s)
		h := s.HTTPHandler()
		started := time.Now()
		call := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_diagnostics","arguments":{}}}`).Body.String()
		if !strings.Contains(call, `"sqlitePools"`) || !strings.Contains(call, `"system"`) {
			t.Fatalf("snapshot unavailable: %s", call)
		}
		if len(call) > 32<<10 || time.Since(started) > time.Second {
			t.Fatalf("snapshot unbounded: bytes=%d elapsed=%v", len(call), time.Since(started))
		}
		for _, secret := range []string{"snapshot-secret", "private"} {
			if strings.Contains(call, secret) {
				t.Errorf("snapshot leaked %s", secret)
			}
		}
		if collector == nil && !strings.Contains(call, "collector_unavailable") {
			t.Errorf("nil collector not marked unavailable: %s", call)
		}
		logs := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_app_logs","arguments":{}}}`).Body.String()
		if collector == c {
			assertSanitizedDiagnosticsLogs(t, logs)
		}
	}
}

func assertDiagnosticSurface(t *testing.T, s *Server) {
	t.Helper()
	cs := connect(t, s, "tok", "diagnostics")
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("diagnostics-only must expose exactly 2 tools, got %d", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("not readonly: %s", tool.Name)
		}
	}
}

func assertSanitizedDiagnosticsLogs(t *testing.T, logs string) {
	t.Helper()
	for _, want := range []string{"2026/11/12", `"entries"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %s: %s", want, logs)
		}
	}
	for _, secret := range []string{"movie.mkv", "othersecret"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs leaked %s: %s", secret, logs)
		}
	}
}

func TestDiagnosticsGrantIsIndependentOfAdminAndMediaScopes(t *testing.T) {
	for _, tc := range []struct {
		scope                    string
		admin, logs, clip, queue bool
	}{
		{"diagnostics", false, true, false, false}, {"diagnostics", true, true, false, false},
		{"read+diagnostics", false, true, false, false}, {"clip+diagnostics", false, true, true, false},
		{"admin+diagnostics", false, true, true, false}, {"admin", false, false, true, false}, {"admin", true, true, true, true},
		{"read", true, false, false, false}, {"clip", true, false, true, false}, {"unknown", true, false, false, false}, {"read+clip", false, false, true, false}, {" diagnostic ", true, false, false, false}, {"diagnostics+unknown", false, true, false, false},
	} {
		t.Run(tc.scope+map[bool]string{true: "/on", false: "/off"}[tc.admin], func(t *testing.T) {
			s := New(Deps{}, Config{Tokens: map[string]string{"tok": tc.scope}, AllowAdmin: tc.admin})
			h := s.HTTPHandler()
			list := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Body.String()
			for name, want := range map[string]bool{"get_app_logs": tc.logs, "get_diagnostics": tc.logs, "create_clip": tc.clip, "queue_control": tc.queue} {
				if strings.Contains(list, `"`+name+`"`) != want {
					t.Errorf("%s visibility want %t: %s", name, want, list)
				}
				if !want {
					call := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`).Body.String()
					if !strings.Contains(call, "unknown tool") {
						t.Errorf("ungranted tool call accepted: %s", call)
					}
				}
			}
			if tc.logs {
				call := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_app_logs","arguments":{}}}`).Body.String()
				if !strings.Contains(call, `"lines":[]`) {
					t.Errorf("granted logs unavailable: %s", call)
				}
			}
		})
	}
}

func TestDiagnosticsOnlyGrantDoesNotExposeMediaResources(t *testing.T) {
	s := New(Deps{}, Config{Tokens: map[string]string{"tok": "diagnostics"}})
	h := s.HTTPHandler()
	list := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`).Body.String()
	if strings.Contains(list, "quipster://") {
		t.Fatalf("diagnostics grant exposed media resources: %s", list)
	}
	cs := connect(t, s, "tok", "diagnostics")
	for _, uri := range []string{"quipster://media/1", "quipster://chunk/1_0-1", "quipster://thumb/1", "quipster://clip/clipkey"} {
		if _, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("diagnostics grant allowed resources/read for %s", uri)
		}
	}
	for _, name := range []string{"find_and_clip_scene", "summarize_show_quotes", "audit_processing_queue"} {
		params := &mcpsdk.GetPromptParams{Name: name, Arguments: map[string]string{"query": "dialog", "show": "show"}}
		if _, err := cs.GetPrompt(t.Context(), params); err == nil {
			t.Errorf("diagnostics grant allowed prompts/get for %s", name)
		}
	}
}
