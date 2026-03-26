package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestRecordingTypeEndpoint(t *testing.T) {
	tests := []struct {
		apiType  string
		id       string
		expected string
	}{
		{"Todo", "123", "/todos/123.json"},
		{"Todolist", "456", "/todolists/456.json"},
		{"Message", "789", "/messages/789.json"},
		{"Comment", "100", "/comments/100.json"},
		{"Kanban::Card", "200", "/card_tables/cards/200.json"},
		{"Document", "300", "/documents/300.json"},
		{"Vault::Document", "301", "/documents/301.json"},
		{"Schedule::Entry", "400", "/schedule_entries/400.json"},
		{"Question", "499", "/questions/499.json"},
		{"Question::Answer", "500", "/question_answers/500.json"},
		{"Todolist::Todo", "124", "/todos/124.json"},
		{"Inbox::Forward", "600", "/forwards/600.json"},
		{"Upload", "700", "/uploads/700.json"},
		{"Vault", "800", "/vaults/800.json"},
		{"Chat::Transcript", "900", "/chats/900.json"},
		{"Todoset", "1000", "/todosets/1000.json"},
		{"Message::Board", "1100", "/message_boards/1100.json"},
		{"Schedule", "1200", "/schedules/1200.json"},
		{"Questionnaire", "1300", "/questionnaires/1300.json"},
		{"Inbox", "1400", "/inboxes/1400.json"},
		{"Kanban::Column", "1500", "/card_tables/columns/1500.json"},
		{"Kanban::Step", "1600", "/card_tables/steps/1600.json"},
	}

	for _, tt := range tests {
		t.Run(tt.apiType, func(t *testing.T) {
			data := map[string]any{"type": tt.apiType}
			result := recordingTypeEndpoint(data, tt.id)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRecordingTypeEndpoint_UnknownType(t *testing.T) {
	data := map[string]any{"type": "SomeNewType"}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "unknown types should return empty string")
}

func TestRecordingTypeEndpoint_MissingType(t *testing.T) {
	data := map[string]any{"title": "no type field"}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "missing type should return empty string")
}

func TestRecordingTypeEndpoint_EmptyType(t *testing.T) {
	data := map[string]any{"type": ""}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "empty type should return empty string")
}

func TestRecordingTypeEndpoint_ChatLine(t *testing.T) {
	data := map[string]any{
		"type":   "Chat::Lines::Text",
		"parent": map[string]any{"id": float64(789), "type": "Chat::Transcript"},
	}
	result := recordingTypeEndpoint(data, "111")
	assert.Equal(t, "/chats/789/lines/111.json", result)
}

func TestRecordingTypeEndpoint_ChatLineNoParent(t *testing.T) {
	data := map[string]any{"type": "Chat::Lines::Text"}
	result := recordingTypeEndpoint(data, "111")
	assert.Equal(t, "", result, "should return empty when parent is missing")
}

func TestRecordingTypeEndpoint_InboxReply(t *testing.T) {
	data := map[string]any{
		"type":   "Inbox::Forward::Reply",
		"parent": map[string]any{"id": float64(380), "type": "Inbox::Forward"},
	}
	result := recordingTypeEndpoint(data, "400")
	assert.Equal(t, "/inbox_forwards/380/replies/400.json", result)
}

func TestRecordingTypeEndpoint_InboxReplyNoParent(t *testing.T) {
	data := map[string]any{"type": "Inbox::Forward::Reply"}
	result := recordingTypeEndpoint(data, "400")
	assert.Equal(t, "", result, "should return empty when parent is missing")
}

// TestKnownURLPathTypesAreValid checks that every PathType we know the SDK
// router can return for a recording-like URL is accepted by isValidRecordType.
// This is a curated list — if the SDK adds new PathTypes, they must be added
// here and to isValidRecordType.
func TestKnownURLPathTypesAreValid(t *testing.T) {
	urlPathTypes := []string{
		// Standard recording types
		"todos", "todolists", "messages", "comments", "documents",
		"uploads", "vaults", "chats", "lines",
		"schedule_entries", "inbox_forwards", "replies",
		// Card table types
		"cards", "card_tables", "columns", "steps",
		// Checkin types
		"questions", "question_answers",
		// Container/dock types
		"todosets", "message_boards", "schedules", "questionnaires", "inboxes",
		// Account-level types
		"people", "boosts", "recordings",
	}

	for _, pt := range urlPathTypes {
		t.Run(pt, func(t *testing.T) {
			assert.True(t, isValidRecordType(pt), "URL PathType %q should be accepted by isValidRecordType", pt)
		})
	}
}

// --- Test helpers ---

// showTestTokenProvider is a mock token provider for show tests.
type showTestTokenProvider struct{}

func (showTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// showTestApp creates a test App with the given transport for show command tests.
func showTestApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
}

func showTestAppWithOutput(t *testing.T, transport http.RoundTripper, format output.Format, writer, errWriter *bytes.Buffer) *appctx.App {
	t.Helper()
	app := showTestApp(t, transport)
	app.Output = output.New(output.Options{
		Format:    format,
		Writer:    writer,
		ErrWriter: errWriter,
	})
	return app
}

// showTrackingTransport records request paths and returns configurable responses.
// By default it returns a generic success response. Set the responder field to
// customize per-request response bodies.
type showTrackingTransport struct {
	mu        sync.Mutex
	requests  []string
	responder func(path string) (int, string) // optional: (statusCode, body)
}

func (t *showTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.URL.Path)
	t.mu.Unlock()

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	status := 200
	body := `{"id": 789, "type": "Item", "title": "Test item"}`
	if t.responder != nil {
		status, body = t.responder(req.URL.Path)
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func (t *showTrackingTransport) getRequests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.requests))
	copy(out, t.requests)
	return out
}

