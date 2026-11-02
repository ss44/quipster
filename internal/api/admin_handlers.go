package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quipster/internal/scan"
	"quipster/internal/store"
)

// handleScan enqueues a scan of the configured library folders. An explicit
// ?root=subfolder scans just that folder without pruning outside it.
func (d Deps) handleScan(w http.ResponseWriter, r *http.Request) {
	if active, err := d.St.HasActiveScanJob(); err == nil && active {
		writeJSON(w, map[string]any{"status": "already-running"})
		return
	}
	payload := map[string]any{}
	if root := strings.TrimSpace(r.URL.Query().Get("root")); root != "" {
		roots, err := scan.CleanRoots([]string{root})
		if err != nil || len(roots) == 0 {
			writeErr(w, http.StatusBadRequest, "invalid scan root")
			return
		}
		payload["roots"] = roots
		payload["prune"] = false
	} else {
		roots, err := d.St.GetLibraryPaths()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		payload["roots"] = roots
		payload["prune"] = true
	}
	pb, _ := json.Marshal(payload) //nolint:errcheck // the map holds only strings and a bool, so marshaling cannot fail
	if _, err := d.St.EnqueueJob("scan", string(pb), 3); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": "started"})
}

// handleConfigGet returns the media root, configured folders and the top-level
// folders available to choose from.
func (d Deps) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	folders, err := d.St.GetLibraryPaths()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if folders == nil {
		folders = []string{}
	}
	available := []string{}
	if entries, err := os.ReadDir(d.MediaDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				available = append(available, e.Name())
			}
		}
	}
	writeJSON(w, map[string]any{
		"mediaDir":      d.MediaDir,
		"folders":       folders,
		"available":     available,
		"cacheMaxBytes": d.CacheMaxBytes,
	})
}

// handleConfigPut validates and persists the library folders to scan.
func (d Deps) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folders []string `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	folders, err := scan.CleanRoots(body.Folders)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, rel := range folders {
		full := filepath.Join(d.MediaDir, filepath.FromSlash(rel))
		if info, err := os.Stat(full); err != nil || !info.IsDir() {
			writeErr(w, http.StatusBadRequest, "folder not found: "+rel)
			return
		}
	}
	if err := d.St.SetLibraryPaths(folders); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"folders": folders})
}

func (d Deps) handleReindex(w http.ResponseWriter, _ *http.Request) {
	n, err := d.Indexer.ReindexAll()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"enqueued": n})
}

func (d Deps) handleQueuePause(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"paused": true})
}

func (d Deps) handleQueueResume(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"paused": false})
}

// handleQueueClear drops queued jobs; running jobs keep running unless paused.
func (d Deps) handleQueueClear(w http.ResponseWriter, _ *http.Request) {
	n, err := d.St.ClearQueuedJobs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"cleared": n})
}

// reprobeRequest optionally narrows a reprobe to specific media ids.
type reprobeRequest struct {
	MediaIDs []int64 `json:"mediaIds"`
}

// handleReprobe re-runs probe (metadata/audio/subtitle selection) for media
// that already finished, e.g. after a pipeline improvement. A JSON body with
// mediaIds targets specific rows; an empty body reprobes every finished row.
// Rows inside an absence grace window are skipped so a flapping share does not
// manufacture error rows; the response reports how many were skipped so the
// admin knows the re-probe was not a silent no-op.
func (d Deps) handleReprobe(w http.ResponseWriter, r *http.Request) {
	var req reprobeRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid reprobe request: "+err.Error())
			return
		}
	}

	var ids []int64
	var skippedMissing int
	if len(req.MediaIDs) > 0 {
		ids = req.MediaIDs
	} else {
		var err error
		ids, err = d.St.MediaIDsByStatus("indexed", "no_subs", "error")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		skippedMissing, err = d.St.CountMissingByStatus("indexed", "no_subs", "error")
		if err != nil {
			log.Printf("[api] count missing media for reprobe: %v", err)
		}
	}

	specs := make([]store.JobSpec, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		payload, _ := json.Marshal(map[string]any{"mediaId": id}) //nolint:errcheck // the map holds only an int64, so marshaling cannot fail
		specs = append(specs, store.JobSpec{Type: "probe", Payload: string(payload), Priority: 2})
	}
	// Enqueue in one transaction: per-row commits would re-create the writer
	// contention this endpoint is meant to recover from.
	if _, err := d.St.EnqueueJobs(specs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"enqueued": len(specs), "skippedMissing": skippedMissing})
}

// handleQueueStop pauses the queue, aborts in-flight jobs and clears queued work.
func (d Deps) handleQueueStop(w http.ResponseWriter, _ *http.Request) {
	if err := d.St.SetQueuePaused(true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	canceled := 0
	if d.Runner != nil {
		canceled = d.Runner.CancelActive()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := d.St.JobStats()
		if err != nil || stats.Running == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cleared, err := d.St.ClearQueuedJobs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The wire key keeps the historical spelling; the web UI reads it.
	writeJSON(w, map[string]any{"paused": true, "cancelled": canceled, "cleared": cleared}) //nolint:misspell // legacy API key consumed by web/src/api.ts
}
