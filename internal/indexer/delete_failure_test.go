package indexer

import (
	"strings"
	"testing"
)

func TestIndexRejectsFailedDeleteBeforeAddOrLatch(t *testing.T) {
	f := newFakeMeili(t)
	f.deleteTasksFail = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	err := New(st, f.url(), "").IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "delete failed") {
		t.Fatalf("expected async delete failure, got %v", err)
	}
	if len(f.addedDocs()) != 0 {
		t.Fatal("added despite failed deletion")
	}
	pending, err := st.PendingIndexChunks(id, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("latches changed: %d pending, %v", len(pending), err)
	}
}
