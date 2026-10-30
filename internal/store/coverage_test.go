package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildDSN mirrors the DSN construction used by Open so tests can pre-seed
// legacy or broken database files through the same driver configuration.
func buildDSN(path string) string {
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
}

// TestLegacySchemaMigration verifies migrate adds the newer media_files
// columns to databases created before they existed.
func TestLegacySchemaMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", buildDSN(path))
	require.NoError(t, err)
	// Old-schema table: no ever_indexed, audio_stream_index or audio_lang.
	_, err = db.Exec(`CREATE TABLE media_files (
		id INTEGER PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		container TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0,
		duration_sec REAL NOT NULL DEFAULT 0,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		video_codec TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		year INTEGER NOT NULL DEFAULT 0,
		show_title TEXT NOT NULL DEFAULT '',
		season INTEGER NOT NULL DEFAULT 0,
		episode INTEGER NOT NULL DEFAULT 0,
		mtime INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending',
		error TEXT NOT NULL DEFAULT '',
		added_at INTEGER NOT NULL DEFAULT 0,
		start_time REAL NOT NULL DEFAULT 0
	)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := Open(path)
	require.NoError(t, err)

	id, _, err := s.UpsertMedia("/legacy/a.mkv", "mkv", 1, 1, "Legacy", 0, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, s.UpdateMediaProbe(id, 10, 1, 1, "h264", "aac", "mkv", 0, 2, "eng"))
	m, err := s.GetMedia(id)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, 2, m.AudioStreamIndex)
	assert.Equal(t, "eng", m.AudioLang)
	require.NoError(t, s.Close())
}

// TestOpenOnGarbageFile covers the migrate failure branch of Open.
func TestOpenOnGarbageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	require.NoError(t, os.WriteFile(path, []byte("this is not a database"), 0o600))
	s, err := Open(path)
	require.Error(t, err)
	require.Nil(t, s)
}

// TestOpenOnDirectory covers the ping failure branch of Open.
func TestOpenOnDirectory(t *testing.T) {
	s, err := Open(t.TempDir())
	require.Error(t, err)
	require.Nil(t, s)
}

func TestDBAccessor(t *testing.T) {
	s := openTest(t)
	require.NotNil(t, s.DB())
}

// TestClosedStoreErrors sweeps every store operation after the underlying
// database has been closed, exercising the error-return branches.
func TestClosedStoreErrors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "closed.db"))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	ctx := context.Background()

	// Jobs.
	_, err = s.EnqueueJob("t", "{}", 0)
	require.Error(t, err)
	_, _, err = s.EnqueueUniqueJob("t", "{}", 0)
	require.Error(t, err)
	_, err = s.ClaimJob(ctx, "w", []string{"t"})
	require.Error(t, err)
	require.Error(t, s.FinishJob(1, "done", ""))
	require.Error(t, s.SetJobProgress(1, 0.5))
	require.Error(t, s.RequeueJob(1, "e"))
	require.Error(t, s.RequeueJobSoft(1, "e"))
	n, err := s.ClearQueuedJobs()
	require.Error(t, err)
	assert.Zero(t, n)
	_, err = s.JobStats()
	require.Error(t, err)
	_, err = s.ListJobs(1)
	require.Error(t, err)
	_, err = s.RunningJobs(1)
	require.Error(t, err)
	require.Error(t, s.Recover())
	_, err = s.LineStats()
	require.Error(t, err)

	// Subtitles.
	_, err = s.ReplaceTrack(1, &SubtitleTrack{Source: "sidecar"})
	require.Error(t, err)
	_, err = s.TracksForMedia(1)
	require.Error(t, err)
	require.Error(t, s.ReplaceCues(1, 1, nil))
	_, err = s.CueCount(1)
	require.Error(t, err)
	require.Error(t, s.CleanSubtitles(1))
	_, _, _, err = s.CuesInRange(1, 0, 1)
	require.Error(t, err)

	// Chunks.
	require.Error(t, s.ReplaceChunks(1, nil))
	_, err = s.ChunksForMedia(1)
	require.Error(t, err)
	_, err = s.PendingIndexChunks(1, 1)
	require.Error(t, err)
	require.Error(t, s.MarkChunksIndexed([]int64{1}, 1))
	require.Error(t, s.ClearChunkIndexFlags(1))
	require.Error(t, s.EnsureAllChunksStale())
	_, err = s.MediaIDsNeedingIndex(1)
	require.Error(t, err)
	_, err = s.GetChunkByKey(1, 0, 1)
	require.Error(t, err)

	// Media.
	_, _, err = s.UpsertMedia("/x", "mp4", 1, 1, "t", 0, 0, 0, "")
	require.Error(t, err)
	_, err = s.GetMedia(1)
	require.Error(t, err)
	_, err = s.GetMediaByPath("/x")
	require.Error(t, err)
	require.Error(t, s.SetMediaStatus(1, "indexed", ""))
	require.Error(t, s.UpdateMediaProbe(1, 1, 1, 1, "a", "b", "c", 0, 0, ""))
	require.Error(t, s.MarkIndexed(1))
	_, err = s.EverIndexed(1)
	require.Error(t, err)
	_, err = s.ListMedia(1, 0, "", "")
	require.Error(t, err)
	_, err = s.MediaStatusCounts()
	require.Error(t, err)
	_, err = s.MediaIDsByStatus("indexed")
	require.Error(t, err)
	_, err = s.TitleMatches("x", 1)
	require.Error(t, err)
	_, _, err = s.ArchiveMissingUnder("/x", nil, 0, time.Now())
	require.Error(t, err)
	_, _, err = s.RestoreSeenUnder("/x", nil)
	require.Error(t, err)
	_, _, err = s.RestoreMedia(1)
	require.Error(t, err)
	require.Error(t, s.MarkMediaDeindexed(1))
	_, err = s.ArchivedPendingDeindex()
	require.Error(t, err)

	// Scans.
	_, err = s.StartScan("/x")
	require.Error(t, err)
	require.Error(t, s.UpdateScan(1, 1, 1, "done"))
	_, err = s.RunningScan()
	require.Error(t, err)
	_, err = s.HasActiveScanJob()
	require.Error(t, err)

	// Settings.
	_, _, err = s.GetSetting("k")
	require.Error(t, err)
	require.Error(t, s.SetSetting("k", "v"))
	_, err = s.GetLibraryPaths()
	require.Error(t, err)
	require.Error(t, s.SetLibraryPaths(nil))
	_, err = s.GetQueuePaused()
	require.Error(t, err)
	require.Error(t, s.SetQueuePaused(true))

	// Cache.
	_, err = s.GetCacheEntry("k")
	require.Error(t, err)
	require.Error(t, s.TouchCache("k"))
	require.Error(t, s.UpsertCacheEntry("k", "/p", "c", 1))
	require.Error(t, s.DeleteCacheEntry("k"))
	_, err = s.CacheBytes("k")
	require.Error(t, err)
	_, err = s.OldestCacheEntries("k")
	require.Error(t, err)
}

// TestReplaceChunksRollbackOnDuplicate verifies ReplaceChunks is all-or-nothing:
// a unique violation on start_cue_idx rolls back the whole transaction.
func TestReplaceChunksRollbackOnDuplicate(t *testing.T) {
	s := openTest(t)
	id, _, err := s.UpsertMedia("/media/dup.mkv", "mkv", 1, 1, "Dup", 0, 0, 0, "")
	require.NoError(t, err)
	trackID, err := s.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	require.NoError(t, err)
	cues := make([]Cue, 0, 3)
	for i := 0; i < 3; i++ {
		cues = append(cues, Cue{MediaFileID: id, TrackID: trackID, Idx: i})
	}
	require.NoError(t, s.ReplaceCues(id, trackID, cues))

	// Second chunk re-uses start_cue_idx 0 → UNIQUE(media_file_id, start_cue_idx) fails.
	chunks := []Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 1, Text: "ok"},
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 2, Text: "conflict"},
	}
	require.Error(t, s.ReplaceChunks(id, chunks))
	got, err := s.ChunksForMedia(id)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestReplaceCuesRollbackOnForeignKey verifies ReplaceCues fails atomically when
// a cue references a missing subtitle track.
func TestReplaceCuesRollbackOnForeignKey(t *testing.T) {
	s := openTest(t)
	id, _, err := s.UpsertMedia("/media/fk.mkv", "mkv", 1, 1, "FK", 0, 0, 0, "")
	require.NoError(t, err)
	err = s.ReplaceCues(id, 999999, []Cue{{MediaFileID: id, TrackID: 999999, Idx: 0, Text: "orphan"}})
	require.Error(t, err)
	n, err := s.CueCount(id)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// TestChunkAndSubtitleQueries covers the read-side chunk/subtitle accessors.
func TestChunkAndSubtitleQueries(t *testing.T) {
	s := openTest(t)
	id, _, err := s.UpsertMedia("/media/series.mkv", "mkv", 1, 1, "", 0, 0, 0, "")
	require.NoError(t, err)
	// ReplaceTrack replaces any previous track for the media.
	_, err = s.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	require.NoError(t, err)
	trackID2, err := s.ReplaceTrack(id, &SubtitleTrack{Source: "embedded", Lang: "en"})
	require.NoError(t, err)
	tracks, err := s.TracksForMedia(id)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	assert.Equal(t, "embedded", tracks[0].Source)
	assert.Equal(t, trackID2, tracks[0].ID)

	cues := make([]Cue, 0, 6)
	for i := 0; i < 6; i++ {
		cues = append(cues, Cue{MediaFileID: id, TrackID: trackID2, Idx: i, StartSec: float64(i), EndSec: float64(i) + 1, Text: "line"})
	}
	require.NoError(t, s.ReplaceCues(id, trackID2, cues))
	n, err := s.CueCount(id)
	require.NoError(t, err)
	require.EqualValues(t, 6, n)

	chunks := []Chunk{
		{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 2, Text: "a b c"},
		{MediaFileID: id, StartCueIdx: 3, EndCueIdx: 5, Text: "d e f"},
	}
	require.NoError(t, s.ReplaceChunks(id, chunks))
	stored, err := s.ChunksForMedia(id)
	require.NoError(t, err)
	require.Len(t, stored, 2)

	byKey, err := s.GetChunkByKey(id, 3, 5)
	require.NoError(t, err)
	require.NotNil(t, byKey)
	assert.Equal(t, "d e f", byKey.Text)
	assert.Equal(t, 3.0, byKey.StartSec)
	assert.Equal(t, 6.0, byKey.EndSec)
	miss, err := s.GetChunkByKey(id, 0, 99)
	require.NoError(t, err)
	assert.Nil(t, miss)

	ids, err := s.MediaIDsNeedingIndex(10)
	require.NoError(t, err)
	assert.Contains(t, ids, id)

	// Media marked as error-status are excluded from the index queue.
	idErr, _, err := s.UpsertMedia("/media/broken.mkv", "mkv", 1, 1, "", 0, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, s.SetMediaStatus(idErr, "error", ""))
	require.NoError(t, s.ReplaceChunks(idErr, []Chunk{{MediaFileID: idErr, StartCueIdx: 0, EndCueIdx: 0, Text: "x"}}))
	ids, err = s.MediaIDsNeedingIndex(10)
	require.NoError(t, err)
	assert.Contains(t, ids, id)
	assert.NotContains(t, ids, idErr)

	scoped, err := s.PendingIndexChunks(id, 10)
	require.NoError(t, err)
	require.Len(t, scoped, 2)
	// Global scope (mediaFileID=0) spans every pending media, including errored.
	global, err := s.PendingIndexChunks(0, 10)
	require.NoError(t, err)
	require.Len(t, global, 3)

	// Mark, clear and stale cycles.
	require.NoError(t, s.MarkChunksIndexed(nil, 1))
	markIDs := make([]int64, 0, len(scoped))
	for _, c := range scoped {
		markIDs = append(markIDs, c.ID)
	}
	require.NoError(t, s.MarkChunksIndexed(markIDs, 7))
	scoped, err = s.PendingIndexChunks(id, 10)
	require.NoError(t, err)
	assert.Empty(t, scoped)
	require.NoError(t, s.ClearChunkIndexFlags(id))
	scoped, err = s.PendingIndexChunks(id, 10)
	require.NoError(t, err)
	require.Len(t, scoped, 2)
	require.NoError(t, s.EnsureAllChunksStale())
	scoped, err = s.PendingIndexChunks(id, 10)
	require.NoError(t, err)
	require.Len(t, scoped, 2)

	// Neighboring cues are returned around the range.
	span, before, after, err := s.CuesInRange(id, 2, 4)
	require.NoError(t, err)
	require.Len(t, span, 3)
	require.NotNil(t, before)
	require.NotNil(t, after)
	span, before, after, err = s.CuesInRange(id, 0, 2)
	require.NoError(t, err)
	require.Len(t, span, 3)
	assert.Nil(t, before)
	require.NotNil(t, after)

	// CleanSubtitles wipes tracks, cues and chunks in one transaction.
	require.NoError(t, s.CleanSubtitles(id))
	n, err = s.CueCount(id)
	require.NoError(t, err)
	assert.Zero(t, n)
	tracks, err = s.TracksForMedia(id)
	require.NoError(t, err)
	assert.Empty(t, tracks)
	stored, err = s.ChunksForMedia(id)
	require.NoError(t, err)
	assert.Empty(t, stored)
}

// TestEnrichJobsDisplayNames covers every displayName branch through enriched jobs.
func TestEnrichJobsDisplayNames(t *testing.T) {
	s := openTest(t)
	showSeason, _, err := s.UpsertMedia("/tv/s1.mkv", "mkv", 1, 1, "", 0, 1, 2, "The Show")
	require.NoError(t, err)
	showOnly, _, err := s.UpsertMedia("/tv/s2.mkv", "mkv", 1, 1, "", 0, 0, 0, "The Show")
	require.NoError(t, err)
	movieYear, _, err := s.UpsertMedia("/films/f.mkv", "mkv", 1, 1, "Movie", 2020, 0, 0, "")
	require.NoError(t, err)
	moviePlain, _, err := s.UpsertMedia("/films/g.mkv", "mkv", 1, 1, "Movie", 0, 0, 0, "")
	require.NoError(t, err)
	noTitle, _, err := s.UpsertMedia("/raw/file.mkv", "mkv", 1, 1, "", 0, 0, 0, "")
	require.NoError(t, err)

	ids := []int64{showSeason, showOnly, movieYear, moviePlain, noTitle}
	for _, mid := range ids {
		payload := fmt.Sprintf(`{"mediaId":%d}`, mid)
		_, jerr := s.EnqueueJob("index", payload, 0)
		require.NoError(t, jerr)
	}
	// Also enqueue a job with an audio-bearing media to cover mapping fields.
	require.NoError(t, s.UpdateMediaProbe(showSeason, 60, 1280, 720, "h264", "aac", "mkv", 0, 3, "eng"))
	require.NoError(t, s.SetMediaStatus(showSeason, "indexed", ""))

	jobs, err := s.ListJobs(20)
	require.NoError(t, err)
	require.Len(t, jobs, 5)
	byMedia := make(map[int64]Job, len(jobs))
	for _, j := range jobs {
		byMedia[j.MediaID] = j
	}
	assert.Equal(t, "The Show S01E02", byMedia[showSeason].MediaTitle)
	assert.Equal(t, "The Show", byMedia[showOnly].MediaTitle)
	assert.Equal(t, "Movie (2020)", byMedia[movieYear].MediaTitle)
	assert.Equal(t, "Movie", byMedia[moviePlain].MediaTitle)
	assert.Equal(t, "file.mkv", byMedia[noTitle].MediaTitle)
	assert.Equal(t, "/tv/s1.mkv", byMedia[showSeason].MediaPath)
	assert.Equal(t, "indexed", byMedia[showSeason].MediaStatus)
}

// TestEnrichJobsWithoutMediaPayload covers the enrichJobs early-return branches.
func TestEnrichJobsWithoutMediaPayload(t *testing.T) {
	s := openTest(t)
	_, err := s.EnqueueJob("orphan", `{"other":1}`, 0)
	require.NoError(t, err)

	jobs, err := s.ListJobs(0)
	require.NoError(t, err)
	assert.Empty(t, jobs)

	jobs, err = s.ListJobs(5)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, int64(0), jobs[0].MediaID)
	assert.Empty(t, jobs[0].MediaTitle)
}

// TestLibraryPathsEdgeCases covers SetLibraryPaths nil handling, unset/blank
// stored values, invalid JSON and the "true" queue-pause variant.
func TestLibraryPathsEdgeCases(t *testing.T) {
	s := openTest(t)
	require.NoError(t, s.SetLibraryPaths(nil))
	paths, err := s.GetLibraryPaths()
	require.NoError(t, err)
	assert.Empty(t, paths)

	require.NoError(t, s.SetSetting(SettingLibraryPaths, "  "))
	paths, err = s.GetLibraryPaths()
	require.NoError(t, err)
	assert.Nil(t, paths)

	require.NoError(t, s.SetSetting(SettingLibraryPaths, "{not json"))
	_, err = s.GetLibraryPaths()
	require.Error(t, err)

	require.NoError(t, s.SetSetting(SettingQueuePaused, "true"))
	paused, err := s.GetQueuePaused()
	require.NoError(t, err)
	assert.True(t, paused)
}
