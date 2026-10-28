package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"quipster/internal/api"
	"quipster/internal/cache"
	"quipster/internal/clips"
	"quipster/internal/diagnostics"
	"quipster/internal/events"
	"quipster/internal/hardware"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/logbuf"
	"quipster/internal/probe"
	"quipster/internal/scan"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
	"quipster/web"
)

//nolint:govet // field grouping favors readability over optimal alignment.
type config struct {
	addr           string
	role           string
	dataDir        string
	mediaDir       string
	cacheDir       string
	meiliURL       string
	meiliKey       string
	apiToken       string
	previewRes     string
	accurateRes    string
	cpuPreset      string
	subWorkers     int
	indexWorkers   int
	clipWorkers    int
	scanIntvl      time.Duration
	padMs          int
	maxClipSec     float64
	cacheMaxGB     float64
	longCutSec     float64
	probeTimeout   time.Duration
	scanTimeout    time.Duration
	extractTimeout time.Duration
	logs           *logbuf.Buffer
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

func loadConfig() config {
	return config{
		addr:           envStr("ADDR", ":8080"),
		role:           strings.ToLower(envStr("ROLE", "all")),
		dataDir:        envStr("DATA_DIR", "./data"),
		mediaDir:       envStr("MEDIA_DIR", "./media"),
		cacheDir:       envStr("CACHE_DIR", "./cache"),
		meiliURL:       envStr("MEILI_URL", "http://localhost:7700"),
		meiliKey:       envStr("MEILI_MASTER_KEY", ""),
		scanIntvl:      parseDuration(envStr("SCAN_INTERVAL", "")),
		subWorkers:     envInt("SUBTITLE_WORKERS", 4),
		indexWorkers:   envInt("INDEX_WORKERS", 4),
		clipWorkers:    envInt("CLIP_WORKERS", 2),
		apiToken:       os.Getenv("API_TOKEN"),
		padMs:          envInt("CLIP_PAD_MS", 250),
		maxClipSec:     envFloat("MAX_CLIP_SEC", 120),
		cacheMaxGB:     envFloat("CACHE_MAX_GB", 10),
		longCutSec:     envFloat("LONG_CUT_SEC", 20),
		probeTimeout:   parseDuration(envStr("PROBE_TIMEOUT", "")),
		scanTimeout:    parseDuration(envStr("SCAN_TIMEOUT", "2h")),
		extractTimeout: parseDuration(envStr("EXTRACT_TIMEOUT", "10m")),
		previewRes:     envStr("CLIP_PREVIEW_RES", ""),
		accurateRes:    envStr("CLIP_ACCURATE_RES", ""),
		cpuPreset:      envStr("CLIP_CPU_PRESET", ""),
	}
}

func parseDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second
	}
	return 0
}

// registerJobHandlers wires the media-ingestion and clip handlers into the
// job runner. Worker pools stay I/O-tuned: subtitle extraction is NFS-bound,
// so its pool size is the throughput lever.
func registerJobHandlers(cfg config, st *store.Store, runner *jobs.Runner, ix *indexer.Indexer, clipSvc *clips.Service) {
	onDeleted := func(ids []int64) {
		if err := ix.DeleteMedia(ids); err != nil {
			log.Printf("delete media docs %v: %v", ids, err)
		}
	}
	// A scan is metadata-only and must not compete with full-file subtitle
	// extraction on a network share. The gate makes scans exclusive: in-flight
	// probes are canceled and requeued, and new probes wait for the walk.
	gate := jobs.NewGate(runner)
	runner.Register("scan", 1, gate.Scan(scan.JobHandler(cfg.mediaDir, st, onDeleted, scan.WithTimeout(cfg.scanTimeout))))
	pipeline := &probe.Pipeline{
		St:             st,
		SubsCacheDir:   filepath.Join(cfg.cacheDir, "subs"),
		ExtractConns:   envInt("EXTRACT_CONNS", 4),
		ProbeTimeout:   cfg.probeTimeout,
		ExtractTimeout: cfg.extractTimeout,
	}
	runner.Register("probe", cfg.subWorkers, gate.Probe(pipeline.JobHandler()))
	runner.Register("index", cfg.indexWorkers, ix.JobHandler())
	runner.Register("clip", cfg.clipWorkers, clipSvc.JobHandler())
}

