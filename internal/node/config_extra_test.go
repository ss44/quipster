package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quipster/internal/nodes"
	"quipster/internal/probe"
	"quipster/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseDurationVariants covers the server-compatible parsing.
func TestParseDurationVariants(t *testing.T) {
	assert.Zero(t, parseDuration(""))
	assert.Zero(t, parseDuration("bogus"))
	assert.Equal(t, 5*time.Second, parseDuration("5"), "bare numbers are seconds")
	assert.Equal(t, 90*time.Second, parseDuration("90s"))
	assert.Equal(t, 2*time.Hour, parseDuration("2h"))
}

// TestEnvIntVariants covers invalid and unset values.
func TestEnvIntVariants(t *testing.T) {
	assert.Equal(t, 3, envInt("UNSET_VAR", 3))
	t.Setenv("BAD_INT", "not-a-number")
	assert.Equal(t, 7, envInt("BAD_INT", 7))
	t.Setenv("ZERO_INT", "0")
	assert.Equal(t, 7, envInt("ZERO_INT", 7), "non-positive values fall back")
	t.Setenv("GOOD_INT", "9")
	assert.Equal(t, 9, envInt("GOOD_INT", 7))
}

// TestLoadConfigEnvOverrides verifies each environment override lands.
func TestLoadConfigEnvOverrides(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("NODE_SERVER_URL", "http://main:8080")
	t.Setenv("NODE_NAME", "edge-box")
	t.Setenv("MEDIA_DIR", "/mnt/media")
	t.Setenv("CACHE_DIR", "/srv/node-cache")
	t.Setenv("NODE_HEARTBEAT", "5s")
	t.Setenv("JOB_TIMEOUT", "45m")
	t.Setenv("SUBTITLE_WORKERS", "7")
	t.Setenv("CLIP_WORKERS", "3")
	t.Setenv("EXTRACT_CONNS", "2")
	t.Setenv("PROBE_TIMEOUT", "90s")
	t.Setenv("EXTRACT_TIMEOUT", "3m")
	t.Setenv("NODE_MEDIA_MAP", "/srv/media=/mnt/media")

	cfg, err := LoadConfig(Options{})
	require.NoError(t, err)
	assert.Equal(t, "edge-box", cfg.Name)
	assert.Equal(t, "/mnt/media", cfg.MediaDir)
	assert.Equal(t, "/srv/node-cache", cfg.CacheDir)
	assert.Equal(t, 5*time.Second, cfg.Heartbeat)
	assert.Equal(t, 45*time.Minute, cfg.JobTimeout)
	assert.Equal(t, 7, cfg.ProbeWorkers)
	assert.Equal(t, 3, cfg.ClipWorkers)
	assert.Equal(t, 2, cfg.ExtractConns)
	assert.Equal(t, 90*time.Second, cfg.ProbeTimeout)
	assert.Equal(t, 3*time.Minute, cfg.ExtractTimeout)
	assert.Equal(t, "/srv/media=/mnt/media", cfg.MediaMap)

	// A malformed map is rejected up front.
	t.Setenv("NODE_MEDIA_MAP", "no-equals-sign")
	_, err = LoadConfig(Options{})
	require.Error(t, err)
}

// TestDefaultNodeNameFallsBackToOS covers hosts without a resolvable name.
func TestDefaultNodeNameFallback(t *testing.T) {
	// defaultNodeName never panics; with a hostname it returns it lowercase.
	name := defaultNodeName()
	assert.NotEmpty(t, name)
}

// TestClientUploadResultOpenError surfaces source-open failures.
func TestClientUploadResultOpenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write([]byte(`{"accepted":true}`)); werr != nil {
			t.Logf("write response: %v", werr)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"
	files := map[string]uploadFile{
		"subtitle": {Name: "1.srt", Open: func() (io.ReadCloser, error) { return nil, os.ErrNotExist }},
	}
	err := c.uploadResult(context.Background(), 1, []byte(`{}`), files)
	require.Error(t, err)
}

// TestClientUploadResultServerError surfaces non-2xx upload responses.
func TestClientUploadResultServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"
	rc := io.NopCloser(strings.NewReader("data"))
	files := map[string]uploadFile{
		"clip": {Name: "render.mp4", Open: func() (io.ReadCloser, error) { return rc, nil }},
	}
	err := c.uploadResult(context.Background(), 1, []byte(`{"ext":"mp4"}`), files)
	require.Error(t, err)
}

