package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// WAL supports concurrent readers; writes serialize via busy_timeout and
	// BEGIN IMMEDIATE transactions.
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS media_files (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL UNIQUE,
			container TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			duration_sec REAL NOT NULL DEFAULT 0,
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			video_codec TEXT NOT NULL DEFAULT '',
			audio_codec TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			year INTEGER NOT NULL DEFAULT 0,
			show_title TEXT NOT NULL DEFAULT '',
			season INTEGER NOT NULL DEFAULT 0,
			episode INTEGER NOT NULL DEFAULT 0,
			mtime INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','indexed','no_subs','error')),
			error TEXT NOT NULL DEFAULT '',
			added_at INTEGER NOT NULL DEFAULT 0,
			start_time REAL NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS subtitle_tracks (
			id INTEGER PRIMARY KEY,
			media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
			source TEXT NOT NULL,
			lang TEXT NOT NULL DEFAULT '',
			stream_index INTEGER NOT NULL DEFAULT 0,
			extracted_path TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id INTEGER PRIMARY KEY,
			media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
			start_cue_idx INTEGER NOT NULL,
			end_cue_idx INTEGER NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			indexed_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(media_file_id, start_cue_idx)
		)`,
		`CREATE TABLE IF NOT EXISTS cues (
			id INTEGER PRIMARY KEY,
			media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
			track_id INTEGER NOT NULL REFERENCES subtitle_tracks(id) ON DELETE CASCADE,
			idx INTEGER NOT NULL,
			start_sec REAL NOT NULL,
			end_sec REAL NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			chunk_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cues_media_start ON cues(media_file_id, start_sec)`,
		`CREATE INDEX IF NOT EXISTS idx_cues_chunk ON cues(chunk_id)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY,
			type TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','done','error')),
			priority INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			worker_id TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			progress REAL NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs(status, priority DESC, id)`,
		`CREATE TABLE IF NOT EXISTS scans (
			id INTEGER PRIMARY KEY,
			root_path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running',
			files_seen INTEGER NOT NULL DEFAULT 0,
			files_done INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS keyframes (
			media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
			pts REAL NOT NULL,
			PRIMARY KEY(media_file_id, pts)
		)`,
		`CREATE TABLE IF NOT EXISTS cache_entries (
			cache_key TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			kind TEXT NOT NULL,
			bytes INTEGER NOT NULL DEFAULT 0,
			last_access INTEGER NOT NULL DEFAULT 0,
			hits INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	var col int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_files') WHERE name='ever_indexed'`).Scan(&col); err != nil {
		return err
	}
	if col == 0 {
		if _, err := s.db.Exec(`ALTER TABLE media_files ADD COLUMN ever_indexed INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate ever_indexed: %w", err)
		}
	}
	return nil
}

// Recover resets jobs that were mid-flight when the process died so they can be retried.
func (s *Store) Recover() error {
	_, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='' WHERE status='running'`)
	return err
}

func nowMillis() int64 { return time.Now().UnixMilli() }

type MediaFile struct {
	ID          int64   `json:"id"`
	Path        string  `json:"path"`
	Container   string  `json:"container"`
	SizeBytes   int64   `json:"sizeBytes"`
	DurationSec float64 `json:"durationSec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	VideoCodec  string  `json:"videoCodec"`
	AudioCodec  string  `json:"audioCodec"`
	Title       string  `json:"title"`
	Year        int     `json:"year"`
	ShowTitle   string  `json:"showTitle"`
	Season      int     `json:"season"`
	Episode     int     `json:"episode"`
	Mtime       int64   `json:"mtime"`
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	AddedAt     int64   `json:"addedAt"`
	StartTime   float64 `json:"startTime"`
}

const mediaCols = `id, path, container, size_bytes, duration_sec, width, height, video_codec, audio_codec,
	title, year, show_title, season, episode, mtime, status, error, added_at, start_time`

func scanMedia(row interface{ Scan(...any) error }) (*MediaFile, error) {
	var m MediaFile
	err := row.Scan(&m.ID, &m.Path, &m.Container, &m.SizeBytes, &m.DurationSec, &m.Width, &m.Height,
		&m.VideoCodec, &m.AudioCodec, &m.Title, &m.Year, &m.ShowTitle, &m.Season, &m.Episode,
		&m.Mtime, &m.Status, &m.Error, &m.AddedAt, &m.StartTime)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpsertMedia inserts or refreshes a media file keyed on path. Returns the id,
// and whether the row was newly created or changed (size/mtime delta).
func (s *Store) UpsertMedia(path, container string, size int64, mtime int64, title string, year, season, episode int, showTitle string) (id int64, changed bool, err error) {
	res, err := s.db.Exec(`INSERT INTO media_files (path, container, size_bytes, mtime, title, year, show_title, season, episode, status, added_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'pending', ?)
		ON CONFLICT(path) DO UPDATE SET
			container=excluded.container, size_bytes=excluded.size_bytes, mtime=excluded.mtime,
			title=excluded.title, year=excluded.year, show_title=excluded.show_title,
			season=excluded.season, episode=excluded.episode,
			status = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN 'pending' ELSE media_files.status END,
			error = CASE WHEN media_files.size_bytes != excluded.size_bytes OR media_files.mtime != excluded.mtime THEN '' ELSE media_files.error END
		WHERE excluded.size_bytes != media_files.size_bytes OR excluded.mtime != media_files.mtime OR media_files.status IN ('error')`,
		path, container, size, mtime, title, year, showTitle, season, episode, nowMillis())
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, _ := res.LastInsertId()
		return id, true, nil
	}
	// No rows changed: fetch existing id.
	row := s.db.QueryRow(`SELECT id FROM media_files WHERE path=?`, path)
	if err := row.Scan(&id); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

func (s *Store) GetMedia(id int64) (*MediaFile, error) {
	row := s.db.QueryRow(`SELECT `+mediaCols+` FROM media_files WHERE id=?`, id)
	m, err := scanMedia(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

func (s *Store) GetMediaByPath(path string) (*MediaFile, error) {
	row := s.db.QueryRow(`SELECT `+mediaCols+` FROM media_files WHERE path=?`, path)
	m, err := scanMedia(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

func (s *Store) UpdateMediaProbe(id int64, duration float64, width, height int, vcodec, acodec, container string, startTime float64) error {
	_, err := s.db.Exec(`UPDATE media_files SET duration_sec=?, width=?, height=?, video_codec=?, audio_codec=?, container=?, start_time=? WHERE id=?`,
		duration, width, height, vcodec, acodec, container, startTime, id)
	return err
}

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

func (s *Store) EverIndexed(id int64) (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT ever_indexed FROM media_files WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

type MediaListPage struct {
	Items []MediaFile `json:"items"`
	Total int64       `json:"total"`
}

func (s *Store) ListMedia(limit, offset int, q string) (*MediaListPage, error) {
	where := "1=1"
	args := []any{}
	if q != "" {
		where = `(title LIKE ? OR show_title LIKE ? OR path LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_files WHERE `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files WHERE `+where+` ORDER BY title, show_title, season, episode LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// DeleteMissing removes media rows whose paths are not in keep; returns the removed ids.
func (s *Store) DeleteMissing(root string, keep map[string]bool) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id, path FROM media_files WHERE path LIKE ?`, root+"/%")
	if err != nil {
		return nil, err
	}
	var remove []int64
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return nil, err
		}
		if !keep[p] {
			remove = append(remove, id)
		}
	}
	rows.Close()
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

type Chunk struct {
	ID          int64 `json:"id"`
	MediaFileID int64 `json:"mediaFileId"`
	StartCueIdx int   `json:"startCueIdx"`
	EndCueIdx   int   `json:"endCueIdx"`
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

type Job struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Payload    string  `json:"payload"`
	Status     string  `json:"status"`
	Priority   int     `json:"priority"`
	Attempts   int     `json:"attempts"`
	WorkerID   string  `json:"workerId"`
	Error      string  `json:"error"`
	Progress   float64 `json:"progress"`
	CreatedAt  int64   `json:"createdAt"`
	StartedAt  int64   `json:"startedAt"`
	FinishedAt int64   `json:"finishedAt"`
}

func (s *Store) EnqueueJob(jobType, payload string, priority int) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO jobs (type, payload, priority, status, created_at) VALUES (?,?,?,'queued',?)`,
		jobType, payload, priority, nowMillis())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimJob atomically claims the next queued job of the given types using a
// single BEGIN IMMEDIATE transaction with RETURNING.
func (s *Store) ClaimJob(ctx context.Context, workerID string, types []string) (*Job, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	rollback := func() { conn.ExecContext(ctx, "ROLLBACK") }
	ph := strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")
	args := []any{workerID, nowMillis()}
	for _, t := range types {
		args = append(args, t)
	}
	row := conn.QueryRowContext(ctx,
		`UPDATE jobs SET status='running', worker_id=?, started_at=?, attempts=attempts+1
		 WHERE id = (SELECT id FROM jobs WHERE status='queued' AND type IN (`+ph+`) ORDER BY priority DESC, id LIMIT 1)
		 RETURNING id, type, payload, priority, attempts`, args...)
	var j Job
	if err := row.Scan(&j.ID, &j.Type, &j.Payload, &j.Priority, &j.Attempts); err != nil {
		rollback()
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		rollback()
		return nil, err
	}
	return &j, nil
}

func (s *Store) FinishJob(id int64, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?, error=?, progress=CASE WHEN ?='done' THEN 1 ELSE progress END, finished_at=? WHERE id=?`,
		status, errMsg, status, nowMillis(), id)
	return err
}

