package store

import "strings"

// likePattern converts a literal string into a LIKE pattern matching it as a
// substring, escaping the LIKE wildcards so candidates are always matched as
// plain text rather than interpreted as patterns.
func likePattern(text string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

// PopularExamples returns the candidates that resolve to real indexed
// dialog, ranked by how many indexed chunks contain each line. A candidate
// only survives when a non-blank chunk with live search coverage
// (indexed_at != 0) belongs to a media file in the indexed state, so the
// landing page never offers a shortcut that would come back empty. It never
// returns nil.
func (s *Store) PopularExamples(candidates []string) ([]string, error) {
	out := []string{}
	if len(candidates) == 0 {
		return out, nil
	}
	// A single CTE pass filters, counts, ranks and orders every candidate in
	// SQL, instead of one COUNT query per candidate plus a Go-side sort.
	placeholders := strings.TrimSuffix(strings.Repeat("(?, ?),", len(candidates)), ",")
	args := make([]any, 0, len(candidates)*2)
	for _, c := range candidates {
		args = append(args, c, likePattern(c))
	}
	//nolint:gosec // the concatenated part is a fixed "(?, ?)," placeholder list sized by len(candidates); values are parameterized
	q := `WITH candidates(text, pattern) AS (VALUES ` + placeholders + `)
		SELECT k.text
		FROM candidates k
		JOIN chunks c ON c.text LIKE k.pattern ESCAPE '\'
		JOIN media_files m ON m.id = c.media_file_id
		WHERE m.status = 'indexed' AND c.indexed_at != 0 AND TRIM(c.text) != ''
		GROUP BY k.text
		ORDER BY COUNT(*) DESC, k.text`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, rows.Err()
}
