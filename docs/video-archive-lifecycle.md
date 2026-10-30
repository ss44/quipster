# Video archive lifecycle: soft delete, lazy de-indexing, and seamless re-activation

Quipster currently hard-deletes a media row (and, through foreign-key cascades, its subtitle tracks, cues, and chunks) the moment a completed library scan no longer sees the file on disk (`internal/store/media.go:266`, `internal/scan/scan.go:211`). The MeiliSearch documents are deleted in the same breath via the `onDeleted` callback (`cmd/quipster/main.go:161`). If the file later returns, everything is re-probed, re-extracted, re-parsed, and re-indexed from scratch.

This design replaces that cliff with an archive lifecycle. The invariants are:

- **No permanent deletion.** Scan pruning never removes `media_files` rows, so subtitle tracks, cues, and chunks survive. A removed file's entire caption corpus stays queryable in SQLite and can be re-activated with zero re-fetching or re-processing of caption data.
- **Lazy by construction.** Archiving is decided across consecutive scans with a grace window, and the expensive work (MeiliSearch de-indexing) drains through the existing job queue at the lowest priority, only when nothing else is queued. Nothing in the scan hot path talks to MeiliSearch.
- **The archived flag is the source of truth.** MeiliSearch is eventually consistent; read paths gate on the SQLite flag first, so a slow or failing de-index can never leak archived content into search results or clips.
- **Restore is O(1) on the data path.** Re-activation reuses the retained cues, chunks, and probe metadata; only the MeiliSearch document re-upload (cheap, server-side embedding) is repeated, and only when the docs were actually deleted.

## Lifecycle state machine

All state lives on `media_files`. `status` keeps its existing enum and is **frozen while archived**: archive and restore never rewrite `status`, and every pipeline entry point that could mutate it (probe, index) no-ops for archived rows.

| State | Columns | Served to users? | Notes |
|---|---|---|---|
| Active | `archived=0, missing_at=0` | yes | Normal operation. |
| Missing | `archived=0, missing_at>0` | yes | Observed absent by a completed scan; still fully served (transient unmount, NFS blip, rename-then-return). |
| Archived, de-index pending | `archived=1, deindexed=0` | no | Flag set; `archive_deindex` job queued/running; Meili docs may still exist. |
| Archived, de-indexed | `archived=1, deindexed=1` | no | Meili docs confirmed removed. Captions intact in SQLite. |

Transitions:

- Active → Missing: a completed scan (walk had zero errors) does not see the path. Cheap batched `UPDATE`, no other work.
- Missing → Active: any later scan sees the path again. `missing_at` cleared, nothing else happens.
- Missing → Archived: a later scan still does not see the path and the grace window has elapsed since `missing_at`. Batched flag update + enqueue one unique `archive_deindex` job per media at priority `-1`.
- Archived → Active (restore): the path reappears in a scan (or an admin calls the restore endpoint). If `deindexed=1`, chunk `indexed_at` flags are cleared so the normal `index` job re-adds docs; if `deindexed=0`, the Meili docs were never deleted and the index is left untouched. `deindexed` resets to 0.
- Archived stays Archived: scans that still don't see the path are no-ops for already-archived rows; no repeated jobs, no repeated Meili calls.

## Database schema adjustments

The `status` CHECK constraint (`status IN ('pending','indexed','no_subs','error')`) is baked into existing databases; `CREATE TABLE IF NOT EXISTS` will not update it, and a table rebuild would force rewriting every status-aware query. Archival is therefore modeled as an **orthogonal flag set on `media_files`**, added through the established `pragma_table_info` + `ALTER TABLE` migration list (`internal/store/store.go:201-218`), which is instant on SQLite for defaulted columns and requires no data rewrite:

