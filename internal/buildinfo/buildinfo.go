// Package buildinfo holds what the binary was built from; the values are set with -ldflags -X at build time.
package buildinfo

var (
	Version = "dev"
	Commit  = "unknown"
)
