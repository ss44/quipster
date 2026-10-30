package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaOptimizationStats(t *testing.T) {
	db := openTest(t)
	first, _, err := db.UpsertMedia("/foo/a.mkv", "matroska", 1, 1, "A", 0, 0, 0, "")
	require.NoError(t, err)
	second, _, err := db.UpsertMedia("/foo/b.mkv", "matroska", 1, 1, "B", 0, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, db.UpdateMediaOptimizationFlags(first, []string{"high_video_bitrate", "audio_codec_compatibility"}))
	require.NoError(t, db.UpdateMediaOptimizationFlags(second, []string{"high_video_bitrate"}))

	scanned, recommended, codes, err := db.MediaOptimizationStats()
	require.NoError(t, err)
	assert.Equal(t, int64(2), scanned)
	assert.Equal(t, int64(2), recommended)
	assert.Equal(t, map[string]int64{"high_video_bitrate": 2, "audio_codec_compatibility": 1}, codes)

	_, changed, err := db.UpsertMedia("/foo/a.mkv", "matroska", 2, 2, "A", 0, 0, 0, "")
	require.NoError(t, err)
	assert.True(t, changed)
	scanned, recommended, codes, err = db.MediaOptimizationStats()
	require.NoError(t, err)
	assert.Equal(t, int64(1), scanned)
	assert.Equal(t, int64(1), recommended)
	assert.Equal(t, map[string]int64{"high_video_bitrate": 1}, codes)
}

func TestMedia(t *testing.T) {
	db := openTest(t)

	// UpsertMedia
	id1, changed, err := db.UpsertMedia("/foo/bar.mp4", "mp4", 1024, 1000, "Title", 2020, 1, 2, "Show")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Greater(t, id1, int64(0))

	id1b, changedb, err := db.UpsertMedia("/foo/bar.mp4", "mp4", 1024, 1000, "Title", 2020, 1, 2, "Show")
	require.NoError(t, err)
	assert.False(t, changedb)
	assert.Equal(t, id1, id1b)

	// GetMedia
	m, err := db.GetMedia(id1)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "/foo/bar.mp4", m.Path)

	// GetMediaByPath
	m2, err := db.GetMediaByPath("/foo/bar.mp4")
	require.NoError(t, err)
	require.NotNil(t, m2)
	assert.Equal(t, id1, m2.ID)

	mNil, err := db.GetMediaByPath("/nonexistent")
	require.NoError(t, err)
	assert.Nil(t, mNil)

	// UpdateMediaProbe(id int64, duration float64, width, height int, vcodec, acodec, container string, startTime float64, audioStream int, audioLang string)
	err = db.UpdateMediaProbe(id1, 3600.0, 1920, 1080, "h264", "aac", "mp4", 0.0, 1, "en")
	require.NoError(t, err)

	m, err = db.GetMedia(id1)
	require.NoError(t, err)
	assert.Equal(t, 3600.0, m.DurationSec)
	assert.Equal(t, "Title", m.Title)
	assert.Equal(t, "Show", m.ShowTitle)
	assert.Equal(t, 2020, m.Year)
	assert.Equal(t, 1, m.Season)
	assert.Equal(t, 2, m.Episode)

	// SetMediaStatus(id int64, status, errMsg string)
	err = db.SetMediaStatus(id1, "no_subs", "")
	require.NoError(t, err)
	m, err = db.GetMedia(id1)
	require.NoError(t, err)
	assert.Equal(t, "no_subs", m.Status)

	// MarkIndexed & EverIndexed
	err = db.MarkIndexed(id1)
	require.NoError(t, err)

	ever, err := db.EverIndexed(id1)
	require.NoError(t, err)
	assert.True(t, ever)

	// ListMedia(limit, offset int, q, archived string) (*MediaListPage, error)
	page, err := db.ListMedia(10, 0, "", "")
	require.NoError(t, err)
	require.NotNil(t, page)
	require.Len(t, page.Items, 1)
	assert.Equal(t, int64(1), page.Total)

	page2, err := db.ListMedia(10, 0, "Show", "")
	require.NoError(t, err)
	require.NotNil(t, page2)
	require.Len(t, page2.Items, 1)

	// MediaStatusCounts
	counts, err := db.MediaStatusCounts()
	require.NoError(t, err)
	assert.Equal(t, int64(1), counts["indexed"])

	// MediaIDsByStatus
	ids, err := db.MediaIDsByStatus("indexed")
	require.NoError(t, err)
	assert.Equal(t, []int64{id1}, ids)

	// ArchiveMissingUnder(base string, keep map[string]bool, grace time.Duration, now time.Time)
	id3, _, err := db.UpsertMedia("/foo/baz.mp4", "mp4", 2048, 2000, "Title2", 2021, 0, 0, "")
	require.NoError(t, err)

	// keep bar.mp4, archive baz.mp4: first pass latches, second pass archives.
	keep := map[string]bool{"/foo/bar.mp4": true}
	now := time.UnixMilli(1_000_000)
	archived, latched, err := db.ArchiveMissingUnder("/foo", keep, 0, now)
	require.NoError(t, err)
	require.Empty(t, archived)
	assert.Equal(t, 1, latched)

	archived, latched, err = db.ArchiveMissingUnder("/foo", keep, 0, now)
	require.NoError(t, err)
	assert.Equal(t, []int64{id3}, archived)
	assert.Equal(t, 0, latched)

	m, err = db.GetMedia(id3)
	require.NoError(t, err)
	require.NotNil(t, m) // baz.mp4 is archived, not deleted
	assert.True(t, m.Archived)

	m, err = db.GetMedia(id1)
	require.NoError(t, err)
	require.NotNil(t, m) // bar.mp4 should still exist
	assert.False(t, m.Archived)

	// TitleMatches(q string, limit int)
	matches, err := db.TitleMatches("Show", 10)
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, id1, matches[0].ID)
}
