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
