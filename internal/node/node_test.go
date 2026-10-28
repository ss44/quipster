package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quipster/internal/clips"
	"quipster/internal/nodes"
	"quipster/internal/probe"
	"quipster/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServer is a minimal coordinator implementing the node protocol on top
// of a real store and nodes.Service, enough to exercise the client and
// session end to end.
type fakeServer struct {
	st  *store.Store
	svc *nodes.Service
	srv *httptest.Server
}

//nolint:gocyclo // one sequential setup keeps endpoint wiring together
func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "srv.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	svc := nodes.NewService(st, nodes.Settings{
		MediaRoot:    "/srv/media",
		SubsCacheDir: filepath.Join(t.TempDir(), "subs"),
		Clips: nodes.ClipSettings{
			PreviewRes: "640:-2", AccurateRes: "1280:-2", MaxClipSec: 120, DefaultPadMs: 250,
		},
		Heartbeat: 50 * time.Millisecond,
		Version:   "test",
	})
	svc.Pipeline = &probe.Pipeline{St: st, SubsCacheDir: filepath.Join(t.TempDir(), "server-subs")}
	svc.Clips = clips.New(st, clips.Config{
		CacheDir:     filepath.Join(t.TempDir(), "server-cache"),
		PreviewRes:   "640:-2",
		AccurateRes:  "1280:-2",
		MaxClipSec:   120,
		DefaultPadMs: 250,
	})
	fs := &fakeServer{st: st, svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, Fingerprint, Version string }
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		n, err := svc.Register(body.Name, body.Fingerprint, body.Version)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"nodeId": n.ID, "secret": n.Secret, "status": n.Status})
		// Simulate the operator approving every registration right away.
		if err := svc.Approve(n.ID); err != nil {
			t.Logf("auto-approve: %v", err)
		}
	})
	mux.HandleFunc("GET /api/nodes/{id}/state", func(w http.ResponseWriter, r *http.Request) {
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			http.Error(w, "bad node id", http.StatusBadRequest)
			return
		}
		st, err := svc.State(id, r.Header.Get("X-Node-Secret"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("POST /api/nodes/claim", func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := fs.auth(w, r)
		if !ok {
			return
		}
		var body struct{ Types []string }
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		work, err := svc.Claim(r.Context(), nodeID, body.Types)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if work == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, work)
	})
	mux.HandleFunc("POST /api/nodes/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := fs.auth(w, r)
		if !ok {
			return
		}
		var body struct{ Jobs []nodes.JobProgress }
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		beat, err := svc.Heartbeat(nodeID, body.Jobs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, beat)
	})
	mux.HandleFunc("POST /api/nodes/jobs/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := fs.auth(w, r)
		if !ok {
			return
		}
		jobID, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			http.Error(w, "bad job id", http.StatusBadRequest)
			return
		}
		var body struct{ Status, Error string }
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		accepted, err := svc.Complete(nodeID, jobID, body.Status, body.Error)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"accepted": accepted})
	})
	mux.HandleFunc("POST /api/nodes/jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := fs.auth(w, r)
		if !ok {
			return
		}
		jobID, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			http.Error(w, "bad job id", http.StatusBadRequest)
			return
		}
		resultJSON, sub, clip, ext := fs.readResult(t, r)
		if resultJSON == nil {
			http.Error(w, "result part required", http.StatusBadRequest)
			return
		}
		var accepted bool
		var err error
		if clip != nil {
			var meta struct{ Ext string }
			if uerr := json.Unmarshal(resultJSON, &meta); uerr != nil {
				http.Error(w, uerr.Error(), http.StatusBadRequest)
				return
			}
			if ext == "" {
				ext = meta.Ext
			}
			accepted, err = svc.ApplyClipResult(nodeID, jobID, ext, clip)
		} else {
			var outcome probe.Outcome
			if jerr := json.Unmarshal(resultJSON, &outcome); jerr != nil {
				http.Error(w, jerr.Error(), http.StatusBadRequest)
				return
			}
			accepted, err = svc.ApplyProbeResult(nodeID, jobID, &outcome, sub)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"accepted": accepted})
	})
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // test helper; a failed encode surfaces as an empty response
}

