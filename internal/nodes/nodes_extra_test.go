package nodes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"quipster/internal/clips"
	"quipster/internal/probe"
	"quipster/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthenticate covers token resolution paths.
func TestAuthenticate(t *testing.T) {
	svc, _ := newTestService(t)
	node, token := approvedNode(t, svc, "n", "fp")

	id, err := svc.Authenticate(token)
	require.NoError(t, err)
	assert.Equal(t, node.ID, id)

	_, err = svc.Authenticate("unknown-token")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// TestApproveAndRevokeLifecycle exercises the service-level admin actions.
func TestApproveAndRevokeLifecycle(t *testing.T) {
	svc, st := newTestService(t)
	n, err := svc.Register("n", "fp", "v")
	require.NoError(t, err)

	require.NoError(t, svc.Approve(n.ID))
	got, err := st.NodeByID(n.ID)
	require.NoError(t, err)
	assert.Equal(t, store.NodeApproved, got.Status)
	assert.NotEmpty(t, got.Token)

	// Approving again conflicts: the node is not pending.
	require.Error(t, svc.Approve(n.ID))

	// Revoking clears the token.
	require.NoError(t, svc.Revoke(n.ID))
	got, err = st.NodeByID(n.ID)
	require.NoError(t, err)
	assert.Equal(t, store.NodeRevoked, got.Status)
	assert.Empty(t, got.Token)
}

// TestRegisterTruncatesAndDefaults covers name defaults.
func TestRegisterTruncatesAndDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	n, err := svc.Register("", "fp", "")
	require.NoError(t, err)
	assert.Equal(t, "node", n.Name, "blank names default to a placeholder")

	_, err = svc.Register("x", "", "")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// TestStatePendingCapAndRevoked covers the revoked state response.
func TestStatePendingCapAndRevoked(t *testing.T) {
	svc, _ := newTestService(t)
	n, err := svc.Register("n", "fp", "")
	require.NoError(t, err)
	// Force the node to approved then revoked; state reports revoked.
	require.NoError(t, svc.Approve(n.ID))
	require.NoError(t, svc.Revoke(n.ID))
	stt, err := svc.State(n.ID, n.Secret)
	require.NoError(t, err)
	assert.Equal(t, store.NodeRevoked, stt.Status)
	assert.Empty(t, stt.Token)
	assert.Nil(t, stt.Server)
}

// TestClaimUnknownOrRevokedNode rejects claims for non-approved nodes.
func TestClaimUnknownOrRevokedNode(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Claim(context.Background(), 999, []string{"probe"})
	require.ErrorIs(t, err, ErrUnauthorized)

	n, err := svc.Register("n", "fp", "")
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), n.ID, []string{"probe"})
	require.ErrorIs(t, err, ErrUnauthorized, "pending nodes cannot claim")
}

// TestClaimOnlyEligibleTypes returns no work when all requested types are
// filtered out.
func TestClaimOnlyEligibleTypes(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	_, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"scan", "index"})
	require.NoError(t, err)
	assert.Nil(t, w, "server-only job types are not claimable by nodes")
}

// TestClaimBadPayloadFinalizesJob covers malformed payload handling.
func TestClaimBadPayloadFinalizesJob(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	jobID, err := st.EnqueueJob("probe", `not-json`, 1)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	assert.Nil(t, w)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "error", j.Status)
	assert.Equal(t, "bad job payload", j.Error)
}

// TestClaimBadClipPayloadFinalizesJob covers a clip job with a broken payload.
func TestClaimBadClipPayloadFinalizesJob(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":"bad"}`, 1)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)
	assert.Nil(t, w)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "error", j.Status)
	assert.Equal(t, "bad clip payload", j.Error)
}

// TestCompleteUnknownStatus covers the guard against malformed verdicts.
func TestCompleteUnknownStatus(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	accepted, err := svc.Complete(node.ID, jobID, "exploded", "")
	require.Error(t, err)
	assert.True(t, accepted, "ownership held; the status itself is the problem")
}

// TestApplyProbeResultGuards covers payload/media lookup failures.
func TestApplyProbeResultGuards(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")

	// Media row vanished between claim and result.
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	require.NoError(t, st.DeleteMediaIDs([]int64{id}))
	out := &probe.Outcome{Kind: probe.OutcomeNoSubs, Meta: &probe.MediaMeta{}}
	accepted, err := svc.ApplyProbeResult(node.ID, jobID, out, nil)
	require.NoError(t, err)
	assert.False(t, accepted, "vanished media finishes the lease as errored, not applied")

	// A held lease with a nil outcome is a caller error.
	id2 := addMedia(t, st, "/srv/media/other.mkv")
	job2, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id2)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	accepted, err = svc.ApplyProbeResult(node.ID, job2, nil, nil)
	require.Error(t, err)
	assert.True(t, accepted)
}

// TestApplyClipResultGuards covers the clip-specific failure paths.
func TestApplyClipResultGuards(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":1,"end":2}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)

	// No clip service configured.
	svc.Clips = nil
	accepted, err := svc.ApplyClipResult(node.ID, jobID, "mp4", strings.NewReader("x"))
	require.Error(t, err)
	assert.True(t, accepted)

	// Bad payload discovered at result time: the lease was claimed with a
	// good payload, then the row was corrupted underneath it.
	svc.Clips = clips.New(st, clips.Config{CacheDir: t.TempDir(), MaxClipSec: 120, DefaultPadMs: 250})
	job2, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":1,"end":2}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)
	_, err = st.DB().Exec(`UPDATE jobs SET payload='{bad' WHERE id=?`, job2)
	require.NoError(t, err)
	accepted, err = svc.ApplyClipResult(node.ID, job2, "mp4", strings.NewReader("x"))
	require.Error(t, err)
	assert.True(t, accepted)
}

// TestJanitorIgnoresFreshNodes verifies the janitor loop is a no-op when
// every node heartbeats.
func TestJanitorIgnoresFreshNodes(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n", "fp")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Janitor(ctx, 5*time.Millisecond); close(done) }()
	// Keep the node fresh while the janitor ticks.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		require.NoError(t, st.TouchNode(node.ID))
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "running", j.Status, "a live node keeps its lease")
}

// TestErrorSentinelsAreDistinct guards the sentinel identity contract.
func TestErrorSentinelsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrTooManyPending, ErrUnauthorized))
	assert.False(t, errors.Is(ErrNotPending, ErrUnauthorized))
}
