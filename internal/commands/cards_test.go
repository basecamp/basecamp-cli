package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// noNetworkTransport is an http.RoundTripper that fails immediately.
// Used in tests to prevent real network calls without waiting for timeouts.
type noNetworkTransport struct{}

func (noNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// testTokenProvider is a mock token provider for tests.
type testTokenProvider struct{}

func (t *testTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// TestIsNumericID tests the isNumericID helper function.
func TestIsNumericID(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Valid numeric IDs
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
			result := isNumericID(tt.input)
			assert.Equal(t, tt.expected, result, "isNumericID(%q)", tt.input)
		})
	}
}

// setupTestApp creates a minimal test app context with a mock output writer.
// The app has a configured account but no project (unless project is set in config).
func setupTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Disable keyring access during tests
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999", // Required for RequireAccount()
	}

	// Create SDK client with mock token provider and no-network transport
	// The transport prevents real HTTP calls - fails instantly instead of timing out
	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, &testTokenProvider{},
		basecamp.WithTransport(noNetworkTransport{}),
		basecamp.WithMaxRetries(1), // Disable retries for instant failure
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
	return app, buf
}

// executeCommand executes a cobra command with the given args and returns the error.
func executeCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

// TestCardsColumnColorRequiresColor tests that --color is required for color command.
func TestCardsColumnColorShowsHelp(t *testing.T) {
	app, _ := setupTestApp(t)

	// Configure app with project
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsColumnColorCmd(&project)

	err := executeCommand(cmd, app, "456") // column ID but no --color
	assert.NoError(t, err, "expected help output, not error")
}

// TestCardsStepsShowsHelpWithoutCardID tests that help is shown when card ID is missing.
func TestCardsStepsShowsHelpWithoutCardID(t *testing.T) {
	app, _ := setupTestApp(t)

	// Configure app with project
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepsCmd(&project)

	err := executeCommand(cmd, app) // no card ID → shows help
	require.NoError(t, err)
}

// TestCardsStepCreateShowsHelpWithoutTitle tests that help is shown when --title is missing.
func TestCardsStepCreateShowsHelpWithoutTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepCreateCmd(&project)

	// No title — shows help
	err := executeCommand(cmd, app)
	assert.NoError(t, err)
}

// TestCardsStepCreateRequiresCard tests that --card is required for step create when title is given.
func TestCardsStepCreateRequiresCard(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepCreateCmd(&project)

	// Title as positional arg, no --card flag
	err := executeCommand(cmd, app, "My step")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "--card is required", e.Message)
	}
}

// TestCardsStepUpdateRequiresFields tests that at least one field is required for step update.
func TestCardsStepUpdateRequiresFields(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newCardsStepUpdateCmd()

	err := executeCommand(cmd, app, "456") // step ID but no update fields — shows help
	assert.NoError(t, err, "expected help output, not error")
}

// mockStepUpdateTransport serves the current step on GET and captures the
// update body on PUT. It only answers the single-step endpoint so a stray
// call to the wrong path fails the test instead of passing on stale data.
type mockStepUpdateTransport struct {
	getCount    int
	capturedPut []byte
}

func (t *mockStepUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.HasSuffix(req.URL.Path, "/card_tables/steps/456") {
		return nil, fmt.Errorf("unexpected request path: %s", req.URL.Path)
	}

	switch req.Method {
	case "GET":
		t.getCount++
	case "PUT":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		t.capturedPut = body
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unexpected request method: %s", req.Method)
	}

	// Echo the title back so callers see the effective value, not a constant.
	title := "Current title"
	if t.capturedPut != nil {
		var put struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(t.capturedPut, &put); err == nil && put.Title != "" {
			title = put.Title
		}
	}
	stepJSON := fmt.Sprintf(`{"id": 456, "title": %q, "completed": false, "assignees": []}`, title)

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(stepJSON)),
		Header:     header,
	}, nil
}

// TestCardsStepUpdateAssigneesOnlyCarriesTitle verifies that updating only
// assignees fetches the current step and includes its title in the request —
// the API rejects step updates without a title.
func TestCardsStepUpdateAssigneesOnlyCarriesTitle(t *testing.T) {
	transport := &mockStepUpdateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := newCardsStepUpdateCmd()
	err := executeCommand(cmd, app, "456", "--assignees", "789")
	require.NoError(t, err)

	assert.Equal(t, 1, transport.getCount)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedPut, &body))
	assert.Equal(t, "Current title", body["title"])
	assert.Equal(t, []any{float64(789)}, body["assignee_ids"])
}

// TestCardsStepUpdateDueOnlyCarriesTitle verifies that updating only the due
// date fetches the current step and includes its title in the request.
func TestCardsStepUpdateDueOnlyCarriesTitle(t *testing.T) {
	transport := &mockStepUpdateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := newCardsStepUpdateCmd()
	err := executeCommand(cmd, app, "456", "--due", "2026-07-04")
	require.NoError(t, err)

	assert.Equal(t, 1, transport.getCount)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedPut, &body))
	assert.Equal(t, "Current title", body["title"])
	assert.Equal(t, "2026-07-04", body["due_on"])
}

// TestCardsStepUpdateWithTitleSkipsFetch verifies that an explicit title is
// sent as-is without fetching the current step.
func TestCardsStepUpdateWithTitleSkipsFetch(t *testing.T) {
	transport := &mockStepUpdateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := newCardsStepUpdateCmd()
	err := executeCommand(cmd, app, "456", "New title")
	require.NoError(t, err)

	assert.Equal(t, 0, transport.getCount)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedPut, &body))
	assert.Equal(t, "New title", body["title"])
}

// TestCardsStepMoveRequiresCard tests that --card is required for step move.
func TestCardsStepMoveShowsHelp(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newCardsStepMoveCmd()

	// Step ID and position but no card — shows help
	err := executeCommand(cmd, app, "456", "--position", "1")
	assert.NoError(t, err, "expected help output, not error")
}

// TestCardsStepMoveRequiresPosition tests that --position is required for step move.
func TestCardsStepMoveRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newCardsStepMoveCmd()

	// Step ID and card but no position
	err := executeCommand(cmd, app, "456", "--card", "789")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "--position is required (0-indexed)", e.Message)
	}
}

// TestCardsCmdRequiresProject tests that Project ID required when not in config.
// TestCardsCmdWithoutProjectListsAccountWide tests that a bare `cards list`
// with no project anywhere lists account-wide rather than prompting.
func TestCardsCmdWithoutProjectListsAccountWide(t *testing.T) {
	app, transport := setupRecordingTestApp(t, cardsOpenRoute())
	// No project in config

	cmd := NewCardsCmd()

	require.NoError(t, executeRecordingCommand(cmd, app, "list"))
	assert.Equal(t, "/99999/cards/open.json", transport.last(t).Path)
}

// TestCardsListColumnNameRequiresCardTable tests that column name requires --card-table.
func TestCardsListColumnNameRequiresCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Use column name (not numeric) without --card-table
	err := executeCommand(cmd, app, "list", "--column", "Done")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "--card-table is required when using --column with a name", e.Message)
	}
}

// TestCardsColumnCreateShowsHelpWithoutTitle tests that help is shown when --title is missing.
func TestCardsColumnCreateShowsHelpWithoutTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := ""
	cmd := newCardsColumnCreateCmd(&project, &cardTable)

	err := executeCommand(cmd, app)
	assert.NoError(t, err)
}

// TestCardsColumnUpdateShowsHelpWithNoFlags tests that column update with no flags shows help.
func TestCardsColumnUpdateShowsHelpWithNoFlags(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newCardsColumnUpdateCmd()

	err := executeCommand(cmd, app, "456") // column ID but no update fields shows help
	assert.NoError(t, err)
}

// TestCardsColumnMoveRequiresPosition tests that --position is required for column move.
func TestCardsColumnMoveRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := ""
	cmd := newCardsColumnMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456") // column ID but no position
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		// Match the actual error message format
		assert.Equal(t, "--position required (1-indexed)", e.Message)
	}
}

// TestCardsMoveShowsHelpWithoutTo tests that help is shown when --to is missing.
func TestCardsMoveShowsHelpWithoutTo(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	// Card ID but no --to — shows help
	err := executeCommand(cmd, app, "456")
	assert.NoError(t, err)
}

// TestCardsMoveRequiresCardTable tests that --card-table is required for cards move when using --to with a column name.
func TestCardsMoveRequiresCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "" // empty card table
	cmd := newCardsMoveCmd(&project, &cardTable)

	// Card ID with --to (column name) but no --card-table
	err := executeCommand(cmd, app, "456", "--to", "Done")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "--card-table is required when --to is a column name", e.Message)
	}
}

// TestCardsMovePositionWithOnHoldRejected tests that --position and --on-hold cannot be used together.
func TestCardsMovePositionWithOnHoldRejected(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "789", "--on-hold", "--position", "1")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "--position cannot be used with --on-hold", e.Message)
	}
}

// mockOnHoldTransport handles the API calls for --on-hold card moves.
// Flow: GET /projects.json -> GET card -> GET column (with on_hold) -> POST move.
type mockOnHoldTransport struct {
	capturedMovePath string
	capturedMoveBody []byte
}

