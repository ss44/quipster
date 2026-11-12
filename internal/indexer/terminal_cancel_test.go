package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/store"
)

func TestRemoteTerminalCanceledTaskDoesNotLatchOrComplete(t *testing.T) {
	for _, stage := range []string{"add", "delete"} {
		t.Run(stage, func(t *testing.T) {
			f := newFakeMeili(t)
			st := newTestStore(t)
			id, _ := seedMedia(t, st)
			taskType := meili.TaskType("documentAddition")
			// The fake must not apply documents for a terminal canceled task.
			f.addTasksFail = true
			if stage == "delete" {
				taskType = meili.TaskType("documentDeletion")
				f.deleteTasksFail = true
				if err := st.MarkIndexed(id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.DB().Exec(`UPDATE media_files SET status='pending', error='retry pending' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			canceled := make(chan struct{}, 1)
			srv := terminalCanceledTaskServer(t, f, taskType, canceled)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var values []float64
			err := New(st, srv.URL, "").JobHandler()(ctx, indexJob(id), func(v float64) { values = append(values, v) })
			if err == nil || !strings.Contains(err.Error(), stage+" failed") || !strings.Contains(err.Error(), "status=canceled") {
				t.Fatalf("want terminal remote %s cancellation, got %v", stage, err)
			}
			if ctx.Err() != nil {
				t.Fatalf("must fail on remote terminal status, not local context: %v", ctx.Err())
			}
			select {
			case <-canceled:
			default:
				t.Fatal("did not serve a canceled task-status response")
			}
			assertTerminalCancellationUnlatched(t, st, id, stage == "delete", values)
			if stage == "delete" && len(f.addedDocs()) != 0 {
				t.Fatal("canceled delete must stop before uploading replacement documents")
			}
		})
	}
}

func terminalCanceledTaskServer(t *testing.T, f *fakeMeili, taskType meili.TaskType, canceled chan<- struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/") {
			uid, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/tasks/"), 10, 64)
			if err != nil {
				http.Error(w, "invalid task UID", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			task, ok := f.tasks[uid]
			if ok && task.Type == taskType {
				task.Status = meili.TaskStatusCanceled
				f.tasks[uid] = task
				select {
				case canceled <- struct{}{}:
				default:
				}
			}
			f.mu.Unlock()
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

func assertTerminalCancellationUnlatched(t *testing.T, st *store.Store, id int64, wantEver bool, values []float64) {
	t.Helper()
	assertPendingCount(t, st, id, 2)
	m, err := st.GetMedia(id)
	if err != nil || m == nil {
		t.Fatalf("read media: media=%v err=%v", m, err)
	}
	ever, err := st.EverIndexed(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "pending" || m.Error != "retry pending" || ever != wantEver {
		t.Fatalf("terminal cancellation changed media completion state: media=%+v ever_indexed=%t", m, ever)
	}
	for _, v := range values {
		if v >= 1 {
			t.Fatalf("terminal cancellation reported completion: %v", values)
		}
	}
}
