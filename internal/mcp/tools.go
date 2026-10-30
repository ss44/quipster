package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"quipster/internal/clips"
	"quipster/internal/indexer"
	"quipster/internal/search"
	"quipster/internal/store"
)

// addReadTools registers every tool granted by the read scope: queue/index
// status, quote search, scene resolution and library metadata.
func (s *Server) addReadTools(srv *mcpsdk.Server, token string) {
	addTool(srv, "get_processing_status",
		"Queue, scan and index health summary: job counts, pause state, running scan, indexed line totals, media status counts and Meilisearch reachability.",
		readOnly(), token, s.processingStatus)
	addTool(srv, "search_quotes",
		"Full-text search over indexed subtitle dialog. Returns chunk hits with media metadata, cue timestamps and thumbnail URLs, plus title-level fallback matches.",
		readOnly(), token, s.searchQuotes)
	addTool(srv, "suggest_quotes",
		"Autocomplete candidates for a partial dialog query.",
		readOnly(), token, s.suggestQuotes)
	addTool(srv, "check_quote_exists",
		"Fast existence test for a line or title: reports whether a quote matches and returns a few sample hits instead of a full result page.",
		readOnly(), token, s.checkQuoteExists)
	addTool(srv, "get_scene_by_line",
		"Resolve a quote (by chunkId, or mediaId plus startSec) to its scene: media metadata, the full cue span and surrounding context cues.",
		readOnly(), token, s.getSceneByLine)
	addTool(srv, "list_media",
		"Browse or filter library metadata by title, show title or path substring.",
		readOnly(), token, s.listMedia)
	addTool(srv, "get_media",
		"Full metadata for one library entry: probe info, subtitle tracks and cue count. Filesystem paths are redacted.",
		readOnly(), token, s.getMedia)
	addTool(srv, "get_job",
		"Inspect one processing job by id, including status, progress and error text.",
		readOnly(), token, s.getJob)
}

// addClipTools registers the tools granted by the clip scope.
func (s *Server) addClipTools(srv *mcpsdk.Server, token string) {
	addTool(srv, "create_clip",
		"Render (or queue) a short clip. Returns a ready playback URL, or a queued jobId for long accurate cuts. Rendering is deduplicated by cache key and bounded by the clip worker pool.",
		&mcpsdk.ToolAnnotations{IdempotentHint: true}, token, s.createClip)
	addTool(srv, "get_thumbnail",
		"Thumbnail URL for a moment in a media file. The image is rendered on demand by the thumbnail service when the URL is fetched.",
		readOnly(), token, s.getThumbnail)
}

// mediaJSON mirrors store.MediaFile without the on-disk path: MCP results
// must not leak filesystem locations, and the REST media endpoint keeps
// serving clients that need the field.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type mediaJSON struct {
	Title            string  `json:"title"`
	Container        string  `json:"container"`
	AudioLang        string  `json:"audioLang"`
	Error            string  `json:"error"`
	Status           string  `json:"status"`
	ShowTitle        string  `json:"showTitle"`
	VideoCodec       string  `json:"videoCodec"`
	AudioCodec       string  `json:"audioCodec"`
	Width            int     `json:"width"`
	Year             int     `json:"year"`
	Height           int     `json:"height"`
	Season           int     `json:"season"`
	Episode          int     `json:"episode"`
	Mtime            int64   `json:"mtime"`
	ID               int64   `json:"id"`
	DurationSec      float64 `json:"durationSec"`
	AddedAt          int64   `json:"addedAt"`
	StartTime        float64 `json:"startTime"`
	AudioStreamIndex int     `json:"audioStreamIndex"`
	SizeBytes        int64   `json:"sizeBytes"`
}

// publicMedia strips the filesystem path from a media row.
func publicMedia(m *store.MediaFile) mediaJSON {
	return mediaJSON{
		ID: m.ID, Title: m.Title, Container: m.Container, SizeBytes: m.SizeBytes,
		DurationSec: m.DurationSec, Width: m.Width, Height: m.Height,
		VideoCodec: m.VideoCodec, AudioCodec: m.AudioCodec,
		Year: m.Year, ShowTitle: m.ShowTitle, Season: m.Season, Episode: m.Episode,
		Mtime: m.Mtime, Status: m.Status, Error: m.Error, AddedAt: m.AddedAt,
		StartTime: m.StartTime, AudioStreamIndex: m.AudioStreamIndex, AudioLang: m.AudioLang,
	}
}

