package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quipster/internal/store"
)

// writeMockBin writes an executable shell script masquerading as a media
// binary so tests can drive ffprobe/ffmpeg paths without real tooling.
func writeMockBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // mock binary must stay executable
		t.Fatal(err)
	}
	return p
}

// newTestStore opens a throwaway SQLite store for pipeline tests.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	return st
}

// addMedia inserts a media row and optionally creates the file on disk.
func addMedia(t *testing.T, st *store.Store, path string, create bool) *store.MediaFile {
	t.Helper()
	if create {
		if err := os.WriteFile(path, []byte("fake media bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, _, err := st.UpsertMedia(path, "matroska", 123, 1000, "T", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMedia(id)
	if err != nil || m == nil {
		t.Fatalf("get media %d: %v", id, err)
	}
	return m
}

// ffprobeJSON builds a canned ffprobe payload; extra is a list of raw stream objects.
func ffprobeJSON(extra ...string) string {
	streams := strings.Join(extra, ",\n")
	return fmt.Sprintf(`{"format":{"format_name":"matroska","duration":"120.0","start_time":"0"},"streams":[%s]}`, streams)
}

const videoStreamJSON = `{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720}`
const enAudioStreamJSON = `{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"tags":{"language":"eng"},"disposition":{"default":1}}`

func subStreamJSON(index int, codec, lang string) string {
	if lang == "" {
		return fmt.Sprintf(`{"index":%d,"codec_type":"subtitle","codec_name":%q}`, index, codec)
	}
	return fmt.Sprintf(`{"index":%d,"codec_type":"subtitle","codec_name":%q,"tags":{"language":%q}}`, index, codec, lang)
}

// writeFFprobeMock installs an ffprobe mock emitting the given JSON.
func writeFFprobeMock(t *testing.T, binDir, json string) {
	t.Helper()
	p := writeMockBin(t, binDir, "ffprobe", fmt.Sprintf("cat << 'FFEOF'\n%s\nFFEOF\n", json))
	t.Setenv("FFPROBE_BIN", p)
}

const embeddedSRT = "1\n00:00:01,000 --> 00:00:03,000\nHello there.\n\n2\n00:00:04,000 --> 00:00:06,000\nGeneral Kenobi.\n"

// writeFFmpegExtractMock installs an ffmpeg mock that dumps a valid SRT into
// its final argument (the extraction output path) and touches a marker file so
// tests can prove whether extraction actually ran.
func writeFFmpegExtractMock(t *testing.T, binDir, marker string) {
	t.Helper()
	body := fmt.Sprintf(`out=""
for a in "$@"; do out="$a"; done
touch %q
cat > "$out" <<'SRT'
%sSRT
`, marker, embeddedSRT)
	p := writeMockBin(t, binDir, "ffmpeg", body)
	t.Setenv("FFMPEG_BIN", p)
}

func writeFFmpegFailMock(t *testing.T, binDir string) {
	t.Helper()
	p := writeMockBin(t, binDir, "ffmpeg", "echo boom >&2\nexit 1\n")
	t.Setenv("FFMPEG_BIN", p)
}

func writeFFmpegGarbageMock(t *testing.T, binDir string) {
	t.Helper()
	// Invalid UTF-8 is the one payload astisub's SRT reader rejects outright.
	p := writeMockBin(t, binDir, "ffmpeg", `out=""
for a in "$@"; do out="$a"; done
printf '\377\376 not utf8\n' > "$out"
`)
	t.Setenv("FFMPEG_BIN", p)
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestJobHandlerBadPayload(t *testing.T) {
	st := newTestStore(t)
	p := &Pipeline{St: st}
	err := p.JobHandler()(context.Background(), &store.Job{Payload: "not json"}, func(float64) {})
	if err == nil || !strings.Contains(err.Error(), "bad payload") {
		t.Fatalf("want bad payload error, got %v", err)
	}
}

func TestJobHandlerMediaNotFound(t *testing.T) {
	st := newTestStore(t)
	p := &Pipeline{St: st}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: `{"mediaId":424242}`}, func(float64) {}); err != nil {
		t.Fatalf("missing media row must be skipped silently, got %v", err)
	}
}

func TestJobHandlerStoreClosed(t *testing.T) {
	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{St: st}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: `{"mediaId":1}`}, func(float64) {}); err == nil {
		t.Fatal("expected store error, got nil")
	}
}

