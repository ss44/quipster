# Quipster screenshots

Captured at 1920×1080. Inline shots are WebP previews — click one for full-res.
Images live in `docs/screenshots/`.

## Home page

[<img src="docs/screenshots/thumbs/home.webp" width="960" height="540" alt="Home page" loading="lazy">](docs/screenshots/home.png)

Landing view. Example quotes from your library, `/` focuses search, quoted words match exact phrases.

## Search results

[<img src="docs/screenshots/thumbs/search_results.webp" width="960" height="540" alt="Search results" loading="lazy">](docs/screenshots/search_results.png)

Query `no way!` — 1,000 hits across 19 titles. Each card: frame thumbnail, highlighted match, context lines, timecodes, Clip/download. Hits are subtitle chunks, not raw files.

<img src="docs/screenshots/feature_search_card.png" width="356" height="380" alt="A single result card" loading="lazy">

One hit, close up.

## Clip studio

[<img src="docs/screenshots/thumbs/clip_modal.webp" width="960" height="540" alt="Clip studio" loading="lazy">](docs/screenshots/clip_modal.png)

Opens on the exact moment: preview player, transcript with the match pre-selected, trimmer to include more/fewer lines. **Fast cut** = lossless stream-copy. **Precise** = exact re-encode. Copy link or download.

<img src="docs/screenshots/feature_clip_player.png" width="640" height="361" alt="Clip preview player" loading="lazy">

<img src="docs/screenshots/feature_clip_transcript.png" width="412" height="320" alt="Transcript with the matched line selected" loading="lazy">

## Admin · Library

[<img src="docs/screenshots/thumbs/admin_library.webp" width="960" height="540" alt="Admin library" loading="lazy">](docs/screenshots/admin_library.png)

Ingestion control room: stat cards, folder multi-select (empty = whole root), Scan/Rebuild/Re-probe, Pause/Stop & clear, live processing feed, recent-jobs table with filters.

<img src="docs/screenshots/feature_library_stats.png" width="1472" height="157" alt="Library stat cards" loading="lazy">

<img src="docs/screenshots/feature_library_operations.png" width="465" height="316" alt="Operations and queue control" loading="lazy">

<img src="docs/screenshots/feature_library_jobs.png" width="1472" height="719" alt="Recent jobs table" loading="lazy">

## Admin · Diagnostics

[<img src="docs/screenshots/thumbs/admin_diagnostics.webp" width="960" height="540" alt="Admin diagnostics" loading="lazy">](docs/screenshots/admin_diagnostics.png)

Live support console: active pipeline badge (VAAPI → `h264_vaapi`) with CPU-fallback count, resource gauges, cache breakdown, job benchmarks (jobs/min, P50/P95/max), codec checks, environment, troubleshooting hints. Export bundle / Copy report, secrets redacted.

<img src="docs/screenshots/feature_diagnostics_hardware.png" width="1472" height="264" alt="Hardware acceleration panel" loading="lazy">

<img src="docs/screenshots/feature_diagnostics_gauges.png" width="1472" height="132" alt="Resource gauges" loading="lazy">

## Admin · Nodes

[<img src="docs/screenshots/thumbs/admin_nodes.webp" width="960" height="540" alt="Worker nodes" loading="lazy">](docs/screenshots/admin_nodes.png)

Scale out: `quipster node --server <main> --media /path/to/media`. Node registers, you approve, it claims probe/clip jobs. Table shows status, current work, done/failed, last seen, revoke/remove.

<img src="docs/screenshots/feature_nodes_cluster.png" width="1472" height="720" alt="Cluster table" loading="lazy">
