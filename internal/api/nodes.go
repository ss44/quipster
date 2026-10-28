package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"quipster/internal/nodes"
	"quipster/internal/probe"
)

// maxResultBytes bounds a node result upload (cue payloads and rendered
// clips). A two-minute 720p transcode can reach a few hundred megabytes, so
// the cap leaves generous headroom while rejecting runaway bodies.
const maxResultBytes = 1 << 30

// maxSubtitleBytes bounds one uploaded subtitle extraction; real SRT/VTT
// files are orders of magnitude smaller.
const maxSubtitleBytes = 64 << 20

// nodeContextKey identifies the authenticated node id stored in a request
// context by the nodeAuth middleware.
type nodeContextKey struct{}

// nodeIDFrom returns the authenticated node id, or 0 when absent.
func nodeIDFrom(ctx context.Context) int64 {
	id, ok := ctx.Value(nodeContextKey{}).(int64)
	if !ok {
		return 0
	}
	return id
}

// nodeAuth authenticates node protocol requests via the X-Node-Token header
// issued at approval time. Node tokens grant job-claiming rights only; they
// never satisfy the admin token guard.
func (d Deps) nodeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := d.Nodes.Authenticate(r.Header.Get("X-Node-Token"))
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "invalid or missing node token"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeContextKey{}, id)))
	})
}

// handleNodeRegister accepts a worker's registration. It is intentionally
// open: access is gated by operator approval, and the service caps the
// pending population.
func (d Deps) handleNodeRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		Version     string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	n, err := d.Nodes.Register(body.Name, body.Fingerprint, body.Version)
	switch {
	case errors.Is(err, nodes.ErrTooManyPending):
		writeErr(w, http.StatusTooManyRequests, "too many pending node registrations")
		return
	case errors.Is(err, nodes.ErrUnauthorized):
		writeErr(w, http.StatusBadRequest, "fingerprint required")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"nodeId": n.ID, "secret": n.Secret, "status": n.Status})
}

// handleNodeState answers the approval poll. The registration secret gates
// the response; an approved node receives its claim token and the server
// configuration it needs (media root for path remapping, clip profile).
func (d Deps) handleNodeState(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad node id")
		return
	}
	st, err := d.Nodes.State(id, r.Header.Get("X-Node-Secret"))
	if errors.Is(err, nodes.ErrUnauthorized) {
		writeErr(w, http.StatusUnauthorized, "invalid node secret")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, st)
}

// handleNodeClaim leases the next eligible job to the node.
func (d Deps) handleNodeClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Types []string `json:"types"`
	}
	if r.Body != nil {
		// A malformed or missing claim body simply means "use defaults".
		if derr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); derr != nil && !errors.Is(derr, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	work, err := d.Nodes.Claim(r.Context(), nodeIDFrom(r.Context()), body.Types)
	if err != nil {
		log.Printf("[api] node claim: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if work == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, work)
}

// handleNodeHeartbeat records liveness plus progress and returns the queue
// pause and scan state.
func (d Deps) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Jobs []nodes.JobProgress `json:"jobs"`
	}
	if r.Body != nil {
		// An empty heartbeat body is valid (idle node); other malformed
		// bodies are rejected.
		if derr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); derr != nil && !errors.Is(derr, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	beat, err := d.Nodes.Heartbeat(nodeIDFrom(r.Context()), body.Jobs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, beat)
}

// handleNodeComplete applies a job verdict reported by a node.
func (d Deps) handleNodeComplete(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad job id")
		return
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if derr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); derr != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	accepted, err := d.Nodes.Complete(nodeIDFrom(r.Context()), jobID, body.Status, body.Error)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"accepted": accepted})
}

// nodeResultParts collects one result upload's parts. Clip bytes spill to a
// temp file so large renders never buffer in memory; subtitle extractions
// are small enough to buffer in memory.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type nodeResultParts struct {
	resultJSON []byte
	subBytes   []byte
	clipPath   string
	clipExt    string
}

// cleanup removes a spilled clip temp file, tolerating a missing file.
func (p *nodeResultParts) cleanup() {
	if p.clipPath == "" {
		return
	}
	//nolint:gosec // the path comes from os.CreateTemp above, not from request input
	if rmErr := os.Remove(p.clipPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		log.Printf("[api] node result temp cleanup: %v", rmErr)
	}
}

