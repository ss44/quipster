package nodes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/probe"
	"quipster/internal/subs"
)

// A node racing an archive must not write probe results into a frozen row.
// The upload is dropped and the job closes quietly with a note.
func TestApplyProbeResultArchivedDropsOutcome(t *testing.T) {
	svc, st := newTestService(t)
	node, _ := approvedNode(t, svc, "n1", "fp1")
	id := addMedia(t, st, "/srv/media/movie.mkv")
	jobID, err := st.EnqueueJob("probe", `{"mediaId":`+itoa(id)+`}`, 1)
	require.NoError(t, err)
	_, err = svc.Claim(context.Background(), node.ID, []string{"probe"})
	require.NoError(t, err)

	// Archive the row exactly as a scan would (zero-grace, two passes).
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 2; i++ {
		_, _, err = st.ArchiveMissingUnder("/srv/media", nil, 0, now)
		require.NoError(t, err)
	}
	m, err := st.GetMedia(id)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.True(t, m.Archived)

	out := &probe.Outcome{
		Kind:         probe.OutcomeOK,
		Meta:         &probe.MediaMeta{Duration: 30, Width: 640, Height: 360, VideoCodec: "h264", AudioCodec: "aac", Container: "mkv", StartTime: 0.5},
		AudioOrdinal: 1,
		AudioLang:    "en",
		Track:        &probe.TrackInfo{Source: "embedded", Lang: "en", StreamIndex: 2},
		Cues:         []subs.Cue{{Start: 1, End: 2, Text: "hi"}},
	}
	accepted, err := svc.ApplyProbeResult(node.ID, jobID, out, strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nhi\n"))
	require.NoError(t, err)
	assert.True(t, accepted)

	// The frozen row must be untouched: no probe metadata, no tracks, no cues.
	m, err = st.GetMedia(id)
	require.NoError(t, err)
	assert.Zero(t, m.DurationSec)
	assert.Zero(t, m.AudioStreamIndex)
	assert.Equal(t, "pending", m.Status, "archived status stays frozen")
	tracks, err := st.TracksForMedia(id)
	require.NoError(t, err)
	assert.Empty(t, tracks)
	cues, err := st.CueCount(id)
	require.NoError(t, err)
	assert.Zero(t, cues)

	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	assert.Contains(t, j.Error, "archived")
	n, err := st.NodeByID(node.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n.JobsDone, "the node still gets credit for its work")
}
