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

// campfireTestTokenProvider is a mock token provider for tests.
type campfireTestTokenProvider struct{}

func (t *campfireTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// mockCampfireCreateTransport handles resolver API calls and captures the create request.
type mockCampfireCreateTransport struct {
	capturedBody []byte
}

func (t *mockCampfireCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	// Handle resolver calls with mock responses
	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			// Projects list - return array
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			// Single project lookup - return project with chat (campfire) in dock
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

// executeCampfireCommand executes a cobra command with the given args.
func executeCampfireCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

// TestCampfirePostContentIsPlainText verifies that campfire line content is sent as plain text,
// not wrapped in HTML tags. The Basecamp API forces campfire lines to text-only and
// HTML-escapes the content, so sending HTML would display literal tags.
func TestCampfirePostContentIsPlainText(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockCampfireCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &campfireTestTokenProvider{},
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

	cmd := NewCampfireCmd()
	plainTextContent := "Hello team!"

	err := executeCampfireCommand(cmd, app, "post", plainTextContent)
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	content, ok := requestBody["content"].(string)
	require.True(t, ok, "expected 'content' field in request body")

	// The content should be exactly what was passed in - plain text, no HTML wrapping
	assert.Equal(t, plainTextContent, content,
		"Campfire content should be plain text, not HTML-wrapped")

	// Explicitly verify no HTML tags were added
	assert.NotContains(t, content, "<p>",
		"Campfire content should not contain <p> tags")
	assert.NotContains(t, content, "</p>",
		"Campfire content should not contain </p> tags")
}

// TestCampfirePostContentTypeSentInPayload verifies that --content-type is passed through
// to the API request body as content_type.
func TestCampfirePostContentTypeSentInPayload(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockCampfireCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &campfireTestTokenProvider{},
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

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "post", "<b>Hello</b>", "--content-type", "text/html")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent when --content-type is specified")
}

// TestCampfirePostDefaultOmitsContentType verifies that content_type is not sent
// when --content-type is not specified.
func TestCampfirePostDefaultOmitsContentType(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockCampfireCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &campfireTestTokenProvider{},
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

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "post", "Hello team!")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	_, hasContentType := requestBody["content_type"]
	assert.False(t, hasContentType,
		"content_type should not be sent when --content-type is not specified")
}

// mockMultiCampfireTransport returns a project with multiple chat dock entries
// and serves individual campfire GET requests.
type mockMultiCampfireTransport struct{}

func (t *mockMultiCampfireTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

	sdkClient := basecamp.NewClient(&basecamp.Config{}, &campfireTestTokenProvider{},
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

// TestCampfireListMultipleCampfires verifies that `campfire list` succeeds on
// projects with multiple campfires (no ambiguous error).
func TestCampfireListMultipleCampfires(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockMultiCampfireTransport{})

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "list")
	require.NoError(t, err)

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2)

	titles := []string{envelope.Data[0]["title"].(string), envelope.Data[1]["title"].(string)}
	assert.Contains(t, titles, "General")
	assert.Contains(t, titles, "Engineering")
}

// TestCampfireListWithCampfireFlag verifies that `campfire list -c <id>` returns
// only the specified campfire.
func TestCampfireListWithCampfireFlag(t *testing.T) {
	app, buf := newTestAppWithTransport(t, &mockMultiCampfireTransport{})

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "list", "--campfire", "1002")
	require.NoError(t, err)

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	assert.Equal(t, "Engineering", envelope.Data[0]["title"])
}

// mockCampfireDockTransport returns a project whose dock payload is configurable.
type mockCampfireDockTransport struct {
	dockJSON string // JSON array for the dock field
}

func (t *mockCampfireDockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

// TestCampfireListNoCampfires verifies the not-found error when a project has
// no chat dock entries at all.
func TestCampfireListNoCampfires(t *testing.T) {
	transport := &mockCampfireDockTransport{
		dockJSON: `[{"name": "todoset", "id": 500, "enabled": true}]`,
	}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "no campfire")
}

// TestCampfireListDisabledCampfire verifies the not-found error hints that
// campfire is disabled when only disabled chat entries exist.
func TestCampfireListDisabledCampfire(t *testing.T) {
	transport := &mockCampfireDockTransport{
		dockJSON: `[{"name": "chat", "id": 900, "title": "Campfire", "enabled": false}]`,
	}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "disabled")
}

// TestCampfirePostViaSubcommandWithCampfireFlag verifies the proper way to post
// to a specific campfire: `basecamp campfire post <msg> --campfire <id>`.
func TestCampfirePostViaSubcommandWithCampfireFlag(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockCampfireCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &campfireTestTokenProvider{},
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

	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "post", "<b>Hello</b>", "--campfire", "789", "--content-type", "text/html")
	require.NoError(t, err, "post via subcommand with --campfire flag should succeed")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent via subcommand path")
	assert.Equal(t, "<b>Hello</b>", requestBody["content"],
		"content should be passed through subcommand path")
}
