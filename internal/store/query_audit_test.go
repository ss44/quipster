package store

import (
	"strings"
	"testing"
)

const auditLegacyPending = `SELECT ` + chunkJoinCols + ` FROM chunks c JOIN media_files m ON m.id=c.media_file_id WHERE c.indexed_at=0 AND m.archived=0 AND c.media_file_id=? ORDER BY c.media_file_id,c.start_cue_idx LIMIT ?`
const auditScopedPending = `SELECT ` + chunkJoinCols + ` FROM chunks c JOIN media_files m ON m.id=c.media_file_id WHERE c.media_file_id=? AND c.indexed_at=0 AND m.archived=0 ORDER BY c.start_cue_idx LIMIT ?`

// auditFixture models a global reindex backlog and a pathological 85k-cue title.
// Setup is synthetic, local, bounded to 1000 rows per statement, and untimed.
func auditFixture(tb testing.TB, mediaCount int) *Store {
	tb.Helper()
	s, err := Open(":memory:")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			tb.Error(closeErr)
		}
	})
	for base := 0; base < mediaCount; base += 1000 {
		n := min(1000, mediaCount-base)
		_, err = s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO media_files(id,path) SELECT x+?,printf('/synthetic/%d.mkv',x+?) FROM n`, n, base, base)
		if err != nil {
			tb.Fatal(err)
		}
	}
	for id := 1; id <= mediaCount; id++ {
		_, err = s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO chunks(media_file_id,start_cue_idx,end_cue_idx,text) SELECT ?,x*3,x*3+2,'A realistic synthetic subtitle line.' FROM n`, id)
		if err != nil {
			tb.Fatal(err)
		}
	}
	_, err = s.DB().Exec(`INSERT INTO subtitle_tracks(id,media_file_id,source) VALUES(1,?,'synthetic')`, mediaCount)
	if err != nil {
		tb.Fatal(err)
	}
	for base := 0; base < 85000; base += 1000 {
		_, err = s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO cues(media_file_id,track_id,idx,start_sec,end_sec,text) SELECT ?,1,x+?,(x+?)*1.2,(x+?)*1.2+2,'synthetic cue' FROM n`, mediaCount, base, base, base)
		if err != nil {
			tb.Fatal(err)
		}
	}
	return s
}

func auditPlan(tb testing.TB, s *Store, q string, args ...any) string {
	tb.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		tb.Fatal(err)
	}
	defer closeRows(rows)
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return strings.Join(out, "; ")
}

func BenchmarkPendingIndexQueryAudit(b *testing.B) {
	s := auditFixture(b, 1001)
	b.Log("unanalyzed", auditPlan(b, s, auditLegacyPending, 1001, 100))
	if _, err := s.DB().Exec(`ANALYZE`); err != nil {
		b.Fatal(err)
	}
	b.Log("analyzed", auditPlan(b, s, auditLegacyPending, 1001, 100))
	b.Log("forced_pending_index", auditPlan(b, s, strings.Replace(auditLegacyPending, "FROM chunks c JOIN", "FROM chunks c INDEXED BY idx_chunks_indexed JOIN", 1), 1001, 100))
	b.Log("global", auditPlan(b, s, `SELECT `+chunkJoinCols+` FROM chunks c JOIN media_files m ON m.id=c.media_file_id WHERE c.indexed_at=0 AND m.archived=0 ORDER BY c.media_file_id,c.start_cue_idx LIMIT ?`, 100))
	for name, q := range map[string]string{"legacy": auditLegacyPending, "scoped": auditScopedPending} {
		b.Run(name, func(b *testing.B) {
			b.Log(auditPlan(b, s, q, 1001, 100))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := s.db.Query(q, 1001, 100)
				if err != nil {
					b.Fatal(err)
				}
				count := 0
				for rows.Next() {
					count++
				}
				err = rows.Err()
				closeRows(rows)
				if err != nil {
					b.Fatal(err)
				}
				if count != 100 {
					b.Fatalf("got %d chunks", count)
				}
			}
		})
	}
}

func TestLoadIndexSnapshotChunkQueryIsMediaScoped(t *testing.T) {
	s := openTest(t)
	// Keep this identical to LoadIndexSnapshot's full replacement query, not
	// the pending-only predecessor: already-latched chunks must be read too.
	q := `SELECT ` + chunkJoinCols + ` FROM chunks c JOIN media_files m ON m.id=c.media_file_id WHERE c.media_file_id=? ORDER BY c.start_cue_idx, c.id`
	plan := auditPlan(t, s, q, 20)
	t.Log(plan)
	for _, detail := range strings.Split(plan, "; ") {
		// Either existing media-scoped index is valid; the constrained column
		// matters, not the chosen index name or an unrelated cue lookup.
		if strings.Contains(detail, "SEARCH c USING ") && strings.Contains(detail, "media_file_id=?") {
			return
		}
	}
	t.Fatalf("snapshot chunks must seek media_file_id, not scan the global backlog: %s", plan)
}

func TestQueryAuditPlans(t *testing.T) {
	s := auditFixture(t, 20)
	for name, q := range map[string]string{"legacy": auditLegacyPending, "scoped": auditScopedPending} {
		t.Log(name, auditPlan(t, s, q, 20, 100))
	}
	for _, q := range []string{
		`SELECT status,COUNT(*) FROM jobs GROUP BY status`,
		`SELECT status,COUNT(*) FROM media_files WHERE archived=0 GROUP BY status`,
		`SELECT ` + jobColumns + ` FROM jobs WHERE status='running' ORDER BY started_at ASC LIMIT 100`,
		`SELECT ` + jobColumns + ` FROM jobs ORDER BY id DESC LIMIT 100`,
		`SELECT COUNT(*) FROM chunks WHERE indexed_at>0`,
		`SELECT ` + jobColumns + ` FROM jobs WHERE status='done' AND finished_at>=? ORDER BY finished_at DESC LIMIT ?`,
		`SELECT media_file_id,COUNT(*) FROM cues WHERE media_file_id IN (?,?) GROUP BY media_file_id`,
	} {
		args := []any{}
		if strings.Contains(q, "?") {
			args = []any{19, 20}
		}
		t.Log(auditPlan(t, s, q, args...))
	}
}
