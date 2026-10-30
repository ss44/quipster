package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNodeRegistrationCycle covers registration idempotency, approval,
// token-based lookup, revocation and deletion.
func TestNodeRegistrationCycle(t *testing.T) {
	st := openTest(t)

	n, err := st.UpsertNode("gpu-box", "fp-1", "dev", "secret-1")
	require.NoError(t, err)
	require.NotNil(t, n)
	assert.Equal(t, int64(1), n.ID)
	assert.Equal(t, "gpu-box", n.Name)
	assert.Equal(t, NodePending, n.Status)
	assert.Equal(t, "secret-1", n.Secret)
	assert.Empty(t, n.Token)

	// Re-registration with the same fingerprint rotates the secret and
	// demotes an approved node back to pending.
	require.NoError(t, st.ApproveNode(n.ID, "token-1"))
	got, err := st.NodeByToken("token-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, n.ID, got.ID)

	n2, err := st.UpsertNode("gpu-box", "fp-1", "v2", "secret-2")
	require.NoError(t, err)
	assert.Equal(t, n.ID, n2.ID)
	assert.Equal(t, "secret-2", n2.Secret)
	assert.Equal(t, NodePending, n2.Status)

	lost, err := st.NodeByToken("token-1")
	require.NoError(t, err)
	assert.Nil(t, lost, "re-registration must clear the old token")

	require.NoError(t, st.ApproveNode(n.ID, "token-2"))
	revived, err := st.NodeByToken("token-2")
	require.NoError(t, err)
	require.NotNil(t, revived)

	require.NoError(t, st.RevokeNode(n.ID))
	revoked, err := st.NodeByToken("token-2")
	require.NoError(t, err)
	assert.Nil(t, revoked, "revoked tokens must stop authorizing")

	byFP, err := st.NodeByFingerprint("fp-1")
	require.NoError(t, err)
	require.NotNil(t, byFP)
	assert.Equal(t, NodeRevoked, byFP.Status)

	require.NoError(t, st.DeleteNode(n.ID))
	gone, err := st.NodeByID(n.ID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}

// TestNodeCountersAndList covers heartbeat touches, counters and listing.
func TestNodeCountersAndList(t *testing.T) {
	st := openTest(t)

	a, err := st.UpsertNode("a", "fp-a", "dev", "s-a")
	require.NoError(t, err)
	b, err := st.UpsertNode("b", "fp-b", "dev", "s-b")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(b.ID, "t-b"))

	require.NoError(t, st.TouchNode(a.ID))
	require.NoError(t, st.BumpNodeCounters(a.ID, false))
	require.NoError(t, st.BumpNodeCounters(a.ID, false))
	require.NoError(t, st.BumpNodeCounters(a.ID, true))

	nodes, err := st.ListNodes()
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, int64(2), nodes[0].JobsDone)
	assert.Equal(t, int64(1), nodes[0].JobsFailed)
	assert.Positive(t, nodes[0].LastSeen)

	pending, err := st.CountPendingNodes()
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending, "only node a is pending")

	unknown, err := st.NodeByToken("nope")
	require.NoError(t, err)
	assert.Nil(t, unknown)
	assert.Equal(t, b.ID, int64(2))
}

// TestNodeWorkerIDRoundTrip checks the worker id encoding used for claims.
func TestNodeWorkerIDRoundTrip(t *testing.T) {
	assert.Equal(t, "node-7", NodeWorkerID(7))
	assert.Equal(t, int64(7), NodeIDFromWorker("node-7"))
	assert.Zero(t, NodeIDFromWorker("worker-probe-0"))
	assert.Zero(t, NodeIDFromWorker("node-notanumber"))
}

