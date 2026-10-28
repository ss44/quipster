//go:build !unix

package diagnostics

import "errors"

// processCPUSeconds is unavailable on non-unix platforms.
func processCPUSeconds() (float64, bool) { return 0, false }

// diskFree is unavailable on non-unix platforms.
func diskFree(string) (int64, int64, error) {
	return 0, 0, errors.New("disk statistics unsupported on this platform")
}
