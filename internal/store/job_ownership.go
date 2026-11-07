package store

import (
	"context"
	"fmt"
)

// FinalizeOwnedJob persists a verdict only while the claimed owner and attempt still match.
func (s *Store) FinalizeOwnedJob(ctx context.Context, id int64, workerID string, attempts int, status, note string, soft bool) (bool, error) {
	if status != "done" && status != "error" && status != "queued" {
		return false, fmt.Errorf("store: invalid final job status %q", status)
	}
	res, err := s.writeDB.ExecContext(ctx, `UPDATE jobs SET status=?, error=?,
		worker_id=CASE WHEN ?='queued' THEN '' ELSE worker_id END,
		progress=CASE WHEN ?='done' THEN 1 ELSE progress END,
		finished_at=CASE WHEN ?='queued' THEN 0 ELSE ? END,
		attempts=CASE WHEN ?='queued' AND ? AND attempts>0 THEN attempts-1 ELSE attempts END
		WHERE id=? AND status='running' AND worker_id=? AND attempts=?`,
		status, note, status, status, status, nowMillis(), status, soft, id, workerID, attempts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RunningJobByWorker finds an unfinished claim owned by the supplied worker.
func (s *Store) RunningJobByWorker(ctx context.Context, workerID string) (*Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+`
		FROM jobs WHERE status='running' AND worker_id=? ORDER BY id LIMIT 1`, workerID))
}

// SetOwnedJobProgress updates progress only for the matching live claim.
func (s *Store) SetOwnedJobProgress(ctx context.Context, id int64, workerID string, attempts int, progress float64) (bool, error) {
	res, err := s.writeDB.ExecContext(ctx, `UPDATE jobs SET progress=?
		WHERE id=? AND status='running' AND worker_id=? AND attempts=?`, progress, id, workerID, attempts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
