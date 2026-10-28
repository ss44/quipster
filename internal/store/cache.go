package store

import (
	"database/sql"
	"strings"
)

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
