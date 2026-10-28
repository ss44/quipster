package indexer

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"quipster/internal/store"
)

// seedMedia creates one media with subtitle cues and chunks (id1) and one
// media marked no_subs without subtitles (id2).
func seedMedia(t *testing.T, st *store.Store) (id1, id2 int64) {
	t.Helper()
	id1, _, err := st.UpsertMedia("/tv/lost-s01e01.mkv", "matroska", 100, 1, "Pilot", 2004, 1, 1, "Lost")
	if err != nil {
		t.Fatal(err)
	}
	track, err := st.ReplaceTrack(id1, &store.SubtitleTrack{Source: "embedded", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	cues := make([]store.Cue, 0, 4)
	for i := 0; i < 4; i++ {
		cues = append(cues, store.Cue{
			MediaFileID: id1, TrackID: track, Idx: i,
			StartSec: float64(i), EndSec: float64(i) + 1, Text: "line",
		})
	}
	err = st.ReplaceCues(id1, track, cues)
	if err != nil {
		t.Fatal(err)
	}
	chunks := []store.Chunk{
		{MediaFileID: id1, StartCueIdx: 0, EndCueIdx: 1, Text: "hello world"},
		{MediaFileID: id1, StartCueIdx: 2, EndCueIdx: 3, Text: "goodbye moon"},
	}
	err = st.ReplaceChunks(id1, chunks)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err = st.UpsertMedia("/movies/gone.mp4", "mp4", 50, 1, "Gone in 60 Seconds", 2000, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	err = st.SetMediaStatus(id2, "no_subs", "")
	if err != nil {
		t.Fatal(err)
	}
	return id1, id2
}

func TestDocIDAndDisplayTitle(t *testing.T) {
	if got := DocID(7, 0, 9); got != "7_0-9" {
		t.Errorf("DocID = %q, want 7_0-9", got)
	}
	cases := []struct {
		title, show, want string
		season, ep, year  int
	}{
		{"The Matrix", "", "The Matrix", 0, 0, 0},
		{"The Matrix", "", "The Matrix (1999)", 0, 0, 1999},
		{"", "", "(unknown)", 0, 0, 0},
		{"", "Severance", "Severance S01E03", 1, 3, 0},
		{"", "Severance", "Severance S01E03 (2022)", 1, 3, 2022},
		{"x", "Show", "Show (2020)", 0, 0, 2020},
	}
	for _, c := range cases {
		if got := displayTitle(c.title, c.show, c.season, c.ep, c.year); got != c.want {
			t.Errorf("displayTitle(%q,%q,%d,%d,%d) = %q, want %q", c.title, c.show, c.season, c.ep, c.year, got, c.want)
		}
	}
}

func TestHealthy(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	ix := New(st, f.url(), "")
	if !ix.Healthy() {
		t.Fatal("expected healthy against fake server")
	}
	url := f.srv.URL
	f.srv.Close()
	ix2 := New(st, url, "")
	if ix2.Healthy() {
		t.Fatal("expected unhealthy against closed server")
	}
}

func TestClientAccessor(t *testing.T) {
	f := newFakeMeili(t)
	ix := New(newTestStore(t), f.url(), "")
	if ix.Client() == nil {
		t.Fatal("Client() returned nil")
	}
}

func TestEnsureSettingsCreatesMissingIndex(t *testing.T) {
	f := newFakeMeili(t)
	ix := New(newTestStore(t), f.url(), "")
	if err := ix.EnsureSettings(); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	if f.calls("POST", "/indexes") != 1 {
		t.Errorf("index should have been created once, got %d", f.calls("POST", "/indexes"))
	}
	if f.settingsCalls != 1 {
		t.Errorf("settingsCalls = %d, want 1", f.settingsCalls)
	}
	if f.typoCalls != 1 {
		t.Errorf("typoCalls = %d, want 1", f.typoCalls)
	}
}

func TestEnsureSettingsExistingIndexSkipsCreate(t *testing.T) {
	f := newFakeMeili(t)
	f.indexExists = true
	ix := New(newTestStore(t), f.url(), "")
	if err := ix.EnsureSettings(); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	if f.calls("POST", "/indexes") != 0 {
		t.Errorf("existing index must not be recreated, got %d create calls", f.calls("POST", "/indexes"))
	}
	if f.settingsCalls != 1 || f.typoCalls != 1 {
		t.Errorf("settingsCalls=%d typoCalls=%d, want 1 and 1", f.settingsCalls, f.typoCalls)
	}
}

func TestEnsureSettingsCreateFailure(t *testing.T) {
	f := newFakeMeili(t)
	f.failCreate = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "create index") {
		t.Fatalf("want create index error, got %v", err)
	}
}

func TestEnsureSettingsCreateRaceRecovers(t *testing.T) {
	// Another instance created the index concurrently: our create call fails
	// but the retry GetIndex must succeed and settings still get applied.
	f := newFakeMeili(t)
	f.createFlips = true
	ix := New(newTestStore(t), f.url(), "")
	if err := ix.EnsureSettings(); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	if f.settingsCalls != 1 || f.typoCalls != 1 {
		t.Errorf("settingsCalls=%d typoCalls=%d, want 1 and 1", f.settingsCalls, f.typoCalls)
	}
}

func TestEnsureSettingsCreateWaitFails(t *testing.T) {
	f := newFakeMeili(t)
	f.failTaskGet = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "create index wait") {
		t.Fatalf("want create index wait error, got %v", err)
	}
}

func TestEnsureSettingsUpdateFails(t *testing.T) {
	f := newFakeMeili(t)
	f.indexExists = true
	f.failSettings = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "update settings") {
		t.Fatalf("want update settings error, got %v", err)
	}
}