func (s *Store) SetJobProgress(id int64, progress float64) error {
	_, err := s.db.Exec(`UPDATE jobs SET progress=? WHERE id=?`, progress, id)
	return err
}

func (s *Store) RequeueJob(id int64, errMsg string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='', error=? WHERE id=?`, errMsg, id)
	return err
}

type JobStats struct {
	Queued  int64 `json:"queued"`
	Running int64 `json:"running"`
	Done    int64 `json:"done"`
	Error   int64 `json:"error"`
}

func (s *Store) JobStats() (*JobStats, error) {
	st := &JobStats{}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		switch status {
		case "queued":
			st.Queued = n
		case "running":
			st.Running = n
		case "done":
			st.Done = n
		case "error":
			st.Error = n
		}
	}
	return st, rows.Err()
}

func (s *Store) ListJobs(limit int) ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Type, &j.Payload, &j.Status, &j.Priority, &j.Attempts, &j.WorkerID, &j.Error, &j.Progress, &j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type Scan struct {
	ID         int64  `json:"id"`
	RootPath   string `json:"rootPath"`
	Status     string `json:"status"`
	FilesSeen  int64  `json:"filesSeen"`
	FilesDone  int64  `json:"filesDone"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
}

func (s *Store) StartScan(root string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO scans (root_path, status, started_at) VALUES (?,'running',?)`, root, nowMillis())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateScan(id int64, seen, done int64, status string) error {
	if status == "" {
		_, err := s.db.Exec(`UPDATE scans SET files_seen=?, files_done=? WHERE id=?`, seen, done, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE scans SET files_seen=?, files_done=?, status=?, finished_at=? WHERE id=?`, seen, done, status, nowMillis(), id)
	return err
}

