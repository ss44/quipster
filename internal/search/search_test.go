package search

import (
	"testing"

	"quipster/internal/indexer"
	"quipster/internal/store"
)

// newTestIndexer builds an indexer bound to the fake Meilisearch URL.
func newTestIndexer(st *store.Store, f *fakeSearch) *indexer.Indexer {
	return indexer.New(st, f.srv.URL, "")
}

// searchFixture holds the wired pieces most search tests need.
type searchFixture struct {
	svc *Service
	st  *store.Store
	f   *fakeSearch
	id  int64
}

// newSearchFixture wires a Service against the fake Meili and a fresh store,
// then seeds one media with chunks (f.id) and one subtitle-less media.
func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := newFakeSearch(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	return &searchFixture{svc: New(st, newTestIndexer(st, f)), st: st, f: f, id: id}
}

func TestCueDTOs(t *testing.T) {
	got := CueDTOs([]store.Cue{
		{Idx: 2, StartSec: 1.5, EndSec: 2.5, Text: "hi"},
		{Idx: 3, StartSec: 2.5, EndSec: 3, Text: "there"},
	})
	if len(got) != 2 || got[0].Idx != 2 || got[0].Start != 1.5 || got[0].Text != "hi" {
		t.Errorf("CueDTOs = %+v", got)
	}
	if empty := CueDTOs(nil); empty == nil || len(empty) != 0 {
		t.Errorf("CueDTOs(nil) = %#v, want empty non-nil slice", empty)
	}
}

