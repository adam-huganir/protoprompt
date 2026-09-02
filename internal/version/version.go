// Package version exposes build metadata stamped in at link time.
//
// The values default to development placeholders and are overridden for
// release builds via -ldflags "-X protoprompt/internal/version.Version=...".
// The canonical version number lives in the repo-root VERSION file, which CI
// reads when stamping and tagging releases.
package version

var (
	// Version is the semantic version, e.g. "1.2.3". "dev" for local builds.
	Version = "dev"
	// Commit is the short git SHA the binary was built from.
	Commit = "none"
	// Date is the RFC3339 UTC build time.
	Date = "unknown"
)

// String renders a human-readable one-line version summary.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
