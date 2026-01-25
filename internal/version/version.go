// Package version provides build-time version information.
// These variables are set via ldflags at build time.
package version

var (
	// Version is the semantic version (e.g., "1.0.0")
	Version = "dev"

	// Commit is the git commit SHA
	Commit = "none"

	// Date is the build date in RFC3339 format
	Date = "unknown"
)

// Full returns the full version string for display.
func Full() string {
	if Version == "dev" {
		return "bcq version dev (built from source)"
	}
	return "bcq version " + Version
}

// UserAgent returns the user agent string for API requests.
func UserAgent() string {
	v := Version
	if v == "dev" {
		v = "dev"
	}
	return "bcq/" + v + " (https://github.com/basecamp/bcq)"
}
