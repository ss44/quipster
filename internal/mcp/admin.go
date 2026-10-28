package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/scan"
	"quipster/internal/store"
)

// addAdminTools registers the operator tools. Callers are gated twice: the
// token must carry the admin scope and Config.AllowAdmin must be enabled.
func (s *Server) addAdminTools(srv *mcpsdk.Server, token string) {
	addTool(srv, "trigger_scan",
		"Enqueue a library scan. Deduplicated while a scan is already queued or running.",
		&mcpsdk.ToolAnnotations{IdempotentHint: true}, token, s.triggerScan)
	addTool(srv, "reindex_library",
		"Re-queue every chunk for search reindexing.",
		&mcpsdk.ToolAnnotations{IdempotentHint: true}, token, s.reindexLibrary)
	addTool(srv, "queue_control",
		"Control the processing queue: pause, resume, stop (pause plus cancel active jobs), or clear queued jobs. stop and clear discard pending work.",
		destructive(), token, s.queueControl)
	addTool(srv, "list_jobs",
		"List recent processing jobs with filters, plus queue stats. Filesystem paths are redacted.",
		readOnly(), token, s.listJobs)
	addTool(srv, "get_library_config",
		"Read the configured scan folders and the top-level folders available for selection.",
		readOnly(), token, s.getLibraryConfig)
	addTool(srv, "set_library_config",
		"Replace the configured scan folders. Folders must be relative names under the media root and must exist.",
		destructive(), token, s.setLibraryConfig)
}

// scanIn / scanOut are the trigger_scan wire types.
type scanIn struct {
	Root string `json:"root,omitempty" jsonschema:"scan only this subfolder of the media root, without pruning outside it"`
}

type scanOut struct {
	Status string `json:"status"`
	JobID  int64  `json:"jobId,omitempty"`
}

// triggerScan mirrors the REST admin scan endpoint: skip when a scan is
// already active, otherwise enqueue the full-library (or single-root) scan.
func (s *Server) triggerScan(_ context.Context, _ *mcpsdk.CallToolRequest, in scanIn) (*mcpsdk.CallToolResult, scanOut, error) {
	if active, err := s.deps.St.HasActiveScanJob(); err == nil && active {
		return nil, scanOut{Status: "already-running"}, nil
	}
	payload, err := s.scanPayload(in.Root)
	if err != nil {
		return nil, scanOut{}, err
	}
	id, err := s.deps.St.EnqueueJob("scan", payload, 3)
	if err != nil {
		return nil, scanOut{}, err
	}
	return nil, scanOut{Status: "started", JobID: id}, nil
}

