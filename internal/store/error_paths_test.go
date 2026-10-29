package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreEmptyCollectionShortCircuits covers the degenerate-input guards
// that return before touching the database, so bulk callers never open a
// transaction for nothing.
func TestStoreEmptyCollectionShortCircuits(t *testing.T) {
	s := openTest(t)

	ids, changed, err := s.UpsertMediaBatch(nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Nil(t, changed)

	jobIDs, err := s.EnqueueJobs(nil)
	require.NoError(t, err)
	assert.Nil(t, jobIDs)

	n, err := s.EnqueueUniqueJobs(nil)
	require.NoError(t, err)
	assert.Zero(t, n)

	sizes, err := s.MediaSizes(nil)
	require.NoError(t, err)
	assert.Empty(t, sizes)
}

// TestJobLookupsAndStatsEdgeCases covers job lookup nil results, the done and
// error buckets of JobStats, the FinishedJobsSince limit clamp, and payload
// parsing fallbacks that the happy-path job tests leave untouched.
func TestJobLookupsAndStatsEdgeCases(t *testing.T) {
	s := openTest(t)

	if j, err := s.JobByID(404); j != nil || err != nil {
		t.Fatalf("JobByID(missing) = %v, %v", j, err)
	}
	if j, err := s.GetJob(404); j != nil || err != nil {
		t.Fatalf("GetJob(missing) = %v, %v", j, err)
	}
	if status, worker, err := s.JobOwner(404); status != "" || worker != "" || err != nil {
		t.Fatalf("JobOwner(missing) = %q, %q, %v", status, worker, err)
	}

	doneID, err := s.EnqueueJob("probe", `{"mediaId":11}`, 0)
	require.NoError(t, err)
	errID, err := s.EnqueueJob("probe", `{"mediaId":12}`, 0)
	require.NoError(t, err)
	require.NoError(t, s.FinishJob(doneID, "done", ""))
	require.NoError(t, s.FinishJob(errID, "error", "boom"))

	stats, err := s.JobStats()
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.Done)
	assert.EqualValues(t, 1, stats.Error)

	// A non-positive limit is clamped to the ceiling instead of returning
	// nothing, so diagnostics callers cannot accidentally truncate history.
	jobs, err := s.FinishedJobsSince(0, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "done", jobs[0].Status)

	job, err := s.GetJob(doneID)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "done", job.Status)
	assert.EqualValues(t, 11, job.MediaID)

	byID, err := s.JobByID(errID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	assert.Equal(t, "error", byID.Status)
	assert.Equal(t, "boom", byID.Error)

	if got := mediaIDFromPayload("not-json"); got != 0 {
		t.Fatalf("mediaIDFromPayload(invalid) = %d, want 0", got)
	}
}

// TestMediaOptimizationStatsEmptyFlags covers the counted-but-unrecommended
// branch of MediaOptimizationStats: a probed file with no recommendations is
// scanned but never suggested.
func TestMediaOptimizationStatsEmptyFlags(t *testing.T) {
	s := openTest(t)
	id, _, err := s.UpsertMedia("/media/ok.mkv", "mkv", 1, 1, "OK", 0, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, s.UpdateMediaOptimizationFlags(id, nil))

	scanned, recommended, counts, err := s.MediaOptimizationStats()
	require.NoError(t, err)
	assert.EqualValues(t, 1, scanned)
	assert.Zero(t, recommended)
	assert.Empty(t, counts)
}

