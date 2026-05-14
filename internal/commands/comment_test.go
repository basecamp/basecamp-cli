package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/names"
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

func TestCommentsCreateReadsDashContentFromStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := newCommentsCreateCmd()
	cmd.SetIn(strings.NewReader("Hello from stdin\n\n**works**\n"))

	err := executeCommand(cmd, app, "789", "-")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]string
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Contains(t, body["content"], "Hello from stdin")
	assert.Contains(t, body["content"], "<strong>works</strong>")
	assert.NotEqual(t, "<p>-</p>", body["content"])
}

func TestCommentsUpdateReadsDashContentFromStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewCommentsCmd()
	cmd.SetIn(strings.NewReader("Updated from stdin\n"))

	err := executeCommand(cmd, app, "update", "1234", "-")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]string
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "<p>Updated from stdin</p>", body["content"])
}

type mockCommentWriteTransport struct {
	capturedBodies [][]byte
}

func (t *mockCommentWriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		t.capturedBodies = append(t.capturedBodies, body)
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	status := http.StatusOK
	if req.Method == http.MethodPost {
		status = http.StatusCreated
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{"id":1234,"content":"ok","status":"active"}`)),
		Header:     header,
	}, nil
}

func setupCommentsWriteTestApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	app, buf := setupTestApp(t)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	app.SDK = sdkClient
	app.Names = names.NewResolver(sdkClient, app.Auth, app.Config.AccountID)
	return app, buf
}
