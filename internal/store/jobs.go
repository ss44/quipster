package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

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

	MediaID     int64  `json:"mediaId"`
	MediaTitle  string `json:"mediaTitle"`
	MediaPath   string `json:"mediaPath"`
	MediaStatus string `json:"mediaStatus"`
	MediaCues   int64  `json:"mediaCues"`
	MediaChunks int64  `json:"mediaChunks"`
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

// RequeueJobSoft returns a paused/cancelled job to the queue without counting
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

type JobStats struct {
	Queued  int64 `json:"queued"`
	Running int64 `json:"running"`
	Done    int64 `json:"done"`
	Error   int64 `json:"error"`
	Paused  bool  `json:"paused"`
}

func (s *Store) JobStats() (*JobStats, error) {
	st := &JobStats{}
	if paused, err := s.GetQueuePaused(); err == nil {
		st.Paused = paused
	}
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

func scanJobs(rows *sql.Rows) ([]Job, error) {
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

func mediaIDFromPayload(payload string) int64 {
	var p struct {
		MediaID int64 `json:"mediaId"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
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
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	ph := strings.Join(placeholders, ",")

	media := map[int64]MediaFile{}
	rows, err := s.db.Query(`SELECT `+mediaCols+` FROM media_files WHERE id IN (`+ph+`)`, args...)
	if err == nil {
		for rows.Next() {
			if m, err := scanMedia(rows); err == nil && m != nil {
				media[m.ID] = *m
			}
		}
		rows.Close()
	}

	type count struct{ mediaID, n int64 }
	loadCounts := func(table string) map[int64]int64 {
		out := map[int64]int64{}
		rows, err := s.db.Query(`SELECT media_file_id, COUNT(*) FROM `+table+` WHERE media_file_id IN (`+ph+`) GROUP BY media_file_id`, args...)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var c count
			if err := rows.Scan(&c.mediaID, &c.n); err == nil {
				out[c.mediaID] = c.n
			}
		}
		return out
	}
	cues := loadCounts("cues")
	chunks := loadCounts("chunks")

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

// LineStats summarises how much dialogue has been digested and indexed.
type LineStats struct {
	Cues          int64 `json:"cues"`
	Chunks        int64 `json:"chunks"`
	ChunksIndexed int64 `json:"chunksIndexed"`
}

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
