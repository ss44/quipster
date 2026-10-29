# Quipster screenshots

Captured from a live instance at 1920×1080. The condensed gallery lives in the
[README](README.md#screenshots); this page walks through every screen (and feature close-ups)
in more detail. All images live under `docs/screenshots/`. Inline screenshots are downsized
WebP previews — click one to open the full-resolution PNG.

## Home page

[<img src="docs/screenshots/thumbs/home.webp" width="960" height="540" alt="Home page" loading="lazy">](docs/screenshots/home.png)

The landing view doubles as a search launcher: example dialogue quotes are pulled live from the
indexed library, `/` focuses the search box, and wrapping words in `"quotes"` matches an exact
phrase. The header links jump straight into the admin console.

## Search results

[<img src="docs/screenshots/thumbs/search_results.webp" width="960" height="540" alt="Search results" loading="lazy">](docs/screenshots/search_results.png)

A query for `"no way!"` matches 1,000 subtitle chunks across 19 titles. Results are subtitle
*chunks* (a few merged lines), not raw files: each card shows the frame thumbnail with clip
duration, show title plus season/episode or year, the matched line with highlighted terms, the
surrounding context lines, exact timecodes, and inline **Clip** / download actions. A
"Matches by title" strip groups hits per title below the grid, and pagination carries the rest.

<img src="docs/screenshots/feature_search_card.png" width="356" height="380" alt="A single result card" loading="lazy">

Close-up of one hit: highlighted match, context lines, timecodes, and the Clip/download actions.

## Clip studio

[<img src="docs/screenshots/thumbs/clip_modal.webp" width="960" height="540" alt="Clip studio" loading="lazy">](docs/screenshots/clip_modal.png)

Clicking any hit opens the clip studio on that exact moment. The left column holds an in-browser
preview player of the cut; the right column holds the transcript around the hit with the matched
line pre-selected. Below the player, the clip-range trimmer ("drag to include more or fewer
subtitle lines", with **Select all**) sets which lines are included, then the output section
offers **Fast cut** (lossless stream-copy from the previous keyframe) versus **Precise**
(frame-exact re-encode), plus **Copy link** and **Download**.

<img src="docs/screenshots/feature_clip_player.png" width="640" height="361" alt="Clip preview player" loading="lazy">

<img src="docs/screenshots/feature_clip_transcript.png" width="412" height="320" alt="Transcript with the matched line selected" loading="lazy">

## Admin · Library

[<img src="docs/screenshots/thumbs/admin_library.webp" width="960" height="540" alt="Admin library" loading="lazy">](docs/screenshots/admin_library.png)

The ingestion control room. Stat cards track library files, indexed dialogue lines, the job
queue, and clip-cache usage, each with a progress bar. **Library folders** multi-selects which
subfolders under the media root are indexed (empty means the whole root; anything unselected is
pruned on scan). **Operations** offers Scan library, Rebuild search index, and Re-probe processed
files; **Queue control** can pause workers or stop and clear everything queued. **Processing
now** streams live per-file progress, and **Recent jobs** lists every job with status, line
counts, durations, and errors.

<img src="docs/screenshots/feature_library_stats.png" width="1472" height="157" alt="Library stat cards" loading="lazy">

<img src="docs/screenshots/feature_library_operations.png" width="465" height="316" alt="Operations and queue control" loading="lazy">

<img src="docs/screenshots/feature_library_jobs.png" width="1472" height="719" alt="Recent jobs table" loading="lazy">

## Admin · Diagnostics

[<img src="docs/screenshots/thumbs/admin_diagnostics.webp" width="960" height="540" alt="Admin diagnostics" loading="lazy">](docs/screenshots/admin_diagnostics.png)

A live support console meant to replace `docker compose logs` for first-line triage. The
hardware-acceleration badge names the active pipeline (here VAAPI → `h264_vaapi` on
`/dev/dri/renderD128`) and counts CPU fallbacks when hardware renders fail. Gauges chart CPU,
process CPU, RAM, VRAM, GPU, and swap; **Storage & cache** breaks down cache usage; the
**Performance** panel benchmarks job throughput (jobs/min, MB/s, P50/P95/max per job type over
1h/6h/24h/7d). Media optimization checks flag codec, bitrate, and frame-rate quirks; the
environment section lists FFmpeg/ffprobe versions, codec capabilities, kernel, and runtime;
troubleshooting hints turn repeated failures into actionable advice. One-click **Export bundle**
and **Copy report** hand support everything they need, with secrets redacted.

<img src="docs/screenshots/feature_diagnostics_hardware.png" width="1472" height="264" alt="Hardware acceleration panel" loading="lazy">

<img src="docs/screenshots/feature_diagnostics_gauges.png" width="1472" height="132" alt="Resource gauges" loading="lazy">

## Admin · Nodes

[<img src="docs/screenshots/thumbs/admin_nodes.webp" width="960" height="540" alt="Worker nodes" loading="lazy">](docs/screenshots/admin_nodes.png)

Horizontal scaling for large libraries: the same binary runs
`quipster node --server <main> --media /path/to/media`, registers itself, and stays pending until
you approve it. Approved nodes claim probe and clip jobs over HTTP while the main app keeps
serving search and the UI. The cluster table shows each node's online status, current work with
per-job progress, done/failed counts, last-seen time, and Revoke/Remove actions.

<img src="docs/screenshots/feature_nodes_cluster.png" width="1472" height="720" alt="Cluster table" loading="lazy">
