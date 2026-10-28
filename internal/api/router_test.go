// Package api exercises the HTTP router and its handlers.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		Runner:   runner,
		MediaDir: t.TempDir(),
		WebFS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<html>index</html>")},
			"app.js":     &fstest.MapFile{Data: []byte("console.log(1)")},
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

func TestHealth(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/health", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header = %q", got)
	}
	body := decodeMap(t, rr)
	if body["ok"] != true || body["meiliUp"] != true {
		t.Errorf("health body = %v", body)
	}
}

func TestHealthMeiliDown(t *testing.T) {
	st := newStore(t)
	deps := buildDeps(t, st, newMeili(t, http.StatusInternalServerError, nil))
	rr := do(t, Router(deps), http.MethodGet, "/api/health", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if body := decodeMap(t, rr); body["meiliUp"] != false {
		t.Errorf("meiliUp = %v, want false", body["meiliUp"])
	}
}

func TestSuggest(t *testing.T) {
	hits := []map[string]any{
		{"text": "hello", "_formatted": map[string]any{"text": "<mark>hello</mark>"}},
		{"text": "hello"},
		{"text": 42},
	}
	st := newStore(t)
	deps := buildDeps(t, st, newMeili(t, http.StatusOK, hits))

	rr := do(t, Router(deps), http.MethodGet, "/api/suggest?q=hel", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	suggestions, ok := body["suggestions"].([]any)
	if !ok || len(suggestions) != 1 {
		t.Fatalf("suggestions = %v", body["suggestions"])
	}
	first, ok := suggestions[0].(map[string]any)
	if !ok || first["textMarked"] != "<mark>hello</mark>" {
		t.Errorf("suggestion = %v", suggestions[0])
	}
}

func TestSuggestBlankQuery(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/suggest?q=%20%20", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Suggestions []any `json:"suggestions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Suggestions) != 0 {
		t.Errorf("suggestions = %v, want empty", body.Suggestions)
	}
}

func TestSuggestAndSearchUnavailable(t *testing.T) {
	st := newStore(t)
	deps := buildDeps(t, st, newMeili(t, http.StatusInternalServerError, nil))
	h := Router(deps)
	for _, target := range []string{"/api/suggest?q=x", "/api/search?q=x"} {
		rr := do(t, h, http.MethodGet, target, nil, nil)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", target, rr.Code)
		}
	}
}

