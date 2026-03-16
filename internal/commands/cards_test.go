package commands

import (
	"bytes"
	"context"
	"encoding/json"
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
	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &testTokenProvider{},
		basecamp.WithTransport(noNetworkTransport{}),
		basecamp.WithMaxRetries(0), // Disable retries for instant failure
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

	cmd := newCardsColumnColorCmd()

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
func TestCardsCmdRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "list")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "Project ID required", e.Message)
	}
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

// TestCardShortcutShowsHelpWithoutTitle tests that help is shown when --title is missing.
func TestCardShortcutShowsHelpWithoutTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardCmd()

	// No --title flag — shows help
	err := executeCommand(cmd, app)
	assert.NoError(t, err)
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
		cardTables []struct {
			ID    int64
			Title string
		}
		expected string
	}{
		{
			name: "single with title",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 123, Title: "Sprint Board"},
			},
			expected: "[123 (Sprint Board)]",
		},
		{
			name: "single without title",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 456, Title: ""},
			},
			expected: "[456]",
		},
		{
			name: "multiple with titles",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 123, Title: "Sprint Board"},
				{ID: 456, Title: "Backlog"},
			},
			expected: "[123 (Sprint Board) 456 (Backlog)]",
		},
		{
			name: "mixed titles",
			cardTables: []struct {
				ID    int64
				Title string
			}{
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

// TestCardShortcutRequiresProject tests that project is required for card shortcut.
func TestCardShortcutRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	cmd := NewCardCmd()

	err := executeCommand(cmd, app, "TestCard")
	require.NotNil(t, err, "expected error, got nil")

	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, "Project ID required", e.Message)
	}
}

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

// TestCardShortcutDashSeparatorTitle verifies the same for the card shortcut.
func TestCardShortcutDashSeparatorTitle(t *testing.T) {
	app, _ := setupTestApp(t)

	cmd := NewCardCmd()

	err := executeCommand(cmd, app, "--in", "123", "--", "--some-title")

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

// TestCardShortcutFlagsAfterTitle guards the same for the card shortcut.
func TestCardShortcutFlagsAfterTitle(t *testing.T) {
	app, _ := setupTestApp(t)

	cmd := NewCardCmd()

	err := executeCommand(cmd, app, "Normal title", "--in", "123")

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

	sdkCfg := &basecamp.Config{}
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

func TestCardShortcutContentIsHTML(t *testing.T) {
	transport := &mockCardCreateTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewCardCmd()
	err := executeCommand(cmd, app, "Title", "**bold** text", "--column", "12345")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "<strong>bold</strong>")
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

func TestCardShortcutHasAssigneeFlag(t *testing.T) {
	cmd := NewCardCmd()

	flag := cmd.Flags().Lookup("assignee")
	require.NotNil(t, flag, "expected --assignee flag on card shortcut")

	toFlag := cmd.Flags().Lookup("to")
	require.NotNil(t, toFlag, "expected --to flag on card shortcut")
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
