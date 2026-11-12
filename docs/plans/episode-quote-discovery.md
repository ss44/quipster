## Expanded episode discovery and bounded-cost execution plan

This addendum refines the original scoring proposal and its implementation triage; it does not claim that notable-quote scoring is implemented. Coordinate metadata identity with #16 instead of duplicating provider matching.[1][2] In conflicts, this addendum's revision safety, staged rollout, signal labels and explicit budget controls take precedence over earlier illustrative weights/phase descriptions. Use the **current repository's coverage gate**; the original 90% checkbox is historical, not a new requirement.

### What users should get
For an identified episode/movie, show a short, diverse list of notable lines or exchanges that actually exist in its current local subtitles, with timestamps, preview/download links and a reason for each selection. Add episode/show filters and an admin review view. Preserve exact-phrase search relevance by default; discovery ranking is a separate opt-in surface until calibrated.

Keep these labels distinct:
- **Known quote:** matched to identified, permitted external evidence with source/provenance and match confidence.
- **Popular in this library:** supported by actual Quipster engagement, with time window and sample size. Count successful downloads, not merely render attempts; a share-sheet opening is not proof of a completed share. Deduplicate events, exclude bots/test traffic where possible, and apply minimum-support/decay policies.
- **Recurring catchphrase / phrase match:** corpus or versioned dictionary evidence; not automatically famous.
- **Suggested memorable line:** deterministic and/or model assessment without external popularity evidence. AI judgment alone cannot earn a “popular” or “iconic” fact label.

### Pipeline: reduce inputs before any AI
1. **Wait for a ready dialogue revision.** Snapshot one completed file/episode input; do not read partially rebuilt cue/chunk batches.
2. **Cheap local candidate pass.** Normalize without rewriting displayed dialogue; reject stage-direction-only/repeated karaoke junk, preserve short iconic exceptions (do not blindly discard two-word phrases), merge a few adjacent cues for exchanges, and match a provenance-tracked phrase dictionary. Use corpus rarity/reuse from a versioned bounded summary, not a full-library query per episode.
3. **Rank and deduplicate locally.** Use deterministic features and optional local embeddings to select a configurable small candidate pool (for example 10–20 candidates per episode). These are starting values for evaluation, not guaranteed optimal thresholds. Retain a small diversity/exploration sample so short or unconventional quotes are not filtered out permanently.
4. **Optional external evidence retrieval.** Resolve the episode via #16, then use allowed curated/licensed quote sources or provider-specific search adapters. Query/cache by external episode/title identity, batch supported lookups, deduplicate normalized phrases across episodes, and never launch a web search per subtitle cue. TVDB metadata/content score is not a line-level popularity catalogue.[9][10] No assumed IMDb scraping rights or automatic scraping fallback.
5. **Optional AI rerank.** Send only selected candidate IDs, bounded text/context and compact episode metadata—not the full transcript, artwork, repeated instructions or full search pages. Prefer one bounded per-episode request; micro-batch episodes only when a provider supports it safely, with explicit episode partitions and independent result IDs. A batch endpoint is an adapter capability, not an assumed cross-provider feature.
6. **Validate and stage results.** Model output selects supplied candidate IDs/ranges and a compact reason code, never invents dialogue/timestamps, speaker identities or web sources. Reject unknown IDs, changed inputs, invalid schemas, duplicates and oversized output. Treat subtitles/provider text as untrusted data, not tool instructions. External popularity claims require retrieved provenance rather than model recollection.
7. **Publish a completed score run** through the coordinator, then asynchronously update discovery/Meilisearch. Missing provider/AI signals stay **unavailable**, not zero; expose signal coverage and recalibrate available-signal weighting rather than silently comparing incompatible score versions.

### Node-assisted execution without SQLite contention
Proposed new capabilities: `quote_candidates` and optionally `quote_rerank_local`. Existing nodes only handle probe/clip; these require explicit protocol negotiation, tests and authorization before rollout.

- Coordinator builds bounded, immutable per-media/episode work packets and leases them to approved nodes. Nodes perform CPU normalization, dictionary matching and optional local-model inference; they do not open the server SQLite database or write Meilisearch directly.
- Packet includes local media identity, external episode mapping, cue IDs/ranges/text/times, a **dialogue revision/readiness token**, transcript hash, dictionary/corpus snapshot versions and algorithm/prompt/model versions. Cache identity includes all inputs that can change results, not just the canonical chunk ID.
- Return bounded candidate IDs/features/results. Server validates lease ownership, attempt/generation and the input revision again at publication; stale completions after reprobe/archive/remapping are rejected. A cue-range ID may be reused with changed text, so identity alone is insufficient.
- Stage chunked results with bounded writes, then atomically flip the completed-run publication pointer. Readers never see partially rescored episodes. Cancel/supersede incomplete runs safely and keep checkpoints for restart.
- Reuse the existing single-writer pool and release it between batches. Never hold a write lock across node/network/model work. Rate-limit completion payloads and apply server backpressure; adding nodes increases compute capacity, **not SQLite writers**.
- Dedicated low-priority enrichment queue/capability slots, bounded CPU/GPU use and clip/probe priority. Local inference uses no hosted-model tokens but still consumes compute and must not starve clip rendering. Keep normal ingestion and search usable with scoring disabled.

