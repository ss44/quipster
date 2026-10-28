package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"quipster/internal/store"
)

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".m4v": true, ".webm": true,
	".ts": true, ".mpg": true, ".mpeg": true, ".wmv": true, ".flv": true, ".ogv": true,
	".m2ts": true, ".mts": true, ".divx": true, ".vob": true, ".3gp": true, ".ogm": true,
}

var skipDirs = map[string]bool{
	"extras": true, "extra": true, "samples": true, "sample": true, "trailers": true,
	"trailer": true, "featurettes": true, "behindthescenes": true, ".deletedbyinference": true,
	".grab": true, "lost+found": true,
}

const (
	defaultBatchSize   = 200
	defaultScanTimeout = 2 * time.Hour
	progressInterval   = 2 * time.Second
	probeEnqueueBatch  = 1000
)

// Result summarizes one scan walk: how many files were seen, added,
// changed, and removed.
type Result struct {
	ScanID     int64
	Seen       int
	Walked     int
	Added      int
	Changed    int
	Removed    int
	Probes     int
	Errors     int
	WalkErrors int
}

// Options tunes a scan run. Timeout is applied by JobHandler; BatchSize
// controls how many file-system entries are committed per transaction.
type Options struct {
	Timeout   time.Duration
	BatchSize int
}

// Option customizes a scan handler.
type Option func(*Options)

// WithTimeout bounds a scan job. A non-positive duration disables the
// scan-specific timeout and leaves the runner's JOB_TIMEOUT as the only bound.
func WithTimeout(d time.Duration) Option {
	return func(o *Options) { o.Timeout = d }
}

// WithBatchSize sets how many media entries are upserted per transaction.
func WithBatchSize(n int) Option {
	return func(o *Options) {
		if n > 0 {
			o.BatchSize = n
		}
	}
}

// CleanRoots normalises library folder paths relative to the media root:
// trims separators, rejects absolute/escaping paths, and de-duplicates.
func CleanRoots(roots []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		if filepath.IsAbs(r) || strings.HasPrefix(r, "/") {
			return nil, fmt.Errorf("library folder %q must be relative to the media root", raw)
		}
		r = strings.Trim(r, "/")
		clean := filepath.ToSlash(filepath.Clean(r))
		if clean == "." || clean == "" {
			continue
		}
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return nil, fmt.Errorf("library folder %q escapes the media root", raw)
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out, nil
}

// Run walks the media root (or only the configured roots), upserts media rows
// and enqueues probe jobs for new or changed files. With prune=true, media rows
// outside the configured folders are removed (Meili cleanup via onDeleted).
func Run(ctx context.Context, mediaRoot string, roots []string, prune bool, st *store.Store, onDeleted func(ids []int64)) (*Result, error) {
	return runWithOptions(ctx, mediaRoot, roots, prune, st, onDeleted, Options{BatchSize: defaultBatchSize})
}

// runWithOptions is the testable scan entry point. Probe enqueueing is
// deliberately deferred until the walk completes: probing the first media
// file while the walker is still traversing the library makes every stat and
// SQLite write contend with a full-file read from the same network share.
func runWithOptions(ctx context.Context, mediaRoot string, roots []string, prune bool, st *store.Store, onDeleted func(ids []int64), opts Options) (res *Result, err error) {
	res = &Result{}
	scanLabel := mediaRoot
	if len(roots) > 0 {
		scanLabel = mediaRoot + " [" + strings.Join(roots, ", ") + "]"
	}
	scanID, err := st.StartScan(scanLabel)
	if err != nil {
		return nil, err
	}
	res.ScanID = scanID
	started := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[scan] #%d panic: %v\n%s", scanID, rec, debug.Stack())
			err = fmt.Errorf("scan panic: %v", rec)
		}
		status := "done"
		if err != nil {
			status = "error"
		}
		if uerr := st.UpdateScan(scanID, int64(res.Seen), int64(res.Seen), status); uerr != nil {
			log.Printf("[scan] #%d update status %s: %v", scanID, status, uerr)
		}
	}()

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	// Recover media whose previous scan wrote the row but died before it could
	// enqueue its probe. This is best-effort: the walk should still run if the
	// recovery query is temporarily unavailable.
	if n, rerr := recoverPendingProbes(ctx, st); rerr != nil {
		log.Printf("[scan] #%d pending probe recovery: %v", scanID, rerr)
	} else if n > 0 {
		log.Printf("[scan] #%d recovered %d pending probe job(s)", scanID, n)
	}

	keep := map[string]bool{}
	batch := newMediaBatch(st, res, batchSize)
	walkRoots := roots
	if len(walkRoots) == 0 {
		walkRoots = []string{""}
	}
	if walkErr := walkAll(ctx, mediaRoot, walkRoots, st, res, keep, batch, scanID, started); walkErr != nil {
		return res, walkErr
	}
	batch.Flush()
	pruneOrLog(st, mediaRoot, walkRoots, keep, prune, res, scanID, onDeleted)

	probes, perr := enqueueProbes(ctx, st, batch.probeIDs)
	res.Probes = probes
	if perr != nil {
		log.Printf("[scan] #%d enqueue probe jobs after %d file(s): %v", scanID, probes, perr)
	}
	log.Printf("[scan] #%d complete roots=%v prune=%v walked=%d seen=%d changed=%d removed=%d probes=%d errors=%d walk_errors=%d elapsed=%s",
		scanID, roots, prune, res.Walked, res.Seen, res.Changed, res.Removed, res.Probes, res.Errors, res.WalkErrors,
		time.Since(started).Round(time.Second))
	return res, nil
}

