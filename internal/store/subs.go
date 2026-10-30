package store

import (
	"database/sql"
)

// SubtitleTrack describes one subtitle track (sidecar or embedded) attached
// to a media file.
type SubtitleTrack struct {
	Source        string `json:"source"`
	Lang          string `json:"lang"`
	ExtractedPath string `json:"extractedPath"`
	ID            int64  `json:"id"`
	MediaFileID   int64  `json:"mediaFileId"`
	StreamIndex   int    `json:"streamIndex"`
}

// ReplaceTrack swaps the subtitle track row for a media file and returns the
// new track id.
func (s *Store) ReplaceTrack(mediaFileID int64, t *SubtitleTrack) (int64, error) {
	if _, err := s.db.Exec(`DELETE FROM subtitle_tracks WHERE media_file_id=?`, mediaFileID); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO subtitle_tracks (media_file_id, source, lang, stream_index, extracted_path) VALUES (?,?,?,?,?)`,
		mediaFileID, t.Source, t.Lang, t.StreamIndex, t.ExtractedPath)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TracksForMedia lists subtitle tracks attached to a media file.
func (s *Store) TracksForMedia(mediaFileID int64) ([]SubtitleTrack, error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, source, lang, stream_index, extracted_path FROM subtitle_tracks WHERE media_file_id=?`, mediaFileID)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []SubtitleTrack
	for rows.Next() {
		var t SubtitleTrack
		if err := rows.Scan(&t.ID, &t.MediaFileID, &t.Source, &t.Lang, &t.StreamIndex, &t.ExtractedPath); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Cue is a single timed subtitle line for a media file.
type Cue struct {
	Text        string  `json:"text"`
	ID          int64   `json:"id"`
	MediaFileID int64   `json:"mediaFileId"`
	TrackID     int64   `json:"trackId"`
	Idx         int     `json:"idx"`
	StartSec    float64 `json:"startSec"`
	EndSec      float64 `json:"endSec"`
	ChunkID     int64   `json:"chunkId"`
}

// ReplaceCues swaps the cue set for a media file within one transaction.
func (s *Store) ReplaceCues(mediaFileID, trackID int64, cues []Cue) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTx(tx)
	if _, derr := tx.Exec(`DELETE FROM cues WHERE media_file_id=?`, mediaFileID); derr != nil {
		return derr
	}
	// One prepared insert for the whole swap: per-row re-preparation would
	// stretch this exclusive write transaction (see idx_cues_media_idx).
	stmt, err := tx.Prepare(`INSERT INTO cues (media_file_id, track_id, idx, start_sec, end_sec, text) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	for _, c := range cues {
		if _, err := stmt.Exec(mediaFileID, trackID, c.Idx, c.StartSec, c.EndSec, c.Text); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CueCount returns the number of cues stored for a media file.
func (s *Store) CueCount(mediaFileID int64) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM cues WHERE media_file_id=?`, mediaFileID).Scan(&n)
	return n, err
}

// CleanSubtitles removes tracks, cues and chunks for a media file in one transaction.
func (s *Store) CleanSubtitles(mediaFileID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTx(tx)
	if _, err := tx.Exec(`DELETE FROM cues WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subtitle_tracks WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	return tx.Commit()
}

// CuesInRange returns cues with idx between start and end inclusive, plus one
// neighboring cue on each side for display context.
func (s *Store) CuesInRange(mediaFileID int64, startIdx, endIdx int) (span, before, after []Cue, err error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, track_id, idx, start_sec, end_sec, text, COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx BETWEEN ? AND ? ORDER BY idx`, mediaFileID, startIdx, endIdx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var c Cue
		if scanErr := rows.Scan(&c.ID, &c.MediaFileID, &c.TrackID, &c.Idx, &c.StartSec, &c.EndSec, &c.Text, &c.ChunkID); scanErr != nil {
			return nil, nil, nil, scanErr
		}
		span = append(span, c)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, nil, nil, rowsErr
	}
	before, err = s.cueAtIdx(mediaFileID, startIdx-1)
	if err != nil {
		return nil, nil, nil, err
	}
	after, err = s.cueAtIdx(mediaFileID, endIdx+1)
	return span, before, after, err
}

// CuesAround returns the cues spanning [startIdx, endIdx] plus up to context
// cues on each side, in chronological order within each slice. It generalizes
// CuesInRange, which is the context=1 special case kept for the REST chunks
// endpoint.
func (s *Store) CuesAround(mediaFileID int64, startIdx, endIdx, context int) (span, before, after []Cue, err error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, track_id, idx, start_sec, end_sec, text, COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx BETWEEN ? AND ? ORDER BY idx`, mediaFileID, startIdx, endIdx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var c Cue
		if scanErr := rows.Scan(&c.ID, &c.MediaFileID, &c.TrackID, &c.Idx, &c.StartSec, &c.EndSec, &c.Text, &c.ChunkID); scanErr != nil {
			return nil, nil, nil, scanErr
		}
		span = append(span, c)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, nil, nil, rowsErr
	}
	if before, err = s.cuesNear(mediaFileID, startIdx, -1, context); err != nil {
		return nil, nil, nil, err
	}
	if after, err = s.cuesNear(mediaFileID, endIdx, 1, context); err != nil {
		return nil, nil, nil, err
	}
	return span, before, after, nil
}

// cuesNear collects up to n cues adjacent to an index boundary. Direction -1
// walks backwards from idx-1 and returns the result in ascending index order;
// direction +1 walks forward from idx+1.
func (s *Store) cuesNear(mediaFileID int64, idx, direction, n int) ([]Cue, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id, media_file_id, track_id, idx, start_sec, end_sec, text, COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx BETWEEN ? AND ? ORDER BY idx`, mediaFileID,
		min(idx+direction, idx+direction*n), max(idx+direction, idx+direction*n))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []Cue
	for rows.Next() {
		var c Cue
		if scanErr := rows.Scan(&c.ID, &c.MediaFileID, &c.TrackID, &c.Idx, &c.StartSec, &c.EndSec, &c.Text, &c.ChunkID); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) cueAtIdx(mediaFileID int64, idx int) ([]Cue, error) {
	row := s.db.QueryRow(`SELECT id, media_file_id, track_id, idx, start_sec, end_sec, text, COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx=?`, mediaFileID, idx)
	var c Cue
	err := row.Scan(&c.ID, &c.MediaFileID, &c.TrackID, &c.Idx, &c.StartSec, &c.EndSec, &c.Text, &c.ChunkID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []Cue{c}, nil
}
