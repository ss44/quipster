// Package nodes implements the server side of the distributed worker
// protocol: registration and approval, lease-safe job claiming over HTTP,
// heartbeat liveness, result application for remote work, and requeueing of
// jobs orphaned by offline nodes.
package nodes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"quipster/internal/clips"
	"quipster/internal/probe"
	"quipster/internal/store"
)

// Sentinel errors surfaced to the HTTP layer for status mapping.
var (
	// ErrUnauthorized covers unknown nodes, wrong secrets and revoked
	// tokens; callers map it to 401.
	ErrUnauthorized = errors.New("unauthorized node request")
	// ErrTooManyPending bounds unauthenticated registrations.
	ErrTooManyPending = errors.New("too many pending node registrations")
	// ErrNotPending reports an approval/revoke against a node whose
	// status does not allow it.
	ErrNotPending = errors.New("node is not pending")
)

// Node status values, re-exported from the store schema so protocol callers
// need not import the store for vocabulary.
const (
	NodePending  = store.NodePending
	NodeApproved = store.NodeApproved
	NodeRevoked  = store.NodeRevoked
)

// Defaults for the protocol cadence. Heartbeats are frequent enough that a
// dead node's leases are reclaimed within a minute, and lenient enough to
// survive jittery Wi-Fi links.
const (
	DefaultHeartbeat    = 10 * time.Second
	DefaultOfflineAfter = 45 * time.Second
	DefaultMaxPending   = 25
	// maxAttempts mirrors the local runner's retry policy.
	maxAttempts = 3
)

// ClipSettings is the subset of the clip service configuration that changes
// rendered output. Nodes must mirror it exactly so an uploaded render matches
// the cache key and encoding profile the server registers for the job.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type ClipSettings struct {
	PreviewRes   string  `json:"previewRes"`
	AccurateRes  string  `json:"accurateRes"`
	CPUPreset    string  `json:"cpuPreset,omitempty"`
	MaxClipSec   float64 `json:"maxClipSec"`
	DefaultPadMs int     `json:"defaultPadMs"`
}

// Settings carries the server configuration the node protocol needs.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Settings struct {
	// MediaRoot is the server's absolute media directory. Claim responses
	// include it so nodes can remap media paths onto their own mounts.
	MediaRoot    string
	SubsCacheDir string
	Clips        ClipSettings
	Heartbeat    time.Duration
	OfflineAfter time.Duration
	MaxPending   int
	Version      string
}

// ServerInfo is the configuration payload an approved node receives: the
// path mapping anchor and the clip profile it must render with.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type ServerInfo struct {
	MediaRoot     string       `json:"mediaRoot"`
	HeartbeatSecs int          `json:"heartbeatSecs"`
	Version       string       `json:"version"`
	ClipSettings  ClipSettings `json:"clips"`
}

// State is the registration-poll response.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type State struct {
	Status string      `json:"status"`
	Token  string      `json:"token,omitempty"`
	Server *ServerInfo `json:"server,omitempty"`
}

