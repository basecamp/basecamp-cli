package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

// chatTestTokenProvider is a mock token provider for tests.
type chatTestTokenProvider struct{}

func (t *chatTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// mockChatCreateTransport handles resolver API calls and captures the create request.
type mockChatCreateTransport struct {
	capturedBody []byte
}

func (t *mockChatCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	// Handle resolver calls with mock responses
	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			// Projects list - return array
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			// Single project lookup - return project with chat in dock
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
		} else if strings.Contains(req.URL.Path, "/chats/") && strings.Contains(req.URL.Path, "/lines.json") {
			// List lines
			body = `[]`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Capture POST request body (the create call)
	if req.Method == "POST" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		// Return a mock line response
		mockResp := `{"id": 999, "content": "Test", "created_at": "2024-01-01T00:00:00Z"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

// mockChatDeleteTransport handles resolver API calls and responds to DELETE requests.
type mockChatDeleteTransport struct {
	capturedMethod string
	capturedPath   string
}

func (t *mockChatDeleteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "DELETE" {
		t.capturedMethod = req.Method
		t.capturedPath = req.URL.Path
		return &http.Response{
			StatusCode: 204,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func newChatDeleteTestApp(transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

// executeChatCommand executes a cobra command with the given args.
func executeChatCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

func TestChatAliases(t *testing.T) {
	cmd := NewChatCmd()
	assert.Equal(t, "chat", cmd.Name())
	assert.Contains(t, cmd.Aliases, "campfire")
}

// TestChatPostContentIsPlainText verifies that chat line content is sent as plain text,
// not wrapped in HTML tags. The Basecamp API forces chat lines to text-only and
// HTML-escapes the content, so sending HTML would display literal tags.
func TestChatPostContentIsPlainText(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

	cmd := NewChatCmd()
	plainTextContent := "Hello team!"

	err := executeChatCommand(cmd, app, "post", plainTextContent)
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	content, ok := requestBody["content"].(string)
	require.True(t, ok, "expected 'content' field in request body")

	// The content should be exactly what was passed in - plain text, no HTML wrapping
	assert.Equal(t, plainTextContent, content,
		"Chat content should be plain text, not HTML-wrapped")

	// Explicitly verify no HTML tags were added
	assert.NotContains(t, content, "<p>",
		"Chat content should not contain <p> tags")
	assert.NotContains(t, content, "</p>",
		"Chat content should not contain </p> tags")
}

// TestChatPostContentTypeSentInPayload verifies that --content-type is passed through
// to the API request body as content_type.
func TestChatPostContentTypeSentInPayload(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "post", "<b>Hello</b>", "--content-type", "text/html")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent when --content-type is specified")
}

// TestChatPostDefaultOmitsContentType verifies that content_type is not sent
// when --content-type is not specified.
func TestChatPostDefaultOmitsContentType(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "post", "Hello team!")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	_, hasContentType := requestBody["content_type"]
	assert.False(t, hasContentType,
		"content_type should not be sent when --content-type is not specified")
}

// mockMultiChatTransport returns a project with multiple chat dock entries
// and serves individual chat GET requests.
type mockMultiChatTransport struct{}

func (t *mockMultiChatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method != "GET" {
		return &http.Response{
			StatusCode: 405,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     header,
		}, nil
	}

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.Contains(req.URL.Path, "/projects/123"):
		body = `{"id": 123, "dock": [` +
			`{"name": "chat", "id": 1001, "title": "General", "enabled": true},` +
			`{"name": "chat", "id": 1002, "title": "Engineering", "enabled": true}` +
			`]}`
	case strings.HasSuffix(req.URL.Path, "/chats/1001"):
		body = `{"id": 1001, "title": "General", "type": "Chat::Transcript", "status": "active",` +
			`"visible_to_clients": false, "inherits_status": true,` +
			`"url": "https://example.com", "app_url": "https://example.com",` +
			`"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z",` +
			`"bucket": {"id": 123, "name": "Test"}, "creator": {"id": 1, "name": "Test"}}`
	case strings.HasSuffix(req.URL.Path, "/chats/1002"):
		body = `{"id": 1002, "title": "Engineering", "type": "Chat::Transcript", "status": "active",` +
			`"visible_to_clients": false, "inherits_status": true,` +
			`"url": "https://example.com", "app_url": "https://example.com",` +
			`"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z",` +
			`"bucket": {"id": 123, "name": "Test"}, "creator": {"id": 1, "name": "Test"}}`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func newTestAppWithTransport(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkClient := basecamp.NewClient(&basecamp.Config{}, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

// TestChatListMultipleChats verifies that `chat list` succeeds on
// projects with multiple chats (no ambiguous error).
func TestChatListMultipleChats(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockMultiChatTransport{})

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list")
	require.NoError(t, err)

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2)

	titles := []string{envelope.Data[0]["title"].(string), envelope.Data[1]["title"].(string)}
	assert.Contains(t, titles, "General")
	assert.Contains(t, titles, "Engineering")

	// Summary should use "chats" not "campfires"
	assert.Contains(t, buf.String(), "2 chats")
}

// TestChatListWithChatFlag verifies that `chat list -c <id>` returns
// only the specified chat.
func TestChatListWithChatFlag(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockMultiChatTransport{})

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list", "--chat", "1002")
	require.NoError(t, err)

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	assert.Equal(t, "Engineering", envelope.Data[0]["title"])

	// Summary should use "Chat:" not "Campfire:"
	assert.Contains(t, buf.String(), "Chat: Engineering")
}

// mockChatDockTransport returns a project whose dock payload is configurable.
type mockChatDockTransport struct {
	dockJSON string // JSON array for the dock field
}

func (t *mockChatDockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.Contains(req.URL.Path, "/projects/123"):
		body = `{"id": 123, "dock": ` + t.dockJSON + `}`
	default:
		body = `{}`
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

// TestChatListNoChats verifies the not-found error when a project has
// no chat dock entries at all.
func TestChatListNoChats(t *testing.T) {
	transport := &mockChatDockTransport{
		dockJSON: `[{"name": "todoset", "id": 500, "enabled": true}]`,
	}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "no chat")
}

// TestChatListDisabledChat verifies the not-found error hints that
// chat is disabled when only disabled chat entries exist.
func TestChatListDisabledChat(t *testing.T) {
	transport := &mockChatDockTransport{
		dockJSON: `[{"name": "chat", "id": 900, "title": "Chat", "enabled": false}]`,
	}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "disabled")
}

// TestChatListMultipleChatsBreadcrumbs verifies breadcrumbs use
// --chat flag syntax with placeholder for multi-chat projects.
func TestChatListMultipleChatsBreadcrumbs(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockMultiChatTransport{})
	app.Flags.Hints = true

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list")
	require.NoError(t, err)

	var envelope struct {
		Summary     string `json:"summary"`
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	assert.Contains(t, envelope.Summary, "2 chats")

	require.NotEmpty(t, envelope.Breadcrumbs)
	for _, bc := range envelope.Breadcrumbs {
		assert.Contains(t, bc.Cmd, "--chat")
	}
}

// TestChatListSingleChatSummary verifies title-based summary and
// concrete chat ID in breadcrumbs for single-chat projects.
func TestChatListSingleChatSummary(t *testing.T) {
	transport := &mockSingleChatTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list")
	require.NoError(t, err)

	var envelope struct {
		Data        []map[string]any `json:"data"`
		Summary     string           `json:"summary"`
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	require.Len(t, envelope.Data, 1)
	assert.Contains(t, envelope.Summary, "Team Chat")

	require.NotEmpty(t, envelope.Breadcrumbs)
	for _, bc := range envelope.Breadcrumbs {
		assert.Contains(t, bc.Cmd, "--chat 501")
	}
}

// mockSingleChatTransport returns a project with one chat dock entry.
type mockSingleChatTransport struct{}

func (t *mockSingleChatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.Contains(req.URL.Path, "/projects/123"):
		body = `{"id": 123, "dock": [{"name": "chat", "id": 501, "title": "Team Chat", "enabled": true}]}`
	case strings.HasSuffix(req.URL.Path, "/chats/501"):
		body = `{"id": 501, "title": "Team Chat", "type": "Chat::Transcript", "status": "active",` +
			`"visible_to_clients": false, "inherits_status": true,` +
			`"url": "https://example.com", "app_url": "https://example.com",` +
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

// mockChatListAllTransport handles the account-wide chat list endpoint.
type mockChatListAllTransport struct{}

func (t *mockChatListAllTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if strings.HasSuffix(req.URL.Path, "/chats.json") {
		body := `[{"id": 789, "title": "General", "type": "Chat::Transcript"}]`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     header,
	}, nil
}

// TestChatListAllBreadcrumbSyntax verifies that --all breadcrumbs use
// --chat flag syntax, not the old positional syntax.
func TestChatListAllBreadcrumbSyntax(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockChatListAllTransport{})
	app.Flags.Hints = true

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "list", "--all")
	require.NoError(t, err)

	var envelope struct {
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Breadcrumbs)

	for _, bc := range envelope.Breadcrumbs {
		assert.Contains(t, bc.Cmd, "--chat")
		assert.NotContains(t, bc.Cmd, "chat <id> messages")
	}
}

// TestChatPostViaSubcommandWithChatFlag verifies the proper way to post
// to a specific chat: `basecamp chat post <msg> --chat <id>`.
func TestChatPostViaSubcommandWithChatFlag(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &chatTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
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

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "post", "<b>Hello</b>", "--chat", "789", "--content-type", "text/html")
	require.NoError(t, err, "post via subcommand with --chat flag should succeed")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent via subcommand path")
	assert.Equal(t, "<b>Hello</b>", requestBody["content"],
		"content should be passed through subcommand path")
}

// mockChatMentionTransport handles resolver API calls for mentions and captures POST body.
type mockChatMentionTransport struct {
	capturedBody []byte
}

func (t *mockChatMentionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.Contains(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/"):
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
		case strings.Contains(req.URL.Path, "/circles/people.json") || strings.Contains(req.URL.Path, "/people/pingable.json"):
			body = `[{"id": 42000, "name": "Jane Smith", "email_address": "jane@example.com", "attachable_sgid": "sgid-jane"}]`
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
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "content": "Test", "created_at": "2024-01-01T00:00:00Z"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

// TestChatPostMentionPromotesToHTML verifies that a chat post with @Name
// auto-promotes content type to text/html when mentions are resolved.
func TestChatPostMentionPromotesToHTML(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatMentionTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "post", "Hey @Jane.Smith, check this")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err)

	// Content type should be promoted to text/html when mentions are present
	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be promoted to text/html when mentions are resolved")

	content, ok := requestBody["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "bc-attachment",
		"content should contain bc-attachment mention tag")
}

// TestChatPostPlainTextOptOut verifies that --content-type text/plain
// bypasses mention resolution and sends content as-is.
func TestChatPostPlainTextOptOut(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatMentionTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "post", "Hey @Jane.Smith", "--content-type", "text/plain")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err)

	// Content type should remain text/plain
	assert.Equal(t, "text/plain", requestBody["content_type"],
		"content_type should remain text/plain when explicitly set")

	content, ok := requestBody["content"].(string)
	require.True(t, ok)
	// Mentions should NOT be resolved — raw text preserved
	assert.NotContains(t, content, "bc-attachment",
		"content should not contain bc-attachment when content-type is text/plain")
	assert.Contains(t, content, "@Jane.Smith",
		"@mention should be left as literal text")
}

// TestChatDeleteReturnsDeletedPayload verifies that delete returns {"deleted": true, "id": "..."}.
func TestChatDeleteReturnsDeletedPayload(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatDeleteTransport{}
	app, buf := newChatDeleteTestApp(transport)
	app.Flags.Agent = true // skip confirmation prompt

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "delete", "111", "--force")
	require.NoError(t, err)

	assert.Equal(t, "DELETE", transport.capturedMethod)

	var envelope map[string]any
	err = json.Unmarshal(buf.Bytes(), &envelope)
	require.NoError(t, err)

	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "expected data object in envelope")
	assert.Equal(t, true, data["deleted"])
	assert.Equal(t, "111", data["id"])
}

// TestChatDeleteSkipsPromptInAgentMode verifies that --agent mode skips the
// confirmation prompt and issues the DELETE call.
func TestChatDeleteSkipsPromptInAgentMode(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatDeleteTransport{}
	app, _ := newChatDeleteTestApp(transport)
	app.Flags.Agent = true // machine output — no prompt

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "delete", "111")
	require.NoError(t, err)

	assert.Equal(t, "DELETE", transport.capturedMethod)
	assert.Contains(t, transport.capturedPath, "/lines/")
}

// TestChatDeleteForceSkipsPrompt verifies that --force bypasses the confirmation
// prompt even when not in machine-output mode.
func TestChatDeleteForceSkipsPrompt(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockChatDeleteTransport{}
	app, _ := newChatDeleteTestApp(transport)
	// Flags.Agent is false — not in machine mode.
	// Test stdout is *bytes.Buffer (not *os.File), so isMachineOutput TTY check
	// falls through to false. Without --force this would attempt tui.ConfirmDangerous.

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "delete", "111", "--force")
	require.NoError(t, err)

	assert.Equal(t, "DELETE", transport.capturedMethod)
	assert.Contains(t, transport.capturedPath, "/lines/")
}

// =============================================================================
// Display content helper tests
// =============================================================================

func TestChatLineDisplayContent_HTMLWithAttachments(t *testing.T) {
	line := &basecamp.CampfireLine{
		Content: `<p>Check this out</p><bc-attachment filename="report.pdf">report.pdf</bc-attachment>`,
		Attachments: []basecamp.CampfireLineAttachment{
			{Filename: "report.pdf", ByteSize: 9_100_000},
		},
	}
	got := chatLineDisplayContent(line)
	assert.Contains(t, got, "Check this out")
	assert.Contains(t, got, "📎 report.pdf (9.1mb)")
}

func TestChatLineDisplayContent_PlainTextWithAttachments(t *testing.T) {
	line := &basecamp.CampfireLine{
		Content: "Here's the file",
		Attachments: []basecamp.CampfireLineAttachment{
			{Filename: "notes.txt", ByteSize: 512},
		},
	}
	got := chatLineDisplayContent(line)
	assert.Contains(t, got, "Here's the file")
	assert.Contains(t, got, "📎 notes.txt (512b)")
}

func TestChatLineDisplayContent_EmptyContentWithAttachments(t *testing.T) {
	line := &basecamp.CampfireLine{
		Attachments: []basecamp.CampfireLineAttachment{
			{Filename: "image.png", ByteSize: 2_500_000},
		},
	}
	got := chatLineDisplayContent(line)
	assert.Equal(t, "📎 image.png (2.5mb)", got)
}

func TestChatLineDisplayContent_EmptyContentWithTitle(t *testing.T) {
	line := &basecamp.CampfireLine{
		Title: "A sound clip",
	}
	got := chatLineDisplayContent(line)
	assert.Equal(t, "A sound clip", got)
}

func TestChatLineDisplayContent_PlainTextOnly(t *testing.T) {
	line := &basecamp.CampfireLine{
		Content: "Just a message",
	}
	got := chatLineDisplayContent(line)
	assert.Equal(t, "Just a message", got)
}

func TestInjectAttachmentSizes_MidLineNotRewritten(t *testing.T) {
	// User-authored text containing 📎 filename mid-line should not be rewritten
	text := "I renamed the file to 📎 report.pdf yesterday"
	attachments := []basecamp.CampfireLineAttachment{
		{Filename: "report.pdf", ByteSize: 1_000},
	}
	got := injectAttachmentSizes(text, attachments)
	assert.Equal(t, text, got)
}

func TestFormatChatAttachments_ZeroByteSize(t *testing.T) {
	attachments := []basecamp.CampfireLineAttachment{
		{Filename: "mystery.dat", ByteSize: 0},
	}
	got := formatChatAttachments(attachments)
	assert.Equal(t, "📎 mystery.dat", got)
	assert.NotContains(t, got, "(")
}

func TestFormatChatAttachments_TitleFallback(t *testing.T) {
	attachments := []basecamp.CampfireLineAttachment{
		{Title: "My Document", ByteSize: 5_000},
	}
	got := formatChatAttachments(attachments)
	assert.Equal(t, "📎 My Document (5.0kb)", got)
}

func TestInjectAttachmentSizes_DuplicateFilenames(t *testing.T) {
	text := "📎 doc.pdf\nsome text\n📎 doc.pdf"
	attachments := []basecamp.CampfireLineAttachment{
		{Filename: "doc.pdf", ByteSize: 1_000},
		{Filename: "doc.pdf", ByteSize: 2_000},
	}
	got := injectAttachmentSizes(text, attachments)
	lines := strings.Split(got, "\n")
	assert.Equal(t, "📎 doc.pdf (1.0kb)", lines[0])
	assert.Equal(t, "some text", lines[1])
	assert.Equal(t, "📎 doc.pdf (2.0kb)", lines[2])
}

// =============================================================================
// Upload command-level test
// =============================================================================

// mockChatUploadTransport handles the multipart upload and returns a line with attachments.
type mockChatUploadTransport struct{}

func (t *mockChatUploadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.Contains(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/123"):
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
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
		// Drain the body
		if req.Body != nil {
			io.ReadAll(req.Body)
			req.Body.Close()
		}
		mockResp := `{
			"id": 555,
			"content": "",
			"created_at": "2024-01-01T00:00:00Z",
			"attachments": [{"filename": "photo.jpg", "byte_size": 3500000, "content_type": "image/jpeg"}]
		}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func TestChatUploadSummaryIncludesFileSize(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	app, buf := newTestAppWithTransport(t, &mockChatUploadTransport{})

	// Create a temp file to upload
	tmp := t.TempDir()
	filePath := tmp + "/photo.jpg"
	os.WriteFile(filePath, []byte("fake image data"), 0644)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "upload", filePath)
	require.NoError(t, err)

	var envelope struct {
		Data    map[string]any `json:"data"`
		Summary string         `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	assert.Contains(t, envelope.Summary, "photo.jpg")
	assert.Contains(t, envelope.Summary, "3.5mb")
}

func TestChatUploadStyledOutputIncludesFileSize(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &chatTestTokenProvider{},
		basecamp.WithTransport(&mockChatUploadTransport{}),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatStyled,
			Writer: buf,
		}),
	}

	tmp := t.TempDir()
	filePath := tmp + "/photo.jpg"
	os.WriteFile(filePath, []byte("fake image data"), 0644)

	cmd := NewChatCmd()
	err := executeChatCommand(cmd, app, "upload", filePath)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "📎 photo.jpg (3.5mb)")
}