```sql
ALTER TABLE media_files ADD COLUMN archived   INTEGER NOT NULL DEFAULT 0; -- 1 = hidden from all serving
ALTER TABLE media_files ADD COLUMN archived_at INTEGER NOT NULL DEFAULT 0; -- unix ms of archive decision
ALTER TABLE media_files ADD COLUMN missing_at INTEGER NOT NULL DEFAULT 0;  -- unix ms first observed absent
ALTER TABLE media_files ADD COLUMN deindexed  INTEGER NOT NULL DEFAULT 0;  -- 1 = Meili docs confirmed removed

CREATE INDEX IF NOT EXISTS idx_media_archived ON media_files(archived, deindexed, missing_at);
```

Rationale per column:

- `archived` composes with every existing `status` value (`indexed`/`no_subs`/`error`/`pending` all freeze as-is), so restore needs no status reconstruction and no new enum value.
- `missing_at` implements the multi-scan grace window: the *first* scan that misses a file only latches a timestamp; a later scan archives. It is cleared whenever the path is seen again. Refreshing it is deliberately avoided so the grace window is deterministic from first observation.
- `deindexed` is the Meili-mirror latch. It makes restore exact: the row knows whether its documents were actually removed, so a restore that races ahead of the de-index job keeps the still-valid documents and pays no re-upload. It also powers boot-time reconciliation (`WHERE archived=1 AND deindexed=0`).
- `idx_media_archived` serves the three hot lookups: scan archive candidates (`archived=0 AND missing_at>0`), the de-index sweeper and boot reconciliation (`archived=1 AND deindexed=0`), and admin archive listing.

The `MediaFile` struct (`internal/store/media.go:10`) and `mediaCols` gain `Archived bool`, `ArchivedAt int64`, `MissingAt int64`, and `Deindexed bool` (the latter two with `json:"-"`; `Archived` is exposed so the admin UI can badge rows). The extra columns ride along in queries that already load the row, so no hot path gains a round trip.

### New store methods

Following the transactional patterns in `internal/store/subs.go` and the batch style of `DeleteMediaIDs` (`internal/store/media.go:298`, IN-chunks of 400 to stay under `SQLITE_MAX_VARIABLE_NUMBER`):

```go
// ArchiveMissingUnder replaces DeleteMissingUnder's hard delete. It returns the
// ids archived in this pass and the count of rows that merely latched missing_at.
func (s *Store) ArchiveMissingUnder(base string, keep map[string]bool, grace time.Duration, now int64) (archived []int64, newlyMissing int, err error)

// RestoreSeenMedia is called by the scan for paths it saw again. It clears
// missing_at everywhere and fully restores archived rows, returning the subset
// that needs index jobs re-enqueued (deindexed was 1).
func (s *Store) RestoreSeenMedia(ids []int64) (needsReindex []int64, err error)

// RestoreMedia is the admin-facing single restore; identical core to RestoreSeenMedia.
func (s *Store) RestoreMedia(id int64) (needsReindex bool, err error)

// MarkMediaDeindexed latches deindexed=1, guarded by archived=1 so a restore
// racing the de-index job cannot latch a stale flag.
func (s *Store) MarkMediaDeindexed(id int64) error

// ArchivedPendingDeindex lists archived rows still awaiting de-indexing; used
// for boot-time reconciliation.
func (s *Store) ArchivedPendingDeindex() ([]int64, error)
```

`ArchiveMissingUnder` partitions rows under `base` not in `keep` into three sets and applies bounded batched writes:

1. `missing_at=0 AND archived=0` → `UPDATE ... SET missing_at=? WHERE id IN (...)` (latch only).
2. `missing_at>0 AND now-missing_at < grace` → untouched; the window keeps running from first observation.
3. `missing_at>0 AND elapsed >= grace AND archived=0` → `UPDATE ... SET archived=1, archived_at=?, deindexed=0 WHERE id IN (...)`. `status` and all caption data are untouched.
4. `archived=1` → skipped entirely (already archived; no repeat work).

## Scan-driven archival workflow (lazy, across subsequent scans)