### Global token / spend guardrails
**Default: AI off.** Deterministic discovery remains useful without it. No paid calls or transcript export until an operator explicitly configures a provider, data policy and budget.

- One coordinator-owned ledger across all nodes, providers, retries, live calls and asynchronous batches. Require explicit per-run/day ceilings for input, output and—where relevant—reasoning tokens, plus a monetary ceiling using operator-configured/provider-reported pricing. Unknown pricing/usage must not silently bypass a cost limit.
- Before dispatch, reserve the bounded request's estimated input plus enforced maximum output/billable reasoning allowance; reconcile actual usage afterward. No node can spend without a valid reservation. Token estimates require a provider tokenizer or documented conservative bound; truncation happens before dispatch.
- Enforce per-episode maximum calls, candidate/context bytes/tokens, output size, concurrency and bounded repair/retry attempts. Quota exhaustion pauses/degrades to local-only results, not endless retries or an automatic expensive-model upgrade.
- Dedup by input revision + candidate hash + algorithm/dictionary/corpus + prompt/model/settings. Reuse completed outputs; skip unchanged episodes and process only changed/uncertain candidates on refresh. Prompt caching may help where supported, but is not counted as free or required for correctness.
- Keep remote LLM calls behind the server broker initially, with credentials central. Nodes can prepare work while the broker batches and budgets it. A future remote-inference node capability must use the same global reservations; N nodes must not each receive the full allowance.
- Async batch retries/restarts retain submission/item IDs, provider idempotency where supported, and reserved budget. Reconcile unknown submitted work before replaying it; cancellation does not imply no charge. Reusing work after transport uncertainty must not double-submit or double-publish.
- Dashboard/dry-run: eligible episodes, cache hits, candidate counts, projected bounded tokens/cost, reservations, actual usage, rejected/stale outputs and quality metrics. Provider log content should avoid raw transcripts and secrets.

### Suggested delivery slices
**1 — Local episode discovery, zero hosted AI:** dialogue-ready revision contract, deterministic candidates/dictionary provenance, staged score runs, episode endpoint/admin review. Keep default search ranking unchanged; evaluate a hand-reviewed, diverse sample before choosing weights.

**2 — Node compute and incremental refresh:** capability negotiation, bounded packets, fenced results, corpus snapshot cache, restart/reprobe/archive tests, global backpressure. Do not enable more writer concurrency.

**3 — Optional evidence and AI pilot:** permitted external quote source adapter, centralized AI broker/ledger, opt-in small pilot and baseline comparison. Measure ranking precision/diversity, verified known-quote coverage and tokens per newly accepted candidate; do not assume AI improves quality or costs before measuring.

**4 — Engagement and discovery UI:** explicit evidence labels, episode/show browsing, opt-in ranking boost after calibration, reversible versioned rollout. Engagement logging should aggregate safely rather than turning every UI event into a contended synchronous write.

### Added acceptance checks
- [ ] Local-only mode performs **zero hosted-model calls**, including retries and fallback paths.
- [ ] Candidate decisions resolve to current real cue spans; AI cannot invent dialogue or label unverified popularity as fact.
- [ ] Two nodes/retries cannot exceed one global reserved/actual budget; exhausted budget leaves local discovery operational.
- [ ] Reprobe, archived media, overlapping runs, remapped episodes and restarted async batches cannot publish stale/double results.
- [ ] No full transcript uploads or full-corpus scans per episode; payload/result/call/concurrency limits are enforced.
- [ ] Missing signals and uncertainty are visible; known quote, local popularity, catchphrase and suggested memorability remain distinct.
- [ ] Tests cover old-node compatibility, work-packet validation, writer pressure, token reservation reconciliation, provider failure/429s and ranking quality against a fixed evaluation set.
- [ ] No credentials, hosted AI accounts, paid searches or production queue settings are changed by this design update.

## Sources

[1] https://github.com/ss44/quipster/issues/16 — Existing metadata enrichment task
[2] https://github.com/ss44/quipster/issues/12 — Existing notable dialogue task
[9] https://github.com/thetvdb/v4-api — TheTVDB official caching, updates, scores and season ordering
[10] https://thetvdb.github.io/v4-api/swagger.yml — TheTVDB v4 OpenAPI schema
