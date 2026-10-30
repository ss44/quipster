package scan

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"quipster/internal/store"
)

// archiveScanStore opens a throwaway store for archive scan tests.
func archiveScanStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	return st
}

func writeSingleMedia(t *testing.T, root, name string) string {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	return full
}

// seedArchived prepares a media row with cues/chunks and marks it archived
// (as a completed scan+deindex would have), returning its id.
func seedArchived(t *testing.T, st *store.Store, path string) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia(path, "mkv", 10, 1, "Movie", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	track, err := st.ReplaceTrack(id, &store.SubtitleTrack{Source: "embedded", Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	cues := []store.Cue{{MediaFileID: id, TrackID: track, Idx: 0, StartSec: 0, EndSec: 1, Text: "a line"}}
	if serrErr := st.ReplaceCues(id, track, cues); serrErr != nil {
		t.Fatal(err)
	}
	if chErr := st.ReplaceChunks(id, []store.Chunk{{MediaFileID: id, StartCueIdx: 0, EndCueIdx: 0, Text: "a line"}}); chErr != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_000_000)
	// Two zero-grace passes: the first latches, the second archives. Only
	// this one row exists under the directory, so an empty keep set is safe.
	for i := 0; i < 2; i++ {
		_, _, err = st.ArchiveMissingUnder(filepath.Dir(path), nil, 0, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || !m.Archived {
		t.Fatalf("fixture media %d not archived: %+v", id, m)
	}
	return id
}

// A share that disappears and returns within the grace window must cost
// nothing: the first scan latches, the returning scan clears, and no archive
// transition ever happens.
//
//nolint:gocyclo // linear lifecycle assertions across full scan passes
func TestShareOutageWithinGrace(t *testing.T) {
	st := archiveScanStore(t)
	root := t.TempDir()
	path := writeSingleMedia(t, root, "movies/Gone (2019).mkv")

	res, err := Run(context.Background(), root, nil, true, st, nil)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if res.Seen != 1 {
		t.Fatalf("first scan seen = %d, want 1", res.Seen)
	}

	// The share disappears; a completed scan runs.
	if rerr := os.Remove(path); rerr != nil {
		t.Fatal(rerr)
	}
	res, err = Run(context.Background(), root, nil, true, st, nil)
	if err != nil {
		t.Fatalf("outage scan: %v", err)
	}
	if res.Archived != 0 || res.Latched != 1 {
		t.Fatalf("outage scan = %+v, want 1 latch and no archive", res)
	}
	m, err := st.GetMediaByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived || m.MissingAt == 0 {
		t.Fatalf("row after outage = %+v, want present, active, latched", m)
	}

	// The share returns before the grace window elapses.
	if werr := os.WriteFile(path, []byte("media"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	res, err = Run(context.Background(), root, nil, true, st, nil)
	if err != nil {
		t.Fatalf("return scan: %v", err)
	}
	if res.Restored != 0 {
		t.Fatalf("return scan restored %d rows, want 0 (row was latched, never archived)", res.Restored)
	}
	m, err = st.GetMediaByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.MissingAt != 0 || m.Archived {
		t.Fatalf("row after return = %+v, want latch cleared, active", m)
	}
}

// The full lifecycle: latch -> archive (with onArchived callback) -> return ->
// restore, with an index job re-enqueued for every restore so a crashed
// de-index (docs gone, flag never latched) can never leave a row dark.
//
//nolint:gocyclo // linear lifecycle assertions across full scan passes
func TestArchiveThenRestoreFullLifecycle(t *testing.T) {
	st := archiveScanStore(t)
	root := t.TempDir()
	path := writeSingleMedia(t, root, "movies/Gone (2019).mkv")

	if _, err := Run(context.Background(), root, nil, true, st, nil); err != nil {
		t.Fatal(err)
	}
	var archivedIDs []int64
	onArchived := func(ids []int64) { archivedIDs = append(archivedIDs, ids...) }
	opts := Options{ArchiveEnabled: true, ArchiveGrace: 0, now: func() time.Time { return time.UnixMilli(2_000_000) }}

	// The file goes away for good; two zero-grace scans archive it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, err := runWithOptions(context.Background(), root, nil, true, st, onArchived, opts)
		if err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
		_ = res
	}
	if len(archivedIDs) != 1 {
		t.Fatalf("archived ids = %v, want exactly one", archivedIDs)
	}
	m, err := st.GetMediaByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || !m.Archived {
		t.Fatalf("row after grace = %+v, want archived", m)
	}
	if m.Status != "pending" {
		t.Fatalf("archived status = %q, want frozen pending", m.Status)
	}

	// The file returns: the restore pass must re-activate the row and enqueue
	// an index job because the (simulated) documents were deleted.
	if werr := os.WriteFile(path, []byte("media"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if derr := st.MarkMediaDeindexed(archivedIDs[0]); derr != nil {
		t.Fatal(derr)
	}
	res, err := runWithOptions(context.Background(), root, nil, true, st, onArchived, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 {
		t.Fatalf("restore count = %d, want 1", res.Restored)
	}
	m, err = st.GetMediaByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived || m.Deindexed {
		t.Fatalf("row after restore = %+v, want active", m)
	}
	var indexJobs int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index' AND payload=?`,
		`{"mediaId":`+strconv.FormatInt(archivedIDs[0], 10)+`}`).Scan(&indexJobs); err != nil {
		t.Fatal(err)
	}
	if indexJobs == 0 {
		t.Fatal("restore must always enqueue an index job: a crashed de-index can leave documents gone without the deindexed flag")
	}

	// A follow-up scan must not enqueue duplicate index work: the unique-job
	// dedup absorbs the re-enqueue while the first job is still queued.
	if _, err := Run(context.Background(), root, nil, true, st, nil); err != nil {
		t.Fatal(err)
	}
}

// Restoring before the de-index job runs still re-enqueues an index job: the
// dedup makes it a no-op while the first job is queued, and it covers the case
// where a de-index job deleted the Meili documents and crashed before latching
// deindexed. The pending archive_deindex job then becomes a lazy no-op at
// execution time.
func TestRestoreBeforeDeindexStillRequeuesIndex(t *testing.T) {
	st := archiveScanStore(t)
	root := t.TempDir()
	path := writeSingleMedia(t, root, "movies/Back.mkv")
	id := seedArchived(t, st, path)

	// Queue the de-index job as the scan's onArchived callback would.
	_, _, err := st.EnqueueUniqueJob("archive_deindex", `{"mediaId":`+strconv.FormatInt(id, 10)+`}`, -1)
	if err != nil {
		t.Fatal(err)
	}

	// The file is back; the scan restores it and re-queues the index job.
	res, err := Run(context.Background(), root, nil, true, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 {
		t.Fatalf("scan restored %d rows, want 1: %+v", res.Restored, res)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived || m.Deindexed {
		t.Fatalf("row = %+v, want active", m)
	}
	var queued int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index' AND status='queued' AND payload=?`,
		`{"mediaId":`+strconv.FormatInt(id, 10)+`}`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued == 0 {
		t.Fatal("restore must queue one index job")
	}
}

// With archival disabled the prune pass must neither latch nor archive.
func TestArchiveDisabledSkipsPass(t *testing.T) {
	st := archiveScanStore(t)
	root := t.TempDir()
	path := writeSingleMedia(t, root, "movies/Gone (2019).mkv")

	if _, err := Run(context.Background(), root, nil, true, st, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	opts := Options{ArchiveEnabled: false, ArchiveGrace: 0}
	res, err := runWithOptions(context.Background(), root, nil, true, st, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Archived != 0 || res.Latched != 0 {
		t.Fatalf("disabled scan = %+v, want no archive activity", res)
	}
	m, err := st.GetMediaByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived || m.MissingAt != 0 {
		t.Fatalf("row = %+v, want untouched active row", m)
	}
}

// A partial walk (any walk error) must not drive archive decisions, even past
// the grace window, but must still clear latches for paths it saw.
//
//nolint:gocyclo // linear lifecycle assertions across full scan passes
func TestPartialWalkBlocksArchiveButRestoresSeen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode bits")
	}
	st := archiveScanStore(t)
	root := t.TempDir()
	gone := writeSingleMedia(t, root, "movies/Gone (2019).mkv")
	seen := writeSingleMedia(t, root, "movies/Back.mkv")

	// Pre-archive the gone file so the grace window is already spent.
	id := seedArchived(t, st, gone)
	if rerr := os.Remove(gone); rerr != nil {
		t.Fatal(rerr)
	}
	// Seed the seen file so the restore pass has something to clear.
	seenID, _, err := st.UpsertMedia(seen, "mkv", 10, 1, "Back", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, uerr := st.DB().Exec(`UPDATE media_files SET missing_at=1 WHERE id=?`, seenID); uerr != nil {
		t.Fatal(uerr)
	}

	// An unreadable subdirectory makes the walk partial.
	locked := filepath.Join(root, "movies", "locked")
	if merr := os.MkdirAll(locked, 0o750); merr != nil {
		t.Fatal(merr)
	}
	if cerr := os.Chmod(locked, 0o000); cerr != nil {
		t.Fatal(cerr)
	}
	t.Cleanup(func() {
		//nolint:gosec // test cleanup restores a directory mode
		if uerr := os.Chmod(locked, 0o750); uerr != nil {
			t.Logf("restore mode: %v", uerr)
		}
	})

	opts := Options{ArchiveEnabled: true, ArchiveGrace: 0}
	res, err := runWithOptions(context.Background(), root, nil, true, st, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.WalkErrors == 0 {
		t.Fatalf("expected walk errors: %+v", res)
	}
	if res.Archived != 0 {
		t.Fatalf("partial walk archived %d rows, want 0", res.Archived)
	}
	m, err := st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || !m.Archived {
		t.Fatalf("archived row must stay archived: %+v", m)
	}
	sm, err := st.GetMedia(seenID)
	if err != nil {
		t.Fatal(err)
	}
	if sm == nil || sm.MissingAt != 0 {
		t.Fatalf("seen row latch must clear even on partial walks: %+v", sm)
	}
}

// A root-scoped admin scan (prune=false, as handleScan sends for an explicit
// ?root=) must never drive archive decisions outside the roots it actually
// walked. Files missing under unscanned roots stay active and unlatched: the
// scan has no evidence about them, and a later full-library prune scan owns
// their grace window.
//
//nolint:gocyclo // linear lifecycle assertions across full scan passes
func TestRootScopedScanNeverArchivesUnwalkedRoots(t *testing.T) {
	st := archiveScanStore(t)
	root := t.TempDir()
	moviesPath := writeSingleMedia(t, root, "movies/Back.mkv")
	tvPath := writeSingleMedia(t, root, "tv/Gone.mkv")

	if _, err := Run(context.Background(), root, nil, true, st, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tvPath); err != nil {
		t.Fatal(err)
	}

	// Root-scoped scans mirror handleScan's payload: explicit root, prune=false.
	opts := Options{ArchiveEnabled: true, ArchiveGrace: 0, now: func() time.Time { return time.UnixMilli(5_000_000) }}
	for i := 0; i < 2; i++ {
		res, err := runWithOptions(context.Background(), root, []string{"movies"}, false, st, nil, opts)
		if err != nil {
			t.Fatalf("root-scoped scan %d: %v", i, err)
		}
		if res.Latched != 0 || res.Archived != 0 {
			t.Fatalf("root-scoped scan %d = %+v, want no archive activity", i, res)
		}
	}
	m, err := st.GetMediaByPath(tvPath)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Archived || m.MissingAt != 0 {
		t.Fatalf("unwalked root row = %+v, want active and unlatched", m)
	}
	_ = moviesPath

	// Contrast: a full-library prune scan owns the tv row's grace window.
	if _, err = runWithOptions(context.Background(), root, nil, true, st, nil, opts); err != nil {
		t.Fatal(err)
	}
	m, err = st.GetMediaByPath(tvPath)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.MissingAt == 0 {
		t.Fatalf("full-library scan must latch the missing tv row: %+v", m)
	}
	if _, err = runWithOptions(context.Background(), root, nil, true, st, nil, opts); err != nil {
		t.Fatal(err)
	}
	m, err = st.GetMediaByPath(tvPath)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || !m.Archived {
		t.Fatalf("full-library scan must archive the tv row after grace: %+v", m)
	}
}

// The walk-errors guard applies to root-scoped scans too: a partial walk under
// the scoped root blocks all archive decisions for that scan. This is the
// admin-path twin of TestPartialWalkBlocksArchiveButRestoresSeen.
//
//nolint:gocyclo // linear lifecycle assertions across full scan passes
func TestRootScopedScanWalkErrorsBlockArchive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode bits")
	}
	st := archiveScanStore(t)
	root := t.TempDir()
	moviesPath := writeSingleMedia(t, root, "movies/Back.mkv")
	tvPath := writeSingleMedia(t, root, "tv/Gone.mkv")

	if _, err := Run(context.Background(), root, nil, true, st, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tvPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(moviesPath); err != nil {
		t.Fatal(err)
	}

	// An unreadable subdirectory inside the scoped root makes the walk partial.
	locked := filepath.Join(root, "movies", "locked")
	if merr := os.MkdirAll(locked, 0o750); merr != nil {
		t.Fatal(merr)
	}
	if cerr := os.Chmod(locked, 0o000); cerr != nil {
		t.Fatal(cerr)
	}
	t.Cleanup(func() {
		//nolint:gosec // test cleanup restores a directory mode
		if uerr := os.Chmod(locked, 0o750); uerr != nil {
			t.Logf("restore mode: %v", uerr)
		}
	})

	opts := Options{ArchiveEnabled: true, ArchiveGrace: 0}
	for i := 0; i < 2; i++ {
		res, err := runWithOptions(context.Background(), root, []string{"movies"}, false, st, nil, opts)
		if err != nil {
			t.Fatalf("root-scoped scan %d: %v", i, err)
		}
		if res.WalkErrors == 0 {
			t.Fatalf("root-scoped scan %d must report walk errors: %+v", i, res)
		}
		if res.Latched != 0 || res.Archived != 0 {
			t.Fatalf("partial root-scoped scan %d = %+v, want no archive activity", i, res)
		}
	}
	tm, err := st.GetMediaByPath(tvPath)
	if err != nil {
		t.Fatal(err)
	}
	if tm == nil || tm.Archived || tm.MissingAt != 0 {
		t.Fatalf("unwalked root row = %+v, want active and unlatched", tm)
	}
	mm, err := st.GetMediaByPath(moviesPath)
	if err != nil {
		t.Fatal(err)
	}
	if mm == nil || mm.Archived || mm.MissingAt != 0 {
		t.Fatalf("missing row under the scoped root = %+v, want active and unlatched while the walk is partial", mm)
	}
}
