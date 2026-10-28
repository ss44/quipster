package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/clips"
	"quipster/internal/indexer"
)

// readScopes is the scope list of a read-only token.
const readScopes = ScopeRead

func TestToolVisibilityFollowsScopes(t *testing.T) {
	fx := newFixture(t)

	read := toolNames(t, connect(t, fx.srv, "tok", readScopes))
	for _, name := range []string{
		"get_processing_status", "search_quotes", "suggest_quotes", "check_quote_exists",
		"get_scene_by_line", "list_media", "get_media", "get_job",
	} {
		if !read[name] {
			t.Errorf("read token cannot see %s", name)
		}
	}
	for _, name := range []string{"create_clip", "get_thumbnail", "queue_control", "trigger_scan"} {
		if read[name] {
			t.Errorf("read token must not see %s", name)
		}
	}

	clip := toolNames(t, connect(t, fx.srv, "tok", "read+clip"))
	if !clip["create_clip"] || !clip["get_thumbnail"] {
		t.Error("clip token must see create_clip and get_thumbnail")
	}
	if clip["queue_control"] {
		t.Error("clip token must not see admin tools")
	}

	admin := toolNames(t, connect(t, fx.srv, "tok", "read+clip+admin"))
	for _, name := range []string{
		"trigger_scan", "reindex_library", "queue_control", "list_jobs",
		"get_library_config", "set_library_config",
	} {
		if !admin[name] {
			t.Errorf("admin token cannot see %s", name)
		}
	}

	// The master switch hides admin tools even from admin-scoped tokens.
	locked := New(testDeps(t, fx.st, fx.fake), Config{AllowAdmin: false})
	lockedTools := toolNames(t, connect(t, locked, "tok", "read+clip+admin"))
	if lockedTools["queue_control"] {
		t.Error("AllowAdmin=false must hide admin tools")
	}
	if !lockedTools["search_quotes"] {
		t.Error("AllowAdmin=false must keep read tools")
	}
}

