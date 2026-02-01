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

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
)

// campfireNoNetworkTransport is an http.RoundTripper that fails immediately.
type campfireNoNetworkTransport struct{}

func (campfireNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// campfireTestTokenProvider is a mock token provider for tests.
type campfireTestTokenProvider struct{}

func (t *campfireTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// mockCampfirePostTransport handles resolver API calls and captures the post request.
type mockCampfirePostTransport struct {
	capturedBody []byte
}

func (t *mockCampfirePostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789}]}`
		} else if strings.Contains(req.URL.Path, "/chats/") {
			// Campfire lookup
			body = `{"id": 456, "title": "Campfire"}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Capture POST request body (the create line call)
	if req.Method == "POST" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		// Return a mock campfire line response
		mockResp := `{"id": 999, "content": "Test", "status": "active"}`
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

// TestCampfirePostContentIsPlainText verifies that campfire message content is sent
// as plain text, not wrapped in HTML tags. The Basecamp API expects plain text for
// campfire lines.
func TestCampfirePostContentIsPlainText(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

	transport := &mockCampfirePostTransport{}
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

	// Execute: bcq campfire post --content "Hello team!" --in 123
	err := executeCampfireCommand(cmd, app, "post", "--content", plainTextContent, "--in", "123")
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
}
