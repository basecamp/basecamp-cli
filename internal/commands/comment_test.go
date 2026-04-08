package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommentShortcutAcceptsInFlag tests that the top-level 'comment' shortcut
// accepts --in, matching the 'comments' group. Previously, 'comment' was built
// directly from newCommentsCreateCmd() and did not inherit the persistent flags
// registered on NewCommentsCmd().
func TestCommentShortcutAcceptsInFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentCmd()

	// --in should be accepted (not "unknown flag"). The command will proceed
	// to RunE and hit an API/network error, which is fine — we're testing
	// flag acceptance, not API behavior.
	err := executeCommand(cmd, app, "--in", "456", "789", "hello")

	// If there's an error, it must NOT be "unknown flag"
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

// TestCommentShortcutAcceptsProjectFlag tests the -p shorthand works too.
func TestCommentShortcutAcceptsProjectFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentCmd()

	err := executeCommand(cmd, app, "-p", "456", "789", "hello")

	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

// TestCommentsGroupAcceptsInFlag tests the 'comments' group accepts --in.
func TestCommentsGroupAcceptsInFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentsCmd()

	err := executeCommand(cmd, app, "list", "--in", "456", "789")

	// Should not be "unknown flag" or "unknown shorthand"
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

type mockCommentCreateTransport struct {
	capturedBody []byte
}

func (t *mockCommentCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method != http.MethodPost {
		return nil, errors.New("unexpected request")
	}
	if !strings.HasSuffix(req.URL.Path, "/comments.json") {
		return nil, errors.New("unexpected path: " + req.URL.Path)
	}

	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.capturedBody = body
		req.Body.Close()
	}

	return &http.Response{
		StatusCode: 201,
		Body:       io.NopCloser(strings.NewReader(`{"id": 999, "content": "<p>hello from stdin</p>", "status": "active"}`)),
		Header:     header,
	}, nil
}

func TestCommentCreateReadsContentFromStdin(t *testing.T) {
	transport := &mockCommentCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, "hello from stdin")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
	})

	cmd := NewCommentCmd()
	err = executeCommand(cmd, app, "123")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "<p>hello from stdin</p>", body["content"])
}

func TestCommentCreatePrefersPositionalContentOverStdin(t *testing.T) {
	transport := &mockCommentCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, "ignored stdin")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
	})

	cmd := NewCommentCmd()
	err = executeCommand(cmd, app, "123", "hello from args")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "<p>hello from args</p>", body["content"])
}

func TestCommentsCreateReadsContentFromStdin(t *testing.T) {
	transport := &mockCommentCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, "hello from stdin")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
	})

	cmd := NewCommentsCmd()
	err = executeCommand(cmd, app, "create", "123")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "<p>hello from stdin</p>", body["content"])
}

func TestCommentCreateMissingContentReturnsUsageBeforeAccountResolution(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.AccountID = ""
	app.Flags.JSON = true

	devNull, err := os.Open("/dev/null")
	if err != nil {
		t.Skip("/dev/null not available")
	}

	origStdin := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() {
		os.Stdin = origStdin
		devNull.Close()
	})

	cmd := NewCommentCmd()
	err = executeCommand(cmd, app, "123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "<content> required")
	assert.NotContains(t, err.Error(), "account")
}

func TestReadPipedStdinIgnoresUnreadableStdin(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, w.Close())

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
	})

	content, hasPipedStdin := readPipedStdin()
	assert.Empty(t, content)
	assert.False(t, hasPipedStdin)
}