func TestJobHandlerMediaFileMissing(t *testing.T) {
	st := newTestStore(t)
	m := addMedia(t, st, filepath.Join(t.TempDir(), "gone.mkv"), false)
	p := &Pipeline{St: st}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatalf("missing file must be recorded, not failed: %v", err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "error" || !strings.Contains(got.Error, "media file missing on disk") {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
}

func TestJobHandlerProbeFails(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeMockBin(t, binDir, "ffprobe", "exit 1\n")
	t.Setenv("FFPROBE_BIN", filepath.Join(binDir, "ffprobe"))
	p := &Pipeline{St: st}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatalf("probe failure must be recorded, not failed: %v", err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "error" || !strings.Contains(got.Error, "ffprobe") {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
	assertProbeLogs(t, logs, "stage=metadata failed elapsed=", "stage=complete outcome=media_error total_elapsed=")
}

func TestJobHandlerEmbeddedSuccess(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, enAudioStreamJSON, subStreamJSON(2, "subrip", "eng")))
	writeFFmpegExtractMock(t, binDir, filepath.Join(binDir, "marker"))
	p := &Pipeline{St: st, SubsCacheDir: cacheDir, ExtractConns: 1}

	var seen []float64
	err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(v float64) { seen = append(seen, v) })
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(binDir, "marker")); serr != nil {
		t.Fatalf("extraction did not run: %v", serr)
	}
	assertProbeStored(t, st, m.ID)
	assertEmbeddedTrack(t, st, m.ID, cacheDir)
	if len(seen) < 3 || seen[0] != 0.05 {
		t.Fatalf("progress calls = %v", seen)
	}
	assertProbeLogs(t, logs, fmt.Sprintf("media %d job 0:", m.ID), "stage=metadata probing", "gpu=false",
		"stage=metadata probe ok elapsed=", `container="matroska" duration_sec=120.000 video="h264"`,
		"width=1280 height=720", `audio="aac" audio_streams=1 audio_ordinal=0 audio_lang="en" subtitle_streams=1`,
		"stage=sidecar none selected", `stage=embedded_selection selected index=2 ordinal=0 codec="subrip" lang="en"`,
		"stage=extraction_queue waiting", "stage=extraction_queue acquired wait_elapsed=", "active=1 capacity=1",
		"stage=extraction started", "full-file container scan, storage-bound", "CPU subtitle conversion, no GPU",
		"stage=extraction succeeded elapsed=", "stage=embedded_parse parsed cues=2 elapsed=",
		"stage=cue_store stored cues=2", "stage=chunk_build building cues=2", "stage=chunk_store stored chunks=",
		"stage=index_enqueue enqueued (indexing runs separately)", "stage=complete outcome=index_queued total_elapsed=")
	if len(p.sem()) != 0 {
		t.Fatal("successful extraction leaked capacity")
	}
}

// assertProbeStored verifies the probe result was persisted onto the media row.
func assertProbeStored(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	got, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioStreamIndex != 0 || got.AudioLang != "en" || got.DurationSec != 120 || got.VideoCodec != "h264" {
		t.Fatalf("probe update wrong: %+v", got)
	}
}

