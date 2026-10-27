package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist serves the built SPA; nil when dist is empty.
var Dist fs.FS

func init() {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return
	}
	if ents, err := fs.ReadDir(sub, "."); err == nil && len(ents) > 0 {
		Dist = sub
	}
}