func TestToolSchemasAndAnnotations(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", "read+clip+admin")
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcpsdk.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	search, ok := byName["search_quotes"]
	if !ok {
		t.Fatal("search_quotes missing")
	}
	schema, err := json.Marshal(search.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "object" {
		t.Errorf("input schema type = %v, want object", decoded["type"])
	}
	props, ok := decoded["properties"].(map[string]any)
	if !ok || props["query"] == nil {
		t.Errorf("input schema missing query property: %v", decoded)
	}
	// Search is read-only; queue control is destructive.
	if !byName["search_quotes"].Annotations.ReadOnlyHint {
		t.Error("search_quotes must be read-only")
	}
	if byName["queue_control"].Annotations.DestructiveHint == nil || !*byName["queue_control"].Annotations.DestructiveHint {
		t.Error("queue_control must be destructive")
	}
}

func TestSearchQuotesTool(t *testing.T) {
	fx := newFixture(t)
	fx.fake.hits = []map[string]any{
		{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world", "_formatted": map[string]any{"text": "hello <mark>world</mark>"}},
		// Stale doc filtered exactly like the REST search.
		{"id": indexer.DocID(fx.id, 7, 8), "text": "gone"},
	}
	fx.fake.total, fx.fake.pages = 2, 1

	cs := connect(t, fx.srv, "tok", readScopes)
	res := callTool(t, cs, "search_quotes", map[string]any{"query": "hello", "show": "Lost", "perPage": 5})
	out := structured[searchOut](t, res)
	if out.TotalHits != 2 || out.TotalPages != 1 || out.PerPage != 5 {
		t.Fatalf("echo fields wrong: %+v", out)
	}
	if len(out.Hits) != 1 {
		t.Fatalf("hits = %d, want 1 (stale filtered)", len(out.Hits))
	}
	hit := out.Hits[0]
	if hit.ChunkID != indexer.DocID(fx.id, 0, 1) || hit.TextMarked != "hello <mark>world</mark>" ||
		hit.MediaTitle != "Lost S01E01" || hit.ThumbURL == "" || hit.StartSec != 0 || hit.EndSec != 2 {
		t.Errorf("hit = %+v", hit)
	}

	// Highlight=false drops the marked text.
	res = callTool(t, cs, "search_quotes", map[string]any{"query": "hello", "highlight": false})
	out = structured[searchOut](t, res)
	if out.Hits[0].TextMarked != "" {
		t.Errorf("highlight=false kept TextMarked: %+v", out.Hits[0])
	}

	// Search failures surface as tool errors, not protocol errors.
	fx.fake.fail = true
	if msg := callToolErr(t, cs, "search_quotes", map[string]any{"query": "x"}); msg == "" {
		t.Error("expected an error message for a failed search")
	}
}

func TestSuggestAndCheckQuoteTools(t *testing.T) {
	fx := newFixture(t)
	fx.fake.hits = []map[string]any{
		{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world", "_formatted": map[string]any{"text": "hell<mark>o</mark> world"}},
		{"text": "hello"}, // missing doc id: dropped by the search join
	}
	fx.fake.total = 1

	cs := connect(t, fx.srv, "tok", readScopes)
	sg := structured[suggestOut](t, callTool(t, cs, "suggest_quotes", map[string]any{"query": "hel", "limit": 3}))
	if len(sg.Suggestions) != 2 || sg.Suggestions[0].TextMarked != "hell<mark>o</mark> world" {
		t.Errorf("suggestions = %+v", sg.Suggestions)
	}

	// Existence probe: totalHits wins for fuzzy queries.
	out := structured[checkOut](t, callTool(t, cs, "check_quote_exists", map[string]any{"query": "hello"}))
	if !out.Exists || out.MatchCount != 1 || len(out.SampleHits) != 1 {
		t.Errorf("check = %+v", out)
	}
	if out.SampleHits[0].TextMarked != "" {
		t.Errorf("existence probe must not return highlights: %+v", out.SampleHits[0])
	}

	// Exact phrase filtering drops non-verbatim hits.
	out = structured[checkOut](t, callTool(t, cs, "check_quote_exists",
		map[string]any{"query": "hello!", "exactPhrase": true}))
	if out.Exists || out.MatchCount != 0 {
		t.Errorf("exact phrase check = %+v", out)
	}
	out = structured[checkOut](t, callTool(t, cs, "check_quote_exists",
		map[string]any{"query": "hello world", "exactPhrase": true}))
	if !out.Exists || out.MatchCount != 1 {
		t.Errorf("verbatim check = %+v", out)
	}

	// Title fallback matches the subtitle-less media.
	fx.fake.hits = nil
	fx.fake.total = 0
	out = structured[checkOut](t, callTool(t, cs, "check_quote_exists",
		map[string]any{"query": "nothing", "title": "Gone"}))
	if !out.Exists || len(out.TitleMatches) != 1 || out.TitleMatches[0].MediaID != 2 || !out.TitleMatches[0].NoSubtitle {
		t.Errorf("title check = %+v", out)
	}
}

func TestProcessingStatusTool(t *testing.T) {
	fx := newFixture(t)
	if _, err := fx.st.EnqueueJob("probe", `{"mediaId":1}`, 1); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, fx.srv, "tok", readScopes)
	out := structured[statusOut](t, callTool(t, cs, "get_processing_status", map[string]any{"includeRecentJobs": true}))
	if out.Queue == nil || out.Queue.Queued != 1 || out.Queue.Paused {
		t.Errorf("queue = %+v", out.Queue)
	}
	if !out.MeiliUp {
		t.Error("meiliUp should be true against the fake")
	}
	if len(out.RecentJobs) != 1 || out.RecentJobs[0].Type != "probe" || out.RecentJobs[0].Status != "queued" {
		t.Errorf("recent jobs = %+v", out.RecentJobs)
	}
	if out.Lines == nil || out.Lines.Chunks != 2 {
		t.Errorf("lines = %+v", out.Lines)
	}
	if out.MediaCounts["indexed"] != 1 || out.MediaCounts["no_subs"] != 1 {
		t.Errorf("mediaCounts = %v", out.MediaCounts)
	}

	lean := structured[statusOut](t, callTool(t, cs, "get_processing_status", map[string]any{}))
	if lean.RecentJobs != nil {
		t.Errorf("recentJobs requested=false should be omitted, got %+v", lean.RecentJobs)
	}
}

func TestGetSceneByLineTool(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)

	out := structured[sceneOut](t, callTool(t, cs, "get_scene_by_line",
		map[string]any{"chunkId": indexer.DocID(fx.id, 2, 3)}))
	if out.ChunkID != indexer.DocID(fx.id, 2, 3) || out.Text != "goodbye moon" {
		t.Errorf("chunk fields = %+v", out)
	}
	if out.Media.ID != fx.id || out.Media.ShowTitle != "Lost" {
		t.Errorf("media fields = %+v", out.Media)
	}
	// The default context is two cues on each side; the last chunk has no
	// cues after it.
	if len(out.Cues) != 2 || len(out.ContextBefore) != 2 || len(out.ContextAfter) != 0 {
		t.Errorf("context sizes = %d/%d/%d, want 2/2/0", len(out.Cues), len(out.ContextBefore), len(out.ContextAfter))
	}
	if sceneJSON := pathInJSON(t, out); strings.Contains(sceneJSON, "/tv/") || strings.Contains(sceneJSON, "\"path\"") {
		t.Errorf("scene leaked a path: %s", sceneJSON)
	}

	// Resolution by timestamp picks the chunk covering that second.
	out = structured[sceneOut](t, callTool(t, cs, "get_scene_by_line",
		map[string]any{"mediaId": fx.id, "startSec": 2.5}))
	if out.ChunkID != indexer.DocID(fx.id, 2, 3) {
		t.Errorf("time-resolved chunk = %q", out.ChunkID)
	}

	// A wider request stays bounded by the available cues.
	out = structured[sceneOut](t, callTool(t, cs, "get_scene_by_line",
		map[string]any{"chunkId": indexer.DocID(fx.id, 0, 1), "contextCues": 99}))
	if len(out.ContextBefore) != 0 || len(out.ContextAfter) != 2 {
		t.Errorf("first-chunk context = %d/%d", len(out.ContextBefore), len(out.ContextAfter))
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"bad chunk id", map[string]any{"chunkId": "garbage"}, "chunkId must be"},
		{"missing chunk", map[string]any{"chunkId": indexer.DocID(fx.id, 7, 8)}, "not found"},
		{"subtitle-less media has no scenes", map[string]any{"mediaId": 2, "startSec": 1}, "no scene found"},
		{"unknown media", map[string]any{"mediaId": 4242, "startSec": 1}, "no scene found"},
		{"no selector", map[string]any{}, "provide chunkId"},
	} {
		if msg := callToolErr(t, cs, "get_scene_by_line", tc.args); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: got %q, want substring %q", tc.name, msg, tc.want)
		}
	}
}

