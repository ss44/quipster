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

// Run walks the media root, upserts media rows and enqueues probe jobs for
// new or changed files. Missing files are deleted (with Meili cleanup via
// onDeleted, if provided).
func Run(ctx context.Context, root string, st *store.Store, onDeleted func(ids []int64)) (*ScanResult, error) {
	res := &ScanResult{}
	scanID, err := st.StartScan(root)
	if err != nil {
		return nil, err
	}
	res.ScanID = scanID
	keep := map[string]bool{}
	var removedIDs []int64
	lastFlush := time.Now()

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(root, path)
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
		if time.Since(lastFlush) > 400*time.Millisecond {
			lastFlush = time.Now()
			st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "")
		}
		return nil
	})
	if err != nil {
		st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "error")
		return res, err
	}

	ids, err := st.DeleteMissing(root, keep)
	if err != nil {
		log.Printf("[scan] delete missing: %v", err)
	}
	res.Removed = len(ids)
	removedIDs = ids

	st.UpdateScan(scanID, int64(res.Seen), int64(res.Done()), "done")
	if onDeleted != nil && len(removedIDs) > 0 {
		onDeleted(removedIDs)
	}
	return res, nil
}

func (r *ScanResult) Done() int { return r.Seen }

// JobHandler adapts Run to the job runner.
func JobHandler(root string, st *store.Store, onDeleted func(ids []int64)) func(context.Context, *store.Job, func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
		res, err := Run(ctx, root, st, onDeleted)
		if err != nil {
			return err
		}
		log.Printf("[scan] #%d: seen=%d changed=%d removed=%d", res.ScanID, res.Seen, res.Changed, res.Removed)
		return nil
	}
}

// ParsePayload extracts {"scanId":n} style payloads.
func ParsePayload(j *store.Job) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(j.Payload), &m); err != nil {
		return map[string]any{}
	}
	return m
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