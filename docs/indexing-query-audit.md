# Indexing integrity, diagnostics and SQLite query audit

## Scope and preserved design

This audit starts from `e2dfa6e` and reviews the incident history in #43, #47, #53,
#60 and #63, plus first-index deletion avoidance (`0940666`) and quote sampling
(`ba7a244`). No production database copy or live query-plan inspection was performed.

Kept deliberately unchanged:
- one SQLite writer, separate WAL readers, 30-second busy timeout and existing indexes;
- independent bounded cue/chunk/latch writes, including 500-ID index latch batches;
- low worker/node concurrency, server-side Meilisearch indexing and archive safeguards;
- metadata-only/offline scan walk and asynchronous cached global dialogue totals;
- first-time indexing's delete skip and exact cue-range MIN/MAX semantics.

The retry deletion defect is **not** a deliberate trade-off: retrying completed media
must not remove its documents without replacements. A full replacement must include
already-latched chunks when the prior local latch failed partway through its bounded
writes. An unchanged fully latched nonempty item is now a remote no-op; genuinely empty
replacements still clean up old remote documents. Task failure/cancellation must stop
before local latches. Read snapshots do not hold a writer across remote work.

Existing missing remote documents with already-completed local flags are not repaired
automatically. Verify affected media before an explicit operator-approved reindex.

The coherent read snapshot does not fence a concurrent reprobe generation. Replacing
the highest-ID media's chunks after document construction can reuse IDs; an old upload
can then stamp replacement rows and leave local/remote text out of sync. This vulnerability
also existed in the former pending-read/ID-latch/MarkIndexed path. It needs a separate
dialogue readiness/revision and conditional-publication design, not a larger transaction
or a claim that snapshot isolation alone solves it. Avoid reprobing the same media while
its index upload is in flight; the clean-baseline local reproduction and follow-up are
tracked in [#79](https://github.com/ss44/quipster/issues/79), separate from this retry fix.

## Reproducible query evidence

These are synthetic warm in-memory modernc SQLite comparisons on Linux amd64 /
i5-4570, not production latency measurements. `GOMAXPROCS=1`, `-cpu=1`, three repeats
of 20 operations; medians are medians of repeated ns/op averages, not request p50s.
Sibling processes were active, so small differences can be noise. Cold disk, WAL,
reader contention and actual production schema/statistics still require live evidence.

```sh
GOMAXPROCS=1 go test ./internal/store \
  -run 'TestQueryAuditPlans|TestFinishedJobsSinceUsesBoundedTimeRange' \
  -bench 'Benchmark(PendingIndexQueryAudit|FinishedJobs)' \
  -benchtime=20x -count=3 -cpu=1 -v
```

### Changed: completed-job time-range lookup

Fixture: 200,000 jobs, 190,000 done / 10,000 queued, mixed probe/index types,
distinct completion times. Lookup: `since=199000`, `limit=100`.

Before:
```text
SEARCH jobs USING INDEX idx_jobs_claim (status=?)
USE TEMP B-TREE FOR ORDER BY
```

The existing completion index begins with job type, so it cannot seek an all-types
completion-time range. Added this partial index without changing query semantics:

```sql
CREATE INDEX IF NOT EXISTS idx_jobs_done_finished
ON jobs(status, finished_at) WHERE status='done';
```

After:
```text
SEARCH jobs USING INDEX idx_jobs_done_finished (status=? AND finished_at>?)
```

| Synthetic operation | Existing indexes | With partial index |
| --- | ---: | ---: |
| Recent completed-job read | 55.506 ms | 0.450 ms |
| Finalize bounded 500-job batch | 6.590 ms | 6.832 ms |

The read improvement is large in this fixture; the completion-write difference is
roughly 3.7% and subject to measurement noise. The index added 779 database pages for
190,000 completed rows. Active rows have no entries, but writes evaluate the predicate;
completion/reopening adds/removes entries. First creation scans existing jobs at startup.
A finished_at-only partial index was rejected because the unanalysed planner did not
choose it. Existing claim/type/dedupe indexes remain.

This benefits diagnostics benchmark aggregation (`FinishedJobsSince`), **not** the normal
status collector tick; it is not proof of the cause of an observed slow status request.

### Retained: media-scoped chunks and cue timing

Fixture: 1,001 media, 1,001,000 pending chunks globally, 1,000 target-media chunks,
85,000 target-media cues, three-cue spans, limit 100. Both the existing query and an
alternate ordering seek:

```text
SEARCH m USING INTEGER PRIMARY KEY (rowid=?)
SEARCH c USING INDEX sqlite_autoindex_chunks_1 (media_file_id=?)
SEARCH q USING INDEX idx_cues_media_idx (media_file_id=? AND idx>? AND idx<?)
```

Existing ordering: 1.066 ms. Alternate ordering: 1.083 ms. No improvement, so no scoped
query rewrite or speculative chunk/cue index was added. The plan remains scoped before
and after ANALYZE. Even diagnostically forcing `idx_chunks_indexed` constrained both
`indexed_at` and `media_file_id`, not every global pending row.

Each correlated MIN/MAX is a bounded cue-index lookup. Replacing span-wide extrema
with endpoint cue times would be wrong for overlapping/non-monotonic cues. A covering
cue-time index would add storage/write work across the corpus without a demonstrated
need in this audit.

### Other inspected paths and remaining measurements

- JobStats scans a covering status/claim index for exact grouped totals. Changing that
  to approximate or cached counts needs a separate contract and measurements.
- Active-media status grouping uses the archive index and a GROUP BY temporary tree;
  no extra composite index without real cardinality/timing evidence.
- RunningJobs is status-scoped; its bounded sort is normally tiny under deliberately
  low concurrency. ListJobs uses reverse row-ID traversal with LIMIT; a plan label
  `SCAN jobs` alone does not establish an unbounded visit.
- Cue/chunk counts for job enrichment use scoped media indexes, not whole-corpus scans.
  Very large titles can still make exact counts costly.
- Exact global line totals remain demand-loaded, asynchronous, cached and cancellable.
- Production diagnosis still needs per-service RSS/limits, swap-in/out and disk latency.
  Swap occupancy alone does not prove thrashing, and pool waits do not measure all SQL
  latency. Do not raise concurrency or migrate databases from those gauges alone.

## Operational visibility

Indexer stage logs separate snapshot, build, delete/add enqueue, remote task states and
local latching. Task IDs and periodic five-second wait heartbeats make a processing
lease distinguishable from a blocked remote request. Progress is a stage estimate,
not a measured percentage of a Meilisearch task's internal work. Logs omit raw dialogue,
media paths, request bodies, credentials and remote error payloads.

The explicit orthogonal MCP `diagnostics` scope grants only sanitized `get_app_logs`
and lightweight `get_diagnostics`, without enabling queue mutation. Ordinary read/clip
tokens retain their prior permissions. Log responses preserve capture timestamps in
structured entries while maintaining legacy lines/count; escaped JSON is size-bounded.
The lightweight snapshot avoids SQL, existing slow collector locks and external probes.

## Verification approach

Regression tests use real SQLite stores and an HTTP Meilisearch test double with actual
delete effects, including duplicate jobs, mixed latches, a failure after the first 500
latch writes, empty replacements, remote failed/canceled tasks, slow HTTP, archive and
context cancellation. Query-plan assertions and benchmarks preserve result semantics.
Scope visibility, least privilege, timestamp/path/secret scrubbing and response limits
are separately tested. Full integrated test, race, coverage, lint and build results are
recorded in the implementation PR; no deployed speedup is asserted here.
