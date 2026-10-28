package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quipster/internal/clips"
	"quipster/internal/hardware"
	"quipster/internal/nodes"
	"quipster/internal/probe"
)

// session runs one node process: registration/approval, claim pools, heart
// beats and result uploads.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type session struct {
	cfg     Config
	st      *state
	client  *client
	hw      *hardware.Monitor
	pipe    *probe.Pipeline
	version string

	mu        sync.Mutex
	recoverMu sync.Mutex
	progress  map[int64]float64 // in-flight job progress for heartbeats
	paused    bool
	cancelAll context.CancelFunc
	// cancelProbe cancels only probe-type jobs (scan gate mirror).
	probeCancels map[int64]context.CancelFunc
	clipCancels  map[int64]context.CancelFunc
}

// Run starts a worker node: it registers (or resumes a previously approved
// identity), waits for operator approval, then claims and executes jobs
// until ctx is canceled.
func Run(ctx context.Context, cfg Config, version string) error {
	st, err := loadState(cfg.StatePath)
	if err != nil {
		return err
	}
	if err := ensureDirs(cfg); err != nil {
		return err
	}
	s := &session{
		cfg:     cfg,
		st:      st,
		version: version,
		client:  newClient(cfg.ServerURL),
		hw:      hardware.NewMonitor(probe.FfmpegBin()),
		pipe: &probe.Pipeline{
			SubsCacheDir:   filepath.Join(cfg.CacheDir, "subs"),
			ExtractConns:   cfg.ExtractConns,
			ProbeTimeout:   cfg.ProbeTimeout,
			ExtractTimeout: cfg.ExtractTimeout,
		},
		progress:     map[int64]float64{},
		probeCancels: map[int64]context.CancelFunc{},
		clipCancels:  map[int64]context.CancelFunc{},
	}
	if caps := s.hw.Capabilities(); caps.Available() {
		log.Printf("[node] hardware acceleration: %s (device=%s)", caps.Backend, caps.Device)
	} else {
		log.Printf("[node] hardware acceleration: none, using cpu")
	}
	log.Printf("[node] starting: server=%s media=%s cache=%s state=%s probe_workers=%d clip_workers=%d",
		cfg.ServerURL, cfg.MediaDir, cfg.CacheDir, cfg.StatePath, cfg.ProbeWorkers, cfg.ClipWorkers)

	if err := s.ensureApproved(ctx, version); err != nil {
		return err
	}
	return s.runPools(ctx)
}