Changes are confined to `internal/scan/scan.go` and the store; the walk itself is untouched, so per-scan cost stays O(tree) with the same stat profile.

- `deleteMissing` (`scan.go:224`) calls `ArchiveMissingUnder` instead of `DeleteMissingUnder`. The existing safety guard stands: `pruneOrLog` (`scan.go:211`) skips all pruning when the walk had errors, because a partial tree is not evidence that unvisited files disappeared.
- After `UpsertMediaBatch` resolves ids for the paths it saw, the scan calls `RestoreSeenMedia(seenIDs)`. The store side first runs `SELECT id FROM media_files WHERE id IN (...) AND (archived=1 OR missing_at!=0)` so unchanged, never-missing files (the overwhelming majority) cost nothing, then applies the batched clears/restores. Returned `needsReindex` ids are enqueued as `index` jobs at normal priority.
- The `onDeleted` callback is replaced by `onArchived func(ids []int64)` (`cmd/quipster/main.go:161`), which does nothing but enqueue unique `archive_deindex` jobs (priority `-1`) via `EnqueueUniqueJobs`. The scan never touches MeiliSearch.
- `recoverPendingProbes` (`scan.go:496`) gains `AND archived=0 AND missing_at=0`, so crash-recovery probes neither resurrect archived rows nor hammer files that vanished mid-processing during grace.

Why two scans instead of one: the grace window is what keeps a transient unmount (NAS reboot, SD card pulled, rename-then-rename-back) from churn of Meili de-index/re-index cycles. A file that is genuinely gone is archived one grace period after the first scan that noticed — typically the second or third periodic scan — while the decision itself costs two tiny batched UPDATEs.

Kill switches and knobs (env, wired through `loadConfig`, `cmd/quipster/main.go:96`):

- `ARCHIVE_GRACE` (duration, default `48h`): minimum absence before archiving. `ARCHIVE_GRACE=0` archives on the first missing observation.
- `ARCHIVE_ENABLED` (default `1`): set to `0` to restore the legacy hard-delete prune behavior for operators who explicitly want it. The default build satisfies the no-permanent-deletion invariant.

## De-indexing workflow (background, lowest priority)

New job type `archive_deindex`, payload `{"mediaId":N}`, priority `-1`, registered with a single dedicated worker (`runner.Register("archive_deindex", 1, ix.ArchiveJobHandler())` next to `cmd/quipster/main.go:170-174`). Because `ClaimJob` orders by `priority DESC` with no floor (`internal/store/jobs.go:166`), priority `-1` jobs are claimed only when no scan/probe/index/clip/reindex job is queued — de-indexing trickles strictly behind user-visible work and never thrashes the system. Deduplication rides on `EnqueueUniqueJob`'s `status IN ('queued','running')` check (`internal/store/jobs.go:491`), so a done/errored job never blocks a legitimate re-archive.

Handler steps (each idempotent):

1. Load the media row. `archived=0` → finish done (lazy cancellation: a restore beat the job; nothing to do).
2. `archived=1 AND deindexed=1` → finish done (already de-indexed).
3. `DeleteDocumentsByFilter("mediaId = N")` + `WaitForTask` (same primitives as `Indexer.DeleteMedia`, `internal/indexer/indexer.go:318`). Unlike `DeleteMedia`, **errors propagate**: the runner retries (3 attempts), the failure is visible in the admin job list, and reconciliation re-enqueues later. Silent best-effort deletion is what allowed stale docs to leak today.
4. Re-read the row after the delete lands. If `archived=0` (restored mid-flight) → re-enqueue an `index` job (priority 1) so the just-deleted docs are re-added, finish done, leave `deindexed=0`. Worst case is one redundant reindex; no content can be lost.
5. Still archived → `MarkMediaDeindexed(id)`.

Three reconciliation layers guarantee eventual convergence without operator action:

