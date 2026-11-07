package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"quipster/internal/store"
)

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
