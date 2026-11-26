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
func (s *Renderer) cut(ctx context.Context, key, src string, audioOrdinal, width, height int, r Request, cues []SubtitleCue, report func(float64)) (string, string, error) {
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
	switch r.Mode {
	case "fast":
		dur := (r.End + pad) - start
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		path, err = s.cutFast(ctx, src, start, dur, audioOrdinal, outBase)
	case "preview":
		dur := r.End - r.Start + 2*pad
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		path, err = s.cutPreview(ctx, src, start, dur, audioOrdinal, outBase)
	case "gif":
		dur := r.End - r.Start + 2*pad
		maxSec := min(s.cfg.MaxGifSec, s.cfg.MaxClipSec)
		if dur > maxSec {
			dur = maxSec
		}
		subFilter, cleanup, serr := s.prepareSubtitleFilter(r.Subtitles, cues, start, dur, width, height)
		if serr != nil {
			return "", "", serr
		}
		defer cleanup()
		path, err = s.cutGif(ctx, src, start, dur, outBase, subFilter, report)
	default:
		dur := r.End - r.Start + 2*pad
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		subFilter, cleanup, serr := s.prepareSubtitleFilter(r.Subtitles, cues, start, dur, width, height)
		if serr != nil {
			return "", "", serr
		}
		defer cleanup()
		path, err = s.cutAccurate(ctx, src, start, dur, audioOrdinal, outBase, subFilter, report)
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

// RenderLocal renders a clip directly to the renderer's configured cache directory.
func (s *Renderer) RenderLocal(ctx context.Context, src string, audioOrdinal int, r Request, report func(float64)) (string, string, error) {
	return s.RenderLocalWithSubtitles(ctx, src, audioOrdinal, 0, 0, r, nil, report)
}

// RenderLocalWithSubtitles renders a clip directly to the renderer's cache
// directory with optional pre-resolved subtitle cues and source dimensions so
// worker nodes can burn subtitles without accessing the coordinator's store.
func (s *Renderer) RenderLocalWithSubtitles(ctx context.Context, src string, audioOrdinal, width, height int, r Request, cues []SubtitleCue, report func(float64)) (string, string, error) {
	return s.cut(ctx, s.CacheKey(r), src, audioOrdinal, width, height, r, cues, report)
}

// RegisterRendered stores an externally rendered clip (from a worker node)
// into the cache directory under the given cache key and records the cache
// entry, exactly as a local render would have. The extension selects the
// container; only mp4, mkv, and gif are accepted because those are the outputs
// the render paths produce.
func (s *Service) RegisterRendered(key, ext string, r io.Reader) error {
	if ext != "mp4" && ext != "mkv" && ext != "gif" {
		return fmt.Errorf("unsupported clip container %q", ext)
	}
	outDir := filepath.Join(s.renderer.cfg.CacheDir, "clips")
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
