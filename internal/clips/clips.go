package clips

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"quipster/internal/probe"
	"quipster/internal/store"
)

type Config struct {
	CacheDir     string
	MaxClipSec   float64
	DefaultPadMs int
	LongCutSec   float64 // accurate cuts longer than this run as async jobs
}

type Service struct {
	st  *store.Store
	cfg Config

	mu       sync.Mutex
	hwavail  bool
	hwprobed bool

	sfMu sync.Mutex
	sf   map[string]*flight
}

type flight struct {
	done chan struct{}
	path string
	ext  string
	err  error
}

func New(st *store.Store, cfg Config) *Service {
	if cfg.MaxClipSec <= 0 {
		cfg.MaxClipSec = 120
	}
	if cfg.DefaultPadMs <= 0 {
		cfg.DefaultPadMs = 250
	}
	if cfg.LongCutSec <= 0 {
		cfg.LongCutSec = 20
	}
	return &Service{st: st, cfg: cfg, sf: map[string]*flight{}}
}

func (s *Service) Config() Config { return s.cfg }

// CacheKey is the stable identity of a rendered clip.
func CacheKey(mediaID int64, start, end float64, mode string, padMs int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%.3f|%.3f|%s|%d", mediaID, start, end, mode, padMs)))
	return hex.EncodeToString(h[:16])
}

type Request struct {
	MediaID int64
	Start   float64
	End     float64
	PadMs   int
	Mode    string // fast | accurate
}

// Validate checks clip parameters against SQLite and caps.
func (s *Service) Validate(r Request) (*store.MediaFile, float64, error) {
	switch r.Mode {
	case "fast", "accurate", "preview":
	default:
		return nil, 0, fmt.Errorf("mode must be fast, preview or accurate")
	}
	if r.End <= r.Start {
		return nil, 0, fmt.Errorf("start must be before end")
	}
	pad := float64(r.PadMs) / 1000
	span := (r.End - r.Start) + 2*pad
	if span > s.cfg.MaxClipSec+0.001 {
		return nil, 0, fmt.Errorf("clip span %.1fs exceeds max %.0fs", span, s.cfg.MaxClipSec)
	}
	m, err := s.st.GetMedia(r.MediaID)
	if err != nil {
		return nil, 0, err
	}
	if m == nil {
		return nil, 0, fmt.Errorf("media %d not found", r.MediaID)
	}
	if m.DurationSec > 0 {
		if r.Start < 0 {
			return nil, 0, fmt.Errorf("start out of range")
		}
		if r.End > m.DurationSec+0.5 {
			return nil, 0, fmt.Errorf("end out of range")
		}
	}
	return m, span, nil
}

// Cached returns the cache entry if the rendered file exists.
func (s *Service) Cached(key string) (*store.CacheEntry, error) {
	e, err := s.st.GetCacheEntry(key)
	if err != nil || e == nil {
		return e, err
	}
	if _, err := os.Stat(e.Path); err != nil {
		s.st.DeleteCacheEntry(key)
		return nil, nil
	}
	return e, nil
}

// Touch records an access for LRU accounting.
func (s *Service) Touch(key string) { s.st.TouchCache(key) }