// trackJSON mirrors store.SubtitleTrack without the extracted file path.
type trackJSON struct {
	Source      string `json:"source"`
	Lang        string `json:"lang"`
	ID          int64  `json:"id"`
	MediaFileID int64  `json:"mediaFileId"`
	StreamIndex int    `json:"streamIndex"`
}

// publicTrack strips the extracted subtitle path from a track row.
func publicTrack(t store.SubtitleTrack) trackJSON {
	return trackJSON{Source: t.Source, Lang: t.Lang, ID: t.ID, MediaFileID: t.MediaFileID, StreamIndex: t.StreamIndex}
}

// jobJSON mirrors store.Job without the payload blob and the media path: the
// payload duplicates request arguments (already covered by the audit log) and
// the path must not leak through MCP results.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type jobJSON struct {
	MediaTitle  string  `json:"mediaTitle"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	MediaStatus string  `json:"mediaStatus"`
	WorkerID    string  `json:"workerId"`
	Error       string  `json:"error"`
	Progress    float64 `json:"progress"`
	CreatedAt   int64   `json:"createdAt"`
	StartedAt   int64   `json:"startedAt"`
	FinishedAt  int64   `json:"finishedAt"`
	MediaID     int64   `json:"mediaId"`
	ID          int64   `json:"id"`
	Attempts    int     `json:"attempts"`
	Priority    int     `json:"priority"`
	MediaCues   int64   `json:"mediaCues"`
	MediaChunks int64   `json:"mediaChunks"`
}

// publicJob strips sensitive fields from a job row.
func publicJob(j store.Job) jobJSON {
	return jobJSON{
		ID: j.ID, Type: j.Type, Status: j.Status, Priority: j.Priority, Attempts: j.Attempts,
		WorkerID: j.WorkerID, Error: j.Error, Progress: j.Progress,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		MediaID: j.MediaID, MediaTitle: j.MediaTitle, MediaStatus: j.MediaStatus,
		MediaCues: j.MediaCues, MediaChunks: j.MediaChunks,
	}
}

// statusIn / statusOut are the get_processing_status wire types.
type statusIn struct {
	IncludeRecentJobs bool `json:"includeRecentJobs,omitempty" jsonschema:"also include the ten most recent jobs"`
}

type statusOut struct {
	Queue       *store.JobStats  `json:"queue,omitempty"`
	Scan        *store.Scan      `json:"scan,omitempty"`
	Lines       *store.LineStats `json:"lines,omitempty"`
	MediaCounts map[string]int64 `json:"mediaCounts,omitempty"`
	RecentJobs  []jobJSON        `json:"recentJobs,omitempty"`
	MeiliUp     bool             `json:"meiliUp"`
}

// processingStatus answers "how is the library doing?" by reusing the same
// snapshot collector the admin status endpoint streams.
func (s *Server) processingStatus(_ context.Context, _ *mcpsdk.CallToolRequest, in statusIn) (*mcpsdk.CallToolResult, statusOut, error) {
	snap := s.deps.Events.Collect()
	out := statusOut{
		Queue:       snap.Queue,
		Scan:        snap.Scan,
		Lines:       snap.Lines,
		MediaCounts: snap.MediaCounts,
		MeiliUp:     snap.MeiliUp,
	}
	if in.IncludeRecentJobs {
		limit := min(len(snap.Recent), 10)
		out.RecentJobs = make([]jobJSON, 0, limit)
		for _, j := range snap.Recent[:limit] {
			out.RecentJobs = append(out.RecentJobs, publicJob(j))
		}
	}
	return nil, out, nil
}

// quoteHit is the agent-facing shape of one search hit: identity, metadata
// and timestamps, without the inline cue arrays (get_scene_by_line expands
// those on demand).
//
//nolint:govet // field grouping favors readability over optimal alignment.
type quoteHit struct {
	ChunkID    string  `json:"chunkId"`
	Text       string  `json:"text"`
	TextMarked string  `json:"textMarked,omitempty"`
	MediaID    int64   `json:"mediaId"`
	MediaTitle string  `json:"mediaTitle"`
	ShowTitle  string  `json:"showTitle"`
	Season     int     `json:"season"`
	Episode    int     `json:"episode"`
	Year       int     `json:"year"`
	StartSec   float64 `json:"startSec"`
	EndSec     float64 `json:"endSec"`
	ThumbURL   string  `json:"thumbUrl"`
}

// titleMatch is the agent-facing shape of a title-level fallback match.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type titleMatch struct {
	Title      string `json:"title"`
	ShowTitle  string `json:"showTitle"`
	Status     string `json:"status"`
	ThumbURL   string `json:"thumbUrl"`
	MediaID    int64  `json:"mediaId"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	Year       int    `json:"year"`
	NoSubtitle bool   `json:"noSubtitle"`
}

