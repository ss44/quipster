// Package clips renders short media clips on demand into a content-addressed
// cache directory, deduplicating concurrent requests for the same clip and
// using vaapi hardware encoding when available.
package clips

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"quipster/internal/probe"
	"quipster/internal/store"
)

// Config holds the clip service tuning parameters; zero fields fall back to
// the defaults applied by New.
type Config struct {
	CacheDir     string
	PreviewRes   string // width:height; one dimension may be -2 to preserve aspect ratio
	AccurateRes  string
	CPUPreset    string // optional x264 override; preview defaults to ultrafast, accurate to veryfast
	MaxClipSec   float64
	DefaultPadMs int
	LongCutSec   float64 // accurate cuts longer than this run as async jobs
}

// Service renders clips into a cache directory and records them as cache
// entries in the store. Concurrent requests for the same clip share a single
// render flight instead of running duplicate ffmpeg work.
type Service struct {
	st       *store.Store
	sf       map[string]*flight
	cfg      Config
	mu       sync.Mutex
	sfMu     sync.Mutex
	hwavail  bool
	hwprobed bool
}

// flight tracks one in-flight render so later requests can await its result.
type flight struct {
	err  error
	done chan struct{}
	path string
	ext  string
}

// New builds a clip service, applying defaults for unset Config fields.
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
	cfg.PreviewRes = normalizeResolution(cfg.PreviewRes, "640:-2")
	cfg.AccurateRes = normalizeResolution(cfg.AccurateRes, "1280:-2")
	if cfg.CPUPreset != "" && !slices.Contains([]string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}, cfg.CPUPreset) {
		log.Printf("[clips] invalid CPU preset %q; using mode defaults", cfg.CPUPreset)
		cfg.CPUPreset = ""
	}
	log.Printf("[clips] settings preview=%s accurate=%s cpu_preset=%q (blank uses mode defaults); fast mode copies original streams", cfg.PreviewRes, cfg.AccurateRes, cfg.CPUPreset)
	return &Service{st: st, cfg: cfg, sf: map[string]*flight{}}
}

// normalizeResolution accepts only even dimensions, not arbitrary filter expressions.
func normalizeResolution(value, fallback string) string {
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		w, werr := strconv.Atoi(parts[0])
		h, herr := strconv.Atoi(parts[1])
		valid := func(n int) bool { return n == -2 || (n >= 2 && n <= 7680 && n%2 == 0) }
		if werr == nil && herr == nil && valid(w) && valid(h) && (w > 0 || h > 0) {
			return fmt.Sprintf("%d:%d", w, h)
		}
	}
	log.Printf("[clips] invalid resolution %q; using %s", value, fallback)
	return fallback
}

// Config returns the effective configuration after defaults were applied.
func (s *Service) Config() Config { return s.cfg }

// CacheKey is the stable identity of a rendered clip.
func CacheKey(mediaID int64, start, end float64, mode string, padMs int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%.3f|%.3f|%s|%d", mediaID, start, end, mode, padMs)))
	return hex.EncodeToString(h[:16])
}

// CacheKey includes the encoding profile so tuning changes cannot reuse stale transcodes.
// Stream-copy identities remain independent of transcode settings.
func (s *Service) CacheKey(r Request) string {
	mode := r.Mode
	switch mode {
	case "preview":
		mode += "|" + s.cfg.PreviewRes + "|" + s.cfg.CPUPreset
	case "accurate":
		mode += "|" + s.cfg.AccurateRes + "|" + s.cfg.CPUPreset
	}
	return CacheKey(r.MediaID, r.Start, r.End, mode, r.PadMs)
}

// Request describes one clip render: the media, the [Start, End] window in
// seconds, symmetric padding in milliseconds and the cut mode
// (fast | preview | accurate).
type Request struct {
	Mode    string // fast | preview | accurate
	MediaID int64
	Start   float64
	End     float64
	PadMs   int
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
	if _, statErr := os.Stat(e.Path); statErr != nil {
		// The rendered file is gone (pruned cache): treat the row as a
		// miss and drop it so later lookups skip the dead path. The delete
		// is best-effort — a failed delete only leaves the stale row for
		// the next lookup to re-detect, so it is logged, not propagated.
		if delErr := s.st.DeleteCacheEntry(key); delErr != nil {
			log.Printf("[clips] drop stale cache entry %s: %v", key, delErr)
		}
		return nil, nil //nolint:nilerr // a missing file is a cache miss, not a lookup failure
	}
	return e, nil
}

