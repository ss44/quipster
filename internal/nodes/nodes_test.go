package nodes

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quipster/internal/clips"
	"quipster/internal/probe"
	"quipster/internal/store"
	"quipster/internal/subs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestService builds a service over a throwaway store with fast cadences.
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	svc := NewService(st, Settings{
		MediaRoot:    "/srv/media",
		SubsCacheDir: t.TempDir(),
		Clips:        ClipSettings{PreviewRes: "640:-2", AccurateRes: "1280:-2", MaxClipSec: 120, DefaultPadMs: 250},
		Heartbeat:    10 * time.Second,
		OfflineAfter: time.Second,
		MaxPending:   2,
		Version:      "test",
	})
	svc.Pipeline = &probe.Pipeline{St: st, SubsCacheDir: filepath.Join(t.TempDir(), "subs")}
	return svc, st
}

// approvedNode registers and approves a node, returning it with its token.
func approvedNode(t *testing.T, svc *Service, name, fp string) (*store.Node, string) {
	t.Helper()
	n, err := svc.Register(name, fp, "test")
	require.NoError(t, err)
	token := "tok-" + fp
	require.NoError(t, svc.st.ApproveNode(n.ID, token))
	return n, token
}

func addMedia(t *testing.T, st *store.Store, path string) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia(path, "mkv", 1, 1, "T", 0, 0, 0, "")
	require.NoError(t, err)
	return id
}

func TestRegisterAndStateFlow(t *testing.T) {
	svc, _ := newTestService(t)

	n, err := svc.Register("gpu-box", "fp-1", "1.2.3")
	require.NoError(t, err)
	assert.Equal(t, store.NodePending, n.Status)
	assert.NotEmpty(t, n.Secret)

	// Wrong secret is rejected.
	_, err = svc.State(n.ID, "wrong")
	require.ErrorIs(t, err, ErrUnauthorized)

	// Pending state carries no token.
	st, err := svc.State(n.ID, n.Secret)
	require.NoError(t, err)
	assert.Equal(t, store.NodePending, st.Status)
	assert.Empty(t, st.Token)
	assert.Nil(t, st.Server)

	require.NoError(t, svc.st.ApproveNode(n.ID, "issued-token"))
	st, err = svc.State(n.ID, n.Secret)
	require.NoError(t, err)
	assert.Equal(t, store.NodeApproved, st.Status)
	assert.Equal(t, "issued-token", st.Token)
	require.NotNil(t, st.Server)
	assert.Equal(t, "/srv/media", st.Server.MediaRoot)
	assert.Equal(t, 10, st.Server.HeartbeatSecs)
	assert.Equal(t, "test", st.Server.Version)
	assert.Equal(t, "640:-2", st.Server.ClipSettings.PreviewRes)

	// Unknown node id is unauthorized.
	_, err = svc.State(999, "x")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestRegisterCapsPending(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register("a", "fp-a", "")
	require.NoError(t, err)
	_, err = svc.Register("b", "fp-b", "")
	require.NoError(t, err)
	_, err = svc.Register("c", "fp-c", "")
	require.ErrorIs(t, err, ErrTooManyPending)

	// Approved nodes do not count against the cap.
	_, err = svc.Register("d", "fp-d", "")
	require.ErrorIs(t, err, ErrTooManyPending)
	n3, err := svc.st.NodeByFingerprint("fp-c")
	require.NoError(t, err)
	_ = n3
}

func TestRegisterSanitizesInput(t *testing.T) {
	svc, _ := newTestService(t)
	n, err := svc.Register(strings.Repeat("x", 200), strings.Repeat("f", 200), strings.Repeat("v", 200))
	require.NoError(t, err)
	assert.Len(t, n.Name, 64)
	assert.Len(t, n.Version, 32)

	_, err = svc.Register("", "", "")
	require.ErrorIs(t, err, ErrUnauthorized, "missing fingerprint must be rejected")
}

func TestClaimProbeWork(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/library/movie.mkv")

	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 2)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"probe", "index"})
	require.NoError(t, err)
	require.NotNil(t, w, "index is filtered out; probe must claim")
	assert.Equal(t, jobID, w.JobID)
	assert.Equal(t, "probe", w.Type)
	require.NotNil(t, w.Media)
	assert.Equal(t, id, w.Media.ID)
	assert.Equal(t, "/srv/media/library/movie.mkv", w.Media.Path)
	assert.Equal(t, "pending", w.Media.Status)
	assert.Equal(t, "/srv/media", w.MediaRoot)

	// The queue row shows the node worker id and consumed an attempt.
	status, owner, err := st.JobOwner(jobID)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.Equal(t, store.NodeWorkerID(node.ID), owner)

	// No more work.
	empty, err := svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	assert.Nil(t, empty)
}