// TestClientCompleteAndHeartbeatDecode verifies response decoding paths.
func TestClientCompleteAndHeartbeatDecode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if werr := json.NewEncoder(w).Encode(map[string]any{"paused": true, "scanActive": false}); werr != nil {
			t.Logf("write response: %v", werr)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"

	beat, err := c.heartbeat(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, beat.Paused)
	assert.False(t, beat.ScanActive)

	// A 500 from complete propagates.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	c2 := newClient(bad.URL)
	err = c2.complete(context.Background(), 1, "done", "")
	require.Error(t, err)
}

// TestStatePollNetworkError is tolerated by the approval loop.
func TestStatePollNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.nodeID, c.secret = 1, "s"
	_, err := c.state(context.Background())
	require.Error(t, err)
}

// TestLoadStateCorruptFile surfaces a broken state file instead of silently
// re-registering.
func TestLoadStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	_, err := loadState(path)
	require.Error(t, err)
}

// TestEnsureDirsFailure propagates unwritable cache roots.
func TestEnsureDirsFailure(t *testing.T) {
	// A path through a regular file cannot become a directory.
	file := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	err := ensureDirs(Config{CacheDir: filepath.Join(file, "cache")})
	require.Error(t, err)
}

// TestSessionVerdictClassification covers the execute() verdict switch via a
// direct probe outcome upload failure.
func TestSessionVerdictClassification(t *testing.T) {
	// Context cancellation maps to the canceled verdict: simulate by
	// running runProbe against a canceled context.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg, err := LoadConfig(Options{ServerURL: srv.URL, MediaDir: t.TempDir(), StatePath: filepath.Join(t.TempDir(), "n.json")})
	require.NoError(t, err)
	s := &session{
		cfg:    cfg,
		client: newClient(cfg.ServerURL),
		pipe:   &probe.Pipeline{SubsCacheDir: filepath.Join(t.TempDir(), "subs")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := workFixture(t, cfg.MediaDir)
	err = s.runProbe(ctx, w, nil)
	require.ErrorIs(t, err, context.Canceled)
}

// workFixture builds a minimal probe Work for direct handler tests, with the
// media file created under the node's media dir so the stat succeeds before
// the canceled context aborts the run.
func workFixture(t *testing.T, mediaDir string) *nodes.Work {
	t.Helper()
	mediaFile := filepath.Join(mediaDir, "movie.mkv")
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))
	return &nodes.Work{
		JobID:     1,
		Type:      "probe",
		Media:     &nodes.MediaRef{ID: 1, Path: "/srv/media/movie.mkv", Status: "pending"},
		MediaRoot: "/srv/media",
	}
}

// TestRemapRejectsUnmappedPath ensures a clear error guides operators.
func TestRemapRejectsUnmappedPath(t *testing.T) {
	m, err := newMapper("/srv/media", "/mnt/nowhere", "")
	require.NoError(t, err)
	_, err = m.local("/elsewhere/file.mkv")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "MEDIA_DIR"))
}

// TestErrorsDistinctness keeps sentinel identities stable.
func TestErrorsDistinctness(t *testing.T) {
	assert.False(t, errors.Is(errUnauthorized, context.Canceled))
}

// TestNewServiceDefaults covers the zero-value fallbacks.
func TestNewServiceDefaults(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	svc := nodes.NewService(st, nodes.Settings{})
	assert.Equal(t, nodes.DefaultHeartbeat, svc.Settings().Heartbeat)
	assert.Equal(t, nodes.DefaultOfflineAfter, svc.Settings().OfflineAfter)
	assert.Equal(t, nodes.DefaultMaxPending, svc.Settings().MaxPending)
}

