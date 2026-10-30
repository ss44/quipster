package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// MediaFile is one media row with its probe metadata and lifecycle status.
//
// Archive lifecycle fields are orthogonal to Status: archiving freezes the
// row's processing status exactly as it was, and restoring resumes it without
// any reconstruction. Archived is exposed to clients so the admin UI can badge
// rows; MissingAt and Deindexed are internal latches and stay out of JSON.
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
	ID               int64   `json:"id"`
	Mtime            int64   `json:"mtime"`
	AddedAt          int64   `json:"addedAt"`
	SizeBytes        int64   `json:"sizeBytes"`
	ArchivedAt       int64   `json:"archivedAt"`
	MissingAt        int64   `json:"-"`
	DurationSec      float64 `json:"durationSec"`
	StartTime        float64 `json:"startTime"`
	Width            int     `json:"width"`
	Year             int     `json:"year"`
	Height           int     `json:"height"`
	Season           int     `json:"season"`
	Episode          int     `json:"episode"`
	AudioStreamIndex int     `json:"audioStreamIndex"`
	Archived         bool    `json:"archived"`
	Deindexed        bool    `json:"-"`
}

const mediaCols = `id, path, container, size_bytes, duration_sec, width, height, video_codec, audio_codec,
	title, year, show_title, season, episode, mtime, status, error, added_at, start_time,
	audio_stream_index, audio_lang, archived, archived_at, missing_at, deindexed`

