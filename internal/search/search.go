package search

import (
	"encoding/json"
	"fmt"
	"strings"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/indexer"
	"quipster/internal/store"
)

type CueDTO struct {
	Idx   int     `json:"idx"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Hit struct {
	ChunkID      string   `json:"chunkId"`
	Text         string   `json:"text"`
	TextMarked   string   `json:"textMarked"`
	MediaID      int64    `json:"mediaId"`
	MediaTitle   string   `json:"mediaTitle"`
	ShowTitle    string   `json:"showTitle"`
	Season       int      `json:"season"`
	Episode      int      `json:"episode"`
	Year         int      `json:"year"`
	StartSec     float64  `json:"startSec"`
	EndSec       float64  `json:"endSec"`
	Cues         []CueDTO `json:"cues"`
	Before       []CueDTO `json:"contextBefore"`
	After        []CueDTO `json:"contextAfter"`
	ThumbURL     string   `json:"thumbUrl"`
	ContainerExt string   `json:"-"`
	FilePath     string   `json:"-"`
}

type TitleMatch struct {
	MediaID    int64  `json:"mediaId"`
	Title      string `json:"title"`
	ShowTitle  string `json:"showTitle"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	Year       int    `json:"year"`
	Status     string `json:"status"`
	ThumbURL   string `json:"thumbUrl"`
	NoSubtitle bool   `json:"noSubtitle"`
}

type Response struct {
	Query        string       `json:"query"`
	Page         int          `json:"page"`
	PerPage      int          `json:"perPage"`
	TotalHits    int64        `json:"totalHits"`
	TotalPages   int64        `json:"totalPages"`
	Hits         []Hit        `json:"hits"`
	TitleMatches []TitleMatch `json:"titleMatches"`
}

type Suggestion struct {
	Text       string `json:"text"`
	TextMarked string `json:"textMarked"`
}

type Service struct {
	st *store.Store
	ix *indexer.Indexer
}

func New(st *store.Store, ix *indexer.Indexer) *Service {
	return &Service{st: st, ix: ix}
}

// CueDTOs converts stored cues for API responses (also used by the chunks endpoint).
func CueDTOs(cues []store.Cue) []CueDTO {
	out := make([]CueDTO, 0, len(cues))
	for _, c := range cues {
		out = append(out, CueDTO{Idx: c.Idx, Start: c.StartSec, End: c.EndSec, Text: c.Text})
	}
	return out
}

func mediaTitleFor(m *store.MediaFile) string {
	if m.ShowTitle != "" {
		if m.Season > 0 && m.Episode > 0 {
			return fmt.Sprintf("%s S%02dE%02d", m.ShowTitle, m.Season, m.Episode)
		}
		return m.ShowTitle
	}
	return m.Title
}

