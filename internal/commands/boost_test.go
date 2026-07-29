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

// --- account-wide boost listing ---

// accountWideBoostsRoute serves the /boosts.json aggregate feed.
func accountWideBoostsRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/boosts.json",
		status: http.StatusOK,
		body: `[{"id":1,"content":"🎉","created_at":"2024-01-01T00:00:00Z",
			"booster":{"id":10,"name":"Alice"},
			"recording":{"id":456,"title":"Ship it","type":"Todo",
				"bucket":{"id":123,"name":"Test Project","type":"Project"}}}]`,
	}
}

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

	app, transport := setupRecordingTestApp(t, projectsRoute(), accountWideBoostsRoute(), recordingBoostsRoute())
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

// TestBoostListWithIDStaysItemScoped verifies that passing an ID still lists
// that item's boosts, unchanged by the account-wide path.
func TestBoostListWithIDStaysItemScoped(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)

	require.NoError(t, executeBoostCommand(cmd, app, "list", "456", "--project", "123"))
	assert.Equal(t, "/99999/recordings/456/boosts.json", transport.last(t).Path)
}

// TestBoostListWithoutIDListsAccountWide verifies that a bare list with no
// project anywhere reaches the account-wide feed instead of prompting.
func TestBoostListWithoutIDListsAccountWide(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)

	require.NoError(t, executeBoostCommand(cmd, app, "list"))

	last := transport.last(t)
	assert.Equal(t, "/99999/boosts.json", last.Path)
	assert.Equal(t, "page=1", last.Query, "boost list has no paging flags, so it stays on the first page")
}

// TestBoostListIgnoresConfiguredProject verifies that a configured project —
// which cannot scope a per-item listing — is ignored rather than errored on.
func TestBoostListIgnoresConfiguredProject(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)
	app.Config.ProjectID = "123"

	require.NoError(t, executeBoostCommand(cmd, app, "list"))
	assert.Equal(t, "/99999/boosts.json", transport.last(t).Path)
}

// TestBoostListAllProjectsOverridesConfiguredProject verifies that
// --all-projects pins account-wide intent over ambient config.
func TestBoostListAllProjectsOverridesConfiguredProject(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)
	app.Config.ProjectID = "123"

	require.NoError(t, executeBoostCommand(cmd, app, "list", "--all-projects"))
	assert.Equal(t, "/99999/boosts.json", transport.last(t).Path)
}

// TestBoostListExplicitProjectWithoutIDAsksForID verifies that an explicit
// project without an ID is a usage error — through --project, its --in alias,
// and the root-level form that lands in app.Flags.Project.
func TestBoostListExplicitProjectWithoutIDAsksForID(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)
	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "--project", "123"),
		"--project alone cannot list them")

	cmd, app, _ = setupBoostListTest(t, nil)
	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "--in", "123"),
		"--project alone cannot list them")

	cmd, app, _ = setupBoostListTest(t, nil)
	app.Flags.Project = "123"
	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list"),
		"--project alone cannot list them")

	assert.Empty(t, transport.recorded(), "a usage error must not reach the API")
}

// TestBoostListExplicitProjectWithAllProjectsConflicts verifies that
// --all-projects conflicts with an explicitly named project.
func TestBoostListExplicitProjectWithAllProjectsConflicts(t *testing.T) {
	cmd, app, _ := setupBoostListTest(t, nil)
	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "--project", "123", "--all-projects"),
		"Cannot combine --all-projects with --project")

	cmd, app, _ = setupBoostListTest(t, nil)
	app.Flags.Project = "123"
	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "--all-projects"),
		"Cannot combine --all-projects with --project")
}

// TestBoostListIDWithAllProjectsConflicts verifies that an item ID and
// --all-projects name two different listings.
func TestBoostListIDWithAllProjectsConflicts(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)

	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "456", "--all-projects"),
		"Cannot combine --all-projects with an item ID")
	assert.Empty(t, transport.recorded())
}

// TestBoostListAccountWideRejectsEvent verifies that --event, which names an
// event inside one item, is rejected rather than silently ignored.
func TestBoostListAccountWideRejectsEvent(t *testing.T) {
	cmd, app, transport := setupBoostListTest(t, nil)

	requireBoostUsageError(t, executeBoostCommand(cmd, app, "list", "--event", "5"), "--event")
	assert.Empty(t, transport.recorded())
}

// TestBoostListHasNoPaginationFlags verifies that the account-wide path did not
// grow a parallel pagination surface.
func TestBoostListHasNoPaginationFlags(t *testing.T) {
	list, _, err := NewBoostsCmd().Find([]string{"list"})
	require.NoError(t, err)

	for _, name := range []string{"limit", "page", "all"} {
		assert.Nil(t, list.Flags().Lookup(name), "boost list must not gain --%s", name)
	}
}

// TestBoostListAccountWideMachineOutputKeepsPayload verifies that machine
// formats get the raw feed, nesting intact.
func TestBoostListAccountWideMachineOutputKeepsPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd, app, _ := setupBoostListTest(t, buf)

	require.NoError(t, executeBoostCommand(cmd, app, "list"))

	var envelope struct {
		Data []struct {
			ID        int64 `json:"id"`
			Recording *struct {
				Title string `json:"title"`
			} `json:"recording"`
		} `json:"data"`
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	require.NotNil(t, envelope.Data[0].Recording)
	assert.Equal(t, "Ship it", envelope.Data[0].Recording.Title)
	assert.Equal(t, "1 boosts across all projects", envelope.Summary)
}

// TestBoostListAccountWideStyledOutputFlattens verifies that styled output gets
// flat rows rather than a nested recording cell.
func TestBoostListAccountWideStyledOutputFlattens(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd, app, _ := setupBoostListTest(t, nil)
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: buf})

	require.NoError(t, executeBoostCommand(cmd, app, "list"))

	rendered := buf.String()
	assert.Contains(t, rendered, "Test Project")
	assert.Contains(t, rendered, "Alice")
	assert.Contains(t, rendered, "Ship it")
	assert.NotContains(t, rendered, "Recording", "the nested recording must be flattened away, not rendered as a cell")
}

// TestFlattenAccountWideBoostsNilPointers verifies that a boost missing its
// booster and recording still yields a row with every column.
func TestFlattenAccountWideBoostsNilPointers(t *testing.T) {
	rows := flattenAccountWideBoosts([]basecamp.EverythingBoost{{ID: 7, Content: "👍"}})

	require.Len(t, rows, 1)
	assert.Equal(t, int64(7), rows[0]["id"])
	assert.Equal(t, "👍", rows[0]["content"])
	assert.Equal(t, "", rows[0]["booster"])
	assert.Equal(t, "", rows[0]["project"])
	assert.Equal(t, "", rows[0]["title"])
	assert.Equal(t, "", rows[0]["type"])
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
