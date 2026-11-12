package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"quipster/internal/store"
)

func TestReadOnlySnapshotDoesNotWaitForSQLiteOrSlowCollectorLocks(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	conn, err := st.DB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	c := &Collector{St: st, Version: "test-build", Revision: strings.Repeat("a", 40), StartedAt: time.Now().Add(-time.Hour), MeiliUp: func() bool { t.Error("unexpected probe"); return false }}
	c.mu.Lock()
	c.systemMu.Lock()
	defer c.mu.Unlock()
	defer c.systemMu.Unlock()
	result := make(chan ReadSnapshot, 1)
	go func() { result <- c.ReadOnlySnapshot() }()
	select {
	case out := <-result:
		if !out.Available || !out.SQLitePools.Writer.Available || out.SQLitePools.Writer.InUse != 1 {
			t.Fatalf("missing in-memory pool sample: %+v", out)
		}
		if out.System.AppRevision != strings.Repeat("a", 40) || out.System.AppUptimeS < 3590 {
			t.Fatalf("system identity: %+v", out.System)
		}
		if out.System.Goroutines <= 0 || out.System.CPUCores <= 0 || out.At == 0 {
			t.Fatalf("runtime counters missing: %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("read-only diagnostic blocked on SQLite or Gauge/System lock")
	}
}

func TestReadOnlySnapshotNilAndOversizedVersion(t *testing.T) {
	var missing *Collector
	if out := missing.ReadOnlySnapshot(); out.Available || out.Reason != "collector_unavailable" {
		t.Fatalf("nil collector: %+v", out)
	}
	c := &Collector{Version: strings.Repeat("x", 1<<20), StartedAt: time.Now().Add(time.Hour)}
	out := c.ReadOnlySnapshot()
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 16<<10 || out.System.AppVersion != "[value omitted]" || out.System.AppUptimeS != 0 {
		t.Fatalf("unbounded/invalid snapshot: bytes=%d uptime=%d", len(data), out.System.AppUptimeS)
	}
}
