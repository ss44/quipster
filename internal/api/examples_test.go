package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestExamplesEndpoint(t *testing.T) {
	st := newStore(t)
	// Return a fake search hit so the candidates resolve.
	hits := []map[string]any{
		{"id": `"1_0-1"`, "text": `"fake hit"`},
	}
	deps := buildDeps(t, st, newMeili(t, http.StatusOK, hits))

	id := seedMedia(t, st)
	seedIndexedChunks(t, st, id)

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
	want := []string{"i could have been a contender", "run, you fools", "show me the money", "the city never sleeps"}
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
