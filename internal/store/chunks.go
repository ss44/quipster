package store

import (
	"database/sql"
	"strings"
)

// Chunk is one indexed dialog chunk spanning a range of subtitle cues.
type Chunk struct {
	Text        string `json:"text"`
	ID          int64  `json:"id"`
	MediaFileID int64  `json:"mediaFileId"`
	StartCueIdx int    `json:"startCueIdx"`
	EndCueIdx   int    `json:"endCueIdx"`
	IndexedAt   int64  `json:"indexedAt"`
}

// ReplaceChunks swaps chunks for a media file and relinks cues by index range.
func (s *Store) ReplaceChunks(mediaFileID int64, chunks []Chunk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTx(tx)
	if _, err := tx.Exec(`DELETE FROM chunks WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	for _, ch := range chunks {
		res, err := tx.Exec(`INSERT INTO chunks (media_file_id, start_cue_idx, end_cue_idx, text) VALUES (?,?,?,?)`,
			mediaFileID, ch.StartCueIdx, ch.EndCueIdx, ch.Text)
		if err != nil {
			return err
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE cues SET chunk_id=? WHERE media_file_id=? AND idx BETWEEN ? AND ?`,
			chunkID, mediaFileID, ch.StartCueIdx, ch.EndCueIdx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ChunksForMedia lists chunks for a media file ordered by cue index.
func (s *Store) ChunksForMedia(mediaFileID int64) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, start_cue_idx, end_cue_idx, text, indexed_at FROM chunks WHERE media_file_id=? ORDER BY start_cue_idx`, mediaFileID)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.MediaFileID, &c.StartCueIdx, &c.EndCueIdx, &c.Text, &c.IndexedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChunkWithMedia is a chunk joined with its media file metadata.
type ChunkWithMedia struct {
	MediaTitle string `json:"mediaTitle"`
	ShowTitle  string `json:"showTitle"`
	FilePath   string `json:"filePath"`
	Chunk
	Year        int     `json:"year"`
	Season      int     `json:"season"`
	Episode     int     `json:"episode"`
	DurationSec float64 `json:"durationSec"`
	StartSec    float64 `json:"startSec"`
	EndSec      float64 `json:"endSec"`
}

const chunkJoinCols = `c.id, c.media_file_id, c.start_cue_idx, c.end_cue_idx, c.text, c.indexed_at,
	COALESCE(m.title,''), COALESCE(m.show_title,''), m.year, m.season, m.episode, m.duration_sec, m.path,
	COALESCE((SELECT MIN(start_sec) FROM cues q WHERE q.media_file_id=c.media_file_id AND q.idx BETWEEN c.start_cue_idx AND c.end_cue_idx),0),
	COALESCE((SELECT MAX(end_sec) FROM cues q WHERE q.media_file_id=c.media_file_id AND q.idx BETWEEN c.start_cue_idx AND c.end_cue_idx),0)`

// PendingIndexChunks returns chunks not yet indexed, optionally scoped to one media file.
func (s *Store) PendingIndexChunks(mediaFileID int64, limit int) ([]ChunkWithMedia, error) {
	const filter = ` AND c.media_file_id = ?`
	const orderLimit = ` ORDER BY c.media_file_id, c.start_cue_idx LIMIT ?`
	args := []any{}
	q := `SELECT ` + chunkJoinCols + `
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.indexed_at = 0` + orderLimit
	if mediaFileID > 0 {
		q = `SELECT ` + chunkJoinCols + `
			FROM chunks c JOIN media_files m ON m.id = c.media_file_id
			WHERE c.indexed_at = 0` + filter + orderLimit
		args = append(args, mediaFileID)
	}
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []ChunkWithMedia
	for rows.Next() {
		var c ChunkWithMedia
		if err := rows.Scan(&c.ID, &c.MediaFileID, &c.StartCueIdx, &c.EndCueIdx, &c.Text, &c.IndexedAt,
			&c.MediaTitle, &c.ShowTitle, &c.Year, &c.Season, &c.Episode, &c.DurationSec, &c.FilePath,
			&c.StartSec, &c.EndSec); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkChunksIndexed stamps the given chunk ids as indexed at the given time.
func (s *Store) MarkChunksIndexed(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, at)
	for _, id := range ids {
		args = append(args, id)
	}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length ids slice; values are parameterized.
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=? WHERE id IN (`+ph+`)`, args...)
	return err
}

