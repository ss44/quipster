package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Logf("close test store: %v", err)
		}
	})
	return s
}

// TestUpsertMediaReturnsUpdatedRowID guards against using LastInsertId after
// an ON CONFLICT DO UPDATE: that rowid belongs to whichever row was inserted
// last on the connection, so a changed existing file would hand scan the wrong
// id and enqueue a probe job for a nonexistent or unrelated media row.
func TestUpsertMediaReturnsUpdatedRowID(t *testing.T) {
	s := openTest(t)
	s.db.SetMaxOpenConns(1)

	idA, changed, err := s.UpsertMedia("/media/a.mkv", "mkv", 1, 1, "A", 0, 0, 0, "")
	if err != nil || !changed {
		t.Fatalf("insert A: id=%d changed=%v err=%v", idA, changed, err)
	}
	if _, _, err := s.UpsertMedia("/media/b.mkv", "mkv", 1, 1, "B", 0, 0, 0, ""); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	gotA, changed, err := s.UpsertMedia("/media/a.mkv", "mkv", 2, 2, "A", 0, 0, 0, "")
	if err != nil || !changed {
		t.Fatalf("update A: id=%d changed=%v err=%v", gotA, changed, err)
	}
	if gotA != idA {
		t.Fatalf("upsert of A returned id %d, want %d", gotA, idA)
	}
}

func TestUpsertMediaChangeDetection(t *testing.T) {
	s := openTest(t)
	id, changed, err := s.UpsertMedia("/media/a.mkv", "mkv", 100, 1000, "A", 2020, 0, 0, "")
	if err != nil || changed != true || id == 0 {
		t.Fatalf("first insert: id=%d changed=%v err=%v", id, changed, err)
	}
	_, changed, err = s.UpsertMedia("/media/a.mkv", "mkv", 100, 1000, "A", 2020, 0, 0, "")
	if err != nil || changed {
		t.Fatalf("no-op upsert should not change: changed=%v err=%v", changed, err)
	}
	id2, changed, err := s.UpsertMedia("/media/a.mkv", "mkv", 200, 2000, "A", 2020, 0, 0, "")
	if err != nil || !changed || id2 != id {
		t.Fatalf("size change should mark changed: id=%d changed=%v err=%v", id2, changed, err)
	}
	m, err := s.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Status != "pending" {
		t.Fatalf("changed media should reset to pending: %+v", m)
	}
}

// setupChunkRelink seeds a store with a media row, a track, six cues, and two
// chunks covering cues 0-5. It returns the media id and fails the test on error.
func setupChunkRelink(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	id, _, err := s.UpsertMedia("/media/b.mkv", "mkv", 1, 1, "B", 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	trackID, err := s.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	var cues []Cue
	for i := 0; i < 6; i++ {
		cues = append(cues, Cue{MediaFileID: id, TrackID: trackID, Idx: i, StartSec: float64(i), EndSec: float64(i) + 1, Text: "line"})
	}
	if cerr := s.ReplaceCues(id, trackID, cues); cerr != nil {
		t.Fatal(cerr)
	}
	chunks := []Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 2, Text: "a b c"},
		{MediaFileID: id, StartCueIdx: 3, EndCueIdx: 5, Text: "d e f"},
	}
	if cerr := s.ReplaceChunks(id, chunks); cerr != nil {
		t.Fatal(cerr)
	}
	return id, trackID
}

func TestReplaceChunksRelinksCues(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	pending, err := s.PendingIndexChunks(id, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending chunks = %d err=%v", len(pending), err)
	}
	if pending[0].StartSec != 0 || pending[0].EndSec != 3 {
		t.Errorf("chunk bounds = %v-%v, want 0-3", pending[0].StartSec, pending[0].EndSec)
	}
	// chunk relink: cues 0-2 must point to chunk 1
	span, _, _, err := s.CuesInRange(id, 0, 2)
	if err != nil || len(span) != 3 {
		t.Fatalf("CuesInRange = %d err=%v", len(span), err)
	}
	if span[0].ChunkID == 0 {
		t.Errorf("cue 0 has no chunk link")
	}
	if cerr := s.MarkChunksIndexed([]int64{pending[0].ID, pending[1].ID}, 42); cerr != nil {
		t.Fatal(cerr)
	}
	pending, err = s.PendingIndexChunks(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("chunks still pending after mark")
	}
}

func TestClaimJobExclusive(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 5; i++ {
		if _, err := s.EnqueueJob("probe", `{"mediaId":1}`, 0); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	claimed := 0
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			j, err := s.ClaimJob(context.Background(), "w", []string{"probe"})
			if err != nil || j == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[j.ID] {
				t.Errorf("job %d claimed twice", j.ID)
			}
			seen[j.ID] = true
			claimed++
		}(w)
	}
	wg.Wait()
	if claimed != 5 {
		t.Errorf("claimed %d jobs, want 5", claimed)
	}
	j, err := s.ClaimJob(context.Background(), "w", []string{"probe"})
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Errorf("queue should be empty")
	}
}

func TestCacheEntriesLRU(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertCacheEntry("k1", "/c/1.mp4", "clip", 100); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	if err := s.UpsertCacheEntry("k2", "/c/2.mp4", "clip", 200); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	if err := s.TouchCache("k1"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.OldestCacheEntries("clip")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	if entries[0].CacheKey != "k2" {
		t.Errorf("k2 must be oldest after touching k1")
	}
	n, err := s.CacheBytes("clip")
	if err != nil {
		t.Fatal(err)
	}
	if n != 300 {
		t.Errorf("total bytes = %d", n)
	}
}
