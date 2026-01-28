package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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
			if result != tt.expected {
				t.Errorf("isNumericID(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// setupTestApp creates a minimal test app context with a mock output writer.
// The app has a configured account but no project (unless project is set in config).
func setupTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Disable keyring access during tests
	t.Setenv("BCQ_NO_KEYRING", "1")

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
func TestCardsColumnColorRequiresColor(t *testing.T) {
	app, _ := setupTestApp(t)

	// Configure app with project
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsColumnColorCmd(&project)

	err := executeCommand(cmd, app, "456") // column ID but no --color
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Check error type
	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--color is required" {
			t.Errorf("expected '--color is required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepsRequiresCardID tests that card ID is required for steps command.
func TestCardsStepsRequiresCardID(t *testing.T) {
	app, _ := setupTestApp(t)

	// Configure app with project
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepsCmd(&project)

	err := executeCommand(cmd, app) // no card ID
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Check error type
	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "Card ID required (bcq cards steps <card_id>)" {
			t.Errorf("expected 'Card ID required (bcq cards steps <card_id>)', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepCreateRequiresTitle tests that --title is required for step create.
func TestCardsStepCreateRequiresTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepCreateCmd(&project)

	// Only card flag, no title
	err := executeCommand(cmd, app, "--card", "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags first
	errStr := err.Error()
	if errStr != `required flag(s) "title" not set` {
		t.Errorf("expected required flag error for title, got %q", errStr)
	}
}

// TestCardsStepCreateRequiresCard tests that --card is required for step create.
func TestCardsStepCreateRequiresCard(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepCreateCmd(&project)

	// Only title flag, no card
	err := executeCommand(cmd, app, "--title", "My step")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags first
	errStr := err.Error()
	if errStr != `required flag(s) "card" not set` {
		t.Errorf("expected required flag error for card, got %q", errStr)
	}
}

// TestCardsStepUpdateRequiresFields tests that at least one field is required for step update.
func TestCardsStepUpdateRequiresFields(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepUpdateCmd(&project)

	err := executeCommand(cmd, app, "456") // step ID but no update fields
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No update fields provided" {
			t.Errorf("expected 'No update fields provided', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepMoveRequiresCard tests that --card is required for step move.
func TestCardsStepMoveRequiresCard(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepMoveCmd(&project)

	// Step ID and position but no card
	err := executeCommand(cmd, app, "456", "--position", "1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--card is required" {
			t.Errorf("expected '--card is required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepMoveRequiresPosition tests that --position is required for step move.
func TestCardsStepMoveRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepMoveCmd(&project)

	// Step ID and card but no position
	err := executeCommand(cmd, app, "456", "--card", "789")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--position is required (0-indexed)" {
			t.Errorf("expected '--position is required (0-indexed)', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsCmdRequiresProject tests that No project specified when not in config.
func TestCardsCmdRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "list")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsListColumnNameRequiresCardTable tests that column name requires --card-table.
func TestCardsListColumnNameRequiresCardTable(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Use column name (not numeric) without --card-table
	err := executeCommand(cmd, app, "list", "--column", "Done")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--card-table is required when using --column with a name" {
			t.Errorf("expected '--card-table is required when using --column with a name', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsColumnCreateRequiresTitle tests that --title is required for column create.
func TestCardsColumnCreateRequiresTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := ""
	cmd := newCardsColumnCreateCmd(&project, &cardTable)

	err := executeCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Check if it's a Cobra required flag error or our custom error
	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--title is required" {
			t.Errorf("expected '--title is required', got %q", e.Message)
		}
	} else {
		// Cobra validates required flags with a different error format
		errStr := err.Error()
		if errStr != `required flag(s) "title" not set` {
			t.Errorf("expected required flag error for title, got %q", errStr)
		}
	}
}

// TestCardsColumnUpdateRequiresFields tests that at least one field is required for column update.
func TestCardsColumnUpdateRequiresFields(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsColumnUpdateCmd(&project)

	err := executeCommand(cmd, app, "456") // column ID but no update fields
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No update fields provided" {
			t.Errorf("expected 'No update fields provided', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsColumnMoveRequiresPosition tests that --position is required for column move.
func TestCardsColumnMoveRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := ""
	cmd := newCardsColumnMoveCmd(&project, &cardTable)

	err := executeCommand(cmd, app, "456") // column ID but no position
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		// Match the actual error message format
		if e.Message != "--position required (1-indexed)" {
			t.Errorf("expected '--position required (1-indexed)', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsMoveRequiresTo tests that --to is required for cards move.
func TestCardsMoveRequiresTo(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cardTable := "999"
	cmd := newCardsMoveCmd(&project, &cardTable)

	// Card ID but no --to
	err := executeCommand(cmd, app, "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags
	errStr := err.Error()
	if errStr != `required flag(s) "to" not set` {
		t.Errorf("expected required flag error for --to, got %q", errStr)
	}
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
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "--card-table is required when --to is a column name" {
			t.Errorf("expected '--card-table is required when --to is a column name', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardShortcutRequiresTitle tests that --title is required for card shortcut.
func TestCardShortcutRequiresTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardCmd()

	// No --title flag
	err := executeCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags
	errStr := err.Error()
	if errStr != `required flag(s) "title" not set` {
		t.Errorf("expected required flag error for --title, got %q", errStr)
	}
}

// TestCardsColumnsRequiresProject tests that No project specified for columns listing.
func TestCardsColumnsRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cardTable := ""
	cmd := newCardsColumnsCmd(&project, &cardTable)

	err := executeCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsColumnShowRequiresProject tests that No project specified for column show.
func TestCardsColumnShowRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cmd := newCardsColumnShowCmd(&project)

	err := executeCommand(cmd, app, "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepCompleteRequiresProject tests that No project specified for step complete.
func TestCardsStepCompleteRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cmd := newCardsStepCompleteCmd(&project)

	err := executeCommand(cmd, app, "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepUncompleteRequiresProject tests that No project specified for step uncomplete.
func TestCardsStepUncompleteRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	project := ""
	cmd := newCardsStepUncompleteCmd(&project)

	err := executeCommand(cmd, app, "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
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
			if e.Message == "--card-table is required when using --column with a name" {
				t.Error("Numeric column ID should not require --card-table")
			}
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
			if e.Message == "--card-table is required when using --column with a name" {
				t.Error("Numeric column ID should not require --card-table for create")
			}
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
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		// Should NOT be the card-table error - numeric IDs bypass that requirement
		if e.Message == "--card-table is required when --to is a column name" {
			t.Errorf("numeric --to should not require --card-table, got %q", e.Message)
		}
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
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		// MUST be the card-table error - partial numeric is NOT a valid ID
		if e.Message != "--card-table is required when --to is a column name" {
			t.Errorf("expected '--card-table is required when --to is a column name', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
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
					if e.Message != "--card-table is required when using --column with a name" {
						t.Errorf("expected card-table required error, got %q", e.Message)
					}
				}
			} else if !tt.expectCardTable && err != nil {
				if errors.As(err, &e) {
					if e.Message == "--card-table is required when using --column with a name" {
						t.Errorf("numeric column %q should not require --card-table", tt.columnArg)
					}
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
			if result != tt.expected {
				t.Errorf("formatCardTableIDs() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestFormatCardTableMatches tests the formatCardTableMatches helper.
func TestFormatCardTableMatches(t *testing.T) {
	tests := []struct {
		name       string
		cardTables []struct {
			ID    int64
			Title string
		}
		expected []string
	}{
		{
			name: "with titles",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 123, Title: "Sprint Board"},
				{ID: 456, Title: "Backlog"},
			},
			expected: []string{"123: Sprint Board", "456: Backlog"},
		},
		{
			name: "without titles",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 123, Title: ""},
				{ID: 456, Title: ""},
			},
			expected: []string{"123", "456"},
		},
		{
			name: "mixed",
			cardTables: []struct {
				ID    int64
				Title string
			}{
				{ID: 123, Title: "Board"},
				{ID: 456, Title: ""},
			},
			expected: []string{"123: Board", "456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCardTableMatches(tt.cardTables)
			if len(result) != len(tt.expected) {
				t.Errorf("formatCardTableMatches() length = %d, want %d", len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("formatCardTableMatches()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

// =============================================================================
// Cards Create Validation Tests
// =============================================================================

// TestCardsCreateRequiresTitle tests that --title is required for card creation.
func TestCardsCreateRequiresTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "create")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if errStr != `required flag(s) "title" not set` {
		t.Errorf("expected required flag error for --title, got %q", errStr)
	}
}

// TestCardsUpdateRequiresCardID tests that card ID is required for update.
// Cobra validates args count, so we get a Cobra error.
func TestCardsUpdateRequiresCardID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	// Update with fields but no card ID
	err := executeCommand(cmd, app, "update", "--title", "New Title")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates args count first
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected Cobra args error, got %q", errStr)
	}
}

// TestCardsUpdateRequiresFields tests that at least one field is required for update.
func TestCardsUpdateRequiresFields(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "update", "456") // card ID but no fields
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "At least one field required" {
			t.Errorf("expected 'At least one field required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsShowRequiresCardID tests that card ID is required for show.
// Cobra validates args count, so we get a Cobra error.
func TestCardsShowRequiresCardID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCardsCmd()

	err := executeCommand(cmd, app, "show")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates args count first
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected Cobra args error, got %q", errStr)
	}
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
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates args count first
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected Cobra args error, got %q", errStr)
	}
}

// =============================================================================
// Card Shortcut Command Tests
// =============================================================================

// TestCardShortcutRequiresProject tests that project is required for card shortcut.
func TestCardShortcutRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config

	cmd := NewCardCmd()

	err := executeCommand(cmd, app, "--title", "Test Card")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepDeleteRequiresProject tests that project is required for step delete.
func TestCardsStepDeleteRequiresProject(t *testing.T) {
	app, _ := setupTestApp(t)
	// No project in config
	app.Config.ProjectID = ""

	project := ""
	cmd := newCardsStepDeleteCmd(&project)

	err := executeCommand(cmd, app, "456") // step ID but no project
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var e *output.Error
	if errors.As(err, &e) {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestCardsStepDeleteRequiresStepID tests that step ID is required for step delete.
func TestCardsStepDeleteRequiresStepID(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCardsStepDeleteCmd(&project)

	err := executeCommand(cmd, app) // no step ID
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates args count first
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected Cobra args error, got %q", errStr)
	}
}