// readNodeResultParts consumes the multipart body while iterating. Parts
// must be read as they are encountered: a saved multipart part reader is
// dead once the iterator advances. It reports failure by writing a response.
func readNodeResultParts(w http.ResponseWriter, r *http.Request) *nodeResultParts {
	r.Body = http.MaxBytesReader(w, r.Body, maxResultBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "multipart body required")
		return nil
	}
	parts := &nodeResultParts{}
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			return parts
		}
		if perr != nil {
			writeErr(w, http.StatusBadRequest, "bad multipart body")
			return nil
		}
		switch part.FormName() {
		case "result":
			b, rerr := io.ReadAll(io.LimitReader(part, 64<<20))
			if rerr != nil {
				writeErr(w, http.StatusBadRequest, "bad result part")
				return nil
			}
			parts.resultJSON = b
		case "subtitle":
			b, rerr := io.ReadAll(io.LimitReader(part, maxSubtitleBytes))
			if rerr != nil {
				writeErr(w, http.StatusBadRequest, "bad subtitle part")
				return nil
			}
			parts.subBytes = b
		case "clip":
			if !spillClipPart(w, part, parts) {
				return nil
			}
		}
	}
}

// spillClipPart streams one clip part into a temp file, recording its path
// and the node-provided container extension.
func spillClipPart(w http.ResponseWriter, part *multipart.Part, parts *nodeResultParts) bool {
	f, terr := os.CreateTemp("", "quipster-node-clip-*")
	if terr != nil {
		writeErr(w, http.StatusInternalServerError, terr.Error())
		return false
	}
	parts.clipPath = f.Name()
	size, cerr := io.Copy(f, part)
	if closeErr := f.Close(); cerr == nil {
		cerr = closeErr
	}
	if cerr != nil || size == 0 {
		writeErr(w, http.StatusBadRequest, "bad clip part")
		return false
	}
	parts.clipExt = strings.TrimPrefix(strings.ToLower(filepath.Ext(part.FileName())), ".")
	return true
}

// handleNodeResult receives a node's work artifact: a multipart body with a
// "result" JSON part and, for probe results with embedded subtitles, a
// "subtitle" part; for clip jobs, a "clip" part with the rendered file.
// Successful uploads finish the job server-side, so a lost follow-up complete
// call cannot orphan the work.
func (d Deps) handleNodeResult(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad job id")
		return
	}
	parts := readNodeResultParts(w, r)
	if parts == nil {
		return
	}
	defer parts.cleanup()
	if len(parts.resultJSON) == 0 {
		writeErr(w, http.StatusBadRequest, "result part required")
		return
	}
	nodeID := nodeIDFrom(r.Context())

	// Clip uploads carry only the container extension; the server derives
	// everything else from the job payload.
	if parts.clipPath != "" {
		clipExt := parts.clipExt
		var meta struct {
			Ext string `json:"ext"`
		}
		if err := json.Unmarshal(parts.resultJSON, &meta); err == nil && meta.Ext != "" {
			clipExt = strings.TrimPrefix(strings.ToLower(meta.Ext), ".")
		}
		f, oerr := os.Open(parts.clipPath) //nolint:gosec // path comes from os.CreateTemp above
		if oerr != nil {
			writeErr(w, http.StatusInternalServerError, oerr.Error())
			return
		}
		accepted, aerr := d.Nodes.ApplyClipResult(nodeID, jobID, clipExt, f)
		if cerr := f.Close(); cerr != nil && aerr == nil {
			// A failed flush means the stored render may be truncated;
			// surface it instead of reporting success.
			aerr = cerr
		}
		writeResultAck(w, accepted, aerr)
		return
	}

	var outcome probe.Outcome
	if err := json.Unmarshal(parts.resultJSON, &outcome); err != nil {
		writeErr(w, http.StatusBadRequest, "bad probe result")
		return
	}
	var subFile io.Reader
	if parts.subBytes != nil {
		subFile = bytes.NewReader(parts.subBytes)
	}
	accepted, aerr := d.Nodes.ApplyProbeResult(nodeID, jobID, &outcome, subFile)
	writeResultAck(w, accepted, aerr)
}

// writeResultAck maps a result application to its response: superseded
// uploads are acknowledged as not accepted (a normal race, not an error).
func writeResultAck(w http.ResponseWriter, accepted bool, err error) {
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"accepted": accepted})
}

// handleNodesList serves the admin dashboard's node inventory.
func (d Deps) handleNodesList(w http.ResponseWriter, _ *http.Request) {
	views, err := d.Nodes.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views == nil {
		views = []nodes.NodeView{}
	}
	writeJSON(w, map[string]any{"nodes": views})
}

// handleNodeApprove approves a pending node and issues its claim token.
func (d Deps) handleNodeApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad node id")
		return
	}
	if err := d.Nodes.Approve(id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{"approved": true})
}

// handleNodeRevoke revokes an approved node's token.
func (d Deps) handleNodeRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad node id")
		return
	}
	if err := d.Nodes.Revoke(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"revoked": true})
}

// handleNodeDelete removes a node and releases its leases.
func (d Deps) handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad node id")
		return
	}
	if err := d.Nodes.Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}
