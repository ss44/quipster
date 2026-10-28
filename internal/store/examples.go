package store

import (
	"database/sql"
	"sort"
)

// ValidExamples filters a list of candidate strings, returning only those that exist
// as non-empty text in chunks belonging to indexed media files. It ranks them by the
// number of matching chunks (popularity).
func (s *Store) ValidExamples(candidates []string) ([]string, error) {
	type candidateMatch struct {
		text  string
		count int
	}
	var matches []candidateMatch

	for _, q := range candidates {
		var count int
		// We use LIKE to do case-insensitive substring match.
		err := s.db.QueryRow(`
			SELECT COUNT(c.id) 
			FROM chunks c 
			JOIN media_files m ON c.media_file_id = m.id
			WHERE m.status = 'indexed'
			  AND TRIM(c.text) != ''
			  AND c.text LIKE ?
		`, "%"+q+"%").Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if count > 0 {
			matches = append(matches, candidateMatch{text: q, count: count})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].count > matches[j].count
	})

	var result []string
	for _, m := range matches {
		result = append(result, m.text)
	}
	return result, nil
}
