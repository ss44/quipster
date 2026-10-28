package store

import (
	"database/sql"
)

type SubtitleTrack struct {
	ID            int64  `json:"id"`
	MediaFileID   int64  `json:"mediaFileId"`
	Source        string `json:"source"` // sidecar | embedded
	Lang          string `json:"lang"`
	StreamIndex   int    `json:"streamIndex"`
	ExtractedPath string `json:"extractedPath"`
}

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

func (s *Store) TracksForMedia(mediaFileID int64) ([]SubtitleTrack, error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, source, lang, stream_index, extracted_path FROM subtitle_tracks WHERE media_file_id=?`, mediaFileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

type Cue struct {
	ID          int64   `json:"id"`
	MediaFileID int64   `json:"mediaFileId"`
	TrackID     int64   `json:"trackId"`
	Idx         int     `json:"idx"`
	StartSec    float64 `json:"startSec"`
	EndSec      float64 `json:"endSec"`
	Text        string  `json:"text"`
	ChunkID     int64   `json:"chunkId"`
}

// ReplaceCues swaps the cue set for a media file within one transaction.
func (s *Store) ReplaceCues(mediaFileID, trackID int64, cues []Cue) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cues WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	for _, c := range cues {
		if _, err := tx.Exec(`INSERT INTO cues (media_file_id, track_id, idx, start_sec, end_sec, text) VALUES (?,?,?,?,?,?)`,
			mediaFileID, trackID, c.Idx, c.StartSec, c.EndSec, c.Text); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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
	defer tx.Rollback()
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
// neighbouring cue on each side for display context.
func (s *Store) CuesInRange(mediaFileID int64, startIdx, endIdx int) (span, before, after []Cue, err error) {
	rows, err := s.db.Query(`SELECT id, media_file_id, track_id, idx, start_sec, end_sec, text, COALESCE(chunk_id,0) FROM cues
		WHERE media_file_id=? AND idx BETWEEN ? AND ? ORDER BY idx`, mediaFileID, startIdx, endIdx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Cue
		if err := rows.Scan(&c.ID, &c.MediaFileID, &c.TrackID, &c.Idx, &c.StartSec, &c.EndSec, &c.Text, &c.ChunkID); err != nil {
			return nil, nil, nil, err
		}
		span = append(span, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	before, err = s.cueAtIdx(mediaFileID, startIdx-1)
	if err != nil {
		return nil, nil, nil, err
	}
	after, err = s.cueAtIdx(mediaFileID, endIdx+1)
	return span, before, after, err
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
