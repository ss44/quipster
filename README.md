# Quipster

Search a local movie/TV library by subtitle dialogue and generate shareable clips on the fly.
Fully containerized, web UI, no pre-rendered clips.

## How it works

```
[Vue SPA] ──HTTP──> [Go API :8080] ──> SQLite (jobs, media, cues) + Meili (lines index)
                          │
                          ├── ingestion workers: scan → ffprobe → subtitle extract/parse → chunk build → Meili batch upsert
                          └── clip workers: keyframe snap → ffmpeg cut → cache → serve

Media dir (bind-mount, read-only) ──> ingestion; cache dir (clips/thumbs) ──> LRU eviction daemon
```

- **Search unit:** cues are merged into non-overlapping chunks (≤ ~280 chars, ≤ 3 cues) indexed in
  Meilisearch (`lines` index). Hits are joined back to SQLite for exact cue timestamps.
- **Clips:** `fast` mode stream-copies from the previous keyframe via a single input-seek pass
  (`-c copy`, no keyframe pre-scan); `preview` mode transcodes a low-bitrate 960px H.264 clip
  (ultrafast/VAAPI) so it always plays in the browser; `accurate` mode re-encodes 720p from the
  exact timestamp with VAAPI/QuickSync and CPU `libx264` fallback. Outputs are cached
  (`sha256(mediaId|start|end|mode|pad)`) and served with HTTP Range support.
- **Subtitles (MVP):** sidecar `.srt`/`.vtt` (English-tagged or untagged) preferred, else the first
  English embedded text track is extracted. No English track → status `no_subs`, still findable by title.
- **Audio:** the English-tagged audio track is selected during probe (default disposition preferred,
  commentary/audio-description tracks avoided) and all clip modes map that exact stream, so
  multilingual releases always yield English clips. `POST /api/admin/reprobe` re-runs selection for
  already-processed files.
- **Metadata (MVP):** filename/folder parsing for `Movies/` + `TV Shows/` layouts (title, year, SxxExx).

## Quickstart (Docker)

```sh
cp .env.example .env            # set MEDIA_DIR (read-only) and MEILI_MASTER_KEY
docker compose up -d              # pulls app image and starts app + meilisearch
```

Open `http://localhost:8080`, go to **Admin → Library folders**, pick the folders to index
(e.g. `movies` and `tv`; empty means the whole media root). Save, then **Scan library**. Files
outside the selected folders are pruned on scan. Wait for indexing, then search dialogue.

Scale-out workers and Intel/AMD iGPU (VAAPI) passthrough:

```sh
docker compose --profile workers up -d worker   # extra ingestion/clip workers
docker compose --profile gpu up -d app-gpu      # /dev/dri passthrough variant
```

## Configuration

| Env | Default | Meaning |
|---|---|---|
| `ADDR` | `:8080` | HTTP listen address |
| `ROLE` | `all` | `api`, `worker`, or `all` (single binary does both) |
| `MEDIA_DIR` | `./media` | Library root (read-only bind mount) |
| `LIBRARY_PATHS` | — | Comma-separated folders under `MEDIA_DIR` to index (e.g. `movies,tv`). Seeds the persisted setting on first run |
| `DATA_DIR` | `./data` | SQLite database location |
| `CACHE_DIR` | `./cache` | Clips, thumbnails, extracted subtitles |
| `MEILI_URL` | `http://localhost:7700` | Meilisearch endpoint |
| `MEILI_MASTER_KEY` | — | Meilisearch key |
| `SCAN_INTERVAL` | off | e.g. `12h` periodic rescan |
| `SUBTITLE_WORKERS` | `4` | probe/extract/chunk worker pool |
| `EXTRACT_CONNS` | `4` | parallel embedded-subtitle extractions (each scans the whole file; tune to storage throughput) |
| `PROBE_TIMEOUT` | `60s` | per-file ffprobe cap; a stalled probe is killed and the file marked errored instead of blocking the queue |
| `JOB_TIMEOUT` | `3h` | hard per-job timeout |
| `INDEX_WORKERS` | `1` | Meili indexer pool |
| `CLIP_WORKERS` | `2` | async clip render pool |
| `CLIP_PAD_MS` | `250` | head+tail padding |
| `MAX_CLIP_SEC` | `120` | hard clip cap |
| `CACHE_MAX_GB` | `10` | LRU cache cap (clips/thumbs/subs) |
| `LONG_CUT_SEC` | `20` | accurate cuts longer than this run as async jobs with progress |
| `API_TOKEN` | — | when set, guards `/api/admin/*` (header `X-API-Token`) |

