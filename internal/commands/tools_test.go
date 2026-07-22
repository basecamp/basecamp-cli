package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// mockToolCreateTransport captures the create request and returns a 201 Tool.
// Project resolution is served so bucket resolution reaches the create call.
type mockToolCreateTransport struct {
	createCalled bool
	capturedPath string
	capturedBody map[string]any
}

func (t *mockToolCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/dock/tools.json") {
		t.createCalled = true
		t.capturedPath = req.URL.Path
		if req.Body != nil {
			data, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(data, &t.capturedBody)
		}
		body := `{"id": 777, "title": "Intervention Log", "name": "message_board", "enabled": true,` +
			`"position": 3, "status": "active", "url": "https://example.com", "app_url": "https://example.com",` +
			`"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	body := `{}`
	if strings.Contains(req.URL.Path, "/projects.json") {
		body = `[{"id": 123, "name": "Test Project"}, {"id": 456, "name": "Other Project"}]`
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

// TestToolsCreateRequiresType verifies that omitting --type produces a usage
// error naming the flag and issues no create request.
func TestToolsCreateRequiresType(t *testing.T) {
	transport := &mockToolCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "My Tool")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--type is required", e.Message)
	assert.False(t, transport.createCalled, "no create request should be issued")
}

// TestToolsCreateRejectsUnknownType verifies that an unrecognized --type errors
// with the friendly nouns in stable slice order, and issues no create request.
func TestToolsCreateRejectsUnknownType(t *testing.T) {
	for _, unknown := range []string{"board", "nope"} {
		transport := &mockToolCreateTransport{}
		app, _ := newTestAppWithTransport(t, transport)

		project := ""
		cmd := newToolsCreateCmd(&project)

		err := executeCommand(cmd, app, "--type", unknown)
		require.NotNil(t, err, "type %q should be rejected", unknown)

		var e *output.Error
		require.True(t, errors.As(err, &e))
		assert.Contains(t, e.Message, "chat, inbox, kanban_board, message_board, questionnaire, schedule, todoset, vault")
		assert.False(t, transport.createCalled, "no create request should be issued for %q", unknown)
	}
}

// TestNormalizeToolType covers the closed 8-set: each friendly noun, a
// degenerate spelling, and the canonical class-name all map to the right
// tool_type; bare "board" is rejected as ambiguous.
func TestNormalizeToolType(t *testing.T) {
	cases := []struct {
		input     string
		canonical string
	}{
		{"chat", "Chat::Transcript"},
		{"campfire", "Chat::Transcript"},
		{"Chat::Transcript", "Chat::Transcript"},
		{"inbox", "Inbox"},
		{"email", "Inbox"},
		{"Inbox", "Inbox"},
		{"kanban_board", "Kanban::Board"},
		{"cardtable", "Kanban::Board"},
		{"Kanban::Board", "Kanban::Board"},
		{"message_board", "Message::Board"},
		{"message-board", "Message::Board"},
		{"messages", "Message::Board"},
		{"Message::Board", "Message::Board"},
		{"questionnaire", "Questionnaire"},
		{"checkins", "Questionnaire"},
		{"Questionnaire", "Questionnaire"},
		{"schedule", "Schedule"},
		{"calendar", "Schedule"},
		{"Schedule", "Schedule"},
		{"todoset", "Todoset"},
		{"todos", "Todoset"},
		{"Todoset", "Todoset"},
		{"vault", "Vault"},
		{"docs", "Vault"},
		{"Vault", "Vault"},
	}
	for _, tc := range cases {
		got, ok := normalizeToolType(tc.input)
		assert.True(t, ok, "input %q should normalize", tc.input)
		assert.Equal(t, tc.canonical, got, "input %q", tc.input)
	}

	for _, bad := range []string{"board", "", "nope", "widget"} {
		_, ok := normalizeToolType(bad)
		assert.False(t, ok, "input %q should be rejected", bad)
	}
}

// TestToolsCreateRejectsExtraPositional verifies MaximumNArgs(1): a stray second
// positional errors instead of being silently dropped.
func TestToolsCreateRejectsExtraPositional(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "--type", "chat", "Title A", "Title B")
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "accepts at most 1 arg")
}

// TestToolsCreateExplicitProjectWinsAndIsSent verifies that an explicit
// --in/--project beats the global flag and config default, and the resolved
// bucket is the one posted to.
func TestToolsCreateExplicitProjectWinsAndIsSent(t *testing.T) {
	transport := &mockToolCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Flags.Project = "123"
	app.Config.ProjectID = "123"

	project := "456" // explicit --in wins over flag/config
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "--type", "message_board")
	require.NoError(t, err)

	require.True(t, transport.createCalled)
	assert.True(t, strings.HasSuffix(transport.capturedPath, "/99999/buckets/456/dock/tools.json"),
		"create path = %s", transport.capturedPath)
	assert.Equal(t, "Message::Board", transport.capturedBody["tool_type"])
}

// TestToolsCreateRequiresResolvableProject verifies that with nothing resolvable
// (no flag, no config, non-interactive) create errors before any request.
func TestToolsCreateRequiresResolvableProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No ProjectID configured — nothing to resolve, non-interactive.

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "--type", "chat")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Project ID required", e.Message)
}

// TestToolsCreateSendsTitle verifies a valid title reaches the request body.
func TestToolsCreateSendsTitle(t *testing.T) {
	transport := &mockToolCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "--type", "message_board", "Intervention Log")
	require.NoError(t, err)

	require.True(t, transport.createCalled)
	assert.Equal(t, "Intervention Log", transport.capturedBody["title"])
	assert.Equal(t, "Message::Board", transport.capturedBody["tool_type"])
}

// TestToolsCreateHappyPath verifies the full request path and 201 payload
// handling for a title-less create.
func TestToolsCreateHappyPath(t *testing.T) {
	transport := &mockToolCreateTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "--type", "todos")
	require.NoError(t, err)

	require.True(t, transport.createCalled)
	assert.True(t, strings.HasSuffix(transport.capturedPath, "/99999/buckets/123/dock/tools.json"),
		"create path = %s", transport.capturedPath)
	assert.Equal(t, "Todoset", transport.capturedBody["tool_type"])
	_, hasTitle := transport.capturedBody["title"]
	assert.False(t, hasTitle, "title should be omitted when not provided")

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.Equal(t, int64(777), envelope.Data.ID)
}

// TestToolsCreateTypeCompletion verifies the registered --type completion
// returns the eight friendly nouns in stable slice order.
func TestToolsCreateTypeCompletion(t *testing.T) {
	project := ""
	cmd := newToolsCreateCmd(&project)

	fn, ok := cmd.GetFlagCompletionFunc("type")
	require.True(t, ok)
	require.NotNil(t, fn)

	comps, directive := fn(cmd, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Equal(t, []string{
		"chat", "inbox", "kanban_board", "message_board",
		"questionnaire", "schedule", "todoset", "vault",
	}, comps)
}

// TestToolsRepositionAcceptsPosAlias tests that --pos works as an alias for --position.
func TestToolsRepositionAcceptsPosAlias(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsRepositionCmd(&project)

	// --pos should reach the RunE and proceed past the position guard
	err := executeCommand(cmd, app, "456", "--pos", "2")

	// Expect an API/network error — NOT "required flag" and NOT the RunE usage guard
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "required flag")
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "--position is required (1-based)", e.Message)
	}
}

// TestToolsRepositionRequiresPosition tests the RunE guard when neither flag is given.
func TestToolsRepositionRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsRepositionCmd(&project)

	err := executeCommand(cmd, app, "456")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--position is required (1-based)", e.Message)
}

// TestToolsUpdateRejectsLongTitle verifies that tool rename rejects titles over 64 characters.
func TestToolsUpdateRejectsLongTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsUpdateCmd(&project)

	longTitle := strings.Repeat("x", 65)
	err := executeCommand(cmd, app, "456", longTitle)
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Tool name too long")
}

// TestToolsUpdateAcceptsMaxTitle verifies that a 64-character title passes validation.
func TestToolsUpdateAcceptsMaxTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsUpdateCmd(&project)

	maxTitle := strings.Repeat("x", 64)
	err := executeCommand(cmd, app, "456", maxTitle)
	require.NotNil(t, err) // fails at network, not validation

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "too long")
	}
}

// TestToolsCreateRejectsLongTitle verifies that tool create rejects titles over 64 characters.
func TestToolsCreateRejectsLongTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	longTitle := strings.Repeat("x", 65)
	err := executeCommand(cmd, app, "--type", "chat", longTitle)
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Tool name too long")
}

// TestToolsCreateAcceptsMaxTitle verifies that a 64-character title passes create validation.
func TestToolsCreateAcceptsMaxTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	maxTitle := strings.Repeat("x", 64)
	err := executeCommand(cmd, app, "--type", "chat", maxTitle)
	require.NotNil(t, err) // fails at network, not validation

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "too long")
	}
}

// TestToolsShowNoProjectRequired verifies that tools show works without --in or a default project.
func TestToolsShowNoProjectRequired(t *testing.T) {
	app, _ := setupTestApp(t)
	// No ProjectID configured — should not prompt or error about project

	project := ""
	cmd := newToolsShowCmd(&project)

	err := executeCommand(cmd, app, "123")
	// Should reach the API call (network error), not fail on project resolution
	require.NotNil(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "project")
}

// TestToolsEnableNoProjectRequired verifies that tools enable works without --in.
func TestToolsEnableNoProjectRequired(t *testing.T) {
	app, _ := setupTestApp(t)

	project := ""
	cmd := newToolsEnableCmd(&project)

	err := executeCommand(cmd, app, "123")
	require.NotNil(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "project")
}

// TestToolsDisableNoProjectRequired verifies that tools disable works without --in.
func TestToolsDisableNoProjectRequired(t *testing.T) {
	app, _ := setupTestApp(t)

	project := ""
	cmd := newToolsDisableCmd(&project)

	err := executeCommand(cmd, app, "123")
	require.NotNil(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "project")
}

// TestToolsTrashNoProjectRequired verifies that tools trash works without --in.
func TestToolsTrashNoProjectRequired(t *testing.T) {
	app, _ := setupTestApp(t)

	project := ""
	cmd := newToolsTrashCmd(&project)

	err := executeCommand(cmd, app, "123")
	require.NotNil(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "project")
}

// TestToolsRepositionNoProjectRequired verifies that tools reposition works without --in.
func TestToolsRepositionNoProjectRequired(t *testing.T) {
	app, _ := setupTestApp(t)

	project := ""
	cmd := newToolsRepositionCmd(&project)

	err := executeCommand(cmd, app, "456", "--position", "2")
	require.NotNil(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "project")
}

// mockToolProjectFailTransport returns tools successfully but fails project resolution.
// This lets us prove that an explicit --in error stops the command before the tools API.
type mockToolProjectFailTransport struct {
	toolsCalled bool
}

func (t *mockToolProjectFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		// Return empty project list so name resolution fails with "not found"
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
			Header:     header,
		}, nil
	case strings.Contains(req.URL.Path, "/tools/"):
		t.toolsCalled = true
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id": 123, "title": "Chat", "name": "chat"}`)),
			Header:     header,
		}, nil
	default:
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     header,
		}, nil
	}
}

