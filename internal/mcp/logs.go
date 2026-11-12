package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/diagnostics"
)

// getAppLogs uses the same bounded, sanitized memory-only service as REST.
func (s *Server) getAppLogs(_ context.Context, _ *mcpsdk.CallToolRequest, in diagnostics.LogQuery) (*mcpsdk.CallToolResult, diagnostics.LogResult, error) {
	out, err := s.deps.Diagnostics.AppLogs(in)
	return nil, out, err
}
