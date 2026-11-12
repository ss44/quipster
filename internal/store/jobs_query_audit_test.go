package store

import (
	"strings"
	"testing"
)

func TestFinishedJobsSinceUsesBoundedTimeRange(t *testing.T) {
	s := openTest(t)
	plan := auditPlan(t, s, auditFinishedJobsQuery, 199000, 100)
	if !strings.Contains(plan, "idx_jobs_done_finished") || !strings.Contains(plan, "finished_at>?") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("finished-job diagnostics must seek the done time range without sorting all completed jobs: %s", plan)
	}
}

const auditFinishedJobsQuery = `SELECT ` + jobColumns + ` FROM jobs WHERE status='done' AND finished_at>=? ORDER BY finished_at DESC LIMIT ?`
const auditFinishedJobsIndex = `CREATE INDEX idx_jobs_done_finished ON jobs(status,finished_at) WHERE status='done'`

func auditJobsFixture(tb testing.TB) *Store {
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
	// Remove the proposed index, if migrated, to retain a true pre-change baseline.
	if _, err := s.DB().Exec(`DROP INDEX IF EXISTS idx_jobs_done_finished`); err != nil {
		tb.Fatal(err)
	}
	for base := 0; base < 200000; base += 1000 {
		_, err := s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO jobs(id,type,status,finished_at) SELECT x+?,CASE WHEN x%3=0 THEN 'index' ELSE 'probe' END,CASE WHEN x%20=0 THEN 'queued' ELSE 'done' END,x+? FROM n`, base, base)
		if err != nil {
			tb.Fatal(err)
		}
	}
	return s
}

func BenchmarkFinishedJobsQueryAudit(b *testing.B) {
	s := auditJobsFixture(b)
	for _, indexed := range []bool{false, true} {
		name := "legacy"
		if indexed {
			var before, after int
			if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&before); err != nil {
				b.Fatal(err)
			}
			if _, err := s.DB().Exec(auditFinishedJobsIndex); err != nil {
				b.Fatal(err)
			}
			if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&after); err != nil {
				b.Fatal(err)
			}
			b.Logf("partial index added %d database pages", after-before)
			name = "partial_index"
		}
		b.Run(name, func(b *testing.B) {
			b.Log(auditPlan(b, s, auditFinishedJobsQuery, 199000, 100))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				jobs, err := s.FinishedJobsSince(199000, 100)
				if err != nil {
					b.Fatal(err)
				}
				if len(jobs) != 100 {
					b.Fatalf("got %d jobs", len(jobs))
				}
			}
		})
	}
}

func BenchmarkFinishedJobsWriteCostAudit(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		name := "legacy"
		if indexed {
			name = "partial_index"
		}
		b.Run(name, func(b *testing.B) {
			s := auditJobsFixture(b)
			if indexed {
				if _, err := s.DB().Exec(auditFinishedJobsIndex); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if _, err := s.DB().Exec(`UPDATE jobs SET status='queued',finished_at=0 WHERE id<=500`); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := s.DB().Exec(`UPDATE jobs SET status='done',finished_at=200000 WHERE id<=500`); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
