// Package version carries build metadata for the shardstore binary.
package version

import "fmt"

var (
	// Version is the semantic version, overridden at build time via -ldflags.
	Version = "0.1.0"
	// Commit is the git SHA the binary was built from.
	Commit = "dev"
	// BuildTime is the UTC timestamp of the build.
	BuildTime = "unknown"
)

// String returns the full version string, e.g. "shardstore v0.1.0 (commit abc1234, built 2026-08-19T12:00:00Z)".
func String() string {
	return fmt.Sprintf("shardstore v%s (commit %s, built %s)", Version, Commit, BuildTime)
}
