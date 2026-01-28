package cli

import "testing"

func TestNormalizeHost(t *testing.T) {
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
		{"::1", "http://::1"},

		// Non-localhost → https
		{"example.com", "https://example.com"},
		{"api.example.com", "https://api.example.com"},
		{"staging.basecamp.com:8080", "https://staging.basecamp.com:8080"},

		// Edge case: localhost.example.com is NOT localhost
		{"localhost.example.com", "https://localhost.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeHost(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeHost(%q) = %q, want %q", tt.input, result, tt.expected)
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

		// IPv4 loopback
		{"127.0.0.1", true},
		{"127.0.0.1:3000", true},

		// IPv6 loopback (bracketed)
		{"[::1]", true},
		{"[::1]:3000", true},

		// IPv6 loopback (bare)
		{"::1", true},

		// Not localhost
		{"example.com", false},
		{"localhost.example.com", false},
		{"127.0.0.2", false},
		{"192.168.1.1", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isLocalhost(tt.input)
			if result != tt.expected {
				t.Errorf("isLocalhost(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