// TestCuesAroundContextWindows covers the generalized context window: cues
// adjacent to the requested span are collected on both sides, and a zero
// context width yields no neighbors.
func TestCuesAroundContextWindows(t *testing.T) {
	s := openTest(t)
	mediaID, _, err := s.UpsertMedia("/media/ctx.mkv", "mkv", 1, 1, "Ctx", 0, 0, 0, "")
	require.NoError(t, err)
	trackID, err := s.ReplaceTrack(mediaID, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	require.NoError(t, err)

	cues := make([]Cue, 0, 5)
	for i := 0; i < 5; i++ {
		cues = append(cues, Cue{MediaFileID: mediaID, TrackID: trackID, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "context window line"})
	}
	require.NoError(t, s.ReplaceCues(mediaID, trackID, cues))

	span, before, after, err := s.CuesAround(mediaID, 2, 3, 1)
	require.NoError(t, err)
	require.Len(t, span, 2)
	assert.Equal(t, 2, span[0].Idx)
	assert.Equal(t, 3, span[1].Idx)
	require.Len(t, before, 1)
	assert.Equal(t, 1, before[0].Idx)
	require.Len(t, after, 1)
	assert.Equal(t, 4, after[0].Idx)

	// A wider window walks further in both directions.
	span, before, after, err = s.CuesAround(mediaID, 2, 2, 2)
	require.NoError(t, err)
	require.Len(t, span, 1)
	require.Len(t, before, 2)
	assert.Equal(t, 0, before[0].Idx)
	assert.Equal(t, 1, before[1].Idx)
	require.Len(t, after, 2)
	assert.Equal(t, 3, after[0].Idx)
	assert.Equal(t, 4, after[1].Idx)

	// Zero context width disables neighbor collection entirely.
	span, before, after, err = s.CuesAround(mediaID, 0, 0, 0)
	require.NoError(t, err)
	require.Len(t, span, 1)
	assert.Nil(t, before)
	assert.Nil(t, after)
}

// TestRandomQuotesAndChunkAtTime covers the landing-page quote query, the
// timestamp-to-chunk resolution fallbacks, and direct chunk lookups.
func TestRandomQuotesAndChunkAtTime(t *testing.T) {
	s := openTest(t)

	// A media file without cues yields no chunk at any timestamp.
	bareID, _, err := s.UpsertMedia("/media/bare.mkv", "mkv", 1, 1, "Bare", 0, 0, 0, "")
	require.NoError(t, err)
	chunk, err := s.ChunkAtTime(bareID, 5)
	require.NoError(t, err)
	assert.Nil(t, chunk)

	seed := func(path, title string) (int64, int64) {
		t.Helper()
		id, _, uerr := s.UpsertMedia(path, "mkv", 1, 1, title, 0, 0, 0, "")
		require.NoError(t, uerr)
		trackID, terr := s.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
		require.NoError(t, terr)
		return id, trackID
	}

	// Cues without chunks resolve to no chunk rather than a wrong one.
	cueOnlyID, cueOnlyTrack := seed("/media/cues.mkv", "CueOnly")
	cueOnly := make([]Cue, 0, 3)
	for i := 0; i < 3; i++ {
		cueOnly = append(cueOnly, Cue{MediaFileID: cueOnlyID, TrackID: cueOnlyTrack, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "plain cue line"})
	}
	require.NoError(t, s.ReplaceCues(cueOnlyID, cueOnlyTrack, cueOnly))
	chunk, err = s.ChunkAtTime(cueOnlyID, 1.5)
	require.NoError(t, err)
	assert.Nil(t, chunk)

	// An indexed chunk on an indexed media file surfaces as a random quote
	// and resolves through both ChunkAtTime and GetChunkByID.
	quotedID, quotedTrack := seed("/media/quoted.mkv", "Quoted")
	quoted := make([]Cue, 0, 3)
	for i := 0; i < 3; i++ {
		quoted = append(quoted, Cue{MediaFileID: quotedID, TrackID: quotedTrack, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "Well, that escalated quickly."})
	}
	require.NoError(t, s.ReplaceCues(quotedID, quotedTrack, quoted))
	require.NoError(t, s.ReplaceChunks(quotedID, []Chunk{{
		MediaFileID: quotedID, StartCueIdx: 0, EndCueIdx: 2, Text: "Well, that escalated quickly.",
	}}))
	chunks, err := s.ChunksForMedia(quotedID)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.NoError(t, s.MarkChunksIndexed([]int64{chunks[0].ID}, 100))
	require.NoError(t, s.MarkIndexed(quotedID))

	for _, limit := range []int{0, 99, 3} {
		quotes, qerr := s.RandomQuotes(limit)
		require.NoError(t, qerr)
		require.Len(t, quotes, 1, "limit %d", limit)
		assert.Equal(t, "Well, that escalated quickly.", quotes[0].Text)
		assert.Equal(t, "Quoted", quotes[0].MediaTitle)
	}

	chunk, err = s.ChunkAtTime(quotedID, 2)
	require.NoError(t, err)
	require.NotNil(t, chunk)
	assert.Equal(t, chunks[0].ID, chunk.ID)

	byID, err := s.GetChunkByID(chunk.ID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	assert.Equal(t, chunk.StartCueIdx, byID.StartCueIdx)

	missing, err := s.GetChunkByID(404)
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// TestClosedStoreTailOperations covers store operations the closed-store
// sweep does not exercise, asserting they report errors instead of panicking
// or silently succeeding after the database is gone.
func TestClosedStoreTailOperations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tail.db"))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	mustErr := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error after close", name)
		}
	}

	// Jobs.
	_, err = s.EnqueueUniqueJobs([]JobSpec{{Type: "probe", Payload: "{}"}})
	mustErr("EnqueueUniqueJobs", err)
	_, err = s.FinishedJobsSince(0, 1)
	mustErr("FinishedJobsSince", err)
	_, err = s.MediaSizes([]int64{1})
	mustErr("MediaSizes", err)
	_, err = s.GetJob(1)
	mustErr("GetJob", err)
	_, err = s.JobByID(1)
	mustErr("JobByID", err)

	// Nodes.
	_, err = s.UpsertNode("n", "fp", "v", "secret")
	mustErr("UpsertNode", err)
	_, err = s.NodeByID(1)
	mustErr("NodeByID", err)
	mustErr("ApproveNode", s.ApproveNode(1, "token"))
	_, err = s.ListNodes()
	mustErr("ListNodes", err)
	_, err = s.RequeueStaleNodeJobs(0, "note")
	mustErr("RequeueStaleNodeJobs", err)
	_, err = s.RequeueNodeJobs(1, "note")
	mustErr("RequeueNodeJobs", err)
	_, err = s.RunningNodeJobs()
	mustErr("RunningNodeJobs", err)

	// Media.
	_, _, _, err = s.MediaOptimizationStats()
	mustErr("MediaOptimizationStats", err)
	mustErr("DeleteMediaIDs", s.DeleteMediaIDs([]int64{1}))

	// Subtitles and chunks.
	_, _, _, err = s.CuesAround(1, 0, 1, 1)
	mustErr("CuesAround", err)
	_, err = s.ChunkAtTime(1, 1)
	mustErr("ChunkAtTime", err)
}
