package search

import (
	"strconv"
	"testing"
	"time"

	"quipster/internal/store"
)

func docID(mediaID int64, start, end int) string {
	return strconv.FormatInt(mediaID, 10) + "_" + strconv.Itoa(start) + "-" + strconv.Itoa(end)
}

func archiveForSearch(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 4; i++ {
		keep := map[string]bool{}
		rows, qerr := st.DB().Query(`SELECT path FROM media_files WHERE id != ?`, id)
		if qerr != nil {
			t.Fatal(qerr)
		}
		for rows.Next() {
			var p string
			if serr := rows.Scan(&p); serr != nil {
				t.Fatal(serr)
			}
			keep[p] = true
		}
		if rerr := rows.Err(); rerr != nil {
			t.Fatal(rerr)
		}
		if cerr := rows.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if _, _, aerr := st.ArchiveMissingUnder("/", keep, 0, now); aerr != nil {
			t.Fatal(aerr)
		}
		m, gerr := st.GetMedia(id)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if m != nil && m.Archived {
			return
		}
	}
	t.Fatalf("media %d never archived", id)
}

// A Meili hit for an archived media must be dropped, and the stray document
// must be reconciled by re-enqueueing an archive_deindex job.
func TestSearchDropsArchivedHits(t *testing.T) {
	svc, st, f := newService(t)
	id1, _ := seedMedia(t, st)
	archiveForSearch(t, st, id1)
	f.mu.Lock()
	f.hits = []map[string]any{{
		"id":            docID(id1, 0, 1),
		"text":          "hello world",
		"_formatted":    map[string]any{"text": "<mark>hello</mark> world"},
		"_rankingScore": 0.9,
	}}
	f.total = 1
	f.mu.Unlock()

	resp, err := svc.Search("hello", "", 1, 20, "keyword")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("archived hit leaked: %+v", resp.Hits)
	}
	var jobs int
	if qerr := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='archive_deindex' AND payload=?`,
		`{"mediaId":`+strconv.FormatInt(id1, 10)+`}`).Scan(&jobs); qerr != nil {
		t.Fatal(qerr)
	}
	if jobs == 0 {
		t.Error("archived hit must re-enqueue an archive_deindex job")
	}

	// After restore, the same documents flow again. The restore cleared the
	// chunk flags (documents had been deleted), so re-latch them here the way
	// the index job would.
	if _, _, rerr := st.RestoreMedia(id1); rerr != nil {
		t.Fatal(rerr)
	}
	pending, qerr2 := st.PendingIndexChunks(id1, 10)
	if qerr2 != nil {
		t.Fatal(qerr2)
	}
	ids := make([]int64, 0, len(pending))
	for _, ch := range pending {
		ids = append(ids, ch.ID)
	}
	if merr := st.MarkChunksIndexed(ids, 42); merr != nil {
		t.Fatal(merr)
	}
	f.mu.Lock()
	f.hits = []map[string]any{{
		"id":            docID(id1, 0, 1),
		"text":          "hello world",
		"_rankingScore": 0.9,
	}}
	f.mu.Unlock()
	resp, err = svc.Search("hello", "", 1, 20, "keyword")
	if err != nil {
		t.Fatalf("search after restore: %v", err)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("restored media hit missing: %+v", resp.Hits)
	}
}

// The title fallback must never surface archived rows either.
func TestSearchTitleFallbackSkipsArchived(t *testing.T) {
	svc, st, _ := newService(t)
	_, id2 := seedMedia(t, st)
	archiveForSearch(t, st, id2)

	resp, err := svc.Search("Gone in 60 Seconds", "", 1, 20, "keyword")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.TitleMatches) != 0 {
		t.Fatalf("archived media surfaced in title matches: %+v", resp.TitleMatches)
	}
}
