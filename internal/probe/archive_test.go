package probe

import (
	"strconv"
	"testing"
	"time"

	"quipster/internal/store"
)

// An archived row must be skipped: no Compute, no Apply, no status mutation.
func TestJobHandlerSkipsArchived(t *testing.T) {
	st := openWorkerTestStore(t)
	id, _, err := st.UpsertMedia("/media/movie.mkv", "mkv", 1, 1, "Movie", 2020, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	// Archive exactly as a scan would (zero-grace, two passes).
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 2; i++ {
		if _, _, aerr := st.ArchiveMissingUnder("/media", nil, 0, now); aerr != nil {
			t.Fatal(aerr)
		}
	}
	m, gerr := st.GetMedia(id)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if m == nil || !m.Archived {
		t.Fatalf("fixture not archived: %+v", m)
	}

	p := &Pipeline{St: st, SubsCacheDir: t.TempDir()}
	err = p.JobHandler()(t.Context(), &store.Job{Payload: `{"mediaId":` + strconv.FormatInt(id, 10) + `}`}, func(float64) {})
	if err != nil {
		t.Fatalf("archived probe must be a no-op, got %v", err)
	}
	m, err = st.GetMedia(id)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Status != "pending" {
		t.Fatalf("archived status must stay frozen: %+v", m)
	}
}