func (t *mockOnHoldTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "POST" && strings.Contains(req.URL.Path, "/moves.json") {
		t.capturedMovePath = req.URL.Path
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedMoveBody = body
			req.Body.Close()
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
	}

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/card_tables/cards/456"):
			body = `{"id": 456, "title": "Test Card", "parent": {"id": 777, "title": "Developing", "type": "Kanban::Column"}}`
		case strings.Contains(req.URL.Path, "/card_tables/columns/777"):
			body = `{"id": 777, "title": "Developing", "on_hold": {"id": 888, "status": "active", "inherits_status": false, "title": "On hold", "cards_count": 0, "cards_url": "https://example.com/cards.json"}}`
		default:
			return nil, fmt.Errorf("unexpected GET request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
}

// TestCardsMoveOnHoldWithoutToDoesNotRequireCardTable tests that --on-hold without --to
// fetches the card's current column via CardColumns().Get and moves to its on-hold section,
// without requiring --card-table.
func TestCardsMoveOnHoldWithoutToDoesNotRequireCardTable(t *testing.T) {
	transport := &mockOnHoldTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--on-hold")
	require.NoError(t, err)

	assert.Contains(t, transport.capturedMovePath, "/card_tables/cards/456/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(888), body["column_id"])
}

// TestCardsMoveOnHoldWithNumericTo tests --on-hold with a numeric --to column ID.
// The card should move to the on-hold section of the specified column.
func TestCardsMoveOnHoldWithNumericTo(t *testing.T) {
	transport := &mockOnHoldTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777", "--on-hold")
	require.NoError(t, err)

	assert.Contains(t, transport.capturedMovePath, "/card_tables/cards/456/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(888), body["column_id"])
}

// TestCardsMoveOnHoldDisabledError tests that moving to on-hold fails when the column
// does not have an on-hold section.
func TestCardsMoveOnHoldDisabledError(t *testing.T) {
	transport := &mockOnHoldDisabledTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--on-hold")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Contains(t, e.Message, "does not have an on-hold section")
	}
}

// mockOnHoldDisabledTransport returns a column without an on-hold section.
type mockOnHoldDisabledTransport struct{}

func (t *mockOnHoldDisabledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/card_tables/cards/456"):
			body = `{"id": 456, "title": "Test Card", "parent": {"id": 777, "title": "Developing", "type": "Kanban::Column"}}`
		case strings.Contains(req.URL.Path, "/card_tables/columns/777"):
			body = `{"id": 777, "title": "Developing"}`
		default:
			return nil, fmt.Errorf("unexpected GET request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
}

// TestCardsMoveOnHoldWithNamedColumn tests --on-hold with a named --to column.
// The card should resolve the column by name from the card table and move to its on-hold section.
func TestCardsMoveOnHoldWithNamedColumn(t *testing.T) {
	transport := &mockOnHoldNamedColumnTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "Developing", "--on-hold")
	require.NoError(t, err)

	assert.Contains(t, transport.capturedMovePath, "/card_tables/cards/456/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(888), body["column_id"])
}

// mockOnHoldNamedColumnTransport handles API calls for --on-hold with a named column.
// Flow: GET /projects.json -> GET card table (with lists) -> POST move.
type mockOnHoldNamedColumnTransport struct {
	capturedMovePath string
	capturedMoveBody []byte
}

func (t *mockOnHoldNamedColumnTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "POST" && strings.Contains(req.URL.Path, "/moves.json") {
		t.capturedMovePath = req.URL.Path
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedMoveBody = body
			req.Body.Close()
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
	}

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/123"):
			body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 999, "title": "Board"}]}`
		case strings.Contains(req.URL.Path, "/card_tables/999"):
			body = `{"id": 999, "lists": [{"id": 777, "title": "Developing", "on_hold": {"id": 888, "status": "active", "inherits_status": false, "title": "On hold", "cards_count": 0, "cards_url": "https://example.com/cards.json"}}]}`
		default:
			return nil, fmt.Errorf("unexpected GET request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
}

// TestCardsColumnsRequiresProject tests that Project ID required for columns listing.
func TestCardsColumnsRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cardTable := ""
	cmd := newCardsColumnsCmd(&project, &cardTable)

	err := executeCommand(cmd, app)
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "Project ID required", e.Message)
	}
}

// TestCardsColumnShowRequiresProject tests that Project ID required for column show.
func TestCardsColumnShowRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cmd := newCardsColumnShowCmd(&project)

	err := executeCommand(cmd, app, "456")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "Project ID required", e.Message)
	}
}

// =============================================================================
// Numeric Column ID Shortcut Tests
// =============================================================================

// TestCardsListNumericColumnDoesNotRequireCardTable tests that numeric column IDs
// don't require --card-table since they can be used directly.
func TestCardsListNumericColumnDoesNotRequireCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Use numeric column ID without --card-table
	// This should NOT error with "card-table is required" since 12345 is numeric
	// Instead it will proceed and hit auth/API errors (which we can't test without mocking)
	err := executeCommand(cmd, app, "list", "--column", "12345")

	// If there's an error, it should NOT be about requiring --card-table
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) {
			assert.NotEqual(t, "--card-table is required when using --column with a name", e.Message,
				"Numeric column ID should not require --card-table")
		}
	}
}

// TestCardsCreateNumericColumnDoesNotRequireCardTable tests that numeric column IDs
// work for create without --card-table.
func TestCardsCreateNumericColumnDoesNotRequireCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Use numeric column ID without --card-table
	err := executeCommand(cmd, app, "create", "--title", "Test", "--column", "12345")

	// If there's an error, it should NOT be about requiring --card-table
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) {
			assert.NotEqual(t, "--card-table is required when using --column with a name", e.Message,
				"Numeric column ID should not require --card-table for create")
		}
	}
}

// TestCardsMoveNumericToDoesNotRequireCardTable tests that numeric --to column IDs
// work without --card-table (bypassing the card-table requirement).
func TestCardsMoveWithNumericTo(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "" // empty - no card table specified
	cmd := newCardsMoveCmd(&project, &cardTable)

	// Card ID with numeric --to but no --card-table - should bypass card-table requirement
	err := executeCommand(cmd, app, "456", "--to", "12345")

	// Expect some error (likely auth), but NOT the card-table requirement error
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if errors.As(err, &e) {
		// Should NOT be the card-table error - numeric IDs bypass that requirement
		assert.NotEqual(t, "--card-table is required when --to is a column name", e.Message,
			"numeric --to should not require --card-table")
	}
}

// TestCardsMovePartialNumericRequiresCardTable tests that partial numeric strings
// like "123abc" are NOT treated as numeric IDs and DO require --card-table.
// This prevents incorrect partial matching (e.g., Sscanf matching "123" from "123abc").
func TestCardsMovePartialNumericRequiresCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "" // empty - no card table specified
	cmd := newCardsMoveCmd(&project, &cardTable)

	// "123abc" looks like a number but isn't - should require --card-table
	err := executeCommand(cmd, app, "456", "--to", "123abc")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		// MUST be the card-table error - partial numeric is NOT a valid ID
		assert.Equal(t, "--card-table is required when --to is a column name", e.Message)
	}
}

// TestCardsColumnNameVariations tests various column name formats.
func TestCardsColumnNameVariations(t *testing.T) {
	tests := []struct {
		name            string
		columnArg       string
		expectCardTable bool // true if --card-table should be required
	}{
		{"pure numeric", "123", false},
		{"leading zero", "0123", false},
		{"large number", "9999999999", false},
		{"alpha only", "Done", true},
		{"alpha with spaces", "In Progress", true},
		{"mixed alphanumeric", "Phase1", true},
		{"numeric with prefix", "col123", true},
		{"numeric with suffix", "123abc", true},
		{"empty", "", false}, // Empty doesn't require card-table (different validation)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _ := setupTestApp(t)
			app.Config.ProjectID = "123"

			cmd := NewCardsCmd()

			args := []string{"list"}
			if tt.columnArg != "" {
				args = append(args, "--column", tt.columnArg)
			}

			err := executeCommand(cmd, app, args...)

			var e *output.Error
			if tt.expectCardTable && err != nil {
				if errors.As(err, &e) {
					assert.Equal(t, "--card-table is required when using --column with a name", e.Message)
				}
			} else if !tt.expectCardTable && err != nil {
				if errors.As(err, &e) {
					assert.NotEqual(t, "--card-table is required when using --column with a name", e.Message,
						"numeric column %q should not require --card-table", tt.columnArg)
				}
			}
		})
	}
}

// =============================================================================
// Helper Function Tests
// =============================================================================

// TestFormatCardTableIDs tests the formatCardTableIDs helper.
func TestFormatCardTableIDs(t *testing.T) {
	tests := []struct {
		name       string
		cardTables []projectCardTable
		expected   string
	}{
		{
			name: "single with title",
			cardTables: []projectCardTable{
				{ID: 123, Title: "Sprint Board"},
			},
			expected: "[123 (Sprint Board)]",
		},
		{
			name: "single without title",
			cardTables: []projectCardTable{
				{ID: 456, Title: ""},
			},
			expected: "[456]",
		},
		{
			name: "multiple with titles",
			cardTables: []projectCardTable{
				{ID: 123, Title: "Sprint Board"},
				{ID: 456, Title: "Backlog"},
			},
			expected: "[123 (Sprint Board) 456 (Backlog)]",
		},
		{
			name: "mixed titles",
			cardTables: []projectCardTable{
				{ID: 123, Title: "Sprint Board"},
				{ID: 456, Title: ""},
				{ID: 789, Title: "Archive"},
			},
			expected: "[123 (Sprint Board) 456 789 (Archive)]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCardTableIDs(tt.cardTables)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// Cards Create Validation Tests
// =============================================================================

// TestCardsCreateShowsHelpWithoutTitle tests that help is shown when --title is missing.
func TestCardsCreateShowsHelpWithoutTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "create")
	assert.NoError(t, err)
}

// TestCardsUpdateShowsHelpWithNoFlags tests that update with no flags shows help.
func TestCardsUpdateShowsHelpWithNoFlags(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Update with card ID but no flags shows help (returns nil)
	err := executeCommand(cmd, app, "update", "12345")
	assert.NoError(t, err)
}

// TestCardsUpdateRequiresFields tests that at least one field is required for update.
func TestCardsUpdateShowsHelp(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "update", "456") // card ID but no fields — shows help
	assert.NoError(t, err, "expected help output, not error")
}

// TestCardsShowRequiresCardID tests that card ID is required for show.
// Cobra validates args count, so we get a Cobra error.
func TestCardsShowRequiresCardID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "show")
	require.NotNil(t, err, "expected error, got nil")

	// Cobra validates args count first
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestCardsMoveRequiresCardID tests that card ID is required for move.
// Cobra validates args count, so we get a Cobra error.
func TestCardsMoveRequiresCardID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	// No card ID, just --to flag
	err := executeCommand(cmd, app, "--to", "Done")
	require.NotNil(t, err, "expected error, got nil")

	// Cobra validates args count first
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// =============================================================================
// Card Shortcut Command Tests
// =============================================================================

// TestCardsListBreadcrumbs tests the cardsListBreadcrumbs helper.
func TestCardsListBreadcrumbs(t *testing.T) {
	breadcrumbs := cardsListBreadcrumbs("123")

	require.Len(t, breadcrumbs, 3)
	assert.Equal(t, "archived", breadcrumbs[2].Action)
	assert.Contains(t, breadcrumbs[2].Cmd, "recordings cards --status archived --in 123")
}

// TestCardsStepDeleteRequiresStepID tests that step ID is required for step delete.
func TestCardsStepDeleteRequiresStepID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newCardsStepDeleteCmd()

	err := executeCommand(cmd, app) // no step ID
	require.NotNil(t, err, "expected error, got nil")

	// Cobra validates args count first
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// =============================================================================
// Cards Move --position Tests
// =============================================================================

// mockCardMoveTransport handles resolver and card table API calls, captures the move POST.
type mockCardMoveTransport struct {
	capturedPath string
	capturedBody []byte
}

func (t *mockCardMoveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/123"):
			body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"}]}`
		case strings.Contains(req.URL.Path, "/card_tables/555"):
			body = `{"id": 555, "lists": [{"id": 777, "title": "Done", "position": 1}]}`
		default:
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" {
		t.capturedPath = req.URL.Path
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		return &http.Response{
			StatusCode: 204,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

// TestCardsMovePositionPayload verifies the CLI sends the intended request contract
// (source_id=card, target_id=column, position) to /card_tables/{id}/moves.json.
// This proves the CLI wiring is correct; it does not prove the BC3 API accepts
// card-as-source on this endpoint — that requires manual/integration validation.
func TestCardsMovePositionPayload(t *testing.T) {
	transport := &mockCardMoveTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := "555"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "Done", "--position", "1")
	require.NoError(t, err)

	// Verify URL path hits the card_tables moves endpoint
	assert.Contains(t, transport.capturedPath, "/card_tables/555/moves.json")

	// Verify payload shape
	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, float64(456), body["source_id"])
	assert.Equal(t, float64(777), body["target_id"])
	assert.Equal(t, float64(1), body["position"])
}

