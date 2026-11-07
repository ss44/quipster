package scan

import (
	"path/filepath"
	"testing"

	"quipster/internal/store"
)

func openQueueTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func TestEnqueueScanJobExactPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		roots   []string
		prune   bool
	}{
		{name: "nil roots", roots: nil, prune: true, payload: `{"prune":true,"roots":null}`},
		{name: "empty roots", roots: []string{}, prune: false, payload: `{"prune":false,"roots":[]}`},
		{name: "folders", roots: []string{"movies", "tv/comedy"}, prune: true, payload: `{"prune":true,"roots":["movies","tv/comedy"]}`},
		{name: "escaping", roots: []string{`movies/"quoted"`, `tv\anime`, `<&>`}, prune: false, payload: `{"prune":false,"roots":["movies/\"quoted\"","tv\\anime","\u003c\u0026\u003e"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openQueueTestStore(t)
			id, err := EnqueueScanJob(st, tt.roots, tt.prune)
			if err != nil {
				t.Fatal(err)
			}
			job, err := st.GetJob(id)
			if err != nil {
				t.Fatal(err)
			}
			if job == nil {
				t.Fatal("enqueued scan job not found")
			}
			if job.Type != "scan" || job.Payload != tt.payload || job.Priority != 3 {
				t.Fatalf("job = type %q payload %q priority %d, want scan %q 3", job.Type, job.Payload, job.Priority, tt.payload)
			}
		})
	}
}

func TestEnqueueScanJobIsNotUnique(t *testing.T) {
	st := openQueueTestStore(t)
	first, err := EnqueueScanJob(st, []string{"movies"}, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnqueueScanJob(st, []string{"movies"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("ordinary enqueue returned the same id %d twice", first)
	}
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='scan'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("scan job count = %d, want 2", count)
	}
}

func TestEnqueueScanJobReturnsStoreError(t *testing.T) {
	st := openQueueTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := EnqueueScanJob(st, nil, true)
	if err == nil {
		t.Fatal("closed store enqueue returned no error")
	}
	if id != 0 {
		t.Fatalf("closed store enqueue id = %d, want 0", id)
	}
}
