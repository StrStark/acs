//go:build !unix

package sysinfo

import "errors"

func Disk(path string) (DiskUsage, error) {
	return DiskUsage{}, errors.New("disk usage is not supported on this platform")
}