func TestSearch(t *testing.T) {
	st := newStore(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	other, _, err := st.UpsertMedia("/media/hello.mp4", "mp4", 10, 20, "Hello", 2021, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMediaStatus(other, "indexed", ""); err != nil {
		t.Fatal(err)
	}
	hits := []map[string]any{
		{"id": 12},
		{"id": "garbage"},
		{"id": "999_0-1"},
		{"id": fmt.Sprintf("%d_0-1", id), "text": "hello world", "_formatted": map[string]any{"text": "<mark>hello</mark> world"}},
	}
	deps := buildDeps(t, st, newMeili(t, http.StatusOK, hits))

	rr := do(t, Router(deps), http.MethodGet,
		"/api/search?q=Hello&show=Show&page=2&perPage=5", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var res search.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Page != 2 || res.PerPage != 5 || len(res.Hits) != 1 {
		t.Fatalf("response = %+v", res)
	}
	if res.Hits[0].TextMarked != "<mark>hello</mark> world" {
		t.Errorf("marked = %q", res.Hits[0].TextMarked)
	}
	if len(res.TitleMatches) != 1 || res.TitleMatches[0].MediaID != other {
		t.Errorf("title matches = %+v", res.TitleMatches)
	}
}

func TestMediaList(t *testing.T) {
	st, deps := newEnv(t)
	seedMedia(t, st)
	second, _, err := st.UpsertMedia("/media/other.mp4", "mp4", 10, 20, "Other", 2021, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMediaStatus(second, "no_subs", ""); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	rr := do(t, h, http.MethodGet, "/api/media?page=0&perPage=0&q=Other", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var page struct {
		Items []store.MediaFile `json:"items"`
		Total int64             `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Title != "Other" {
		t.Fatalf("page = %+v", page)
	}

	rr = do(t, h, http.MethodGet, "/api/media?perPage=500", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Errorf("expected both media, got %+v", page)
	}
}

func TestMediaDetail(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	h := Router(deps)

	rr := do(t, h, http.MethodGet, fmt.Sprintf("/api/media/%d", id), nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	if body["cueCount"] != float64(4) {
		t.Errorf("cueCount = %v", body["cueCount"])
	}
	tracks, ok := body["tracks"].([]any)
	if !ok || len(tracks) != 1 {
		t.Errorf("tracks = %v", body["tracks"])
	}

	if rr := do(t, h, http.MethodGet, "/api/media/abc", nil, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/api/media/4242", nil, nil); rr.Code != http.StatusNotFound {
		t.Errorf("missing status = %d, want 404", rr.Code)
	}
}

func TestThumb(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)

	if rr := do(t, Router(deps), http.MethodGet, "/api/thumb/abc", nil, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", rr.Code)
	}
	if rr := do(t, Router(deps), http.MethodGet, "/api/thumb/999", nil, nil); rr.Code != http.StatusInternalServerError {
		t.Errorf("missing media status = %d, want 500", rr.Code)
	}

	// Seed the cache entry the thumbs service would produce for this bucket.
	sum := sha256.Sum256([]byte(fmt.Sprintf("thumb|%d|%.0f|%d", id, 5.0, 320)))
	key := hex.EncodeToString(sum[:16])
	path := filepath.Join(t.TempDir(), "thumb.jpg")
	if err := os.WriteFile(path, []byte("jpegdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCacheEntry(key, path, "thumb", int64(len("jpegdata"))); err != nil {
		t.Fatal(err)
	}
	rr := do(t, Router(deps), http.MethodGet, fmt.Sprintf("/api/thumb/%d?t=5&w=100", id), nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("cached status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("content type = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("cache control = %q", got)
	}
	if rr.Body.String() != "jpegdata" {
		t.Errorf("body = %q", rr.Body.String())
	}

	// An uncached thumb must fail cleanly when ffmpeg cannot run.
	t.Setenv("FFMPEG_BIN", filepath.Join(t.TempDir(), "missing-ffmpeg"))
	rr = do(t, Router(deps), http.MethodGet, fmt.Sprintf("/api/thumb/%d?t=10&w=640", id), nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("render failure status = %d, want 500", rr.Code)
	}
}

func TestChunk(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	h := Router(deps)

	rr := do(t, h, http.MethodGet, fmt.Sprintf("/api/chunks/%d_0-1", id), nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	if body["chunk"] == nil || body["media"] == nil {
		t.Errorf("missing payload: %v", body)
	}
	if cues, ok := body["cues"].([]any); !ok || len(cues) != 2 {
		t.Errorf("cues = %v", body["cues"])
	}

	for _, target := range []string{
		"/api/chunks/garbage",
		fmt.Sprintf("/api/chunks/%d_9-9", id),
		"/api/chunks/999_0-1",
	} {
		rr := do(t, h, http.MethodGet, target, nil, nil)
		want := http.StatusNotFound
		if target == "/api/chunks/garbage" {
			want = http.StatusBadRequest
		}
		if rr.Code != want {
			t.Errorf("%s status = %d, want %d", target, rr.Code, want)
		}
	}
}

func TestClipStreamBadRequests(t *testing.T) {
	_, deps := newEnv(t)
	h := Router(deps)
	cases := []string{
		"/api/clips/stream?start=1&end=2",
		"/api/clips/stream?mediaId=1",
		"/api/clips/stream?mediaId=1&start=1&end=2&padMs=abc",
		"/api/clips/stream?mediaId=1&start=1&end=2&padMs=6000",
		"/api/clips/stream?mediaId=1&start=1&end=2&mode=nope",
		"/api/clips/stream?mediaId=1&start=2&end=1",
		"/api/clips/stream?mediaId=999&start=1&end=2",
	}
	for _, target := range cases {
		rr := do(t, h, http.MethodGet, target, nil, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", target, rr.Code)
		}
	}
}

func TestClipStreamCached(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	key := clips.CacheKey(id, 1, 2, "fast", 0)
	path := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCacheEntry(key, path, "clip", 10); err != nil {
		t.Fatal(err)
	}

	target := fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=2&mode=fast&padMs=0&download=1", id)
	rr := do(t, Router(deps), http.MethodGet, target, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Errorf("content type = %q", got)
	}
	if got := rr.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("accept ranges = %q", got)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "Show S01E01") {
		t.Errorf("content disposition = %q", cd)
	}
	if rr.Body.String() != "0123456789" {
		t.Errorf("body = %q", rr.Body.String())
	}

	rr = do(t, Router(deps), http.MethodGet, target, nil, map[string]string{"Range": "bytes=2-5"})
	if rr.Code != http.StatusPartialContent {
		t.Errorf("range status = %d, want 206", rr.Code)
	}
	if rr.Body.String() != "2345" {
		t.Errorf("range body = %q", rr.Body.String())
	}
}

func TestClipStreamProduceFailure(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	t.Setenv("FFMPEG_BIN", filepath.Join(t.TempDir(), "missing-ffmpeg"))
	rr := do(t, Router(deps), http.MethodGet,
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=2&mode=fast&padMs=0", id), nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "clip render failed") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestClipStreamAsyncAccurate(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	rr := do(t, Router(deps), http.MethodGet,
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=0&end=10&mode=accurate", id), nil, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	if body["status"] != "queued" {
		t.Errorf("body = %v", body)
	}
	if jobID, ok := body["jobId"].(float64); !ok || jobID <= 0 {
		t.Errorf("jobId = %v", body["jobId"])
	}
}

func TestAdminConfigGet(t *testing.T) {
	st, deps := newEnv(t)
	for _, dir := range []string{"Shows", ".hidden"} {
		if err := os.Mkdir(filepath.Join(deps.MediaDir, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(deps.MediaDir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}

	rr := do(t, Router(deps), http.MethodGet, "/api/admin/config", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	if body["mediaDir"] != deps.MediaDir {
		t.Errorf("mediaDir = %v", body["mediaDir"])
	}
	if folders, ok := body["folders"].([]any); !ok || len(folders) != 1 || folders[0] != "Shows" {
		t.Errorf("folders = %v", body["folders"])
	}
	available, ok := body["available"].([]any)
	if !ok || len(available) != 1 || available[0] != "Shows" {
		t.Errorf("available = %v", body["available"])
	}
}

func TestAdminConfigPut(t *testing.T) {
	st, deps := newEnv(t)
	if err := os.Mkdir(filepath.Join(deps.MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	rr := do(t, h, http.MethodPut, "/api/admin/config",
		strings.NewReader(`{"folders":["Shows","Shows",""]}`), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	paths, err := st.GetLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "Shows" {
		t.Errorf("paths = %v", paths)
	}

	cases := []struct {
		body string
		code int
	}{
		{`{`, http.StatusBadRequest},
		{`{"folders":["/abs"]}`, http.StatusBadRequest},
		{`{"folders":["../escape"]}`, http.StatusBadRequest},
		{`{"folders":["Missing"]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rr := do(t, h, http.MethodPut, "/api/admin/config", strings.NewReader(tc.body), nil)
		if rr.Code != tc.code {
			t.Errorf("body %q status = %d, want %d", tc.body, rr.Code, tc.code)
		}
	}
}

func TestAdminScan(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	if err := st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}
	rr := do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "started" {
		t.Fatalf("scan status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := st.ClearQueuedJobs(); err != nil {
		t.Fatal(err)
	}

	rr = do(t, h, http.MethodPost, "/api/admin/scan?root=Shows/sub", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "started" {
		t.Fatalf("root scan status = %d body=%s", rr.Code, rr.Body.String())
	}

	if _, err := st.EnqueueJob("scan", `{}`, 3); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "already-running" {
		t.Errorf("active scan status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := st.ClearQueuedJobs(); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{"/abs", "../escape"} {
		resp := do(t, h, http.MethodPost, "/api/admin/scan?root="+root, nil, nil)
		if resp.Code != http.StatusBadRequest {
			t.Errorf("root %q status = %d, want 400", root, resp.Code)
		}
	}

	// Invalid stored JSON makes the library configuration unreadable.
	if err := st.SetSetting(store.SettingLibraryPaths, "{bad"); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("bad settings status = %d, want 500", rr.Code)
	}
}

func TestAdminReindexAndReprobe(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	h := Router(deps)

	rr := do(t, h, http.MethodPost, "/api/admin/reindex", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reindex status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n, ok := decodeMap(t, rr)["enqueued"].(float64); !ok || n != 1 {
		t.Errorf("enqueued = %v", decodeMap(t, rr)["enqueued"])
	}

	rr = do(t, h, http.MethodPost, "/api/admin/reprobe", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reprobe status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n, ok := decodeMap(t, rr)["enqueued"].(float64); !ok || n != 1 {
		t.Errorf("enqueued = %v", decodeMap(t, rr)["enqueued"])
	}
}

func enqueueProbes(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := st.EnqueueJob("probe", `{}`, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminQueuePauseResume(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	rr := do(t, h, http.MethodPost, "/api/admin/queue/pause", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["paused"] != true {
		t.Fatalf("pause status = %d body=%s", rr.Code, rr.Body.String())
	}
	if paused, err := st.GetQueuePaused(); err != nil || !paused {
		t.Errorf("queue paused = %v err=%v", paused, err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/queue/resume", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["paused"] != false {
		t.Fatalf("resume status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminQueueClearAndStop(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	enqueueProbes(t, st, 2)
	rr := do(t, h, http.MethodPost, "/api/admin/queue/clear", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["cleared"] != float64(2) {
		t.Fatalf("clear status = %d body=%s", rr.Code, rr.Body.String())
	}

	enqueueProbes(t, st, 2)
	rr = do(t, h, http.MethodPost, "/api/admin/queue/stop", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	if body["paused"] != true || body["cleared"] != float64(2) {
		t.Errorf("stop body = %v", body)
	}

	// A nil runner must not panic and still stops the queue.
	deps.Runner = nil
	rr = do(t, Router(deps), http.MethodPost, "/api/admin/queue/stop", nil, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("nil runner stop status = %d", rr.Code)
	}
}

func TestAdminJobs(t *testing.T) {
	st, deps := newEnv(t)
	for i := 0; i < 3; i++ {
		if _, err := st.EnqueueJob("probe", `{}`, 1); err != nil {
			t.Fatal(err)
		}
	}
	h := Router(deps)

	rr := do(t, h, http.MethodGet, "/api/admin/jobs?limit=1", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	jobsList, ok := body["jobs"].([]any)
	if !ok || len(jobsList) != 1 {
		t.Errorf("jobs = %v", body["jobs"])
	}
	stats, ok := body["stats"].(map[string]any)
	if !ok || stats["queued"] != float64(3) {
		t.Errorf("stats = %v", body["stats"])
	}

	rr = do(t, h, http.MethodGet, "/api/admin/jobs?limit=9999", nil, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	var all struct {
		Jobs []store.Job `json:"jobs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Jobs) != 3 {
		t.Errorf("jobs = %d, want 3", len(all.Jobs))
	}
}

func TestAdminStatus(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	rr := do(t, Router(deps), http.MethodGet, "/api/admin/status", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	if body["queue"] == nil || body["mediaCounts"] == nil {
		t.Errorf("snapshot = %v", body)
	}
}

func TestAPIToken(t *testing.T) {
	_, deps := newEnv(t)
	deps.APIToken = "secret"
	h := Router(deps)

	cases := []struct {
		headers map[string]string
		name    string
		want    int
	}{
		{nil, "missing", http.StatusUnauthorized},
		{map[string]string{"X-API-Token": "nope"}, "wrong header", http.StatusUnauthorized},
		{map[string]string{"Authorization": "Bearer nope"}, "wrong bearer", http.StatusUnauthorized},
		{map[string]string{"Authorization": "Basic abc"}, "basic auth", http.StatusUnauthorized},
		{map[string]string{"X-API-Token": "secret"}, "header", http.StatusOK},
		{map[string]string{"Authorization": "Bearer secret"}, "bearer", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, h, http.MethodGet, "/api/admin/config", nil, tc.headers)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}

	// Public routes stay reachable without a token.
	if rr := do(t, h, http.MethodGet, "/api/health", nil, nil); rr.Code != http.StatusOK {
		t.Errorf("health status = %d", rr.Code)
	}
}

func TestAdminEventsSSE(t *testing.T) {
	_, deps := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/events", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	deps.Events.Handler()(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "event: tick") || !strings.Contains(body, `"meiliUp":true`) {
		t.Fatalf("sse body = %q", body)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type = %q", got)
	}

	// Writers without Flusher are rejected.
	plain := &plainWriter{header: http.Header{}}
	deps.Events.Handler()(plain, httptest.NewRequest(http.MethodGet, "/api/admin/events", nil))
	if plain.code != http.StatusInternalServerError {
		t.Errorf("plain writer status = %d, want 500", plain.code)
	}
}

type plainWriter struct {
	header http.Header
	code   int
}

func (p *plainWriter) Header() http.Header { return p.header }

func (p *plainWriter) Write(b []byte) (int, error) { return len(b), nil }

func (p *plainWriter) WriteHeader(code int) { p.code = code }

func TestWebServing(t *testing.T) {
	_, deps := newEnv(t)
	h := Router(deps)

	cases := []struct {
		target string
		want   string
	}{
		{"/", "<html>index</html>"},
		{"/app.js", "console.log(1)"},
		{"/some/client/route", "<html>index</html>"},
	}
	for _, tc := range cases {
		rr := do(t, h, http.MethodGet, tc.target, nil, nil)
		if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
			t.Errorf("%s status=%d body=%q", tc.target, rr.Code, rr.Body.String())
		}
	}

	deps.WebFS = nil
	rr := do(t, Router(deps), http.MethodGet, "/missing", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("nil WebFS status = %d, want 404", rr.Code)
	}
}

func TestStoreErrorPaths(t *testing.T) {
	st, deps := newEnv(t)
	if err := os.Mkdir(filepath.Join(deps.MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	cases := []struct {
		body   io.Reader
		method string
		target string
	}{
		{nil, http.MethodGet, "/api/media"},
		{nil, http.MethodGet, "/api/media/1"},
		{nil, http.MethodGet, "/api/chunks/1_0-1"},
		{nil, http.MethodGet, "/api/thumb/1"},
		{nil, http.MethodGet, "/api/admin/config"},
		{strings.NewReader(`{"folders":["Shows"]}`), http.MethodPut, "/api/admin/config"},
		{nil, http.MethodPost, "/api/admin/scan"},
		{nil, http.MethodPost, "/api/admin/scan?root=Shows"},
		{nil, http.MethodPost, "/api/admin/reindex"},
		{nil, http.MethodPost, "/api/admin/reprobe"},
		{nil, http.MethodPost, "/api/admin/queue/pause"},
		{nil, http.MethodPost, "/api/admin/queue/resume"},
		{nil, http.MethodPost, "/api/admin/queue/clear"},
		{nil, http.MethodPost, "/api/admin/queue/stop"},
		{nil, http.MethodGet, "/api/admin/jobs"},
	}
	for _, tc := range cases {
		rr := do(t, h, tc.method, tc.target, tc.body, nil)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s %s status = %d, want 500", tc.method, tc.target, rr.Code)
		}
	}
}