// TestCardsMovePositionPosAlias verifies that --pos triggers the same
// positioned-move contract as --position.
func TestCardsMovePositionPosAlias(t *testing.T) {
	transport := &mockCardMoveTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := "555"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "Done", "--pos", "2")
	require.NoError(t, err)

	assert.Contains(t, transport.capturedPath, "/card_tables/555/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, float64(456), body["source_id"])
	assert.Equal(t, float64(777), body["target_id"])
	assert.Equal(t, float64(2), body["position"])
}

// TestCardsMovePositionNumericToSingleTableAutoResolves verifies that a
// positioned move with numeric --to and no --card-table auto-resolves
// the card table when the project has exactly one.
func TestCardsMovePositionNumericToSingleTableAutoResolves(t *testing.T) {
	transport := &mockCardMoveTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := "" // no --card-table
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777", "--position", "1")
	require.NoError(t, err)

	// Should auto-resolve to card table 555 (single table in mock dock)
	assert.Contains(t, transport.capturedPath, "/card_tables/555/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, float64(456), body["source_id"])
	assert.Equal(t, float64(777), body["target_id"])
	assert.Equal(t, float64(1), body["position"])
}

// TestCardsMoveWithoutPositionUsesCardsMove verifies the old path
// (POST /card_tables/cards/{id}/moves.json with {column_id}) is taken
// when --position is absent.
func TestCardsMoveWithoutPositionUsesCardsMove(t *testing.T) {
	transport := &mockCardMoveTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777")
	require.NoError(t, err)

	// Verify URL path hits the cards move endpoint, not card_tables moves
	assert.Contains(t, transport.capturedPath, "/card_tables/cards/456/moves.json")

	// Verify payload has column_id, not source_id/target_id
	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, float64(777), body["column_id"])
	_, hasSourceID := body["source_id"]
	assert.False(t, hasSourceID, "non-positioned move should not send source_id")
}

// TestCardsMovePositionRejectsNonPositive tests that --position -1 is rejected.
func TestCardsMovePositionRejectsNonPositive(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777", "--position", "-1")
	require.NotNil(t, err)

	var e *output.Error
	if assert.True(t, errors.As(err, &e)) {
		assert.Equal(t, "--position must be a positive integer (1-indexed)", e.Message)
	}
}

// TestCardsMovePositionRejectsZero tests that --position 0 is rejected.
func TestCardsMovePositionRejectsZero(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777", "--position", "0")
	require.NotNil(t, err)

	var e *output.Error
	if assert.True(t, errors.As(err, &e)) {
		assert.Equal(t, "--position must be a positive integer (1-indexed)", e.Message)
	}
}

// mockMultiCardTableTransport returns a project with multiple card tables.
type mockMultiCardTableTransport struct{}

