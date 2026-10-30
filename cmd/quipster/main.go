package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	"quipster/internal/mcp"
	"quipster/internal/node"
	"quipster/internal/nodes"
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
	archiveEnabled bool
	archiveGrace   time.Duration
	logs           *logbuf.Buffer
	mcpEnabled     bool
	mcpStdio       bool
	mcpTokens      string
	mcpRateLimit   int
	mcpAllowAdmin  bool
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
		archiveEnabled: envBool("ARCHIVE_ENABLED", true),
		archiveGrace:   parseDuration(envStr("ARCHIVE_GRACE", "48h")),
		previewRes:     envStr("CLIP_PREVIEW_RES", ""),
		accurateRes:    envStr("CLIP_ACCURATE_RES", ""),
		cpuPreset:      envStr("CLIP_CPU_PRESET", ""),
		mcpEnabled:     envBool("MCP_ENABLED", false),
		mcpStdio:       strings.EqualFold(strings.TrimSpace(os.Getenv("MCP_TRANSPORT")), "stdio"),
		mcpTokens:      os.Getenv("MCP_TOKENS"),
		mcpRateLimit:   envInt("MCP_RATE_LIMIT", mcp.DefaultRateLimit),
		mcpAllowAdmin:  envBool("MCP_ALLOW_ADMIN", false),
	}
}

// envBool parses a boolean environment flag, accepting the usual 1/true/yes
// spellings and their negations; an absent or unrecognized variable keeps the
// default.
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
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
func registerJobHandlers(cfg config, st *store.Store, runner *jobs.Runner, ix *indexer.Indexer, clipSvc *clips.Service, pipeline *probe.Pipeline) {
	// Scans never delete media. onArchived hands archived ids to the lazy
	// de-index queue: priority -1 jobs are claimed only when nothing else is
	// queued, so a large archive backlog trickles strictly behind
	// user-visible work and the scan itself never waits on Meili.
	onArchived := func(ids []int64) {
		enqueueArchiveDeindex(st, ids)
	}
	// A scan is metadata-only and must not compete with full-file subtitle
	// extraction on a network share. The gate makes scans exclusive: in-flight
	// probes are canceled and requeued, and new probes wait for the walk.
	gate := jobs.NewGate(runner)
	runner.Register("scan", 1, gate.Scan(scan.JobHandler(cfg.mediaDir, st, onArchived,
		scan.WithTimeout(cfg.scanTimeout),
		scan.WithArchivePolicy(cfg.archiveEnabled, cfg.archiveGrace))))
	runner.Register("probe", cfg.subWorkers, gate.Probe(pipeline.JobHandler()))
	runner.Register("index", cfg.indexWorkers, ix.JobHandler())
	runner.Register("clip", cfg.clipWorkers, clipSvc.JobHandler())
	// De-indexing runs on a single dedicated worker so Meili pressure from the
	// archive trickle stays flat regardless of backlog size.
	runner.Register("archive_deindex", 1, ix.ArchiveJobHandler())
}

// enqueueArchiveDeindex enqueues one unique low-priority de-index job per
// archived media, in bounded batches. Deduplication makes the call idempotent
// across overlapping scans and reconcile passes. A failed batch does not stop
// the remaining batches: the next boot reconcile would recover the backlog,
// but continuing here shrinks the gap instead of leaving it until restart.
func enqueueArchiveDeindex(st *store.Store, ids []int64) {
	if len(ids) == 0 {
		return
	}
	const batch = 1000
	enqueued := 0
	skipped := 0
	for start := 0; start < len(ids); start += batch {
		specs := make([]store.JobSpec, 0, min(batch, len(ids)-start))
		for _, id := range ids[start:min(start+batch, len(ids))] {
			specs = append(specs, store.JobSpec{
				Type:     "archive_deindex",
				Payload:  fmt.Sprintf(`{"mediaId":%d}`, id),
				Priority: -1,
			})
		}
		n, err := st.EnqueueUniqueJobs(specs)
		if err != nil {
			log.Printf("enqueue archive deindex jobs: %v", err)
			skipped += len(specs)
			continue
		}
		enqueued += n
	}
	if enqueued > 0 {
		log.Printf("queued %d archive de-index job(s)", enqueued)
	}
	if skipped > 0 {
		log.Printf("skipped %d archive de-index job(s) after enqueue errors; boot reconcile will retry them", skipped)
	}
}

// reconcileArchivedDeindex re-enqueues de-index jobs for rows archived before
// a crash or Meili outage, closing the gap left by the runner's bounded
// retries. Best-effort: the next query-time reconciliation pass also covers it.
func reconcileArchivedDeindex(st *store.Store) {
	ids, err := st.ArchivedPendingDeindex()
	if err != nil {
		log.Printf("archive reconcile: %v", err)
		return
	}
	if len(ids) > 0 {
		log.Printf("archive reconcile: %d archived media awaiting de-index", len(ids))
		enqueueArchiveDeindex(st, ids)
	}
}

