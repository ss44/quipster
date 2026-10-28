package clips

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// cut is the store-free render core shared by the service's cache-backed
// paths and worker-node renders: it cuts the requested region of src into
// the cache directory and returns the output path and extension. It performs
// no validation and no store bookkeeping, which lets a worker node render a
// clip for a coordinating server without database access. report is forwarded
// to the transcode path for 0..1 progress and may be nil.
func (s *Service) cut(ctx context.Context, key, src string, audioOrdinal int, r Request, report func(float64)) (string, string, error) {
	pad := float64(r.PadMs) / 1000
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil {
		return "", "", mkErr
	}
	start := r.Start - pad
	if start < 0 {
		start = 0
	}
	outBase := filepath.Join(outDir, key)

	var path string
	var err error
	if r.Mode == "fast" {
		dur := (r.End + pad) - start
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		path, err = s.cutFast(ctx, src, start, dur, audioOrdinal, outBase)
	} else {
		dur := r.End - r.Start + 2*pad
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		if r.Mode == "preview" {
			path, err = s.cutPreview(ctx, src, start, dur, audioOrdinal, outBase)
		} else {
			path, err = s.cutAccurate(ctx, src, start, dur, audioOrdinal, outBase, report)
		}
	}
	if err != nil {
		return "", "", err
	}
	return path, strings.TrimPrefix(filepath.Ext(path), "."), nil
}

// registerCacheEntry records a rendered file for LRU accounting; failures
// surface because a missing row would make the clip unreachable.
func (s *Service) registerCacheEntry(key, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	return s.st.UpsertCacheEntry(key, path, "clip", st.Size())
}

// RenderLocal renders a clip for a worker node. Unlike the cache-backed
// service paths it takes the media path and audio stream ordinal directly and
// skips store validation and cache registration: the node uploads the result
// to the coordinating server, which owns the cache entry. The node's clip
// configuration must mirror the server's so the encoding profile matches the
// cache key the server will register.
func (s *Service) RenderLocal(ctx context.Context, src string, audioOrdinal int, r Request, report func(float64)) (string, string, error) {
	return s.cut(ctx, s.CacheKey(r), src, audioOrdinal, r, report)
}

// RegisterRendered stores an externally rendered clip (from a worker node)
// into the cache directory under the given cache key and records the cache
// entry, exactly as a local render would have. The extension selects the
// container; only mp4 and mkv are accepted because those are the outputs the
// render paths produce.
func (s *Service) RegisterRendered(key, ext string, r io.Reader) error {
	if ext != "mp4" && ext != "mkv" {
		return fmt.Errorf("unsupported clip container %q", ext)
	}
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(outDir, key+"."+ext) //nolint:gosec // the key is a service-derived hash and ext is allowlisted
	f, err := os.Create(path)                  //nolint:gosec // path is derived from the service cache dir and a hash key
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(f, r)
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		removePartial(path)
		return fmt.Errorf("store node render: %w", copyErr)
	}
	if err := s.st.UpsertCacheEntry(key, path, "clip", size); err != nil {
		removePartial(path)
		return err
	}
	return nil
}