func (t *mockMultiCardTableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/123"):
			body = `{"id": 123, "dock": [` +
				`{"name": "kanban_board", "id": 555, "title": "Board A"},` +
				`{"name": "kanban_board", "id": 666, "title": "Board B"}` +
				`]}`
		default:
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

// TestCardsMovePositionNumericToMultiTableAmbiguous verifies that a positioned move
// with a numeric --to and no --card-table returns an ambiguous error when the project
// has multiple card tables.
func TestCardsMovePositionNumericToMultiTableAmbiguous(t *testing.T) {
	transport := &mockMultiCardTableTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := "" // no --card-table
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to", "777", "--position", "1")
	require.NotNil(t, err)

	var e *output.Error
	if assert.True(t, errors.As(err, &e)) {
		assert.Equal(t, output.CodeAmbiguous, e.Code)
		assert.Equal(t, "Multiple card tables found", e.Message)
		assert.Contains(t, e.Hint, "Specify one with --card-table <id>:")
		assert.Contains(t, e.Hint, "  555  Board A")
		assert.Contains(t, e.Hint, "  666  Board B")
	}
}

func TestGetCardTableIDRejectsPartialNumericExplicitID(t *testing.T) {
	transport := &mockCardMoveTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	_, err := getCardTableID(cmd, app, "123", "555abc")
	require.Error(t, err)

	var e *output.Error
	if assert.True(t, errors.As(err, &e)) {
		assert.Equal(t, "Card table '555abc' not found", e.Message)
	}
}

type mockCardsDoneTransport struct {
	projectDock       string
	initialCard       string
	updatedCard       string
	columns           map[string]string
	tables            map[string]string
	cardGetCount      int
	cardTableGetCount int
	moveCalls         int
	capturedMovePath  string
	capturedMoveBody  []byte
}

func (t *mockCardsDoneTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "POST" && strings.Contains(req.URL.Path, "/moves.json") {
		t.moveCalls++
		t.capturedMovePath = req.URL.Path
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedMoveBody = body
			req.Body.Close()
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
	}

	if req.Method != "GET" {
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}

	switch {
	case strings.HasSuffix(req.URL.Path, "/projects.json"):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"id": 123, "name": "Test Project"}]`)), Header: header}, nil
	case strings.Contains(req.URL.Path, "/projects/123"):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(t.projectDock)), Header: header}, nil
	case strings.Contains(req.URL.Path, "/card_tables/cards/456"):
		t.cardGetCount++
		body := t.initialCard
		if t.cardGetCount > 1 && t.updatedCard != "" {
			body = t.updatedCard
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	case strings.Contains(req.URL.Path, "/card_tables/columns/"):
		for id, body := range t.columns {
			if strings.Contains(req.URL.Path, "/card_tables/columns/"+id) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
			}
		}
	case strings.Contains(req.URL.Path, "/card_tables/"):
		for id, body := range t.tables {
			if strings.Contains(req.URL.Path, "/card_tables/"+id) {
				t.cardTableGetCount++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
			}
		}
	}

	return nil, fmt.Errorf("unexpected GET request: %s", req.URL.Path)
}

func TestCardsDoneMovesCardToDoneColumn(t *testing.T) {
	transport := &mockCardsDoneTransport{
		projectDock: `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"}]}`,
		initialCard: `{"id": 456, "title": "Test Card", "completed": false, "parent": {"id": 777, "title": "Doing", "type": "Kanban::Column"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		updatedCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		tables: map[string]string{
			"555": `{"id": 555, "title": "Board", "lists": [{"id": 777, "title": "Doing", "type": "Kanban::Column"}, {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)
	assert.Equal(t, 1, transport.moveCalls)
	assert.Contains(t, transport.capturedMovePath, "/card_tables/cards/456/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(888), body["column_id"])
}

func TestCardsDoneAcceptsLeadingZeroCardTableID(t *testing.T) {
	transport := &mockCardsDoneTransport{
		projectDock: `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"}]}`,
		initialCard: `{"id": 456, "title": "Test Card", "completed": false, "parent": {"id": 777, "title": "Doing", "type": "Kanban::Column"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		updatedCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		tables: map[string]string{
			"555": `{"id": 555, "title": "Board", "lists": [{"id": 777, "title": "Doing", "type": "Kanban::Column"}, {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := "0555"
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)
	assert.Equal(t, 1, transport.moveCalls)
}

func TestCardsDoneUsesParentColumnToResolveTable(t *testing.T) {
	transport := &mockCardsDoneTransport{
		projectDock: `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board A"}, {"name": "kanban_board", "id": 666, "title": "Board B"}]}`,
		initialCard: `{"id": 456, "title": "Test Card", "completed": false, "parent": {"id": 990, "title": "Doing", "type": "Kanban::Column"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		updatedCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 991, "title": "Done", "type": "Kanban::DoneColumn"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		columns: map[string]string{
			"990": `{"id": 990, "title": "Doing", "type": "Kanban::Column", "parent": {"id": 666, "title": "Board B", "type": "Kanban::Board"}}`,
		},
		tables: map[string]string{
			"555": `{"id": 555, "title": "Board A", "lists": [{"id": 777, "title": "Doing", "type": "Kanban::Column"}, {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
			"666": `{"id": 666, "title": "Board B", "lists": [{"id": 990, "title": "Doing", "type": "Kanban::Column"}, {"id": 991, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(991), body["column_id"])
	assert.Equal(t, 1, transport.cardTableGetCount)
}

func TestCardsDoneUsesOnHoldParentToResolveTable(t *testing.T) {
	transport := &mockCardsDoneTransport{
		projectDock: `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board A"}, {"name": "kanban_board", "id": 666, "title": "Board B"}]}`,
		initialCard: `{"id": 456, "title": "Test Card", "completed": false, "parent": {"id": 1990, "title": "On hold", "type": "Kanban::Column"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		updatedCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 991, "title": "Done", "type": "Kanban::DoneColumn"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		tables: map[string]string{
			"555": `{"id": 555, "title": "Board A", "lists": [{"id": 777, "title": "Doing", "type": "Kanban::Column", "on_hold": {"id": 1777, "title": "On hold"}}, {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
			"666": `{"id": 666, "title": "Board B", "lists": [{"id": 990, "title": "Doing", "type": "Kanban::Column", "on_hold": {"id": 1990, "title": "On hold"}}, {"id": 991, "title": "Done", "type": "Kanban::DoneColumn"}]}`,
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedMoveBody, &body))
	assert.Equal(t, float64(991), body["column_id"])
	assert.Equal(t, 2, transport.cardTableGetCount)
}

func TestCardsDoneAlreadyCompletedSkipsMove(t *testing.T) {
	transport := &mockCardsDoneTransport{
		initialCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 888, "title": "Done", "type": "Kanban::DoneColumn"}}`,
	}
	app, buf := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)
	assert.Equal(t, 0, transport.moveCalls)

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "Card #456 is already in 'Done'", out["summary"])
}

func TestCardsDoneCompletedOutsideDoneUsesAccurateSummary(t *testing.T) {
	transport := &mockCardsDoneTransport{
		initialCard: `{"id": 456, "title": "Test Card", "completed": true, "parent": {"id": 777, "title": "Doing", "type": "Kanban::Column"}}`,
	}
	app, buf := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.NoError(t, err)
	assert.Equal(t, 0, transport.moveCalls)

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "Card #456 is already completed", out["summary"])
}

func TestCardsDoneWithoutDoneColumnErrors(t *testing.T) {
	transport := &mockCardsDoneTransport{
		projectDock: `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"}]}`,
		initialCard: `{"id": 456, "title": "Test Card", "completed": false, "parent": {"id": 777, "title": "Doing", "type": "Kanban::Column"}, "bucket": {"id": 123, "name": "Test Project"}}`,
		tables: map[string]string{
			"555": `{"id": 555, "title": "Board", "lists": [{"id": 777, "title": "Doing", "type": "Kanban::Column"}]}`,
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsDoneCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456")
	require.Error(t, err)

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "Card table 'Board' does not have a Done column", e.Message)
		assert.Contains(t, e.Hint, "basecamp cards columns --card-table 555 --in 123")
	}
}

// =============================================================================
// Dash-separator title tests
// =============================================================================

// TestCardsCreateDashSeparatorTitle verifies that `--` lets a dash-prefixed
// title pass through without being parsed as a flag.
func TestCardsCreateDashSeparatorTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	// ProjectID intentionally empty — --in must be parsed for the command to
	// proceed past project resolution.

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "create", "--in", "123", "--", "--some-title")

	// No-network transport guarantees an error past arg parsing.
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "Project ID required", e.Message)
	}
}

// TestCardsCreateFlagsAfterTitle guards the flags-anywhere behavior:
// flags placed after the positional title must still be parsed.
func TestCardsCreateFlagsAfterTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	// ProjectID intentionally empty — if --in after the title is NOT parsed,
	// the command would fail with "Project ID required".

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "create", "Normal title", "--in", "123")

	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "Project ID required", e.Message)
	}
}

// =============================================================================
// Card Content HTML Conversion Tests
// =============================================================================

// mockCardCreateTransport handles resolver and dock API calls, and captures the POST body.
type mockCardCreateTransport struct {
	capturedBody []byte
	capturedPath string
}

func (t *mockCardCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [{"name": "card_table", "id": 777, "enabled": true}]}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" || req.Method == "PUT" {
		t.capturedPath = req.URL.Path
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "title": "Test", "status": "active"}`
		status := 201
		if req.Method == "PUT" {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func setupCardsMockApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
}

func TestCardsCreateContentIsHTML(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	err := executeCommand(cmd, app, "create", "Title", "**bold** text", "--column", "12345")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "<strong>bold</strong>")
}

func TestCardsUpdateContentIsHTML(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	err := executeCommand(cmd, app, "update", "999", "--body", "**bold** text")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "<strong>bold</strong>")
}

func TestCardsUpdatePreservesHTMLBody(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	err := executeCommand(cmd, app, "update", "999", "--body", "<p>Hello <code>world</code></p>")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Equal(t, "<p>Hello <code>world</code></p>", content)
}

func TestCardsCreateLocalImageErrors(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	// Local image path triggers resolveLocalImages which should error on missing file
	err := executeCommand(cmd, app, "create", "Title", "![alt](./missing.png)", "--column", "12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}

func TestCardsUpdateLocalImageErrors(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	err := executeCommand(cmd, app, "update", "999", "--body", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}

func TestCardsCreateRemoteImagePassesThrough(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardsCmd()
	err := executeCommand(cmd, app, "create", "Title", "![alt](https://example.com/img.png)", "--column", "12345")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, `<img src="https://example.com/img.png"`)
}

// =============================================================================
// Assignee Flag Tests
// =============================================================================

func TestCardsCreateHasAssigneeFlag(t *testing.T) {
	project := ""
	cardTable := ""
	cmd := newCardsCreateCmd(&project, &cardTable)

	flag := cmd.Flags().Lookup("assignee")
	require.NotNil(t, flag, "expected --assignee flag on cards create")

	toFlag := cmd.Flags().Lookup("to")
	require.NotNil(t, toFlag, "expected --to flag on cards create")
}

// mockCardAssignTransport handles resolver API calls with people endpoint,
// card creation, and captures the PUT body for assignment verification.
type mockCardAssignTransport struct {
	capturedPutBody []byte
}

func (t *mockCardAssignTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 789, "title": "Card Table"}]}`
		} else if strings.Contains(req.URL.Path, "/card_tables/") {
			body = `{"id": 789, "lists": [{"id": 111, "title": "Backlog"}]}`
		} else if strings.Contains(req.URL.Path, "/people.json") {
			body = `[{"id": 42, "name": "Annie Bryan"}]`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" {
		mockResp := `{"id": 999, "title": "Test Card", "assignees": []}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	if req.Method == "PUT" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedPutBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "title": "Test Card", "assignees": [{"id": 42, "name": "Annie Bryan"}]}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func TestCardsCreateWithAssigneeSendsUpdate(t *testing.T) {
	transport := &mockCardAssignTransport{}
	app := setupCardsMockApp(t, transport)
	app.Output = output.New(output.Options{
		Format: output.FormatJSON,
		Writer: &bytes.Buffer{},
	})

	project := ""
	cardTable := ""
	cmd := newCardsCreateCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "Test Card", "--assignee", "Annie Bryan")
	require.NoError(t, err)

	require.NotEmpty(t, transport.capturedPutBody, "expected PUT request for assignment")

	var putBody map[string]any
	err = json.Unmarshal(transport.capturedPutBody, &putBody)
	require.NoError(t, err)

	assigneeIDs, ok := putBody["assignee_ids"].([]any)
	require.True(t, ok, "expected assignee_ids array in PUT body")
	require.Len(t, assigneeIDs, 1)
	assert.Equal(t, float64(42), assigneeIDs[0])
}

func TestResolveAssigneeIDRejectsZero(t *testing.T) {
	app, _ := setupTestApp(t)

	_, err := resolveAssigneeID(context.Background(), app, "0")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Assignee ID must be a positive number", e.Message)
}

func TestResolveAssigneeIDRejectsNegative(t *testing.T) {
	app, _ := setupTestApp(t)

	_, err := resolveAssigneeID(context.Background(), app, "-5")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Assignee ID must be a positive number", e.Message)
}

func TestResolveAssigneeIDAcceptsPositive(t *testing.T) {
	app, _ := setupTestApp(t)

	id, err := resolveAssigneeID(context.Background(), app, "42")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

// mockCardColumnTransport serves the endpoints used by the column color/on-hold
// commands. columnType controls the type returned by the column GET (which the
// type guard inspects); getStatus lets a test simulate a missing column. It
// records the method and path of any color/on-hold mutation so tests can assert
// the correct bucket-scoped endpoint was called — or that no mutation happened.
type mockCardColumnTransport struct {
	columnType string
	getStatus  int

	mutateMethod string
	mutatePath   string
}

func (m *mockCardColumnTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}
	column := func(colType string) string {
		return fmt.Sprintf(`{"id":789,"type":%q,"title":"In progress","status":"active",`+
			`"inherits_status":true,"visible_to_clients":false,`+
			`"created_at":"2026-07-03T00:00:00Z","updated_at":"2026-07-03T00:00:00Z",`+
			`"url":"https://example.test/columns/789","app_url":"https://example.test/columns/789",`+
			`"cards_count":0,"comment_count":0}`, colType)
	}

	path := req.URL.Path
	switch {
	case req.Method == "GET" && strings.Contains(path, "/projects.json"):
		return respond(200, `[{"id":123,"name":"Test Project"},{"id":999,"name":"Other Project"}]`)
	case strings.Contains(path, "/on_hold.json"), strings.Contains(path, "/color.json"):
		m.mutateMethod = req.Method
		m.mutatePath = path
		return respond(200, column(standardColumnType))
	case req.Method == "GET" && strings.Contains(path, "/card_tables/columns/"):
		status := m.getStatus
		if status == 0 {
			status = 200
		}
		if status != 200 {
			return respond(status, `{"error":"Not Found"}`)
		}
		return respond(200, column(m.columnType))
	default:
		return respond(404, `{"error":"Not Found"}`)
	}
}

func TestCardsColumnOnHoldStandardColumn(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType}
	app, _ := newTestAppWithTransport(t, tr)

	project := ""
	err := executeCommand(newCardsColumnOnHoldCmd(&project), app, "789")
	require.NoError(t, err)
	assert.Equal(t, "POST", tr.mutateMethod)
	assert.Contains(t, tr.mutatePath, "/buckets/123/card_tables/columns/789/on_hold.json")
}

func TestCardsColumnNoOnHoldStandardColumn(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType}
	app, _ := newTestAppWithTransport(t, tr)

	project := ""
	err := executeCommand(newCardsColumnNoOnHoldCmd(&project), app, "789")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", tr.mutateMethod)
	assert.Contains(t, tr.mutatePath, "/buckets/123/card_tables/columns/789/on_hold.json")
}

func TestCardsColumnColorStandardColumn(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType}
	app, _ := newTestAppWithTransport(t, tr)

	project := ""
	err := executeCommand(newCardsColumnColorCmd(&project), app, "789", "--color", "blue")
	require.NoError(t, err)
	assert.Equal(t, "PUT", tr.mutateMethod)
	assert.Contains(t, tr.mutatePath, "/buckets/123/card_tables/columns/789/color.json")
}

// TestCardsColumnActionsRejectNonStandardColumns verifies the type guard: on
// Triage, Not now, and Done columns the on-hold and color commands return a
// friendly usage error and never call the mutating endpoint (which the API would
// answer with a bare 404).
func TestCardsColumnActionsRejectNonStandardColumns(t *testing.T) {
	for _, colType := range []string{"Kanban::Triage", "Kanban::NotNowColumn", "Kanban::DoneColumn"} {
		t.Run(colType, func(t *testing.T) {
			cases := []struct {
				name string
				cmd  func(*string) *cobra.Command
				args []string
			}{
				{"on-hold", newCardsColumnOnHoldCmd, []string{"789"}},
				{"no-on-hold", newCardsColumnNoOnHoldCmd, []string{"789"}},
				{"color", newCardsColumnColorCmd, []string{"789", "--color", "blue"}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					tr := &mockCardColumnTransport{columnType: colType}
					app, _ := newTestAppWithTransport(t, tr)

					project := ""
					err := executeCommand(tc.cmd(&project), app, tc.args...)
					require.Error(t, err)

					var e *output.Error
					require.True(t, errors.As(err, &e))
					assert.Contains(t, e.Message, "standard columns")
					assert.Empty(t, tr.mutatePath, "mutation endpoint must not be called for a non-standard column")
				})
			}
		})
	}
}

func TestCardsColumnOnHoldColumnNotFound(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType, getStatus: 404}
	app, _ := newTestAppWithTransport(t, tr)

	project := ""
	err := executeCommand(newCardsColumnOnHoldCmd(&project), app, "789")
	require.Error(t, err)
	assert.Empty(t, tr.mutatePath, "mutation must not be attempted when the column lookup fails")
}

// TestCardsColumnColorResolvesProjectFromURL verifies the bucket ID is taken from
// the URL (123) over the configured project (999) and threaded into the endpoint.
func TestCardsColumnColorResolvesProjectFromURL(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType}
	app, _ := newTestAppWithTransport(t, tr)
	app.Config.ProjectID = "999"

	project := ""
	url := "https://3.basecamp.com/99999/buckets/123/card_tables/columns/789"
	err := executeCommand(newCardsColumnColorCmd(&project), app, url, "--color", "blue")
	require.NoError(t, err)
	assert.Contains(t, tr.mutatePath, "/buckets/123/card_tables/columns/789/color.json")
}

// TestCardsColumnColorURLBucketBeatsFlag verifies the URL's bucket (123) wins over
// an explicit --in/--project flag pointing elsewhere (999): the URL identifies the
// column's real bucket, so targeting the flag project would 404.
func TestCardsColumnColorURLBucketBeatsFlag(t *testing.T) {
	tr := &mockCardColumnTransport{columnType: standardColumnType}
	app, _ := newTestAppWithTransport(t, tr)

	project := "999" // explicit --in/--project pointing at a different bucket
	url := "https://3.basecamp.com/99999/buckets/123/card_tables/columns/789"
	err := executeCommand(newCardsColumnColorCmd(&project), app, url, "--color", "blue")
	require.NoError(t, err)
	assert.Contains(t, tr.mutatePath, "/buckets/123/card_tables/columns/789/color.json")
}

// --- Wormholes (cross-project card move, #342) ---

func wormholePtr[T any](v T) *T { return &v }

// mockWormholeTransport serves the dock/card/card-table fixtures the wormhole
// commands need and records the last mutating (POST/PUT/DELETE) request.
//
//   - Card 456 lives in project (bucket) 123, parent column 777, on card table 555.
//   - destinationURL controls the single wormhole's destination column.
//   - linked controls whether that wormhole is linked.
//   - secondTable adds a board 666 (columns [888], no wormholes) so tests can
//     exercise an explicit --card-table that doesn't contain the card.
type mockWormholeTransport struct {
	destinationURL string
	linked         *bool
	secondTable    bool
	cardNoParent   bool // card 456 comes back without a parent column
	method         string
	path           string
	body           []byte
}

func (t *mockWormholeTransport) wormholeJSON() string {
	dest := t.destinationURL
	if dest == "" {
		dest = "https://3.basecampapi.com/99999/buckets/999/card_tables/columns/888.json"
	}
	linked := true
	if t.linked != nil {
		linked = *t.linked
	}
	return fmt.Sprintf(`{"id":111,"status":"active","title":"Anniversary › Card Table › Triage",`+
		`"type":"Kanban::Wormhole","color":null,"linked":%t,"destination_url":"%s",`+
		`"parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`, linked, dest)
}

func (t *mockWormholeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/123"):
			if t.secondTable {
				body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"},{"name": "kanban_board", "id": 666, "title": "Other"}]}`
			} else {
				body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 555, "title": "Board"}]}`
			}
		case strings.Contains(req.URL.Path, "/card_tables/cards/456"):
			if t.cardNoParent {
				body = `{"id": 456, "title": "Test Card", "bucket": {"id": 123, "name": "Test Project", "type": "Project"}}`
			} else {
				body = `{"id": 456, "title": "Test Card", "bucket": {"id": 123, "name": "Test Project", "type": "Project"}, "parent": {"id": 777, "title": "Developing", "type": "Kanban::Column"}}`
			}
		case strings.Contains(req.URL.Path, "/card_tables/555"):
			body = `{"id": 555, "lists": [{"id": 777, "title": "Developing", "position": 1}], "wormholes": [` + t.wormholeJSON() + `]}`
		case strings.Contains(req.URL.Path, "/card_tables/666"):
			// A real wormhole (222) that lives on a sibling board, not the card's table.
			body = `{"id": 666, "lists": [{"id": 888, "title": "Elsewhere", "position": 1}], "wormholes": [{"id":222,"status":"active","title":"Sibling › Board › Col","type":"Kanban::Wormhole","color":null,"linked":true,"destination_url":"https://3.basecampapi.com/99999/buckets/999/card_tables/columns/321.json"}]}`
		default:
			body = `{}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	t.method = req.Method
	t.path = req.URL.Path
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		t.body = b
		req.Body.Close()
	}

	switch req.Method {
	case "POST":
		if strings.Contains(req.URL.Path, "/moves.json") {
			return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(t.wormholeJSON())), Header: header}, nil
	case "PUT":
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(t.wormholeJSON())), Header: header}, nil
	case "DELETE":
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
	}
	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
}

