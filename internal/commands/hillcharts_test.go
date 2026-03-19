package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// hillchartsTestTokenProvider is a mock token provider for tests.
type hillchartsTestTokenProvider struct{}

func (hillchartsTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// hillchartsTransport is a configurable mock transport for hill chart tests.
// It routes requests based on URL path to return predetermined responses.
type hillchartsTransport struct {
	// hillChartStatus is the HTTP status for GET .../hill.json
	hillChartStatus int
	// hillChartBody is the response body for GET .../hill.json
	hillChartBody string
	// todosetStatus is the HTTP status for GET .../todosets/{id} (no /hill suffix)
	todosetStatus int
	// todosetBody is the response body for GET .../todosets/{id}
	todosetBody string
	// projectsBody is the response body for GET /projects.json
	projectsBody string
	// updateStatus is the HTTP status for PUT .../hills/settings.json
	updateStatus int
	// updateBody is the response body for PUT .../hills/settings.json
	updateBody string
}

func (t *hillchartsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path

	// PUT .../hills/settings.json — hill chart update
	if req.Method == "PUT" && strings.Contains(path, "/hills/settings.json") {
		status := t.updateStatus
		if status == 0 {
			status = 200
		}
		body := t.updateBody
		if body == "" {
			body = `{"enabled": true, "stale": false, "dots": []}`
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// GET .../hill.json — hill chart show
	if strings.Contains(path, "/hill.json") {
		status := t.hillChartStatus
		if status == 0 {
			status = 200
		}
		body := t.hillChartBody
		if body == "" {
			body = `{"enabled": true, "stale": false, "dots": []}`
		}
		if status >= 400 {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"error": "Forbidden", "status": %d}`, status))),
				Header:     header,
			}, nil
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// GET .../todosets/{id} (without /hill) — todoset probe
	if strings.Contains(path, "/todosets/") {
		status := t.todosetStatus
		if status == 0 {
			status = 200
		}
		body := t.todosetBody
		if body == "" {
			body = `{"id": 12345, "todolists_count": 0}`
		}
		if status >= 400 {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(`{"error": "Server Error"}`)),
				Header:     header,
			}, nil
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// GET /projects.json — project resolution
	if strings.Contains(path, "/projects.json") {
		body := t.projectsBody
		if body == "" {
			body = `[]`
		}
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

func setupHillchartsMockApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &hillchartsTestTokenProvider{},
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

func executeHillchartsCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// --- 403 heuristic tests (todoset-only, no project needed) ---

func TestHillchartsShow403EmptyTodoset(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 403,
		todosetStatus:   200,
		todosetBody:     `{"id": 12345, "todolists_count": 0}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "No todolists to track")
}

func TestHillchartsShow403NonEmptyTodoset(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 403,
		todosetStatus:   200,
		todosetBody:     `{"id": 12345, "todolists_count": 3}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.Error(t, err)

	// Should preserve the original 403 — not the usage error
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.NotEqual(t, output.CodeUsage, e.Code)
}

func TestHillchartsShow403ProbeFails(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 403,
		todosetStatus:   500,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.Error(t, err)

	// Should preserve the original 403
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.NotEqual(t, output.CodeUsage, e.Code)
}

func TestHillchartsShowSuccess(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 200,
		hillChartBody: `{
			"enabled": true,
			"stale": false,
			"dots": [{"id": 1, "label": "Alpha", "position": 50}]
		}`,
	}
	app, buf := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Alpha")
}

func TestHillchartsShow404(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 404,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.NotEqual(t, output.CodeUsage, e.Code)
}

// --- Mismatch validation tests ---

func TestHillchartsShowMismatchProject(t *testing.T) {
	transport := &hillchartsTransport{
		projectsBody: `[{"id": 123, "name": "My Project"}]`,
		todosetBody:  `{"id": 12345, "todolists_count": 5, "bucket": {"id": 456, "name": "Other Project"}}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345", "--in", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "belongs to project 456")
	assert.Contains(t, e.Message, "not 123")
}

func TestHillchartsShowMatchingProject(t *testing.T) {
	transport := &hillchartsTransport{
		projectsBody:    `[{"id": 123, "name": "My Project"}]`,
		todosetBody:     `{"id": 12345, "todolists_count": 5, "bucket": {"id": 123, "name": "My Project"}}`,
		hillChartBody:   `{"enabled": true, "stale": false, "dots": []}`,
		hillChartStatus: 200,
	}
	app, buf := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345", "--in", "123")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "enabled")
}

// --- Todoset-only invocations (no project required) ---

func TestHillchartsShowTodosetOnlyNoProjectError(t *testing.T) {
	transport := &hillchartsTransport{
		hillChartStatus: 200,
		hillChartBody:   `{"enabled": true, "stale": false, "dots": []}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "show", "--todoset", "12345")
	require.NoError(t, err)
}

func TestHillchartsTrackTodosetOnly(t *testing.T) {
	transport := &hillchartsTransport{
		updateStatus: 200,
		updateBody:   `{"enabled": true, "stale": false, "dots": [{"id": 111, "label": "List", "position": 0}]}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "track", "111", "--todoset", "12345")
	require.NoError(t, err)
}

func TestHillchartsUntrackTodosetOnly(t *testing.T) {
	transport := &hillchartsTransport{
		updateStatus: 200,
		updateBody:   `{"enabled": true, "stale": false, "dots": []}`,
	}
	app, _ := setupHillchartsMockApp(t, transport)

	cmd := NewHillchartsCmd()
	err := executeHillchartsCommand(cmd, app, "untrack", "111", "--todoset", "12345")
	require.NoError(t, err)
}

// --- Breadcrumb scope tests ---

func TestHillchartScope(t *testing.T) {
	assert.Equal(t, "--todoset 12345", hillchartScope("123", "12345"))
	assert.Equal(t, "--in 123", hillchartScope("123", ""))
}

// --- Empty todoset hint tests ---

func TestEmptyTodosetHintWithProject(t *testing.T) {
	hint := emptyTodosetHint("123", "12345", "12345")
	assert.Contains(t, hint, "--in 123")
	assert.Contains(t, hint, "--todoset 12345")
	assert.Contains(t, hint, "todolists create")
}

func TestEmptyTodosetHintWithoutProject(t *testing.T) {
	hint := emptyTodosetHint("", "12345", "12345")
	assert.Contains(t, hint, "--todoset 12345")
	assert.Contains(t, hint, "Create todolists in the project that owns this todoset")
	assert.NotContains(t, hint, "todolists create")
}

func TestEmptyTodosetHintAutoDetectedTodoset(t *testing.T) {
	hint := emptyTodosetHint("123", "12345", "")
	assert.Contains(t, hint, "--in 123")
	assert.NotContains(t, hint, "--todoset")
}
