package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"quipster/internal/store"
)

func TestCleanRoots(t *testing.T) {
	cases := []struct {
		in    []string
		out   []string
		error bool
	}{
		{in: []string{"movies", "tv"}, out: []string{"movies", "tv"}, error: false},
		{in: []string{"movies/new", "tv//comedy"}, out: []string{"movies/new", "tv/comedy"}, error: false},
		{in: []string{"movies", "movies", ""}, out: []string{"movies"}, error: false},
		{in: []string{"/absolute"}, error: true},
		{in: []string{".."}, error: true},
		{in: []string{"movies/../../tv"}, error: true},
	}

	for _, c := range cases {
		out, err := CleanRoots(c.in)
		if c.error {
			if err == nil {
				t.Errorf("CleanRoots(%v) expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("CleanRoots(%v) unexpected error: %v", c.in, err)
			continue
		}
		if len(out) != len(c.out) {
			t.Errorf("CleanRoots(%v) = %v, want %v", c.in, out, c.out)
			continue
		}
		for i := range out {
			if out[i] != c.out[i] {
				t.Errorf("CleanRoots(%v) = %v, want %v", c.in, out, c.out)
				break
			}
		}
	}
}

func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "d1")
	p2 := filepath.Join(dir, "d2", "d3")
	err := EnsureDirs(p1, p2)
	if err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}
	if info, err := os.Stat(p1); err != nil || !info.IsDir() {
		t.Errorf("d1 not created properly")
	}
	if info, err := os.Stat(p2); err != nil || !info.IsDir() {
		t.Errorf("d2/d3 not created properly")
	}
}

func TestRunAndJobHandler(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("store.Close failed: %v", cerr)
		}
	})

	mediaRoot := t.TempDir()

	files := []string{
		"movies/The Matrix (1999).mkv",
		"tv/Severance/Season 1/S01E01.mkv",
		"movies/.hidden.mkv",
		"movies/extras/bts.mkv",
		"movies/image.jpg",
	}
	for _, f := range files {
		full := filepath.Join(mediaRoot, filepath.FromSlash(f))
		if merr := os.MkdirAll(filepath.Dir(full), 0o750); merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(full, []byte("fake video data"), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}

	archivedIDs := []int64{}
	onArchived := func(ids []int64) {
		archivedIDs = append(archivedIDs, ids...)
	}

	// 1. Run full scan
	res, err := Run(context.Background(), mediaRoot, []string{"movies", "tv"}, true, st, onArchived)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Seen != 2 {
		t.Errorf("Run Seen = %d, want 2 (Matrix and Severance)", res.Seen)
	}
	if res.Changed != 2 {
		t.Errorf("Run Changed = %d, want 2", res.Changed)
	}

	// 2. Remove file to trigger the archive pass. The default grace window
	// only latches an absence on the first scan, so run the handler twice with
	// a zero grace policy: first pass latches, second pass archives.
	if rerr := os.Remove(filepath.Join(mediaRoot, filepath.FromSlash("movies/The Matrix (1999).mkv"))); rerr != nil {
		t.Fatal(rerr)
	}

	handler := JobHandler(mediaRoot, st, onArchived, WithArchivePolicy(true, 0))
	job := &store.Job{
		Payload: `{"roots": ["movies"], "prune": true}`,
	}
	err = handler(context.Background(), job, func(float64) {})
	if err != nil {
		t.Fatalf("JobHandler failed: %v", err)
	}
	if len(archivedIDs) != 0 {
		t.Errorf("first missing observation must only latch, got archived ids %v", archivedIDs)
	}
	err = handler(context.Background(), job, func(float64) {})
	if err != nil {
		t.Fatalf("JobHandler second pass failed: %v", err)
	}
	if len(archivedIDs) == 0 {
		t.Errorf("Expected archived IDs after the grace window elapsed, got none")
	}

	// 3. Test with prune=false via JobHandler
	job2 := &store.Job{
		Payload: `{"roots": ["tv"], "prune": false}`,
	}
	err = handler(context.Background(), job2, func(float64) {})
	if err != nil {
		t.Fatalf("JobHandler prune=false failed: %v", err)
	}
}

func TestJobHandlerErrors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("store.Close failed: %v", cerr)
		}
	})

	handler := JobHandler(t.TempDir(), st, nil)

	jobBadJSON := &store.Job{
		Payload: `{"roots": [`,
	}
	if err := handler(context.Background(), jobBadJSON, nil); err == nil {
		t.Errorf("Expected error for bad json, got nil")
	}

	jobBadPath := &store.Job{
		Payload: `{"roots": ["missing_dir"]}`,
	}
	if err := handler(context.Background(), jobBadPath, nil); err == nil {
		t.Errorf("Expected error for missing dir, got nil")
	}

	jobBadRoots := &store.Job{
		Payload: `{"roots": [".."]}`,
	}
	if err := handler(context.Background(), jobBadRoots, nil); err == nil {
		t.Errorf("Expected error for bad roots, got nil")
	}
}