// itoa is a tiny helper for building payloads in tests.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestClaimClipWork(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/library/movie.mkv")
	require.NoError(t, st.UpdateMediaProbe(id, 120, 1920, 1080, "h264", "aac", "mkv", 0, 3, "en"))

	jobID, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":5,"end":25,"padMs":0}`, 1)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)
	require.NotNil(t, w)
	require.NotNil(t, w.Clip)
	assert.Equal(t, jobID, w.JobID)
	assert.Equal(t, "accurate", w.Clip.Mode, "blank mode defaults to accurate")
	assert.Equal(t, 250, w.Clip.PadMs, "padMs<=0 falls back to the configured default")
	assert.Equal(t, 3, w.Clip.AudioOrdinal)
	assert.InDelta(t, 5, w.Clip.Start, 0.001)
	assert.InDelta(t, 25, w.Clip.End, 0.001)
}

func TestClaimMissingMediaFailsJob(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":424242}`, 1)
	require.NoError(t, err)

	w, err := svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	assert.Nil(t, w)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "error", j.Status)
	assert.Equal(t, "media row missing", j.Error)
}

func TestClaimPausedQueue(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	addMedia(t, st, "/srv/media/library/movie.mkv")
	_, err := st.EnqueueJob("probe", `{"mediaId":1}`, 1)
	require.NoError(t, err)
	require.NoError(t, st.SetQueuePaused(true))

	w, err := svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	assert.Nil(t, w)
}

func TestHeartbeatOwnershipAndState(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	other, _ := approvedNode(t, svc, "n2", "fp2")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	// Owned job progress applies; foreign or finished jobs are ignored.
	beat, err := svc.Heartbeat(node.ID, []JobProgress{{ID: jobID, Progress: 0.5}, {ID: 9999, Progress: 0.9}})
	require.NoError(t, err)
	assert.False(t, beat.Paused)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, j.Progress, 0.001)

	// Out-of-range progress is ignored.
	_, err = svc.Heartbeat(node.ID, []JobProgress{{ID: jobID, Progress: 1.5}})
	require.NoError(t, err)

	// Another node cannot move someone else's job.
	otherBeat, err := svc.Heartbeat(other.ID, []JobProgress{{ID: jobID, Progress: 0.9}})
	require.NoError(t, err)
	assert.False(t, otherBeat.Paused)
	j, err = st.JobByID(jobID)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, j.Progress, 0.001)

	require.NoError(t, st.SetQueuePaused(true))
	beat, err = svc.Heartbeat(node.ID, nil)
	require.NoError(t, err)
	assert.True(t, beat.Paused)
}

func TestCompleteDoneAndError(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	accepted, err := svc.Complete(node.ID, jobID, "done", "")
	require.NoError(t, err)
	assert.True(t, accepted)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	n, err := st.NodeByID(node.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n.JobsDone)
	assert.Equal(t, int64(0), n.JobsFailed)

	// A finished job no longer belongs to the node.
	accepted, err = svc.Complete(node.ID, jobID, "done", "")
	require.NoError(t, err)
	assert.False(t, accepted, "double completion is superseded")
}

func TestCompleteErrorRetriesThenFinalizes(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)

	// Burn the retry budget: 3 attempts (claims) each reporting an error.
	for i := 0; i < maxAttempts; i++ {
		w, cerr := svc.Claim(context.Background(), node.ID, []string{"probe"})
		require.NoError(t, cerr)
		require.NotNil(t, w)
		accepted, aerr := svc.Complete(node.ID, jobID, "error", "boom")
		require.NoError(t, aerr)
		assert.True(t, accepted)
	}
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "error", j.Status, "exhausted attempts finalize as error")
	assert.Equal(t, "boom", j.Error)
	n, err := st.NodeByID(node.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n.JobsFailed, "only the final failure counts")
}