// scanPayload builds the scan job payload for a root-scoped or full scan.
func (s *Server) scanPayload(root string) (string, error) {
	payload := map[string]any{}
	if strings.TrimSpace(root) != "" {
		roots, err := scan.CleanRoots([]string{root})
		if err != nil || len(roots) == 0 {
			return "", errors.New("invalid scan root")
		}
		payload["roots"] = roots
		payload["prune"] = false
	} else {
		roots, err := s.deps.St.GetLibraryPaths()
		if err != nil {
			return "", err
		}
		payload["roots"] = roots
		payload["prune"] = true
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// reindexOut is the reindex_library wire type.
type reindexOut struct {
	Enqueued int `json:"enqueued"`
}

// reindexLibrary wraps Indexer.ReindexAll.
func (s *Server) reindexLibrary(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, reindexOut, error) {
	n, err := s.deps.Indexer.ReindexAll()
	if err != nil {
		return nil, reindexOut{}, err
	}
	return nil, reindexOut{Enqueued: n}, nil
}

// queueIn / queueOut are the queue_control wire types.
type queueIn struct {
	Action string `json:"action" jsonschema:"pause, resume, stop, or clear"`
}

type queueOut struct {
	Paused   bool  `json:"paused,omitempty"`
	Canceled int64 `json:"canceled,omitempty"`
	Cleared  int64 `json:"cleared,omitempty"`
}

// queueControl mirrors the four REST queue endpoints.
func (s *Server) queueControl(_ context.Context, _ *mcpsdk.CallToolRequest, in queueIn) (*mcpsdk.CallToolResult, queueOut, error) {
	switch in.Action {
	case "pause", "resume", "stop":
		return s.queueControlMutating(in.Action)
	case "clear":
		n, err := s.deps.St.ClearQueuedJobs()
		if err != nil {
			return nil, queueOut{}, err
		}
		return nil, queueOut{Cleared: n}, nil
	default:
		return nil, queueOut{}, errors.New("action must be pause, resume, stop or clear")
	}
}

// queueControlMutating applies the pause/resume/stop actions, which share the
// queue-paused flag.
func (s *Server) queueControlMutating(action string) (*mcpsdk.CallToolResult, queueOut, error) {
	paused := action != "resume"
	if err := s.deps.St.SetQueuePaused(paused); err != nil {
		return nil, queueOut{}, err
	}
	out := queueOut{Paused: paused}
	if action != "stop" {
		return nil, out, nil
	}
	if s.deps.Runner != nil {
		out.Canceled = int64(s.deps.Runner.CancelActive())
	}
	waitQueueIdle(s.deps.St, 3*time.Second)
	cleared, err := s.deps.St.ClearQueuedJobs()
	if err != nil {
		return nil, out, err
	}
	out.Cleared = cleared
	return nil, out, nil
}

// waitQueueIdle polls job stats until nothing is running or the deadline
// passes, mirroring the REST stop endpoint's bounded wait.
func waitQueueIdle(st *store.Store, within time.Duration) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		stats, err := st.JobStats()
		if err != nil || stats.Running == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// listJobsIn / listJobsOut are the list_jobs wire types.
type listJobsIn struct {
	Limit   int   `json:"limit,omitempty" jsonschema:"1-100, default 20"`
	MediaID int64 `json:"mediaId,omitempty" jsonschema:"only jobs for this media id"`
}

type listJobsOut struct {
	Stats *store.JobStats `json:"stats,omitempty"`
	Jobs  []jobJSON       `json:"jobs"`
}

// listJobs wraps store.ListJobs with an optional media filter, matching the
// "why did episode X fail?" workflow from the design notes.
func (s *Server) listJobs(_ context.Context, _ *mcpsdk.CallToolRequest, in listJobsIn) (*mcpsdk.CallToolResult, listJobsOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	jobs, err := s.deps.St.ListJobs(limit)
	if err != nil {
		return nil, listJobsOut{}, err
	}
	out := listJobsOut{Jobs: []jobJSON{}}
	for _, j := range jobs {
		if in.MediaID > 0 && j.MediaID != in.MediaID {
			continue
		}
		out.Jobs = append(out.Jobs, publicJob(j))
	}
	if stats, serr := s.deps.St.JobStats(); serr == nil {
		out.Stats = stats
	}
	return nil, out, nil
}

// libraryConfigOut is the get_library_config wire type. The absolute media
// root is intentionally omitted: folder names are relative and safe to show,
// server paths are not.
type libraryConfigOut struct {
	Folders   []string `json:"folders"`
	Available []string `json:"available"`
}

// getLibraryConfig lists the configured scan folders plus the folders
// available under the media root.
func (s *Server) getLibraryConfig(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, libraryConfigOut, error) {
	folders, err := s.deps.St.GetLibraryPaths()
	if err != nil {
		return nil, libraryConfigOut{}, err
	}
	if folders == nil {
		folders = []string{}
	}
	return nil, libraryConfigOut{Folders: folders, Available: availableFolders(s.deps.MediaDir)}, nil
}

// availableFolders lists non-hidden top-level directories under the media
// root. Read errors degrade to an empty list, mirroring the REST endpoint.
func availableFolders(mediaDir string) []string {
	out := []string{}
	entries, err := os.ReadDir(mediaDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// setLibraryConfigIn is the set_library_config wire type.
type setLibraryConfigIn struct {
	Folders []string `json:"folders" jsonschema:"relative folder names under the media root"`
}

// setLibraryConfig validates and persists the scan folder list, applying the
// same traversal and existence checks as the REST config endpoint.
func (s *Server) setLibraryConfig(_ context.Context, _ *mcpsdk.CallToolRequest, in setLibraryConfigIn) (*mcpsdk.CallToolResult, libraryConfigOut, error) {
	folders, err := scan.CleanRoots(in.Folders)
	if err != nil {
		return nil, libraryConfigOut{}, err
	}
	for _, rel := range folders {
		full := filepath.Join(s.deps.MediaDir, filepath.FromSlash(rel))
		if info, serr := os.Stat(full); serr != nil || !info.IsDir() {
			return nil, libraryConfigOut{}, fmt.Errorf("folder not found: %s", rel)
		}
	}
	if err := s.deps.St.SetLibraryPaths(folders); err != nil {
		return nil, libraryConfigOut{}, err
	}
	return nil, libraryConfigOut{Folders: folders, Available: availableFolders(s.deps.MediaDir)}, nil
}
