# Video archive lifecycle: soft delete, lazy de-indexing, and seamless re-activation

Quipster originally hard-deleted a media row (and, through foreign-key cascades, its subtitle tracks, cues, and chunks) the moment a completed library scan no longer saw the file on disk, deleting the MeiliSearch documents in the same breath. On a network share that disappears — a NAS reboot, a dropped NFS mount, a pulled USB disk — a single scan during the outage destroyed metadata for the entire library, and every restored file then had to be re-probed, re-extracted, re-parsed, and re-indexed: weeks of work on a large library.

This design replaces that cliff with an archive lifecycle. The invariants are:

- **No permanent deletion.** Scan pruning never removes `media_files` rows; subtitle tracks, cues, and chunks survive with the row. A removed file's entire caption corpus stays in SQLite and re-activates without re-fetching or re-processing caption data.
- **Lazy by construction.** Archive decisions are made across consecutive scans with a grace window, and the expensive work (MeiliSearch de-indexing) drains through the existing job queue at the lowest priority, only when nothing else is queued. Nothing in the scan hot path talks to MeiliSearch.
- **The archived flag is the source of truth.** MeiliSearch is eventually consistent; read paths gate on the SQLite flag first, so a slow or failing de-index can never leak archived content into search results or clips.
- **Restore is O(1) on the data path.** Re-activation reuses retained cues, chunks, and probe metadata; only the MeiliSearch document re-upload is repeated, and only when documents were actually deleted.

## Lifecycle state machine

All state lives on `media_files`. `status` keeps its existing enum and is **frozen while archived**: archive and restore never rewrite it, and every pipeline entry point that could mutate it (probe, index, node result apply) no-ops for archived rows.

| State | Columns | Served to users? | Notes |
|---|---|---|---|
| Active | `archived=0, missing_at=0` | yes | Normal operation. |
| Missing | `archived=0, missing_at>0` | yes | Observed absent by a completed scan; still fully served (transient unmount, NFS blip, rename-then-return). |
| Archived, de-index pending | `archived=1, deindexed=0` | no | Flag set; `archive_deindex` job queued/running; Meili docs may still exist. |
| Archived, de-indexed | `archived=1, deindexed=1` | no | Meili docs confirmed removed. Captions intact in SQLite. |

Transitions:

- Active → Missing: a completed scan (walk had zero errors) does not see the path. Batched `UPDATE` latch only.
- Missing → Active: any later scan sees the path again. `missing_at` cleared; nothing else happens.
- Missing → Archived: a later scan still does not see the path and the grace window has elapsed since `missing_at`. Batched flag update + one unique `archive_deindex` job per media at priority `-1`.
- Archived → Active (restore): the path reappears in a scan (or an admin calls the restore endpoint). If `deindexed=1`, chunk `indexed_at` flags are cleared so the normal `index` job re-adds documents; if `deindexed=0`, the Meili documents were never deleted and the index is left untouched. `deindexed` resets to 0.
- Archived stays Archived: scans that still don't see the path are no-ops for already-archived rows; no repeated jobs, no repeated Meili calls.

## Database schema

The `status` CHECK constraint is baked into existing databases; `CREATE TABLE IF NOT EXISTS` will not update it, and a table rebuild would force rewriting every status-aware query. Archival is therefore modeled as an **orthogonal flag set on `media_files`**, added through the established `pragma_table_info` + `ALTER TABLE` migration list in `internal/store/store.go` (instant on SQLite for defaulted columns, no data rewrite):

```sql
ALTER TABLE media_files ADD COLUMN archived   INTEGER NOT NULL DEFAULT 0; -- 1 = hidden from all serving
ALTER TABLE media_files ADD COLUMN archived_at INTEGER NOT NULL DEFAULT 0; -- unix ms of archive decision
ALTER TABLE media_files ADD COLUMN missing_at INTEGER NOT NULL DEFAULT 0;  -- unix ms first observed absent
ALTER TABLE media_files ADD COLUMN deindexed  INTEGER NOT NULL DEFAULT 0;  -- 1 = Meili docs confirmed removed

CREATE INDEX IF NOT EXISTS idx_media_archived ON media_files(archived, deindexed, missing_at);
```

