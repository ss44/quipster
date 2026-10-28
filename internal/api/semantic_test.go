package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"quipster/internal/indexer"
	"quipster/internal/search"
)

// semanticFake is a Meilisearch stand-in that records search request bodies
// so the API layer's mode pass-through can be asserted.
type semanticFake struct {
	srv   *httptest.Server
	last  map[string]any
	hits  []map[string]any
	total int64
	mu    sync.Mutex
}

func newSemanticFake(t *testing.T, hits []map[string]any) *semanticFake {
	t.Helper()
	f := &semanticFake{hits: hits}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"status":"available"}`)); err != nil {
				t.Errorf("write health: %v", err)
			}
		case strings.HasSuffix(r.URL.Path, "/search") && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.last = req
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"hits": hits, "query": req["q"], "processingTimeMs": 1,
				"totalHits": f.total, "totalPages": 1, "page": 1, "hitsPerPage": 20,
			}); err != nil {
				t.Errorf("encode search: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// lastReq returns the most recent captured search request body.
func (f *semanticFake) lastReq() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// withEmbedder rebuilds the deps' search stack around an embedder-enabled
// indexer so semantic endpoints can be exercised end to end.
func withEmbedder(t *testing.T, deps Deps, meiliURL string) Deps {
	t.Helper()
	ix := indexer.New(deps.St, meiliURL, "", indexer.WithEmbedder(indexer.EmbedderConfig{
		Source: "openAi", APIKey: "k", SemanticRatio: 0.5,
	}))
	deps.Indexer = ix
	deps.Search = search.New(deps.St, ix)
	return deps
}

func TestSearchModePassesThroughToMeili(t *testing.T) {
	st := newStore(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	f := newSemanticFake(t, []map[string]any{
		{"id": indexer.DocID(id, 0, 1), "text": "hello world", "_rankingScore": 0.92},
	})
	deps := withEmbedder(t, buildDeps(t, st, f.srv.URL), f.srv.URL)

	rr := do(t, Router(deps), http.MethodGet, "/api/search?q=hello&mode=semantic", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	req := f.last
	f.mu.Unlock()
	hybrid, ok := req["hybrid"].(map[string]any)
	if !ok || hybrid["embedder"] != indexer.EmbedderName || hybrid["semanticRatio"] != float64(1) {
		t.Errorf("hybrid = %v, want semantic ratio 1", req["hybrid"])
	}

	var res search.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Mode != "semantic" || !res.SemanticAvailable {
		t.Errorf("mode/semanticAvailable = %q/%v", res.Mode, res.SemanticAvailable)
	}
	if len(res.Hits) != 1 || res.Hits[0].Score != 0.92 {
		t.Errorf("hits = %+v", res.Hits)
	}

	// A garbage mode is normalized server-side into hybrid.
	rr = do(t, Router(deps), http.MethodGet, "/api/search?q=hello&mode=nonsense", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("garbage mode status = %d", rr.Code)
	}
	var res2 search.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.Mode != "hybrid" {
		t.Errorf("garbage mode resolved to %q, want hybrid", res2.Mode)
	}
}

func TestSearchModeDefaultsToKeywordWithoutEmbedder(t *testing.T) {
	st := newStore(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	f := newSemanticFake(t, nil)
	deps := buildDeps(t, st, f.srv.URL)

	rr := do(t, Router(deps), http.MethodGet, "/api/search?q=hello&mode=semantic", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var res search.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Mode != "keyword" || res.SemanticAvailable {
		t.Errorf("mode/semanticAvailable = %q/%v, want keyword/false", res.Mode, res.SemanticAvailable)
	}
	// The client always emits a "hybrid" key; a keyword fallback must carry
	// a null payload rather than an embedder object.
	if hybrid, ok := f.lastReq()["hybrid"].(map[string]any); ok {
		t.Errorf("keyword fallback sent a hybrid payload: %v", hybrid)
	}
}

func TestHealthReportsSemanticAvailability(t *testing.T) {
	st := newStore(t)
	f := newSemanticFake(t, nil)

	plain := buildDeps(t, st, f.srv.URL)
	rr := do(t, Router(plain), http.MethodGet, "/api/health", nil, nil)
	if body := decodeMap(t, rr); body["semantic"] != false {
		t.Errorf("semantic = %v, want false without embedder", body["semantic"])
	}

	semantic := withEmbedder(t, buildDeps(t, st, f.srv.URL), f.srv.URL)
	rr = do(t, Router(semantic), http.MethodGet, "/api/health", nil, nil)
	if body := decodeMap(t, rr); body["semantic"] != true {
		t.Errorf("semantic = %v, want true with embedder", body["semantic"])
	}
}
