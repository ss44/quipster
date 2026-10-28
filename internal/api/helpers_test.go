package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"quipster/internal/clips"
	"quipster/internal/store"
)

func TestParseChunkID(t *testing.T) {
	cases := []struct {
		id    string
		ok    bool
		media int64
		start int
		end   int
	}{
		{"7_0-2", true, 7, 0, 2},
		{"12_5-9", true, 12, 5, 9},
		{"garbage", false, 0, 0, 0},
		{"x_0-1", false, 0, 0, 0},
		{"1_abc", false, 0, 0, 0},
		{"1_a-1", false, 0, 0, 0},
		{"1_0-b", false, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			media, start, end, ok := parseChunkID(tc.id)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (media != tc.media || start != tc.start || end != tc.end) {
				t.Errorf("got %d_%d-%d", media, start, end)
			}
		})
	}
}

func TestClipName(t *testing.T) {
	req := clips.Request{MediaID: 3, Start: 1.5, End: 2.5}
	cases := []struct {
		name  string
		media *store.MediaFile
		want  string
	}{
		{
			name:  "title only",
			media: &store.MediaFile{ID: 3, Title: "Movie"},
			want:  "Movie [1500-2500]",
		},
		{
			name:  "show with episode",
			media: &store.MediaFile{ID: 3, Title: "ignored", ShowTitle: "Show", Season: 1, Episode: 2},
			want:  "Show S01E02 [1500-2500]",
		},
		{
			name:  "show without episode",
			media: &store.MediaFile{ID: 3, ShowTitle: "Show"},
			want:  "Show [1500-2500]",
		},
		{
			name:  "empty title",
			media: &store.MediaFile{ID: 3},
			want:  "clip-3 [1500-2500]",
		},
		{
			name:  "unsafe characters",
			media: &store.MediaFile{ID: 3, Title: "!!! / ??"},
			want:  "clip-3 [1500-2500]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clipName(tc.media, req); got != tc.want {
				t.Errorf("clipName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBestEffort covers both outcomes of the best-effort lookup helper: the
// value passes through and failures are logged, not propagated.
func TestBestEffort(t *testing.T) {
	if got := bestEffort(7, nil, "unit"); got != 7 {
		t.Errorf("bestEffort success = %d, want 7", got)
	}
	if got := bestEffort(0, errors.New("boom"), "unit"); got != 0 {
		t.Errorf("bestEffort failure = %d, want 0", got)
	}
}

// TestWriteJSONEncodeError covers the logged encode failure: an unsupported
// value leaves the body empty instead of panicking or writing garbage.
func TestWriteJSONEncodeError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, map[string]any{"ch": make(chan int)})
	if rr.Body.String() != "" {
		t.Errorf("body = %q, want empty", rr.Body.String())
	}
}

func TestServeClipUnreadable(t *testing.T) {
	d := Deps{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/clips/stream", nil)
	d.serveClip(rr, req, "/nonexistent/clip.mp4", &store.MediaFile{ID: 1, Title: "Movie"}, clips.Request{MediaID: 1})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if body := decodeMap(t, rr); body["error"] != "clip file unreadable" {
		t.Errorf("body = %v", body)
	}
}