// TestRequeueStaleNodeJobs verifies the janitor query: only running node
// claims of stale or deleted nodes are requeued, with the claim attempt
// refunded, and local worker jobs are never touched.
func TestRequeueStaleNodeJobs(t *testing.T) {
	st := openTest(t)
	staleNode, err := st.UpsertNode("stale", "fp-stale", "dev", "sec")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(staleNode.ID, "tok-stale"))
	freshNode, err := st.UpsertNode("fresh", "fp-fresh", "dev", "sec")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(freshNode.ID, "tok-fresh"))
	require.NoError(t, st.TouchNode(freshNode.ID))

	staleJob, err := st.EnqueueJob("probe", `{"mediaId":1}`, 1)
	require.NoError(t, err)
	ghostJob, err := st.EnqueueJob("probe", `{"mediaId":2}`, 1)
	require.NoError(t, err)
	localJob, err := st.EnqueueJob("probe", `{"mediaId":3}`, 1)
	require.NoError(t, err)
	freshJob, err := st.EnqueueJob("probe", `{"mediaId":4}`, 1)
	require.NoError(t, err)

	claim := func(worker string, want int64) {
		t.Helper()
		j, cerr := st.ClaimJob(t.Context(), worker, []string{"probe"})
		require.NoError(t, cerr)
		require.NotNil(t, j)
		assert.Equal(t, want, j.ID)
	}
	claim(NodeWorkerID(staleNode.ID), staleJob)
	claim(NodeWorkerID(freshNode.ID+999), ghostJob) // deleted/unknown node row
	claim("worker-probe-0", localJob)
	claim(NodeWorkerID(freshNode.ID), freshJob)

	// Push the stale node's last_seen backwards; the fresh node stays live.
	old := nowMillis() - 10_000
	_, err = st.DB().Exec(`UPDATE nodes SET last_seen=? WHERE id=?`, old, staleNode.ID)
	require.NoError(t, err)

	requeued, err := st.RequeueStaleNodeJobs(nowMillis()-5_000, "node offline")
	require.NoError(t, err)
	assert.Equal(t, int64(2), requeued, "stale and ghost claims requeue; fresh and local do not")

	for _, id := range []int64{staleJob, ghostJob} {
		status, worker, oerr := st.JobOwner(id)
		require.NoError(t, oerr)
		assert.Equal(t, "queued", status, "job %d", id)
		assert.Empty(t, worker)
	}
	status, worker, err := st.JobOwner(freshJob)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.Equal(t, NodeWorkerID(freshNode.ID), worker)
	status, worker, err = st.JobOwner(localJob)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.Equal(t, "worker-probe-0", worker)

	// Deleting the node releases its remaining lease.
	require.NoError(t, st.DeleteNode(freshNode.ID))
	requeued, err = st.RequeueStaleNodeJobs(nowMillis()-5_000, "node deleted")
	require.NoError(t, err)
	assert.Equal(t, int64(1), requeued)
	status, _, err = st.JobOwner(freshJob)
	require.NoError(t, err)
	assert.Equal(t, "queued", status)
}

