package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// checkInterval is how often we query GitHub for the latest version.
var checkInterval = 24 * time.Hour

// stdoutIsTerminal reports whether stdout is a terminal. Extracted for testability.
var stdoutIsTerminal = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// UpdateCheck holds state for a non-blocking version check.
type UpdateCheck struct {
	latest string
	done   chan struct{}
}

// StartUpdateCheck begins a background version check if the cache is stale.
// Returns nil if the check should be skipped (dev build, opted out, etc.).
func StartUpdateCheck() *UpdateCheck {
	if version.IsDev() {
		return nil
	}
	if os.Getenv("BASECAMP_NO_UPDATE_CHECK") == "1" {
		return nil
	}

	// Skip for non-interactive sessions — no point fetching if we won't display
	if !stdoutIsTerminal() {
		return nil
	}

	uc := &UpdateCheck{done: make(chan struct{})}
	cached := readUpdateCache()

	if cached != nil {
		age := time.Since(cached.CheckedAt)
		if age >= 0 && age < checkInterval {
			// Cache is fresh — use it directly, no goroutine needed
			uc.latest = cached.LatestVersion
			close(uc.done)
			return uc
		}
	}

	// Cache is stale or missing — fetch in the background
	go func() {
		defer close(uc.done)
		latest, err := versionChecker()
		if err != nil || latest == "" {
			return
		}
		uc.latest = latest
		writeUpdateCache(latest)
	}()

	return uc
}

// Notice returns a formatted update notice, or "" if no update is available
// or the check hasn't completed. Never blocks.
func (uc *UpdateCheck) Notice() string {
	if uc == nil {
		return ""
	}

	// Non-blocking check: if the goroutine hasn't finished, skip
	select {
	case <-uc.done:
	default:
		return ""
	}

	if !isUpdateAvailable(version.Version, uc.latest) {
		return ""
	}

	return fmt.Sprintf(
		"Update available: %s → %s — Run \"basecamp upgrade\" to update",
		version.Version, uc.latest,
	)
}

// updateCache is the on-disk format for the version check result.
type updateCache struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

func updateCachePath() string {
	return filepath.Join(config.GlobalConfigDir(), ".update-check")
}

func readUpdateCache() *updateCache {
	data, err := os.ReadFile(updateCachePath())
	if err != nil {
		return nil
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.LatestVersion == "" || c.CheckedAt.IsZero() {
		return nil
	}
	return &c
}

func writeUpdateCache(latestVersion string) {
	c := updateCache{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now().UTC(),
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	dir := filepath.Dir(updateCachePath())
	_ = os.MkdirAll(dir, 0o755)                      //nolint:gosec // G301: config dir
	_ = os.WriteFile(updateCachePath(), data, 0o644) //nolint:gosec // G306: not a secret
}
