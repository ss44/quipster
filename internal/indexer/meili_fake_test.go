package indexer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/store"
)

// request captures a single HTTP request seen by the fake Meilisearch server.
type request struct {
	method string
	path   string
	body   []byte
}

// fakeMeili is an in-process Meilisearch stand-in. It implements just enough
// of the REST API surface for the client methods the indexer uses, and offers
// per-test failure knobs so error paths can be exercised without a live
// Meilisearch instance.
type fakeMeili struct {
	srv *httptest.Server
	// onDelete, when set, runs synchronously while a deletion request is in
	// flight, simulating state changes that race the delete (e.g. a restore).
	onDelete      func()
	tasks         map[int64]meili.Task
	docs          map[string]map[string]any
	requests      []request
	nextUID       int64
	settingsCalls int
	typoCalls     int
	embedderCalls int
	mu            sync.Mutex
	indexExists   bool
	failCreate    bool
	createFlips   bool
	failTaskGet   bool
	delayNextGet  bool
	failSettings  bool
	failTypo      bool
	abortTypo     bool
	failAdd       bool
	failDelete    bool
	addTasksFail  bool
	// deleteTasksFail makes the enqueued deletion task itself fail, distinct
	// from failDelete which fails the HTTP call that enqueues it.
	deleteTasksFail bool
	failEmbedder    bool
	failEmbTask     bool
}

// respWriter funnels all fake-server JSON writes through checked calls so the
// handler can satisfy errcheck without blank-ignoring anything.
type respWriter struct {
	w   http.ResponseWriter
	err error
}

func (rw *respWriter) json(status int, v any) {
	if rw.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		rw.err = err
		return
	}
	rw.w.Header().Set("Content-Type", "application/json")
	rw.w.WriteHeader(status)
	if _, err := rw.w.Write(b); err != nil {
		rw.err = err
	}
}

func (rw *respWriter) fail(status int, code, msg string) {
	rw.json(status, map[string]string{"message": msg, "code": code, "type": "invalid_request", "link": "https://docs.meilisearch.com"})
}

// newFakeMeili starts the fake server and registers cleanup.
func newFakeMeili(t *testing.T) *fakeMeili {
	t.Helper()
	f := &fakeMeili{
		tasks: map[int64]meili.Task{},
		docs:  map[string]map[string]any{},
	}
	f.srv = httptest.NewServer(f)
	t.Cleanup(func() {
		f.srv.Close()
	})
	return f
}

// url returns the base URL of the fake server.
func (f *fakeMeili) url() string { return f.srv.URL }

// enqueue records a task in the fake task table and returns its uid.
func (f *fakeMeili) enqueue(typ string, failed bool) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid := f.nextUID
	f.nextUID++
	status := meili.TaskStatusSucceeded
	if failed {
		status = meili.TaskStatusFailed
	}
	now := time.Now().UTC()
	f.tasks[uid] = meili.Task{
		UID: uid, TaskUID: uid, Status: status, Type: meili.TaskType(typ), IndexUID: IndexName,
		EnqueuedAt: now, StartedAt: now, FinishedAt: now, Duration: "PT0S", Details: meili.Details{},
	}
	return uid
}

// writeTaskInfo answers an async API call with an enqueued TaskInfo payload.
func (f *fakeMeili) writeTaskInfo(rw *respWriter, uid int64) {
	f.mu.Lock()
	task := f.tasks[uid]
	f.mu.Unlock()
	rw.json(http.StatusAccepted, map[string]any{
		"taskUid":    uid,
		"indexUid":   IndexName,
		"status":     "enqueued",
		"type":       string(task.Type),
		"enqueuedAt": task.EnqueuedAt.Format(time.RFC3339Nano),
	})
}