## API

- `GET /api/health`
- `GET /api/suggest?q=` — autocomplete (limit 8, `<mark>` highlight)
- `GET /api/search?q=&show=&page=` — chunk hits + media meta + cue timestamps
- `GET /api/media`, `GET /api/media/{id}`
- `GET /api/thumb/{mediaId}?t=&w=` — JPEG, 5s-bucketed cache
- `GET /api/chunks/{mediaId}:{startCueIdx}-{endCueIdx}` — chunk + cues for the clip modal
- `GET /api/clips/stream?mediaId=&start=&end=&padMs=&mode=fast|preview|accurate[&download=1]`
  - Range-capable; long accurate cuts return `202 {jobId}` and progress via SSE/admin jobs
- Admin (`X-API-Token` when configured):
  - `GET/PUT /api/admin/config` — read/update the persisted library folders (`{"folders":["movies","tv"]}`)
  - `POST /api/admin/scan[?root=subdir]` — scans the configured folders, or one ad-hoc folder
  - `POST /api/admin/reindex` — rebuild the Meili index from SQLite
  - `POST /api/admin/reprobe` — re-run probe/audio-subtitle selection for processed files
  - Queue control: `POST /api/admin/queue/pause`, `POST /api/admin/queue/resume`,
    `POST /api/admin/queue/clear` (drop queued jobs), `POST /api/admin/queue/stop`
    (pause + abort in-flight jobs + clear queued). Pause state is persisted and workers in any
    process stop claiming/cancel within a second.
  - `GET /api/admin/status`, `GET /api/admin/events` (SSE, 1s tick), `GET /api/admin/jobs`

## Development

The easiest way to run the full stack locally with hot-reloading is using the included dev script. It will automatically spin up an ephemeral Meilisearch container, generate dummy media test data, run the Go backend, and launch the Vite frontend server.

```sh
./dev.sh
```

When you stop the script (`Ctrl+C`), it cleans up the background processes and containers automatically.

Manual commands:

```sh
(cd web && npm install && npm run dev)      # Vite dev server proxies /api → :8080
go run ./cmd/quipster                        # API + workers in one process
go test ./internal/...                       # unit tests
sh testdata/make_fixtures.sh                 # small fixture library (sidecar/embedded/no-subs)
```

The built SPA (`web/dist`) is embedded into the Go binary via `go:embed`; rebuild the web app
before `go build` after frontend changes.

## Notes & limits (MVP)

- Probing is bounded so file size cannot stall ingestion: ffprobe runs with a 10-second analysis
  window and a 50 MiB probe budget, and is killed after `PROBE_TIMEOUT` (default 60s); pathological
  files (e.g. missing `moov`/cues index) are marked errored and skipped instead of blocking the queue.
- English-only subtitle indexing; OpenSubtitles download, TMDB enrichment, burned-in subs and
  share links are phase 2.
- mp4 stream-copy falls back to Matroska output when codecs are not mp4-compatible.
- The Meili index is derived state — `POST /api/admin/reindex` rebuilds it from SQLite.
- VAAPI is probed at startup; without `/dev/dri` the app uses CPU encoding automatically.

## Troubleshooting

**`failed to create mount source path ... mkdir /mnt/plex-library: file exists`**

Docker's daemon runs as root and cannot see or enter FUSE mounts (sshfs/rclone) created by your
user session — by default only the mounting user may access them. Docker then tries to create the
missing bind source itself and fails. Fix the mount so root can access it, then mount the parent
root (not a subfolder) and use the library-folders setting:

```sh
sudo sed -i 's/^#user_allow_other/user_allow_other/' /etc/fuse.conf
fusermount -u /mnt/plex-library
sshfs -o ro,allow_other,default_permissions bigrig:/media/plex-storage-new /mnt/plex-library
```

Persist it system-wide (requires the `allow_other` line above), e.g. in `/etc/fstab`:

```
bigrig:/media/plex-storage-new /mnt/plex-library fuse.sshfs ro,allow_other,default_permissions,IdentityFile=/home/shinda/.ssh/id_rsa,_netdev,nofail,x-systemd.automount 0 0
```

A kernel NFS mount is preferable to sshfs for throughput if the server exports it. The compose
file uses `create_host_path: false`, so Docker will report a missing library path instead of
silently creating host directories.

## License

[MIT](LICENSE)

## AI disclosure

This project was created with AI assistance. The code, tests, documentation, and design were
generated and iterated on with **Kilo Code** (an AI coding agent) under human direction and
review. AI-generated output has not been independently audited; review the code before relying
on it in production or exposing it to untrusted networks.