// runShowCmd is a helper that executes the show command with the given args
// and returns the error (if any) and the transport's recorded requests.
func runShowCmd(t *testing.T, transport *showTrackingTransport, args ...string) ([]string, error) {
	t.Helper()
	app := showTestApp(t, transport)
	cmd := NewShowCmd()
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return transport.getRequests(), err
}

func runShowCmdCapture(t *testing.T, transport *showTrackingTransport, format output.Format, args ...string) ([]string, string, string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, format, stdout, stderr)
	cmd := NewShowCmd()
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return transport.getRequests(), stdout.String(), stderr.String(), err
}

type showJSONEnvelope struct {
	Summary     string              `json:"summary"`
	Notice      string              `json:"notice"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
	Data        json.RawMessage     `json:"data"`
}

func decodeShowJSONEnvelope(t *testing.T, stdout string) showJSONEnvelope {
	t.Helper()

	var envelope showJSONEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
	return envelope
}

func decodeShowJSONDataMap(t *testing.T, data json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &decoded))
	return decoded
}

func showCommentsJSON(count int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id": %d, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<p>Comment %d</p>", "creator": {"name": "Person %d"}}`, 9001+i, i+1, i+1)
	}
	b.WriteString("]")
	return b.String()
}

// --- Generic recording refetch tests ---

func TestShowGenericRecordingRefetchesTypeEndpoint(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 42, "type": "Todo", "title": "sparse title"}`
			}
			if strings.Contains(path, "/todos/") {
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "content": "Full todo content", "completed": false}`
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)
	// Override output writer so we can inspect it
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"42"}) // No type → generic recording lookup
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.Len(t, reqs, 2, "expected 2 requests: /recordings/ then /todos/")
	assert.Contains(t, reqs[0], "/recordings/42.json")
	assert.Contains(t, reqs[1], "/todos/42.json")
	assert.Contains(t, buf.String(), "Full todo content")
}

