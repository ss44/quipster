package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"quipster/internal/clips"
	"quipster/internal/diagnostics"
	"quipster/internal/events"
	"quipster/internal/hardware"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	return st
}

// newMeili starts a fake Meilisearch server. A non-200 status makes every
// request fail, which exercises the unavailable paths.
func newMeili(t *testing.T, status int, hits []map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			if _, err := w.Write([]byte(`{"status":"available"}`)); err != nil {
				t.Errorf("write health: %v", err)
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/search") {
			body := map[string]any{
				"hits":             hits,
				"query":            "",
				"processingTimeMs": 0,
				"totalHits":        len(hits),
				"totalPages":       1,
				"page":             1,
				"hitsPerPage":      20,
			}
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Errorf("encode search: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func buildDeps(t *testing.T, st *store.Store, meiliURL string) Deps {
	t.Helper()
	ix := indexer.New(st, meiliURL, "")
	clipSvc := clips.New(st, clips.Config{
		CacheDir:     t.TempDir(),
		MaxClipSec:   60,
		DefaultPadMs: 250,
		LongCutSec:   5,
	})
	thumbSvc := &thumbs.Service{St: st, CacheDir: t.TempDir()}
	runner := jobs.NewRunner(st, "test")
	t.Cleanup(runner.Stop)
	return Deps{
		St:      st,
		Search:  search.New(st, ix),
		Indexer: ix,
		Clips:   clipSvc,
		Thumbs:  thumbSvc,
		Events:  &events.Collector{St: st, MeiliUp: ix.Healthy},
		Diagnostics: diagnostics.New(st, hardware.MonitorFor(hardware.Capabilities{}), nil, ix.Healthy,
			"test", t.TempDir(), t.TempDir(), t.TempDir()),
		Runner:        runner,
		MediaDir:      t.TempDir(),
		CacheMaxBytes: 10 << 30,
		WebFS: fstest.MapFS{
			"index.html":       &fstest.MapFile{Data: []byte("<html>index</html>")},
			"app.js":           &fstest.MapFile{Data: []byte("console.log(1)")},
			"site.webmanifest": &fstest.MapFile{Data: []byte("{}")},
		},
	}
}

func newEnv(t *testing.T) (*store.Store, Deps) {
	t.Helper()
	st := newStore(t)
	return st, buildDeps(t, st, newMeili(t, http.StatusOK, nil))
}

func seedMedia(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia("/media/show-s01e01.mkv", "matroska", 1234, 1700000000, "Pilot", 2020, 1, 1, "Show")
	if err != nil {
		t.Fatalf("UpsertMedia: %v", err)
	}
	if err := st.UpdateMediaProbe(id, 600, 1920, 1080, "h264", "aac", "matroska", 0, 0, "eng"); err != nil {
		t.Fatalf("UpdateMediaProbe: %v", err)
	}
	if err := st.SetMediaStatus(id, "indexed", ""); err != nil {
		t.Fatalf("SetMediaStatus: %v", err)
	}
	return id
}

func seedChunks(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	trackID, err := st.ReplaceTrack(id, &store.SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatalf("ReplaceTrack: %v", err)
	}
	cues := make([]store.Cue, 0, 4)
	for i := 0; i < 4; i++ {
		cues = append(cues, store.Cue{
			MediaFileID: id,
			TrackID:     trackID,
			Idx:         i,
			StartSec:    float64(i),
			EndSec:      float64(i) + 1,
			Text:        fmt.Sprintf("line %d", i),
		})
	}
	if err := st.ReplaceCues(id, trackID, cues); err != nil {
		t.Fatalf("ReplaceCues: %v", err)
	}
	chunks := []store.Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 1, Text: "hello world"},
		{MediaFileID: id, StartCueIdx: 2, EndCueIdx: 3, Text: "second chunk"},
	}
	if err := st.ReplaceChunks(id, chunks); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}
}

// seedIndexedChunks stores two quotable chunks and latches them as indexed so
// quote/examples endpoints can surface them.
func seedIndexedChunks(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if err := st.ReplaceChunks(id, []store.Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 1, Text: "Show me the money!"},
		{MediaFileID: id, StartCueIdx: 2, EndCueIdx: 3, Text: "the city never sleeps"},
	}); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}
	chunks, err := st.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(chunks))
	for _, ch := range chunks {
		ids = append(ids, ch.ID)
	}
	if err := st.MarkChunksIndexed(ids, 1); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return v
}
