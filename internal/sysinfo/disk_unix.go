//go:build unix

package sysinfo

import "golang.org/x/sys/unix"

// Disk returns usage of the filesystem containing path.
func Disk(path string) (DiskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bsize := uint64(st.Bsize)
	total := uint64(st.Blocks) * bsize
	free := uint64(st.Bavail) * bsize
	return DiskUsage{Total: total, Free: free, Used: total - uint64(st.Bfree)*bsize}, nil
}
