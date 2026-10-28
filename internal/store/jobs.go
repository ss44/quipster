package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// Job is one row of the processing queue, optionally enriched with media
// metadata for display.
type Job struct {
	MediaTitle  string  `json:"mediaTitle"`
	Type        string  `json:"type"`
	Payload     string  `json:"payload"`
	Status      string  `json:"status"`
	MediaStatus string  `json:"mediaStatus"`
	MediaPath   string  `json:"mediaPath"`
	WorkerID    string  `json:"workerId"`
	Error       string  `json:"error"`
	Progress    float64 `json:"progress"`
	CreatedAt   int64   `json:"createdAt"`
	StartedAt   int64   `json:"startedAt"`
	FinishedAt  int64   `json:"finishedAt"`
	MediaID     int64   `json:"mediaId"`
	ID          int64   `json:"id"`
	Attempts    int     `json:"attempts"`
	Priority    int     `json:"priority"`
	MediaCues   int64   `json:"mediaCues"`
	MediaChunks int64   `json:"mediaChunks"`
}

// JobSpec describes one queued job for EnqueueJobs.
type JobSpec struct {
	Type     string
	Payload  string
	Priority int
}

// EnqueueJob inserts a new queued job and returns its id.
func (s *Store) EnqueueJob(jobType, payload string, priority int) (int64, error) {
	ids, err := s.EnqueueJobs([]JobSpec{{Type: jobType, Payload: payload, Priority: priority}})
	if err != nil {
		return 0, err
	}
	if len(ids) != 1 {
		return 0, errors.New("store: enqueue returned no job id")
	}
	return ids[0], nil
}

// EnqueueJobs inserts many queued jobs in one transaction. The scanner uses
// this to hand off thousands of probes after a walk without paying one commit
// (and one fsync) per file.
func (s *Store) EnqueueJobs(specs []JobSpec) ([]int64, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer rollbackTx(tx)
	stmt, err := tx.Prepare(`INSERT INTO jobs (type, payload, priority, status, created_at) VALUES (?,?,?,'queued',?)`)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(specs))
	for i, spec := range specs {
		res, err := stmt.Exec(spec.Type, spec.Payload, spec.Priority, nowMillis())
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// EnqueueUniqueJobs inserts jobs that do not already have an identical
// queued/running job. The scanner uses it to recover thousands of pending
// probes in one transaction while still deduplicating against work already in
// flight. It returns the number of rows actually inserted.
func (s *Store) EnqueueUniqueJobs(specs []JobSpec) (int, error) {
	if len(specs) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer rollbackTx(tx)
	stmt, err := tx.Prepare(`INSERT INTO jobs (type, payload, priority, status, created_at)
		SELECT ?,?,?,'queued',?
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE type=? AND payload=? AND status IN ('queued','running')
		)`)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, spec := range specs {
		now := nowMillis()
		res, err := stmt.Exec(spec.Type, spec.Payload, spec.Priority, now, spec.Type, spec.Payload)
		if err != nil {
			return created, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return created, err
		}
		created += int(n)
	}
	if err := tx.Commit(); err != nil {
		return created, err
	}
	return created, nil
}

// ClaimJob atomically claims the next queued job of the given types using a
// single BEGIN IMMEDIATE transaction with RETURNING.
func (s *Store) ClaimJob(ctx context.Context, workerID string, types []string) (*Job, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			log.Printf("store: close job conn: %v", cerr)
		}
	}()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	rollback := func() {
		if _, rbErr := conn.ExecContext(ctx, "ROLLBACK"); rbErr != nil {
			log.Printf("store: job rollback: %v", rbErr)
		}
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")
	args := []any{workerID, nowMillis()}
	for _, t := range types {
		args = append(args, t)
	}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length types slice; values are parameterized.
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

// FinishJob marks a job done or errored, latching full progress on success.
func (s *Store) FinishJob(id int64, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?, error=?, progress=CASE WHEN ?='done' THEN 1 ELSE progress END, finished_at=? WHERE id=?`,
		status, errMsg, status, nowMillis(), id)
	return err
}

// SetJobProgress updates the progress fraction for a running job.
func (s *Store) SetJobProgress(id int64, progress float64) error {
	_, err := s.db.Exec(`UPDATE jobs SET progress=? WHERE id=?`, progress, id)
	return err
}

// RequeueJob returns a failed job to the queue for another attempt.
func (s *Store) RequeueJob(id int64, errMsg string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='', error=? WHERE id=?`, errMsg, id)
	return err
}

