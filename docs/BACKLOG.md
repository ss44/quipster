# Planned episode metadata and quote discovery

Design drafts for existing open tasks; **not implemented or enabled**.

| Existing task | Expanded specification |
| --- | --- |
| [#16 — TMDB / TVDB metadata enrichment](https://github.com/ss44/quipster/issues/16) | [Episode metadata enrichment](plans/episode-metadata-enrichment.md) |
| [#12 — Notable dialogue detection and scoring](https://github.com/ss44/quipster/issues/12) | [Episode quote discovery and bounded-cost execution](plans/episode-quote-discovery.md) |

These drafts incorporate the existing issue discussions: keep offline filename parsing, avoid provider calls in scan/write paths, and publish scores only for a completed dialogue revision. They are proposed issue expansions, not a claim that the issue bodies have been updated.

## Recommended sequence
1. Optional metadata identity, provenance, episode ordering and manual correction; cache/batch series and season lookups.
2. Local episode-level notable-quote candidates and a separately published discovery read model, with no hosted AI required.
3. Capability-negotiated node computation, coordinator-only persistence, stale-result rejection and incremental refresh.
4. Opt-in evidence retrieval / AI reranking of a small shortlist under a **single global reserved token/spend budget**, followed by engagement-backed discovery.

Keep externally evidenced quotes, local popularity, recurring phrases and suggested memorability separately labeled. Adding nodes must not multiply provider quotas, AI allowances or SQLite writers.

## Publication note
The configured GitHub token returned HTTP 403 when attempting to update existing issues #16 and #12. Preserve these expansions here for review; an authorized maintainer can apply them to the issues. This planning change must not close either implementation task.