func TestCompleteCanceledNotes(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	accepted, err := svc.Complete(node.ID, jobID, "canceled", "")
	require.NoError(t, err)
	assert.True(t, accepted)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "queued", j.Status)
	assert.Equal(t, "canceled", j.Error, "plain cancel notes as canceled")
	assert.Equal(t, 0, j.Attempts, "the claim's attempt is refunded on cancellation")

	// Pause turns the note into "paused".
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	require.NoError(t, st.SetQueuePaused(true))
	_, err = svc.Complete(node.ID, jobID, "canceled", "")
	require.NoError(t, err)
	j, err = st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "paused", j.Error)
	require.NoError(t, st.SetQueuePaused(false))

	// An active scan turns the note into "scan-active".
	_, err = st.EnqueueJob("scan", `{"roots":["a"],"prune":false}`, 3)
	require.NoError(t, err)
	scanJob, err := st.ClaimJob(context.Background(), "worker-scan-0", []string{"scan"})
	require.NoError(t, err)
	require.NotNil(t, scanJob)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	_, err = svc.Complete(node.ID, jobID, "canceled", "")
	require.NoError(t, err)
	j, err = st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "scan-active", j.Error)
}

func TestApplyProbeResult(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	out := &probe.Outcome{
		Kind:         probe.OutcomeOK,
		Meta:         &probe.MediaMeta{Duration: 30, Width: 640, Height: 360, VideoCodec: "h264", AudioCodec: "aac", Container: "mkv", StartTime: 0.5},
		AudioOrdinal: 1,
		AudioLang:    "en",
		Track:        &probe.TrackInfo{Source: "embedded", Lang: "en", StreamIndex: 2},
		Cues:         []subs.Cue{{Start: 1, End: 2, Text: "hi"}, {Start: 2.5, End: 4, Text: "there"}},
	}
	accepted, err := svc.ApplyProbeResult(node.ID, jobID, out, strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nhi\n"))
	require.NoError(t, err)
	assert.True(t, accepted)

	m, err := st.GetMedia(id)
	require.NoError(t, err)
	assert.Equal(t, "pending", m.Status, "media stays pending until indexing finishes")
	assert.InDelta(t, 30.0, m.DurationSec, 0.001)
	assert.Equal(t, 1, m.AudioStreamIndex)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	tracks, err := st.TracksForMedia(id)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	assert.Equal(t, "embedded", tracks[0].Source)
	assert.Equal(t, 2, tracks[0].StreamIndex)
	assert.Equal(t, filepath.Join(svc.Pipeline.SubsCacheDir, itoa(id)+".srt"), tracks[0].ExtractedPath)
	cues, err := st.CueCount(id)
	require.NoError(t, err)
	assert.Equal(t, int64(2), cues)
	n, err := st.NodeByID(node.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n.JobsDone)

	// Sidecar tracks map back to the server-side media directory.
	s2 := addMedia(t, st, "/srv/media/library/side.mkv")
	job2, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(s2)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	side := &probe.Outcome{
		Kind:  probe.OutcomeOK,
		Meta:  &probe.MediaMeta{Duration: 10},
		Track: &probe.TrackInfo{Source: "sidecar", Lang: "en", SidecarName: "side.en.srt"},
		Cues:  []subs.Cue{{Start: 0, End: 1, Text: "x"}},
	}
	accepted, err = svc.ApplyProbeResult(node.ID, job2, side, nil)
	require.NoError(t, err)
	assert.True(t, accepted)
	tracks2, err := st.TracksForMedia(s2)
	require.NoError(t, err)
	require.Len(t, tracks2, 1)
	assert.Equal(t, filepath.Join("/srv/media/library", "side.en.srt"), tracks2[0].ExtractedPath)
}

func TestApplyProbeResultNoSubsAndSupersede(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	out := &probe.Outcome{Kind: probe.OutcomeNoSubs, Meta: &probe.MediaMeta{Duration: 5}, Note: "nothing usable"}
	accepted, err := svc.ApplyProbeResult(node.ID, jobID, out, nil)
	require.NoError(t, err)
	assert.True(t, accepted)
	m, err := st.GetMedia(id)
	require.NoError(t, err)
	assert.Equal(t, "no_subs", m.Status)
	assert.Equal(t, "nothing usable", m.Error)

	// Superseded: requeue the job under a different worker and try again.
	job2, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)
	_, err = st.ClaimJob(context.Background(), "worker-probe-1", []string{"probe"})
	require.NoError(t, err)
	_ = job2
	accepted, err = svc.ApplyProbeResult(node.ID, jobID, out, nil)
	require.NoError(t, err)
	assert.False(t, accepted)
}