// RunningScan returns the most recent unfinished scan, or nil.
func (s *Store) RunningScan() (*Scan, error) {
	row := s.db.QueryRow(`SELECT id, root_path, status, files_seen, files_done, started_at, COALESCE(finished_at,0) FROM scans WHERE status='running' ORDER BY id DESC LIMIT 1`)
	var sc Scan
	err := row.Scan(&sc.ID, &sc.RootPath, &sc.Status, &sc.FilesSeen, &sc.FilesDone, &sc.StartedAt, &sc.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *Store) HasActiveScanJob() (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='scan' AND status IN ('queued','running')`).Scan(&n)
	return n > 0, err
}

func (s *Store) MediaStatusCounts() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM media_files GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *Store) SetKeyframes(mediaFileID int64, pts []float64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM keyframes WHERE media_file_id=?`, mediaFileID); err != nil {
		return err
	}
	for _, p := range pts {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO keyframes (media_file_id, pts) VALUES (?,?)`, mediaFileID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddKeyframes merges keyframe points without clearing existing rows (used by
// windowed probes that cover different regions of the same file).
func (s *Store) AddKeyframes(mediaFileID int64, pts []float64) error {
	for _, p := range pts {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO keyframes (media_file_id, pts) VALUES (?,?)`, mediaFileID, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) HasKeyframes(mediaFileID int64) (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM keyframes WHERE media_file_id=?`, mediaFileID).Scan(&n)
	return n > 0, err
}

// SnapKeyframe returns the largest keyframe at or before t (0 if none).
func (s *Store) SnapKeyframe(mediaFileID int64, t float64) (float64, error) {
	var kf sql.NullFloat64
	err := s.db.QueryRow(`SELECT MAX(pts) FROM keyframes WHERE media_file_id=? AND pts <= ?`, mediaFileID, t).Scan(&kf)
	if err != nil {
		return 0, err
	}
	if !kf.Valid {
		return 0, nil
	}
	return kf.Float64, nil
}

type CacheEntry struct {
	CacheKey   string
	Path       string
	Kind       string
	Bytes      int64
	LastAccess int64
	Hits       int64
}

func (s *Store) GetCacheEntry(key string) (*CacheEntry, error) {
	row := s.db.QueryRow(`SELECT cache_key, path, kind, bytes, last_access, hits FROM cache_entries WHERE cache_key=?`, key)
	var e CacheEntry
	err := row.Scan(&e.CacheKey, &e.Path, &e.Kind, &e.Bytes, &e.LastAccess, &e.Hits)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) TouchCache(key string) error {
	_, err := s.db.Exec(`UPDATE cache_entries SET last_access=?, hits=hits+1 WHERE cache_key=?`, nowMillis(), key)
	return err
}

func (s *Store) UpsertCacheEntry(key, path, kind string, bytes int64) error {
	_, err := s.db.Exec(`INSERT INTO cache_entries (cache_key, path, kind, bytes, last_access, hits) VALUES (?,?,?,?,?,0)
		ON CONFLICT(cache_key) DO UPDATE SET path=excluded.path, kind=excluded.kind, bytes=excluded.bytes, last_access=excluded.last_access`,
		key, path, kind, bytes, nowMillis())
	return err
}

func (s *Store) DeleteCacheEntry(key string) error {
	_, err := s.db.Exec(`DELETE FROM cache_entries WHERE cache_key=?`, key)
	return err
}

// CacheBytes returns the total bytes stored for the given kinds.
func (s *Store) CacheBytes(kinds ...string) (int64, error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, 0, len(kinds))
	for _, k := range kinds {
		args = append(args, k)
	}
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM cache_entries WHERE kind IN (`+ph+`)`, args...).Scan(&n)
	return n, err
}

// OldestCacheEntries lists entries for kinds ordered by least-recent access.
func (s *Store) OldestCacheEntries(kinds ...string) ([]CacheEntry, error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, 0, len(kinds))
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := s.db.Query(`SELECT cache_key, path, kind, bytes, last_access, hits FROM cache_entries WHERE kind IN (`+ph+`) ORDER BY last_access ASC, rowid ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheEntry
	for rows.Next() {
		var e CacheEntry
		if err := rows.Scan(&e.CacheKey, &e.Path, &e.Kind, &e.Bytes, &e.LastAccess, &e.Hits); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetChunkByKey resolves a chunk identity "mediaId:startIdx-endIdx".
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
func (s *Store) TitleMatches(q string, limit int) ([]MediaFile, error) {
	like := "%" + q + "%"
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files
		WHERE (title LIKE ? OR show_title LIKE ?) AND status IN ('indexed','no_subs') ORDER BY title LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
