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
	defer st.Close()

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
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte("fake video data"), 0644)
	}

	deletedIds := []int64{}
	onDeleted := func(ids []int64) {
		deletedIds = append(deletedIds, ids...)
	}

	// 1. Run full scan
	res, err := Run(context.Background(), mediaRoot, []string{"movies", "tv"}, true, st, onDeleted)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Seen != 2 {
		t.Errorf("Run Seen = %d, want 2 (Matrix and Severance)", res.Seen)
	}
	if res.Changed != 2 {
		t.Errorf("Run Changed = %d, want 2", res.Changed)
	}

	// 2. Remove file to trigger prune logic
	os.Remove(filepath.Join(mediaRoot, filepath.FromSlash("movies/The Matrix (1999).mkv")))

	// Prune=true via JobHandler
	handler := JobHandler(mediaRoot, st, onDeleted)
	job := &store.Job{
		Payload: `{"roots": ["movies"], "prune": true}`,
	}
	err = handler(context.Background(), job, func(float64) {})
	if err != nil {
		t.Fatalf("JobHandler failed: %v", err)
	}

	if len(deletedIds) == 0 {
		t.Errorf("Expected some deleted IDs after pruning, got none")
	}

	// 3. Test with prune=false via JobHandler
	job2 := &store.Job{
		Payload: `{"roots": ["tv"], "prune": false}`,
	}
	err = handler(context.Background(), job2, func(float64) {})
	if err != nil {
		t.Fatalf("JobHandler prune=false failed: %v", err)
	}

	// 4. Test error cases in JobHandler
	jobBadJson := &store.Job{
		Payload: `{"roots": [`,
	}
	if err := handler(context.Background(), jobBadJson, nil); err == nil {
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