// toQuoteHit projects a search hit onto the agent-facing shape.
func toQuoteHit(h search.Hit, highlight bool) quoteHit {
	q := quoteHit{
		ChunkID: h.ChunkID, Text: h.Text, MediaID: h.MediaID,
		MediaTitle: h.MediaTitle, ShowTitle: h.ShowTitle,
		Season: h.Season, Episode: h.Episode, Year: h.Year,
		StartSec: h.StartSec, EndSec: h.EndSec, ThumbURL: h.ThumbURL,
	}
	if highlight {
		q.TextMarked = h.TextMarked
	}
	return q
}

// toTitleMatch projects a title fallback match.
func toTitleMatch(t search.TitleMatch) titleMatch {
	return titleMatch{
		Title: t.Title, ShowTitle: t.ShowTitle, Status: t.Status, ThumbURL: t.ThumbURL,
		MediaID: t.MediaID, Season: t.Season, Episode: t.Episode, Year: t.Year, NoSubtitle: t.NoSubtitle,
	}
}

// searchIn / searchOut are the search_quotes wire types. Highlight is a
// pointer so an absent field keeps the default (highlighted text included).
type searchIn struct {
	Highlight *bool  `json:"highlight,omitempty" jsonschema:"include highlighted text (default true)"`
	Query     string `json:"query" jsonschema:"dialog text to search for"`
	Show      string `json:"show,omitempty" jsonschema:"restrict hits to this show title"`
	Page      int    `json:"page,omitempty"`
	PerPage   int    `json:"perPage,omitempty"`
}

type searchOut struct {
	Hits         []quoteHit   `json:"hits"`
	TitleMatches []titleMatch `json:"titleMatches"`
	TotalHits    int64        `json:"totalHits"`
	TotalPages   int64        `json:"totalPages"`
	Page         int          `json:"page"`
	PerPage      int          `json:"perPage"`
}

// searchQuotes wraps search.Service.Search so agents see exactly what the
// REST search returns, including its stale-document filtering.
func (s *Server) searchQuotes(_ context.Context, _ *mcpsdk.CallToolRequest, in searchIn) (*mcpsdk.CallToolResult, searchOut, error) {
	resp, err := s.deps.Search.Search(in.Query, in.Show, in.Page, in.PerPage, "")
	if err != nil {
		return nil, searchOut{}, fmt.Errorf("search unavailable: %w", err)
	}
	highlight := in.Highlight == nil || *in.Highlight
	out := searchOut{
		TotalHits: resp.TotalHits, TotalPages: resp.TotalPages,
		Page: resp.Page, PerPage: resp.PerPage,
		Hits:         make([]quoteHit, 0, len(resp.Hits)),
		TitleMatches: make([]titleMatch, 0, len(resp.TitleMatches)),
	}
	for _, h := range resp.Hits {
		out.Hits = append(out.Hits, toQuoteHit(h, highlight))
	}
	for _, t := range resp.TitleMatches {
		out.TitleMatches = append(out.TitleMatches, toTitleMatch(t))
	}
	return nil, out, nil
}