// Touch records an access for LRU accounting. Failures only cost cache
// freshness, so they are logged instead of propagated to callers.
func (s *Service) Touch(key string) {
	if err := s.st.TouchCache(key); err != nil {
		log.Printf("[clips] touch cache %s: %v", key, err)
	}
}

// Produce renders the clip if not cached, deduplicating concurrent requests.
func (s *Service) Produce(ctx context.Context, r Request) (string, string, error) {
	key := s.CacheKey(r)
	if e, err := s.Cached(key); err != nil {
		return "", "", err
	} else if e != nil {
		log.Printf("[clips] media=%d mode=%s cache=hit key=%s", r.MediaID, r.Mode, key)
		return e.Path, strings.TrimPrefix(filepath.Ext(e.Path), "."), nil
	}

	f, joined := s.joinFlight(key)
	if joined {
		log.Printf("[clips] media=%d mode=%s waiting for existing render key=%s", r.MediaID, r.Mode, key)
		return awaitFlight(ctx, f)
	}
	defer s.endFlight(key, f)

	started := time.Now()
	log.Printf("[clips] media=%d mode=%s cache=miss start=%.3f end=%.3f key=%s", r.MediaID, r.Mode, r.Start, r.End, key)
	f.path, f.ext, f.err = s.render(ctx, key, r)
	log.Printf("[clips] media=%d mode=%s finished elapsed=%s error=%v", r.MediaID, r.Mode, time.Since(started).Round(time.Millisecond), f.err)
	return f.path, f.ext, f.err
}

// joinFlight returns the flight already rendering key, or registers a fresh
// one; joined reports the former case so the caller can await instead of
// rendering.
func (s *Service) joinFlight(key string) (*flight, bool) {
	s.sfMu.Lock()
	defer s.sfMu.Unlock()
	if f, ok := s.sf[key]; ok {
		return f, true
	}
	f := &flight{done: make(chan struct{})}
	s.sf[key] = f
	return f, false
}

