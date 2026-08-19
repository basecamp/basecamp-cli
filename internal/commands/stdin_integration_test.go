package commands

// Integration coverage for the "-" (stdin) tier-1 patterns: one command per
// resolver shape, driven through a real Execute with a mock transport.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// Pattern 2: exact positional — messages create <title> [body].
func TestMessagesCreateBodyDashReadsStdin(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()
	cmd.SetIn(strings.NewReader("Body **from stdin**\n"))

	err := executeMessagesCommand(cmd, app, "create", "Title", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "Title", body["subject"])
	content, _ := body["content"].(string)
	assert.Contains(t, content, "<strong>from stdin</strong>")
}

// Pattern 3: content flag — api post --data -.
func TestAPIPostDataDashReadsStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewAPICmd()
	cmd.SetIn(strings.NewReader(`{"content":"from stdin"}` + "\n"))

	err := executeCommand(cmd, app, "post", "/buckets/1/todos.json", "--data", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBodies)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "from stdin", body["content"])
}

// Pattern 1: join-all positionals — todos create <content>.
func TestTodosCreateDashReadsStdin(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockTodoCreateTransport{}
	cfg := &config.Config{AccountID: "99999", ProjectID: "123", TodolistID: "456"}
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}

	cmd := NewTodosCmd()
	cmd.SetIn(strings.NewReader("Call the vendor back\n"))

	err := executeTodosCommand(cmd, app, "create", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "Call the vendor back", body["content"],
		"the trailing newline must be trimmed from a piped title")
}

// Boost content from stdin: the trailing newline is trimmed before the 16-rune
// limit is applied, so a printf'd emoji doesn't burn a rune.
func TestBoostCreateDashReadsStdin(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	cmd.SetIn(strings.NewReader("🎉\n"))

	err := executeBoostCommand(cmd, app, "create", "456", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "🎉", body["content"])
}

func TestBoostCreateDashOverLimitStillRejected(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	cmd.SetIn(strings.NewReader("seventeen chars!!\n"))

	err := executeBoostCommand(cmd, app, "create", "456", "-")
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Boost content too long")
}

// Pattern 3 on an update: todos update --description -.
func TestTodosUpdateDescriptionDashReadsStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewTodosCmd()
	cmd.SetIn(strings.NewReader("New **details**\n"))

	err := executeCommand(cmd, app, "update", "789", "--description", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBodies)

	found := false
	for _, captured := range transport.capturedBodies {
		var body map[string]any
		if json.Unmarshal(captured, &body) == nil {
			if desc, _ := body["description"].(string); strings.Contains(desc, "<strong>details</strong>") {
				found = true
			}
		}
	}
	assert.True(t, found, "the piped description should reach the wire as HTML")
}