// pathInJSON serializes the scene output so tests can assert no path leaks.
func pathInJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestListAndGetMediaRedactPaths(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)

	page := structured[listMediaOut](t, callTool(t, cs, "list_media", map[string]any{"query": "Lost", "perPage": 200}))
	if page.Total != 1 || len(page.Items) != 1 || page.PerPage != 50 {
		t.Fatalf("page = %+v", page)
	}
	if pageJSON := pathInJSON(t, page); strings.Contains(pageJSON, "/tv/") || strings.Contains(pageJSON, "\"path\"") {
		t.Errorf("list_media leaked a path: %s", pageJSON)
	}

	detail := structured[getMediaOut](t, callTool(t, cs, "get_media", map[string]any{"mediaId": fx.id}))
	if detail.Media.ID != fx.id || detail.Media.ShowTitle != "Lost" || len(detail.Tracks) != 1 {
		t.Errorf("detail = %+v", detail)
	}
	if detail.CueCount != 4 {
		t.Errorf("cueCount = %d", detail.CueCount)
	}
	if trackJSON := pathInJSON(t, detail.Tracks); strings.Contains(trackJSON, "extracted") {
		t.Errorf("get_media leaked an extracted path: %s", trackJSON)
	}

	if msg := callToolErr(t, cs, "get_media", map[string]any{"mediaId": 4242}); msg != "media 4242 not found" {
		t.Errorf("missing media error = %q", msg)
	}
}

