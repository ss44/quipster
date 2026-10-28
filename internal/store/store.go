// Package store provides the SQLite-backed persistence layer for Quipster:
// media files, subtitle tracks and cues, dialog chunks, scan progress, the
// byte-bounded cache, queue jobs, and key/value settings.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	// blank import: register the pure-Go SQLite driver.
	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database and provides all persistence operations for
// media files, jobs, subtitles, chunks, cache entries, and settings.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the SQLite database at the given path. The DSN
// enables WAL journaling, foreign keys, a busy timeout, and immediate
// transactions so concurrent readers work alongside serialized writers.
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
		// The connection never worked; surface the ping error and log the
		// close failure since the original error is the one that matters.
		if cerr := db.Close(); cerr != nil {
			log.Printf("store: close after ping failure: %v", cerr)
		}
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		if cerr := db.Close(); cerr != nil {
			log.Printf("store: close after migration failure: %v", cerr)
		}
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database connection pool.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying connection pool for callers that need raw access.
func (s *Store) DB() *sql.DB { return s.db }

// closeRows closes query rows after iteration, logging a non-nil close error
// since row data has already been consumed and the caller reports its own
// iteration/scan errors.
func closeRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		log.Printf("store: close rows: %v", err)
	}
}

// rollbackTx rolls back a transaction, tolerating the expected ErrTxDone
// sentinel that follows a successful Commit and logging any other failure.
func rollbackTx(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		log.Printf("store: tx rollback: %v", err)
	}
}

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
			start_time REAL NOT NULL DEFAULT 0,
			ever_indexed INTEGER NOT NULL DEFAULT 0,
			audio_stream_index INTEGER NOT NULL DEFAULT 0,
			audio_lang TEXT NOT NULL DEFAULT ''
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
		`CREATE INDEX IF NOT EXISTS idx_jobs_type_status_finished ON jobs(type, status, finished_at)`,
		`CREATE TABLE IF NOT EXISTS scans (
			id INTEGER PRIMARY KEY,
			root_path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running',
			files_seen INTEGER NOT NULL DEFAULT 0,
			files_done INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
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
	migrations := []struct{ name, ddl string }{
		{"ever_indexed", "INTEGER NOT NULL DEFAULT 0"},
		{"audio_stream_index", "INTEGER NOT NULL DEFAULT 0"},
		{"audio_lang", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, m := range migrations {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_files') WHERE name=?`, m.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := s.db.Exec(`ALTER TABLE media_files ADD COLUMN ` + m.name + ` ` + m.ddl); err != nil {
				return fmt.Errorf("migrate %s: %w", m.name, err)
			}
		}
	}
	return nil
}

func nowMillis() int64 { return time.Now().UnixMilli() }
