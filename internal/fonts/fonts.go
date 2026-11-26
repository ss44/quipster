// Package fonts bundles curated open-source Google Fonts (SIL OFL 1.1) so
// both the browser subtitle preview and FFmpeg's libass renderer use the exact
// same TrueType font files without external network dependencies.
package fonts

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed assets/*.ttf
var embeddedFS embed.FS

// DefaultID is the default subtitle font family slug.
const DefaultID = "inter"

// Font describes one bundled open-source font family available for subtitle
// preview and burn-in.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Font struct {
	ID       string `json:"id"`
	Family   string `json:"family"`
	Category string `json:"category"`
	HasBold  bool   `json:"hasBold"`
}

var catalog = []Font{
	{ID: "inter", Family: "Inter", Category: "sans-serif", HasBold: true},
	{ID: "montserrat", Family: "Montserrat", Category: "sans-serif", HasBold: true},
	{ID: "oswald", Family: "Oswald", Category: "display", HasBold: true},
	{ID: "roboto", Family: "Roboto", Category: "sans-serif", HasBold: true},
	{ID: "merriweather", Family: "Merriweather", Category: "serif", HasBold: true},
	{ID: "jetbrains-mono", Family: "JetBrains Mono", Category: "monospace", HasBold: true},
	{ID: "bangers", Family: "Bangers", Category: "display", HasBold: false},
}

var byID = func() map[string]Font {
	m := make(map[string]Font, len(catalog))
	for _, f := range catalog {
		m[f.ID] = f
	}
	return m
}()

// Catalog returns a copy of the bundled font metadata in display order.
func Catalog() []Font {
	out := make([]Font, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the bundled font matching id (case-insensitive).
func Lookup(id string) (Font, bool) {
	f, ok := byID[strings.ToLower(strings.TrimSpace(id))]
	return f, ok
}

// File returns the embedded TrueType bytes for the given font id and numeric
// weight (400 or 700). Fonts without a dedicated bold face only serve 400.
func File(id string, weight int) ([]byte, bool) {
	f, ok := Lookup(id)
	if !ok {
		return nil, false
	}
	switch weight {
	case 400:
	case 700:
		if !f.HasBold {
			return nil, false
		}
	default:
		return nil, false
	}
	name := fmt.Sprintf("assets/%s-%d.ttf", f.ID, weight)
	data, err := embeddedFS.ReadFile(name)
	if err != nil {
		return nil, false
	}
	return data, true
}

// FileByName resolves a font filename in "<id>-<weight>.ttf" form (e.g.
// "montserrat-700.ttf") to its embedded TrueType bytes.
func FileByName(filename string) ([]byte, bool) {
	if !strings.HasSuffix(filename, ".ttf") || strings.ContainsAny(filename, "/\\") {
		return nil, false
	}
	stem := strings.TrimSuffix(filename, ".ttf")
	idx := strings.LastIndexByte(stem, '-')
	if idx <= 0 || idx == len(stem)-1 {
		return nil, false
	}
	weight, err := strconv.Atoi(stem[idx+1:])
	if err != nil {
		return nil, false
	}
	return File(stem[:idx], weight)
}

// Materialize writes every embedded font file into dir so FFmpeg's libass
// fontsdir option can load them from disk. Existing files whose size already
// matches are left untouched to avoid unnecessary disk writes.
func Materialize(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create fonts dir: %w", err)
	}
	entries, err := embeddedFS.ReadDir("assets")
	if err != nil {
		return "", fmt.Errorf("read embedded fonts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ttf") {
			continue
		}
		data, rerr := embeddedFS.ReadFile("assets/" + entry.Name())
		if rerr != nil {
			return "", fmt.Errorf("read embedded font %s: %w", entry.Name(), rerr)
		}
		dst := filepath.Join(dir, entry.Name())
		if st, serr := os.Stat(dst); serr == nil && st.Size() == int64(len(data)) {
			continue
		}
		if werr := writeAtomic(dst, data); werr != nil {
			return "", fmt.Errorf("write font %s: %w", entry.Name(), werr)
		}
	}
	return dir, nil
}

// writeAtomic writes data via a sibling temp file and rename so concurrent
// FFmpeg renders never observe a partially written TrueType file.
func writeAtomic(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".font-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		if rerr := os.Remove(tmpPath); rerr != nil && !os.IsNotExist(rerr) {
			return
		}
	}
	if _, err := tmp.Write(data); err != nil {
		if cerr := tmp.Close(); cerr != nil {
			cleanup()
			return cerr
		}
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Make readable by libass/fontconfig regardless of umask.
	if err := os.Chmod(tmpPath, 0o644); err != nil { //nolint:gosec // G302: font assets are non-sensitive static files
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
