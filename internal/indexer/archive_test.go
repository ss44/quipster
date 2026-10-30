package indexer

import (
	"context"
	"strconv"
	"testing"
	"time"

	"quipster/internal/store"
)

func archiveMedia(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	// Reuse the scan's transition: zero-grace passes until the target is
	// archived. Each pass treats every other row as seen so only the target
	// moves.
	now := time.UnixMilli(1_000_000)
	target := mediaPath(t, st, id)
	for i := 0; i < 4; i++ {
		keep := map[string]bool{}
		rows, qerr := st.DB().Query(`SELECT path FROM media_files WHERE path LIKE '/' || '%' AND path != ?`, target)
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
		if _, _, err := st.ArchiveMissingUnder("/", keep, 0, now); err != nil {
			t.Fatal(err)
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

func mediaPath(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	var p string
	if err := st.DB().QueryRow(`SELECT path FROM media_files WHERE id=?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArchiveJobHandlerDeletesAndLatches(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	archiveMedia(t, st, id)

	ix := New(st, f.url(), "")
	handler := ix.ArchiveJobHandler()
	payload := `{"mediaId":` + strconv.FormatInt(id, 10) + `}`
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err != nil {
		t.Fatalf("archive job: %v", err)
	}
	filters := f.deleteFilters()
	if len(filters) != 1 || filters[0] != "mediaId = "+strconv.FormatInt(id, 10) {
		t.Fatalf("delete filters = %v, want one per the archived id", filters)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || !m.Deindexed {
		t.Fatalf("row = %+v, want deindexed latched", m)
	}

	// A second run is a no-op: the latch short-circuits the Meili call.
	before := len(f.deleteFilters())
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err != nil {
		t.Fatalf("repeat archive job: %v", err)
	}
	if len(f.deleteFilters()) != before {
		t.Error("repeat archive job must not delete again")
	}
}

func TestArchiveJobHandlerLazyCancellation(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	handler := ix.ArchiveJobHandler()

	// Row restored before the job runs: nothing to delete.
	if _, _, err := st.RestoreMedia(id); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, nil); err != nil {
		t.Fatalf("restored row job: %v", err)
	}
	if len(f.deleteFilters()) != 0 {
		t.Error("restored row must not trigger a delete")
	}

	// Unknown media: also a no-op.
	if err := handler(context.Background(), &store.Job{Payload: `{"mediaId":424242}`}, nil); err != nil {
		t.Fatalf("unknown media job: %v", err)
	}
}

func TestArchiveJobHandlerDeleteErrors(t *testing.T) {
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	archiveMedia(t, st, id)
	payload := `{"mediaId":` + strconv.FormatInt(id, 10) + `}`
	ix := New(st, "", "")
	handler := ix.ArchiveJobHandler()
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err == nil {
		t.Fatal("an unreachable Meili must fail the archive job (unlike the legacy best-effort delete)")
	}

	f := newFakeMeili(t)
	ix = New(st, f.url(), "")
	handler = ix.ArchiveJobHandler()
	f.failDelete = true
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err == nil {
		t.Fatal("delete HTTP failure must propagate")
	}
	f.failDelete = false
	f.failTaskGet = true
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err == nil {
		t.Fatal("task wait failure must propagate")
	}
	f.failTaskGet = false
	f.deleteTasksFail = true
	if err := handler(context.Background(), &store.Job{Payload: payload}, nil); err == nil {
		t.Fatal("failed deletion task must propagate")
	}
}

func TestArchiveJobHandlerRestoreRace(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	archiveMedia(t, st, id)

	// The row is restored while the delete is in flight. The handler must
	// notice after the effect, clear the chunk flags, and queue an index job
	// to rebuild the just-deleted documents.
	f.onDelete = func() {
		if _, _, err := st.RestoreMedia(id); err != nil {
			t.Errorf("restore: %v", err)
		}
	}
	ix := New(st, f.url(), "")
	if err := ix.ArchiveJobHandler()(context.Background(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, nil); err != nil {
		t.Fatalf("archive job under restore race: %v", err)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived {
		t.Fatalf("row = %+v, want restored", m)
	}
	pending, err := st.PendingIndexChunks(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Errorf("chunk flags must be cleared after a raced de-index, got %d pending", len(pending))
	}
	var indexJobs int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index' AND payload=?`,
		`{"mediaId":`+strconv.FormatInt(id, 10)+`}`).Scan(&indexJobs); err != nil {
		t.Fatal(err)
	}
	if indexJobs == 0 {
		t.Error("raced de-index must enqueue an index job")
	}
}

func TestIndexJobHandlerSkipsArchived(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	archiveMedia(t, st, id)
	ix := New(st, f.url(), "")

	if err := ix.JobHandler()(context.Background(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, nil); err != nil {
		t.Fatalf("index job for archived media: %v", err)
	}
	if len(f.addedDocs()) != 0 {
		t.Error("archived media must not be indexed")
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Status == "indexed" {
		t.Errorf("archived status must stay frozen, got %+v", m)
	}
}
