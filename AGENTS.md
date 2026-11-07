# Quipster AI Agent Guidelines

This document outlines the standard operating procedures, architectural guidelines, and coding standards for all AI agents and developers contributing to the Quipster repository.

## 1. Commit Message Conventions

We strictly enforce a uniform commit message format to maintain a readable and organized project history. Follow this convention when creating new PR's and setting PR titles also!!

**Format:** `<DOMAIN>: <summary of changes>`
*(Note: Start the summary with a lowercase letter and use the imperative mood. Do not end the summary with a period.)*

**Valid Domains:**
Based on the repository's history and architecture, you must use one of the following exact domain tags:
- `BACKEND`: For changes to the Go backend services, API router, data pipelines, and SQLite store.
- `ADMIN`: For changes related to administration features, queue control, multi-folder library settings, and ops endpoints.
- `UI`: For frontend Vue3/Vuetify3 components, design system overhauls, and client-side logic.
- `CLIPS`: For updates to FFmpeg processing, thumbnail extraction, keyframe snapping, video/audio mapping, and caching.
- `SEARCH`: For changes affecting MeiliSearch indexing, document ingestion, and search APIs.
- `INFRA`: For infrastructure adjustments, Dockerfiles, Docker Compose configurations, and GitHub Actions (CI).
- `DOCS`: For changes strictly relating to READMEs, markdown documentation, or licensing.

*Example:* `BACKEND: fix subtitle extraction concurrency limits on NFS`

## 2. Architecture and Design Standards

- **SOLID Principles:** Enforce a clean, modular structure. Ensure that functions have a single responsibility, interfaces are strictly segregated, and dependencies are cleanly injected.
- **Maintainability & Readability:** Code must prioritize human readability over cleverness. Avoid deeply nested structures and excessive complexity. 
- **Decoupled Architecture:** Maintain strict boundaries between HTTP routing layers (e.g., `chi`), business logic, data persistence (`SQLite`), and external services (`MeiliSearch`, `FFmpeg`).
- **Respect Deliberate Trade-offs:** Several "smells" (low concurrency, bounded non-atomic writes, sequential scan phases, oddly specific subtitle heuristics) are intentional incident-driven decisions. Read Section 5 before refactoring them.

## 3. Go Documentation & Commenting Standards