// wormholeMoveError runs `cards move <card> --to-wormhole ...`, asserts it failed
// with an *output.Error, and asserts no mutating request was issued — a rejected
// teleport must never touch the server, since the move is irreversible.
func wormholeMoveError(t *testing.T, transport *mockWormholeTransport, project string, args ...string) *output.Error {
	t.Helper()
	app, _ := newTestAppWithTransport(t, transport)
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)
	err := executeCommand(cmd, app, args...)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Empty(t, transport.method, "a rejected wormhole move must not issue a mutating request")
	return e
}

// TestCardsMoveToWormholeByID verifies a numeric --to-wormhole is still validated
// against the card's own source table (fail-closed) and moved through the
// wormhole's id.
func TestCardsMoveToWormholeByID(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456", "--to-wormhole", "111")
	require.NoError(t, err)

	assert.Equal(t, "POST", transport.method)
	assert.Contains(t, transport.path, "/card_tables/cards/456/moves.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.body, &body))
	assert.Equal(t, float64(111), body["column_id"])

	// Output reports source_id (not id) and status teleporting, and carries no
	// same-id "view card" breadcrumb (that id is about to 404).
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Equal(t, "456", envelope.Data["source_id"])
	assert.Equal(t, "teleporting", envelope.Data["status"])
	assert.NotContains(t, envelope.Data, "id")
	assert.NotContains(t, buf.String(), "cards show 456")
}

// TestCardsMoveToWormholeByDestinationURL verifies a destination-column URL is
// matched against the card's source table wormholes[] and moved through the
// matching wormhole's id (111), not the destination column id (888). The
// breadcrumb pins the resolved --card-table.
func TestCardsMoveToWormholeByDestinationURL(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)

	url := "https://3.basecamp.com/99999/buckets/999/card_tables/columns/888"
	err := executeCommand(cmd, app, "456", "--to-wormhole", url)
	require.NoError(t, err)

	assert.Contains(t, transport.path, "/card_tables/cards/456/moves.json")
	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.body, &body))
	assert.Equal(t, float64(111), body["column_id"])
}