func scanMedia(row interface{ Scan(...any) error }) (*MediaFile, error) {
	var m MediaFile
	var archived, deindexed int64
	err := row.Scan(&m.ID, &m.Path, &m.Container, &m.SizeBytes, &m.DurationSec, &m.Width, &m.Height,
		&m.VideoCodec, &m.AudioCodec, &m.Title, &m.Year, &m.ShowTitle, &m.Season, &m.Episode,
		&m.Mtime, &m.Status, &m.Error, &m.AddedAt, &m.StartTime,
		&m.AudioStreamIndex, &m.AudioLang, &archived, &m.ArchivedAt, &m.MissingAt, &deindexed)
	if err != nil {
		return nil, err
	}
	m.Archived = archived > 0
	m.Deindexed = deindexed > 0
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
//
// archived selects the archive slice: "1" lists only archived rows, "any"
// lists everything, and every other value (including empty) lists active rows
// only, so ordinary library views never surface archived content.
func (s *Store) ListMedia(limit, offset int, q, archived string) (*MediaListPage, error) {
	where := "archived = 0"
	switch archived {
	case "1":
		where = "archived = 1"
	case "any":
		where = "1 = 1"
	}
	countQ := `SELECT COUNT(*) FROM media_files WHERE ` + where
	listQ := `SELECT ` + mediaCols + ` FROM media_files WHERE ` + where + ` ORDER BY title, show_title, season, episode LIMIT ? OFFSET ?`
	args := []any{}
	if q != "" {
		like := "%" + q + "%"
		countQ = `SELECT COUNT(*) FROM media_files WHERE ` + where + ` AND (title LIKE ? OR show_title LIKE ? OR path LIKE ?)`
		listQ = `SELECT ` + mediaCols + ` FROM media_files WHERE ` + where + ` AND (title LIKE ? OR show_title LIKE ? OR path LIKE ?) ORDER BY title, show_title, season, episode LIMIT ? OFFSET ?`
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

// ArchiveMissingUnder implements scan pruning without deletion. Rows under
// base that the scan did not see are partitioned by the archive grace window:
// first-time absences only latch missing_at, absences inside the grace window
// are left untouched, and absences older than the grace window are archived.
// Archived rows are skipped entirely so repeated scans of a long-gone share
// stay free. Rows that ARE in keep are never touched.
//
// The UPDATE statements deliberately avoid rewriting status: an archived row
// keeps its pre-archive processing status frozen, so a later restore resumes
// the exact prior lifecycle without reconstruction.
//
// Returns the ids archived in this pass (which the caller turns into
// de-index jobs) and the number of rows that merely latched missing_at.
func (s *Store) ArchiveMissingUnder(base string, keep map[string]bool, grace time.Duration, now time.Time) (archived []int64, newlyMissing int, err error) {
	base = strings.TrimSuffix(base, "/")
	rows, err := s.db.Query(`SELECT id, path, archived, missing_at FROM media_files WHERE path LIKE ?`, base+"/%")
	if err != nil {
		return nil, 0, err
	}
	var latch, archive []int64
	for rows.Next() {
		var id int64
		var p string
		var isArchived, missingAt int64
		if err := rows.Scan(&id, &p, &isArchived, &missingAt); err != nil {
			closeRows(rows)
			return nil, 0, err
		}
		if keep[p] || isArchived > 0 {
			continue
		}
		if missingAt == 0 {
			latch = append(latch, id)
			continue
		}
		if now.UnixMilli()-missingAt >= grace.Milliseconds() {
			archive = append(archive, id)
		}
		// Inside the grace window: keep running the clock from the first
		// observation; refreshing missing_at here would make the window
		// indeterminate for shares that flap every scan.
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	nowMs := now.UnixMilli()
	if err := updateIDsBatched(s.db, `UPDATE media_files SET missing_at=? WHERE id IN (`, nowMs, latch); err != nil {
		return archived, 0, err
	}
	if err := updateIDsBatched(s.db, `UPDATE media_files SET archived=1, archived_at=?, deindexed=0 WHERE id IN (`, nowMs, archive); err != nil {
		return archived, len(latch), err
	}
	return archive, len(latch), nil
}

// RestoreSeenUnder re-activates rows for paths the scan saw again. It is the
// mirror of ArchiveMissingUnder and is safe to run even after an incomplete
// walk: a seen path was genuinely observed, so clearing its absence state
// cannot hide a real disappearance.
//
// Restoring an archived row clears the archive flags and, when the Meili
// documents were already deleted (deindexed=1), resets the chunk index flags
// so the normal index job re-adds them. Rows restored while their documents
// were never deleted keep their index untouched and become searchable
// instantly. Returns the ids that need index jobs re-enqueued and how many
// archived rows were restored.
func (s *Store) RestoreSeenUnder(base string, keep map[string]bool) (needsReindex []int64, restored int, err error) {
	base = strings.TrimSuffix(base, "/")
	rows, err := s.db.Query(`SELECT id, path, archived FROM media_files
		WHERE path LIKE ? AND (missing_at != 0 OR archived = 1)`, base+"/%")
	if err != nil {
		return nil, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var p string
		var isArchived int64
		if scanErr := rows.Scan(&id, &p, &isArchived); scanErr != nil {
			closeRows(rows)
			return nil, 0, scanErr
		}
		if keep[p] {
			ids = append(ids, id)
		}
	}
	closeRows(rows)
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	reindex, n, rerr := s.restoreMediaIDs(ids)
	return reindex, n, rerr
}

// RestoreMedia re-activates one media row (admin action). A row that is not
// archived is a no-op; restoring a row whose file is still absent is allowed
// and safe: search re-activates from stored captions immediately, clip
// requests fail until the file exists again, and a still-absent file is
// simply archived again after the grace window on a later scan.
func (s *Store) RestoreMedia(id int64) (needsReindex, restored bool, err error) {
	reindex, n, err := s.restoreMediaIDs([]int64{id})
	if err != nil {
		return false, false, err
	}
	return len(reindex) > 0, n > 0, nil
}

// restoreMediaIDs applies the restore transition to the given ids in bounded
// batches. Returns the subset whose Meili documents had been deleted and
// therefore need an index job re-enqueued, plus how many rows were actually
// archived before this call.
func (s *Store) restoreMediaIDs(ids []int64) (needsReindex []int64, restored int, err error) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
	ph := placeholders(len(ids))
	args := idArgs(ids)
	//nolint:gosec // the concatenated part is a "?" placeholder list sized by len(ids); values are parameterized.
	rows, err := s.db.Query(`SELECT id, deindexed FROM media_files WHERE id IN (`+ph+`) AND archived=1`, args...)
	if err != nil {
		return nil, 0, err
	}
	var archivedIDs []int64
	for rows.Next() {
		var id, deindexed int64
		if scanErr := rows.Scan(&id, &deindexed); scanErr != nil {
			closeRows(rows)
			return nil, 0, scanErr
		}
		archivedIDs = append(archivedIDs, id)
		if deindexed > 0 {
			needsReindex = append(needsReindex, id)
		}
	}
	closeRows(rows)
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	// Documents for these rows are gone from Meili; make the normal index job
	// re-add every chunk. Media restored before de-indexing keep their docs
	// and their index flags.
	if len(needsReindex) > 0 {
		if cerr := clearChunkFlagsBatched(s.db, needsReindex); cerr != nil {
			return nil, 0, cerr
		}
	}
	restored, err = s.clearArchiveFlagsBatched(archivedIDs)
	if err != nil {
		return nil, 0, err
	}
	// Clear stale absence latches on rows that were missing but never archived.
	if lerr := updateIDsBatched(s.db, `UPDATE media_files SET missing_at=? WHERE archived=0 AND id IN (`, 0, ids); lerr != nil {
		return nil, 0, lerr
	}
	return needsReindex, restored, nil
}

// clearArchiveFlagsBatched re-activates archived rows in bounded batches and
// returns how many rows transitioned.
func (s *Store) clearArchiveFlagsBatched(ids []int64) (int, error) {
	const batch = 400
	restored := 0
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		chunk := ids[start:end]
		ph := placeholders(len(chunk))
		args := idArgs(chunk)
		//nolint:gosec // only a parameter placeholder list is concatenated; ids are bound values.
		res, err := s.db.Exec(`UPDATE media_files
			SET archived=0, archived_at=0, missing_at=0, deindexed=0
			WHERE id IN (`+ph+`) AND archived=1`, args...)
		if err != nil {
			return restored, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return restored, err
		}
		restored += int(n)
	}
	return restored, nil
}

// MarkMediaDeindexed latches that a media's Meili documents were removed. The
// archived=1 guard keeps a de-index that raced a restore from latching a
// stale flag onto a re-activated row.
func (s *Store) MarkMediaDeindexed(id int64) error {
	_, err := s.db.Exec(`UPDATE media_files SET deindexed=1 WHERE id=? AND archived=1`, id)
	return err
}

// ArchivedPendingDeindex lists archived media whose Meili documents have not
// been confirmed removed, for boot-time reconciliation.
func (s *Store) ArchivedPendingDeindex() ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM media_files WHERE archived=1 AND deindexed=0 ORDER BY id`)
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

// updateIDsBatched runs one statement per chunk of ids, staying well inside
// SQLite's variable limit so large prunes and restores collapse into a
// handful of transactions. The prefix must end with "IN (" and is completed
// with the placeholder list.
func updateIDsBatched(db *sql.DB, prefix string, arg any, ids []int64) error {
	const batch = 400
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		chunk := ids[start:end]
		ph := placeholders(len(chunk))
		args := append([]any{arg}, idArgs(chunk)...)
		//nolint:gosec // only a parameter placeholder list is concatenated; ids are bound values.
		if _, err := db.Exec(prefix+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// clearChunkFlagsBatched resets chunk index flags for many media files in
// bounded batches (restore path).
func clearChunkFlagsBatched(db *sql.DB, mediaIDs []int64) error {
	const batch = 400
	for start := 0; start < len(mediaIDs); start += batch {
		end := min(start+batch, len(mediaIDs))
		chunk := mediaIDs[start:end]
		ph := placeholders(len(chunk))
		args := idArgs(chunk)
		//nolint:gosec // only a parameter placeholder list is concatenated; ids are bound values.
		if _, err := db.Exec(`UPDATE chunks SET indexed_at=0 WHERE media_file_id IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// placeholders builds "?,?" repeated n times.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// idArgs converts ids to driver arguments.
func idArgs(ids []int64) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
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

// MediaStatusCounts returns the number of media rows per status. The archive
// count is reported under the "archived" key so the admin dashboard can show
// hidden inventory without a second query.
func (s *Store) MediaStatusCounts() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM media_files WHERE archived=0 GROUP BY status`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var archivedCount int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_files WHERE archived=1`).Scan(&archivedCount); err != nil {
		return nil, err
	}
	out["archived"] = archivedCount
	return out, nil
}

// MediaIDsByStatus lists media ids matching any of the given statuses. Rows
// that are archived or currently inside an absence grace window are excluded:
// re-probing an archived row would mutate its frozen status, and probing a
// file known to be missing only manufactures error rows that outlive the
// grace window.
func (s *Store) MediaIDsByStatus(statuses ...string) ([]int64, error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, 0, len(statuses))
	for _, st := range statuses {
		args = append(args, st)
	}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length statuses slice; values are parameterized.
	rows, err := s.db.Query(`SELECT id FROM media_files
		WHERE status IN (`+ph+`) AND archived=0 AND missing_at=0`, args...)
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
// query substring (LIKE-based fallback for files without subtitles). Archived
// rows never match, even though their frozen status still reads 'indexed'.
func (s *Store) TitleMatches(q string, limit int) ([]MediaFile, error) {
	like := "%" + q + "%"
	//nolint:gosec // mediaCols is a fixed constant column list; all query values are parameterized.
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files
		WHERE (title LIKE ? OR show_title LIKE ?) AND status IN ('indexed','no_subs') AND archived=0
		ORDER BY title LIMIT ?`, like, like, limit)
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
