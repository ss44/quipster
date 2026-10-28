package search

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"quipster/internal/store"
)

// fakeSearch is a minimal Meilisearch stand-in that only serves the search
// endpoint used by the Service, records request bodies for assertions, and
// can be made to fail for error-path tests.
type fakeSearch struct {
	srv   *httptest.Server
	last  map[string]any
	hits  []map[string]any
	total int64
	pages int64
	calls int
	mu    sync.Mutex
	fail  bool
}

// respWriter funnels response writes through checked calls so the handler
// satisfies errcheck without blank-ignoring anything.
type respWriter struct {
	w   http.ResponseWriter
	err error
}

func (rw *respWriter) json(status int, v any) {
	if rw.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		rw.err = err
		return
	}
	rw.w.Header().Set("Content-Type", "application/json")
	rw.w.WriteHeader(status)
	if _, err := rw.w.Write(b); err != nil {
		rw.err = err
	}
}

func (rw *respWriter) fail(status int, code, msg string) {
	rw.json(status, map[string]string{"message": msg, "code": code, "type": "invalid_request", "link": "https://docs.meilisearch.com"})
}

func newFakeSearch(t *testing.T) *fakeSearch {
	t.Helper()
	f := &fakeSearch{hits: []map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rw := &respWriter{w: w}
		f.handle(rw, r, body)
		if rw.err != nil {
			http.Error(w, rw.err.Error(), http.StatusInternalServerError)
		}
	}))
	t.Cleanup(func() {
		f.srv.Close()
	})
	return f
}

func (f *fakeSearch) handle(rw *respWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodPost || r.URL.Path != "/indexes/lines/search" {
		rw.fail(http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path)
		return
	}
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		rw.fail(http.StatusInternalServerError, "search_error", "search failed")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		rw.fail(http.StatusBadRequest, "bad_request", "bad search body")
		return
	}
	f.mu.Lock()
	f.last = req
	f.calls++
	hits := f.hits
	if hits == nil {
		hits = []map[string]any{}
	}
	total, pages := f.total, f.pages
	f.mu.Unlock()
	rw.json(http.StatusOK, map[string]any{
		"hits":               hits,
		"query":              req["q"],
		"processingTimeMs":   1,
		"totalHits":          total,
		"totalPages":         pages,
		"estimatedTotalHits": total,
		"hitsPerPage":        req["hitsPerPage"],
		"page":               req["page"],
	})
}

// lastReq returns the decoded body of the most recent search request.
func (f *fakeSearch) lastReq() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// reqCount returns how many search requests were made.
func (f *fakeSearch) reqCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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

// seedMedia creates one media with cues and two chunks (id1) and one media
// marked no_subs without any subtitles (id2).
func seedMedia(t *testing.T, st *store.Store) (id1, id2 int64) {
	t.Helper()
	id1, _, err := st.UpsertMedia("/tv/lost-s01e01.mkv", "matroska", 100, 1, "Pilot", 2004, 1, 1, "Lost")
	if err != nil {
		t.Fatal(err)
	}
	track, err := st.ReplaceTrack(id1, &store.SubtitleTrack{Source: "embedded", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	cues := make([]store.Cue, 0, 4)
	for i := 0; i < 4; i++ {
		cues = append(cues, store.Cue{
			MediaFileID: id1, TrackID: track, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "cue text",
		})
	}
	err = st.ReplaceCues(id1, track, cues)
	if err != nil {
		t.Fatal(err)
	}
	chunks := []store.Chunk{
		{MediaFileID: id1, StartCueIdx: 0, EndCueIdx: 1, Text: "hello world"},
		{MediaFileID: id1, StartCueIdx: 2, EndCueIdx: 3, Text: "goodbye moon"},
	}
	err = st.ReplaceChunks(id1, chunks)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err = st.UpsertMedia("/movies/gone.mp4", "mp4", 50, 1, "Gone in 60 Seconds", 2000, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	err = st.SetMediaStatus(id2, "no_subs", "")
	if err != nil {
		t.Fatal(err)
	}
	return id1, id2
}

// newService wires a Service against the fake Meili and a fresh store.
func newService(t *testing.T) (*Service, *store.Store, *fakeSearch) {
	t.Helper()
	f := newFakeSearch(t)
	st := newTestStore(t)
	return New(st, newTestIndexer(st, f)), st, f
}
