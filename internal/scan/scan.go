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

type ScanResult struct {
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
func Run(ctx context.Context, mediaRoot string, roots []string, prune bool, st *store.Store, onDeleted func(ids []int64)) (*ScanResult, error) {
	res := &ScanResult{}
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
	var removedIDs []int64
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
			st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "error")
			return res, err
		}
	}

	if prune {
		ids, err := st.DeleteMissingUnder(mediaRoot, keep)
		if err != nil {
			log.Printf("[scan] delete missing: %v", err)
		}
		removedIDs = ids
	} else {
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
	}
	res.Removed = len(removedIDs)

	st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "done")
	if onDeleted != nil && len(removedIDs) > 0 {
		onDeleted(removedIDs)
	}
	return res, nil
}

// walkRoot walks one library folder; library-relative paths are always
// computed from mediaRoot so parsing context stays consistent.
func walkRoot(ctx context.Context, mediaRoot, root string, st *store.Store, res *ScanResult, keep map[string]bool, scanID int64, lastFlush *time.Time) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("[scan] walk %s: %v", path, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skipDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !videoExts[ext] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(mediaRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		keep[path] = true
		res.Seen++

		p := Parse(rel)
		id, changed, err := st.UpsertMedia(path, strings.TrimPrefix(ext, "."), info.Size(), info.ModTime().Unix(),
			p.Title, p.Year, p.Season, p.Episode, p.ShowTitle)
		if err != nil {
			log.Printf("[scan] upsert %s: %v", path, err)
			return nil
		}
		if changed {
			payload, _ := json.Marshal(map[string]any{"mediaId": id})
			if _, err := st.EnqueueJob("probe", string(payload), 0); err != nil {
				log.Printf("[scan] enqueue probe %d: %v", id, err)
			}
			res.Changed++
		}
		if time.Since(*lastFlush) > 400*time.Millisecond {
			*lastFlush = time.Now()
			st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "")
		}
		return nil
	})
}

func (r *ScanResult) Done() int { return r.Seen }

// JobHandler adapts Run to the job runner. Payload carries {"roots": ["movies","tv"],
// "prune": true}; roots are validated relative to the media root.
func JobHandler(baseRoot string, st *store.Store, onDeleted func(ids []int64)) func(context.Context, *store.Job, func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
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
			if info, err := os.Stat(full); err != nil || !info.IsDir() {
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

// EnsureDirs creates the data/cache layout used across the app.
func EnsureDirs(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return nil
}
