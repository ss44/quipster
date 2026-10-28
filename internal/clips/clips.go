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

	"quipster/internal/hardware"
	"quipster/internal/probe"
	"quipster/internal/store"
)

type Config struct {
	CacheDir     string
	MaxClipSec   float64
	DefaultPadMs int
	LongCutSec   float64 // accurate cuts longer than this run as async jobs
	Hw           hardware.Capabilities // detected hw backend; zero value = cpu only
}

type Service struct {
	st  *store.Store
	cfg Config

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
	return s.renderCached(ctx, r, nil)
}

// RunWithProgress renders a clip while reporting 0..1 progress (for async jobs).
func (s *Service) RunWithProgress(ctx context.Context, r Request, report func(float64)) (string, string, error) {
	return s.renderCached(ctx, r, report)
}

// renderCached returns the cached clip or renders it once under a singleflight.
func (s *Service) renderCached(ctx context.Context, r Request, report func(float64)) (string, string, error) {
	key := CacheKey(r.MediaID, r.Start, r.End, r.Mode, r.PadMs)
	if e, err := s.Cached(key); err != nil {
		return "", "", err
	} else if e != nil {
		return e.Path, strings.TrimPrefix(filepath.Ext(e.Path), "."), nil
	}
	return s.doFlight(ctx, key, func() (string, string, error) {
		return s.render(ctx, key, r, report)
	})
}

// doFlight ensures only one render runs per cache key; concurrent callers
// wait for the in-flight render and share its result.
func (s *Service) doFlight(ctx context.Context, key string, render func() (string, string, error)) (string, string, error) {
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

	f.path, f.ext, f.err = render()
	return f.path, f.ext, f.err
}

func (s *Service) render(ctx context.Context, key string, r Request, report func(float64)) (string, string, error) {
	m, _, err := s.Validate(r)
	if err != nil {
		return "", "", err
	}
	pad := float64(r.PadMs) / 1000
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}
	start := r.Start - pad
	if start < 0 {
		start = 0
	}
	dur := (r.End + pad) - start
	if dur > s.cfg.MaxClipSec {
		dur = s.cfg.MaxClipSec
	}

	var path string
	switch r.Mode {
	case "fast":
		path, err = s.cutFast(ctx, m.Path, start, dur, m.AudioStreamIndex, filepath.Join(outDir, key))
	case "preview":
		path, err = s.cutPreview(ctx, m.Path, start, dur, m.AudioStreamIndex, filepath.Join(outDir, key)+".mp4")
	default: // accurate
		path, err = s.cutAccurate(ctx, m.Path, start, dur, m.AudioStreamIndex, filepath.Join(outDir, key)+".mp4", report)
	}
	if err != nil {
		return "", "", err
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
func (s *Service) cutFast(ctx context.Context, src string, start, dur float64, audioOrdinal int, outBase string) (string, error) {
	mp4 := outBase + ".mp4"
	args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
		"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"}
	args = append(args, audioMapArgs(audioOrdinal)...)
	args = append(args, "-dn", "-sn", "-map_chapters", "-1", "-c", "copy", "-avoid_negative_ts", "make_zero",
		"-movflags", "+faststart", mp4)
	if err := s.runFFmpeg(ctx, args, nil, 0); err != nil {
		log.Printf("[clips] mp4 remux failed (%v), falling back to matroska", err)
		mkv := outBase + ".mkv"
		args := []string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
			"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"}
		args = append(args, audioMapArgs(audioOrdinal)...)
		args = append(args, "-dn", "-sn", "-map_chapters", "-1", "-c", "copy", "-avoid_negative_ts", "make_zero", mkv)
		if err2 := s.runFFmpeg(ctx, args, nil, 0); err2 != nil {
			os.Remove(mp4)
			return "", fmt.Errorf("fast cut failed: %v / %v", err, err2)
		}
		os.Remove(mp4)
		return mkv, nil
	}
	os.Remove(outBase + ".mkv")
	return mp4, nil
}

// encProfile holds the per-mode encoder parameters for transcoded cuts.
type encProfile struct {
	width    int
	hwEnc    []string // extra encoder args for the hardware encoder
	cpuEnc   []string // x264 args for the CPU encoder
	abitrate string
}