// suggestIn / suggestOut are the suggest_quotes wire types.
type suggestIn struct {
	Query string `json:"query" jsonschema:"partial dialog text"`
	Limit int    `json:"limit,omitempty"`
}

type suggestOut struct {
	Suggestions []search.Suggestion `json:"suggestions"`
}

// suggestQuotes wraps search.Service.Suggest.
func (s *Server) suggestQuotes(_ context.Context, _ *mcpsdk.CallToolRequest, in suggestIn) (*mcpsdk.CallToolResult, suggestOut, error) {
	sg, err := s.deps.Search.Suggest(in.Query, in.Limit)
	if err != nil {
		return nil, suggestOut{}, fmt.Errorf("search unavailable: %w", err)
	}
	return nil, suggestOut{Suggestions: sg}, nil
}

// checkIn / checkOut are the check_quote_exists wire types.
type checkIn struct {
	Query       string `json:"query" jsonschema:"dialog text to test"`
	Title       string `json:"title,omitempty" jsonschema:"also match this string against library titles"`
	ExactPhrase bool   `json:"exactPhrase,omitempty" jsonschema:"only count hits containing the query verbatim"`
}

type checkOut struct {
	SampleHits   []quoteHit   `json:"sampleHits,omitempty"`
	TitleMatches []titleMatch `json:"titleMatches,omitempty"`
	MatchCount   int64        `json:"matchCount"`
	Exists       bool         `json:"exists"`
}

// checkQuoteExists runs a deliberately small search (five hits) so a cheap
// existence probe never pays for a full result page.
func (s *Server) checkQuoteExists(_ context.Context, _ *mcpsdk.CallToolRequest, in checkIn) (*mcpsdk.CallToolResult, checkOut, error) {
	resp, err := s.deps.Search.Search(in.Query, in.Title, 1, 5, "")
	if err != nil {
		return nil, checkOut{}, fmt.Errorf("search unavailable: %w", err)
	}
	out := checkOut{SampleHits: []quoteHit{}}
	for _, h := range resp.Hits {
		if in.ExactPhrase && !strings.Contains(strings.ToLower(h.Text), strings.ToLower(strings.TrimSpace(in.Query))) {
			continue
		}
		out.SampleHits = append(out.SampleHits, toQuoteHit(h, false))
		out.MatchCount++
	}
	if !in.ExactPhrase {
		out.MatchCount = resp.TotalHits
	}
	out.Exists = out.MatchCount > 0
	if in.Title != "" {
		tm, terr := s.deps.St.TitleMatches(in.Title, 5)
		if terr != nil {
			return nil, checkOut{}, terr
		}
		out.TitleMatches = make([]titleMatch, 0, len(tm))
		for _, m := range tm {
			out.TitleMatches = append(out.TitleMatches, titleMatch{
				Title: m.Title, ShowTitle: m.ShowTitle, Status: m.Status,
				MediaID: m.ID, Season: m.Season, Episode: m.Episode, Year: m.Year,
				NoSubtitle: m.Status == "no_subs",
			})
		}
		out.Exists = out.Exists || len(tm) > 0
	}
	return nil, out, nil
}

// sceneIn / sceneOut are the get_scene_by_line wire types.
type sceneIn struct {
	ChunkID     string  `json:"chunkId,omitempty" jsonschema:"chunk id in mediaId_startIdx-endIdx form, from search results"`
	MediaID     int64   `json:"mediaId,omitempty" jsonschema:"media id, used with startSec when chunkId is absent"`
	StartSec    float64 `json:"startSec,omitempty" jsonschema:"timestamp within the media to resolve"`
	ContextCues int     `json:"contextCues,omitempty" jsonschema:"context cues on each side (default 2, max 5)"`
}

//
//nolint:govet // field grouping favors readability over optimal alignment.
type sceneOut struct {
	Cues          []search.CueDTO `json:"cues"`
	ContextBefore []search.CueDTO `json:"contextBefore"`
	ContextAfter  []search.CueDTO `json:"contextAfter"`
	ChunkID       string          `json:"chunkId"`
	Text          string          `json:"text"`
	Media         mediaJSON       `json:"media"`
	ThumbURL      string          `json:"thumbUrl"`
}

