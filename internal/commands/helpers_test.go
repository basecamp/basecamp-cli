package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Valid numeric strings
		{"0", true},
		{"1", true},
		{"123", true},
		{"123456789", true},

		// Invalid inputs
		{"", false},
		{"abc", false},
		{"123abc", false},
		{"abc123", false},
		{"12.34", false},
		{"-1", false},
		{" 123", false},
		{"123 ", false},
		{"12 34", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isNumeric(tt.input)
			assert.Equal(t, tt.expected, result, "isNumeric(%q)", tt.input)
		})
	}
}

func TestApplySubscribeFlags_MutualExclusion(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=true, noSubscribe=true
	_, err := applySubscribeFlags(ctx, nil, "someone", true, true)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

func TestApplySubscribeFlags_NoSubscribe(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=false, noSubscribe=true
	result, err := applySubscribeFlags(ctx, nil, "", false, true)

	require.NoError(t, err)
	require.NotNil(t, result, "expected non-nil pointer for --no-subscribe")
	assert.Empty(t, *result, "expected empty slice for --no-subscribe")
}

func TestApplySubscribeFlags_Neither(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=false, noSubscribe=false
	result, err := applySubscribeFlags(ctx, nil, "", false, false)

	require.NoError(t, err)
	assert.Nil(t, result, "expected nil when neither flag is set")
}

func TestApplySubscribeFlags_ExplicitEmptyString(t *testing.T) {
	// --subscribe "" (explicitly set but empty value) should be a hard error
	ctx := context.Background()
	// subscribeChanged=true (flag was explicitly passed), value=""
	_, err := applySubscribeFlags(ctx, nil, "", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}

func TestApplySubscribeFlags_WhitespaceOnlyRequiresAtLeastOne(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=true, value=" "
	_, err := applySubscribeFlags(ctx, nil, " ", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}

func TestApplySubscribeFlags_CommaOnlyRequiresAtLeastOne(t *testing.T) {
	// --subscribe ",,," should fail: only delimiters, no actual tokens
	ctx := context.Background()
	// subscribeChanged=true, value=",,,"
	_, err := applySubscribeFlags(ctx, nil, ",,,", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}

// newTestCmd creates a minimal cobra.Command for testing missingArg/noChanges.
// The --agent flag on the root simulates machine-output detection.
func newTestCmd(agent bool, example string) *cobra.Command {
	root := &cobra.Command{Use: "basecamp"}
	root.PersistentFlags().Bool("agent", false, "")
	root.PersistentFlags().Bool("json", false, "")

	child := &cobra.Command{
		Use:     "test <arg>",
		Example: example,
	}
	// Capture output so cmd.Help() doesn't write to real stdout
	child.SetOut(&bytes.Buffer{})
	child.SetContext(context.Background())
	root.AddCommand(child)

	if agent {
		_ = root.PersistentFlags().Set("agent", "true")
	}
	return child
}

func TestMissingArg_AgentMode(t *testing.T) {
	cmd := newTestCmd(true, "  basecamp test \"hello\"")

	err := missingArg(cmd, "<arg>")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "<arg> required", e.Message)
	assert.Contains(t, e.Hint, "Usage: basecamp test")
	assert.Contains(t, e.Hint, "Example: basecamp test \"hello\"")
}

func TestMissingArg_AgentMode_NoExample(t *testing.T) {
	cmd := newTestCmd(true, "")

	err := missingArg(cmd, "<query>")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "<query> required", e.Message)
	assert.Contains(t, e.Hint, "Usage:")
	assert.NotContains(t, e.Hint, "Example:")
}

func TestMissingArg_InteractiveMode(t *testing.T) {
	cmd := newTestCmd(false, "")

	// In interactive (non-agent) mode, missingArg returns nil (cmd.Help() succeeds)
	err := missingArg(cmd, "<arg>")
	assert.NoError(t, err)
}

func TestNoChanges_AgentMode(t *testing.T) {
	cmd := newTestCmd(true, "  basecamp test 123 --title \"New\"")

	err := noChanges(cmd)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "No update fields specified", e.Message)
	assert.Contains(t, e.Hint, "Usage:")
	assert.Contains(t, e.Hint, "Example:")
}

func TestNoChanges_InteractiveMode(t *testing.T) {
	cmd := newTestCmd(false, "")

	err := noChanges(cmd)
	assert.NoError(t, err)
}

func TestIsMachineOutput_AgentFlag(t *testing.T) {
	cmd := newTestCmd(true, "")
	assert.True(t, isMachineOutput(cmd))
}

func TestIsMachineOutput_NoFlags(t *testing.T) {
	cmd := newTestCmd(false, "")
	// With a buffer (not a real file), the file stat fallback won't trigger
	assert.False(t, isMachineOutput(cmd))
}

func TestIsMachineOutput_JSONFlag(t *testing.T) {
	cmd := newTestCmd(false, "")
	_ = cmd.Root().PersistentFlags().Set("json", "true")
	assert.True(t, isMachineOutput(cmd))
}

func TestMissingArg_MultiLineExample(t *testing.T) {
	cmd := newTestCmd(true, "  basecamp test \"first\"\n  basecamp test \"second\"")

	err := missingArg(cmd, "<arg>")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	// Should only include the first example line
	assert.Contains(t, e.Hint, "Example: basecamp test \"first\"")
	assert.NotContains(t, e.Hint, "second")
}

// dockTestTokenProvider is a mock token provider for getDockToolID tests.
type dockTestTokenProvider struct{}

func (dockTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// dockTestTransport returns canned project JSON for dock resolution tests.
type dockTestTransport struct {
	projectJSON string
}

func (t *dockTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/projects/") {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(t.projectJSON)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	return nil, errors.New("unexpected request: " + req.URL.Path)
}

func newDockTestApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	sdk := basecamp.NewClient(&basecamp.Config{}, dockTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	return &appctx.App{
		Config: &config.Config{AccountID: "99999"},
		SDK:    sdk,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}
}

func TestGetDockToolID_DisabledToolShowsDisabledError(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [{"name": "chat", "id": 789, "enabled": false}]}`,
	}
	app := newDockTestApp(t, transport)

	_, err := getDockToolID(context.Background(), app, "1", "chat", "", "chat", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "disabled for this project")
}

func TestGetDockToolID_AbsentToolShowsNotFoundError(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": []}`,
	}
	app := newDockTestApp(t, transport)

	_, err := getDockToolID(context.Background(), app, "1", "chat", "", "chat", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "has no chat")
}

