package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves the embedded SPA with index.html fallback.
func (d Deps) spaHandler() http.HandlerFunc {
	fileServer := http.FileServer(http.FS(d.WebFS))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(d.WebFS, p); err != nil {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		// Go's MIME table has no entry for .webmanifest, and a PWA manifest
		// must be served as application/manifest+json for browsers to accept it.
		if strings.HasSuffix(p, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		fileServer.ServeHTTP(w, r)
	}
}
