// Package node implements the worker-node operation mode: the same quipster
// binary connects to a coordinating server over HTTP, registers itself for
// operator approval, then claims jobs (probing and clip rendering), executes
// them against a local mount of the media library and uploads results back.
package node

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved node configuration.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Config struct {
	// ServerURL is the base URL of the main quipster app.
	ServerURL string
	// Name is the display name shown in the admin approval UI.
	Name string
	// MediaDir is the node's local mount of the media library. Claimed
	// jobs reference server-side paths that are remapped onto it.
	MediaDir string
	// MediaMap holds explicit serverPrefix=localPath pairs for mounts that
	// cover only part of the server's media root; it wins over MediaDir
	// when a path matches. Empty falls back to MediaDir root-to-root.
	MediaMap string
	// CacheDir holds extracted subtitles and rendered clips awaiting upload.
	CacheDir string
	// StatePath persists the node identity and approval token.
	StatePath string
	// ProbeWorkers bounds parallel probe claims; ClipWorkers the same for
	// clip renders.
	ProbeWorkers int
	ClipWorkers  int
	// ExtractConns bounds parallel embedded-subtitle extractions.
	ExtractConns int
	// PollInterval is the approval-poll cadence; 0 uses the 3s default.
	PollInterval time.Duration
	// Heartbeat is the reporting cadence told to the server.
	Heartbeat time.Duration
	// ProbeTimeout, ExtractTimeout and JobTimeout mirror the server-side
	// pipeline bounds.
	ProbeTimeout   time.Duration
	ExtractTimeout time.Duration
	JobTimeout     time.Duration
}

// Options are the operator-supplied overrides (flags); empty fields fall back
// to the environment and then to cross-platform defaults.
type Options struct {
	ServerURL string
	Name      string
	MediaDir  string
	MediaMap  string
	CacheDir  string
	StatePath string
}

// LoadConfig resolves the node configuration: flags win, then environment
// variables, then platform defaults. Defaults follow the XDG convention via
// Go's UserConfigDir/UserCacheDir so the same invocation works on Windows,
// macOS and Linux without platform-specific flags: state lands in the user
// config directory, scratch cache in the user cache directory.
func LoadConfig(opts Options) (Config, error) {
	cfg := Config{
		ServerURL:  firstNonEmpty(opts.ServerURL, envStr("NODE_SERVER_URL", ""), envStr("SERVER_URL", "")),
		Name:       firstNonEmpty(opts.Name, envStr("NODE_NAME", "")),
		MediaDir:   firstNonEmpty(opts.MediaDir, envStr("MEDIA_DIR", "")),
		MediaMap:   firstNonEmpty(opts.MediaMap, envStr("NODE_MEDIA_MAP", "")),
		CacheDir:   firstNonEmpty(opts.CacheDir, envStr("CACHE_DIR", "")),
		StatePath:  opts.StatePath,
		Heartbeat:  parseDuration(envStr("NODE_HEARTBEAT", "")),
		JobTimeout: parseDuration(envStr("JOB_TIMEOUT", "3h")),
	}
	if cfg.ServerURL == "" {
		return cfg, errors.New("node mode requires the main app URL (--server or NODE_SERVER_URL)")
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")

	if cfg.Name == "" {
		cfg.Name = defaultNodeName()
	}
	if cfg.MediaDir == "" {
		cfg.MediaDir = "./media"
	}
	abs, err := filepath.Abs(cfg.MediaDir)
	if err != nil {
		return cfg, fmt.Errorf("resolve media dir: %w", err)
	}
	cfg.MediaDir = abs

	// Fail fast on mapping typos: every job would otherwise fail at
	// claim time with the same syntax error.
	if _, merr := parseMediaMap(cfg.MediaMap); merr != nil {
		return cfg, merr
	}

	if cfg.CacheDir == "" {
		base, derr := os.UserCacheDir()
		if derr != nil {
			return cfg, fmt.Errorf("resolve cache dir: %w", derr)
		}
		cfg.CacheDir = filepath.Join(base, "quipster")
	}
	abs, err = filepath.Abs(cfg.CacheDir)
	if err != nil {
		return cfg, fmt.Errorf("resolve cache dir: %w", err)
	}
	cfg.CacheDir = abs

	if cfg.StatePath == "" {
		base, serr := os.UserConfigDir()
		if serr != nil {
			return cfg, fmt.Errorf("resolve config dir: %w", serr)
		}
		cfg.StatePath = filepath.Join(base, "quipster", "node.json")
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 3 * time.Hour
	}
	cfg.ProbeWorkers = envInt("SUBTITLE_WORKERS", 4)
	cfg.ClipWorkers = envInt("CLIP_WORKERS", 2)
	cfg.ExtractConns = envInt("EXTRACT_CONNS", 4)
	cfg.ProbeTimeout = parseDuration(envStr("PROBE_TIMEOUT", ""))
	cfg.ExtractTimeout = parseDuration(envStr("EXTRACT_TIMEOUT", "10m"))
	return cfg, nil
}

// defaultNodeName builds a human-readable default display name from the
// hostname (falling back to the OS when the hostname is unavailable).
func defaultNodeName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = runtime.GOOS
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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

// parseDuration accepts Go duration strings or bare seconds, mirroring the
// server's env parsing.
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

// state is the persisted node identity: the fingerprint that ties
// re-registrations to the same row, the registration secret for the approval
// poll and the claim token issued at approval.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type state struct {
	Fingerprint string `json:"fingerprint"`
	Secret      string `json:"secret,omitempty"`
	Token       string `json:"token,omitempty"`
	NodeID      int64  `json:"nodeId,omitempty"`
}

// loadState reads the persisted state, generating the identity on first run.
// The file is written with 0600 permissions because it holds the approval
// token that grants job-claiming access.
func loadState(path string) (*state, error) {
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the path is operator configuration
		var st state
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, fmt.Errorf("parse node state %s: %w", path, err)
		}
		if st.Fingerprint != "" {
			return &st, nil
		}
	}
	// First run: bind the identity to this machine with a random component
	// so two nodes on one host are still distinct installations.
	host, herr := os.Hostname()
	if herr != nil {
		// An unavailable hostname still yields a unique fingerprint via
		// the random component below.
		host = ""
	}
	rnd := make([]byte, 16)
	if _, err := rand.Read(rnd); err != nil {
		return nil, fmt.Errorf("generate node fingerprint: %w", err)
	}
	sum := sha256.Sum256([]byte(host + "\x00" + hex.EncodeToString(rnd)))
	return &state{Fingerprint: hex.EncodeToString(sum[:])}, nil
}

// save writes the state atomically (temp file + rename) so a crash cannot
// truncate an approved token.
func (s *state) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ") //nolint:gosec // the state file intentionally persists the node's own credentials
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { //nolint:gosec // identity file must be owner-only
		return err
	}
	return os.Rename(tmp, path)
}
