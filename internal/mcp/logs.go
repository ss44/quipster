package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/diagnostics"
)

// addDiagnosticTools installs read-only diagnostics independently of mutations.
func (s *Server) addDiagnosticTools(srv *mcpsdk.Server, token string) {
	addTool(srv, "get_diagnostics", "Read a lightweight resource and runtime snapshot: RAM, swap, load, in-memory SQLite writer pool stats and bounded system identity. No SQL, full counts, benchmarks, disk walks or external probes. Requires explicit diagnostics scope or admin scope with MCP_ALLOW_ADMIN.", readOnly(), token, s.getDiagnostics)
	addTool(srv, "get_app_logs", "Read sanitized process-local application logs from at most the last hour, subject to memory caps. Newest matches, oldest first, with capture timestamps. Requires explicit diagnostics scope or admin scope with MCP_ALLOW_ADMIN; no SQLite queries.", readOnly(), token, s.getAppLogs)
}

// getDiagnostics delegates to the shared bounded, query-free collector.
func (s *Server) getDiagnostics(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, diagnostics.ReadSnapshot, error) {
	return nil, s.deps.Diagnostics.ReadOnlySnapshot(), nil
}

// getAppLogs uses the same bounded, sanitized memory-only service as REST.
func (s *Server) getAppLogs(_ context.Context, _ *mcpsdk.CallToolRequest, in diagnostics.LogQuery) (*mcpsdk.CallToolResult, diagnostics.LogResult, error) {
	out, err := s.deps.Diagnostics.AppLogs(in)
	return nil, out, err
}