// assertEmbeddedTrack verifies an embedded subtitle track with parsed cues,
// built chunks and a follow-up index job landed in the store.
func assertEmbeddedTrack(t *testing.T, st *store.Store, id int64, cacheDir string) {
	t.Helper()
	tracks, err := st.TracksForMedia(id)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks = %v, %v", tracks, err)
	}
	if tracks[0].Source != "embedded" || tracks[0].Lang != "en" || tracks[0].StreamIndex != 2 {
		t.Fatalf("track = %+v", tracks[0])
	}
	wantPath := filepath.Join(cacheDir, fmt.Sprintf("%d.srt", id))
	if tracks[0].ExtractedPath != wantPath {
		t.Fatalf("extracted path = %q, want %q", tracks[0].ExtractedPath, wantPath)
	}
	n, err := st.CueCount(id)
	if err != nil || n != 2 {
		t.Fatalf("cues = %d, %v, want 2", n, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM chunks WHERE media_file_id=?`, id) == 0 {
		t.Fatal("expected chunks to be built")
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM jobs WHERE type='index'`); n != 1 {
		t.Fatalf("index jobs = %d, want 1", n)
	}
}

func TestJobHandlerDriftOffset(t *testing.T) {
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	// en-US exercises the en- prefix match; start_time > 1 triggers the
	// PTS drift mitigation which pulls cues back and clamps negatives.
	drift := strings.Replace(ffprobeJSON(videoStreamJSON, enAudioStreamJSON, subStreamJSON(2, "subrip", "en-US")),
		`"start_time":"0"`, `"start_time":"2.5"`, 1)
	writeFFprobeMock(t, binDir, drift)
	writeFFmpegExtractMock(t, binDir, filepath.Join(binDir, "marker"))
	p := &Pipeline{St: st, SubsCacheDir: cacheDir, ExtractConns: 1}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	span, _, _, err := st.CuesInRange(m.ID, 0, 10)
	if err != nil || len(span) != 2 {
		t.Fatalf("cues = %v, %v", span, err)
	}
	if span[0].StartSec != 0 || span[0].EndSec != 0.5 {
		t.Fatalf("drift offset not applied: %+v", span[0])
	}
}

