package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestMediaCuesArchived(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	archiveViaScan(t, st, id)

	cues, _, _, err := st.CuesAround(id, 2, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 || cues[0].Text != "line 2" {
		t.Fatalf("archived captions must be retained: %v", cues)
	}

	target := fmt.Sprintf("/api/media/%d/cues?startIdx=2&endIdx=2", id)
	rr := do(t, Router(deps), http.MethodGet, target, nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if body := decodeMap(t, rr); body["error"] != "media not found" || body["cues"] != nil {
		t.Fatalf("archived captions exposed: %v", body)
	}
}

func TestMediaCues(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	h := Router(deps)
	target := fmt.Sprintf("/api/media/%d/cues", id)
	rr := do(t, h, http.MethodGet, target+"?startIdx=2&endIdx=2", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	cues, ok := body["cues"].([]any)
	if !ok || len(cues) != 1 {
		t.Fatalf("cues = %v", body["cues"])
	}
	line, ok := cues[0].(map[string]any)
	if !ok || line["idx"] != float64(2) {
		t.Fatalf("cue = %v", cues[0])
	}
	rr = do(t, h, http.MethodGet, target+"?startIdx=4&endIdx=4", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "{\"cues\":[]}\n" {
		t.Fatalf("boundary = %d %s", rr.Code, rr.Body.String())
	}
	for _, query := range []string{"", "?startIdx=-1&endIdx=0", "?startIdx=2&endIdx=1", "?startIdx=0&endIdx=100", "?startIdx=no&endIdx=1"} {
		if rr := do(t, h, http.MethodGet, target+query, nil, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d", query, rr.Code)
		}
	}
	for _, item := range []struct {
		path   string
		status int
	}{
		{"/api/media/bad/cues?startIdx=0&endIdx=0", http.StatusBadRequest},
		{"/api/media/999/cues?startIdx=0&endIdx=0", http.StatusNotFound},
	} {
		if rr := do(t, h, http.MethodGet, item.path, nil, nil); rr.Code != item.status {
			t.Errorf("%s status = %d", item.path, rr.Code)
		}
	}
}
