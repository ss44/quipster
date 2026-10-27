package main

import (
	"context"
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
	"quipster/internal/events"
	"quipster/internal/indexer"
	"quipster/internal/jobs"
	"quipster/internal/probe"
	"quipster/internal/scan"
	"quipster/internal/search"
	"quipster/internal/store"
	"quipster/internal/thumbs"
	"quipster/web"
)

type config struct {
	addr         string
	role         string
	dataDir      string
	mediaDir     string
	cacheDir     string
	meiliURL     string
	meiliKey     string
	scanIntvl    time.Duration
	subWorkers   int
	indexWorkers int
	clipWorkers  int
	apiToken     string
	padMs        int
	maxClipSec   float64
	cacheMaxGB   float64
	longCutSec   float64
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
		addr:         envStr("ADDR", ":8080"),
		role:         strings.ToLower(envStr("ROLE", "all")),
		dataDir:      envStr("DATA_DIR", "./data"),
		mediaDir:     envStr("MEDIA_DIR", "./media"),
		cacheDir:     envStr("CACHE_DIR", "./cache"),
		meiliURL:     envStr("MEILI_URL", "http://localhost:7700"),
		meiliKey:     envStr("MEILI_MASTER_KEY", ""),
		scanIntvl:    parseDuration(envStr("SCAN_INTERVAL", "")),
		subWorkers:   envInt("SUBTITLE_WORKERS", 4),
		indexWorkers: envInt("INDEX_WORKERS", 4),
		clipWorkers:  envInt("CLIP_WORKERS", 2),
		apiToken:     os.Getenv("API_TOKEN"),
		padMs:        envInt("CLIP_PAD_MS", 250),
		maxClipSec:   envFloat("MAX_CLIP_SEC", 120),
		cacheMaxGB:   envFloat("CACHE_MAX_GB", 10),
		longCutSec:   envFloat("LONG_CUT_SEC", 20),
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

func main() {
	cfg := loadConfig()
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
	defer st.Close()
	if err := st.Recover(); err != nil {
		log.Printf("recover jobs: %v", err)
	}

	ix := indexer.New(st, cfg.meiliURL, cfg.meiliKey)
	go ensureSettingsLoop(ix)

	lru := &cache.LRU{St: st, MaxBytes: int64(cfg.cacheMaxGB * 1024 * 1024 * 1024), Kinds: []string{"clip", "thumb", "sub"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.RunDaemon(ctx, lru, 5*time.Minute)

	clipSvc := clips.New(st, clips.Config{
		CacheDir:     cfg.cacheDir,
		MaxClipSec:   cfg.maxClipSec,
		DefaultPadMs: cfg.padMs,
		LongCutSec:   cfg.longCutSec,
	})
	thumbSvc := &thumbs.Service{St: st, CacheDir: cfg.cacheDir, Hw: clipSvc}
	searchSvc := search.New(st, ix)
	collector := &events.Collector{St: st, MeiliUp: ix.Healthy}

	runner := jobs.NewRunner(st, "worker")
	runner.SetJobTimeout(parseDuration(envStr("JOB_TIMEOUT", "3h")))
	if cfg.role == "all" || cfg.role == "worker" {
		onDeleted := func(ids []int64) { ix.DeleteMedia(ids) }
		runner.Register("scan", 1, scan.JobHandler(cfg.mediaDir, st, onDeleted))
		pipeline := &probe.Pipeline{
			St:           st,
			SubsCacheDir: filepath.Join(cfg.cacheDir, "subs"),
			ExtractConns: envInt("EXTRACT_CONNS", 4),
		}
		runner.Register("probe", cfg.subWorkers, pipeline.JobHandler())
		runner.Register("index", cfg.indexWorkers, ix.JobHandler())
		runner.Register("clip", cfg.clipWorkers, clipSvc.JobHandler())
	}

	if cfg.scanIntvl > 0 && (cfg.role == "all" || cfg.role == "api") {
		go scanTicker(ctx, st, cfg.scanIntvl)
	}

	deps := api.Deps{
		St:       st,
		Search:   searchSvc,
		Indexer:  ix,
		Clips:    clipSvc,
		Thumbs:   thumbSvc,
		Events:   collector,
		MediaDir: cfg.mediaDir,
		WebFS:    web.Dist,
		APIToken: cfg.apiToken,
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.Router(deps),
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
	cancel()
	runner.Stop()
}

func ensureSettingsLoop(ix *indexer.Indexer) {
	for {
		if err := ix.EnsureSettings(); err != nil {
			log.Printf("meilisearch settings: %v (retrying in 15s)", err)
			time.Sleep(15 * time.Second)
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
				if _, err := st.EnqueueJob("scan", "{}", 3); err != nil {
					log.Printf("scan ticker enqueue: %v", err)
				} else {
					log.Printf("scan ticker: scan enqueued")
				}
			}
		}
	}
}