// app bundles the wired services a running quipster process needs. Keeping
// assembly separate from main() makes the whole wiring testable without
// starting an HTTP server or waiting for a signal.
type app struct {
	Runner *jobs.Runner
	Stop   func()
	Deps   api.Deps
}

// assembleApp wires every service around the shared store: clip/thumbnail
// caches, the search stack, the job runner with role-based handlers, the LRU
// eviction daemon, and the periodic scan ticker. It owns a cancellable child
// of parent for background work; the returned Stop func cancels it and stops
// the runner, and must be called on shutdown.
func assembleApp(parent context.Context, cfg config, st *store.Store, ix *indexer.Indexer) *app {
	ctx, cancel := context.WithCancel(parent)
	lru := &cache.LRU{St: st, MaxBytes: int64(cfg.cacheMaxGB * 1024 * 1024 * 1024), Kinds: []string{"clip", "thumb", "sub"}}
	cache.RunDaemon(ctx, lru, 5*time.Minute)

	// Probe hardware acceleration once at startup so clip and thumbnail
	// pipelines can use NVENC/QSV/VAAPI/VideoToolbox when available. The
	// monitor also records runtime fallbacks for the diagnostics view.
	hw := hardware.NewMonitor(probe.FfmpegBin())
	if caps := hw.Capabilities(); caps.Available() {
		log.Printf("hardware acceleration: %s (device=%s)", caps.Backend, caps.Device)
	} else {
		log.Printf("hardware acceleration: none, using cpu")
	}

	clipSvc := clips.New(st, clips.Config{
		CacheDir:     cfg.cacheDir,
		MaxClipSec:   cfg.maxClipSec,
		DefaultPadMs: cfg.padMs,
		LongCutSec:   cfg.longCutSec,
		PreviewRes:   cfg.previewRes,
		AccurateRes:  cfg.accurateRes,
		CPUPreset:    cfg.cpuPreset,
		Hw:           hw,
	})
	thumbSvc := &thumbs.Service{St: st, CacheDir: cfg.cacheDir, Hw: hw}
	searchSvc := search.New(st, ix)
	collector := &events.Collector{St: st, MeiliUp: ix.Healthy}
	diag := diagnostics.New(st, hw, cfg.logs, ix.Healthy, version, cfg.dataDir, cfg.cacheDir, cfg.mediaDir)

	runner := jobs.NewRunner(st, "worker")
	runner.SetJobTimeout(parseDuration(envStr("JOB_TIMEOUT", "3h")))
	if cfg.role == "all" || cfg.role == "worker" {
		registerJobHandlers(cfg, st, runner, ix, clipSvc)
	}

	if cfg.scanIntvl > 0 && (cfg.role == "all" || cfg.role == "api") {
		go scanTicker(ctx, st, cfg.scanIntvl)
	}

	deps := api.Deps{
		St:          st,
		Search:      searchSvc,
		Indexer:     ix,
		Clips:       clipSvc,
		Thumbs:      thumbSvc,
		Events:      collector,
		Runner:      runner,
		MediaDir:    cfg.mediaDir,
		WebFS:       web.Dist,
		APIToken:    cfg.apiToken,
		Diagnostics: diag,
	}
	return &app{Deps: deps, Runner: runner, Stop: func() {
		cancel()
		runner.Stop()
	}}
}

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := loadConfig()
	cfg.logs = logbuf.New(0)
	log.SetOutput(io.MultiWriter(os.Stderr, cfg.logs))
	log.Printf("quipster starting: role=%s addr=%s media=%s data=%s cache=%s meili=%s",
		cfg.role, cfg.addr, cfg.mediaDir, cfg.dataDir, cfg.cacheDir, cfg.meiliURL)

	if err := scan.EnsureDirs(
		cfg.dataDir,
		cfg.cacheDir,
		filepath.Join(cfg.cacheDir, "clips"),
		filepath.Join(cfg.cacheDir, "thumbs"),
		filepath.Join(cfg.cacheDir, "subs"),
	); err != nil {
		log.Fatalf("dirs: %v", err)
	}

	st, err := store.Open(filepath.Join(cfg.dataDir, "quipster.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("close store: %v", cerr)
		}
	}()
	if err := st.Recover(); err != nil {
		log.Printf("recover jobs: %v", err)
	}
	seedLibraryPaths(st)

	// Semantic search is optional: an invalid embedder configuration logs a
	// clear error and stays disabled rather than aborting startup.
	emb, embErr := indexer.ParseEmbedderConfig(os.Getenv)
	if embErr != nil {
		log.Printf("semantic search disabled: %v", embErr)
	} else if emb.Enabled() {
		log.Printf("semantic search: enabled (source=%s model=%s hybridRatio=%.2f)",
			emb.Source, emb.Model, emb.SemanticRatio) //nolint:gosec // operator-supplied env, logged locally only
	} else {
		log.Printf("semantic search: disabled (set EMBEDDINGS_SOURCE to enable)")
	}

	ix := indexer.New(st, cfg.meiliURL, cfg.meiliKey, indexer.WithEmbedder(emb))
	go ensureSettingsLoop(ix, 15*time.Second)

	application := assembleApp(context.Background(), cfg, st, ix)

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.Router(application.Deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if cfg.role == "all" || cfg.role == "api" {
		go func() {
			log.Printf("http listening on %s", cfg.addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("http: %v", err)
			}
		}()
	} else {
		log.Printf("worker mode: processing jobs, no http server")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
	shCtx, shCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	application.Stop()
}

// seedLibraryPaths seeds the persisted library-folders setting from the
// LIBRARY_PATHS env var on first run only: an already-persisted setting (even
// an empty one chosen in the admin UI) always wins over the environment.
func seedLibraryPaths(st *store.Store) {
	if _, ok, err := st.GetSetting(store.SettingLibraryPaths); err != nil || ok {
		if err != nil {
			log.Printf("read library paths setting: %v", err)
		}
		return
	}
	v := strings.TrimSpace(os.Getenv("LIBRARY_PATHS"))
	if v == "" {
		return
	}
	roots, err := scan.CleanRoots(strings.Split(v, ","))
	if err != nil {
		log.Printf("invalid LIBRARY_PATHS: %v", err)
		return
	}
	if err := st.SetLibraryPaths(roots); err != nil {
		log.Printf("seed library paths: %v", err)
		return
	}
	log.Printf("library folders seeded from LIBRARY_PATHS") //nolint:gosec // folder names are operator-supplied env, logged locally only
}

// ensureSettingsLoop blocks until the Meilisearch index exists with the
// desired settings, retrying every retry interval so startup survives a
// Meilisearch that is still booting (compose starts both at once).
func ensureSettingsLoop(ix *indexer.Indexer, retry time.Duration) {
	for {
		if err := ix.EnsureSettings(); err != nil {
			log.Printf("meilisearch settings: %v (retrying in %s)", err, retry)
			time.Sleep(retry)
			continue
		}
		log.Printf("meilisearch index %q ready", indexer.IndexName)
		return
	}
}

func scanTicker(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if active, err := st.HasActiveScanJob(); err == nil && !active {
				roots, rerr := st.GetLibraryPaths()
				if rerr != nil {
					log.Printf("scan ticker read roots: %v", rerr)
					continue
				}
				payload, merr := json.Marshal(map[string]any{"roots": roots, "prune": true})
				if merr != nil {
					log.Printf("scan ticker marshal: %v", merr)
					continue
				}
				if _, err := st.EnqueueJob("scan", string(payload), 3); err != nil {
					log.Printf("scan ticker enqueue: %v", err)
				} else {
					log.Printf("scan ticker: scan enqueued (%d folders)", len(roots))
				}
			}
		}
	}
}
