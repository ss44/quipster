package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertMediaBatch(t *testing.T) {
	s := openTest(t)
	entries := []MediaUpsert{
		{Path: "/media/a.mkv", Container: "mkv", Size: 10, Mtime: 100, Title: "A", Year: 2001},
		{Path: "/media/b.mkv", Container: "mkv", Size: 20, Mtime: 200, Title: "B", Year: 2002},
		{Path: "/media/c.mkv", Container: "mkv", Size: 30, Mtime: 300, Title: "C", Year: 2003},
	}

	ids, changed, err := s.UpsertMediaBatch(entries)
	require.NoError(t, err)
	require.Len(t, ids, len(entries))
	require.Len(t, changed, len(entries))
	for i := range ids {
		assert.NotZero(t, ids[i])
		assert.True(t, changed[i], "new row %d must be reported changed", i)
	}

	// An unchanged batch resolves the existing ids and reports no work.
	ids2, changed2, err := s.UpsertMediaBatch(entries)
	require.NoError(t, err)
	assert.Equal(t, ids, ids2)
	assert.Equal(t, []bool{false, false, false}, changed2)

	// One content change must not mark its siblings changed.
	entries[1].Size++
	ids3, changed3, err := s.UpsertMediaBatch(entries)
	require.NoError(t, err)
	assert.Equal(t, ids, ids3)
	assert.Equal(t, []bool{false, true, false}, changed3)

	page, err := s.ListMedia(10, 0, "")
	require.NoError(t, err)
	assert.EqualValues(t, 3, page.Total)
}

func TestUpsertMediaDoesNotRequeueUnchangedErrorRows(t *testing.T) {
	s := openTest(t)
	id, _, err := s.UpsertMedia("/media/broken.mkv", "mkv", 10, 100, "Broken", 2020, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, s.SetMediaStatus(id, "error", "permanent ffmpeg failure"))

	_, changed, err := s.UpsertMedia("/media/broken.mkv", "mkv", 10, 100, "Broken", 2020, 0, 0, "")
	require.NoError(t, err)
	assert.False(t, changed, "an unchanged error row must not re-enter the probe queue on every scan")

	// A real content change is still detected and resets the row to pending.
	_, changed, err = s.UpsertMedia("/media/broken.mkv", "mkv", 11, 101, "Broken", 2020, 0, 0, "")
	require.NoError(t, err)
	assert.True(t, changed)
	m, err := s.GetMedia(id)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "pending", m.Status)
	assert.Empty(t, m.Error)
}

func TestDeleteMediaIDs(t *testing.T) {
	s := openTest(t)
	ids := make([]int64, 3)
	for i := range ids {
		id, _, err := s.UpsertMedia(
			[]string{"/media/a.mkv", "/media/b.mkv", "/media/c.mkv"}[i],
			"mkv", int64(i+1), int64(i+1), "T", 0, 0, 0, "")
		require.NoError(t, err)
		ids[i] = id
	}
	require.NoError(t, s.DeleteMediaIDs([]int64{ids[0], ids[2]}))
	for _, id := range []int64{ids[0], ids[2]} {
		m, err := s.GetMedia(id)
		require.NoError(t, err)
		assert.Nil(t, m)
	}
	m, err := s.GetMedia(ids[1])
	require.NoError(t, err)
	assert.NotNil(t, m)
}

func TestEnqueueJobsBatch(t *testing.T) {
	s := openTest(t)
	ids, err := s.EnqueueJobs([]JobSpec{
		{Type: "probe", Payload: `{"mediaId":1}`, Priority: 0},
		{Type: "probe", Payload: `{"mediaId":2}`, Priority: 0},
		{Type: "index", Payload: `{"mediaId":1}`, Priority: 1},
	})
	require.NoError(t, err)
	require.Len(t, ids, 3)
	stats, err := s.JobStats()
	require.NoError(t, err)
	assert.EqualValues(t, 3, stats.Queued)
}

func TestRecoverClosesStaleScans(t *testing.T) {
	s := openTest(t)
	scanID, err := s.StartScan("/media")
	require.NoError(t, err)
	require.NoError(t, s.Recover())

	sc, err := s.RunningScan()
	require.NoError(t, err)
	assert.Nil(t, sc, "a stale running scan must not be reported after recovery")

	var status string
	var finished int64
	require.NoError(t, s.DB().QueryRow(`SELECT status, finished_at FROM scans WHERE id=?`, scanID).Scan(&status, &finished))
	assert.Equal(t, "error", status)
	assert.NotZero(t, finished)
}