func TestGetJobTool(t *testing.T) {
	fx := newFixture(t)
	id, err := fx.st.EnqueueJob("probe", `{"mediaId":1}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, fx.srv, "tok", readScopes)
	out := structured[getJobOut](t, callTool(t, cs, "get_job", map[string]any{"jobId": id}))
	if out.Job.ID != id || out.Job.Type != "probe" || out.Job.Status != "queued" {
		t.Errorf("job = %+v", out.Job)
	}
	if msg := callToolErr(t, cs, "get_job", map[string]any{"jobId": 9999}); msg != "job 9999 not found" {
		t.Errorf("missing job error = %q", msg)
	}
}

func TestCreateClipTool(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", "read+clip")

	// Validation errors surface as tool errors.
	for _, args := range []map[string]any{
		{"mediaId": fx.id, "start": 10, "end": 5, "mode": "fast"},
		{"mediaId": fx.id, "start": 0, "end": 100, "mode": "fast"},
		{"mediaId": fx.id, "start": 0, "end": 2, "mode": "nope"},
		{"mediaId": 4242, "start": 0, "end": 2, "mode": "fast"},
		{"mediaId": fx.id, "start": 0, "end": 2, "mode": "fast", "padMs": 6000},
	} {
		if msg := callToolErr(t, cs, "create_clip", args); msg == "" {
			t.Errorf("create_clip(%v) unexpectedly succeeded", args)
		}
	}

	// A long accurate cut is queued, mirroring the REST stream endpoint.
	out := structured[createClipOut](t, callTool(t, cs, "create_clip",
		map[string]any{"mediaId": fx.id, "start": 0, "end": 10, "mode": "accurate"}))
	if out.Status != "queued" || out.JobID <= 0 || out.URL == "" || out.CacheKey == "" {
		t.Errorf("queued result = %+v", out)
	}
	if out.DurationSec != 10 {
		t.Errorf("durationSec = %v", out.DurationSec)
	}

	// A cache hit is served without rendering.
	req := clips.Request{MediaID: fx.id, Start: 1, End: 2, Mode: "fast", PadMs: 250}
	key := fx.deps().Clips.CacheKey(req)
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("clipdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fx.st.UpsertCacheEntry(key, path, "clip", 8); err != nil {
		t.Fatal(err)
	}
	out = structured[createClipOut](t, callTool(t, cs, "create_clip",
		map[string]any{"mediaId": fx.id, "start": 1, "end": 2, "mode": "fast", "padMs": 250, "download": true}))
	if out.Status != "ready" || out.CacheKey != key {
		t.Errorf("cached result = %+v", out)
	}
	if !strings.Contains(out.URL, "download=1") || !strings.Contains(out.URL, "/api/clips/stream") {
		t.Errorf("clip url = %q", out.URL)
	}
}

func TestGetThumbnailTool(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", "read+clip")
	out := structured[thumbOut](t, callTool(t, cs, "get_thumbnail",
		map[string]any{"mediaId": fx.id, "timeSec": 12.3, "width": 999}))
	if out.URL != "/api/thumb/1?t=12.3&w=320" || out.Width != 320 {
		t.Errorf("thumb = %+v", out)
	}
	out = structured[thumbOut](t, callTool(t, cs, "get_thumbnail",
		map[string]any{"mediaId": fx.id, "timeSec": 3, "width": 640}))
	if out.Width != 640 || !strings.Contains(out.URL, "w=640") {
		t.Errorf("thumb = %+v", out)
	}
	if msg := callToolErr(t, cs, "get_thumbnail", map[string]any{"mediaId": 4242, "timeSec": 1}); msg != "media 4242 not found" {
		t.Errorf("missing media error = %q", msg)
	}
}

func TestAdminToolsScanAndReindex(t *testing.T) {
	fx := newFixture(t)
	if err := fx.st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fx.deps().MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, fx.srv, "tok", "read+clip+admin")

	res := structured[scanOut](t, callTool(t, cs, "trigger_scan", map[string]any{}))
	if res.Status != "started" || res.JobID <= 0 {
		t.Fatalf("scan = %+v", res)
	}
	// A second scan is deduplicated against the queued one.
	res = structured[scanOut](t, callTool(t, cs, "trigger_scan", map[string]any{}))
	if res.Status != "already-running" {
		t.Errorf("dedup scan = %+v", res)
	}
	if _, err := fx.st.ClearQueuedJobs(); err != nil {
		t.Fatal(err)
	}
	if msg := callToolErr(t, cs, "trigger_scan", map[string]any{"root": "/abs"}); msg != "invalid scan root" {
		t.Errorf("bad root error = %q", msg)
	}

	re := structured[reindexOut](t, callTool(t, cs, "reindex_library", map[string]any{}))
	if re.Enqueued != 1 {
		t.Errorf("reindex enqueued = %d, want 1 media", re.Enqueued)
	}
}

func TestAdminToolsQueueAndConfig(t *testing.T) {
	fx := newFixture(t)
	if err := fx.st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fx.deps().MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, fx.srv, "tok", "read+clip+admin")

	out := structured[queueOut](t, callTool(t, cs, "queue_control", map[string]any{"action": "pause"}))
	if !out.Paused {
		t.Errorf("pause = %+v", out)
	}
	out = structured[queueOut](t, callTool(t, cs, "queue_control", map[string]any{"action": "resume"}))
	if out.Paused {
		t.Errorf("resume = %+v", out)
	}
	if msg := callToolErr(t, cs, "queue_control", map[string]any{"action": "detonate"}); msg == "" {
		t.Error("invalid action must fail")
	}

	cfg := structured[libraryConfigOut](t, callTool(t, cs, "get_library_config", map[string]any{}))
	if len(cfg.Folders) != 1 || cfg.Folders[0] != "Shows" || len(cfg.Available) != 1 {
		t.Errorf("config = %+v", cfg)
	}
	set := structured[libraryConfigOut](t, callTool(t, cs, "set_library_config", map[string]any{"folders": []string{"Shows", "Shows"}}))
	if len(set.Folders) != 1 {
		t.Errorf("set config = %+v", set)
	}
	for _, folders := range [][]any{
		{"../escape"}, {"Missing"}, {"/abs"},
	} {
		if msg := callToolErr(t, cs, "set_library_config", map[string]any{"folders": folders}); msg == "" {
			t.Errorf("set_library_config(%v) unexpectedly succeeded", folders)
		}
	}
}

func TestAdminToolsListJobs(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", "read+clip+admin")
	jobs := structured[listJobsOut](t, callTool(t, cs, "list_jobs", map[string]any{"limit": 5, "mediaId": 2}))
	if len(jobs.Jobs) != 0 || jobs.Stats == nil {
		t.Errorf("filtered jobs = %+v", jobs)
	}
	if _, err := fx.st.EnqueueJob("probe", `{"mediaId":2}`, 1); err != nil {
		t.Fatal(err)
	}
	jobs = structured[listJobsOut](t, callTool(t, cs, "list_jobs", map[string]any{"mediaId": 2}))
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].MediaID != 2 || jobs.Jobs[0].WorkerID != "" {
		t.Errorf("jobs = %+v", jobs.Jobs)
	}
}

func TestResourcesAndPrompts(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)

	res, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: fmt.Sprintf("quipster://media/%d", fx.id)})
	if err != nil {
		t.Fatalf("read media: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("contents = %+v", res.Contents)
	}
	var media map[string]any
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &media); err != nil {
		t.Fatal(err)
	}
	if media["id"] != float64(fx.id) {
		t.Errorf("media resource = %v", media)
	}
	if _, hasPath := media["path"]; hasPath {
		t.Error("media resource leaked the path")
	}
	if _, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "quipster://media/4242"}); err == nil {
		t.Error("missing media resource must fail")
	}
	if _, err := cs.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "quipster://media/notanumber"}); err == nil {
		t.Error("malformed media resource must fail")
	}
}

func TestPromptsListed(t *testing.T) {
	fx := newFixture(t)
	cs := connect(t, fx.srv, "tok", readScopes)
	prompts, err := cs.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range prompts.Prompts {
		got[p.Name] = true
	}
	for _, want := range []string{"find_and_clip_scene", "summarize_show_quotes", "audit_processing_queue"} {
		if !got[want] {
			t.Errorf("prompt %s missing", want)
		}
	}
	pr, err := cs.GetPrompt(t.Context(), &mcpsdk.GetPromptParams{Name: "find_and_clip_scene", Arguments: map[string]string{"query": "show me the money"}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := pr.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok || len(pr.Messages) != 1 || !strings.Contains(text.Text, "show me the money") {
		t.Errorf("prompt = %+v", pr)
	}
	if _, err := cs.GetPrompt(t.Context(), &mcpsdk.GetPromptParams{Name: "find_and_clip_scene"}); err == nil {
		t.Error("missing required prompt argument must fail")
	}
}

func TestScopeRanking(t *testing.T) {
	cases := []struct {
		granted, required string
		want              bool
	}{
		{"read", "read", true},
		{"read", "clip", false},
		{"clip", "read", true},
		{"clip", "clip", true},
		{"clip", "admin", false},
		{"admin", "read", true},
		{"admin", "admin", true},
		{"", "read", false},
		{"bogus", "read", false},
	}
	for _, tc := range cases {
		if got := allows(tc.granted, tc.required); got != tc.want {
			t.Errorf("allows(%q, %q) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
	// A blank grant list implicitly grants read-only access.
	if !hasScope("", ScopeRead) {
		t.Error("blank grants must imply read")
	}
}

func TestParseTokens(t *testing.T) {
	got := ParseTokens("a:read+clip, b:admin,c,,:junk")
	if len(got) != 3 {
		t.Fatalf("tokens = %v", got)
	}
	if got["a"] != "read+clip" || got["b"] != "admin" || got["c"] != ScopeRead {
		t.Errorf("scopes = %v", got)
	}
}

// deps exposes the wired service stack for cache seeding in tests.
func (f *fixture) deps() Deps {
	return f.srv.deps
}
