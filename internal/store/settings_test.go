package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettings(t *testing.T) {
	db := openTest(t)

	// Setting doesn't exist
	val, ok, err := db.GetSetting("nonexistent")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, val)

	// Set and Get
	err = db.SetSetting("mykey", "myval")
	require.NoError(t, err)

	val, ok, err = db.GetSetting("mykey")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "myval", val)

	// Library paths
	paths, err := db.GetLibraryPaths()
	require.NoError(t, err)
	assert.Empty(t, paths)

	err = db.SetLibraryPaths([]string{"/foo", "/bar"})
	require.NoError(t, err)

	paths, err = db.GetLibraryPaths()
	require.NoError(t, err)
	assert.Equal(t, []string{"/foo", "/bar"}, paths)

	// Queue paused
	paused, err := db.GetQueuePaused()
	require.NoError(t, err)
	assert.False(t, paused)

	err = db.SetQueuePaused(true)
	require.NoError(t, err)

	paused, err = db.GetQueuePaused()
	require.NoError(t, err)
	assert.True(t, paused)

	// Recover
	err = db.Recover()
	require.NoError(t, err)
}
