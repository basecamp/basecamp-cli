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
			body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
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

	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

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

// TestBoostCreateRejectsLongContent verifies that boost create rejects content over 16 characters.
func TestBoostCreateRejectsLongContent(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	err := executeBoostCommand(cmd, app, "create", "456", "this is way long!")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Boost content too long")
}

// TestBoostCreateAcceptsMaxContent verifies that 16-character content passes validation.
func TestBoostCreateAcceptsMaxContent(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	err := executeBoostCommand(cmd, app, "create", "456", "exactly16chars!!")
	require.NoError(t, err)
	assert.Equal(t, "POST", transport.capturedMethod)
}

// --- item-scoped boost listing ---

// recordingBoostsRoute serves the item-scoped boost listing.
func recordingBoostsRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/recordings/456/boosts.json",
		status: http.StatusOK,
		body:   `[{"id":1,"content":"🎉","created_at":"2024-01-01T00:00:00Z"}]`,
	}
}

// setupBoostListTest builds a command and app wired to the boost routes.
func setupBoostListTest(t *testing.T, buf *bytes.Buffer) (*cobra.Command, *appctx.App, *recordingTransport) {
	t.Helper()

	app, transport := setupRecordingTestApp(t, projectsRoute(), recordingBoostsRoute())
	if buf != nil {
		app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	}
	return NewBoostsCmd(), app, transport
}

// requireBoostUsageError asserts that err is a usage error mentioning want.
func requireBoostUsageError(t *testing.T, err error, want string) {
	t.Helper()

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, want)
}

// TestBoostListWithIDStaysItemScoped verifies that passing an ID lists that
// item's boosts.
func TestBoostListWithIDStaysItemScoped(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)

	require.NoError(t, executeBoostCommand(cmd, app, "list", "456", "--project", "123"))
	assert.Equal(t, "/99999/recordings/456/boosts.json", transport.last(t).Path)
}

// Boosts hang off a single item, so an ID is required. The account-wide feed
// that used to answer a bare `boost list` was an unlinked easter egg on the
// web side and has been withdrawn, so there is nothing to fall back to.
//
// A machine-output invocation gets a structured usage error; an interactive one
// gets help. Either way the point is that nothing is fetched — silently listing
// something else is the failure mode worth pinning.
func TestBoostListWithoutIDAsksForAnID(t *testing.T) {
	// missingArg shows help interactively and errors otherwise; pin the
	// non-interactive branch so the assertion is about the contract, not
	// about whether the test process happens to look like a terminal.
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	buf := &bytes.Buffer{}
	cmd, app, transport := setupBoostListTest(t, buf)

	err := executeBoostCommand(cmd, app, "list")

	requireBoostUsageError(t, err, "<id|url> required")
	assert.Empty(t, transport.recorded(), "a missing argument must not reach the API")
}

// A configured project cannot scope a per-item listing, so it must not be
// silently promoted into one.
func TestBoostListConfiguredProjectStillNeedsAnID(t *testing.T) {
	// missingArg shows help interactively and errors otherwise; pin the
	// non-interactive branch so the assertion is about the contract, not
	// about whether the test process happens to look like a terminal.
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	buf := &bytes.Buffer{}
	cmd, app, transport := setupBoostListTest(t, buf)
	app.Config.ProjectID = "123"

	err := executeBoostCommand(cmd, app, "list")

	requireBoostUsageError(t, err, "<id|url> required")
	assert.Empty(t, transport.recorded())
}

// The pagination flags existed only for the account-wide feed. With that gone
// they must not linger: an item's boosts arrive in one unpaginated response,
// and the SDK documents BoostListOptions.Page as not honoring a page number.
func TestBoostListHasNoPaginationFlags(t *testing.T) {
	list, _, err := NewBoostsCmd().Find([]string{"list"})
	require.NoError(t, err)

	for _, name := range []string{"limit", "page", "all", "all-projects"} {
		assert.Nil(t, list.Flags().Lookup(name), "boost list must not carry --%s", name)
	}
}

// --event names an event inside the item, so it still needs that item's ID.
func TestBoostListEventWithoutIDAsksForAnID(t *testing.T) {
	// missingArg shows help interactively and errors otherwise; pin the
	// non-interactive branch so the assertion is about the contract, not
	// about whether the test process happens to look like a terminal.
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	buf := &bytes.Buffer{}
	cmd, app, transport := setupBoostListTest(t, buf)

	err := executeBoostCommand(cmd, app, "list", "--event", "999")

	requireBoostUsageError(t, err, "<id|url> required")
	assert.Empty(t, transport.recorded())
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
		body = `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`
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
