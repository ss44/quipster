package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

// TestOpenAppliesWALSizeLimit verifies pooled connections inherit the WAL
// size cap so a large one-off write cannot leave an unbounded sidecar.
func TestOpenAppliesWALSizeLimit(t *testing.T) {
	s := openTest(t)
	var limit int64
	if err := s.db.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil {
		t.Fatalf("read journal_size_limit: %v", err)
	}
	if limit != walSizeLimitBytes {
		t.Fatalf("journal_size_limit = %d, want %d", limit, walSizeLimitBytes)
	}
}

// TestOpenAppliesBusyTimeout verifies pooled connections wait long enough for
// batched subtitle writes to release SQLite's writer lock.
func TestOpenAppliesBusyTimeout(t *testing.T) {
	s := openTest(t)
	var timeout int64
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != busyTimeoutMS {
		t.Fatalf("busy_timeout = %d, want %d", timeout, busyTimeoutMS)
	}
}

// TestOpenCreatesChunksMediaIndex verifies the migration indexes chunk deletes
// by media_file_id instead of scanning the whole chunks table.
func TestOpenCreatesChunksMediaIndex(t *testing.T) {
	s := openTest(t)
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_chunks_media'`).Scan(&name)
	if err != nil {
		t.Fatalf("idx_chunks_media missing: %v", err)
	}
}

// TestUpsertMediaReturnsUpdatedRowID guards against using LastInsertId after
// an ON CONFLICT DO UPDATE: that rowid belongs to whichever row was inserted
// last on the connection, so a changed existing file would hand scan the wrong
// id and enqueue a probe job for a nonexistent or unrelated media row.
func TestUpsertMediaReturnsUpdatedRowID(t *testing.T) {
	s := openTest(t)
	s.writeDB.SetMaxOpenConns(1)

	idA, changed, err := s.UpsertMedia("/media/a.mkv", "mkv", 1, 1, "A", 0, 0, 0, "")
	if err != nil || !changed {
		t.Fatalf("insert A: id=%d changed=%v err=%v", idA, changed, err)
	}
	if _, _, insErr := s.UpsertMedia("/media/b.mkv", "mkv", 1, 1, "B", 0, 0, 0, ""); insErr != nil {
		t.Fatalf("insert B: %v", insErr)
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

// TestMarkChunksIndexedBatches covers media large enough to span multiple
// bounded UPDATE statements and verifies every chunk is stamped.
func TestMarkChunksIndexedBatches(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	const total = markIndexedBatchSize*2 + 7
	chunks := make([]Chunk, total)
	for i := range chunks {
		chunks[i] = Chunk{MediaFileID: id, StartCueIdx: i, EndCueIdx: i, Text: "line"}
	}
	if err := s.ReplaceChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	all, err := s.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("chunks = %d, want %d", len(all), total)
	}
	ids := make([]int64, len(all))
	for i := range all {
		ids[i] = all[i].ID
	}
	if merr := s.MarkChunksIndexed(ids, 99); merr != nil {
		t.Fatal(merr)
	}
	after, err := s.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range after {
		if c.IndexedAt != 99 {
			t.Fatalf("chunk %d indexed_at = %d, want 99", c.ID, c.IndexedAt)
		}
	}
}

// TestReplaceCuesBatches verifies cue writes spanning multiple bounded
// transactions preserve every row.
func TestReplaceCuesBatches(t *testing.T) {
	s := openTest(t)
	id, trackID := setupChunkRelink(t, s)
	const total = cueWriteBatchSize*2 + 3
	cues := make([]Cue, total)
	for i := range cues {
		cues[i] = Cue{MediaFileID: id, TrackID: trackID, Idx: i, StartSec: float64(i), EndSec: float64(i) + 1, Text: "line"}
	}
	if err := s.ReplaceCues(id, trackID, cues); err != nil {
		t.Fatal(err)
	}
	n, err := s.CueCount(id)
	if err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Fatalf("cues = %d, want %d", n, total)
	}
}

