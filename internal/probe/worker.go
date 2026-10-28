package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quipster/internal/chunks"
	"quipster/internal/store"
	"quipster/internal/subs"
)

// Pipeline is the probe worker: ffprobe → subtitle pick/extract/parse →
// cue store → chunk build → enqueue index job.
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
	once         sync.Once
}

func (p *Pipeline) probeTimeout() time.Duration {
	if p.ProbeTimeout > 0 {
		return p.ProbeTimeout
	}
	return defaultProbeTimeout
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

// JobHandler adapts Pipeline to the job runner.
//
// The flow is deliberately kept linear: splitting it across helpers would
// scatter the media status transitions it must keep coherent.
//
//nolint:gocyclo // sequential store pipeline
func (p *Pipeline) JobHandler() func(context.Context, *store.Job, func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) (jobErr error) {
		var payload struct {
			MediaID int64 `json:"mediaId"`
		}
		if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		started := time.Now()
		logger := log.New(log.Writer(), fmt.Sprintf("[probe] media %d job %d: ", payload.MediaID, j.ID), log.Flags())
		stage, outcome := "lookup", "skipped"
		defer func() {
			if jobErr != nil {
				logger.Printf("stage=%s failed elapsed=%s error=%q", stage, time.Since(started).Round(time.Millisecond), jobErr)
				outcome = "failed"
			}
			logger.Printf("stage=complete outcome=%s total_elapsed=%s", outcome, time.Since(started).Round(time.Millisecond))
		}()
		m, err := p.St.GetMedia(payload.MediaID)
		if err != nil {
			return err
		}
		if m == nil {
			return nil
		}
		stage = "stat"
		fi, serr := os.Stat(m.Path)
		if serr != nil {
			// The job itself is handled: record the media as errored and
			// let the queue move on instead of spinning on retries.
			p.recordStatus(m.ID, "error", "media file missing on disk")
			logger.Printf("stage=stat failed path=%q error=%q", m.Path, serr)
			outcome = "media_error"
			return nil //nolint:nilerr // intentional: media-level failure, not job failure
		}
		progress(0.05)

		start := time.Now()
		stage = "metadata"
		logger.Printf("stage=metadata probing path=%q size=%d gpu=false (ffprobe metadata inspection, no GPU)", m.Path, fi.Size())
		pctx, cancel := context.WithTimeout(ctx, p.probeTimeout())
		res, perr := Probe(pctx, m.Path)
		cancel()
		elapsed := time.Since(start).Round(time.Millisecond)
		if perr != nil {
			if ctx.Err() != nil {
				// Job context gone (queue pause/stop or shutdown): surface it
				// so the runner requeues instead of marking the media errored.
				return ctx.Err()
			}
			note := perr.Error()
			if errors.Is(perr, context.DeadlineExceeded) {
				// Fallback for stalled probes (issue #1): the killed ffprobe
				// must not block the queue; skip the file and log it.
				note = fmt.Sprintf("probe timed out after %s; file skipped", p.probeTimeout())
			}
			logger.Printf("stage=metadata failed elapsed=%s error=%q", elapsed, note)
			outcome = "media_error"
			p.recordStatus(m.ID, "error", note)
			return nil //nolint:nilerr // intentional: media-level failure, not job failure
		}
		audioOrdinal, audioLang := 0, ""
		if chosen := PickEnglishAudio(res.Audio); chosen != nil {
			audioOrdinal, audioLang = chosen.Ordinal, chosen.Lang
		} else {
			audioOrdinal = -1
		}
		flags := optimizationFlags(res)
		if len(flags) > 0 {
			logger.Printf("stage=optimization recommended=true flags=%q video_bitrate=%d format_bitrate=%d avg_frame_rate=%.3f nominal_frame_rate=%.3f max_b_frames=%d", flags, res.metadata.videoBitRate, res.metadata.formatBitRate, res.metadata.avgFrameRate, res.metadata.frameRate, res.metadata.maxBFrames)
		}
		logger.Printf("stage=metadata probe ok elapsed=%s container=%q duration_sec=%.3f video=%q width=%d height=%d audio=%q audio_streams=%d audio_ordinal=%d audio_lang=%q subtitle_streams=%d", elapsed, res.Container, res.Duration, res.VideoCodec, res.Width, res.Height, res.AudioCodec, len(res.Audio), audioOrdinal, audioLang, len(res.Subs))
		stage = "metadata_store"
		if uerr := p.St.UpdateMediaProbe(m.ID, res.Duration, res.Width, res.Height, res.VideoCodec, res.AudioCodec, res.Container, res.StartTime, audioOrdinal, audioLang); uerr != nil {
			return uerr
		}
		if uerr := p.St.UpdateMediaOptimizationFlags(m.ID, flags); uerr != nil {
			return uerr
		}
		progress(0.3)

		var cues []subs.Cue
		var track store.SubtitleTrack
		var note string

		stage = "sidecar"
		logger.Printf("stage=sidecar selecting")
		if sidecar := subs.PickSidecar(m.Path); sidecar != "" {
			logger.Printf("stage=sidecar selected path=%q; parsing", sidecar)
			parseStart := time.Now()
			parsed, perr := subs.ParseFile(sidecar)
			if perr != nil {
				note = "sidecar unparsable: " + perr.Error()
				logger.Printf("stage=sidecar parse failed elapsed=%s error=%q; falling back to embedded", time.Since(parseStart).Round(time.Millisecond), perr)
			} else {
				lang := detectSidecarLang(sidecar)
				track = store.SubtitleTrack{Source: "sidecar", Lang: lang, ExtractedPath: sidecar}
				cues = parsed
				logger.Printf("stage=sidecar parsed cues=%d elapsed=%s (zero cues falls back to embedded)", len(cues), time.Since(parseStart).Round(time.Millisecond))
			}
		} else {
			logger.Printf("stage=sidecar none selected; checking embedded streams")
		}
		if len(cues) == 0 {
			stage = "embedded_selection"
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
			if pick >= 0 {
				es := res.Subs[pick]
				logger.Printf("stage=embedded_selection selected index=%d ordinal=%d codec=%q lang=%q", es.Index, es.Ordinal, es.Codec, es.Lang)
				outPath := filepath.Join(p.SubsCacheDir, fmt.Sprintf("%d.srt", m.ID))
				var xerr error
				reused := false
				// Re-probe of an already-processed, unchanged file: reuse the
				// cached extraction instead of paying another full-file read.
				if m.Status != "pending" {
					if st, serr := os.Stat(outPath); serr == nil && st.Size() > 0 {
						reused = true
					}
				}
				if !reused {
					stage = "extraction"
					xerr = p.extract(ctx, m.Path, es.Ordinal, outPath, logger, 30*time.Second)
					if ctx.Err() != nil {
						return ctx.Err()
					}
				} else {
					logger.Printf("stage=extraction cache reused path=%q (no full-file scan)", outPath)
				}
				if xerr != nil {
					note = "embedded extraction failed: " + xerr.Error()
				} else {
					stage = "embedded_parse"
					parseStart := time.Now()
					logger.Printf("stage=embedded_parse parsing path=%q", outPath)
					parsed, perr := subs.ParseFile(outPath)
					if perr != nil {
						note = "embedded subs unparsable: " + perr.Error()
						logger.Printf("stage=embedded_parse failed elapsed=%s error=%q", time.Since(parseStart).Round(time.Millisecond), perr)
					} else {
						track = store.SubtitleTrack{Source: "embedded", Lang: es.Lang, StreamIndex: es.Index, ExtractedPath: outPath}
						cues = parsed
						logger.Printf("stage=embedded_parse parsed cues=%d elapsed=%s", len(cues), time.Since(parseStart).Round(time.Millisecond))
					}
				}
			} else {
				logger.Printf("stage=embedded_selection none eligible subtitle_streams=%d (requires English text or a sole untagged text stream; bitmap subtitles skipped)", len(res.Subs))
			}
		}

		if len(cues) == 0 {
			stage = "no_subs"
			if cerr := p.St.CleanSubtitles(m.ID); cerr != nil {
				return cerr
			}
			if serr := p.St.SetMediaStatus(m.ID, "no_subs", note); serr != nil {
				return serr
			}
			logger.Printf("stage=no_subs no English subtitles note=%q", note)
			outcome = "no_subs"
			return nil
		}

		// MKV PTS drift mitigation: shift cues that begin at the container offset.
		if res.StartTime > 1 && cues[0].Start >= res.StartTime-1.5 {
			cues = subs.Offset(cues, -res.StartTime)
		}
		for i := range cues {
			cues[i].Start = clampMin(cues[i].Start)
		}

		stage = "cue_store"
		logger.Printf("stage=cue_store storing source=%s cues=%d", track.Source, len(cues))
		trackID, err := p.St.ReplaceTrack(m.ID, &track)
		if err != nil {
			return err
		}
		scues := make([]store.Cue, len(cues))
		for i, c := range cues {
			scues[i] = store.Cue{MediaFileID: m.ID, TrackID: trackID, Idx: i, StartSec: c.Start, EndSec: c.End, Text: c.Text}
		}
		if err := p.St.ReplaceCues(m.ID, trackID, scues); err != nil {
			return err
		}
		progress(0.6)
		logger.Printf("stage=cue_store stored cues=%d", len(scues))

		stage = "chunk_build"
		logger.Printf("stage=chunk_build building cues=%d", len(scues))
		build := make([]chunks.Cue, len(scues))
		for i, c := range scues {
			build[i] = chunks.Cue{Idx: c.Idx, StartSec: c.StartSec, EndSec: c.EndSec, Text: c.Text}
		}
		built := chunks.Build(build)
		stage = "chunk_store"
		logger.Printf("stage=chunk_store storing chunks=%d", len(built))
		schunks := make([]store.Chunk, len(built))
		for i, ch := range built {
			schunks[i] = store.Chunk{MediaFileID: m.ID, StartCueIdx: ch.StartCueIdx, EndCueIdx: ch.EndCueIdx, Text: ch.Text}
		}
		if err := p.St.ReplaceChunks(m.ID, schunks); err != nil {
			return err
		}
		progress(0.85)
		logger.Printf("stage=chunk_store stored chunks=%d", len(schunks))

		stage = "index_enqueue"
		logger.Printf("stage=index_enqueue enqueueing cues=%d chunks=%d", len(scues), len(schunks))
		payloadB, merr := json.Marshal(map[string]any{"mediaId": m.ID})
		if merr != nil {
			return fmt.Errorf("marshal index payload: %w", merr)
		}
		if _, err := p.St.EnqueueJob("index", string(payloadB), 1); err != nil {
			return err
		}
		logger.Printf("stage=index_enqueue enqueued (indexing runs separately)")
		if note != "" {
			p.recordStatus(m.ID, "pending", note)
		}
		outcome = "index_queued"
		return nil
	}
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
			case <-ticker.C:
				logger.Printf("stage=extraction running elapsed=%s (elapsed only, not measured bytes or throughput)", time.Since(started).Round(time.Second))
			}
		}
	}()
	err := ExtractSubtitle(ctx, path, ordinal, outPath)
	close(stop)
	<-done
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
