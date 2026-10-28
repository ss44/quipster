package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/search"
)

// addResources registers the quipster:// resource templates. Resources return
// compact JSON descriptors that reference the HTTP endpoints for binary
// payloads (thumbnails, clips), so no media bytes or filesystem paths flow
// through MCP responses.
func (s *Server) addResources(srv *mcpsdk.Server) {
	srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "quipster://media/{id}",
		Name:        "Media metadata",
		Description: "Metadata for one library entry, keyed by media id.",
		MIMEType:    "application/json",
	}, s.readMediaResource)
	srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "quipster://chunk/{chunkId}",
		Name:        "Dialog chunk",
		Description: "One dialog chunk with its media metadata and cue span.",
		MIMEType:    "application/json",
	}, s.readChunkResource)
	srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "quipster://thumb/{mediaId}",
		Name:        "Thumbnail link",
		Description: "Thumbnail URL for a media entry's first seconds.",
		MIMEType:    "application/json",
	}, s.readThumbResource)
	srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "quipster://clip/{cacheKey}",
		Name:        "Clip link",
		Description: "Cache identity and playback URL for a rendered clip.",
		MIMEType:    "application/json",
	}, s.readClipResource)
}

// resourceBody marshals v into a single text resource; a failure would mean a
// non-serializable payload, which the fixed JSON shapes here rule out.
func resourceBody(uri string, v any) *mcpsdk.ReadResourceResult {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(b),
	}}}
}

// readMediaResource serves quipster://media/{id}.
func (s *Server) readMediaResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	id, err := resourceID(req.Params.URI, "media")
	if err != nil {
		return nil, err
	}
	m, err := s.deps.St.GetMedia(id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, mcpsdk.ResourceNotFoundError(req.Params.URI)
	}
	return resourceBody(req.Params.URI, publicMedia(m)), nil
}

// readChunkResource serves quipster://chunk/{chunkId}.
func (s *Server) readChunkResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	uri := req.Params.URI
	_, chunkID, ok := strings.Cut(uri, "quipster://chunk/")
	if !ok || chunkID == "" {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	mediaID, startIdx, endIdx, ok := parseChunkID(chunkID)
	if !ok {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	ch, err := s.deps.St.GetChunkByKey(mediaID, startIdx, endIdx)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	m, err := s.deps.St.GetMedia(mediaID)
	if err != nil || m == nil {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	span, _, _, err := s.deps.St.CuesAround(mediaID, ch.StartCueIdx, ch.EndCueIdx, 1)
	if err != nil {
		return nil, err
	}
	return resourceBody(uri, map[string]any{
		"chunkId": chunkID,
		"text":    ch.Text,
		"media":   publicMedia(m),
		"cues":    search.CueDTOs(span),
	}), nil
}

// readThumbResource serves quipster://thumb/{mediaId}.
func (s *Server) readThumbResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	id, err := resourceID(req.Params.URI, "thumb")
	if err != nil {
		return nil, err
	}
	m, err := s.deps.St.GetMedia(id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, mcpsdk.ResourceNotFoundError(req.Params.URI)
	}
	return resourceBody(req.Params.URI, map[string]any{
		"url": fmt.Sprintf("/api/thumb/%d?t=5.0&w=320", id),
	}), nil
}

// readClipResource serves quipster://clip/{cacheKey}. The cache key alone
// cannot rebuild a playback URL (it hides the request parameters), so the
// descriptor points agents back at create_clip, which resolves the same key
// deterministically.
func (s *Server) readClipResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	uri := req.Params.URI
	_, cacheKey, ok := strings.Cut(uri, "quipster://clip/")
	if !ok || cacheKey == "" {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	e, err := s.deps.St.GetCacheEntry(cacheKey)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, mcpsdk.ResourceNotFoundError(uri)
	}
	return resourceBody(uri, map[string]any{
		"cacheKey": cacheKey,
		"kind":     e.Kind,
		"bytes":    e.Bytes,
	}), nil
}

// resourceID extracts the "{id}" path segment from a quipster:// scheme URI
// and parses it as an integer id.
func resourceID(uri, kind string) (int64, error) {
	_, rest, ok := strings.Cut(uri, "quipster://"+kind+"/")
	if !ok || rest == "" {
		return 0, mcpsdk.ResourceNotFoundError(uri)
	}
	// Trailing query components (e.g. ?t=) are ignored by callers that do not
	// need them; split them off before parsing.
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, mcpsdk.ResourceNotFoundError(uri)
	}
	return id, nil
}
