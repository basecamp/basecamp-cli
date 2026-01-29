// Package hostutil provides shared utilities for host URL handling.
package hostutil

import "strings"

// Normalize converts a host string to a full URL.
// - Empty string returns empty
// - localhost/127.0.0.1 defaults to http://
// - Other bare hostnames default to https://
// - Full URLs are used as-is
func Normalize(host string) string {
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host
	}
	if IsLocalhost(host) {
		return "http://" + host
	}
	return "https://" + host
}

// IsLocalhost returns true if host is localhost, a .localhost subdomain,
// 127.0.0.1, or [::1] (with optional port).
func IsLocalhost(host string) bool {
	// Strip port if present for easier matching
	hostWithoutPort := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// Check if this is IPv6 bracketed address
		if !strings.HasPrefix(host, "[") || strings.HasPrefix(host, "[::1]:") {
			hostWithoutPort = host[:idx]
		}
	}

	// Check for localhost or .localhost subdomain
	if hostWithoutPort == "localhost" || strings.HasSuffix(hostWithoutPort, ".localhost") {
		return true
	}
	if hostWithoutPort == "127.0.0.1" {
		return true
	}
	// IPv6 loopback (must be bracketed for valid URL)
	if hostWithoutPort == "[::1]" {
		return true
	}
	return false
}
