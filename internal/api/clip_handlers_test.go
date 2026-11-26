package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/clips"
)

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

func TestClipStreamGif(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	h := Router(deps)

	// GIF clips longer than MaxGifSec (10s default) are rejected with 400.
	rr := do(t, h, http.MethodGet,
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=0&end=11&mode=gif&padMs=0", id), nil, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("long gif status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "gif clip span") {
		t.Errorf("long gif body = %q", rr.Body.String())
	}

	// Cached GIF is served with image/gif Content-Type and .gif attachment name.
	req := clips.Request{MediaID: id, Start: 1, End: 4, Mode: "gif", PadMs: 250}
	key := deps.Clips.CacheKey(req)
	path := filepath.Join(t.TempDir(), "clip.gif")
	if err := os.WriteFile(path, []byte("GIF89a-mock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCacheEntry(key, path, "clip", 11); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, http.MethodGet,
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=gif&padMs=250&download=1", id), nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("gif status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/gif" {
		t.Errorf("content type = %q, want image/gif", got)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, ".gif\"") {
		t.Errorf("content disposition = %q, want .gif filename", cd)
	}
	if rr.Body.String() != "GIF89a-mock" {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestFontsEndpoints(t *testing.T) {
	_, deps := newEnv(t)
	h := Router(deps)

	rr := do(t, h, http.MethodGet, "/api/fonts", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/fonts status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	list, ok := body["fonts"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("fonts list = %v", body["fonts"])
	}

	rr = do(t, h, http.MethodGet, "/api/fonts/inter-400.ttf", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/fonts/inter-400.ttf status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "font/ttf" {
		t.Errorf("Content-Type = %q, want font/ttf", got)
	}
	if rr.Body.Len() < 1000 {
		t.Errorf("font body too small: %d bytes", rr.Body.Len())
	}

	rr = do(t, h, http.MethodGet, "/api/fonts/unknown-400.ttf", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown font status = %d, want 404", rr.Code)
	}
}

func TestClipStreamSubtitles(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	h := Router(deps)

	badCases := []string{
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=fast&subs=1", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=maybe", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subFont=bogus", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subSize=abc", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subOutline=abc", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subShadow=abc", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subBg=abc", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subMargin=abc", id),
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&subs=1&subPos=sideways", id),
	}
	for _, target := range badCases {
		rr := do(t, h, http.MethodGet, target, nil, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400, body=%s", target, rr.Code, rr.Body.String())
		}
	}

	sub := clips.DefaultSubtitleStyle()
	sub.Font = "montserrat"
	sub.FontSize = 48
	sub.Color = "ffd166"
	sub.Outline = 3
	sub.OutlineColor = "111111"
	sub.Shadow = 2
	sub.BgOpacity = 0.5
	sub.BgColor = "222222"
	sub.Position = "top"
	sub.MarginV = 44
	sub.Bold = true
	sub.Italic = true
	sub.Uppercase = true

	req := clips.Request{MediaID: id, Start: 1, End: 4, Mode: "accurate", PadMs: 250, Subtitles: &sub}
	key := deps.Clips.CacheKey(req)
	path := filepath.Join(t.TempDir(), "subbed.mp4")
	if err := os.WriteFile(path, []byte("MP4-SUBS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCacheEntry(key, path, "clip", 8); err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf(
		"/api/clips/stream?mediaId=%d&start=1&end=4&mode=accurate&padMs=250&subs=1&subFont=montserrat&subSize=48&subColor=ffd166&subOutline=3&subOutlineColor=111111&subShadow=2&subBg=0.5&subBgColor=222222&subPos=top&subMargin=44&subBold=1&subItalic=1&subUpper=1",
		id,
	)
	rr := do(t, h, http.MethodGet, target, nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "MP4-SUBS" {
		t.Fatalf("subtitled cache hit: status=%d body=%q", rr.Code, rr.Body.String())
	}

	// Long subtitled accurate clip enqueues an async job carrying the style.
	rr = do(t, h, http.MethodGet,
		fmt.Sprintf("/api/clips/stream?mediaId=%d&start=0&end=10&mode=accurate&subs=1&subFont=oswald&subUpper=1", id), nil, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("long subtitled clip status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func stringField(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	val, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s = %T (%v), want string", key, m[key], m[key])
	}
	return val
}

func int64Field(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	val, ok := m[key].(float64)
	if !ok || val <= 0 {
		t.Fatalf("%s = %T (%v), want positive number", key, m[key], m[key])
	}
	return int64(val)
}

func TestClipJobsLifecycle(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	// Ensure public clip job endpoints stay accessible even when admin API token is set.
	deps.APIToken = "secret-admin-token"
	h := Router(deps)

	target := fmt.Sprintf("/api/clips/jobs?mediaId=%d&start=1&end=4&mode=gif&padMs=250", id)
	rr := do(t, h, http.MethodPost, target, nil, nil)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	body := decodeMap(t, rr)
	jobID := int64Field(t, body, "jobId")
	cacheKey := stringField(t, body, "cacheKey")
	assert.True(t, isHexCacheKey(cacheKey))
	assert.Equal(t, "queued", body["status"])
	assert.Equal(t, false, body["cached"])
	assert.True(t, strings.HasSuffix(stringField(t, body, "filename"), ".gif"))
	assert.Contains(t, stringField(t, body, "downloadUrl"), "download=1")
	assert.Contains(t, stringField(t, body, "downloadUrl"), "mode=gif")

	// Duplicate submission deduplicates onto the same in-flight job.
	rr2 := do(t, h, http.MethodPost, target, nil, nil)
	require.Equal(t, http.StatusAccepted, rr2.Code)
	assert.Equal(t, jobID, int64Field(t, decodeMap(t, rr2), "jobId"))

	// Public status endpoint reports queued before render completes.
	rr = do(t, h, http.MethodGet, fmt.Sprintf("/api/clips/jobs/%d", jobID), nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "queued", decodeMap(t, rr)["status"])

	// Simulate completion + cached file on disk.
	path := filepath.Join(t.TempDir(), "out.gif")
	require.NoError(t, os.WriteFile(path, []byte("GIF89a-ready"), 0o600))
	require.NoError(t, st.UpsertCacheEntry(cacheKey, path, "clip", 12))
	require.NoError(t, st.FinishJob(jobID, "done", ""))

	rr = do(t, h, http.MethodGet, fmt.Sprintf("/api/clips/jobs/%d", jobID), nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	doneBody := decodeMap(t, rr)
	assert.Equal(t, "done", doneBody["status"])
	assert.Equal(t, true, doneBody["cached"])
	assert.Contains(t, stringField(t, doneBody, "filename"), "Show S01E01")
	assert.True(t, strings.HasSuffix(stringField(t, doneBody, "filename"), ".gif"))

	// Re-submitting the same clip now returns 200 ready immediately from cache.
	rr = do(t, h, http.MethodPost, target, nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	readyBody := decodeMap(t, rr)
	assert.Equal(t, "ready", readyBody["status"])
	assert.Equal(t, true, readyBody["cached"])

	// Cache status endpoint reports hit for cached key and miss for unknown key.
	rr = do(t, h, http.MethodGet, "/api/clips/cache/"+cacheKey, nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	cacheBody := decodeMap(t, rr)
	assert.Equal(t, true, cacheBody["cached"])
	assert.Equal(t, float64(12), cacheBody["sizeBytes"])

	rr = do(t, h, http.MethodGet, "/api/clips/cache/0123456789abcdef0123456789abcdef", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, false, decodeMap(t, rr)["cached"])
}

func TestClipJobsValidationAndIsolation(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	h := Router(deps)

	badCreates := []string{
		"/api/clips/jobs?mediaId=1&start=5&end=1",
		fmt.Sprintf("/api/clips/jobs?mediaId=%d&start=1&end=4&mode=fast&subs=1", id),
	}
	for _, target := range badCreates {
		if rr := do(t, h, http.MethodPost, target, nil, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("POST %s status = %d, want 400", target, rr.Code)
		}
	}

	if rr := do(t, h, http.MethodGet, "/api/clips/cache/not-hex!", nil, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad cache key status = %d, want 400", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/api/clips/jobs/abc", nil, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad job id status = %d, want 400", rr.Code)
	}

	// Non-clip jobs must not be exposed by the public clip job endpoint.
	probeJobID, err := st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 0)
	if err != nil {
		t.Fatal(err)
	}
	if rr := do(t, h, http.MethodGet, fmt.Sprintf("/api/clips/jobs/%d", probeJobID), nil, nil); rr.Code != http.StatusNotFound {
		t.Errorf("probe job on clip endpoint status = %d, want 404", rr.Code)
	}

	// Archived media returns 404 on job creation.
	archiveViaScan(t, st, id)
	rr := do(t, h, http.MethodPost, fmt.Sprintf("/api/clips/jobs?mediaId=%d&start=1&end=4&mode=accurate", id), nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("archived media create status = %d, want 404", rr.Code)
	}
}
