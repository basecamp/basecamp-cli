package commands

import (
	"strings"

	"golang.org/x/mod/semver"
)

// isUpdateAvailable reports whether latest is newer than current.
//
// Versions are compared as semantic versions with an optional leading "v".
// This correctly handles prerelease/pseudo versions such as
// "0.4.1-0.20260313174735-243815fa23b2", which should compare newer than
// "0.4.0".
func isUpdateAvailable(current, latest string) bool {
	current = normalizeSemver(current)
	latest = normalizeSemver(latest)
	if !semver.IsValid(current) || !semver.IsValid(latest) {
		return latest != "" && latest != current
	}
	return semver.Compare(latest, current) > 0
}

func normalizeSemver(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
