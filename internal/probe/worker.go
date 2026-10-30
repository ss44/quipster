package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"quipster/internal/store"
)

// JobHandler adapts Pipeline to the job runner: it resolves the media row,
// runs the media-bound Compute stages and applies the resulting Outcome to
// the store. The media-bound logic lives in Compute so worker nodes can
// execute it remotely and upload the same Outcome for Apply.
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
		// Archived rows are frozen: probing must never mutate their status,
		// and the file is gone from disk anyway. If the media is restored the
		// scan enqueues whatever work it needs at that point.
		if m.Archived {
			return nil
		}
		o, cerr := p.Compute(ctx, ComputeOptions{
			MediaID:     m.ID,
			MediaPath:   m.Path,
			KnownStatus: m.Status,
			Logger:      logger,
			Progress:    progress,
			Stage:       &stage,
		})
		if cerr != nil {
			return cerr
		}
		stage = "apply"
		if aerr := p.Apply(m.ID, m.Path, o, nil, logger); aerr != nil {
			return aerr
		}
		progress(0.85)
		outcome = o.Kind
		if o.Kind == OutcomeOK {
			outcome = "index_queued"
		}
		return nil
	}
}
