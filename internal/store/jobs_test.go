package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobs(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	// EnqueueJob
	id1, err := db.EnqueueJob("extract", `{"mediaId":1}`, 10)
	require.NoError(t, err)
	assert.Greater(t, id1, int64(0))

	id2, err := db.EnqueueJob("extract", `{"mediaId":2}`, 5)
	require.NoError(t, err)
	assert.Greater(t, id2, int64(0))

	// EnqueueUniqueJob
	id, enqueued, err := db.EnqueueUniqueJob("extract", `{"mediaId":1}`, 20)
	require.NoError(t, err)
	assert.False(t, enqueued)
	assert.Equal(t, id1, id)

	id3, enqueued, err := db.EnqueueUniqueJob("transcode", `{"mediaId":3}`, 1)
	require.NoError(t, err)
	assert.True(t, enqueued)
	assert.Greater(t, id3, int64(0))

	// ListJobs
	jobs, err := db.ListJobs(10)
	require.NoError(t, err)
	require.Len(t, jobs, 3)

	// ClaimJob
	job, err := db.ClaimJob(ctx, "worker1", []string{"extract"})
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, id1, job.ID) // priority 10
	assert.Equal(t, 1, job.Attempts)

	// SetJobProgress
	err = db.SetJobProgress(job.ID, 0.5)
	require.NoError(t, err)

	// RunningJobs
	running, err := db.RunningJobs(10)
	require.NoError(t, err)
	require.Len(t, running, 1)
	assert.Equal(t, id1, running[0].ID)
	assert.Equal(t, 0.5, running[0].Progress)
	assert.Equal(t, "worker1", running[0].WorkerID)
	assert.Equal(t, "running", running[0].Status)

	// FinishJob (done)
	err = db.FinishJob(job.ID, "done", "")
	require.NoError(t, err)

	jobs, err = db.ListJobs(10)
	require.NoError(t, err)
	var finishedJob Job
	for _, j := range jobs {
		if j.ID == job.ID {
			finishedJob = j
		}
	}
	assert.Equal(t, "done", finishedJob.Status)
	assert.Equal(t, 1.0, finishedJob.Progress)
	assert.Greater(t, finishedJob.FinishedAt, int64(0))

	// Claim next
	job2, err := db.ClaimJob(ctx, "worker1", []string{"extract"})
	require.NoError(t, err)
	require.NotNil(t, job2)
	assert.Equal(t, id2, job2.ID)

	// RequeueJob
	err = db.RequeueJob(job2.ID, "error occurred")
	require.NoError(t, err)

	jobs, err = db.ListJobs(10)
	require.NoError(t, err)
	var requeuedJob Job
	for _, j := range jobs {
		if j.ID == job2.ID {
			requeuedJob = j
		}
	}
	assert.Equal(t, "queued", requeuedJob.Status)
	assert.Equal(t, "error occurred", requeuedJob.Error)
	assert.Empty(t, requeuedJob.WorkerID)

	// Claim and RequeueJobSoft
	job2b, err := db.ClaimJob(ctx, "worker1", []string{"extract"})
	require.NoError(t, err)
	assert.Equal(t, 2, job2b.Attempts)

	err = db.RequeueJobSoft(job2b.ID, "interrupted")
	require.NoError(t, err)

	jobs, err = db.ListJobs(10)
	require.NoError(t, err)
	for _, j := range jobs {
		if j.ID == job2b.ID {
			assert.Equal(t, "queued", j.Status)
			assert.Equal(t, "interrupted", j.Error)
			assert.Equal(t, 1, j.Attempts) // went from 2 to 1
		}
	}

	// JobStats
	stats, err := db.JobStats()
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats.Queued)
	assert.Equal(t, int64(1), stats.Done)
	assert.Equal(t, int64(0), stats.Running)
	assert.Equal(t, int64(0), stats.Error)

	// ClearQueuedJobs
	n, err := db.ClearQueuedJobs()
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	stats, err = db.JobStats()
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.Queued)

	// LineStats (just basic test to run it)
	lstats, err := db.LineStats()
	require.NoError(t, err)
	assert.Equal(t, int64(0), lstats.Cues)
	assert.Equal(t, int64(0), lstats.Chunks)
	assert.Equal(t, int64(0), lstats.ChunksIndexed)
}