- **Boot reconciliation:** after migration, main enqueues unique `archive_deindex` jobs for `ArchivedPendingDeindex()`. This self-heals crashes, Meili outages, and pre-deindex rows left by upgrades.
- **Query-time reconciliation:** `appendHits` (`internal/search/search.go:215`) resolves every hit back to SQLite anyway. When a hit's media row is archived, the hit is dropped *and* a unique `archive_deindex` job is enqueued fire-and-forget. Search traffic itself repairs missed deletions, bounded by job dedupe.
- **Runner retries** on transient Meili failures.

## Read-path gating (de-indexed from queries and clips)

All gates are on `archived=0`/`archived=1`, applied at the store choke points so every caller (HTTP API, MCP tools, admin endpoints) is covered once:

| Query / path | Change | Why |
|---|---|---|
| `search.go appendHits` (`search.go:215`) | skip hits whose media is archived; enqueue reconciliation job | Primary gate; correct even when Meili docs lag. |
| `GetChunkByKey`, `GetChunkByID`, `ChunkAtTime` (`internal/store/chunks.go:201-237`) | join filter `AND m.archived=0` | Single choke point for `/api/chunks/{id}`, MCP `get_scene_by_line`, quote deep-links. Stale-doc tolerance then treats archived hits as missing. |
| `RandomQuotes` (`chunks.go:174`), `PopularExamples` (`internal/store/examples.go`) | `AND m.archived=0` | Landing page/examples must not surface archived rows whose frozen `status` is still `indexed`. |
| `TitleMatches` (`media.go:361`) | `AND archived=0` | Search's no-subtitle fallback. |
| `ListMedia` (`media.go:235`) | default `archived=0`; `archived` query param (`1` → only archived, `any` → all) for the admin archive browser | Operators can still see what is archived. |
| `MediaIDsByStatus` (`media.go:336`) | `AND archived=0` | `/api/admin/reprobe` and scan recovery must not re-probe archived rows. |
| `MediaIDsNeedingIndex` (`chunks.go:153`) | `AND m.archived=0` | `ReindexAll` must never touch archived rows; keeps archived chunks out of index jobs. |
| `clips.Validate` (`internal/clips/clips.go:135`) | reject archived → HTTP 404 | Archived media is not renderable; the async `clip` job handler also no-ops (finish done) for rows archived between request and render. |
| `GET /api/thumb/{mediaId}` | no change | Cached thumbs of archived rows keep serving; useful in the archive browser and harmless (the video bytes are already gone from disk; regeneration is impossible either way until the file returns). |
| `probe` worker (`internal/probe/worker.go`) and `nodes.ApplyProbeResult` (`internal/nodes/nodes.go:480`) | guard: archived → finish done with no writes | Prevents remote-node or in-flight probes from mutating a frozen row's `status`. |

`MediaStatusCounts` (`media.go:317`) additionally reports the archived count under key `archived`, which flows through the existing SSE collector (`internal/events/events.go`) into the admin dashboard with no new endpoint.

## Re-activation pipeline

Restore can be triggered two ways, sharing one code path:

1. **Scan-driven (primary):** the file reappears under a known library root; the next completed scan's `RestoreSeenMedia` restores it. Lazy, consistent with the rest of the design — no operator action.
2. **Admin:** `POST /api/admin/media/{id}/restore` for "bring it back now" (e.g. the file was re-added under a different path, or the operator wants the captions searchable before the file lands). Restoring a row whose file is still absent is allowed and safe: search re-activates from SQLite text immediately, clip requests 404 until the file exists, and if the file never returns the next scan re-latches `missing_at` and the row re-archives after grace — a stable loop with no special cases.

What restore does and does not do:

- Clears `archived/archived_at/missing_at`, resets `deindexed=0`.
- If `deindexed=1`: clears the media's chunk `indexed_at` flags (existing `ClearChunkIndexFlags`, `chunks.go:141`) and enqueues an `index` job (priority 1). The normal `IndexMedia` path re-adds every document from `chunks` + `media_files`; with a semantic embedder configured, vectors are regenerated server-side by Meili from the document template (`internal/indexer/indexer.go:233-235`), so re-activation costs one Meili upload — no ffprobe, no subtitle extraction, no cue re-parsing, no chunk rebuild.
- If `deindexed=0`: touches nothing in the index — the documents were never deleted, and the row becomes instantly searchable.
- Does **not** enqueue a probe while cues exist. Probe metadata, subtitle tracks, cues, and chunks are intact. The single exception: a row archived while `status='pending'` (removed mid-initial-processing) restores with `status='pending'`, and the ordinary `recoverPendingProbes` path probes it once the file is back — correct, because there was no caption data to preserve.

The `ever_indexed` latch is never cleared by archive/restore; it continues to mean "docs may exist", and the `deindexed` column refines that during archive windows.

## Storage management edge cases

- **No disk relocation.** The archive lives in SQLite; the video bytes are already gone (operator removal is the trigger). An "archive bin" directory is explicitly out of scope: moving files would change their paths, break scan identity (path is the unique upsert key), and re-introduce the very delete-and-reprobe churn this design removes.
- **DB growth is bounded and small.** Cues/chunks text dominates: at ~280 chars per chunk and ~3 cues per chunk, a dialogue-heavy 45-minute film costs roughly 150-200 KB. Ten thousand archived films stay in the low single-digit GB of SQLite — versus the video bytes that are already reclaimed. There is no auto-purge (the invariant forbids it); growth is observable via the `archived` status count and `LineStats`.
- **Cache directory.** Extracted subtitles (`cache/subs/{mediaID}.srt`), thumbnails, and rendered clips stay LRU-managed (`internal/cache`). Eviction of an archived row's cached extraction is harmless — SQLite cues are canonical, and the extraction is only consulted during probes, which restore bypasses. A thumbnail evicted while archived yields the existing not-found response until the file returns.
- **Dangling sidecar references.** `subtitle_tracks.extracted_path` may point at a removed sidecar while archived; it is only read during a probe of the same path, so restore never dereferences it.
- **`cache_entries` rows** keep their existing lifecycle; clips caches of archived rows become unreachable (Validate rejects first) and age out via LRU.
- **Permanent renames.** A move to a new path is seen as missing(old) + new(new row). After grace the old row archives with its captions preserved; the new path is processed normally. Future enhancement (schema already supports it): content-hash linking (`size_bytes`+`mtime`+duration fingerprint) could let the new path adopt the archived row's captions instead of re-probing.

## Search engine sync edge cases

- **Ordering:** the `archived` flag commits before any Meili deletion is requested, and every read path gates on the flag. A de-index that is slow, failing, or never runs cannot leak archived content; it can only leave unused documents (cleaned by reconciliation).
- **Meili down:** the job retries, then errors visibly; boot and query-time reconciliation re-enqueue unique jobs until the delete lands. No operator action, no scan-time failure (the scan never touches Meili).
- **Meili wiped or recreated:** `deindexed=1` rows believe their docs are gone (true); `deindexed=0` archived rows still get a delete-by-filter (a no-op on an empty index). Consistent either way.
- **Restore/de-index races:** covered by the handler's post-delete re-check (step 4 above). The only cost is a redundant reindex; the flag-then-delete ordering means the failure mode is always "too many documents, re-added later", never "missing content".
- **Idempotence:** delete-by-filter is idempotent, archive/restore loops re-enqueue cleanly (unique-job dedupe only blocks queued/running duplicates), and repeated reconcile passes are no-ops once converged.
- **Hybrid/semantic modes:** same index, same document shape; no embedder-side work is added on either transition.

## Performance implications

