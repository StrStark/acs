// Package sysinfo reports host resource usage for the dashboard.
package sysinfo

// DiskUsage is in bytes. Free is what is available to this process, which can
// be less than Total-Used on filesystems that reserve blocks for root.
type DiskUsage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}