// TestToolsShowWithExplicitProjectErrorSurfaces verifies that an invalid explicit --in
// produces an error rather than silently dropping breadcrumbs, and the tools API is never called.
func TestToolsShowWithExplicitProjectErrorSurfaces(t *testing.T) {
	transport := &mockToolProjectFailTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = ""

	// Explicit --in with a name that won't match any project
	project := "nonexistent-project"
	cmd := newToolsShowCmd(&project)

	err := executeCommand(cmd, app, "123")
	require.NotNil(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not found")
	assert.False(t, transport.toolsCalled, "tools API should not be called when project resolution fails")
}

// TestToolsShowConfigProjectErrorIgnored verifies that a config default project that
// fails resolution is silently ignored (best-effort breadcrumbs).
func TestToolsShowConfigProjectErrorIgnored(t *testing.T) {
	app, _ := setupTestApp(t)
	// Config default with a name (not numeric) — resolution will fail on noNetworkTransport
	app.Config.ProjectID = "stale-project-name"

	project := "" // no explicit flag
	cmd := newToolsShowCmd(&project)

	err := executeCommand(cmd, app, "123")
	// Should reach the API call, not fail on project resolution
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "stale-project-name")
}

// mockToolTransport serves canned responses for tools and project resolution.
type mockToolTransport struct{}

