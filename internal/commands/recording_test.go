package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// Shared recording-transport test harness for request-level assertions:
// commands run against a stub route table, every request is recorded, and
// unmatched requests fail the command rather than passing silently.

// recordedCall captures one request seen by the recording transport.
type recordedCall struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// stubRoute matches a request by method + exact URL path and serves a canned
// response.
type stubRoute struct {
	method string
	path   string
	status int
	body   string
}

// recordingTransport is an http.RoundTripper that records every request and
// serves canned responses from its route table. Unmatched requests get an
// error response — a wrong route fails the command rather than passing
// silently.
type recordingTransport struct {
	mu       sync.Mutex
	requests []recordedCall
	routes   []stubRoute
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		defer req.Body.Close()
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(b)
	}

	t.mu.Lock()
	t.requests = append(t.requests, recordedCall{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  req.URL.RawQuery,
		Body:   body,
	})
	t.mu.Unlock()

	for _, route := range t.routes {
		if route.method == req.Method && route.path == req.URL.Path {
			return &http.Response{
				StatusCode: route.status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(route.body)),
				Request:    req,
			}, nil
		}
	}

	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"error":"no stub route for %s %s"}`, req.Method, req.URL.Path))),
		Request: req,
	}, nil
}

// recorded returns a snapshot of the requests seen so far.
func (t *recordingTransport) recorded() []recordedCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]recordedCall(nil), t.requests...)
}

// last returns the most recent request, failing the test if none were made.
func (t *recordingTransport) last(tb testing.TB) recordedCall {
	tb.Helper()
	reqs := t.recorded()
	require.NotEmpty(tb, reqs, "expected at least one request")
	return reqs[len(reqs)-1]
}

// projectsRoute serves the project list that name resolution fetches first.
func projectsRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/projects.json",
		status: http.StatusOK,
		body:   `[{"id":123,"name":"Test Project"}]`,
	}
}

// recordingTestTokenProvider is a mock token provider for recording tests.
type recordingTestTokenProvider struct{}

func (recordingTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// setupRecordingTestApp creates a test app whose SDK client talks to a
// recording stub transport seeded with the given routes.
func setupRecordingTestApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport) {
	t.Helper()

	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &recordingTransport{routes: routes}
	cfg := &config.Config{
		AccountID: "99999",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, recordingTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
	return app, transport
}

// executeRecordingCommand executes a cobra command against the test app.
func executeRecordingCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}
