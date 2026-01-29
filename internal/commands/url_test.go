package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestURLParsing tests the URL parsing logic via the command interface.
// These tests mirror the bash url.bats tests.

// parseURLWithOutput is a helper that runs URL parsing and captures output.
func parseURLWithOutput(t *testing.T, url string) (output.Response, error) {
	t.Helper()

	var buf bytes.Buffer
	app := &appctx.App{
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &buf,
		}),
	}

	err := runURLParse(app, url)
	if err != nil {
		return output.Response{}, err
	}

	var resp output.Response
	err = json.Unmarshal(buf.Bytes(), &resp)
	require.NoError(t, err, "failed to unmarshal response (raw: %s)", buf.String())
	return resp, nil
}

// getParsedURL extracts ParsedURL from response data.
func getParsedURL(t *testing.T, resp output.Response) ParsedURL {
	t.Helper()
	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err, "failed to marshal data")
	var parsed ParsedURL
	err = json.Unmarshal(dataBytes, &parsed)
	require.NoError(t, err, "failed to unmarshal ParsedURL")
	return parsed
}

// =============================================================================
// Basic URL Parsing
// =============================================================================

func TestURLParseFullMessageURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assertStringPtr(t, parsed.BucketID, "41746046", "bucket_id")
	assertStringPtr(t, parsed.Type, "messages", "type")
	assertStringPtr(t, parsed.RecordingID, "9478142982", "recording_id")
}

func TestURLParseWithCommentFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assertStringPtr(t, parsed.BucketID, "41746046", "bucket_id")
	assertStringPtr(t, parsed.Type, "messages", "type")
	assertStringPtr(t, parsed.RecordingID, "9478142982", "recording_id")
	assertStringPtr(t, parsed.CommentID, "9488783598", "comment_id")
}

// =============================================================================
// Different Recording Types
// =============================================================================

func TestURLParseTodoURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todos/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "todos", "type")
	assertStringPtr(t, parsed.TypeSingular, "todo", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseTodolistURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todolists/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "todolists", "type")
	assertStringPtr(t, parsed.TypeSingular, "todolist", "type_singular")
}

func TestURLParseDocumentURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/documents/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "documents", "type")
	assertStringPtr(t, parsed.TypeSingular, "document", "type_singular")
}

func TestURLParseCampfireURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/chats/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "chats", "type")
	assertStringPtr(t, parsed.TypeSingular, "campfire", "type_singular")
}

func TestURLParseCardURLWithNestedPath(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/27/card_tables/cards/9486682178#__recording_9500689518")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assertStringPtr(t, parsed.BucketID, "27", "bucket_id")
	assertStringPtr(t, parsed.Type, "cards", "type")
	assertStringPtr(t, parsed.TypeSingular, "card", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "9486682178", "recording_id")
	assertStringPtr(t, parsed.CommentID, "9500689518", "comment_id")
}

func TestURLParseColumnURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/columns/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "columns", "type")
	assertStringPtr(t, parsed.TypeSingular, "column", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseColumnListURL(t *testing.T) {
	// lists is an alias for columns
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/lists/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "columns", "type")
	assertStringPtr(t, parsed.TypeSingular, "column", "type_singular")
}

func TestURLParseStepURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/steps/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "steps", "type")
	assertStringPtr(t, parsed.TypeSingular, "step", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseUploadURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/uploads/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "uploads", "type")
	assertStringPtr(t, parsed.TypeSingular, "upload", "type_singular")
}

func TestURLParseScheduleURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/schedule_entries/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "schedule_entries", "type")
	assertStringPtr(t, parsed.TypeSingular, "schedule_entry", "type_singular")
}

func TestURLParseVaultURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/vaults/789")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "vaults", "type")
	assertStringPtr(t, parsed.TypeSingular, "vault", "type_singular")
}

// =============================================================================
// Project URLs
// =============================================================================

func TestURLParseProjectURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/projects/41746046")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assertStringPtr(t, parsed.BucketID, "41746046", "bucket_id")
	assertStringPtr(t, parsed.Type, "project", "type")
}

// =============================================================================
// Type List URLs (no recording_id)
// =============================================================================

func TestURLParseTypeListURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todos")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.BucketID, "456", "bucket_id")
	assertStringPtr(t, parsed.Type, "todos", "type")
	assert.Nil(t, parsed.RecordingID, "recording_id should be nil for type list")
}

func TestURLParseTypeListURLWithTrailingSlash(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "messages", "type")
	assert.Nil(t, parsed.RecordingID, "recording_id should be nil for type list")
}

