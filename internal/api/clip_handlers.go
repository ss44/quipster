package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"quipster/internal/clips"
	"quipster/internal/store"
)

// parseClipStreamRequest builds a clips.Request from the stream endpoint's
// query parameters, applying the service defaults for absent mode and padMs.
func parseClipStreamRequest(q url.Values, defaultPadMs int) (clips.Request, error) {
	mediaID, err := strconv.ParseInt(q.Get("mediaId"), 10, 64)
	if err != nil {
		return clips.Request{}, errors.New("mediaId required")
	}
	start, err1 := strconv.ParseFloat(q.Get("start"), 64)
	end, err2 := strconv.ParseFloat(q.Get("end"), 64)
	if err1 != nil || err2 != nil {
		return clips.Request{}, errors.New("start and end required")
	}
	mode := q.Get("mode")
	if mode == "" {
		mode = "fast"
	}
	padMs := defaultPadMs
	if p := q.Get("padMs"); p != "" {
		padMs, err = strconv.Atoi(p)
		if err != nil || padMs < 0 || padMs > 5000 {
			return clips.Request{}, errors.New("padMs out of range")
		}
	}
	subs, err := parseSubtitleStyle(q)
	if err != nil {
		return clips.Request{}, err
	}
	return clips.Request{MediaID: mediaID, Start: start, End: end, PadMs: padMs, Mode: mode, Subtitles: subs}, nil
}

// parseSubtitleStyle extracts optional burned-in subtitle styling query
// parameters from the clip stream URL. Absent or false "subs" yields nil so
// ordinary clips stay completely unaffected.
func parseSubtitleStyle(q url.Values) (*clips.SubtitleStyle, error) {
	raw := strings.ToLower(strings.TrimSpace(q.Get("subs")))
	if raw == "" || raw == "0" || raw == "false" {
		return nil, nil
	}
	if raw != "1" && raw != "true" {
		return nil, errors.New("subs must be 0 or 1")
	}
	st := clips.DefaultSubtitleStyle()
	if v := q.Get("subFont"); v != "" {
		st.Font = v
	}
	if err := parseSubNumerics(q, &st); err != nil {
		return nil, err
	}
	if v := q.Get("subColor"); v != "" {
		st.Color = v
	}
	if v := q.Get("subOutlineColor"); v != "" {
		st.OutlineColor = v
	}
	if v := q.Get("subBgColor"); v != "" {
		st.BgColor = v
	}
	if v := q.Get("subPos"); v != "" {
		st.Position = v
	}
	st.Bold = parseBoolQuery(q.Get("subBold"))
	st.Italic = parseBoolQuery(q.Get("subItalic"))
	st.Uppercase = parseBoolQuery(q.Get("subUpper"))

	norm, err := clips.NormalizeSubtitleStyle(st)
	if err != nil {
		return nil, err
	}
	return &norm, nil
}

func parseSubNumerics(q url.Values, st *clips.SubtitleStyle) error {
	if v := q.Get("subSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return errors.New("invalid subSize")
		}
		st.FontSize = n
	}
	if v := q.Get("subOutline"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return errors.New("invalid subOutline")
		}
		st.Outline = f
	}
	if v := q.Get("subShadow"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return errors.New("invalid subShadow")
		}
		st.Shadow = f
	}
	if v := q.Get("subBg"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return errors.New("invalid subBg")
		}
		st.BgOpacity = f
	}
	if v := q.Get("subMargin"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return errors.New("invalid subMargin")
		}
		st.MarginV = n
	}
	return nil
}

func parseBoolQuery(v string) bool {
	s := strings.ToLower(strings.TrimSpace(v))
	return s == "1" || s == "true"
}

func (d Deps) handleClipStream(w http.ResponseWriter, r *http.Request) {
	req, err := parseClipStreamRequest(r.URL.Query(), d.Clips.Config().DefaultPadMs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, _, verr := d.Clips.Validate(req)
	if verr != nil {
		// Archived media is not renderable and is treated as absent rather
		// than as a bad request, so stale UI links fail cleanly.
		if errors.Is(verr, clips.ErrMediaArchived) {
			writeErr(w, http.StatusNotFound, "media not found")
			return
		}
		writeErr(w, http.StatusBadRequest, verr.Error())
		return
	}
	key := d.Clips.CacheKey(req)

	if e, cerr := d.Clips.Cached(key); cerr != nil {
		writeErr(w, http.StatusInternalServerError, cerr.Error())
		return
	} else if e != nil {
		d.Clips.Touch(key)
		d.serveClip(w, r, e.Path, m, req)
		return
	}

	// Long accurate cuts run as background jobs with progress.
	span := req.End - req.Start
	if req.Mode == "accurate" && span > d.Clips.Config().LongCutSec {
		jobID, newlyQueued, qerr := d.St.EnqueueUniqueJob("clip", clips.AsyncJobPayload(req), 1)
		if qerr != nil {
			writeErr(w, http.StatusInternalServerError, qerr.Error())
			return
		}
		if newlyQueued || jobID > 0 { // always return accepted if it's async
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"status": "queued", "jobId": jobID, "cacheKey": key})
			return
		}
	}

	path, _, perr := d.Clips.Produce(r.Context(), req)
	if perr != nil {
		if r.Context().Err() != nil {
			return
		}
		log.Printf("[api] clip produce: %v", perr)
		writeErr(w, http.StatusInternalServerError, "clip render failed")
		return
	}
	d.serveClip(w, r, path, m, req)
}

func (d Deps) serveClip(w http.ResponseWriter, r *http.Request, path string, m *store.MediaFile, req clips.Request) {
	f, err := os.Open(path) //nolint:gosec // path is a clips-service cache entry (cache dir + hash key), not raw user input
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "clip file unreadable")
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("[api] close clip file: %v", cerr)
		}
	}()
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	ct := "video/mp4"
	switch ext {
	case "mkv":
		ct = "video/x-matroska"
	case "gif":
		ct = "image/gif"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	if r.URL.Query().Get("download") == "1" {
		name := clipName(m, req) + "." + ext
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat failed")
		return
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

func clipName(m *store.MediaFile, req clips.Request) string {
	title := m.Title
	if m.ShowTitle != "" {
		title = m.ShowTitle
		if m.Season > 0 && m.Episode > 0 {
			title += fmt.Sprintf(" S%02dE%02d", m.Season, m.Episode)
		}
	}
	if title == "" {
		title = fmt.Sprintf("clip-%d", m.ID)
	}
	var b strings.Builder
	for _, rn := range title {
		switch {
		case rn >= 'a' && rn <= 'z', rn >= 'A' && rn <= 'Z', rn >= '0' && rn <= '9', rn == ' ', rn == '-', rn == '_':
			b.WriteRune(rn)
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = fmt.Sprintf("clip-%d", m.ID)
	}
	return fmt.Sprintf("%s [%d-%d]", name, int(req.Start*1000), int(req.End*1000))
}
