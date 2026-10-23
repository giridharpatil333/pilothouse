// Package version is set at build time with -ldflags "-X".
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
