package indexer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func captureIndexLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func TestIndexStageDiagnosticsAndProgress(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	logs := captureIndexLogs(t)
	var values []float64
	if err := New(st, f.url(), "private-key").JobHandler()(context.Background(), indexJob(id), func(v float64) { values = append(values, v) }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stage=snapshot", "stage=build", "stage=delete", "stage=add", "stage=latch", "elapsed=", "chunks=2", "pending=2", "taskUID=", "status=succeeded"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in logs: %s", want, logs.String())
		}
	}
	for _, forbidden := range []string{"private-key", "hello world", "goodbye moon", "/tv/"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("sensitive content in logs: %q", forbidden)
		}
	}
	if len(values) < 2 || values[0] <= 0 || values[len(values)-1] != 1 {
		t.Fatalf("missing staged progress/completion: %v", values)
	}
	for i, v := range values {
		if v > 1 || (i > 0 && v < values[i-1]) {
			t.Fatalf("nonmonotonic progress: %v", values)
		}
	}
}

func TestSlowTaskHTTPReportsPeriodicProgress(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	f.taskGetPause = 5600 * time.Millisecond
	captureIndexLogs(t)
	started := time.Now()
	heartbeat := false
	err := New(st, f.url(), "").JobHandler()(context.Background(), indexJob(id), func(v float64) {
		if v == 0.25 && time.Since(started) > 4*time.Second {
			heartbeat = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat {
		t.Fatal("no progress while task HTTP request was slow")
	}
}

func TestSlowTaskHTTPCancellation(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	f.taskGetPause = time.Hour
	logs := captureIndexLogs(t)
	// Bound the whole test, but trigger cancellation only once the slow
	// request starts. A deadline begun before SQLite setup tests the wrong stage.
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
	err := New(st, srv.URL, "").JobHandler()(ctx, indexJob(id), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want slow-HTTP cancellation, got %v", err)
	}
	select {
	case started := <-arrived:
		if time.Since(started) > time.Second {
			t.Fatal("slow HTTP request prevented cancellation")
		}
	default:
		t.Fatal("canceled before reaching task-status HTTP request")
	}
	pending, pendingErr := st.PendingIndexChunks(id, 10)
	if pendingErr != nil || len(pending) != 2 {
		t.Fatalf("canceled HTTP wait latched chunks: pending=%d err=%v", len(pending), pendingErr)
	}
	if !strings.Contains(logs.String(), "status=canceled") {
		t.Fatalf("missing cancel stage: %s", logs.String())
	}
	assertPendingCount(t, st, id, 2)
}

func TestSlowTaskReportsBoundedPeriodicProgress(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	f.pendingUntil = time.Now().Add(5600 * time.Millisecond)
	logs := captureIndexLogs(t)
	var updates []time.Time
	if err := New(st, f.url(), "").JobHandler()(context.Background(), indexJob(id), func(v float64) {
		if v == 0.25 {
			updates = append(updates, time.Now())
		}
	}); err != nil {
		t.Fatal(err)
	}
	periodic := false
	for i := 1; i < len(updates); i++ {
		if updates[i].Sub(updates[i-1]) >= 4*time.Second {
			periodic = true
		}
	}
	if !periodic {
		t.Fatalf("no heartbeat during slow task: %v", updates)
	}
	if strings.Count(logs.String(), "status=enqueued") > 3 {
		t.Fatalf("poll log spam: %s", logs.String())
	}
}
