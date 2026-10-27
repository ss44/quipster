package thumbs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"quipster/internal/probe"
	"quipster/internal/store"
)

const bucketSec = 5.0

type Service struct {
	St       *store.Store
	CacheDir string
	Hw       interface{ HWAvailable() bool } // optional vaapi provider
}

var widths = []int{320, 640}

// Bucket rounds t to the shared 5s bucket used to raise cache hits.
func Bucket(t float64) float64 {
	if t < 0 {
		t = 0
	}
	return float64(int(t/bucketSec)) * bucketSec
}

func NearestWidth(w int) int {
	for _, c := range widths {
		if w <= c {
			return c
		}
	}
	return widths[len(widths)-1]
}

func cacheKey(mediaID int64, t float64, width int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("thumb|%d|%.0f|%d", mediaID, t, width)))
	return hex.EncodeToString(h[:16])
}

// Get returns a cached or freshly rendered JPEG path for the thumbnail.
func (s *Service) Get(ctx context.Context, mediaID int64, t float64, width int) (*store.CacheEntry, error) {
	m, err := s.St.GetMedia(mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("media %d not found", mediaID)
	}
	if m.DurationSec > 0 {
		if t > m.DurationSec-0.1 {
			t = m.DurationSec - 0.1
		}
	}
	if t < 0 {
		t = 0
	}
	t = Bucket(t)
	width = NearestWidth(width)
	key := cacheKey(mediaID, t, width)
	if e, err := s.St.GetCacheEntry(key); err != nil {
		return nil, err
	} else if e != nil {
		if _, err := os.Stat(e.Path); err == nil {
			s.St.TouchCache(key)
			return e, nil
		}
		s.St.DeleteCacheEntry(key)
	}

	dir := filepath.Join(s.CacheDir, "thumbs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, key+".jpg")
	if err := s.render(ctx, m.Path, t, width, out); err != nil {
		return nil, err
	}
	st, err := os.Stat(out)
	if err != nil {
		return nil, err
	}
	if err := s.St.UpsertCacheEntry(key, out, "thumb", st.Size()); err != nil {
		return nil, err
	}
	return &store.CacheEntry{CacheKey: key, Path: out, Kind: "thumb", Bytes: st.Size()}, nil
}

func (s *Service) render(ctx context.Context, src string, t float64, width int, out string) error {
	build := func(hw bool) []string {
		args := []string{"-v", "error", "-y"}
		if hw {
			args = append(args, "-hwaccel", "vaapi")
		}
		return append(args, "-ss", fmt.Sprintf("%.3f", t), "-i", src, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2", width), "-q:v", "5", "-f", "image2", out)
	}
	hw := s.Hw != nil && s.Hw.HWAvailable()
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), build(hw)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		if !hw {
			return fmt.Errorf("thumbnail failed: %w: %s", err, strings.TrimSpace(string(b)))
		}
		os.Remove(out)
		cmd2 := exec.CommandContext(ctx, probe.FfmpegBin(), build(false)...)
		if b2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("thumbnail failed: %v / %v: %s", err, err2, strings.TrimSpace(string(b2)))
		}
		return nil
	}
	return nil
}