// TestCardsMoveToWormholeSiblingBoardRejected verifies that a real wormhole (222)
// which exists on a sibling board — not the card's own card table — is rejected
// (fail-closed) with no move issued. The server would otherwise honor it, since
// it resolves column_id against the whole project bucket.
func TestCardsMoveToWormholeSiblingBoardRejected(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{secondTable: true}, "", "456", "--to-wormhole", "222")
	assert.Contains(t, e.Message, "Wormhole 222 is not on this card's card table")
	assert.Contains(t, e.Hint, "#111")
}

// TestCardsMoveToWormholeUnlinkedRejected verifies an unlinked wormhole is
// rejected before any move.
func TestCardsMoveToWormholeUnlinkedRejected(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{linked: wormholePtr(false)}, "", "456", "--to-wormhole", "111")
	assert.Contains(t, e.Message, "unlinked")
	// The fix-it hint must be copy/pasteable — include --in with the source project.
	assert.Contains(t, e.Hint, "--in 123")
}

// TestCardsMoveToWormholeNoMatch verifies a destination-column URL that no
// wormhole targets errors with a hint listing the reachable wormholes.
func TestCardsMoveToWormholeNoMatch(t *testing.T) {
	transport := &mockWormholeTransport{destinationURL: "https://3.basecampapi.com/99999/buckets/999/card_tables/columns/222.json"}
	url := "https://3.basecamp.com/99999/buckets/999/card_tables/columns/888"
	e := wormholeMoveError(t, transport, "", "456", "--to-wormhole", url)
	assert.Contains(t, e.Message, "column 888")
	assert.Contains(t, e.Hint, "#111")
}

// TestCardsMoveToWormholeProjectConflict verifies an explicit --in that
// contradicts the card's own project is rejected.
func TestCardsMoveToWormholeProjectConflict(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{}, "999", "456", "--to-wormhole", "111")
	assert.Contains(t, e.Message, "points at project 999")
}

// TestCardsMoveToWormholeTableConflict verifies an explicit --card-table that
// doesn't contain the card is rejected.
func TestCardsMoveToWormholeTableConflict(t *testing.T) {
	transport := &mockWormholeTransport{secondTable: true}
	app, _ := newTestAppWithTransport(t, transport)
	project := ""
	cardTable := "666"
	cmd := newCardsMoveCmd(&project, &cardTable)
	err := executeCommand(cmd, app, "456", "--to-wormhole", "111")
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "not on the specified card table")
	assert.Empty(t, transport.method, "a rejected wormhole move must not issue a mutating request")
}

// TestCardsMoveToWormholeRejectsNonColumnURL verifies a non-column Basecamp URL
// is rejected rather than having its trailing id accepted.
func TestCardsMoveToWormholeRejectsNonColumnURL(t *testing.T) {
	cardURL := "https://3.basecamp.com/99999/buckets/999/card_tables/cards/456"
	e := wormholeMoveError(t, &mockWormholeTransport{}, "", "456", "--to-wormhole", cardURL)
	assert.Contains(t, e.Message, "Invalid destination column")
}

// TestCardsMoveToWormholeRejectsNonPositiveID verifies a zero/negative wormhole
// id is rejected.
func TestCardsMoveToWormholeRejectsNonPositiveID(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{}, "", "456", "--to-wormhole", "0")
	assert.Contains(t, e.Message, "positive wormhole ID")
}

// TestCardsMoveToWormholeMutuallyExclusive verifies --to-wormhole rejects each of
// --to, --on-hold, and --position.
func TestCardsMoveToWormholeMutuallyExclusive(t *testing.T) {
	for _, extra := range [][]string{
		{"--to", "Done"},
		{"--on-hold"},
		{"--position", "2"},
	} {
		args := append([]string{"456", "--to-wormhole", "111"}, extra...)
		e := wormholeMoveError(t, &mockWormholeTransport{}, "", args...)
		assert.Contains(t, e.Message, "--to-wormhole cannot be combined")
	}
}

// TestCardsWormholesList verifies list reads wormholes[] from the card table.
func TestCardsWormholesList(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	err := executeCommand(newCardsWormholesListCmd(&project, &cardTable), app)
	require.NoError(t, err)

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	assert.Equal(t, float64(111), envelope.Data[0]["id"])
	assert.Contains(t, buf.String(), "1 wormholes")
}

// TestCardsWormholesCreate verifies create POSTs destination_recording_id to the
// board-scoped wormholes endpoint.
func TestCardsWormholesCreate(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	err := executeCommand(newCardsWormholesCreateCmd(&project, &cardTable), app, "--to-column", "888")
	require.NoError(t, err)

	assert.Equal(t, "POST", transport.method)
	assert.Contains(t, transport.path, "/buckets/123/card_tables/555/wormholes.json")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.body, &body))
	assert.Equal(t, float64(888), body["destination_recording_id"])
}

// TestCardsWormholesCreateFromColumnURL verifies --to-column accepts a column URL.
func TestCardsWormholesCreateFromColumnURL(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	url := "https://3.basecamp.com/99999/buckets/999/card_tables/columns/888"
	err := executeCommand(newCardsWormholesCreateCmd(&project, &cardTable), app, "--to-column", url)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.body, &body))
	assert.Equal(t, float64(888), body["destination_recording_id"])
}

// TestCardsWormholesCreateRejectsNonColumnURL verifies create rejects a
// non-column Basecamp URL for --to-column.
func TestCardsWormholesCreateRejectsNonColumnURL(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	cardTable := ""
	cardURL := "https://3.basecamp.com/99999/buckets/999/card_tables/cards/456"
	err := executeCommand(newCardsWormholesCreateCmd(&project, &cardTable), app, "--to-column", cardURL)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "Invalid destination column")
}

// TestCardsWormholesUpdate verifies update PUTs to the wormhole-scoped endpoint.
func TestCardsWormholesUpdate(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	err := executeCommand(newCardsWormholesUpdateCmd(&project), app, "111", "--to-column", "888")
	require.NoError(t, err)

	assert.Equal(t, "PUT", transport.method)
	// NB: the merged SDK's generated Update/Delete routes omit the .json suffix
	// that create and the bc-api docs use; assert the path the SDK actually issues.
	assert.Contains(t, transport.path, "/buckets/123/card_tables/wormholes/111")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.body, &body))
	assert.Equal(t, float64(888), body["destination_recording_id"])
}

// TestCardsWormholesDelete verifies delete DELETEs the wormhole-scoped endpoint.
func TestCardsWormholesDelete(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	project := ""
	err := executeCommand(newCardsWormholesDeleteCmd(&project), app, "111")
	require.NoError(t, err)

	assert.Equal(t, "DELETE", transport.method)
	assert.Contains(t, transport.path, "/buckets/123/card_tables/wormholes/111")
}

// TestFindWormholeByDestinationColumn verifies destination-column matching skips
// unlinked wormholes and matches by the destination_url's column id.
func TestFindWormholeByDestinationColumn(t *testing.T) {
	wormholes := []basecamp.Wormhole{
		{ID: 100, DestinationURL: nil}, // unlinked — never matches
		{ID: 111, DestinationURL: wormholePtr("https://3.basecampapi.com/99999/buckets/999/card_tables/columns/888.json")},
	}

	assert.Equal(t, int64(111), findWormholeByDestinationColumn(wormholes, 888).ID)
	assert.Nil(t, findWormholeByDestinationColumn(wormholes, 999))
}

// TestWormholeMoveBreadcrumbs verifies the move breadcrumb pins the resolved
// --card-table (multi-table safety) and offers no same-id view action.
func TestWormholeMoveBreadcrumbs(t *testing.T) {
	crumbs := wormholeMoveBreadcrumbs("123", "555")
	require.Len(t, crumbs, 1)
	assert.Contains(t, crumbs[0].Cmd, "--in 123")
	assert.Contains(t, crumbs[0].Cmd, "--card-table 555")
	assert.NotContains(t, crumbs[0].Cmd, "cards show")
}

// TestWormholeListBreadcrumb verifies the list follow-up pins --card-table from
// the wormhole's parent, and omits it when the parent is unknown.
func TestWormholeListBreadcrumb(t *testing.T) {
	withParent := wormholeListBreadcrumb(123, &basecamp.Wormhole{ID: 111, Parent: &basecamp.Parent{ID: 555}})
	assert.Contains(t, withParent.Cmd, "--in 123")
	assert.Contains(t, withParent.Cmd, "--card-table 555")

	noParent := wormholeListBreadcrumb(123, &basecamp.Wormhole{ID: 111})
	assert.Contains(t, noParent.Cmd, "--in 123")
	assert.NotContains(t, noParent.Cmd, "--card-table")
}

// TestParseColumnID covers numeric IDs, column URLs, and rejections.
func TestParseColumnID(t *testing.T) {
	id, err := parseColumnID("888")
	require.NoError(t, err)
	assert.Equal(t, int64(888), id)

	id, err = parseColumnID("https://3.basecamp.com/99999/buckets/999/card_tables/columns/888")
	require.NoError(t, err)
	assert.Equal(t, int64(888), id)

	_, err = parseColumnID("0")
	require.Error(t, err)

	_, err = parseColumnID("-4")
	require.Error(t, err)

	// A card URL is not a column URL — reject rather than accept its trailing id.
	_, err = parseColumnID("https://3.basecamp.com/99999/buckets/999/card_tables/cards/456")
	require.Error(t, err)
}

// TestCardsMoveToWormholeRootProjectConflict verifies a root-level --project
// (which lands in app.Flags.Project, not the card command's own flag) that
// contradicts the card's project is still rejected before the destructive move.
func TestCardsMoveToWormholeRootProjectConflict(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Flags.Project = "999" // e.g. `basecamp --project 999 cards move …`

	project := ""
	cardTable := ""
	cmd := newCardsMoveCmd(&project, &cardTable)
	err := executeCommand(cmd, app, "456", "--to-wormhole", "111")

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "points at project 999")
	assert.Empty(t, transport.method, "a rejected wormhole move must not issue a mutating request")
}

// TestCardsMoveToWormholeEmptyValue verifies --to-wormhole= (present but empty)
// errors explicitly instead of falling back to the normal move path.
func TestCardsMoveToWormholeEmptyValue(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{}, "", "456", "--to-wormhole=")
	assert.Contains(t, e.Message, "requires a wormhole ID or destination-column URL")
}

