package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quipster/internal/clips"
	"quipster/internal/nodes"
	"quipster/internal/probe"

	"github.com/stretchr/testify/require"
)

// nodeDepsWithCap mirrors nodeDeps but caps pending registrations so the
// registration limit can be exercised without provisioning 25 nodes.
func nodeDepsWithCap(t *testing.T, maxPending int) Deps {
	t.Helper()
	st, deps := newEnv(t)
	depClips := clips.New(st, clips.Config{CacheDir: t.TempDir(), MaxClipSec: 120, DefaultPadMs: 250})
	svc := nodes.NewService(st, nodes.Settings{
		MediaRoot:    "/srv/media",
		SubsCacheDir: t.TempDir(),
		Clips: nodes.ClipSettings{
			PreviewRes: "640:-2", AccurateRes: "1280:-2", MaxClipSec: 120, DefaultPadMs: 250,
		},
		Version:    "test",
		MaxPending: maxPending,
	})
	svc.Pipeline = &probe.Pipeline{St: st, SubsCacheDir: filepath.Join(t.TempDir(), "subs")}
	svc.Clips = depClips
	deps.Clips = depClips
	deps.Nodes = svc
	return deps
}

// TestNodeIDFromUnauthenticatedContext covers the zero-value fallback for
// requests that bypass node authentication.
func TestNodeIDFromUnauthenticatedContext(t *testing.T) {
	require.Zero(t, nodeIDFrom(context.Background()))
}

// TestNodeRegisterRejectsBadInput covers the registration guard rails:
// malformed JSON bodies and registrations without a fingerprint.
func TestNodeRegisterRejectsBadInput(t *testing.T) {
	d, _ := nodeDeps(t)

	rec := serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewBufferString("{not json")))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := mustJSON(t, map[string]string{"name": "no-fingerprint", "version": "test"})
	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestNodeRegisterCapsPending covers the unauthenticated-registration cap.
func TestNodeRegisterCapsPending(t *testing.T) {
	d := nodeDepsWithCap(t, 1)

	first := mustJSON(t, map[string]string{"name": "n1", "fingerprint": "fp1", "version": "test"})
	rec := serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(first)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	second := mustJSON(t, map[string]string{"name": "n2", "fingerprint": "fp2", "version": "test"})
	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/nodes/register", bytes.NewReader(second)))
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
}

// TestNodeProtocolRejectsBadInput drives malformed node-protocol requests
// through an approved node's token: bad url ids and undecodable bodies.
func TestNodeProtocolRejectsBadInput(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "worker", "fp-bad-input")

	rec := serveNode(t, d, withHeader(httptest.NewRequest(http.MethodGet, "/api/nodes/not-an-id/state", nil), "X-Node-Secret", "irrelevant"))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/claim", token, "{not json"))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/not-an-id/complete", token, "{}"))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/complete", token, "{not json"))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// A plain JSON body is rejected: result uploads must be multipart.
	rec = serveNode(t, d, nodeJSON(http.MethodPost, "/api/nodes/jobs/1/result", token, "{}"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestNodeResultRejectsBrokenMultipart covers the multipart iterator failure
// path: a body that announces parts but ends before any part is complete.
func TestNodeResultRejectsBrokenMultipart(t *testing.T) {
	d, _ := nodeDeps(t)
	_, token := registerNode(t, d, "worker", "fp-broken-multipart")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_, err := mw.CreateFormFile("result", "result.json")
	require.NoError(t, err)
	// The writer is intentionally not closed, so the part never terminates.

	req := httptest.NewRequest(http.MethodPost, "/api/nodes/jobs/1/result", &buf)
	req.Header.Set("X-Node-Token", token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := serveNode(t, d, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminNodeEndpointsRejectBadIDs covers the id-parsing guards shared by
// the admin node lifecycle endpoints and the empty-inventory list shape.
func TestAdminNodeEndpointsRejectBadIDs(t *testing.T) {
	d, _ := nodeDeps(t)

	// With no nodes registered the inventory serializes as an empty list.
	rec := serveNode(t, d, httptest.NewRequest(http.MethodGet, "/api/admin/nodes", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var listed struct {
		Nodes []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.NotNil(t, listed.Nodes)
	require.Empty(t, listed.Nodes)

	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/admin/nodes/not-an-id/approve", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serveNode(t, d, httptest.NewRequest(http.MethodPost, "/api/admin/nodes/not-an-id/revoke", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serveNode(t, d, httptest.NewRequest(http.MethodDelete, "/api/admin/nodes/not-an-id", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Unknown but well-formed ids are still 404-free deletes.
	rec = serveNode(t, d, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/nodes/%d", 404), nil))
	require.Equal(t, http.StatusOK, rec.Code)
}
