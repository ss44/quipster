package store

import (
	"database/sql"
	"strings"
)

type Chunk struct {
	ID          int64  `json:"id"`
	MediaFileID int64  `json:"mediaFileId"`
	StartCueIdx int    `json:"startCueIdx"`
	EndCueIdx   int    `json:"endCueIdx"`
	Text        string `json:"text"`
	IndexedAt   int64  `json:"indexedAt"`
}

// ReplaceChunks swaps chunks for a media file and relinks cues by index range.
func (s *Store) ReplaceChunks(mediaFileID int64, chunks []Chunk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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

func (s *Store) ChunksForMedia(mediaFileID int64) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, start_cue_idx, end_cue_idx, text, indexed_at FROM chunks WHERE media_file_id=? ORDER BY start_cue_idx`, mediaFileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

type ChunkWithMedia struct {
	Chunk
	MediaTitle  string  `json:"mediaTitle"`
	ShowTitle   string  `json:"showTitle"`
	Year        int     `json:"year"`
	Season      int     `json:"season"`
	Episode     int     `json:"episode"`
	DurationSec float64 `json:"durationSec"`
	FilePath    string  `json:"filePath"`
	StartSec    float64 `json:"startSec"`
	EndSec      float64 `json:"endSec"`
}

const chunkJoinCols = `c.id, c.media_file_id, c.start_cue_idx, c.end_cue_idx, c.text, c.indexed_at,
	COALESCE(m.title,''), COALESCE(m.show_title,''), m.year, m.season, m.episode, m.duration_sec, m.path,
	COALESCE((SELECT MIN(start_sec) FROM cues q WHERE q.media_file_id=c.media_file_id AND q.idx BETWEEN c.start_cue_idx AND c.end_cue_idx),0),
	COALESCE((SELECT MAX(end_sec) FROM cues q WHERE q.media_file_id=c.media_file_id AND q.idx BETWEEN c.start_cue_idx AND c.end_cue_idx),0)`

// PendingIndexChunks returns chunks not yet indexed, optionally scoped to one media file.
func (s *Store) PendingIndexChunks(mediaFileID int64, limit int) ([]ChunkWithMedia, error) {
	q := `SELECT ` + chunkJoinCols + `
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.indexed_at = 0`
	args := []any{}
	if mediaFileID > 0 {
		q += ` AND c.media_file_id = ?`
		args = append(args, mediaFileID)
	}
	q += ` ORDER BY c.media_file_id, c.start_cue_idx LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *Store) MarkChunksIndexed(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, at)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=? WHERE id IN (`+ph+`)`, args...)
	return err
}

func (s *Store) ClearChunkIndexFlags(mediaFileID int64) error {
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=0 WHERE media_file_id=?`, mediaFileID)
	return err
}

// EnsureAllChunksStale clears indexed_at on every chunk (used by full reindex).
func (s *Store) EnsureAllChunksStale() error {
	_, err := s.db.Exec(`UPDATE chunks SET indexed_at=0`)
	return err
}

func (s *Store) MediaIDsNeedingIndex(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT c.media_file_id FROM chunks c
		JOIN media_files m ON m.id = c.media_file_id
		WHERE c.indexed_at = 0 AND m.status != 'error' LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *Store) GetChunkByKey(mediaID int64, startIdx, endIdx int) (*ChunkWithMedia, error) {
	row := s.db.QueryRow(`SELECT `+chunkJoinCols+`
		FROM chunks c JOIN media_files m ON m.id = c.media_file_id
		WHERE c.media_file_id=? AND c.start_cue_idx=? AND c.end_cue_idx=?`, mediaID, startIdx, endIdx)
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

// TitleMatches provides the LIKE-based fallback used for files without subtitles.