func TestMediaTitleFor(t *testing.T) {
	cases := []struct {
		m    store.MediaFile
		want string
	}{
		{store.MediaFile{ShowTitle: "Lost", Season: 1, Episode: 1}, "Lost S01E01"},
		{store.MediaFile{ShowTitle: "Lost", Season: 0, Episode: 0}, "Lost"},
		{store.MediaFile{ShowTitle: "Lost", Season: 1, Episode: 0}, "Lost"},
		{store.MediaFile{Title: "The Matrix"}, "The Matrix"},
		{store.MediaFile{}, ""},
	}
	for _, c := range cases {
		if got := mediaTitleFor(&c.m); got != c.want {
			t.Errorf("mediaTitleFor(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestParseDocID(t *testing.T) {
	cases := []struct {
		id         string
		mediaID    int64
		start, end int
		ok         bool
	}{
		{"3_0-5", 3, 0, 5, true},
		{"12_34-56", 12, 34, 56, true},
		{"nounderscore", 0, 0, 0, false},
		{"x_0-5", 0, 0, 0, false},
		{"1_abc", 0, 0, 0, false},
		{"1_0", 0, 0, 0, false},
	}
	for _, c := range cases {
		mediaID, start, end, ok := parseDocID(c.id)
		if ok != c.ok || mediaID != c.mediaID || start != c.start || end != c.end {
			t.Errorf("parseDocID(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)", c.id, mediaID, start, end, ok, c.mediaID, c.start, c.end, c.ok)
		}
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`a"b\c`); got != `a\"b\\c` {
		t.Errorf("escape = %q, want %q", got, `a\"b\\c`)
	}
}

func TestContainerExt(t *testing.T) {
	cases := map[string]string{
		"matroska,webm": "mkv",
		"webm":          "mkv",
		"mov,mp4":       "mp4",
		"mp4":           "mp4",
		"quicktime":     "",
		"avi":           "",
	}
	for in, want := range cases {
		if got := containerExt(in); got != want {
			t.Errorf("containerExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchJoinsHitsToStore(t *testing.T) {
	fx := newSearchFixture(t)
	if err := fx.st.MarkIndexed(fx.id); err != nil {
		t.Fatal(err)
	}
	fx.f.hits = []map[string]any{
		// Well-formed hit with a formatted highlight.
		{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world", "_formatted": map[string]any{"text": "hello <mark>world</mark>"}},
		// Hit without _formatted: falls back to the chunk text.
		{"id": indexer.DocID(fx.id, 2, 3), "text": "goodbye moon"},
		// Stale doc: no matching chunk in SQLite.
		{"id": indexer.DocID(fx.id, 7, 8), "text": "gone"},
		// Malformed ids are skipped.
		{"id": "garbage", "text": "x"},
		{"text": "missing id key"},
		{"id": 42, "text": "non-string id"},
		// Chunk exists but media row was deleted.
		{"id": indexer.DocID(999, 0, 1), "text": "orphan"},
	}
	fx.f.total, fx.f.pages = 7, 2

	resp, err := fx.svc.Search("hello", "", 1, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertSearchEcho(t, resp)
	if len(resp.Hits) != 2 {
		t.Fatalf("hits = %d, want 2 (%+v)", len(resp.Hits), resp.Hits)
	}
	assertHitCore(t, fx.id, &resp.Hits[0])
	assertHitContext(t, &resp.Hits[0], &resp.Hits[1])
}

// assertSearchEcho checks that pagination and totals round-trip.
func assertSearchEcho(t *testing.T, resp *Response) {
	t.Helper()
	if resp.Query != "hello" || resp.Page != 1 || resp.PerPage != 20 {
		t.Errorf("echo fields wrong: %+v", resp)
	}
	if resp.TotalHits != 7 || resp.TotalPages != 2 {
		t.Errorf("totals = %d/%d, want 7/2", resp.TotalHits, resp.TotalPages)
	}
	// q "hello" matches no titles.
	if len(resp.TitleMatches) != 0 {
		t.Errorf("TitleMatches = %+v, want none", resp.TitleMatches)
	}
}

// assertHitCore checks chunk joining and metadata of the first hit.
func assertHitCore(t *testing.T, id int64, h *Hit) {
	t.Helper()
	if h.ChunkID != indexer.DocID(id, 0, 1) || h.Text != "hello world" || h.TextMarked != "hello <mark>world</mark>" {
		t.Errorf("hit 1 = %+v", h)
	}
	if h.MediaID != id || h.MediaTitle != "Lost S01E01" || h.ShowTitle != "Lost" || h.Season != 1 || h.Episode != 1 || h.Year != 2004 {
		t.Errorf("hit 1 metadata wrong: %+v", h)
	}
	if h.ContainerExt != "mkv" || h.FilePath != "/tv/lost-s01e01.mkv" {
		t.Errorf("hit 1 file info wrong: %+v", h)
	}
}

// assertHitContext checks cue spans, context and thumb URLs of both hits.
func assertHitContext(t *testing.T, h1, h2 *Hit) {
	t.Helper()
	if h1.StartSec != 0 || h1.EndSec != 2 {
		t.Errorf("hit 1 span = %v-%v, want 0-2", h1.StartSec, h1.EndSec)
	}
	if h1.ThumbURL != "/api/thumb/1?t=0.0&w=320" {
		t.Errorf("thumb url = %q", h1.ThumbURL)
	}
	if len(h1.Cues) != 2 || len(h1.Before) != 0 || len(h1.After) != 1 {
		t.Errorf("hit 1 cues/before/after = %d/%d/%d, want 2/0/1", len(h1.Cues), len(h1.Before), len(h1.After))
	}
	if h2.TextMarked != "goodbye moon" || h2.ContainerExt != "mkv" {
		t.Errorf("hit 2 = %+v", h2)
	}
	if h2.ThumbURL != "/api/thumb/1?t=2.0&w=320" {
		t.Errorf("thumb url 2 = %q", h2.ThumbURL)
	}
}

func TestSearchTitleFallbackForNoSubs(t *testing.T) {
	fx := newSearchFixture(t)
	if err := fx.st.MarkIndexed(fx.id); err != nil {
		t.Fatal(err)
	}
	// Only the subtitle-less media matches "Gone".
	resp, err := fx.svc.Search("Gone", "", 1, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("hits = %d, want 0", len(resp.Hits))
	}
	if len(resp.TitleMatches) != 1 {
		t.Fatalf("TitleMatches = %+v, want 1", resp.TitleMatches)
	}
	tm := resp.TitleMatches[0]
	if tm.MediaID != 2 || tm.Title != "Gone in 60 Seconds" {
		t.Errorf("title match = %+v", tm)
	}
	if !tm.NoSubtitle || tm.Status != "no_subs" || tm.ThumbURL != "/api/thumb/2?t=5.0&w=320" {
		t.Errorf("no-subtitle flag wrong: %+v", tm)
	}
}

func TestSearchTitleFallbackForIndexed(t *testing.T) {
	fx := newSearchFixture(t)
	if err := fx.st.MarkIndexed(fx.id); err != nil {
		t.Fatal(err)
	}
	// An indexed media that did not appear as a hit gets NoSubtitle=false.
	resp, err := fx.svc.Search("lost", "", 1, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.TitleMatches) != 1 || resp.TitleMatches[0].MediaID != fx.id {
		t.Fatalf("TitleMatches = %+v, want only media %d", resp.TitleMatches, fx.id)
	}
	if resp.TitleMatches[0].NoSubtitle {
		t.Errorf("indexed media must not be flagged noSubtitle: %+v", resp.TitleMatches[0])
	}
}

func TestSearchTitleDedupAgainstChunkHits(t *testing.T) {
	fx := newSearchFixture(t)
	if err := fx.st.MarkIndexed(fx.id); err != nil {
		t.Fatal(err)
	}
	// The media already surfaced as a chunk hit: it must not be duplicated
	// as a title match.
	fx.f.hits = []map[string]any{{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world"}}
	resp, err := fx.svc.Search("lost", "", 1, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(resp.Hits))
	}
	if len(resp.TitleMatches) != 0 {
		t.Fatalf("TitleMatches = %+v, want deduplicated to none", resp.TitleMatches)
	}
}

func TestSearchPaginationAndFilter(t *testing.T) {
	fx := newSearchFixture(t)

	// Defaults: perPage<=0 or >50 becomes 20; page<=0 becomes 1.
	if _, err := fx.svc.Search("x", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	assertSearchReq(t, fx.f, 20, 1, "")
	if _, err := fx.svc.Search("x", "", -1, 100); err != nil {
		t.Fatal(err)
	}
	assertSearchReq(t, fx.f, 20, 1, "")
	// In-range values pass through.
	if _, err := fx.svc.Search("x", "", 2, 30); err != nil {
		t.Fatal(err)
	}
	assertSearchReq(t, fx.f, 30, 2, "")
	// A show filter is escaped and passed along.
	if _, err := fx.svc.Search("x", `Fa"ke\`, 1, 20); err != nil {
		t.Fatal(err)
	}
	assertSearchReq(t, fx.f, 20, 1, `showTitle = "Fa\"ke\\"`)
	if fx.f.reqCount() != 4 {
		t.Errorf("search calls = %d, want 4", fx.f.reqCount())
	}
}

// assertSearchReq checks the hitsPerPage/page/filter fields of the last
// recorded search request body.
func assertSearchReq(t *testing.T, f *fakeSearch, perPage int, page int, filter string) {
	t.Helper()
	req := f.lastReq()
	if req["hitsPerPage"] != float64(perPage) || req["page"] != float64(page) {
		t.Errorf("hitsPerPage/page = %v/%v, want %d/%d", req["hitsPerPage"], req["page"], perPage, page)
	}
	if filter == "" {
		if req["filter"] != nil {
			t.Errorf("filter = %v, want none", req["filter"])
		}
		return
	}
	if req["filter"] != filter {
		t.Errorf("filter = %v, want %q", req["filter"], filter)
	}
}

func TestSearchEmptyArraysAreNotNull(t *testing.T) {
	fx := newSearchFixture(t)
	resp, err := fx.svc.Search("", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Hits == nil || len(resp.Hits) != 0 {
		t.Errorf("Hits = %#v, want empty non-nil", resp.Hits)
	}
	if resp.TitleMatches == nil || len(resp.TitleMatches) != 0 {
		t.Errorf("TitleMatches = %#v, want empty non-nil (blank query skips fallback)", resp.TitleMatches)
	}
}

func TestSearchMeiliError(t *testing.T) {
	fx := newSearchFixture(t)
	fx.f.fail = true
	resp, err := fx.svc.Search("x", "", 1, 20)
	if err == nil || resp != nil {
		t.Fatalf("want error and nil response, got %v / %+v", err, resp)
	}
}

func TestSuggest(t *testing.T) {
	svc, _, f := newService(t)
	f.hits = []map[string]any{
		{"text": "hello", "_formatted": map[string]any{"text": "hell<mark>o</mark>"}},
		{"text": "hello"}, // duplicate text is deduplicated
		{"text": "world"}, // plain hit keeps empty TextMarked
		{"other": "x"},    // missing text key is skipped
		{"text": 42},      // non-string text is skipped
	}
	if _, err := svc.Suggest("hel", 3); err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if limit := f.lastReq()["limit"]; limit != float64(3) {
		t.Errorf("limit = %v, want 3", limit)
	}
	out, err := svc.Suggest("hel", 0)
	if err != nil {
		t.Fatal(err)
	}
	if limit := f.lastReq()["limit"]; limit != float64(8) {
		t.Errorf("default limit = %v, want 8", limit)
	}
	if len(out) != 2 {
		t.Fatalf("suggestions = %+v, want 2", out)
	}
	if out[0].Text != "hello" || out[0].TextMarked != "hell<mark>o</mark>" {
		t.Errorf("suggestion 0 = %+v", out[0])
	}
	if out[1].Text != "world" || out[1].TextMarked != "" {
		t.Errorf("suggestion 1 = %+v", out[1])
	}
	// Over-limit clamps back to the default.
	if _, err = svc.Suggest("hel", 25); err != nil {
		t.Fatal(err)
	}
	if limit := f.lastReq()["limit"]; limit != float64(8) {
		t.Errorf("clamped limit = %v, want 8", limit)
	}
}

func TestSuggestEmptyAndError(t *testing.T) {
	svc, _, f := newService(t)
	out, err := svc.Suggest("x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out) != 0 {
		t.Errorf("Suggest = %#v, want empty non-nil", out)
	}
	f.fail = true
	if out, err = svc.Suggest("x", 5); err == nil || out != nil {
		t.Fatalf("want error and nil, got %v / %+v", err, out)
	}
}

func TestSuggestFormattedFallback(t *testing.T) {
	svc, _, f := newService(t)
	f.hits = []map[string]any{
		// _formatted present but without text: falls back to empty TextMarked.
		{"text": "hello", "_formatted": map[string]any{"other": 1}},
		// _formatted with an empty text: also falls back.
		{"text": "world", "_formatted": map[string]any{"text": ""}},
		// _formatted that is not valid JSON: skipped highlight, hit kept.
		{"text": "raw", "_formatted": "not-an-object"},
	}
	out, err := svc.Suggest("x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("suggestions = %+v, want 3", out)
	}
	for _, s := range out {
		if s.TextMarked != "" {
			t.Errorf("TextMarked should be empty for %q, got %q", s.Text, s.TextMarked)
		}
	}
}

func TestSearchFormattedPayloadVariants(t *testing.T) {
	fx := newSearchFixture(t)
	fx.f.hits = []map[string]any{
		// _formatted without a text field: falls back to chunk text.
		{"id": indexer.DocID(fx.id, 0, 1), "text": "hello world", "_formatted": map[string]any{"other": 1}},
		// _formatted as garbage JSON: falls back too.
		{"id": indexer.DocID(fx.id, 2, 3), "text": "goodbye moon", "_formatted": "junk"},
	}
	resp, err := fx.svc.Search("x", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(resp.Hits))
	}
	if resp.Hits[0].TextMarked != "hello world" || resp.Hits[1].TextMarked != "goodbye moon" {
		t.Errorf("TextMarked fallback wrong: %+v", resp.Hits)
	}
}

func TestSearchShowFilterBlankIgnored(t *testing.T) {
	fx := newSearchFixture(t)
	if _, err := fx.svc.Search("x", "   ", 1, 20); err != nil {
		t.Fatal(err)
	}
	assertSearchReq(t, fx.f, 20, 1, "")
}