func TestApplyClipResult(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	require.NoError(t, st.UpdateMediaProbe(id, 120, 1920, 1080, "h264", "aac", "mkv", 0, 0, "en"))
	jobID, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":1,"end":3,"mode":"accurate"}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)

	cacheDir := t.TempDir()
	svc.Clips = clips.New(st, clips.Config{CacheDir: cacheDir, MaxClipSec: 120, DefaultPadMs: 250})
	accepted, err := svc.ApplyClipResult(node.ID, jobID, "mp4", strings.NewReader("clip-bytes"))
	require.NoError(t, err)
	assert.True(t, accepted)

	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	key := svc.Clips.CacheKey(clips.Request{MediaID: id, Start: 1, End: 3, PadMs: 250, Mode: "accurate"})
	e, err := st.GetCacheEntry(key)
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, "clip", e.Kind)
	assert.Equal(t, int64(len("clip-bytes")), e.Bytes)
	assert.True(t, strings.HasSuffix(e.Path, ".mp4"))

	// Unsupported containers are rejected and the job stays unfinished.
	job2, err := st.EnqueueJob("clip", `{"mediaId":`+itoa(id)+`,"start":4,"end":6}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"clip"})
	require.NoError(t, err)
	_, err = svc.ApplyClipResult(node.ID, job2, "avi", strings.NewReader("x"))
	require.Error(t, err)
}

func TestJanitorRequeuesOffline(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	// Heartbeat keeps the node alive; nothing is requeued.
	_, err = svc.Heartbeat(node.ID, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Janitor(ctx, 10*time.Millisecond); close(done) }()
	time.Sleep(50 * time.Millisecond)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "running", j.Status)

	// Stop heartbeats; after the offline window the janitor requeues.
	time.Sleep(1200 * time.Millisecond)
	j, err = st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "queued", j.Status)
	cancel()
	<-done
}

func TestListDerivesOnlineAndJobs(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "live", "fp1")
	ghost, _ := approvedNode(t, svc, "ghost", "fp2")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	_, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	// Make the ghost node look offline.
	old := time.Now().Add(-10 * time.Second).UnixMilli()
	_, err = st.DB().Exec(`UPDATE nodes SET last_seen=? WHERE id=?`, old, ghost.ID)
	require.NoError(t, err)

	views, err := svc.List()
	require.NoError(t, err)
	require.Len(t, views, 2)
	byName := map[string]NodeView{}
	for _, v := range views {
		byName[v.Node.Name] = v
	}
	live := byName["live"]
	assert.True(t, live.Online)
	require.Len(t, live.CurrentJobs, 1)
	assert.Equal(t, "probe", live.CurrentJobs[0].Type)
	assert.False(t, byName["ghost"].Online)
	assert.Empty(t, byName["ghost"].CurrentJobs)
}

func TestDeleteReleasesLeases(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(node.ID))
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "queued", j.Status)
	n, err := st.NodeByID(node.ID)
	require.NoError(t, err)
	assert.Nil(t, n)
}

func TestStateRejectsEmptySecret(t *testing.T) {
	svc, _ := newTestService(t)
	n, err := svc.Register("n", "fp", "")
	require.NoError(t, err)
	// A node whose stored secret is somehow empty must not authorize.
	_, err = svc.st.DB().Exec(`UPDATE nodes SET secret='' WHERE id=?`, n.ID)
	require.NoError(t, err)
	_, err = svc.State(n.ID, "")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestRegisterErrorIsSentinel(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register("n", "", "")
	require.True(t, errors.Is(err, ErrUnauthorized))
}