// buildProbePipeline constructs the probe pipeline shared by the local job
// runner and the worker-node protocol (which applies remote results through
// the same Apply path).
func buildProbePipeline(cfg config, st *store.Store) *probe.Pipeline {
	return &probe.Pipeline{
		St:             st,
		SubsCacheDir:   filepath.Join(cfg.cacheDir, "subs"),
		ExtractConns:   envInt("EXTRACT_CONNS", 4),
		ProbeTimeout:   cfg.probeTimeout,
		ExtractTimeout: cfg.extractTimeout,
	}
}

// app bundles the wired services a running quipster process needs. Keeping
// assembly separate from main() makes the whole wiring testable without
// starting an HTTP server or waiting for a signal.
type app struct {
	Runner *jobs.Runner
	Stop   func()
	Deps   api.Deps
	MCP    *mcp.Server
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
	pipeline := buildProbePipeline(cfg, st)
	if cfg.role == "all" || cfg.role == "worker" {
		registerJobHandlers(cfg, st, runner, ix, clipSvc, pipeline)
	}

	// API-serving roles host the worker-node protocol; the janitor
	// requeues jobs whose claiming node went quiet.
	var nodeSvc *nodes.Service
	if cfg.role == "all" || cfg.role == "api" {
		nodeSvc = newNodeService(cfg, st, pipeline, clipSvc)
		go nodeSvc.Janitor(ctx, nodesJanitorInterval)
	}

	if cfg.scanIntvl > 0 && (cfg.role == "all" || cfg.role == "api") {
		go scanTicker(ctx, st, cfg.scanIntvl)
	}

	deps := api.Deps{
		St:            st,
		Search:        searchSvc,
		Indexer:       ix,
		Clips:         clipSvc,
		Thumbs:        thumbSvc,
		Events:        collector,
		Runner:        runner,
		MediaDir:      cfg.mediaDir,
		WebFS:         web.Dist,
		APIToken:      cfg.apiToken,
		CacheMaxBytes: lru.MaxBytes,
		Diagnostics:   diag,
		Nodes:         nodeSvc,
	}
	// MCP is served in two modes: streamable HTTP mounted at /mcp on API
	// processes, and stdio for local single-process integrations (worker role
	// plus stdio turns this process into an MCP subprocess that also processes
	// jobs). Enablement is explicit and requires at least one scoped token.
	var mcpSrv *mcp.Server
	if cfg.mcpEnabled && strings.TrimSpace(cfg.mcpTokens) != "" {
		mcpTokens := mcp.ParseTokens(cfg.mcpTokens)
		mcpSrv = mcp.New(mcp.Deps{
			St: st, Search: searchSvc, Indexer: ix, Clips: clipSvc, Thumbs: thumbSvc,
			Events: collector, Runner: runner, MediaDir: cfg.mediaDir, ClipWorkers: cfg.clipWorkers,
		}, mcp.Config{
			Tokens: mcpTokens, AllowAdmin: cfg.mcpAllowAdmin,
			RateLimit: cfg.mcpRateLimit, Version: version,
		})
		if cfg.role == "all" || cfg.role == "api" {
			deps.MCP = mcpSrv.HTTPHandler()
			log.Printf("mcp enabled at /mcp (%d token(s), admin tools %t)",
				len(mcpTokens), cfg.mcpAllowAdmin)
		}
	} else if cfg.mcpEnabled {
		log.Printf("mcp requested but disabled: set MCP_TOKENS to grant agent access")
	}
	return &app{Deps: deps, Runner: runner, MCP: mcpSrv, Stop: func() {
		cancel()
		runner.Stop()
	}}
}

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// `quipster node` launches the stateless worker-node mode: it talks to
	// a main app over HTTP instead of opening the store directly.
	if len(os.Args) > 1 && os.Args[1] == "node" {
		runNodeCmd(os.Args[2:])
		return
	}
	cfg := loadConfig()
	if cfg.role == "node" {
		// Env-driven node mode for container deployments: ROLE=node plus
		// NODE_SERVER_URL behaves exactly like the subcommand.
		startNode(node.Options{})
		return
	}
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
	logEmbedderConfig(emb, embErr)

	ix := indexer.New(st, cfg.meiliURL, cfg.meiliKey, indexer.WithEmbedder(emb))
	go ensureSettingsLoop(ix, 15*time.Second)

	application := assembleApp(context.Background(), cfg, st, ix)
	defer application.Stop()

	// Repair any de-index backlog left by a crash or a Meili outage before
	// serving traffic.
	reconcileArchivedDeindex(st)

	// Stdio mode replaces the HTTP server entirely: the process is launched
	// by an MCP client as a subprocess and speaks MCP over stdin/stdout. It
	// runs with the full scope set (operator-launched), still processing jobs
	// per the configured role.
	if cfg.mcpEnabled && cfg.mcpStdio && application.MCP != nil {
		log.Printf("mcp stdio mode: serving over stdin/stdout")
		if err := application.MCP.ConnectStdio(context.Background()); err != nil {
			log.Printf("mcp stdio: %v", err)
		}
		return
	}

	serveHTTP(cfg, application)
}

