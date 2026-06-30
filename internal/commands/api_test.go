package commands

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBaseURL = "https://3.basecampapi.com"

func TestParsePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects.json", "projects.json"},
		{"/projects.json", "projects.json"},
		{"buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		{"/buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		// Same-host absolute URLs: extract the path, dropping the account segment.
		{"https://3.basecampapi.com/999/projects.json", "/projects.json"},
		{"https://3.basecampapi.com/12345/buckets/1/todos/2.json", "/buckets/1/todos/2.json"},
		// Same host but no account segment — accepted, path used as-is.
		{"https://3.basecampapi.com/projects.json", "/projects.json"},
		// Query strings are preserved.
		{"https://3.basecampapi.com/999/projects.json?page=2", "/projects.json?page=2"},
		// Schemes are case-insensitive (RFC 3986): a same-host uppercase scheme
		// still extracts the path.
		{"HTTPS://3.basecampapi.com/999/projects.json", "/projects.json"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parsePath(tt.input, testBaseURL)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParsePathRejectsForeignHosts guards against bearer-token exfiltration: an
// absolute URL whose host differs from the configured base URL must be rejected
// before it reaches the SDK, which would otherwise attach the Authorization
// header and send credentials to the foreign host. Covers mixed-case schemes
// and a leading-slash smuggling attempt.
func TestParsePathRejectsForeignHosts(t *testing.T) {
	bad := []string{
		"https://evil.com/projects.json",
		"http://evil.com/projects.json",
		"https://evil.com/",
		"https://evil.com",
		"https://evil.example/999/projects.json", // foreign host, numeric form
		"http://127.0.0.1:9999/projects.json",
		"HTTPS://evil.example/projects.json",  // uppercase scheme
		"HtTpS://evil.example/projects.json",  // mixed-case scheme
		"/https://evil.example/projects.json", // leading-slash smuggling
		"/HTTPS://evil.example/projects.json", // leading slash + uppercase
	}

	for _, input := range bad {
		t.Run(input, func(t *testing.T) {
			got, err := parsePath(input, testBaseURL)
			require.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

func TestAPIPathArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "get <path>"}

	t.Run("no args returns path required", func(t *testing.T) {
		err := apiPathArgs(cmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path required")
	})

	t.Run("one arg succeeds", func(t *testing.T) {
		assert.NoError(t, apiPathArgs(cmd, []string{"projects.json"}))
	})

	t.Run("two args returns path required", func(t *testing.T) {
		err := apiPathArgs(cmd, []string{"a", "b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path required")
	})
}