func TestShowGenericRecordingFallsBackOnRefetchError(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 42, "type": "Todo", "title": "sparse title"}`
			}
			return 500, `{"error":"internal"}`
		},
	}
	app := showTestApp(t, transport)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"42"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err, "should succeed with sparse data when refetch fails")
	assert.Contains(t, buf.String(), "sparse title")
}

func TestShowIncludesCommentsWhenPresent(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 2}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<div>First <strong>comment</strong></div>", "creator": {"name": "Annie Bryan"}},
					{"id": 9002, "status": "active", "type": "Comment", "created_at": "2026-03-26T11:00:00Z", "updated_at": "2026-03-26T11:00:00Z", "content": "<p>Second comment</p>", "creator": {"name": "Jason Fried"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "todo", "42")
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[0], "/todos/42.json")
	assert.Contains(t, reqs[1], "/recordings/42/comments.json")

	envelope := decodeShowJSONEnvelope(t, stdout)
	assert.Equal(t, "Todo #42: Buy milk (2 comments)", envelope.Summary)

	data := decodeShowJSONDataMap(t, envelope.Data)
	comments, ok := data["comments"]
	require.True(t, ok, "comments field should be present")

	var decodedComments []struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal(comments, &decodedComments))
	require.Len(t, decodedComments, 2)
	assert.Equal(t, []int{9001, 9002}, []int{decodedComments[0].ID, decodedComments[1].ID})
}

func TestShowNoCommentsWhenCountZero(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/todos/42.json") {
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 0}`
			}
			return 200, `{}`
		},
	}

	reqs, _, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "todo", "42")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "/todos/42.json")
}

func TestShowNoCommentsFlag(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/todos/42.json") {
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 2}`
			}
			return 200, `{}`
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "todo", "42", "--no-comments")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "/todos/42.json")

	envelope := decodeShowJSONEnvelope(t, stdout)
	assert.Equal(t, "Todo #42: Buy milk (2 comments)", envelope.Summary)

	data := decodeShowJSONDataMap(t, envelope.Data)
	_, ok := data["comments"]
	assert.False(t, ok, "comments field should be omitted when --no-comments is set")
}

func TestShowCommentsDefaultLimitAddsNotice(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 150}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, showCommentsJSON(100)
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "todo", "42")
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[0], "/todos/42.json")
	assert.Contains(t, reqs[1], "/recordings/42/comments.json")

	envelope := decodeShowJSONEnvelope(t, stdout)
	assert.Equal(t, "Todo #42: Buy milk (150 comments)", envelope.Summary)
	assert.Equal(t, "Showing 100 of 150 comments — use --all-comments for the full discussion", envelope.Notice)

	data := decodeShowJSONDataMap(t, envelope.Data)
	comments, ok := data["comments"]
	require.True(t, ok, "comments field should be present")

	var decodedComments []struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal(comments, &decodedComments))
	require.Len(t, decodedComments, 100)
}

func TestShowAllCommentsFlagFetchesEntireDiscussion(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 150}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, showCommentsJSON(150)
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "todo", "42", "--all-comments")
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[0], "/todos/42.json")
	assert.Contains(t, reqs[1], "/recordings/42/comments.json")

	envelope := decodeShowJSONEnvelope(t, stdout)
	assert.Equal(t, "Todo #42: Buy milk (150 comments)", envelope.Summary)
	assert.Empty(t, envelope.Notice)

	data := decodeShowJSONDataMap(t, envelope.Data)
	comments, ok := data["comments"]
	require.True(t, ok, "comments field should be present")

	var decodedComments []struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal(comments, &decodedComments))
	require.Len(t, decodedComments, 150)
}

func TestShowCommentFlagsMutuallyExclusive(t *testing.T) {
	transport := &showTrackingTransport{}
	app := showTestApp(t, transport)
	cmd := NewShowCmd()
	cmd.SetArgs([]string{"todo", "42", "--no-comments", "--all-comments"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-comments")
	assert.Contains(t, err.Error(), "all-comments")
	assert.Empty(t, transport.getRequests())
}

func TestShowCommentsGracefulDegradation(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 2}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 500, `{"error":"boom"}`
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, stderr, err := runShowCmdCapture(t, transport, output.FormatQuiet, "todo", "42")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 2)
	assert.Contains(t, reqs[0], "/todos/42.json")
	assert.Contains(t, reqs[1], "/recordings/42/comments.json")
	assert.Contains(t, stderr, "notice: 2 comments available, but fetching them failed")
	assert.NotContains(t, stdout, `"comments"`)
	assert.Contains(t, stdout, `"comments_count": 2`)
}

func TestShowCommentsDiagnosticPreservesAttachmentNotice(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 2, "content": "<p>See <bc-attachment url=\"https://example.com/a.png\" filename=\"a.png\"></bc-attachment></p>"}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 500, `{"error":"boom"}`
			default:
				return 200, `{}`
			}
		},
	}

	_, _, stderr, err := runShowCmdCapture(t, transport, output.FormatQuiet, "todo", "42")
	require.NoError(t, err)
	assert.Contains(t, stderr, "notice: 2 comments available, but fetching them failed")
	assert.Contains(t, stderr, "1 attachment(s) — download: basecamp attachments download 42")
}

func TestShowCommentsMissingField(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/people/42.json") {
				return 200, `{"id": 42, "type": "Person", "name": "Alice"}`
			}
			return 200, `{}`
		},
	}

	reqs, _, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "people", "42")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "/people/42.json")
}

func TestShowCommentsUsesCommentCountFallback(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/card_tables/columns/42.json"):
				return 200, `{"id": 42, "type": "Kanban::Column", "title": "In Progress", "comment_count": 1}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<p>Needs follow-up</p>", "creator": {"name": "Annie Bryan"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "columns", "42")
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[0], "/card_tables/columns/42.json")
	assert.Contains(t, reqs[1], "/recordings/42/comments.json")

	var envelope struct {
		Summary string          `json:"summary"`
		Data    json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
	assert.Equal(t, "Kanban::Column #42: In Progress (1 comment)", envelope.Summary)

	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	comments, ok := data["comments"]
	require.True(t, ok, "comments field should be present")

	var decodedComments []map[string]any
	require.NoError(t, json.Unmarshal(comments, &decodedComments))
	require.Len(t, decodedComments, 1)
}

