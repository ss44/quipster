package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"quipster/internal/nodes"
)

// client speaks the worker-node protocol against the coordinating server.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type client struct {
	base   string
	http   *http.Client
	token  string
	nodeID int64
	secret string
}

func newClient(serverURL string) *client {
	return &client{
		base: serverURL,
		http: &http.Client{Timeout: 5 * time.Minute},
	}
}

// do issues a JSON request, attaching the claim token when one is held. A
// 401 maps to errUnauthorized so the caller can re-register.
func (c *client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("X-Node-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("[node] close response body: %v", cerr)
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 300 {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if rerr != nil {
			b = nil
		}
		return fmt.Errorf("node api %s %s: %s", method, path, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// errUnauthorized signals a rejected node token; the session re-registers.
var errUnauthorized = fmt.Errorf("node token rejected")

// register posts the node's identity and stores the returned id/secret.
func (c *client) register(ctx context.Context, name, fingerprint, version string) error {
	//nolint:errcheck // the map holds only strings, so marshaling cannot fail
	body, _ := json.Marshal(map[string]string{"name": name, "fingerprint": fingerprint, "version": version})
	var out struct {
		Secret string `json:"secret"`
		NodeID int64  `json:"nodeId"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/nodes/register", "application/json", bytes.NewReader(body), &out); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.nodeID, c.secret = out.NodeID, out.Secret
	return nil
}

// state polls the approval state.
func (c *client) state(ctx context.Context) (*nodes.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/nodes/%d/state", c.base, c.nodeID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Node-Secret", c.secret)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("[node] close response body: %v", cerr)
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("node state poll: http %d", resp.StatusCode)
	}
	var st nodes.State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// claim requests the next job; ok=false means no eligible work.
func (c *client) claim(ctx context.Context, types []string) (*nodes.Work, bool, error) {
	//nolint:errcheck // the map holds only strings, so marshaling cannot fail
	body, _ := json.Marshal(map[string]any{"types": types})
	var w nodes.Work
	err := c.do(ctx, http.MethodPost, "/api/nodes/claim", "application/json", bytes.NewReader(body), &w)
	if err != nil {
		return nil, false, err
	}
	if w.JobID == 0 {
		return nil, false, nil
	}
	return &w, true, nil
}

// heartbeat reports liveness and in-flight progress.
func (c *client) heartbeat(ctx context.Context, jobs []nodes.JobProgress) (*nodes.Beat, error) {
	//nolint:errcheck // the map holds only scalars, so marshaling cannot fail
	body, _ := json.Marshal(map[string]any{"jobs": jobs})
	var beat nodes.Beat
	if err := c.do(ctx, http.MethodPost, "/api/nodes/heartbeat", "application/json", bytes.NewReader(body), &beat); err != nil {
		return nil, err
	}
	return &beat, nil
}

// complete reports a job verdict.
func (c *client) complete(ctx context.Context, jobID int64, status, errMsg string) error {
	//nolint:errcheck // the map holds only strings, so marshaling cannot fail
	body, _ := json.Marshal(map[string]string{"status": status, "error": errMsg})
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/nodes/jobs/%d/complete", jobID), "application/json", bytes.NewReader(body), nil)
}

// uploadResult posts a work artifact: the result JSON plus optional named
// file parts. On success the server finishes the job.
func (c *client) uploadResult(ctx context.Context, jobID int64, resultJSON []byte, files map[string]uploadFile) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		// Multipart writing happens on a goroutine so a slow server
		// consuming the body never blocks the piped writer setup.
		var werr error
		defer func() {
			if cerr := mw.Close(); cerr != nil && werr == nil {
				werr = cerr
			}
			// CloseWithError always returns nil; ignore it explicitly.
			_ = pw.CloseWithError(werr) //nolint:errcheck // documented no-error closer
		}()
		part, err := mw.CreateFormField("result")
		if err != nil {
			werr = err
			return
		}
		if _, err = part.Write(resultJSON); err != nil {
			werr = err
			return
		}
		for name, f := range files {
			fp, err := mw.CreateFormFile(name, f.Name)
			if err != nil {
				werr = err
				return
			}
			src, err := f.Open()
			if err != nil {
				werr = err
				return
			}
			if _, err = io.Copy(fp, src); err != nil {
				werr = err
				if cerr := src.Close(); cerr != nil {
					log.Printf("[node] close upload source: %v", cerr)
				}
				return
			}
			if cerr := src.Close(); cerr != nil {
				log.Printf("[node] close upload source: %v", cerr)
			}
		}
	}()
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/nodes/jobs/%d/result", jobID), mw.FormDataContentType(), pr, nil)
	return err
}

// uploadFile is one named file attachment for a result upload.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type uploadFile struct {
	Name string
	Open func() (io.ReadCloser, error)
}