func TestEnsureSettingsUpdateWaitFails(t *testing.T) {
	f := newFakeMeili(t)
	f.indexExists = true
	f.failTaskGet = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "settings wait") {
		t.Fatalf("want settings wait error, got %v", err)
	}
}

func TestEnsureSettingsTypoPatchFails(t *testing.T) {
	f := newFakeMeili(t)
	f.indexExists = true
	f.failTypo = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "typo settings") || !strings.Contains(err.Error(), "typo boom") {
		t.Fatalf("want typo settings error with body, got %v", err)
	}
}

func TestEnsureSettingsTypoPatchTransportError(t *testing.T) {
	f := newFakeMeili(t)
	f.indexExists = true
	f.abortTypo = true
	ix := New(newTestStore(t), f.url(), "")
	err := ix.EnsureSettings()
	if err == nil || !strings.Contains(err.Error(), "typo settings") {
		t.Fatalf("want typo settings transport error, got %v", err)
	}
}

func TestIndexMediaSkipsDeleteOnFirstIndex(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	if err := ix.IndexMedia(id); err != nil {
		t.Fatalf("IndexMedia: %v", err)
	}
	if got := f.calls("POST", "/indexes/"+IndexName+"/documents/delete"); got != 0 {
		t.Errorf("first-time indexing must skip the delete round-trip, got %d deletes", got)
	}
	docs := f.addedDocs()
	if len(docs) != 2 {
		t.Fatalf("added docs = %d, want 2", len(docs))
	}
	doc := f.docs[DocID(id, 0, 1)]
	if doc == nil || doc["text"] != "hello world" {
		t.Errorf("doc for chunk 0-1 wrong: %+v", doc)
	}
	if doc["mediaTitle"] != "Lost S01E01 (2004)" {
		t.Errorf("mediaTitle = %v, want Lost S01E01 (2004)", doc["mediaTitle"])
	}
	if doc["showTitle"] != "Lost" {
		t.Errorf("showTitle = %v, want Lost", doc["showTitle"])
	}
	pending, err := st.PendingIndexChunks(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("chunks should be latched as indexed, got %d pending", len(pending))
	}
	// The ever-indexed latch is only set by MarkIndexed, not by IndexMedia.
	ever, err := st.EverIndexed(id)
	if err != nil {
		t.Fatal(err)
	}
	if ever {
		t.Error("IndexMedia must not latch ever_indexed; JobHandler does")
	}
}

func TestIndexMediaDeletesBeforeAddingWhenEverIndexed(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "")
	if err := ix.IndexMedia(id); err != nil {
		t.Fatalf("IndexMedia: %v", err)
	}
	filters := f.deleteFilters()
	if len(filters) != 1 || filters[0] != "mediaId = 1" {
		t.Errorf("delete filters = %v, want [mediaId = 1]", filters)
	}
	delIdx := f.firstIndex("POST", "/indexes/"+IndexName+"/documents/delete")
	addIdx := f.firstIndex("POST", "/indexes/"+IndexName+"/documents")
	if delIdx == -1 || addIdx == -1 || delIdx > addIdx {
		t.Errorf("delete (idx %d) must precede document add (idx %d)", delIdx, addIdx)
	}
}

func TestIndexMediaDeleteError(t *testing.T) {
	f := newFakeMeili(t)
	f.failDelete = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "")
	err := ix.IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "delete docs") {
		t.Fatalf("want delete docs error, got %v", err)
	}
}

func TestIndexMediaDeleteWaitError(t *testing.T) {
	f := newFakeMeili(t)
	f.failTaskGet = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "")
	err := ix.IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "delete wait") {
		t.Fatalf("want delete wait error, got %v", err)
	}
}

func TestIndexMediaNoPendingChunks(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	latchChunksIndexed(t, st, id)
	if err := st.MarkIndexed(id); err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "")
	if err := ix.IndexMedia(id); err != nil {
		t.Fatalf("IndexMedia with no pending chunks: %v", err)
	}
	if len(f.addedDocs()) != 0 {
		t.Error("no documents should be added when there are no pending chunks")
	}
}

