package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveFixture builds a media file with a subtitle track, cues and chunks so
// archive/restore tests can prove that caption data survives every transition.
func archiveFixture(t *testing.T, st *Store, path string) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia(path, "mkv", 100, 1, "Show", 2020, 0, 0, "")
	require.NoError(t, err)
	track, err := st.ReplaceTrack(id, &SubtitleTrack{Source: "embedded", Lang: "en"})
	require.NoError(t, err)
	cues := []Cue{
		{MediaFileID: id, TrackID: track, Idx: 0, StartSec: 0, EndSec: 1, Text: "hello there"},
		{MediaFileID: id, TrackID: track, Idx: 1, StartSec: 1, EndSec: 2, Text: "general kenobi"},
	}
	require.NoError(t, st.ReplaceCues(id, track, cues))
	require.NoError(t, st.ReplaceChunks(id, []Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 1, Text: "hello there general kenobi"},
	}))
	require.NoError(t, st.MarkIndexed(id))
	// Latch the chunks as indexed, matching a fully indexed file.
	pending, err := st.PendingIndexChunks(id, 10)
	require.NoError(t, err)
	ids := make([]int64, 0, len(pending))
	for _, ch := range pending {
		ids = append(ids, ch.ID)
	}
	require.NoError(t, st.MarkChunksIndexed(ids, 42))
	return id
}