func TestJobHandlerNoSubs(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON))
	p := &Pipeline{St: st, SubsCacheDir: t.TempDir()}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "no_subs" || got.Error != "" {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
	if got.AudioStreamIndex != -1 {
		t.Fatalf("no audio must set ordinal -1, got %d", got.AudioStreamIndex)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM jobs WHERE type='index'`); n != 0 {
		t.Fatal("no index job expected")
	}
	assertProbeLogs(t, logs, "stage=embedded_selection none eligible subtitle_streams=0", "stage=no_subs", "stage=complete outcome=no_subs total_elapsed=")
}

func TestJobHandlerBitmapSubsSkipped(t *testing.T) {
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, enAudioStreamJSON, subStreamJSON(2, "pgs", "eng")))
	writeFFmpegFailMock(t, binDir)
	p := &Pipeline{St: st, SubsCacheDir: t.TempDir()}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "no_subs" {
		t.Fatalf("bitmap-only media must be no_subs, got %q (%q)", got.Status, got.Error)
	}
}

func TestJobHandlerUntaggedSub(t *testing.T) {
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(1, "subrip", "")))
	writeFFmpegExtractMock(t, binDir, filepath.Join(binDir, "marker"))
	p := &Pipeline{St: st, SubsCacheDir: cacheDir}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	tracks, err := st.TracksForMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Source != "embedded" || tracks[0].Lang != "" {
		t.Fatalf("untagged stream must be sole fallback, got %+v", tracks)
	}
}

func TestJobHandlerSidecarWins(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	mediaPath := filepath.Join(dir, "Film.mkv")
	m := addMedia(t, st, mediaPath, true)
	sidecar := filepath.Join(dir, "Film.en.srt")
	if err := os.WriteFile(sidecar, []byte(embeddedSRT), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, enAudioStreamJSON, subStreamJSON(2, "subrip", "eng")))
	writeFFmpegExtractMock(t, binDir, filepath.Join(binDir, "marker"))
	p := &Pipeline{St: st, SubsCacheDir: cacheDir}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(binDir, "marker")); serr == nil {
		t.Fatal("sidecar must prevent embedded extraction")
	}
	tracks, err := st.TracksForMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Source != "sidecar" || tracks[0].ExtractedPath != sidecar {
		t.Fatalf("track = %+v, want sidecar %q", tracks, sidecar)
	}
	assertProbeLogs(t, logs, fmt.Sprintf("stage=sidecar selected path=%q; parsing", sidecar), "stage=sidecar parsed cues=2 elapsed=", "stage=cue_store storing source=sidecar cues=2")
	if strings.Contains(logs.String(), "stage=extraction started") {
		t.Fatal("sidecar path logged an extraction that did not occur")
	}
}

func TestJobHandlerSidecarUnparsable(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "Film.mkv"), true)
	if err := os.WriteFile(filepath.Join(dir, "Film.en.srt"), []byte("1\n00:00:00,000 --> notatime\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, enAudioStreamJSON, subStreamJSON(2, "subrip", "eng")))
	writeFFmpegExtractMock(t, binDir, filepath.Join(binDir, "marker"))
	p := &Pipeline{St: st, SubsCacheDir: cacheDir}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || !strings.Contains(got.Error, "sidecar unparsable") {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
	tracks, err := st.TracksForMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Source != "embedded" {
		t.Fatalf("track = %+v, want embedded fallback", tracks)
	}
	assertProbeLogs(t, logs, "stage=sidecar parse failed elapsed=", "falling back to embedded", "stage=embedded_parse parsed cues=2")
}

func TestJobHandlerExtractionFails(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(2, "subrip", "eng")))
	writeFFmpegFailMock(t, binDir)
	p := &Pipeline{St: st, SubsCacheDir: t.TempDir()}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "no_subs" || !strings.Contains(got.Error, "embedded extraction failed") {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
	n, err := st.CueCount(m.ID)
	if err != nil || n != 0 {
		t.Fatalf("cues = %d, %v, want 0", n, err)
	}
	assertProbeLogs(t, logs, "stage=extraction failed elapsed=", "boom", "stage=complete outcome=no_subs total_elapsed=")
	if len(p.sem()) != 0 {
		t.Fatal("failed extraction leaked capacity")
	}
}

func TestJobHandlerEmbeddedUnparsable(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(2, "subrip", "eng")))
	writeFFmpegGarbageMock(t, binDir)
	p := &Pipeline{St: st, SubsCacheDir: t.TempDir()}
	if err := p.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "no_subs" || !strings.Contains(got.Error, "embedded subs unparsable") {
		t.Fatalf("status = %q error = %q", got.Status, got.Error)
	}
	assertProbeLogs(t, logs, "stage=embedded_parse failed elapsed=", "stage=complete outcome=no_subs total_elapsed=")
}

func TestJobHandlerReusesCache(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	if err := st.SetMediaStatus(m.ID, "indexed", ""); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(cacheDir, fmt.Sprintf("%d.srt", m.ID))
	if err := os.WriteFile(cached, []byte(embeddedSRT), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(2, "subrip", "eng")))
	// If extraction ran despite the cache, it fails loudly and leaves no track.
	p := writeMockBin(t, binDir, "ffmpeg", fmt.Sprintf("touch %q\nexit 1\n", filepath.Join(binDir, "marker")))
	t.Setenv("FFMPEG_BIN", p)
	pl := &Pipeline{St: st, SubsCacheDir: cacheDir}
	if err := pl.JobHandler()(context.Background(), &store.Job{Payload: fmt.Sprintf(`{"mediaId":%d}`, m.ID)}, func(float64) {}); err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(binDir, "marker")); serr == nil {
		t.Fatal("cached extraction must be reused without invoking ffmpeg")
	}
	tracks, err := st.TracksForMedia(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].ExtractedPath != cached {
		t.Fatalf("track = %+v, want reused cache %q", tracks, cached)
	}
	n, err := st.CueCount(m.ID)
	if err != nil || n != 2 {
		t.Fatalf("cues = %d, %v, want 2", n, err)
	}
	assertProbeLogs(t, logs, fmt.Sprintf("stage=extraction cache reused path=%q", cached), "stage=embedded_parse parsed cues=2")
	if strings.Contains(logs.String(), "stage=extraction_queue") || strings.Contains(logs.String(), "stage=extraction started") {
		t.Fatal("cache hit must neither queue nor start extraction")
	}
}