// awaitFlight blocks until the joined flight completes and returns its result
// or the context is done.
func awaitFlight(ctx context.Context, f *flight) (string, string, error) {
	select {
	case <-f.done:
		return f.path, f.ext, f.err
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// endFlight forgets the flight for key and wakes everyone awaiting it.
func (s *Service) endFlight(key string, f *flight) {
	s.sfMu.Lock()
	delete(s.sf, key)
	s.sfMu.Unlock()
	close(f.done)
}

func (s *Service) render(ctx context.Context, key string, r Request) (string, string, error) {
	m, _, err := s.Validate(r)
	if err != nil {
		return "", "", err
	}
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
	if r.Mode == "fast" {
		dur := (r.End + pad) - start
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		path, err = s.cutFast(ctx, m.Path, start, dur, m.AudioStreamIndex, outBase)
	} else {
		dur := r.End - r.Start + 2*pad
		if dur > s.cfg.MaxClipSec {
			dur = s.cfg.MaxClipSec
		}
		if r.Mode == "preview" {
			path, err = s.cutPreview(ctx, m.Path, start, dur, m.AudioStreamIndex, outBase)
		} else {
			path, err = s.cutAccurate(ctx, m.Path, start, dur, m.AudioStreamIndex, outBase)
		}
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
	err := runFFmpeg(ctx, fastCopyArgs(src, start, dur, audioOrdinal, mp4))
	if err == nil {
		removePartial(outBase + ".mkv")
		return mp4, nil
	}
	// Stream copy into mp4 can fail on odd stream layouts; matroska is more
	// tolerant, so retry once before giving up.
	log.Printf("[clips] mp4 remux failed (%v), falling back to matroska", err)
	mkv := outBase + ".mkv"
	if err2 := runFFmpeg(ctx, fastCopyArgs(src, start, dur, audioOrdinal, mkv)); err2 != nil {
		removePartial(mp4)
		return "", fmt.Errorf("fast cut failed: %v / %v", err, err2)
	}
	removePartial(mp4)
	return mkv, nil
}

// cutPreview transcodes a small, browser-playable clip as fast as possible;
// preview quality is intentionally low (640px by default).
func (s *Service) cutPreview(ctx context.Context, src string, start, dur float64, audioOrdinal int, outBase string) (string, error) {
	out := outBase + ".mp4"
	hw := s.HWAvailable()
	err := runFFmpeg(ctx, previewArgs(src, audioOrdinal, start, dur, out, hw, s.cfg))
	if err == nil {
		return out, nil
	}
	if !hw {
		removePartial(out)
		return "", fmt.Errorf("preview cut failed: %w", err)
	}
	log.Printf("[clips] vaapi preview failed (%v), falling back to cpu", err)
	removePartial(out)
	if err2 := runFFmpeg(ctx, previewArgs(src, audioOrdinal, start, dur, out, false, s.cfg)); err2 != nil {
		removePartial(out)
		return "", fmt.Errorf("preview cut failed: %w", err2)
	}
	return out, nil
}

// cutAccurate transcodes a high-quality 720p clip, preferring the vaapi
// pipeline and falling back to libx264 when hardware encoding fails.
func (s *Service) cutAccurate(ctx context.Context, src string, start, dur float64, audioOrdinal int, outBase string) (string, error) {
	out := outBase + ".mp4"
	hw := s.HWAvailable()
	err := runFFmpeg(ctx, accurateArgs(src, audioOrdinal, start, dur, out, hw, false, s.cfg))
	if err == nil {
		return out, nil
	}
	if !hw {
		removePartial(out)
		return "", fmt.Errorf("accurate cut failed: %w", err)
	}
	log.Printf("[clips] vaapi encode failed (%v), falling back to cpu", err)
	removePartial(out)
	if err2 := runFFmpeg(ctx, accurateArgs(src, audioOrdinal, start, dur, out, false, false, s.cfg)); err2 != nil {
		removePartial(out)
		return "", fmt.Errorf("accurate cut failed: %w", err2)
	}
	return out, nil
}

// audioMapArgs maps the media's chosen English audio stream. Optional '?' keeps
// ffmpeg from failing if a stale probe points past the track list.
func audioMapArgs(ordinal int) []string {
	if ordinal < 0 {
		return nil
	}
	return []string{"-map", fmt.Sprintf("0:a:%d?", ordinal)}
}

// fastCopyArgs builds the stream-copy invocation for one output container;
// the mp4 remux adds -movflags +faststart, the matroska fallback omits it.
func fastCopyArgs(src string, start, dur float64, audioOrdinal int, out string) []string {
	tail := []string{"-dn", "-sn", "-map_chapters", "-1", "-c", "copy", "-avoid_negative_ts", "make_zero"}
	if filepath.Ext(out) == ".mp4" {
		tail = slices.Concat(tail, []string{"-movflags", "+faststart"})
	}
	return slices.Concat(
		[]string{"-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src,
			"-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"},
		audioMapArgs(audioOrdinal),
		tail, []string{out},
	)
}

// previewArgs builds the low-latency transcode invocation; hw selects
// the vaapi pipeline over the cpu ultrafast fallback.
func previewArgs(src string, audioOrdinal int, start, dur float64, out string, hw bool, cfg Config) []string {
	var hwinput, video []string
	if hw {
		hwinput = []string{"-vaapi_device", "/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
		video = []string{"-vf", "scale_vaapi=" + cfg.PreviewRes + ":format=nv12", "-c:v", "h264_vaapi", "-qp", "32"}
	} else {
		preset := cfg.CPUPreset
		if preset == "" {
			preset = "ultrafast"
		}
		video = []string{"-vf", "scale=" + cfg.PreviewRes, "-c:v", "libx264", "-preset", preset,
			"-crf", "32", "-tune", "zerolatency"}
	}
	return slices.Concat(
		[]string{"-v", "error", "-y"},
		hwinput,
		[]string{"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"},
		audioMapArgs(audioOrdinal),
		[]string{"-dn", "-sn", "-map_chapters", "-1"},
		video,
		[]string{"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", out},
	)
}

// accurateArgs builds the 720p transcode invocation shared by the synchronous
// accurate cutter and the async progress-reporting job: progress adds the
// pipe:1 progress switches and hw selects the vaapi pipeline.
func accurateArgs(src string, audioOrdinal int, start, dur float64, out string, hw, progress bool, cfg Config) []string {
	var prefix, hwinput, video []string
	if progress {
		prefix = []string{"-progress", "pipe:1", "-nostats"}
	}
	if hw {
		hwinput = []string{"-vaapi_device", "/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
		video = []string{"-vf", "scale_vaapi=" + cfg.AccurateRes + ":format=nv12", "-c:v", "h264_vaapi", "-b:v", "2M"}
	} else {
		preset := cfg.CPUPreset
		if preset == "" {
			preset = "veryfast"
		}
		video = []string{"-vf", "scale=" + cfg.AccurateRes, "-c:v", "libx264", "-preset", preset, "-crf", "23"}
	}
	return slices.Concat(
		[]string{"-v", "error", "-y"},
		prefix,
		hwinput,
		[]string{"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"},
		audioMapArgs(audioOrdinal),
		[]string{"-dn", "-sn", "-map_chapters", "-1"},
		video,
		[]string{"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", out},
	)
}

// removePartial deletes a failed render's output file. A missing file is
// expected (the encode may never have created one); any other failure is
// logged since leftover files only waste cache space.
func removePartial(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[clips] cleanup %s: %v", path, err)
	}
}

func runFFmpeg(ctx context.Context, args []string) error {
	started := time.Now()
	log.Printf("[clips] ffmpeg started args=%q", args)
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), args...) //nolint:gosec // the binary comes from operator config and args are fixed clip switches
	if b, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[clips] ffmpeg failed elapsed=%s", time.Since(started).Round(time.Millisecond))
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
	}
	log.Printf("[clips] ffmpeg completed elapsed=%s", time.Since(started).Round(time.Millisecond))
	return nil
}

// HWAvailable reports whether vaapi acceleration works. The probe runs once
// per process and the result is cached, so cold starts pay one ffmpeg run.
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

// probeVaapi runs a tiny synthetic encode to verify the render device works.
func probeVaapi() bool {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		log.Printf("[clips] VAAPI device unavailable: %v", err)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), "-v", "error", "-init_hw_device", "vaapi=va:/dev/dri/renderD128", //nolint:gosec // fixed diagnostic invocation with no user input
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2", "-vf", "format=nv12,hwupload",
		"-c:v", "h264_vaapi", "-f", "null", "-")
	if b, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[clips] VAAPI encode self-test failed: %v stderr=%q context=%v", err, strings.TrimSpace(string(b)), ctx.Err())
		return false
	}
	return true
}