// ensureDirs creates the scratch cache layout up front so the first claim
// does not race directory creation.
func ensureDirs(cfg Config) error {
	for _, dir := range []string{cfg.CacheDir, filepath.Join(cfg.CacheDir, "subs"), filepath.Join(cfg.CacheDir, "clips")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// ensureApproved gets the session into the approved state: verify a stored
// token, reissue it from the stored registration secret, or register and poll
// until the operator approves.
func (s *session) ensureApproved(ctx context.Context, version string) error {
	if s.resumeStored(ctx) {
		return nil
	}
	if err := s.register(ctx, version); err != nil {
		return err
	}
	return s.awaitApproval(ctx)
}

// resumeStored tries the persisted claim token first, then the persisted
// registration secret; it reports whether the session is authorized again.
func (s *session) resumeStored(ctx context.Context) bool {
	if s.st.Token != "" {
		s.client.nodeID, s.client.token = s.st.NodeID, s.st.Token
		if _, err := s.client.heartbeat(ctx, nil); err == nil {
			log.Printf("[node] resumed approved node %d %q", s.st.NodeID, s.cfg.Name)
			return true
		} else if !errors.Is(err, errUnauthorized) {
			// Server unreachable or transient failure: fall through to
			// the secret-based poll instead of re-registering, since the
			// token may still be valid.
			log.Printf("[node] token check failed (%v); trying approval poll", err)
		} else {
			log.Printf("[node] stored token was rejected")
		}
		s.client.token = ""
	}
	if s.st.NodeID == 0 || s.st.Secret == "" {
		return false
	}
	s.client.nodeID, s.client.secret = s.st.NodeID, s.st.Secret
	st, err := s.client.state(ctx)
	if err != nil {
		return false
	}
	switch st.Status {
	case nodes.NodeApproved:
		s.adoptToken(st.Token)
		return true
	case nodes.NodeRevoked:
		log.Printf("[node] node %d was revoked; re-registering", s.st.NodeID)
		s.st.Secret, s.st.NodeID, s.st.Token = "", 0, ""
	}
	return false
}

// register posts a fresh registration (first run, or a revoked/lost
// identity) and persists the returned id and secret.
func (s *session) register(ctx context.Context, version string) error {
	if err := s.client.register(ctx, s.cfg.Name, s.st.Fingerprint, version); err != nil {
		return err
	}
	s.st.NodeID, s.st.Secret = s.client.nodeID, s.client.secret
	if err := s.st.save(s.cfg.StatePath); err != nil {
		return err
	}
	return nil
}

// awaitApproval polls until the operator approves (or revokes) the node.
func (s *session) awaitApproval(ctx context.Context) error {
	log.Printf("[node] waiting for approval — open %s/admin/nodes and approve node %q (id %d)",
		s.cfg.ServerURL, s.cfg.Name, s.st.NodeID)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		st, err := s.client.state(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[node] approval poll: %v", err)
		} else {
			switch st.Status {
			case nodes.NodeApproved:
				s.adoptToken(st.Token)
				return nil
			case nodes.NodeRevoked:
				return fmt.Errorf("node %d was revoked by the operator", s.st.NodeID)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// adoptToken stores a freshly issued claim token in memory and on disk.
func (s *session) adoptToken(token string) {
	s.client.token = token
	s.st.Token = token
	if err := s.st.save(s.cfg.StatePath); err != nil {
		log.Printf("[node] persist token: %v", err)
	}
	log.Printf("[node] approved; token stored")
}

// runPools starts the claim pools and the heartbeat loop, and waits for the
// context to end.
func (s *session) runPools(ctx context.Context) error {
	ctx, s.cancelAll = context.WithCancel(ctx)
	defer s.cancelAll()
	var wg sync.WaitGroup
	startPool := func(jobType string, n int) {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				s.pool(ctx, jobType, worker)
			}(i)
		}
	}
	startPool("probe", s.cfg.ProbeWorkers)
	startPool("clip", s.cfg.ClipWorkers)

	beatStop := make(chan struct{})
	go s.heartbeatLoop(ctx, beatStop)

	<-ctx.Done()
	log.Printf("[node] shutting down")
	close(beatStop)
	wg.Wait()
	return nil
}

// heartbeatLoop reports progress and mirrors server-side pause/scan state
// onto local job cancellation, exactly like the local runner's pause watcher
// and scan gate.
func (s *session) heartbeatLoop(ctx context.Context, stop chan struct{}) {
	ticker := time.NewTicker(s.cfg.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			jobs := make([]nodes.JobProgress, 0, len(s.progress))
			for id, p := range s.progress {
				jobs = append(jobs, nodes.JobProgress{ID: id, Progress: p})
			}
			s.mu.Unlock()
			beat, err := s.client.heartbeat(ctx, jobs)
			if err != nil {
				if errors.Is(err, errUnauthorized) {
					log.Printf("[node] token rejected at heartbeat; approval may have been revoked")
				} else if ctx.Err() == nil {
					log.Printf("[node] heartbeat: %v", err)
				}
				continue
			}
			s.mu.Lock()
			s.paused = beat.Paused
			s.mu.Unlock()
			if beat.Paused {
				log.Printf("[node] queue paused by server; canceling in-flight jobs")
				s.cancelJobs(s.probeCancels)
				s.cancelJobs(s.clipCancels)
			} else if beat.ScanActive {
				// Mirror the scan gate: probes yield the media share.
				s.cancelJobs(s.probeCancels)
			}
		}
	}
}

// cancelJobs fires every tracked cancel func and clears the map.
func (s *session) cancelJobs(m map[int64]context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cancel := range m {
		cancel()
		delete(m, id)
	}
}

// pool claims and executes jobs of one type until the context ends. Pause
// state stops claiming; the heartbeat loop cancels in-flight work when the
// server pauses the queue or starts a scan.
func (s *session) pool(ctx context.Context, jobType string, worker int) {
	workerID := fmt.Sprintf("%s-%d", jobType, worker)
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if paused {
			if !sleepCtx(ctx, s.cfg.Heartbeat) {
				return
			}
			continue
		}
		w, ok, err := s.client.claim(ctx, []string{jobType})
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				log.Printf("[node] (%s) token rejected at claim; re-registering", workerID)
				if rerr := s.recoverAuthorization(ctx); rerr != nil {
					log.Printf("[node] (%s) re-registration: %v", workerID, rerr)
				}
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("[node] (%s) claim: %v", workerID, err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}
		if !ok {
			if !sleepCtx(ctx, 750*time.Millisecond) {
				return
			}
			continue
		}
		s.execute(ctx, workerID, w)
	}
}

// recoverAuthorization re-registers the node after its token was rejected,
// returning the node to the approval-waiting state. Pools serialize on the
// recovery mutex: the first caller re-registers, later callers see the
// resulting token or wait through the approval poll.
func (s *session) recoverAuthorization(ctx context.Context) error {
	s.recoverMu.Lock()
	defer s.recoverMu.Unlock()
	s.st.Token = ""
	s.client.token = ""
	s.st.Secret = ""
	s.st.NodeID = 0
	if err := s.st.save(s.cfg.StatePath); err != nil {
		return err
	}
	return s.ensureApproved(ctx, s.version)
}

// execute claims ownership of one job's lifecycle: per-job context with the
// job timeout, progress tracking for heartbeats, and verdict reporting.
func (s *session) execute(ctx context.Context, workerID string, w *nodes.Work) {
	jctx, cancel := context.WithTimeout(ctx, s.cfg.JobTimeout)
	defer cancel()

	s.mu.Lock()
	s.progress[w.JobID] = 0
	if w.Type == "probe" {
		s.probeCancels[w.JobID] = cancel
	} else {
		s.clipCancels[w.JobID] = cancel
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.progress, w.JobID)
		if w.Type == "probe" {
			delete(s.probeCancels, w.JobID)
		} else {
			delete(s.clipCancels, w.JobID)
		}
		s.mu.Unlock()
	}()

	report := func(p float64) {
		s.mu.Lock()
		s.progress[w.JobID] = p
		s.mu.Unlock()
	}

	log.Printf("[node] (%s) claimed job %d (%s)", workerID, w.JobID, w.Type)
	var err error
	switch w.Type {
	case "probe":
		err = s.runProbe(jctx, w, report)
	case "clip":
		err = s.runClip(jctx, w, report)
	default:
		err = fmt.Errorf("unsupported job type %q", w.Type)
	}

	switch {
	case err == nil:
		// Probe and clip results finish the job server-side during
		// upload; the done verdict is a no-op confirmation.
		s.reportComplete(ctx, workerID, w.JobID, "done", "")
	case errors.Is(err, context.Canceled), ctx.Err() != nil:
		// Pause, scan suspension or shutdown: the server requeues
		// without consuming an attempt.
		s.reportComplete(ctx, workerID, w.JobID, "canceled", "")
	default:
		log.Printf("[node] (%s) job %d failed: %v", workerID, w.JobID, err)
		s.reportComplete(ctx, workerID, w.JobID, "error", err.Error())
	}
}

