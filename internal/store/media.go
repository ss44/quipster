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

// MediaUpsert is the scanner's metadata snapshot for one media file. It exists
// so many file-system entries can be persisted in a single transaction instead
// of paying one SQLite commit per file.
type MediaUpsert struct {
	Path      string
	Container string
	Title     string
	ShowTitle string
	Size      int64
	Mtime     int64
	Year      int
	Season    int
	Episode   int
}

// upsertMediaSQL updates only when size or mtime changed. The WHERE clause is
// what makes scan idempotent: unchanged files return no row and therefore do
// not enqueue another probe. Error rows are intentionally not special-cased;
// retrying permanent failures on every scan creates an endless probe backlog.
// Explicit reprobes still exist for operator-driven retries.
const upsertMediaSQL = `INSERT INTO media_files (path, container, size_bytes, mtime, title, year, show_title, season, episode, status, added_at)
	VALUES (?,?,?,?,?,?,?,?,?, 'pending', ?)
	ON CONFLICT(path) DO UPDATE SET
		container=excluded.container, size_bytes=excluded.size_bytes, mtime=excluded.mtime,
		title=excluded.title, year=excluded.year, show_title=excluded.show_title,
		season=excluded.season, episode=excluded.episode,
		status = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN 'pending' ELSE media_files.status END,
		error = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN '' ELSE media_files.error END,
		optimization_flags = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN '' ELSE media_files.optimization_flags END,
		optimization_checked = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN 0 ELSE media_files.optimization_checked END
	WHERE excluded.size_bytes != media_files.size_bytes OR excluded.mtime != media_files.mtime
	RETURNING id`

// UpsertMedia inserts or refreshes a media file keyed on path. Returns the id,
// and whether the row was newly created or changed (size/mtime delta).
func (s *Store) UpsertMedia(path, container string, size int64, mtime int64, title string, year, season, episode int, showTitle string) (id int64, changed bool, err error) {
	ids, changedRows, err := s.UpsertMediaBatch([]MediaUpsert{{
		Path: path, Container: container, Size: size, Mtime: mtime,
		Title: title, Year: year, Season: season, Episode: episode, ShowTitle: showTitle,
	}})
	if err != nil {
		return 0, false, err
	}
	if len(ids) != 1 || len(changedRows) != 1 {
		return 0, false, errors.New("store: media upsert returned no row")
	}
	return ids[0], changedRows[0], nil
}

// UpsertMediaBatch inserts or refreshes many media rows in one transaction.
// It returns parallel slices of ids and change flags, in the same order as
// entries. A suppressed update (unchanged file) resolves its id with a SELECT
// inside the transaction so callers still receive a usable media id.
func (s *Store) UpsertMediaBatch(entries []MediaUpsert) ([]int64, []bool, error) {
	if len(entries) == 0 {
		return nil, nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer rollbackTx(tx)
	// Prepared statements are owned by the transaction and are closed by
	// Commit/Rollback, so there is no separate Close here.
	stmt, err := tx.Prepare(upsertMediaSQL)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, len(entries))
	changed := make([]bool, len(entries))
	for i, e := range entries {
		var id int64
		err := stmt.QueryRow(e.Path, e.Container, e.Size, e.Mtime, e.Title, e.Year, e.ShowTitle,
			e.Season, e.Episode, nowMillis()).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			// No row was updated because the file is unchanged. Resolve the
			// existing row id in the same transaction so callers can still
			// correlate it without a second round trip.
			if serr := tx.QueryRow(`SELECT id FROM media_files WHERE path=?`, e.Path).Scan(&id); serr != nil {
				return nil, nil, serr
			}
			ids[i] = id
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ids[i] = id
		changed[i] = true
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return ids, changed, nil
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

// UpdateMediaOptimizationFlags stores the deterministic optimization codes from a probe.
func (s *Store) UpdateMediaOptimizationFlags(id int64, flags []string) error {
	_, err := s.db.Exec(`UPDATE media_files SET optimization_flags=?, optimization_checked=1 WHERE id=?`, strings.Join(flags, ","), id)
	return err
}

// MediaOptimizationStats returns probed-file counts and per-code recommendation counts.
func (s *Store) MediaOptimizationStats() (int64, int64, map[string]int64, error) {
	rows, err := s.db.Query(`SELECT optimization_flags FROM media_files WHERE optimization_checked=1`)
	if err != nil {
		return 0, 0, nil, err
	}
	defer closeRows(rows)
	counts := map[string]int64{}
	var scanned, recommended int64
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return 0, 0, nil, err
		}
		scanned++
		if encoded == "" {
			continue
		}
		recommended++
		for _, flag := range strings.Split(encoded, ",") {
			if flag != "" {
				counts[flag]++
			}
		}
	}
	return scanned, recommended, counts, rows.Err()
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
	if err := s.DeleteMediaIDs(remove); err != nil {
		return remove, err
	}
	return remove, nil
}

// DeleteMediaIDs removes media rows in bounded batches. A single DELETE per id
// is unnecessarily slow during a large prune; IN chunks keep the statement
// below SQLite's variable limit while still collapsing thousands of rows into
// a handful of transactions.
func (s *Store) DeleteMediaIDs(ids []int64) error {
	const deleteBatch = 400 // stay well inside SQLITE_MAX_VARIABLE_NUMBER
	for start := 0; start < len(ids); start += deleteBatch {
		end := min(start+deleteBatch, len(ids))
		chunk := ids[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		//nolint:gosec // only a parameter placeholder list is concatenated; ids are bound values.
		if _, err := s.db.Exec(`DELETE FROM media_files WHERE id IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
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
