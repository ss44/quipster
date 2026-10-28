// App-assembly tests: the wiring that main() performs must produce a usable
// set of services against a throwaway store.
package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/indexer"
	"quipster/internal/scan"
	"quipster/internal/store"
)

// assemblyConfig returns a config pointing every directory at a temp dir so
// assembleApp can run without touching the workspace.
func assemblyConfig(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	return config{
		addr:         ":0",
		role:         "all",
		dataDir:      dir,
		mediaDir:     filepath.Join(dir, "media"),
		cacheDir:     filepath.Join(dir, "cache"),
		meiliURL:     newSettingsFake(t, 0).srv.URL,
		scanIntvl:    5 * time.Millisecond,
		subWorkers:   1,
		indexWorkers: 1,
		clipWorkers:  1,
		padMs:        250,
		maxClipSec:   120,
		cacheMaxGB:   0.001,
		longCutSec:   20,
	}
}

func TestAssembleAppAllRole(t *testing.T) {
	t.Setenv("JOB_TIMEOUT", "")
	clearConfigEnv(t)
	cfg := assemblyConfig(t)
	if err := scan.EnsureDirs(cfg.dataDir, cfg.cacheDir,
		filepath.Join(cfg.cacheDir, "clips"), filepath.Join(cfg.cacheDir, "thumbs"),
		filepath.Join(cfg.cacheDir, "subs")); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.dataDir, "quipster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Log(cerr)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ix := indexer.New(st, cfg.meiliURL, "")

	a := assembleApp(ctx, cfg, st, ix)
	if a.Deps.St != st || a.Deps.Runner != a.Runner {
		t.Fatal("deps must be wired around the shared store and runner")
	}
	if a.Deps.Search == nil || a.Deps.Clips == nil || a.Deps.Thumbs == nil || a.Deps.Events == nil {
		t.Fatal("all services must be wired")
	}

	// Worker role handlers must be registered: a queued job gets picked up.
	if _, err := st.EnqueueJob("probe", `{"mediaId":1}`, 0); err != nil {
		t.Fatal(err)
	}
	waitForJobFinish(t, st)

	a.Stop()
}

// waitForJobFinish polls the job list until the first job leaves "queued".
func waitForJobFinish(t *testing.T, st *store.Store) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		jobs, err := st.ListJobs(10)
		if err != nil {
			t.Fatal(err)
		}
		done := len(jobs) > 0 && jobs[0].Status != "queued"
		if done || time.Now().After(deadline) {
			if !done {
				t.Fatalf("probe job never finished: %+v", jobs)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAssembleAppApiRoleSkipsWorkers(t *testing.T) {
	t.Setenv("JOB_TIMEOUT", "")
	clearConfigEnv(t)
	cfg := assemblyConfig(t)
	cfg.role = "api"
	cfg.scanIntvl = 0
	if err := scan.EnsureDirs(cfg.dataDir, cfg.cacheDir,
		filepath.Join(cfg.cacheDir, "clips"), filepath.Join(cfg.cacheDir, "thumbs"),
		filepath.Join(cfg.cacheDir, "subs")); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.dataDir, "quipster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Log(cerr)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := assembleApp(ctx, cfg, st, indexer.New(st, cfg.meiliURL, ""))

	if _, enqErr := st.EnqueueJob("probe", `{"mediaId":1}`, 0); enqErr != nil {
		t.Fatal(enqErr)
	}
	time.Sleep(20 * time.Millisecond)
	a.Stop()

	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != "queued" {
		t.Errorf("api role must not register workers, job = %+v", jobs)
	}
}
