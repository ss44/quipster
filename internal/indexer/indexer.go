// Package indexer pushes subtitle chunk documents into Meilisearch and keeps
// their indexed state latched in the local store.
package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"quipster/internal/store"
)

// IndexName is the Meilisearch index holding one document per subtitle chunk.
const IndexName = "lines"

// Indexer wraps the Meilisearch client and mediates all index mutations for
// the local SQLite store.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Indexer struct {
	st     *store.Store
	client meili.ServiceManager
	url    string
	key    string
	batch  int
	emb    EmbedderConfig
}

// Option customizes an Indexer at construction time.
type Option func(*Indexer)

// WithEmbedder configures the semantic-search embedder installed on the index
// during EnsureSettings. Passing a zero-valued config keeps semantic search
// disabled and leaves any previously installed embedder untouched.
func WithEmbedder(cfg EmbedderConfig) Option {
	return func(ix *Indexer) { ix.emb = cfg }
}

// New builds an Indexer bound to the given store and Meilisearch endpoint.
func New(st *store.Store, url, masterKey string, opts ...Option) *Indexer {
	ix := &Indexer{
		st:     st,
		client: meili.New(url, meili.WithAPIKey(masterKey)),
		url:    url,
		key:    masterKey,
		batch:  500,
	}
	for _, opt := range opts {
		opt(ix)
	}
	return ix
}

// Client exposes the underlying Meilisearch service manager for readers such
// as the search service.
func (ix *Indexer) Client() meili.ServiceManager { return ix.client }

// EmbedderEnabled reports whether a semantic-search embedder is configured.
func (ix *Indexer) EmbedderEnabled() bool { return ix.emb.Enabled() }

// EmbedderName returns the embedder key queries must reference, or "" when
// semantic search is disabled.
func (ix *Indexer) EmbedderName() string {
	if !ix.emb.Enabled() {
		return ""
	}
	return EmbedderName
}