// RequeueJobSoft returns a paused/canceled job to the queue without counting
// the interrupted attempt against its retry budget.
func (s *Store) RequeueJobSoft(id int64, note string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='', error=?,
		attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END WHERE id=? AND status='running'`, note, id)
	return err
}

// ClearQueuedJobs removes all queued jobs and returns how many were removed.
func (s *Store) ClearQueuedJobs() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM jobs WHERE status='queued'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// JobStats summarizes the queue state: per-status counts plus whether the
// queue is paused.
type JobStats struct {
	Queued  int64 `json:"queued"`
	Running int64 `json:"running"`
	Done    int64 `json:"done"`
	Error   int64 `json:"error"`
	Paused  bool  `json:"paused"`
}

// JobStats returns per-status job counts and whether the queue is paused.
func (s *Store) JobStats() (*JobStats, error) {
	st := &JobStats{}
	if paused, err := s.GetQueuePaused(); err == nil {
		st.Paused = paused
	}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
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

// ListJobs lists the most recent jobs with media metadata attached.
func (s *Store) ListJobs(limit int) ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out, err := scanJobs(rows)
	if err != nil {
		return nil, err
	}
	return s.enrichJobs(out), nil
}

// RunningJobs lists currently executing jobs with media metadata attached.
func (s *Store) RunningJobs(limit int) ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs WHERE status='running' ORDER BY started_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out, err := scanJobs(rows)
	if err != nil {
		return nil, err
	}
	return s.enrichJobs(out), nil
}

// FinishedJobsSince lists completed jobs finished at or after since (unix
// milliseconds), newest first. It is the bounded input for diagnostics
// benchmark aggregation.
func (s *Store) FinishedJobsSince(since int64, limit int) ([]Job, error) {
	if limit <= 0 || limit > 20000 {
		limit = 20000
	}
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs WHERE status='done' AND finished_at>=? ORDER BY finished_at DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// MediaSizes returns the size in bytes for each requested media id. Missing ids
// are omitted from the result.
func (s *Store) MediaSizes(ids []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length ids slice; values are parameterized.
	rows, err := s.db.Query(`SELECT id, size_bytes FROM media_files WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			return nil, err
		}
		out[id] = size
	}
	return out, rows.Err()
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	defer closeRows(rows)
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

