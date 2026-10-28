package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quipster/internal/clips"
	"quipster/internal/nodes"
	"quipster/internal/probe"
	"quipster/internal/store"
	"quipster/internal/subs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustJSON marshals v for request bodies, failing the test on error.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// nodeDeps builds Deps with the node protocol service attached.
func nodeDeps(t *testing.T) (Deps, *nodes.Service) {
	t.Helper()
	st, deps := newEnv(t)
	depClips := clips.New(st, clips.Config{CacheDir: t.TempDir(), MaxClipSec: 120, DefaultPadMs: 250})
	svc := nodes.NewService(st, nodes.Settings{
		MediaRoot:    "/srv/media",
		SubsCacheDir: t.TempDir(),
		Clips: nodes.ClipSettings{
			PreviewRes: "640:-2", AccurateRes: "1280:-2", MaxClipSec: 120, DefaultPadMs: 250,
		},
		Version: "test",
	})
	svc.Pipeline = &probe.Pipeline{St: st, SubsCacheDir: filepath.Join(t.TempDir(), "subs")}
	svc.Clips = depClips
	deps.Clips = depClips
	deps.Nodes = svc
	return deps, svc
}

// serveNode runs a request against the router built from deps.
func serveNode(t *testing.T, d Deps, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Router(d).ServeHTTP(rec, r)
	return rec
}

// registerNode drives registration + approval and returns (nodeID, token).
func registerNode(t *testing.T, d Deps, name, fp string) (int64, string) {
	t.Helper()
	body := mustJSON(t, map[string]string{"name": name, "fingerprint": fp, "version": "test"})
	rec := serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var reg struct {
		Secret string `json:"secret"`
		NodeID int64  `json:"nodeId"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reg))

	// Approve through the admin endpoint (open in tests: no API token).
	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/nodes/%d/approve", reg.NodeID), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = serveNode(t, d, withHeader(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/nodes/%d/state", reg.NodeID), nil), "X-Node-Secret", reg.Secret))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var state struct {
		Status string `json:"status"`
		Token  string `json:"token"`
		Server struct {
			MediaRoot string `json:"mediaRoot"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	require.Equal(t, "approved", state.Status)
	require.NotEmpty(t, state.Token)
	require.Equal(t, "/srv/media", state.Server.MediaRoot)
	return reg.NodeID, state.Token
}

func withHeader(r *http.Request, key, val string) *http.Request {
	r.Header.Set(key, val)
	return r
}

// nodeJSON builds a JSON request with the node token header.
func nodeJSON(method, path, token, body string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("X-Node-Token", token)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestNodeRegisterAndState(t *testing.T) {
	d, _ := nodeDeps(t)

	// Missing fingerprint is a bad request, not a registration.
	body := mustJSON(t, map[string]string{"name": "x"})
	rec := serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The state poll requires the registration secret.
	body = mustJSON(t, map[string]string{"name": "n", "fingerprint": "fp", "version": "1"})
	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code)
	var reg struct {
		Secret string `json:"secret"`
		NodeID int64  `json:"nodeId"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reg))

	rec = serveNode(t, d, withHeader(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/nodes/%d/state", reg.NodeID), nil), "X-Node-Secret", "wrong"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = serveNode(t, d, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/nodes/%d/state", reg.NodeID), nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "missing secret is unauthorized")
}

func TestNodeClaimRequiresToken(t *testing.T) {
	d, _ := nodeDeps(t)
	rec := serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/claim", "", `{"types":["probe"]}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Node tokens grant claiming rights only: with an admin token
	// configured, a node token must not open admin endpoints.
	id, token := registerNode(t, d, "n", "fp")
	d.APIToken = "admin-secret"
	rec = serveNode(t, d, withHeader(httptest.NewRequest(http.MethodGet, "/api/admin/nodes", nil), "X-Node-Token", token))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "node tokens are not admin tokens")
	rec = serveNode(t, d, withHeader(httptest.NewRequest(http.MethodGet, "/api/admin/nodes", nil), "X-API-Token", "admin-secret"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Positive(t, id)
}

func TestNodeClaimLifecycle(t *testing.T) {
	d, _ := nodeDeps(t)
	st := d.St
	_, token := registerNode(t, d, "n", "fp")
	mediaID, _, err := st.UpsertMedia("/srv/media/lib/movie.mkv", "mkv", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	_, err = st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, mediaID), 2)
	require.NoError(t, err)

	rec := serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/claim", token, `{"types":["probe"]}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var work nodes.Work
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &work))
	require.NotNil(t, work.Media)
	assert.Equal(t, mediaID, work.Media.ID)
	assert.Equal(t, "/srv/media", work.MediaRoot)

	// Empty queue yields 204.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/claim", token, `{"types":["probe"]}`))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Heartbeat reports progress and queue state.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/heartbeat", token,
		fmt.Sprintf(`{"jobs":[{"id":%d,"progress":0.5}]}`, work.JobID)))
	require.Equal(t, http.StatusOK, rec.Code)
	var beat nodes.Beat
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &beat))
	assert.False(t, beat.Paused)

	// Complete as error: requeued within the retry budget.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, fmt.Sprintf("/api/nodes/jobs/%d/complete", work.JobID), token, `{"status":"error","error":"boom"}`))
	require.Equal(t, http.StatusOK, rec.Code)
	j, err := st.JobByID(work.JobID)
	require.NoError(t, err)
	assert.Equal(t, "queued", j.Status)
}

