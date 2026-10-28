package node

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/nodes"
	"quipster/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecoverAuthorizationOnRevocation verifies the node re-registers (and
// waits for approval again) after its token stops being accepted.
func TestRecoverAuthorizationOnRevocation(t *testing.T) {
	fs := newFakeServer(t)

	id, _, err := fs.st.UpsertMedia("/srv/media/movie.mkv", "matroska", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	_, err = fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 1)
	require.NoError(t, err)

	nodeMedia := t.TempDir()
	mediaFile := filepath.Join(nodeMedia, "library", "movie.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(mediaFile), 0o750))
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))

	binDir := t.TempDir()
	// Broken ffprobe mock: claims still exercise the full lifecycle.
	t.Setenv("FFPROBE_BIN", filepath.Join(binDir, "missing-ffprobe"))

	cfg := nodeConfig(t, fs.srv.URL, nodeMedia)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// Once running, revoke the node's token server-side; the next claim
	// must 401, triggering re-registration and the approval wait.
	deadline := time.Now().Add(10 * time.Second)
	var nodeID int64
	for time.Now().Before(deadline) {
		list, lerr := fs.svc.List()
		require.NoError(t, lerr)
		if len(list) > 0 && list[0].Node.Status == nodes.NodeApproved {
			nodeID = list[0].Node.ID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotZero(t, nodeID, "node never became approved")

	// Give the node a moment to reach the claim loop, then revoke.
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, fs.svc.Revoke(nodeID))

	// After revocation the node re-registers (the fingerprint maps back to
	// the same row, reset to pending), the fake server auto-approves, and
	// the node resumes claiming: enqueue a second job and watch it finish.
	job2, err := fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 1)
	require.NoError(t, err)
	deadline = time.Now().Add(20 * time.Second)
	for {
		j, jerr := fs.st.JobByID(job2)
		require.NoError(t, jerr)
		if j.Status == "done" || time.Now().After(deadline) {
			assert.Equal(t, "done", j.Status, "node should recover and finish post-revocation work")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestHeartbeatPauseCancelsInFlight verifies that a server-side queue pause
// cancels an in-flight job and the node reports it as canceled.
func TestHeartbeatPauseCancelsInFlight(t *testing.T) {
	fs := newFakeServer(t)

	id, _, err := fs.st.UpsertMedia("/srv/media/library/slow.mkv", "matroska", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	jobID, err := fs.st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, id), 1)
	require.NoError(t, err)

	nodeMedia := t.TempDir()
	mediaFile := filepath.Join(nodeMedia, "library", "slow.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(mediaFile), 0o750))
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake"), 0o600))

	binDir := t.TempDir()
	// ffprobe mock that sleeps long enough to be canceled mid-run; exec
	// replaces the shell so the context kill reaches the sleeping process
	// directly instead of an orphaned child holding the output pipes.
	t.Setenv("FFPROBE_BIN", writeMockBin(t, binDir, "ffprobe", "exec sleep 30\n"))
	t.Setenv("FFMPEG_BIN", writeMockBin(t, binDir, "ffmpeg", "exit 0\n"))

	cfg := nodeConfig(t, fs.srv.URL, nodeMedia)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test-version") }()

	// Wait until the job is running, then pause the queue.
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, jerr := fs.st.JobByID(jobID)
		require.NoError(t, jerr)
		if j.Status == "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, fs.st.SetQueuePaused(true))

	// The node's next heartbeat sees paused=true, cancels the job, and the
	// server requeues it.
	deadline = time.Now().Add(15 * time.Second)
	for {
		j, jerr := fs.st.JobByID(jobID)
		require.NoError(t, jerr)
		if j.Status == "queued" || time.Now().After(deadline) {
			assert.Equal(t, "queued", j.Status, "paused queue must requeue the in-flight job")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("node did not shut down")
	}
}

// TestCompleteRetryUntilAccepted verifies the reportComplete retry loop
// delivers verdicts across transient server failures.
func TestCompleteRetryUntilAccepted(t *testing.T) {
	attempts := 0
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write([]byte(`{"accepted":true}`)); werr != nil {
			t.Logf("write response: %v", werr)
		}
	}))
	defer inner.Close()

	cfg, err := LoadConfig(Options{ServerURL: inner.URL, StatePath: filepath.Join(t.TempDir(), "n.json")})
	require.NoError(t, err)
	cfg.CacheDir = t.TempDir()

	s := &session{cfg: cfg, client: newClient(cfg.ServerURL)}
	done := make(chan struct{})
	go func() {
		s.reportComplete(context.Background(), "probe-0", 42, "done", "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reportComplete did not converge")
	}
	assert.Equal(t, 3, attempts, "verdict retried until the server accepted it")
}

// TestStoreLookupFailureIsUnauthorized verifies error propagation through
// Authenticate.
func TestStoreLookupFailureIsUnauthorized(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	svc := nodes.NewService(st, nodes.Settings{})
	require.NoError(t, st.Close())
	_, err = svc.Authenticate("tok")
	require.Error(t, err, "a broken store surfaces as an auth error, not a panic")
}
