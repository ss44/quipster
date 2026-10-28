package node

import (
	"fmt"
	"path/filepath"
	"strings"
)

// mapper translates server-side media paths onto the node's local mounts.
//
// The server stores absolute paths under its media root (for example
// /srv/media/library/show.mkv); a node mounts the same share wherever it
// likes (E:\media, /mnt/nas, ...). By default the node's media root is
// assumed to correspond to the server's media root. Operators with finer
// mounts (say, only a subfolder) provide an explicit NODE_MEDIA_MAP of
// serverPrefix=localPath pairs, checked in order before the root-to-root
// fallback. The relative part always uses forward slashes on the wire and is
// re-localized on the node, which keeps mixed-OS clusters working.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type mapper struct {
	pairs     [][2]string
	localRoot string
}

// newMapper builds the path mapper for one claim: serverRoot is the server's
// media directory as reported by the claim, localRoot the node's MEDIA_DIR,
// and mapSpec the operator's explicit mapping pairs
// ("/srv/media/library=/mnt/library", optionally several comma-separated).
// Both sides of a pair are required; empty local paths are rejected so a
// mapping can never resolve to a working-directory-relative path.
func newMapper(serverRoot, localRoot, mapSpec string) (*mapper, error) {
	pairs, err := parseMediaMap(mapSpec)
	if err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		// Root-to-root mapping: the node mounts the whole media root.
		if strings.TrimSpace(localRoot) == "" {
			return nil, fmt.Errorf("no media mapping available: set MEDIA_DIR or NODE_MEDIA_MAP")
		}
		pairs = append(pairs, [2]string{filepath.ToSlash(filepath.Clean(serverRoot)), localRoot})
	}
	return &mapper{pairs: pairs, localRoot: localRoot}, nil
}

// parseMediaMap parses comma-separated serverPrefix=localPath pairs.
func parseMediaMap(mapSpec string) ([][2]string, error) {
	var pairs [][2]string
	for _, pair := range strings.Split(mapSpec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		server, local, ok := strings.Cut(pair, "=")
		server = strings.TrimSpace(server)
		local = strings.TrimSpace(local)
		if !ok || server == "" || local == "" {
			return nil, fmt.Errorf("invalid NODE_MEDIA_MAP entry %q (want serverPrefix=localPath)", pair)
		}
		pairs = append(pairs, [2]string{filepath.ToSlash(filepath.Clean(server)), filepath.ToSlash(filepath.Clean(local))})
	}
	return pairs, nil
}

// local resolves the node-local path for a server-side media path.
func (m *mapper) local(serverPath string) (string, error) {
	sPath := filepath.ToSlash(filepath.Clean(serverPath))
	for _, pair := range m.pairs {
		if rel, ok := relUnder(sPath, pair[0]); ok {
			return filepath.Join(pair[1], filepath.FromSlash(rel)), nil
		}
	}
	return "", fmt.Errorf("media path %q matches no media mapping (check MEDIA_DIR and NODE_MEDIA_MAP)", serverPath)
}

// relUnder returns the forward-slash relative part of path under prefix. The
// comparison falls back to case-insensitivity for case-insensitive mounts
// (Windows and default macOS filesystems), where the same share is often
// spelled with different casing on each side.
func relUnder(path, prefix string) (string, bool) {
	if path == prefix {
		return "", true
	}
	if strings.HasPrefix(path, prefix+"/") {
		return path[len(prefix)+1:], true
	}
	if len(path) > len(prefix) &&
		strings.EqualFold(path[:len(prefix)], prefix) &&
		strings.HasPrefix(path[len(prefix):], "/") {
		return path[len(prefix)+1:], true
	}
	return "", false
}