func TestIndexMediaAddError(t *testing.T) {
	f := newFakeMeili(t)
	f.failAdd = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	err := ix.IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "add docs") {
		t.Fatalf("want add docs error, got %v", err)
	}
}

func TestIndexMediaAddWaitError(t *testing.T) {
	f := newFakeMeili(t)
	f.failTaskGet = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	err := ix.IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "add wait") {
		t.Fatalf("want add wait error, got %v", err)
	}
}

func TestIndexMediaTaskFailed(t *testing.T) {
	f := newFakeMeili(t)
	f.addTasksFail = true
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	err := ix.IndexMedia(id)
	if err == nil || !strings.Contains(err.Error(), "add failed") {
		t.Fatalf("want add failed error, got %v", err)
	}
	pending, err := st.PendingIndexChunks(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Errorf("failed indexing must not latch chunks, got %d pending", len(pending))
	}
}

func TestDeleteMediaBestEffort(t *testing.T) {
	f := newFakeMeili(t)
	ix := New(newTestStore(t), f.url(), "")
	if err := ix.DeleteMedia([]int64{1, 2}); err != nil {
		t.Fatalf("DeleteMedia: %v", err)
	}
	filters := f.deleteFilters()
	if len(filters) != 2 || filters[0] != "mediaId = 1" || filters[1] != "mediaId = 2" {
		t.Errorf("delete filters = %v, want one per id", filters)
	}
	// Errors are logged and skipped, never returned.
	f.failDelete = true
	if err := ix.DeleteMedia([]int64{3}); err != nil {
		t.Fatalf("DeleteMedia must be best-effort, got %v", err)
	}
}

func TestJobHandler(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id, _ := seedMedia(t, st)
	ix := New(st, f.url(), "")
	handler := ix.JobHandler()

	err := handler(context.Background(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, func(float64) {})
	if err != nil {
		t.Fatalf("job handler: %v", err)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Status != "indexed" {
		t.Errorf("media status = %+v, want indexed", m)
	}
	if ever, err := st.EverIndexed(id); err != nil || !ever {
		t.Errorf("ever_indexed = %v err=%v, want true", ever, err)
	}

	if err := handler(context.Background(), &store.Job{Payload: "{oops"}, func(float64) {}); err == nil || !strings.Contains(err.Error(), "bad payload") {
		t.Fatalf("want bad payload error, got %v", err)
	}

	// Re-stale the chunks: with everything latched, IndexMedia would return
	// early before touching the index at all.
	if err := st.ClearChunkIndexFlags(id); err != nil {
		t.Fatal(err)
	}
	f.failAdd = true
	if err := handler(context.Background(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, func(float64) {}); err == nil {
		t.Fatal("index failure must propagate from job handler")
	}
}

// latchChunksIndexed marks every pending chunk of a media as indexed.
func latchChunksIndexed(t *testing.T, st *store.Store, mediaID int64) {
	t.Helper()
	pending, err := st.PendingIndexChunks(mediaID, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(pending))
	for _, ch := range pending {
		ids = append(ids, ch.ID)
	}
	if err := st.MarkChunksIndexed(ids, 42); err != nil {
		t.Fatal(err)
	}
}

func TestReindexAll(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	id1, id2 := seedMedia(t, st)

	// Latch id1's chunks as indexed and mark id2 errored: only id1 with stale
	// chunks and a non-error status should be re-enqueued.
	latchChunksIndexed(t, st, id1)
	if err := st.SetMediaStatus(id2, "error", "broken"); err != nil {
		t.Fatal(err)
	}
	before, err := st.MediaIDsNeedingIndex(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("expected nothing needing index before reindex, got %v", before)
	}

	n, err := New(st, f.url(), "").ReindexAll()
	if err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if n != 1 {
		t.Errorf("ReindexAll enqueued %d jobs, want 1", n)
	}
	// EnsureAllChunksStale must have reset id1's latch.
	after, err := st.MediaIDsNeedingIndex(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0] != id1 {
		t.Errorf("ids needing index = %v, want [%d]", after, id1)
	}
	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Type != "index" {
		t.Fatalf("jobs = %+v, want one index job", jobs)
	}
	want := `{"mediaId":` + strconv.FormatInt(id1, 10) + `}`
	if jobs[0].Payload != want {
		t.Errorf("payload = %q, want %q", jobs[0].Payload, want)
	}
}

func TestReindexAllNoChunks(t *testing.T) {
	st := newTestStore(t)
	n, err := New(st, "http://127.0.0.1:1", "").ReindexAll()
	if err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if n != 0 {
		t.Errorf("ReindexAll = %d, want 0", n)
	}
}
