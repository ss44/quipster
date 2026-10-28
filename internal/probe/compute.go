package probe

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quipster/internal/store"
	"quipster/internal/subs"
)

// Outcome kinds returned by Compute. They classify how the store must be
// updated once the media-bound work finishes.
const (
	// OutcomeOK means subtitles were found and parsed; the caller stores
	// metadata, cues, chunks and enqueues indexing.
	OutcomeOK = "ok"
	// OutcomeNoSubs means no usable English subtitles exist for the file.
	OutcomeNoSubs = "no_subs"
	// OutcomeMediaError means the media file itself is unreadable or
	// unprobeable; the media row is marked errored and the job succeeds.
	OutcomeMediaError = "media_error"
)

// MediaMeta carries the ffprobe facts that the store persists for a media
// file. It is the serializable subset of Result: worker nodes send it to the
// coordinating server instead of the full internal probe structure.
type MediaMeta struct {
	VideoCodec string  `json:"videoCodec"`
	AudioCodec string  `json:"audioCodec"`
	Container  string  `json:"container"`
	Duration   float64 `json:"duration"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	StartTime  float64 `json:"startTime"`
}

// TrackInfo identifies the subtitle track chosen by Compute without any
// store-specific ids or absolute paths, so it can travel between a worker
// node and the coordinating server unchanged.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type TrackInfo struct {
	// Source is "sidecar" or "embedded".
	Source string `json:"source"`
	Lang   string `json:"lang"`
	// StreamIndex is the embedded subtitle stream index (0 for sidecar).
	StreamIndex int `json:"streamIndex"`
	// SidecarName is the base name of the chosen sidecar file when
	// Source is "sidecar"; the server rebuilds its own path beside the
	// media file because mount points differ per node.
	SidecarName string `json:"sidecarName,omitempty"`
}

// Outcome is the media-bound result of Compute: everything needed to persist
// a probe without touching the media file again. SubFile holds the node-local
// path of an extracted subtitle file so callers can register or upload it;
// it is deliberately not serialized.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Outcome struct {
	Kind         string     `json:"kind"`
	Note         string     `json:"note,omitempty"`
	Meta         *MediaMeta `json:"meta,omitempty"`
	Flags        []string   `json:"flags,omitempty"`
	AudioOrdinal int        `json:"audioOrdinal"`
	AudioLang    string     `json:"audioLang"`
	Track        *TrackInfo `json:"track,omitempty"`
	Cues         []subs.Cue `json:"cues,omitempty"`
	SubFile      string     `json:"-"`
}

// Pipeline is the probe worker: ffprobe → subtitle pick/extract/parse →
// cue store → chunk build → enqueue index job. Compute carries the
// media-bound stages and Apply the store writes, so the same pipeline logic
// serves local execution and remote worker-node results.
type Pipeline struct {
	St           *store.Store
	extractSem   chan struct{}
	SubsCacheDir string
	// ExtractConns bounds parallel embedded-subtitle extractions; each one
	// scans the entire media file, so unbounded parallelism thrashes storage.
	ExtractConns int
	// ProbeTimeout bounds each ffprobe run; <=0 uses defaultProbeTimeout. A
	// timed-out probe is killed and the file skipped instead of stalling the
	// queue (issue #1).
	ProbeTimeout time.Duration
	// ExtractTimeout bounds one embedded-subtitle extraction; <=0 uses
	// defaultExtractTimeout. This keeps a pathological file from pinning a
	// worker for the full JOB_TIMEOUT and starving the rest of the queue.
	ExtractTimeout time.Duration
	once           sync.Once
}

func (p *Pipeline) probeTimeout() time.Duration {
	if p.ProbeTimeout > 0 {
		return p.ProbeTimeout
	}
	return defaultProbeTimeout
}

func (p *Pipeline) extractTimeout() time.Duration {
	if p.ExtractTimeout > 0 {
		return p.ExtractTimeout
	}
	return defaultExtractTimeout
}

func (p *Pipeline) sem() chan struct{} {
	p.once.Do(func() {
		n := p.ExtractConns
		if n <= 0 {
			n = 2
		}
		p.extractSem = make(chan struct{}, n)
	})
	return p.extractSem
}

// ComputeOptions carries the per-run inputs of Compute that are not owned by
// the Pipeline configuration.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type ComputeOptions struct {
	// MediaID and MediaPath identify the media to probe. MediaPath must
	// already be resolved for the local filesystem (nodes remap it first).
	MediaID   int64
	MediaPath string
	// KnownStatus is the persisted media status; a non-pending status lets
	// Compute reuse a previously extracted subtitle file instead of
	// rescanning the container.
	KnownStatus string
	// Logger receives the stage-by-stage pipeline log lines.
	Logger *log.Logger
	// Progress reports 0..1 for long-running stages; may be nil.
	Progress func(float64)
	// Stage, when non-nil, is updated with the current pipeline stage so
	// the caller's deferred error logging names the right stage.
	Stage *string
}

func (o ComputeOptions) progress(v float64) {
	if o.Progress != nil {
		o.Progress(v)
	}
}

func (o ComputeOptions) stagef(stage string) {
	if o.Stage != nil {
		*o.Stage = stage
	}
}

// Compute runs the media-bound stages of a probe: file stat, ffprobe
// metadata inspection, subtitle selection/extraction/parsing and cue
// normalization. It performs no store writes and no media-path resolution,
// which keeps it reusable from worker nodes whose only difference is where
// the media and cache directories live.
//
// A nil Outcome with a non-nil error means the job itself was interrupted
// (queue pause, stop or shutdown) and must be requeued rather than recorded
// as a media-level failure.
func (p *Pipeline) Compute(ctx context.Context, opts ComputeOptions) (*Outcome, error) {
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	out := &Outcome{Kind: OutcomeOK}

	opts.stagef("stat")
	fi, serr := os.Stat(opts.MediaPath)
	if serr != nil {
		// The job itself is handled: record the media as errored and let
		// the queue move on instead of spinning on retries.
		out.Kind, out.Note = OutcomeMediaError, "media file missing on disk"
		logger.Printf("stage=stat failed path=%q error=%q", opts.MediaPath, serr)
		return out, nil
	}
	opts.progress(0.05)

	opts.stagef("metadata")
	logger.Printf("stage=metadata probing path=%q size=%d gpu=false (ffprobe metadata inspection, no GPU)", opts.MediaPath, fi.Size())
	probeStart := time.Now()
	pctx, cancel := context.WithTimeout(ctx, p.probeTimeout())
	res, perr := Probe(pctx, opts.MediaPath)
	cancel()
	elapsed := time.Since(probeStart).Round(time.Millisecond)
	if perr != nil {
		if ctx.Err() != nil {
			// Job context gone (queue pause/stop or shutdown): surface it
			// so the runner requeues instead of marking the media errored.
			return nil, ctx.Err()
		}
		note := perr.Error()
		if errors.Is(perr, context.DeadlineExceeded) {
			// Fallback for stalled probes (issue #1): the killed ffprobe
			// must not block the queue; skip the file and log it.
			note = fmt.Sprintf("probe timed out after %s; file skipped", p.probeTimeout())
		}
		logger.Printf("stage=metadata failed elapsed=%s error=%q", elapsed, note)
		out.Kind, out.Note = OutcomeMediaError, note
		return out, nil
	}
	audioOrdinal, audioLang := 0, ""
	if chosen := PickEnglishAudio(res.Audio); chosen != nil {
		audioOrdinal, audioLang = chosen.Ordinal, chosen.Lang
	} else {
		audioOrdinal = -1
	}
	out.Meta = &MediaMeta{
		Duration:   res.Duration,
		Width:      res.Width,
		Height:     res.Height,
		VideoCodec: res.VideoCodec,
		AudioCodec: res.AudioCodec,
		Container:  res.Container,
		StartTime:  res.StartTime,
	}
	out.AudioOrdinal, out.AudioLang = audioOrdinal, audioLang
	out.Flags = optimizationFlags(res)
	if len(out.Flags) > 0 {
		logger.Printf("stage=optimization recommended=true flags=%q video_bitrate=%d format_bitrate=%d avg_frame_rate=%.3f nominal_frame_rate=%.3f max_b_frames=%d", out.Flags, res.metadata.videoBitRate, res.metadata.formatBitRate, res.metadata.avgFrameRate, res.metadata.frameRate, res.metadata.maxBFrames)
	}
	logger.Printf("stage=metadata probe ok elapsed=%s container=%q duration_sec=%.3f video=%q width=%d height=%d audio=%q audio_streams=%d audio_ordinal=%d audio_lang=%q subtitle_streams=%d", elapsed, res.Container, res.Duration, res.VideoCodec, res.Width, res.Height, res.AudioCodec, len(res.Audio), audioOrdinal, audioLang, len(res.Subs))

	cues, track, note, err := p.selectSubtitles(ctx, opts, res, logger, out)
	if err != nil {
		return nil, err
	}
	out.Note = note
	if len(cues) == 0 {
		opts.stagef("no_subs")
		out.Kind, out.Note = OutcomeNoSubs, note
		logger.Printf("stage=no_subs no English subtitles note=%q", note)
		return out, nil
	}

	// MKV PTS drift mitigation: shift cues that begin at the container offset.
	if res.StartTime > 1 && cues[0].Start >= res.StartTime-1.5 {
		cues = subs.Offset(cues, -res.StartTime)
	}
	for i := range cues {
		cues[i].Start = clampMin(cues[i].Start)
	}
	out.Track = track
	out.Cues = cues
	opts.progress(0.6)
	return out, nil
}

// selectSubtitles carries out the sidecar-first, embedded-fallback selection
// including extraction and parsing. It returns the normalized cues, the track
// descriptor and the accumulated note; a nil track with empty cues means no
// usable subtitles were found.
//
//nolint:gocyclo // sequential subtitle-selection pipeline
func (p *Pipeline) selectSubtitles(ctx context.Context, opts ComputeOptions, res *Result, logger *log.Logger, out *Outcome) ([]subs.Cue, *TrackInfo, string, error) {
	var cues []subs.Cue
	var track *TrackInfo
	var note string

	logger.Printf("stage=sidecar selecting")
	if sidecar := subs.PickSidecar(opts.MediaPath); sidecar != "" {
		logger.Printf("stage=sidecar selected path=%q; parsing", sidecar)
		parseStart := time.Now()
		parsed, perr := subs.ParseFile(sidecar)
		if perr != nil {
			note = "sidecar unparsable: " + perr.Error()
			logger.Printf("stage=sidecar parse failed elapsed=%s error=%q; falling back to embedded", time.Since(parseStart).Round(time.Millisecond), perr)
		} else {
			lang := detectSidecarLang(sidecar)
			track = &TrackInfo{Source: "sidecar", Lang: lang, SidecarName: filepath.Base(sidecar)}
			cues = parsed
			logger.Printf("stage=sidecar parsed cues=%d elapsed=%s (zero cues falls back to embedded)", len(cues), time.Since(parseStart).Round(time.Millisecond))
		}
	} else {
		logger.Printf("stage=sidecar none selected; checking embedded streams")
	}
	if len(cues) > 0 {
		return cues, track, note, nil
	}

	// Choose an English TEXT subtitle stream; bitmap subs (pgs/vobsub)
	// cannot convert to SRT and are skipped entirely.
	pick, untaggedText := -1, -1
	for i, s := range res.Subs {
		if !IsTextSubtitle(s.Codec) {
			continue
		}
		l := strings.ToLower(strings.TrimSpace(s.Lang))
		if l == "en" || l == "eng" || strings.HasPrefix(l, "en-") {
			pick = i
			break
		}
		if l == "" && untaggedText == -1 {
			untaggedText = i
		}
	}
	if pick == -1 && untaggedText != -1 && len(res.Subs) == 1 {
		pick = untaggedText
	}
	if pick < 0 {
		logger.Printf("stage=embedded_selection none eligible subtitle_streams=%d (requires English text or a sole untagged text stream; bitmap subtitles skipped)", len(res.Subs))
		return nil, nil, note, nil
	}
	es := res.Subs[pick]
	logger.Printf("stage=embedded_selection selected index=%d ordinal=%d codec=%q lang=%q", es.Index, es.Ordinal, es.Codec, es.Lang)
	outPath := filepath.Join(p.SubsCacheDir, fmt.Sprintf("%d.srt", opts.MediaID))
	var xerr error
	reused := false
	// Re-probe of an already-processed, unchanged file: reuse the cached
	// extraction instead of paying another full-file read.
	if opts.KnownStatus != "pending" {
		if st, serr := os.Stat(outPath); serr == nil && st.Size() > 0 {
			reused = true
		}
	}
	if !reused {
		if xerr = p.extract(ctx, opts.MediaPath, es.Ordinal, outPath, logger, 30*time.Second); ctx.Err() != nil {
			return nil, nil, note, ctx.Err()
		}
		if xerr != nil && errors.Is(xerr, context.DeadlineExceeded) {
			logger.Printf("stage=extraction timed out after %s", p.extractTimeout())
		}
	} else {
		logger.Printf("stage=extraction cache reused path=%q (no full-file scan)", outPath)
	}
	if xerr != nil {
		note = "embedded extraction failed: " + xerr.Error()
		//nolint:nilerr // intentional: an extraction failure is a media-level note (no_subs), not a job failure
		return nil, nil, note, nil
	}
	parseStart := time.Now()
	logger.Printf("stage=embedded_parse parsing path=%q", outPath)
	parsed, perr := subs.ParseFile(outPath)
	if perr != nil {
		note = "embedded subs unparsable: " + perr.Error()
		logger.Printf("stage=embedded_parse failed elapsed=%s error=%q", time.Since(parseStart).Round(time.Millisecond), perr)
		return nil, nil, note, nil
	}
	track = &TrackInfo{Source: "embedded", Lang: es.Lang, StreamIndex: es.Index}
	out.SubFile = outPath
	cues = parsed
	logger.Printf("stage=embedded_parse parsed cues=%d elapsed=%s", len(cues), time.Since(parseStart).Round(time.Millisecond))
	return cues, track, note, nil
}

// extract holds storage capacity only while ffmpeg runs. Heartbeats describe
// liveness, not bytes read: ffmpeg does not report measured storage throughput.
func (p *Pipeline) extract(ctx context.Context, path string, ordinal int, outPath string, logger *log.Logger, heartbeat time.Duration) error {
	sem := p.sem()
	waitStart := time.Now()
	logger.Printf("stage=extraction_queue waiting active=%d capacity=%d", len(sem), cap(sem))
	select {
	case <-ctx.Done():
		logger.Printf("stage=extraction_queue canceled wait_elapsed=%s error=%q", time.Since(waitStart).Round(time.Millisecond), ctx.Err())
		return ctx.Err()
	case sem <- struct{}{}:
	}
	defer func() { <-sem }()
	// If cancellation and capacity became ready together, do not start ffmpeg.
	if err := ctx.Err(); err != nil {
		logger.Printf("stage=extraction_queue canceled wait_elapsed=%s error=%q", time.Since(waitStart).Round(time.Millisecond), err)
		return err
	}
	logger.Printf("stage=extraction_queue acquired wait_elapsed=%s active=%d capacity=%d", time.Since(waitStart).Round(time.Millisecond), len(sem), cap(sem))
	// The timeout starts after the concurrency slot is acquired: waiting
	// behind other full-file extractions must not turn a healthy file into a
	// false no_subs result.
	runCtx := ctx
	if d := p.extractTimeout(); d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	started := time.Now()
	logger.Printf("stage=extraction started ordinal=%d output=%q gpu=false (full-file container scan, storage-bound; CPU subtitle conversion, no GPU; elapsed only, not measured bytes or throughput)", ordinal, outPath)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				logger.Printf("stage=extraction running elapsed=%s (elapsed only, not measured bytes or throughput)", time.Since(started).Round(time.Second))
			}
		}
	}()
	err := ExtractSubtitle(runCtx, path, ordinal, outPath)
	close(stop)
	<-done
	if err != nil && runCtx.Err() != nil && ctx.Err() == nil {
		err = fmt.Errorf("subtitle extraction timed out after %s: %w", p.extractTimeout(), runCtx.Err())
	}
	if err != nil {
		logger.Printf("stage=extraction failed elapsed=%s error=%q", time.Since(started).Round(time.Millisecond), err)
	} else {
		logger.Printf("stage=extraction succeeded elapsed=%s", time.Since(started).Round(time.Millisecond))
	}
	return err
}

// recordStatus persists a media status change; failures are logged rather
// than returned because the caller has usually already committed the primary
// outcome (cues, chunks, job) and must not fail the job over a status write.
func (p *Pipeline) recordStatus(mediaID int64, status, msg string) {
	if err := p.St.SetMediaStatus(mediaID, status, msg); err != nil {
		log.Printf("[probe] media %d: set status %q failed: %v", mediaID, status, err)
	}
}

func detectSidecarLang(_ string) string {
	return "en"
}

func clampMin(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}