// multipartResult builds a multipart result upload body with the given parts.
func multipartResult(t *testing.T, resultJSON []byte, parts map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormField("result")
	require.NoError(t, err)
	_, err = part.Write(resultJSON)
	require.NoError(t, err)
	for name, data := range parts {
		f, err := mw.CreateFormFile(name, name+".bin")
		require.NoError(t, err)
		_, err = f.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func postResult(t *testing.T, d Deps, token string, jobID int64, resultJSON []byte, parts map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartResult(t, resultJSON, parts)
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/nodes/jobs/%d/result", jobID), body)
	r.Header.Set("X-Node-Token", token)
	r.Header.Set("Content-Type", ct)
	return serveNode(t, d, r)
}

func TestNodeProbeResultUpload(t *testing.T) {
	d, _ := nodeDeps(t)
	st := d.St
	nodeID, token := registerNode(t, d, "n", "fp")
	mediaID, _, err := st.UpsertMedia("/srv/media/lib/movie.mkv", "mkv", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	jobID, err := st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, mediaID), 2)
	require.NoError(t, err)

	// The node must hold the lease before its result is accepted.
	_, err = st.ClaimJob(t.Context(), store.NodeWorkerID(nodeID), []string{"probe"})
	require.NoError(t, err)

	outcome := probe.Outcome{
		Kind:         probe.OutcomeOK,
		Meta:         &probe.MediaMeta{Duration: 30, Width: 640, Height: 360, VideoCodec: "h264", AudioCodec: "aac", Container: "mkv"},
		AudioOrdinal: 0,
		AudioLang:    "en",
		Track:        &probe.TrackInfo{Source: "embedded", Lang: "en", StreamIndex: 2},
		Cues: []subs.Cue{
			{Text: "hi", Start: 1, End: 2},
			{Text: "there", Start: 3, End: 4},
		},
	}
	resultJSON := mustJSON(t, outcome)

	rec := postResult(t, d, token, jobID, resultJSON, map[string][]byte{
		"subtitle": []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"),
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	m, err := st.GetMedia(mediaID)
	require.NoError(t, err)
	assert.InDelta(t, 30.0, m.DurationSec, 0.001)
	cues, err := st.CueCount(mediaID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), cues)
	var indexJobs int64
	require.NoError(t, st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index'`).Scan(&indexJobs))
	assert.Equal(t, int64(1), indexJobs, "index job enqueued after probe result")
}

func TestNodeClipResultUpload(t *testing.T) {
	d, svc := nodeDeps(t)
	st := d.St
	nodeID, token := registerNode(t, d, "n", "fp")
	mediaID, _, err := st.UpsertMedia("/srv/media/lib/movie.mkv", "mkv", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	require.NoError(t, st.UpdateMediaProbe(mediaID, 120, 1920, 1080, "h264", "aac", "mkv", 0, 0, "en"))
	jobID, err := st.EnqueueJob("clip", fmt.Sprintf(`{"mediaId":%d,"start":1,"end":3,"mode":"accurate","padMs":250}`, mediaID), 1)
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), store.NodeWorkerID(nodeID), []string{"clip"})
	require.NoError(t, err)

	rec := postResult(t, d, token, jobID, []byte(`{"ext":"mp4"}`), map[string][]byte{
		"clip": bytes.Repeat([]byte{0}, 128),
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "done", j.Status)
	key := svc.Clips.CacheKey(clips.Request{MediaID: mediaID, Start: 1, End: 3, PadMs: 250, Mode: "accurate"})
	e, err := st.GetCacheEntry(key)
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, int64(128), e.Bytes)
}

func TestNodeAdminListAndLifecycle(t *testing.T) {
	d, _ := nodeDeps(t)
	id, token := registerNode(t, d, "worker-1", "fp-1")

	rec := serveNode(t, d, httptest.NewRequest(http.MethodGet, "/api/admin/nodes", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Nodes []nodes.NodeView `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Nodes, 1)
	assert.Equal(t, "worker-1", list.Nodes[0].Node.Name)
	assert.True(t, list.Nodes[0].Online, "a freshly approved node counts online")

	// Revoke then delete.
	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/nodes/%d/revoke", id), nil))
	require.Equal(t, http.StatusOK, rec.Code)
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/claim", token, `{"types":["probe"]}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "revoked token stops authorizing")

	rec = serveNode(t, d, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/nodes/%d", id), nil))
	require.Equal(t, http.StatusOK, rec.Code)
	rec = serveNode(t, d, httptest.NewRequest(http.MethodGet, "/api/admin/nodes", nil))
	var after struct {
		Nodes []nodes.NodeView `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &after))
	assert.Empty(t, after.Nodes)
}

func TestNodeApproveConflictWhenNotPending(t *testing.T) {
	d, _ := nodeDeps(t)
	id, _ := registerNode(t, d, "n", "fp")
	rec := serveNode(t, d, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/nodes/%d/approve", id), nil))
	assert.Equal(t, http.StatusConflict, rec.Code, "approving an approved node conflicts")
}

func TestNodeResultRejectsNonMultipart(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")
	rec := serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/result", token, `{"x":1}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNodeResultSupersededUploadDropped(t *testing.T) {
	d, _ := nodeDeps(t)
	st := d.St
	_, token := registerNode(t, d, "n", "fp")
	mediaID, _, err := st.UpsertMedia("/srv/media/lib/movie.mkv", "mkv", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	jobID, err := st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, mediaID), 2)
	require.NoError(t, err)

	// A local runner steals the lease before the result arrives.
	_, err = st.ClaimJob(t.Context(), "worker-probe-0", []string{"probe"})
	require.NoError(t, err)

	rec := postResult(t, d, token, jobID, []byte(`{"kind":"ok"}`), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"accepted":false`)
}

func TestNodeResultMissingResultPart(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_, err := mw.CreateFormFile("clip", "clip.mp4")
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	r := httptest.NewRequest(http.MethodPost, "/api/nodes/jobs/1/result", &buf)
	r.Header.Set("X-Node-Token", token)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := serveNode(t, d, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNodeEndpointBadInputPaths(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")

	// Malformed ids are bad requests.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/nodes/abc/approve"},
		{http.MethodPost, "/api/admin/nodes/abc/revoke"},
		{http.MethodDelete, "/api/admin/nodes/abc"},
		{http.MethodPost, "/api/nodes/jobs/abc/complete"},
		{http.MethodPost, "/api/nodes/jobs/abc/result"},
	} {
		body := `{}`
		contentType := "application/json"
		if tc.path == "/api/nodes/jobs/abc/result" {
			body = ""
			contentType = "" // rejected as non-multipart before id parsing
		}
		rec := serveNode(t, d, nodeJSON(tc.method, tc.path, token, body))
		if contentType == "" {
			rec = serveNode(t, d, nodeJSON(tc.method, tc.path, token, body))
		}
		assert.Equal(t, http.StatusBadRequest, rec.Code, tc.path)
	}

	// Deleting an unknown node is still a successful delete.
	rec := serveNode(t, d, httptest.NewRequest(http.MethodDelete, "/api/admin/nodes/999", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	// Bad JSON on complete is rejected.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/complete", token, `{bad`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNodeHeartbeatBadBody(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")
	rec := serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/heartbeat", token, `{bad`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNodeResultUnknownJob(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")
	rec := postResult(t, d, token, 424242, []byte(`{"kind":"ok"}`), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"accepted":false`, "unknown jobs are superseded, not errors")
}

func TestNodeCompleteRejectsBadBody(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")
	rec := serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/complete", token, `not-json`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// A missing body is also invalid.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/complete", token, ``))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNodeResultTruncatedMultipart(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "n", "fp")

	// A body whose declared length exceeds its content makes the multipart
	// iterator fail mid-stream.
	body := &truncatedReader{data: buildMultipart(t, []byte(`{"kind":"ok"}`), nil)}
	r := httptest.NewRequest(http.MethodPost, "/api/nodes/jobs/1/result", body)
	r.Header.Set("X-Node-Token", token)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	rec := serveNode(t, d, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a corrupt multipart body is a bad request")
}

// truncatedReader advertises more bytes than it delivers.
type truncatedReader struct {
	data []byte
	pos  int
}

func (r *truncatedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data)-1 {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// buildMultipart assembles a result upload body without sending it.
func buildMultipart(t *testing.T, resultJSON []byte, parts map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormField("result")
	require.NoError(t, err)
	_, err = part.Write(resultJSON)
	require.NoError(t, err)
	for name, data := range parts {
		f, err := mw.CreateFormFile(name, name+".bin")
		require.NoError(t, err)
		_, err = f.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	return buf.Bytes()
}

func TestNodeResultSubtitleReadError(t *testing.T) {
	d, _ := nodeDeps(t)
	st := d.St
	nodeID, token := registerNode(t, d, "n", "fp")
	mediaID, _, err := st.UpsertMedia("/srv/media/lib/movie.mkv", "mkv", 1, 1, "M", 0, 0, 0, "")
	require.NoError(t, err)
	jobID, err := st.EnqueueJob("probe", fmt.Sprintf(`{"mediaId":%d}`, mediaID), 2)
	require.NoError(t, err)
	_, err = st.ClaimJob(t.Context(), store.NodeWorkerID(nodeID), []string{"probe"})
	require.NoError(t, err)

	// A subtitle part that dies mid-read is reported as a bad request and
	// the lease survives for a retry.
	body := &truncatedReader{data: buildMultipart(t, []byte(`{"kind":"ok"}`), map[string][]byte{"subtitle": []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n")})}
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/nodes/jobs/%d/result", jobID), body)
	r.Header.Set("X-Node-Token", token)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	rec := serveNode(t, d, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	j, err := st.JobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, "running", j.Status, "a failed upload must not finalize the lease")
}
