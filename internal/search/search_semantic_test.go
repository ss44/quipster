package search

import (
	"testing"

	"quipster/internal/indexer"
)

// newSemanticFixture wires a Service whose indexer has an embedder configured
// so hybrid/semantic request construction can be asserted end to end.
func newSemanticFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := newFakeSearch(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := indexer.New(st, f.srv.URL, "", indexer.WithEmbedder(indexer.EmbedderConfig{
		Source:        "openAi",
		APIKey:        "k",
		SemanticRatio: 0.7,
	}))
	return &searchFixture{svc: New(st, ix), st: st, f: f, id: id}
}

// assertHybrid checks the hybrid payload of the last recorded request.
func assertHybrid(t *testing.T, f *fakeSearch, wantRatio float64, want bool) {
	t.Helper()
	hybrid, ok := f.lastReq()["hybrid"].(map[string]any)
	if !want {
		if ok {
			t.Errorf("hybrid = %v, want none", hybrid)
		}
		return
	}
	if !ok {
		t.Fatalf("hybrid missing from request %v", f.lastReq())
	}
	if hybrid["embedder"] != indexer.EmbedderName || hybrid["semanticRatio"] != wantRatio {
		t.Errorf("hybrid = %v, want embedder %q ratio %v", hybrid, indexer.EmbedderName, wantRatio)
	}
}

// assertMatchingStrategy checks the matchingStrategy of the last request.
func assertMatchingStrategy(t *testing.T, f *fakeSearch, want string) {
	t.Helper()
	if got := f.lastReq()["matchingStrategy"]; got != want {
		t.Errorf("matchingStrategy = %v, want %q", got, want)
	}
}

func TestSearchAutoModeResolvesToHybrid(t *testing.T) {
	fx := newSemanticFixture(t)
	resp, err := fx.svc.Search("a quiet evening", "", 1, 20, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertHybrid(t, fx.f, 0.7, true)
	assertMatchingStrategy(t, fx.f, "last")
	if resp.Mode != ModeHybrid || !resp.SemanticAvailable {
		t.Errorf("mode/semanticAvailable = %q/%v", resp.Mode, resp.SemanticAvailable)
	}
	// An unknown mode string behaves like auto.
	if _, err := fx.svc.Search("x", "", 1, 20, "bogus"); err != nil {
		t.Fatal(err)
	}
	assertHybrid(t, fx.f, 0.7, true)
}

func TestSearchExplicitHybridAndSemantic(t *testing.T) {
	fx := newSemanticFixture(t)
	resp, err := fx.svc.Search("x", "", 1, 20, ModeHybrid)
	if err != nil {
		t.Fatal(err)
	}
	assertHybrid(t, fx.f, 0.7, true)
	if resp.Mode != ModeHybrid {
		t.Errorf("mode = %q", resp.Mode)
	}

	resp, err = fx.svc.Search("x", "", 1, 20, ModeSemantic)
	if err != nil {
		t.Fatal(err)
	}
	assertHybrid(t, fx.f, 1, true)
	if resp.Mode != ModeSemantic || resp.SemanticAvailable != true {
		t.Errorf("mode/semanticAvailable = %q/%v", resp.Mode, resp.SemanticAvailable)
	}
	// The show filter must survive vector queries.
	if _, err = fx.svc.Search("x", `Fa"ke\`, 1, 20, ModeSemantic); err != nil {
		t.Fatal(err)
	}
	if got := fx.f.lastReq()["filter"]; got != `showTitle = "Fa\"ke\\"` {
		t.Errorf("filter = %v", got)
	}
}

func TestSearchKeywordModeSkipsVectors(t *testing.T) {
	fx := newSemanticFixture(t)
	resp, err := fx.svc.Search("x", "", 1, 20, ModeKeyword)
	if err != nil {
		t.Fatal(err)
	}
	assertHybrid(t, fx.f, 0, false)
	assertMatchingStrategy(t, fx.f, "all")
	if resp.Mode != ModeKeyword || !resp.SemanticAvailable {
		t.Errorf("mode/semanticAvailable = %q/%v", resp.Mode, resp.SemanticAvailable)
	}
}

func TestSearchBlankQueryStaysKeyword(t *testing.T) {
	fx := newSemanticFixture(t)
	// An empty query cannot drive vector search, so auto stays keyword even
	// when an embedder exists.
	resp, err := fx.svc.Search("", "", 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	assertHybrid(t, fx.f, 0, false)
	if resp.Mode != ModeKeyword {
		t.Errorf("mode = %q, want keyword", resp.Mode)
	}
}

func TestSearchDegradesToKeywordWithoutEmbedder(t *testing.T) {
	fx := newSearchFixture(t)
	for _, mode := range []string{ModeHybrid, ModeSemantic, ""} {
		resp, err := fx.svc.Search("x", "", 1, 20, mode)
		if err != nil {
			t.Fatal(err)
		}
		assertHybrid(t, fx.f, 0, false)
		if resp.Mode != ModeKeyword {
			t.Errorf("mode %q: got %q, want keyword fallback", mode, resp.Mode)
		}
		if resp.SemanticAvailable {
			t.Errorf("mode %q: semanticAvailable = true without embedder", mode)
		}
	}
}

func TestSearchRankingScore(t *testing.T) {
	fx := newSearchFixture(t)
	if err := fx.st.MarkIndexed(fx.id); err != nil {
		t.Fatal(err)
	}
	fx.f.hits = []map[string]any{
		{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world", "_rankingScore": 0.87},
		{"id": indexer.DocID(fx.id, 2, 3), "text": "goodbye moon"},
		{"id": indexer.DocID(fx.id, 0, 1), "text": "junk score", "_rankingScore": "not-a-number"},
	}
	resp, err := fx.svc.Search("x", "", 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(resp.Hits))
	}
	if resp.Hits[0].Score != 0.87 {
		t.Errorf("score = %v, want 0.87", resp.Hits[0].Score)
	}
	if resp.Hits[1].Score != 0 || resp.Hits[2].Score != 0 {
		t.Errorf("missing/malformed scores must be zero: %+v", resp.Hits[1:])
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"  ":        "",
		"keyword":   ModeKeyword,
		"  HYBRID ": ModeHybrid,
		"Semantic":  ModeSemantic,
		"vector":    "",
	}
	for in, want := range cases {
		if got := normalizeMode(in); got != want {
			t.Errorf("normalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
