package hostutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Empty
		{"", ""},

		// Full URLs passed through
		{"http://example.com", "http://example.com"},
		{"https://example.com", "https://example.com"},
		{"http://localhost:3000", "http://localhost:3000"},
		{"https://localhost:3000", "https://localhost:3000"},

		// Localhost variants → http
		{"localhost", "http://localhost"},
		{"localhost:3000", "http://localhost:3000"},
		{"127.0.0.1", "http://127.0.0.1"},
		{"127.0.0.1:3000", "http://127.0.0.1:3000"},
		{"[::1]", "http://[::1]"},
		{"[::1]:3000", "http://[::1]:3000"},

		// .localhost subdomains → http (RFC 6761)
		{"app.localhost", "http://app.localhost"},
		{"app.localhost:3000", "http://app.localhost:3000"},
		{"foo.bar.localhost", "http://foo.bar.localhost"},
		{"foo.bar.localhost:8080", "http://foo.bar.localhost:8080"},

		// Non-localhost → https
		{"example.com", "https://example.com"},
		{"api.example.com", "https://api.example.com"},
		{"staging.basecamp.com:8080", "https://staging.basecamp.com:8080"},

		// Edge case: localhost.example.com is NOT localhost
		{"localhost.example.com", "https://localhost.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := Normalize(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRequireSecureURL(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		// HTTPS always ok
		{"https://api.example.com", false},
		{"https://evil.com", false},
		{"", false},

		// HTTP localhost ok (dev use)
		{"http://localhost:3001", false},
		{"http://127.0.0.1:8080", false},
		{"http://[::1]:3000", false},
		{"http://3.basecamp.localhost:3001", false},
		{"http://app.localhost", false},

		// HTTP non-localhost rejected
		{"http://evil.com", true},
		{"http://api.example.com", true},
		{"http://staging.basecamp.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := RequireSecureURL(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "insecure http://")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsRemoteSession(t *testing.T) {
	// Clear all SSH env vars first
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(key, "")
	}

	require.False(t, IsRemoteSession(), "no SSH vars → not remote")

	tests := []struct {
		envVar string
		value  string
	}{
		{"SSH_CONNECTION", "10.0.0.1 12345 10.0.0.2 22"},
		{"SSH_CLIENT", "10.0.0.1 12345 22"},
		{"SSH_TTY", "/dev/pts/0"},
	}

	for _, tt := range tests {
		t.Run(tt.envVar, func(t *testing.T) {
			// Clear all, then set one
			for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
				t.Setenv(key, "")
			}
			t.Setenv(tt.envVar, tt.value)
			assert.True(t, IsRemoteSession(), "%s set → remote", tt.envVar)
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Localhost
		{"localhost", true},
		{"localhost:3000", true},
		{"localhost:8080", true},

		// .localhost subdomains (RFC 6761)
		{"app.localhost", true},
		{"app.localhost:3000", true},
		{"foo.bar.localhost", true},
		{"foo.bar.localhost:8080", true},

		// IPv4 loopback
		{"127.0.0.1", true},
		{"127.0.0.1:3000", true},

		// IPv6 loopback (must be bracketed for valid URL)
		{"[::1]", true},
		{"[::1]:3000", true},

		// Not localhost
		{"::1", false}, // bare ::1 is invalid URL format
		{"example.com", false},
		{"localhost.example.com", false},
		{"127.0.0.2", false},
		{"192.168.1.1", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := IsLocalhost(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsTrustedBasecampHost(t *testing.T) {
	const prodBaseURL = "https://3.basecampapi.com"
	const localBaseURL = "http://3.basecamp.localhost:3001"

	tests := []struct {
		name       string
		rawURL     string
		cfgBaseURL string
		expected   bool
	}{
		// Production hosts are always trusted.
		{"production web host", "https://3.basecamp.com/99/buckets/1/chats/2/lines/3", prodBaseURL, true},
		{"production api host", "https://3.basecampapi.com/99/chats/2/lines/3", prodBaseURL, true},

		// Host comparison is case-insensitive (hostnames are).
		{"uppercased production host", "https://3.BASECAMP.com/99/buckets/1/chats/2/lines/3", prodBaseURL, true},
		{"mixed-case configured host", "https://3.BasecampApi.com/99/chats/2/lines/3", "https://3.basecampapi.com", true},

		// Localhost dev hosts are trusted regardless of the configured base URL.
		{"basecamp.localhost with port", "http://3.basecamp.localhost:3001/99/buckets/1/chats/2/lines/3", "", true},
		{"plain localhost", "http://localhost:3001/99/buckets/1/chats/2/lines/3", "", true},

		// The configured base URL host is trusted (covers staging/custom deploys).
		{"configured base URL host", "https://3.basecampapi.com/99/chats/2/lines/3", "https://3.basecampapi.com", true},
		{"configured local base URL", "http://3.basecamp.localhost:3001/99/buckets/1/chats/2/lines/3", localBaseURL, true},

		// Everything else is rejected — the router is host-agnostic, so this gate
		// is what stops a look-alike URL on an attacker-controlled host.
		{"attacker host", "https://evil.example/99/buckets/1/chats/2/lines/3", prodBaseURL, false},
		{"basecamp substring host", "https://3.basecamp.com.evil.example/99/chats/2/lines/3", prodBaseURL, false},
		{"empty URL", "", prodBaseURL, false},
		{"bare id, not a URL", "789", prodBaseURL, false},

		// Non-web schemes on an otherwise-trusted host are rejected — only pasted
		// http(s) URLs are trusted.
		{"ftp scheme on trusted host", "ftp://3.basecamp.com/99/buckets/1/chats/2/lines/3", prodBaseURL, false},
		{"protocol-relative on trusted host", "//3.basecamp.com/99/buckets/1/chats/2/lines/3", prodBaseURL, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsTrustedBasecampHost(tt.rawURL, tt.cfgBaseURL))
		})
	}
}
