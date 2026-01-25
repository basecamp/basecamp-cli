package commands

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
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
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v (raw: %s)", err, buf.String())
	}
	return resp, nil
}

// getParsedURL extracts ParsedURL from response data.
func getParsedURL(t *testing.T, resp output.Response) ParsedURL {
	t.Helper()
	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("failed to marshal data: %v", err)
	}
	var parsed ParsedURL
	if err := json.Unmarshal(dataBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal ParsedURL: %v", err)
	}
	return parsed
}

// =============================================================================
// Basic URL Parsing
// =============================================================================

func TestURLParseFullMessageURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	assertStringPtr(t, parsed.BucketID, "41746046", "bucket_id")
	assertStringPtr(t, parsed.Type, "messages", "type")
	assertStringPtr(t, parsed.RecordingID, "9478142982", "recording_id")
}

func TestURLParseWithCommentFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "todos", "type")
	assertStringPtr(t, parsed.TypeSingular, "todo", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseTodolistURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todolists/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "todolists", "type")
	assertStringPtr(t, parsed.TypeSingular, "todolist", "type_singular")
}

func TestURLParseDocumentURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/documents/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "documents", "type")
	assertStringPtr(t, parsed.TypeSingular, "document", "type_singular")
}

func TestURLParseCampfireURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/chats/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "chats", "type")
	assertStringPtr(t, parsed.TypeSingular, "campfire", "type_singular")
}

func TestURLParseCardURLWithNestedPath(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/buckets/27/card_tables/cards/9486682178#__recording_9500689518")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "columns", "type")
	assertStringPtr(t, parsed.TypeSingular, "column", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseColumnListURL(t *testing.T) {
	// lists is an alias for columns
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/lists/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "columns", "type")
	assertStringPtr(t, parsed.TypeSingular, "column", "type_singular")
}

func TestURLParseStepURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/steps/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "steps", "type")
	assertStringPtr(t, parsed.TypeSingular, "step", "type_singular")
	assertStringPtr(t, parsed.RecordingID, "789", "recording_id")
}

func TestURLParseUploadURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/uploads/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "uploads", "type")
	assertStringPtr(t, parsed.TypeSingular, "upload", "type_singular")
}

func TestURLParseScheduleURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/schedule_entries/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "schedule_entries", "type")
	assertStringPtr(t, parsed.TypeSingular, "schedule_entry", "type_singular")
}

func TestURLParseVaultURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/vaults/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "vaults", "type")
	assertStringPtr(t, parsed.TypeSingular, "vault", "type_singular")
}

// =============================================================================
// Project URLs
// =============================================================================

func TestURLParseProjectURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079/projects/41746046")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.BucketID, "456", "bucket_id")
	assertStringPtr(t, parsed.Type, "todos", "type")
	if parsed.RecordingID != nil {
		t.Errorf("recording_id should be nil for type list, got %q", *parsed.RecordingID)
	}
}

func TestURLParseTypeListURLWithTrailingSlash(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.Type, "messages", "type")
	if parsed.RecordingID != nil {
		t.Errorf("recording_id should be nil for type list, got %q", *parsed.RecordingID)
	}
}

// =============================================================================
// Account Only URLs
// =============================================================================

func TestURLParseAccountOnlyURL(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/2914079")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.AccountID, "2914079", "account_id")
	if parsed.BucketID != nil {
		t.Errorf("bucket_id should be nil for account-only URL, got %q", *parsed.BucketID)
	}
}

// =============================================================================
// Fragment Variations
// =============================================================================

func TestURLParseNumericOnlyFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	assertStringPtr(t, parsed.CommentID, "111", "comment_id")
}

func TestURLParseNonNumericFragment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#section-header")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := getParsedURL(t, resp)
	// Non-numeric fragment should not be parsed as comment_id
	if parsed.CommentID != nil {
		t.Errorf("comment_id should be nil for non-numeric fragment, got %q", *parsed.CommentID)
	}
}

// =============================================================================
// Error Cases
// =============================================================================

