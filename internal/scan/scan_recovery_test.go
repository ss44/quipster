package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	return st
}

func writeMediaTree(t *testing.T, root string, count int) {
	t.Helper()
	dir := filepath.Join(root, "movies")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		path := filepath.Join(dir, fmt.Sprintf("Movie %04d (2020).mkv", i))
		if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

//nolint:gocyclo // two full scan passes with independent assertions
func TestRunBatchesLargeTreeAndEnqueuesProbes(t *testing.T) {
	st := openTestStore(t)
	root := t.TempDir()
	writeMediaTree(t, root, 550)

	res, err := Run(context.Background(), root, []string{"movies"}, false, st, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Seen != 550 || res.Changed != 550 || res.Probes != 550 {
		t.Fatalf("first scan = %+v, want 550 seen/changed/probes", res)
	}
	if res.Errors != 0 || res.WalkErrors != 0 {
		t.Fatalf("first scan errors = %+v", res)
	}
	if res.Walked < 550 {
		t.Fatalf("Walked = %d, want at least 550", res.Walked)
	}

	var mediaCount, probeJobs int64
	if queryErr := st.DB().QueryRow(`SELECT COUNT(*) FROM media_files`).Scan(&mediaCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='probe'`).Scan(&probeJobs); queryErr != nil {
		t.Fatal(queryErr)
	}
	if mediaCount != 550 || probeJobs != 550 {
		t.Fatalf("rows: media=%d probe jobs=%d, want 550/550", mediaCount, probeJobs)
	}

	// A second walk must detect no changes and must not multiply the probe
	// queue; pending-probe recovery deduplicates against the queued jobs.
	res2, err := Run(context.Background(), root, []string{"movies"}, false, st, nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res2.Seen != 550 || res2.Changed != 0 || res2.Probes != 0 {
		t.Fatalf("second scan = %+v, want 550 seen and no changes", res2)
	}
	if queryErr := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='probe'`).Scan(&probeJobs); queryErr != nil {
		t.Fatal(queryErr)
	}
	if probeJobs != 550 {
		t.Fatalf("probe jobs after second scan = %d, want 550", probeJobs)
	}
}

func TestPruneSkipsIncompleteWalk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode bits")
	}
	st := openTestStore(t)
	root := t.TempDir()
	locked := filepath.Join(root, "movies", "locked")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o750); err != nil { //nolint:gosec // test cleanup restores a directory mode
			t.Logf("restore locked dir mode: %v", err)
		}
	})
	missingPath := filepath.Join(locked, "keep.mkv")
	id, _, err := st.UpsertMedia(missingPath, "mkv", 1, 1, "Keep", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if chmodErr := os.Chmod(locked, 0o000); chmodErr != nil {
		t.Fatal(chmodErr)
	}

	res, err := Run(context.Background(), root, []string{"movies"}, true, st, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.WalkErrors == 0 {
		t.Fatalf("expected an unreadable-directory walk error: %+v", res)
	}
	if res.Archived != 0 {
		t.Fatalf("incomplete walk archived %d rows", res.Archived)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("incomplete walk removed a media row")
	}
	if m.Archived {
		t.Fatal("incomplete walk archived a media row")
	}
}

func TestPruneOrLogSkipsOnWalkErrors(t *testing.T) {
	st := openTestStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "movies", "keep.mkv")
	id, _, err := st.UpsertMedia(path, "mkv", 1, 1, "Keep", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	res := &Result{WalkErrors: 1}
	archiveOrLog(st, root, []string{"movies"}, map[string]bool{}, true, res, 1,
		Options{ArchiveEnabled: true, ArchiveGrace: 0, now: func() time.Time { return time.Now() }}, nil)
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("archiveOrLog removed a row despite walk errors")
	}
	if m.Archived {
		t.Fatal("archiveOrLog archived a row despite walk errors")
	}
}

func TestRunCanceledContextMarksScanError(t *testing.T) {
	st := openTestStore(t)
	root := t.TempDir()
	writeMediaTree(t, root, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := Run(ctx, root, []string{"movies"}, false, st, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if res == nil || res.ScanID == 0 {
		t.Fatalf("Run result = %+v, want a scan id", res)
	}
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM scans WHERE id=?`, res.ScanID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("scan status = %q, want error", status)
	}
	if running, err := st.RunningScan(); err != nil {
		t.Fatal(err)
	} else if running != nil {
		t.Fatalf("canceled scan is still running: %+v", running)
	}
}
