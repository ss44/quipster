package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFinalizeOwnedJobRetrySemantics(t *testing.T) {
	for _, tc := range []struct {
		status   string
		soft     bool
		attempts int
	}{
		{status: "done", attempts: 1},
		{status: "error", attempts: 1},
		{status: "queued", attempts: 1},
		{status: "queued", soft: true, attempts: 0},
	} {
		t.Run(tc.status+map[bool]string{false: "-hard", true: "-soft"}[tc.soft], func(t *testing.T) {
			s := openTest(t)
			id, err := s.EnqueueJob("demo", `{}`, 0)
			require.NoError(t, err)
			j, err := s.ClaimJob(t.Context(), "owner", []string{"demo"})
			require.NoError(t, err)
			require.NotNil(t, j)
			matched, err := s.FinalizeOwnedJob(t.Context(), id, "owner", j.Attempts, tc.status, "note", tc.soft)
			require.NoError(t, err)
			require.True(t, matched)
			matched, err = s.FinalizeOwnedJob(t.Context(), id, "owner", j.Attempts, tc.status, "changed", tc.soft)
			require.NoError(t, err)
			require.False(t, matched)
			got, err := s.GetJob(id)
			require.NoError(t, err)
			require.Equal(t, tc.status, got.Status)
			require.Equal(t, tc.attempts, got.Attempts)
			require.Equal(t, "note", got.Error)
			if tc.status == "queued" {
				require.Empty(t, got.WorkerID)
				require.Zero(t, got.FinishedAt)
			} else {
				require.Equal(t, "owner", got.WorkerID)
				require.Positive(t, got.FinishedAt)
			}
			if tc.status == "done" {
				require.Equal(t, float64(1), got.Progress)
			}
		})
	}
}

func TestOwnedJobUpdatesRejectStaleClaims(t *testing.T) {
	s := openTest(t)
	id, err := s.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	j, err := s.ClaimJob(t.Context(), "owner", []string{"demo"})
	require.NoError(t, err)
	require.NotNil(t, j)
	for _, tc := range []struct {
		owner   string
		attempt int
	}{
		{owner: "other", attempt: j.Attempts},
		{owner: "owner", attempt: j.Attempts + 1},
	} {
		matched, ferr := s.FinalizeOwnedJob(t.Context(), id, tc.owner, tc.attempt, "done", "", false)
		require.NoError(t, ferr)
		require.False(t, matched)
		matched, perr := s.SetOwnedJobProgress(t.Context(), id, tc.owner, tc.attempt, 0.99)
		require.NoError(t, perr)
		require.False(t, matched)
	}
	matched, err := s.SetOwnedJobProgress(t.Context(), id, "owner", j.Attempts, 0.4)
	require.NoError(t, err)
	require.True(t, matched)
	got, err := s.RunningJobByWorker(t.Context(), "owner")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 0.4, got.Progress)
	missing, err := s.RunningJobByWorker(t.Context(), "missing")
	require.NoError(t, err)
	require.Nil(t, missing)
	matched, err = s.FinalizeOwnedJob(t.Context(), id, "owner", j.Attempts, "invalid", "", false)
	require.Error(t, err)
	require.False(t, matched)
}

func TestFinalizeOwnedJobBoundsSQLiteBusyWait(t *testing.T) {
	s := openTest(t)
	id, err := s.EnqueueJob("demo", `{}`, 0)
	require.NoError(t, err)
	j, err := s.ClaimJob(t.Context(), "owner", []string{"demo"})
	require.NoError(t, err)
	require.NotNil(t, j)
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, rbErr := conn.ExecContext(context.Background(), "ROLLBACK")
		require.NoError(t, rbErr)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	matched, err := s.FinalizeOwnedJob(ctx, id, "owner", j.Attempts, "done", "", false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, matched)
	require.Less(t, time.Since(started), time.Second)
	got, err := s.RunningJobByWorker(t.Context(), "owner")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, id, got.ID)
}

func TestOwnedJobWritesBoundPoolWait(t *testing.T) {
	s := openTest(t)
	conn, err := s.writeDB.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	matched, err := s.FinalizeOwnedJob(ctx, 1, "owner", 1, "done", "", false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, matched)
	matched, err = s.SetOwnedJobProgress(ctx, 1, "owner", 1, 0.5)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, matched)
}

func TestOwnedJobAPIsClosedStore(t *testing.T) {
	s := openTest(t)
	require.NoError(t, s.Close())
	matched, err := s.FinalizeOwnedJob(t.Context(), 1, "owner", 1, "done", "", false)
	require.Error(t, err)
	require.False(t, matched)
	matched, err = s.SetOwnedJobProgress(t.Context(), 1, "owner", 1, 0.5)
	require.Error(t, err)
	require.False(t, matched)
	j, err := s.RunningJobByWorker(t.Context(), "owner")
	require.Error(t, err)
	require.Nil(t, j)
}