// TestCardsWormholesUpdateRejectsNonPositiveID verifies update rejects id <= 0.
func TestCardsWormholesUpdateRejectsNonPositiveID(t *testing.T) {
	app, _ := newTestAppWithTransport(t, &mockWormholeTransport{})
	project := ""
	err := executeCommand(newCardsWormholesUpdateCmd(&project), app, "0", "--to-column", "888")
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "Invalid wormhole ID")
}

// TestCardsWormholesDeleteRejectsNonPositiveID verifies delete rejects id <= 0.
func TestCardsWormholesDeleteRejectsNonPositiveID(t *testing.T) {
	app, _ := newTestAppWithTransport(t, &mockWormholeTransport{})
	project := ""
	err := executeCommand(newCardsWormholesDeleteCmd(&project), app, "0")
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "Invalid wormhole ID")
}

// TestCardsMoveToWormholeUnverifiableTableRejected verifies that when the card's
// parent column is unavailable (so its table placement can't be confirmed) the
// teleport fails closed at the command level with no mutation.
func TestCardsMoveToWormholeUnverifiableTableRejected(t *testing.T) {
	e := wormholeMoveError(t, &mockWormholeTransport{cardNoParent: true}, "", "456", "--to-wormhole", "111")
	assert.Contains(t, e.Message, "Could not verify which card table")
}

// TestCardsMoveToWormholeURLProjectConflict verifies a project encoded in the
// card URL that contradicts the card's own project is rejected (urlProjectID
// path, distinct from the --in flag and root --project paths).
func TestCardsMoveToWormholeURLProjectConflict(t *testing.T) {
	cardURL := "https://3.basecamp.com/99999/buckets/777/card_tables/cards/456"
	e := wormholeMoveError(t, &mockWormholeTransport{}, "", cardURL, "--to-wormhole", "111")
	assert.Contains(t, e.Message, "the card URL")
	assert.Contains(t, e.Message, "777")
}

// TestCardsWormholesUpdateBreadcrumbPinsCardTable verifies the follow-up
// breadcrumb emitted by the update command (not just the helper) pins
// --card-table from the wormhole's parent, so command wiring can't regress
// unnoticed. Flags.Hints keeps breadcrumbs in the envelope (app.OK strips them
// otherwise).
func TestCardsWormholesUpdateBreadcrumbPinsCardTable(t *testing.T) {
	transport := &mockWormholeTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true

	project := ""
	err := executeCommand(newCardsWormholesUpdateCmd(&project), app, "111", "--to-column", "888")
	require.NoError(t, err)

	var envelope struct {
		Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Breadcrumbs, 1)
	assert.Contains(t, envelope.Breadcrumbs[0].Cmd, "cards wormholes list --in 123 --card-table 555")
}

// --- account-wide card listings ---

// cardsGroupsFixture is a two-project grouped response, two cards each.
const cardsGroupsFixture = `[
	{"bucket":{"id":1,"name":"Alpha","type":"Project"},
	 "cards":[{"id":11,"title":"Alpha one","status":"active","due_on":"2026-01-01"},
	          {"id":12,"title":"Alpha two","status":"active"}]},
	{"bucket":{"id":2,"name":"Beta","type":"Project"},
	 "cards":[{"id":21,"title":"Beta one","status":"active"},
	          {"id":22,"title":"Beta two","status":"active","due_on":"2026-03-01"}]}
]`

// cardsOverdueFixture is the flat, unpaginated overdue payload.
const cardsOverdueFixture = `[
	{"id":31,"title":"Zeta late","status":"active","due_on":"2025-02-01"},
	{"id":32,"title":"Alpha late","status":"active","due_on":"2025-01-01"}
]`

func cardsAggregateRoute(name, body string) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   fmt.Sprintf("/99999/cards/%s.json", name),
		status: http.StatusOK,
		body:   body,
		// The whole fixture is page 1; page 2 is where the listing runs out.
		// Without a page-aware route the bounded walk would never see an empty
		// page and would keep asking until it hit the cap.
		pages: []string{body},
	}
}

// cardsAggregatePath is the path cardsAggregateRoute serves, for tests that
// assert on the request sequence.
func cardsAggregatePath(name string) string {
	return fmt.Sprintf("/99999/cards/%s.json", name)
}

func cardsOpenRoute() stubRoute { return cardsAggregateRoute("open", cardsGroupsFixture) }

// cardsAllAggregateRoutes serves every account-wide card endpoint, so a test
// that dispatches to the wrong one fails on the recorded path rather than on a
// missing stub.
func cardsAllAggregateRoutes() []stubRoute {
	return []stubRoute{
		cardsAggregateRoute("open", cardsGroupsFixture),
		cardsAggregateRoute("completed", cardsGroupsFixture),
		cardsAggregateRoute("unassigned", cardsGroupsFixture),
		cardsAggregateRoute("no_due_date", cardsGroupsFixture),
		cardsAggregateRoute("not_now", cardsGroupsFixture),
		cardsAggregateRoute("overdue", cardsOverdueFixture),
	}
}

// setupCardsAccountWideApp wires the recording harness to an output writer the
// test can read back.
func setupCardsAccountWideApp(t *testing.T, format output.Format, routes ...stubRoute) (*appctx.App, *recordingTransport, *bytes.Buffer) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: format, Writer: buf})
	return app, transport, buf
}

// cardsUsageError asserts the command failed with a usage error and returns it.
func cardsUsageError(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	return e
}

// TestCardsListProjectScopedUnchanged pins that a project in scope still lists
// through the project-scoped column endpoint.
func TestCardsListProjectScopedUnchanged(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{method: http.MethodGet, path: "/99999/card_tables/lists/12345/cards.json", status: http.StatusOK, body: `[]`},
	)

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--project", "123", "--column", "12345"))
	assert.Equal(t, "/99999/card_tables/lists/12345/cards.json", transport.last(t).Path)
}

// TestCardsListAllProjectsOverridesConfiguredProject tests that --all-projects
// beats an ambient configured project rather than conflicting with it.
func TestCardsListAllProjectsOverridesConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, cardsOpenRoute())
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects"))
	assert.Equal(t, "/99999/cards/open.json", transport.last(t).Path)
}

// TestCardsListAllProjectsConflictsWithExplicitProject tests the conflict for
// both the after-the-noun and root-level spellings of an explicit project.
func TestCardsListAllProjectsConflictsWithExplicitProject(t *testing.T) {
	for _, spelling := range []string{"--project", "--in"} {
		app, _ := setupRecordingTestApp(t)
		err := executeRecordingCommand(NewCardsCmd(), app, "list", spelling, "123", "--all-projects")
		assert.Equal(t, "--all-projects cannot be combined with --project", cardsUsageError(t, err).Message, spelling)
	}

	app, _ := setupRecordingTestApp(t)
	app.Flags.Project = "123" // basecamp --project 123 cards list
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects")
	assert.Equal(t, "--all-projects cannot be combined with --project", cardsUsageError(t, err).Message)
}

// TestCardsListAccountWideSelectorEndpoints tests that each selector flag
// reaches its own endpoint.
func TestCardsListAccountWideSelectorEndpoints(t *testing.T) {
	cases := []struct {
		args []string
		path string
	}{
		{[]string{"--all-projects"}, "/99999/cards/open.json"},
		{[]string{"--all-projects", "--status", "completed"}, "/99999/cards/completed.json"},
		{[]string{"--all-projects", "--unassigned"}, "/99999/cards/unassigned.json"},
		{[]string{"--all-projects", "--no-due-date"}, "/99999/cards/no_due_date.json"},
		{[]string{"--all-projects", "--not-now"}, "/99999/cards/not_now.json"},
		{[]string{"--all-projects", "--overdue"}, "/99999/cards/overdue.json"},
	}
	for _, tc := range cases {
		app, transport := setupRecordingTestApp(t, cardsAllAggregateRoutes()...)
		require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, append([]string{"list"}, tc.args...)...), tc.args)
		assert.Equal(t, tc.path, transport.last(t).Path, tc.args)
	}
}

// TestCardsListSelectorsRejectedWithProject tests that the account-wide-only
// selectors are a usage error whenever a project is in scope — explicit,
// root-level, or configured.
func TestCardsListSelectorsRejectedWithProject(t *testing.T) {
	selectors := [][]string{
		{"--status", "completed"},
		{"--unassigned"},
		{"--no-due-date"},
		{"--not-now"},
		{"--overdue"},
	}
	for _, selector := range selectors {
		// Explicit --project after the group noun.
		app, _ := setupRecordingTestApp(t, projectsRoute())
		err := executeRecordingCommand(NewCardsCmd(), app, append([]string{"list", "--project", "123"}, selector...)...)
		assert.Contains(t, cardsUsageError(t, err).Message, "cannot be combined with a project", selector)

		// Root-level --project, which lands in app.Flags.Project.
		app, _ = setupRecordingTestApp(t, projectsRoute())
		app.Flags.Project = "123"
		err = executeRecordingCommand(NewCardsCmd(), app, append([]string{"list"}, selector...)...)
		assert.Contains(t, cardsUsageError(t, err).Message, "cannot be combined with a project", selector)

		// Configured project.
		app, _ = setupRecordingTestApp(t, projectsRoute())
		app.Config.ProjectID = "123"
		err = executeRecordingCommand(NewCardsCmd(), app, append([]string{"list"}, selector...)...)
		assert.Contains(t, cardsUsageError(t, err).Message, "cannot be combined with a project", selector)
	}
}

// TestCardsListSelectorsMutuallyExclusive tests that two selectors name the
// conflicting pair rather than silently picking one.
func TestCardsListSelectorsMutuallyExclusive(t *testing.T) {
	app, _ := setupRecordingTestApp(t)
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--unassigned", "--overdue")
	msg := cardsUsageError(t, err).Message
	assert.Contains(t, msg, "--unassigned")
	assert.Contains(t, msg, "--overdue")
	assert.Contains(t, msg, "mutually exclusive")
}

