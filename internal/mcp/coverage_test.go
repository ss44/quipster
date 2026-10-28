package mcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/indexer"
)

func TestParseChunkIDVariants(t *testing.T) {
	cases := []struct {
		id    string
		ok    bool
		media int64
		start int
		end   int
	}{
		{"7_0-2", true, 7, 0, 2},
		{"12_5-9", true, 12, 5, 9},
		{"garbage", false, 0, 0, 0},
		{"_0-1", false, 0, 0, 0},
		{"x_0-1", false, 0, 0, 0},
		{"1_abc-1", false, 0, 0, 0},
		{"1_0-b", false, 0, 0, 0},
		{"1_0", false, 0, 0, 0},
		{"1_", false, 0, 0, 0},
	}
	for _, tc := range cases {
		media, start, end, ok := parseChunkID(tc.id)
		if ok != tc.ok || media != tc.media || start != tc.start || end != tc.end {
			t.Errorf("parseChunkID(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				tc.id, media, start, end, ok, tc.media, tc.start, tc.end, tc.ok)
		}
	}
}

func TestResourceHelpers(t *testing.T) {
	// resourceBody cannot serialize a channel: it must return nil instead of
	// half-written content.
	if body := resourceBody("quipster://x", make(chan int)); body != nil {
		t.Errorf("resourceBody(bad value) = %+v, want nil", body)
	}

	fx := newFixture(t)
	for _, uri := range []string{
		"quipster://media/0",
		"quipster://media/-3",
		"quipster://media/abc",
		"quipster://media/",
		"https://elsewhere/media/1",
	} {
		if _, err := resourceID(uri, "media"); err == nil {
			t.Errorf("resourceID(%q) unexpectedly succeeded", uri)
		}
	}
	if id, err := resourceID("quipster://media/12?t=3", "media"); err != nil || id != 12 {
		t.Errorf("resourceID with query = %d/%v", id, err)
	}
	// The helpers are exercised through real reads in TestResourcesAndPrompts;
	// this guard keeps the thumb resource honest about unknown media.
	if _, err := fx.srv.readThumbResource(context.Background(), &mcpsdk.ReadResourceRequest{
		Params: &mcpsdk.ReadResourceParams{URI: "quipster://thumb/4242"},
	}); err == nil {
		t.Error("thumb resource for missing media must fail")
	}
}

func TestPromptVariants(t *testing.T) {
	sum, sumErr := summarizeShowQuotes(context.Background(), &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{Arguments: map[string]string{"show": "Lost"}},
	})
	if sumErr != nil {
		t.Fatalf("summarize prompt: %v", sumErr)
	}
	text, ok := sum.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "Lost") {
		t.Errorf("summarize prompt = %+v", sum)
	}
	if _, err := summarizeShowQuotes(context.Background(), &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{},
	}); err == nil {
		t.Error("summarize prompt without show must fail")
	}

	audit, err := auditProcessingQueue(context.Background(), &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{},
	})
	if err != nil {
		t.Fatalf("audit prompt: %v", err)
	}
	auditText, ok := audit.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok || !strings.Contains(auditText.Text, "get_processing_status") {
		t.Errorf("audit prompt = %+v", audit)
	}
}

func TestChunkThumbClipResources(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)

	// Chunk resource joins the chunk, media and cue span.
	res, chunkErr := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{
		URI: "quipster://chunk/" + indexer.DocID(fx.id, 0, 1),
	})
	if chunkErr != nil {
		t.Fatalf("read chunk: %v", chunkErr)
	}
	body := res.Contents[0].Text
	if !strings.Contains(body, `"text":"hello world"`) || strings.Contains(body, "/tv/") {
		t.Errorf("chunk resource = %s", body)
	}
	for _, uri := range []string{
		"quipster://chunk/garbage",
		"quipster://chunk/" + indexer.DocID(fx.id, 7, 8),
	} {
		if _, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("chunk resource %q must fail", uri)
		}
	}

	// Thumb resource returns the REST URL.
	res, thumbErr := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "quipster://thumb/1"})
	if thumbErr != nil {
		t.Fatalf("read thumb: %v", thumbErr)
	}
	if !strings.Contains(res.Contents[0].Text, "/api/thumb/1") {
		t.Errorf("thumb resource = %s", res.Contents[0].Text)
	}

	// Clip resource describes a seeded cache entry.
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if werr := os.WriteFile(path, []byte("clip"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if uerr := fx.st.UpsertCacheEntry("clipkey", path, "clip", 4); uerr != nil {
		t.Fatal(uerr)
	}
	res, clipErr := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "quipster://clip/clipkey"})
	if clipErr != nil {
		t.Fatalf("read clip: %v", clipErr)
	}
	if !strings.Contains(res.Contents[0].Text, `"cacheKey":"clipkey"`) {
		t.Errorf("clip resource = %s", res.Contents[0].Text)
	}
	if _, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "quipster://clip/missing"}); err == nil {
		t.Error("missing clip resource must fail")
	}
}

func TestQueueControlStop(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", "read+clip+admin")
	out := structured[queueOut](t, callTool(t, cs, "queue_control", map[string]any{"action": "stop"}))
	if !out.Paused {
		t.Errorf("stop = %+v", out)
	}
	paused, err := fx.st.GetQueuePaused()
	if err != nil || !paused {
		t.Errorf("queue paused = %v err=%v", paused, err)
	}
}

