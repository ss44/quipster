package store

import (
	"context"
	"database/sql"
	"strings"
)

// IndexSnapshot describes one coherent SQLite view used to replace a media's
// remote documents. A complete nonempty snapshot has no Chunks: it is a no-op,
// unlike a genuinely empty replacement, which must remove old documents.
type IndexSnapshot struct {
	Chunks      []ChunkWithMedia
	Total       int
	Pending     int
	Active      bool
	EverIndexed bool
}

// LoadIndexSnapshot reads media state and, when work is pending, ALL chunks in
// one read transaction. ReadOnly selects a deferred transaction in the SQLite
// driver despite the deliberate immediate-writer DSN. Replacement must not
// omit already-latched chunks after a partial bounded latch write. No writer
// is held while remote tasks run.
func (s *Store) LoadIndexSnapshot(ctx context.Context, mediaID int64) (IndexSnapshot, error) {
	var snap IndexSnapshot
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snap, err
	}
	defer rollbackTx(tx)
	var archived bool
	err = tx.QueryRowContext(ctx, `SELECT archived, ever_indexed FROM media_files WHERE id=?`, mediaID).Scan(&archived, &snap.EverIndexed)
	if err == sql.ErrNoRows {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	if archived {
		return snap, nil
	}
	snap.Active = true
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(indexed_at=0),0) FROM chunks WHERE media_file_id=?`, mediaID).Scan(&snap.Total, &snap.Pending)
	if err != nil {
		return snap, err
	}
	if snap.Pending == 0 {
		return snap, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+chunkJoinCols+` FROM chunks c JOIN media_files m ON m.id=c.media_file_id WHERE c.media_file_id=? ORDER BY c.start_cue_idx, c.id`, mediaID)
	if err != nil {
		return snap, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var c ChunkWithMedia
		if err := rows.Scan(&c.ID, &c.MediaFileID, &c.StartCueIdx, &c.EndCueIdx, &c.Text, &c.IndexedAt,
			&c.MediaTitle, &c.ShowTitle, &c.Year, &c.Season, &c.Episode, &c.DurationSec, &c.FilePath, &c.StartSec, &c.EndSec); err != nil {
			return snap, err
		}
		snap.Chunks = append(snap.Chunks, c)
	}
	return snap, rows.Err()
}

// MarkIndexSnapshotChunks latches uploaded IDs in bounded independent writes,
// honoring cancellation while waiting for the single writer or between batches.
// Archive state is checked inside each write so frozen flags remain unchanged.
func (s *Store) MarkIndexSnapshotChunks(ctx context.Context, mediaID int64, ids []int64, at int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for start := 0; start < len(ids); start += markIndexedBatchSize {
		batch := ids[start:min(start+markIndexedBatchSize, len(ids))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := []any{at, mediaID, mediaID}
		for _, id := range batch {
			args = append(args, id)
		}
		//nolint:gosec // Only a bounded placeholder list is interpolated; values are bound.
		_, err := s.writeDB.ExecContext(ctx, `UPDATE chunks SET indexed_at=? WHERE media_file_id=? AND EXISTS (SELECT 1 FROM media_files WHERE id=? AND archived=0) AND id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
	}
	return nil
}

// MarkIndexSnapshotComplete latches media completion only while active. This
// must follow all remote tasks and chunk writes, and shares the job context.
func (s *Store) MarkIndexSnapshotComplete(ctx context.Context, mediaID int64) error {
	_, err := s.writeDB.ExecContext(ctx, `UPDATE media_files SET status='indexed', error='', ever_indexed=1 WHERE id=? AND archived=0`, mediaID)
	return err
}