The index is created after the `ALTER` pass (it references columns that legacy databases only gain there) and serves the three hot lookups: scan archive candidates (`archived=0 AND missing_at>0`), the de-index sweeper and boot reconciliation (`archived=1 AND deindexed=0`), and admin archive listing.

Why a flag and not a new `status` value:

- The flag composes with every existing status (`indexed`, `no_subs`, `error`, `pending`), so restore needs no status reconstruction and no data migration.
- Because `status` is never rewritten while archived, a restored row resumes its exact prior lifecycle: `indexed` rows stay searchable, `pending` rows resume probing through the normal recovery path, and `error` rows remain for operator attention.

`MediaFile` gains `Archived`/`ArchivedAt` (exposed so the admin UI can badge rows), `MissingAt`/`Deindexed` (internal latches, `json:"-"`), and `mediaCols` loads them in every query that already loads the row, so no hot path gains a round trip.

### Store API (as implemented)

```go
// ArchiveMissingUnder implements scan pruning without deletion. Rows under
// base absent from keep latch missing_at on first observation, stay untouched
// inside the grace window, and archive past it. Archived rows are skipped.
func (s *Store) ArchiveMissingUnder(base string, keep map[string]bool, grace time.Duration, now time.Time) (archived []int64, newlyMissing int, err error)

// RestoreSeenUnder re-activates rows for paths the scan saw again. Returns the
// ids whose deleted documents need an index job, and how many rows restored.
func (s *Store) RestoreSeenUnder(base string, keep map[string]bool) (needsReindex []int64, restored int, err error)

// RestoreMedia re-activates one row (admin action).
func (s *Store) RestoreMedia(id int64) (needsReindex, restored bool, err error)

// MarkMediaDeindexed latches deindexed=1, guarded by archived=1 so a restore
// racing the de-index job cannot latch a stale flag.
func (s *Store) MarkMediaDeindexed(id int64) error

// ArchivedPendingDeindex lists archived rows still awaiting de-indexing.
func (s *Store) ArchivedPendingDeindex() ([]int64, error)
```

All multi-row writes run in bounded batches (400 ids per statement, `updateIDsBatched`/`clearChunkFlagsBatched`) to stay inside `SQLITE_MAX_VARIABLE_NUMBER` and collapse a 50k-file prune into a handful of transactions. `ListMedia` gains an `archived` filter (`""`/`"0"` active only, `"1"` archive only, `"any"` both).

## Scan-driven archival workflow (lazy, across subsequent scans)

Changes are confined to `internal/scan/scan.go` and the store; the walk itself is untouched, so per-scan cost stays O(tree) with the same stat profile.

1. **Walk** (`walkAll`): unchanged. The `keep` set records every path the walk observed.
2. **Restore pass** (`restoreSeen` → `Store.RestoreSeenUnder`): before any archive decision, every seen path with a latch or archive flag is re-activated. This runs even after a partial walk — a seen path was genuinely seen — and is what makes a returning share recover with zero work. Restored ids whose documents had been deleted are enqueued as `index` jobs at standard priority (1) in batches (`enqueueIndexJobs`).
3. **Archive pass** (`archiveOrLog` → `Store.ArchiveMissingUnder`): skipped entirely when `WalkErrors > 0` (a partial tree is not evidence that unvisited files disappeared), and when archiving is disabled. Otherwise rows absent from `keep` are partitioned by the grace window; archived ids are handed to `onArchived`, which only enqueues unique `archive_deindex` jobs (priority `-1`). The scan never touches MeiliSearch.

Why the grace window exists: it is what keeps a transient outage from churning the index. A share that flaps for one scan cycle is latched, then cleared on the next successful walk with no archive transition at all. Only a file that stays absent across the window reaches the archive state, and the decision itself costs two tiny batched UPDATEs. Note the floor: `ARCHIVE_GRACE=0` still archives on the *second* consecutive observation, never the first — one bad scan can never archive anything.

Configuration (`cmd/quipster/main.go`, `loadConfig`):

- `ARCHIVE_GRACE` (duration, default `48h`): minimum absence before archiving.
- `ARCHIVE_ENABLED` (default `true`): set to `0` to skip the prune pass entirely — no latch, no archive, and never a deletion. The system has no code path that deletes media rows or caption data.

## De-indexing workflow (background, lowest priority)