func TestCreateClipRendersWithMockFFmpeg(t *testing.T) {
	fx := newFixture(t)
	fx.srv.deps.ClipWorkers = 1
	t.Setenv("FFMPEG_BIN", "../clips/mock_ffmpeg.sh")
	cs := connect(t, fx.srv, "tok", "read+clip")

	// Saturating the single render slot yields a typed busy error.
	fx.srv.slot <- struct{}{}
	if msg := callToolErr(t, cs, "create_clip",
		map[string]any{"mediaId": fx.id, "start": 1, "end": 2, "mode": "fast", "padMs": 0}); !strings.Contains(msg, "capacity") {
		t.Errorf("busy error = %q", msg)
	}
	<-fx.srv.slot

	// With capacity free the render runs and the result is ready.
	out := structured[createClipOut](t, callTool(t, cs, "create_clip",
		map[string]any{"mediaId": fx.id, "start": 1, "end": 2, "mode": "fast", "padMs": 0}))
	if out.Status != "ready" || out.URL == "" {
		t.Errorf("render result = %+v", out)
	}
	// A negative pad is rejected before validation.
	if msg := callToolErr(t, cs, "create_clip",
		map[string]any{"mediaId": fx.id, "start": 1, "end": 2, "mode": "fast", "padMs": -5}); msg != "padMs out of range (0-5000)" {
		t.Errorf("negative pad error = %q", msg)
	}
}

func TestSearchQuotesTitleMatches(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)
	// The subtitle-less media matches the query by title only.
	out := structured[searchOut](t, callTool(t, cs, "search_quotes", map[string]any{"query": "Gone"}))
	if len(out.Hits) != 0 || len(out.TitleMatches) != 1 {
		t.Fatalf("search = %+v", out)
	}
	tm := out.TitleMatches[0]
	if tm.MediaID != 2 || tm.Title != "Gone in 60 Seconds" || !tm.NoSubtitle || tm.ThumbURL == "" {
		t.Errorf("title match = %+v", tm)
	}
}

func TestServerForRequestRejectsUnknownTokens(t *testing.T) {
	fx := newFixture(t)
	req, err := newHTTPRequest("Bearer nope")
	if err != nil {
		t.Fatal(err)
	}
	if srv := fx.srv.serverForRequest(req); srv != nil {
		t.Error("unknown token must not resolve a server")
	}
	authed, err := newHTTPRequest("Bearer tok")
	if err != nil {
		t.Fatal(err)
	}
	if srv := fx.srv.serverForRequest(authed); srv == nil {
		t.Error("valid token must resolve a server")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	srv := New(Deps{}, Config{})
	if srv.cfg.RateLimit != DefaultRateLimit || srv.cfg.Version != "dev" || cap(srv.slot) != 2 {
		t.Errorf("defaults not applied: %+v slot=%d", srv.cfg, cap(srv.slot))
	}
	if !srv.limiter.allow("any") {
		t.Error("default limiter must allow traffic")
	}
}

// newHTTPRequest builds a request carrying the given Authorization header.
func newHTTPRequest(auth string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req, nil
}

func TestClosedStoreErrorPaths(t *testing.T) {
	fx := newFixture(t)
	fx.fake.hits = []map[string]any{{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world"}}
	if err := fx.st.Close(); err != nil {
		t.Log(err)
	}
	cs := connect(t, fx.srv, "tok", "read+clip+admin")

	for _, tc := range []struct { //nolint:govet // test table favors readability over field alignment
		name string
		call func()
	}{
		{"search_quotes", func() { callToolErr(t, cs, "search_quotes", map[string]any{"query": "hello"}) }},
		{"check_quote_exists", func() { callToolErr(t, cs, "check_quote_exists", map[string]any{"query": "x", "title": "Gone"}) }},
		{"get_scene_by_line", func() { callToolErr(t, cs, "get_scene_by_line", map[string]any{"chunkId": indexer.DocID(fx.id, 0, 1)}) }},
		{"list_media", func() { callToolErr(t, cs, "list_media", map[string]any{}) }},
		{"get_media", func() { callToolErr(t, cs, "get_media", map[string]any{"mediaId": fx.id}) }},
		{"get_job", func() { callToolErr(t, cs, "get_job", map[string]any{"jobId": 1}) }},
		{"create_clip", func() {
			callToolErr(t, cs, "create_clip", map[string]any{"mediaId": fx.id, "start": 1, "end": 2, "mode": "fast"})
		}},
		{"get_thumbnail", func() { callToolErr(t, cs, "get_thumbnail", map[string]any{"mediaId": fx.id, "timeSec": 1}) }},
		{"trigger_scan", func() { callToolErr(t, cs, "trigger_scan", map[string]any{}) }},
		{"reindex_library", func() { callToolErr(t, cs, "reindex_library", map[string]any{}) }},
		{"queue_control", func() { callToolErr(t, cs, "queue_control", map[string]any{"action": "pause"}) }},
		{"list_jobs", func() { callToolErr(t, cs, "list_jobs", map[string]any{}) }},
		{"get_library_config", func() { callToolErr(t, cs, "get_library_config", map[string]any{}) }},
		{"set_library_config", func() { callToolErr(t, cs, "set_library_config", map[string]any{"folders": []string{"Shows"}}) }},
	} {
		t.Run(tc.name, func(_ *testing.T) {
			tc.call()
		})
	}

	// Resource reads fail against a closed store too.
	if _, err := fx.srv.readMediaResource(context.Background(), &mcpsdk.ReadResourceRequest{
		Params: &mcpsdk.ReadResourceParams{URI: "quipster://media/1"},
	}); err == nil {
		t.Error("media resource must fail on a closed store")
	}
}
