package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/store"
)

// archiveViaScan performs the exact two-pass zero-grace transition a scan
// would run, keeping every other row.
func archiveViaScan(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 4; i++ {
		keep := map[string]bool{}
		rows, err := st.DB().Query(`SELECT path FROM media_files WHERE id != ?`, id)
		require.NoError(t, err)
		for rows.Next() {
			var p string
			require.NoError(t, rows.Scan(&p))
			keep[p] = true
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		_, _, err = st.ArchiveMissingUnder("/", keep, 0, now)
		require.NoError(t, err)
		m, err := st.GetMedia(id)
		require.NoError(t, err)
		if m != nil && m.Archived {
			return
		}
	}
	t.Fatalf("media %d never archived", id)
}

func TestRestoreEndpoint(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	archiveViaScan(t, st, id)
	if err := st.MarkMediaDeindexed(id); err != nil {
		t.Fatal(err)
	}
	h := Router(deps)

	// Unknown media 404s.
	rr := do(t, h, http.MethodPost, "/api/admin/media/424242/restore", nil, nil)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// A deindexed restore re-activates and queues the reindex.
	rr = do(t, h, http.MethodPost, "/api/admin/media/"+strconv.FormatInt(id, 10)+"/restore", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, true, body["restored"])
	assert.Equal(t, true, body["reindexQueued"])

	var indexJobs int
	require.NoError(t, st.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='index' AND payload=?`,
		`{"mediaId":`+strconv.FormatInt(id, 10)+`}`).Scan(&indexJobs))
	assert.Equal(t, 1, indexJobs)

	// Restoring an already-active row reports no-op without queueing work.
	rr = do(t, h, http.MethodPost, "/api/admin/media/"+strconv.FormatInt(id, 10)+"/restore", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, false, body["restored"])
	assert.Equal(t, false, body["reindexQueued"])
}

// seedMediaAt seeds one indexed media row at a distinct path.
func seedMediaAt(t *testing.T, st *store.Store, path string) int64 {
	t.Helper()
	id, _, err := st.UpsertMedia(path, "matroska", 1234, 1700000000, "Pilot", 2020, 1, 1, "Show")
	require.NoError(t, err)
	require.NoError(t, st.UpdateMediaProbe(id, 600, 1920, 1080, "h264", "aac", "matroska", 0, 0, "eng"))
	require.NoError(t, st.SetMediaStatus(id, "indexed", ""))
	return id
}

func TestMediaListArchivedFilter(t *testing.T) {
	st, deps := newEnv(t)
	active := seedMediaAt(t, st, "/media/active.mkv")
	hidden := seedMediaAt(t, st, "/media/hidden.mkv")
	archiveViaScan(t, st, hidden)
	h := Router(deps)

	// Default lists exclude archived rows.
	rr := do(t, h, http.MethodGet, "/api/media", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var page struct {
		Items []struct {
			ID       int64 `json:"id"`
			Archived bool  `json:"archived"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page))
	assert.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	assert.Equal(t, active, page.Items[0].ID)
	assert.False(t, page.Items[0].Archived)

	// archived=1 exposes the archive to the admin UI.
	rr = do(t, h, http.MethodGet, "/api/media?archived=1", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page))
	assert.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	assert.Equal(t, hidden, page.Items[0].ID)
	assert.True(t, page.Items[0].Archived)

	// archived=any shows both.
	rr = do(t, h, http.MethodGet, "/api/media?archived=any", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page))
	assert.Equal(t, int64(2), page.Total)
}

func TestArchivedMediaEndpointsHide(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	seedChunks(t, st, id)
	archiveViaScan(t, st, id)
	h := Router(deps)
	docID := strconv.FormatInt(id, 10) + "_0-1"

	// Chunk deep-links 404 for archived media.
	rr := do(t, h, http.MethodGet, "/api/chunks/"+docID, nil, nil)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Clip streams reject archived media as absent, not as a bad request.
	rr = do(t, h, http.MethodGet, "/api/clips/stream?mediaId="+strconv.FormatInt(id, 10)+"&start=0&end=2&mode=fast", nil, nil)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// The media detail endpoint still resolves so the admin can inspect and
	// restore the row.
	rr = do(t, h, http.MethodGet, "/api/media/"+docID2(id), nil, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	media, ok := body["media"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, media["archived"])
}

func docID2(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestRestoreReindexesOnScanReturn(t *testing.T) {
	st, deps := newEnv(t)
	id := seedMedia(t, st)
	h := Router(deps)

	// The admin restores a row whose file has not returned yet.
	archiveViaScan(t, st, id)
	rr := do(t, h, http.MethodPost, "/api/admin/media/"+strconv.FormatInt(id, 10)+"/restore", nil, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// A later scan that sees the file again is a no-op for the restored row
	// and does not resurrect archive state.
	m, err := st.GetMedia(id)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.False(t, m.Archived)
	assert.Equal(t, int64(0), m.MissingAt)
}