// logEmbedderConfig reports the semantic-search startup state.
func logEmbedderConfig(emb indexer.EmbedderConfig, err error) {
	switch {
	case err != nil:
		log.Printf("semantic search disabled: %v", err)
	case emb.Enabled():
		log.Printf("semantic search: enabled (source=%s model=%s hybridRatio=%.2f)",
			emb.Source, emb.Model, emb.SemanticRatio) //nolint:gosec // operator-supplied env, logged locally only
	default:
		log.Printf("semantic search: disabled (set EMBEDDINGS_SOURCE to enable)")
	}
}

// serveHTTP starts the HTTP server (API roles only) and blocks until a
// termination signal, then drains connections and stops the runner.
func serveHTTP(cfg config, application *app) {
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.Router(application.Deps),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.role != "all" && cfg.role != "api" {
		log.Printf("worker mode: processing jobs, no http server")
		waitForSignal(application)
		return
	}
	go func() {
		log.Printf("http listening on %s", cfg.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()
	waitForSignal(application)
	shCtx, shCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
}

// waitForSignal blocks until SIGINT/SIGTERM, then stops the runner.
func waitForSignal(application *app) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
	application.Stop()
}

// runNodeCmd implements `quipster node`: the same binary acting as a
// stateless worker node that claims jobs from a main app over HTTP.
func runNodeCmd(args []string) {
	opts, err := parseNodeArgs(args)
	if err != nil {
		log.Fatalf("node flags: %v", err)
	}
	startNode(opts)
}

// parseNodeArgs parses the `node` subcommand flags into node options. It is
// separated from runNodeCmd so flag handling is testable without launching a
// node session.
func parseNodeArgs(args []string) (node.Options, error) {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	fs.Usage = func() {
		// Usage output goes to a writer the caller chose; a write
		// failure cannot be meaningfully handled here.
		fmt.Fprintln(fs.Output(), "usage: quipster node [flags]")                                                   //nolint:errcheck // usage output, nothing sensible on failure
		fmt.Fprintln(fs.Output(), "\nRuns a worker node that claims probe and clip jobs from a main quipster app.") //nolint:errcheck // usage output, nothing sensible on failure
		fs.PrintDefaults()
	}
	var (
		server   = fs.String("server", "", "base URL of the main quipster app (or NODE_SERVER_URL)")
		name     = fs.String("name", "", "display name shown in the admin Nodes panel (default: hostname)")
		media    = fs.String("media", "", "local mount of the media library (default: ./media)")
		mediaMap = fs.String("media-map", "", "explicit media path mapping serverPrefix=localPath (or NODE_MEDIA_MAP)")
		cache    = fs.String("cache", "", "scratch cache dir (default: user cache dir/quipster)")
		state    = fs.String("state", "", "identity and token state file (default: user config dir/quipster/node.json)")
	)
	if err := fs.Parse(args); err != nil {
		return node.Options{}, err
	}
	return node.Options{
		ServerURL: *server,
		Name:      *name,
		MediaDir:  *media,
		MediaMap:  *mediaMap,
		CacheDir:  *cache,
		StatePath: *state,
	}, nil
}

// startNode resolves the configuration and runs the node until interrupted.
func startNode(opts node.Options) {
	log.SetFlags(log.LstdFlags)
	cfg, err := node.LoadConfig(opts)
	if err != nil {
		log.Fatalf("node config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := node.Run(ctx, cfg, version); err != nil {
		log.Fatalf("node: %v", err)
	}
}

// nodesJanitorInterval is how often the server sweeps for jobs orphaned by
// offline worker nodes.
const nodesJanitorInterval = 20 * time.Second

// newNodeService builds the server side of the worker-node protocol for
// API-serving roles. MediaRoot is passed exactly as the scanner receives it,
// because media paths in the store are built from that same string; nodes
// remap it onto their own mounts.
func newNodeService(cfg config, st *store.Store, pipe *probe.Pipeline, clipSvc *clips.Service) *nodes.Service {
	ccfg := clipSvc.Config()
	svc := nodes.NewService(st, nodes.Settings{
		MediaRoot:    cfg.mediaDir,
		SubsCacheDir: filepath.Join(cfg.cacheDir, "subs"),
		Clips: nodes.ClipSettings{
			PreviewRes:   ccfg.PreviewRes,
			AccurateRes:  ccfg.AccurateRes,
			CPUPreset:    ccfg.CPUPreset,
			MaxClipSec:   ccfg.MaxClipSec,
			DefaultPadMs: ccfg.DefaultPadMs,
		},
		Version: version,
	})
	svc.Pipeline = pipe
	svc.Clips = clipSvc
	return svc
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