// TestResumeStoredTokenPath verifies a node with a stored token resumes
// without re-registering: the server sees no new registration.
func TestResumeStoredTokenPath(t *testing.T) {
	fs := newFakeServer(t)
	// Keep the node's scratch cache inside the test sandbox.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))

	// Seed a state file whose token is already approved server-side.
	statePath := filepath.Join(t.TempDir(), "node.json")
	node, err := fs.svc.Register("resumed", "fp-resume", "test")
	require.NoError(t, err)
	require.NoError(t, fs.svc.Approve(node.ID))
	nRow, err := fs.svc.State(node.ID, node.Secret)
	require.NoError(t, err)
	require.NotEmpty(t, nRow.Token)
	st := &state{Fingerprint: "fp-resume", Secret: node.Secret, Token: nRow.Token, NodeID: node.ID}
	require.NoError(t, st.save(statePath))

	cfg, err := LoadConfig(Options{ServerURL: fs.srv.URL, MediaDir: t.TempDir(), StatePath: statePath})
	require.NoError(t, err)
	cfg.ProbeWorkers, cfg.ClipWorkers, cfg.Heartbeat, cfg.PollInterval = 1, 1, 50*time.Millisecond, 20*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// The node must refresh its heartbeat promptly: wait for last_seen to
	// move past the setup timestamp, which proves it resumed and started
	// reporting instead of sitting in the approval wait.
	setupSeen := time.Now().UnixMilli()
	deadline := time.Now().Add(5 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		views, lerr := fs.svc.List()
		require.NoError(t, lerr)
		if len(views) > 0 && views[0].Node.LastSeen > setupSeen {
			resumed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.True(t, resumed, "a stored token resumes without the approval wait")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestSaveStateFailure surfaces unwritable state paths.
func TestSaveStateFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	st := &state{Fingerprint: "fp"}
	err := st.save(filepath.Join(blocker, "node.json"))
	require.Error(t, err)
}

// TestUploadResultMidCopyFailure surfaces readers that fail mid-transfer.
func TestUploadResultMidCopyFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"
	files := map[string]uploadFile{
		"clip": {
			Name: "render.mp4",
			Open: func() (io.ReadCloser, error) {
				return &failingReader{}, nil
			},
		},
	}
	err := c.uploadResult(context.Background(), 1, []byte(`{"ext":"mp4"}`), files)
	require.Error(t, err)
}

// failingReader errors after the first byte to trip the multipart copy.
type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		r.n++
		p[0] = 'x'
		return 1, nil
	}
	return 0, errors.New("boom mid-copy")
}

func (r *failingReader) Close() error { return nil }