New job type `archive_deindex`, payload `{"mediaId":N}`, priority `-1`, registered with a single dedicated worker (`registerJobHandlers`, `cmd/quipster/main.go`). Because `ClaimJob` orders by `priority DESC` with no floor, priority `-1` jobs are claimed only when no scan/probe/index/clip/reindex job is queued — de-indexing trickles strictly behind user-visible work. Deduplication rides on `EnqueueUniqueJob`'s `status IN ('queued','running')` check, so a done/errored job never blocks a legitimate re-archive.

Handler steps (`Indexer.ArchiveJobHandler`, each idempotent):

1. Load the media row. `archived=0` → finish done (lazy cancellation: a restore beat the job).
2. `archived=1 AND deindexed=1` → finish done (already de-indexed).
3. `DeleteDocumentsByFilter("mediaId = N")` + `WaitForTask`. Unlike the legacy best-effort `DeleteMedia`, **errors propagate**: the runner retries, the failure is visible in the admin job list, and reconciliation re-enqueues later.
4. Re-read the row after the delete lands. If `archived=0` (restored mid-flight) → clear the chunk index flags and enqueue an `index` job (priority 1) so the just-deleted documents are rebuilt; finish done. Worst case is one redundant reindex; content is never lost.
5. Still archived → `MarkMediaDeindexed(id)`.

Three reconciliation layers guarantee convergence:

- **Boot reconciliation** (`reconcileArchivedDeindex` in `main`): enqueues unique jobs for `ArchivedPendingDeindex()` after startup, healing crashes, Meili outages, and pre-upgrade rows.
- **Query-time reconciliation** (`search.Service.requeueArchiveDeindex`): `appendHits` resolves every hit back to SQLite anyway; a hit whose media row is archived is dropped *and* re-enqueued as an `archive_deindex` job. Search traffic itself repairs missed deletions, bounded by job dedupe.
- **Runner retries** on transient Meili failures.

## Read-path gating

All gates are on `archived` and applied at store choke points, so every caller (HTTP API, MCP tools, admin endpoints, job handlers) is covered once:

| Query / path | Change | Why |
|---|---|---|
| `search.go appendHits` | skip hits whose media is archived; enqueue reconciliation job | Primary gate; correct even when Meili docs lag. |
| `GetChunkByKey`, `GetChunkByID`, `ChunkAtTime` | join filter `AND m.archived=0` | Single choke point for `/api/chunks/{id}`, MCP `get_scene_by_line`, quote deep-links. Stale-doc tolerance then treats archived hits as missing. |
| `RandomQuotes`, `PopularExamples`, `TitleMatches` | `AND m.archived=0` | Landing page, examples, and the no-subtitle fallback must not surface archived rows whose frozen `status` is still `indexed`. |
| `ListMedia` | default `archived=0`; `archived=1` / `archived=any` for the admin archive browser | Operators can still see what is archived. |
| `MediaIDsByStatus` | `AND archived=0 AND missing_at=0` | `/api/admin/reprobe` and scan probe recovery must not re-probe archived rows or files known to be missing during grace. |
| `MediaIDsNeedingIndex`, `PendingIndexChunks` | `AND m.archived=0` | `ReindexAll` and index jobs never touch archived rows; a stale index job cannot re-add hidden documents. |
| `clips.Validate` | returns `ErrMediaArchived` → HTTP 404 | Archived media is not renderable; the async clip job handler no-ops (finish done) for rows archived between request and render. |
| `probe.JobHandler`, `nodes.ApplyProbeResult`, `indexer.JobHandler` | archived → finish done with no writes | Keeps the frozen row's `status` frozen even when in-flight local or remote work races the archive. |
| `GET /api/thumb/{mediaId}` | no change | Cached thumbs of archived rows keep serving (useful in the archive browser); regeneration is impossible until the file returns. |

`MediaStatusCounts` additionally reports the archived count under key `archived`, which flows through the existing SSE collector into the admin dashboard.

## Re-activation pipeline

Restore has two triggers sharing one code path (`restoreMediaIDs`):

