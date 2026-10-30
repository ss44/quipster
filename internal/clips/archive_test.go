package clips

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/store"
)

func archiveForClips(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 4; i++ {
		keep := map[string]bool{}
		rows, qerr := st.DB().Query(`SELECT path FROM media_files WHERE id != ?`, id)
		require.NoError(t, qerr)
		for rows.Next() {
			var p string
			require.NoError(t, rows.Scan(&p))
			keep[p] = true
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		_, _, aerr := st.ArchiveMissingUnder("/", keep, 0, now)
		require.NoError(t, aerr)
		m, gerr := st.GetMedia(id)
		require.NoError(t, gerr)
		if m != nil && m.Archived {
			return
		}
	}
	t.Fatalf("media %d never archived", id)
}

// Validate rejects archived media with the sentinel error so callers can map
// it to HTTP 404 without string matching.
func TestValidateArchived(t *testing.T) {
	svc, st := newTestService(t)
	id := seedMedia(t, st, filepath.Join(t.TempDir(), "movie.mkv"), -1)
	archiveForClips(t, st, id)

	_, _, err := svc.Validate(Request{Mode: "fast", MediaID: id, Start: 0, End: 5})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMediaArchived, "sentinel must be wrappable via errors.Is")

	// Active media is unaffected.
	active := seedMedia(t, st, filepath.Join(t.TempDir(), "active.mkv"), -1)
	_, _, err = svc.Validate(Request{Mode: "fast", MediaID: active, Start: 0, End: 5})
	require.NoError(t, err)
}

// The async clip job finishes quietly for media archived between request and
// render instead of burning retries on a render that must not happen.
func TestJobHandlerSkipsArchived(t *testing.T) {
	svc, st := newTestService(t)
	id := seedMedia(t, st, filepath.Join(t.TempDir(), "movie.mkv"), -1)
	archiveForClips(t, st, id)

	payload, err := json.Marshal(map[string]any{"mediaId": id, "start": 0.0, "end": 2.0, "padMs": 0, "mode": "fast"})
	require.NoError(t, err)
	err = svc.JobHandler()(context.Background(), &store.Job{Payload: string(payload)}, func(float64) {})
	require.NoError(t, err, "archived media must finish the clip job as a no-op")
}
