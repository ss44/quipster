package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func readIndexSnapshot(t *testing.T, s *Store, id int64) IndexSnapshot {
	t.Helper()
	snap, err := s.LoadIndexSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func assertSnapshotCounts(t *testing.T, snap IndexSnapshot, total, pending, returned int) {
	t.Helper()
	got := [3]int{snap.Total, snap.Pending, len(snap.Chunks)}
	want := [3]int{total, pending, returned}
	if got != want {
		t.Fatalf("snapshot counts=%v want=%v", got, want)
	}
}

func TestIndexSnapshotIncludesLatchedRows(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	ctx := context.Background()
	snap := readIndexSnapshot(t, s, id)
	assertSnapshotCounts(t, snap, 2, 2, 2)
	if !snap.Active {
		t.Fatal("active media excluded")
	}
	if err := s.MarkIndexSnapshotChunks(ctx, id, []int64{snap.Chunks[0].ID}, 42); err != nil {
		t.Fatal(err)
	}
	partial := readIndexSnapshot(t, s, id)
	assertSnapshotCounts(t, partial, 2, 1, 2)
	if partial.Chunks[0].IndexedAt != 42 {
		t.Fatal("latched chunk missing")
	}
	if err := s.MarkIndexSnapshotChunks(ctx, id, []int64{snap.Chunks[1].ID}, 42); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIndexSnapshotComplete(ctx, id); err != nil {
		t.Fatal(err)
	}
	complete := readIndexSnapshot(t, s, id)
	assertSnapshotCounts(t, complete, 2, 0, 0)
	if !complete.EverIndexed {
		t.Fatal("completion not latched")
	}
}

func TestIndexSnapshotCueBounds(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	snap := readIndexSnapshot(t, s, id)
	assertSnapshotCounts(t, snap, 2, 2, 2)
	if snap.Chunks[0].StartSec != 0 || snap.Chunks[0].EndSec != 3 || snap.Chunks[1].StartSec != 3 || snap.Chunks[1].EndSec != 6 {
		t.Fatalf("cue bounds=%+v", snap.Chunks)
	}
}

func TestIndexSnapshotReadDoesNotAcquireWriter(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	tx, err := s.writeDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTx(tx)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	snap, err := s.LoadIndexSnapshot(ctx, id)
	if err != nil {
		t.Fatalf("read blocked by writer transaction: %v", err)
	}
	assertSnapshotCounts(t, snap, 2, 2, 2)
}

func TestIndexSnapshotEmptyMissingArchivedAndCanceled(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	ctx := context.Background()
	missing := readIndexSnapshot(t, s, -1)
	if missing.Active {
		t.Fatal("missing media active")
	}
	if err := s.ReplaceChunks(id, nil); err != nil {
		t.Fatal(err)
	}
	empty := readIndexSnapshot(t, s, id)
	assertSnapshotCounts(t, empty, 0, 0, 0)
	if !empty.Active {
		t.Fatal("empty active media excluded")
	}
	if _, err := s.DB().Exec(`UPDATE media_files SET archived=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	archived := readIndexSnapshot(t, s, id)
	if archived.Active {
		t.Fatal("archived media active")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.LoadIndexSnapshot(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("load cancellation=%v", err)
	}
	if err := s.MarkIndexSnapshotChunks(canceled, id, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("latch cancellation=%v", err)
	}
	if err := s.MarkIndexSnapshotComplete(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion cancellation=%v", err)
	}
}

func TestIndexSnapshotClosedStoreErrors(t *testing.T) {
	s := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.LoadIndexSnapshot(ctx, 1); err == nil {
		t.Fatal("load accepted closed store")
	}
	if err := s.MarkIndexSnapshotChunks(ctx, 1, []int64{1}, 1); err == nil {
		t.Fatal("latch accepted closed store")
	}
	if err := s.MarkIndexSnapshotComplete(ctx, 1); err == nil {
		t.Fatal("completion accepted closed store")
	}
}
