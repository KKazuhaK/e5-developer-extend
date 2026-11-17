// Package version holds build metadata injected at link time.
package version

// These are overridden with -ldflags "-X github.com/KKazuhaK/e5-developer-extend/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
