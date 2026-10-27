package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/store"
)

const IndexName = "lines"

type Indexer struct {
	st     *store.Store
	client meili.ServiceManager
	batch  int
}

func New(st *store.Store, url, masterKey string) *Indexer {
	return &Indexer{
		st:     st,
		client: meili.New(url, meili.WithAPIKey(masterKey)),
		batch:  500,
	}
}

func (ix *Indexer) Client() meili.ServiceManager { return ix.client }

// Healthy reports whether Meilisearch is reachable.
func (ix *Indexer) Healthy() bool {
	_, err := ix.client.Health()
	return err == nil
}

type LineDoc struct {
	ID            string  `json:"id"`
	Text          string  `json:"text"`
	MediaID       int64   `json:"mediaId"`
	MediaTitle    string  `json:"mediaTitle"`
	ShowTitle     string  `json:"showTitle"`
	Season        int     `json:"season"`
	Episode       int     `json:"episode"`
	Year          int     `json:"year"`
	StartCueIdx   int     `json:"startCueIdx"`
	EndCueIdx     int     `json:"endCueIdx"`
	ChunkStartSec float64 `json:"chunkStartSec"`
	ChunkEndSec   float64 `json:"chunkEndSec"`
}

func DocID(mediaID int64, startIdx, endIdx int) string {
	// Meili doc ids allow alnum, "-", "_": use "_{mediaId}-{start}-{end}".
	return fmt.Sprintf("%d_%d-%d", mediaID, startIdx, endIdx)
}

// EnsureSettings idempotently creates the index and applies MVP settings.
func (ix *Indexer) EnsureSettings() error {
	if _, err := ix.client.GetIndex(IndexName); err != nil {
		task, cerr := ix.client.CreateIndex(&meili.IndexConfig{Uid: IndexName, PrimaryKey: "id"})
		if cerr != nil {
			// index may already exist from a concurrent startup; verify
			if _, gerr := ix.client.GetIndex(IndexName); gerr != nil {
				return fmt.Errorf("create index: %w", cerr)
			}
		} else if _, err := ix.client.WaitForTask(task.TaskUID, 200*time.Millisecond); err != nil {
			return fmt.Errorf("create index wait: %w", err)
		}
	}
	settings := meili.Settings{
		SearchableAttributes: []string{"text"},
		FilterableAttributes: []string{"mediaId", "showTitle", "year"},
		SortableAttributes:   []string{"chunkStartSec"},
		DisplayedAttributes:  []string{"id", "text", "mediaId", "mediaTitle", "showTitle", "season", "episode", "year", "chunkStartSec", "chunkEndSec"},
	}
	task, err := ix.client.Index(IndexName).UpdateSettings(&settings)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if _, err := ix.client.WaitForTask(task.TaskUID, 200*time.Millisecond); err != nil {
		return fmt.Errorf("settings wait: %w", err)
	}
	return nil
}

// IndexMedia replaces all docs of a media and marks its chunks indexed.
func (ix *Indexer) IndexMedia(mediaID int64) error {
	chunkList, err := ix.st.PendingIndexChunks(mediaID, 200000)
	if err != nil {
		return err
	}
	// Docs for a never-indexed media cannot exist; skip the delete round-trip.
	if ever, err := ix.st.EverIndexed(mediaID); err == nil && ever {
		filter := fmt.Sprintf(`mediaId = %d`, mediaID)
		task, err := ix.client.Index(IndexName).DeleteDocumentsByFilter(filter, nil)
		if err != nil {
			return fmt.Errorf("delete docs %d: %w", mediaID, err)
		}
		if _, err := ix.client.WaitForTask(task.TaskUID, 200*time.Millisecond); err != nil {
			return fmt.Errorf("delete wait %d: %w", mediaID, err)
		}
	} else if err != nil {
		return err
	}
	if len(chunkList) == 0 {
		return nil
	}
	docs := make([]LineDoc, len(chunkList))
	ids := make([]int64, len(chunkList))
	for i, ch := range chunkList {
		docs[i] = LineDoc{
			ID:            DocID(ch.MediaFileID, ch.StartCueIdx, ch.EndCueIdx),
			Text:          ch.Text,
			MediaID:       ch.MediaFileID,
			MediaTitle:    displayTitle(ch.MediaTitle, ch.ShowTitle, ch.Season, ch.Episode, ch.Year),
			ShowTitle:     ch.ShowTitle,
			Season:        ch.Season,
			Episode:       ch.Episode,
			Year:          ch.Year,
			StartCueIdx:   ch.StartCueIdx,
			EndCueIdx:     ch.EndCueIdx,
			ChunkStartSec: ch.StartSec,
			ChunkEndSec:   ch.EndSec,
		}
		ids[i] = ch.ID
	}
	tasks, err := ix.client.Index(IndexName).AddDocumentsInBatches(docs, ix.batch, nil)
	if err != nil {
		return fmt.Errorf("add docs %d: %w", mediaID, err)
	}
	for _, t := range tasks {
		res, err := ix.client.WaitForTask(t.TaskUID, 300*time.Millisecond)
		if err != nil {
			return fmt.Errorf("add wait %d: %w", mediaID, err)
		}
		if res.Status == meili.TaskStatusFailed {
			return fmt.Errorf("add failed %d: %s", mediaID, res.Error)
		}
	}
	if err := ix.st.MarkChunksIndexed(ids, time.Now().Unix()); err != nil {
		return err
	}
	return nil
}

func displayTitle(title, showTitle string, season, episode, year int) string {
	if showTitle != "" {
		base := showTitle
		if season > 0 && episode > 0 {
			base = fmt.Sprintf("%s S%02dE%02d", showTitle, season, episode)
		}
		if year > 0 {
			base += fmt.Sprintf(" (%d)", year)
		}
		return base
	}
	if title == "" {
		return "(unknown)"
	}
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// DeleteMedia removes a media's docs (best-effort; stale docs are skipped at search time).
func (ix *Indexer) DeleteMedia(ids []int64) error {
	for _, id := range ids {
		filter := fmt.Sprintf(`mediaId = %d`, id)
		task, err := ix.client.Index(IndexName).DeleteDocumentsByFilter(filter, nil)
		if err != nil {
			log.Printf("[indexer] delete docs media %d: %v", id, err)
			continue
		}
		ix.client.WaitForTask(task.TaskUID, 300*time.Millisecond)
	}
	return nil
}

// JobHandler runs index jobs.
func (ix *Indexer) JobHandler() func(ctx context.Context, j *store.Job, progress func(float64)) error {
	return func(ctx context.Context, j *store.Job, progress func(float64)) error {
		var payload struct {
			MediaID int64 `json:"mediaId"`
		}
		if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		if err := ix.IndexMedia(payload.MediaID); err != nil {
			return err
		}
		return ix.st.MarkIndexed(payload.MediaID)
	}
}

// ReindexAll clears index flags and enqueues index jobs for every media with chunks.
func (ix *Indexer) ReindexAll() (int, error) {
	ids, err := ix.st.MediaIDsNeedingIndex(1000000)
	if err != nil {
		return 0, err
	}
	if err := ix.st.EnsureAllChunksStale(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		payload, _ := json.Marshal(map[string]any{"mediaId": id})
		if _, err := ix.st.EnqueueJob("index", string(payload), 2); err == nil {
			n++
		}
	}
	return n, nil
}
