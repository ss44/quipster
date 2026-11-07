package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
