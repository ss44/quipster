package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quipster/internal/store"
)

func TestEmptyReplacementRemovesOnlyItsMedia(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, other := seedMedia(t, st)
	if err := st.ReplaceChunks(other, []store.Chunk{{StartCueIdx: 4, EndCueIdx: 5, Text: "other"}}); err != nil {
		t.Fatal(err)
	}
	handler := New(st, f.url(), "").JobHandler()
	for _, mediaID := range []int64{id, other} {
		if err := handler(context.Background(), indexJob(mediaID), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceChunks(id, nil); err != nil {
		t.Fatal(err)
	}
	logs := captureIndexLogs(t)
	var values []float64
	if err := handler(context.Background(), indexJob(id), func(v float64) { values = append(values, v) }); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 1 || f.docs[DocID(other, 4, 5)] == nil {
		t.Fatalf("empty replacement left wrong docs: %v", f.docs)
	}
	if len(f.addedDocs()) != 3 {
		t.Fatal("empty replacement uploaded documents")
	}
	if len(values) == 0 || values[len(values)-1] != 1 {
		t.Fatalf("empty replacement completion missing: %v", values)
	}
	if !strings.Contains(logs.String(), "chunks=0 pending=0") || !strings.Contains(logs.String(), "stage=delete") {
		t.Fatalf("empty diagnostics missing: %s", logs.String())
	}
}

func TestFailedDeleteCanRetryWithoutLosingDocuments(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	handler := New(st, f.url(), "").JobHandler()
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearChunkIndexFlags(id); err != nil {
		t.Fatal(err)
	}
	logs := captureIndexLogs(t)
	f.deleteTasksFail = true
	if err := handler(context.Background(), indexJob(id), nil); err == nil {
		t.Fatal("failed delete accepted")
	}
	if len(f.docs) != 2 {
		t.Fatal("failed deletion changed documents")
	}
	if !strings.Contains(logs.String(), "stage=delete") || !strings.Contains(logs.String(), "status=failed") {
		t.Fatalf("failed task diagnostics missing: %s", logs.String())
	}
	f.deleteTasksFail = false
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 2 {
		t.Fatal("delete retry lost documents")
	}
}

func TestFailedAddLeavesChunksRetryable(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	f.addTasksFail = true
	logs := captureIndexLogs(t)
	var values []float64
	handler := New(st, f.url(), "").JobHandler()
	if err := handler(context.Background(), indexJob(id), func(v float64) { values = append(values, v) }); err == nil {
		t.Fatal("failed add accepted")
	}
	if len(f.docs) != 0 {
		t.Fatal("fake must not apply failed task documents")
	}
	for _, v := range values {
		if v >= 1 {
			t.Fatal("failure reported completion")
		}
	}
	if !strings.Contains(logs.String(), "status=failed") {
		t.Fatalf("missing failed add diagnostics: %s", logs.String())
	}
	f.addTasksFail = false
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 2 {
		t.Fatal("retry failed to restore complete snapshot")
	}
}

func TestCanceledRemoteWaitRemainsRetryable(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	f.pendingUntil = time.Now().Add(time.Hour)
	logs := captureIndexLogs(t)
	// The timeout is only a safety bound: cancel after the task-status HTTP
	// request arrives, never while SQLite setup or snapshot loading is running.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	arrived := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/") {
			select {
			case arrived <- time.Now():
			default:
			}
			cancel()
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	handler := New(st, srv.URL, "").JobHandler()
	if err := handler(ctx, indexJob(id), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation after task-status arrival, got %v", err)
	}
	select {
	case started := <-arrived:
		if time.Since(started) > time.Second {
			t.Fatal("remote task wait prevented prompt cancellation")
		}
	default:
		t.Fatal("canceled without reaching task-status HTTP request")
	}
	pending, err := st.PendingIndexChunks(id, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("canceled wait changed latches: %v %v", pending, err)
	}
	if !strings.Contains(logs.String(), "status=canceled") {
		t.Fatalf("cancel diagnostics missing: %s", logs.String())
	}
	f.mu.Lock()
	f.pendingUntil = time.Time{}
	f.mu.Unlock()
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 2 || len(f.deleteFilters()) != 0 {
		t.Fatal("first-index retry must upsert both without deletion")
	}
}

func assertPendingCount(t *testing.T, st *store.Store, id int64, want int) {
	t.Helper()
	pending, err := st.PendingIndexChunks(id, 1000)
	if err != nil || len(pending) != want {
		t.Fatalf("pending=%d want=%d err=%v", len(pending), want, err)
	}
}

func TestPartialBoundedLatchFailureRebuildsCompleteSnapshot(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	chunks := make([]store.Chunk, 501)
	for i := range chunks {
		chunks[i] = store.Chunk{StartCueIdx: i * 2, EndCueIdx: i*2 + 1, Text: "dialog"}
	}
	if err := st.ReplaceChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_last_latch BEFORE UPDATE OF indexed_at ON chunks WHEN NEW.id=(SELECT MAX(id) FROM chunks) BEGIN SELECT RAISE(FAIL, 'latch blocked'); END`); err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "")
	ix.batch = 100
	handler := ix.JobHandler()
	if err := handler(context.Background(), indexJob(id), nil); err == nil {
		t.Fatal("latch failure not propagated")
	}
	assertPendingCount(t, st, id, 1)
	if len(f.docs) != 501 {
		t.Fatalf("remote docs before retry=%d", len(f.docs))
	}
	if _, err := st.DB().Exec(`DROP TRIGGER fail_last_latch`); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), indexJob(id), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 501 {
		t.Fatalf("retry lost partially latched docs: %d", len(f.docs))
	}
	assertPendingCount(t, st, id, 0)
	for _, request := range f.requests {
		if request.path != "/indexes/"+IndexName+"/documents" {
			continue
		}
		// The HTTP batching limit remains independent of the 500-ID SQLite limit.
		if strings.Count(string(request.body), `"mediaId"`) > 100 {
			t.Fatal("unbounded upload batch")
		}
	}
}
