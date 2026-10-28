package probe

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"quipster/internal/chunks"
	"quipster/internal/store"
)

// Apply persists a Compute outcome into the store. Both the local pipeline
// and the server-side handler for remote node results call it, so a probe
// produces identical rows no matter which machine executed the media-bound
// work.
//
// subFile carries the extracted subtitle bytes for remote runs; local runs
// pass nil because Compute already wrote the file into the subs cache
// directory at its canonical location.
//
//nolint:gocyclo // sequential store application
func (p *Pipeline) Apply(mediaID int64, mediaPath string, o *Outcome, subFile io.Reader, logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}
	switch o.Kind {
	case OutcomeMediaError:
		if err := p.St.SetMediaStatus(mediaID, "error", o.Note); err != nil {
			return err
		}
		return nil
	case OutcomeNoSubs, OutcomeOK:
	default:
		return fmt.Errorf("unknown probe outcome %q", o.Kind)
	}

	// Metadata is persisted before the subtitle verdict: even a no_subs or
	// extraction-failed file keeps its ffprobe facts and audio selection.
	if o.Meta == nil {
		return fmt.Errorf("outcome %q without probe metadata", o.Kind)
	}
	if err := p.St.UpdateMediaProbe(mediaID, o.Meta.Duration, o.Meta.Width, o.Meta.Height,
		o.Meta.VideoCodec, o.Meta.AudioCodec, o.Meta.Container, o.Meta.StartTime, o.AudioOrdinal, o.AudioLang); err != nil {
		return err
	}
	if err := p.St.UpdateMediaOptimizationFlags(mediaID, o.Flags); err != nil {
		return err
	}
	if o.Kind == OutcomeNoSubs {
		if err := p.St.CleanSubtitles(mediaID); err != nil {
			return err
		}
		if err := p.St.SetMediaStatus(mediaID, "no_subs", o.Note); err != nil {
			return err
		}
		return nil
	}

	extractedPath, err := p.placeSubtitle(mediaID, mediaPath, o, subFile)
	if err != nil {
		return err
	}
	track := store.SubtitleTrack{
		Source:        o.Track.Source,
		Lang:          o.Track.Lang,
		StreamIndex:   o.Track.StreamIndex,
		ExtractedPath: extractedPath,
	}
	trackID, err := p.St.ReplaceTrack(mediaID, &track)
	if err != nil {
		return err
	}
	logger.Printf("stage=cue_store storing source=%s cues=%d", track.Source, len(o.Cues))
	scues := make([]store.Cue, len(o.Cues))
	for i, c := range o.Cues {
		scues[i] = store.Cue{MediaFileID: mediaID, TrackID: trackID, Idx: i, StartSec: c.Start, EndSec: c.End, Text: c.Text}
	}
	if err := p.St.ReplaceCues(mediaID, trackID, scues); err != nil {
		return err
	}
	logger.Printf("stage=cue_store stored cues=%d", len(scues))

	logger.Printf("stage=chunk_build building cues=%d", len(scues))
	build := make([]chunks.Cue, len(scues))
	for i, c := range scues {
		build[i] = chunks.Cue{Idx: c.Idx, StartSec: c.StartSec, EndSec: c.EndSec, Text: c.Text}
	}
	built := chunks.Build(build)
	logger.Printf("stage=chunk_store storing chunks=%d", len(built))
	schunks := make([]store.Chunk, len(built))
	for i, ch := range built {
		schunks[i] = store.Chunk{MediaFileID: mediaID, StartCueIdx: ch.StartCueIdx, EndCueIdx: ch.EndCueIdx, Text: ch.Text}
	}
	if err := p.St.ReplaceChunks(mediaID, schunks); err != nil {
		return err
	}
	logger.Printf("stage=chunk_store stored chunks=%d", len(schunks))

	payload, merr := json.Marshal(map[string]any{"mediaId": mediaID})
	if merr != nil {
		return fmt.Errorf("marshal index payload: %w", merr)
	}
	if _, err := p.St.EnqueueJob("index", string(payload), 1); err != nil {
		return err
	}
	logger.Printf("stage=index_enqueue enqueued (indexing runs separately)")
	if o.Note != "" {
		// Keep the accumulated note visible (e.g. an unparsable sidecar
		// that was replaced by a working embedded track) without failing
		// the successful probe.
		p.recordStatus(mediaID, "pending", o.Note)
	}
	return nil
}

// placeSubtitle resolves the ExtractedPath for the chosen track. Embedded
// extractions are stored in the subs cache under the media id; a remote
// upload is written there, a local extraction is already present, and sidecar
// tracks point at the file sitting next to the media on the server mount.
func (p *Pipeline) placeSubtitle(mediaID int64, mediaPath string, o *Outcome, subFile io.Reader) (string, error) {
	if o.Track.Source == "sidecar" {
		return filepath.Join(filepath.Dir(mediaPath), o.Track.SidecarName), nil
	}
	outPath := filepath.Join(p.SubsCacheDir, fmt.Sprintf("%d.srt", mediaID))
	if subFile != nil {
		// Remote results may arrive before the cache directory exists
		// (fresh deployments); create it instead of failing the upload.
		if err := os.MkdirAll(p.SubsCacheDir, 0o750); err != nil {
			return "", err
		}
	}
	if subFile == nil {
		if o.SubFile != "" && o.SubFile != outPath {
			// Defensive: a local run must already have written the
			// canonical path; anything else is a caller bug.
			return "", fmt.Errorf("extracted subtitle path %q is not the canonical %q", o.SubFile, outPath)
		}
		return outPath, nil
	}
	f, err := os.Create(outPath) //nolint:gosec // outPath is built from the configured cache dir and a numeric media id
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("[probe] close uploaded subtitle %d: %v", mediaID, cerr)
		}
	}()
	if _, err := io.Copy(f, subFile); err != nil {
		return "", fmt.Errorf("store uploaded subtitle: %w", err)
	}
	return outPath, nil
}
