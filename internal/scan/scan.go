package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
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

// Result summarizes one scan walk: how many files were seen, added,
// changed, and removed.
type Result struct {
	ScanID  int64
	Seen    int
	Added   int
	Changed int
	Removed int
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
	res := &Result{}
	scanLabel := mediaRoot
	if len(roots) > 0 {
		scanLabel = mediaRoot + " [" + strings.Join(roots, ", ") + "]"
	}
	scanID, err := st.StartScan(scanLabel)
	if err != nil {
		return nil, err
	}
	res.ScanID = scanID
	keep := map[string]bool{}
	lastFlush := time.Now()

	walkRoots := roots
	if len(walkRoots) == 0 {
		walkRoots = []string{""}
	}
	for _, relRoot := range walkRoots {
		root := mediaRoot
		if relRoot != "" {
			root = filepath.Join(mediaRoot, filepath.FromSlash(relRoot))
		}
		err = walkRoot(ctx, mediaRoot, root, st, res, keep, scanID, &lastFlush)
		if err != nil {
			if uerr := st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "error"); uerr != nil {
				log.Printf("[scan] update status error: %v", uerr)
			}
			return res, err
		}
	}

	removedIDs := deleteMissing(st, mediaRoot, walkRoots, keep, prune)
	res.Removed = len(removedIDs)

	if uerr := st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "done"); uerr != nil {
		log.Printf("[scan] update status done: %v", uerr)
	}
	if onDeleted != nil && len(removedIDs) > 0 {
		onDeleted(removedIDs)
	}
	return res, nil
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

// walkRoot walks one library folder; library-relative paths are always
// computed from mediaRoot so parsing context stays consistent.
func walkRoot(ctx context.Context, mediaRoot, root string, st *store.Store, res *Result, keep map[string]bool, scanID int64, lastFlush *time.Time) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("[scan] walk %s: %v", path, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if skip, walkErr := isIgnored(path, root, d); skip {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			log.Printf("[scan] stat %s: %v", path, err)
			return nil //nolint:nilerr // unreadable entry: skip and continue the walk.
		}
		rel, err := filepath.Rel(mediaRoot, path)
		if err != nil {
			log.Printf("[scan] rel %s: %v", path, err)
			return nil //nolint:nilerr // path outside root: skip and continue the walk.
		}
		rel = filepath.ToSlash(rel)
		keep[path] = true
		res.Seen++

		if !recordMedia(st, res, path, rel, strings.ToLower(filepath.Ext(d.Name())), info) {
			return nil
		}
		if time.Since(*lastFlush) > 400*time.Millisecond {
			*lastFlush = time.Now()
			if uerr := st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), ""); uerr != nil {
				log.Printf("[scan] update progress: %v", uerr)
			}
		}
		return nil
	})
}

// recordMedia upserts metadata and queues probing when file contents changed.
// A failed upsert or payload prevents this entry from triggering a progress flush.
func recordMedia(st *store.Store, res *Result, path, rel, ext string, info fs.FileInfo) bool {
	p := Parse(rel)
	id, changed, err := st.UpsertMedia(path, strings.TrimPrefix(ext, "."), info.Size(), info.ModTime().Unix(),
		p.Title, p.Year, p.Season, p.Episode, p.ShowTitle)
	if err != nil {
		log.Printf("[scan] upsert %s: %v", path, err)
		return false
	}
	if changed {
		payload, merr := json.Marshal(map[string]any{"mediaId": id})
		if merr != nil {
			log.Printf("[scan] marshal probe payload %d: %v", id, merr)
			return false
		}
		if _, err := st.EnqueueJob("probe", string(payload), 0); err != nil {
			log.Printf("[scan] enqueue probe %d: %v", id, err)
		}
		res.Changed++
	}
	return true
}

// Done reports how many files the scan has processed so far.
func (r *Result) Done() int { return r.Seen }

// JobHandler adapts Run to the job runner. Payload carries {"roots": ["movies","tv"],
// "prune": true}; roots are validated relative to the media root. The progress
// callback is unused because scan progress is already latched in the scans table.
func JobHandler(baseRoot string, st *store.Store, onDeleted func(ids []int64)) func(context.Context, *store.Job, func(float64)) error {
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
		res, err := Run(ctx, baseRoot, roots, payload.Prune, st, onDeleted)
		if err != nil {
			return err
		}
		log.Printf("[scan] #%d: roots=%v prune=%v seen=%d changed=%d removed=%d", res.ScanID, roots, payload.Prune, res.Seen, res.Changed, res.Removed)
		return nil
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
