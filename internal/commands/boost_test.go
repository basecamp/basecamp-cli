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

// boostTestTokenProvider is a mock token provider for tests.
type boostTestTokenProvider struct{}

func (t *boostTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// mockBoostTransport handles resolver calls and captures mutating requests.
type mockBoostTransport struct {
	capturedMethod string
	capturedPath   string
	capturedBody   []byte
}

func (t *mockBoostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.Contains(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/"):
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789}]}`
		case strings.Contains(req.URL.Path, "/boosts") && !strings.Contains(req.URL.Path, "/boosts/"):
			body = `[{"id": 1, "content": "🎉", "created_at": "2024-01-01T00:00:00Z", "booster": {"id": 10, "name": "Alice"}}]`
		case strings.Contains(req.URL.Path, "/boosts/"):
			body = `{"id": 1, "content": "👍", "created_at": "2024-01-01T00:00:00Z", "booster": {"id": 10, "name": "Alice"}}`
		default:
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	t.capturedMethod = req.Method
	t.capturedPath = req.URL.Path

	if req.Method == "POST" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 2, "content": "🎉", "created_at": "2024-01-01T00:00:00Z"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	if req.Method == "DELETE" {
		return &http.Response{
			StatusCode: 204,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func newBoostTestApp(transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &boostTestTokenProvider{},
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

func executeBoostCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// TestBoostCreateSendsContent verifies that boost create sends the emoji content
// in the request body via the SDK.
func TestBoostCreateSendsContent(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	err := executeBoostCommand(cmd, app, "create", "456", "🎉")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err)

	assert.Equal(t, "🎉", requestBody["content"],
		"boost content should be the emoji passed as argument")
	assert.Equal(t, "POST", transport.capturedMethod)
}

// TestBoostDeleteCallsDelete verifies that boost delete issues a DELETE request.
func TestBoostDeleteCallsDelete(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	err := executeBoostCommand(cmd, app, "delete", "789")
	require.NoError(t, err)

	assert.Equal(t, "DELETE", transport.capturedMethod)
	assert.Contains(t, transport.capturedPath, "/boosts/")
}

// TestBoostShowNilBoosterSummary verifies that the summary handles a nil booster
// without producing a trailing "by ".
func TestBoostShowNilBoosterSummary(t *testing.T) {
	t.Setenv("BCQ_NO_KEYRING", "1")

	// Custom transport that returns a boost with no booster
	transport := &mockBoostNilBoosterTransport{}
	app, buf := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	err := executeBoostCommand(cmd, app, "show", "1")
	require.NoError(t, err)

	// Parse the JSON output to check the summary
	var envelope map[string]any
	err = json.Unmarshal(buf.Bytes(), &envelope)
	require.NoError(t, err)

	summary, _ := envelope["summary"].(string)
	assert.NotContains(t, summary, "by ", "summary should not contain trailing 'by ' when booster is nil")
}

// mockBoostNilBoosterTransport returns a boost with no booster field.
type mockBoostNilBoosterTransport struct{}

func (t *mockBoostNilBoosterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.Contains(req.URL.Path, "/projects/"):
		body = `{"id": 123, "dock": [{"name": "chat", "id": 789}]}`
	case strings.Contains(req.URL.Path, "/boosts/"):
		body = `{"id": 1, "content": "👍", "created_at": "2024-01-01T00:00:00Z"}`
	default:
		body = `{}`
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}
