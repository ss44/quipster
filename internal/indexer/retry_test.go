package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"quipster/internal/store"
)

func indexJob(id int64) *store.Job {
	return &store.Job{ID: 17, Payload: fmt.Sprintf(`{"mediaId":%d}`, id)}
}

func TestCompletedIndexJobIsIdempotent(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	handler := New(st, f.url(), "").JobHandler()
	for i := 0; i < 2; i++ {
		if err := handler(context.Background(), indexJob(id), nil); err != nil {
			t.Fatal(err)
		}
		if got := len(f.docs); got != 2 {
			t.Fatalf("run %d: remote docs=%d, want 2", i, got)
		}
	}
	if len(f.deleteFilters()) != 0 {
		t.Fatal("complete duplicate must not delete")
	}
}

func TestCanceledBeforeMediaLatchDoesNotComplete(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := New(st, f.url(), "").JobHandler()(ctx, indexJob(id), func(v float64) {
		if v == 0.95 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	ever, err := st.EverIndexed(id)
	if err != nil || ever {
		t.Fatalf("canceled job completed: ever=%t err=%v", ever, err)
	}
}

func TestChunkLatchWriterWaitIsContextCancellable(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	started := time.Now()
	err := New(st, f.url(), "").JobHandler()(ctx, indexJob(id), func(v float64) {
		if v != 0.9 {
			return
		}
		conn, err := st.DB().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		release = func() {
			once.Do(func() {
				if err := conn.Close(); err != nil {
					t.Errorf("release writer: %v", err)
				}
			})
		}
		time.AfterFunc(50*time.Millisecond, cancel)
		// Ensure a broken implementation cannot hang the test indefinitely.
		timer := time.AfterFunc(time.Second, release)
		t.Cleanup(func() { timer.Stop(); release() })
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("writer wait ignored context: %s", elapsed)
	}
}

func TestArchiveDuringIndexDoesNotLatchFrozenState(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	err := New(st, f.url(), "").JobHandler()(context.Background(), indexJob(id), func(v float64) {
		if v == 0.9 {
			archiveMedia(t, st, id)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := st.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if ch.IndexedAt != 0 {
			t.Fatal("archived chunk latch mutated")
		}
	}
	m, err := st.GetMedia(id)
	if err != nil || m.Status != "pending" || !m.Archived {
		t.Fatalf("archived status mutated: %+v err=%v", m, err)
	}
}

func TestPartialLatchRetryRebuildsAllDocuments(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	handler := New(st, f.url(), "").JobHandler()
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	chunks, err := st.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce MarkChunksIndexed's intentional partial-commit window.
	if _, err := st.DB().Exec(`UPDATE chunks SET indexed_at=0 WHERE id=?`, chunks[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 2 || f.docs[DocID(id, 0, 1)] == nil || f.docs[DocID(id, 2, 3)] == nil {
		t.Fatalf("partial retry lost documents: %v", f.docs)
	}
}
