package mcp

import (
	"strings"
	"testing"
)

func TestAppLogsRequiresAdminAndMasterSwitch(t *testing.T) {
	for _, tc := range []struct {
		scope         string
		enabled, want bool
	}{
		{ScopeRead, true, false}, {ScopeClip, true, false}, {ScopeAdmin, false, false}, {ScopeAdmin, true, true},
	} {
		t.Run(tc.scope+"/"+strings.TrimSpace(map[bool]string{true: "enabled", false: "disabled"}[tc.enabled]), func(t *testing.T) {
			s := New(Deps{}, Config{Tokens: map[string]string{"tok": tc.scope}, AllowAdmin: tc.enabled})
			h := s.HTTPHandler()
			list := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			if strings.Contains(list.Body.String(), `"get_app_logs"`) != tc.want {
				t.Fatalf("tool visibility: %s", list.Body.String())
			}
			call := postMCP(t, h, "/mcp", "tok", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_app_logs","arguments":{}}}`)
			if tc.want {
				if !strings.Contains(call.Body.String(), `"lines":[]`) {
					t.Fatalf("allowed call: %s", call.Body.String())
				}
				cs := connect(t, s, "tok", tc.scope)
				tools, err := cs.ListTools(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range tools.Tools {
					if tool.Name == "get_app_logs" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
						t.Fatal("get_app_logs missing read-only annotation")
					}
				}
			} else if strings.Contains(call.Body.String(), `"lines"`) {
				t.Fatalf("unauthorized call returned logs: %s", call.Body.String())
			}
		})
	}
}