func TestShowCommentsAfterGenericRefetch(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/recordings/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Sparse todo"}`
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Rich todo", "comments_count": 2}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<p>First comment</p>", "creator": {"name": "Annie Bryan"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "42")
	require.NoError(t, err)
	require.Len(t, reqs, 3)
	assert.Contains(t, reqs[0], "/recordings/42.json")
	assert.Contains(t, reqs[1], "/todos/42.json")
	assert.Contains(t, reqs[2], "/recordings/42/comments.json")

	envelope := decodeShowJSONEnvelope(t, stdout)
	assert.Equal(t, "Todo #42: Rich todo (2 comments)", envelope.Summary)

	data := decodeShowJSONDataMap(t, envelope.Data)
	title, ok := data["title"]
	require.True(t, ok, "title field should be present")
	var decodedTitle string
	require.NoError(t, json.Unmarshal(title, &decodedTitle))
	assert.Equal(t, "Rich todo", decodedTitle)

	comments, ok := data["comments"]
	require.True(t, ok, "comments field should be present")
	var decodedComments []struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal(comments, &decodedComments))
	require.Len(t, decodedComments, 1)
	assert.Equal(t, 9001, decodedComments[0].ID)
}

func TestShowStyledRendersCommentsSection(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "content": "<p>Main body</p>", "comments_count": 2}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<div>First <strong>comment</strong></div>", "creator": {"name": "Annie Bryan"}},
					{"id": 9002, "status": "active", "type": "Comment", "created_at": "2026-03-26T11:00:00Z", "updated_at": "2026-03-26T11:00:00Z", "content": "<p>Second comment</p>", "creator": {"name": "Jason Fried"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatStyled, "todo", "42")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Comments:")
	assert.Contains(t, stdout, "Annie Bryan")
	assert.Contains(t, stdout, "Jason Fried")
	assert.Contains(t, stdout, "First")
	assert.Contains(t, stdout, "Second comment")
	assert.NotContains(t, stdout, "<div>")
}

