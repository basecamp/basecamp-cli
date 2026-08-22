// Package hostutil provides shared utilities for host URL handling.
package hostutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
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

// trustedBasecampHosts are the production Basecamp 3 hosts the CLI trusts when
// resolving a pasted resource URL: the web host and the API host returned in
// API payloads.
var trustedBasecampHosts = map[string]bool{
	"3.basecamp.com":    true,
	"3.basecampapi.com": true,
}

// IsTrustedBasecampHost reports whether rawURL points at a host the CLI trusts
// for resolving Basecamp resource URLs. Trusted hosts are the production
// Basecamp 3 domains, any localhost host (covers *.localhost dev domains), and
// the host of the configured base URL (covers custom/staging deployments and
// http://3.basecamp.localhost:3001-style local dev). Everything else is
// rejected so a look-alike URL on an attacker-controlled host — which the
// host-agnostic URL router would otherwise parse — cannot be trusted into a
// mutating request. cfgBaseURL may be empty.
func IsTrustedBasecampHost(rawURL, cfgBaseURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	// Only pasted web/API URLs are trusted. Requiring an http(s) scheme keeps
	// non-web schemes (ftp://…) and protocol-relative references (//host/…) —
	// which url.Parse still resolves to a trusted host — from being accepted.
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	// Hostnames are case-insensitive, so normalize before comparing against the
	// trusted set and the configured host (whose keys/values are lower-case).
	host := strings.ToLower(u.Hostname())
	if IsLocalhost(strings.ToLower(u.Host)) {
		return true
	}
	if trustedBasecampHosts[host] {
		return true
	}
	if cfgBaseURL != "" {
		if cu, err := url.Parse(cfgBaseURL); err == nil {
			if ch := strings.ToLower(cu.Hostname()); ch != "" && ch == host {
				return true
			}
		}
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

// IsRemoteSession returns true when running inside an SSH session,
// detected via SSH_CONNECTION, SSH_CLIENT, or SSH_TTY environment variables.
func IsRemoteSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" ||
		os.Getenv("SSH_CLIENT") != "" ||
		os.Getenv("SSH_TTY") != ""
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