// RunWithProgress renders a clip while reporting 0..1 progress (for async jobs).
func (s *Service) RunWithProgress(ctx context.Context, r Request, report func(float64)) (string, string, error) {
	if r.Mode != "accurate" {
		return s.Produce(ctx, r)
	}
	key := s.CacheKey(r)
	if e, err := s.Cached(key); err != nil {
		return "", "", err
	} else if e != nil {
		return e.Path, strings.TrimPrefix(filepath.Ext(e.Path), "."), nil
	}
	f, joined := s.joinFlight(key)
	if joined {
		return awaitFlight(ctx, f)
	}
	defer s.endFlight(key, f)

	m, _, err := s.Validate(r)
	if err != nil {
		f.err = err
		return "", "", err
	}
	pad := float64(r.PadMs) / 1000
	outDir := filepath.Join(s.cfg.CacheDir, "clips")
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil {
		f.err = mkErr
		return "", "", mkErr
	}
	start := r.Start - pad
	if start < 0 {
		start = 0
	}
	dur := r.End - r.Start + 2*pad
	if dur > s.cfg.MaxClipSec {
		dur = s.cfg.MaxClipSec
	}
	path := filepath.Join(outDir, key) + ".mp4"

	hw := s.HWAvailable()
	if perr := runFFmpegProgress(ctx, accurateArgs(m.Path, m.AudioStreamIndex, start, dur, path, hw, true, s.cfg), dur, report); perr != nil {
		if !hw {
			removePartial(path)
			f.err = perr
			return "", "", perr
		}
		// vaapi may fail mid-run; retry once on cpu
		log.Printf("[clips] vaapi async encode failed (%v), falling back to cpu", perr)
		removePartial(path)
		if err2 := runFFmpegProgress(ctx, accurateArgs(m.Path, m.AudioStreamIndex, start, dur, path, false, true, s.cfg), dur, report); err2 != nil {
			removePartial(path)
			f.err = err2
			return "", "", err2
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
	started := time.Now()
	log.Printf("[clips] async ffmpeg started args=%q", args)
	defer func() {
		log.Printf("[clips] async ffmpeg exited elapsed=%s", time.Since(started).Round(time.Millisecond))
	}()
	cmd := exec.CommandContext(ctx, probe.FfmpegBin(), args...) //nolint:gosec // the binary comes from operator config and args are fixed clip switches
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
	b, _ := json.Marshal(map[string]any{ //nolint:errcheck // the map holds only JSON scalars, so marshaling cannot fail
		"mediaId": r.MediaID, "start": r.Start, "end": r.End, "padMs": r.PadMs,
		"mode": r.Mode,
	})
	return string(b)
}

// JobHandler renders clips enqueued as async jobs, reporting progress.
func (s *Service) JobHandler() func(ctx context.Context, j *store.Job, progress func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
		var payload struct {
			Mode    string  `json:"mode"`
			MediaID int64   `json:"mediaId"`
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			PadMs   int     `json:"padMs"`
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
