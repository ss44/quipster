package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"quipster/internal/store"
)

// quoteItem mirrors the /api/quotes payload so assertions are checked against
// the real wire shape instead of loose map lookups. Field order groups the
// pointer-carrying strings first to satisfy govet fieldalignment.
type quoteItem struct {
	ChunkID   string  `json:"chunkId"`
	Text      string  `json:"text"`
	ShowTitle string  `json:"showTitle"`
	ThumbURL  string  `json:"thumbUrl"`
	MediaID   int64   `json:"mediaId"`
	StartSec  float64 `json:"startSec"`
	Season    int     `json:"season"`
	Episode   int     `json:"episode"`
}

type quotesResponse struct {
	Quotes []quoteItem `json:"quotes"`
}

func decodeQuotes(t *testing.T, body []byte) quotesResponse {
	t.Helper()
	var resp quotesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode quotes body %q: %v", body, err)
	}
	return resp
}

func TestQuotesEndpointServesIndexedDialogue(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedIndexedChunks(t, st, id)

	rr := do(t, Router(deps), http.MethodGet, "/api/quotes?limit=4", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeQuotes(t, rr.Body.Bytes())
	if len(resp.Quotes) != 2 {
		t.Fatalf("quotes = %d, want 2 (body=%s)", len(resp.Quotes), rr.Body.String())
	}

	// RandomQuotes has no deterministic order; compare sorted projections.
	texts := make([]string, 0, len(resp.Quotes))
	chunkIDs := make([]string, 0, len(resp.Quotes))
	for _, quote := range resp.Quotes {
		texts = append(texts, quote.Text)
		chunkIDs = append(chunkIDs, quote.ChunkID)
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

	for _, quote := range resp.Quotes {
		if quote.MediaID != id {
			t.Errorf("mediaId = %d, want %d", quote.MediaID, id)
		}
		if quote.ShowTitle != "Show" || quote.Season != 1 || quote.Episode != 1 {
			t.Errorf("title fields = %q S%02dE%02d", quote.ShowTitle, quote.Season, quote.Episode)
		}
		wantThumb := fmt.Sprintf("/api/thumb/%d?t=%.1f&w=320", id, quote.StartSec)
		if quote.ThumbURL != wantThumb {
			t.Errorf("thumbUrl = %q, want %q", quote.ThumbURL, wantThumb)
		}
	}
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
	if resp := decodeQuotes(t, rr.Body.Bytes()); len(resp.Quotes) != 0 {
		t.Errorf("quotes = %v, want none before chunks are indexed", resp.Quotes)
	}
}

func TestQuotesEndpointEmptyLibrary(t *testing.T) {
	_, deps := newEnv(t)
	rr := do(t, Router(deps), http.MethodGet, "/api/quotes", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// The landing page must receive [] rather than null so it can hide the
	// quotes block without a nil check.
	if resp := decodeQuotes(t, rr.Body.Bytes()); resp.Quotes == nil {
		t.Errorf("quotes = null, want an empty array")
	}
}
