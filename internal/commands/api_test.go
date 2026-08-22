package commands

import (
	"errors"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	testBaseURL   = "https://3.basecampapi.com"
	testAccountID = "12345"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects.json", "projects.json"},
		{"/projects.json", "projects.json"},
		{"buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		{"/buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		// Same-host absolute URLs: extract the path, dropping the account
		// segment when it matches the configured account.
		{"https://3.basecampapi.com/12345/projects.json", "/projects.json"},
		{"https://3.basecampapi.com/12345/buckets/1/todos/2.json", "/buckets/1/todos/2.json"},
		// Same host but no account segment — accepted, path used as-is.
		{"https://3.basecampapi.com/projects.json", "/projects.json"},
		// Same-host URL with no path at all normalizes to the account root.
		{"https://3.basecampapi.com", "/"},
		// Same-host query-only URL normalizes to root and keeps the query.
		{"https://3.basecampapi.com?foo=bar", "/?foo=bar"},
		// A bare "/<account-id>" with no trailing segment still matches and
		// strips to the account root path rather than leaving it re-prefixed.
		{"https://3.basecampapi.com/12345", "/"},
		// Bare "/<account-id>" plus a query strips to root and keeps the query.
		{"https://3.basecampapi.com/12345?foo=bar", "/?foo=bar"},
		// Query strings are preserved.
		{"https://3.basecampapi.com/12345/projects.json?page=2", "/projects.json?page=2"},
		// Schemes are case-insensitive (RFC 3986): a same-host uppercase scheme
		// still extracts the path.
		{"HTTPS://3.basecampapi.com/12345/projects.json", "/projects.json"},
		// An explicit default port is still the configured host.
		{"https://3.basecampapi.com:443/12345/projects.json", "/projects.json"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parsePath(tt.input, testBaseURL, testAccountID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParsePathRejectsForeignAccount guards against silent account
// retargeting: the SDK re-prefixes the configured account, so stripping a
// different account's id from a pasted URL would aim the request (including
// DELETEs) at the configured account instead of the one the URL names.
func TestParsePathRejectsForeignAccount(t *testing.T) {
	for _, input := range []string{
		"https://3.basecampapi.com/999/projects.json",
		"https://3.basecampapi.com/999/buckets/1/todos/2.json",
		"https://3.basecampapi.com/999/projects.json?page=2",
		// A bare foreign "/<account-id>" (no trailing path) must still be
		// rejected — the broadened pattern must not open a retargeting hole.
		"https://3.basecampapi.com/999",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := parsePath(input, testBaseURL, testAccountID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to retarget")
			assert.Empty(t, got)
		})
	}

	t.Run("empty configured account strips the segment as before", func(t *testing.T) {
		got, err := parsePath("https://3.basecampapi.com/999/projects.json", testBaseURL, "")
		require.NoError(t, err)
		assert.Equal(t, "/projects.json", got)
	})
}

// TestParsePathRejectsForeignHosts guards against bearer-token exfiltration: an
// absolute URL whose host differs from the configured base URL must be rejected
// before it reaches the SDK, which would otherwise attach the Authorization
// header and send credentials to the foreign host. Covers mixed-case schemes,
// leading-slash smuggling attempts, and a same-hostname non-default port.
func TestParsePathRejectsForeignHosts(t *testing.T) {
	bad := []string{
		"https://evil.com/projects.json",
		"http://evil.com/projects.json",
		"https://evil.com/",
		"https://evil.com",
		"https://evil.example/999/projects.json", // foreign host, numeric form
		"http://127.0.0.1:9999/projects.json",
		"HTTPS://evil.example/projects.json",                   // uppercase scheme
		"HtTpS://evil.example/projects.json",                   // mixed-case scheme
		"/https://evil.example/projects.json",                  // leading-slash smuggling
		"/HTTPS://evil.example/projects.json",                  // leading slash + uppercase
		"//https://evil.example/projects.json",                 // double-slash smuggling
		"///https://evil.example/x",                            // triple-slash smuggling
		"https://3.basecampapi.com:8443/projects.json",         // same hostname, non-default port
		"https://3.basecampapi.com:443.evil.com/projects.json", // port-lookalike host smuggling
		"https://3.basecampapi.com:abc/x",                      // non-numeric port
	}

	for _, input := range bad {
		t.Run(input, func(t *testing.T) {
			got, err := parsePath(input, testBaseURL, testAccountID)
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

func TestConvertSDKErrorPreservesRequestID(t *testing.T) {
	sdkErr := &basecamp.Error{
		Code:       basecamp.CodeAPI,
		Message:    "server error",
		HTTPStatus: 500,
		RequestID:  "req-cli-123",
	}

	err := convertSDKError(sdkErr)

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeAPI, outErr.Code)

	var gotSDK *basecamp.Error
	require.True(t, errors.As(err, &gotSDK), "expected wrapped *basecamp.Error, got %T", err)
	assert.Equal(t, "req-cli-123", gotSDK.RequestID)
}
