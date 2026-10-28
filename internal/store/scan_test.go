package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScan(t *testing.T) {
	db := openTest(t)

	// No running scan initially
	scan, err := db.RunningScan()
	require.NoError(t, err)
	assert.Nil(t, scan)

	has, err := db.HasActiveScanJob()
	require.NoError(t, err)
	assert.False(t, has)

	// Start scan
	id, err := db.StartScan("/root")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	scan, err = db.RunningScan()
	require.NoError(t, err)
	require.NotNil(t, scan)
	assert.Equal(t, id, scan.ID)
	assert.Equal(t, "/root", scan.RootPath)
	assert.Equal(t, "running", scan.Status)

	// Update without status
	err = db.UpdateScan(id, 100, 50, "")
	require.NoError(t, err)

	scan, err = db.RunningScan()
	require.NoError(t, err)
	require.NotNil(t, scan)
	assert.Equal(t, int64(100), scan.FilesSeen)
	assert.Equal(t, int64(50), scan.FilesDone)

	// Update with status
	err = db.UpdateScan(id, 100, 100, "success")
	require.NoError(t, err)

	scan, err = db.RunningScan()
	require.NoError(t, err)
	assert.Nil(t, scan) // No longer 'running'

	// Test HasActiveScanJob
	_, err = db.EnqueueJob("scan", "payload", 0)
	require.NoError(t, err)
	has, err = db.HasActiveScanJob()
	require.NoError(t, err)
	assert.True(t, has)
}
