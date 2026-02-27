// Package hostutil provides shared utilities for host URL handling.
package hostutil

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

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

// RequireSecureURL returns an error if the URL uses http:// for a non-localhost host.
// Localhost (127.0.0.1, ::1, *.localhost) is exempt for local development.
func RequireSecureURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme == "http" && !IsLocalhost(u.Host) {
		return fmt.Errorf("refusing insecure http:// URL for non-localhost host %q — use https:// or target localhost for development", u.Host)
	}
	return nil
}

// OpenBrowser opens the specified URL in the default browser.
func OpenBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return exec.CommandContext(context.Background(), cmd, args...).Start() //nolint:gosec // G204: cmd is hardcoded per-platform
}