// TestCardsListStatusRejectsUnsupportedValue tests that only completed is a
// recognized --status.
func TestCardsListStatusRejectsUnsupportedValue(t *testing.T) {
	app, _ := setupRecordingTestApp(t)
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--status", "archived")
	assert.Contains(t, cardsUsageError(t, err).Message, "--status archived is not a card listing")
}

// TestCardsListAccountWideRejectsScopeChildren tests that --column and
// --card-table are rejected account-wide, including via the shorthand and the
// group's persistent flag.
func TestCardsListAccountWideRejectsScopeChildren(t *testing.T) {
	cases := []struct {
		args    []string
		message string
	}{
		{[]string{"list", "--all-projects", "--column", "9"}, "--column names a column"},
		{[]string{"list", "--all-projects", "-c", "9"}, "--column names a column"},
		{[]string{"list", "--all-projects", "--card-table", "7"}, "--card-table names one project's"},
		{[]string{"--card-table", "7", "list", "--all-projects"}, "--card-table names one project's"},
		{[]string{"list", "--column", "9"}, "--column names a column"},
	}
	for _, tc := range cases {
		app, _ := setupRecordingTestApp(t)
		err := executeRecordingCommand(NewCardsCmd(), app, tc.args...)
		assert.Contains(t, cardsUsageError(t, err).Message, tc.message, tc.args)
	}
}

// TestCardsListAccountWidePagination pins the request sequence, not just the
// last call. The old default asked for page 0 — one request that made the
// server walk the whole account — and --limit did the same before throwing most
// of the result away. Both now walk positive pages and stop as soon as they
// can, so the sequence is the behavior under test.
func TestCardsListAccountWidePagination(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		queries []string
	}{
		{
			"the default walks positive pages until the listing runs out",
			[]string{"--all-projects"},
			[]string{"page=1", "page=2"},
		},
		{
			"--all is the only spelling that asks for the full crawl",
			[]string{"--all-projects", "--all"},
			[]string{""},
		},
		{
			"--page N asks for exactly N",
			[]string{"--all-projects", "--page", "3"},
			[]string{"page=3"},
		},
		{
			"--limit stops at the first page that satisfies it",
			[]string{"--all-projects", "--limit", "2"},
			[]string{"page=1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, cardsOpenRoute())
			require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, append([]string{"list"}, tc.args...)...))
			assert.Equal(t, tc.queries, transport.queriesFor(cardsAggregatePath("open")))
		})
	}
}

// TestCardsListAccountWideRejectsBadPaging tests that the page and limit values
// with no account-wide meaning are usage errors rather than surprises.
func TestCardsListAccountWideRejectsBadPaging(t *testing.T) {
	cases := []struct {
		args    []string
		message string
	}{
		{[]string{"--all-projects", "--page", "0"}, "--page 0 is not a page number"},
		{[]string{"--all-projects", "--page", "-1"}, "--page cannot be negative"},
		{[]string{"--all-projects", "--limit", "-1"}, "--limit cannot be negative"},
		{[]string{"--all-projects", "--all", "--limit", "2"}, "--all and --limit are mutually exclusive"},
		{[]string{"--all-projects", "--page", "2", "--limit", "2"}, "--page cannot be combined with --all or --limit"},
	}
	for _, tc := range cases {
		app, _ := setupRecordingTestApp(t, cardsOpenRoute())
		err := executeRecordingCommand(NewCardsCmd(), app, append([]string{"list"}, tc.args...)...)
		assert.Equal(t, tc.message, cardsUsageError(t, err).Message, tc.args)
	}
}

// TestCardsListOverdueRejectsPage tests that --page is refused rather than
// accepted and dropped: there is no page to address on an unpaginated endpoint.
func TestCardsListOverdueRejectsPage(t *testing.T) {
	app, _ := setupRecordingTestApp(t, cardsAllAggregateRoutes()...)
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--overdue", "--page", "1")
	assert.Contains(t, cardsUsageError(t, err).Message, "--page is not supported with --overdue")
}

// --all is a different question from --page. The overdue listing is capped at
// 100 by default, so rejecting --all as well would leave card 101 unreachable —
// capped with no escape hatch is the defect, not the fix. The endpoint is
// unpaginated, so honoring --all costs nothing: the complete array is already
// in hand.
func TestCardsListOverdueAcceptsAll(t *testing.T) {
	app, transport, buf := setupCardsAccountWideApp(t, output.FormatJSON, cardsAllAggregateRoutes()...)
	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--overdue", "--all"))

	var envelope struct {
		Data   []basecamp.Card `json:"data"`
		Notice string          `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Len(t, envelope.Data, 2, "--all returns the complete array")
	assert.Empty(t, envelope.Notice, "nothing was withheld, so there is nothing to warn about")
	assert.Len(t, transport.queriesFor(cardsAggregatePath("overdue")), 1,
		"--all costs no extra request on an unpaginated endpoint")
}

// TestCardsListAccountWideSorting tests that sorting is rejected for the
// grouped aggregates and honored for the flat overdue list.
func TestCardsListAccountWideSorting(t *testing.T) {
	app, _ := setupRecordingTestApp(t, cardsOpenRoute())
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--sort", "due")
	assert.Contains(t, cardsUsageError(t, err).Message, "--sort is not supported for grouped")

	app, _ = setupRecordingTestApp(t, cardsAllAggregateRoutes()...)
	err = executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--overdue", "--sort", "position")
	assert.Contains(t, cardsUsageError(t, err).Message, "--sort position requires --column")

	// Sorting precedes truncation: the earliest-titled card survives --limit 1.
	app, _, buf := setupCardsAccountWideApp(t, output.FormatJSON, cardsAllAggregateRoutes()...)
	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app,
		"list", "--all-projects", "--overdue", "--sort", "title", "--limit", "1"))

	var envelope struct {
		Data   []basecamp.Card `json:"data"`
		Notice string          `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	assert.Equal(t, "Alpha late", envelope.Data[0].Title)
	assert.Contains(t, envelope.Notice, "first 1 of 2")
}

// TestCardsListReverseRequiresSort tests that --reverse alone is an error
// rather than a no-op.
func TestCardsListReverseRequiresSort(t *testing.T) {
	app, _ := setupRecordingTestApp(t, cardsOpenRoute())
	err := executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--reverse")
	assert.Equal(t, "--reverse requires --sort", cardsUsageError(t, err).Message)
}

// TestCardsListAccountWideLimitCountsCards tests that --limit truncates inner
// cards, keeping the groups that hold them, rather than dropping projects.
func TestCardsListAccountWideLimitCountsCards(t *testing.T) {
	app, _, buf := setupCardsAccountWideApp(t, output.FormatJSON, cardsOpenRoute())
	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--limit", "3"))

	var envelope struct {
		Data    []basecamp.BucketCardsGroup `json:"data"`
		Summary string                      `json:"summary"`
		Notice  string                      `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2, "both projects survive a card-counted limit")
	assert.Equal(t, 3, countAccountWideCards(envelope.Data))
	assert.Equal(t, "Alpha", envelope.Data[0].Bucket.Name)
	assert.Len(t, envelope.Data[1].Cards, 1)
	assert.Contains(t, envelope.Summary, "3 open cards across 2 projects")
	// The walk stops at the cap, so the account-wide total is unknown by
	// construction — claiming "of 4" would be reporting the size of the one
	// page that happened to be fetched.
	assert.Contains(t, envelope.Notice, "Showing the first 3 cards; more may exist")
}

// TestCardsListAccountWideOutputBranches tests that machine formats keep the
// grouping and styled output gets flattened rows.
func TestCardsListAccountWideOutputBranches(t *testing.T) {
	app, _, buf := setupCardsAccountWideApp(t, output.FormatJSON, cardsOpenRoute())
	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects"))

	var envelope struct {
		Data []basecamp.BucketCardsGroup `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2)
	assert.Equal(t, 4, countAccountWideCards(envelope.Data))

	app, _, styled := setupCardsAccountWideApp(t, output.FormatStyled, cardsOpenRoute())
	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects"))
	rendered := styled.String()
	assert.Contains(t, rendered, "Alpha")
	assert.Contains(t, rendered, "Beta")
	assert.Contains(t, rendered, "Alpha one")
	assert.NotContains(t, rendered, "bucket")
}

// TestFlattenAccountWideCards tests that flattening carries the project name
// alongside each card.
func TestFlattenAccountWideCards(t *testing.T) {
	groups := []basecamp.BucketCardsGroup{
		{Bucket: basecamp.Bucket{ID: 1, Name: "Alpha"}, Cards: []basecamp.Card{{ID: 11, Title: "One", Status: "active", DueOn: "2026-01-01"}}},
		{Bucket: basecamp.Bucket{ID: 2, Name: "Beta"}},
	}

	rows := flattenAccountWideCards(groups)
	require.Len(t, rows, 1)
	assert.Equal(t, map[string]any{
		"id": int64(11), "title": "One", "project": "Alpha", "status": "active", "due": "2026-01-01",
	}, rows[0])
}

// TestTruncateAccountWideCards tests that truncation trims from the tail and
// never keeps more cards than asked for.
func TestTruncateAccountWideCards(t *testing.T) {
	groups := []basecamp.BucketCardsGroup{
		{Bucket: basecamp.Bucket{Name: "Alpha"}, Cards: []basecamp.Card{{ID: 1}, {ID: 2}}},
		{Bucket: basecamp.Bucket{Name: "Beta"}, Cards: []basecamp.Card{{ID: 3}, {ID: 4}}},
	}

	assert.Equal(t, 1, countAccountWideCards(truncateAccountWideCards(groups, 1)))
	assert.Len(t, truncateAccountWideCards(groups, 1), 1)
	assert.Len(t, truncateAccountWideCards(groups, 2), 1)
	assert.Len(t, truncateAccountWideCards(groups, 3), 2)
	assert.Equal(t, 4, countAccountWideCards(truncateAccountWideCards(groups, 9)))
}