// walkAll walks every root, sharing the keep set and upsert batch. A shared
// batch lets a large root commit in bounded transaction sizes without holding a
// transaction open across the whole file-system walk.
func walkAll(ctx context.Context, mediaRoot string, roots []string, st *store.Store, res *Result, keep map[string]bool, batch *mediaBatch, scanID int64, started time.Time) error {
	for _, relRoot := range roots {
		root := mediaRoot
		if relRoot != "" {
			root = filepath.Join(mediaRoot, filepath.FromSlash(relRoot))
		}
		w := &walker{
			mediaRoot: mediaRoot,
			root:      root,
			st:        st,
			res:       res,
			keep:      keep,
			scanID:    scanID,
			started:   started,
			batch:     batch,
		}
		walkErr := w.walkWithHeartbeat(ctx)
		batch.Flush()
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// pruneOrLog removes rows for files that were not seen, but only when the walk
// completed without a single read/stat error. A partial tree is not evidence
// that the unvisited files disappeared.
func pruneOrLog(st *store.Store, mediaRoot string, walkRoots []string, keep map[string]bool, prune bool, res *Result, scanID int64, onDeleted func([]int64)) {
	if res.WalkErrors > 0 {
		log.Printf("[scan] #%d skipping prune: %d walk error(s) left the tree incomplete", scanID, res.WalkErrors)
		return
	}
	removedIDs := deleteMissing(st, mediaRoot, walkRoots, keep, prune)
	res.Removed = len(removedIDs)
	if onDeleted != nil && len(removedIDs) > 0 {
		onDeleted(removedIDs)
	}
}

// deleteMissing limits deletion to walked folders unless pruning was requested.
func deleteMissing(st *store.Store, mediaRoot string, walkRoots []string, keep map[string]bool, prune bool) []int64 {
	if prune {
		ids, err := st.DeleteMissingUnder(mediaRoot, keep)
		if err != nil {
			log.Printf("[scan] delete missing: %v", err)
		}
		return ids
	}
	var removedIDs []int64
	for _, relRoot := range walkRoots {
		base := mediaRoot
		if relRoot != "" {
			base = filepath.Join(mediaRoot, filepath.FromSlash(relRoot))
		}
		ids, err := st.DeleteMissingUnder(base, keep)
		if err != nil {
			log.Printf("[scan] delete missing under %s: %v", base, err)
			continue
		}
		removedIDs = append(removedIDs, ids...)
	}
	return removedIDs
}

// isIgnored checks if a directory entry should be skipped or passed over during walk.
func isIgnored(path, root string, d fs.DirEntry) (bool, error) {
	name := d.Name()
	if d.IsDir() {
		if path != root && (skipDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".")) {
			return true, fs.SkipDir
		}
		return true, nil
	}
	if strings.HasPrefix(name, ".") {
		return true, nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !videoExts[ext] {
		return true, nil
	}
	return false, nil
}

// walker owns the mutable state for one root walk. A heartbeat logs the current
// path every couple of seconds so a blocked stat on a network mount is visible
// in the admin logs instead of looking like a silent hang.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type walker struct {
	st        *store.Store
	res       *Result
	keep      map[string]bool
	batch     *mediaBatch
	mediaRoot string
	root      string
	current   string
	scanID    int64
	started   time.Time
	mu        sync.Mutex
}

// walkWithHeartbeat guarantees the progress goroutine is stopped even when a
// parser or store panic unwinds the walk.
func (w *walker) walkWithHeartbeat(ctx context.Context) error {
	stop := w.startHeartbeat(ctx)
	defer stop()
	return w.walk(ctx)
}

func (w *walker) startHeartbeat(ctx context.Context) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.mu.Lock()
				seen, walked, current := w.res.Seen, w.res.Walked, w.current
				w.mu.Unlock()
				log.Printf("[scan] #%d progress seen=%d walked=%d elapsed=%s current=%q",
					w.scanID, seen, walked, time.Since(w.started).Round(time.Second), current)
				if uerr := w.st.UpdateScan(w.scanID, int64(seen), int64(seen), ""); uerr != nil {
					log.Printf("[scan] #%d progress update: %v", w.scanID, uerr)
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// walk visits one root. Directory entries are counted even when they are not
// media so a walk stuck in a large non-media tree still shows forward progress.
func (w *walker) walk(ctx context.Context) error {
	return filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == w.root {
				// A configured root that cannot be read is a failed scan, not a
				// partial walk: surface it so the job retries and the admin sees
				// an error instead of a false "done".
				return fmt.Errorf("scan root %s: %w", path, err)
			}
			w.mu.Lock()
			w.res.WalkErrors++
			w.mu.Unlock()
			log.Printf("[scan] walk %s: %v", path, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.mu.Lock()
		w.res.Walked++
		w.current = path
		w.mu.Unlock()
		if skip, walkErr := isIgnored(path, w.root, d); skip {
			return walkErr
		}

		// Keep the path before stat so a transient stat failure cannot cause
		// the prune pass to delete an existing media row.
		w.keep[path] = true
		rel, err := filepath.Rel(w.mediaRoot, path)
		if err != nil {
			w.mu.Lock()
			w.res.WalkErrors++
			w.mu.Unlock()
			log.Printf("[scan] rel %s: %v", path, err)
			return nil //nolint:nilerr // path outside root: skip and continue the walk.
		}
		info, err := d.Info()
		if err != nil {
			w.mu.Lock()
			w.res.WalkErrors++
			w.mu.Unlock()
			log.Printf("[scan] stat %s: %v", path, err)
			return nil //nolint:nilerr // unreadable entry: skip and continue the walk.
		}
		w.mu.Lock()
		w.res.Seen++
		w.mu.Unlock()

		p := Parse(filepath.ToSlash(rel))
		w.batch.Add(store.MediaUpsert{
			Path:      path,
			Container: strings.TrimPrefix(strings.ToLower(filepath.Ext(d.Name())), "."),
			Size:      info.Size(),
			Mtime:     info.ModTime().Unix(),
			Title:     p.Title,
			Year:      p.Year,
			Season:    p.Season,
			Episode:   p.Episode,
			ShowTitle: p.ShowTitle,
		})
		return nil
	})
}

// mediaBatch buffers media rows and commits them in transactions. A failed
// batch is retried entry-by-entry so one malformed row cannot discard the
// progress of the whole batch.
type mediaBatch struct {
	st       *store.Store
	res      *Result
	entries  []store.MediaUpsert
	probeIDs []int64
	size     int
}

func newMediaBatch(st *store.Store, res *Result, size int) *mediaBatch {
	if size <= 0 {
		size = defaultBatchSize
	}
	return &mediaBatch{st: st, res: res, size: size}
}

func (b *mediaBatch) Add(entry store.MediaUpsert) {
	b.entries = append(b.entries, entry)
	if len(b.entries) >= b.size {
		b.Flush()
	}
}

func (b *mediaBatch) Flush() {
	if len(b.entries) == 0 {
		return
	}
	entries := b.entries
	b.entries = nil
	ids, changed, err := b.st.UpsertMediaBatch(entries)
	if err != nil {
		log.Printf("[scan] batch upsert of %d file(s) failed, retrying individually: %v", len(entries), err)
		for _, entry := range entries {
			b.upsertOne(entry)
		}
		return
	}
	for i, id := range ids {
		if changed[i] {
			b.res.Changed++
			b.probeIDs = append(b.probeIDs, id)
		}
	}
}

func (b *mediaBatch) upsertOne(entry store.MediaUpsert) {
	id, changed, err := b.st.UpsertMedia(entry.Path, entry.Container, entry.Size, entry.Mtime,
		entry.Title, entry.Year, entry.Season, entry.Episode, entry.ShowTitle)
	if err != nil {
		b.res.Errors++
		log.Printf("[scan] upsert %s: %v", entry.Path, err)
		return
	}
	if changed {
		b.res.Changed++
		b.probeIDs = append(b.probeIDs, id)
	}
}

// probeSpecs converts media ids to deduplicated probe job specifications.
func probeSpecs(ids []int64) ([]store.JobSpec, error) {
	specs := make([]store.JobSpec, 0, len(ids))
	for _, id := range ids {
		payload, err := json.Marshal(map[string]any{"mediaId": id})
		if err != nil {
			return nil, fmt.Errorf("marshal probe payload %d: %w", id, err)
		}
		specs = append(specs, store.JobSpec{Type: "probe", Payload: string(payload), Priority: 0})
	}
	return specs, nil
}

// enqueueProbeSpecs inserts batches of unique probe jobs and returns how many
// were newly created. Batching keeps a large library's handoff to the queue
// from paying one transaction (and fsync) per file.
func enqueueProbeSpecs(ctx context.Context, st *store.Store, specs []store.JobSpec) (int, error) {
	enqueued := 0
	for start := 0; start < len(specs); start += probeEnqueueBatch {
		if err := ctx.Err(); err != nil {
			return enqueued, err
		}
		end := min(start+probeEnqueueBatch, len(specs))
		n, err := st.EnqueueUniqueJobs(specs[start:end])
		if err != nil {
			return enqueued, err
		}
		enqueued += n
	}
	return enqueued, nil
}

// enqueueProbes hands changed media to the probe queue in bounded batches.
func enqueueProbes(ctx context.Context, st *store.Store, ids []int64) (int, error) {
	specs, err := probeSpecs(ids)
	if err != nil {
		return 0, err
	}
	return enqueueProbeSpecs(ctx, st, specs)
}

// recoverPendingProbes re-enqueues probes for media that were left pending by
// an interrupted or crashed scan. It recovers the whole pending set in bounded
// batches; deduplication keeps active probe jobs from being duplicated.
func recoverPendingProbes(ctx context.Context, st *store.Store) (int, error) {
	ids, err := st.MediaIDsByStatus("pending")
	if err != nil {
		return 0, err
	}
	specs, err := probeSpecs(ids)
	if err != nil {
		return 0, err
	}
	return enqueueProbeSpecs(ctx, st, specs)
}

// Done reports how many files the scan has processed so far.
func (r *Result) Done() int { return r.Seen }

// JobHandler adapts Run to the job runner. Payload carries {"roots": ["movies","tv"],
// "prune": true}; roots are validated relative to the media root. The progress
// callback is unused because scan progress is already latched in the scans table.
func JobHandler(baseRoot string, st *store.Store, onDeleted func(ids []int64), opts ...Option) func(context.Context, *store.Job, func(float64)) error {
	cfg := Options{Timeout: defaultScanTimeout, BatchSize: defaultBatchSize}
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(ctx context.Context, j *store.Job, _ func(float64)) error {
		var payload struct {
			Roots []string `json:"roots"`
			Prune bool     `json:"prune"`
		}
		if b := strings.TrimSpace(j.Payload); b != "" && b != "{}" {
			if err := json.Unmarshal([]byte(b), &payload); err != nil {
				return fmt.Errorf("bad scan payload: %w", err)
			}
		}
		roots, err := CleanRoots(payload.Roots)
		if err != nil {
			return err
		}
		for _, rel := range roots {
			full := filepath.Join(baseRoot, filepath.FromSlash(rel))
			if info, statErr := os.Stat(full); statErr != nil || !info.IsDir() {
				return fmt.Errorf("library folder %s unavailable", full)
			}
		}
		if cfg.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
			defer cancel()
		}
		_, err = runWithOptions(ctx, baseRoot, roots, payload.Prune, st, onDeleted, cfg)
		return err
	}
}

// EnsureDirs creates the data/cache layout used across the app. Directories
// use 0o750 so they stay private to the owning user/group.
func EnsureDirs(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return nil
}