// MediaRef points a node at the media file a claimed job targets. Path is
// the server-side absolute path; the node remaps it onto its own mount using
// MediaRoot from the claim.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type MediaRef struct {
	ID     int64  `json:"id"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// ClipWork carries the clip parameters a node needs to render without
// touching the store: the request plus the audio stream ordinal chosen by
// the server's probe.
type ClipWork struct {
	Mode         string  `json:"mode"`
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	PadMs        int     `json:"padMs"`
	AudioOrdinal int     `json:"audioOrdinal"`
}

// Work is a claimed job as seen by a node.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Work struct {
	JobID   int64     `json:"jobId"`
	Type    string    `json:"type"`
	Payload string    `json:"payload"`
	Media   *MediaRef `json:"media,omitempty"`
	Clip    *ClipWork `json:"clip,omitempty"`
	// Clips carries the server's current clip profile with clip jobs so
	// the node renders with the exact settings whose hash the server will
	// register, even if the profile changed since the node last synced.
	Clips *ClipSettings `json:"clips,omitempty"`
	// MediaRoot is the server's media directory; the node strips it from
	// Media.Path and prepends its own media root.
	MediaRoot string `json:"mediaRoot"`
}

// JobProgress is one in-flight job reported by a heartbeat.
type JobProgress struct {
	ID       int64   `json:"id"`
	Progress float64 `json:"progress"`
}

// Beat is the heartbeat response: queue pause and scan activity let nodes
// mirror the local runner's suspension behavior.
type Beat struct {
	Paused     bool `json:"paused"`
	ScanActive bool `json:"scanActive"`
}

// NodeView is the admin dashboard view of one node.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type NodeView struct {
	Node        store.Node  `json:"node"`
	Online      bool        `json:"online"`
	CurrentJobs []store.Job `json:"currentJobs"`
}

// Service implements the protocol on top of the store. Pipeline applies
// remote probe results exactly like local ones, and Clips registers uploaded
// renders into the shared cache.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Service struct {
	st       *store.Store
	cfg      Settings
	Pipeline *probe.Pipeline
	Clips    *clips.Service
}

// NewService builds the server-side protocol service.
func NewService(st *store.Store, cfg Settings) *Service {
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = DefaultOfflineAfter
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = DefaultMaxPending
	}
	return &Service{st: st, cfg: cfg}
}

// Settings exposes the effective settings after defaults were applied.
func (s *Service) Settings() Settings { return s.cfg }

// randomToken returns a fresh 256-bit hex token for node secrets and claim
// tokens. Randomness failure is effectively impossible but surfaced as an
// error rather than silently reusing a token.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate node token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Register creates or re-registers a node by fingerprint. Registration is
// deliberately unauthenticated (the operator approves it afterwards), so the
// pending population is capped and inputs are length-bounded to keep a
// drive-by registration from growing the database.
func (s *Service) Register(name, fingerprint, version string) (*store.Node, error) {
	name = truncate(name, 64)
	fingerprint = truncate(fingerprint, 128)
	version = truncate(version, 32)
	if fingerprint == "" {
		return nil, fmt.Errorf("%w: missing fingerprint", ErrUnauthorized)
	}
	if pending, err := s.st.CountPendingNodes(); err == nil && pending >= int64(s.cfg.MaxPending) {
		return nil, ErrTooManyPending
	}
	secret, err := randomToken()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "node"
	}
	n, err := s.st.UpsertNode(name, fingerprint, version, secret)
	if err != nil {
		return nil, err
	}
	log.Printf("[nodes] registered node %d %q (status=%s)", n.ID, n.Name, n.Status)
	return n, nil
}

// State answers a registration poll. The one-time registration secret gates
// the response, so only the registering process can learn the approval token.
func (s *Service) State(nodeID int64, secret string) (*State, error) {
	n, err := s.st.NodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	if n == nil || n.Secret == "" || n.Secret != secret {
		return nil, ErrUnauthorized
	}
	st := &State{Status: n.Status}
	if n.Status == store.NodeApproved {
		st.Token = n.Token
		st.Server = s.serverInfo()
	}
	return st, nil
}

// Authenticate resolves a claim token to its approved node's id. Revoked or
// unknown tokens are ErrUnauthorized.
func (s *Service) Authenticate(token string) (int64, error) {
	n, err := s.st.NodeByToken(token)
	if err != nil {
		return 0, err
	}
	if n == nil {
		return 0, ErrUnauthorized
	}
	return n.ID, nil
}

// serverInfo builds the configuration payload for approved nodes.
func (s *Service) serverInfo() *ServerInfo {
	secs := int(s.cfg.Heartbeat / time.Second)
	if secs < 1 {
		secs = 1
	}
	return &ServerInfo{
		MediaRoot:     s.cfg.MediaRoot,
		HeartbeatSecs: secs,
		Version:       s.cfg.Version,
		ClipSettings:  s.cfg.Clips,
	}
}

// claimTypes restricts the job types nodes may claim. Scans and indexing
// stay on the server: scans write metadata rows directly and indexing talks
// to Meilisearch, neither of which a stateless node should touch.
var claimTypes = map[string]bool{"probe": true, "clip": true}

// Claim atomically leases the next queued job of the requested types to the
// node. A nil Work means no eligible job (or a paused queue).
func (s *Service) Claim(ctx context.Context, nodeID int64, types []string) (*Work, error) {
	n, err := s.st.NodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	if n == nil || n.Status != store.NodeApproved {
		return nil, ErrUnauthorized
	}
	filtered := make([]string, 0, len(types))
	for _, t := range types {
		if claimTypes[t] {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	if paused, perr := s.st.GetQueuePaused(); perr == nil && paused {
		return nil, nil
	}
	j, err := s.st.ClaimJob(ctx, store.NodeWorkerID(nodeID), filtered)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, nil
	}
	return s.toWork(j)
}

// toWork enriches a claimed job with the media facts a node needs to execute
// it: the server-side media path (for remapping), the media status (for
// subtitle-cache reuse) and, for clips, the request parameters plus the audio
// ordinal from the probe. A vanished media row finishes the job as an error
// instead of leasing unexecutable work.
func (s *Service) toWork(j *store.Job) (*Work, error) {
	w := &Work{JobID: j.ID, Type: j.Type, Payload: j.Payload, MediaRoot: s.cfg.MediaRoot}
	var payload struct {
		MediaID int64 `json:"mediaId"`
	}
	if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
		return s.finishLease(j, "bad job payload")
	}
	m, merr := s.st.GetMedia(payload.MediaID)
	if merr != nil {
		return nil, merr
	}
	if m == nil {
		return s.finishLease(j, "media row missing")
	}
	w.Media = &MediaRef{ID: m.ID, Path: m.Path, Status: m.Status}
	if j.Type == "clip" {
		req, err := clipRequestFromPayload(j.Payload, s.cfg.Clips.DefaultPadMs)
		if err != nil {
			return s.finishLease(j, "bad clip payload")
		}
		w.Clip = &ClipWork{Mode: req.Mode, Start: req.Start, End: req.End, PadMs: req.PadMs, AudioOrdinal: m.AudioStreamIndex}
		w.Clips = &s.cfg.Clips
	}
	return w, nil
}

// clipRequestFromPayload decodes a clip job payload with the same defaults the
// clip service applies, so claim responses and result registration always
// derive the identical cache key. padDefault is the configured fallback for
// absent or non-positive padMs values.
func clipRequestFromPayload(payload string, padDefault int) (clips.Request, error) {
	var cp struct {
		Mode    string  `json:"mode"`
		MediaID int64   `json:"mediaId"`
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		PadMs   int     `json:"padMs"`
	}
	if err := json.Unmarshal([]byte(payload), &cp); err != nil {
		return clips.Request{}, err
	}
	if cp.Mode == "" {
		// Mirror the clip service's async default.
		cp.Mode = "accurate"
	}
	if cp.PadMs <= 0 {
		cp.PadMs = padDefault
	}
	return clips.Request{MediaID: cp.MediaID, Start: cp.Start, End: cp.End, PadMs: cp.PadMs, Mode: cp.Mode}, nil
}

// finishLease marks an unexecutable lease as errored and reports no work so
// the claim is not silently lost.
func (s *Service) finishLease(j *store.Job, reason string) (*Work, error) {
	if ferr := s.st.FinishJob(j.ID, "error", reason); ferr != nil {
		log.Printf("[nodes] finish job %d (%s): %v", j.ID, reason, ferr)
	}
	return nil, nil
}

// Heartbeat records node liveness and best-effort progress for its in-flight
// jobs, then reports the queue pause and scan state so nodes can mirror the
// local runner's suspension behavior. Progress writes only touch jobs the
// node actually owns.
func (s *Service) Heartbeat(nodeID int64, jobs []JobProgress) (*Beat, error) {
	if err := s.st.TouchNode(nodeID); err != nil {
		return nil, err
	}
	worker := store.NodeWorkerID(nodeID)
	reported := make([]int64, 0, len(jobs))
	for _, jp := range jobs {
		reported = append(reported, jp.ID)
		if jp.Progress < 0 || jp.Progress > 1 {
			continue
		}
		status, owner, err := s.st.JobOwner(jp.ID)
		if err != nil {
			return nil, err
		}
		if status != "running" || owner != worker {
			continue
		}
		if err := s.st.SetJobProgress(jp.ID, jp.Progress); err != nil {
			log.Printf("[nodes] progress job %d node %d: %v", jp.ID, nodeID, err)
		}
	}
	// Reconcile orphaned leases: the node lists every in-flight job it owns,
	// so a running claim it no longer reports is stale (restart or lost claim
	// response). The grace window covers a claim whose first heartbeat has not
	// been observed yet.
	grace := 3 * s.cfg.Heartbeat
	if grace < s.cfg.OfflineAfter {
		grace = s.cfg.OfflineAfter
	}
	cutoff := time.Now().Add(-grace).UnixMilli()
	if n, err := s.st.RequeueMissingNodeJobs(nodeID, reported, cutoff, "node no longer reports job"); err != nil {
		log.Printf("[nodes] heartbeat reconcile node %d: %v", nodeID, err)
	} else if n > 0 {
		log.Printf("[nodes] heartbeat requeued %d orphaned job(s) from node %d", n, nodeID)
	}
	beat := &Beat{}
	if paused, err := s.st.GetQueuePaused(); err == nil {
		beat.Paused = paused
	}
	if active, err := s.st.HasActiveScanJob(); err == nil {
		beat.ScanActive = active
	}
	return beat, nil
}

// ownsJob reports whether the job still belongs to the node: a running lease
// held by this node's worker id. Requeued, finished or reassigned jobs are
// superseded and their late reports are dropped.
func (s *Service) ownsJob(nodeID, jobID int64) (bool, error) {
	status, owner, err := s.st.JobOwner(jobID)
	if err != nil {
		return false, err
	}
	return status == "running" && owner == store.NodeWorkerID(nodeID), nil
}

// Complete applies a node's job verdict with the same retry policy the local
// runner uses: ordinary failures requeue until the attempt budget is spent,
// cancellations requeue without consuming an attempt, and success finishes
// the job. It reports whether the verdict was accepted or the lease had been
// superseded.
//
//nolint:gocyclo // the verdict switch mirrors the local runner's retry policy
func (s *Service) Complete(nodeID, jobID int64, status, errMsg string) (bool, error) {
	owned, err := s.ownsJob(nodeID, jobID)
	if err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	switch status {
	case "done":
		if err := s.st.FinishJob(jobID, "done", ""); err != nil {
			return true, err
		}
		return true, s.st.BumpNodeCounters(nodeID, false)
	case "error":
		j, err := s.st.JobByID(jobID)
		if err != nil {
			return true, err
		}
		if j != nil && j.Attempts < maxAttempts {
			if rerr := s.st.RequeueJob(jobID, errMsg); rerr != nil {
				return true, rerr
			}
			return true, nil
		}
		if err := s.st.FinishJob(jobID, "error", errMsg); err != nil {
			return true, err
		}
		return true, s.st.BumpNodeCounters(nodeID, true)
	case "canceled":
		note := "canceled"
		if paused, err := s.st.GetQueuePaused(); err == nil && paused {
			note = "paused"
		} else if active, serr := s.st.HasActiveScanJob(); serr == nil && active {
			// Mirror the scan gate: canceled probes yield the media share
			// to the scan instead of burning an attempt.
			note = "scan-active"
		}
		return true, s.st.RequeueJobSoft(jobID, note)
	default:
		return true, fmt.Errorf("unknown completion status %q", status)
	}
}

// ApplyProbeResult persists a node's probe outcome and finishes the job. The
// uploaded subtitle file, when present, lands at the canonical server-side
// subs cache path so later re-probes can reuse it.
func (s *Service) ApplyProbeResult(nodeID, jobID int64, o *probe.Outcome, subFile io.Reader) (bool, error) {
	owned, err := s.ownsJob(nodeID, jobID)
	if err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	if o == nil {
		return true, fmt.Errorf("missing probe result")
	}
	var payload struct {
		MediaID int64 `json:"mediaId"`
	}
	j, jerr := s.st.JobByID(jobID)
	if jerr != nil {
		return true, jerr
	}
	if j == nil {
		return false, nil
	}
	if uerr := json.Unmarshal([]byte(j.Payload), &payload); uerr != nil {
		return true, fmt.Errorf("bad probe payload: %w", uerr)
	}
	m, merr := s.st.GetMedia(payload.MediaID)
	if merr != nil {
		return true, merr
	}
	if m == nil {
		if ferr := s.st.FinishJob(jobID, "error", "media row missing"); ferr != nil {
			log.Printf("[nodes] finish missing media job %d: %v", jobID, ferr)
		}
		return false, nil
	}
	if aerr := s.Pipeline.Apply(m.ID, m.Path, o, subFile, log.Default()); aerr != nil {
		return true, aerr
	}
	if ferr := s.st.FinishJob(jobID, "done", ""); ferr != nil {
		return true, ferr
	}
	return true, s.st.BumpNodeCounters(nodeID, false)
}

// ApplyClipResult stores a node's rendered clip into the server cache under
// the cache key the server itself derives, then finishes the job. If the job
// was requeued meanwhile (superseded), the upload is dropped.
func (s *Service) ApplyClipResult(nodeID, jobID int64, ext string, file io.Reader) (bool, error) {
	owned, err := s.ownsJob(nodeID, jobID)
	if err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	if s.Clips == nil {
		return true, fmt.Errorf("clip service not configured")
	}
	j, err := s.st.JobByID(jobID)
	if err != nil {
		return true, err
	}
	if j == nil {
		return false, nil
	}
	req, err := clipRequestFromPayload(j.Payload, s.cfg.Clips.DefaultPadMs)
	if err != nil {
		return true, fmt.Errorf("bad clip payload: %w", err)
	}
	key := s.Clips.CacheKey(req)
	if err := s.Clips.RegisterRendered(key, ext, file); err != nil {
		return true, err
	}
	if ferr := s.st.FinishJob(jobID, "done", ""); ferr != nil {
		return true, ferr
	}
	return true, s.st.BumpNodeCounters(nodeID, false)
}

// Approve approves a pending node, generating and returning its claim token
// once. The token is not shown again; nodes persist it from their next
// registration poll.
func (s *Service) Approve(nodeID int64) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	if err := s.st.ApproveNode(nodeID, token); err != nil {
		return err
	}
	log.Printf("[nodes] node %d approved", nodeID)
	return nil
}

// Revoke revokes an approved node's token; its claims start failing
// immediately.
func (s *Service) Revoke(nodeID int64) error {
	if err := s.st.RevokeNode(nodeID); err != nil {
		return err
	}
	log.Printf("[nodes] node %d revoked", nodeID)
	return nil
}

// Janitor periodically requeues running jobs whose claiming node stopped
// heartbeating (or was deleted), so a crashed node cannot strand work until
// the next server restart.
func (s *Service) Janitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-s.cfg.OfflineAfter).UnixMilli()
			n, err := s.st.RequeueStaleNodeJobs(cutoff, "node offline")
			if err != nil {
				log.Printf("[nodes] janitor requeue: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("[nodes] janitor requeued %d job(s) from offline nodes", n)
			}
		}
	}
}

// List builds the admin dashboard view: every node plus its live status and
// in-flight jobs.
func (s *Service) List() ([]NodeView, error) {
	ns, err := s.st.ListNodes()
	if err != nil {
		return nil, err
	}
	running, err := s.st.RunningNodeJobs()
	if err != nil {
		return nil, err
	}
	byNode := map[int64][]store.Job{}
	for _, j := range running {
		id := store.NodeIDFromWorker(j.WorkerID)
		if id > 0 {
			byNode[id] = append(byNode[id], j)
		}
	}
	cutoff := time.Now().Add(-s.cfg.OfflineAfter).UnixMilli()
	out := make([]NodeView, 0, len(ns))
	for _, n := range ns {
		out = append(out, NodeView{
			Node:        n,
			Online:      n.LastSeen >= cutoff,
			CurrentJobs: byNode[n.ID],
		})
	}
	return out, nil
}

// Delete removes a node and releases any leases it still holds, so deletion
// cannot strand running jobs.
func (s *Service) Delete(nodeID int64) error {
	if _, err := s.st.RequeueNodeJobs(nodeID, "node deleted"); err != nil {
		return err
	}
	return s.st.DeleteNode(nodeID)
}

// truncate clamps operator-controlled strings to sane lengths.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
