package api

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"quipster/internal/fonts"
)

// handleFontsList returns the bundled Google Fonts catalog so the frontend can
// populate the subtitle editor with the exact faces available to FFmpeg.
func (d Deps) handleFontsList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	writeJSON(w, map[string]any{"fonts": fonts.Catalog()})
}

// handleFontFile serves one embedded TrueType font file (e.g. inter-400.ttf)
// so browser previews render with the exact same typeface FFmpeg burns in.
func (d Deps) handleFontFile(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	data, ok := fonts.FileByName(file)
	if !ok {
		writeErr(w, http.StatusNotFound, "font not found")
		return
	}
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "font.ttf", time.Time{}, bytes.NewReader(data))
}
