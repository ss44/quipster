package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/store"
)

func TestAdminConfigGet(t *testing.T) {
	st, deps := newEnv(t)
	for _, dir := range []string{"Shows", ".hidden"} {
		if err := os.Mkdir(filepath.Join(deps.MediaDir, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(deps.MediaDir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}

	rr := do(t, Router(deps), http.MethodGet, "/api/admin/config", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	if body["mediaDir"] != deps.MediaDir {
		t.Errorf("mediaDir = %v", body["mediaDir"])
	}
	if body["cacheMaxBytes"] != float64(deps.CacheMaxBytes) {
		t.Errorf("cacheMaxBytes = %v, want %d", body["cacheMaxBytes"], deps.CacheMaxBytes)
	}
	if folders, ok := body["folders"].([]any); !ok || len(folders) != 1 || folders[0] != "Shows" {
		t.Errorf("folders = %v", body["folders"])
	}
	available, ok := body["available"].([]any)
	if !ok || len(available) != 1 || available[0] != "Shows" {
		t.Errorf("available = %v", body["available"])
	}
}

func TestAdminConfigPut(t *testing.T) {
	st, deps := newEnv(t)
	if err := os.Mkdir(filepath.Join(deps.MediaDir, "Shows"), 0o750); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	rr := do(t, h, http.MethodPut, "/api/admin/config",
		strings.NewReader(`{"folders":["Shows","Shows",""]}`), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	paths, err := st.GetLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "Shows" {
		t.Errorf("paths = %v", paths)
	}

	cases := []struct {
		body string
		code int
	}{
		{`{`, http.StatusBadRequest},
		{`{"folders":["/abs"]}`, http.StatusBadRequest},
		{`{"folders":["../escape"]}`, http.StatusBadRequest},
		{`{"folders":["Missing"]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rr := do(t, h, http.MethodPut, "/api/admin/config", strings.NewReader(tc.body), nil)
		if rr.Code != tc.code {
			t.Errorf("body %q status = %d, want %d", tc.body, rr.Code, tc.code)
		}
	}
}

func TestAdminScan(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	if err := st.SetLibraryPaths([]string{"Shows"}); err != nil {
		t.Fatal(err)
	}
	rr := do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "started" {
		t.Fatalf("scan status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := st.ClearQueuedJobs(); err != nil {
		t.Fatal(err)
	}

	rr = do(t, h, http.MethodPost, "/api/admin/scan?root=Shows/sub", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "started" {
		t.Fatalf("root scan status = %d body=%s", rr.Code, rr.Body.String())
	}

	if _, err := st.EnqueueJob("scan", `{}`, 3); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["status"] != "already-running" {
		t.Errorf("active scan status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := st.ClearQueuedJobs(); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{"/abs", "../escape"} {
		resp := do(t, h, http.MethodPost, "/api/admin/scan?root="+root, nil, nil)
		if resp.Code != http.StatusBadRequest {
			t.Errorf("root %q status = %d, want 400", root, resp.Code)
		}
	}

	// Invalid stored JSON makes the library configuration unreadable.
	if err := st.SetSetting(store.SettingLibraryPaths, "{bad"); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/scan", nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("bad settings status = %d, want 500", rr.Code)
	}
}

func TestAdminReindexAndReprobe(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	h := Router(deps)

	rr := do(t, h, http.MethodPost, "/api/admin/reindex", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reindex status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n, ok := decodeMap(t, rr)["enqueued"].(float64); !ok || n != 1 {
		t.Errorf("enqueued = %v", decodeMap(t, rr)["enqueued"])
	}

	rr = do(t, h, http.MethodPost, "/api/admin/reprobe", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reprobe status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n, ok := decodeMap(t, rr)["enqueued"].(float64); !ok || n != 1 {
		t.Errorf("enqueued = %v", decodeMap(t, rr)["enqueued"])
	}
}

// TestAdminReprobeTargeted verifies the optional mediaIds body limits the
// reprobe to the requested rows, so operators can rebuild only bad episodes.
func TestAdminReprobeTargeted(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	other, _, err := st.UpsertMedia("/media/show-s01e02.mkv", "matroska", 1234, 1700000001, "Second", 2020, 1, 2, "Show")
	if err != nil {
		t.Fatalf("UpsertMedia: %v", err)
	}
	if serr := st.SetMediaStatus(other, "indexed", ""); serr != nil {
		t.Fatalf("SetMediaStatus: %v", serr)
	}
	h := Router(deps)

	body := strings.NewReader(fmt.Sprintf(`{"mediaIds":[%d,%d,%d]}`, id, id, other))
	rr := do(t, h, http.MethodPost, "/api/admin/reprobe", body, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reprobe status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n, ok := decodeMap(t, rr)["enqueued"].(float64); !ok || n != 2 {
		t.Fatalf("enqueued = %v, want 2 after dedupe", decodeMap(t, rr)["enqueued"])
	}
	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		fmt.Sprintf(`"mediaId":%d`, id):    false,
		fmt.Sprintf(`"mediaId":%d`, other): false,
	}
	for _, j := range jobs {
		if j.Type != "probe" {
			continue
		}
		for payload := range want {
			if strings.Contains(j.Payload, payload) {
				want[payload] = true
			}
		}
	}
	for payload, found := range want {
		if !found {
			t.Errorf("missing probe job for %s", payload)
		}
	}
}

func enqueueProbes(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := st.EnqueueJob("probe", `{}`, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminQueuePauseResume(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	rr := do(t, h, http.MethodPost, "/api/admin/queue/pause", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["paused"] != true {
		t.Fatalf("pause status = %d body=%s", rr.Code, rr.Body.String())
	}
	if paused, err := st.GetQueuePaused(); err != nil || !paused {
		t.Errorf("queue paused = %v err=%v", paused, err)
	}
	rr = do(t, h, http.MethodPost, "/api/admin/queue/resume", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["paused"] != false {
		t.Fatalf("resume status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminQueueClearAndStop(t *testing.T) {
	st, deps := newEnv(t)
	h := Router(deps)

	enqueueProbes(t, st, 2)
	rr := do(t, h, http.MethodPost, "/api/admin/queue/clear", nil, nil)
	if rr.Code != http.StatusOK || decodeMap(t, rr)["cleared"] != float64(2) {
		t.Fatalf("clear status = %d body=%s", rr.Code, rr.Body.String())
	}

	enqueueProbes(t, st, 2)
	rr = do(t, h, http.MethodPost, "/api/admin/queue/stop", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := decodeMap(t, rr)
	if body["paused"] != true || body["cleared"] != float64(2) {
		t.Errorf("stop body = %v", body)
	}

	// A nil runner must not panic and still stops the queue.
	deps.Runner = nil
	rr = do(t, Router(deps), http.MethodPost, "/api/admin/queue/stop", nil, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("nil runner stop status = %d", rr.Code)
	}
}

func TestAdminJobs(t *testing.T) {
	st, deps := newEnv(t)
	for i := 0; i < 3; i++ {
		if _, err := st.EnqueueJob("probe", `{}`, 1); err != nil {
			t.Fatal(err)
		}
	}
	h := Router(deps)

	rr := do(t, h, http.MethodGet, "/api/admin/jobs?limit=1", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	jobsList, ok := body["jobs"].([]any)
	if !ok || len(jobsList) != 1 {
		t.Errorf("jobs = %v", body["jobs"])
	}
	stats, ok := body["stats"].(map[string]any)
	if !ok || stats["queued"] != float64(3) {
		t.Errorf("stats = %v", body["stats"])
	}

	rr = do(t, h, http.MethodGet, "/api/admin/jobs?limit=9999", nil, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	var all struct {
		Jobs []store.Job `json:"jobs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Jobs) != 3 {
		t.Errorf("jobs = %d, want 3", len(all.Jobs))
	}
}

func TestAdminStatus(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	rr := do(t, Router(deps), http.MethodGet, "/api/admin/status", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := decodeMap(t, rr)
	if body["queue"] == nil || body["mediaCounts"] == nil {
		t.Errorf("snapshot = %v", body)
	}
}

func TestAdminEventsSSE(t *testing.T) {
	_, deps := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/events", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	deps.Events.Handler()(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "event: tick") || !strings.Contains(body, `"meiliUp":true`) {
		t.Fatalf("sse body = %q", body)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type = %q", got)
	}

	// Writers without Flusher are rejected.
	plain := &plainWriter{header: http.Header{}}
	deps.Events.Handler()(plain, httptest.NewRequest(http.MethodGet, "/api/admin/events", nil))
	if plain.code != http.StatusInternalServerError {
		t.Errorf("plain writer status = %d, want 500", plain.code)
	}
}

type plainWriter struct {
	header http.Header
	code   int
}

func (p *plainWriter) Header() http.Header { return p.header }

func (p *plainWriter) Write(b []byte) (int, error) { return len(b), nil }

func (p *plainWriter) WriteHeader(code int) { p.code = code }