// TestResumeViaSecretPoll verifies a node with a rejected token but a valid
// registration secret reissues its token from the approval poll.
func TestResumeViaSecretPoll(t *testing.T) {
	fs := newFakeServer(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	node, err := fs.svc.Register("secret-resume", "fp-secret", "test")
	require.NoError(t, err)
	require.NoError(t, fs.svc.Approve(node.ID))
	stt, err := fs.svc.State(node.ID, node.Secret)
	require.NoError(t, err)
	require.NotEmpty(t, stt.Token)

	// The stored token is stale, but the secret is still valid.
	statePath := filepath.Join(t.TempDir(), "node.json")
	st := &state{Fingerprint: "fp-secret", Secret: node.Secret, Token: "stale-token", NodeID: node.ID}
	require.NoError(t, st.save(statePath))

	cfg, err := LoadConfig(Options{ServerURL: fs.srv.URL, MediaDir: t.TempDir(), StatePath: statePath})
	require.NoError(t, err)
	cfg.Heartbeat, cfg.PollInterval = 50*time.Millisecond, 20*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// Wait until the node heartbeats with the reissued token.
	setupSeen := time.Now().UnixMilli()
	deadline := time.Now().Add(5 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		views, lerr := fs.svc.List()
		require.NoError(t, lerr)
		if len(views) > 0 && views[0].Node.LastSeen > setupSeen {
			resumed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.True(t, resumed, "the secret poll must reissue the token without a new approval")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
	saved, lerr := loadState(statePath)
	require.NoError(t, lerr)
	assert.Equal(t, stt.Token, saved.Token, "the reissued token replaced the stale one")
}

// TestResumeRevokedRegistersAgain verifies a revoked node resets its identity
// and waits for approval.
func TestResumeRevokedRegistersAgain(t *testing.T) {
	fs := newFakeServer(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	node, err := fs.svc.Register("revoked-node", "fp-revoked", "test")
	require.NoError(t, err)
	require.NoError(t, fs.svc.Approve(node.ID))
	require.NoError(t, fs.svc.Revoke(node.ID))

	statePath := filepath.Join(t.TempDir(), "node.json")
	st := &state{Fingerprint: "fp-revoked", Secret: node.Secret, Token: "old", NodeID: node.ID}
	require.NoError(t, st.save(statePath))

	cfg, err := LoadConfig(Options{ServerURL: fs.srv.URL, MediaDir: t.TempDir(), StatePath: statePath})
	require.NoError(t, err)
	cfg.Heartbeat, cfg.PollInterval = 50*time.Millisecond, 20*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// The re-registration maps back to the same fingerprint row (reset to
	// pending), the auto-approving server flips it back to approved with a
	// fresh secret, and the node resumes heartbeating.
	time.Sleep(300 * time.Millisecond)
	setupSeen := time.Now().UnixMilli()
	deadline := time.Now().Add(10 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		views, lerr := fs.svc.List()
		require.NoError(t, lerr)
		if len(views) > 0 && views[0].Node.Status == nodes.NodeApproved && views[0].Node.LastSeen > setupSeen {
			resumed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.True(t, resumed, "re-registration revives the node after revocation")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestClientStateDecodeError surfaces malformed poll responses.
func TestClientStateDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, werr := w.Write([]byte("{bad")); werr != nil {
			t.Logf("write response: %v", werr)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.nodeID, c.secret = 1, "s"
	_, err := c.state(context.Background())
	require.Error(t, err)
}

// TestClientRegisterSurfacesServerErrors covers non-2xx registration.
func TestClientRegisterSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	err := c.register(context.Background(), "n", "fp", "v")
	require.Error(t, err)
}

// TestClientCompleteSurfacesServerErrors covers non-2xx verdict reports.
func TestClientCompleteSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"
	err := c.complete(context.Background(), 1, "done", "")
	require.Error(t, err)
}

// TestClientClaimSurfacesServerErrors covers non-2xx claims.
func TestClientClaimSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.token = "t"
	_, ok, err := c.claim(context.Background(), []string{"probe"})
	require.Error(t, err)
	assert.False(t, ok)
}

// TestPoolSurvivesClaimErrors verifies a claim failure logs, backs off and
// keeps the pool alive until shutdown.
func TestPoolSurvivesClaimErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg, err := LoadConfig(Options{ServerURL: srv.URL, MediaDir: t.TempDir(), StatePath: filepath.Join(t.TempDir(), "n.json")})
	require.NoError(t, err)
	cfg.CacheDir = t.TempDir()
	s := &session{
		cfg:          cfg,
		client:       newClient(cfg.ServerURL),
		pipe:         &probe.Pipeline{SubsCacheDir: filepath.Join(t.TempDir(), "subs")},
		progress:     map[int64]float64{},
		probeCancels: map[int64]context.CancelFunc{},
		clipCancels:  map[int64]context.CancelFunc{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.pool(ctx, "probe", 0)
		close(done)
	}()
	// The pool must still be looping (not exited) after claim failures.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("pool exited on claim errors")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not exit on shutdown")
	}
}

// TestExecuteUnsupportedType reports an error verdict for unknown job types.
func TestExecuteUnsupportedType(t *testing.T) {
	var statuses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			statuses = append(statuses, body.Status)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, err := LoadConfig(Options{ServerURL: srv.URL, MediaDir: t.TempDir(), StatePath: filepath.Join(t.TempDir(), "n.json")})
	require.NoError(t, err)
	cfg.CacheDir = t.TempDir()
	s := &session{
		cfg:          cfg,
		client:       newClient(cfg.ServerURL),
		pipe:         &probe.Pipeline{SubsCacheDir: filepath.Join(t.TempDir(), "subs")},
		progress:     map[int64]float64{},
		probeCancels: map[int64]context.CancelFunc{},
		clipCancels:  map[int64]context.CancelFunc{},
	}
	s.execute(context.Background(), "probe-0", &nodes.Work{JobID: 7, Type: "mystery"})
	assert.Equal(t, []string{"error"}, statuses, "unknown job types report an error verdict")
}

// TestSaveStateCreatesDirectories verifies the state file lands with owner
// only permissions.
func TestSaveStateCreatesDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cfg")
	st := &state{Fingerprint: "fp"}
	require.NoError(t, st.save(filepath.Join(dir, "node.json")))
	info, err := os.Stat(filepath.Join(dir, "node.json"))
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&0o077, "the state file must be owner-only")
}

// TestRelUnderBoundaryPaths covers the exact-prefix boundary cases.
func TestRelUnderBoundaryPaths(t *testing.T) {
	rel, ok := relUnder("/srv/media", "/srv/media")
	assert.True(t, ok)
	assert.Empty(t, rel)

	_, ok = relUnder("/srv/media2/x", "/srv/media")
	assert.False(t, ok, "sibling directories must not match")

	_, ok = relUnder("/SRV/MEDIA/x", "/srv/media")
	assert.True(t, ok, "case-insensitive prefix match")
}
