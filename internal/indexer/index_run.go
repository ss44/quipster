package indexer

import (
	"context"
	"fmt"
	"log"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/store"
)

const taskPollInterval = 300 * time.Millisecond
const taskProgressInterval = 5 * time.Second

type indexRun struct {
	ix       *Indexer
	progress func(float64)
	mediaID  int64
	jobID    int64
}

func (r indexRun) report(value float64) {
	if r.progress != nil {
		r.progress(value)
	}
}

// stage intentionally records only bounded operational metadata, never remote
// error messages, document text, paths, titles, credentials or request bodies.
func (r indexRun) stage(stage, status string, started time.Time, count int) {
	log.Printf("[indexer] media=%d job=%d stage=%s status=%s elapsed=%s count=%d", r.mediaID, r.jobID, stage, status, time.Since(started).Round(time.Millisecond), count)
}

func (ix *Indexer) indexMedia(ctx context.Context, mediaID, jobID int64, progress func(float64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := indexRun{ix: ix, mediaID: mediaID, jobID: jobID, progress: progress}
	r.report(0.02)
	started := time.Now()
	snapshot, err := ix.st.LoadIndexSnapshot(ctx, mediaID)
	if err != nil {
		r.stage("snapshot", "error", started, 0)
		return err
	}
	log.Printf("[indexer] media=%d job=%d stage=snapshot status=succeeded elapsed=%s chunks=%d pending=%d ever_indexed=%t active=%t", mediaID, jobID, time.Since(started).Round(time.Millisecond), snapshot.Total, snapshot.Pending, snapshot.EverIndexed, snapshot.Active)
	r.report(0.1)
	if !snapshot.Active || (snapshot.Total > 0 && snapshot.Pending == 0) {
		return nil
	}
	started = time.Now()
	docs, ids := buildIndexDocs(snapshot.Chunks)
	r.stage("build", "succeeded", started, len(docs))
	// The first-index skip is deliberate. Retrying an incomplete first upload
	// upserts the complete snapshot by stable document ID, without a delete.
	if snapshot.EverIndexed {
		if err := r.delete(ctx); err != nil {
			return err
		}
	}
	r.report(0.25)
	if len(docs) == 0 {
		return nil
	}
	if err := r.add(ctx, docs); err != nil {
		return err
	}
	r.report(0.9)
	started = time.Now()
	if err := ctx.Err(); err != nil {
		r.stage("latch", "canceled", started, 0)
		return err
	}
	if err := ix.st.MarkIndexSnapshotChunks(ctx, mediaID, ids, time.Now().Unix()); err != nil {
		r.stage("latch", "error", started, len(ids))
		return err
	}
	r.stage("latch", "succeeded", started, len(ids))
	r.report(0.95)
	return nil
}

func buildIndexDocs(chunks []store.ChunkWithMedia) ([]LineDoc, []int64) {
	docs := make([]LineDoc, len(chunks))
	ids := make([]int64, len(chunks))
	for i, ch := range chunks {
		docs[i] = LineDoc{ID: DocID(ch.MediaFileID, ch.StartCueIdx, ch.EndCueIdx), Text: ch.Text,
			MediaID: ch.MediaFileID, MediaTitle: displayTitle(ch.MediaTitle, ch.ShowTitle, ch.Season, ch.Episode, ch.Year),
			ShowTitle: ch.ShowTitle, Season: ch.Season, Episode: ch.Episode, Year: ch.Year,
			StartCueIdx: ch.StartCueIdx, EndCueIdx: ch.EndCueIdx, ChunkStartSec: ch.StartSec, ChunkEndSec: ch.EndSec}
		ids[i] = ch.ID
	}
	return docs, ids
}

func (r indexRun) delete(ctx context.Context) error {
	started := time.Now()
	task, err := r.ix.client.Index(IndexName).DeleteDocumentsByFilterWithContext(ctx, fmt.Sprintf(`mediaId = %d`, r.mediaID), nil)
	if err != nil {
		r.stage("delete_enqueue", "error", started, 0)
		return fmt.Errorf("delete docs %d: %w", r.mediaID, err)
	}
	r.stage("delete_enqueue", "succeeded", started, 1)
	return r.waitTask(ctx, "delete", task.TaskUID, 0.1)
}

func (r indexRun) add(ctx context.Context, docs []LineDoc) error {
	started := time.Now()
	tasks, err := r.ix.client.Index(IndexName).AddDocumentsInBatchesWithContext(ctx, docs, r.ix.batch, nil)
	if err != nil {
		r.stage("add_enqueue", "error", started, len(docs))
		return fmt.Errorf("add docs %d: %w", r.mediaID, err)
	}
	r.stage("add_enqueue", "succeeded", started, len(docs))
	for i, task := range tasks {
		value := 0.25 + 0.6*float64(i)/float64(len(tasks))
		r.report(value)
		if err := r.waitTask(ctx, "add", task.TaskUID, value); err != nil {
			return err
		}
	}
	return nil
}

// waitTask polls at a fixed bounded cadence and reports a heartbeat at most
// once per five seconds, not once per poll. Progress is a completed-stage
// estimate, not a claim that embedding has advanced while a task is pending.
func (r indexRun) waitTask(ctx context.Context, stage string, uid int64, value float64) error {
	started := time.Now()
	lastProgress := started
	r.taskStage(stage, "enqueued", uid, started)
	for {
		task, err := r.getTask(ctx, stage, uid, value, started, &lastProgress)
		if err != nil {
			status := "error"
			if ctx.Err() != nil {
				status = "canceled"
			}
			r.taskStage(stage, status, uid, started)
			return fmt.Errorf("%s wait %d task %d: %w", stage, r.mediaID, uid, err)
		}
		status := task.Status
		if status == meili.TaskStatusSucceeded {
			r.taskStage(stage, string(status), uid, started)
			return nil
		}
		if status == meili.TaskStatusFailed || status == meili.TaskStatusCanceled {
			r.taskStage(stage, string(status), uid, started)
			return fmt.Errorf("%s failed %d: task=%d status=%s", stage, r.mediaID, uid, status)
		}
		if time.Since(lastProgress) >= taskProgressInterval {
			r.taskStage(stage, string(status), uid, started)
			r.report(value)
			lastProgress = time.Now()
		}
		timer := time.NewTimer(taskPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			r.taskStage(stage, "canceled", uid, started)
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// getTask keeps progress callbacks on the handler goroutine even when an HTTP
// round-trip is slow. Only one request is outstanding; a buffered result and
// derived context let its goroutine exit safely when the job is canceled.
func (r indexRun) getTask(ctx context.Context, stage string, uid int64, value float64, started time.Time, lastProgress *time.Time) (*meili.Task, error) {
	type result struct {
		task *meili.Task
		err  error
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, 1)
	go func() {
		task, err := r.ix.client.GetTaskWithContext(requestCtx, uid)
		results <- result{task: task, err: err}
	}()
	timer := time.NewTimer(max(0, taskProgressInterval-time.Since(*lastProgress)))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case got := <-results:
			return got.task, got.err
		case <-timer.C:
			r.taskStage(stage+"_poll", "waiting", uid, started)
			r.report(value)
			*lastProgress = time.Now()
			timer.Reset(taskProgressInterval)
		}
	}
}

func (r indexRun) taskStage(stage, status string, uid int64, started time.Time) {
	log.Printf("[indexer] media=%d job=%d stage=%s taskUID=%d status=%s elapsed=%s", r.mediaID, r.jobID, stage, uid, status, time.Since(started).Round(time.Millisecond))
}