func TestURLParseFailsForNonBasecampURL(t *testing.T) {
	_, err := parseURLWithOutput(t, "https://github.com/test/repo")
	if err == nil {
		t.Fatal("expected error for non-Basecamp URL")
	}

	outErr, ok := err.(*output.Error)
	if !ok {
		t.Fatalf("expected *output.Error, got %T", err)
	}
	if outErr.Code != output.CodeUsage {
		t.Errorf("expected CodeUsage, got %q", outErr.Code)
	}
}

func TestURLParseFailsForInvalidPath(t *testing.T) {
	// A valid Basecamp domain but unparseable path
	_, err := parseURLWithOutput(t, "https://3.basecamp.com/notanumber/invalid")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// =============================================================================
// Summary Tests
// =============================================================================

func TestURLParseSummaryForMessageWithComment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Message #789 in project #456, comment #111"
	if resp.Summary != expected {
		t.Errorf("summary = %q, want %q", resp.Summary, expected)
	}
}

func TestURLParseSummaryForTodo(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/todos/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Todo #789 in project #456"
	if resp.Summary != expected {
		t.Errorf("summary = %q, want %q", resp.Summary, expected)
	}
}

func TestURLParseSummaryForProject(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/projects/456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Project #456"
	if resp.Summary != expected {
		t.Errorf("summary = %q, want %q", resp.Summary, expected)
	}
}

func TestURLParseSummaryForAccount(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Account #123"
	if resp.Summary != expected {
		t.Errorf("summary = %q, want %q", resp.Summary, expected)
	}
}

func TestURLParseSummaryForTypeList(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Message list in project #456"
	if resp.Summary != expected {
		t.Errorf("summary = %q, want %q", resp.Summary, expected)
	}
}

// =============================================================================
// Breadcrumb Tests
// =============================================================================

func TestURLParseBreadcrumbsForMessage(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Breadcrumbs) < 3 {
		t.Errorf("expected at least 3 breadcrumbs, got %d", len(resp.Breadcrumbs))
	}

	// Should have show, comment, comments
	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	for _, expected := range []string{"show", "comment", "comments"} {
		if !actions[expected] {
			t.Errorf("missing breadcrumb action %q", expected)
		}
	}
}

func TestURLParseBreadcrumbsIncludeShowComment(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hasShowComment bool
	for _, bc := range resp.Breadcrumbs {
		if bc.Action == "show-comment" {
			hasShowComment = true
			break
		}
	}

	if !hasShowComment {
		t.Error("expected show-comment breadcrumb when comment_id is present")
	}
}

func TestURLParseBreadcrumbsForColumn(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/columns/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	// Column should have show, columns
	if !actions["show"] {
		t.Error("missing 'show' breadcrumb for column")
	}
	if !actions["columns"] {
		t.Error("missing 'columns' breadcrumb for column")
	}
}

func TestURLParseBreadcrumbsForStep(t *testing.T) {
	resp, err := parseURLWithOutput(t, "https://3.basecamp.com/123/buckets/456/card_tables/steps/789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actions := make(map[string]bool)
	for _, bc := range resp.Breadcrumbs {
		actions[bc.Action] = true
	}

	// Step should have complete, uncomplete
	if !actions["complete"] {
		t.Error("missing 'complete' breadcrumb for step")
	}
	if !actions["uncomplete"] {
		t.Error("missing 'uncomplete' breadcrumb for step")
	}
}

// =============================================================================
// Command Interface Tests
// =============================================================================

func TestURLCmdCreation(t *testing.T) {
	cmd := NewURLCmd()
	if cmd == nil {
		t.Fatal("NewURLCmd returned nil")
	}
	if cmd.Use != "url [parse] <url>" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("command should have short description")
	}

	// Should have parse subcommand
	parseCmd, _, err := cmd.Find([]string{"parse"})
	if err != nil {
		t.Errorf("expected parse subcommand: %v", err)
	}
	if parseCmd == nil || parseCmd.Use != "parse <url>" {
		t.Error("parse subcommand not found or has wrong Use")
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func assertStringPtr(t *testing.T, got *string, want, name string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want %q", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %q, want %q", name, *got, want)
	}
}
