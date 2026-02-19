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
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789}]}`
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
	t.Setenv("BCQ_NO_KEYRING", "1")

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

	var requestBody map[string]interface{}
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
	t.Setenv("BCQ_NO_KEYRING", "1")

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

	var requestBody map[string]interface{}
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent when --content-type is specified")
}

// TestCampfirePostDefaultOmitsContentType verifies that content_type is not sent
// when --content-type is not specified.
func TestCampfirePostDefaultOmitsContentType(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

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

	var requestBody map[string]interface{}
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	_, hasContentType := requestBody["content_type"]
	assert.False(t, hasContentType,
		"content_type should not be sent when --content-type is not specified")
}

// TestCampfireNumericIDPostContentType exercises the numeric-ID dispatch path:
// `bcq campfire <id> post <msg> --content-type text/html` which goes through the
// parent command's RunE rather than the post subcommand.
func TestCampfireNumericIDPostContentType(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

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

	// This hits the isNumeric(args[0]) branch in NewCampfireCmd().RunE
	cmd := NewCampfireCmd()
	err := executeCampfireCommand(cmd, app, "789", "post", "<b>Hello</b>", "--content-type", "text/html")
	require.NoError(t, err, "numeric-ID dispatch should succeed")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]interface{}
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	assert.Equal(t, "text/html", requestBody["content_type"],
		"content_type should be sent via numeric-ID dispatch path")
	assert.Equal(t, "<b>Hello</b>", requestBody["content"],
		"content should be passed through numeric-ID dispatch path")
}
