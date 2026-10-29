<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img src="docs/assets/logo.svg" alt="Quipster logo" width="120">
  </picture>
</p>

<h1 align="center">Quipster</h1>

<p align="center">
  <strong>Search your local movie and TV library by subtitle dialogue, then clip the exact moment.</strong>
</p>

<p align="center">
  <a href="https://github.com/ss44/quipster/actions/workflows/ci.yml"><img alt="CI status" src="https://github.com/ss44/quipster/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/ss44/quipster/blob/main/LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
  <a href="https://github.com/ss44/quipster/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/ss44/quipster?sort=semver&display_name=tag"></a>
  <img alt="Coverage ≥ 85%" src="https://img.shields.io/badge/coverage-%E2%89%A585%25-brightgreen">
  <a href="https://github.com/ss44/quipster/pkgs/container/quipster"><img alt="Container image" src="https://img.shields.io/badge/ghcr.io-quipster-2496ED?logo=docker&logoColor=white"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white">
  <a href="https://github.com/ss44/quipster/graphs/commit-activity"><img alt="Maintained" src="https://img.shields.io/badge/maintained-yes-brightgreen"></a>
</p>

## Table of contents

- [Why Quipster](#why-quipster)
- [Screenshots](#screenshots)
- [Demo](#demo)
- [Features](#features)
- [How it works](#how-it-works)
- [Quickstart (Docker)](#quickstart-docker)
- [Worker nodes](#worker-nodes)
- [Configuration](#configuration)
- [API](#api)
- [MCP for AI agents](#mcp-for-ai-agents)
- [Local development](#local-development)
- [Contributing](#contributing)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)
- [License](#license)
- [Acknowledgments](#acknowledgments)
- [AI disclosure](#ai-disclosure)

## Why Quipster

- **Find the moment, not the title.** Search every spoken line of your library — sidecar or embedded English subtitles — instead of browsing filenames.
- **Clip it on demand.** Lossless fast cuts, in-browser previews, or frame-accurate re-encodes are generated on the fly. No pre-rendering, no waiting for a batch job.
- **Search by meaning.** Optional semantic (vector) search finds paraphrased dialogue when you do not remember the exact words.
- **Local-first and private.** Your media never leaves your machine. One `docker compose up` brings up the app, the Meilisearch index, and the web UI.
- **Uses your hardware.** Automatic GPU acceleration (NVENC → QuickSync → VAAPI → VideoToolbox) with a CPU fallback, so clips never fail on machines without a GPU.

## Screenshots

*Coming soon — screenshots of the landing page, search results, clip studio, and admin console will be added from a seeded demo library.*

<!--
| Search results | Clip studio |
|---|---|
| ![Search results](docs/assets/screenshots/search.png) | ![Clip studio](docs/assets/screenshots/clip-studio.png) |

| Landing page | Admin console |
|---|---|
| ![Landing page](docs/assets/screenshots/landing.png) | ![Admin console](docs/assets/screenshots/admin.png) |
-->

## Demo

*Coming soon — a short demo of search → preview → clip download.*

<!--
![Quipster demo: search dialogue and export a clip](docs/assets/demo.gif)

<sub>30s demo: search for a line, preview the clip, download in fast mode.</sub>
-->

## Features

- **Subtitle dialogue search** — sidecar `.srt`/`.vtt` (English-tagged or untagged) preferred, else
  the first English embedded text track is extracted. Files with no English track are still
  findable by title.
- **Three clip modes** — `fast` (lossless stream-copy from the nearest keyframe), `preview`
  (low-bitrate H.264 transcode that always plays in the browser), and `accurate`
  (frame-exact 720p re-encode with GPU/CPU fallback). Long accurate cuts run as async jobs.
- **English audio selection** — the English audio track is probed and every clip mode maps that
  exact stream, so multilingual releases always yield English clips.
- **Multi-folder libraries** — index specific subfolders (e.g. `movies` and `tv`) under your media
  root; unselected folders are pruned on scan. Filename parsing fills in title, year, and `SxxExx`.
- **Semantic (vector) search** — Meilisearch embedders (local HuggingFace, any OpenAI-compatible
  endpoint, or a REST service) power hybrid keyword + vector retrieval with a Keyword / Hybrid /
  Semantic toggle. See [Configuration](#configuration).
- **Queue control** — pause, resume, clear, or stop ingestion and clip jobs from the admin UI;
  pause state is persisted across restarts.
- **Diagnostics console** — live view of the encode pipeline, CPU/RAM/GPU usage, storage, job
  benchmarks, and detected environment, with a one-click export bundle and bug-report copy.
- **GPU acceleration with fallback** — hardware backends are probed at startup; without a usable
  GPU the app falls back to CPU encoding automatically. Tune with the `HWACCEL` env var.
- **Local-first, containerized** — read-only bind-mount of your media root; clips, thumbnails, and
  extracted subtitles are cached under an LRU cap.

## How it works

```text
[Vue SPA] ──HTTP──> [Go API :8080] ──> SQLite (jobs, media, cues) + Meili (lines index)
                          │
                          ├── ingestion workers: scan → ffprobe → subtitle extract/parse → chunk build → Meili batch upsert
                          └── clip workers: keyframe snap → ffmpeg cut → cache → serve

Media dir (bind-mount, read-only) ──> ingestion; cache dir (clips/thumbs) ──> LRU eviction daemon
```

- **Search unit:** cues are merged into non-overlapping chunks (≤ ~280 chars, ≤ 3 cues) indexed in
  Meilisearch (`lines` index). Hits are joined back to SQLite for exact cue timestamps. With the
  optional embedder configured, Meilisearch also generates one vector per chunk during ingestion and
  the search endpoints can run hybrid or pure-semantic queries.
- **Clips:** `fast` mode stream-copies from the previous keyframe via a single input-seek pass
  (`-c copy`, no keyframe pre-scan); `preview` mode transcodes a low-bitrate 960px H.264 clip
  (ultrafast/VAAPI) so it always plays in the browser; `accurate` mode re-encodes 720p from the
  exact timestamp with VAAPI/QuickSync and CPU `libx264` fallback. Outputs are cached
  (`sha256(mediaId|start|end|mode|pad)`) and served with HTTP Range support.
- **Metadata (MVP):** filename/folder parsing for `Movies/` + `TV Shows/` layouts (title, year, SxxExx).

## Quickstart (Docker)

1. **Prerequisite:** Docker with the compose plugin.
2. **Clone and configure:**

   ```sh
   git clone https://github.com/ss44/quipster.git && cd quipster
   cp .env.example .env
   ```

   Edit `.env` and set at least `MEDIA_DIR` (your library root, mounted read-only) and
   `MEILI_MASTER_KEY`.
3. **Start the stack:**

   ```sh
   docker compose up -d
   ```

   This pulls the app image and starts the app plus Meilisearch.
4. **Verify it is up:**

   ```sh
   curl localhost:8080/api/health
   ```

5. **Pick library folders:** open `http://localhost:8080`, go to **Admin → Library folders**, and
   select the folders to index (e.g. `movies` and `tv`; empty means the whole media root). Save,
   then click **Scan library**. Files outside the selected folders are pruned on scan.
6. **Wait for indexing, then search** a line of dialogue. The UI shows suggestions as you type.

Optional compose profiles replace the default `app` service when your hardware has a GPU:

```sh
docker compose --profile igpu up -d app-igpu    # Intel/AMD iGPU via /dev/dri (VAAPI/QSV)
docker compose --profile nvidia up -d app-nvidia # NVIDIA NVENC/NVDEC (needs NVIDIA Container Toolkit)
docker compose --profile cpu up -d app-cpu      # force software encoding
```

- **Intel/AMD (`app-igpu`)**: exposes `/dev/dri` and adds the `video`/`render` groups; the Debian
  `ffmpeg` build ships VAAPI and QuickSync support out of the box.
- **NVIDIA (`app-nvidia`)**: uses Docker's `nvidia` device driver (requires
  [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
  on the host). FFmpeg's `h264_nvenc` encoder talks to the host driver through the container
  runtime.
- **`HWACCEL` env** (default `auto`): `auto` probes all backends in priority order, `none`/`cpu`
  disables hardware acceleration, or force a specific backend (`nvenc`, `qsv`, `vaapi`,
  `videotoolbox`).

Startup logs report what was detected, e.g. `hardware acceleration: vaapi (device=/dev/dri/renderD128)`
or `hardware acceleration: none, using cpu`.

### Diagnostics

Open **Admin → Diagnostics** (`/admin/diagnostics`) for a live view of the active encode pipeline,
CPU/RAM/VRAM/GPU usage, storage, 1h/6h/24h/7d job benchmarks, and the detected environment
(FFmpeg version, codecs, drivers, effective settings). It is designed to replace `docker compose
logs` for first-line support:

- **Hardware/software badge** distinguishes `HARDWARE · NVENC/QUICKSYNC/VAAPI/VIDEOTOOLBOX` from a
  `SOFTWARE · CPU` fallback and explains a failed forced `HWACCEL` probe.
- **Runtime counters** show hardware renders vs. CPU fallbacks, so driver hiccups are visible.
- **Export bundle** downloads a ZIP (`system.json`, `metrics.json`, `benchmarks.json`, recent jobs
  and logs). Secrets are always redacted; media paths are excluded unless explicitly included.
- **Copy report** produces Markdown formatted for a bug report, and **Open logs** shows the API
  process's recent log lines in-app.

Telemetry endpoints are admin/token-guarded: `GET /api/admin/diagnostics/system`,
`/metrics`, `/benchmarks`, `/stream` (SSE), `/bundle`, `POST /probe` and `/report`, plus
`GET /api/admin/logs`. System probes are cached (five minutes) and gauge samples are shared and
cached briefly, so opening several tabs does not multiply vendor tool invocations. In a split
`ROLE=api`/`ROLE=worker` deployment the diagnostics page describes the API process; check each
worker's logs separately.

### Semantic (vector) search

Quipster can search dialogue by meaning, not just exact words. Meilisearch generates the vectors
itself: configure an embedder once and every subtitle chunk gets embedded during the normal
ingestion step (the document template embeds only the dialogue text). No extra workers, no local
model in the Go app.

```sh
# Free, fully local option (Meilisearch downloads BAAI/bge-base-en-v1.5 on first use):
EMBEDDINGS_SOURCE=huggingFace

# Any OpenAI-compatible endpoint (OpenAI, Ollama, LM Studio, vLLM ...):
EMBEDDINGS_SOURCE=openAi
EMBEDDINGS_API_KEY=sk-...            # local servers accept a placeholder value
EMBEDDINGS_URL=http://ollama:11434/v1
EMBEDDINGS_MODEL=nomic-embed-text

# Or any HTTP embedding service via the REST embedder (requires dimensions + templates):
EMBEDDINGS_SOURCE=rest
EMBEDDINGS_URL=https://embedder.example/embed
EMBEDDINGS_DIMENSIONS=768
EMBEDDINGS_REQUEST={"text":"{{text}}"}
EMBEDDINGS_RESPONSE={"embedding":"{{embedding}}"}
```

With an embedder configured, `/api/search` defaults to **hybrid** retrieval (keyword + vectors,
blend via `EMBEDDINGS_SEMANTIC_RATIO`, default `0.5`), and the results view gains a
Keyword / Hybrid / Semantic toggle. `mode=semantic` runs pure vector search. Without an embedder
everything behaves exactly as before — queries degrade to plain keyword search. Changing embedder
settings makes Meilisearch re-embed the whole index on startup; run **Admin → Reindex** after
switching models or dimensions to be safe.

## Worker nodes

One machine probing a large library can bottleneck on storage I/O and CPU. Worker nodes scale
probing and clip rendering horizontally: the same binary runs in `node` mode, talks to the main
app over HTTP, and processes jobs while the main app keeps serving search and the UI.

### Launching a node

```sh
quipster node --server http://main:8080 --media /mnt/library
```

That is the whole setup. The node registers itself, prints a "waiting for approval" line, and
stays idle until you approve it:

1. Open **Admin → Nodes** on the main app.
2. Click **Approve** next to the pending node.
3. The node receives a token (stored in its user config directory), starts claiming jobs, and
   shows up in the panel with live status, current work, and throughput.

State lives in cross-platform defaults with environment overrides, so the same command works on
Windows, macOS and Linux: identity/token in the user config dir (`~/.config/quipster/node.json`,
`%AppData%\quipster\node.json`, `~/Library/Application Support/quipster/node.json`) and scratch
cache in the user cache dir (`~/.cache/quipster`, ...).

### How paths are mapped

Nodes mount the same media library locally, but mount points can differ per node. The server
sends its media root with every claimed job; the node replaces that prefix with its own
`MEDIA_DIR`. If a node mounts only a subfolder (for example the `library` folder instead of the
whole media root), tell it explicitly:

```sh
NODE_MEDIA_MAP=/srv/media/library=/mnt/library quipster node --server http://main:8080
```

Path comparisons are case-insensitive-tolerant and separator-agnostic, so mixed
Windows/macOS/Linux clusters work.

### What nodes run

Nodes claim `probe` jobs (ffprobe + subtitle extraction, the ingestion bottleneck) and `clip`
jobs (rendering). Results stream back to the main app, which owns the database, search index and
cache. Scans and Meili indexing always run server-side. Each node needs `ffmpeg`/`ffprobe` on its
`PATH` (or `FFMPEG_BIN`/`FFPROBE_BIN`); the Docker image ships them.

### Safety

- **Approval-gated access:** a node token is issued only when you approve it; revoke any time.
  Node tokens can claim jobs but never touch admin APIs.
- **Lease-safe queue:** jobs are claimed atomically and report progress via heartbeats. A node
  that dies mid-job has its work requeued automatically (within ~1 minute), and late results from
  a requeued job are dropped instead of double-applied.
- **Pause/stop respected:** queue pause and scan exclusivity propagate to nodes on the next
  heartbeat, mirroring local worker behavior.

### Docker

```yaml
# in docker-compose.yml (included, gated behind the "node" profile)
docker compose --profile node up -d quipster-node
```

Set `NODE_SERVER_URL` to the main app's URL and mount the library the same way the app does.

## Configuration

| Env | Default | Meaning |
|---|---|---|
| `ADDR` | `:8080` | HTTP listen address |
| `ROLE` | `all` | `api`, `worker`, `all`, or `node` (worker-node mode; needs `NODE_SERVER_URL`) |
| `NODE_SERVER_URL` | — | worker-node mode: base URL of the main app (`quipster node --server` or this var) |
| `NODE_NAME` | hostname | worker-node mode: display name shown in the admin Nodes panel |
| `NODE_MEDIA_MAP` | — | worker-node mode: explicit media path mapping `serverPrefix=localPath` pairs; see [Worker nodes](#worker-nodes) |
| `NODE_HEARTBEAT` | `10s` | worker-node mode: heartbeat cadence; a node silent for ~45s is treated offline and its jobs requeued |
| `MEDIA_DIR` | `./media` | Library root (read-only bind mount) |
| `LIBRARY_PATHS` | — | Comma-separated folders under `MEDIA_DIR` to index (e.g. `movies,tv`). Seeds the persisted setting on first run |
| `DATA_DIR` | `./data` | SQLite database location |
| `CACHE_DIR` | `./cache` | Clips, thumbnails, extracted subtitles |
| `MEILI_URL` | `http://localhost:7700` | Meilisearch endpoint |
| `MEILI_MASTER_KEY` | — | Meilisearch key |
| `SCAN_INTERVAL` | off | e.g. `12h` periodic rescan |
| `SCAN_TIMEOUT` | `2h` | hard per-scan cap; `0` disables it and leaves `JOB_TIMEOUT` as the only bound |
| `SUBTITLE_WORKERS` | `4` | probe/extract/chunk worker pool |
| `EXTRACT_CONNS` | `4` | parallel embedded-subtitle extractions (each scans the whole file; tune to storage throughput) |
| `EXTRACT_TIMEOUT` | `10m` | per-embedded-subtitle extraction cap; a stalled ffmpeg is killed and the media marked `no_subs` with a note |
| `PROBE_TIMEOUT` | `60s` | per-file ffprobe cap; a stalled probe is killed and the file marked errored instead of blocking the queue |
| `JOB_TIMEOUT` | `3h` | hard per-job timeout |
| `INDEX_WORKERS` | `1` | Meili indexer pool |
| `CLIP_WORKERS` | `2` | async clip render pool |
| `CLIP_PAD_MS` | `250` | head+tail padding |
| `MAX_CLIP_SEC` | `120` | hard clip cap |
| `CLIP_PREVIEW_RES` | `640:-2` | preview width:height; even dimensions, with `-2` in one dimension to preserve aspect ratio |
| `CLIP_ACCURATE_RES` | `1280:-2` | optional precise transcode resolution; does not affect stream-copy downloads |
| `CLIP_CPU_PRESET` | mode-specific | x264 preset override; blank keeps preview `ultrafast`, precise `veryfast`; ignored for VAAPI |
| `CACHE_MAX_GB` | `10` | LRU cache cap (clips/thumbs/subs) |
| `LONG_CUT_SEC` | `20` | accurate cuts longer than this run as async jobs with progress |
| `HWACCEL` | `auto` | `auto` (probe NVENC/QSV/VAAPI/VideoToolbox), `none`/`cpu` (software only), or force a backend: `nvenc`, `qsv`, `vaapi`, `videotoolbox` |
| `API_TOKEN` | — | when set, guards `/api/admin/*` (header `X-API-Token`) |
| `MCP_ENABLED` | `false` | expose Quipster to AI agents over MCP (see below) |
| `MCP_TOKENS` | — | scoped agent tokens: `token:scopes` comma-separated, scopes are `read`, `clip`, `admin` joined by `+` (e.g. `secret1:read+clip,secret2:admin`); bare tokens default to `read` |
| `MCP_ALLOW_ADMIN` | `false` | master switch for MCP admin tools (admin-scoped tokens still required) |
| `MCP_RATE_LIMIT` | `120` | MCP requests per minute per token |
| `MCP_TRANSPORT` | `http` | `http` mounts `/mcp` on API roles; `stdio` serves MCP over stdin/stdout (replaces the HTTP server) |
| `EMBEDDINGS_SOURCE` | off | `openAi`, `huggingFace`, or `rest`; enables semantic/vector search |
| `EMBEDDINGS_URL` | — | embedder endpoint (optional for `openAi`, required for `rest`) |
| `EMBEDDINGS_API_KEY` | — | embedder API key (required for `openAi`; placeholder accepted for local servers) |
| `EMBEDDINGS_MODEL` | provider default | embedder model name |
| `EMBEDDINGS_DIMENSIONS` | — | vector size (optional for `openAi`, required for `rest`) |
| `EMBEDDINGS_DOCUMENT_TEMPLATE` | `{{doc.text}}` | Liquid template rendering the text to embed |
| `EMBEDDINGS_REQUEST` / `EMBEDDINGS_RESPONSE` | — | `rest` source JSON request/response templates |
| `EMBEDDINGS_SEMANTIC_RATIO` | `0.5` | keyword/semantic blend for hybrid queries (0 = keyword only, 1 = pure semantic) |

## API

- `GET /api/health` — also reports `semantic` (embedder configured)
- `GET /api/suggest?q=` — autocomplete (limit 8, `<mark>` highlight)
- `GET /api/search?q=&show=&page=[&mode=keyword|hybrid|semantic]` — chunk hits + media meta + cue
  timestamps; the response echoes the executed `mode` and `semanticAvailable` (hybrid/semantic fall
  back to keyword when no embedder is configured)
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
  - Nodes: `GET /api/admin/nodes`, `POST /api/admin/nodes/{id}/approve`,
    `POST /api/admin/nodes/{id}/revoke`, `DELETE /api/admin/nodes/{id}`
- Worker nodes (token issued at approval; see [Worker nodes](#worker-nodes)):
  - `POST /api/nodes/register` — register for approval (no auth; approval gates everything else)
  - `GET /api/nodes/{id}/state` — approval poll with the registration secret
  - `POST /api/nodes/claim`, `POST /api/nodes/heartbeat`
  - `POST /api/nodes/jobs/{id}/complete`, `POST /api/nodes/jobs/{id}/result` (probe/clip artifacts)

## MCP for AI agents

Quipster ships a [Model Context Protocol](https://modelcontextprotocol.io) server so AI agents can
search dialogue and cut clips directly. It is **off by default**; enable it with scoped tokens:

```sh
MCP_ENABLED=1
MCP_TOKENS=agent-secret:read+clip,ops-secret:admin
MCP_ALLOW_ADMIN=0        # 1 additionally exposes admin tools to admin-scoped tokens
MCP_RATE_LIMIT=120       # requests per minute per token (default 120)
```

Then point any streamable-HTTP MCP client at `http://<host>:8080/mcp` with
`Authorization: Bearer <token>` (or `X-API-Token`). A `read` token grants the search/status tools,
`clip` adds clip creation and thumbnails, `admin` adds queue/scan/config tools (which also require
`MCP_ALLOW_ADMIN=1`).

Local single-process integrations can run MCP over stdio instead of HTTP:

```sh
MCP_ENABLED=1 MCP_TRANSPORT=stdio quipster
```

### Tools

| Tool | Scope | Purpose |
|---|---|---|
| `get_processing_status` | read | queue, scan, index and media counts in one snapshot |
| `search_quotes` | read | full-text dialogue search with metadata and timestamps |
| `suggest_quotes` | read | autocomplete candidates for a partial line |
| `check_quote_exists` | read | cheap existence probe (5 sample hits) |
| `get_scene_by_line` | read | expand a chunk (by `chunkId` or `mediaId` + `startSec`) into its scene with context cues |
| `list_media`, `get_media` | read | library metadata (filesystem paths are redacted) |
| `get_job` | read | poll one processing job |
| `create_clip` | clip | render or queue a clip; returns a ready URL or a `jobId` to poll |
| `get_thumbnail` | clip | thumbnail URL for a timestamp |
| `trigger_scan`, `reindex_library`, `queue_control`, `list_jobs`, `get_library_config`, `set_library_config` | admin | the operator surface |

Resources (`quipster://media/{id}`, `quipster://chunk/{chunkId}`, `quipster://thumb/{mediaId}`,
`quipster://clip/{cacheKey}`) expose the same data as compact JSON, and prompts
(`find_and_clip_scene`, `summarize_show_quotes`, `audit_processing_queue`) encode safe multi-step
workflows. Every tool call is rate-limited per token and audited with the token's hashed id.

## Local development

### Prerequisites

- Go 1.27+
- Node.js 22+ (web UI)
- FFmpeg + ffprobe on `PATH`
- Meilisearch v1.x reachable at `MEILI_URL` — or run it via Docker (see below)

### Run

The easiest way to run the full stack locally with hot-reloading is the included dev script. It
spins up an ephemeral Meilisearch container, generates dummy media test data, runs the Go backend,
and launches the Vite frontend server. Stopping it (`Ctrl+C`) cleans up background processes and
containers automatically.

```sh
./dev.sh
```

Manual commands instead:

```sh
docker compose up -d meilisearch           # or point MEILI_URL at an existing instance
cp .env.example .env
go run ./cmd/quipster                      # API + workers in one process on :8080
(cd web && npm install && npm run dev)     # Vite on :5173, proxies /api → :8080
sh testdata/make_fixtures.sh               # small fixture library (sidecar/embedded/no-subs)
```

The built SPA (`web/dist`) is embedded into the Go binary via `go:embed`; rebuild the web app
before `go build` after frontend changes.

The built SPA (`web/dist`) is embedded into the Go binary via `go:embed`; rebuild the web app
before `go build` after frontend changes.

Set `VITE_SITE_URL` (for example `https://quipster.example.com`) when building the web app — or pass
it as a `--build-arg` to `docker build` — so `og:image`/`twitter:image` are emitted as absolute URLs.
When it is unset the paths stay relative, which browsers render but social crawlers ignore.

### Test, lint, and coverage

```sh
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | grep total   # must be >= 85.0%
golangci-lint run
(cd web && npm run build)
```

The pre-commit hook (`.githooks/pre-commit`, enabled via `git config core.hooksPath .githooks`)
runs the linter and enforces the coverage gate.

## Contributing

Contributions are welcome. Fork the repo, create a branch, and open a pull request against
`main`. Before you push:

1. `go test ./...` must pass, `golangci-lint run` must be clean, and total statement coverage must
   stay **>= 85%** — the pre-commit hook rejects commits that fail any of these. Write new unit
   tests if coverage drops.
2. Follow the commit message convention (see below); the linter enforces the format.
3. Keep pull requests focused and small. See [`AGENTS.md`](AGENTS.md) for the full ruleset used
   by AI agents and humans alike, including
   [good first issue](https://github.com/ss44/quipster/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
   and help-wanted tickets.

### Commit convention

Commits use `<DOMAIN>: <summary>` — an uppercase domain from the list below, an imperative summary
starting with a lowercase letter, and no trailing period:

| Domain | Scope |
|---|---|
| `BACKEND` | Go backend services, API router, data pipelines, SQLite store |
| `ADMIN` | Administration features, queue control, multi-folder library settings, ops endpoints |
| `UI` | Frontend Vue3/Vuetify3 components, design system, client-side logic |
| `CLIPS` | FFmpeg processing, thumbnail extraction, keyframe snapping, video/audio mapping, caching |
| `SEARCH` | Meilisearch indexing, document ingestion, search APIs |
| `INFRA` | Infrastructure, Dockerfiles, Docker Compose, GitHub Actions (CI) |
| `DOCS` | READMEs, markdown documentation, licensing |

### Coding standards

- SOLID, readable, decoupled packages; documented exported identifiers (Effective Go).
- Consistent terminology: a **clip** is a generated video segment (never "cut" in user-facing
  copy), a **library folder** is a folder selected under the media root.

## Troubleshooting

### Slow previews on network storage

Previews default to a small 640px-wide H.264 transcode. Downloads default to **Fast cut**,
which copies the original video/audio without quality loss, but starts at a keyframe rather
than an exact frame. **Precise** remains an explicit re-encoding option for exact timing.
Changing transcode settings creates new cache identities; existing files expire via the LRU.

Lowering preview resolution reduces scaling/encoding work and output size, not the source
resolution that must be decoded or necessarily the bytes read over Samba. Embedded subtitle
extraction can scan the full movie and compete with interactive requests. Try `EXTRACT_CONNS=1`
on slower shares, and keep `CACHE_DIR` and `DATA_DIR` on local storage when possible.

On the **deployment host**, while opening a preview, use these diagnostics (some tools may
need installation or elevated access):

```sh
docker compose stats --no-stream
docker compose top app
vmstat 1 10
iostat -xz 1 10
sar -n DEV 1 10
sudo intel_gpu_top
docker compose logs --since 5m app
```

Look for CPU saturation, swap activity, device latency, and busy GPU Video engines.
Docker CPU percentages may exceed 100% across cores; its I/O counters are cumulative.
Host-mounted Samba traffic may not appear in container network counters or local disk stats;
use host network rates and CIFS statistics for the mounted share rather than treating zero
container network I/O as proof the app is idle. Low CPU alone does not prove an I/O bottleneck.

### `failed to create mount source path ... mkdir /mnt/media-library: file exists`

Docker's daemon runs as root and cannot see or enter FUSE mounts (sshfs/rclone) created by your
user session — by default only the mounting user may access them. Docker then tries to create the
missing bind source itself and fails. Fix the mount so root can access it, then mount the parent
root (not a subfolder) and use the library-folders setting:

```sh
sudo sed -i 's/^#user_allow_other/user_allow_other/' /etc/fuse.conf
fusermount -u /mnt/media-library
sshfs -o ro,allow_other,default_permissions nas.example:/media/storage /mnt/media-library
```

Persist it system-wide (requires the `allow_other` line above), e.g. in `/etc/fstab`:

```ini
nas.example:/media/storage /mnt/media-library fuse.sshfs ro,allow_other,default_permissions,IdentityFile=~/.ssh/id_ed25519,_netdev,nofail,x-systemd.automount 0 0
```

A kernel NFS mount is preferable to sshfs for throughput if the server exports it. The compose
file uses `create_host_path: false`, so Docker will report a missing library path instead of
silently creating host directories.

### Notes & limits (MVP)

- Probing is bounded so file size cannot stall ingestion: ffprobe runs with a 10-second analysis
  window and a 50 MiB probe budget, and is killed after `PROBE_TIMEOUT` (default 60s); pathological
  files (e.g. missing `moov`/cues index) are marked errored and skipped instead of blocking the queue.
- Scans are metadata-only and exclusive: while a scan walks the library, in-flight probes are
  canceled/requeued and new probes wait, so full-file subtitle extraction cannot starve the scan on
  Samba/NFS. Media upserts, probe enqueueing, and prune deletes are batched, and the scanner logs
  heartbeat progress plus the current path every couple of seconds.
- A scan never prunes when any directory or file stat failed; a transient network error leaves the
  existing rows in place and is reported in the log instead of deleting a chunk of the library.
- Subtitle indexing is English-only for now; see [Roadmap](#roadmap) for what is next.
- mp4 stream-copy falls back to Matroska output when codecs are not mp4-compatible.
- The Meili index is derived state — `POST /api/admin/reindex` rebuilds it from SQLite.

## Roadmap

- [ ] More subtitle sources: OpenSubtitles download and burned-in subtitle support
- [ ] Metadata enrichment via TMDB
- [ ] Share links for generated clips
- [ ] Languages beyond English subtitle indexing
- [ ] Split the API reference and troubleshooting notes into `docs/` if the README keeps growing

## License

[MIT](LICENSE)

## Acknowledgments

- [Meilisearch](https://www.meilisearch.com/) — the dialogue index
- [FFmpeg](https://ffmpeg.org/) — probing, subtitle extraction, and clip rendering
- [chi](https://github.com/go-chi/chi) router and [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) pure-Go driver
- [Vue 3](https://vuejs.org/) + [Vuetify 3](https://vuetifyjs.com/) for the SPA
- [Kilo Code](https://kilo.ai/) — AI-assisted development (see [AI disclosure](#ai-disclosure))

## AI disclosure

This project was created with AI assistance. The code, tests, documentation, and design were
generated and iterated on with **Kilo Code** (an AI coding agent) under human direction and
review. AI-generated output has not been independently audited; review the code before relying
on it in production or exposing it to untrusted networks.
