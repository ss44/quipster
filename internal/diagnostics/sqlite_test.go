package diagnostics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quipster/internal/store"
)

func openSQLiteTestStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return st
}

func TestWriterPragmasMatchConnection(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "private.db"), ":memory:"} {
		t.Run(path, func(t *testing.T) {
			st := openSQLiteTestStore(t, path)
			if _, err := st.DB().Exec("PRAGMA busy_timeout=1234"); err != nil {
				t.Fatal(err)
			}
			sample := sampleSQLite(context.Background(), st)
			if !sample.Writer.Available || sample.Writer.BusyTimeoutMS != 1234 || sample.SampledAt == 0 {
				t.Fatalf("sample: %+v", sample)
			}
			if path == ":memory:" && sample.Writer.JournalMode != "memory" {
				t.Fatalf("memory sample: %+v", sample)
			}
			if path != ":memory:" && sample.Writer.JournalMode != "wal" {
				t.Fatalf("file sample: %+v", sample)
			}
			assertWriterPragmas(t, st, sample.Writer)
		})
	}
}

func assertWriterPragmas(t *testing.T, st *store.Store, sample sqlitePragmas) {
	t.Helper()
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	checks := []struct {
		query string
		want  int64
	}{
		{"PRAGMA busy_timeout", sample.BusyTimeoutMS},
		{"PRAGMA foreign_keys", sample.ForeignKeys},
		{"PRAGMA synchronous", sample.Synchronous},
		{"PRAGMA journal_size_limit", sample.JournalSizeLimit},
	}
	for _, check := range checks {
		var value int64
		if queryErr := conn.QueryRowContext(context.Background(), check.query).Scan(&value); queryErr != nil {
			t.Fatal(queryErr)
		}
		if value != check.want {
			t.Errorf("%s = %d, sampled %d", check.query, value, check.want)
		}
	}
}

func TestWriterBusyTimeoutAndStats(t *testing.T) {
	st := openSQLiteTestStore(t, filepath.Join(t.TempDir(), "private.db"))
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	before := writerPoolStats(st).Writer
	start := time.Now()
	sample := sampleSQLite(context.Background(), st)
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("elapsed %v", elapsed)
	}
	if sample.Writer.Available || sample.Writer.Reason != "timeout" {
		t.Fatalf("sample %+v", sample)
	}
	after := writerPoolStats(st).Writer
	if after.WaitCount <= before.WaitCount || after.WaitDurationNS <= before.WaitDurationNS || after.InUse != 1 {
		t.Fatalf("before %+v after %+v", before, after)
	}
	for i := 0; i < 100; i++ {
		_ = writerPoolStats(st)
	}
	if writerPoolStats(st).Writer.WaitCount != after.WaitCount {
		t.Fatal("pool stats acquired a connection")
	}
	data, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private.db") || strings.Contains(string(data), "PRAGMA") {
		t.Fatalf("unsafe sample %s", data)
	}
}

func TestWriterUnavailable(t *testing.T) {
	if got := sampleSQLite(context.Background(), nil); got.Writer.Reason != "store_unavailable" {
		t.Fatalf("nil store: %+v", got)
	}
	if got := sampleSQLite(context.Background(), &store.Store{}); got.Writer.Reason != "store_unavailable" {
		t.Fatalf("missing DB: %+v", got)
	}
	if writerPoolStats(nil).Writer.Available || writerPoolStats(&store.Store{}).Writer.Available {
		t.Fatal("missing pool available")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := sampleSQLite(ctx, st); got.Writer.Reason != "canceled" {
		t.Fatalf("canceled: %+v", got)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := sampleSQLite(context.Background(), st); got.Writer.Reason != "unavailable" {
		t.Fatalf("closed: %+v", got)
	}
}

func TestStreamDoesNotSamplePragmas(t *testing.T) {
	c, st := newTestCollector(t)
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	before := writerPoolStats(st).Writer.WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	c.StreamHandler()(rr, req)
	if !strings.Contains(rr.Body.String(), "event: tick") {
		t.Fatalf("stream: %s", rr.Body.String())
	}
	if after := writerPoolStats(st).Writer.WaitCount; after != before {
		t.Fatalf("stream acquired writer: %d waits", after-before)
	}
}