- **Idiomatic Go Docs:** Adhere to [Effective Go](https://go.dev/doc/effective_go) documentation practices. Every exported identifier (packages, structs, interfaces, functions, variables) **must** have a preceding documentation comment starting with the name of the identifier.
- **Intent Over Mechanics:** Inside functions, comments should explain the *why* and the *intent* of the implementation rather than restating the code structure. 
- **Non-Trivial Logic:** Ensure core business logic (e.g., filename parsing heuristics, FFmpeg stream mapping, search index latching) is extensively documented so that the context and edge cases are preserved for future developers.

## 4. Linting and Testing (Pre-Commit Requirements)

Before finalizing any changes or attempting to commit code, AI agents **must** ensure the codebase passes all tests, meets our strict linting standards, and maintains at least 85% test coverage.

1. **Format and Lint:** Run `golangci-lint run`. The configuration in `.golangci.yml` enforces standards for formatting, complexity (cyclomatic <= 15), security (`gosec`), best practices (`staticcheck`), and missing documentation. Agents must fix any reported errors.
2. **Test & Coverage:** Run `go test -coverprofile=coverage.out ./...` followed by `go tool cover -func=coverage.out | grep total`. Ensure that no tests are broken and that the total project statement coverage is **>= 85.0%**. If coverage drops below this threshold, you must write new unit tests.
3. **Pre-commit:** This repository utilizes a pre-commit hook (`.githooks/pre-commit`) that will automatically reject commits if tests fail, linters fail, or total coverage falls below 85%.

### Example Go Documentation:
```go
// SubtitleExtractor represents a service for parsing and extracting
// subtitle tracks from a given media file.
type SubtitleExtractor struct {
    // fields...
}

// Extract processes the media file at the given path and extracts the english
// audio track or subtitle stream based on the provided configuration.
// It uses a bounded concurrency model to prevent I/O hangs on NFS mounts.
func (s *SubtitleExtractor) Extract(ctx context.Context, mediaPath string) error {
    // We avoid full-file re-reads by checking the cached state first.
    // This dramatically speeds up re-probing on unchanged files.
    if s.isCached(mediaPath) {
        return nil
    }
    // ...
}
```

## 5. Deliberate Design Decisions (Do Not Undo Without Evidence)

Quipster's architecture carries a set of deliberate trade-offs that exist because of real production incidents: SQLite writer starvation, NFS/SMB I/O contention, disappearing network shares, MeiliSearch eventual consistency, and pathological subtitle files (30k–85k karaoke cues in a single episode). These are **not** accidental complexity.

Before "cleaning up", "simplifying", "modernizing", or otherwise refactoring any item below, assume it was intentional. Require production evidence or a new requirement, update this section and the PR description with the reasoning, and call out the contradiction explicitly in the PR. A code-quality review that flags one of these is not evidence by itself.

### 5.1 SQLite persistence and concurrency

- **SQLite has one writer, so concurrency is deliberately serialized and low.** Local probe workers, clip workers, and node defaults are intentionally `1` (probe `4→1`, clips `2→1` in #57), and the server enforces `NODE_MAX_CONCURRENT_JOBS` (default `1`). Do not raise these defaults without production measurements.
- **Each store has a dedicated single-connection writer pool and a separate WAL reader pool.** Production inspection on 2026-11-07 found 34 orphaned jobs after completion/requeue writes exhausted SQLite's busy timeout on 2026-11-03. Worker limits alone did not serialize claims, heartbeats, progress and ingestion writes. Route every mutation and write transaction through the writer, retain the read-first empty-claim path, and do not hold the writer across batches. `DB()` exposes the writer for raw-access compatibility; `:memory:` aliases both pools to preserve database identity. Serialization is per store instance, not across processes.
- **Local workers retain completed claims until their verdict is durable or ownership changes.** The November 2026 incident left 34 phantom running jobs when terminal/requeue writes failed and workers moved on. Persistence retries use bounded attempt contexts and capped backoff without rerunning the handler or spending attempts. Claim IDs are unique, and progress/finalization compare ownership and attempt; uncertain claims are reconciled only by their exact owner. Do not recover other live workers by age or absence. Shutdown can leave unresolved claims for the existing startup recovery.
- **Batched writes are intentionally non-atomic across batches.** `updateIDsBatched`, `clearChunkFlagsBatched`, `ReplaceCues` (2,000/tx), `ReplaceChunks` (1,000/tx), `MarkChunksIndexed` (500/batch), and 400-id prune/restore batches deliberately release the single writer lock between batches. Do **not** collapse them into one giant transaction or one statement: the partial-update window is the price of avoiding `SQLITE_BUSY` livelocks (#43, #47, #50, #55, #44).
- **The DSN pragmas are deliberate:** WAL, `busy_timeout(30000)`, `journal_size_limit(256 MiB)`, `synchronous(NORMAL)`, `foreign_keys(1)`, and `_txlock=immediate`. The WAL cap exists because a production DB had a 5.2 GB WAL (#46). The long busy timeout exists because of large batched writes (#55). Do not remove or inline the `dsn` helper.
- **Hot-path indexes are deliberate, not redundant:** `idx_cues_media_idx`, `idx_chunks_media`, `idx_chunks_indexed`, `idx_jobs_claim`, `idx_jobs_type_status_finished`, `idx_jobs_dedupe`, `idx_media_archived`, `idx_media_status`, `idx_scans_status`. Several prevent O(chunks × cues) scans or writer-lock starvation (#43, #60, #24, #44). Verify with `EXPLAIN QUERY PLAN` before dropping any.
- **Empty queue polling reads before it writes.** `ClaimJob` uses a WAL read-first check so idle pollers do not open `BEGIN IMMEDIATE` (#45). The rollback on a canceled claim deliberately uses a background context so a request cancellation cannot return a pooled connection with an open write transaction (#43).
- **Expand the migration list, never rewrite the schema.** Legacy `media_files.status` has a baked-in `CHECK` constraint, so new lifecycle state is added as orthogonal columns via the `pragma_table_info` + `ALTER TABLE` migration pattern, not by rebuilding tables or adding `status` values.
- **`error` media rows are not retried automatically.** The scan upsert intentionally dropped `OR media_files.status IN ('error')`; unchanged failures would otherwise re-enter the probe queue every scan and starve the walk (#24). Explicit reprobe (`POST /api/admin/reprobe`, optionally by media id) is the recovery path.
- **Media rows and caption data are never deleted.** Pruning only sets archive flags. Restore unconditionally clears chunk index flags and enqueues an `index` job because the `deindexed` latch can be stale after a crash mid-de-index (#44, `docs/video-archive-lifecycle.md`). Do not add a purge path or make restore conditional on `deindexed`.

### 5.2 Scan and probe pipeline

- **Scans are metadata-only and mutually exclusive with probing.** Probes are canceled and **soft-requeued without consuming an attempt** during a scan, and probes are enqueued only after the walk completes. Do not enqueue probes during the walk, parallelize scans with extraction, or spend retry attempts on scan-induced cancellation (#24, `internal/jobs/gate.go`).
- **The scan phase order is intentional:** `recoverPendingProbes → walk → flush batch → restore → archive → enqueue probes`. Restore runs before archive (a seen path is genuinely back), archive is skipped entirely when `WalkErrors > 0`, and a path is added to `keep` **before** `Stat` so a transient stat failure cannot archive an existing row. Do not "parallelize" or reorder these phases.
- **The archive grace window is intentional.** `ARCHIVE_GRACE` (default 48h) requires absence across consecutive scans, and even `ARCHIVE_GRACE=0` archives only on the second consecutive miss. One bad scan must never archive anything (#44).
- **Probe/extraction timeouts have deliberate ordering.** The extraction timeout starts **after** the concurrency semaphore is acquired, so waiting behind other full-file reads cannot produce a false `no_subs` (#24). Keep that ordering.
- **Subtitle selection is deliberately narrow:** sidecar-first, embedded fallback, English text streams only (or a sole untagged text stream), bitmap codecs skipped, cached extraction reused for unchanged already-processed files. Do not generalize to bitmap OCR or all languages without a product requirement.
- **Subtitle normalization heuristics are deliberate and data-driven:** exact-cue dedupe at millisecond precision, ASS/SSA override-tag stripping, repeated-text filtering (>20 occurrences), and ASS vector-drawing removal (#48, #52, #50). They exist because real episodes produced 30k–85k cues.
- **Filename parsing rules are deliberate:** preserve in-title years (`1917`, `2001`, `Blade Runner 2049`), strip only parenthesized/release years, and strip `{imdb-…}` brace groups (#98f6942, #aa4fc12). Do not simplify with a generic year regex.

### 5.3 Clips and rendering

- **`clips.Renderer` is deliberately separate from `clips.Service` and store-free.** Worker nodes render through `Renderer` without a store; merging it back into `Service` would reintroduce the `clips.New(nil, …)` boundary leak (#38, #56, #26). The same applies to `probe.Compute` (store-free) vs `probe.Apply` (store writes): do not collapse that split.
- **Render dedupe uses `singleflight`.** Do not reintroduce bespoke flight maps/mutexes (#56).
- **Encoding choices are deliberate:** fast mode is a single input-seek stream copy (no keyframe pre-scan), preview is ultrafast/low-res for browser playback, accurate prefers hardware encoding and falls back to CPU `libx264` (including mid-run fallback), and chapters/data/subtitle streams are dropped (#8db4183, #15, #2e1f027).
- **The clip cache key includes the encoding profile** for transcoded modes while stream-copy identity stays profile-independent, so tuning changes cannot reuse stale transcodes. Node claims pin the server's current clip settings so uploads match the server-derived key (`internal/clips/renderer.go`, #26).

### 5.4 Search and indexing

- **SQLite is the source of truth; MeiliSearch is eventually consistent.** Read paths gate on `media_files.archived` before trusting Meili docs, and stale archived hits are dropped and lazily re-enqueued as `archive_deindex` jobs. Do not make search trust Meili alone (#44).
- **De-indexing is a lowest-priority trickle** (`priority -1`, single worker) with three reconciliation layers: runner retries, boot sweep, and query-time repair. The scan never talks to Meili.
- **Semantic search is Meili-side.** Auto mode resolves to hybrid only when an embedder is configured and the query is non-blank; explicit hybrid/semantic degrade to keyword instead of failing (#19).
- **Document IDs are `mediaId_start-end`; chunk ID parsing is canonical in `internal/chunks`** and reused by HTTP and MCP. Do not duplicate parsing in transports (#39).
- **Search responses return empty arrays, never `null`,** and subtitle-less media fall back to LIKE-based title matches (#4bd704d).
- **`ever_indexed` / `MarkIndexed` avoids a pointless delete round-trip** on first-time indexing (#0940666).

### 5.5 Worker nodes

- **Nodes claim only `probe` and `clip`.** Scans and Meili indexing intentionally stay server-side (they write metadata rows directly / talk to Meili) (#26).
- **Registration is unauthenticated but capped; approval issues a one-time claim token.** Heartbeats reconcile unreported in-flight leases and requeue them past a grace window, and the janitor requeues work from offline/deleted nodes (#26, #45).
- **Path remapping is server-driven:** each claim carries the server media root, and the node rewrites it onto its own mount with separator- and case-tolerant matching. Comparison logic is deliberately permissive for mixed-OS clusters.

### 5.6 HTTP API and MCP

- **`internal/api/router.go` is intentionally wiring-only.** Handlers live in domain files (`media_handlers.go`, `admin_handlers.go`, etc.); feature logic must not leak back into the router (#39).
- **MCP is deliberately a thin adapter.** Every tool delegates to the same services as REST; it owns no business logic. Scope tokens, the admin master switch, path redaction, hashed audit token IDs, and bounded result sizes are intentional (#25).
- **SSE endpoints are deliberately exempt from the global request timeout.** Applying the 5-minute timeout severs streams and forces reconnect storms (#45).
- **Admin dialogue totals are demand-loaded and cached, not recomputed for every status tick.** Production has tens of millions of cues and chunks; a full count on every SSE tick competes with ingestion for disk I/O. The shared collector refreshes totals asynchronously with a bounded query timeout and suppresses expired values rather than presenting them as current. A cold cache can return `lines: null`; do not replace unknown counts with zero or restore synchronous full-table counts.
- **Ad-hoc `map[string]any` JSON in handlers is accepted.** These are one-shot response envelopes; typed DTOs are required at service boundaries (`search.Hit`, `clips.Request`, `probe.Outcome`), not for every tiny handler payload.

### 5.7 Frontend

- **Views are orchestrators, not god components.** Static heuristics/business dictionaries live in `web/src/lib/*`, and panels are focused subcomponents (#41, #59). Do not re-inline rule maps or dense markup into views.
- **Search submit semantics are deliberate:** Enter always submits the raw typed query; suggestions activate only via click/tap or arrow keys + Enter; Escape only dismisses. This fixed a real mobile/keyboard trap (#35).

### 5.8 Review protocol

- If a review or refactor proposal touches any item above, check `git log`/PR history and the linked design docs first, then state explicitly why the original constraint no longer applies.
- A finding such as "this is sequential", "these writes are not atomic", "this helper is a thin wrapper", "this defaults to 1 worker", or "this heuristic is oddly specific" is **not** sufficient justification on its own.
- When a deliberate decision changes, update this section in the same PR and record the evidence that justified the change.