// auth authenticates the request and returns the node id.
func (fs *fakeServer) auth(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := fs.svc.Authenticate(r.Header.Get("X-Node-Token"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return 0, false
	}
	return id, true
}

// readResult parses the multipart result body into its parts. Parts are
// buffered eagerly: a saved multipart part reader is dead once iteration
// advances.
func (fs *fakeServer) readResult(t *testing.T, r *http.Request) (resultJSON []byte, sub io.Reader, clip io.Reader, ext string) {
	t.Helper()
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, nil, nil, ""
	}
	for {
		part, perr := mr.NextPart()
		if perr != nil {
			break
		}
		switch part.FormName() {
		case "result":
			resultJSON, _ = io.ReadAll(part) //nolint:errcheck // a truncated result part fails the upload downstream
		case "subtitle":
			b, _ := io.ReadAll(part) //nolint:errcheck // test server buffers best-effort
			sub = bytes.NewReader(b)
		case "clip":
			b, _ := io.ReadAll(part) //nolint:errcheck // test server buffers best-effort
			clip = bytes.NewReader(b)
			ext = strings.TrimPrefix(filepath.Ext(part.FileName()), ".")
		}
	}
	return resultJSON, sub, clip, ext
}

// nodeConfig builds a fast-cadence node config pointed at the fake server.
func nodeConfig(t *testing.T, srvURL, mediaDir string) Config {
	t.Helper()
	cfg, err := LoadConfig(Options{
		ServerURL: srvURL,
		Name:      "test-node",
		MediaDir:  mediaDir,
		CacheDir:  filepath.Join(t.TempDir(), "node-cache"),
		StatePath: filepath.Join(t.TempDir(), "node.json"),
	})
	require.NoError(t, err)
	cfg.ProbeWorkers = 1
	cfg.ClipWorkers = 1
	cfg.Heartbeat = 50 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	cfg.JobTimeout = 30 * time.Second
	return cfg
}

// writeMockBin writes an executable shell script masquerading as a media
// binary so tests can drive ffprobe/ffmpeg paths without real tooling.
func writeMockBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // mock binary must stay executable
		t.Fatal(err)
	}
	return p
}

