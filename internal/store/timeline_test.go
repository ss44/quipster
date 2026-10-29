package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTimelineCues inserts four cues (idx 0..3) for a media and links the
// middle two (idx 1..2) through a chunk, mirroring the probe/apply flow.
func seedTimelineCues(t *testing.T) (*Store, int64) {
	t.Helper()
	db := openTest(t)
	id, _, err := db.UpsertMedia("/media/show-s01e01.mkv", "matroska", 1234, 1700000000, "Pilot", 2020, 1, 1, "Show")
	require.NoError(t, err)
	trackID, err := db.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	require.NoError(t, err)
	cues := make([]Cue, 0, 4)
	for i := 0; i < 4; i++ {
		cues = append(cues, Cue{
			MediaFileID: id,
			TrackID:     trackID,
			Idx:         i,
			StartSec:    float64(i * 10),
			EndSec:      float64(i*10 + 5),
			Text:        "cue text",
		})
	}
	require.NoError(t, db.ReplaceCues(id, trackID, cues))
	require.NoError(t, db.ReplaceChunks(id, []Chunk{
		{MediaFileID: id, StartCueIdx: 1, EndCueIdx: 2, Text: "middle chunk"},
	}))
	return db, id
}

func TestChunkAtTime(t *testing.T) {
	db, id := seedTimelineCues(t)

	// A timestamp inside the chunked span resolves through cues.chunk_id.
	ch, err := db.ChunkAtTime(id, 12)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, 1, ch.StartCueIdx)
	assert.Equal(t, 2, ch.EndCueIdx)

	// Mid-silence timestamps still get the last cue that started before them.
	ch, err = db.ChunkAtTime(id, 15)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// An unchunked span yields no chunk instead of the wrong scene.
	ch, err = db.ChunkAtTime(id, 0)
	require.NoError(t, err)
	assert.Nil(t, ch)

	// Before the first cue there is nothing to return at all.
	ch, err = db.ChunkAtTime(id, -1)
	require.NoError(t, err)
	assert.Nil(t, ch)

	// Unknown media behaves like an empty timeline.
	ch, err = db.ChunkAtTime(424242, 10)
	require.NoError(t, err)
	assert.Nil(t, ch)
}

func TestGetChunkByIDAndCuesAround(t *testing.T) {
	db, id := seedTimelineCues(t)

	chunks, err := db.ChunksForMedia(id)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	ch, err := db.GetChunkByID(chunks[0].ID)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, "middle chunk", ch.Text)
	assert.Equal(t, id, ch.MediaFileID)

	span, before, after, err := db.CuesAround(id, 1, 2, 1)
	require.NoError(t, err)
	require.Len(t, span, 2)
	// Context cues stay adjacent to the span boundaries, ordered outward.
	require.Len(t, before, 1)
	require.Len(t, after, 1)
	assert.Equal(t, 0, before[0].Idx)
	assert.Equal(t, 3, after[0].Idx)
	assert.Equal(t, chunks[0].ID, span[0].ChunkID)
}

func TestCuesAroundWithoutContext(t *testing.T) {
	db, id := seedTimelineCues(t)

	span, before, after, err := db.CuesAround(id, 1, 2, 0)
	require.NoError(t, err)
	require.Len(t, span, 2)
	assert.Nil(t, before)
	assert.Nil(t, after)
}