func TestShowMarkdownRendersCommentsSection(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "content": "<p>Main body</p>", "comments_count": 1}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<div>First <strong>comment</strong></div>", "creator": {"name": "Annie Bryan"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatMarkdown, "todo", "42")
	require.NoError(t, err)
	assert.Contains(t, stdout, "## Comments")
	assert.Contains(t, stdout, "**Annie Bryan**")
	assert.Contains(t, stdout, "First")
	assert.NotContains(t, stdout, "<div>")
}

func TestShowStyledNoRawCommentsFieldDump(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			switch {
			case strings.Contains(path, "/todos/42.json"):
				return 200, `{"id": 42, "type": "Todo", "title": "Buy milk", "comments_count": 2}`
			case strings.Contains(path, "/recordings/42/comments.json"):
				return 200, `[
					{"id": 9001, "status": "active", "type": "Comment", "created_at": "2026-03-26T10:00:00Z", "updated_at": "2026-03-26T10:00:00Z", "content": "<p>First comment</p>", "creator": {"name": "Annie Bryan"}},
					{"id": 9002, "status": "active", "type": "Comment", "created_at": "2026-03-26T11:00:00Z", "updated_at": "2026-03-26T11:00:00Z", "content": "<p>Second comment</p>", "creator": {"name": "Jason Fried"}}
				]`
			default:
				return 200, `{}`
			}
		},
	}

	_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatStyled, "todo", "42")
	require.NoError(t, err)
	assert.NotContains(t, stdout, "9001, 9002")
	assert.NotContains(t, stdout, "map[")
}

// --- URL type routing tests ---

func TestShowFragmentURLResolvesComment(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/999") {
				return 200, `{"id": 999, "type": "Comment", "content": "This is the comment"}`
			}
			if strings.Contains(path, "/comments/999") {
				return 200, `{"id": 999, "type": "Comment", "content": "Rich comment content"}`
			}
			return 200, `{"id": 789, "type": "Todo", "title": "Parent todo"}`
		},
	}

	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todos/789#__recording_999")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/999.json")
}

