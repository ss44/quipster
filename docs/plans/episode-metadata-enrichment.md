## Description
Implement **optional TVDB/TMDB metadata enrichment**, retaining filename parsing and offline scans as the fallback. Use reliable series/movie/episode identity to support the episode-level notable-quote work in #12—not to assume metadata providers supply a ranked quote catalogue.

Related: #12 (notable/popular dialogue), Epic #8. Existing implementation triage in this thread remains applicable; this expanded plan supersedes the original proposal to replace filename parsing wholesale.[1][2]

## Product outcomes
- Correct series/movie/episode identity, title, year, season/episode numbering, summary, artwork, genres and credits where available.
- An episode page can eventually show notable dialogue grounded in **our actual subtitle cues**, with reasons and clip links, via #12.
- Clearly distinguish a provider's show/episode popularity or rating, an externally documented quote, local Quipster engagement, and an AI-predicted memorable line. They are different signals; one must not be presented as another.
- Existing scan, search, preview and downloads continue working without provider credentials, internet access, or AI.

## Provider identity and matching
1. Preserve parsed metadata and embedded external IDs, including existing brace/year filename heuristics. Attempt deterministic external-ID resolution first; TMDB supports external-ID lookup, and TheTVDB documents remote-ID lookup.[4][10]
2. Otherwise search once for a normalized series/movie identity, not once per file or subtitle line. Use year, media type and episode information to disambiguate. Cache failed/ambiguous matches as well as successes.
3. Resolve a series, then fetch/cache season or paginated episode metadata and join episodes locally. TMDB's season-details endpoint supplies a season resource; TheTVDB documents extended series responses and episode pagination.[5][9]
4. Store the **episode ordering** explicitly (aired/DVD/absolute/other supported ordering), plus specials and multi-episode mappings. Do not treat season/episode numbers as interchangeable across providers/orders. TheTVDB specifically documents alternate season types.[9]
5. Ambiguous matches go to an admin approval queue; support manual ID/mapping correction, pinning, undo and an explanation of match evidence. Do not silently relabel a library from a low-confidence text match.
6. Keep provider records/mappings separate from local files: multiple encodes of one episode share external identity, while cue timing/revision remains file-specific. One multi-episode file can map to multiple episode identities.

## Data contract and lifecycle (proposed, not implemented)
- Mapping: local media ID → provider, media type, series/movie ID, episode ID(s), ordering, match evidence/confidence and manual override.
- Enrichment snapshot: provider/schema version, language, requested fields, fetched/expiry times, response/content hash, refresh status and attribution/provenance.
- Track parsed, provider and manually overridden values independently; define display precedence without destroying original metadata.
- Fetch outside the metadata-only scan walk. No provider calls, AI calls, or waits inside a SQLite write transaction.
- Incremental refresh uses provider updates/change tracking where available and response validators where supported; checkpoint only after durable publication. Cache retention/refresh settings must fit provider terms. TheTVDB's official guidance describes a cached database and scheduled `/updates` retrieval.[9]
- Persist small batches through the existing server writer; publish completed metadata snapshots and reconcile Meilisearch after commit. Title metadata refresh must not force subtitle extraction or a full corpus reindex.

## Batching and worker-node assistance
**Coordinator owns identity, credentials, provider quotas and persistence.** The current node contract only handles probe/clip; enrichment capabilities are new work, not an existing switch.

- Plan jobs at series/season granularity to avoid repeated title searches across episode files. Reuse cached provider responses across all matching files.
- Initial provider fetches stay server/broker-side behind one global request budget and singleflight deduplication. Approved nodes can help with local matching/normalization over bounded, immutable work packets without receiving provider credentials or opening the SQLite database.
- A future `metadata_match` capability needs explicit version negotiation, scoped leases and result limits. Older nodes continue doing probe/clip only. Do not allow nodes to self-claim arbitrary new job types.
- If provider fetching is later delegated, use short-lived broker-issued work authorization and a **shared provider-account limiter**, never a full per-node allowance. More nodes must not multiply API traffic.
- Prioritize normal probing/clips ahead of enrichment. Pause/backpressure on queue pressure, writer latency, provider errors or quota exhaustion; bounded retries with jitter, Retry-After handling and dead-letter diagnostics.
- Dedup key includes provider, external identity/query, ordering, language, requested fields and enrichment version. Retries reuse that identity rather than refreshing everything.

## AI / search policy
Metadata identity should be deterministic first. No AI is required for Phase 1. An optional AI match suggestion can be reviewed, but cannot override pinned IDs or fabricate provider IDs.

The detailed candidate-ranking, external quote evidence and global token ledger belong in #12. Metadata enables accurate episode association; it is **not** evidence that a particular line is famous. The reviewed TheTVDB v4 schema has no dedicated quote endpoint, and TheTVDB describes its content score as a relative-popularity value for the record, not line-level quote popularity.[9][10]

## Operational and provider requirements
- Provider enablement and credentials must be explicitly configured. Do not change production settings or send existing transcripts to an AI provider as part of this planning task.
- Verify account tier, allowed use, caching/redistribution and attribution before enabling a provider. TheTVDB and TMDB publish their access/attribution requirements; do not hardcode assumed universal quotas or pricing.[7][8]
- Rate limits are centrally configurable and adaptive; TMDB asks clients to handle 429s and notes limits can change.[6]
- Diagnostics: cache hits/misses, unique identities resolved, ambiguous approvals, requests/retries/429s, stale records, job age and writer/queue impact; no secret values in logs.

## Phased delivery and acceptance
### A — Optional metadata foundation (no AI)
- [ ] Provider adapter/configuration, provenance and attribution; filename/offline fallback retained.
- [ ] ID-first matching, cached title search, explicit ordering, specials/multi-episode mappings and admin override.
- [ ] Series/season batching, deduplication, global rate limiting and incremental refresh; no calls in scan walk/write transactions.
- [ ] Tests for remakes, ambiguous titles, alternate orders, negative cache, manual pins, offline/429/error paths and refresh idempotency.

### B — Connect episode discovery to #12
- [ ] Share external episode identity and file-specific dialogue revision with the quote pipeline.
- [ ] Episode-level notable quote read model, with links to actual local cues; no fabricated quotes.
- [ ] Provider popularity/rating is never mislabeled as quote popularity.

### C — Optional node offload
- [ ] Capability-negotiated bounded matching jobs, immutable input versions, fenced leases, idempotent results and coordinator-only writes.
- [ ] Global quotas still hold with multiple nodes/retries; enrichment cannot starve clips or ingestion.
- [ ] Current repository test/coverage requirements apply; this issue does not silently raise or weaken them.

## Sources

[1] https://github.com/ss44/quipster/issues/16 — Existing metadata enrichment task
[2] https://github.com/ss44/quipster/issues/12 — Existing notable dialogue task
[4] https://developer.themoviedb.org/reference/find-by-id — TMDB external ID resolution
[5] https://developer.themoviedb.org/reference/tv-season-details — TMDB season details
[6] https://developer.themoviedb.org/docs/rate-limiting — TMDB rate limiting
[7] https://thetvdb.com/api-information — TheTVDB API attribution and licensing
[8] https://developer.themoviedb.org/docs/faq — TMDB FAQ and attribution
[9] https://github.com/thetvdb/v4-api — TheTVDB official caching, updates, scores and season ordering
[10] https://thetvdb.github.io/v4-api/swagger.yml — TheTVDB v4 OpenAPI schema
