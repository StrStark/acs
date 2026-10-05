// Package version holds build information injected at link time.
package version

// Version is set via -ldflags "-X acs/internal/version.Version=...".
var Version = "dev"
