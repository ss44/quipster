package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"quipster/internal/store"
)

// seedQuoteChunks inserts two indexable chunks for a media file and latches
// them as indexed so RandomQuotes can surface them.
func seedQuoteChunks(t *testing.T, st *store.Store, id int64) {
	t.Helper()
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
}

func TestQuotesEndpointServesIndexedDialogue(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedQuoteChunks(t, st, id)

	rr := do(t, Router(deps), http.MethodGet, "/api/quotes?limit=4", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	quotes := decodeQuotes(t, rr)
	if len(quotes) != 2 {
		t.Fatalf("quotes = %d, want 2 (body=%s)", len(quotes), rr.Body.String())
	}

	// RandomQuotes has no deterministic order; compare sorted projections.
	texts := make([]string, 0, len(quotes))
	chunkIDs := make([]string, 0, len(quotes))
	for _, q := range quotes {
		texts = append(texts, qstr(t, q, "text"))
		chunkIDs = append(chunkIDs, qstr(t, q, "chunkId"))
	}
	sort.Strings(texts)
	wantTexts := []string{"Show me the money!", "the city never sleeps"}
	if !reflect.DeepEqual(texts, wantTexts) {
		t.Errorf("texts = %v, want %v", texts, wantTexts)
	}
	sort.Strings(chunkIDs)
	wantIDs := []string{fmt.Sprintf("%d_0-1", id), fmt.Sprintf("%d_2-3", id)}
	if !reflect.DeepEqual(chunkIDs, wantIDs) {
		t.Errorf("chunkIds = %v, want %v", chunkIDs, wantIDs)
	}

	for _, q := range quotes {
		if int64(qnum(t, q, "mediaId")) != id {
			t.Errorf("mediaId = %v, want %d", q["mediaId"], id)
		}
		if qstr(t, q, "showTitle") != "Show" || qnum(t, q, "season") != 1 || qnum(t, q, "episode") != 1 {
			t.Errorf("title fields = %q S%vE%v", q["showTitle"], q["season"], q["episode"])
		}
		start := qnum(t, q, "startSec")
		wantThumb := fmt.Sprintf("/api/thumb/%d?t=%.1f&w=320", id, start)
		if qstr(t, q, "thumbUrl") != wantThumb {
			t.Errorf("thumbUrl = %q, want %q", q["thumbUrl"], wantThumb)
		}
	}
}

// qstr and qnum read decoded quote fields, failing the test on a missing or
// mistyped value instead of asserting unchecked.
func qstr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("quote field %q missing or not a string (got %v)", key, m[key])
	}
	return v
}

func qnum(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("quote field %q missing or not a number (got %v)", key, m[key])
	}
	return v
}

// decodeQuotes decodes the quotes payload into generic maps, keeping the
// assertion code independent of response field ordering.
func decodeQuotes(t *testing.T, rr *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Quotes []map[string]any `json:"quotes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return body.Quotes
}

func TestQuotesEndpointExcludesUnindexedChunks(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	if err := st.ReplaceChunks(id, []store.Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 1, Text: "Show me the money!"},
	}); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}

	rr := do(t, Router(deps), http.MethodGet, "/api/quotes?limit=4", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Quotes []map[string]any `json:"quotes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Quotes) != 0 {
		t.Errorf("quotes = %v, want none before chunks are indexed", body.Quotes)
	}
}

func TestQuotesEndpointEmptyLibrary(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/quotes", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Quotes []map[string]any `json:"quotes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The landing page must receive [] rather than null so it can hide the
	// quotes block without a nil check.
	if body.Quotes == nil {
		t.Errorf("quotes = null, want an empty array")
	}
}
