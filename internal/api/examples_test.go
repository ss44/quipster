package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"quipster/internal/store"
)

func TestExamplesEndpoint(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
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

	rr := do(t, Router(deps), http.MethodGet, "/api/examples", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Examples []string `json:"examples"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// "the city never sleeps" outranks nothing and both resolve, so the
	// candidates are returned ranked by chunk count.
	want := []string{"show me the money", "the city never sleeps"}
	if !reflect.DeepEqual(body.Examples, want) {
		t.Errorf("examples = %v, want %v", body.Examples, want)
	}
}

func TestExamplesEndpointEmptyLibrary(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/examples", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Examples []string `json:"examples"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The empty library must serialize as [] (never null) so the UI hides the
	// example block instead of rendering dead shortcuts.
	if !reflect.DeepEqual(body.Examples, []string{}) {
		t.Errorf("examples = %v, want empty non-nil", body.Examples)
	}
}
