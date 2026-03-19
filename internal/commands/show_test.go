package commands

import (
	"bytes"
	"context"
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

// TestAllURLPathTypesAreValid ensures every PathType the SDK router can return
// for a recording-like URL is accepted by isValidRecordType. This prevents the
// "Unknown type" error class from recurring for new URL shapes.
func TestAllURLPathTypesAreValid(t *testing.T) {
	// Every PathType the SDK router returns for URLs users might paste.
	urlPathTypes := []string{
		// Standard recording types
		"todos", "todolists", "messages", "comments", "documents",
		"uploads", "vaults", "chats", "lines",
		"schedule_entries", "inbox_forwards",
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

// showTestTokenProvider is a mock token provider for show tests.
type showTestTokenProvider struct{}

func (showTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// showRefetchTransport tracks which endpoints are hit and returns appropriate responses.
type showRefetchTransport struct {
	mu       sync.Mutex
	requests []string
}

func (t *showRefetchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.URL.Path)
	t.mu.Unlock()

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	if strings.Contains(req.URL.Path, "/recordings/") {
		// Sparse recording response with type field
		body = `{"id": 42, "type": "Todo", "title": "sparse title"}`
	} else if strings.Contains(req.URL.Path, "/todos/") {
		// Rich type-specific response
		body = `{"id": 42, "type": "Todo", "title": "Buy milk", "content": "Full todo content", "completed": false}`
	} else {
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func (t *showRefetchTransport) getRequests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.requests))
	copy(out, t.requests)
	return out
}

func TestShowGenericRecordingRefetchesTypeEndpoint(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showRefetchTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

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

	// Output should contain the richer response content
	assert.Contains(t, buf.String(), "Full todo content")
}

func TestShowGenericRecordingFallsBackOnRefetchError(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	// Transport that returns sparse data for /recordings/ and 500 for the refetch
	transport := &showRefetchFailTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"42"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err, "should succeed with sparse data when refetch fails")

	// Output should contain the sparse recording data
	assert.Contains(t, buf.String(), "sparse title")
}

func TestShowFragmentURLResolvesComment(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showFragmentTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/todos/789#__recording_999"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	// Should fetch recording 999 (the comment), not 789 (the parent todo)
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/999.json")
}

// showFragmentTransport returns comment data for /recordings/999.
type showFragmentTransport struct {
	mu       sync.Mutex
	requests []string
}

func (t *showFragmentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.URL.Path)
	t.mu.Unlock()

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	if strings.Contains(req.URL.Path, "/recordings/999") {
		body = `{"id": 999, "type": "Comment", "content": "This is the comment"}`
	} else if strings.Contains(req.URL.Path, "/comments/999") {
		body = `{"id": 999, "type": "Comment", "content": "Rich comment content"}`
	} else {
		body = `{"id": 789, "type": "Todo", "title": "Parent todo"}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func (t *showFragmentTransport) getRequests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.requests))
	copy(out, t.requests)
	return out
}

func TestShowChatAtURLResolvesLine(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showRefetchTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	// The @-form chat line URL from the QA issue
	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/chats/789@111"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	// The @-form should normalize to lines type and use /recordings/{lineID}
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/111.json")
}

func TestShowProjectURLReturnsHelpfulError(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/projects/456"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "projects show")
}

func TestShowCircleURLReturnsHelpfulError(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/circles/789@456"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be shown")
}

func TestShowInboxForwardURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/inbox_forwards/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/forwards/789.json")
}

func TestShowScheduleEntryOccurrenceURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/schedule_entries/789/occurrences/20251229"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/schedule_entries/789.json")
}

func TestShowQuestionURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/questions/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/questions/789.json")
}

func TestShowQuestionAnswerURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/question_answers/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/question_answers/789.json")
}

func TestShowCardTablesURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/card_tables/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/card_tables/789.json")
}

func TestShowPeopleURL(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/people/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/people/789.json")
}

func TestShowContainerURLUsesGenericRecording(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	// Todoset is a container type — no shortcut endpoint, uses generic recording
	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/todosets/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/789.json")
}

func TestShowColumnURLUsesGenericRecording(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &showTypeTrackingTransport{}
	app := showTestApp(t, transport)

	cmd := NewShowCmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99999/buckets/456/card_tables/columns/789"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.GreaterOrEqual(t, len(reqs), 1)
	assert.Contains(t, reqs[0], "/recordings/789.json")
}

// showTestApp creates a test App with the given transport for show command tests.
func showTestApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
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

// showTypeTrackingTransport returns generic success responses and tracks request paths.
type showTypeTrackingTransport struct {
	mu       sync.Mutex
	requests []string
}

func (t *showTypeTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.URL.Path)
	t.mu.Unlock()

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	body := `{"id": 789, "type": "Item", "title": "Test item"}`

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func (t *showTypeTrackingTransport) getRequests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.requests))
	copy(out, t.requests)
	return out
}

// showRefetchFailTransport returns sparse recording data, then fails on refetch.
type showRefetchFailTransport struct{}

func (showRefetchFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if strings.Contains(req.URL.Path, "/recordings/") {
		body := `{"id": 42, "type": "Todo", "title": "sparse title"}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Refetch fails with 500
	return &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader(`{"error":"internal"}`)),
		Header:     header,
	}, nil
}
