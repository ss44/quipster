package indexer

import (
	"fmt"
	"testing"
)

func TestEnqueueArchiveDeindexExactPayloadAndDedupe(t *testing.T) {
	st := newTestStore(t)
	ix := New(st, "", "")
	ix.EnqueueArchiveDeindex(nil)
	ix.EnqueueArchiveDeindex([]int64{7, 7, 8})
	ix.EnqueueArchiveDeindex([]int64{7, 8})

	rows, err := st.DB().Query(`SELECT type, payload, priority FROM jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	got := map[string]bool{}
	count := 0
	for rows.Next() {
		var jobType, payload string
		var priority int
		if err := rows.Scan(&jobType, &payload, &priority); err != nil {
			t.Fatal(err)
		}
		if jobType != "archive_deindex" || priority != -1 {
			t.Fatalf("job type = %q priority = %d, want archive_deindex -1", jobType, priority)
		}
		got[payload] = true
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 || !got[`{"mediaId":7}`] || !got[`{"mediaId":8}`] {
		t.Fatalf("jobs = %v count %d, want exact deduplicated media payloads", got, count)
	}
}

func TestEnqueueArchiveDeindexContinuesAfterFailedMiddleBatch(t *testing.T) {
	st := newTestStore(t)
	ix := New(st, "", "")
	ids := make([]int64, 2501)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, err := st.DB().Exec(`CREATE TRIGGER fail_middle_archive_batch BEFORE INSERT ON jobs
		WHEN NEW.type='archive_deindex' AND NEW.payload='{"mediaId":1500}'
		BEGIN SELECT RAISE(ABORT, 'forced middle batch failure'); END`)
	if err != nil {
		t.Fatal(err)
	}

	ix.EnqueueArchiveDeindex(ids)

	rows, err := st.DB().Query(`SELECT type, payload, priority FROM jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	got := map[string]bool{}
	for rows.Next() {
		var jobType, payload string
		var priority int
		if err := rows.Scan(&jobType, &payload, &priority); err != nil {
			t.Fatal(err)
		}
		if jobType != "archive_deindex" || priority != -1 {
			t.Fatalf("job type = %q priority = %d, want archive_deindex -1", jobType, priority)
		}
		got[payload] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1501 {
		t.Fatalf("committed jobs = %d, want 1501 from first and third batches", len(got))
	}
	for _, id := range ids {
		payload := fmt.Sprintf(`{"mediaId":%d}`, id)
		want := id <= 1000 || id > 2000
		if got[payload] != want {
			t.Fatalf("media %d committed = %t, want %t", id, got[payload], want)
		}
	}
}

func TestReconcileArchivedDeindexFiltersAndDedupes(t *testing.T) {
	st := newTestStore(t)
	ix := New(st, "", "")
	pending, _, err := st.UpsertMedia("/pending.mkv", "matroska", 1, 1, "Pending", 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	deindexed, _, err := st.UpsertMedia("/deindexed.mkv", "matroska", 1, 1, "Deindexed", 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := st.UpsertMedia("/active.mkv", "matroska", 1, 1, "Active", 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := st.DB().Exec(`UPDATE media_files SET archived=1, deindexed=0 WHERE id=?`, pending); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := st.DB().Exec(`UPDATE media_files SET archived=1, deindexed=1 WHERE id=?`, deindexed); execErr != nil {
		t.Fatal(execErr)
	}

	ix.ReconcileArchivedDeindex()
	ix.ReconcileArchivedDeindex()

	rows, err := st.DB().Query(`SELECT payload, priority FROM jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var payloads []string
	for rows.Next() {
		var payload string
		var priority int
		if err := rows.Scan(&payload, &priority); err != nil {
			t.Fatal(err)
		}
		if priority != -1 {
			t.Fatalf("priority = %d, want -1", priority)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"mediaId":%d}`, pending)
	if len(payloads) != 1 || payloads[0] != want {
		t.Fatalf("reconciled payloads = %v, want only %s; active=%d deindexed=%d", payloads, want, active, deindexed)
	}
}