func (t *mockToolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.HasSuffix(req.URL.Path, "/tools/555"):
		body = `{"id": 555, "title": "Chat", "name": "chat", "enabled": true, "position": 2,` +
			`"status": "active", "url": "https://example.com", "app_url": "https://example.com",` +
			`"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z"}`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

// TestToolsShowBreadcrumbsWithProject verifies that --in produces breadcrumbs containing --in <id>.
func TestToolsShowBreadcrumbsWithProject(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockToolTransport{})
	app.Config.ProjectID = "" // clear default so only explicit flag matters
	app.Flags.Hints = true    // enable breadcrumbs in output

	project := "123"
	cmd := newToolsShowCmd(&project)

	err := executeCommand(cmd, app, "555")
	require.NoError(t, err)

	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	// Should have rename, reposition, and project breadcrumbs
	require.Len(t, envelope.Breadcrumbs, 3)

	assert.Contains(t, envelope.Breadcrumbs[0].Cmd, "--in 123")
	assert.Contains(t, envelope.Breadcrumbs[1].Cmd, "--in 123")
	assert.Equal(t, "project", envelope.Breadcrumbs[2].Action)
	assert.Contains(t, envelope.Breadcrumbs[2].Cmd, "projects show 123")
}

// TestToolsShowBreadcrumbsWithoutProject verifies that omitting --in produces
// breadcrumbs without --in and no "View project" breadcrumb.
func TestToolsShowBreadcrumbsWithoutProject(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockToolTransport{})
	app.Config.ProjectID = "" // no default project
	app.Flags.Hints = true    // enable breadcrumbs in output

	project := ""
	cmd := newToolsShowCmd(&project)

	err := executeCommand(cmd, app, "555")
	require.NoError(t, err)

	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	// Should have rename and reposition breadcrumbs only — no project breadcrumb
	require.Len(t, envelope.Breadcrumbs, 2)

	for _, bc := range envelope.Breadcrumbs {
		assert.NotContains(t, bc.Cmd, "--in")
		assert.NotEqual(t, "project", bc.Action)
	}
}