func TestGetDockToolID_AmbiguousToolShowsFlagHint(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "chat", "id": 100, "title": "General", "enabled": true},
			{"name": "chat", "id": 200, "title": "Engineering", "enabled": true}
		]}`,
	}
	app := newDockTestApp(t, transport)

	_, err := getDockToolID(context.Background(), app, "1", "chat", "", "chat", "chat")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Equal(t, "Multiple chats found", e.Message)
	assert.Contains(t, e.Hint, "Specify one with --chat <id>:")
	assert.Contains(t, e.Hint, "  100  General")
	assert.Contains(t, e.Hint, "  200  Engineering")
}

func TestGetDockToolID_AmbiguousInboxPluralizesCorrectly(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "inbox", "id": 300, "title": "Support", "enabled": true},
			{"name": "inbox", "id": 400, "title": "Billing", "enabled": true}
		]}`,
	}
	app := newDockTestApp(t, transport)

	_, err := getDockToolID(context.Background(), app, "1", "inbox", "", "inbox", "inbox")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Multiple inboxes found", e.Message)
	assert.Contains(t, e.Hint, "Specify one with --inbox <id>:")
	assert.Contains(t, e.Hint, "  300  Support")
	assert.Contains(t, e.Hint, "  400  Billing")
}

func TestGetDockTools_ReturnsMultipleEnabled(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "chat", "id": 789, "enabled": true, "title": "General"},
			{"name": "chat", "id": 790, "enabled": true, "title": "Random"}
		]}`,
	}
	app := newDockTestApp(t, transport)

	enabled, all, err := getDockTools(context.Background(), app, "1", "chat")
	require.NoError(t, err)
	assert.Len(t, enabled, 2)
	assert.Len(t, all, 2)
	ids := []int64{enabled[0].ID, enabled[1].ID}
	assert.Contains(t, ids, int64(789))
	assert.Contains(t, ids, int64(790))
}

func TestGetDockTools_FiltersDisabledFromEnabled(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "chat", "id": 789, "enabled": true, "title": "Active"},
			{"name": "chat", "id": 790, "enabled": false, "title": "Disabled"}
		]}`,
	}
	app := newDockTestApp(t, transport)

	enabled, all, err := getDockTools(context.Background(), app, "1", "chat")
	require.NoError(t, err)
	assert.Len(t, enabled, 1)
	assert.Equal(t, int64(789), enabled[0].ID)
	assert.Len(t, all, 2)
}

func TestGetDockTools_NoMatchingTools(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [{"name": "todoset", "id": 100, "enabled": true}]}`,
	}
	app := newDockTestApp(t, transport)

	enabled, all, err := getDockTools(context.Background(), app, "1", "chat")
	require.NoError(t, err)
	assert.Empty(t, enabled)
	assert.Empty(t, all)
}

func TestGetDockToolID_AmbiguousMultipleReturnsError(t *testing.T) {
	transport := &dockTestTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "chat", "id": 789, "enabled": true, "title": "General"},
			{"name": "chat", "id": 790, "enabled": true, "title": "Random"}
		]}`,
	}
	app := newDockTestApp(t, transport)

	_, err := getDockToolID(context.Background(), app, "1", "chat", "", "chat", "chat")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "789")
	assert.Contains(t, e.Hint, "790")
}
