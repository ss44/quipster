package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"quipster/internal/chunks"
	"quipster/internal/store"
	"quipster/internal/subs"
)

// Pipeline is the probe worker: ffprobe → subtitle pick/extract/parse →
// cue store → chunk build → enqueue index job.
type Pipeline struct {
	St           *store.Store
	SubsCacheDir string
}

// JobHandler adapts Pipeline to the job runner.
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
		if _, err := os.Stat(m.Path); err != nil {
			p.St.SetMediaStatus(m.ID, "error", "media file missing on disk")
			return nil
		}
		progress(0.05)

		res, err := Probe(m.Path)
		if err != nil {
			p.St.SetMediaStatus(m.ID, "error", err.Error())
			return nil
		}
		if err := p.St.UpdateMediaProbe(m.ID, res.Duration, res.Width, res.Height, res.VideoCodec, res.AudioCodec, res.Container, res.StartTime); err != nil {
			return err
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
			langs := make([]string, len(res.Subs))
			for i, s := range res.Subs {
				langs[i] = s.Lang
			}
			if ord := subs.EnglishStream(langs); ord >= 0 {
				outPath := filepath.Join(p.SubsCacheDir, fmt.Sprintf("%d.srt", m.ID))
				if xerr := ExtractSubtitle(m.Path, ord, outPath); xerr != nil {
					note = "embedded extraction failed: " + xerr.Error()
				} else {
					parsed, perr := subs.ParseFile(outPath)
					if perr != nil {
						note = "embedded subs unparsable: " + perr.Error()
					} else {
						track = store.SubtitleTrack{Source: "embedded", Lang: res.Subs[ord].Lang, StreamIndex: res.Subs[ord].Index, ExtractedPath: outPath}
						cues = parsed
					}
				}
			}
		}

		if len(cues) == 0 {
			if err := p.St.CleanSubtitles(m.ID); err != nil {
				return err
			}
			if err := p.St.SetMediaStatus(m.ID, "no_subs", note); err != nil {
				return err
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

		payloadB, _ := json.Marshal(map[string]any{"mediaId": m.ID})
		if _, err := p.St.EnqueueJob("index", string(payloadB), 1); err != nil {
			return err
		}
		if note != "" {
			p.St.SetMediaStatus(m.ID, "pending", note)
		}
		return nil
	}
}

func detectSidecarLang(path string) string {
	return "en"
}

func clampMin(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}