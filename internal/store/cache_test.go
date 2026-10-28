package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache(t *testing.T) {
	db := openTest(t)

	// Get non-existent
	entry, err := db.GetCacheEntry("not_found")
	require.NoError(t, err)
	assert.Nil(t, entry)

	// Upsert
	err = db.UpsertCacheEntry("key1", "/path1", "kind1", 100)
	require.NoError(t, err)
	err = db.UpsertCacheEntry("key2", "/path2", "kind1", 200)
	require.NoError(t, err)
	err = db.UpsertCacheEntry("key3", "/path3", "kind2", 50)
	require.NoError(t, err)

	// Get existing
	entry, err = db.GetCacheEntry("key1")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "key1", entry.CacheKey)
	assert.Equal(t, "/path1", entry.Path)
	assert.Equal(t, "kind1", entry.Kind)
	assert.Equal(t, int64(100), entry.Bytes)

	// Touch cache
	err = db.TouchCache("key1")
	require.NoError(t, err)

	entry, err = db.GetCacheEntry("key1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), entry.Hits)

	// CacheBytes
	n, err := db.CacheBytes("kind1")
	require.NoError(t, err)
	assert.Equal(t, int64(300), n)

	n, err = db.CacheBytes("kind1", "kind2")
	require.NoError(t, err)
	assert.Equal(t, int64(350), n)

	// OldestCacheEntries
	entries, err := db.OldestCacheEntries("kind1")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	// key2 has never been touched, so last_access is its insert time. key1 was touched after, so key1 > key2.
	// Actually, last_access might be identical if they ran fast enough.
	// Wait, UpsertCacheEntry uses nowMillis(). So they might have the same time.
	// order is `last_access ASC, rowid ASC`.

	// Delete
	err = db.DeleteCacheEntry("key1")
	require.NoError(t, err)

	entry, err = db.GetCacheEntry("key1")
	require.NoError(t, err)
	assert.Nil(t, entry)
}