// SemanticRatio returns the configured keyword/semantic blend for hybrid
// queries, clamped to the 0..1 range Meilisearch accepts.
func (ix *Indexer) SemanticRatio() float64 {
	r := ix.emb.SemanticRatio
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// Healthy reports whether Meilisearch is reachable.
func (ix *Indexer) Healthy() bool {
	_, err := ix.client.Health()
	return err == nil
}

// LineDoc is the Meilisearch document shape for one subtitle chunk.
type LineDoc struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	MediaTitle  string `json:"mediaTitle"`
	ShowTitle   string `json:"showTitle"`
	MediaID     int64  `json:"mediaId"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	Year        int    `json:"year"`
	StartCueIdx int    `json:"startCueIdx"`
	EndCueIdx   int    `json:"endCueIdx"`

	ChunkStartSec float64 `json:"chunkStartSec"`
	ChunkEndSec   float64 `json:"chunkEndSec"`
}

// DocID builds the Meilisearch document id for a chunk. Meili doc ids allow
// alnum, "-" and "_": use "_{mediaId}-{start}-{end}".
func DocID(mediaID int64, startIdx, endIdx int) string {
	return fmt.Sprintf("%d_%d-%d", mediaID, startIdx, endIdx)
}

// EnsureSettings idempotently creates the index and applies MVP settings,
// then installs the configured semantic-search embedder when enabled.
func (ix *Indexer) EnsureSettings() error {
	if _, err := ix.client.GetIndex(IndexName); err != nil {
		task, cerr := ix.client.CreateIndex(&meili.IndexConfig{Uid: IndexName, PrimaryKey: "id"})
		if cerr != nil {
			// index may already exist from a concurrent startup; verify
			if _, gerr := ix.client.GetIndex(IndexName); gerr != nil {
				return fmt.Errorf("create index: %w", cerr)
			}
		} else if res, err := ix.client.WaitForTask(task.TaskUID, 200*time.Millisecond); err != nil {
			return fmt.Errorf("create index wait: %w", err)
		} else if res.Status == meili.TaskStatusFailed {
			return fmt.Errorf("create index task failed: %s", res.Error)
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
	// A settings task can fail asynchronously (e.g. a rejected embedder
	// payload), so the awaited task status must be checked before reporting
	// the index as ready.
	res, err := ix.client.WaitForTask(task.TaskUID, 200*time.Millisecond)
	if err != nil {
		return fmt.Errorf("settings wait: %w", err)
	}
	if res.Status == meili.TaskStatusFailed {
		return fmt.Errorf("settings task failed: %s", res.Error)
	}
	if err := ix.applyTypoTolerance(); err != nil {
		return err
	}
	if ix.emb.Enabled() {
		if err := ix.applyEmbedders(); err != nil {
			return err
		}
	}
	return nil
}

// applyTypoTolerance patches typo settings directly: the Go client always
// serializes newer fields (disableOnNumbers) that Meili 1.11 rejects.
func (ix *Indexer) applyTypoTolerance() error {
	body := []byte(`{"typoTolerance":{"enabled":true,"minWordSizeForTypos":{"oneTypo":6,"twoTypos":10}}}`)
	return ix.patchSettings(body, "typo settings")
}

// applyEmbedders installs the configured embedder under the "default" key so
// Meilisearch generates document vectors during the ingestion phase and can
// serve hybrid/vector queries. Updating the embedder makes Meilisearch
// re-embed every existing document inside the settings task itself.
func (ix *Indexer) applyEmbedders() error {
	body, err := ix.emb.settingsPatch()
	if err != nil {
		return fmt.Errorf("embedder settings: %w", err)
	}
	return ix.patchSettings(body, "embedder settings")
}

// patchSettings sends a raw partial settings update and waits for the
// resulting task, rejecting failed tasks. It bypasses the Go client because
// the client serializes settings fields newer Meilisearch releases reject.
func (ix *Indexer) patchSettings(body []byte, what string) error {
	req, err := http.NewRequest(http.MethodPatch, strings.TrimRight(ix.url, "/")+"/indexes/"+IndexName+"/settings", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if ix.key != "" {
		req.Header.Set("Authorization", "Bearer "+ix.key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("[indexer] close %s response: %v", what, cerr)
		}
	}()
	if resp.StatusCode >= 300 {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if rerr != nil {
			return fmt.Errorf("%s: %s: read body: %w", what, resp.Status, rerr)
		}
		return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(b)))
	}
	// The PATCH enqueues a settings task; wait for it and surface failures so
	// the startup loop keeps retrying until the settings actually apply.
	var enqueued struct {
		TaskUID int64 `json:"taskUid"`
	}
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&enqueued); derr != nil {
		return fmt.Errorf("%s: decode task: %w", what, derr)
	}
	res, err := ix.client.WaitForTask(enqueued.TaskUID, 200*time.Millisecond)
	if err != nil {
		return fmt.Errorf("%s wait: %w", what, err)
	}
	if res.Status == meili.TaskStatusFailed {
		return fmt.Errorf("%s task failed: %s", what, res.Error)
	}
	return nil
}

// IndexMedia replaces all docs of a media and marks its chunks indexed.
func (ix *Indexer) IndexMedia(mediaID int64) error {
	return ix.IndexMediaContext(context.Background(), mediaID)
}

// IndexMediaContext is IndexMedia bound to ctx. Every Meilisearch round-trip
// and task wait uses the job context, so queue pause, shutdown and the runner
// job timeout can interrupt an indexing run instead of leaving a worker
// blocked on a remote task indefinitely.
//
// With a semantic-search embedder configured, Meilisearch generates the
// document vectors during this add phase from the embedder's document
// template, so no local embedding step is required here.
func (ix *Indexer) IndexMediaContext(ctx context.Context, mediaID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	chunkList, err := ix.st.PendingIndexChunks(mediaID, 200000)
	if err != nil {
		return err
	}
	// Docs for a never-indexed media cannot exist; skip the delete round-trip.
	ever, eerr := ix.st.EverIndexed(mediaID)
	if eerr != nil {
		return eerr
	}
	if ever {
		filter := fmt.Sprintf(`mediaId = %d`, mediaID)
		task, derr := ix.client.Index(IndexName).DeleteDocumentsByFilterWithContext(ctx, filter, nil)
		if derr != nil {
			return fmt.Errorf("delete docs %d: %w", mediaID, derr)
		}
		if _, werr := ix.client.WaitForTaskWithContext(ctx, task.TaskUID, 200*time.Millisecond); werr != nil {
			return fmt.Errorf("delete wait %d: %w", mediaID, werr)
		}
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
	tasks, err := ix.client.Index(IndexName).AddDocumentsInBatchesWithContext(ctx, docs, ix.batch, nil)
	if err != nil {
		return fmt.Errorf("add docs %d: %w", mediaID, err)
	}
	for _, t := range tasks {
		res, err := ix.client.WaitForTaskWithContext(ctx, t.TaskUID, 300*time.Millisecond)
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
		if _, err := ix.client.WaitForTask(task.TaskUID, 300*time.Millisecond); err != nil {
			log.Printf("[indexer] wait delete media %d: %v", id, err)
		}
	}
	return nil
}

// JobHandler runs index jobs. It honors the runner context so every remote
// task wait can be canceled by a queue pause, shutdown or the job timeout.
func (ix *Indexer) JobHandler() func(ctx context.Context, j *store.Job, progress func(float64)) error {
	return func(ctx context.Context, j *store.Job, _ func(float64)) error {
		var payload struct {
			MediaID int64 `json:"mediaId"`
		}
		if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		started := time.Now()
		log.Printf("[indexer] media %d job %d: start", payload.MediaID, j.ID)
		if err := ix.IndexMediaContext(ctx, payload.MediaID); err != nil {
			log.Printf("[indexer] media %d job %d: failed elapsed=%s error=%q",
				payload.MediaID, j.ID, time.Since(started).Round(time.Millisecond), err)
			return err
		}
		if err := ix.st.MarkIndexed(payload.MediaID); err != nil {
			log.Printf("[indexer] media %d job %d: mark indexed elapsed=%s error=%q",
				payload.MediaID, j.ID, time.Since(started).Round(time.Millisecond), err)
			return err
		}
		log.Printf("[indexer] media %d job %d: indexed elapsed=%s",
			payload.MediaID, j.ID, time.Since(started).Round(time.Millisecond))
		return nil
	}
}

// ReindexAll clears index flags and enqueues index jobs for every media with chunks.
func (ix *Indexer) ReindexAll() (int, error) {
	// Mark every chunk stale first: the id list is derived from that flag, so
	// listing before clearing would skip already-indexed media.
	if err := ix.st.EnsureAllChunksStale(); err != nil {
		return 0, err
	}
	ids, err := ix.st.MediaIDsNeedingIndex(1000000)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		payload := fmt.Sprintf(`{"mediaId":%d}`, id)
		if _, err := ix.st.EnqueueJob("index", payload, 2); err == nil {
			n++
		}
	}
	return n, nil
}