// writeTask answers GET /tasks/:uid with the stored task body.
func (f *fakeMeili) writeTask(rw *respWriter, task *meili.Task) {
	body := map[string]any{
		"uid":        task.UID,
		"taskUid":    task.TaskUID,
		"status":     string(task.Status),
		"type":       string(task.Type),
		"indexUid":   task.IndexUID,
		"enqueuedAt": task.EnqueuedAt.Format(time.RFC3339Nano),
		"startedAt":  task.StartedAt.Format(time.RFC3339Nano),
		"finishedAt": task.FinishedAt.Format(time.RFC3339Nano),
		"duration":   task.Duration,
		"details":    map[string]any{},
	}
	if task.Status == meili.TaskStatusFailed {
		body["error"] = map[string]any{"message": "boom", "code": "internal", "type": "internal"}
	}
	rw.json(http.StatusOK, body)
}

// ServeHTTP routes a request to the right fake endpoint.
func (f *fakeMeili) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	rw := &respWriter{w: w}
	f.mu.Lock()
	f.requests = append(f.requests, request{method: r.Method, path: r.URL.Path, body: body})
	f.mu.Unlock()
	f.route(rw, r, body)
	if rw.err != nil {
		http.Error(w, rw.err.Error(), http.StatusInternalServerError)
	}
}