// mediaIDFromPayload extracts the media id from a job payload; invalid or
// missing payloads yield 0 (best-effort enrichment only).
func mediaIDFromPayload(payload string) int64 {
	var p struct {
		MediaID int64 `json:"mediaId"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return 0
	}
	return p.MediaID
}

// enrichJobs attaches media title/path/status and cue/chunk counts to jobs.
func (s *Store) enrichJobs(jobs []Job) []Job {
	if len(jobs) == 0 {
		return jobs
	}
	idSet := map[int64]bool{}
	for i := range jobs {
		jobs[i].MediaID = mediaIDFromPayload(jobs[i].Payload)
		if jobs[i].MediaID > 0 {
			idSet[jobs[i].MediaID] = true
		}
	}
	if len(idSet) == 0 {
		return jobs
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	media := s.loadMediaMeta(ph, args)
	cues := s.loadCountsByMedia("cues", ph, args)
	chunks := s.loadCountsByMedia("chunks", ph, args)

	for i := range jobs {
		if m, ok := media[jobs[i].MediaID]; ok {
			jobs[i].MediaTitle = displayName(&m)
			jobs[i].MediaPath = m.Path
			jobs[i].MediaStatus = m.Status
		}
		jobs[i].MediaCues = cues[jobs[i].MediaID]
		jobs[i].MediaChunks = chunks[jobs[i].MediaID]
	}
	return jobs
}

// loadMediaMeta loads media rows for the given ids, keyed by id. Failures are
// swallowed: enrichment is best-effort and must never fail listing.
func (s *Store) loadMediaMeta(ph string, args []any) map[int64]MediaFile {
	media := map[int64]MediaFile{}
	//nolint:gosec // the concatenated part is a "?" placeholder list built from the fixed-length ids slice; values are parameterized.
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return media
	}
	defer closeRows(rows)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil || m == nil {
			continue
		}
		media[m.ID] = *m
	}
	return media
}

// loadCountsByMedia counts rows per media_file_id in the given table for the
// given ids. Failures are swallowed: enrichment is best-effort.
func (s *Store) loadCountsByMedia(table, ph string, args []any) map[int64]int64 {
	out := map[int64]int64{}
	//nolint:gosec // the concatenated table name comes from the fixed internal allowlist ("cues"/"chunks") and the rest is a "?" placeholder list; values are parameterized.
	rows, err := s.db.Query(`SELECT media_file_id, COUNT(*) FROM `+table+` WHERE media_file_id IN (`+ph+`) GROUP BY media_file_id`, args...)
	if err != nil {
		return out
	}
	defer closeRows(rows)
	for rows.Next() {
		var mediaID, n int64
		if err := rows.Scan(&mediaID, &n); err != nil {
			continue
		}
		out[mediaID] = n
	}
	return out
}

func displayName(m *MediaFile) string {
	if m.ShowTitle != "" {
		if m.Season > 0 && m.Episode > 0 {
			return fmt.Sprintf("%s S%02dE%02d", m.ShowTitle, m.Season, m.Episode)
		}
		return m.ShowTitle
	}
	if m.Title != "" {
		if m.Year > 0 {
			return fmt.Sprintf("%s (%d)", m.Title, m.Year)
		}
		return m.Title
	}
	return filepath.Base(m.Path)
}

// GetJob returns one job by id, enriched with media metadata like ListJobs,
// or nil when the id is unknown.
func (s *Store) GetJob(jobID int64) (*Job, error) {
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs WHERE id=?`, jobID)
	if err != nil {
		return nil, err
	}
	jobs, err := scanJobs(rows)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	return &s.enrichJobs(jobs)[0], nil
}

// LineStats summarizes how much dialog has been digested and indexed.
type LineStats struct {
	Cues          int64 `json:"cues"`
	Chunks        int64 `json:"chunks"`
	ChunksIndexed int64 `json:"chunksIndexed"`
}

// LineStats returns total cue/chunk counts plus how many chunks are indexed.
func (s *Store) LineStats() (*LineStats, error) {
	st := &LineStats{}
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM cues),
		(SELECT COUNT(*) FROM chunks),
		(SELECT COUNT(*) FROM chunks WHERE indexed_at > 0)`).Scan(&st.Cues, &st.Chunks, &st.ChunksIndexed)
	return st, err
}

// EnqueueUniqueJob enqueues a job if one with the same type and payload
// isn't already queued or running. It returns the job ID and a boolean
// indicating if it was newly enqueued.
func (s *Store) EnqueueUniqueJob(jobType, payload string, priority int) (int64, bool, error) {
	var existingID int64
	err := s.db.QueryRow(`SELECT id FROM jobs WHERE type=? AND payload=? AND status IN ('queued', 'running') LIMIT 1`, jobType, payload).Scan(&existingID)
	if err == nil {
		return existingID, false, nil // already exists
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	id, err := s.EnqueueJob(jobType, payload, priority)
	return id, true, err
}