// TestReplaceChunksBatches verifies chunk writes spanning multiple bounded
// transactions preserve every row.
func TestReplaceChunksBatches(t *testing.T) {
	s := openTest(t)
	id, _ := setupChunkRelink(t, s)
	const total = chunkWriteBatchSize*2 + 3
	chunks := make([]Chunk, total)
	for i := range chunks {
		chunks[i] = Chunk{MediaFileID: id, StartCueIdx: i, EndCueIdx: i, Text: "line"}
	}
	if err := s.ReplaceChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChunksForMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("chunks = %d, want %d", len(got), total)
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

func TestPoolsApplySQLitePragmas(t *testing.T) {
	s := openTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, pool := range []*sql.DB{s.writeDB, s.db, s.db} {
		conn, err := pool.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, conn.Close()) })
		var mode string
		require.NoError(t, conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode))
		require.Equal(t, "wal", mode)
		for query, want := range map[string]int64{
			"PRAGMA busy_timeout":       busyTimeoutMS,
			"PRAGMA journal_size_limit": walSizeLimitBytes,
			"PRAGMA foreign_keys":       1,
			"PRAGMA synchronous":        1,
		} {
			var got int64
			require.NoError(t, conn.QueryRowContext(ctx, query).Scan(&got))
			require.Equal(t, want, got, query)
		}
	}
}

func TestWriterPoolWALReadersAndCancellation(t *testing.T) {
	s := openTest(t)
	require.Same(t, s.writeDB, s.DB())
	require.NotSame(t, s.db, s.writeDB)
	require.Equal(t, 1, s.writeDB.Stats().MaxOpenConnections)
	require.NoError(t, s.SetSetting("key", "committed"))
	jobID, err := s.EnqueueJob("probe", "{}", 0)
	require.NoError(t, err)
	tx, err := s.writeDB.Begin()
	require.NoError(t, err)
	defer rollbackTx(tx)
	_, err = tx.Exec(`UPDATE settings SET value='uncommitted' WHERE key='key'`)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader1, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader1.Close()) }()
	reader2, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader2.Close()) }()
	for _, reader := range []*sql.Conn{reader1, reader2} {
		var value string
		require.NoError(t, reader.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='key'`).Scan(&value))
		require.Equal(t, "committed", value)
	}
	job, err := s.ClaimJob(ctx, "worker", []string{"clip"})
	require.NoError(t, err)
	require.Nil(t, job)

	claimCtx, claimCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer claimCancel()
	job, err = s.ClaimJob(claimCtx, "worker", []string{"probe"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, job)
	waitCount := s.writeDB.Stats().WaitCount
	require.Positive(t, waitCount)

	done := make(chan error, 1)
	go func() { done <- s.SetSetting("other", "after") }()
	require.Eventually(t, func() bool { return s.writeDB.Stats().WaitCount > waitCount }, 2*time.Second, time.Millisecond)
	select {
	case writeErr := <-done:
		t.Fatalf("second writer completed before commit: %v", writeErr)
	default:
	}
	require.NoError(t, tx.Commit())
	select {
	case writeErr := <-done:
		require.NoError(t, writeErr)
	case <-time.After(2 * time.Second):
		t.Fatal("second writer did not resume")
	}
	job, err = s.ClaimJob(ctx, "worker", []string{"probe"})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, jobID, job.ID)
}

func TestClaimRollbackReleasesWriter(t *testing.T) {
	s := openTest(t)
	id, err := s.EnqueueJob("probe", "{}", 0)
	require.NoError(t, err)
	_, err = s.DB().Exec(`CREATE TRIGGER fail_claim BEFORE UPDATE ON jobs BEGIN SELECT RAISE(ABORT, 'claim failure'); END`)
	require.NoError(t, err)
	_, err = s.ClaimJob(context.Background(), "worker", []string{"probe"})
	require.Error(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = s.DB().ExecContext(ctx, `DROP TRIGGER fail_claim`)
	require.NoError(t, err)
	job, err := s.ClaimJob(ctx, "worker", []string{"probe"})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, id, job.ID)
}

func TestMemoryStoreUsesIsolatedSharedPool(t *testing.T) {
	first, err := Open(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()
	second, err := Open(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Close()) }()
	require.Same(t, first.db, first.writeDB)
	require.Same(t, first.db, first.DB())
	require.Equal(t, 1, first.db.Stats().MaxOpenConnections)
	require.NoError(t, first.SetSetting("key", "value"))
	value, ok, err := first.GetSetting("key")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "value", value)
	_, ok, err = second.GetSetting("key")
	require.NoError(t, err)
	require.False(t, ok)
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