func TestShowChatAtURLResolvesLine(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 111, "type": "Chat::Lines::Text", "content": "hello"}`
			}
			return 200, `{}`
		},
	}

	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/chats/789@111")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/111.json")
}

func TestShowProjectURLReturnsHelpfulError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/projects/456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "projects show")
}

func TestShowCircleURLReturnsHelpfulError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/circles/789@456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be shown")
}

func TestShowInboxForwardURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/inbox_forwards/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/forwards/789.json")
}

func TestShowScheduleEntryOccurrenceURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/schedule_entries/789/occurrences/20251229")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/schedule_entries/789/occurrences/20251229.json",
		"occurrence URL should route to the occurrence endpoint, not the parent entry")
}

func TestShowScheduleEntryURLWithoutOccurrence(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/schedule_entries/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/schedule_entries/789.json",
		"plain schedule entry URL should route to the entry endpoint")
}

func TestShowQuestionURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/questions/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/questions/789.json")
}

func TestShowQuestionAnswerURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/question_answers/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/question_answers/789.json")
}

func TestShowCardTablesURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/card_tables/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/card_tables/789.json")
}

func TestShowPeopleURL(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/people/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/people/789.json")
}

func TestShowContainerURLUsesDedicatedEndpoint(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todosets/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/todosets/789.json")
}

func TestShowColumnURLUsesDedicatedEndpoint(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/card_tables/columns/789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/card_tables/columns/789.json")
}

// --- Positional type arg tests (basecamp show <type> <id>) ---

func TestShowPositionalVault(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "vault", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/vaults/789.json")
}

func TestShowPositionalChat(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "chat", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/chats/789.json")
}

func TestShowPositionalPeople(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "people", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/people/789.json")
}

func TestShowPositionalBoosts(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "boosts", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/boosts/789.json")
}

func TestShowPositionalCampfire(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "campfire", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/chats/789.json")
}

func TestShowPositionalCampfires(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "campfires", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/chats/789.json")
}

// --- Collection URL tests ---

func TestShowCollectionURLTodolistsReturnsError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todosets/777/todolists")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
}

func TestShowCollectionURLColumnsReturnsError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/card_tables/789/columns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
}

func TestShowCollectionURLQuestionsReturnsError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/questionnaires/999/questions")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
}

func TestShowCollectionURLLinesReturnsError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/chats/789/lines")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
}

func TestShowStructuralListURLReturnsListError(t *testing.T) {
	transport := &showTrackingTransport{}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todolists")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
}

// --- Line/reply refetch tests ---

func TestShowLineRefetchesViaParent(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 111, "type": "Chat::Lines::Text", "content": "sparse", "parent": {"id": 789, "type": "Chat::Transcript"}}`
			}
			if strings.Contains(path, "/chats/789/lines/111") {
				return 200, `{"id": 111, "type": "Chat::Lines::Text", "content": "Rich line content with extras"}`
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"line", "111"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.Len(t, reqs, 2, "expected 2 requests: /recordings/ then /chats/{parentId}/lines/")
	assert.Contains(t, reqs[0], "/recordings/111.json")
	assert.Contains(t, reqs[1], "/chats/789/lines/111.json")
	assert.Contains(t, buf.String(), "Rich line content with extras")
}