- **Scan path:** adds at most three batched UPDATEs proportional to the number of missing/returned files (batches of 400, one transaction each). A 2,000-file removal completes in a handful of transactions; unchanged files cost one extra SELECT with zero hits. No new walks, stats, or ffprobe invocations.
- **De-index trickle:** priority `-1` + single worker means archive backlog drains only when the queue is otherwise empty. Each job is ~2 Meili requests (task submit + wait at the existing 300 ms poll). A 10k-file backlog fully drains in a few hours of idle trickle, strictly behind every user-visible job, and is invisible to probe/index latency.
- **Restore burst (remounted drive):** produces normal-priority `index` jobs, identical in shape to a routine library index; `EnqueueUniqueJobs` dedupes overlapping scans.
- **Read paths:** gating reuses rows already loaded (`appendHits` and the chunk resolvers already join `media_files`); five integer columns added to `mediaCols` are noise. The reconciliation enqueue is one deduped INSERT per affected hit, amortized to zero once converged.
- **Write pressure vs. today:** archival replaces cascade DELETEs of cues/chunks (the largest tables) with small flag UPDATEs — strictly less WAL and checkpoint work than the current prune.
- **`jobs` table growth:** one row per archived media, consistent with existing per-media probe/index job retention.

## API and configuration surface

| Surface | Change |
|---|---|
| `POST /api/admin/media/{id}/restore` | new, token-guarded admin route (router admin group, `internal/api/router.go:98-123`) |
| `GET /api/media?archived=1\|any` | extend `handleMediaList` (`router.go:238`) |
| `GET /api/admin/status` + SSE | gains `archived` count via `MediaStatusCounts` |
| `ARCHIVE_ENABLED`, `ARCHIVE_GRACE` | env knobs via `loadConfig` |

Existing public endpoints gain no new fields beyond `archived` on media objects; every archived exclusion is server-side.

## Rollout, migration, and test plan

1. **Migration:** the four columns land via the existing `migrations` list; index creation is the only multi-row operation and is bounded by table size. `TestLegacySchemaArchiveMigration` extends the pre-seeded legacy-DB pattern in `internal/store/coverage_test.go:17-60`.
2. **Deploy posture:** nothing is archived on deploy; the first completed scan only latches `missing_at`. Archiving begins one grace period later, so operators get a full grace cycle of observation before any behavior change. The count surfaces in the admin dashboard from day one.
3. **Tests** (all packages currently have established patterns to extend; coverage must stay ≥ 85% per `.githooks/pre-commit`):
   - store: `ArchiveMissingUnder` grace partitioning, `RestoreSeenMedia`/`RestoreMedia` (deindexed both ways), `MarkMediaDeindexed` guard, list/title/quotes/index-needing filters, migration test.
   - scan: first pass latches only; second pass archives; return-during-grace clears; return-after-archive restores and re-enqueues index (extend `internal/scan/scan_test.go` fixtures and the captured-callback style of `TestRunAndJobHandler`).
   - indexer: `archive_deindex` handler against the fake Meili (`meili_fake_test.go`): success latches, restored race re-enqueues index, Meili failure retries (extend `TestDeleteMediaBestEffort` style).
   - search: archived hits dropped + reconciliation enqueued (fake Meili, `internal/search/fake_test.go`).
   - api/clips: restore endpoint, archived 404s for chunks/clips, list filtering (`internal/api/router_test.go` helpers).
4. **Manual QA:** remove a file → verify `missing_at` latch and unchanged search; force grace expiry → verify docs gone, search/clip empty, cues intact in SQLite; return the file → verify restore with zero probe/extraction work (no `probe` job enqueued, `extracted_path` untouched) and search live again.

## Non-goals and future work

- **Explicit archiving of files that exist on disk** (hide-but-keep): v2; needs an archive-origin column so scans do not fight admin intent.
- **Content-hash re-link on permanent renames**: adopt an archived row's captions for a new path without re-probing.
- **Permanent purge endpoint**: deliberately omitted — the invariant forbids the system from ever destroying caption data; out-of-band DB maintenance remains the only path to true deletion.
- **Cold-storage compression of archived cue text**: unnecessary at projected volumes; revisit if archive growth becomes measurable.