// Produce renders the clip if not cached, deduplicating concurrent requests.
func (s *Service) Produce(ctx context.Context, r Request) (string, string, error) {
	key := CacheKey(r.MediaID, r.Start, r.End, r.Mode, r.PadMs)
	if e, err := s.Cached(key); err != nil {
		return "", "", err
	} else if e != nil {
		return e.Path, strings.TrimPrefix(filepath.Ext(e.Path), "."), nil
	}

	s.sfMu.Lock()
	if f, ok := s.sf[key]; ok {
		s.sfMu.Unlock()
		select {
		case <-f.done:
			return f.path, f.ext, f.err
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.sf[key] = f
	s.sfMu.Unlock()

	defer func() {
		s.sfMu.Lock()
		delete(s.sf, key)
		s.sfMu.Unlock()
		close(f.done)
	}()

	f.path, f.ext, f.err = s.render(ctx, key, r)
	return f.path, f.ext, f.err
}

func (s *Service) render(ctx context.Context, key string, r Request) (string, string, error) {
	m, _, err := s.Validate(r)
	if err != nil {
		return "", "", err
	}
	pad := float64(r.PadMs) / 1000
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}

	var path string
	if r.Mode == "fast" {
		start := r.Start - pad
		if start < 0 {
			start = 0
		}
		dur := (r.End + pad) - start
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		var err error
		path, err = s.cutFast(ctx, m.Path, start, dur, filepath.Join(outDir, key))
		if err != nil {
			return "", "", err
		}
	} else {
		dur := r.End - r.Start + 2*pad
		start := r.Start - pad
		if start < 0 {
			start = 0
		}
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		var err error
		if r.Mode == "preview" {
			path, err = s.cutPreview(ctx, m.Path, start, dur, filepath.Join(outDir, key))
		} else {
			path, err = s.cutAccurate(ctx, m.Path, start, dur, filepath.Join(outDir, key))
		}
		if err != nil {
			return "", "", err
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if err := s.st.UpsertCacheEntry(key, path, "clip", st.Size()); err != nil {
		return "", "", err
	}
	return path, strings.TrimPrefix(filepath.Ext(path), "."), nil
}

// cutFast stream-copies the clip region. Input-side seek lets ffmpeg start at
// the keyframe at or before `start` in a single pass — no separate probe.
func (s *Service) cutFast(ctx context.Context, src string, start, dur float64, outBase string) (string, error) {
	mp4 := outBase + ".mp4"
	args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
		"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0", "-map", "0:a:0?",
		"-dn", "-sn", "-c", "copy", "-avoid_negative_ts", "make_zero",
		"-movflags", "+faststart", mp4}
	if err := runFFmpeg(ctx, args); err != nil {
		log.Printf("[clips] mp4 remux failed (%v), falling back to matroska", err)
		mkv := outBase + ".mkv"
		args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
			"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0", "-map", "0:a:0?",
			"-dn", "-sn", "-c", "copy", "-avoid_negative_ts", "make_zero", mkv}
		if err2 := runFFmpeg(ctx, args); err2 != nil {
			os.Remove(mp4)
			return "", fmt.Errorf("fast cut failed: %v / %v", err, err2)
		}
		os.Remove(mp4)
		return mkv, nil
	}
	os.Remove(outBase + ".mkv")
	return mp4, nil
}

// cutPreview transcodes a small, browser-playable clip as fast as possible;
// preview quality is intentionally low (ultrafast x264 @ 960px).
func (s *Service) cutPreview(ctx context.Context, src string, start, dur float64, outBase string) (string, error) {
	out := outBase + ".mp4"
	if s.HWAvailable() {
		args := []string{"-v", "error", "-y", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi",
			"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", dur),
			"-map", "0:v:0", "-map", "0:a:0?", "-dn", "-sn",
			"-vf", "scale_vaapi=w=960:h=-2:format=nv12", "-c:v", "h264_vaapi", "-qp", "32",
			"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", out}
		if err := runFFmpeg(ctx, args); err == nil {
			return out, nil
		}
		log.Printf("[clips] vaapi preview failed, falling back to cpu ultrafast")
		os.Remove(out)
	}
	args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
		"-t", fmt.Sprintf("%.3f", dur),
		"-map", "0:v:0", "-map", "0:a:0?", "-dn", "-sn", "-vf", "scale=960:-2",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "32", "-tune", "zerolatency",
		"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", out}
	if err := runFFmpeg(ctx, args); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("preview cut failed: %w", err)
	}
	return out, nil
}

func (s *Service) cutAccurate(ctx context.Context, src string, start, dur float64, outBase string) (string, error) {
	out := outBase + ".mp4"
	if s.HWAvailable() {
		args := []string{"-v", "error", "-y", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi",
			"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", dur),
			"-map", "0:v:0", "-map", "0:a:0?", "-dn", "-sn",
			"-vf", "scale_vaapi=w=1280:h=720:format=nv12", "-c:v", "h264_vaapi", "-b:v", "2M",
			"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", out}
		if err := runFFmpeg(ctx, args); err == nil {
			return out, nil
		} else {
			log.Printf("[clips] vaapi encode failed (%v), falling back to cpu", err)
			os.Remove(out)
		}
	}
	args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
		"-t", fmt.Sprintf("%.3f", dur),
		"-map", "0:v:0", "-map", "0:a:0?", "-dn", "-sn", "-vf", "scale=1280:-2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", out}
	if err := runFFmpeg(ctx, args); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("accurate cut failed: %w", err)
	}
	return out, nil
}

func runFFmpeg(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *Service) HWAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hwprobed {
		return s.hwavail
	}
	s.hwprobed = true
	s.hwavail = probeVaapi()
	if s.hwavail {
		log.Printf("[clips] vaapi acceleration available")
	} else {
		log.Printf("[clips] vaapi unavailable, using cpu encode fallback")
	}
	return s.hwavail
}

func probeVaapi() bool {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		return false
	}
	cmd := exec.Command(probe.FfmpegBin(), "-v", "error", "-init_hw_device", "vaapi=va:/dev/dri/renderD128",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2", "-vf", "format=nv12,hwupload",
		"-c:v", "h264_vaapi", "-f", "null", "-")
	return cmd.Run() == nil
}

