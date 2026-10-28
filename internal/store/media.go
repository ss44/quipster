package store

import (
	"database/sql"
	"errors"
	"strings"
)

// MediaFile is one media row with its probe metadata and lifecycle status.
type MediaFile struct {
	Title            string  `json:"title"`
	Path             string  `json:"path"`
	Container        string  `json:"container"`
	AudioLang        string  `json:"audioLang"`
	Error            string  `json:"error"`
	Status           string  `json:"status"`
	ShowTitle        string  `json:"showTitle"`
	VideoCodec       string  `json:"videoCodec"`
	AudioCodec       string  `json:"audioCodec"`
	Width            int     `json:"width"`
	Year             int     `json:"year"`
	Height           int     `json:"height"`
	Season           int     `json:"season"`
	Episode          int     `json:"episode"`
	Mtime            int64   `json:"mtime"`
	ID               int64   `json:"id"`
	DurationSec      float64 `json:"durationSec"`
	AddedAt          int64   `json:"addedAt"`
	StartTime        float64 `json:"startTime"`
	AudioStreamIndex int     `json:"audioStreamIndex"`
	SizeBytes        int64   `json:"sizeBytes"`
}

const mediaCols = `id, path, container, size_bytes, duration_sec, width, height, video_codec, audio_codec,
	title, year, show_title, season, episode, mtime, status, error, added_at, start_time,
	audio_stream_index, audio_lang`

