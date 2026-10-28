// Wiring tests for the main package: env seeding, Meili settings wait loop,
// and the periodic scan ticker.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"quipster/internal/indexer"
	"quipster/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Log(cerr)
		}
	})
	return st
}

func TestSeedLibraryPathsFromEnv(t *testing.T) {
	st := openStore(t)
	t.Setenv("LIBRARY_PATHS", " Movies , tv/ ,,")

	seedLibraryPaths(st)

	roots, err := st.GetLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != "Movies" || roots[1] != "tv" {
		t.Errorf("roots = %v, want [Movies tv]", roots)
	}
}

func TestSeedLibraryPathsInvalidEnvKeepsEmpty(t *testing.T) {
	st := openStore(t)
	t.Setenv("LIBRARY_PATHS", "/absolutely/not/allowed")

	seedLibraryPaths(st)

	if _, ok, err := st.GetSetting(store.SettingLibraryPaths); err != nil || ok {
		t.Errorf("invalid env must not persist a setting: ok=%v err=%v", ok, err)
	}
}

func TestSeedLibraryPathsExistingSettingWins(t *testing.T) {
	st := openStore(t)
	if err := st.SetLibraryPaths([]string{"ui-chosen"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIBRARY_PATHS", "from-env")

	seedLibraryPaths(st)

	roots, err := st.GetLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != "ui-chosen" {
		t.Errorf("roots = %v, want persisted value untouched", roots)
	}
}

func TestSeedLibraryPathsUnsetEnvNoop(t *testing.T) {
	t.Setenv("LIBRARY_PATHS", "")
	st := openStore(t)

	seedLibraryPaths(st)

	if _, ok, err := st.GetSetting(store.SettingLibraryPaths); err != nil || ok {
		t.Errorf("empty env must not persist a setting: ok=%v err=%v", ok, err)
	}
}

func TestSeedLibraryPathsStoreError(t *testing.T) {
	st := openStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIBRARY_PATHS", "movies")

	// Reads and writes both fail on the closed store; the helper must log
	// and return instead of panicking.
	seedLibraryPaths(st)
}

// settingsFake is a minimal in-process Meilisearch stand-in implementing the
// exact request/response contract of indexer.EnsureSettings: index lookup,
// index creation, settings update, and task polling. failSettings counts down
// the number of settings PATCHes that fail so the retry loop is exercised.
type settingsFake struct {
	srv          *httptest.Server
	tasks        map[int64]string
	failSettings int
	nextTask     int64
	mu           sync.Mutex
	indexExists  bool
}

func newSettingsFake(t *testing.T, failSettings int) *settingsFake {
	t.Helper()
	f := &settingsFake{failSettings: failSettings, tasks: map[int64]string{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *settingsFake) enqueue(status string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid := f.nextTask
	f.nextTask++
	f.tasks[uid] = status
	return uid
}

func (f *settingsFake) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Client gone mid-write; nothing further to do in the fake.
		_ = err
	}
}

func (f *settingsFake) writeTaskInfo(w http.ResponseWriter, uid int64) {
	f.writeJSON(w, http.StatusAccepted, map[string]any{
		"taskUid": uid, "indexUid": indexer.IndexName, "status": "enqueued", "type": "settingsUpdate",
	})
}

func (f *settingsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.routeGet(w, r)
	case http.MethodPost:
		f.routePost(w, r)
	case http.MethodPatch:
		f.routePatch(w)
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "no route"})
	}
}

func (f *settingsFake) routeGet(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/indexes/"+indexer.IndexName:
		f.mu.Lock()
		exists := f.indexExists
		f.mu.Unlock()
		if !exists {
			f.writeJSON(w, http.StatusNotFound, map[string]string{
				"message": "index " + indexer.IndexName + " not found", "code": "index_not_found",
			})
			return
		}
		f.writeJSON(w, http.StatusOK, map[string]any{"uid": indexer.IndexName, "primaryKey": "id"})
	case strings.HasPrefix(r.URL.Path, "/tasks/"):
		f.writeTask(w, r.URL.Path)
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "no route"})
	}
}

func (f *settingsFake) writeTask(w http.ResponseWriter, path string) {
	uid, err := strconv.ParseInt(strings.TrimPrefix(path, "/tasks/"), 10, 64)
	if err != nil {
		f.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad task uid"})
		return
	}
	f.mu.Lock()
	status := f.tasks[uid]
	f.mu.Unlock()
	if status == "" {
		f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "task not found"})
		return
	}
	f.writeJSON(w, http.StatusOK, map[string]any{
		"uid": uid, "taskUid": uid, "status": status, "type": "settingsUpdate",
		"indexUid": indexer.IndexName, "duration": "PT0S",
	})
}

func (f *settingsFake) routePost(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/indexes" {
		f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "no route"})
		return
	}
	uid := f.enqueue("succeeded")
	f.mu.Lock()
	f.indexExists = true
	f.mu.Unlock()
	f.writeTaskInfo(w, uid)
}

func (f *settingsFake) routePatch(w http.ResponseWriter) {
	f.mu.Lock()
	fail := f.failSettings > 0
	if fail {
		f.failSettings--
	}
	f.mu.Unlock()
	if fail {
		f.writeJSON(w, http.StatusInternalServerError, map[string]string{
			"message": "warming up", "code": "internal",
		})
		return
	}
	f.writeTaskInfo(w, f.enqueue("succeeded"))
}

func TestEnsureSettingsLoopReady(t *testing.T) {
	st := openStore(t)
	ix := indexer.New(st, newSettingsFake(t, 0).srv.URL, "")

	done := make(chan struct{})
	go func() {
		ensureSettingsLoop(ix, time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ensureSettingsLoop did not return on success")
	}
}

func TestEnsureSettingsLoopRetriesThenReady(t *testing.T) {
	st := openStore(t)
	ix := indexer.New(st, newSettingsFake(t, 2).srv.URL, "")

	done := make(chan struct{})
	go func() {
		ensureSettingsLoop(ix, time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ensureSettingsLoop did not recover after failures")
	}
}

func TestScanTickerEnqueuesAndStops(t *testing.T) {
	st := openStore(t)
	if err := st.SetLibraryPaths([]string{"movies"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		scanTicker(ctx, st, 5*time.Millisecond)
		close(done)
	}()

	// Wait for at least one tick to enqueue a scan job, then stop the ticker.
	deadline := time.Now().Add(2 * time.Second)
	for {
		jobs, err := st.ListJobs(10)
		if err != nil {
			t.Fatal(err)
		}
		enqueued := false
		for _, j := range jobs {
			if j.Type == "scan" {
				enqueued = true
			}
		}
		if enqueued || time.Now().After(deadline) {
			if !enqueued {
				t.Fatal("scan ticker never enqueued a scan job")
			}
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scanTicker did not stop after context cancel")
	}
}

func TestScanTickerSkipsWhenScanActive(t *testing.T) {
	st := openStore(t)
	if err := st.SetLibraryPaths([]string{"movies"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob("scan", "{}", 3); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		scanTicker(ctx, st, 5*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Errorf("active scan must suppress ticker enqueues, got %d jobs", len(jobs))
	}
}
