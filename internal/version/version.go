// Package version provides build-time version information.
// These variables are set via ldflags at build time.
package version

import (
	_ "embed"
	"encoding/json"
	"sync"
)

var (
	// Version is the semantic version (e.g., "1.0.0")
	Version = "dev"

	// Commit is the git commit SHA
	Commit = "none"

	// Date is the build date in RFC3339 format
	Date = "unknown"
)

//go:embed sdk-provenance.json
var sdkProvenanceJSON []byte

// SDKProvenance contains SDK and API revision information embedded at build time.
type SDKProvenance struct {
	SDK struct {
		Module    string `json:"module"`
		Version   string `json:"version"`
		Revision  string `json:"revision"`
		UpdatedAt string `json:"updated_at"`
	} `json:"sdk"`
	API struct {
		Repo     string `json:"repo"`
		Revision string `json:"revision"`
		SyncedAt string `json:"synced_at"`
	} `json:"api"`
}

var (
	sdkProvenance     *SDKProvenance
	sdkProvenanceOnce sync.Once
)

// GetSDKProvenance returns the embedded SDK provenance information.
// Returns nil if the provenance data cannot be parsed.
func GetSDKProvenance() *SDKProvenance {
	sdkProvenanceOnce.Do(func() {
		var p SDKProvenance
		if err := json.Unmarshal(sdkProvenanceJSON, &p); err == nil {
			sdkProvenance = &p
		}
	})
	return sdkProvenance
}

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

// IsDev returns true if this is a development build.
func IsDev() bool {
	return Version == "dev"
}
