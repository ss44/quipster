package store

import (
	"database/sql"
)

// Scan tracks the progress of a media library scan run.
type Scan struct {
	RootPath   string `json:"rootPath"`
	Status     string `json:"status"`
	ID         int64  `json:"id"`
	FilesSeen  int64  `json:"filesSeen"`
	FilesDone  int64  `json:"filesDone"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
}

// StartScan inserts a new running scan row and returns its id.
func (s *Store) StartScan(root string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO scans (root_path, status, started_at) VALUES (?,'running',?)`, root, nowMillis())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateScan records scan progress and, when a status is given, finishes the
// scan row with that status and a finished timestamp.
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

// HasActiveScanJob reports whether a scan job is queued or running.
func (s *Store) HasActiveScanJob() (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='scan' AND status IN ('queued','running')`).Scan(&n)
	return n > 0, err
}
