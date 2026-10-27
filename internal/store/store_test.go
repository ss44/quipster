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
	t.Cleanup(func() { s.Close() })
	return s
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
	m, _ := s.GetMedia(id)
	if m == nil || m.Status != "pending" {
		t.Fatalf("changed media should reset to pending: %+v", m)
	}
}

func TestReplaceChunksRelinksCues(t *testing.T) {
	s := openTest(t)
	id, _, _ := s.UpsertMedia("/media/b.mkv", "mkv", 1, 1, "B", 0, 0, 0, "")
	trackID, err := s.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	var cues []Cue
	for i := 0; i < 6; i++ {
		cues = append(cues, Cue{MediaFileID: id, TrackID: trackID, Idx: i, StartSec: float64(i), EndSec: float64(i) + 1, Text: "line"})
	}
	if err := s.ReplaceCues(id, trackID, cues); err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 2, Text: "a b c"},
		{MediaFileID: id, StartCueIdx: 3, EndCueIdx: 5, Text: "d e f"},
	}
	if err := s.ReplaceChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
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
	if err := s.MarkChunksIndexed([]int64{pending[0].ID, pending[1].ID}, 42); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingIndexChunks(id, 10)
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
		go func(n int) {
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
	if j, _ := s.ClaimJob(context.Background(), "w", []string{"probe"}); j != nil {
		t.Errorf("queue should be empty")
	}
}

func TestCacheEntriesLRU(t *testing.T) {
	s := openTest(t)
	s.UpsertCacheEntry("k1", "/c/1.mp4", "clip", 100)
	time.Sleep(3 * time.Millisecond)
	s.UpsertCacheEntry("k2", "/c/2.mp4", "clip", 200)
	time.Sleep(3 * time.Millisecond)
	s.TouchCache("k1")
	entries, err := s.OldestCacheEntries("clip")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	if entries[0].CacheKey != "k2" {
		t.Errorf("k2 must be oldest after touching k1")
	}
	if n, _ := s.CacheBytes("clip"); n != 300 {
		t.Errorf("total bytes = %d", n)
	}
}