// =============================================================================
// Account Only URLs
// =============================================================================

func TestURLParseAccountOnlyURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assert.Nil(t, parsed.BucketID, "bucket_id should be nil for account-only URL")
}

// =============================================================================
// Fragment Variations
// =============================================================================

func TestURLParseNumericOnlyFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#111")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.CommentID, "111", "comment_id")
}

func TestURLParseNonNumericFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#section-header")
	require.NoError(t, err)

	parsed := getParsedURL(t, resp)
	// Non-numeric fragment should not be parsed as comment_id
	assert.Nil(t, parsed.CommentID, "comment_id should be nil for non-numeric fragment")
}

// =============================================================================
// Error Cases
// =============================================================================

func TestURLParseFailsForNonBasecampURL(t *testing.T) {
	_, err := parseURLWithOutput(t, "https://github.com/test/repo")
	require.Error(t, err, "expected error for non-Basecamp URL")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeUsage, outErr.Code)
}

func TestURLParseFailsForInvalidPath(t *testing.T) {
	// A valid Basecamp domain but unparseable path
	_, err := parseURLWithOutput(t, "https://3.basecamp.com/notanumber/invalid")
	require.Error(t, err, "expected error for invalid path")
}

// =============================================================================
// Summary Tests
// =============================================================================

func TestURLParseSummaryForMessageWithComment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111")
	require.NoError(t, err)

	expected := "Message #789 in project #456, comment #111"
	assert.Equal(t, expected, resp.Summary)
}

func TestURLParseSummaryForTodo(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todos/789")
	require.NoError(t, err)

	expected := "Todo #789 in project #456"
	assert.Equal(t, expected, resp.Summary)
}

func TestURLParseSummaryForProject(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/projects/456")
	require.NoError(t, err)

	expected := "Project #456"
	assert.Equal(t, expected, resp.Summary)
}

func TestURLParseSummaryForAccount(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123")
	require.NoError(t, err)

	expected := "Account #123"
	assert.Equal(t, expected, resp.Summary)
}

func TestURLParseSummaryForTypeList(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages")
	require.NoError(t, err)

	expected := "Message list in project #456"
	assert.Equal(t, expected, resp.Summary)
}

// =============================================================================
// Breadcrumb Tests
// =============================================================================

func TestURLParseBreadcrumbsForMessage(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789")
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(resp.Breadcrumbs), 3, "expected at least 3 breadcrumbs")

	// Should have show, comment, comments
	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	for _, expected := range []string{"show", "comment", "comments"} {
		assert.True(t, actions[expected], "missing breadcrumb action %q", expected)
	}
}

func TestURLParseBreadcrumbsIncludeShowComment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111")
	require.NoError(t, err)

	var hasShowComment bool
	for _, bc := range resp.Breadcrumbs {
		if bc.Action == "show-comment" {
			hasShowComment = true
			break
		}
	}

	assert.True(t, hasShowComment, "expected show-comment breadcrumb when comment_id is present")
}

func TestURLParseBreadcrumbsForColumn(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/columns/789")
	require.NoError(t, err)

	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	// Column should have show, columns
	assert.True(t, actions["show"], "missing 'show' breadcrumb for column")
	assert.True(t, actions["columns"], "missing 'columns' breadcrumb for column")
}

func TestURLParseBreadcrumbsForStep(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/steps/789")
	require.NoError(t, err)

	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	// Step should have complete, uncomplete
	assert.True(t, actions["complete"], "missing 'complete' breadcrumb for step")
	assert.True(t, actions["uncomplete"], "missing 'uncomplete' breadcrumb for step")
}

// =============================================================================
// Command Interface Tests
// =============================================================================

func TestURLCmdCreation(t *testing.T) {
	cmd := NewURLCmd()
	require.NotNil(t, cmd, "NewURLCmd returned nil")
	assert.Equal(t, "url [parse] <url>", cmd.Use)
	assert.NotEmpty(t, cmd.Short, "command should have short description")

	// Should have parse subcommand
	parseCmd, _, err := cmd.Find([]string{"parse"})
	require.NoError(t, err, "expected parse subcommand")
	require.NotNil(t, parseCmd, "parse subcommand not found")
	assert.Equal(t, "parse <url>", parseCmd.Use)
}

// =============================================================================
// Helper Functions
// =============================================================================

func assertStringPtr(t *testing.T, got *string, want, name string) {
	t.Helper()
	require.NotNil(t, got, "%s is nil, want %q", name, want)
	assert.Equal(t, want, *got, "%s mismatch", name)
}
