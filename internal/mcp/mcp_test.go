package mcp

import (
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/clips"
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
)

// fakeSearch is a minimal Meilisearch stand-in that serves the search
// endpoint with canned hits and can be made to fail.
type fakeSearch struct {
	srv   *httptest.Server
	hits  []map[string]any
	total int64
	pages int64
	fail  bool
}

func newFakeSearch(t *testing.T) *fakeSearch {
	t.Helper()
	f := &fakeSearch{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"status":"available"}`)); err != nil {
				t.Errorf("write health: %v", err)
			}
			return
		}
		if f.fail || r.Method != http.MethodPost || r.URL.Path != "/indexes/lines/search" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"hits": f.hits, "query": req["q"], "processingTimeMs": 1,
			"totalHits": f.total, "totalPages": f.pages,
			"estimatedTotalHits": f.total, "hitsPerPage": req["hitsPerPage"], "page": req["page"],
		}); err != nil {
			t.Errorf("encode search response: %v", err)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newTestStore opens a throwaway SQLite store for one test.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Log(err)
		}
	})
	return st
}

// seedLibrary inserts one indexed media with cues and two chunks plus one
// subtitle-less media, mirroring the REST search fixtures.
func seedLibrary(t *testing.T, st *store.Store) (id1, id2 int64) {
	t.Helper()
	id1, _, err := st.UpsertMedia("/tv/lost-s01e01.mkv", "matroska", 100, 1, "Pilot", 2004, 1, 1, "Lost")
	if err != nil {
		t.Fatal(err)
	}
	track, terr := st.ReplaceTrack(id1, &store.SubtitleTrack{Source: "embedded", Lang: "en"})
	if terr != nil {
		t.Fatal(terr)
	}
	cues := make([]store.Cue, 0, 4)
	for i := 0; i < 4; i++ {
		cues = append(cues, store.Cue{
			MediaFileID: id1, TrackID: track, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "cue text",
		})
	}
	if cerr := st.ReplaceCues(id1, track, cues); cerr != nil {
		t.Fatal(cerr)
	}
	chunks := []store.Chunk{
		{MediaFileID: id1, StartCueIdx: 0, EndCueIdx: 1, Text: "hello world"},
		{MediaFileID: id1, StartCueIdx: 2, EndCueIdx: 3, Text: "goodbye moon"},
	}
	if cerr := st.ReplaceChunks(id1, chunks); cerr != nil {
		t.Fatal(cerr)
	}
	id2, _, err = st.UpsertMedia("/movies/gone.mp4", "mp4", 50, 1, "Gone in 60 Seconds", 2000, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMediaStatus(id2, "no_subs", ""); err != nil {
		t.Fatal(err)
	}
	return id1, id2
}

// testDeps wires the service stack the MCP facade delegates to.
func testDeps(t *testing.T, st *store.Store, fake *fakeSearch) Deps {
	t.Helper()
	ix := indexer.New(st, fake.srv.URL, "")
	clipSvc := clips.New(st, clips.Config{
		CacheDir: t.TempDir(), MaxClipSec: 60, DefaultPadMs: 250, LongCutSec: 5,
	})
	runner := jobs.NewRunner(st, "mcp-test")
	t.Cleanup(runner.Stop)
	return Deps{
		St:          st,
		Search:      search.New(st, ix),
		Indexer:     ix,
		Clips:       clipSvc,
		Thumbs:      &thumbs.Service{St: st, CacheDir: t.TempDir()},
		Events:      &events.Collector{St: st, MeiliUp: ix.Healthy},
		Runner:      runner,
		MediaDir:    t.TempDir(),
		ClipWorkers: 1,
	}
}

// fixture holds the fully wired test server plus its seeded data.
type fixture struct {
	srv  *Server
	fake *fakeSearch
	st   *store.Store
	id   int64
}

// newFixture builds the wiring with one admin-capable token and admin
// tools enabled.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := newFakeSearch(t)
	st := newTestStore(t)
	id, _ := seedLibrary(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	srv := New(testDeps(t, st, fake), Config{
		Tokens:     map[string]string{"tok": "read+clip+admin"},
		AllowAdmin: true,
	})
	return &fixture{srv: srv, fake: fake, st: st, id: id}
}

// connect opens a client session against the server variant built for the
// given token and scope list, using the SDK's in-memory transport pair.
func connect(t *testing.T, srv *Server, token, scopes string) *mcpsdk.ClientSession {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cTransport, sTransport := mcpsdk.NewInMemoryTransports()
	sSession, err := srv.serverFor(tokenID(token), scopes).Connect(t.Context(), sTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() {
		if cerr := sSession.Close(); cerr != nil {
			t.Log(cerr)
		}
	})
	cSession, err := client.Connect(t.Context(), cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		if err := cSession.Close(); err != nil {
			t.Log(err)
		}
	})
	return cSession
}

// callTool invokes a tool and fails the test on a tool error result.
func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %+v", name, res.Content)
	}
	return res
}

// callToolErr invokes a tool and returns its tool error text.
func callToolErr(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s unexpectedly succeeded: %+v", name, res.StructuredContent)
	}
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("error content type = %T", res.Content[0])
	}
	return tc.Text
}

// structured decodes a tool result's structured content into a typed value.
func structured[T any](t *testing.T, res *mcpsdk.CallToolResult) T {
	t.Helper()
	var out T
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", b, err)
	}
	return out
}

// toolNames lists the tools visible to a session.
func toolNames(t *testing.T, cs *mcpsdk.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}
