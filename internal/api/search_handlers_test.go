package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"quipster/internal/search"
)

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
