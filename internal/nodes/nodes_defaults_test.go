package nodes

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/probe"
	"quipster/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewServiceDefaults covers the zero-value fallback branches.
func TestNewServiceDefaults(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})

	svc := NewService(st, Settings{})
	cfg := svc.Settings()
	assert.Equal(t, DefaultHeartbeat, cfg.Heartbeat)
	assert.Equal(t, DefaultOfflineAfter, cfg.OfflineAfter)
	assert.Equal(t, DefaultMaxPending, cfg.MaxPending)

	// Explicit values pass through untouched.
	svc2 := NewService(st, Settings{Heartbeat: time.Second, OfflineAfter: 2 * time.Second, MaxPending: 3})
	cfg2 := svc2.Settings()
	assert.Equal(t, time.Second, cfg2.Heartbeat)
	assert.Equal(t, 2*time.Second, cfg2.OfflineAfter)
	assert.Equal(t, 3, cfg2.MaxPending)
}

// TestServerInfoHeartbeatClamp keeps the reported cadence at least one second.
func TestServerInfoHeartbeatClamp(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	svc := NewService(st, Settings{Heartbeat: 300 * time.Millisecond})
	info := svc.serverInfo()
	assert.GreaterOrEqual(t, info.HeartbeatSecs, 1)
}

// TestHeartbeatSurfacesStoreErrors propagates store failures to the caller.
func TestHeartbeatSurfacesStoreErrors(t *testing.T) {
	svc, st := newTestService(t)
	require.NoError(t, st.Close())
	_, err := svc.Heartbeat(1, nil)
	require.Error(t, err, "a broken store must surface, not silently pass")
}

// TestJanitorLogsStoreErrors keeps sweeping after a failed sweep.
func TestJanitorLogsStoreErrors(t *testing.T) {
	svc, st := newTestService(t)
	require.NoError(t, st.Close())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Janitor(ctx, time.Millisecond)
		close(done)
	}()
	// The loop must survive store errors and exit on cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not exit")
	}
}

// TestRegisterHandlesStoreFailure propagates upsert errors.
func TestRegisterHandlesStoreFailure(t *testing.T) {
	svc, st := newTestService(t)
	require.NoError(t, st.Close())
	_, err := svc.Register("n", "fp", "")
	require.Error(t, err)
}

// TestApplyProbeResultStoreFailure surfaces apply errors.
func TestApplyProbeResultStoreFailure(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	require.NoError(t, st.Close())

	out := &probe.Outcome{Kind: probe.OutcomeOK, Meta: &probe.MediaMeta{}}
	_, err = svc.ApplyProbeResult(node.ID, jobID, out, nil)
	require.Error(t, err, "apply failures surface instead of being swallowed")
}

// TestCompleteUnknownJobIsSuperseded covers completes for jobs that never
// existed.
func TestCompleteUnknownJobIsSuperseded(t *testing.T) {
	svc, _ := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	accepted, err := svc.Complete(node.ID, 424242, "done", "")
	require.NoError(t, err)
	assert.False(t, accepted)
}
