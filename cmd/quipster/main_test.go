// Package main tests the configuration helpers.
package main

import (
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/clips"
	"quipster/internal/probe"
	"quipster/internal/store"
)

func TestEnvStr(t *testing.T) {
	t.Setenv("QUIP_TEST_STR", "  value  ")
	if got := envStr("QUIP_TEST_STR", "fallback"); got != "value" {
		t.Errorf("envStr = %q, want value", got)
	}
	t.Setenv("QUIP_TEST_STR", "   ")
	if got := envStr("QUIP_TEST_STR", "fallback"); got != "fallback" {
		t.Errorf("envStr blank = %q, want fallback", got)
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"7", 7},
		{"0", 42},
		{"-3", 42},
		{"not-a-number", 42},
		{"  ", 42},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("QUIP_TEST_INT", tc.value)
			if got := envInt("QUIP_TEST_INT", 42); got != tc.want {
				t.Errorf("envInt(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestEnvFloat(t *testing.T) {
	cases := []struct {
		value string
		want  float64
	}{
		{"2.5", 2.5},
		{"0", 1.5},
		{"-2", 1.5},
		{"bad", 1.5},
		{"", 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("QUIP_TEST_FLOAT", tc.value)
			if got := envFloat("QUIP_TEST_FLOAT", 1.5); got != tc.want {
				t.Errorf("envFloat(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"   ", 0},
		{"30s", 30 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"45", 45 * time.Second},
		{"not-a-duration", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := parseDuration(tc.in); got != tc.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ADDR", "ROLE", "DATA_DIR", "MEDIA_DIR", "CACHE_DIR", "MEILI_URL",
		"MEILI_MASTER_KEY", "SCAN_INTERVAL", "SUBTITLE_WORKERS", "INDEX_WORKERS",
		"CLIP_WORKERS", "API_TOKEN", "CLIP_PAD_MS", "MAX_CLIP_SEC", "CACHE_MAX_GB",
		"LONG_CUT_SEC", "CLIP_PREVIEW_RES", "CLIP_ACCURATE_RES", "CLIP_CPU_PRESET",
		"SCAN_TIMEOUT", "EXTRACT_TIMEOUT", "PROBE_TIMEOUT",
		"MCP_ENABLED", "MCP_TRANSPORT", "MCP_TOKENS", "MCP_RATE_LIMIT", "MCP_ALLOW_ADMIN",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearConfigEnv(t)
	want := config{
		addr:           ":8080",
		role:           "all",
		dataDir:        "./data",
		mediaDir:       "./media",
		cacheDir:       "./cache",
		meiliURL:       "http://localhost:7700",
		meiliKey:       "",
		scanIntvl:      0,
		subWorkers:     4,
		indexWorkers:   4,
		clipWorkers:    2,
		apiToken:       "",
		padMs:          250,
		maxClipSec:     120,
		cacheMaxGB:     10,
		longCutSec:     20,
		scanTimeout:    2 * time.Hour,
		extractTimeout: 10 * time.Minute,
		mcpRateLimit:   120,
	}
	if got := loadConfig(); got != want {
		t.Errorf("loadConfig = %+v, want %+v", got, want)
	}
}

func TestMCPEnvironment(t *testing.T) {
	clearConfigEnv(t)
	if cfg := loadConfig(); cfg.mcpEnabled || cfg.mcpStdio || cfg.mcpAllowAdmin {
		t.Errorf("MCP must default to disabled: %+v", cfg)
	}

	t.Setenv("MCP_ENABLED", "1")
	t.Setenv("MCP_TRANSPORT", "STDIO")
	t.Setenv("MCP_ALLOW_ADMIN", "yes")
	t.Setenv("MCP_RATE_LIMIT", "30")
	t.Setenv("MCP_TOKENS", "tok:read+clip")
	cfg := loadConfig()
	if !cfg.mcpEnabled || !cfg.mcpStdio || !cfg.mcpAllowAdmin || cfg.mcpRateLimit != 30 || cfg.mcpTokens != "tok:read+clip" {
		t.Fatalf("MCP env not loaded: %+v", cfg)
	}

	t.Setenv("MCP_ENABLED", "false")
	if cfg := loadConfig(); cfg.mcpEnabled {
		t.Errorf("MCP_ENABLED=false must disable: %+v", cfg)
	}
}

func TestClipTuningEnvironment(t *testing.T) {
	t.Setenv("CLIP_PREVIEW_RES", "480:-2")
	t.Setenv("CLIP_ACCURATE_RES", "854:480")
	t.Setenv("CLIP_CPU_PRESET", "superfast")
	cfg := loadConfig()
	if cfg.previewRes != "480:-2" || cfg.accurateRes != "854:480" || cfg.cpuPreset != "superfast" {
		t.Fatalf("clip tuning not loaded: %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	values := map[string]string{
		"ADDR":             ":9000",
		"ROLE":             "API",
		"DATA_DIR":         "/data",
		"MEDIA_DIR":        "/media",
		"CACHE_DIR":        "/cache",
		"MEILI_URL":        "http://meili:7700",
		"MEILI_MASTER_KEY": "key",
		"SCAN_INTERVAL":    "15m",
		"SUBTITLE_WORKERS": "8",
		"INDEX_WORKERS":    "6",
		"CLIP_WORKERS":     "3",
		"API_TOKEN":        "token",
		"CLIP_PAD_MS":      "100",
		"MAX_CLIP_SEC":     "300",
		"CACHE_MAX_GB":     "2.5",
		"LONG_CUT_SEC":     "30",
	}
	for k, v := range values {
		t.Setenv(k, v)
	}
	want := config{
		addr:           ":9000",
		role:           "api",
		dataDir:        "/data",
		mediaDir:       "/media",
		cacheDir:       "/cache",
		meiliURL:       "http://meili:7700",
		meiliKey:       "key",
		scanIntvl:      15 * time.Minute,
		subWorkers:     8,
		indexWorkers:   6,
		clipWorkers:    3,
		apiToken:       "token",
		padMs:          100,
		maxClipSec:     300,
		cacheMaxGB:     2.5,
		longCutSec:     30,
		scanTimeout:    2 * time.Hour,
		extractTimeout: 10 * time.Minute,
		mcpRateLimit:   120,
	}
	if got := loadConfig(); got != want {
		t.Errorf("loadConfig = %+v, want %+v", got, want)
	}
}

func TestParseNodeArgs(t *testing.T) {
	opts, err := parseNodeArgs([]string{
		"--server", "http://main:8080",
		"--name", "gpu-box",
		"--media", "/mnt/library",
		"--media-map", "/srv/media/library=/mnt/library",
		"--cache", "/srv/cache",
		"--state", "/srv/state/node.json",
	})
	if err != nil {
		t.Fatalf("parseNodeArgs: %v", err)
	}
	if opts.ServerURL != "http://main:8080" || opts.Name != "gpu-box" || opts.MediaDir != "/mnt/library" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.MediaMap != "/srv/media/library=/mnt/library" || opts.CacheDir != "/srv/cache" || opts.StatePath != "/srv/state/node.json" {
		t.Errorf("opts = %+v", opts)
	}

	// Unknown flags are parse errors, not fatals.
	if _, err := parseNodeArgs([]string{"--bogus"}); err == nil {
		t.Error("unknown flag must be a parse error")
	}
}

func TestNewNodeServiceWiring(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	cfg := config{
		mediaDir:   "/srv/media",
		cacheDir:   dir,
		maxClipSec: 90,
	}
	clipSvc := clips.New(st, clips.Config{
		CacheDir:     dir,
		PreviewRes:   "640:-2",
		AccurateRes:  "1280:-2",
		CPUPreset:    "veryfast",
		MaxClipSec:   90,
		DefaultPadMs: 250,
	})
	pipe := &probe.Pipeline{St: st, SubsCacheDir: filepath.Join(dir, "subs")}
	svc := newNodeService(cfg, st, pipe, clipSvc)
	if svc == nil {
		t.Fatal("newNodeService returned nil")
	}
	got := svc.Settings()
	if got.MediaRoot != "/srv/media" {
		t.Errorf("MediaRoot = %q, want the scanner's exact string", got.MediaRoot)
	}
	if got.Clips.PreviewRes != "640:-2" || got.Clips.AccurateRes != "1280:-2" || got.Clips.CPUPreset != "veryfast" {
		t.Errorf("clip settings not mirrored: %+v", got.Clips)
	}
	if got.Clips.MaxClipSec != 90 || got.Clips.DefaultPadMs != 250 {
		t.Errorf("clip settings not mirrored: %+v", got.Clips)
	}
	if got.Version != version {
		t.Errorf("Version = %q, want %q", got.Version, version)
	}
}