// route dispatches one request by HTTP method to keep complexity low.
func (f *fakeMeili) route(rw *respWriter, r *http.Request, body []byte) {
	switch r.Method {
	case http.MethodGet:
		f.routeGet(rw, r)
	case http.MethodPost:
		f.routePost(rw, r, body)
	case http.MethodPatch:
		f.handleSettings(rw, body)
	default:
		rw.fail(http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeMeili) routeGet(rw *respWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		rw.json(http.StatusOK, map[string]string{"status": "available"})
	case "/indexes/" + IndexName:
		f.handleGetIndex(rw)
	default:
		if strings.HasPrefix(r.URL.Path, "/tasks/") {
			f.handleGetTask(rw, r.URL.Path)
			return
		}
		rw.fail(http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeMeili) routePost(rw *respWriter, r *http.Request, body []byte) {
	switch r.URL.Path {
	case "/indexes":
		f.handleCreateIndex(rw)
	case "/indexes/" + IndexName + "/documents":
		f.handleAddDocuments(rw, body)
	case "/indexes/" + IndexName + "/documents/delete":
		f.handleDeleteDocuments(rw)
	default:
		rw.fail(http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeMeili) handleGetIndex(rw *respWriter) {
	f.mu.Lock()
	exists := f.indexExists
	f.mu.Unlock()
	if !exists {
		rw.fail(http.StatusNotFound, "index_not_found", "index "+IndexName+" not found")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rw.json(http.StatusOK, map[string]any{"uid": IndexName, "primaryKey": "id", "createdAt": now, "updatedAt": now})
}

func (f *fakeMeili) handleCreateIndex(rw *respWriter) {
	f.mu.Lock()
	fail, flip := f.failCreate, f.createFlips
	if flip {
		f.createFlips = false
		f.indexExists = true
	}
	if !fail && !flip {
		f.indexExists = true
	}
	f.mu.Unlock()
	if fail || flip {
		rw.fail(http.StatusBadRequest, "index_already_exists", "index already exists")
		return
	}
	uid := f.enqueue("indexCreation", false)
	f.writeTaskInfo(rw, uid)
}

func (f *fakeMeili) handleGetTask(rw *respWriter, path string) {
	uid, err := strconv.ParseInt(strings.TrimPrefix(path, "/tasks/"), 10, 64)
	if err != nil {
		rw.fail(http.StatusBadRequest, "invalid_task_uid", "bad task uid")
		return
	}
	f.mu.Lock()
	failGet, delay := f.failTaskGet, f.delayNextGet
	if delay {
		f.delayNextGet = false
	}
	f.mu.Unlock()
	if failGet {
		rw.fail(http.StatusInternalServerError, "internal", "task lookup failed")
		return
	}
	f.mu.Lock()
	task, ok := f.tasks[uid]
	f.mu.Unlock()
	if !ok {
		rw.fail(http.StatusNotFound, "task_not_found", "task not found")
		return
	}
	if delay {
		task.Status = meili.TaskStatusEnqueued
	}
	f.writeTask(rw, &task)
}

func (f *fakeMeili) handleAddDocuments(rw *respWriter, body []byte) {
	f.mu.Lock()
	fail := f.failAdd
	f.mu.Unlock()
	if fail {
		rw.fail(http.StatusInternalServerError, "internal", "add documents failed")
		return
	}
	var docs []map[string]any
	if err := json.Unmarshal(body, &docs); err != nil {
		rw.fail(http.StatusBadRequest, "bad_request", "bad documents body")
		return
	}
	f.mu.Lock()
	for _, d := range docs {
		if id, ok := d["id"].(string); ok {
			f.docs[id] = d
		}
	}
	f.mu.Unlock()
	uid := f.enqueue("documentAddition", f.addTasksFail)
	f.writeTaskInfo(rw, uid)
}

func (f *fakeMeili) handleDeleteDocuments(rw *respWriter) {
	f.mu.Lock()
	fail, taskFail := f.failDelete, f.deleteTasksFail
	f.mu.Unlock()
	if fail {
		rw.fail(http.StatusInternalServerError, "internal", "delete documents failed")
		return
	}
	if f.onDelete != nil {
		f.onDelete()
	}
	uid := f.enqueue("documentDeletion", taskFail)
	f.writeTaskInfo(rw, uid)
}

func (f *fakeMeili) handleSettings(rw *respWriter, body []byte) {
	if strings.Contains(string(body), "embedders") {
		f.mu.Lock()
		f.embedderCalls++
		fail, failTask := f.failEmbedder, f.failEmbTask
		f.mu.Unlock()
		if fail {
			rw.fail(http.StatusInternalServerError, "internal", "embedder boom")
			return
		}
		uid := f.enqueue("settingsUpdate", failTask)
		f.writeTaskInfo(rw, uid)
		return
	}
	if strings.Contains(string(body), "typoTolerance") {
		f.mu.Lock()
		f.typoCalls++
		abort, fail := f.abortTypo, f.failTypo
		f.mu.Unlock()
		if abort {
			panic(http.ErrAbortHandler)
		}
		if fail {
			rw.fail(http.StatusInternalServerError, "internal", "typo boom")
			return
		}
		uid := f.enqueue("settingsUpdate", false)
		f.writeTaskInfo(rw, uid)
		return
	}
	f.mu.Lock()
	f.settingsCalls++
	fail := f.failSettings
	f.mu.Unlock()
	if fail {
		rw.fail(http.StatusInternalServerError, "internal", "settings boom")
		return
	}
	uid := f.enqueue("settingsUpdate", false)
	f.writeTaskInfo(rw, uid)
}

// calls counts recorded requests matching method and path.
func (f *fakeMeili) calls(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.method == method && r.path == path {
			n++
		}
	}
	return n
}

// deleteFilters returns the filter expression of every recorded delete call.
func (f *fakeMeili) deleteFilters() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.path != "/indexes/"+IndexName+"/documents/delete" {
			continue
		}
		var payload struct {
			Filter string `json:"filter"`
		}
		if err := json.Unmarshal(r.body, &payload); err == nil {
			out = append(out, payload.Filter)
		}
	}
	return out
}

// addedDocs returns every document body posted to the fake index.
func (f *fakeMeili) addedDocs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range f.requests {
		if r.path != "/indexes/"+IndexName+"/documents" {
			continue
		}
		var docs []map[string]any
		if err := json.Unmarshal(r.body, &docs); err == nil {
			out = append(out, docs...)
		}
	}
	return out
}

// patchBodies returns the raw bodies of every recorded settings PATCH.
func (f *fakeMeili) patchBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.method == http.MethodPatch {
			out = append(out, string(r.body))
		}
	}
	return out
}

// firstIndex returns the index of the first recorded request matching method
// and path, or -1.
func (f *fakeMeili) firstIndex(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.requests {
		if r.method == method && r.path == path {
			return i
		}
	}
	return -1
}

// newTestStore opens a throwaway SQLite store for one test.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Log(err)
		}
	})
	return st
}