// Search runs a Meili query, joins chunk docs back to SQLite cues, and adds a
// title-match fallback so files without subtitles remain findable.
func (s *Service) Search(q, show string, page, perPage int) (*Response, error) {
	if perPage <= 0 || perPage > 50 {
		perPage = 20
	}
	if page <= 0 {
		page = 1
	}
	resp := &Response{Query: q, Page: page, PerPage: perPage, Hits: []Hit{}, TitleMatches: []TitleMatch{}}

	req := meili.SearchRequest{
		Query:                 q,
		AttributesToHighlight: []string{"text"},
		HighlightPreTag:       "<mark>",
		HighlightPostTag:      "</mark>",
		HitsPerPage:           i64(perPage),
		Page:                  int64(page),
		MatchingStrategy:      meili.All,
	}
	if strings.TrimSpace(show) != "" {
		req.Filter = fmt.Sprintf(`showTitle = "%s"`, escape(show))
	}
	res, err := s.ix.Client().Index(indexer.IndexName).Search(q, &req)
	if err != nil {
		return nil, err
	}
	resp.TotalHits = res.TotalHits
	resp.TotalPages = res.TotalPages

	seenMedia := map[int64]bool{}
	for _, h := range res.Hits {
		var id string
		var marked string
		if err := json.Unmarshal(h["id"], &id); err != nil {
			continue
		}
		mediaID, startIdx, endIdx, ok := parseDocID(id)
		if !ok {
			continue
		}
		ch, err := s.st.GetChunkByKey(mediaID, startIdx, endIdx)
		if err != nil {
			return nil, err
		}
		if ch == nil {
			continue // stale doc (media re-probed or deleted)
		}
		m, err := s.st.GetMedia(mediaID)
		if err != nil || m == nil {
			continue
		}
		span, before, after, err := s.st.CuesInRange(mediaID, startIdx, endIdx)
		if err != nil {
			return nil, err
		}
		marked = ch.Text
		var fm map[string]json.RawMessage
		if raw, ok := h["_formatted"]; ok && len(raw) > 0 {
			if err := json.Unmarshal(raw, &fm); err == nil {
				var ft string
				if err := json.Unmarshal(fm["text"], &ft); err == nil && ft != "" {
					marked = ft
				}
			}
		}
		seenMedia[mediaID] = true
		resp.Hits = append(resp.Hits, Hit{
			ChunkID:      id,
			Text:         ch.Text,
			TextMarked:   marked,
			MediaID:      mediaID,
			MediaTitle:   mediaTitleFor(m),
			ShowTitle:    m.ShowTitle,
			Season:       m.Season,
			Episode:      m.Episode,
			Year:         m.Year,
			StartSec:     ch.StartSec,
			EndSec:       ch.EndSec,
			Cues:         CueDTOs(span),
			Before:       CueDTOs(before),
			After:        CueDTOs(after),
			ThumbURL:     fmt.Sprintf("/api/thumb/%d?t=%.1f&w=320", mediaID, ch.StartSec),
			ContainerExt: containerExt(m.Container),
			FilePath:     m.Path,
		})
	}

	if strings.TrimSpace(q) != "" {
		tm, err := s.st.TitleMatches(q, 6)
		if err != nil {
			return nil, err
		}
		for _, m := range tm {
			if seenMedia[m.ID] {
				continue
			}
			resp.TitleMatches = append(resp.TitleMatches, TitleMatch{
				MediaID:    m.ID,
				Title:      mediaTitleFor(&m),
				ShowTitle:  m.ShowTitle,
				Season:     m.Season,
				Episode:    m.Episode,
				Year:       m.Year,
				Status:     m.Status,
				ThumbURL:   fmt.Sprintf("/api/thumb/%d?t=5.0&w=320", m.ID),
				NoSubtitle: m.Status == "no_subs",
			})
		}
	}
	return resp, nil
}

// Suggest returns autocomplete candidates with highlight markers.
func (s *Service) Suggest(q string, limit int) ([]Suggestion, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	req := meili.SearchRequest{
		Limit:                 int64(limit),
		AttributesToRetrieve:  []string{"text"},
		AttributesToHighlight: []string{"text"},
		HighlightPreTag:       "<mark>",
		HighlightPostTag:      "</mark>",
	}
	res, err := s.ix.Client().Index(indexer.IndexName).Search(q, &req)
	if err != nil {
		return nil, err
	}
	out := make([]Suggestion, 0, len(res.Hits))
	seen := map[string]bool{}
	for _, h := range res.Hits {
		var text string
		if err := json.Unmarshal(h["text"], &text); err != nil {
			continue
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		sg := Suggestion{Text: text}
		var fm map[string]json.RawMessage
		if raw, ok := h["_formatted"]; ok && len(raw) > 0 {
			if err := json.Unmarshal(raw, &fm); err == nil {
				var ft string
				if err := json.Unmarshal(fm["text"], &ft); err == nil && ft != "" {
					sg.TextMarked = ft
				}
			}
		}
		out = append(out, sg)
	}
	return out, nil
}

func parseDocID(id string) (mediaID int64, startIdx, endIdx int, ok bool) {
	parts := strings.SplitN(id, "_", 2)
	if len(parts) != 2 {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &mediaID); err != nil {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d-%d", &startIdx, &endIdx); err != nil {
		return 0, 0, 0, false
	}
	return mediaID, startIdx, endIdx, true
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}

func containerExt(container string) string {
	if strings.Contains(container, "matroska") || strings.Contains(container, "webm") {
		return "mkv"
	}
	if strings.Contains(container, "mov") || strings.Contains(container, "mp4") {
		return "mp4"
	}
	return ""
}

func i64(v int) *int64 {
	x := int64(v)
	return &x
}