// reportComplete delivers the job verdict with a short retry loop so a
// transient network blip cannot orphan finished work.
func (s *session) reportComplete(ctx context.Context, workerID string, jobID int64, status, errMsg string) {
	if ctx.Err() != nil {
		// Shutdown: one best-effort attempt with a fresh short context.
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = bctx
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := s.client.complete(ctx, jobID, status, errMsg)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[node] (%s) complete job %d gave up: %v", workerID, jobID, err)
			return
		}
		log.Printf("[node] (%s) complete job %d retrying: %v", workerID, jobID, err)
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

// runProbe executes a claimed probe job locally and uploads the outcome.
func (s *session) runProbe(ctx context.Context, w *nodes.Work, report func(float64)) error {
	localPath, err := s.localMediaPath(w)
	if err != nil {
		return err
	}
	logger := log.New(log.Writer(), fmt.Sprintf("[node] probe media %d job %d: ", w.Media.ID, w.JobID), log.Flags())
	o, cerr := s.pipe.Compute(ctx, probe.ComputeOptions{
		MediaID:     w.Media.ID,
		MediaPath:   localPath,
		KnownStatus: w.Media.Status,
		Logger:      logger,
		Progress:    report,
	})
	if cerr != nil {
		return cerr
	}
	resultJSON, merr := json.Marshal(o)
	if merr != nil {
		return fmt.Errorf("marshal probe outcome: %w", merr)
	}
	files := map[string]uploadFile{}
	if o.SubFile != "" {
		path := o.SubFile
		files["subtitle"] = uploadFile{
			Name: filepath.Base(path),
			Open: func() (io.ReadCloser, error) { return os.Open(path) }, //nolint:gosec // path is built from the node's own cache dir
		}
	}
	if uerr := s.client.uploadResult(ctx, w.JobID, resultJSON, files); uerr != nil {
		return fmt.Errorf("upload probe result: %w", uerr)
	}
	log.Printf("[node] probe media %d job %d: outcome=%s uploaded", w.Media.ID, w.JobID, o.Kind)
	return nil
}

// runClip renders a claimed clip job locally and uploads the file.
func (s *session) runClip(ctx context.Context, w *nodes.Work, report func(float64)) error {
	if w.Clip == nil {
		return fmt.Errorf("clip job %d missing clip parameters", w.JobID)
	}
	if w.Clips == nil {
		return fmt.Errorf("clip job %d missing server clip settings", w.JobID)
	}
	localPath, err := s.localMediaPath(w)
	if err != nil {
		return err
	}
	req := clips.Request{MediaID: w.Media.ID, Start: w.Clip.Start, End: w.Clip.End, PadMs: w.Clip.PadMs, Mode: w.Clip.Mode}
	// The renderer mirrors the server's encoding profile so the uploaded
	// file matches the cache key the server registers.
	svc := clips.New(nil, clips.Config{
		CacheDir:     s.cfg.CacheDir,
		PreviewRes:   w.Clips.PreviewRes,
		AccurateRes:  w.Clips.AccurateRes,
		CPUPreset:    w.Clips.CPUPreset,
		MaxClipSec:   w.Clips.MaxClipSec,
		DefaultPadMs: w.Clips.DefaultPadMs,
		Hw:           s.hw,
	})
	path, ext, rerr := svc.RenderLocal(ctx, localPath, w.Clip.AudioOrdinal, req, report)
	if rerr != nil {
		return rerr
	}
	resultJSON, _ := json.Marshal(map[string]string{"ext": ext}) //nolint:errcheck // the map holds one string, so marshaling cannot fail
	files := map[string]uploadFile{
		"clip": {
			Name: "render." + ext,
			Open: func() (io.ReadCloser, error) { return os.Open(path) }, //nolint:gosec // path is built from the node's own cache dir
		},
	}
	if uerr := s.client.uploadResult(ctx, w.JobID, resultJSON, files); uerr != nil {
		return fmt.Errorf("upload clip render: %w", uerr)
	}
	log.Printf("[node] clip media %d job %d: rendered %s uploaded", w.Media.ID, w.JobID, ext)
	return nil
}

// localMediaPath resolves the node-local media path for a claimed job using
// the operator's mapping (NODE_MEDIA_MAP) or the root-to-root default.
func (s *session) localMediaPath(w *nodes.Work) (string, error) {
	m, err := newMapper(w.MediaRoot, s.cfg.MediaDir, s.cfg.MediaMap)
	if err != nil {
		return "", err
	}
	return m.local(w.Media.Path)
}

// sleepCtx sleeps for d or until ctx ends; false means the context ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