func archiveAll(t *testing.T, st *Store, base string, ids ...int64) {
	t.Helper()
	now := time.UnixMilli(1_000_000)
	for _, id := range ids {
		var path string
		require.NoError(t, st.DB().QueryRow(`SELECT path FROM media_files WHERE id=?`, id).Scan(&path))
		// The archive pass treats every row absent from keep as missing, so
		// the target is left out and every sibling row is kept.
		keep := map[string]bool{}
		rows, err := st.DB().Query(`SELECT path FROM media_files WHERE path LIKE ? AND id != ?`, base+"/%", id)
		require.NoError(t, err)
		for rows.Next() {
			var p string
			require.NoError(t, rows.Scan(&p))
			keep[p] = true
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		// Zero grace still archives on the second consecutive observation:
		// the first pass latches missing_at.
		_, _, err = st.ArchiveMissingUnder(base, keep, 0, now)
		require.NoError(t, err)
		archived, _, err := st.ArchiveMissingUnder(base, keep, 0, now)
		require.NoError(t, err)
		require.Contains(t, archived, id, "media %d should archive in the second zero-grace pass", id)
	}
}

func TestArchiveMissingUnderGracePartitioning(t *testing.T) {
	st := openTest(t)
	id1 := archiveFixture(t, st, "/lib/a.mkv")
	id2 := archiveFixture(t, st, "/lib/b.mkv")
	archiveFixture(t, st, "/lib/c.mkv")
	now := time.UnixMilli(1_000_000)
	grace := time.Hour
	keep := map[string]bool{"/lib/c.mkv": true} // seen again by the scan

	// First pass: a and b latch, c is seen and untouched.
	archived, latched, err := st.ArchiveMissingUnder("/lib", keep, grace, now)
	require.NoError(t, err)
	require.Empty(t, archived)
	assert.Equal(t, 2, latched)

	// A row seen again must have its latch cleared only via RestoreSeenUnder,
	// so c still carries missing_at=0 here and both latches are fresh.
	m, err := st.GetMedia(id1)
	require.NoError(t, err)
	assert.False(t, m.Archived)
	assert.Equal(t, now.UnixMilli(), m.MissingAt)

	// Second pass inside the grace window: nothing archives yet.
	archived, latched, err = st.ArchiveMissingUnder("/lib", keep, grace, now.Add(30*time.Minute))
	require.NoError(t, err)
	require.Empty(t, archived)
	assert.Equal(t, 0, latched)

	// Third pass past the grace window: a and b archive, c stays untouched.
	archived, latched, err = st.ArchiveMissingUnder("/lib", keep, grace, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{id1, id2}, archived)
	assert.Equal(t, 0, latched)

	m1, err := st.GetMedia(id1)
	require.NoError(t, err)
	require.True(t, m1.Archived)
	assert.Equal(t, now.Add(2*time.Hour).UnixMilli(), m1.ArchivedAt)
	assert.Equal(t, "indexed", m1.Status, "archive must freeze the processing status")
	assert.False(t, m1.Deindexed)

	// Repeated scans of a long-gone share are free: already-archived rows are
	// skipped and nothing re-latches.
	archived, latched, err = st.ArchiveMissingUnder("/lib", keep, grace, now.Add(3*time.Hour))
	require.NoError(t, err)
	require.Empty(t, archived)
	assert.Equal(t, 0, latched)
}

func TestArchiveMissingUnderKeepsCaptions(t *testing.T) {
	st := openTest(t)
	id := archiveFixture(t, st, "/lib/a.mkv")
	archiveAll(t, st, "/lib", id)

	cues, err := st.CueCount(id)
	require.NoError(t, err)
	assert.Equal(t, int64(2), cues, "cues must survive archiving")

	chunks, err := st.ChunksForMedia(id)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	tracks, err := st.TracksForMedia(id)
	require.NoError(t, err)
	assert.Len(t, tracks, 1, "subtitle tracks must survive archiving")
}

func TestRestoreSeenUnder(t *testing.T) {
	st := openTest(t)
	deindexed := archiveFixture(t, st, "/lib/a.mkv")
	pending := archiveFixture(t, st, "/lib/b.mkv")
	archiveFixture(t, st, "/lib/c.mkv")

	archiveAll(t, st, "/lib", deindexed, pending)
	// Simulate a completed de-index for a only.
	require.NoError(t, st.MarkMediaDeindexed(deindexed))

	// A scan sees b and c again, but not a. b restores without touching the
	// index (docs were never deleted); a stays archived.
	reindex, restored, err := st.RestoreSeenUnder("/lib", map[string]bool{
		"/lib/b.mkv": true,
		"/lib/c.mkv": true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, restored, "only b transitions out of the archive; c was never latched")
	assert.Empty(t, reindex, "deindexed=0 restores must not re-index")

	mb, err := st.GetMedia(pending)
	require.NoError(t, err)
	require.False(t, mb.Archived)
	assert.Equal(t, "indexed", mb.Status)
	// Chunk flags stay latched: the documents were never deleted.
	flags, err := st.DB().Query(`SELECT indexed_at FROM chunks WHERE media_file_id=?`, pending)
	require.NoError(t, err)
	var latchedFlags []int64
	for flags.Next() {
		var v int64
		require.NoError(t, flags.Scan(&v))
		latchedFlags = append(latchedFlags, v)
	}
	require.NoError(t, flags.Err())
	require.NoError(t, flags.Close())
	assert.NotZero(t, latchedFlags[0], "chunk flags must stay when docs were never deleted")

	// A later scan sees a as well: restore with deindexed=1 must clear chunk
	// flags and report the id for index re-enqueue.
	reindex, restored, err = st.RestoreSeenUnder("/lib", map[string]bool{"/lib/a.mkv": true})
	require.NoError(t, err)
	assert.Equal(t, 1, restored)
	assert.Equal(t, []int64{deindexed}, reindex)

	ma, err := st.GetMedia(deindexed)
	require.NoError(t, err)
	require.False(t, ma.Archived)
	assert.False(t, ma.Deindexed)
	assert.Equal(t, int64(0), ma.MissingAt)

	pendingChunks, err := st.PendingIndexChunks(deindexed, 10)
	require.NoError(t, err)
	assert.Len(t, pendingChunks, 1, "chunks must be stale after a deindexed restore")

	// Restore is idempotent: a second pass over active rows does nothing.
	reindex, restored, err = st.RestoreSeenUnder("/lib", map[string]bool{"/lib/a.mkv": true})
	require.NoError(t, err)
	assert.Empty(t, reindex)
	assert.Equal(t, 0, restored)
}

func TestRestoreMediaAdmin(t *testing.T) {
	st := openTest(t)
	id := archiveFixture(t, st, "/lib/a.mkv")
	archiveAll(t, st, "/lib", id)
	require.NoError(t, st.MarkMediaDeindexed(id))

	needsReindex, restored, err := st.RestoreMedia(id)
	require.NoError(t, err)
	assert.True(t, restored)
	assert.True(t, needsReindex)

	m, err := st.GetMedia(id)
	require.NoError(t, err)
	require.False(t, m.Archived)

	// Restoring an active row is a no-op.
	needsReindex, restored, err = st.RestoreMedia(id)
	require.NoError(t, err)
	assert.False(t, restored)
	assert.False(t, needsReindex)

	// Unknown ids are a no-op too.
	_, restored, err = st.RestoreMedia(424242)
	require.NoError(t, err)
	assert.False(t, restored)
}

func TestMarkMediaDeindexedGuard(t *testing.T) {
	st := openTest(t)
	id := archiveFixture(t, st, "/lib/a.mkv")
	archiveAll(t, st, "/lib", id)
	require.NoError(t, st.MarkMediaDeindexed(id))
	m, err := st.GetMedia(id)
	require.NoError(t, err)
	assert.True(t, m.Deindexed)

	// The archived=1 guard: after restore the latch must not be re-settable.
	_, _, err = st.RestoreMedia(id)
	require.NoError(t, err)
	require.NoError(t, st.MarkMediaDeindexed(id))
	m, err = st.GetMedia(id)
	require.NoError(t, err)
	assert.False(t, m.Deindexed, "deindexed must not latch onto a restored row")
}

func TestArchivedPendingDeindex(t *testing.T) {
	st := openTest(t)
	a := archiveFixture(t, st, "/lib/a.mkv")
	b := archiveFixture(t, st, "/lib/b.mkv")
	archiveAll(t, st, "/lib", a, b)
	require.NoError(t, st.MarkMediaDeindexed(a))

	ids, err := st.ArchivedPendingDeindex()
	require.NoError(t, err)
	assert.Equal(t, []int64{b}, ids)
}

func TestArchivedExcludedFromReadPaths(t *testing.T) {
	st := openTest(t)
	active := archiveFixture(t, st, "/lib/active.mkv")
	hidden := archiveFixture(t, st, "/lib/hidden.mkv")
	archiveAll(t, st, "/lib", hidden)

	// ListMedia defaults to active only.
	page, err := st.ListMedia(10, 0, "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	assert.Equal(t, active, page.Items[0].ID)

	onlyArchived, err := st.ListMedia(10, 0, "", "1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), onlyArchived.Total)
	assert.Equal(t, hidden, onlyArchived.Items[0].ID)

	anyList, err := st.ListMedia(10, 0, "", "any")
	require.NoError(t, err)
	assert.Equal(t, int64(2), anyList.Total)

	// Title fallback never matches archived rows.
	matches, err := st.TitleMatches("Show", 10)
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, active, matches[0].ID)

	// Status-driven sweeps exclude archived and in-grace rows.
	_, err = st.DB().Exec(`UPDATE media_files SET missing_at=1 WHERE id=?`, active)
	require.NoError(t, err)
	ids, err := st.MediaIDsByStatus("indexed")
	require.NoError(t, err)
	assert.Empty(t, ids, "missing_at rows must not be re-probed during grace")

	// Index-driven sweeps exclude archived rows. Make the active row's chunks
	// stale so MediaIDsNeedingIndex has something to find.
	require.NoError(t, st.ClearChunkIndexFlags(active))
	need, err := st.MediaIDsNeedingIndex(10)
	require.NoError(t, err)
	assert.Equal(t, []int64{active}, need, "only the active row needs indexing")
	_, err = st.DB().Exec(`UPDATE media_files SET missing_at=0 WHERE id=?`, active)
	require.NoError(t, err)

	// Status counts carry the archive bucket separately.
	counts, err := st.MediaStatusCounts()
	require.NoError(t, err)
	assert.Equal(t, int64(1), counts["indexed"])
	assert.Equal(t, int64(1), counts["archived"])
}

func TestArchivedChunkResolversReturnNil(t *testing.T) {
	st := openTest(t)
	id := archiveFixture(t, st, "/lib/a.mkv")
	archiveAll(t, st, "/lib", id)

	ch, err := st.GetChunkByKey(id, 0, 1)
	require.NoError(t, err)
	assert.Nil(t, ch, "archived media must not resolve chunks by key")

	ch, err = st.ChunkAtTime(id, 0.5)
	require.NoError(t, err)
	assert.Nil(t, ch, "archived media must not resolve chunks by time")

	// The active row keeps resolving through the same paths.
	// ChunkAtTime on an archived row must not resolve either.
	ids, err := st.DB().Query(`SELECT id FROM chunks WHERE media_file_id=?`, id)
	require.NoError(t, err)
	require.True(t, ids.Next(), "chunks still exist for archived media")
	var chunkID int64
	require.NoError(t, ids.Scan(&chunkID))
	require.NoError(t, ids.Close())
	_, err = st.DB().Exec(`UPDATE media_files SET archived=0 WHERE id=?`, id)
	require.NoError(t, err)
	ch, err = st.GetChunkByID(chunkID)
	require.NoError(t, err)
	require.NotNil(t, ch, "active media resolves chunks by id")
}

func TestPendingIndexChunksExcludesArchived(t *testing.T) {
	st := openTest(t)
	a := archiveFixture(t, st, "/lib/a.mkv")
	b := archiveFixture(t, st, "/lib/b.mkv")
	archiveAll(t, st, "/lib", a)
	require.NoError(t, st.ClearChunkIndexFlags(b))

	pending, err := st.PendingIndexChunks(0, 10)
	require.NoError(t, err)
	for _, ch := range pending {
		assert.NotEqual(t, a, ch.MediaFileID, "archived media must not feed index jobs")
	}
	pending, err = st.PendingIndexChunks(a, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)

	pending, err = st.PendingIndexChunks(b, 10)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
}

func TestRandomQuotesAndExamplesExcludeArchived(t *testing.T) {
	st := openTest(t)
	hidden := archiveFixture(t, st, "/lib/hidden.mkv")
	active := archiveFixture(t, st, "/lib/active.mkv")
	archiveAll(t, st, "/lib", hidden)

	quotes, err := st.RandomQuotes(10)
	require.NoError(t, err)
	require.Len(t, quotes, 1)
	assert.Equal(t, active, quotes[0].MediaFileID)

	examples, err := st.PopularExamples([]string{"hello there"})
	require.NoError(t, err)
	assert.NotEmpty(t, examples, "the active row still backs the example")

	// With only the archived row left, neither surface has anything to show.
	_, err = st.DB().Exec(`DELETE FROM chunks WHERE media_file_id=?`, active)
	require.NoError(t, err)
	quotes, err = st.RandomQuotes(10)
	require.NoError(t, err)
	assert.Empty(t, quotes)
	examples, err = st.PopularExamples([]string{"hello there"})
	require.NoError(t, err)
	assert.Empty(t, examples)
	_ = hidden
}
