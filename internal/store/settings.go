package store

import (
	"database/sql"
	"encoding/json"
	"strings"
)

const (
	SettingLibraryPaths = "library_paths"
	SettingQueuePaused  = "queue_paused"
)

func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetLibraryPaths returns configured scan folders (relative to MEDIA_DIR).
func (s *Store) GetLibraryPaths() ([]string, error) {
	v, ok, err := s.GetSetting(SettingLibraryPaths)
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return nil, err
	}
	var paths []string
	if err := json.Unmarshal([]byte(v), &paths); err != nil {
		return nil, err
	}
	return paths, nil
}

// GetQueuePaused reports whether workers should stop claiming jobs.
func (s *Store) GetQueuePaused() (bool, error) {
	v, ok, err := s.GetSetting(SettingQueuePaused)
	if err != nil || !ok {
		return false, err
	}
	return v == "1" || v == "true", nil
}

func (s *Store) SetQueuePaused(paused bool) error {
	value := "0"
	if paused {
		value = "1"
	}
	return s.SetSetting(SettingQueuePaused, value)
}

func (s *Store) SetLibraryPaths(paths []string) error {
	if paths == nil {
		paths = []string{}
	}
	b, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	return s.SetSetting(SettingLibraryPaths, string(b))
}

// Recover resets jobs that were mid-flight when the process died so they can be retried.
func (s *Store) Recover() error {
	_, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='' WHERE status='running'`)
	return err
}
