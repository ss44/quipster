package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetJobEnrichesMediaMetadata(t *testing.T) {
	db := openTest(t)
	id, _, err := db.UpsertMedia("/media/show-s01e01.mkv", "matroska", 1234, 1700000000, "Pilot", 2020, 1, 1, "Show")
	require.NoError(t, err)
	jobID, err := db.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 1)
	require.NoError(t, err)

	job, err := db.GetJob(jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, jobID, job.ID)
	assert.Equal(t, "probe", job.Type)
	// Enrichment resolves the display name, path and media status from the
	// payload's media id so the admin table can render one row per job.
	assert.Equal(t, "Show S01E01", job.MediaTitle)
	assert.Equal(t, "/media/show-s01e01.mkv", job.MediaPath)
	assert.Equal(t, "pending", job.MediaStatus)

	missing, err := db.GetJob(9999)
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestJobByIDReturnsRawRow(t *testing.T) {
	db := openTest(t)
	jobID, err := db.EnqueueJob("index", `{"mediaId":7}`, 2)
	require.NoError(t, err)

	job, err := db.JobByID(jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, jobID, job.ID)
	assert.Equal(t, "queued", job.Status)
	assert.Equal(t, `{"mediaId":7}`, job.Payload)
	assert.Equal(t, int64(7), mediaIDFromPayload(job.Payload))

	missing, err := db.JobByID(424242)
	require.NoError(t, err)
	assert.Nil(t, missing)
}