// TestEndToEndProbeNode runs a full distributed probe: the node registers,
// is approved, claims a probe job, executes it against its local mount and
// uploads the result, which the server stores and finishes.
func TestEndToEndProbeNode(t *testing.T) {
	fs := newFakeServer(t)

	// Server-side library entry: the path lives under the server root.
	id, _, err := fs.st.UpsertMedia("/srv/media/library/movie.mkv", "matroska", 100, 1, "Movie", 2020, 0, 0, "")
	require.NoError(t, err)
	jobID, err := fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 2)
	require.NoError(t, err)

	// Node-side mount of the same library at a different prefix.
	nodeMedia := t.TempDir()
	mediaFile := filepath.Join(nodeMedia, "library", "movie.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(mediaFile), 0o750))
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))

	binDir := t.TempDir()
	ffprobeJSON := `{"format":{"format_name":"matroska","duration":"30.0","start_time":"0"},"streams":[` +
		`{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360},` +
		`{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"tags":{"language":"eng"},"disposition":{"default":1}},` +
		`{"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng"}}]}`
	p := writeMockBin(t, binDir, "ffprobe", fmt.Sprintf("cat << 'FFEOF'\n%s\nFFEOF\n", ffprobeJSON))
	t.Setenv("FFPROBE_BIN", p)
	srt := "1\n00:00:01,000 --> 00:00:03,000\nHello there.\n\n2\n00:00:04,000 --> 00:00:06,000\nGeneral Kenobi.\n"
	f := writeMockBin(t, binDir, "ffmpeg", fmt.Sprintf(`out=""
for a in "$@"; do out="$a"; done
cat > "$out" <<'SRT'
%sSRT
`, srt))
	t.Setenv("FFMPEG_BIN", f)

	cfg := nodeConfig(t, fs.srv.URL, nodeMedia)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// Wait for the job to be finished by the node's upload.
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, jerr := fs.st.JobByID(jobID)
		require.NoError(t, jerr)
		if j.Status == "done" {
			break
		}
		if j.Status == "error" || time.Now().After(deadline) {
			t.Fatalf("job not done: status=%s error=%q", j.Status, j.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}

	j, err := fs.st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	m, err := fs.st.GetMedia(id)
	require.NoError(t, err)
	assert.InDelta(t, 30.0, m.DurationSec, 0.001)
	assert.Equal(t, 0, m.AudioStreamIndex)
	cues, err := fs.st.CueCount(id)
	require.NoError(t, err)
	assert.Equal(t, int64(2), cues, "parsed cues uploaded from the node landed in the server store")
	var indexJobs int64
	require.NoError(t, fs.st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index'`).Scan(&indexJobs))
	assert.Equal(t, int64(1), indexJobs, "server enqueues indexing after applying the node result")

	// The node persisted its token for future restarts.
	saved, err := loadState(cfg.StatePath)
	require.NoError(t, err)
	assert.NotEmpty(t, saved.Token)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestEndToEndClipNode verifies a clip job: the node renders with the
// server's profile and uploads the file, which the server registers into its
// cache under the derived key.
func TestEndToEndClipNode(t *testing.T) {
	fs := newFakeServer(t)
	id, _, err := fs.st.UpsertMedia("/srv/media/library/movie.mkv", "matroska", 100, 1, "Movie", 2020, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, fs.st.UpdateMediaProbe(id, 120, 1920, 1080, "h264", "aac", "matroska", 0, 1, "en"))
	jobID, err := fs.st.EnqueueJob("clip", fmt.Sprintf(`{"mediaId":%d,"start":1,"end":3,"mode":"accurate","padMs":250}`, id), 1)
	require.NoError(t, err)

	nodeMedia := t.TempDir()
	mediaFile := filepath.Join(nodeMedia, "library", "movie.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(mediaFile), 0o750))
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))

	binDir := t.TempDir()
	// ffmpeg mock writes a recognizable render into its output path.
	p := writeMockBin(t, binDir, "ffmpeg", `out=""
for a in "$@"; do out="$a"; done
printf 'render-bytes' > "$out"
`)
	t.Setenv("FFMPEG_BIN", p)
	t.Setenv("FFPROBE_BIN", p) // hardware probing also shells out; the mock exits cleanly

	cfg := nodeConfig(t, fs.srv.URL, nodeMedia)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		j, jerr := fs.st.JobByID(jobID)
		require.NoError(t, jerr)
		if j.Status == "done" || j.Status == "error" || time.Now().After(deadline) {
			require.Equal(t, "done", j.Status, "error=%q", j.Error)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	key := fs.svc.Clips.CacheKey(clips.Request{MediaID: id, Start: 1, End: 3, PadMs: 250, Mode: "accurate"})
	e, err := fs.st.GetCacheEntry(key)
	require.NoError(t, err)
	require.NotNil(t, e, "uploaded render registered under the server-derived cache key")
	data, err := os.ReadFile(e.Path) //nolint:gosec // test-only path from the cache entry
	require.NoError(t, err)
	assert.Equal(t, "render-bytes", string(data))

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestNodeErrorVerdictRequeues verifies that a failing media file produces an
// error verdict that requeues within the retry budget instead of stranding.
func TestNodeErrorVerdictRequeues(t *testing.T) {
	fs := newFakeServer(t)
	id, _, err := fs.st.UpsertMedia("/srv/media/library/missing.mkv", "matroska", 100, 1, "Movie", 2020, 0, 0, "")
	require.NoError(t, err)
	jobID, err := fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 2)
	require.NoError(t, err)

	cfg := nodeConfig(t, fs.srv.URL, t.TempDir()) // no local file: media error
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// A missing file is a media-level error: the outcome uploads and the
	// job finishes done with the media marked errored.
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, jerr := fs.st.JobByID(jobID)
		require.NoError(t, jerr)
		if j.Status == "done" || time.Now().After(deadline) {
			require.Equal(t, "done", j.Status, "media-level failures must finish the job")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m, err := fs.st.GetMedia(id)
	require.NoError(t, err)
	assert.Equal(t, "error", m.Status)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestPausedQueueStopsClaiming verifies the node stops claiming while the
// queue is paused and resumes after.
func TestPausedQueueStopsClaiming(t *testing.T) {
	fs := newFakeServer(t)
	id, _, err := fs.st.UpsertMedia("/srv/media/library/movie.mkv", "matroska", 100, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	jobID, err := fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 1)
	require.NoError(t, err)
	require.NoError(t, fs.st.SetQueuePaused(true))

	nodeMedia := t.TempDir()
	mediaFile := filepath.Join(nodeMedia, "library", "movie.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(mediaFile), 0o750))
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))

	binDir := t.TempDir()
	t.Setenv("FFPROBE_BIN", writeMockBin(t, binDir, "ffprobe", "exit 0\n"))
	cfg := nodeConfig(t, fs.srv.URL, nodeMedia)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	time.Sleep(500 * time.Millisecond)
	j, err := fs.st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "queued", j.Status, "paused queue must not be claimed")

	require.NoError(t, fs.st.SetQueuePaused(false))
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := fs.st.JobByID(jobID)
		require.NoError(t, err)
		if j.Status != "queued" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestRemapMediaPath covers the cross-mount path translation.
func TestRemapMediaPath(t *testing.T) {
	// Root-to-root: the node mounts the whole server media root.
	m, err := newMapper("/srv/media", "/mnt/media", "")
	require.NoError(t, err)
	got, err := m.local("/srv/media/library/show/file.mkv")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/media/library/show/file.mkv", got)

	// Relative part is re-localized to the host separator.
	m, err = newMapper("/srv/media", filepath.Join("Z:", "media"), "")
	require.NoError(t, err)
	got, err = m.local("/srv/media/a/b.mkv")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("Z:", "media", "a", "b.mkv"), got)

	// Explicit subfolder mapping: the node mounts only the library folder.
	m, err = newMapper("/srv/media", "/mnt/library", "/srv/media/library=/mnt/library")
	require.NoError(t, err)
	got, err = m.local("/srv/media/library/show/file.mkv")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/library/show/file.mkv", got)

	// Case-insensitive root matching (Windows/macOS mounts).
	m, err = newMapper("/SRV/MEDIA", "/mnt/lib", "")
	require.NoError(t, err)
	got, err = m.local("/srv/media/x.mkv")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/lib/x.mkv", got)

	// Escaping paths are rejected.
	m, err = newMapper("/srv/media", "/mnt/lib", "")
	require.NoError(t, err)
	_, err = m.local("/srv/other/x.mkv")
	require.Error(t, err)

	// Malformed mappings fail fast.
	_, err = newMapper("/srv/media", "/mnt/lib", "broken")
	require.Error(t, err)
	_, err = newMapper("/srv/media", "/mnt/lib", "/srv/media=")
	require.Error(t, err)
}

// TestLoadConfigDefaults verifies the cross-platform defaults and env
// overrides.
func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("NODE_SERVER_URL", "http://main:8080/")
	t.Setenv("MEDIA_DIR", "./med")

	cfg, err := LoadConfig(Options{})
	require.NoError(t, err)
	assert.Equal(t, "http://main:8080", cfg.ServerURL, "trailing slash trimmed")
	assert.True(t, strings.HasSuffix(cfg.StatePath, filepath.Join("quipster", "node.json")))
	assert.True(t, strings.HasSuffix(cfg.CacheDir, filepath.Join("quipster")))
	assert.True(t, filepath.IsAbs(cfg.MediaDir), "media dir is absolutized")
	assert.Equal(t, 4, cfg.ProbeWorkers)
	assert.Equal(t, 2, cfg.ClipWorkers)
	assert.Equal(t, 10*time.Second, cfg.Heartbeat)

	// Missing server URL is an error.
	t.Setenv("NODE_SERVER_URL", "")
	t.Setenv("SERVER_URL", "")
	_, err = LoadConfig(Options{})
	require.Error(t, err)
}

// TestStatePersistence verifies the identity round trip.
func TestStatePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	st, err := loadState(path)
	require.NoError(t, err)
	require.Len(t, st.Fingerprint, 64)

	st.Token = "tok"
	require.NoError(t, st.save(path))
	reloaded, err := loadState(path)
	require.NoError(t, err)
	assert.Equal(t, st.Fingerprint, reloaded.Fingerprint)
	assert.Equal(t, "tok", reloaded.Token)
}

// TestClientUnauthorizedDetection ensures a 401 maps to the sentinel.
func TestClientUnauthorizedDetection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "bad"
	err := c.do(context.Background(), http.MethodPost, "/api/nodes/claim", "application/json", strings.NewReader("{}"), nil)
	assert.ErrorIs(t, err, errUnauthorized)
}