1. **Scan-driven (primary):** the file reappears under a known library root; the next completed scan's restore pass re-activates it. Lazy, consistent with the rest of the design.
2. **Admin:** `POST /api/admin/media/{id}/restore` for "bring it back now". Restoring a row whose file is still absent is allowed and safe: search re-activates from stored captions immediately, clip requests 404 until the file exists, and if the file never returns the next scan re-latches `missing_at` and the row re-archives after grace — a stable loop with no special cases.

What restore does and does not do:

- Clears `archived/archived_at/missing_at`, resets `deindexed=0`.
- **Always** clears the media's chunk `indexed_at` flags and enqueues an `index` job (priority 1), regardless of `deindexed`. `IndexMedia` re-adds every document from `chunks` + `media_files`; with a semantic embedder, vectors are regenerated server-side by Meili from the document template, so re-activation costs one Meili upload — no ffprobe, no subtitle extraction, no cue re-parsing, no chunk rebuild. Doing this unconditionally is deliberate: a de-index job can delete the Meili documents and then crash before latching `deindexed`, and a trusted-flag restore would leave that row permanently dark in search (restored rows have no boot reconciliation pass). The cost is at most one redundant reindex per restore.
- Does **not** enqueue a probe while cues exist. Probe metadata, subtitle tracks, cues, and chunks are intact. The single exception: a row archived while `status='pending'` restores with `status='pending'`, and the ordinary probe-recovery path probes it once the file is back — correct, because there was no caption data to preserve.

The `ever_indexed` latch is never cleared by archive/restore; it continues to mean "docs may exist", and `deindexed` refines that during archive windows.

## Storage management edge cases

- **No disk relocation.** The archive lives in SQLite; the video bytes are already gone (operator removal is the trigger). Moving files into an archive bin would change paths, break scan identity (path is the unique upsert key), and re-introduce the delete-and-reprobe churn this design removes.
- **DB growth is bounded and small.** Cues/chunks text dominates: at ~280 chars per chunk and ~3 cues per chunk, a dialogue-heavy 45-minute film costs roughly 150-200 KB. Ten thousand archived films stay in the low single-digit GB of SQLite — versus the video bytes already reclaimed. Nothing auto-purges (the invariant forbids it); growth is observable via the `archived` status count.
- **Cache directory.** Extracted subtitles (`cache/subs/{mediaID}.srt`), thumbnails, and rendered clips stay LRU-managed. Eviction of an archived row's cached extraction is harmless — SQLite cues are canonical and the extraction is only consulted during probes, which restore bypasses. A thumbnail evicted while archived yields the existing not-found response until the file returns.
- **Dangling sidecar references.** `subtitle_tracks.extracted_path` may point at a removed sidecar while archived; it is only read during a probe of the same path, so restore never dereferences it.
- **Permanent renames.** A move to a new path is seen as missing(old) + new(new row). After grace the old row archives with its captions preserved; the new path is processed normally. Future enhancement (schema already supports it): content fingerprinting could let a new path adopt an archived row's captions instead of re-probing.

## Search engine sync edge cases

- **Ordering:** the `archived` flag commits before any Meili deletion is requested, and every read path gates on the flag. A de-index that is slow, failing, or never runs cannot leak archived content; it can only leave unused documents, cleaned by reconciliation.
- **Meili down:** the job retries, then errors visibly; boot and query-time reconciliation re-enqueue unique jobs until the delete lands. The scan never fails because of Meili.
- **Meili wiped or recreated:** `deindexed=1` rows believe their docs are gone (true); `deindexed=0` archived rows still get a delete-by-filter (a no-op on an empty index). Consistent either way.
- **Restore/de-index races:** covered by the handler's post-delete re-check and by restore always re-enqueueing the index job. The flag-then-delete ordering means the failure mode is always "too many documents, re-added later", never "missing content". The one benign redundancy: a restore racing a de-index can double-book one reindex (the handler's post-delete re-check and the restore's unconditional enqueue both fire); dedup collapses them when both are queued, and the cost is otherwise one extra reindex pass.
- **Idempotence:** delete-by-filter is idempotent; archive/restore loops re-enqueue cleanly; repeated reconcile passes are no-ops once converged.
- **Hybrid/semantic modes:** same index, same document shape; no embedder-side work is added on either transition.

## Performance implications