// getSceneByLine expands a chunk into its full scene context. The context
// window is bounded so a large request cannot pull an entire subtitle track
// into one tool result.
func (s *Server) getSceneByLine(_ context.Context, _ *mcpsdk.CallToolRequest, in sceneIn) (*mcpsdk.CallToolResult, sceneOut, error) {
	ch, err := s.resolveSceneChunk(in)
	if err != nil {
		return nil, sceneOut{}, err
	}
	m, err := s.deps.St.GetMedia(ch.MediaFileID)
	if err != nil {
		return nil, sceneOut{}, err
	}
	if m == nil {
		return nil, sceneOut{}, fmt.Errorf("media %d not found", ch.MediaFileID)
	}
	span, before, after, err := s.deps.St.CuesAround(ch.MediaFileID, ch.StartCueIdx, ch.EndCueIdx, sceneContext(in.ContextCues))
	if err != nil {
		return nil, sceneOut{}, err
	}
	out := sceneOut{
		ChunkID:       indexer.DocID(ch.MediaFileID, ch.StartCueIdx, ch.EndCueIdx),
		Text:          ch.Text,
		Media:         publicMedia(m),
		Cues:          search.CueDTOs(span),
		ContextBefore: search.CueDTOs(before),
		ContextAfter:  search.CueDTOs(after),
		ThumbURL:      fmt.Sprintf("/api/thumb/%d?t=%.1f&w=320", ch.MediaFileID, ch.StartSec),
	}
	return nil, out, nil
}

// resolveSceneChunk locates the chunk for a scene request: by explicit chunk
// id, or by the chunk covering a timestamp within one media file.
func (s *Server) resolveSceneChunk(in sceneIn) (*store.ChunkWithMedia, error) {
	switch {
	case strings.TrimSpace(in.ChunkID) != "":
		mediaID, startIdx, endIdx, ok := parseChunkID(in.ChunkID)
		if !ok {
			return nil, errors.New("chunkId must be mediaId_startCueIdx-endCueIdx")
		}
		ch, err := s.deps.St.GetChunkByKey(mediaID, startIdx, endIdx)
		if err != nil {
			return nil, err
		}
		if ch == nil {
			return nil, fmt.Errorf("chunk %s not found", in.ChunkID)
		}
		return ch, nil
	case in.MediaID > 0:
		ch, err := s.deps.St.ChunkAtTime(in.MediaID, in.StartSec)
		if err != nil {
			return nil, err
		}
		if ch == nil {
			return nil, fmt.Errorf("no scene found for media %d at %.1fs", in.MediaID, in.StartSec)
		}
		return ch, nil
	default:
		return nil, errors.New("provide chunkId, or mediaId together with startSec")
	}
}

// sceneContext clamps the requested context window to sane bounds.
func sceneContext(requested int) int {
	if requested <= 0 {
		return 2
	}
	return min(requested, 5)
}

