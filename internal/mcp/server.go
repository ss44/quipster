// Package mcp exposes Quipster's search, metadata, and clipping services to
// AI agents over the Model Context Protocol (MCP).
//
// The server is a thin adapter: every tool delegates to the same services the
// REST API uses (search.Service, clips.Service, thumbs.Service, store.Store)
// so agent-visible behavior cannot drift from the HTTP surface. Access is off
// by default, gated by scoped bearer tokens, per-token rate limits, and an
// explicit master switch for admin tools.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/clips"
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
)

// Tool scopes, ordered from least to most privileged. A grant of a higher
// scope implies every lower one, mirroring the read/clip/admin split from the
// design notes: read covers search and metadata, clip adds clip creation and
// thumbnails, admin adds queue/scan/config control.
const (
	ScopeRead  = "read"
	ScopeClip  = "clip"
	ScopeAdmin = "admin"
)

// DefaultRateLimit is the per-token request-per-minute budget applied when
// Config.RateLimit is zero.
const DefaultRateLimit = 120

// Deps bundles the collaborators the MCP tools delegate to. Every field is a
// shared service also used by the HTTP API; the package owns no business
// logic of its own.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Deps struct {
	St          *store.Store
	Search      *search.Service
	Indexer     *indexer.Indexer
	Clips       *clips.Service
	Thumbs      *thumbs.Service
	Events      *events.Collector
	Runner      *jobs.Runner
	MediaDir    string
	ClipWorkers int // bound on concurrent synchronous clip renders
}

// Config controls MCP exposure. Tokens maps a raw bearer token to its granted
// scope list (for example "read+clip"); unknown tokens are rejected.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Config struct {
	Tokens     map[string]string
	AllowAdmin bool // master switch: admin tools also require the admin scope
	RateLimit  int  // requests per minute per token; 0 applies the default
	Version    string
}

// Server hands out one MCP server per granted token so tool visibility and
// audit attribution follow the caller's scopes.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Server struct {
	deps    Deps
	cfg     Config
	limiter *limiter
	slot    chan struct{} // bounds concurrent synchronous clip renders

	mu      sync.Mutex
	servers map[string]*mcpsdk.Server
}

// New builds an MCP facade over the given services. Call HTTPHandler or
// ConnectStdio to expose it.
func New(deps Deps, cfg Config) *Server {
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = DefaultRateLimit
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if deps.ClipWorkers <= 0 {
		deps.ClipWorkers = 2
	}
	return &Server{
		deps:    deps,
		cfg:     cfg,
		limiter: newLimiter(cfg.RateLimit),
		slot:    make(chan struct{}, deps.ClipWorkers),
		servers: map[string]*mcpsdk.Server{},
	}
}

// tokenID derives a stable, non-reversible identifier for audit logs so raw
// bearer tokens never appear in log output.
func tokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:6])
}

// ParseTokens decodes the MCP_TOKENS environment variable into the token map
// Config.Tokens expects. Tokens are comma-separated, each entry
// "token:scopes" where scopes is a plus-separated list of read/clip/admin
// defaulting to read. Example:
//
//	MCP_TOKENS=secret1:read+clip,secret2:admin
func ParseTokens(v string) map[string]string {
	out := map[string]string{}
	for _, entry := range strings.Split(v, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		token, scopes, _ := strings.Cut(entry, ":")
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.TrimSpace(scopes) == "" {
			scopes = ScopeRead
		}
		out[token] = scopes
	}
	return out
}

// bearerToken extracts the caller's token from the Authorization header
// (Bearer scheme) or the X-API-Token fallback used by the admin UI.
func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Token"))
}

// HTTPHandler returns the streamable-HTTP MCP endpoint: token auth, per-token
// rate limiting, then the SDK's streamable handler. Stateless mode keeps the
// endpoint compatible with one-shot agent calls and horizontal proxies; each
// POST carries its own MCP payload.
func (s *Server) HTTPHandler() http.Handler {
	inner := mcpsdk.NewStreamableHTTPHandler(s.serverForRequest, &mcpsdk.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if _, ok := s.cfg.Tokens[tok]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"invalid or missing MCP token"}`) //nolint:errcheck // best-effort body for a rejected request
			return
		}
		if !s.limiter.allow(tok) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"rate limit exceeded"}`) //nolint:errcheck // best-effort body for a rejected request
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// serverForRequest resolves the MCP server for the caller's token. It returns
// nil for unknown tokens; the auth wrapper rejects those before the SDK sees
// the request.
func (s *Server) serverForRequest(r *http.Request) *mcpsdk.Server {
	tok := bearerToken(r)
	scopes, ok := s.cfg.Tokens[tok]
	if !ok {
		return nil
	}
	return s.serverFor(tokenID(tok), scopes)
}