// ClearChunkIndexFlags resets indexed_at for all chunks of a media file.
func (s *Store) ClearChunkIndexFlags(mediaFileID int64) error {
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=0 WHERE media_file_id=?`, mediaFileID)
	return err
}

// EnsureAllChunksStale clears indexed_at on every chunk (used by full reindex).
func (s *Store) EnsureAllChunksStale() error {
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=0`)
	return err
}

// MediaIDsNeedingIndex lists media files with unindexed chunks, excluding error rows.
func (s *Store) MediaIDsNeedingIndex(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT c.media_file_id FROM chunks c
		JOIN media_files m ON m.id = c.media_file_id
		WHERE c.indexed_at = 0 AND m.status != 'error' LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RandomQuotes returns random indexed chunks for the landing page so the home
// screen always shows dialog that actually exists in the library.
func (s *Store) RandomQuotes(limit int) ([]ChunkWithMedia, error) {
	if limit <= 0 || limit > 12 {
		limit = 6
	}
	rows, err := s.db.Query(`SELECT `+chunkJoinCols+`
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.indexed_at > 0 AND m.status = 'indexed'
		  AND length(c.text) BETWEEN 12 AND 220 AND TRIM(c.text) != ''
		  AND substr(TRIM(c.text), 1, 1) != '['
		ORDER BY RANDOM() LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []ChunkWithMedia
	for rows.Next() {
		var c ChunkWithMedia
		if err := rows.Scan(&c.ID, &c.MediaFileID, &c.StartCueIdx, &c.EndCueIdx, &c.Text, &c.IndexedAt,
			&c.MediaTitle, &c.ShowTitle, &c.Year, &c.Season, &c.Episode, &c.DurationSec, &c.FilePath,
			&c.StartSec, &c.EndSec); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChunkByKey resolves a chunk identity "mediaId:startIdx-endIdx".
func (s *Store) GetChunkByKey(mediaID int64, startIdx, endIdx int) (*ChunkWithMedia, error) {
	row := s.db.QueryRow(`SELECT `+chunkJoinCols+`
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.media_file_id=? AND c.start_cue_idx=? AND c.end_cue_idx=?`, mediaID, startIdx, endIdx)
	return scanChunkJoin(row)
}

// GetChunkByID resolves a chunk by its primary key.
func (s *Store) GetChunkByID(chunkID int64) (*ChunkWithMedia, error) {
	row := s.db.QueryRow(`SELECT `+chunkJoinCols+`
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.id=?`, chunkID)
	return scanChunkJoin(row)
}

// ChunkAtTime resolves the chunk whose cue span covers the given second.
// Lookup goes through cues.chunk_id so a cue that is not (yet) part of any
// chunk yields no match rather than the wrong one. When no cue is active at
// that instant the chunk owning the last cue that starts at or before it is
// returned, so callers asking for a mid-silence timestamp still get the
// surrounding scene. Returns nil when the media has no preceding cue at all.
func (s *Store) ChunkAtTime(mediaID int64, sec float64) (*ChunkWithMedia, error) {
	var chunkID int64
	err := s.db.QueryRow(`SELECT COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx=(SELECT MAX(idx) FROM cues WHERE media_file_id=? AND start_sec<=?)`,
		mediaID, mediaID, sec).Scan(&chunkID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if chunkID == 0 {
		return nil, nil
	}
	return s.GetChunkByID(chunkID)
}

// scanChunkJoin scans a single row produced by chunkJoinCols.
func scanChunkJoin(row *sql.Row) (*ChunkWithMedia, error) {
	var c ChunkWithMedia
	err := row.Scan(&c.ID, &c.MediaFileID, &c.StartCueIdx, &c.EndCueIdx, &c.Text, &c.IndexedAt,
		&c.MediaTitle, &c.ShowTitle, &c.Year, &c.Season, &c.Episode, &c.DurationSec, &c.FilePath,
		&c.StartSec, &c.EndSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
