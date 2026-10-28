//go:build unix

package diagnostics

import "syscall"

// processCPUSeconds returns cumulative user+system CPU seconds for this process.
func processCPUSeconds() (float64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return user + sys, true
}

// diskFree returns available and total bytes for the filesystem at path.
func diskFree(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	//nolint:gosec // G115: block counts on real filesystems fit in int64
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize, nil
}