func scanMedia(row interface{ Scan(...any) error }) (*MediaFile, error) {
	var m MediaFile
	err := row.Scan(&m.ID, &m.Path, &m.Container, &m.SizeBytes, &m.DurationSec, &m.Width, &m.Height,
		&m.VideoCodec, &m.AudioCodec, &m.Title, &m.Year, &m.ShowTitle, &m.Season, &m.Episode,
		&m.Mtime, &m.Status, &m.Error, &m.AddedAt, &m.StartTime,
		&m.AudioStreamIndex, &m.AudioLang)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpsertMedia inserts or refreshes a media file keyed on path. Returns the id,
// and whether the row was newly created or changed (size/mtime delta).
func (s *Store) UpsertMedia(path, container string, size int64, mtime int64, title string, year, season, episode int, showTitle string) (id int64, changed bool, err error) {
	// RETURNING reports the id of both the inserted row and the conflicting row
	// we updated. LastInsertId cannot be used here: an ON CONFLICT DO UPDATE is
	// an update, so the connection's last-insert rowid stays pointing at an
	// unrelated (or nonexistent) row and would enqueue jobs for the wrong media.
	row := s.db.QueryRow(`INSERT INTO media_files (path, container, size_bytes, mtime, title, year, show_title, season, episode, status, added_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'pending', ?)
		ON CONFLICT(path) DO UPDATE SET
			container=excluded.container, size_bytes=excluded.size_bytes, mtime=excluded.mtime,
			title=excluded.title, year=excluded.year, show_title=excluded.show_title,
			season=excluded.season, episode=excluded.episode,
			status = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN 'pending' ELSE media_files.status END,
			error = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN '' ELSE media_files.error END
		WHERE excluded.size_bytes != media_files.size_bytes OR excluded.mtime != media_files.mtime OR media_files.status IN ('error')
		RETURNING id`,
		path, container, size, mtime, title, year, showTitle, season, episode, nowMillis())
	if err := row.Scan(&id); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
		// The WHERE clause suppressed the update because the file is unchanged;
		// resolve the existing row id explicitly.
		if err := s.db.QueryRow(`SELECT id FROM media_files WHERE path=?`, path).Scan(&id); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	return id, true, nil
}

// GetMedia returns a media row by id, or nil when not found.
func (s *Store) GetMedia(id int64) (*MediaFile, error) {
	row := s.db.QueryRow(`SELECT `+mediaCols+` FROM media_files WHERE id=?`, id)
	m, err := scanMedia(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// GetMediaByPath returns a media row by path, or nil when not found.
func (s *Store) GetMediaByPath(path string) (*MediaFile, error) {
	row := s.db.QueryRow(`SELECT `+mediaCols+` FROM media_files WHERE path=?`, path)
	m, err := scanMedia(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// UpdateMediaProbe persists probe results for a media file.
func (s *Store) UpdateMediaProbe(id int64, duration float64, width, height int, vcodec, acodec, container string, startTime float64, audioStream int, audioLang string) error {
	_, err := s.db.Exec(`UPDATE media_files SET duration_sec=?, width=?, height=?, video_codec=?, audio_codec=?, container=?, start_time=?, audio_stream_index=?, audio_lang=? WHERE id=?`,
		duration, width, height, vcodec, acodec, container, startTime, audioStream, audioLang, id)
	return err
}

// SetMediaStatus updates a media row's processing status (and error text).
func (s *Store) SetMediaStatus(id int64, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE media_files SET status=?, error=? WHERE id=?`, status, errMsg, id)
	return err
}

// MarkIndexed sets status=indexed and latches ever_indexed (used to skip
// pointless delete round-trips on first-time indexing).
func (s *Store) MarkIndexed(id int64) error {
	_, err := s.db.Exec(`UPDATE media_files SET status='indexed', error='', ever_indexed=1 WHERE id=?`, id)
	return err
}

// EverIndexed reports whether the media file has ever been indexed.
func (s *Store) EverIndexed(id int64) (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT ever_indexed FROM media_files WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

// MediaListPage is a page of media rows with the total matching count.
type MediaListPage struct {
	Items []MediaFile `json:"items"`
	Total int64       `json:"total"`
}

// ListMedia lists media rows newest-title first, optionally filtered by a
// LIKE search on title, show title, and path.
func (s *Store) ListMedia(limit, offset int, q string) (*MediaListPage, error) {
	countQ := `SELECT COUNT(*) FROM media_files`
	listQ := `SELECT ` + mediaCols + ` FROM media_files ORDER BY title, show_title, season, episode LIMIT ? OFFSET ?`
	args := []any{}
	if q != "" {
		like := "%" + q + "%"
		countQ = `SELECT COUNT(*) FROM media_files WHERE (title LIKE ? OR show_title LIKE ? OR path LIKE ?)`
		listQ = `SELECT ` + mediaCols + ` FROM media_files WHERE (title LIKE ? OR show_title LIKE ? OR path LIKE ?) ORDER BY title, show_title, season, episode LIMIT ? OFFSET ?`
		args = append(args, like, like, like)
	}
	var total int64
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(listQ, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	page := &MediaListPage{Total: total}
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, *m)
	}
	return page, rows.Err()
}

// DeleteMissingUnder removes media rows under base whose paths are not in keep.
func (s *Store) DeleteMissingUnder(base string, keep map[string]bool) ([]int64, error) {
	base = strings.TrimSuffix(base, "/")
	rows, err := s.db.Query(`SELECT id, path FROM media_files WHERE path LIKE ?`, base+"/%")
	if err != nil {
		return nil, err
	}
	var remove []int64
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			closeRows(rows)
			return nil, err
		}
		if !keep[p] {
			remove = append(remove, id)
		}
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range remove {
		if _, err := s.db.Exec(`DELETE FROM media_files WHERE id=?`, id); err != nil {
			return remove, err
		}
	}
	return remove, nil
}

// MediaStatusCounts returns the number of media rows per status.
func (s *Store) MediaStatusCounts() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM media_files GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// MediaIDsByStatus lists media ids matching any of the given statuses.
func (s *Store) MediaIDsByStatus(statuses ...string) ([]int64, error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, 0, len(statuses))
	for _, st := range statuses {
		args = append(args, st)
	}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length statuses slice; values are parameterized.
	rows, err := s.db.Query(`SELECT id FROM media_files WHERE status IN (`+ph+`)`, args...)
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

// TitleMatches lists indexed media whose title or show title contains the
// query substring (LIKE-based fallback for files without subtitles).
func (s *Store) TitleMatches(q string, limit int) ([]MediaFile, error) {
	like := "%" + q + "%"
	//nolint:gosec // mediaCols is a fixed constant column list; all query values are parameterized.
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files
		WHERE (title LIKE ? OR show_title LIKE ?) AND status IN ('indexed','no_subs') ORDER BY title LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []MediaFile
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
