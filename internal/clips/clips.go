// Package clips renders short media clips on demand into a content-addressed
// cache directory, deduplicating concurrent requests for the same clip and
// using hardware encoding (NVENC/QSV/VAAPI/VideoToolbox) when available.
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

	"quipster/internal/hardware"
	"quipster/internal/probe"
	"quipster/internal/store"
)

// Config holds the clip service tuning parameters; zero fields fall back to
// the defaults applied by New.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Config struct {
	CacheDir     string
	PreviewRes   string // width:height; one dimension may be -2 to preserve aspect ratio
	AccurateRes  string
	CPUPreset    string // optional x264 override; preview defaults to ultrafast, accurate to veryfast
	MaxClipSec   float64
	DefaultPadMs int
	LongCutSec   float64           // accurate cuts longer than this run as async jobs
	Hw           *hardware.Monitor // detected hw backend; nil = cpu only
}

// Service renders clips into a cache directory and records them as cache
// entries in the store. Concurrent requests for the same clip share a single
// render flight instead of running duplicate ffmpeg work.
type Service struct {
	st   *store.Store
	sf   map[string]*flight
	cfg  Config
	sfMu sync.Mutex
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

// ErrMediaArchived is returned when a clip is requested for an archived media
// file. Callers map it to HTTP 404 rather than treating it as a bad request.
var ErrMediaArchived = errors.New("media is archived")

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
	// Archived media are not renderable: the video bytes are gone from disk,
	// and the row is hidden from serving until it is restored. Check the
	// sentinel with errors.Is, not string matching.
	if m.Archived {
		return nil, 0, fmt.Errorf("media %d %w", r.MediaID, ErrMediaArchived)
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
	path, ext, err := s.cut(ctx, key, m.Path, m.AudioStreamIndex, r, nil)
	if err != nil {
		return "", "", err
	}
	if err := s.registerCacheEntry(key, path); err != nil {
		return "", "", err
	}
	return path, ext, nil
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
	if err := s.runTranscode(ctx, src, out, start, dur, audioOrdinal, s.previewProfile(), nil); err != nil {
		removePartial(out)
		return "", fmt.Errorf("preview cut failed: %w", err)
	}
	return out, nil
}

// cutAccurate transcodes a high-quality 720p clip, preferring the hardware
// pipeline and falling back to libx264 when hardware encoding fails. report
// forwards ffmpeg progress when non-nil.
func (s *Service) cutAccurate(ctx context.Context, src string, start, dur float64, audioOrdinal int, outBase string, report func(float64)) (string, error) {
	out := outBase + ".mp4"
	if err := s.runTranscode(ctx, src, out, start, dur, audioOrdinal, s.accurateProfile(), report); err != nil {
		removePartial(out)
		return "", fmt.Errorf("accurate cut failed: %w", err)
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

// encProfile holds the per-mode encoder parameters for transcoded cuts: the
// target resolution, the extra encoder args for hardware and CPU pipelines,
// and the audio bitrate.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type encProfile struct {
	res      string   // ffmpeg resolution string for the scale filter
	hwEnc    []string // extra args for the hardware encoder
	cpuEnc   []string // extra x264 args for the CPU encoder
	abitrate string
}

// previewProfile returns the low-quality, low-latency encoder profile.
func (s *Service) previewProfile() encProfile {
	return encProfile{
		res:      s.cfg.PreviewRes,
		hwEnc:    []string{"-b:v", "1M"},
		cpuEnc:   s.cpuEncArgs("ultrafast", "32", "-tune", "zerolatency"),
		abitrate: "96k",
	}
}

// accurateProfile returns the 720p high-quality encoder profile.
func (s *Service) accurateProfile() encProfile {
	return encProfile{
		res:      s.cfg.AccurateRes,
		hwEnc:    []string{"-b:v", "2M"},
		cpuEnc:   s.cpuEncArgs("veryfast", "23"),
		abitrate: "128k",
	}
}

// cpuEncArgs builds the x264 encoder args for a mode, honoring the configured
// CPU preset override over the mode default.
func (s *Service) cpuEncArgs(defPreset, crf string, extra ...string) []string {
	preset := s.cfg.CPUPreset
	if preset == "" {
		preset = defPreset
	}
	return append([]string{"-preset", preset, "-crf", crf}, extra...)
}

// transcodeArgs builds the transcode invocation from a pipeline plan and a
// per-mode encoder profile: plan.Input supplies the input-side decode flags,
// plan.Filter the scale filter, and plan.Encoder the video codec; progress
// adds the pipe:1 progress switches for the async reporting path.
func transcodeArgs(plan hardware.Pipeline, prof encProfile, src string, audioOrdinal int,
	start, dur float64, out string, progress bool) []string {
	var prefix []string
	if progress {
		prefix = []string{"-progress", "pipe:1", "-nostats"}
	}
	enc := prof.cpuEnc
	if plan.IsHW {
		enc = prof.hwEnc
	}
	return slices.Concat(
		[]string{"-v", "error", "-y"},
		prefix,
		plan.Input,
		[]string{"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", dur), "-map", "0:v:0"},
		audioMapArgs(audioOrdinal),
		[]string{"-dn", "-sn", "-map_chapters", "-1"},
		[]string{"-vf", plan.Filter, "-c:v", plan.Encoder},
		enc,
		[]string{"-c:a", "aac", "-b:a", prof.abitrate, "-movflags", "+faststart", out},
	)
}

// runTranscode renders one transcoded clip, trying the hardware pipeline
// first and falling back to CPU libx264 when the hardware attempt fails
// (hardware encoders can fail mid-run on driver hiccups or unsupported input).
// When report is non-nil, ffmpeg progress is parsed and forwarded (0..1).
func (s *Service) runTranscode(ctx context.Context, src, out string, start, dur float64,
	audioOrdinal int, prof encProfile, report func(float64)) error {

	hwCaps := s.cfg.Hw.Capabilities()
	hwPlan := hwCaps.Pipeline(prof.res)
	cpuPlan := hardware.Capabilities{}.Pipeline(prof.res)
	progress := report != nil
	run := func(args []string) error { return runFFmpeg(ctx, args) }
	if progress {
		run = func(args []string) error { return runFFmpegProgress(ctx, args, dur, report) }
	}
	if hwPlan.IsHW {
		if err := run(transcodeArgs(hwPlan, prof, src, audioOrdinal, start, dur, out, progress)); err != nil {
			log.Printf("[clips] %s encode failed (%v), falling back to cpu", hwCaps.Backend, err)
			s.cfg.Hw.RecordFallback(err.Error(), time.Now().UnixMilli())
			removePartial(out)
		} else {
			s.cfg.Hw.RecordHW()
			return nil
		}
	}
	s.cfg.Hw.RecordCPU()
	return run(transcodeArgs(cpuPlan, prof, src, audioOrdinal, start, dur, out, progress))
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
	path, ext, rerr := s.cut(ctx, key, m.Path, m.AudioStreamIndex, r, report)
	if rerr != nil {
		f.err = rerr
		return "", "", rerr
	}
	if err := s.registerCacheEntry(key, path); err != nil {
		f.err = err
		return "", "", err
	}
	f.path, f.ext = path, ext
	return path, ext, nil
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
		// Media archived between request and render: finish quietly instead of
		// burning retries on a render that must not happen. If the media is
		// restored later, the client simply re-requests the clip. Unknown
		// media still falls through to the normal validation error.
		if m, err := s.st.GetMedia(payload.MediaID); err != nil {
			return err
		} else if m != nil && m.Archived {
			return nil
		}
		_, _, err := s.RunWithProgress(ctx, r, progress)
		return err
	}
}
