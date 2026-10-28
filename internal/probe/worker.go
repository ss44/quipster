package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	once         sync.Once
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
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
		var payload struct {
			MediaID int64 `json:"mediaId"`
		}
		if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		m, err := p.St.GetMedia(payload.MediaID)
		if err != nil {
			return err
		}
		if m == nil {
			return nil
		}
		if _, serr := os.Stat(m.Path); serr != nil {
			// The job itself is handled: record the media as errored and
			// let the queue move on instead of spinning on retries.
			p.recordStatus(m.ID, "error", "media file missing on disk")
			return nil //nolint:nilerr // intentional: media-level failure, not job failure
		}
		progress(0.05)

		res, err := Probe(m.Path)
		if err != nil {
			p.recordStatus(m.ID, "error", err.Error())
			return nil
		}
		audioOrdinal, audioLang := 0, ""
		if chosen := PickEnglishAudio(res.Audio); chosen != nil {
			audioOrdinal, audioLang = chosen.Ordinal, chosen.Lang
		} else {
			audioOrdinal = -1
		}
		if uerr := p.St.UpdateMediaProbe(m.ID, res.Duration, res.Width, res.Height, res.VideoCodec, res.AudioCodec, res.Container, res.StartTime, audioOrdinal, audioLang); uerr != nil {
			return uerr
		}
		progress(0.3)

		var cues []subs.Cue
		var track store.SubtitleTrack
		var note string

		if sidecar := subs.PickSidecar(m.Path); sidecar != "" {
			parsed, perr := subs.ParseFile(sidecar)
			if perr != nil {
				note = "sidecar unparsable: " + perr.Error()
			} else {
				lang := detectSidecarLang(sidecar)
				track = store.SubtitleTrack{Source: "sidecar", Lang: lang, ExtractedPath: sidecar}
				cues = parsed
			}
		}
		if len(cues) == 0 {
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
					p.sem() <- struct{}{}
					xerr = ExtractSubtitle(ctx, m.Path, es.Ordinal, outPath)
					<-p.sem()
				}
				if xerr != nil {
					note = "embedded extraction failed: " + xerr.Error()
				} else {
					parsed, perr := subs.ParseFile(outPath)
					if perr != nil {
						note = "embedded subs unparsable: " + perr.Error()
					} else {
						track = store.SubtitleTrack{Source: "embedded", Lang: es.Lang, StreamIndex: es.Index, ExtractedPath: outPath}
						cues = parsed
					}
				}
			}
		}

		if len(cues) == 0 {
			if cerr := p.St.CleanSubtitles(m.ID); cerr != nil {
				return cerr
			}
			if serr := p.St.SetMediaStatus(m.ID, "no_subs", note); serr != nil {
				return serr
			}
			log.Printf("[probe] media %d %s: no English subtitles (%s)", m.ID, m.Path, note)
			return nil
		}

		// MKV PTS drift mitigation: shift cues that begin at the container offset.
		if res.StartTime > 1 && cues[0].Start >= res.StartTime-1.5 {
			cues = subs.Offset(cues, -res.StartTime)
		}
		for i := range cues {
			cues[i].Start = clampMin(cues[i].Start)
		}

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

		build := make([]chunks.Cue, len(scues))
		for i, c := range scues {
			build[i] = chunks.Cue{Idx: c.Idx, StartSec: c.StartSec, EndSec: c.EndSec, Text: c.Text}
		}
		built := chunks.Build(build)
		schunks := make([]store.Chunk, len(built))
		for i, ch := range built {
			schunks[i] = store.Chunk{MediaFileID: m.ID, StartCueIdx: ch.StartCueIdx, EndCueIdx: ch.EndCueIdx, Text: ch.Text}
		}
		if err := p.St.ReplaceChunks(m.ID, schunks); err != nil {
			return err
		}
		progress(0.85)

		payloadB, merr := json.Marshal(map[string]any{"mediaId": m.ID})
		if merr != nil {
			return fmt.Errorf("marshal index payload: %w", merr)
		}
		if _, err := p.St.EnqueueJob("index", string(payloadB), 1); err != nil {
			return err
		}
		if note != "" {
			p.recordStatus(m.ID, "pending", note)
		}
		return nil
	}
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
