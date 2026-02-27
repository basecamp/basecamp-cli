package hostutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