// ConnectStdio runs a single MCP session over stdin/stdout for local
// single-process integrations. Stdio is operator-launched, so it receives the
// full scope set; admin tools still require Config.AllowAdmin.
func (s *Server) ConnectStdio(ctx context.Context) error {
	srv := s.serverFor("stdio", ScopeAdmin)
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

// serverFor builds (once) the MCP server for one token id and scope list.
// The cache key includes the scopes: the same token can be probed with
// different scope lists, and each combination gets its own tool surface.
func (s *Server) serverFor(id, scopes string) *mcpsdk.Server {
	key := id + "|" + scopes
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv, ok := s.servers[key]; ok {
		return srv
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "quipster",
		Title:   "Quipster video library",
		Version: s.cfg.Version,
	}, &mcpsdk.ServerOptions{
		Instructions: "Search indexed dialog, resolve scenes, and render short clips. " +
			"Search results carry chunkId values that feed get_scene_by_line; " +
			"create_clip returns ready or queued status with a playback URL.",
	})
	s.registerTools(srv, id, scopes)
	s.addResources(srv)
	s.addPrompts(srv)
	s.servers[key] = srv
	return srv
}

// registerTools installs the tool surface matching the caller's scopes.
// Admin tools additionally require the Config.AllowAdmin master switch so a
// leaked admin-scoped token alone cannot operate the queue.
func (s *Server) registerTools(srv *mcpsdk.Server, token, scopes string) {
	if hasScope(scopes, ScopeRead) {
		s.addReadTools(srv, token)
	}
	if hasScope(scopes, ScopeClip) {
		s.addClipTools(srv, token)
	}
	if hasScope(scopes, ScopeAdmin) && s.cfg.AllowAdmin {
		s.addAdminTools(srv, token)
	}
}

// addTool installs one typed tool wrapped with audit logging.
func addTool[In, Out any](srv *mcpsdk.Server, name, desc string, ann *mcpsdk.ToolAnnotations,
	token string, h mcpsdk.ToolHandlerFor[In, Out]) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: name, Description: desc, Annotations: ann}, audited(token, name, h))
}

// audited wraps a tool handler with the structured audit line required for
// agent access: caller token id, tool name, latency and outcome. Arguments
// are deliberately not logged to avoid recording user dialog content.
func audited[In, Out any](token, name string, h mcpsdk.ToolHandlerFor[In, Out]) mcpsdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, Out, error) {
		started := time.Now()
		res, out, err := h(ctx, req, in)
		//nolint:gosec // token is a server-derived hash id and name is a registered tool name, never raw client input.
		log.Printf("[mcp-audit] token=%s tool=%s elapsed=%s ok=%t", token, name,
			time.Since(started).Round(time.Millisecond), err == nil)
		return res, out, err
	}
}

// readOnly marks tools that never modify state.
func readOnly() *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{ReadOnlyHint: true}
}

// destructive marks tools that can discard work or change scan scope, which
// MCP clients typically confirm with the user before calling.
func destructive() *mcpsdk.ToolAnnotations {
	yes := true
	return &mcpsdk.ToolAnnotations{DestructiveHint: &yes}
}

// scopeRank orders scopes; unknown scopes rank lowest so they grant nothing.
func scopeRank(scope string) int {
	switch scope {
	case ScopeRead:
		return 1
	case ScopeClip:
		return 2
	case ScopeAdmin:
		return 3
	default:
		return 0
	}
}

// allows reports whether a granted scope satisfies the required scope,
// treating higher scopes as supersets of lower ones.
func allows(granted, required string) bool {
	return scopeRank(granted) >= scopeRank(required)
}

// hasScope checks a plus-separated grant list for the required scope.
func hasScope(grants, required string) bool {
	if grants == "" {
		return required == ScopeRead
	}
	for _, g := range strings.Split(grants, "+") {
		if allows(strings.TrimSpace(g), required) {
			return true
		}
	}
	return false
}