var (
	previewProfile = encProfile{
		width:  960,
		hwEnc:  []string{"-b:v", "1M"},
		cpuEnc: []string{"-preset", "ultrafast", "-crf", "32", "-tune", "zerolatency"},
		abitrate: "96k",
	}
	accurateProfile = encProfile{
		width:  1280,
		hwEnc:  []string{"-b:v", "2M"},
		cpuEnc: []string{"-preset", "veryfast", "-crf", "23"},
		abitrate: "128k",
	}
)

// cutPreview transcodes a small, browser-playable clip as fast as possible;
// preview quality is intentionally low (ultrafast x264 @ 960px).
func (s *Service) cutPreview(ctx context.Context, src string, start, dur float64, audioOrdinal int, out string) (string, error) {
	if err := s.runTranscode(ctx, src, out, start, dur, audioOrdinal, previewProfile, nil); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("preview cut failed: %w", err)
	}
	return out, nil
}

// cutAccurate transcodes a 720p clip from the exact timestamp.
func (s *Service) cutAccurate(ctx context.Context, src string, start, dur float64, audioOrdinal int, out string, report func(float64)) (string, error) {
	if err := s.runTranscode(ctx, src, out, start, dur, audioOrdinal, accurateProfile, report); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("accurate cut failed: %w", err)
	}
	return out, nil
}

// runTranscode renders one transcoded clip, trying the hardware pipeline
// first and falling back to CPU libx264 when the hardware attempt fails
// (hardware encoders can fail mid-run on driver hiccups or unsupported input).
func (s *Service) runTranscode(ctx context.Context, src, out string, start, dur float64,
	audioOrdinal int, p encProfile, report func(float64)) error {

	hwPlan := s.cfg.Hw.Pipeline(p.width)
	cpuPlan := hardware.Capabilities{}.Pipeline(p.width)
	build := func(plan hardware.Pipeline) []string {
		args := []string{"-v", "error", "-y"}
		if report != nil {
			args = append(args, "-progress", "pipe:1", "-nostats")
		}
		args = append(args, plan.Input...)
		args = append(args, "-ss", fmt.Sprintf("%.3f", start), "-i", src,
			"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0")
		args = append(args, audioMapArgs(audioOrdinal)...)
		args = append(args, "-dn", "-sn", "-map_chapters", "-1",
			"-vf", plan.Filter, "-c:v", plan.Encoder)
		if plan.IsHW {
			args = append(args, p.hwEnc...)
		} else {
			args = append(args, p.cpuEnc...)
		}
		args = append(args, "-c:a", "aac", "-b:a", p.abitrate, "-movflags", "+faststart", out)
		return args
	}

	if hwPlan.IsHW {
		if err := s.runFFmpeg(ctx, build(hwPlan), report, dur); err == nil {
			return nil
		} else {
			log.Printf("[clips] %s encode failed (%v), falling back to cpu", s.cfg.Hw.Backend, err)
			os.Remove(out)
		}
	}
	return s.runFFmpeg(ctx, build(cpuPlan), report, dur)
}

// audioMapArgs maps the media's chosen English audio stream. Optional '?' keeps
// ffmpeg from failing if a stale probe points past the track list.
func audioMapArgs(ordinal int) []string {
	if ordinal < 0 {
		return nil
	}
	return []string{"-map", fmt.Sprintf("0:a:%d?", ordinal)}
}

// runFFmpeg runs one ffmpeg command, reporting 0..1 progress when report is set.
func (s *Service) runFFmpeg(ctx context.Context, args []string, report func(float64), dur float64) error {
	if report == nil {
		cmd := exec.CommandContext(ctx, probe.FfmpegBin(), args...)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
		}
		return nil
	}
	return runFFmpegProgress(ctx, probe.FfmpegBin(), args, dur, report)
}

// AsyncJobPayload is the job payload used for long accurate cuts.
func AsyncJobPayload(r Request) string {
	b, _ := json.Marshal(map[string]any{"mediaId": r.MediaID, "start": r.Start, "end": r.End, "padMs": r.PadMs,
		"mode": r.Mode})
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

func runFFmpegProgress(ctx context.Context, bin string, args []string, dur float64, report func(float64)) error {
	cmd := exec.CommandContext(ctx, bin, args...)
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
				if v, ok := strings.CutPrefix(line, "out_time_us="); ok {
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
				break
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