func TestShowReplyRefetchesViaParent(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 400, "type": "Inbox::Forward::Reply", "content": "sparse", "parent": {"id": 380, "type": "Inbox::Forward"}}`
			}
			if strings.Contains(path, "/inbox_forwards/380/replies/400") {
				return 200, `{"id": 400, "type": "Inbox::Forward::Reply", "content": "Rich reply content"}`
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"replies", "400"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.Len(t, reqs, 2, "expected 2 requests: /recordings/ then /inbox_forwards/{parentId}/replies/")
	assert.Contains(t, reqs[0], "/recordings/400.json")
	assert.Contains(t, reqs[1], "/inbox_forwards/380/replies/400.json")
	assert.Contains(t, buf.String(), "Rich reply content")
}

// --- 204 error message tests ---

func TestShowLine204ReturnsNotFound(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			return 204, ""
		},
	}
	_, err := runShowCmd(t, transport, "line", "789")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "type required")
}

func TestShowFragment204ReturnsNotFound(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			return 204, ""
		},
	}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todos/789#__recording_999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "type required")
}

func TestShowTypedURL204ReturnsNotFound(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			return 204, ""
		},
	}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todos/789")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "type required")
}

func TestShowUntyped204ReturnsTypeHint(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			return 204, ""
		},
	}
	_, err := runShowCmd(t, transport, "789")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type required")
}

// --- Reply URL tests ---

func TestShowReplyURLRoutesToRecordings(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "replies", "789")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/789.json")
}

// --- Dedicated container endpoint tests ---

func TestShowContainerEndpoints(t *testing.T) {
	tests := []struct {
		typ      string
		expected string
	}{
		{"columns", "/card_tables/columns/789.json"},
		{"steps", "/card_tables/steps/789.json"},
		{"todosets", "/todosets/789.json"},
		{"message_boards", "/message_boards/789.json"},
		{"schedules", "/schedules/789.json"},
		{"questionnaires", "/questionnaires/789.json"},
		{"inboxes", "/inboxes/789.json"},
	}

	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			transport := &showTrackingTransport{}
			reqs, err := runShowCmd(t, transport, tt.typ, "789")
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(reqs), 1)
			assert.Contains(t, reqs[0], tt.expected)
		})
	}
}

// --- Switch coverage test ---

// TestAllValidRecordTypesHandledInSwitch ensures every type accepted by
// isValidRecordType is routed by the switch in RunE (never hits default).
func TestAllValidRecordTypesHandledInSwitch(t *testing.T) {
	validTypes := []string{
		"todo", "todos", "todolist", "todolists", "message", "messages",
		"comment", "comments", "card", "cards",
		"card-table", "card_table", "cardtable", "card_tables",
		"document", "documents", "recording", "recordings",
		"schedule-entry", "schedule_entry", "schedule_entries",
		"checkin", "check-in", "check_in", "questions", "question_answers",
		"forward", "forwards", "inbox_forwards", "upload", "uploads",
		"vault", "vaults", "chat", "chats", "campfire", "campfires",
		"line", "lines", "replies", "columns", "steps",
		"todosets", "message_boards", "schedules", "questionnaires", "inboxes",
		"people", "boosts",
	}

	for _, typ := range validTypes {
		t.Run(typ, func(t *testing.T) {
			transport := &showTrackingTransport{}
			_, err := runShowCmd(t, transport, typ, "999")
			if err != nil {
				assert.NotContains(t, err.Error(), "Unknown type",
					"type %q passes isValidRecordType but is not handled in switch", typ)
			}
		})
	}
}

// --- Attachment discovery tests ---

func TestShowInjectsFieldScopedAttachments(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			return 200, `{
				"id": 42,
				"type": "Message",
				"title": "Design review",
				"content": "<p>See <bc-attachment url=\"https://example.com/a.png\" filename=\"a.png\"></bc-attachment></p>",
				"description": "<p>Also <bc-attachment url=\"https://example.com/b.pdf\" filename=\"b.pdf\"></bc-attachment></p>"
			}`
		},
	}
	app := showTestApp(t, transport)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"message", "42"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	// Field-scoped keys present
	assert.Contains(t, out, "content_attachments")
	assert.Contains(t, out, "description_attachments")
	// Flat "attachments" key not injected (avoids collision with native API field)
	assert.NotContains(t, out, `"attachments"`)
	// Notice mentions download command
	assert.Contains(t, out, "attachment(s)")
}

// TestShowPreservesNativeAttachmentsField verifies that when a recording
// already has a native API "attachments" field (e.g. CampfireLine), the
// show command preserves it untouched while adding field-scoped
// content_attachments alongside it.
func TestShowPreservesNativeAttachmentsField(t *testing.T) {
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			// Simulate a CampfireLine-like record with both a native
			// "attachments" array and inline bc-attachment tags in content.
			return 200, `{
				"id": 99,
				"type": "Chat::Lines::Text",
				"content": "<p>See <bc-attachment url=\"https://example.com/inline.png\" filename=\"inline.png\"></bc-attachment></p>",
				"attachments": [{"sgid": "native-sgid", "filename": "native.pdf"}]
			}`
		},
	}
	app := showTestApp(t, transport)
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"99"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// Parse JSON envelope to inspect data keys directly
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data, &data))

	// Native "attachments" key preserved with original structure
	native, ok := data["attachments"]
	require.True(t, ok, "native 'attachments' key should be preserved")
	var nativeAtts []map[string]string
	require.NoError(t, json.Unmarshal(native, &nativeAtts))
	require.Len(t, nativeAtts, 1)
	assert.Equal(t, "native-sgid", nativeAtts[0]["sgid"])
	assert.Equal(t, "native.pdf", nativeAtts[0]["filename"])

	// Field-scoped collection added under its own key
	scoped, ok := data["content_attachments"]
	require.True(t, ok, "content_attachments key should be present")
	var scopedAtts []map[string]string
	require.NoError(t, json.Unmarshal(scoped, &scopedAtts))
	require.Len(t, scopedAtts, 1)
	assert.Equal(t, "inline.png", scopedAtts[0]["filename"])
}