// TestRequeueMissingNodeJobs verifies heartbeat reconciliation: running jobs
// the node stopped reporting are requeued, while reported jobs and claims
// younger than the grace cutoff are left alone.
func TestRequeueMissingNodeJobs(t *testing.T) {
	st := openTest(t)
	node, err := st.UpsertNode("n", "fp", "dev", "sec")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(node.ID, "tok"))

	reported, err := st.EnqueueJob("probe", `{"mediaId":1}`, 1)
	require.NoError(t, err)
	orphaned, err := st.EnqueueJob("probe", `{"mediaId":2}`, 1)
	require.NoError(t, err)
	fresh, err := st.EnqueueJob("probe", `{"mediaId":3}`, 1)
	require.NoError(t, err)
	claim := func(want int64) {
		t.Helper()
		j, cerr := st.ClaimJob(t.Context(), NodeWorkerID(node.ID), []string{"probe"})
		require.NoError(t, cerr)
		require.NotNil(t, j)
		assert.Equal(t, want, j.ID)
	}
	claim(reported)
	claim(orphaned)
	claim(fresh)

	// Make the fresh claim newer than the cutoff, mimicking a job queued after
	// the last heartbeat window.
	future := nowMillis() + 60_000
	_, err = st.DB().Exec(`UPDATE jobs SET started_at=? WHERE id=?`, future, fresh)
	require.NoError(t, err)

	requeued, err := st.RequeueMissingNodeJobs(node.ID, []int64{reported}, nowMillis()+1_000, "node no longer reports job")
	require.NoError(t, err)
	assert.Equal(t, int64(1), requeued, "only the unreported, older claim is requeued")

	status, owner, err := st.JobOwner(reported)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.Equal(t, NodeWorkerID(node.ID), owner)
	status, _, err = st.JobOwner(orphaned)
	require.NoError(t, err)
	assert.Equal(t, "queued", status)
	status, _, err = st.JobOwner(fresh)
	require.NoError(t, err)
	assert.Equal(t, "running", status, "fresh claim is inside the grace window")

	// An unknown node releases nothing even when it reports no jobs.
	requeued, err = st.RequeueMissingNodeJobs(node.ID+999, nil, nowMillis()+120_000, "node no longer reports job")
	require.NoError(t, err)
	assert.Zero(t, requeued)
}

// TestRunningNodeJobs verifies the dashboard query only returns node claims.
func TestRunningNodeJobs(t *testing.T) {
	st := openTest(t)
	node, err := st.UpsertNode("dash", "fp", "dev", "sec")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(node.ID, "tok"))

	j1, err := st.EnqueueJob("probe", `{"mediaId":11}`, 1)
	require.NoError(t, err)
	j2, err := st.EnqueueJob("clip", `{"mediaId":12}`, 1)
	require.NoError(t, err)
	j3, err := st.EnqueueJob("index", `{"mediaId":13}`, 1)
	require.NoError(t, err)

	_, err = st.ClaimJob(t.Context(), NodeWorkerID(node.ID), []string{"probe"})
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), NodeWorkerID(node.ID), []string{"clip"})
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), "worker-index-0", []string{"index"})
	require.NoError(t, err)

	running, err := st.RunningNodeJobs()
	require.NoError(t, err)
	ids := make([]int64, 0, len(running))
	for _, j := range running {
		ids = append(ids, j.ID)
	}
	assert.ElementsMatch(t, []int64{j1, j2}, ids)
	assert.NotContains(t, ids, j3)
}

// TestRequeueNodeJobs verifies the per-node lease release used by node
// deletion.
func TestRequeueNodeJobs(t *testing.T) {
	st := openTest(t)
	node, err := st.UpsertNode("n", "fp", "dev", "sec")
	require.NoError(t, err)
	require.NoError(t, st.ApproveNode(node.ID, "tok"))

	j1, err := st.EnqueueJob("probe", `{"mediaId":1}`, 1)
	require.NoError(t, err)
	j2, err := st.EnqueueJob("probe", `{"mediaId":2}`, 1)
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), NodeWorkerID(node.ID), []string{"probe"})
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), "worker-probe-0", []string{"probe"})
	require.NoError(t, err)

	requeued, err := st.RequeueNodeJobs(node.ID, "node deleted")
	require.NoError(t, err)
	assert.Equal(t, int64(1), requeued, "only this node's lease is released")

	status, _, err := st.JobOwner(j1)
	require.NoError(t, err)
	assert.Equal(t, "queued", status)
	status, worker, err := st.JobOwner(j2)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.Equal(t, "worker-probe-0", worker)

	// An unknown node releases nothing.
	requeued, err = st.RequeueNodeJobs(node.ID+999, "node deleted")
	require.NoError(t, err)
	assert.Equal(t, int64(0), requeued)
}

// TestApproveNodeRejectsUnknown covers approval of a missing node row.
func TestApproveNodeRejectsUnknown(t *testing.T) {
	st := openTest(t)
	err := st.ApproveNode(424242, "tok")
	require.Error(t, err, "approving an unknown node must fail, not silently pass")
}
