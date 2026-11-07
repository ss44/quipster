package indexer

import (
	"fmt"
	"log"

	"quipster/internal/store"
)

// EnqueueArchiveDeindex enqueues one unique low-priority de-index job per
// archived media, in bounded batches. Deduplication makes the call idempotent
// across overlapping scans and reconcile passes. A failed batch does not stop
// the remaining batches: the next boot reconcile would recover the backlog,
// but continuing here shrinks the gap instead of leaving it until restart.
func (ix *Indexer) EnqueueArchiveDeindex(ids []int64) {
	if len(ids) == 0 {
		return
	}
	const batch = 1000
	enqueued := 0
	skipped := 0
	for start := 0; start < len(ids); start += batch {
		specs := make([]store.JobSpec, 0, min(batch, len(ids)-start))
		for _, id := range ids[start:min(start+batch, len(ids))] {
			specs = append(specs, store.JobSpec{
				Type:     "archive_deindex",
				Payload:  fmt.Sprintf(`{"mediaId":%d}`, id),
				Priority: -1,
			})
		}
		n, err := ix.st.EnqueueUniqueJobs(specs)
		if err != nil {
			log.Printf("enqueue archive deindex jobs: %v", err)
			skipped += len(specs)
			continue
		}
		enqueued += n
	}
	if enqueued > 0 {
		log.Printf("queued %d archive de-index job(s)", enqueued)
	}
	if skipped > 0 {
		log.Printf("skipped %d archive de-index job(s) after enqueue errors; boot reconcile will retry them", skipped)
	}
}

// ReconcileArchivedDeindex re-enqueues de-index jobs for rows archived before
// a crash or Meili outage, closing the gap left by the runner's bounded
// retries. Best-effort: the next query-time reconciliation pass also covers it.
func (ix *Indexer) ReconcileArchivedDeindex() {
	ids, err := ix.st.ArchivedPendingDeindex()
	if err != nil {
		log.Printf("archive reconcile: %v", err)
		return
	}
	if len(ids) > 0 {
		log.Printf("archive reconcile: %d archived media awaiting de-index", len(ids))
		ix.EnqueueArchiveDeindex(ids)
	}
}