// parseChunkID splits a chunk id of the form "{mediaId}_{startIdx}-{endIdx}".
func parseChunkID(id string) (int64, int, int, bool) {
	underscore := strings.Index(id, "_")
	if underscore <= 0 {
		return 0, 0, 0, false
	}
	rest := id[underscore+1:]
	dash := strings.Index(rest, "-")
	if dash <= 0 {
		return 0, 0, 0, false
	}
	var mediaID int64
	var start, end int
	if _, err := fmt.Sscanf(id[:underscore], "%d", &mediaID); err != nil {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(rest[:dash], "%d", &start); err != nil {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(rest[dash+1:], "%d", &end); err != nil {
		return 0, 0, 0, false
	}
	return mediaID, start, end, true
}

// listMediaIn / listMediaOut are the list_media wire types. PerPage is
// capped at 50 to bound result sizes.
type listMediaIn struct {
	Query   string `json:"query,omitempty" jsonschema:"substring filter over title, show title and path"`
	Page    int    `json:"page,omitempty"`
	PerPage int    `json:"perPage,omitempty"`
}

type listMediaOut struct {
	Items   []mediaJSON `json:"items"`
	Total   int64       `json:"total"`
	Page    int         `json:"page"`
	PerPage int         `json:"perPage"`
}

// listMedia wraps store.ListMedia with MCP-side clamps and path redaction.
func (s *Server) listMedia(_ context.Context, _ *mcpsdk.CallToolRequest, in listMediaIn) (*mcpsdk.CallToolResult, listMediaOut, error) {
	perPage := in.PerPage
	if perPage <= 0 {
		perPage = 20
	}
	perPage = min(perPage, 50)
	page := max(in.Page, 1)
	res, err := s.deps.St.ListMedia(perPage, (page-1)*perPage, strings.TrimSpace(in.Query), "")
	if err != nil {
		return nil, listMediaOut{}, err
	}
	out := listMediaOut{Items: make([]mediaJSON, 0, len(res.Items)), Total: res.Total, Page: page, PerPage: perPage}
	for i := range res.Items {
		out.Items = append(out.Items, publicMedia(&res.Items[i]))
	}
	return nil, out, nil
}

// getMediaIn / getMediaOut are the get_media wire types.
type getMediaIn struct {
	MediaID int64 `json:"mediaId"`
}

type getMediaOut struct {
	Tracks   []trackJSON `json:"tracks"`
	Media    mediaJSON   `json:"media"`
	CueCount int64       `json:"cueCount"`
}

// getMedia enriches one media row with its subtitle tracks and cue count.
// Track enrichment is best-effort, mirroring the REST media endpoint.
func (s *Server) getMedia(_ context.Context, _ *mcpsdk.CallToolRequest, in getMediaIn) (*mcpsdk.CallToolResult, getMediaOut, error) {
	m, err := s.deps.St.GetMedia(in.MediaID)
	if err != nil {
		return nil, getMediaOut{}, err
	}
	if m == nil {
		return nil, getMediaOut{}, fmt.Errorf("media %d not found", in.MediaID)
	}
	out := getMediaOut{Media: publicMedia(m), Tracks: []trackJSON{}}
	if tracks, terr := s.deps.St.TracksForMedia(in.MediaID); terr == nil {
		for _, t := range tracks {
			out.Tracks = append(out.Tracks, publicTrack(t))
		}
	}
	if n, cerr := s.deps.St.CueCount(in.MediaID); cerr == nil {
		out.CueCount = n
	}
	return nil, out, nil
}

// getJobIn / getJobOut are the get_job wire types.
type getJobIn struct {
	JobID int64 `json:"jobId"`
}

type getJobOut struct {
	Job jobJSON `json:"job"`
}

// getJob polls one job, the companion of create_clip's queued status.
func (s *Server) getJob(_ context.Context, _ *mcpsdk.CallToolRequest, in getJobIn) (*mcpsdk.CallToolResult, getJobOut, error) {
	j, err := s.deps.St.GetJob(in.JobID)
	if err != nil {
		return nil, getJobOut{}, err
	}
	if j == nil {
		return nil, getJobOut{}, fmt.Errorf("job %d not found", in.JobID)
	}
	return nil, getJobOut{Job: publicJob(*j)}, nil
}

// thumbIn / thumbOut are the get_thumbnail wire types.
type thumbIn struct {
	MediaID int64   `json:"mediaId"`
	TimeSec float64 `json:"timeSec,omitempty" jsonschema:"timestamp to snapshot; defaults to the video start"`
	Width   int     `json:"width,omitempty" jsonschema:"320 (default) or 640"`
}

type thumbOut struct {
	URL   string `json:"url"`
	Width int    `json:"width"`
}

// getThumbnail validates the media and returns the thumbnail URL. The image
// itself is rendered (and cached) by the thumbs service when the URL is
// fetched, so a status query never triggers an ffmpeg run.
func (s *Server) getThumbnail(_ context.Context, _ *mcpsdk.CallToolRequest, in thumbIn) (*mcpsdk.CallToolResult, thumbOut, error) {
	m, err := s.deps.St.GetMedia(in.MediaID)
	if err != nil {
		return nil, thumbOut{}, err
	}
	if m == nil {
		return nil, thumbOut{}, fmt.Errorf("media %d not found", in.MediaID)
	}
	width := in.Width
	if width != 640 {
		width = 320
	}
	return nil, thumbOut{URL: fmt.Sprintf("/api/thumb/%d?t=%.1f&w=%d", in.MediaID, in.TimeSec, width), Width: width}, nil
}

// createClipIn / createClipOut are the create_clip wire types.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type createClipIn struct {
	PadMs    *int    `json:"padMs,omitempty" jsonschema:"symmetric padding in milliseconds (0-5000)"`
	MediaID  int64   `json:"mediaId"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Mode     string  `json:"mode,omitempty" jsonschema:"fast (default, stream copy), preview, or accurate"`
	Download bool    `json:"download,omitempty" jsonschema:"return an attachment download URL"`
}

type createClipOut struct {
	Status      string  `json:"status"`
	URL         string  `json:"url"`
	CacheKey    string  `json:"cacheKey"`
	JobID       int64   `json:"jobId,omitempty"`
	DurationSec float64 `json:"durationSec"`
}

// createClip validates the request, serves cache hits instantly, queues long
// accurate cuts as background jobs (mirroring the REST stream endpoint), and
// bounds synchronous renders with the clip worker semaphore so agents cannot
// starve ffmpeg capacity.
func (s *Server) createClip(ctx context.Context, _ *mcpsdk.CallToolRequest, in createClipIn) (*mcpsdk.CallToolResult, createClipOut, error) {
	if err := ctx.Err(); err != nil {
		return nil, createClipOut{}, err
	}
	req, err := s.clipRequest(in)
	if err != nil {
		return nil, createClipOut{}, err
	}
	_, span, verr := s.deps.Clips.Validate(req)
	if verr != nil {
		return nil, createClipOut{}, verr
	}
	key := s.deps.Clips.CacheKey(req)
	out := createClipOut{CacheKey: key, DurationSec: span, URL: clipStreamURL(req, in.Download)}
	if e, cerr := s.deps.Clips.Cached(key); cerr != nil {
		return nil, createClipOut{}, cerr
	} else if e != nil {
		s.deps.Clips.Touch(key)
		out.Status = "ready"
		return nil, out, nil
	}
	// Long accurate cuts follow the REST behavior: hand the render to the job
	// queue and return a jobId the agent can poll with get_job.
	if req.Mode == "accurate" && span > s.deps.Clips.Config().LongCutSec {
		jobID, _, qerr := s.deps.St.EnqueueUniqueJob("clip", clips.AsyncJobPayload(req), 1)
		if qerr != nil {
			return nil, createClipOut{}, qerr
		}
		out.Status, out.JobID = "queued", jobID
		return nil, out, nil
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	default:
		return nil, createClipOut{}, errors.New("clip renderer at capacity, retry shortly")
	}
	if _, _, perr := s.deps.Clips.Produce(ctx, req); perr != nil {
		return nil, createClipOut{}, perr
	}
	out.Status = "ready"
	return nil, out, nil
}

// clipRequest normalizes tool input into a clips.Request, applying the mode
// default and pad bounds shared with the REST stream endpoint.
func (s *Server) clipRequest(in createClipIn) (clips.Request, error) {
	req := clips.Request{MediaID: in.MediaID, Start: in.Start, End: in.End, Mode: in.Mode}
	if req.Mode == "" {
		req.Mode = "fast"
	}
	if in.PadMs != nil {
		if *in.PadMs < 0 || *in.PadMs > 5000 {
			return clips.Request{}, errors.New("padMs out of range (0-5000)")
		}
		req.PadMs = *in.PadMs
	}
	return req, nil
}

// clipStreamURL builds the public REST URL that serves (or renders) the clip,
// so MCP results never embed cache directories or filesystem paths.
func clipStreamURL(req clips.Request, download bool) string {
	d := 0
	if download {
		d = 1
	}
	return fmt.Sprintf("/api/clips/stream?mediaId=%d&start=%.3f&end=%.3f&mode=%s&padMs=%d&download=%d",
		req.MediaID, req.Start, req.End, req.Mode, req.PadMs, d)
}