// RunWithProgress renders a clip while reporting 0..1 progress (for async jobs).
func (s *Service) RunWithProgress(ctx context.Context, r Request, report func(float64)) (string, string, error) {
	if r.Mode != "accurate" {
		return s.Produce(ctx, r)
	}
	key := CacheKey(r.MediaID, r.Start, r.End, r.Mode, r.PadMs)
	if e, err := s.Cached(key); err != nil {
		return "", "", err
	} else if e != nil {
		return e.Path, strings.TrimPrefix(filepath.Ext(e.Path), "."), nil
	}
	s.sfMu.Lock()
	if f, ok := s.sf[key]; ok {
		s.sfMu.Unlock()
		select {
		case <-f.done:
			return f.path, f.ext, f.err
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.sf[key] = f
	s.sfMu.Unlock()
	defer func() {
		s.sfMu.Lock()
		delete(s.sf, key)
		s.sfMu.Unlock()
		close(f.done)
	}()

	m, _, err := s.Validate(r)
	if err != nil {
		f.err = err
		return "", "", err
	}
	pad := float64(r.PadMs) / 1000
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	os.MkdirAll(outDir, 0o755)
	start := r.Start - pad
	if start < 0 {
		start = 0
	}
	dur := r.End - r.Start + 2*pad
	if dur > s.cfg.MaxClipSec {
		dur = s.cfg.MaxClipSec
	}
	outBase := filepath.Join(outDir, key)
	path := outBase + ".mp4"

	var args []string
	if s.HWAvailable() {
		args = []string{"-progress", "pipe:1", "-nostats", "-v", "error", "-y", "-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi", "-ss", fmt.Sprintf("%.3f", start), "-i", m.Path,
			"-t", fmt.Sprintf("%.3f", dur), "-vf", "scale_vaapi=w=1280:h=720:format=nv12",
			"-c:v", "h264_vaapi", "-b:v", "2M", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", path}
	} else {
		args = []string{"-progress", "pipe:1", "-nostats", "-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start),
			"-i", m.Path, "-t", fmt.Sprintf("%.3f", dur), "-vf", "scale=1280:-2",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-c:a", "aac", "-b:a", "128k",
			"-movflags", "+faststart", path}
	}
	if err := runFFmpegProgress(ctx, args, dur, report); err != nil {
		// vaapi may fail mid-run; retry once on cpu
		if s.HWAvailable() {
			os.Remove(path)
			args = args[:0]
			args = append(args, "-progress", "pipe:1", "-nostats", "-v", "error", "-y", "-ss",
				fmt.Sprintf("%.3f", start), "-i", m.Path, "-t", fmt.Sprintf("%.3f", dur),
				"-vf", "scale=1280:-2", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
				"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", path)
			if err2 := runFFmpegProgress(ctx, args, dur, report); err2 != nil {
				os.Remove(path)
				f.err = err2
				return "", "", err2
			}
		} else {
			os.Remove(path)
			f.err = err
			return "", "", err
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		f.err = err
		return "", "", err
	}
	if err := s.st.UpsertCacheEntry(key, path, "clip", st.Size()); err != nil {
		f.err = err
		return "", "", err
	}
	f.path = path
	f.ext = "mp4"
	return path, "mp4", nil
}

func runFFmpegProgress(ctx context.Context, args []string, dur float64, report func(float64)) error {
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, rerr := stdout.Read(buf)
			acc = append(acc, buf[:n]...)
			for {
				idx := bytes.IndexByte(acc, '\n')
				if idx < 0 {
					break
				}
				line := string(acc[:idx])
				acc = acc[idx+1:]
				if strings.HasPrefix(line, "out_time_us=") {
					v := strings.TrimPrefix(line, "out_time_us=")
					var us float64
					if _, err := fmt.Sscanf(v, "%f", &us); err == nil && dur > 0 {
						p := us / (dur * 1e6)
						if p < 0 {
							p = 0
						}
						if p > 0.99 {
							p = 0.99
						}
						report(p)
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	<-readerDone
	if werr != nil {
		return fmt.Errorf("%w: %s", werr, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// AsyncJobPayload is the job payload used for long accurate cuts.
func AsyncJobPayload(r Request) string {
	b, _ := json.Marshal(map[string]any{
		"mediaId": r.MediaID, "start": r.Start, "end": r.End, "padMs": r.PadMs,
		"mode": r.Mode,
	})
	return string(b)
}

// JobHandler renders clips enqueued as async jobs, reporting progress.
func (s *Service) JobHandler() func(ctx context.Context, j *store.Job, progress func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
		var payload struct {
			MediaID int64   `json:"mediaId"`
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			PadMs   int     `json:"padMs"`
			Mode    string  `json:"mode"`
		}
		if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		r := Request{MediaID: payload.MediaID, Start: payload.Start, End: payload.End, PadMs: payload.PadMs, Mode: payload.Mode}
		if r.Mode == "" {
			r.Mode = "accurate"
		}
		if r.PadMs <= 0 {
			r.PadMs = s.cfg.DefaultPadMs
		}
		_, _, err := s.RunWithProgress(ctx, r, progress)
		return err
	}
}