- **Scan path:** adds at most three batched UPDATEs proportional to the number of missing/returned files plus one extra SELECT over the walked base (the same shape the old prune already ran). A 2,000-file removal completes in a handful of transactions; unchanged files cost nothing beyond that SELECT.
- **De-index trickle:** priority `-1` + single worker means the archive backlog drains only when the queue is otherwise empty. Each job is ~2 Meili requests (submit + wait at the existing 300 ms poll); a 10k-file backlog drains in a few hours of idle trickle, invisible to probe/index latency.
- **Restore burst (remounted drive):** produces normal-priority `index` jobs, identical in shape to a routine library index; `EnqueueUniqueJobs` dedupes overlapping scans. A restore that races a still-pending de-index can cost one redundant full reindex per raced row — bounded, and cheap next to the two-week re-probe the design eliminates.
- **Read paths:** gating reuses rows already loaded; the extra integer columns in `mediaCols` are noise. Query-time reconciliation is one deduped INSERT per affected hit, amortized to zero once converged.
- **Write pressure vs. before:** archival replaces cascade DELETEs of the largest tables (cues/chunks) with small flag UPDATEs — strictly less WAL and checkpoint work than the old prune.
- **`jobs` table growth:** one row per archived media, consistent with existing per-media probe/index job retention.

## API and configuration surface

| Surface | Change |
|---|---|
| `POST /api/admin/media/{id}/restore` | re-activates a row; response `{restored, reindexQueued, warning}` (warning names a de-index still in flight, if any); unknown id 404, active row a no-op |
| `GET /api/media?archived=1\|any` | archived slice for the admin archive browser; default excludes archived |
| `GET /api/media/{id}` | still resolves archived rows (with `archived: true`) so the admin can inspect and restore |
| `GET /api/chunks/{id}`, `GET /api/clips/stream` | archived media 404 (clip rejects as absent, not a bad request) |
| `GET /api/admin/status` + SSE | gains `archived` count via `MediaStatusCounts` |
| `ARCHIVE_ENABLED`, `ARCHIVE_GRACE` | env knobs via `loadConfig` |

## Rollout and testing

1. **Migration:** the four columns land via the existing `migrations` list; the index is created after the ALTER pass. Nothing is archived on deploy — the first completed scan only latches absences, and archiving begins one grace period later.
2. **Tests** (all green at ≥85% statement coverage, enforced by `.githooks/pre-commit`):
   - `internal/store/archive_test.go`: grace partitioning, caption preservation, restore in both `deindexed` states, admin restore, latch guards, boot list, read-path exclusions (`ListMedia`/`TitleMatches`/`MediaIDsByStatus`/`MediaIDsNeedingIndex`/`PendingIndexChunks`/`RandomQuotes`/`PopularExamples`/chunk resolvers).
   - `internal/scan/archive_test.go`: outage-within-grace costs nothing, full latch→archive→restore lifecycle, restore always re-enqueues the index job, disabled pass, partial walk blocks archiving but still restores seen paths.
   - `internal/indexer/archive_test.go`: de-index success/latch, lazy cancellation, error propagation (HTTP, task wait, failed task), restore race re-index, index-job archived guard.
   - `internal/search/archive_test.go`: archived hits dropped + reconciliation enqueued; title fallback excludes archived.
   - `internal/api/archive_test.go`: restore endpoint, media list filter, chunk/clip 404s, admin-visible archived flag.
   - `internal/clips/archive_test.go`, `internal/probe/archive_test.go`, `internal/nodes/archive_test.go`: archived guards on render, probe, and node-result apply.
3. **Manual QA:** remove a file → verify latch only and unchanged search; force grace expiry → verify docs gone, search/clip empty, cues intact; return the file → verify restore with zero probe/extraction work (no `probe` job enqueued, `extracted_path` untouched) and search live again; simulate a full-mount outage → verify a failed root walk archives nothing and a returning mount restores nothing-deleted.

## Non-goals and future work

- **Explicit archiving of files that exist on disk** (hide-but-keep): v2; needs an archive-origin column so scans do not fight admin intent.
- **Content-fingerprint re-link on permanent renames**: adopt an archived row's captions for a new path without re-probing.
- **Permanent purge endpoint**: deliberately omitted — the system never destroys caption data; out-of-band DB maintenance remains the only path to true deletion.
- **Cold-storage compression of archived cue text**: unnecessary at projected volumes; revisit if archive growth becomes measurable.
